package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"trakka/internal/auth"
	"trakka/internal/db"
	"trakka/internal/ical"
	"trakka/internal/models"
	"trakka/internal/recurrence"
)

// Personal calendar feed (docs/CALENDAR_EXPORT.md): every user can generate
// a secret link, GET /api/v1/calendar/feed.ics?token=..., that calendar
// apps (Nextcloud, Google Calendar, Apple Calendar, Thunderbird) subscribe
// to as an iCalendar feed. It lists the user's dated tasks — on every list
// they can access — as events, with their reminders as alarms.
//
// The feed route is the one /api/v1/... route outside RequireSession: a
// calendar app polling in the background has no session cookie and can't
// go through a login form or SSO, so the token in the URL is the
// credential. It is a read-only one: it opens this feed and nothing else.
// The token is 32 crypto/rand bytes, stored only as its SHA-256 hash
// (auth.HashToken, like a session token), so it is shown to the user once,
// right after it is generated. Regenerating it replaces it, which is how a
// leaked link is revoked. Logging records r.URL.Path only, so the token
// never reaches the logs, and Referrer-Policy: no-referrer is on every
// response.

// calendarFeedTokenBytes is the token's entropy, the same as a session's.
const calendarFeedTokenBytes = 32

// maxCalendarFeedTokenLength rejects absurd query strings before hashing.
const maxCalendarFeedTokenLength = 128

// timedTaskDuration is how long a task with a due time lasts in the
// calendar. A task is a point in time, but RFC 5545 requires DTEND to be
// strictly after DTSTART.
const timedTaskDuration = 30 * time.Minute

// calendarFeedRefresh is the polling interval the feed suggests to calendar
// apps (REFRESH-INTERVAL, X-PUBLISHED-TTL). Many apps impose their own:
// Google Calendar refreshes subscriptions every few hours at best.
const calendarFeedRefresh = "PT1H"

// calendarFeedTokenResponse is the shape of GET/POST
// /api/v1/calendar/feed-token. Token is only ever set in the POST response.
type calendarFeedTokenResponse struct {
	Enabled    bool    `json:"enabled"`
	Token      string  `json:"token,omitempty"`
	CreatedAt  *string `json:"created_at,omitempty"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
}

func newCalendarFeedTokenResponse(t *db.CalendarFeedToken) calendarFeedTokenResponse {
	resp := calendarFeedTokenResponse{Enabled: true, CreatedAt: &t.CreatedAt}
	if t.LastUsedAt != "" {
		resp.LastUsedAt = &t.LastUsedAt
	}
	return resp
}

// handleCalendarFeedTokenShow answers GET /api/v1/calendar/feed-token:
// whether the caller has a feed link, and when it was made and last used.
func (app *Application) handleCalendarFeedTokenShow(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	t, err := app.DB.GetCalendarFeedToken(r.Context(), user.ID)
	if errors.Is(err, db.ErrNotFound) {
		writeJSON(w, http.StatusOK, calendarFeedTokenResponse{Enabled: false})
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCalendarFeedTokenResponse(t))
}

// handleCalendarFeedTokenCreate answers POST /api/v1/calendar/feed-token:
// generates the caller's feed token, replacing any previous one, and
// returns it — the only time it is ever returned.
func (app *Application) handleCalendarFeedTokenCreate(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	token, err := auth.RandomToken(calendarFeedTokenBytes)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	t, err := app.DB.SetCalendarFeedToken(r.Context(), user.ID, auth.HashToken(token))
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	resp := newCalendarFeedTokenResponse(t)
	resp.Token = token
	writeJSON(w, http.StatusCreated, resp)
}

// handleCalendarFeedTokenDelete answers DELETE /api/v1/calendar/feed-token:
// the caller's feed link stops working. Idempotent.
func (app *Application) handleCalendarFeedTokenDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	if err := app.DB.DeleteCalendarFeedToken(r.Context(), user.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCalendarFeed answers GET /api/v1/calendar/feed.ics?token=...: the
// token owner's tasks as an iCalendar document. An unknown token gets a 404
// rather than a 401, which Apple Calendar would answer with a password
// prompt that can never succeed.
func (app *Application) handleCalendarFeed(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" || len(token) > maxCalendarFeedTokenLength {
		writeError(w, http.StatusNotFound, "calendar feed not found")
		return
	}
	userID, err := app.DB.UseCalendarFeedToken(r.Context(), auth.HashToken(token))
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "calendar feed not found")
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	user, err := app.DB.GetUser(r.Context(), userID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	items, err := app.DB.ListCalendarFeedItems(r.Context(), userID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	feed := calendarFeed{
		user:     user,
		host:     app.calendarUIDHost(r),
		baseURL:  strings.TrimRight(app.Config.BaseURL, "/"),
		location: app.location(),
		now:      time.Now(),
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="trakka.ics"`)
	// Same as every other /api/v1/... response: the feed is per-user and
	// secret, so no shared cache may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(feed.render(items))
}

// calendarUIDHost is the domain part of every event's UID, which must stay
// stable across fetches so calendar apps update events instead of
// duplicating them: BASE_URL's host when configured, else the Host the
// calendar app used (the same on every poll of the same link).
func (app *Application) calendarUIDHost(r *http.Request) string {
	if host := baseURLHost(app.Config.BaseURL); host != "" {
		return host
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host = strings.ToLower(strings.Trim(host, "[]")); host == "" {
		return "trakka"
	}
	return host
}

// calendarFeed renders one user's feed.
type calendarFeed struct {
	user     *models.User
	host     string
	baseURL  string
	location *time.Location
	now      time.Time
}

func (f calendarFeed) render(items []*db.CalendarFeedItem) []byte {
	var w ical.Writer
	w.Line("BEGIN", "VCALENDAR")
	w.Line("VERSION", "2.0")
	w.Line("PRODID", "-//Trakka//Calendar feed//EN")
	w.Line("CALSCALE", "GREGORIAN")
	w.Line("METHOD", "PUBLISH")
	name := "Trakka"
	if f.user.DisplayName != "" {
		name += " — " + f.user.DisplayName
	}
	w.Text("X-WR-CALNAME", name)
	w.Text("X-WR-CALDESC", f.text("Vos tâches Trakka avec une échéance", "Your Trakka tasks with a due date"))
	w.Line("REFRESH-INTERVAL;VALUE=DURATION", calendarFeedRefresh)
	w.Line("X-PUBLISHED-TTL", calendarFeedRefresh)

	// Timed events are written in the instance's time zone (TZID plus a
	// VTIMEZONE describing it), so a recurring 18:00 task stays at 18:00
	// across daylight saving changes; UTC is the fallback for a zone whose
	// rules a VTIMEZONE can't capture this simply (see newVTimezone).
	tzid := ""
	if f.location != time.UTC {
		if tz, ok := newVTimezone(f.location, f.now.Year()); ok {
			tzid = tz.tzid
			// A calendar property, so before any component.
			w.Text("X-WR-TIMEZONE", tzid)
			tz.write(&w)
		}
	}

	for _, it := range items {
		f.writeEvent(&w, it, tzid)
	}
	w.Line("END", "VCALENDAR")
	return w.Bytes()
}

// text picks the French or English version of a feed string by the user's
// language (users.language, "fr" unless set otherwise).
func (f calendarFeed) text(fr, en string) string {
	if f.user.Language == "en" {
		return en
	}
	return fr
}

func (f calendarFeed) writeEvent(w *ical.Writer, it *db.CalendarFeedItem, tzid string) {
	day, err := time.ParseInLocation(dateLayout, it.Date, f.location)
	if err != nil {
		return
	}
	var dueMinutes int
	timed := it.DueTime != ""
	if timed {
		hour, minute, err := parseTimeOfDay(it.DueTime)
		if err != nil {
			return
		}
		dueMinutes = hour*60 + minute
	}

	w.Line("BEGIN", "VEVENT")
	w.Line("UID", fmt.Sprintf("trakka-item-%d@%s", it.ItemID, f.host))
	w.Line("DTSTAMP", ical.UTC(f.now))
	if updated, err := time.Parse(time.RFC3339, it.UpdatedAt); err == nil {
		w.Line("LAST-MODIFIED", ical.UTC(updated))
	}
	w.Text("SUMMARY", it.Title)

	var start time.Time
	if timed {
		start = time.Date(day.Year(), day.Month(), day.Day(), dueMinutes/60, dueMinutes%60, 0, 0, f.location)
		f.writeDateTime(w, "DTSTART", start, tzid)
		f.writeDateTime(w, "DTEND", start.Add(timedTaskDuration), tzid)
	} else {
		w.Line("DTSTART;VALUE=DATE", ical.Date(day))
		w.Line("DTEND;VALUE=DATE", ical.Date(day.AddDate(0, 0, 1)))
	}
	if rule := f.rrule(it, day, timed); rule != "" {
		w.Line("RRULE", rule)
	}
	// A task doesn't make anyone busy.
	w.Line("TRANSP", "TRANSPARENT")
	w.Text("CATEGORIES", it.ListName)
	description := f.text("Liste : ", "List: ") + it.ListName
	if f.baseURL != "" {
		link := fmt.Sprintf("%s/?list=%d", f.baseURL, it.ListID)
		w.Line("URL", link)
		description += "\n" + link
	}
	w.Text("DESCRIPTION", description)

	if it.HasReminder {
		// Relative to the event's start, so it applies to every occurrence
		// of a recurring task: an all-day event starts at local midnight,
		// a timed one at its due time.
		trigger := 0
		if !timed || !it.AtDueTime {
			hour, minute, err := parseTimeOfDay(it.TimeOfDay)
			if err == nil {
				trigger = -it.OffsetDays*24*60 + hour*60 + minute - dueMinutes
			}
		}
		w.Line("BEGIN", "VALARM")
		w.Line("ACTION", "DISPLAY")
		w.Text("DESCRIPTION", it.Title)
		w.Line("TRIGGER", ical.Duration(trigger))
		w.Line("END", "VALARM")
	}
	w.Line("END", "VEVENT")
}

func (f calendarFeed) writeDateTime(w *ical.Writer, name string, t time.Time, tzid string) {
	if tzid == "" {
		w.Line(name, ical.UTC(t))
		return
	}
	w.Line(name+";TZID="+tzid, t.Format("20060102T150405"))
}

// rrule turns the task's recurrence into an RRULE, or "" when it has none
// or the calendar couldn't show it the way Trakka schedules it. Trakka's
// rules are already an RFC 5545 subset and step the same way (BYDAY weeks
// start on Monday, RRULE's default WKST), with one exception: a monthly
// task on the 29th–31st (or a yearly one on February 29) is clamped to
// shorter months' last day by Trakka, while RFC 5545 skips those months —
// such a task shows only its next occurrence. A task left overdue shows
// its past occurrences too; Trakka itself skips them once it is done.
func (f calendarFeed) rrule(it *db.CalendarFeedItem, day time.Time, timed bool) string {
	if it.RecurrenceRule == "" {
		return ""
	}
	rule, err := recurrence.Parse(it.RecurrenceRule)
	if err != nil {
		return ""
	}
	if (rule.Freq == recurrence.Monthly && day.Day() > 28) ||
		(rule.Freq == recurrence.Yearly && day.Month() == time.February && day.Day() == 29) {
		return ""
	}
	out := rule.String()
	if it.RecurrenceEndDate != "" {
		end, err := time.ParseInLocation(dateLayout, it.RecurrenceEndDate, f.location)
		if err != nil || end.Before(day) {
			return ""
		}
		if timed {
			// With a TZID'd or UTC DTSTART, UNTIL must be a UTC date-time;
			// the last occurrence is the end date at the due time.
			hour, minute, _ := parseTimeOfDay(it.DueTime)
			out += ";UNTIL=" + ical.UTC(time.Date(end.Year(), end.Month(), end.Day(), hour, minute, 0, 0, f.location))
		} else {
			out += ";UNTIL=" + ical.Date(end)
		}
	}
	return out
}

// vtimezone is a VTIMEZONE component describing one time zone.
type vtimezone struct {
	tzid        string
	observances []tzObservance
}

// tzObservance is one STANDARD or DAYLIGHT sub-component.
type tzObservance struct {
	kind       string
	name       string
	offsetFrom int
	offsetTo   int
	start      string
	rule       string
}

// newVTimezone describes loc by the transitions it makes during year,
// recurring yearly, and reports whether it could: a zone with no daylight
// saving gets one STANDARD observance, one with the usual two yearly
// transitions gets STANDARD and DAYLIGHT with a BYMONTH/BYDAY rule each.
// Anything else (a zone with more transitions in a year, or rules a "nth
// weekday of the month" can't express) returns false, and the feed falls
// back to UTC times.
func newVTimezone(loc *time.Location, year int) (*vtimezone, bool) {
	tz := &vtimezone{tzid: loc.String()}
	transitions := zoneTransitions(loc, year)
	switch len(transitions) {
	case 0:
		name, offset := time.Date(year, time.January, 1, 0, 0, 0, 0, loc).Zone()
		tz.observances = append(tz.observances, tzObservance{kind: "STANDARD", name: name, offsetFrom: offset, offsetTo: offset, start: "19700101T000000"})
	case 2:
		for _, at := range transitions {
			_, before := at.Add(-time.Second).Zone()
			name, after := at.Zone()
			// The onset is given in the local time in force before it.
			local := at.In(time.FixedZone("", before))
			byDay, ok := weekdayOrdinal(local)
			if !ok {
				return nil, false
			}
			kind := "STANDARD"
			if at.IsDST() {
				kind = "DAYLIGHT"
			}
			first := firstYearlyOccurrence(1970, local, byDay)
			tz.observances = append(tz.observances, tzObservance{
				kind: kind, name: name, offsetFrom: before, offsetTo: after,
				start: first.Format("20060102T150405"),
				rule:  fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%s", int(local.Month()), byDay),
			})
		}
	default:
		return nil, false
	}
	return tz, true
}

func (tz *vtimezone) write(w *ical.Writer) {
	w.Line("BEGIN", "VTIMEZONE")
	w.Line("TZID", tz.tzid)
	for _, o := range tz.observances {
		w.Line("BEGIN", o.kind)
		w.Line("DTSTART", o.start)
		w.Line("TZOFFSETFROM", utcOffset(o.offsetFrom))
		w.Line("TZOFFSETTO", utcOffset(o.offsetTo))
		if o.rule != "" {
			w.Line("RRULE", o.rule)
		}
		if o.name != "" {
			w.Text("TZNAME", o.name)
		}
		w.Line("END", o.kind)
	}
	w.Line("END", "VTIMEZONE")
}

// zoneTransitions returns the instants during year at which loc's UTC
// offset changes, to the second.
func zoneTransitions(loc *time.Location, year int) []time.Time {
	var out []time.Time
	t := time.Date(year, time.January, 1, 0, 0, 0, 0, loc)
	end := time.Date(year+1, time.January, 1, 0, 0, 0, 0, loc)
	_, offset := t.Zone()
	for t.Before(end) {
		next := t.Add(24 * time.Hour)
		if _, o := next.Zone(); o != offset {
			// Binary search for the first second with the new offset.
			lo, hi := t, next
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if _, o := mid.Zone(); o == offset {
					lo = mid
				} else {
					hi = mid
				}
			}
			out = append(out, hi)
			_, offset = hi.Zone()
		}
		t = next
	}
	return out
}

// weekdayOrdinal describes local's date as an RRULE BYDAY value: "-1SU"
// for the last Sunday of its month, "2SU" for the second, and so on.
func weekdayOrdinal(local time.Time) (string, bool) {
	code := strings.ToUpper(local.Weekday().String()[:2])
	daysInMonth := time.Date(local.Year(), local.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if local.Day()+7 > daysInMonth {
		return "-1" + code, true
	}
	n := (local.Day()-1)/7 + 1
	if n > 4 {
		return "", false
	}
	return strconv.Itoa(n) + code, true
}

// firstYearlyOccurrence returns the date in year matching byDay (as
// weekdayOrdinal writes it) in local's month, at local's clock time — a
// VTIMEZONE observance's DTSTART must be an occurrence of its own RRULE.
func firstYearlyOccurrence(year int, local time.Time, byDay string) time.Time {
	n, _ := strconv.Atoi(byDay[:len(byDay)-2])
	weekday := local.Weekday()
	clock := func(day int) time.Time {
		return time.Date(year, local.Month(), day, local.Hour(), local.Minute(), local.Second(), 0, time.UTC)
	}
	if n < 0 {
		last := time.Date(year, local.Month()+1, 0, 0, 0, 0, 0, time.UTC)
		back := (int(last.Weekday()) - int(weekday) + 7) % 7
		return clock(last.Day() - back)
	}
	first := time.Date(year, local.Month(), 1, 0, 0, 0, 0, time.UTC)
	forward := (int(weekday) - int(first.Weekday()) + 7) % 7
	return clock(1 + forward + 7*(n-1))
}

// utcOffset formats an offset in seconds east of UTC as a UTC-OFFSET value
// (+0100, -0430).
func utcOffset(seconds int) string {
	sign := '+'
	if seconds < 0 {
		sign = '-'
		seconds = -seconds
	}
	out := fmt.Sprintf("%c%02d%02d", sign, seconds/3600, seconds%3600/60)
	if s := seconds % 60; s != 0 {
		out += fmt.Sprintf("%02d", s)
	}
	return out
}
