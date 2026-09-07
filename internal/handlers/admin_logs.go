package handlers

import (
	"net/http"
	"strconv"

	"trakka/internal/logbuffer"
)

// defaultAdminLogsLimit/maxAdminLogsLimit bound how many entries GET
// /api/v1/admin/logs returns — a generous default for glancing at recent
// activity without an explicit ?limit=, capped so a caller can't ask for
// more than the ring buffer (internal/logbuffer, sized in cmd/server/main.go)
// could ever actually hold.
const (
	defaultAdminLogsLimit = 200
	maxAdminLogsLimit     = 1000
)

// handleAdminLogsList returns the most recent server log entries, most
// recent first — the admin "Logs" panel's data source. There is no
// persistence across a restart (see internal/logbuffer's package doc) and
// no offline mirror; this is purely a live, in-process snapshot.
func (app *Application) handleAdminLogsList(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) {
		return
	}

	limit := defaultAdminLogsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	if limit > maxAdminLogsLimit {
		limit = maxAdminLogsLimit
	}

	entries := []logbuffer.Entry{}
	if app.LogBuffer != nil {
		entries = app.LogBuffer.Entries()
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	writeJSON(w, http.StatusOK, entries)
}
