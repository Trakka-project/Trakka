package backup

import (
	"net"
	"testing"
	"time"
)

func TestScheduleSlots(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no tzdata available:", err)
	}
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, paris)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}

	cases := []struct {
		name         string
		sched        schedule
		now          string
		latest, next string
	}{
		{"daily before slot", schedule{Frequency: FrequencyDaily, Hour: 3}, "2026-09-29 02:59", "2026-09-28 03:00", "2026-09-29 03:00"},
		{"daily on slot", schedule{Frequency: FrequencyDaily, Hour: 3}, "2026-09-29 03:00", "2026-09-29 03:00", "2026-09-30 03:00"},
		{"12h morning", schedule{Frequency: FrequencyEvery12h, Hour: 3}, "2026-09-29 10:00", "2026-09-29 03:00", "2026-09-29 15:00"},
		{"12h anchored on pm hour", schedule{Frequency: FrequencyEvery12h, Hour: 15}, "2026-09-29 02:00", "2026-09-28 15:00", "2026-09-29 03:00"},
		// 2026-09-29 is a Tuesday.
		{"weekly monday", schedule{Frequency: FrequencyWeekly, Hour: 4, Minute: 30, Weekday: time.Monday}, "2026-09-29 12:00", "2026-09-28 04:30", "2026-10-05 04:30"},
		{"weekly same day later", schedule{Frequency: FrequencyWeekly, Hour: 22, Weekday: time.Tuesday}, "2026-09-29 12:00", "2026-09-22 22:00", "2026-09-29 22:00"},
		// Europe/Paris leaves summer time on 2026-10-25 at 03:00 → 02:00:
		// the slot stays on the 03:00 wall clock on both sides.
		{"daily across DST end", schedule{Frequency: FrequencyDaily, Hour: 3}, "2026-10-25 12:00", "2026-10-25 03:00", "2026-10-26 03:00"},
	}
	for _, tc := range cases {
		now := at(tc.now)
		if got := tc.sched.latestSlot(now, paris); !got.Equal(at(tc.latest)) {
			t.Errorf("%s: latestSlot = %s, want %s", tc.name, got, tc.latest)
		}
		if got := tc.sched.nextSlot(now, paris); !got.Equal(at(tc.next)) {
			t.Errorf("%s: nextSlot = %s, want %s", tc.name, got, tc.next)
		}
	}
}

func TestStaleAfter(t *testing.T) {
	for freq, want := range map[string]time.Duration{
		FrequencyEvery12h: 72 * time.Hour,
		FrequencyDaily:    72 * time.Hour,
		FrequencyWeekly:   9 * 24 * time.Hour,
	} {
		if got := (schedule{Frequency: freq}).staleAfter(); got != want {
			t.Errorf("%s: staleAfter = %s, want %s", freq, got, want)
		}
	}
}

func TestAllowedIP(t *testing.T) {
	cases := []struct {
		ip                  string
		public, withPrivate bool
	}{
		{"93.184.216.34", true, true},
		{"2606:4700::1111", true, true},
		{"127.0.0.1", false, true},
		{"192.168.1.20", false, true},
		{"10.0.0.5", false, true},
		{"100.101.102.103", false, true}, // Tailscale/CGNAT
		{"fd7a:115c:a1e0::1", false, true},
		{"169.254.169.254", false, false}, // cloud metadata: never
		{"::ffff:169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"0.0.0.0", false, false},
		{"224.0.0.1", false, false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if got := allowedIP(ip, false); got != tc.public {
			t.Errorf("%s default: got %v, want %v", tc.ip, got, tc.public)
		}
		if got := allowedIP(ip, true); got != tc.withPrivate {
			t.Errorf("%s with private allowed: got %v, want %v", tc.ip, got, tc.withPrivate)
		}
	}
}
