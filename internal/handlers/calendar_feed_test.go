package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"trakka/internal/auth"
	"trakka/internal/ical"
	"trakka/internal/models"
)

func createFeedToken(t *testing.T, app *Application, user *models.User) string {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleCalendarFeedTokenCreate(rec, withUser(httptest.NewRequest(http.MethodPost, "/api/v1/calendar/feed-token", nil), user))
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating feed token: %d %s", rec.Code, rec.Body.String())
	}
	var got calendarFeedTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Token == "" || !got.Enabled {
		t.Fatalf("unexpected create response %s (%v)", rec.Body.String(), err)
	}
	return got.Token
}

func showFeedToken(t *testing.T, app *Application, user *models.User) calendarFeedTokenResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleCalendarFeedTokenShow(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/v1/calendar/feed-token", nil), user))
	if rec.Code != http.StatusOK {
		t.Fatalf("showing feed token: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("GET must never return the token: %s", rec.Body.String())
	}
	var got calendarFeedTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// fetchFeed requests the feed through the full router, with no session
// cookie — the way a calendar app does.
func fetchFeed(handler http.Handler, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/calendar/feed.ics?token="+token, nil))
	return rec
}

func TestCalendarFeedTokenLifecycle(t *testing.T) {
	app := newTestApplication(t)
	app.Auth = auth.NewService(app.DB, nil, time.Hour, false)
	handler := app.Routes()
	user := mustCreateTestUser(t, app, "alice@example.com")

	if got := showFeedToken(t, app, user); got.Enabled {
		t.Fatalf("a new user has no feed link, got %+v", got)
	}
	if rec := fetchFeed(handler, "made-up-token"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token: got %d, want 404", rec.Code)
	}
	if rec := fetchFeed(handler, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing token: got %d, want 404", rec.Code)
	}

	first := createFeedToken(t, app, user)
	rec := fetchFeed(handler, first)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/calendar; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if body := rec.Body.String(); !strings.HasPrefix(body, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(body, "END:VCALENDAR\r\n") {
		t.Errorf("not an iCalendar document: %q", body)
	}
	if got := showFeedToken(t, app, user); !got.Enabled || got.LastUsedAt == nil {
		t.Errorf("after a fetch, want enabled with last_used_at, got %+v", got)
	}

	// Regenerating revokes the previous link.
	second := createFeedToken(t, app, user)
	if second == first {
		t.Fatal("regenerating returned the same token")
	}
	if rec := fetchFeed(handler, first); rec.Code != http.StatusNotFound {
		t.Errorf("replaced token: got %d, want 404", rec.Code)
	}
	if rec := fetchFeed(handler, second); rec.Code != http.StatusOK {
		t.Errorf("new token: got %d", rec.Code)
	}
	if got := showFeedToken(t, app, user); got.LastUsedAt == nil {
		t.Errorf("want last_used_at after fetching the new link, got %+v", got)
	}

	for range 2 { // idempotent
		rec := httptest.NewRecorder()
		app.handleCalendarFeedTokenDelete(rec, withUser(httptest.NewRequest(http.MethodDelete, "/api/v1/calendar/feed-token", nil), user))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := fetchFeed(handler, second); rec.Code != http.StatusNotFound {
		t.Errorf("deleted token: got %d, want 404", rec.Code)
	}
	if got := showFeedToken(t, app, user); got.Enabled {
		t.Errorf("after delete, got %+v", got)
	}

	// Managing the link, unlike reading the feed, needs a session.
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/v1/calendar/feed-token", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Errorf("feed-token without a session: got %d, want 401", unauthenticated.Code)
	}
}

// feedEvent returns the unfolded VEVENT whose SUMMARY is title, "" if none.
func feedEvent(feed, title string) string {
	for _, event := range strings.Split(feed, "BEGIN:VEVENT\r\n")[1:] {
		if strings.Contains(event, "\r\nSUMMARY:"+title+"\r\n") || strings.HasPrefix(event, "SUMMARY:"+title+"\r\n") {
			return event
		}
	}
	return ""
}

func TestCalendarFeedContent(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	app := newTestApplication(t)
	app.Location = paris
	app.Config.BaseURL = "https://trakka.example.com"
	ctx := context.Background()

	owner := mustCreateTestUser(t, app, "owner@example.com")
	stranger := mustCreateTestUser(t, app, "stranger@example.com")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := app.DB.CreateList(ctx, "Corvées", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	strangerHouse, err := app.DB.CreateHouseWithOwner(ctx, "Ailleurs", stranger.ID)
	if err != nil {
		t.Fatal(err)
	}
	privateList, err := app.DB.CreateList(ctx, "Privée", "todo", strangerHouse.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	sharedList, err := app.DB.CreateList(ctx, "Partagée", "todo", strangerHouse.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateOrUpdateListShare(ctx, sharedList.ID, owner.ID, "read"); err != nil {
		t.Fatal(err)
	}

	createTask := func(listID int64, title string, dueDate, rule, endDate *string) *models.Item {
		t.Helper()
		item, err := app.DB.CreateItem(ctx, listID, title, nil, 1, nil, false, 0, nil, dueDate, rule, endDate, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating %s: %v", title, err)
		}
		return item
	}
	str := func(s string) *string { return &s }
	intp := func(i int) *int { return &i }

	// Weekly at 18:30 until June 30th, reminded at the due time.
	trash := createTask(list.ID, "Sortir les poubelles", str("2030-01-15"), str("FREQ=WEEKLY;BYDAY=TU"), str("2030-06-30"))
	if _, err := app.DB.SetItemSchedule(ctx, trash.ID, str("18:30"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.SetItemReminder(ctx, trash.ID, true, intp(0), str("09:00"), true); err != nil {
		t.Fatal(err)
	}
	// Monthly on the 31st, all day, reminded the day before at 20:00.
	rent := createTask(list.ID, "Payer le loyer", str("2030-01-31"), str("FREQ=MONTHLY"), nil)
	if _, err := app.DB.SetItemReminder(ctx, rent.ID, true, intp(1), str("20:00"), false); err != nil {
		t.Fatal(err)
	}
	createTask(list.ID, "Pain, lait; œufs", str("2030-02-01"), nil, nil)
	createTask(list.ID, "Sans date", nil, nil, nil)
	done := createTask(list.ID, "Déjà fait", str("2030-02-02"), nil, nil)
	if rec := patchItem(t, app, owner, done.ID, `{"done":true}`); rec.Code != http.StatusOK {
		t.Fatalf("checking off: %d %s", rec.Code, rec.Body.String())
	}
	today := app.today()
	water := createTask(list.ID, "Arroser", str(today), str("FREQ=DAILY"), nil)
	if rec := patchItem(t, app, owner, water.ID, `{"done":true}`); rec.Code != http.StatusOK {
		t.Fatalf("checking off: %d %s", rec.Code, rec.Body.String())
	}
	createTask(privateList.ID, "Secret", str("2030-03-01"), nil, nil)
	createTask(sharedList.ID, "Tâche partagée", str("2030-03-02"), nil, nil)

	handler := app.Routes()
	rec := fetchFeed(handler, createFeedToken(t, app, owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for i, line := range strings.Split(strings.TrimSuffix(raw, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Errorf("line %d is %d octets: %q", i, len(line), line)
		}
	}
	feed := strings.ReplaceAll(raw, "\r\n ", "")

	for _, want := range []string{
		"X-WR-CALNAME:Trakka — owner@example.com",
		// Calendar properties come before any component.
		"X-WR-TIMEZONE:Europe/Paris\r\nBEGIN:VTIMEZONE\r\nTZID:Europe/Paris",
	} {
		if !strings.Contains(feed, want) {
			t.Errorf("feed lacks %q", want)
		}
	}

	if got := strings.Count(feed, "BEGIN:VEVENT"); got != 5 {
		t.Errorf("got %d events, want 5 (trash, rent, groceries, watering, shared)", got)
	}
	for _, absent := range []string{"Sans date", "Déjà fait", "Secret"} {
		if feedEvent(feed, absent) != "" {
			t.Errorf("%q must not be in the feed", absent)
		}
	}

	expectIn := func(title string, wants ...string) {
		t.Helper()
		event := feedEvent(feed, title)
		if event == "" {
			t.Fatalf("no event for %q in:\n%s", title, feed)
		}
		for _, want := range wants {
			if !strings.Contains(event, want+"\r\n") {
				t.Errorf("%q event lacks %q:\n%s", title, want, event)
			}
		}
	}

	expectIn("Sortir les poubelles",
		"UID:trakka-item-"+strconv.FormatInt(trash.ID, 10)+"@trakka.example.com",
		"DTSTART;TZID=Europe/Paris:20300115T183000",
		"DTEND;TZID=Europe/Paris:20300115T190000",
		// June 30th, 18:30 CEST (+02:00).
		"RRULE:FREQ=WEEKLY;BYDAY=TU;UNTIL=20300630T163000Z",
		"TRIGGER:PT0S",
		"CATEGORIES:Corvées",
		"URL:https://trakka.example.com/?list="+strconv.FormatInt(list.ID, 10),
	)
	expectIn("Payer le loyer",
		"DTSTART;VALUE=DATE:20300131",
		"DTEND;VALUE=DATE:20300201",
		// The day before at 20:00, from the event's start at midnight.
		"TRIGGER:-PT4H",
	)
	if strings.Contains(feedEvent(feed, "Payer le loyer"), "RRULE") {
		t.Error("a monthly task on the 31st must not get an RRULE (RFC 5545 skips short months, Trakka clamps)")
	}
	expectIn(`Pain\, lait\; œufs`, "DTSTART;VALUE=DATE:20300201")
	if strings.Contains(feedEvent(feed, `Pain\, lait\; œufs`), "BEGIN:VALARM") {
		t.Error("a task without a resolved reminder must not get an alarm")
	}
	todayDate, err := time.Parse(dateLayout, today)
	if err != nil {
		t.Fatal(err)
	}
	expectIn("Arroser",
		"DTSTART;VALUE=DATE:"+todayDate.AddDate(0, 0, 1).Format("20060102"),
		"RRULE:FREQ=DAILY",
	)
	expectIn("Tâche partagée", "CATEGORIES:Partagée")
}

func TestCalendarFeedWithoutBaseURLUsesRequestHostAndUTC(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, list := itemTestFixture(t, app)
	due := "2030-05-10"
	item, err := app.DB.CreateItem(ctx, list.ID, "Rendez-vous", nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	dueTime := "09:15"
	if _, err := app.DB.SetItemSchedule(ctx, item.ID, &dueTime, nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/calendar/feed.ics?token="+createFeedToken(t, app, owner), nil)
	req.Host = "tasks.local:8080"
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.String())
	}
	feed := strings.ReplaceAll(rec.Body.String(), "\r\n ", "")
	if strings.Contains(feed, "VTIMEZONE") {
		t.Error("a UTC instance needs no VTIMEZONE")
	}
	event := feedEvent(feed, "Rendez-vous")
	for _, want := range []string{
		"UID:trakka-item-" + strconv.FormatInt(item.ID, 10) + "@tasks.local",
		"DTSTART:20300510T091500Z",
		"DTEND:20300510T094500Z",
	} {
		if !strings.Contains(event, want+"\r\n") {
			t.Errorf("event lacks %q:\n%s", want, event)
		}
	}
	if strings.Contains(event, "URL:") {
		t.Error("without BASE_URL there is no absolute link to give")
	}
}

func TestVTimezone(t *testing.T) {
	tests := []struct {
		zone  string
		wants []string
	}{
		{"Europe/Paris", []string{
			"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nTZNAME:CEST\r\nEND:DAYLIGHT",
			"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nTZNAME:CET\r\nEND:STANDARD",
		}},
		{"America/New_York", []string{
			"DTSTART:19700308T020000\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU",
			"DTSTART:19701101T020000\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nRRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU",
		}},
		{"Australia/Sydney", []string{
			"BEGIN:DAYLIGHT\r\nDTSTART:19701004T020000\r\nTZOFFSETFROM:+1000\r\nTZOFFSETTO:+1100\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=1SU",
			"BEGIN:STANDARD\r\nDTSTART:19700405T030000\r\nTZOFFSETFROM:+1100\r\nTZOFFSETTO:+1000\r\nRRULE:FREQ=YEARLY;BYMONTH=4;BYDAY=1SU",
		}},
		{"Asia/Tokyo", []string{
			"BEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0900\r\nTZOFFSETTO:+0900\r\nTZNAME:JST\r\nEND:STANDARD",
		}},
	}
	for _, tt := range tests {
		loc, err := time.LoadLocation(tt.zone)
		if err != nil {
			t.Skip("no tzdata:", err)
		}
		tz, ok := newVTimezone(loc, 2026)
		if !ok {
			t.Errorf("%s: no VTIMEZONE", tt.zone)
			continue
		}
		var w ical.Writer
		tz.write(&w)
		out := string(w.Bytes())
		if !strings.HasPrefix(out, "BEGIN:VTIMEZONE\r\nTZID:"+tt.zone+"\r\n") {
			t.Errorf("%s: %q", tt.zone, out)
		}
		for _, want := range tt.wants {
			if !strings.Contains(out, want) {
				t.Errorf("%s: lacks %q in:\n%s", tt.zone, want, out)
			}
		}
	}
}
