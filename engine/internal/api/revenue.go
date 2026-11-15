package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/revenue"
)

// Revenue (docs/revenue.md): a brand's recurring revenue and how it
// moved, what it collected, the renewals coming up and the revenue that
// sits with unhealthy customers. internal/revenue counts the days.

func (s *Server) routesRevenue(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/revenue", s.onBrand(auth.ScopeCustomersRead, s.getRevenue))
}

type revenuePeriod struct {
	Period       string `json:"period"`
	MRR          int64  `json:"mrr_minor"`
	Customers    int    `json:"customers"`
	New          int64  `json:"new_minor"`
	Expansion    int64  `json:"expansion_minor"`
	Reactivation int64  `json:"reactivation_minor"`
	Contraction  int64  `json:"contraction_minor"`
	Churned      int64  `json:"churned_minor"`
}

type revenueRecurring struct {
	MRR              int64    `json:"mrr_minor"`
	ARR              int64    `json:"arr_minor"`
	Customers        int      `json:"customers"`
	AtRisk           int64    `json:"at_risk_minor"`
	AtRiskCustomers  int      `json:"at_risk_customers"`
	StartMRR         int64    `json:"start_mrr_minor"`
	New              int64    `json:"new_minor"`
	Expansion        int64    `json:"expansion_minor"`
	Reactivation     int64    `json:"reactivation_minor"`
	Contraction      int64    `json:"contraction_minor"`
	Churned          int64    `json:"churned_minor"`
	NewCustomers     int      `json:"new_customers"`
	ChurnedCustomers int      `json:"churned_customers"`
	NetRetention     *float64 `json:"net_retention"`
	GrossRetention   *float64 `json:"gross_retention"`
	CountedSince     string   `json:"counted_since"`
}

type revenueCollectedPeriod struct {
	Period   string `json:"period"`
	Paid     int64  `json:"paid_minor"`
	Refunded int64  `json:"refunded_minor"`
}

type revenueCollected struct {
	Paid         int64                    `json:"paid_minor"`
	Refunded     int64                    `json:"refunded_minor"`
	PreviousPaid int64                    `json:"previous_paid_minor"`
	Periods      []revenueCollectedPeriod `json:"periods"`
}

type revenueRenewal struct {
	RecordID    string          `json:"record_id"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	RenewsOn    string          `json:"renews_on"`
	Value       json.RawMessage `json:"value"`
	CompanyID   *string         `json:"company_id"`
	CompanyName *string         `json:"company_name"`
	MRR         *int64          `json:"mrr_minor"`
	HealthBand  *string         `json:"health_band"`
	OwnerID     *string         `json:"owner_id"`
}

type revenueAtRisk struct {
	RecordID string  `json:"record_id"`
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	MRR      int64   `json:"mrr_minor"`
	Health   *int    `json:"health"`
	OwnerID  *string `json:"owner_id"`
}

// revenueMover is a customer whose payments moved between this period
// and the one before: how brands without subscriptions see who is
// spending more and who less.
type revenueMover struct {
	RecordID string `json:"record_id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Paid     int64  `json:"paid_minor"`
	Previous int64  `json:"previous_paid_minor"`
}

// revenueProduct is what one product brought in: payments that name it,
// less refunds, against the same number of days before.
type revenueProduct struct {
	// ProductID is null for payments that name no product.
	ProductID *string `json:"product_id"`
	Key       *string `json:"key"`
	Name      *string `json:"name"`
	Paid      int64   `json:"paid_minor"`
	Refunded  int64   `json:"refunded_minor"`
	Previous  int64   `json:"previous_paid_minor"`
	// Customers is how many people and companies paid for it in the period.
	Customers int `json:"customers"`
}

type revenueReport struct {
	Currency  string            `json:"currency"`
	Days      int               `json:"days"`
	Group     string            `json:"group"`
	Recurring *revenueRecurring `json:"recurring"`
	Periods   []revenuePeriod   `json:"periods"`
	Collected *revenueCollected `json:"collected"`
	// Products splits what was collected by product, the biggest first;
	// empty when no payment names a product.
	Products []revenueProduct `json:"products"`
	Renewals []revenueRenewal `json:"renewals"`
	AtRisk   []revenueAtRisk  `json:"at_risk"`
	Spending struct {
		Up   []revenueMover `json:"up"`
		Down []revenueMover `json:"down"`
	} `json:"spending"`
	Bands map[string]map[string]int `json:"bands"`
}

// revenueGroup is how the days of a report are grouped: by day up to a
// month, by week up to half a year, by month beyond.
func revenueGroup(days int) string {
	switch {
	case days <= 31:
		return "day"
	case days <= 180:
		return "week"
	}
	return "month"
}

// periodStart is the first day of the period a day falls in (weeks start
// on Monday).
func periodStart(d time.Time, group string) time.Time {
	switch group {
	case "week":
		return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
	case "month":
		return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return d
}

func nextPeriod(d time.Time, group string) time.Time {
	switch group {
	case "week":
		return d.AddDate(0, 0, 7)
	case "month":
		return d.AddDate(0, 1, 0)
	}
	return d.AddDate(0, 0, 1)
}

func (s *Server) getRevenue(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days, ok := intParam(w, r, "days", 90, 7, 730)
	if !ok {
		return
	}
	ctx := r.Context()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from := today.AddDate(0, 0, -(days - 1))
	out := revenueReport{Days: days, Group: revenueGroup(days), Periods: []revenuePeriod{}, Renewals: []revenueRenewal{}, AtRisk: []revenueAtRisk{}, Products: []revenueProduct{},
		Bands: map[string]map[string]int{}}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&out.Currency); err != nil {
			return err
		}

		// Recurring revenue: the counted days, oldest first.
		type dayRow struct {
			day time.Time
			revenuePeriod
			atRisk                                          int64
			atRiskCustomers, newCustomers, churnedCustomers int
			bands                                           []byte
		}
		rows, err := tx.Query(ctx, `SELECT day, mrr_minor, customers, new_minor, expansion_minor, reactivation_minor, contraction_minor, churned_minor,
				at_risk_minor, at_risk_customers, new_customers, churned_customers, bands
			FROM revenue_days WHERE workspace_id = $1 AND brand_id = $2 AND day >= $3 ORDER BY day`, a.WorkspaceID, a.Brand, from)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dayRow, error) {
			var d dayRow
			return d, row.Scan(&d.day, &d.MRR, &d.Customers, &d.New, &d.Expansion, &d.Reactivation, &d.Contraction, &d.Churned,
				&d.atRisk, &d.atRiskCustomers, &d.newCustomers, &d.churnedCustomers, &d.bands)
		})
		if err != nil {
			return err
		}
		if len(list) > 0 {
			last := list[len(list)-1]
			rec := &revenueRecurring{MRR: last.MRR, ARR: last.MRR * 12, Customers: last.Customers, AtRisk: last.atRisk, AtRiskCustomers: last.atRiskCustomers}
			if err := json.Unmarshal(last.bands, &out.Bands); err != nil {
				return err
			}
			var since time.Time
			if err := tx.QueryRow(ctx, `SELECT min(day) FROM revenue_days WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&since); err != nil {
				return err
			}
			rec.CountedSince = since.Format(time.DateOnly)
			// Where the period started: the last day before it, or the
			// first day in it less what moved that day.
			var before *int64
			if err := tx.QueryRow(ctx, `SELECT (SELECT mrr_minor FROM revenue_days WHERE workspace_id = $1 AND brand_id = $2 AND day < $3 ORDER BY day DESC LIMIT 1)`,
				a.WorkspaceID, a.Brand, from).Scan(&before); err != nil {
				return err
			}
			first := list[0]
			rec.StartMRR = first.MRR - first.New - first.Expansion - first.Reactivation + first.Contraction + first.Churned
			if before != nil {
				rec.StartMRR = *before
			}
			// One row per period, with the days nothing was counted
			// carrying the level before them.
			level, customers := rec.StartMRR, 0
			i := 0
			for p := periodStart(from, out.Group); !p.After(today); p = nextPeriod(p, out.Group) {
				row := revenuePeriod{Period: p.Format(time.DateOnly)}
				end := nextPeriod(p, out.Group)
				for ; i < len(list) && list[i].day.Before(end); i++ {
					d := list[i]
					level, customers = d.MRR, d.Customers
					row.New += d.New
					row.Expansion += d.Expansion
					row.Reactivation += d.Reactivation
					row.Contraction += d.Contraction
					row.Churned += d.Churned
					rec.New += d.New
					rec.Expansion += d.Expansion
					rec.Reactivation += d.Reactivation
					rec.Contraction += d.Contraction
					rec.Churned += d.Churned
					rec.NewCustomers += d.newCustomers
					rec.ChurnedCustomers += d.churnedCustomers
				}
				row.MRR, row.Customers = level, customers
				out.Periods = append(out.Periods, row)
			}
			if rec.StartMRR > 0 {
				start := float64(rec.StartMRR)
				net := (start + float64(rec.Expansion+rec.Reactivation-rec.Contraction-rec.Churned)) / start
				gross := (start - float64(rec.Contraction+rec.Churned)) / start
				if gross < 0 {
					gross = 0
				}
				rec.NetRetention, rec.GrossRetention = &net, &gross
			}
			if rec.MRR > 0 || rec.StartMRR > 0 || rec.New > 0 {
				out.Recurring = rec
			} else {
				out.Periods = []revenuePeriod{}
			}
		}

		// Collected: payments less refunds, in the brand's currency.
		coll := revenueCollected{Periods: []revenueCollectedPeriod{}}
		rows, err = tx.Query(ctx, `SELECT (occurred_at AT TIME ZONE 'UTC')::date,
				COALESCE(sum(brand_amount_minor) FILTER (WHERE kind = 'payment'), 0)::bigint,
				COALESCE(sum(brand_amount_minor) FILTER (WHERE kind IN ('refund', 'chargeback')), 0)::bigint
			FROM transactions WHERE workspace_id = $1 AND brand_id = $2 AND status = 'succeeded' AND brand_amount_minor IS NOT NULL
				AND occurred_at >= $3 AND kind IN ('payment', 'refund', 'chargeback') GROUP BY 1 ORDER BY 1`, a.WorkspaceID, a.Brand, from)
		if err != nil {
			return err
		}
		type paidDay struct {
			day            time.Time
			paid, refunded int64
		}
		paid, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (paidDay, error) {
			var d paidDay
			return d, row.Scan(&d.day, &d.paid, &d.refunded)
		})
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(brand_amount_minor), 0)::bigint FROM transactions
			WHERE workspace_id = $1 AND brand_id = $2 AND status = 'succeeded' AND kind = 'payment' AND brand_amount_minor IS NOT NULL
				AND occurred_at >= $3 AND occurred_at < $4`, a.WorkspaceID, a.Brand, from.AddDate(0, 0, -days), from).Scan(&coll.PreviousPaid); err != nil {
			return err
		}
		if len(paid) > 0 || coll.PreviousPaid > 0 {
			i := 0
			for p := periodStart(from, out.Group); !p.After(today); p = nextPeriod(p, out.Group) {
				row := revenueCollectedPeriod{Period: p.Format(time.DateOnly)}
				end := nextPeriod(p, out.Group)
				for ; i < len(paid) && paid[i].day.Before(end); i++ {
					row.Paid += paid[i].paid
					row.Refunded += paid[i].refunded
				}
				coll.Paid += row.Paid
				coll.Refunded += row.Refunded
				coll.Periods = append(coll.Periods, row)
			}
			out.Collected = &coll
		}

		// By product: the payments of this period and the one before.
		rows, err = tx.Query(ctx, `SELECT t.product_id::text, p.key, p.name,
				COALESCE(sum(t.brand_amount_minor) FILTER (WHERE t.kind = 'payment' AND t.occurred_at >= $3), 0)::bigint,
				COALESCE(sum(t.brand_amount_minor) FILTER (WHERE t.kind IN ('refund', 'chargeback') AND t.occurred_at >= $3), 0)::bigint,
				COALESCE(sum(t.brand_amount_minor) FILTER (WHERE t.kind = 'payment' AND t.occurred_at < $3), 0)::bigint,
				count(DISTINCT t.record_id) FILTER (WHERE t.kind = 'payment' AND t.occurred_at >= $3)::int
			FROM transactions t LEFT JOIN products p ON p.id = t.product_id AND p.workspace_id = t.workspace_id AND p.brand_id = t.brand_id
			WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.status = 'succeeded' AND t.brand_amount_minor IS NOT NULL
				AND t.occurred_at >= $4 AND t.kind IN ('payment', 'refund', 'chargeback')
			GROUP BY 1, 2, 3 ORDER BY 4 DESC, 6 DESC, 3 NULLS LAST, 1 LIMIT 50`, a.WorkspaceID, a.Brand, from, from.AddDate(0, 0, -days))
		if err != nil {
			return err
		}
		products, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (revenueProduct, error) {
			var v revenueProduct
			return v, row.Scan(&v.ProductID, &v.Key, &v.Name, &v.Paid, &v.Refunded, &v.Previous, &v.Customers)
		})
		if err != nil {
			return err
		}
		// A split is only worth showing when some payment names a product.
		for _, v := range products {
			if v.ProductID != nil {
				out.Products = products
				break
			}
		}

		// Renewals: anything with a "Renews on" date from a month ago
		// (late) to 90 days out, with the company it belongs to.
		band := revenue.HealthKey + "_band"
		rows, err = tx.Query(ctx, `SELECT r.id::text, r.record_type, r.name, r.attributes->>'renews_on', r.attributes->'value',
				c.id::text, c.name, rc.mrr_minor, c.attributes->'scores'->>$5, NULLIF(COALESCE(r.attributes->>'owner', c.attributes->>'owner'), '')
			FROM records r
			LEFT JOIN records c ON c.workspace_id = r.workspace_id AND c.brand_id = r.brand_id AND c.deleted_at IS NULL AND c.merged_into IS NULL
				AND c.id::text = CASE WHEN r.record_type = 'account' THEN r.id::text ELSE r.attributes->>'company' END
			LEFT JOIN revenue_customers rc ON rc.record_id = c.id AND rc.mrr_minor > 0
			WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL
				AND r.attributes->>'renews_on' ~ '^\d{4}-\d{2}-\d{2}$'
				AND r.attributes->>'renews_on' >= $3 AND r.attributes->>'renews_on' <= $4
				AND COALESCE(r.attributes->>'stage', '') NOT IN ('ended', 'cancelled', 'canceled', 'lost')
			ORDER BY r.attributes->>'renews_on', r.id LIMIT 100`,
			a.WorkspaceID, a.Brand, today.AddDate(0, 0, -30).Format(time.DateOnly), today.AddDate(0, 0, 90).Format(time.DateOnly), band)
		if err != nil {
			return err
		}
		if out.Renewals, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (revenueRenewal, error) {
			var v revenueRenewal
			var value []byte
			if err := row.Scan(&v.RecordID, &v.Type, &v.Name, &v.RenewsOn, &value, &v.CompanyID, &v.CompanyName, &v.MRR, &v.HealthBand, &v.OwnerID); err != nil {
				return v, err
			}
			v.Value = json.RawMessage("null")
			if len(value) > 0 {
				v.Value = value
			}
			if v.Type == "account" {
				v.CompanyID, v.CompanyName = nil, nil
			}
			return v, nil
		}); err != nil {
			return err
		}
		out.Renewals = nonNil(out.Renewals)

		// The paying customers whose health score is low, biggest first.
		rows, err = tx.Query(ctx, `SELECT r.id::text, r.record_type, r.name, c.mrr_minor,
				CASE WHEN jsonb_typeof(r.attributes->'scores'->$3) = 'number' THEN (r.attributes->'scores'->>$3)::int END, NULLIF(r.attributes->>'owner', '')
			FROM revenue_customers c JOIN records r ON r.id = c.record_id AND r.deleted_at IS NULL AND r.merged_into IS NULL
			WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.mrr_minor > 0 AND r.attributes->'scores'->>$4 = 'low'
			ORDER BY c.mrr_minor DESC, r.id LIMIT 20`, a.WorkspaceID, a.Brand, revenue.HealthKey, band)
		if err != nil {
			return err
		}
		if out.AtRisk, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (revenueAtRisk, error) {
			var v revenueAtRisk
			return v, row.Scan(&v.RecordID, &v.Type, &v.Name, &v.MRR, &v.Health, &v.OwnerID)
		}); err != nil {
			return err
		}
		out.AtRisk = nonNil(out.AtRisk)

		// Who paid more and who less than in the days before, by the
		// company a person belongs to when there is one.
		rows, err = tx.Query(ctx, `WITH paid AS (
				SELECT record_id,
					COALESCE(sum(brand_amount_minor) FILTER (WHERE occurred_at >= $3), 0)::bigint AS now,
					COALESCE(sum(brand_amount_minor) FILTER (WHERE occurred_at < $3), 0)::bigint AS before
				FROM transactions WHERE workspace_id = $1 AND brand_id = $2 AND status = 'succeeded' AND kind = 'payment'
					AND brand_amount_minor IS NOT NULL AND record_id IS NOT NULL AND occurred_at >= $4 GROUP BY 1)
			SELECT r.id::text, r.record_type, r.name, p.now, p.before FROM paid p
			JOIN records r ON r.id = p.record_id AND r.deleted_at IS NULL AND r.merged_into IS NULL
			WHERE p.now <> p.before ORDER BY abs(p.now - p.before) DESC, r.id LIMIT 200`, a.WorkspaceID, a.Brand, from, from.AddDate(0, 0, -days))
		if err != nil {
			return err
		}
		movers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (revenueMover, error) {
			var v revenueMover
			return v, row.Scan(&v.RecordID, &v.Type, &v.Name, &v.Paid, &v.Previous)
		})
		if err != nil {
			return err
		}
		for _, m := range movers {
			if m.Paid > m.Previous && len(out.Spending.Up) < 10 {
				out.Spending.Up = append(out.Spending.Up, m)
			} else if m.Paid < m.Previous && len(out.Spending.Down) < 10 {
				out.Spending.Down = append(out.Spending.Down, m)
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.Spending.Up, out.Spending.Down = nonNil(out.Spending.Up), nonNil(out.Spending.Down)
	writeJSON(w, http.StatusOK, out)
}
