package validate

import (
	"errors"
	"testing"
)

func TestAisle(t *testing.T) {
	for in, want := range map[string]string{"": "", "   ": "", "dairy": "dairy", " frozen ": "frozen", "other": "other"} {
		got, err := Aisle(in)
		if err != nil || got != want {
			t.Errorf("Aisle(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"Dairy", "fruits", "produits frais", "dairy;drop"} {
		if _, err := Aisle(in); !errors.Is(err, ErrInvalidAisle) {
			t.Errorf("Aisle(%q) error = %v, want ErrInvalidAisle", in, err)
		}
	}
}

func TestBarcode(t *testing.T) {
	valid := map[string]string{
		"":                 "",
		"  ":               "",
		"3017620422003":    "3017620422003",
		"3017 6204 22003":  "3017620422003",
		"96385074":         "96385074",
		"036000291452":     "0036000291452", // UPC-A → EAN-13
		"00012345678905":   "00012345678905",
		" 4006381-333931 ": "4006381333931",
	}
	for in, want := range valid {
		got, err := Barcode(in)
		if err != nil || got != want {
			t.Errorf("Barcode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"1234567", "123456789012345", "30176204220a3", "../../etc", "--", "3017620422003?x=1"} {
		if _, err := Barcode(in); !errors.Is(err, ErrInvalidBarcode) {
			t.Errorf("Barcode(%q) error = %v, want ErrInvalidBarcode", in, err)
		}
	}
}
