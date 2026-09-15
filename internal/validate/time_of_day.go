package validate

import (
	"errors"
	"strings"
	"time"
)

// ErrInvalidTimeOfDay is returned when a user-supplied wall-clock time is
// not in HH:MM (24h) format.
var ErrInvalidTimeOfDay = errors.New("time must be in HH:MM (24h) format")

// TimeOfDay trims and validates a user-supplied wall-clock time string (e.g.
// a reminder's time of day). An empty (or whitespace-only) input is valid
// and returns "" with no error, meaning "no explicit time set" — the same
// convention validate.Date already follows for an optional date. A
// non-empty input must be a real 24h HH:MM time.
func TimeOfDay(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if _, err := time.Parse("15:04", trimmed); err != nil {
		return "", ErrInvalidTimeOfDay
	}
	return trimmed, nil
}
