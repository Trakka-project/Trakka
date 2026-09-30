package handlers

import (
	"context"
	"fmt"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/recurrence"
)

// nextOccurrenceScanTimeout bounds one RunNextOccurrenceScan pass. The scan
// only ever touches done recurring items, a handful of rows per household.
const nextOccurrenceScanTimeout = 30 * time.Second

// dateLayout is the YYYY-MM-DD form every item date (due_date,
// next_due_date, recurrence_end_date) is stored and compared in.
const dateLayout = "2006-01-02"

// applyRecurrenceLifecycle keeps item.NextDueDate consistent with item's
// done/recurrence state before a write. item holds the values about to be
// persisted; before is the item as it was prior to this request.
//
// Checking off a recurring item no longer un-checks it on the spot: it
// stays done (so it shows in the list's "Terminés" section) and NextDueDate
// is set to its next occurrence (see nextOccurrence), until
// RunNextOccurrenceScan brings it back when that occurrence starts. A
// recurring item that is not done, or an item that isn't recurring, never
// carries a NextDueDate — so un-checking an item by hand simply cancels the
// pending occurrence and leaves due_date on the one being un-checked.
//
// A done item whose rule, due date and end date are unchanged keeps the
// NextDueDate computed when it was checked off (including nil for a series
// that has ended), so an unrelated edit — a rename, a label — never moves
// it. If the next occurrence would fall after RecurrenceEndDate, the series
// has run its course: NextDueDate is nil and the item simply stays done,
// like a non-recurring one.
func applyRecurrenceLifecycle(item, before *models.Item, today string) {
	if item.RecurrenceRule == nil || !item.Done {
		item.NextDueDate = nil
		return
	}
	if before.Done && before.RecurrenceRule != nil &&
		*before.RecurrenceRule == *item.RecurrenceRule &&
		stringValue(before.DueDate) == stringValue(item.DueDate) &&
		stringValue(before.RecurrenceEndDate) == stringValue(item.RecurrenceEndDate) {
		item.NextDueDate = before.NextDueDate
		return
	}

	next, err := nextOccurrence(stringValue(item.DueDate), *item.RecurrenceRule, today)
	if err != nil {
		// Only reachable if a rule already persisted before validation was
		// tightened somehow slipped through — leave the item plainly done
		// rather than scheduling on a rule we can't interpret.
		item.NextDueDate = nil
		return
	}
	if end := stringValue(item.RecurrenceEndDate); end != "" && next > end {
		item.NextDueDate = nil
		return
	}
	item.NextDueDate = &next
}

// nextOccurrence is recurrence.Rule.Next over the YYYY-MM-DD strings items
// store: the occurrence that follows from (the completed occurrence's due
// date, or "" when it had none, in which case today stands in for it) and
// falls strictly after today. The rule is parsed once for the whole
// catch-up. It has already been validated by internal/validate.Recurrence
// by the time this is called; legacy spellings still parse, for rows
// written before migration 22.
//
// static/js/recurrence.js (TrakkaRecurrence.nextOccurrence, shared by the
// page scripts and the service worker) is the hand-kept JS port of this and
// of internal/recurrence: any change here must be mirrored there.
func nextOccurrence(from, rule, today string) (string, error) {
	if from == "" {
		from = today
	}
	start, err := time.Parse(dateLayout, from)
	if err != nil {
		return "", fmt.Errorf("parsing due date %q: %w", from, err)
	}
	parsed, err := recurrence.Parse(rule)
	if err != nil {
		return "", fmt.Errorf("unrecognized recurrence rule %q: %w", rule, err)
	}
	day, err := time.Parse(dateLayout, today)
	if err != nil {
		return "", fmt.Errorf("parsing today %q: %w", today, err)
	}
	return parsed.Next(start, day).Format(dateLayout), nil
}

// occurrenceStart is when a done recurring item's next occurrence comes
// back into the active list: the start of its due day, or its reminder
// moment if that comes first (a "la veille à 20:00" reminder brings the
// task back the evening before, so the notification never points at a
// task still sitting in "Terminés").
func occurrenceStart(c *db.NextOccurrenceCandidate, loc *time.Location) (time.Time, error) {
	start, err := time.ParseInLocation(dateLayout, c.NextDueDate, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing next due date %q: %w", c.NextDueDate, err)
	}
	if c.ReminderEnabled && c.OffsetDays != nil && c.TimeOfDay != nil {
		at, err := reminderMoment(c.NextDueDate, c.DueTime, c.AtDueTime, *c.OffsetDays, *c.TimeOfDay, loc)
		if err != nil {
			return time.Time{}, err
		}
		if at.Before(start) {
			start = at
		}
	}
	return start, nil
}

// RunNextOccurrenceScan brings back every done recurring item whose next
// occurrence has started (see occurrenceStart): un-checked, due on its
// NextDueDate, with its reminder re-armed by the due date change. Called
// every minute from cmd/server/main.go regardless of whether Web Push is
// configured — bringing a task back is not a notification feature — and at
// the start of every RunDueReminderScan, so a reminder due at the very
// moment its occurrence starts is sent in the same pass. Best-effort: one
// item's failure is logged and never stops the rest.
func (app *Application) RunNextOccurrenceScan(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, nextOccurrenceScanTimeout)
	defer cancel()

	candidates, err := app.DB.ListItemsAwaitingNextOccurrence(scanCtx)
	if err != nil {
		app.Logger.Error("listing items awaiting their next occurrence", "error", err)
		return
	}

	loc := app.location()
	now := time.Now()
	for _, c := range candidates {
		if scanCtx.Err() != nil {
			return
		}
		start, err := occurrenceStart(c, loc)
		if err != nil {
			app.Logger.Error("computing next occurrence start", "item_id", c.ItemID, "error", err)
			continue
		}
		if now.Before(start) {
			continue
		}
		activated, err := app.DB.ActivateNextOccurrence(scanCtx, c.ItemID, c.NextDueDate)
		if err != nil {
			app.Logger.Error("activating next occurrence", "item_id", c.ItemID, "error", err)
			continue
		}
		if activated {
			app.Logger.Info("recurring task back for its next occurrence", "item_id", c.ItemID, "due_date", c.NextDueDate)
		}
	}
}

// today is the current calendar date (YYYY-MM-DD) in the instance's
// APP_TIMEZONE — the "today" recurring items are scheduled against.
func (app *Application) today() string {
	return time.Now().In(app.location()).Format(dateLayout)
}
