package db

import (
	"context"
	"testing"
)

func setupPriceAlertItem(t *testing.T, ctx context.Context, d *DB, price float64) (itemID int64, url string) {
	t.Helper()
	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}
	url = "https://example.com/product"
	item, err := d.CreateItem(ctx, list.ID, "Article", &url, 1, &price, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	return item.ID, url
}

// TestCreatePriceAlertIfNonePending exercises the guard the periodic scan
// relies on to avoid spawning a duplicate alert for an item that already
// has one pending.
func TestCreatePriceAlertIfNonePending(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	itemID, url := setupPriceAlertItem(t, ctx, d, 20.0)

	if created, err := d.CreatePriceAlertIfNonePending(ctx, itemID, 20.0, 15.0, url); err != nil || !created {
		t.Fatalf("CreatePriceAlertIfNonePending = %v, %v; want created", created, err)
	}
	alert, err := d.GetPendingPriceAlertForItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetPendingPriceAlertForItem: %v", err)
	}
	if alert.FoundPrice != 15.0 || alert.OriginalPrice != 20.0 {
		t.Fatalf("unexpected alert %+v", alert)
	}

	// A second, even lower price found while the first alert is still
	// pending must not create a duplicate row.
	if created, err := d.CreatePriceAlertIfNonePending(ctx, itemID, 20.0, 12.0, url); err != nil || created {
		t.Fatalf("CreatePriceAlertIfNonePending (second) = %v, %v; want not created", created, err)
	}
	alerts, err := d.ListPriceAlertsByHouse(ctx, mustHouseIDForItem(t, ctx, d, itemID), "")
	if err != nil {
		t.Fatalf("ListPriceAlertsByHouse: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected exactly one alert to survive, got %d", len(alerts))
	}
	if alerts[0].FoundPrice != 15.0 {
		t.Fatalf("expected the original (first) found_price 15.0 to survive, got %v", alerts[0].FoundPrice)
	}
}

// TestAcceptPriceAlert exercises the accept path: the item's price is
// updated to found_price, price_auto is set, the alert flips to accepted,
// and a second accept/reject attempt on the same alert is rejected as
// ErrNotFound (already resolved).
func TestAcceptPriceAlert(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	itemID, url := setupPriceAlertItem(t, ctx, d, 20.0)

	if _, err := d.CreatePriceAlertIfNonePending(ctx, itemID, 20.0, 15.0, url); err != nil {
		t.Fatalf("CreatePriceAlertIfNonePending: %v", err)
	}
	alert, err := d.GetPendingPriceAlertForItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetPendingPriceAlertForItem: %v", err)
	}

	accepted, err := d.AcceptPriceAlert(ctx, alert.ID)
	if err != nil {
		t.Fatalf("AcceptPriceAlert: %v", err)
	}
	if accepted.Status != "accepted" {
		t.Fatalf("expected status accepted, got %q", accepted.Status)
	}

	item, err := d.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Price == nil || *item.Price != 15.0 {
		t.Fatalf("expected item price to become 15.0, got %v", item.Price)
	}
	if !item.PriceAuto {
		t.Fatal("expected price_auto to be true after accepting a price alert")
	}

	if _, err := d.AcceptPriceAlert(ctx, alert.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound re-accepting an already-resolved alert, got %v", err)
	}
	if _, err := d.RejectPriceAlert(ctx, alert.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound rejecting an already-resolved alert, got %v", err)
	}
}

// TestRejectPriceAlert exercises the reject path: the alert flips to
// rejected and the item's price is left untouched.
func TestRejectPriceAlert(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	itemID, url := setupPriceAlertItem(t, ctx, d, 20.0)

	if _, err := d.CreatePriceAlertIfNonePending(ctx, itemID, 20.0, 15.0, url); err != nil {
		t.Fatalf("CreatePriceAlertIfNonePending: %v", err)
	}
	alert, err := d.GetPendingPriceAlertForItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetPendingPriceAlertForItem: %v", err)
	}

	rejected, err := d.RejectPriceAlert(ctx, alert.ID)
	if err != nil {
		t.Fatalf("RejectPriceAlert: %v", err)
	}
	if rejected.Status != "rejected" {
		t.Fatalf("expected status rejected, got %q", rejected.Status)
	}

	item, err := d.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Price == nil || *item.Price != 20.0 {
		t.Fatalf("expected item price to remain 20.0, got %v", item.Price)
	}
}

// TestListItemsForPriceScan exercises the eligibility filter the periodic
// scan relies on: not-done items with a url, with or without a price yet,
// and without an active target price (those are RunTargetPriceScan's).
func TestListItemsForPriceScan(t *testing.T) {
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
	price := 10.0
	target := 8.0

	eligible, err := d.CreateItem(ctx, list.ID, "Éligible", &url, 1, &price, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating eligible item: %v", err)
	}
	if _, err := d.CreateItem(ctx, list.ID, "Sans URL", nil, 1, &price, false, 0, nil, nil, nil, nil, false, nil, nil, false); err != nil {
		t.Fatalf("creating url-less item: %v", err)
	}
	priceless, err := d.CreateItem(ctx, list.ID, "Sans prix", &url, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating price-less item: %v", err)
	}
	// A target price without the opt-in is not an active target price.
	inactiveTarget, err := d.CreateItem(ctx, list.ID, "Seuil inactif", &url, 1, &price, false, 0, nil, nil, nil, nil, false, nil, &target, false)
	if err != nil {
		t.Fatalf("creating inactive-target item: %v", err)
	}
	if _, err := d.CreateItem(ctx, list.ID, "Seuil actif", &url, 1, &price, false, 0, nil, nil, nil, nil, false, nil, &target, true); err != nil {
		t.Fatalf("creating active-target item: %v", err)
	}
	done, err := d.CreateItem(ctx, list.ID, "Terminé", &url, 1, &price, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("creating done item: %v", err)
	}
	if _, err := d.UpdateItem(ctx, done.ID, done.Title, done.URL, done.Quantity, done.Price, done.PriceAuto, done.ImageURL,
		true, done.Position, done.TargetMonth, done.DueDate, done.RecurrenceRule, done.RecurrenceEndDate, false, done.RecurrenceLeadMinutes, nil, false); err != nil {
		t.Fatalf("marking item done: %v", err)
	}

	items, err := d.ListItemsForPriceScan(ctx)
	if err != nil {
		t.Fatalf("ListItemsForPriceScan: %v", err)
	}
	got := map[int64]bool{}
	for _, it := range items {
		got[it.ID] = true
	}
	if len(items) != 3 || !got[eligible.ID] || !got[priceless.ID] || !got[inactiveTarget.ID] {
		t.Fatalf("expected the eligible, price-less and inactive-target items, got %+v", items)
	}
}

func mustHouseIDForItem(t *testing.T, ctx context.Context, d *DB, itemID int64) int64 {
	t.Helper()
	item, err := d.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	list, err := d.GetList(ctx, item.ListID)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	return list.HouseID
}
