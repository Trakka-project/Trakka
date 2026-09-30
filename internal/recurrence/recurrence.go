// Package recurrence parses, normalizes and steps the recurrence rules of
// recurring items (models.Item.RecurrenceRule).
//
// Rules are stored as a subset of the iCalendar RRULE syntax (RFC 5545
// §3.3.10), normalized to one canonical spelling:
//
//	FREQ=DAILY | FREQ=WEEKLY | FREQ=MONTHLY | FREQ=YEARLY
//	[;INTERVAL=n]          n in 1..MaxInterval, omitted when 1
//	[;BYDAY=MO,WE,FR]      FREQ=WEEKLY only, days in MO→SU order
//
// The forms stored before this syntax existed (DAILY, WEEKLY, MONTHLY,
// YEARLY, EVERY_X_DAYS:<n>, EVERY_X_MONTHS:<n>) are still accepted on input
// and normalized, and migration 22 rewrote stored values the same way.
// Anything else — COUNT/UNTIL (items carry recurrence_end_date instead),
// BYMONTHDAY, ordinal BYDAY values such as 1MO — is rejected rather than
// silently ignored.
//
// static/js/recurrence.js is a hand-kept JS port of Parse/String/After/Next,
// shared by the page scripts and the service worker: any change here must
// be mirrored there.
package recurrence

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxInterval caps INTERVAL (and the legacy EVERY_X_* counts). It keeps
// time.AddDate far inside four-digit years, where YYYY-MM-DD dates still
// compare correctly as strings, and is well past any real chore.
const MaxInterval = 999

// Freq is a rule's base period.
type Freq string

const (
	Daily   Freq = "DAILY"
	Weekly  Freq = "WEEKLY"
	Monthly Freq = "MONTHLY"
	Yearly  Freq = "YEARLY"
)

// ErrInvalid is returned for any rule this package doesn't accept.
var ErrInvalid = errors.New("recurrence_rule must be an RRULE such as FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR (FREQ DAILY/WEEKLY/MONTHLY/YEARLY, INTERVAL 1-999, BYDAY only with FREQ=WEEKLY)")

// Rule is a parsed recurrence rule.
type Rule struct {
	Freq     Freq
	Interval int
	// ByDay lists the weekdays a WEEKLY rule falls on, sorted Monday first
	// (weeks start on Monday, RRULE's default WKST). Empty means "the
	// weekday of the occurrence being stepped from".
	ByDay []time.Weekday
}

// weekdayCodes maps RRULE day codes to time.Weekday.
var weekdayCodes = map[string]time.Weekday{
	"MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday,
	"FR": time.Friday, "SA": time.Saturday, "SU": time.Sunday,
}

var legacyEveryX = regexp.MustCompile(`^EVERY_X_(DAYS|MONTHS):([1-9][0-9]*)$`)

// Parse reads a rule in the canonical RRULE form, any equivalent RRULE
// spelling (case, part order, an "RRULE:" prefix, INTERVAL=1, unsorted or
// repeated BYDAY), or a legacy form. The empty string is not a rule; callers
// treat it as "not recurring" before getting here.
func Parse(raw string) (Rule, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "RRULE:")

	switch s {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
		return Rule{Freq: Freq(s), Interval: 1}, nil
	}
	if m := legacyEveryX.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil || n > MaxInterval {
			return Rule{}, ErrInvalid
		}
		if m[1] == "MONTHS" {
			return Rule{Freq: Monthly, Interval: n}, nil
		}
		// A whole number of weeks reads (and is shown) as weeks.
		if n%7 == 0 {
			return Rule{Freq: Weekly, Interval: n / 7}, nil
		}
		return Rule{Freq: Daily, Interval: n}, nil
	}

	rule := Rule{Interval: 1}
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok || value == "" || seen[key] {
			return Rule{}, ErrInvalid
		}
		seen[key] = true
		switch key {
		case "FREQ":
			switch Freq(value) {
			case Daily, Weekly, Monthly, Yearly:
				rule.Freq = Freq(value)
			default:
				return Rule{}, ErrInvalid
			}
		case "INTERVAL":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > MaxInterval {
				return Rule{}, ErrInvalid
			}
			rule.Interval = n
		case "BYDAY":
			present := map[time.Weekday]bool{}
			for _, code := range strings.Split(value, ",") {
				day, ok := weekdayCodes[code]
				if !ok {
					return Rule{}, ErrInvalid
				}
				present[day] = true
			}
			for _, day := range mondayFirst {
				if present[day] {
					rule.ByDay = append(rule.ByDay, day)
				}
			}
		default:
			return Rule{}, ErrInvalid
		}
	}
	if rule.Freq == "" || (len(rule.ByDay) > 0 && rule.Freq != Weekly) {
		return Rule{}, ErrInvalid
	}
	return rule, nil
}

var mondayFirst = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}

// String is the rule's canonical spelling — what gets stored.
func (r Rule) String() string {
	var b strings.Builder
	b.WriteString("FREQ=")
	b.WriteString(string(r.Freq))
	if r.Interval > 1 {
		fmt.Fprintf(&b, ";INTERVAL=%d", r.Interval)
	}
	if len(r.ByDay) > 0 {
		b.WriteString(";BYDAY=")
		for i, day := range r.ByDay {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(dayCode(day))
		}
	}
	return b.String()
}

// dayCodes is weekdayCodes' inverse, indexed by time.Weekday.
var dayCodes = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

func dayCode(day time.Weekday) string {
	return dayCodes[day]
}

// After returns the first occurrence strictly after d (a calendar date; its
// clock time is ignored and the result is at midnight in d's location):
//
//   - DAILY: d + Interval days.
//   - WEEKLY without BYDAY: d + Interval weeks.
//   - WEEKLY with BYDAY: the next listed day later in d's own week
//     (Monday–Sunday), otherwise the first listed day of the week Interval
//     weeks after d's. Stepping from occurrence to occurrence therefore
//     keeps an "every 2 weeks" rule on the same weeks; a task on Monday,
//     Wednesday and Friday completed on Friday comes back on Monday.
//   - MONTHLY/YEARLY: the same day of the month Interval months/years
//     later, clamped to the month's last day (January 31 → February 28),
//     rather than time.AddDate's roll-over into the next month.
func (r Rule) After(d time.Time) time.Time {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
	interval := r.Interval
	if interval < 1 {
		interval = 1
	}
	switch r.Freq {
	case Daily:
		return d.AddDate(0, 0, interval)
	case Weekly:
		if len(r.ByDay) == 0 {
			return d.AddDate(0, 0, 7*interval)
		}
		index := isoIndex(d.Weekday())
		for _, day := range r.ByDay {
			if i := isoIndex(day); i > index {
				return d.AddDate(0, 0, i-index)
			}
		}
		weekStart := d.AddDate(0, 0, -index)
		return weekStart.AddDate(0, 0, 7*interval+isoIndex(r.ByDay[0]))
	case Monthly:
		return addMonthsClamped(d, interval)
	default: // Yearly
		return addMonthsClamped(d, 12*interval)
	}
}

// maxCatchUpSteps bounds Next's catch-up loop: ten years of a DAILY rule.
// Only reachable for an occurrence left overdue for years.
const maxCatchUpSteps = 3660

// Next returns the occurrence that follows from (the date of the
// occurrence just completed) and falls strictly after today: it steps by
// the rule at least once, then keeps stepping until past today. Completing
// a task early therefore still advances from its own date (a weekly chore
// due Friday, done on Wednesday, comes back the following Friday), while
// one left overdue for several periods skips the missed occurrences
// instead of coming back already overdue. from and today are calendar
// dates at midnight in the same location, as After returns them.
func (r Rule) Next(from, today time.Time) time.Time {
	next := r.After(from)
	for i := 0; !next.After(today) && i < maxCatchUpSteps; i++ {
		next = r.After(next)
	}
	return next
}

// isoIndex numbers weekdays Monday=0 … Sunday=6.
func isoIndex(day time.Weekday) int {
	return (int(day) + 6) % 7
}

func addMonthsClamped(d time.Time, months int) time.Time {
	first := time.Date(d.Year(), d.Month()+time.Month(months), 1, 0, 0, 0, 0, d.Location())
	lastDay := first.AddDate(0, 1, -1).Day()
	day := d.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, d.Location())
}
