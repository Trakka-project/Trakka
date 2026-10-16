package budget

import (
	"math"
	"testing"
)

func TestSummarize(t *testing.T) {
	tests := []struct {
		name string
		in   Sums
		want Summary
	}{
		{
			name: "nothing bought yet is on budget, not a saving",
			in:   Sums{RemainingEstimated: 120},
			want: Summary{EstimatedTotal: 120, RemainingTotal: 120, ProjectedTotal: 120, Status: StatusOnPlan},
		},
		{
			name: "purchases that cost more than planned are an overrun",
			in:   Sums{RemainingEstimated: 50, BoughtEstimated: 30, Spent: 34.5},
			want: Summary{EstimatedTotal: 80, SpentTotal: 34.5, RemainingTotal: 50, ProjectedTotal: 84.5, Variance: 4.5, Status: StatusOver},
		},
		{
			name: "purchases that cost less than planned are a saving",
			in:   Sums{RemainingEstimated: 10, BoughtEstimated: 40, Spent: 32.2},
			want: Summary{EstimatedTotal: 50, SpentTotal: 32.2, RemainingTotal: 10, ProjectedTotal: 42.2, Variance: -7.8, Status: StatusUnder},
		},
		{
			name: "everything bought at the estimate is on budget",
			in:   Sums{BoughtEstimated: 25, Spent: 25},
			want: Summary{EstimatedTotal: 25, SpentTotal: 25, ProjectedTotal: 25, Status: StatusOnPlan},
		},
		{
			name: "an unplanned purchase (no estimate) is all overrun",
			in:   Sums{RemainingEstimated: 10, Spent: 6},
			want: Summary{EstimatedTotal: 10, SpentTotal: 6, RemainingTotal: 10, ProjectedTotal: 16, Variance: 6, Status: StatusOver},
		},
		{
			name: "float noise below a cent is on budget",
			in:   Sums{BoughtEstimated: 0.1 + 0.2, Spent: 0.3},
			want: Summary{EstimatedTotal: 0.3, SpentTotal: 0.3, ProjectedTotal: 0.3, Status: StatusOnPlan},
		},
		{
			name: "a variance of a cent still counts",
			in:   Sums{BoughtEstimated: 9.99, Spent: 10},
			want: Summary{EstimatedTotal: 9.99, SpentTotal: 10, ProjectedTotal: 10, Variance: 0.01, Status: StatusOver},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Summarize(tt.in)
			if got != tt.want {
				t.Fatalf("Summarize(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// The variance is the projected total minus the planned budget, whatever
// the mix of bought and remaining items.
func TestSummarizeVarianceIsProjectedMinusEstimated(t *testing.T) {
	for _, s := range []Sums{
		{RemainingEstimated: 12.34, BoughtEstimated: 56.78, Spent: 60.01},
		{RemainingEstimated: 0, BoughtEstimated: 19.99, Spent: 3},
		{RemainingEstimated: 7, BoughtEstimated: 0, Spent: 0},
	} {
		got := Summarize(s)
		if diff := RoundCents(got.ProjectedTotal - got.EstimatedTotal); diff != got.Variance {
			t.Errorf("%+v: projected − estimated = %v, variance = %v", s, diff, got.Variance)
		}
	}
}

func TestRoundCents(t *testing.T) {
	tests := map[float64]float64{
		0.1 + 0.2: 0.3,
		2.336:     2.34,
		-0.004:    0,
		-1.236:    -1.24,
		19.999:    20,
	}
	for in, want := range tests {
		if got := RoundCents(in); got != want {
			t.Errorf("RoundCents(%v) = %v, want %v", in, got, want)
		}
	}
	if got := RoundCents(-0.001); math.Signbit(got) {
		t.Errorf("RoundCents(-0.001) = %v, want +0", got)
	}
}
