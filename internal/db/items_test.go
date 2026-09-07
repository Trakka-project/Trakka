package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "trakka.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestUpdateItemPriceIfMissing exercises the three-way guard
// (UpdateItemPriceIfMissing's WHERE clause) that internal/handlers'
// background price scrape relies on to never clobber a manual price or a
// price meant for a different (since-changed) url.
func TestUpdateItemPriceIfMissing(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	url := "https://example.com/product"

	t.Run("sets price when missing and url matches", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Article", &url, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if err := d.UpdateItemPriceIfMissing(ctx, item.ID, url, 9.99); err != nil {
			t.Fatalf("UpdateItemPriceIfMissing: %v", err)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.Price == nil || *got.Price != 9.99 {
			t.Fatalf("expected price 9.99, got %v", got.Price)
		}
		if !got.PriceAuto {
			t.Fatal("expected price_auto to be true")
		}
	})

	t.Run("does not override an existing price", func(t *testing.T) {
		manual := 5.0
		item, err := d.CreateItem(ctx, list.ID, "Article manuel", &url, 1, &manual, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if err := d.UpdateItemPriceIfMissing(ctx, item.ID, url, 9.99); err != nil {
			t.Fatalf("UpdateItemPriceIfMissing: %v", err)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.Price == nil || *got.Price != 5.0 {
			t.Fatalf("expected manual price 5.0 to survive, got %v", got.Price)
		}
		if got.PriceAuto {
			t.Fatal("expected price_auto to remain false for a manual price")
		}
	})

	t.Run("does not set price when url no longer matches", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Article changé", &url, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		other := "https://example.com/other-product"
		if err := d.UpdateItemPriceIfMissing(ctx, item.ID, other, 9.99); err != nil {
			t.Fatalf("UpdateItemPriceIfMissing: %v", err)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.Price != nil {
			t.Fatalf("expected price to remain nil, got %v", *got.Price)
		}
	})
}

// TestUpdateItemImageIfMissing exercises the same three-way guard as
// TestUpdateItemPriceIfMissing above, but for UpdateItemImageIfMissing —
// internal/handlers' background product lookup relies on it to never
// clobber an image already set, or one meant for a different (since
// changed) url.
func TestUpdateItemImageIfMissing(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	url := "https://example.com/product"

	t.Run("sets image when missing and url matches", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Article", &url, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if err := d.UpdateItemImageIfMissing(ctx, item.ID, url, "https://cdn.example.com/a.jpg"); err != nil {
			t.Fatalf("UpdateItemImageIfMissing: %v", err)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.ImageURL == nil || *got.ImageURL != "https://cdn.example.com/a.jpg" {
			t.Fatalf("expected the scraped image, got %v", got.ImageURL)
		}
	})

	t.Run("does not override an existing image", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Article", &url, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if err := d.UpdateItemImageIfMissing(ctx, item.ID, url, "https://cdn.example.com/first.jpg"); err != nil {
			t.Fatalf("UpdateItemImageIfMissing: %v", err)
		}
		if err := d.UpdateItemImageIfMissing(ctx, item.ID, url, "https://cdn.example.com/second.jpg"); err != nil {
			t.Fatalf("UpdateItemImageIfMissing: %v", err)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.ImageURL == nil || *got.ImageURL != "https://cdn.example.com/first.jpg" {
			t.Fatalf("expected the first image to survive, got %v", got.ImageURL)
		}
	})

	t.Run("does not set image when url no longer matches", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Article changé", &url, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		other := "https://example.com/other-product"
		if err := d.UpdateItemImageIfMissing(ctx, item.ID, other, "https://cdn.example.com/a.jpg"); err != nil {
			t.Fatalf("UpdateItemImageIfMissing: %v", err)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.ImageURL != nil {
			t.Fatalf("expected image to remain nil, got %v", *got.ImageURL)
		}
	})
}

// TestRecurringItemPersistence exercises CreateItem/UpdateItem's handling
// of due_date/recurrence_rule/recurrence_end_date, in particular that
// is_recurring is derived purely from whether recurrence_rule is non-nil
// (there is no separate is_recurring argument to get out of sync).
func TestRecurringItemPersistence(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	t.Run("creating with a recurrence rule sets is_recurring", func(t *testing.T) {
		rule := "WEEKLY"
		item, err := d.CreateItem(ctx, list.ID, "Sortir les poubelles", nil, 1, nil, false, 0, nil, nil, &rule, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if !item.IsRecurring {
			t.Fatal("expected is_recurring to be true")
		}
		if item.RecurrenceRule == nil || *item.RecurrenceRule != "WEEKLY" {
			t.Fatalf("expected recurrence_rule WEEKLY, got %v", item.RecurrenceRule)
		}

		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if !got.IsRecurring || got.RecurrenceRule == nil || *got.RecurrenceRule != "WEEKLY" {
			t.Fatalf("expected persisted recurrence to survive a re-fetch, got is_recurring=%v rule=%v", got.IsRecurring, got.RecurrenceRule)
		}
	})

	t.Run("creating without a recurrence rule leaves is_recurring false", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Tâche unique", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if item.IsRecurring || item.RecurrenceRule != nil {
			t.Fatalf("expected a non-recurring item, got is_recurring=%v rule=%v", item.IsRecurring, item.RecurrenceRule)
		}
	})

	t.Run("UpdateItem persists due_date and recurrence_end_date, and clearing the rule clears is_recurring", func(t *testing.T) {
		rule := "MONTHLY"
		item, err := d.CreateItem(ctx, list.ID, "Facture", nil, 1, nil, false, 0, nil, nil, &rule, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}

		due := "2026-09-01"
		end := "2027-01-01"
		updated, err := d.UpdateItem(ctx, item.ID, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL,
			false, item.Position, item.TargetMonth, &due, &rule, &end, false, nil, nil, false)
		if err != nil {
			t.Fatalf("UpdateItem: %v", err)
		}
		if updated.DueDate == nil || *updated.DueDate != due {
			t.Fatalf("expected due_date %q, got %v", due, updated.DueDate)
		}
		if updated.RecurrenceEndDate == nil || *updated.RecurrenceEndDate != end {
			t.Fatalf("expected recurrence_end_date %q, got %v", end, updated.RecurrenceEndDate)
		}

		cleared, err := d.UpdateItem(ctx, item.ID, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL,
			false, item.Position, item.TargetMonth, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("UpdateItem (clearing recurrence): %v", err)
		}
		if cleared.IsRecurring || cleared.RecurrenceRule != nil || cleared.DueDate != nil || cleared.RecurrenceEndDate != nil {
			t.Fatalf("expected recurrence fully cleared, got %+v", cleared)
		}
	})
}

// TestUrgentItemPersistence exercises is_urgent surviving CreateItem,
// UpdateItem, and a re-fetch — it's a plain, independent boolean, unlike
// is_recurring which is derived from another field.
func TestUrgentItemPersistence(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	t.Run("creating with is_urgent true persists it", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Papier toilette", nil, 1, nil, false, 0, nil, nil, nil, nil, true, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if !item.IsUrgent {
			t.Fatal("expected is_urgent to be true")
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if !got.IsUrgent {
			t.Fatal("expected is_urgent to survive a re-fetch")
		}
	})

	t.Run("UpdateItem can toggle is_urgent off", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Lait", nil, 1, nil, false, 0, nil, nil, nil, nil, true, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		updated, err := d.UpdateItem(ctx, item.ID, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL,
			item.Done, item.Position, item.TargetMonth, item.DueDate, item.RecurrenceRule, item.RecurrenceEndDate, false, nil, nil, false)
		if err != nil {
			t.Fatalf("UpdateItem: %v", err)
		}
		if updated.IsUrgent {
			t.Fatal("expected is_urgent to be cleared")
		}
	})
}

// TestTargetPriceAlertPersistence exercises CreateItem/UpdateItem's
// target_price/alert_on_price_drop columns, mirroring
// TestUrgentItemPersistence's shape for a similarly independent, plain
// user-set pair of fields (see the "Price drop alerts" migration).
func TestTargetPriceAlertPersistence(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	t.Run("creating with a target price and alert enabled persists both", func(t *testing.T) {
		target := 19.99
		item, err := d.CreateItem(ctx, list.ID, "Casque audio", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, &target, true)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if item.TargetPrice == nil || *item.TargetPrice != target {
			t.Fatalf("expected target_price %v, got %v", target, item.TargetPrice)
		}
		if !item.AlertOnPriceDrop {
			t.Fatal("expected alert_on_price_drop to be true")
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.TargetPrice == nil || *got.TargetPrice != target || !got.AlertOnPriceDrop {
			t.Fatalf("expected target_price/alert_on_price_drop to survive a re-fetch, got %+v", got)
		}
	})

	t.Run("creating without a target price leaves it nil and the alert off", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Article ordinaire", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if item.TargetPrice != nil {
			t.Fatalf("expected nil target_price, got %v", *item.TargetPrice)
		}
		if item.AlertOnPriceDrop {
			t.Fatal("expected alert_on_price_drop to default to false")
		}
	})

	t.Run("UpdateItem can change and clear the target price", func(t *testing.T) {
		target := 50.0
		item, err := d.CreateItem(ctx, list.ID, "Chaise de bureau", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, &target, true)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}

		lower := 39.5
		updated, err := d.UpdateItem(ctx, item.ID, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL,
			item.Done, item.Position, item.TargetMonth, item.DueDate, item.RecurrenceRule, item.RecurrenceEndDate, false, nil, &lower, true)
		if err != nil {
			t.Fatalf("UpdateItem: %v", err)
		}
		if updated.TargetPrice == nil || *updated.TargetPrice != lower {
			t.Fatalf("expected target_price %v, got %v", lower, updated.TargetPrice)
		}

		cleared, err := d.UpdateItem(ctx, item.ID, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL,
			item.Done, item.Position, item.TargetMonth, item.DueDate, item.RecurrenceRule, item.RecurrenceEndDate, false, nil, nil, false)
		if err != nil {
			t.Fatalf("UpdateItem (clearing): %v", err)
		}
		if cleared.TargetPrice != nil || cleared.AlertOnPriceDrop {
			t.Fatalf("expected target_price/alert_on_price_drop fully cleared, got %+v", cleared)
		}
	})
}

// TestReorderItems exercises db.ReorderItems: the happy path (positions end
// up matching the requested order, both by re-fetching the items and by
// checking ListItemsByList's own ordering), and the validation that rejects
// anything short of an exact permutation of the list's current items.
func TestReorderItems(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	itemA, err := d.CreateItem(ctx, list.ID, "Lait", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item A: %v", err)
	}
	itemB, err := d.CreateItem(ctx, list.ID, "Pain", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item B: %v", err)
	}
	itemC, err := d.CreateItem(ctx, list.ID, "Beurre", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item C: %v", err)
	}

	t.Run("reorders and persists new positions", func(t *testing.T) {
		reordered, err := d.ReorderItems(ctx, list.ID, []int64{itemC.ID, itemA.ID, itemB.ID})
		if err != nil {
			t.Fatalf("ReorderItems: %v", err)
		}
		if len(reordered) != 3 || reordered[0].ID != itemC.ID || reordered[1].ID != itemA.ID || reordered[2].ID != itemB.ID {
			t.Fatalf("unexpected order in response: %+v", reordered)
		}

		fromScratch, err := d.ListItemsByList(ctx, list.ID)
		if err != nil {
			t.Fatalf("ListItemsByList: %v", err)
		}
		if len(fromScratch) != 3 || fromScratch[0].ID != itemC.ID || fromScratch[1].ID != itemA.ID || fromScratch[2].ID != itemB.ID {
			t.Fatalf("unexpected persisted order: %+v", fromScratch)
		}
		if fromScratch[0].Position != 0 || fromScratch[1].Position != 1 || fromScratch[2].Position != 2 {
			t.Fatalf("expected sequential 0-based positions, got %d/%d/%d", fromScratch[0].Position, fromScratch[1].Position, fromScratch[2].Position)
		}
	})

	t.Run("rejects a partial list", func(t *testing.T) {
		if _, err := d.ReorderItems(ctx, list.ID, []int64{itemA.ID, itemB.ID}); !errors.Is(err, ErrInvalidReorder) {
			t.Fatalf("expected ErrInvalidReorder for a partial list, got %v", err)
		}
	})

	t.Run("rejects a duplicated id", func(t *testing.T) {
		if _, err := d.ReorderItems(ctx, list.ID, []int64{itemA.ID, itemA.ID, itemB.ID}); !errors.Is(err, ErrInvalidReorder) {
			t.Fatalf("expected ErrInvalidReorder for a duplicated id, got %v", err)
		}
	})

	t.Run("rejects an id from a different list", func(t *testing.T) {
		otherList, err := d.CreateList(ctx, "Autre liste", "shopping", house.ID, nil, "")
		if err != nil {
			t.Fatalf("creating other list: %v", err)
		}
		foreignItem, err := d.CreateItem(ctx, otherList.ID, "Intrus", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating foreign item: %v", err)
		}
		if _, err := d.ReorderItems(ctx, list.ID, []int64{itemA.ID, itemB.ID, foreignItem.ID}); !errors.Is(err, ErrInvalidReorder) {
			t.Fatalf("expected ErrInvalidReorder for a foreign item id, got %v", err)
		}
	})
}

func TestItemLabelsPersistence(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	t.Run("a freshly created item has an empty, non-nil label set", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Article", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if item.Labels == nil || len(item.Labels) != 0 {
			t.Fatalf("expected an empty, non-nil label set, got %#v", item.Labels)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got.Labels == nil || len(got.Labels) != 0 {
			t.Fatalf("expected an empty, non-nil label set on re-fetch, got %#v", got.Labels)
		}
	})

	t.Run("SetItemLabels persists and survives a re-fetch", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Fromage", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		updated, err := d.SetItemLabels(ctx, item.ID, []string{"Bio", "Promo"})
		if err != nil {
			t.Fatalf("SetItemLabels: %v", err)
		}
		if !reflect.DeepEqual(updated.Labels, []string{"Bio", "Promo"}) {
			t.Fatalf("expected labels [Bio Promo], got %v", updated.Labels)
		}
		got, err := d.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if !reflect.DeepEqual(got.Labels, []string{"Bio", "Promo"}) {
			t.Fatalf("expected labels to survive a re-fetch, got %v", got.Labels)
		}
	})

	t.Run("SetItemLabels can clear the label set back to empty", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Pain", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if _, err := d.SetItemLabels(ctx, item.ID, []string{"Bio"}); err != nil {
			t.Fatalf("SetItemLabels: %v", err)
		}
		cleared, err := d.SetItemLabels(ctx, item.ID, nil)
		if err != nil {
			t.Fatalf("SetItemLabels(nil): %v", err)
		}
		if cleared.Labels == nil || len(cleared.Labels) != 0 {
			t.Fatalf("expected an empty, non-nil label set after clearing, got %#v", cleared.Labels)
		}
	})

	t.Run("UpdateItem does not clobber a previously set label set", func(t *testing.T) {
		item, err := d.CreateItem(ctx, list.ID, "Oeufs", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating item: %v", err)
		}
		if _, err := d.SetItemLabels(ctx, item.ID, []string{"Bio"}); err != nil {
			t.Fatalf("SetItemLabels: %v", err)
		}
		updated, err := d.UpdateItem(ctx, item.ID, "Oeufs bio", item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL,
			item.Done, item.Position, item.TargetMonth, item.DueDate, item.RecurrenceRule, item.RecurrenceEndDate, item.IsUrgent, nil, nil, false)
		if err != nil {
			t.Fatalf("UpdateItem: %v", err)
		}
		if !reflect.DeepEqual(updated.Labels, []string{"Bio"}) {
			t.Fatalf("expected UpdateItem to leave labels untouched, got %v", updated.Labels)
		}
	})

	t.Run("SetItemLabels returns ErrNotFound for a nonexistent item", func(t *testing.T) {
		if _, err := d.SetItemLabels(ctx, 999999, []string{"Bio"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
}

func mustCreateUser(t *testing.T, ctx context.Context, d *DB) int64 {
	t.Helper()
	hash := "x"
	user, err := d.CreateUser(ctx, "owner@example.com", &hash, nil, nil, "Owner")
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	return user.ID
}
