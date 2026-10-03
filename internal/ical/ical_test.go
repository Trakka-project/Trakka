package ical

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEscapeText(t *testing.T) {
	tests := map[string]string{
		"Plain":                     "Plain",
		`a\b`:                       `a\\b`,
		"Pain, lait; œufs":          `Pain\, lait\; œufs`,
		"line1\nline2\r\nline3\rx":  `line1\nline2\nline3\nx`,
		"bell\x07 and del\x7f gone": "bell and del gone",
		"tab\tkept":                 "tab\tkept",
		"bad \xff byte":             "bad  byte",
	}
	for in, want := range tests {
		if got := EscapeText(in); got != want {
			t.Errorf("EscapeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLineFoldsAt75OctetsWithoutSplittingRunes(t *testing.T) {
	var w Writer
	// "é" is two octets, so a naive byte split would land mid-rune.
	value := strings.Repeat("é", 100)
	w.Text("SUMMARY", value)
	out := string(w.Bytes())

	if !strings.HasSuffix(out, "\r\n") {
		t.Fatalf("missing final CRLF: %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
	if len(lines) < 2 {
		t.Fatalf("expected folding, got one line of %d octets", len(lines[0]))
	}
	var unfolded strings.Builder
	for i, line := range lines {
		if len(line) > maxLineOctets {
			t.Errorf("line %d is %d octets", i, len(line))
		}
		if !utf8.ValidString(line) {
			t.Errorf("line %d splits a rune: %q", i, line)
		}
		if i > 0 {
			if !strings.HasPrefix(line, " ") {
				t.Errorf("continuation line %d doesn't start with a space: %q", i, line)
			}
			line = line[1:]
		}
		unfolded.WriteString(line)
	}
	if got := unfolded.String(); got != "SUMMARY:"+value {
		t.Errorf("unfolded = %q", got)
	}
}

func TestShortLineIsNotFolded(t *testing.T) {
	var w Writer
	w.Line("BEGIN", "VCALENDAR")
	if got := string(w.Bytes()); got != "BEGIN:VCALENDAR\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestDuration(t *testing.T) {
	tests := map[int]string{
		0:     "PT0S",
		540:   "PT9H",
		-240:  "-PT4H",
		-30:   "-PT30M",
		-1440: "-P1D",
		-1500: "-P1DT1H",
		2925:  "P2DT45M", // 2 days (2880) + 45 minutes
		90:    "PT1H30M",
	}
	for in, want := range tests {
		if got := Duration(in); got != want {
			t.Errorf("Duration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDateAndUTC(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	moment := time.Date(2026, 7, 14, 18, 30, 0, 0, paris)
	if got := Date(moment); got != "20260714" {
		t.Errorf("Date = %q", got)
	}
	if got := UTC(moment); got != "20260714T163000Z" {
		t.Errorf("UTC = %q", got)
	}
}
