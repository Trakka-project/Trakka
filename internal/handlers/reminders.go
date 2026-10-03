package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Local reminders for the Android app (android/, docs/MOBILE_BUILD.md).
//
// The app's WebView has no Web Push, so the app schedules task reminders on
// the phone itself, as local notifications that fire without network. It
// asks this endpoint for what to schedule, so that when a reminder fires
// stays computed in one place (reminderMoment, in the instance's time zone,
// from the timing resolved when the task was written) rather than being
// re-derived in JavaScript. Each reminder carries the same title, body and
// link the due-reminder push would.

// upcomingRemindersHorizon is how far ahead reminders are returned. The app
// asks again every time it opens or a task changes, so this only needs to
// cover a long stretch without opening it.
const upcomingRemindersHorizon = 60 * 24 * time.Hour

// maxUpcomingReminders keeps the app well under Android's limit on the alarms
// one app may hold (500). The earliest ones are kept.
const maxUpcomingReminders = 200

type upcomingReminder struct {
	ItemID   int64     `json:"item_id"`
	ListID   int64     `json:"list_id"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	URL      string    `json:"url"`
	RemindAt time.Time `json:"remind_at"`
}

// handleRemindersUpcoming answers GET /api/v1/reminders/upcoming: the caller's
// reminders still to come within upcomingRemindersHorizon, earliest first.
func (app *Application) handleRemindersUpcoming(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	reminders, err := app.DB.ListActiveRemindersForUser(r.Context(), user.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	loc := app.location()
	now := time.Now()
	upcoming := []upcomingReminder{}
	for _, c := range reminders {
		at, err := reminderMoment(c.DueDate, c.DueTime, c.AtDueTime, c.OffsetDays, c.TimeOfDay, loc)
		if err != nil {
			app.Logger.Warn("skipping a reminder with an invalid moment", "item_id", c.ItemID, "error", err)
			continue
		}
		// Past ones are the push scan's to catch up on; the phone would only
		// show them all at once.
		if !at.After(now) || at.Sub(now) > upcomingRemindersHorizon {
			continue
		}
		upcoming = append(upcoming, upcomingReminder{
			ItemID: c.ItemID,
			ListID: c.ListID,
			Title:  c.Title,
			// Phrased as of the moment it fires, as the push is.
			Body:     fmt.Sprintf("🔔 %s — %s", describeDueFR(c.DueDate, c.DueTime, at), c.ListName),
			URL:      fmt.Sprintf("/?list=%d", c.ListID),
			RemindAt: at.UTC(),
		})
	}
	sort.Slice(upcoming, func(i, j int) bool { return upcoming[i].RemindAt.Before(upcoming[j].RemindAt) })
	if len(upcoming) > maxUpcomingReminders {
		upcoming = upcoming[:maxUpcomingReminders]
	}
	writeJSON(w, http.StatusOK, map[string]any{"reminders": upcoming})
}
