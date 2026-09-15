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
	if _, err := d.SetItemReminder(ctx, plainTask.ID, true, &offsetDays, &timeOfDay); err != nil {
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
	if _, err := d.SetItemReminder(ctx, recurring.ID, true, &offsetDays, &timeOfDay); err != nil {
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
	if _, err := d.SetItemReminder(ctx, disabled.ID, false, nil, nil); err != nil {
		t.Fatalf("SetItemReminder: %v", err)
	}
	doneTask, err := d.CreateItem(ctx, list.ID, "Déjà faite", nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating done task: %v", err)
	}
	if _, err := d.SetItemReminder(ctx, doneTask.ID, true, &offsetDays, &timeOfDay); err != nil {
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
