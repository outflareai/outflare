package paidads

import (
	"context"
	"time"
)

// Lag is how long a brand's new customers take from the ad click to their
// first payment: the median and the 80th percentile over everyone the
// ledger has, and how many that is.
type Lag struct {
	Median time.Duration `json:"median"`
	P80    time.Duration `json:"p80"`
	Payers int           `json:"payers"`
}

// MinJudgementWindow is the least a change is measured for before it is
// judged, whatever the brand's lag.
const MinJudgementWindow = 14 * 24 * time.Hour

// ClickToPaymentLag reads the brand's lag curve from its acquisitions.
// With no payers yet, Median and P80 are zero.
func ClickToPaymentLag(ctx context.Context, q Querier, brandID string) (Lag, error) {
	var median, p80 *float64
	var l Lag
	err := q.QueryRow(ctx, `SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch from first_paid_at - clicked_at)),
			percentile_cont(0.8) WITHIN GROUP (ORDER BY extract(epoch from first_paid_at - clicked_at)), count(*)::int
		FROM ad_acquisitions WHERE brand_id = $1 AND first_paid_at IS NOT NULL AND first_paid_at >= clicked_at`, brandID).Scan(&median, &p80, &l.Payers)
	if err != nil {
		return l, err
	}
	if median != nil {
		l.Median = time.Duration(*median * float64(time.Second))
	}
	if p80 != nil {
		l.P80 = time.Duration(*p80 * float64(time.Second))
	}
	return l, nil
}

// JudgementWindow is how long a change is measured before it is judged:
// two conversion cycles (the median click-to-payment lag), and never less
// than 14 days.
func JudgementWindow(l Lag) time.Duration {
	return max(2*l.Median, MinJudgementWindow)
}

// JudgementWindowDays is JudgementWindow for a brand, in whole days
// (rounded up).
func JudgementWindowDays(ctx context.Context, q Querier, brandID string) (int, error) {
	l, err := ClickToPaymentLag(ctx, q, brandID)
	if err != nil {
		return 0, err
	}
	w := JudgementWindow(l)
	days := int(w / (24 * time.Hour))
	if w%(24*time.Hour) != 0 {
		days++
	}
	return days, nil
}
