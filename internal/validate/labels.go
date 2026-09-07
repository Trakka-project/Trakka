package validate

import (
	"errors"
	"strings"
)

// Length/count ceilings for an item's freeform label set (see
// models.Item.Labels). Deliberately generous for the same reason as the
// MaxNameLen/MaxTitleLen/... ceilings in text.go — large enough that no
// realistic label set hits them, small enough that a single item can't be
// inflated into an unbounded blob of "labels".
const (
	MaxLabelLen      = 30 // one label, e.g. "Bio", "Promo"
	MaxLabelsPerItem = 20
)

// ErrLabelTooLong is returned when a single label exceeds MaxLabelLen after
// cleaning.
var ErrLabelTooLong = errors.New("a label is too long")

// ErrTooManyLabels is returned when an item's label set exceeds
// MaxLabelsPerItem after cleaning.
var ErrTooManyLabels = errors.New("an item can carry at most 20 labels")

// Labels normalizes a user-supplied label list: each label is run through
// Text (trims whitespace, strips control characters — the same cleaning
// every other free-text field in this package gets), empty results are
// dropped, and case-insensitive duplicates are removed, keeping the first
// occurrence's own casing/order — "Bio" and "bio" are the same label to a
// user tapping chips in the label management bottom sheet
// (static/js/list_view.js's openLabelManageSheet), even though that sheet's
// own suggestions are drawn from whatever casing already exists on other
// items in the list. Returns ErrLabelTooLong / ErrTooManyLabels if a single
// cleaned label exceeds MaxLabelLen or the cleaned set exceeds
// MaxLabelsPerItem. Never returns a nil slice on success — an empty or
// all-empty input becomes []string{}, matching models.Item.Labels' own
// "never nil in a response" contract.
func Labels(raw []string) ([]string, error) {
	cleaned := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, label := range raw {
		label = Text(label)
		if label == "" {
			continue
		}
		if !MaxLen(label, MaxLabelLen) {
			return nil, ErrLabelTooLong
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, label)
	}
	if len(cleaned) > MaxLabelsPerItem {
		return nil, ErrTooManyLabels
	}
	return cleaned, nil
}
