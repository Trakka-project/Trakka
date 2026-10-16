package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"trakka/internal/catalog"
)

// productsFixture returns a fresh DB with one House (and its owner).
func productsFixture(t *testing.T) (context.Context, *DB, int64) {
	t.Helper()
	ctx := context.Background()
	d := openTestDB(t)
	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	return ctx, d, house.ID
}

func TestRecordProductAddedLearnsAisleAndSpelling(t *testing.T) {
	ctx, d, houseID := productsFixture(t)

	// A guessed aisle fills a gap...
	if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: "lait", Key: "lait", Aisle: "dairy"}); err != nil {
		t.Fatalf("RecordProductAdded: %v", err)
	}
	// ...but never overrides the product's known aisle...
	if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: "Lait", Key: "lait", Aisle: "drinks"}); err != nil {
		t.Fatalf("RecordProductAdded: %v", err)
	}
	p, err := d.GetProductByKey(ctx, houseID, "lait")
	if err != nil {
		t.Fatalf("GetProductByKey: %v", err)
	}
	if p.Aisle != "dairy" || p.AddCount != 2 || p.Name != "Lait" {
		t.Fatalf("after two additions: %+v, want aisle dairy, 2 additions, latest spelling", p)
	}
	// ...while an explicit choice replaces it.
	if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: "Lait", Key: "lait", Aisle: "breakfast", AisleExplicit: true}); err != nil {
		t.Fatalf("RecordProductAdded: %v", err)
	}
	if p, _ = d.GetProductByKey(ctx, houseID, "lait"); p.Aisle != "breakfast" {
		t.Fatalf("explicit aisle not learned: %+v", p)
	}
}

func TestProductBarcodesMoveBetweenProducts(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	add := func(name, key, barcode string) {
		t.Helper()
		if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: name, Key: key, Barcode: barcode}); err != nil {
			t.Fatalf("RecordProductAdded(%s): %v", name, err)
		}
	}
	add("Lait demi-écrémé Lactel", "lait demi ecreme lactel", "3428272950018")
	add("Lait", "lait", "3428272950025") // the six-pack

	// Rename by re-adding the first code under "Lait": it moves.
	add("Lait", "lait", "3428272950018")
	for _, code := range []string{"3428272950018", "3428272950025"} {
		p, err := d.GetProductByBarcode(ctx, houseID, code)
		if err != nil {
			t.Fatalf("GetProductByBarcode(%s): %v", code, err)
		}
		if p.Name != "Lait" {
			t.Fatalf("barcode %s filed under %q, want Lait", code, p.Name)
		}
	}

	// Another House doesn't see it.
	other, err := d.CreateHouseWithOwner(ctx, "Autre", mustCreateUserWithEmail(t, ctx, d, "other@example.com"))
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	if _, err := d.GetProductByBarcode(ctx, other.ID, "3428272950018"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other house lookup: %v, want ErrNotFound", err)
	}

	// Forgetting the product drops its barcodes.
	p, _ := d.GetProductByKey(ctx, houseID, "lait")
	if err := d.DeleteProduct(ctx, houseID, p.ID); err != nil {
		t.Fatalf("DeleteProduct: %v", err)
	}
	if _, err := d.GetProductByBarcode(ctx, houseID, "3428272950025"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("barcode survived its product: %v", err)
	}
}

func TestRecordProductPurchased(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	last := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	// Unknown to the catalog: created by the purchase itself.
	if err := d.RecordProductPurchased(ctx, houseID, "Beurre", "beurre", "dairy", first); err != nil {
		t.Fatalf("RecordProductPurchased: %v", err)
	}
	if err := d.RecordProductPurchased(ctx, houseID, "beurre", "beurre", "", last); err != nil {
		t.Fatalf("RecordProductPurchased: %v", err)
	}
	p, err := d.GetProductByKey(ctx, houseID, "beurre")
	if err != nil {
		t.Fatalf("GetProductByKey: %v", err)
	}
	if p.PurchaseCount != 2 || p.Aisle != "dairy" || p.Name != "Beurre" {
		t.Fatalf("got %+v", p)
	}
	if p.FirstPurchasedAt == nil || *p.FirstPurchasedAt != "2026-09-01T10:00:00.000Z" ||
		p.LastPurchasedAt == nil || *p.LastPurchasedAt != "2026-09-15T10:00:00.000Z" {
		t.Fatalf("purchase dates %v %v", p.FirstPurchasedAt, p.LastPurchasedAt)
	}

	candidates, err := d.ListPurchasedProducts(ctx, houseID, 2)
	if err != nil || len(candidates) != 1 || candidates[0].ID != p.ID {
		t.Fatalf("ListPurchasedProducts = %v, %v", candidates, err)
	}
	if candidates, _ = d.ListPurchasedProducts(ctx, houseID, 3); len(candidates) != 0 {
		t.Fatalf("min 3 purchases: %v", candidates)
	}
}

func TestSearchProductsRanking(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	for _, name := range []string{"Chocolat noir", "Lait de coco", "Lait", "Pain au lait", "Lait d'amande", "Salade"} {
		if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: name, Key: catalog.Key(name)}); err != nil {
			t.Fatalf("RecordProductAdded: %v", err)
		}
	}
	// "Lait d'amande" is a favorite: first among the names starting with "lait".
	fav := true
	if _, err := d.UpsertProduct(ctx, houseID, "Lait d'amande", catalog.Key("Lait d'amande"), &fav, nil); err != nil {
		t.Fatalf("UpsertProduct: %v", err)
	}

	got, err := d.SearchProducts(ctx, houseID, "lait", 10)
	if err != nil {
		t.Fatalf("SearchProducts: %v", err)
	}
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	want := []string{"Lait d'amande", "Lait", "Lait de coco", "Pain au lait"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}

	// LIKE wildcards in the query are literal.
	if got, _ := d.SearchProducts(ctx, houseID, "%", 10); len(got) != 0 {
		t.Fatalf("%% matched %d products", len(got))
	}
	if got, _ := d.SearchProducts(ctx, houseID, "", 10); len(got) != 0 {
		t.Fatalf("empty query matched %d products", len(got))
	}
}

func TestUpsertAndUpdateProduct(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	fav := true
	aisle := "frozen"
	p, err := d.UpsertProduct(ctx, houseID, "Glace vanille", "glace vanille", &fav, &aisle)
	if err != nil {
		t.Fatalf("UpsertProduct: %v", err)
	}
	if !p.IsFavorite || p.Aisle != "frozen" {
		t.Fatalf("created %+v", p)
	}

	// nil fields are left alone; "" clears the aisle.
	empty := ""
	p, err = d.UpdateProduct(ctx, houseID, p.ID, nil, &empty)
	if err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	if !p.IsFavorite || p.Aisle != "" {
		t.Fatalf("after clearing the aisle: %+v", p)
	}
	unfav := false
	if p, _ = d.UpdateProduct(ctx, houseID, p.ID, &unfav, nil); p.IsFavorite {
		t.Fatalf("still a favorite: %+v", p)
	}
	favorites, err := d.ListFavoriteProducts(ctx, houseID)
	if err != nil || len(favorites) != 0 {
		t.Fatalf("ListFavoriteProducts = %v, %v", favorites, err)
	}

	// Scoped by House.
	other, _ := d.CreateHouseWithOwner(ctx, "Autre", mustCreateUserWithEmail(t, ctx, d, "other@example.com"))
	if _, err := d.UpdateProduct(ctx, other.ID, p.ID, &fav, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-house update: %v", err)
	}
	if err := d.DeleteProduct(ctx, other.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-house delete: %v", err)
	}
}

func TestReactivateItem(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	list, err := d.CreateList(ctx, "Courses", "groceries", houseID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	item, err := d.CreateItem(ctx, list.ID, "Lait", nil, 3, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := d.conn.ExecContext(ctx, `UPDATE items SET done = 1, next_due_date = '2026-10-20' WHERE id = ?`, item.ID); err != nil {
		t.Fatalf("checking off: %v", err)
	}
	dairy := "dairy"
	got, err := d.ReactivateItem(ctx, item.ID, 1, &dairy, true)
	if err != nil {
		t.Fatalf("ReactivateItem: %v", err)
	}
	if got.Done || got.NextDueDate != nil || got.Quantity != 1 || got.Aisle == nil || *got.Aisle != "dairy" || !got.IsUrgent {
		t.Fatalf("reactivated: %+v", got)
	}

	// An existing aisle is kept; urgent is never turned off.
	drinks := "drinks"
	if got, _ = d.ReactivateItem(ctx, item.ID, 2, &drinks, false); *got.Aisle != "dairy" || !got.IsUrgent || got.Quantity != 2 {
		t.Fatalf("second reactivation: %+v", got)
	}
	if _, err := d.ReactivateItem(ctx, 999999, 1, nil, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
}

// TestSeedHouseProductsMigration rewinds to version 30 and replays
// migration 31 over pre-existing groceries items.
func TestSeedHouseProductsMigration(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	groceries, err := d.CreateList(ctx, "Courses", "groceries", houseID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	todo, err := d.CreateList(ctx, "Tâches", "todo", houseID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	insert := func(listID int64, title string, done int, created, updated string) int64 {
		t.Helper()
		res, err := d.conn.ExecContext(ctx,
			`INSERT INTO items (list_id, title, done, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			listID, title, done, created, updated)
		if err != nil {
			t.Fatalf("inserting %q: %v", title, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	milkOld := insert(groceries.ID, "lait", 1, "2026-08-01T08:00:00.000Z", "2026-08-02T08:00:00.000Z")
	milkNew := insert(groceries.ID, "Lait", 1, "2026-08-10T08:00:00.000Z", "2026-08-12T08:00:00.000Z")
	unknown := insert(groceries.ID, "Cadeau Léa", 0, "2026-08-11T08:00:00.000Z", "2026-08-11T08:00:00.000Z")
	chore := insert(todo.ID, "Lessive", 0, "2026-08-11T08:00:00.000Z", "2026-08-11T08:00:00.000Z")

	undoMigrationsSince32(t, d.conn)
	if _, err := d.conn.Exec(`PRAGMA user_version = 30`); err != nil {
		t.Fatalf("rewinding user_version: %v", err)
	}
	if err := migrateSchema(d.conn, discardLogger(), func(int, int) error { return nil }); err != nil {
		t.Fatalf("migrateSchema: %v", err)
	}

	p, err := d.GetProductByKey(ctx, houseID, "lait")
	if err != nil {
		t.Fatalf("GetProductByKey: %v", err)
	}
	if p.Name != "Lait" || p.AddCount != 2 || p.PurchaseCount != 2 || p.Aisle != "dairy" ||
		*p.FirstPurchasedAt != "2026-08-02T08:00:00.000Z" || *p.LastPurchasedAt != "2026-08-12T08:00:00.000Z" {
		t.Fatalf("seeded %+v", p)
	}
	if _, err := d.GetProductByKey(ctx, houseID, "lessive"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a todo item was seeded: %v", err)
	}
	if p, err := d.GetProductByKey(ctx, houseID, "cadeau lea"); err != nil || p.PurchaseCount != 0 || p.Aisle != "" {
		t.Fatalf("unrecognized item: %+v, %v", p, err)
	}

	for id, want := range map[int64]string{milkOld: "dairy", milkNew: "dairy", unknown: "", chore: ""} {
		item, err := d.GetItem(ctx, id)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if got := stringValueOrEmpty(item.Aisle); got != want {
			t.Errorf("item %q aisle %q, want %q", item.Title, got, want)
		}
		if id == milkNew && item.UpdatedAt != "2026-08-12T08:00:00.000Z" {
			t.Errorf("backfill moved updated_at to %s", item.UpdatedAt)
		}
	}
}
