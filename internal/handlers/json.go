package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// decodeJSON decodes the request body into dst, rejecting unknown fields
// and bodies over 1 MiB. On failure it writes a 400 response itself and
// returns false, so callers can just `if !decodeJSON(...) { return }`.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	// Reject anything after the first JSON value. Without this, a body like
	// `{"done":true}{"done":false}` is accepted and the trailing document
	// silently ignored — an ambiguity worth refusing outright in a request
	// parser rather than resolving by accident.
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid JSON body: unexpected data after the JSON document")
		return false
	}
	return true
}

// writeJSON writes data as a JSON response. encoding/json's default
// SetEscapeHTML(true) behavior (left untouched here) escapes '<', '>' and
// '&', so user-supplied text is safe even if this JSON is ever rendered
// into an HTML context.
//
// Cache-Control: no-store is set on every response through this helper —
// every /api/v1/... JSON response funnels through here — so neither the
// browser's own HTTP disk cache nor any intermediary proxy can ever serve a
// stale GET response for a list/item without an explicit network round trip.
// This is deliberately independent of the service worker: sw.js's own
// handleApiRead already never uses the Cache Storage API for API routes
// (network-first, falling back to reading the IndexedDB mirror directly on
// failure — see sw.js's own comments), but that guarantee only holds while a
// service worker is actually installed and controlling the page. Without
// this header, a GET to /api/v1/lists/{id} carries no explicit freshness
// info at all, which is squarely in the range where different browsers'
// heuristic HTTP caching can disagree — this closes that off unconditionally
// rather than relying on it never mattering in practice.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// pathID parses the "{id}" path value as a positive integer. On failure it
// writes a 400 response itself and returns ok=false.
func pathID(w http.ResponseWriter, r *http.Request) (id int64, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// nullableString converts an empty string to nil, for storing optional
// text columns (e.g. item URL) as SQL NULL rather than "".
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// stringValue is nullableString's inverse: it reads an optional text
// column back out as "" when unset, for callers that need to compare a
// "was this actually changed" value (e.g. an item's URL before/after an
// edit) rather than round-trip it back into SQL.
func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sameFloat reports whether two optional amounts are equal: both unset, or
// both set to the same value.
func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
