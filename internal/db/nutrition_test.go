package db

import (
	"context"
	"database/sql"
	"testing"
)

// undoMigration39 drops what migration 39 added, for the tests that rewind
// user_version below it and replay every later migration.
func undoMigration39(t *testing.T, conn *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`ALTER TABLE items DROP COLUMN nutri_score`,
		`ALTER TABLE items DROP COLUMN yuka_score`,
		`ALTER TABLE house_products DROP COLUMN nutri_score`,
		`ALTER TABLE house_products DROP COLUMN yuka_score`,
		`ALTER TABLE users DROP COLUMN nutrition_scores_mobile`,
		`ALTER TABLE users DROP COLUMN nutrition_scores_web`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("undoing migration 39 (%s): %v", stmt, err)
		}
	}
}

func intPtr(v int) *int { return &v }

func TestSetItemNutrition(t *testing.T) {
	ctx, d, houseID := productsFixture(t)
	list, err := d.CreateList(ctx, "Courses", "groceries", houseID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	item, err := d.CreateItem(ctx, list.ID, "Nutella", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if item.NutriScore != nil || item.YukaScore != nil {
		t.Fatalf("a new item has no scores, got %v / %v", item.NutriScore, item.YukaScore)
	}

	grade := "E"
	item, err = d.SetItemNutrition(ctx, item.ID, &grade, intPtr(12))
	if err != nil {
		t.Fatalf("SetItemNutrition: %v", err)
	}
	if item.NutriScore == nil || *item.NutriScore != "E" || item.YukaScore == nil || *item.YukaScore != 12 {
		t.Fatalf("scores = %v / %v, want E / 12", item.NutriScore, item.YukaScore)
	}
	// Read back by the list query too.
	items, err := d.ListItemsByList(ctx, list.ID)
	if err != nil || len(items) != 1 || *items[0].NutriScore != "E" || *items[0].YukaScore != 12 {
		t.Fatalf("ListItemsByList = %+v, %v", items, err)
	}

	if item, err = d.SetItemNutrition(ctx, item.ID, nil, nil); err != nil || item.NutriScore != nil || item.YukaScore != nil {
		t.Fatalf("clearing: %+v, %v", item, err)
	}

	// The CHECK constraints back the handler's validation.
	bad := "F"
	if _, err := d.SetItemNutrition(ctx, item.ID, &bad, nil); err == nil {
		t.Fatal("nutri_score F accepted")
	}
	if _, err := d.SetItemNutrition(ctx, item.ID, nil, intPtr(101)); err == nil {
		t.Fatal("yuka_score 101 accepted")
	}
	if _, err := d.SetItemNutrition(ctx, item.ID+1000, nil, nil); err != ErrNotFound {
		t.Fatalf("unknown item: %v, want ErrNotFound", err)
	}
}

func TestProductNutritionIsLearned(t *testing.T) {
	ctx, d, houseID := productsFixture(t)

	// An addition with scores teaches them...
	if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: "Nutella", Key: "nutella", NutriScore: "E", YukaScore: intPtr(10)}); err != nil {
		t.Fatalf("RecordProductAdded: %v", err)
	}
	// ...one without leaves them as they were...
	if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: "nutella", Key: "nutella"}); err != nil {
		t.Fatalf("RecordProductAdded: %v", err)
	}
	p, err := d.GetProductByKey(ctx, houseID, "nutella")
	if err != nil || p.NutriScore != "E" || p.YukaScore == nil || *p.YukaScore != 10 {
		t.Fatalf("product = %+v, %v; want E / 10 kept", p, err)
	}
	// ...a known one replaces the product's, an unknown one keeps it.
	if err := d.RecordProductAdded(ctx, ProductAddition{HouseID: houseID, Name: "Nutella", Key: "nutella", NutriScore: "D"}); err != nil {
		t.Fatalf("RecordProductAdded: %v", err)
	}
	if p, _ = d.GetProductByKey(ctx, houseID, "nutella"); p.NutriScore != "D" || p.YukaScore == nil || *p.YukaScore != 10 {
		t.Fatalf("product = %+v; want D / 10", p)
	}

	// SetProductNutrition sets both, clearing included, creating the
	// product if needed.
	if err := d.SetProductNutrition(ctx, houseID, "Nutella", "nutella", "", nil); err != nil {
		t.Fatalf("SetProductNutrition: %v", err)
	}
	if p, _ = d.GetProductByKey(ctx, houseID, "nutella"); p.NutriScore != "" || p.YukaScore != nil {
		t.Fatalf("product = %+v; want scores cleared", p)
	}
	if err := d.SetProductNutrition(ctx, houseID, "Pommes", "pommes", "A", intPtr(95)); err != nil {
		t.Fatalf("SetProductNutrition: %v", err)
	}
	if p, err = d.GetProductByKey(ctx, houseID, "pommes"); err != nil || p.NutriScore != "A" || *p.YukaScore != 95 {
		t.Fatalf("new product = %+v, %v", p, err)
	}
}

func TestUserNutritionScoreDisplay(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	user, err := d.GetUser(ctx, mustCreateUser(t, ctx, d))
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	// Off by default, everywhere.
	if user.NutritionScoresMobile || user.NutritionScoresWeb {
		t.Fatalf("new account shows nutrition scores: %+v", user)
	}

	on := true
	updated, err := d.UpdateUserNutritionScoreDisplay(ctx, user.ID, &on, nil)
	if err != nil || !updated.NutritionScoresMobile || updated.NutritionScoresWeb {
		t.Fatalf("mobile on: %+v, %v", updated, err)
	}
	off := false
	updated, err = d.UpdateUserNutritionScoreDisplay(ctx, user.ID, &off, &on)
	if err != nil || updated.NutritionScoresMobile || !updated.NutritionScoresWeb {
		t.Fatalf("mobile off, web on: %+v, %v", updated, err)
	}
	if _, err := d.UpdateUserNutritionScoreDisplay(ctx, user.ID+1000, &on, nil); err != ErrNotFound {
		t.Fatalf("unknown user: %v, want ErrNotFound", err)
	}
}
