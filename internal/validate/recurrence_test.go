package validate

import "testing"

// TestRecurrence covers the validate-level contract: empty means "not
// recurring", anything else comes back in canonical RRULE form or is
// rejected. The full grammar is tested in internal/recurrence.
func TestRecurrence(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"empty is valid (not recurring)", "", "", false},
		{"whitespace-only is valid", "   ", "", false},
		{"canonical rrule", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "FREQ=WEEKLY;BYDAY=MO,WE,FR", false},
		{"normalizes an rrule", "freq=weekly;byday=fr,mo;interval=2", "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR", false},
		{"legacy fixed cadence", "weekly", "FREQ=WEEKLY", false},
		{"legacy every-x-days", "EVERY_X_DAYS:3", "FREQ=DAILY;INTERVAL=3", false},
		{"legacy every-x-days in weeks", "EVERY_X_DAYS:14", "FREQ=WEEKLY;INTERVAL=2", false},
		{"legacy every-x-months", "EVERY_X_MONTHS:3", "FREQ=MONTHLY;INTERVAL=3", false},
		{"rejects every-x-days with zero", "EVERY_X_DAYS:0", "", true},
		{"rejects BYDAY on a monthly rule", "FREQ=MONTHLY;BYDAY=MO", "", true},
		{"rejects unknown rule", "FORTNIGHTLY", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Recurrence(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got none", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("Recurrence(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
