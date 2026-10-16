// Package budget holds the stdlib-only arithmetic behind a list's budget
// summary — planned budget vs. what was actually spent, and the variance
// between the two (see models.ListBudget). It knows nothing about the
// database or HTTP: internal/db sums a list's items in SQL (listSelect) and
// hands the three sums to Summarize, the same split internal/recurrence and
// internal/catalog already follow.
//
// static/js/list_view.js (computeBudget) is the hand-kept JS port the list
// view uses to update its summary bar live, before any request lands: keep
// the two in step.
package budget

import "math"

// Variance statuses reported by Summarize: the purchases made so far cost
// less than their estimate, exactly it (to the cent), or more.
const (
	StatusUnder  = "under"
	StatusOnPlan = "on_budget"
	StatusOver   = "over"
)

// Sums are a list's three money totals, each already summed over its items
// as quantity × unit price (internal/db.listSelect):
//
//   - RemainingEstimated: the estimated price (items.price) of every item
//     not bought yet, planned or to buy;
//   - BoughtEstimated: the estimated price of every bought item;
//   - Spent: what every bought item actually cost — its actual_price when
//     one was entered, its estimated price otherwise (a purchase confirmed
//     without typing a price is taken to have cost what was planned).
//
// An item without an estimated price contributes nothing to the two
// estimated sums, but its actual price, if any, still counts in Spent: it is
// money spent outside the budget.
type Sums struct {
	RemainingEstimated float64
	BoughtEstimated    float64
	Spent              float64
}

// Summary is what Summarize derives from Sums, every figure rounded to the
// cent:
//
//   - EstimatedTotal: the planned budget, every item's estimate;
//   - SpentTotal: the money actually spent so far;
//   - RemainingTotal: the estimate of what is still to buy;
//   - ProjectedTotal: what the list will have cost once everything is
//     bought, if the rest costs what was planned (spent + remaining);
//   - Variance: ProjectedTotal − EstimatedTotal, which is also what the
//     purchases made so far cost beyond their estimate (Spent −
//     BoughtEstimated). Positive is an overrun, negative a saving — never
//     merely "money not spent yet", which the budget-minus-spent figure
//     would wrongly report as a saving on a half-bought list;
//   - Status: StatusOver, StatusUnder or StatusOnPlan for that variance.
type Summary struct {
	EstimatedTotal float64
	SpentTotal     float64
	RemainingTotal float64
	ProjectedTotal float64
	Variance       float64
	Status         string
}

// Summarize computes a list's Summary from its Sums.
func Summarize(s Sums) Summary {
	estimated := s.RemainingEstimated + s.BoughtEstimated
	variance := RoundCents(s.Spent - s.BoughtEstimated)
	status := StatusOnPlan
	switch {
	case variance > 0:
		status = StatusOver
	case variance < 0:
		status = StatusUnder
	}
	return Summary{
		EstimatedTotal: RoundCents(estimated),
		SpentTotal:     RoundCents(s.Spent),
		RemainingTotal: RoundCents(s.RemainingEstimated),
		ProjectedTotal: RoundCents(s.Spent + s.RemainingEstimated),
		Variance:       variance,
		Status:         status,
	}
}

// RoundCents rounds an amount in euros to the nearest cent, so float sums
// such as 0.1 + 0.2 report 0.3 and a variance of a fraction of a cent counts
// as on budget. -0 comes back as 0.
func RoundCents(amount float64) float64 {
	rounded := math.Round(amount*100) / 100
	if rounded == 0 {
		return 0
	}
	return rounded
}
