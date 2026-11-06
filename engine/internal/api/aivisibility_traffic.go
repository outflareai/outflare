package api

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/attribution"
)

// What AI answers bring: the brand's website visits from AI assistants
// (the ai_answers channel, from website tracking), the conversions and
// revenue attribution credits to them under the brand's default model,
// and what AI visibility spent, over the overview's period. A cohort with
// no brand (the company's own) has none.

// aiVisTraffic is what AI answers brought one brand.
type aiVisTraffic struct {
	Visits      int     `json:"visits"`
	Conversions float64 `json:"conversions"`
	Revenue     []money `json:"revenue"`
	// SpendCents is the spend recorded on the brand's AI answers channel
	// (AI visibility's screens, demand and benchmarks, and anything else
	// put there), in US cents.
	SpendCents int64            `json:"spend_cents"`
	Model      string           `json:"model"`
	Sources    []aiVisTrafficBy `json:"sources"`
	Pages      []aiVisTrafficBy `json:"landing_pages"`
}

// aiVisTrafficBy is one assistant's, or one landing page's, share.
type aiVisTrafficBy struct {
	Name        string  `json:"name"`
	Visits      int     `json:"visits"`
	Conversions float64 `json:"conversions"`
}

// maxTrafficPages is how many landing pages the overview lists.
const maxTrafficPages = 10

func (s *Server) aiVisibilityTraffic(ctx context.Context, ws string, ids []string, byID map[string]*aiVisCohort, from time.Time) error {
	done := map[string]*aiVisTraffic{}
	for _, id := range ids {
		co := byID[id]
		if co.Cohort.BrandID == nil {
			continue
		}
		brand := *co.Cohort.BrandID
		if t, ok := done[brand]; ok {
			co.Traffic = t
			continue
		}
		settings, err := s.loadAttributionSettings(ctx, ws, brand)
		if err != nil {
			return err
		}
		model, _, err := attribution.Resolve(ctx, s.pool, ws, brand, settings.Model)
		if errors.Is(err, attribution.ErrUnknownModel) || errors.Is(err, attribution.ErrNotCredited) {
			model, err = attribution.LastNonDirect, nil
		}
		if err != nil {
			return err
		}
		t := &aiVisTraffic{Model: model, Revenue: []money{}, Sources: []aiVisTrafficBy{}, Pages: []aiVisTrafficBy{}}
		src, pages := map[string]*aiVisTrafficBy{}, map[string]*aiVisTrafficBy{}
		by := func(m map[string]*aiVisTrafficBy, k string) *aiVisTrafficBy {
			if m[k] == nil {
				m[k] = &aiVisTrafficBy{Name: k}
			}
			return m[k]
		}
		rows, err := s.pool.Query(ctx, `
			SELECT source, landing_path, count(*) FROM site_sessions
			WHERE workspace_id = $1 AND brand_id = $2 AND channel = 'ai_answers' AND started_at >= $3
			GROUP BY 1, 2`, ws, brand, from)
		if err != nil {
			return err
		}
		var source, path string
		var n int
		if _, err := pgx.ForEachRow(rows, []any{&source, &path, &n}, func() error {
			t.Visits += n
			by(src, source).Visits += n
			by(pages, path).Visits += n
			return nil
		}); err != nil {
			return err
		}
		rows, err = s.pool.Query(ctx, `
			SELECT cr.source, COALESCE(mq.landing_path, ''), c.name, sum(cr.weight)::float8, c.currency, sum(cr.weight * c.value_minor)::float8
			FROM attribution_credits cr JOIN attribution_conversions c ON c.event_id = cr.event_id
			LEFT JOIN site_sessions mq ON mq.id = cr.session_id
			WHERE cr.workspace_id = $1 AND cr.model = $4 AND cr.channel = 'ai_answers' AND c.brand_id = $2 AND c.occurred_at >= $3
			GROUP BY 1, 2, 3, 5`, ws, brand, from, model)
		if err != nil {
			return err
		}
		revenue := map[string]float64{}
		var name string
		var weight float64
		var currency *string
		var value *float64
		if _, err := pgx.ForEachRow(rows, []any{&source, &path, &name, &weight, &currency, &value}, func() error {
			if name != "payment.refunded" {
				t.Conversions += weight
				by(src, source).Conversions += weight
				by(pages, path).Conversions += weight
			}
			if value != nil && currency != nil && *currency != "" {
				revenue[*currency] += *value
			}
			return nil
		}); err != nil {
			return err
		}
		t.Conversions, t.Revenue = round2(t.Conversions), moneyList(revenue)
		if err := s.pool.QueryRow(ctx, `
			SELECT COALESCE(sum(amount_cents), 0)::bigint FROM spend_entries
			WHERE workspace_id = $1 AND brand_id = $2 AND channel = 'ai_answers' AND currency = 'USD' AND occurred_at >= $3`, ws, brand, from).Scan(&t.SpendCents); err != nil {
			return err
		}
		list := func(m map[string]*aiVisTrafficBy, limit int) []aiVisTrafficBy {
			out := []aiVisTrafficBy{}
			for _, x := range m {
				if x.Name == "" {
					continue
				}
				x.Conversions = round2(x.Conversions)
				out = append(out, *x)
			}
			sort.Slice(out, func(i, j int) bool {
				if out[i].Visits != out[j].Visits {
					return out[i].Visits > out[j].Visits
				}
				if out[i].Conversions != out[j].Conversions {
					return out[i].Conversions > out[j].Conversions
				}
				return out[i].Name < out[j].Name
			})
			if limit > 0 && len(out) > limit {
				out = out[:limit]
			}
			return out
		}
		t.Sources, t.Pages = list(src, 0), list(pages, maxTrafficPages)
		done[brand] = t
		co.Traffic = t
	}
	return nil
}
