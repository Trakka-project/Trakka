package validate

import (
	"errors"
	"testing"
)

func TestNutriScore(t *testing.T) {
	valid := map[string]string{"": "", "  ": "", "A": "A", "b": "B", " c ": "C", "D": "D", "e": "E"}
	for in, want := range valid {
		got, err := NutriScore(in)
		if err != nil || got != want {
			t.Errorf("NutriScore(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"F", "AB", "1", "unknown", "not-applicable", "é"} {
		if _, err := NutriScore(in); !errors.Is(err, ErrInvalidNutriScore) {
			t.Errorf("NutriScore(%q) error = %v, want ErrInvalidNutriScore", in, err)
		}
	}
}

func TestYukaScore(t *testing.T) {
	for _, in := range []int{0, 1, 50, 99, 100} {
		if err := YukaScore(in); err != nil {
			t.Errorf("YukaScore(%d) = %v, want nil", in, err)
		}
	}
	for _, in := range []int{-1, 101, 1000} {
		if err := YukaScore(in); !errors.Is(err, ErrInvalidYukaScore) {
			t.Errorf("YukaScore(%d) = %v, want ErrInvalidYukaScore", in, err)
		}
	}
}
