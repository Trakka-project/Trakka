package logbuffer

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

// TestHandlerCapturesAndDelegates confirms a handled record both reaches
// the wrapped handler unchanged (stdout logging must never regress) and
// shows up in Entries(), most-recent-first.
func TestHandlerCapturesAndDelegates(t *testing.T) {
	var out bytes.Buffer
	h := NewHandler(slog.NewJSONHandler(&out, nil), 10)
	logger := slog.New(h)

	logger.Info("first", "n", 1)
	logger.Warn("second", "n", 2)

	if out.Len() == 0 {
		t.Fatal("expected the wrapped handler to still receive records and write to its own output")
	}

	entries := h.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Message != "second" || entries[1].Message != "first" {
		t.Fatalf("expected most-recent-first order, got %q then %q", entries[0].Message, entries[1].Message)
	}
	if entries[0].Level != "WARN" {
		t.Fatalf("expected level WARN, got %q", entries[0].Level)
	}
	if entries[0].Attrs["n"] != int64(2) {
		t.Fatalf("expected attr n=2, got %v", entries[0].Attrs["n"])
	}
}

// TestHandlerRingWraps confirms the buffer never grows past its capacity
// and correctly drops the oldest entries first once it's full.
func TestHandlerRingWraps(t *testing.T) {
	h := NewHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), 3)
	logger := slog.New(h)

	for i := 0; i < 5; i++ {
		logger.Info("msg", "i", i)
	}

	entries := h.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected the ring to cap at 3 entries, got %d", len(entries))
	}
	// Most recent first: i=4, then i=3, then i=2 — i=0 and i=1 were evicted.
	for idx, wantI := range []int64{4, 3, 2} {
		if entries[idx].Attrs["i"] != wantI {
			t.Fatalf("entry %d: expected i=%d, got %v", idx, wantI, entries[idx].Attrs["i"])
		}
	}
}

// TestHandlerWithAttrsSharesRing confirms a derived handler (via
// logger.With(...), which calls WithAttrs) still writes into the same
// shared ring rather than starting a fresh, empty one.
func TestHandlerWithAttrsSharesRing(t *testing.T) {
	h := NewHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), 10)
	logger := slog.New(h).With("component", "test")

	logger.Info("from derived logger")

	entries := h.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry captured via the derived logger, got %d", len(entries))
	}
	if entries[0].Message != "from derived logger" {
		t.Fatalf("unexpected message %q", entries[0].Message)
	}
}

func TestHandlerEnabledDelegates(t *testing.T) {
	inner := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError})
	h := NewHandler(inner, 10)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected Enabled to delegate to the wrapped handler's own level filter")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("expected Enabled(Error) to be true given the wrapped handler's LevelError threshold")
	}
}
