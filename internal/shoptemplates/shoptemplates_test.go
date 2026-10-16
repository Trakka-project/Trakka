package shoptemplates

import (
	"testing"

	"trakka/internal/catalog"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// The templates the feature promises, in picker order: the five purchase
// templates, then the three task templates.
func TestBuiltinsOrder(t *testing.T) {
	want := []struct{ key, listType string }{
		{KeyCleaning, "groceries"},
		{KeyTravel, "groceries"},
		{KeyMoving, "groceries"},
		{KeyBackToSchool, "groceries"},
		{KeyHomeMaintenance, "groceries"},
		{KeyCleaningTasks, "todo"},
		{KeyMovingTasks, "todo"},
		{KeyBackToSchoolTasks, "todo"},
	}
	got := Builtins("fr")
	if len(got) != len(want) {
		t.Fatalf("got %d built-in templates, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Key != w.key || got[i].ListType != w.listType {
			t.Errorf("template %d: %s (%s), want %s (%s)", i, got[i].Key, got[i].ListType, w.key, w.listType)
		}
	}
}

// Every template, in every language, must be something POST /api/v1/lists
// and POST /api/v1/items would accept as is: a valid list type, valid
// lengths, and no item twice — a "Courses" list never holds the same
// product twice, and a checklist has no use for a duplicate. A purchase
// template's items have a known aisle, a sane quantity and price and no
// recurrence; a task template's have none of the first three, and a
// recurrence rule (if any) in the canonical spelling stored rules have.
func TestBuiltinsAreValid(t *testing.T) {
	for lang := range validate.SupportedLanguages {
		for _, tmpl := range Builtins(lang) {
			if !tmpl.Builtin || tmpl.ID != 0 || tmpl.Key == "" {
				t.Errorf("%s/%s: not identified as a built-in by key", lang, tmpl.Key)
			}
			if tmpl.Name == "" || !validate.MaxLen(tmpl.Name, validate.MaxNameLen) {
				t.Errorf("%s/%s: invalid name %q", lang, tmpl.Key, tmpl.Name)
			}
			if tmpl.Icon == "" || !validate.MaxLen(tmpl.Icon, validate.MaxIconLen) {
				t.Errorf("%s/%s: invalid icon %q", lang, tmpl.Key, tmpl.Icon)
			}
			if !models.ValidListTypes[tmpl.ListType] {
				t.Errorf("%s/%s: invalid list type %q", lang, tmpl.Key, tmpl.ListType)
			}
			if len(tmpl.Items) < 10 {
				t.Errorf("%s/%s: only %d items", lang, tmpl.Key, len(tmpl.Items))
			}
			purchase := models.IsPurchaseListType(tmpl.ListType)
			seen := map[string]bool{}
			for _, it := range tmpl.Items {
				if it.Title == "" || validate.Text(it.Title) != it.Title || !validate.MaxLen(it.Title, validate.MaxTitleLen) {
					t.Errorf("%s/%s: invalid title %q", lang, tmpl.Key, it.Title)
				}
				key := catalog.Key(it.Title)
				if seen[key] {
					t.Errorf("%s/%s: %q appears twice", lang, tmpl.Key, it.Title)
				}
				seen[key] = true
				if it.Quantity < 1 || it.Quantity > 100 {
					t.Errorf("%s/%s: %q has quantity %d", lang, tmpl.Key, it.Title, it.Quantity)
				}
				if purchase {
					if !catalog.IsAisle(it.Aisle) {
						t.Errorf("%s/%s: %q has unknown aisle %q", lang, tmpl.Key, it.Title, it.Aisle)
					}
					if it.EstimatedPrice == nil || *it.EstimatedPrice <= 0 || *it.EstimatedPrice > 1000 {
						t.Errorf("%s/%s: %q has estimated price %v", lang, tmpl.Key, it.Title, it.EstimatedPrice)
					}
					if it.RecurrenceRule != "" {
						t.Errorf("%s/%s: %q repeats (%s)", lang, tmpl.Key, it.Title, it.RecurrenceRule)
					}
					continue
				}
				if it.Aisle != "" || it.EstimatedPrice != nil || it.Quantity != 1 {
					t.Errorf("%s/%s: task %q has an aisle, a price or a quantity", lang, tmpl.Key, it.Title)
				}
				if canonical, err := validate.Recurrence(it.RecurrenceRule); err != nil || canonical != it.RecurrenceRule {
					t.Errorf("%s/%s: %q has rule %q, canonical %q (%v)", lang, tmpl.Key, it.Title, it.RecurrenceRule, canonical, err)
				}
			}
			if !purchase && tmpl.EstimatedTotal != 0 {
				t.Errorf("%s/%s: a task template has an estimated total of %v", lang, tmpl.Key, tmpl.EstimatedTotal)
			}
		}
	}
}

// The cleaning chores repeat; the moving and paperwork checklists are
// one-off tasks.
func TestTaskTemplatesRecurrence(t *testing.T) {
	recurring := func(key string) (n int) {
		tmpl, _ := Builtin(key, "fr")
		for _, it := range tmpl.Items {
			if it.RecurrenceRule != "" {
				n++
			}
		}
		return n
	}
	chores, _ := Builtin(KeyCleaningTasks, "fr")
	if n := recurring(KeyCleaningTasks); n != len(chores.Items) {
		t.Errorf("%d of %d cleaning chores repeat, want all", n, len(chores.Items))
	}
	for _, key := range []string{KeyMovingTasks, KeyBackToSchoolTasks} {
		if n := recurring(key); n != 0 {
			t.Errorf("%s: %d tasks repeat, want none", key, n)
		}
	}
}

// Both languages describe the same items, in the same order: only the
// words differ.
func TestBuiltinsTranslationsMatch(t *testing.T) {
	fr, en := Builtins("fr"), Builtins("en")
	for i := range fr {
		if fr[i].Name == en[i].Name {
			t.Errorf("%s: name %q is not translated", fr[i].Key, fr[i].Name)
		}
		if len(fr[i].Items) != len(en[i].Items) || fr[i].EstimatedTotal != en[i].EstimatedTotal || fr[i].ListType != en[i].ListType {
			t.Errorf("%s: fr and en differ in items, total or list type", fr[i].Key)
			continue
		}
		for j := range fr[i].Items {
			if fr[i].Items[j].RecurrenceRule != en[i].Items[j].RecurrenceRule {
				t.Errorf("%s: item %d repeats differently in fr and en", fr[i].Key, j)
			}
		}
	}
}

func TestBuiltinLookupAndFallback(t *testing.T) {
	tmpl, ok := Builtin(KeyBackToSchool, "en")
	if !ok || tmpl.Name != "Back to School" {
		t.Fatalf("Builtin(back_to_school, en) = %+v, %v", tmpl, ok)
	}
	if tmpl, _ := Builtin(KeyBackToSchool, "de"); tmpl.Name != "Rentrée scolaire" {
		t.Errorf("an unsupported language should fall back to French, got %q", tmpl.Name)
	}
	if _, ok := Builtin("nope", "fr"); ok {
		t.Error("an unknown key should not be found")
	}
	// Each call hands out its own copy: a caller editing one can't change
	// the next caller's template.
	a, _ := Builtin(KeyCleaning, "fr")
	*a.Items[0].EstimatedPrice = 999
	a.Items[0].Title = "changed"
	if b, _ := Builtin(KeyCleaning, "fr"); *b.Items[0].EstimatedPrice == 999 || b.Items[0].Title == "changed" {
		t.Error("Builtin returned shared state")
	}
}

func TestFillTotals(t *testing.T) {
	price := func(v float64) *float64 { return &v }
	tmpl := &models.ShoppingTemplate{Items: []*models.ShoppingTemplateItem{
		{Title: "Cahiers", Quantity: 6, EstimatedPrice: price(1.5)},
		{Title: "Colle", Quantity: 3, EstimatedPrice: price(0.1)},
		{Title: "Sans prix", Quantity: 2},
		{Title: "Quantité nulle", Quantity: 0, EstimatedPrice: price(2)},
	}}
	FillTotals(tmpl)
	if tmpl.ItemCount != 4 || tmpl.EstimatedTotal != 11.3 {
		t.Fatalf("FillTotals: count %d, total %v; want 4, 11.3", tmpl.ItemCount, tmpl.EstimatedTotal)
	}
}
