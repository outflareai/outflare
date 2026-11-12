package api

import (
	"net/http"
	"sort"

	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Who the credit went to: whether the person was already known when the
// credited visit happened (warm) or not (cold), and whether a payment was
// their first (new revenue) or a later one (repeat). And what people are
// worth over time, by the channel that first brought them.

// customerRow is one channel's split.
type customerRow struct {
	Channel string `json:"channel"`
	Label   string `json:"label"`
	// Conversions credited to visits by people who were already a record
	// (warm) or not yet (cold). Refunds aren't conversions.
	WarmConversions float64 `json:"warm_conversions"`
	ColdConversions float64 `json:"cold_conversions"`
	// Revenue credited from each person's first payment (new) and from
	// later ones (repeat), net of refunds, per currency.
	NewRevenue    []money `json:"new_revenue"`
	RepeatRevenue []money `json:"repeat_revenue"`
	warm, cold    float64
	newRev, rep   map[string]float64
}

func channelLabel(ch string) string {
	if c, ok := utm.ChannelByID(ch); ok {
		return c.Label
	}
	if ch == attribution.Direct {
		return "Direct"
	}
	return ch
}

// attributionCustomers splits each channel's credit by warm or cold and new
// or repeat, under the model.
func (s *Server) attributionCustomers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	rows := map[string]*customerRow{}
	row := func(ch string) *customerRow {
		if rows[ch] == nil {
			rows[ch] = &customerRow{Channel: ch, Label: channelLabel(ch), newRev: map[string]float64{}, rep: map[string]float64{}}
		}
		return rows[ch]
	}
	// The person's record and their first payment. creditRows counts its
	// key columns by their commas, so the keys have none.
	join := ` LEFT JOIN records pr ON pr.id = c.record_id
		LEFT JOIN LATERAL (SELECT min(p.occurred_at) AS first_paid FROM attribution_conversions p
			WHERE p.workspace_id = c.workspace_id AND p.record_id = c.record_id AND p.value_minor > 0) fp ON true`
	warm := `(CASE WHEN pr.created_at IS NULL THEN false WHEN cr.touch_at IS NULL THEN pr.created_at < c.occurred_at ELSE pr.created_at < cr.touch_at END)`
	repeat := `(fp.first_paid IS NOT NULL AND c.occurred_at > fp.first_paid)`
	err := s.creditRows(r.Context(), a.WorkspaceID, f, join, `cr.channel, `+warm+`, `+repeat, ``, nil,
		func(key []any, name string, weight float64, currency *string, value *float64) error {
			ch, _ := key[0].(string)
			isWarm, _ := key[1].(bool)
			isRepeat, _ := key[2].(bool)
			x := row(ch)
			if name != "payment.refunded" {
				if isWarm {
					x.warm += weight
				} else {
					x.cold += weight
				}
			}
			if value != nil && currency != nil && *currency != "" {
				if isRepeat {
					x.rep[*currency] += *value
				} else {
					x.newRev[*currency] += *value
				}
			}
			return nil
		})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]*customerRow, 0, len(rows))
	for _, x := range rows {
		x.WarmConversions, x.ColdConversions = round2(x.warm), round2(x.cold)
		x.NewRevenue, x.RepeatRevenue = moneyList(x.newRev), moneyList(x.rep)
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].warm+out[i].cold, out[j].warm+out[j].cold
		if ti != tj {
			return ti > tj
		}
		return out[i].Channel < out[j].Channel
	})
	writeJSON(w, http.StatusOK, map[string]any{"model": f.ref, "model_version": f.version, "from": f.from, "to": f.to, "data": out})
}

// cohortRow is the people a channel first brought.
type cohortRow struct {
	Channel string `json:"channel"`
	Label   string `json:"label"`
	// People whose first conversion was in the period and whose first touch
	// was this channel.
	People int `json:"people"`
	// Of them, those who have paid anything, ever.
	Customers int `json:"customers"`
	// What they have paid to date, net of refunds, per currency, and the
	// same per person.
	Revenue          []money `json:"revenue"`
	RevenuePerPerson []money `json:"revenue_per_person"`
	revenue          map[string]float64
}

// attributionCohorts is lifetime value by first-touch channel: people whose
// first conversion fell in the period, by the channel of its first-touch
// credit, and everything they have paid since.
func (s *Server) attributionCohorts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	args := []any{a.WorkspaceID, f.start, f.end}
	cohort := `WITH firsts AS (
			SELECT DISTINCT ON (c.record_id) c.record_id, c.event_id, c.occurred_at, c.brand_id
			FROM attribution_conversions c
			WHERE c.workspace_id = $1 AND c.record_id IS NOT NULL
			ORDER BY c.record_id, c.occurred_at, c.event_id),
		cohort AS (
			SELECT f.record_id, cr.channel FROM firsts f
			JOIN attribution_credits cr ON cr.event_id = f.event_id AND cr.model = 'first_touch' AND cr.position = 0
			WHERE f.occurred_at >= $2 AND f.occurred_at < $3` + f.brand.sql(`f.brand_id`, &args) + `)`
	rows := map[string]*cohortRow{}
	row := func(ch string) *cohortRow {
		if rows[ch] == nil {
			rows[ch] = &cohortRow{Channel: ch, Label: channelLabel(ch), revenue: map[string]float64{}}
		}
		return rows[ch]
	}
	q, err := s.pool.Query(ctx, cohort+`
		SELECT co.channel, count(*)::int,
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM attribution_conversions p
				WHERE p.workspace_id = $1 AND p.record_id = co.record_id AND p.value_minor > 0))::int
		FROM cohort co GROUP BY co.channel`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for q.Next() {
		var ch string
		var people, customers int
		if err := q.Scan(&ch, &people, &customers); err != nil {
			q.Close()
			s.internal(w, r, err)
			return
		}
		x := row(ch)
		x.People, x.Customers = people, customers
	}
	q.Close()
	if err := q.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	q, err = s.pool.Query(ctx, cohort+`
		SELECT co.channel, p.currency, sum(p.value_minor)::float8
		FROM cohort co JOIN attribution_conversions p ON p.workspace_id = $1 AND p.record_id = co.record_id
		WHERE p.value_minor IS NOT NULL AND p.currency <> ''
		GROUP BY co.channel, p.currency`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for q.Next() {
		var ch, cur string
		var v float64
		if err := q.Scan(&ch, &cur, &v); err != nil {
			q.Close()
			s.internal(w, r, err)
			return
		}
		row(ch).revenue[cur] += v
	}
	q.Close()
	if err := q.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]*cohortRow, 0, len(rows))
	for _, x := range rows {
		x.Revenue = moneyList(x.revenue)
		per := map[string]float64{}
		for cur, v := range x.revenue {
			if x.People > 0 {
				per[cur] = v / float64(x.People)
			}
		}
		x.RevenuePerPerson = moneyList(per)
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].People != out[j].People {
			return out[i].People > out[j].People
		}
		return out[i].Channel < out[j].Channel
	})
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}
