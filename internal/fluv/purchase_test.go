package fluv

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"trakka/internal/models"
)

func floatPtr(f float64) *float64 { return &f }

func TestAmountOf(t *testing.T) {
	cases := []struct {
		name   string
		item   models.Item
		amount int64
	}{
		{"estimate only", models.Item{Quantity: 2, Price: floatPtr(1.25)}, 250},
		{"price paid wins", models.Item{Quantity: 3, Price: floatPtr(1.25), ActualPrice: floatPtr(0.99)}, 297},
		{"quantity 0 counts as 1", models.Item{Quantity: 0, Price: floatPtr(4.2)}, 420},
		{"float rounding", models.Item{Quantity: 3, Price: floatPtr(0.1)}, 30},
		{"no price", models.Item{Quantity: 1}, 0},
		{"zero price", models.Item{Quantity: 1, Price: floatPtr(0)}, 0},
		{"below a cent", models.Item{Quantity: 1, Price: floatPtr(0.004)}, 0},
		{"negative", models.Item{Quantity: 1, Price: floatPtr(-3)}, 0},
		{"NaN", models.Item{Quantity: 1, Price: floatPtr(math.NaN())}, 0},
		{"absurd", models.Item{Quantity: 1, Price: floatPtr(1e300)}, 0},
	}
	for _, c := range cases {
		if got := amountOf(&c.item); got != c.amount {
			t.Errorf("%s: amountOf = %d, want %d", c.name, got, c.amount)
		}
	}
	if got := labelQuantity(&models.Item{Quantity: 12000}); got != maxQuantity {
		t.Errorf("labelQuantity(12000) = %d, want %d", got, maxQuantity)
	}
}

func TestCleanText(t *testing.T) {
	cases := map[string]string{
		"  Lait demi-écrémé  ":         "Lait demi-écrémé",
		"Pain\tcomplet\n":              "Pain complet",
		"evil\u202Egnp.exe":            "evilgnp.exe",
		"\uFEFFBOM":                    "BOM",
		"bad\xffutf8":                  "bad utf8",
		strings.Repeat("é", 120):       strings.Repeat("é", 100),
		strings.Repeat("a", 99) + " b": strings.Repeat("a", 99),
	}
	for in, want := range cases {
		if got := cleanText(in, maxTitleRunes); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := titleOf(&models.Item{Title: "\u200E"}); got != "Trakka" {
		t.Errorf("titleOf an invisible title = %q, want the fallback", got)
	}
}

func TestNewRefMatchesFluvPattern(t *testing.T) {
	pattern := regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	a, err := newRef(9223372036854775807)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newRef(9223372036854775807)
	if !pattern.MatchString(a) || a == b {
		t.Errorf("refs %q, %q: want two distinct references within Fluv's pattern", a, b)
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := backoff(maxAttempts); got != maxBackoff {
		t.Errorf("backoff(%d) = %v, want the cap %v", maxAttempts, got, maxBackoff)
	}
}
