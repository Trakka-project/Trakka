package validate

import (
	"reflect"
	"strings"
	"testing"
)

func TestLabels(t *testing.T) {
	cases := []struct {
		name    string
		raw     []string
		want    []string
		wantErr error
	}{
		{"nil is valid (no labels)", nil, []string{}, nil},
		{"empty slice is valid", []string{}, []string{}, nil},
		{"trims and keeps order", []string{"  Bio  ", "Promo"}, []string{"Bio", "Promo"}, nil},
		{"drops empty/whitespace-only entries", []string{"Bio", "   ", ""}, []string{"Bio"}, nil},
		{"dedups case-insensitively, keeping first casing", []string{"Bio", "bio", "BIO"}, []string{"Bio"}, nil},
		{"rejects a label over MaxLabelLen", []string{strings.Repeat("a", MaxLabelLen+1)}, nil, ErrLabelTooLong},
		{"accepts a label at exactly MaxLabelLen", []string{strings.Repeat("a", MaxLabelLen)}, []string{strings.Repeat("a", MaxLabelLen)}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Labels(tc.raw)
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("Labels(%v) error = %v, want %v", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %v: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Labels(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestLabelsRejectsTooMany(t *testing.T) {
	raw := make([]string, MaxLabelsPerItem+1)
	for i := range raw {
		// Distinct labels so none of them get deduplicated away before the
		// count check runs.
		raw[i] = string(rune('a' + i))
	}
	if _, err := Labels(raw); err != ErrTooManyLabels {
		t.Fatalf("Labels() error = %v, want %v", err, ErrTooManyLabels)
	}
}
