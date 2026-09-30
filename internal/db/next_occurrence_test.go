package db

import (
	"context"
	"testing"
)

// TestActivateNextOccurrence covers the scan side of a recurring task's
// lifecycle: only done items with a next_due_date are listed, activation
// moves next_due_date into due_date and un-checks the item, and the
// compare-and-swap guard refuses a stale snapshot.
func TestActivateNextOccurrence(t *testing.T) {
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

	due := "2026-01-06"
	next := "2026-01-13"
	rule := "WEEKLY"
	item, err := d.CreateItem(ctx, list.ID, "Sortir les poubelles", nil, 1, nil, false, 0, nil, &due, &rule, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	offsetDays := 1
	timeOfDay := "20:00"
	if _, err := d.SetItemReminder(ctx, item.ID, true, &offsetDays, &timeOfDay, false); err != nil {
		t.Fatalf("SetItemReminder: %v", err)
	}

	// Not done yet: nothing to list even with a next_due_date (which the
	// handlers never leave on an active item anyway).
	if _, err := d.SetItemSchedule(ctx, item.ID, nil, &next); err != nil {
		t.Fatalf("SetItemSchedule: %v", err)
	}
	if got, err := d.ListItemsAwaitingNextOccurrence(ctx); err != nil || len(got) != 0 {
		t.Fatalf("expected no candidate for an active item, got %+v (err %v)", got, err)
	}

	if _, err := d.UpdateItem(ctx, item.ID, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL,
		true, item.Position, item.TargetMonth, item.DueDate, item.RecurrenceRule, item.RecurrenceEndDate, item.IsUrgent, item.RecurrenceLeadMinutes, nil, false); err != nil {
		t.Fatalf("marking item done: %v", err)
	}
	candidates, err := d.ListItemsAwaitingNextOccurrence(ctx)
	if err != nil {
		t.Fatalf("ListItemsAwaitingNextOccurrence: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %+v", candidates)
	}
	c := candidates[0]
	if c.ItemID != item.ID || c.NextDueDate != next || !c.ReminderEnabled ||
		c.OffsetDays == nil || *c.OffsetDays != 1 || c.TimeOfDay == nil || *c.TimeOfDay != "20:00" {
		t.Fatalf("unexpected candidate %+v", c)
	}

	// A snapshot that no longer matches (the user edited the item in the
	// meantime) is refused.
	if ok, err := d.ActivateNextOccurrence(ctx, item.ID, "2026-01-20"); err != nil || ok {
		t.Fatalf("expected a stale next_due_date to be refused, got ok=%v err=%v", ok, err)
	}

	ok, err := d.ActivateNextOccurrence(ctx, item.ID, next)
	if err != nil || !ok {
		t.Fatalf("ActivateNextOccurrence = %v, %v; want true, nil", ok, err)
	}
	back, err := d.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if back.Done || back.DueDate == nil || *back.DueDate != next || back.NextDueDate != nil {
		t.Fatalf("expected the item back, due %s, got done=%v due=%v next=%v", next, back.Done, back.DueDate, back.NextDueDate)
	}
	// Activating twice is a no-op.
	if ok, err := d.ActivateNextOccurrence(ctx, item.ID, next); err != nil || ok {
		t.Fatalf("expected a second activation to be a no-op, got ok=%v err=%v", ok, err)
	}
}
