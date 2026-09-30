package validate

import (
	"strings"

	"trakka/internal/recurrence"
)

// ErrInvalidRecurrenceRule is returned when a user-supplied recurrence rule
// isn't one internal/recurrence accepts.
var ErrInvalidRecurrenceRule = recurrence.ErrInvalid

// Recurrence validates a user-supplied recurrence rule and returns its
// canonical spelling (see internal/recurrence), e.g.
// "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR". An empty (or whitespace-only)
// input is valid and returns "" with no error, meaning "not recurring". The
// legacy forms (DAILY, EVERY_X_DAYS:<n>, ...) are accepted and come back in
// the canonical RRULE form, so only that form is ever stored.
func Recurrence(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	rule, err := recurrence.Parse(raw)
	if err != nil {
		return "", ErrInvalidRecurrenceRule
	}
	return rule.String(), nil
}
