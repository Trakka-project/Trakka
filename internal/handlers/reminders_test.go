package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"trakka/internal/models"
)

type upcomingRemindersResponse struct {
	Reminders []upcomingReminder `json:"reminders"`
}

func getUpcomingReminders(t *testing.T, app *Application, user *models.User) upcomingRemindersResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleRemindersUpcoming(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/v1/reminders/upcoming", nil), user))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got upcomingRemindersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return got
}

// mustCreateTaskWithReminder creates a task due on dueDate, reminded
// offsetDays before at timeOfDay.
func mustCreateTaskWithReminder(t *testing.T, app *Application, listID int64, title, dueDate string, offsetDays int, timeOfDay string) *models.Item {
	t.Helper()
	ctx := context.Background()
	item, err := app.DB.CreateItem(ctx, listID, title, nil, 1, nil, false, 0, nil, &dueDate, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating %s: %v", title, err)
	}
	if item, err = app.DB.SetItemReminder(ctx, item.ID, true, &offsetDays, &timeOfDay, false); err != nil {
		t.Fatalf("setting the reminder of %s: %v", title, err)
	}
	return item
}

func TestRemindersUpcomingReturnsTheCallersFutureRemindersOnly(t *testing.T) {
	app := newTestApplication(t)
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

	// app.Location is unset: reminders are computed in UTC.
	today := time.Now().UTC()
	inThreeDays := today.AddDate(0, 0, 3).Format(dateLayout)
	future := mustCreateTaskWithReminder(t, app, list.ID, "Arroser", inThreeDays, 1, "09:00")
	mustCreateTaskWithReminder(t, app, list.ID, "Passé", today.AddDate(0, 0, -2).Format(dateLayout), 0, "09:00")
	done := mustCreateTaskWithReminder(t, app, list.ID, "Fait", inThreeDays, 0, "10:00")
	if rec := patchItem(t, app, owner, done.ID, `{"done":true}`); rec.Code != http.StatusOK {
		t.Fatalf("checking off: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := app.DB.CreateItem(ctx, list.ID, "Sans rappel", nil, 1, nil, false, 0, nil, &inThreeDays, nil, nil, false, nil, nil, false); err != nil {
		t.Fatal(err)
	}

	got := getUpcomingReminders(t, app, owner).Reminders
	if len(got) != 1 {
		t.Fatalf("got %+v, want only %q's reminder", got, future.Title)
	}
	wantAt := time.Date(today.Year(), today.Month(), today.Day()+2, 9, 0, 0, 0, time.UTC)
	r := got[0]
	if r.ItemID != future.ID || r.ListID != list.ID || r.Title != "Arroser" || !r.RemindAt.Equal(wantAt) || r.URL != "/?list="+strconv.FormatInt(list.ID, 10) {
		t.Fatalf("got %+v, want item %d at %s", r, future.ID, wantAt)
	}
	if r.Body != "🔔 Échéance demain — Corvées" {
		t.Fatalf("body %q, want it phrased as of when it fires", r.Body)
	}

	if others := getUpcomingReminders(t, app, stranger).Reminders; len(others) != 0 {
		t.Fatalf("a user without access to the list got %+v", others)
	}
}

// A list shared with someone outside the House reminds them too, as the push
// scan does.
func TestRemindersUpcomingIncludesSharedLists(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner := mustCreateTestUser(t, app, "owner@example.com")
	friend := mustCreateTestUser(t, app, "friend@example.com")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := app.DB.CreateList(ctx, "Partagée", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateOrUpdateListShare(ctx, list.ID, friend.ID, "read"); err != nil {
		t.Fatal(err)
	}
	mustCreateTaskWithReminder(t, app, list.ID, "Rendre la perceuse", time.Now().UTC().AddDate(0, 0, 5).Format(dateLayout), 0, "08:30")

	if got := getUpcomingReminders(t, app, friend).Reminders; len(got) != 1 {
		t.Fatalf("share recipient got %+v, want the shared task's reminder", got)
	}
}

// A checked-off recurring task's next occurrence is returned before the task
// comes back, which can be as late as its reminder moment (occurrenceStart).
func TestRemindersUpcomingIncludesTheNextOccurrence(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	ctx := context.Background()

	due := time.Now().UTC().AddDate(0, 0, 2)
	dueDate := due.Format(dateLayout)
	rule := "WEEKLY"
	item, err := app.DB.CreateItem(ctx, list.ID, "Sortir les poubelles", nil, 1, nil, false, 0, nil, &dueDate, &rule, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	offset, timeOfDay := 1, "20:00"
	if _, err := app.DB.SetItemReminder(ctx, item.ID, true, &offset, &timeOfDay, false); err != nil {
		t.Fatal(err)
	}
	decodeItemResponse(t, patchItem(t, app, owner, item.ID, `{"done":true}`))

	got := getUpcomingReminders(t, app, owner).Reminders
	wantAt := time.Date(due.Year(), due.Month(), due.Day()+7-1, 20, 0, 0, 0, time.UTC)
	if len(got) != 1 || got[0].ItemID != item.ID || !got[0].RemindAt.Equal(wantAt) {
		t.Fatalf("got %+v, want the next occurrence's reminder at %s", got, wantAt)
	}
}
