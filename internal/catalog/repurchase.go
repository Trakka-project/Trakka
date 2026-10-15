package catalog

import (
	"sort"
	"time"
)

// PurchaseHistory is what the catalog remembers of a product's purchases:
// how many times it was checked off a list, and when the first and the
// latest of those happened.
type PurchaseHistory struct {
	Count int
	First time.Time
	Last  time.Time
}

// Repurchase describes where a product stands in its own buying rhythm.
type Repurchase struct {
	// AvgIntervalDays is the average number of days between two purchases.
	AvgIntervalDays float64
	// DaysSinceLast is how long ago it was last bought.
	DaysSinceLast float64
	// Ratio is DaysSinceLast / AvgIntervalDays: 1 means "exactly one usual
	// interval ago", above 1 overdue.
	Ratio float64
	// Due is Ratio >= DueRatio.
	Due bool
}

// DueRatio is how far into its usual interval a product has to be for it
// to be suggested as "time to buy it again": a little before the interval
// is up, since a shopping trip covers the next few days too.
const DueRatio = 0.8

// minInterval keeps two purchases logged moments apart (a double check-off,
// a bulk check) from making a product look like it is bought every second.
const minInterval = 24 * time.Hour

// Assess places a product in its buying rhythm as of now. It needs at least
// two purchases to have an interval at all, and reports false otherwise.
func Assess(h PurchaseHistory, now time.Time) (Repurchase, bool) {
	if h.Count < 2 || h.Last.IsZero() || h.First.IsZero() {
		return Repurchase{}, false
	}
	avg := h.Last.Sub(h.First) / time.Duration(h.Count-1)
	if avg < minInterval {
		avg = minInterval
	}
	since := now.Sub(h.Last)
	if since < 0 {
		since = 0
	}
	ratio := float64(since) / float64(avg)
	return Repurchase{
		AvgIntervalDays: avg.Hours() / 24,
		DaysSinceLast:   since.Hours() / 24,
		Ratio:           ratio,
		Due:             ratio >= DueRatio,
	}, true
}

// RankRepurchases orders the products whose histories are given and returns
// the indexes of at most limit of them, best suggestion first, with their
// assessment: every product that is due, most overdue first, then the
// others, most often bought first (then most recently bought). Products
// with fewer than two purchases are left out.
func RankRepurchases(histories []PurchaseHistory, now time.Time, limit int) []RankedRepurchase {
	ranked := make([]RankedRepurchase, 0, len(histories))
	for i, h := range histories {
		if r, ok := Assess(h, now); ok {
			ranked = append(ranked, RankedRepurchase{Index: i, Repurchase: r, count: h.Count, last: h.Last})
		}
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		ra, rb := ranked[a], ranked[b]
		if ra.Due != rb.Due {
			return ra.Due
		}
		if ra.Due && ra.Ratio != rb.Ratio {
			return ra.Ratio > rb.Ratio
		}
		if ra.count != rb.count {
			return ra.count > rb.count
		}
		return ra.last.After(rb.last)
	})
	if limit >= 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// RankedRepurchase is one entry of RankRepurchases' result: Index points
// into the histories slice it was given.
type RankedRepurchase struct {
	Index int
	Repurchase
	count int
	last  time.Time
}
