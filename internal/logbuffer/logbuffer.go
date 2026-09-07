// Package logbuffer implements a fixed-capacity, in-memory ring buffer of
// recent structured log entries, exposed as an slog.Handler middleware —
// wrap it around the application's real handler (see cmd/server/main.go)
// and every record that flows through slog also lands in the buffer,
// readable via Entries() for the admin "Console d'Administration" logs
// panel (internal/handlers/admin_logs.go, GET /api/v1/admin/logs).
//
// This is deliberately not backed by a file or the database: Trakka logs
// structured JSON to stdout by convention (see CLAUDE.md's "Logging" rule)
// and has no log-shipping/persistence story, so a recent-activity viewer
// scoped to "since this process started" is exactly what an admin needs to
// glance at what's happening right now — matching the project's
// ultra-lightweight, stdlib-only ethos rather than introducing a log file,
// a database table, or a third-party logging backend.
package logbuffer

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Entry is one captured log record, shaped for direct JSON serialization
// by the admin logs endpoint.
type Entry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// ring is a fixed-capacity circular buffer of Entry, shared by pointer
// across every Handler a WithAttrs/WithGroup call derives — slog creates a
// new handler value for each such call (e.g. logger.With("key", val)), but
// they must all still write into the same buffer rather than each starting
// a fresh, empty one.
type ring struct {
	mu    sync.Mutex
	buf   []Entry
	start int // index of the oldest entry currently stored
	count int // number of valid entries currently stored (<= len(buf))
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]Entry, capacity)}
}

func (r *ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := len(r.buf)
	if r.count < size {
		r.buf[(r.start+r.count)%size] = e
		r.count++
	} else {
		r.buf[r.start] = e
		r.start = (r.start + 1) % size
	}
}

// snapshot returns every currently stored entry, oldest first.
func (r *ring) snapshot() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}

// Handler wraps another slog.Handler, capturing every record it handles
// into a shared ring buffer before delegating to it unchanged — stdout
// JSON logging behavior is completely unaffected by this wrapper.
type Handler struct {
	next slog.Handler
	ring *ring
}

// NewHandler wraps next, keeping the most recent capacity records in
// memory (older ones are silently dropped).
func NewHandler(next slog.Handler, capacity int) *Handler {
	return &Handler{next: next, ring: newRing(capacity)}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	attrs := make(map[string]any, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.ring.add(Entry{
		Time:    record.Time,
		Level:   record.Level.String(),
		Message: record.Message,
		Attrs:   attrs,
	})
	return h.next.Handle(ctx, record)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{next: h.next.WithAttrs(attrs), ring: h.ring}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name), ring: h.ring}
}

// Entries returns every currently buffered entry, most recent first — the
// order the admin logs panel wants to read them in.
func (h *Handler) Entries() []Entry {
	entries := h.ring.snapshot()
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries
}
