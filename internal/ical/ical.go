// Package ical writes iCalendar (RFC 5545) content: the content-line
// syntax (CRLF line endings, folding at 75 octets), TEXT value escaping,
// and the DATE, DATE-TIME and DURATION value formats. It knows nothing
// about Trakka's items; internal/handlers' calendar feed decides which
// components and properties to write.
//
// Hand-rolled with the standard library only, like internal/webpush and the
// OIDC client: the subset a read-only feed needs is small, and none of it
// requires parsing iCalendar.
package ical

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// maxLineOctets is RFC 5545 §3.1's limit on a content line's length,
// excluding the CRLF.
const maxLineOctets = 75

// Writer accumulates content lines.
type Writer struct {
	buf bytes.Buffer
}

// Line writes one content line, folding it as needed. name is the property
// name together with any parameters ("DTSTART;VALUE=DATE"); value must
// already be in its value type's syntax — see Text for TEXT values.
func (w *Writer) Line(name, value string) {
	line := name + ":" + value
	limit := maxLineOctets
	for len(line) > limit {
		// Never split a multi-octet UTF-8 sequence across two lines.
		cut := limit
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		w.buf.WriteString(line[:cut])
		w.buf.WriteString("\r\n ")
		line = line[cut:]
		// A continuation line starts with the space written above, which
		// counts toward its own 75 octets.
		limit = maxLineOctets - 1
	}
	w.buf.WriteString(line)
	w.buf.WriteString("\r\n")
}

// Text writes a property whose value type is TEXT, escaping value.
func (w *Writer) Text(name, value string) {
	w.Line(name, EscapeText(value))
}

// Bytes returns everything written so far.
func (w *Writer) Bytes() []byte {
	return w.buf.Bytes()
}

// EscapeText escapes s as an RFC 5545 §3.3.11 TEXT value: backslash,
// semicolon and comma are backslash-escaped, any line break becomes "\n",
// and other control characters (which TEXT does not allow) are dropped.
func EscapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\\' || r == ';' || r == ',':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\r':
			if i < len(s) && s[i] == '\n' {
				i++
			}
			b.WriteString(`\n`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r == utf8.RuneError && size == 1):
			// Dropped: a control character, or a byte that isn't UTF-8.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Date formats t's calendar date as a DATE value (YYYYMMDD).
func Date(t time.Time) string {
	return t.Format("20060102")
}

// UTC formats t as a DATE-TIME value in UTC (YYYYMMDDTHHMMSSZ).
func UTC(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// Duration formats a signed whole number of minutes as a DURATION value:
// -1500 → "-P1DT1H", 540 → "PT9H", 0 → "PT0S". Whole days use the day
// designator, which RFC 5545 defines as nominal days (a calendar day, even
// across a daylight saving change) rather than 24 hours.
func Duration(minutes int) string {
	if minutes == 0 {
		return "PT0S"
	}
	var b strings.Builder
	if minutes < 0 {
		b.WriteByte('-')
		minutes = -minutes
	}
	b.WriteByte('P')
	days, rest := minutes/(24*60), minutes%(24*60)
	if days > 0 {
		b.WriteString(strconv.Itoa(days))
		b.WriteByte('D')
	}
	if rest > 0 {
		b.WriteByte('T')
		if hours := rest / 60; hours > 0 {
			b.WriteString(strconv.Itoa(hours))
			b.WriteByte('H')
		}
		if mins := rest % 60; mins > 0 {
			b.WriteString(strconv.Itoa(mins))
			b.WriteByte('M')
		}
	}
	return b.String()
}
