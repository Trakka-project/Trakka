package backup

import "time"

// Frequencies an automatic backup schedule can use.
const (
	FrequencyEvery12h = "every_12h"
	FrequencyDaily    = "daily"
	FrequencyWeekly   = "weekly"
)

// schedule describes when automatic backups are due, as wall-clock times in
// the instance's APP_TIMEZONE (the same zone task reminders use):
//
//   - every_12h: at Time and 12 hours later, every day (03:00 → 03:00 and
//     15:00);
//   - daily: at Time, every day;
//   - weekly: at Time, on Weekday (0 = Sunday … 6 = Saturday).
//
// Slots are built with time.Date in that zone rather than by adding fixed
// durations, so they stay on the configured wall-clock time across DST
// changes (time.Date normalizes a time skipped by a spring-forward jump).
type schedule struct {
	Frequency string
	Hour      int
	Minute    int
	Weekday   time.Weekday
}

func (s schedule) at(day time.Time, hour int, loc *time.Location) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, hour, s.Minute, 0, 0, loc)
}

// candidates returns the slot times on the calendar day of `day` (in loc).
func (s schedule) candidates(day time.Time, loc *time.Location) []time.Time {
	switch s.Frequency {
	case FrequencyEvery12h:
		h := s.Hour % 12
		return []time.Time{s.at(day, h, loc), s.at(day, h+12, loc)}
	case FrequencyWeekly:
		if day.Weekday() != s.Weekday {
			return nil
		}
		return []time.Time{s.at(day, s.Hour, loc)}
	default: // FrequencyDaily
		return []time.Time{s.at(day, s.Hour, loc)}
	}
}

// latestSlot returns the most recent scheduled time at or before now.
func (s schedule) latestSlot(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	y, m, d := local.Date()
	for back := 0; back <= 8; back++ {
		day := time.Date(y, m, d-back, 12, 0, 0, 0, loc)
		slots := s.candidates(day, loc)
		for i := len(slots) - 1; i >= 0; i-- {
			if !slots[i].After(now) {
				return slots[i]
			}
		}
	}
	return time.Time{} // unreachable for a valid schedule
}

// nextSlot returns the first scheduled time strictly after now.
func (s schedule) nextSlot(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	y, m, d := local.Date()
	for ahead := 0; ahead <= 8; ahead++ {
		day := time.Date(y, m, d+ahead, 12, 0, 0, 0, loc)
		for _, slot := range s.candidates(day, loc) {
			if slot.After(now) {
				return slot
			}
		}
	}
	return time.Time{} // unreachable for a valid schedule
}

// period is the nominal spacing between two slots.
func (s schedule) period() time.Duration {
	switch s.Frequency {
	case FrequencyEvery12h:
		return 12 * time.Hour
	case FrequencyWeekly:
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// staleAfter is how long automatic backups may go without a single success
// before the admin console raises its "no recent backup" alert: at least 3
// days (so one bad night, or a weekend of the WebDAV server being down,
// doesn't cry wolf), and for a weekly schedule one full missed week plus
// two days of slack.
func (s schedule) staleAfter() time.Duration {
	return max(3*24*time.Hour, s.period()+2*24*time.Hour)
}
