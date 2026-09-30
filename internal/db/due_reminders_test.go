package db

import (
	"context"
	"testing"
)

// TestListItemsForDueReminderScan exercises the scan query's filter (not
// done, has a due date, reminder enabled, resolved offset/time) — including
// that it applies to a plain, non-recurring item exactly as it does to a
// recurring one, the generalization this scan replaced
// ListItemsForRecurringNotifyScan with — and, separately, that
// MarkDueReminderSent excludes an item until its due date changes again —
// the "re-arms automatically" behavior ListItemsForDueReminderScan's own
// doc comment describes.
func TestListItemsForDueReminderScan(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	owner := mustCreateUser(t, ctx, d)
	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}

	due := "2026-01-10"
	offsetDays := 1
	timeOfDay := "20:00"

	// A plain, non-recurring task with a due date and a custom reminder —
	// the case this generalized scan newly supports.
	plainTask, err := d.CreateItem(ctx, list.ID, "Payer le loyer", nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating plain task: %v", err)
	}
	if _, err := d.SetItemReminder(ctx, plainTask.ID, true, &offsetDays, &timeOfDay, false); err != nil {
		t.Fatalf("SetItemReminder: %v", err)
	}

	// A recurring item with a due date and an enabled reminder must still
	// show up too — this scan is a superset of the old recurring-only one,
	// not a replacement scoped away from it.
	rule := "WEEKLY"
	recurring, err := d.CreateItem(ctx, list.ID, "Sortir les poubelles", nil, 1, nil, false, 0, nil, &due, &rule, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating recurring item: %v", err)
	}
	if _, err := d.SetItemReminder(ctx, recurring.ID, true, &offsetDays, &timeOfDay, false); err != nil {
		t.Fatalf("SetItemReminder: %v", err)
	}

	// A task with its reminder explicitly disabled, an already-done task, a
	// task with no due date yet, and a task whose reminder was never given a
	// resolved offset/time (reminder_offset_days/reminder_time stay NULL at
	// the SQL level until SetItemReminder is called — see that method's own
	// doc comment) must never show up in the scan.
	disabled, err := d.CreateItem(ctx, list.ID, "Rappel désactivé", nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item with reminder disabled: %v", err)
	}
	if _, err := d.SetItemReminder(ctx, disabled.ID, false, nil, nil, false); err != nil {
		t.Fatalf("SetItemReminder: %v", err)
	}
	doneTask, err := d.CreateItem(ctx, list.ID, "Déjà faite", nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating done task: %v", err)
	}
	if _, err := d.SetItemReminder(ctx, doneTask.ID, true, &offsetDays, &timeOfDay, false); err != nil {
		t.Fatalf("SetItemReminder: %v", err)
	}
	if _, err := d.UpdateItem(ctx, doneTask.ID, doneTask.Title, doneTask.URL, doneTask.Quantity, doneTask.Price, doneTask.PriceAuto, doneTask.ImageURL,
		true, doneTask.Position, doneTask.TargetMonth, doneTask.DueDate, doneTask.RecurrenceRule, doneTask.RecurrenceEndDate, doneTask.IsUrgent, doneTask.RecurrenceLeadMinutes, nil, false); err != nil {
		t.Fatalf("marking item done: %v", err)
	}
	if _, err := d.CreateItem(ctx, list.ID, "Sans échéance", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false); err != nil {
		t.Fatalf("creating no-due-date item: %v", err)
	}
	if _, err := d.CreateItem(ctx, list.ID, "Rappel jamais résolu", nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false); err != nil {
		t.Fatalf("creating item with unresolved reminder: %v", err)
	}

	candidates, err := d.ListItemsForDueReminderScan(ctx)
	if err != nil {
		t.Fatalf("ListItemsForDueReminderScan: %v", err)
	}
	byID := map[int64]*DueReminderCandidate{}
	for _, c := range candidates {
		byID[c.ItemID] = c
	}
	if len(candidates) != 2 || byID[plainTask.ID] == nil || byID[recurring.ID] == nil {
		t.Fatalf("expected exactly the plain task and the recurring item, got %+v", candidates)
	}
	for _, id := range []int64{plainTask.ID, recurring.ID} {
		c := byID[id]
		if c.DueDate != due {
			t.Errorf("item %d: DueDate = %q, want %q", id, c.DueDate, due)
		}
		if c.OffsetDays != offsetDays {
			t.Errorf("item %d: OffsetDays = %d, want %d", id, c.OffsetDays, offsetDays)
		}
		if c.TimeOfDay != timeOfDay {
			t.Errorf("item %d: TimeOfDay = %q, want %q", id, c.TimeOfDay, timeOfDay)
		}
		if c.ListID != list.ID {
			t.Errorf("item %d: ListID = %d, want %d", id, c.ListID, list.ID)
		}
	}

	// Marking the reminder sent for one item's current due date excludes
	// just that item from the next scan...
	if err := d.MarkDueReminderSent(ctx, plainTask.ID, due); err != nil {
		t.Fatalf("MarkDueReminderSent: %v", err)
	}
	afterMark, err := d.ListItemsForDueReminderScan(ctx)
	if err != nil {
		t.Fatalf("ListItemsForDueReminderScan (after mark): %v", err)
	}
	if len(afterMark) != 1 || afterMark[0].ItemID != recurring.ID {
		t.Fatalf("expected only the recurring item to remain, got %+v", afterMark)
	}

	// ...but the moment its due_date changes to something new, it re-arms
	// automatically with no separate "clear the flag" call needed.
	newDue := "2026-01-17"
	refetched, err := d.GetItem(ctx, plainTask.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if _, err := d.UpdateItem(ctx, refetched.ID, refetched.Title, refetched.URL, refetched.Quantity, refetched.Price, refetched.PriceAuto, refetched.ImageURL,
		refetched.Done, refetched.Position, refetched.TargetMonth, &newDue, refetched.RecurrenceRule, refetched.RecurrenceEndDate, refetched.IsUrgent, refetched.RecurrenceLeadMinutes, nil, false); err != nil {
		t.Fatalf("advancing due date: %v", err)
	}
	rearmed, err := d.ListItemsForDueReminderScan(ctx)
	if err != nil {
		t.Fatalf("ListItemsForDueReminderScan (after due date change): %v", err)
	}
	byID = map[int64]*DueReminderCandidate{}
	for _, c := range rearmed {
		byID[c.ItemID] = c
	}
	if len(rearmed) != 2 || byID[plainTask.ID] == nil || byID[plainTask.ID].DueDate != newDue {
		t.Fatalf("expected the plain task to re-arm with its new due date, got %+v", rearmed)
	}
}

// TestDueReminderKeyIncludesDueTime covers migration 21's dedup key: an
// item with a due time is marked sent under "date T time", so moving the
// task to another time re-arms its reminder, and notification_sent_at is
// only reported while it belongs to the current due date/time.
func TestDueReminderKeyIncludesDueTime(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	owner := mustCreateUser(t, ctx, d)
	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}

	due := "2026-01-10"
	offsetDays := 0
	timeOfDay := "09:00"
	item, err := d.CreateItem(ctx, list.ID, "Rendez-vous", nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := d.SetItemReminder(ctx, item.ID, true, &offsetDays, &timeOfDay, true); err != nil {
		t.Fatalf("SetItemReminder: %v", err)
	}
	dueTime := "18:30"
	if _, err := d.SetItemSchedule(ctx, item.ID, &dueTime, nil); err != nil {
		t.Fatalf("SetItemSchedule: %v", err)
	}

	candidates, err := d.ListItemsForDueReminderScan(ctx)
	if err != nil {
		t.Fatalf("ListItemsForDueReminderScan: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %+v", candidates)
	}
	c := candidates[0]
	if c.DueTime != dueTime || !c.AtDueTime {
		t.Fatalf("candidate DueTime/AtDueTime = %q/%v, want %q/true", c.DueTime, c.AtDueTime, dueTime)
	}
	if c.Key() != "2026-01-10T18:30" {
		t.Fatalf("Key() = %q, want 2026-01-10T18:30", c.Key())
	}

	if err := d.MarkDueReminderSent(ctx, item.ID, c.Key()); err != nil {
		t.Fatalf("MarkDueReminderSent: %v", err)
	}
	if left, err := d.ListItemsForDueReminderScan(ctx); err != nil || len(left) != 0 {
		t.Fatalf("expected no candidate once sent, got %+v (err %v)", left, err)
	}
	sent, err := d.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if sent.NotificationSentAt == nil {
		t.Fatal("expected notification_sent_at to be reported for the current due time")
	}

	// Moving the task to another time re-arms it, and the earlier send no
	// longer counts for it.
	later := "20:00"
	moved, err := d.SetItemSchedule(ctx, item.ID, &later, nil)
	if err != nil {
		t.Fatalf("SetItemSchedule: %v", err)
	}
	if moved.NotificationSentAt != nil {
		t.Fatalf("expected notification_sent_at to reset after moving the due time, got %q", *moved.NotificationSentAt)
	}
	rearmed, err := d.ListItemsForDueReminderScan(ctx)
	if err != nil || len(rearmed) != 1 || rearmed[0].Key() != "2026-01-10T20:00" {
		t.Fatalf("expected the item to re-arm at its new time, got %+v (err %v)", rearmed, err)
	}
}
