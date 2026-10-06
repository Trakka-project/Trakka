package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
)

// Daily overdue-tasks summary: once a day, at the user's own
// overdue_tasks_summary_time in APP_TIMEZONE, one notification listing every
// task past its due date that nobody checked off yet (see
// models.User.OverdueTasksSummaryEnabled/OverdueTasksSummaryTime). Sent by
// Web Push from RunOverdueSummaryScan, and scheduled on the phone by the
// Android app from GET /api/v1/reminders/upcoming's overdue_summary
// (nextOverdueSummary), the same split as the per-task reminders.

// overdueSummaryTag is the Notification tag of every summary, so a new day's
// summary replaces a previous one still in the tray instead of stacking.
const overdueSummaryTag = "trakka-overdue-summary"

// overdueSummaryMaxTitles is how many task titles the summary's body spells
// out before "et N autres".
const overdueSummaryMaxTitles = 3

// RunOverdueSummaryScan sends the daily summary to every user who has it
// turned on, hasn't had today's yet, and whose summary time has passed.
// Called on the due-reminder ticker from cmd/server/main.go (so every
// minute by default, only with push configured); best-effort like
// RunDueReminderScan: one user's failure is logged and never stops the rest.
func (app *Application) RunOverdueSummaryScan(ctx context.Context) {
	if !app.Config.PushEnabled() {
		return
	}
	app.runOverdueSummaryScan(ctx, time.Now())
}

// runOverdueSummaryScan is RunOverdueSummaryScan as of now. A user whose
// time has passed is marked as handled for today whether or not they had
// anything overdue, so the scan looks them up once a day, not every minute.
// A summary turned on after its time of day goes out on the next tick: the
// day's summary hasn't been sent yet, and it's still that day.
func (app *Application) runOverdueSummaryScan(ctx context.Context, now time.Time) {
	scanCtx, cancel := context.WithTimeout(ctx, dueReminderScanTimeout)
	defer cancel()

	loc := app.location()
	today := now.In(loc).Format(dateLayout)
	users, err := app.DB.ListUsersAwaitingOverdueSummary(scanCtx, today)
	if err != nil {
		app.Logger.Error("listing users for the overdue summary scan", "error", err)
		return
	}
	for _, u := range users {
		if scanCtx.Err() != nil {
			return
		}
		at, err := reminderMoment(today, "", false, 0, u.Time, loc)
		if err != nil {
			app.Logger.Warn("skipping an overdue summary with an invalid time", "user_id", u.UserID, "error", err)
			continue
		}
		if now.Before(at) {
			continue
		}
		tasks, err := app.DB.ListOverdueTasksForUser(scanCtx, u.UserID, today)
		if err != nil {
			app.Logger.Error("listing overdue tasks", "user_id", u.UserID, "error", err)
			continue
		}
		if len(tasks) > 0 {
			app.sendToUsers(scanCtx, []int64{u.UserID}, overdueSummaryPayload(tasks, today))
		}
		if err := app.DB.MarkOverdueSummarySent(scanCtx, u.UserID, today); err != nil {
			app.Logger.Error("marking the overdue summary sent", "user_id", u.UserID, "error", err)
		}
	}
}

// overdueSummaryPayload phrases the summary of tasks (at least one, oldest
// first) for the day it is sent (YYYY-MM-DD), in French like every
// server-composed notification (see notifyListChange's doc comment). It
// opens the tasks' list when they all belong to one, else the dashboard.
func overdueSummaryPayload(tasks []*db.OverdueTask, day string) pushPayload {
	url := fmt.Sprintf("/?list=%d", tasks[0].ListID)
	for _, t := range tasks[1:] {
		if t.ListID != tasks[0].ListID {
			url = "/"
			break
		}
	}
	if len(tasks) == 1 {
		return pushPayload{
			Title: "Tâche en retard",
			Body:  fmt.Sprintf("« %s » (%s), %s, n'est toujours pas cochée.", tasks[0].Title, tasks[0].ListName, overdueSinceFR(tasks[0].DueDate, day)),
			URL:   url,
			Tag:   overdueSummaryTag,
		}
	}

	shown := tasks
	if len(shown) > overdueSummaryMaxTitles {
		shown = shown[:overdueSummaryMaxTitles]
	}
	titles := make([]string, len(shown))
	for i, t := range shown {
		titles[i] = t.Title
	}
	names := strings.Join(titles, ", ")
	switch rest := len(tasks) - len(shown); {
	case rest == 1:
		names += " et 1 autre"
	case rest > 1:
		names += fmt.Sprintf(" et %d autres", rest)
	}
	return pushPayload{
		Title: "Tâches en retard",
		Body:  fmt.Sprintf("%d tâches en retard ne sont pas cochées : %s", len(tasks), names),
		URL:   url,
		Tag:   overdueSummaryTag,
	}
}

// overdueSinceFR says when a task due on dueDate fell due, as of day:
// "échue hier" or "échue le ven. 2 oct.".
func overdueSinceFR(dueDate, day string) string {
	if dueDate == shiftDate(day, -1) {
		return "échue hier"
	}
	due, err := time.Parse(dateLayout, dueDate)
	if err != nil {
		return "échue le " + dueDate
	}
	now, err := time.Parse(dateLayout, day)
	if err != nil {
		now = due
	}
	return "échue le " + shortDateFR(due, now)
}

// upcomingOverdueSummary is the next overdue-tasks summary, as GET
// /api/v1/reminders/upcoming returns it for the Android app to schedule:
// the push's text, when it fires, and which tasks it lists, so the app can
// drop it once they are all checked off before it fires, offline too.
type upcomingOverdueSummary struct {
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	URL      string    `json:"url"`
	RemindAt time.Time `json:"remind_at"`
	ItemIDs  []int64   `json:"item_ids"`
}

// nextOverdueSummary returns user's next summary still to come as of now, or
// nil when it is turned off or would have nothing to list. Before today's
// summary time, that's today's, listing the tasks due before today; after
// it, tomorrow's, listing those due today or earlier still not done — which
// the app replaces whenever a task changes, so checking one off takes it
// out.
func (app *Application) nextOverdueSummary(ctx context.Context, user *models.User, now time.Time) (*upcomingOverdueSummary, error) {
	if !user.OverdueTasksSummaryEnabled {
		return nil, nil
	}
	loc := app.location()
	day := now.In(loc).Format(dateLayout)
	at, err := reminderMoment(day, "", false, 0, user.OverdueTasksSummaryTime, loc)
	if err != nil {
		return nil, err
	}
	if !at.After(now) {
		day = shiftDate(day, 1)
		if at, err = reminderMoment(day, "", false, 0, user.OverdueTasksSummaryTime, loc); err != nil {
			return nil, err
		}
	}
	tasks, err := app.DB.ListOverdueTasksForUser(ctx, user.ID, day)
	if err != nil || len(tasks) == 0 {
		return nil, err
	}
	payload := overdueSummaryPayload(tasks, day)
	ids := make([]int64, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ItemID
	}
	return &upcomingOverdueSummary{
		Title:    payload.Title,
		Body:     payload.Body,
		URL:      payload.URL,
		RemindAt: at.UTC(),
		ItemIDs:  ids,
	}, nil
}

// shiftDate moves a YYYY-MM-DD calendar date by days. Computed on a UTC
// midnight, so no DST transition can shift it by a day.
func shiftDate(date string, days int) string {
	d, err := time.Parse(dateLayout, date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, days).Format(dateLayout)
}
