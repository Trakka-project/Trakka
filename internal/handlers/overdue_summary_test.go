package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
)

// sentPush is one notification sendToUsers was asked to deliver.
type sentPush struct {
	userIDs []int64
	payload pushPayload
}

// pushRecorder collects what sendToUsers would deliver (Application.pushHook).
type pushRecorder struct {
	mu    sync.Mutex
	sent  []sentPush
	queue chan sentPush
}

func recordPushes(app *Application) *pushRecorder {
	rec := &pushRecorder{queue: make(chan sentPush, 16)}
	app.pushHook = func(userIDs []int64, payload pushPayload) {
		p := sentPush{userIDs: append([]int64(nil), userIDs...), payload: payload}
		rec.mu.Lock()
		rec.sent = append(rec.sent, p)
		rec.mu.Unlock()
		rec.queue <- p
	}
	return rec
}

// all returns what was recorded so far, and forgets it.
func (rec *pushRecorder) all() []sentPush {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	sent := rec.sent
	rec.sent = nil
	for len(rec.queue) > 0 {
		<-rec.queue
	}
	return sent
}

// next waits for a notification sent from a background goroutine.
func (rec *pushRecorder) next(t *testing.T) sentPush {
	t.Helper()
	select {
	case p := <-rec.queue:
		rec.mu.Lock()
		rec.sent = rec.sent[1:]
		rec.mu.Unlock()
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no notification was sent")
		return sentPush{}
	}
}

func setPrefs(t *testing.T, app *Application, userID int64, prefs db.NotificationPreferences) *models.User {
	t.Helper()
	user, err := app.DB.UpdateUserNotificationPreferences(context.Background(), userID, prefs)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

var (
	on  = func() *bool { b := true; return &b }()
	off = func() *bool { b := false; return &b }()
)

func TestOverdueSummaryPayload(t *testing.T) {
	day := "2026-10-06"
	one := overdueSummaryPayload([]*db.OverdueTask{{ItemID: 1, ListID: 7, ListName: "Maison", Title: "Arroser", DueDate: "2026-10-05"}}, day)
	if one.Title != "Tâche en retard" || one.Body != "« Arroser » (Maison), échue hier, n'est toujours pas cochée." || one.URL != "/?list=7" || one.Tag != overdueSummaryTag {
		t.Fatalf("one task due yesterday: %+v", one)
	}
	older := overdueSummaryPayload([]*db.OverdueTask{{ListID: 7, ListName: "Maison", Title: "Arroser", DueDate: "2026-10-02"}}, day)
	if older.Body != "« Arroser » (Maison), échue le ven. 2 oct., n'est toujours pas cochée." {
		t.Fatalf("one task four days late: %+v", older)
	}
	lastYear := overdueSummaryPayload([]*db.OverdueTask{{ListID: 7, ListName: "Maison", Title: "Arroser", DueDate: "2025-12-31"}}, day)
	if !strings.Contains(lastYear.Body, "échue le mer. 31 déc. 2025") {
		t.Fatalf("one task from last year: %+v", lastYear)
	}

	sameList := overdueSummaryPayload([]*db.OverdueTask{
		{ItemID: 1, ListID: 7, Title: "Arroser", DueDate: "2026-10-02"},
		{ItemID: 2, ListID: 7, Title: "Payer", DueDate: "2026-10-05"},
	}, day)
	if sameList.Title != "Tâches en retard" || sameList.Body != "2 tâches en retard ne sont pas cochées : Arroser, Payer" || sameList.URL != "/?list=7" {
		t.Fatalf("two tasks of one list: %+v", sameList)
	}

	many := overdueSummaryPayload([]*db.OverdueTask{
		{ListID: 7, Title: "A"}, {ListID: 7, Title: "B"}, {ListID: 8, Title: "C"}, {ListID: 7, Title: "D"}, {ListID: 7, Title: "E"},
	}, day)
	if many.Body != "5 tâches en retard ne sont pas cochées : A, B, C et 2 autres" || many.URL != "/" {
		t.Fatalf("five tasks of two lists: %+v", many)
	}
	four := overdueSummaryPayload([]*db.OverdueTask{{Title: "A"}, {Title: "B"}, {Title: "C"}, {Title: "D"}}, day)
	if !strings.HasSuffix(four.Body, "A, B, C et 1 autre") {
		t.Fatalf("four tasks: %+v", four)
	}
}

// overdueSummaryFixture is a user with the summary on at 08:00 (app.Location
// unset: UTC) and a list in their house.
func overdueSummaryFixture(t *testing.T, app *Application) (*models.User, *models.List) {
	t.Helper()
	owner, list := itemTestFixture(t, app)
	at := "08:00"
	return setPrefs(t, app, owner.ID, db.NotificationPreferences{OverdueTasksSummaryEnabled: on, OverdueTasksSummaryTime: &at}), list
}

func mustCreateDueTask(t *testing.T, app *Application, listID int64, title, due string) *models.Item {
	t.Helper()
	item, err := app.DB.CreateItem(context.Background(), listID, title, nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating %s: %v", title, err)
	}
	return item
}

// A task left undone for four days is in the summary, sent once the user's
// time has passed, and only once that day.
func TestRunOverdueSummaryScanSendsTasksOverdueForDays(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	user, list := overdueSummaryFixture(t, app)
	late := mustCreateDueTask(t, app, list.ID, "Appeler le plombier", "2026-10-02")
	mustCreateDueTask(t, app, list.ID, "Aujourd'hui", "2026-10-06")

	app.runOverdueSummaryScan(ctx, time.Date(2026, 10, 6, 7, 59, 0, 0, time.UTC))
	if sent := pushes.all(); len(sent) != 0 {
		t.Fatalf("sent before the user's summary time: %+v", sent)
	}

	app.runOverdueSummaryScan(ctx, time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC))
	sent := pushes.all()
	if len(sent) != 1 || len(sent[0].userIDs) != 1 || sent[0].userIDs[0] != user.ID {
		t.Fatalf("got %+v, want one summary to user %d", sent, user.ID)
	}
	if want := "« Appeler le plombier » (Courses), échue le ven. 2 oct., n'est toujours pas cochée."; sent[0].payload.Body != want {
		t.Fatalf("body %q, want %q (task %d only: today's isn't overdue yet)", sent[0].payload.Body, want, late.ID)
	}

	app.runOverdueSummaryScan(ctx, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	if sent := pushes.all(); len(sent) != 0 {
		t.Fatalf("sent twice the same day: %+v", sent)
	}
	// Still undone the next day: summarized again, with today's task now late too.
	app.runOverdueSummaryScan(ctx, time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC))
	if sent := pushes.all(); len(sent) != 1 || !strings.HasPrefix(sent[0].payload.Body, "2 tâches en retard") {
		t.Fatalf("next day: got %+v, want both tasks", sent)
	}
}

func TestRunOverdueSummaryScanRespectsThePreference(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	owner, list := itemTestFixture(t, app) // summary off by default
	mustCreateDueTask(t, app, list.ID, "Appeler le plombier", "2026-10-02")

	app.runOverdueSummaryScan(context.Background(), time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC))
	if sent := pushes.all(); len(sent) != 0 {
		t.Fatalf("summary off: sent %+v", sent)
	}

	setPrefs(t, app, owner.ID, db.NotificationPreferences{OverdueTasksSummaryEnabled: on})
	app.runOverdueSummaryScan(context.Background(), time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC))
	if sent := pushes.all(); len(sent) != 1 {
		t.Fatalf("summary turned on: got %+v, want one", sent)
	}
}

// The Android app is given today's summary before its time, tomorrow's
// after it, each listing every task still undone by then.
func TestNextOverdueSummary(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	user, list := overdueSummaryFixture(t, app)
	late := mustCreateDueTask(t, app, list.ID, "Appeler le plombier", "2026-10-02")
	yesterday := mustCreateDueTask(t, app, list.ID, "Arroser", "2026-10-05")
	today := mustCreateDueTask(t, app, list.ID, "Payer", "2026-10-06")

	before, err := app.nextOverdueSummary(ctx, user, time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if before == nil || !before.RemindAt.Equal(time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)) ||
		len(before.ItemIDs) != 2 || before.ItemIDs[0] != late.ID || before.ItemIDs[1] != yesterday.ID {
		t.Fatalf("before 08:00: got %+v, want today's summary of the two late tasks", before)
	}

	after, err := app.nextOverdueSummary(ctx, user, time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if after == nil || !after.RemindAt.Equal(time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)) ||
		len(after.ItemIDs) != 3 || after.ItemIDs[2] != today.ID {
		t.Fatalf("from 08:00: got %+v, want tomorrow's summary, today's task included", after)
	}

	user.OverdueTasksSummaryEnabled = false
	if got, err := app.nextOverdueSummary(ctx, user, time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)); err != nil || got != nil {
		t.Fatalf("turned off: got %+v (%v), want none", got, err)
	}
}

// sharedTaskFixture is a list owned by owner and shared (write) with friend.
func sharedTaskFixture(t *testing.T, app *Application) (owner, friend *models.User, list *models.List) {
	t.Helper()
	owner, list = itemTestFixture(t, app)
	friend = mustCreateTestUser(t, app, "friend@example.com")
	if _, err := app.DB.CreateOrUpdateListShare(context.Background(), list.ID, friend.ID, "write"); err != nil {
		t.Fatal(err)
	}
	return owner, friend, list
}

// A due reminder skips the users who turned task reminders off.
func TestDueReminderRespectsRemindersEnabled(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, friend, list := sharedTaskFixture(t, app)
	setPrefs(t, app, friend.ID, db.NotificationPreferences{RemindersEnabled: off})
	mustCreateTaskWithReminder(t, app, list.ID, "Arroser", "2026-10-06", 0, "09:00")

	candidates, err := app.DB.ListItemsForDueReminderScan(ctx)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates %+v (%v)", candidates, err)
	}
	if err := app.checkItemForDueReminder(ctx, candidates[0], time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	sent := pushes.all()
	if len(sent) != 1 || len(sent[0].userIDs) != 1 || sent[0].userIDs[0] != owner.ID {
		t.Fatalf("got %+v, want the reminder sent to the owner only", sent)
	}
}

// Each list change reaches the other users who want that type only.
func TestListChangeRespectsPreferences(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, friend, list := sharedTaskFixture(t, app)

	for _, change := range []listChange{itemAdded, itemChecked, itemUnchecked} {
		app.deliverListChange(ctx, list, owner, "Pain", change)
	}
	sent := pushes.all()
	if len(sent) != 3 {
		t.Fatalf("got %+v, want three notifications to the friend", sent)
	}
	for i, verb := range []string{"a ajouté", "a coché", "a décoché"} {
		if len(sent[i].userIDs) != 1 || sent[i].userIDs[0] != friend.ID || !strings.Contains(sent[i].payload.Body, verb) {
			t.Fatalf("notification %d: %+v, want %q sent to the friend only", i, sent[i], verb)
		}
	}

	setPrefs(t, app, friend.ID, db.NotificationPreferences{ItemAdditionsEnabled: off})
	app.deliverListChange(ctx, list, owner, "Pain", itemAdded)
	app.deliverListChange(ctx, list, owner, "Pain", itemChecked)
	if sent := pushes.all(); len(sent) != 1 || !strings.Contains(sent[0].payload.Body, "a coché") {
		t.Fatalf("additions off: got %+v, want only the check-off", sent)
	}

	setPrefs(t, app, friend.ID, db.NotificationPreferences{CollaboratorActionsEnabled: off})
	app.deliverListChange(ctx, list, owner, "Pain", itemChecked)
	app.deliverListChange(ctx, list, owner, "Pain", itemUnchecked)
	if sent := pushes.all(); len(sent) != 0 {
		t.Fatalf("collaborator actions off: got %+v, want nothing", sent)
	}
}

// Unchecking an item through the API now notifies too, as checking it does.
func TestItemsPatchNotifiesCheckAndUncheck(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	owner, friend, list := sharedTaskFixture(t, app)
	item, err := app.DB.CreateItem(context.Background(), list.ID, "Pain", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct{ body, verb string }{{`{"done":true}`, "a coché"}, {`{"done":false}`, "a décoché"}} {
		if rec := patchItem(t, app, owner, item.ID, step.body); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step.body, rec.Code, rec.Body.String())
		}
		p := pushes.next(t)
		if len(p.userIDs) != 1 || p.userIDs[0] != friend.ID || !strings.Contains(p.payload.Body, step.verb) {
			t.Fatalf("%s: got %+v, want %q sent to the friend", step.body, p, step.verb)
		}
	}
}

// An invitation or share notifies the invitee when they have an account and
// want it, and nobody otherwise.
func TestDeliverInvitation(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, list := itemTestFixture(t, app)
	friend := mustCreateTestUser(t, app, "friend@example.com")
	space, err := app.DB.CreateCustomCategory(ctx, owner.ID, "Vacances", "🏖️", "", 0)
	if err != nil {
		t.Fatal(err)
	}

	app.deliverInvitation(ctx, db.InvitationKindList, list.ID, "Friend@Example.com", owner)
	app.deliverInvitation(ctx, db.InvitationKindSpace, space.ID, "friend@example.com", owner)
	app.deliverInvitation(ctx, db.InvitationKindHouse, list.HouseID, "friend@example.com", owner)
	app.deliverInvitation(ctx, db.InvitationKindList, list.ID, "nobody@example.com", owner)
	sent := pushes.all()
	wantBodies := []string{
		"owner@example.com a partagé la liste « Courses » avec vous",
		"owner@example.com a partagé l'espace « Vacances » avec vous",
		"owner@example.com vous a invité à rejoindre la maison « Maison Test »",
	}
	if len(sent) != len(wantBodies) {
		t.Fatalf("got %+v, want %d notifications (none for an address without an account)", sent, len(wantBodies))
	}
	for i, body := range wantBodies {
		if len(sent[i].userIDs) != 1 || sent[i].userIDs[0] != friend.ID || sent[i].payload.Body != body {
			t.Fatalf("notification %d: %+v, want %q to the friend", i, sent[i], body)
		}
	}
	if sent[0].payload.URL != "/?list="+strconv.FormatInt(list.ID, 10) {
		t.Fatalf("list share URL %q", sent[0].payload.URL)
	}

	setPrefs(t, app, friend.ID, db.NotificationPreferences{ListSharingEnabled: off})
	app.deliverInvitation(ctx, db.InvitationKindList, list.ID, "friend@example.com", owner)
	if sent := pushes.all(); len(sent) != 0 {
		t.Fatalf("sharing notifications off: got %+v", sent)
	}
}

// Sharing a list through the API notifies the recipient in the background.
func TestListShareCreateNotifiesTheRecipient(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	owner, list := itemTestFixture(t, app)
	friend := mustCreateTestUser(t, app, "friend@example.com")

	idStr := strconv.FormatInt(list.ID, 10)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/lists/"+idStr+"/share", strings.NewReader(`{"email":"friend@example.com","permission":"read"}`))
	req.SetPathValue("id", idStr)
	rec := httptest.NewRecorder()
	app.handleListShareCreate(rec, withUser(req, owner))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if p := pushes.next(t); len(p.userIDs) != 1 || p.userIDs[0] != friend.ID || p.payload.Title != "Nouveau partage" {
		t.Fatalf("got %+v, want the share notice sent to the friend", p)
	}
}

// Turning task reminders off empties the Android app's schedule.
func TestRemindersUpcomingRespectsRemindersEnabled(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	mustCreateTaskWithReminder(t, app, list.ID, "Arroser", time.Now().UTC().AddDate(0, 0, 3).Format(dateLayout), 0, "09:00")

	if got := getUpcomingReminders(t, app, owner).Reminders; len(got) != 1 {
		t.Fatalf("reminders on: got %+v, want one", got)
	}
	owner = setPrefs(t, app, owner.ID, db.NotificationPreferences{RemindersEnabled: off})
	if got := getUpcomingReminders(t, app, owner).Reminders; len(got) != 0 {
		t.Fatalf("reminders off: got %+v, want none", got)
	}
}

func TestHandleMeUpdateNotificationPreferences(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "prefs@example.com")

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handleMeUpdate(rec, withUser(httptest.NewRequest(http.MethodPatch, "/api/v1/me", strings.NewReader(body)), user))
		return rec
	}

	for _, bad := range []string{`{"overdue_tasks_summary_time":"25:00"}`, `{"overdue_tasks_summary_time":""}`} {
		if rec := patch(bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", bad, rec.Code)
		}
	}
	if rec := patch(`{"reminders_enabled":false,"overdue_tasks_summary_enabled":true,"overdue_tasks_summary_time":" 07:15 ",
		"collaborator_actions_enabled":false,"item_additions_enabled":false,"list_sharing_enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	// Absent fields are left alone.
	if rec := patch(`{"keep_last_page":false,"item_additions_enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}

	reloaded, err := app.DB.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RemindersEnabled || !reloaded.OverdueTasksSummaryEnabled || reloaded.OverdueTasksSummaryTime != "07:15" ||
		reloaded.CollaboratorActionsEnabled || !reloaded.ItemAdditionsEnabled || reloaded.ListSharingEnabled {
		t.Fatalf("got %+v", reloaded)
	}
}
