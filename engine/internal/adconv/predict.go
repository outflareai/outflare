package adconv

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/classify"
)

// historyQuerier is what reading a brand's history needs: the pool, or a
// transaction.
type historyQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Qualified sign-ups and their predicted value: an ad network that only
// hears about payments learns slowly, and one that hears about every
// sign-up learns to find people who never pay. A rule with predicted_value
// sends each sign-up with what people like them went on to pay, and with
// qualified_only it sends only the sign-ups at least as likely to pay as
// the brand's average.
//
// "People like them" is the brand's own history: everyone who signed up
// in the 90 days before the last two (so they had time to pay), grouped
// by country and by the kind of email address they used (work, free or
// throwaway), with whether they paid within 30 days and how much. A
// group's paid share is pulled toward the brand's overall share by
// qualityPrior sign-ups, so a group of three people never reads as 100%.
// The value is that share times what a payer pays in their first 30 days.
// Nothing is learned across brands or workspaces.

const (
	// qualityDays is the cohort's length, qualityMature how recent a
	// sign-up is left out, and qualityPayDays how long after signing up a
	// payment counts.
	qualityDays    = 90
	qualityMature  = 2
	qualityPayDays = 30
	// qualityPrior is how many sign-ups at the overall share a group is
	// mixed with.
	qualityPrior = 25
	// A brand needs this much history before anything is predicted.
	qualityMinSignups = 100
	qualityMinPayers  = 10
	// qualityTTL is how long a brand's history is kept before it is read
	// again.
	qualityTTL = time.Hour
	// PaymentEvent is the event a payment arrives as, with amount_minor
	// and currency.
	PaymentEvent = "payment.succeeded"
)

// Email kinds a sign-up is grouped by.
const (
	EmailWork      = "work"
	EmailFree      = "free"
	EmailThrowaway = "throwaway"
	EmailUnknown   = "unknown"
)

// EmailKind is the group an email address's domain falls in, from the
// global classifier's data alone (no lookup leaves the server).
func EmailKind(ctx context.Context, domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return EmailUnknown
	}
	res, err := (&classify.Classifier{}).Classify(ctx, classify.Input{Domain: domain})
	if err != nil {
		return EmailUnknown
	}
	switch res.Global.Category {
	case classify.FreePersonal, classify.PrivateMailbox:
		return EmailFree
	case classify.Disposable, classify.RelayAlias:
		return EmailThrowaway
	case classify.Unknown, "":
		return EmailUnknown
	}
	return EmailWork
}

// QualitySegment is one group of past sign-ups.
type QualitySegment struct {
	Country   string `json:"country"`
	EmailKind string `json:"email_kind"`
	Signups   int    `json:"signups"`
	Paid      int    `json:"paid"`
	// PaidShare is the share who paid, as counted; PredictedShare the
	// share a new sign-up in the group is given, pulled toward the brand's.
	PaidShare      float64 `json:"paid_share"`
	PredictedShare float64 `json:"predicted_share"`
	ValueMicros    int64   `json:"predicted_value_micros"`
	Qualified      bool    `json:"qualified"`
}

// Quality is a brand's history of who pays after signing up.
type Quality struct {
	Event string    `json:"event"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	// Signups and Paid count the whole cohort; PaidShare is its share.
	Signups   int     `json:"signups"`
	Paid      int     `json:"paid"`
	PaidShare float64 `json:"paid_share"`
	// ValuePerPayerMicros is what a payer paid in their first 30 days, on
	// average, in Currency; payments in another currency are left out.
	ValuePerPayerMicros int64  `json:"value_per_payer_micros"`
	Currency            string `json:"currency"`
	// Enough is false while the brand has too little history to predict
	// from: sign-ups are then sent without a value, and all of them.
	Enough      bool `json:"enough"`
	MinSignups  int  `json:"min_signups"`
	MinPayers   int  `json:"min_payers"`
	PayDays     int  `json:"pay_days"`
	segments    map[string]*QualitySegment
	SegmentRows []QualitySegment `json:"segments"`
}

func segmentKey(country, kind string) string { return strings.ToUpper(country) + "|" + kind }

// Predict is what a new sign-up from country with this kind of email is
// given: the share expected to pay, the value to send, and whether it is a
// qualified sign-up. Without enough history everyone is qualified and
// there is no value.
func (q Quality) Predict(country, kind string) (share float64, valueMicros *int64, qualified bool) {
	if !q.Enough {
		return 0, nil, true
	}
	n, paid := 0, 0
	if s := q.segments[segmentKey(country, kind)]; s != nil {
		n, paid = s.Signups, s.Paid
	}
	share = (float64(paid) + qualityPrior*q.PaidShare) / (float64(n) + qualityPrior)
	v := int64(math.Round(share * float64(q.ValuePerPayerMicros)))
	// A throwaway address is never a qualified sign-up, whatever its
	// group's few payers did.
	qualified = kind != EmailThrowaway && share >= q.PaidShare
	return share, &v, qualified
}

// qualityRow is one (email domain, country) group as the database counts it.
type qualityRow struct {
	domain, country string
	signups, paid   int
	valueMinor      float64
}

// LoadQuality reads a brand's sign-up history for event (brand "" is a
// workspace without brands on its events) and sums payments in currency.
func LoadQuality(ctx context.Context, q historyQuerier, ws, brand, event, currency string, now time.Time) (Quality, error) {
	out := Quality{Event: event, Currency: currency, MinSignups: qualityMinSignups, MinPayers: qualityMinPayers, PayDays: qualityPayDays,
		From: now.AddDate(0, 0, -(qualityDays + qualityMature)), To: now.AddDate(0, 0, -qualityMature)}
	rows, err := q.Query(ctx, `
		WITH su AS (
			SELECT e.record_id, min(e.occurred_at) AS at FROM customer_events e
			WHERE e.workspace_id = $1 AND ($2 = '' OR e.brand_id::text = $2) AND e.kind = 'track' AND e.name = $3
			  AND e.record_id IS NOT NULL AND e.occurred_at >= $4 AND e.occurred_at < $5
			GROUP BY e.record_id
		), person AS (
			SELECT su.record_id,
				COALESCE((SELECT lower(split_part(i.value, '@', 2)) FROM record_identities i WHERE i.workspace_id = $1 AND i.record_id = su.record_id
					AND i.namespace = 'email' AND i.valid_to IS NULL ORDER BY i.verified DESC, i.created_at DESC LIMIT 1), '') AS domain,
				COALESCE((SELECT upper(s.country) FROM site_sessions s WHERE s.workspace_id = $1 AND s.record_id = su.record_id AND s.country <> ''
					ORDER BY s.started_at DESC LIMIT 1),
					(SELECT upper(e2.context->'location'->>'country') FROM customer_events e2 WHERE e2.workspace_id = $1 AND e2.record_id = su.record_id
						AND e2.kind = 'track' AND e2.name = $3 AND e2.context->'location'->>'country' ~ '^[A-Za-z]{2}$' ORDER BY e2.occurred_at LIMIT 1),
					(SELECT upper(r.attributes->>'country') FROM records r WHERE r.id = su.record_id AND r.attributes->>'country' ~ '^[A-Za-z]{2}$'), '') AS country,
				pay.n > 0 AS paid, pay.minor
			FROM su, LATERAL (
				SELECT count(*) AS n, COALESCE(sum(CASE WHEN p.properties->>'amount_minor' ~ '^[0-9]+(\.[0-9]+)?$'
						AND upper(COALESCE(NULLIF(p.properties->>'currency', ''), $7)) = $7
					THEN (p.properties->>'amount_minor')::numeric ELSE 0 END), 0)::float8 AS minor
				FROM customer_events p WHERE p.workspace_id = $1 AND p.record_id = su.record_id AND p.kind = 'track' AND p.name = $6
				  AND p.occurred_at >= su.at AND p.occurred_at < su.at + make_interval(days => $8)) pay
		)
		SELECT domain, country, count(*)::int, count(*) FILTER (WHERE paid)::int, COALESCE(sum(minor) FILTER (WHERE paid), 0)::float8
		FROM person GROUP BY domain, country`,
		ws, brand, event, out.From, out.To, PaymentEvent, strings.ToUpper(currency), qualityPayDays)
	if err != nil {
		return out, err
	}
	groups, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (qualityRow, error) {
		var g qualityRow
		return g, r.Scan(&g.domain, &g.country, &g.signups, &g.paid, &g.valueMinor)
	})
	if err != nil {
		return out, err
	}
	out.fill(ctx, groups)
	return out, nil
}

// fill turns the database's groups into segments and the cohort's totals.
func (q *Quality) fill(ctx context.Context, groups []qualityRow) {
	q.segments = map[string]*QualitySegment{}
	kinds := map[string]string{}
	var minor float64
	for _, g := range groups {
		kind, ok := kinds[g.domain]
		if !ok {
			kind = EmailKind(ctx, g.domain)
			kinds[g.domain] = kind
		}
		key := segmentKey(g.country, kind)
		s := q.segments[key]
		if s == nil {
			s = &QualitySegment{Country: strings.ToUpper(g.country), EmailKind: kind}
			q.segments[key] = s
		}
		s.Signups += g.signups
		s.Paid += g.paid
		q.Signups += g.signups
		q.Paid += g.paid
		minor += g.valueMinor
	}
	if q.Signups > 0 {
		q.PaidShare = float64(q.Paid) / float64(q.Signups)
	}
	if q.Paid > 0 {
		q.ValuePerPayerMicros = int64(math.Round(minor / float64(q.Paid) * 10_000))
	}
	q.Enough = q.Signups >= qualityMinSignups && q.Paid >= qualityMinPayers
	q.SegmentRows = make([]QualitySegment, 0, len(q.segments))
	for _, s := range q.segments {
		s.PaidShare = float64(s.Paid) / float64(s.Signups)
		if q.Enough {
			var v *int64
			s.PredictedShare, v, s.Qualified = q.Predict(s.Country, s.EmailKind)
			s.ValueMicros = *v
		}
		q.SegmentRows = append(q.SegmentRows, *s)
	}
	sort.Slice(q.SegmentRows, func(i, j int) bool {
		a, b := q.SegmentRows[i], q.SegmentRows[j]
		if a.Signups != b.Signups {
			return a.Signups > b.Signups
		}
		return segmentKey(a.Country, a.EmailKind) < segmentKey(b.Country, b.EmailKind)
	})
}

// qualityCache keeps each brand's history for qualityTTL, so a batch of
// sign-ups reads it once.
type qualityCache struct {
	mu sync.Mutex
	at map[string]time.Time
	q  map[string]Quality
}

var qualities = &qualityCache{at: map[string]time.Time{}, q: map[string]Quality{}}

func (c *qualityCache) get(ctx context.Context, db historyQuerier, ws, brand, event, currency string, now time.Time) (Quality, error) {
	key := ws + "|" + brand + "|" + event + "|" + currency
	c.mu.Lock()
	if at, ok := c.at[key]; ok && now.Sub(at) < qualityTTL && now.Sub(at) >= 0 {
		q := c.q[key]
		c.mu.Unlock()
		return q, nil
	}
	c.mu.Unlock()
	q, err := LoadQuality(ctx, db, ws, brand, event, currency, now)
	if err != nil {
		return q, err
	}
	c.mu.Lock()
	c.at[key], c.q[key] = now, q
	c.mu.Unlock()
	return q, nil
}

// ResetQualityCache forgets every brand's history (tests).
func ResetQualityCache() {
	qualities.mu.Lock()
	qualities.at, qualities.q = map[string]time.Time{}, map[string]Quality{}
	qualities.mu.Unlock()
}

// personEmailDomain is the domain of the person's best email address.
func personEmailDomain(ctx context.Context, q historyQuerier, ws string, recordID *string) (string, error) {
	if recordID == nil {
		return "", nil
	}
	var domain string
	err := q.QueryRow(ctx, `SELECT lower(split_part(value, '@', 2)) FROM record_identities
		WHERE workspace_id = $1 AND record_id = $2 AND namespace = 'email' AND valid_to IS NULL
		ORDER BY verified DESC, created_at DESC LIMIT 1`, ws, *recordID).Scan(&domain)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return domain, err
}
