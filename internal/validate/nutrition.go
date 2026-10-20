package validate

import (
	"errors"
	"strings"
)

// ErrInvalidNutriScore is returned when a Nutri-Score isn't a grade from A
// to E.
var ErrInvalidNutriScore = errors.New("nutri_score must be one of A, B, C, D, E")

// ErrInvalidYukaScore is returned when a Yuka score is outside 0-100.
var ErrInvalidYukaScore = errors.New("yuka_score must be between 0 and 100")

// NutriScore validates a Nutri-Score grade and returns it upper-case, the
// form items.nutri_score/house_products.nutri_score store: "a" and " B "
// are accepted, anything but A to E refused. An empty input is valid and
// returns "" (no score).
func NutriScore(raw string) (string, error) {
	grade := strings.ToUpper(strings.TrimSpace(raw))
	switch grade {
	case "", "A", "B", "C", "D", "E":
		return grade, nil
	}
	return "", ErrInvalidNutriScore
}

// YukaScore validates a Yuka score (0 to 100).
func YukaScore(score int) error {
	if score < 0 || score > 100 {
		return ErrInvalidYukaScore
	}
	return nil
}
