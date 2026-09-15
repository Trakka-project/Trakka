package validate

import "testing"

func TestTimeOfDay(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"empty is valid", "", "", false},
		{"whitespace-only is valid", "   ", "", false},
		{"valid time", "09:00", "09:00", false},
		{"valid time near midnight", "23:59", "23:59", false},
		{"trims surrounding whitespace", "  20:00  ", "20:00", false},
		{"rejects hour out of range", "24:00", "", true},
		{"rejects minute out of range", "09:60", "", true},
		{"rejects garbage", "not-a-time", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TimeOfDay(tc.raw)
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
				t.Fatalf("TimeOfDay(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
