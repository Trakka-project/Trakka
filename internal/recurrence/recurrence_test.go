package recurrence

import (
	"testing"
	"time"
)

func TestParseNormalizes(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"FREQ=WEEKLY;BYDAY=MO,WE,FR", "FREQ=WEEKLY;BYDAY=MO,WE,FR"},
		{"rrule:byday=fr,mo,we,mo;freq=weekly", "FREQ=WEEKLY;BYDAY=MO,WE,FR"},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=TU", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU"},
		{"FREQ=DAILY;INTERVAL=1", "FREQ=DAILY"},
		{" FREQ=MONTHLY;INTERVAL=3 ", "FREQ=MONTHLY;INTERVAL=3"},
		{"FREQ=YEARLY", "FREQ=YEARLY"},
		// Legacy forms.
		{"daily", "FREQ=DAILY"},
		{"WEEKLY", "FREQ=WEEKLY"},
		{"MONTHLY", "FREQ=MONTHLY"},
		{"YEARLY", "FREQ=YEARLY"},
		{"EVERY_X_DAYS:3", "FREQ=DAILY;INTERVAL=3"},
		{"EVERY_X_DAYS:7", "FREQ=WEEKLY"},
		{"EVERY_X_DAYS:14", "FREQ=WEEKLY;INTERVAL=2"},
		{"EVERY_X_MONTHS:6", "FREQ=MONTHLY;INTERVAL=6"},
	}
	for _, tc := range cases {
		rule, err := Parse(tc.raw)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", tc.raw, err)
			continue
		}
		if got := rule.String(); got != tc.want {
			t.Errorf("Parse(%q).String() = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, raw := range []string{
		"",
		"FORTNIGHTLY",
		"FREQ=HOURLY",
		"INTERVAL=2",                         // no FREQ
		"FREQ=DAILY;INTERVAL=0",              // below range
		"FREQ=DAILY;INTERVAL=1000",           // above MaxInterval
		"FREQ=DAILY;INTERVAL=x",              // not a number
		"FREQ=MONTHLY;BYDAY=MO",              // BYDAY is weekly-only
		"FREQ=WEEKLY;BYDAY=1MO",              // ordinal days unsupported
		"FREQ=WEEKLY;BYDAY=",                 // empty day list
		"FREQ=WEEKLY;BYDAY=MO,XX",            // unknown day
		"FREQ=WEEKLY;COUNT=5",                // unsupported part
		"FREQ=WEEKLY;FREQ=DAILY",             // repeated part
		"FREQ=WEEKLY;",                       // empty part
		"EVERY_X_DAYS:0",                     // legacy, zero
		"EVERY_X_DAYS:1000",                  // legacy, above the cap
		"FREQ=WEEKLY;UNTIL=20261231T000000Z", // end date lives in recurrence_end_date
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q): expected an error", raw)
		}
	}
}

func date(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestAfter(t *testing.T) {
	cases := []struct {
		name string
		rule string
		from string
		want string
	}{
		{"daily", "FREQ=DAILY", "2026-01-31", "2026-02-01"},
		{"every 3 days", "FREQ=DAILY;INTERVAL=3", "2026-01-01", "2026-01-04"},
		{"weekly", "FREQ=WEEKLY", "2026-10-02", "2026-10-09"},
		{"every 2 weeks", "FREQ=WEEKLY;INTERVAL=2", "2026-10-02", "2026-10-16"},
		// 2026-10-02 is a Friday: Mon/Wed/Fri comes back on Monday.
		{"Mon/Wed/Fri from Friday", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-02", "2026-10-05"},
		{"Mon/Wed/Fri from Monday", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-05", "2026-10-07"},
		{"Mon/Wed/Fri from Saturday", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-03", "2026-10-05"},
		{"Mon/Wed/Fri from Tuesday (off-pattern)", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-06", "2026-10-07"},
		{"every 2 weeks Mon/Fri, mid-week", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR", "2026-10-05", "2026-10-09"},
		{"every 2 weeks Mon/Fri, skips a week", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR", "2026-10-09", "2026-10-19"},
		{"weekdays from Friday", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", "2026-10-02", "2026-10-05"},
		{"Sunday only from Sunday", "FREQ=WEEKLY;BYDAY=SU", "2026-10-04", "2026-10-11"},
		{"monthly", "FREQ=MONTHLY", "2026-12-15", "2027-01-15"},
		{"monthly clamps to month end", "FREQ=MONTHLY", "2026-01-31", "2026-02-28"},
		{"monthly clamps in a leap year", "FREQ=MONTHLY", "2028-01-31", "2028-02-29"},
		{"every 3 months", "FREQ=MONTHLY;INTERVAL=3", "2026-11-30", "2027-02-28"},
		{"yearly", "FREQ=YEARLY", "2026-02-28", "2027-02-28"},
		{"yearly from Feb 29", "FREQ=YEARLY", "2028-02-29", "2029-02-28"},
		{"every 2 years", "FREQ=YEARLY;INTERVAL=2", "2026-06-01", "2028-06-01"},
		// Legacy spellings step exactly like their canonical form.
		{"legacy yearly", "YEARLY", "2026-02-28", "2027-02-28"},
		{"legacy every x days", "EVERY_X_DAYS:10", "2026-01-01", "2026-01-11"},
		{"legacy every x months", "EVERY_X_MONTHS:6", "2026-11-30", "2027-05-30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, err := Parse(tc.rule)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.rule, err)
			}
			if got := rule.After(date(tc.from)).Format("2006-01-02"); got != tc.want {
				t.Fatalf("%s after %s = %s, want %s", tc.rule, tc.from, got, tc.want)
			}
		})
	}
}

func TestNext(t *testing.T) {
	cases := []struct {
		name  string
		rule  string
		from  string
		today string
		want  string
	}{
		{"completed on its date", "FREQ=WEEKLY", "2026-01-05", "2026-01-05", "2026-01-12"},
		{"completed early steps from its own date", "FREQ=WEEKLY", "2026-01-09", "2026-01-05", "2026-01-16"},
		{"overdue skips the missed occurrences", "FREQ=WEEKLY", "2025-12-01", "2026-01-05", "2026-01-12"},
		{"overdue BYDAY rule lands on the next listed day", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-09-28", "2026-10-01", "2026-10-02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, err := Parse(tc.rule)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.rule, err)
			}
			if got := rule.Next(date(tc.from), date(tc.today)).Format("2006-01-02"); got != tc.want {
				t.Fatalf("%s next after %s (today %s) = %s, want %s", tc.rule, tc.from, tc.today, got, tc.want)
			}
		})
	}
}

// TestAfterKeepsLocalMidnight guards the DST case: stepping a date in a zone
// with a DST change inside the step must still land on local midnight.
func TestAfterKeepsLocalMidnight(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	rule, _ := Parse("FREQ=WEEKLY;BYDAY=MO")
	got := rule.After(time.Date(2026, 10, 21, 0, 0, 0, 0, paris)) // DST ends 2026-10-25
	want := time.Date(2026, 10, 26, 0, 0, 0, 0, paris)
	if !got.Equal(want) {
		t.Fatalf("After = %v, want %v", got, want)
	}
}
