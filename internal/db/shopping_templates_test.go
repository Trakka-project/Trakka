package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"trakka/internal/models"
)

// undoMigrationsSince32 drops what migrations 32 to 34 added (33's column
// goes with 32's shopping_template_items table), for the tests that rewind
// user_version below them and replay every later migration.
func undoMigrationsSince32(t *testing.T, conn *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`DROP TABLE item_assignees`,
		`DROP TABLE shopping_template_items`,
		`DROP TABLE shopping_templates`,
		`ALTER TABLE items DROP COLUMN is_planned`,
		`ALTER TABLE items DROP COLUMN actual_price`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("undoing migrations 32 to 34 (%s): %v", stmt, err)
		}
	}
}

func floatPtr(v float64) *float64 { return &v }

// budgetItem inserts an item straight into a list with the given price,
// quantity, done flag and actual price.
func budgetItem(t *testing.T, ctx context.Context, d *DB, listID int64, title string, price *float64, quantity int, done bool, actual *float64) {
	t.Helper()
	if _, err := d.conn.ExecContext(ctx,
		`INSERT INTO items (list_id, title, price, quantity, done, actual_price) VALUES (?, ?, ?, ?, ?, ?)`,
		listID, title, price, quantity, boolToInt(done), actual); err != nil {
		t.Fatalf("inserting %q: %v", title, err)
	}
}

// TestListBudgetAggregation checks listSelect's budget sums end to end:
// the planned budget counts every estimate × quantity, spending counts
// bought items at their actual price (or their estimate when none was
// entered), and the variance only compares what was bought.
func TestListBudgetAggregation(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	list, err := d.CreateList(ctx, "Rentrée", "groceries", houseID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if list.Budget != nil {
		t.Fatalf("an empty list has no budget, got %+v", list.Budget)
	}

	// Not bought yet: 6 × 1.50 + 39.90 = 48.90 remaining.
	budgetItem(t, ctx, d, list.ID, "Cahiers", floatPtr(1.5), 6, false, nil)
	budgetItem(t, ctx, d, list.ID, "Cartable", floatPtr(39.9), 1, false, nil)
	// Bought dearer than planned: 2 × 3.50 estimated, 2 × 4.20 paid.
	budgetItem(t, ctx, d, list.ID, "Classeur", floatPtr(3.5), 2, true, floatPtr(4.2))
	// Bought without entering a price: taken at its estimate.
	budgetItem(t, ctx, d, list.ID, "Trousse", floatPtr(7.9), 1, true, nil)
	// Bought cheaper than planned.
	budgetItem(t, ctx, d, list.ID, "Calculatrice", floatPtr(19.9), 1, true, floatPtr(15))
	// Bought without any estimate: unplanned spending.
	budgetItem(t, ctx, d, list.ID, "Gourde", nil, 1, true, floatPtr(10))
	// Neither estimated nor bought: counts for nothing.
	budgetItem(t, ctx, d, list.ID, "Agenda", nil, 1, false, nil)

	got, err := d.GetList(ctx, list.ID)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	// estimated: 48.90 + 7 + 7.90 + 19.90 = 83.70
	// spent:     8.40 + 7.90 + 15 + 10   = 41.30
	// variance:  41.30 − (7 + 7.90 + 19.90) = 6.50
	want := models.ListBudget{
		EstimatedTotal: 83.7,
		SpentTotal:     41.3,
		RemainingTotal: 48.9,
		ProjectedTotal: 90.2,
		Variance:       6.5,
		VarianceStatus: "over",
	}
	if got.Budget == nil || *got.Budget != want {
		t.Fatalf("budget = %+v, want %+v", got.Budget, want)
	}
	if got.TotalAmount == nil || *got.TotalAmount != 48.9 {
		t.Errorf("total_amount = %v, want the remaining 48.9", got.TotalAmount)
	}

	// The index read goes through the same SELECT.
	lists, err := d.ListListsForUser(ctx, 1, "", houseID)
	if err != nil || len(lists) != 1 || lists[0].Budget == nil || *lists[0].Budget != want {
		t.Fatalf("ListListsForUser budget = %+v (err %v), want %+v", lists, err, want)
	}
}

// The purchase columns round-trip, and purchase_status is derived from
// done and is_planned on every read.
func TestSetItemPurchase(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	list, err := d.CreateList(ctx, "Courses", "groceries", houseID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	item, err := d.CreateItem(ctx, list.ID, "Lessive", nil, 1, floatPtr(11.9), false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if item.PurchaseStatus != models.PurchaseToBuy || item.ActualPrice != nil {
		t.Fatalf("a new item is to buy with no actual price, got %q / %v", item.PurchaseStatus, item.ActualPrice)
	}
	item, err = d.SetItemPurchase(ctx, item.ID, true, nil)
	if err != nil || item.PurchaseStatus != models.PurchasePlanned {
		t.Fatalf("SetItemPurchase(planned) = %+v, %v", item, err)
	}
	if _, err := d.conn.ExecContext(ctx, `UPDATE items SET done = 1 WHERE id = ?`, item.ID); err != nil {
		t.Fatal(err)
	}
	item, err = d.SetItemPurchase(ctx, item.ID, false, floatPtr(12.5))
	if err != nil || item.PurchaseStatus != models.PurchaseBought || item.ActualPrice == nil || *item.ActualPrice != 12.5 {
		t.Fatalf("SetItemPurchase(bought, 12.5) = %+v, %v", item, err)
	}
	if _, err := d.SetItemPurchase(ctx, 999999, false, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetItemPurchase on a missing item: %v, want ErrNotFound", err)
	}
}

// Every write that takes an item out of "bought" outside the item
// handlers — bringing a product back on a Courses list, a recurring item's
// next occurrence — starts a new purchase: no paid price, not planned.
func TestLeavingBoughtClearsActualPrice(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	list, err := d.CreateList(ctx, "Courses", "groceries", houseID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}

	reactivated, err := d.CreateItem(ctx, list.ID, "Lait", nil, 1, floatPtr(1.1), false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := d.conn.ExecContext(ctx, `UPDATE items SET done = 1, actual_price = 1.3, is_planned = 1 WHERE id = ?`, reactivated.ID); err != nil {
		t.Fatal(err)
	}
	item, err := d.ReactivateItem(ctx, reactivated.ID, 1, nil, false)
	if err != nil {
		t.Fatalf("ReactivateItem: %v", err)
	}
	if item.PurchaseStatus != models.PurchaseToBuy || item.ActualPrice != nil {
		t.Errorf("ReactivateItem: status %q, actual %v; want to_buy, nil", item.PurchaseStatus, item.ActualPrice)
	}

	recurring, err := d.CreateItem(ctx, list.ID, "Abonnement", nil, 1, floatPtr(9.99), false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := d.conn.ExecContext(ctx,
		`UPDATE items SET done = 1, actual_price = 10.99, recurrence_rule = 'FREQ=MONTHLY', is_recurring = 1, next_due_date = '2026-11-01' WHERE id = ?`,
		recurring.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ActivateNextOccurrence(ctx, recurring.ID, "2026-11-01"); err != nil || !ok {
		t.Fatalf("ActivateNextOccurrence = %v, %v", ok, err)
	}
	item, err = d.GetItem(ctx, recurring.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.PurchaseStatus != models.PurchaseToBuy || item.ActualPrice != nil {
		t.Errorf("next occurrence: status %q, actual %v; want to_buy, nil", item.PurchaseStatus, item.ActualPrice)
	}
}

func TestShoppingTemplatesCRUD(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	ownerID := int64(1)
	otherUser := mustCreateUserWithEmail(t, ctx, d, "other@example.com")
	otherHouse, err := d.CreateHouseWithOwner(ctx, "Ailleurs", otherUser)
	if err != nil {
		t.Fatalf("creating other house: %v", err)
	}

	items := []*models.ShoppingTemplateItem{
		{Title: "Cartons", Quantity: 20, EstimatedPrice: floatPtr(2.5), Aisle: "diy"},
		{Title: "Scotch", Quantity: 4},
	}
	tmpl, err := d.CreateShoppingTemplate(ctx, houseID, "Déménagement", "📦", "groceries", ownerID, items)
	if err != nil {
		t.Fatalf("CreateShoppingTemplate: %v", err)
	}
	if tmpl.ID == 0 || tmpl.HouseID != houseID || tmpl.Builtin || tmpl.CreatedBy == nil || *tmpl.CreatedBy != ownerID {
		t.Fatalf("unexpected template %+v", tmpl)
	}
	if len(tmpl.Items) != 2 || tmpl.Items[0].Title != "Cartons" || tmpl.Items[1].EstimatedPrice != nil || tmpl.Items[1].Aisle != "" {
		t.Fatalf("items not stored in order with their fields: %+v %+v", tmpl.Items[0], tmpl.Items[1])
	}
	if _, err := d.CreateShoppingTemplate(ctx, otherHouse.ID, "Autre", "", "shopping", otherUser, items[:1]); err != nil {
		t.Fatalf("CreateShoppingTemplate (other house): %v", err)
	}

	// A member sees their House's templates only, with items.
	listed, err := d.ListShoppingTemplatesForUser(ctx, ownerID, 0)
	if err != nil || len(listed) != 1 || listed[0].ID != tmpl.ID || len(listed[0].Items) != 2 {
		t.Fatalf("ListShoppingTemplatesForUser = %+v, %v", listed, err)
	}
	// Asking for another House's yields nothing, not its templates.
	if listed, err := d.ListShoppingTemplatesForUser(ctx, ownerID, otherHouse.ID); err != nil || len(listed) != 0 {
		t.Fatalf("ListShoppingTemplatesForUser(other house) = %+v, %v", listed, err)
	}
	if n, err := d.CountShoppingTemplates(ctx, houseID); err != nil || n != 1 {
		t.Fatalf("CountShoppingTemplates = %d, %v", n, err)
	}

	if err := d.DeleteShoppingTemplate(ctx, tmpl.ID); err != nil {
		t.Fatalf("DeleteShoppingTemplate: %v", err)
	}
	if _, err := d.GetShoppingTemplate(ctx, tmpl.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetShoppingTemplate after delete: %v, want ErrNotFound", err)
	}
	var orphans int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM shopping_template_items WHERE template_id = ?`, tmpl.ID).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("template items not cascaded: %d left (%v)", orphans, err)
	}
	if err := d.DeleteShoppingTemplate(ctx, tmpl.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v, want ErrNotFound", err)
	}
}

// CreateListFromTemplate copies every template item — title, quantity,
// estimate as price, aisle, order — into a new list, planned, in one go.
func TestCreateListFromTemplate(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	items := []*models.ShoppingTemplateItem{
		{Title: "Cahiers", Quantity: 6, EstimatedPrice: floatPtr(1.5), Aisle: "stationery"},
		{Title: "Calculatrice", Quantity: 1, EstimatedPrice: floatPtr(19.9), Aisle: "electronics"},
		{Title: "Sans prix", Quantity: 0},
	}
	list, err := d.CreateListFromTemplate(ctx, "Rentrée", "groceries", houseID, nil, "🎒", items, true)
	if err != nil {
		t.Fatalf("CreateListFromTemplate: %v", err)
	}
	if list.Name != "Rentrée" || list.Type != "groceries" || list.Icon != "🎒" {
		t.Fatalf("unexpected list %+v", list)
	}
	if list.Budget == nil || list.Budget.EstimatedTotal != 28.9 || list.Budget.SpentTotal != 0 || list.Budget.VarianceStatus != "on_budget" {
		t.Fatalf("budget of a fresh template list = %+v", list.Budget)
	}

	got, err := d.ListItemsByList(ctx, list.ID)
	if err != nil {
		t.Fatalf("ListItemsByList: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	for i, item := range got {
		if item.Title != items[i].Title || item.Position != i || item.PurchaseStatus != models.PurchasePlanned || item.Done || item.ReminderEnabled {
			t.Errorf("item %d = %+v", i, item)
		}
	}
	if *got[0].Price != 1.5 || got[0].Quantity != 6 || *got[0].Aisle != "stationery" {
		t.Errorf("first item fields not copied: %+v", got[0])
	}
	if got[2].Price != nil || got[2].Aisle != nil || got[2].Quantity != 1 {
		t.Errorf("an item without price/aisle/quantity should get nil, nil, 1: %+v", got[2])
	}
}

// A task template (migration 33) keeps each item's recurrence rule, and a
// list created from it gets them back on its items — recurring, with no due
// date yet, and not planned since a todo list has no purchase cycle.
func TestTaskTemplateRecurrence(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	items := []*models.ShoppingTemplateItem{
		{Title: "Laver les vitres", Quantity: 1, RecurrenceRule: "FREQ=MONTHLY"},
		{Title: "Relever les compteurs", Quantity: 1},
	}
	tmpl, err := d.CreateShoppingTemplate(ctx, houseID, "Maison", "🧹", "todo", 1, items)
	if err != nil {
		t.Fatalf("CreateShoppingTemplate: %v", err)
	}
	if tmpl.ListType != "todo" || tmpl.Items[0].RecurrenceRule != "FREQ=MONTHLY" || tmpl.Items[1].RecurrenceRule != "" {
		t.Fatalf("recurrence not stored: %+v / %+v %+v", tmpl, tmpl.Items[0], tmpl.Items[1])
	}
	listed, err := d.ListShoppingTemplatesForUser(ctx, 1, houseID)
	if err != nil || len(listed) != 1 || listed[0].Items[0].RecurrenceRule != "FREQ=MONTHLY" {
		t.Fatalf("ListShoppingTemplatesForUser = %+v, %v", listed, err)
	}

	list, err := d.CreateListFromTemplate(ctx, "Ménage", "todo", houseID, nil, "🧹", tmpl.Items, false)
	if err != nil {
		t.Fatalf("CreateListFromTemplate: %v", err)
	}
	if list.Type != "todo" || list.Budget != nil {
		t.Fatalf("a task list from a template = %+v (budget %+v)", list, list.Budget)
	}
	got, err := d.ListItemsByList(ctx, list.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListItemsByList = %d items, %v", len(got), err)
	}
	if !got[0].IsRecurring || got[0].RecurrenceRule == nil || *got[0].RecurrenceRule != "FREQ=MONTHLY" || got[0].DueDate != nil {
		t.Errorf("recurring task not copied as such: %+v", got[0])
	}
	if got[1].IsRecurring || got[1].RecurrenceRule != nil {
		t.Errorf("one-off task copied as recurring: %+v", got[1])
	}
	for _, item := range got {
		if item.PurchaseStatus != models.PurchaseToBuy || item.Done || item.ReminderEnabled || item.Price != nil || item.Aisle != nil {
			t.Errorf("task %q = %+v", item.Title, item)
		}
	}
}
