package catalog

import (
	"testing"
	"time"
)

func TestKey(t *testing.T) {
	cases := map[string]string{
		"Lait demi-écrémé":     "lait demi ecreme",
		"  LAIT  DEMI ECREME ": "lait demi ecreme",
		"Pâtes":                "pates",
		"pâté":                 "pate",
		"Œufs":                 "oeufs",
		"jus d'orange":         "jus d orange",
		"Jus d’orange (1L)":    "jus d orange 1l",
		"Crème fraîche 30%":    "creme fraiche 30",
		"Straße":               "strasse",
		"!!!":                  "",
		"":                     "",
		"Ørsted":               "orsted",
		"kéfir\tnature":        "kefir nature",
		"Yaourt à la fraise":   "yaourt a la fraise",
	}
	for in, want := range cases {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKeyKeepsPluralsApart guards the "no stemming" rule documented on Key:
// folded, "pâtes" and "pâté" differ only by their trailing s.
func TestKeyKeepsPluralsApart(t *testing.T) {
	if Key("pâtes") == Key("pâté") {
		t.Fatal("pâtes and pâté must not share a key")
	}
	if Key("Pomme") == Key("pommes") {
		t.Fatal("singular and plural are distinct products")
	}
}

func TestIsAisle(t *testing.T) {
	for _, a := range Aisles {
		if !IsAisle(a) {
			t.Errorf("IsAisle(%q) = false", a)
		}
	}
	for _, bad := range []string{"", "Produce", "fruits", "other "} {
		if IsAisle(bad) {
			t.Errorf("IsAisle(%q) = true", bad)
		}
	}
}

func TestGuessAisle(t *testing.T) {
	cases := map[string]string{
		"Tomates":                   AisleProduce,
		"pommes de terre":           AisleProduce,
		"Pommes":                    AisleProduce,
		"Haricots verts":            AisleProduce,
		"baguette tradition":        AisleBakery,
		"Pain de mie complet":       AisleBakery,
		"Lait demi-écrémé":          AisleDairy,
		"Œufs bio x12":              AisleDairy,
		"yaourt à la fraise":        AisleDairy,
		"strawberry yogurt":         AisleDairy,
		"Crème fraîche épaisse":     AisleDairy,
		"lait de coco":              AislePantry,
		"Sauce tomate":              AislePantry,
		"Pâtes":                     AislePantry,
		"spaghetti n°5":             AislePantry,
		"thon":                      AislePantry,
		"Café moulu":                AisleBreakfast,
		"thé":                       AisleBreakfast,
		"Thé vert":                  AisleBreakfast,
		"pâte à tartiner":           AisleBreakfast,
		"compote de pommes":         AisleBreakfast,
		"Jus d'orange":              AisleDrinks,
		"orange juice":              AisleDrinks,
		"eau gazeuse":               AisleDrinks,
		"Bières":                    AisleDrinks,
		"Eau de javel":              AisleHousehold,
		"Papier toilette":           AisleHousehold,
		"liquide vaisselle":         AisleHousehold,
		"sacs poubelle 30L":         AisleHousehold,
		"Gel douche":                AisleHygiene,
		"Dentifrice":                AisleHygiene,
		"crème solaire":             AisleHygiene,
		"Couches taille 4":          AisleBaby,
		"croquettes chat":           AislePets,
		"Pâtée pour chien":          AislePets,
		"Glace vanille":             AisleFrozen,
		"ice cream":                 AisleFrozen,
		"Poulet rôti":               AisleMeatFish,
		"blanc de poulet":           AisleMeatFish,
		"Lardons fumés":             AisleMeatFish,
		"Steak haché":               AisleMeatFish,
		"Cadeau pour Léa":           "",
		"":                          "",
		"pâté":                      "",
		"Piles AA":                  AisleHousehold,
		"Fromage râpé emmental":     AisleDairy,
		"Ice tea pêche":             AisleDrinks,
		"Glaçons":                   AisleFrozen,
		"Pommes de terre grenaille": AisleProduce,
	}
	for in, want := range cases {
		if got := GuessAisle(in); got != want {
			t.Errorf("GuessAisle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGuessAisleTablesUseValidKeysInKeyForm keeps the keyword tables honest:
// every value must be a real aisle, and every key must already be in the
// Key form lookups compare against, or it could never match.
func TestGuessAisleTablesUseValidKeysInKeyForm(t *testing.T) {
	tables := map[string]map[string]string{
		"exactAisles":     exactAisles,
		"phraseAisles":    phraseAisles,
		"productWords":    productWords,
		"ingredientWords": ingredientWords,
	}
	for name, table := range tables {
		for word, aisle := range table {
			if !IsAisle(aisle) {
				t.Errorf("%s[%q] = %q, not an aisle", name, word, aisle)
			}
			if Key(word) != word {
				t.Errorf("%s key %q is not in Key form (%q)", name, word, Key(word))
			}
		}
	}
}

func TestAisleFromCategories(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"en:dairies", "en:fermented-foods", "en:yogurts"}, AisleDairy},
		{[]string{"en:plant-based-foods-and-beverages", "en:vegetables", "en:frozen-foods"}, AisleFrozen},
		{[]string{"en:beverages", "en:coffees"}, AisleBreakfast},
		{[]string{"en:beverages", "en:waters"}, AisleDrinks},
		{[]string{"en:canned-foods", "en:vegetables"}, AislePantry},
		{[]string{"en:fresh-vegetables"}, AisleProduce},
		{[]string{"  EN:Breads "}, AisleBakery},
		{[]string{"en:unknown-thing"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := AisleFromCategories(c.tags); got != c.want {
			t.Errorf("AisleFromCategories(%v) = %q, want %q", c.tags, got, c.want)
		}
	}
}

func TestAssess(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	if _, ok := Assess(PurchaseHistory{Count: 1, First: now, Last: now}, now); ok {
		t.Fatal("one purchase has no interval")
	}

	// Bought 5 times over 28 days: every 7 days; last one 6 days ago.
	r, ok := Assess(PurchaseHistory{Count: 5, First: now.Add(-34 * day), Last: now.Add(-6 * day)}, now)
	if !ok {
		t.Fatal("expected an assessment")
	}
	if r.AvgIntervalDays != 7 || r.DaysSinceLast != 6 {
		t.Fatalf("avg %.2f since %.2f, want 7 and 6", r.AvgIntervalDays, r.DaysSinceLast)
	}
	if !r.Due {
		t.Fatalf("6 of 7 days (ratio %.2f) should be due", r.Ratio)
	}

	// Same rhythm, bought yesterday: not due.
	r, _ = Assess(PurchaseHistory{Count: 5, First: now.Add(-29 * day), Last: now.Add(-1 * day)}, now)
	if r.Due {
		t.Fatalf("ratio %.2f should not be due", r.Ratio)
	}

	// Two check-offs a minute apart don't make a once-a-minute product.
	r, _ = Assess(PurchaseHistory{Count: 2, First: now.Add(-2 * time.Minute), Last: now.Add(-time.Minute)}, now)
	if r.AvgIntervalDays != 1 || r.Due {
		t.Fatalf("burst purchases: avg %.4f due %v, want a one-day floor and not due", r.AvgIntervalDays, r.Due)
	}
}

func TestRankRepurchases(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	histories := []PurchaseHistory{
		0: {Count: 1, First: now.Add(-20 * day), Last: now.Add(-20 * day)}, // single purchase: left out
		1: {Count: 3, First: now.Add(-30 * day), Last: now.Add(-10 * day)}, // every 10 days, 10 ago: ratio 1
		2: {Count: 4, First: now.Add(-21 * day), Last: now.Add(-14 * day)}, // every ~2.3 days, 14 ago: ratio 6
		3: {Count: 10, First: now.Add(-90 * day), Last: now.Add(-1 * day)}, // frequent, not due
		4: {Count: 2, First: now.Add(-60 * day), Last: now.Add(-2 * day)},  // not due, fewer purchases
	}
	got := RankRepurchases(histories, now, 10)
	want := []int{2, 1, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, idx := range want {
		if got[i].Index != idx {
			t.Fatalf("position %d: index %d, want %d (full: %+v)", i, got[i].Index, idx, got)
		}
	}
	if !got[0].Due || !got[1].Due || got[2].Due || got[3].Due {
		t.Fatalf("due flags wrong: %+v", got)
	}

	if limited := RankRepurchases(histories, now, 2); len(limited) != 2 || limited[0].Index != 2 {
		t.Fatalf("limit 2: %+v", limited)
	}
}
