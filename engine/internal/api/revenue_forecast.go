package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/revenue"
	"github.com/outflareai/outflare/engine/internal/scores"
)

// The forecast (docs/revenue.md, "Forecast"): what a brand can expect to
// book in a quarter. What is already won, plus every renewal due in the
// quarter weighted by the company's health, plus every open deal weighted
// by its stage, against the target the brand set. It is arithmetic anyone
// can check line by line; nothing is predicted by a model, and nothing is
// stored but the target and the weights.

func (s *Server) routesRevenueForecast(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/revenue/forecast", s.onBrand(auth.ScopeCustomersRead, s.getRevenueForecast))
	mux.HandleFunc("PUT /v1/brands/{brand}/revenue/forecast", s.onBrand(auth.ScopeCatalogManage, s.setRevenueForecast))
}

// The default share of a renewal's value that is counted, by the health
// band of the company it belongs to. "none" is a company with no score.
var defaultRenewalWeights = map[string]int{"high": 95, "medium": 80, "low": 50, "none": 85}

const forecastLines = 200 // the most renewals or deals listed

type forecastStage struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Weight is the percent of a deal's value counted at this stage.
	Weight int `json:"weight"`
	// Custom is true when the brand set the weight itself.
	Custom bool `json:"custom"`
}

type forecastWeights struct {
	Renewal map[string]int  `json:"renewal"`
	Stages  []forecastStage `json:"stages"`
}

type forecastRenewal struct {
	RecordID    string  `json:"record_id"`
	Type        string  `json:"type"`
	Name        string  `json:"name"`
	CompanyID   *string `json:"company_id"`
	CompanyName *string `json:"company_name"`
	RenewsOn    string  `json:"renews_on"`
	// Late is true when the date has passed and the record still says so.
	Late       bool    `json:"late"`
	Value      int64   `json:"value_minor"`
	HealthBand *string `json:"health_band"`
	Weight     int     `json:"weight"`
	Expected   int64   `json:"expected_minor"`
	// OwnerID is the record's owner, or its company's.
	OwnerID *string `json:"owner_id"`
}

type forecastDeal struct {
	DealID     string  `json:"deal_id"`
	Name       string  `json:"name"`
	RecordID   *string `json:"record_id"`
	RecordName *string `json:"record_name"`
	// Kind is renewal for a deal opened for a renewal (it stands in for
	// the renewal's own line), expansion when the deal is with someone
	// who already pays, else new.
	Kind      string  `json:"kind"`
	Stage     string  `json:"stage"`
	CloseDate *string `json:"close_date"`
	Amount    int64   `json:"amount_minor"`
	Weight    int     `json:"weight"`
	// WeightFrom is score when the weight is the deal's win likelihood (a
	// deal score with the key win), health for a renewal deal (the
	// renewal weight of the company's health band), else stage.
	WeightFrom string `json:"weight_from"`
	Expected   int64  `json:"expected_minor"`
	// OwnerID is the deal's owner, or the owner of the person or company
	// it is with.
	OwnerID *string `json:"owner_id"`
	// CoOwnerID is the deal's second owner, when it has one, and
	// CoOwnerPercent their share of it.
	CoOwnerID      *string  `json:"co_owner_id"`
	CoOwnerPercent *float64 `json:"co_owner_percent"`
}

// forecastOwner is one teammate's share of the forecast: what they won,
// and what is expected from the renewals and open deals they own.
type forecastOwner struct {
	// OwnerID is null for everything nobody owns.
	OwnerID  *string `json:"owner_id"`
	Won      int64   `json:"won_minor"`
	Renewals int64   `json:"renewals_expected_minor"`
	Deals    int64   `json:"deals_expected_minor"`
	Forecast int64   `json:"forecast_minor"`
	BestCase int64   `json:"best_case_minor"`
	// Open counts the renewals and open deals behind the numbers.
	Open int `json:"open"`
}

type forecastSum struct {
	Count    int   `json:"count"`
	Total    int64 `json:"total_minor"`
	Expected int64 `json:"expected_minor"`
}

type revenueForecast struct {
	Quarter  string `json:"quarter"`
	StartsOn string `json:"starts_on"`
	EndsOn   string `json:"ends_on"`
	DaysLeft int    `json:"days_left"`
	Currency string `json:"currency"`
	Target   *int64 `json:"target_minor"`
	// Won is the deals won in the quarter so far.
	Won forecastSum `json:"won"`
	// Collected is payments less refunds in the quarter so far, for
	// comparison; it is not part of the forecast.
	Collected int64       `json:"collected_minor"`
	Renewals  forecastSum `json:"renewals"`
	NewDeals  forecastSum `json:"new_business"`
	Expansion forecastSum `json:"expansion"`
	// Forecast is won plus everything expected; BestCase counts every
	// renewal and open deal in full.
	Forecast int64 `json:"forecast_minor"`
	BestCase int64 `json:"best_case_minor"`
	// Gap is the target less the forecast (negative when ahead), and
	// Attainment the forecast over the target; null with no target.
	Gap          *int64            `json:"gap_minor"`
	Attainment   *float64          `json:"attainment"`
	RenewalLines []forecastRenewal `json:"renewal_lines"`
	DealLines    []forecastDeal    `json:"deal_lines"`
	// Owners splits the forecast by who owns each deal and renewal, the
	// biggest forecast first; it adds up to Forecast and BestCase.
	Owners []forecastOwner `json:"owners"`
	// Skipped counts what could not be valued: deals with no amount,
	// renewals with no value, and amounts in a currency with no rate.
	Skipped struct {
		Deals    int `json:"deals"`
		Renewals int `json:"renewals"`
	} `json:"skipped"`
	Weights forecastWeights `json:"weights"`
}

var quarterRe = regexp.MustCompile(`^(\d{4})-Q([1-4])$`)

// quarterOf names the quarter a day falls in.
func quarterOf(d time.Time) string {
	return fmt.Sprintf("%d-Q%d", d.Year(), (int(d.Month())-1)/3+1)
}

// quarterSpan parses "2026-Q4" into its first and last day.
func quarterSpan(q string) (time.Time, time.Time, bool) {
	m := quarterRe.FindStringSubmatch(q)
	if m == nil {
		return time.Time{}, time.Time{}, false
	}
	year, _ := strconv.Atoi(m[1])
	n, _ := strconv.Atoi(m[2])
	start := time.Date(year, time.Month((n-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 3, -1), true
}

// forecastQuarter reads ?quarter=, which is this quarter or one of the
// next four; the past is the Revenue report's.
func forecastQuarter(q string, today time.Time) (string, time.Time, time.Time, error) {
	if q == "" {
		q = quarterOf(today)
	}
	start, end, ok := quarterSpan(q)
	first, _, _ := quarterSpan(quarterOf(today))
	if !ok || start.Before(first) || start.After(first.AddDate(1, 0, 0)) {
		return "", start, end, errInvalid("quarter is this quarter or one of the next four, such as " + quarterOf(today))
	}
	return q, start, end, nil
}

type forecastSettings struct {
	Targets map[string]int64
	Renewal map[string]int
	Stages  map[string]int
}

func loadForecastSettings(r *http.Request, tx pgx.Tx, a *access) (forecastSettings, error) {
	out := forecastSettings{Targets: map[string]int64{}, Renewal: map[string]int{}, Stages: map[string]int{}}
	var targets, renewal, stages []byte
	err := tx.QueryRow(r.Context(), `SELECT targets, renewal_weights, stage_weights FROM revenue_forecast_settings WHERE brand_id = $1 AND workspace_id = $2`,
		a.Brand, a.WorkspaceID).Scan(&targets, &renewal, &stages)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(targets, &out.Targets); err != nil {
		return out, err
	}
	if err := json.Unmarshal(renewal, &out.Renewal); err != nil {
		return out, err
	}
	return out, json.Unmarshal(stages, &out.Stages)
}

// forecastStages is the brand's open deal stages in order with their
// weights: the brand's own where set, else evenly spread, so four stages
// count 20, 40, 60 and 80 percent.
func forecastStages(c *catalog, custom map[string]int) []forecastStage {
	out := []forecastStage{}
	t := c.recordType("deal")
	if t == nil {
		return out
	}
	for _, f := range t.Fields {
		if f.Key != "stage" || f.OptionSet == nil {
			continue
		}
		set := c.optionSet(*f.OptionSet)
		if set == nil {
			break
		}
		for _, o := range set.Options {
			switch o.Key {
			case "won", "closed_won", "lost", "closed_lost":
				continue
			}
			if !o.Archived {
				out = append(out, forecastStage{Key: o.Key, Label: o.Label})
			}
		}
	}
	for i := range out {
		out[i].Weight = int(math.Round(100 * float64(i+1) / float64(len(out)+1)))
		if w, ok := custom[out[i].Key]; ok {
			out[i].Weight, out[i].Custom = w, true
		}
	}
	return out
}

func weighted(minor int64, percent int) int64 {
	return int64(math.Round(float64(minor) * float64(percent) / 100))
}

func (s *Server) getRevenueForecast(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var out revenueForecast
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		out, err = buildForecast(r, tx, a, r.URL.Query().Get("quarter"))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// splitOwners divides a deal's amount between its owner and its second
// owner, when it has one that isn't the owner again.
func splitOwners(owner *string, co string, percent *float64, v int64) (mine, theirs int64, second *string) {
	first := ""
	if owner != nil {
		first = *owner
	}
	co, pct := coOwnerShare(first, co, percent)
	if co == "" {
		return v, 0, nil
	}
	mine, theirs = splitAmount(v, pct)
	return mine, theirs, &co
}

func buildForecast(r *http.Request, tx pgx.Tx, a *access, quarter string) (revenueForecast, error) {
	ctx := r.Context()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	out := revenueForecast{RenewalLines: []forecastRenewal{}, DealLines: []forecastDeal{}, Owners: []forecastOwner{}}
	owners := map[string]*forecastOwner{}
	// A record given to someone before they had a sign-in carries their
	// directory id: count it under their user id, with the rest of theirs.
	alias := map[string]string{}
	{
		rows, err := tx.Query(ctx, `SELECT id::text, user_id::text FROM staff WHERE workspace_id = $1 AND user_id IS NOT NULL`, a.WorkspaceID)
		if err != nil {
			return out, err
		}
		var from, to string
		if _, err := pgx.ForEachRow(rows, []any{&from, &to}, func() error { alias[from] = to; return nil }); err != nil {
			return out, err
		}
	}
	share := func(id *string) *forecastOwner {
		key := ""
		if id != nil {
			if to, ok := alias[*id]; ok {
				id = &to
			}
			key = *id
		}
		o, ok := owners[key]
		if !ok {
			o = &forecastOwner{OwnerID: id}
			owners[key] = o
		}
		return o
	}
	q, start, end, err := forecastQuarter(quarter, today)
	if err != nil {
		return out, err
	}
	current := !start.After(today)
	out.Quarter, out.StartsOn, out.EndsOn = q, start.Format(time.DateOnly), end.Format(time.DateOnly)
	if current {
		out.DaysLeft = int(end.Sub(today).Hours()/24) + 1
	} else {
		out.DaysLeft = int(end.Sub(start).Hours()/24) + 1
	}
	if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&out.Currency); err != nil {
		return out, err
	}
	set, err := loadForecastSettings(r, tx, a)
	if err != nil {
		return out, err
	}
	if t, ok := set.Targets[q]; ok {
		out.Target = &t
	}
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return out, err
	}
	out.Weights.Stages = forecastStages(c, set.Stages)
	out.Weights.Renewal = map[string]int{}
	for band, v := range defaultRenewalWeights {
		out.Weights.Renewal[band] = v
		if own, ok := set.Renewal[band]; ok {
			out.Weights.Renewal[band] = own
		}
	}
	// A deal's stage is the option's key, or its label on deals that came
	// in from a CRM by name.
	stageWeight, stageKey := map[string]int{}, map[string]string{}
	for _, st := range out.Weights.Stages {
		stageWeight[st.Key] = st.Weight
		stageKey[strings.ToLower(st.Label)] = st.Key
	}
	rates, err := revenue.Rates(ctx, tx)
	if err != nil {
		return out, err
	}
	endExclusive := end.AddDate(0, 0, 1)

	// Won and collected so far, for the quarter we are in.
	if current {
		rows, err := tx.Query(ctx, `SELECT COALESCE(d.amount_cents, 0), d.currency, NULLIF(COALESCE(NULLIF(dr.attributes->>'owner', ''), NULLIF(p.attributes->>'owner', ''),
				(SELECT ac.attributes->>'owner' FROM records ac WHERE ac.workspace_id = d.workspace_id AND ac.deleted_at IS NULL
					AND ac.id::text = dr.attributes->>'associated_company')), ''),
				COALESCE(dr.attributes->>'co_owner', ''), CASE WHEN jsonb_typeof(dr.attributes->'co_owner_percent') = 'number' THEN (dr.attributes->>'co_owner_percent')::float8 END
			FROM deals d
			LEFT JOIN records dr ON dr.id = d.id AND dr.workspace_id = d.workspace_id
			LEFT JOIN records p ON p.id = d.record_id AND p.workspace_id = d.workspace_id AND p.deleted_at IS NULL
			WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.state = 'won' AND d.closed_at >= $3 AND d.closed_at < $4`, a.WorkspaceID, a.Brand, start, endExclusive)
		if err != nil {
			return out, err
		}
		var amount int64
		var code string
		var who *string
		var co string
		var coPercent *float64
		if _, err := pgx.ForEachRow(rows, []any{&amount, &code, &who, &co, &coPercent}, func() error {
			out.Won.Count++
			if v, ok := revenue.Convert(rates, amount, code, out.Currency); ok {
				out.Won.Total += v
				mine, theirs, second := splitOwners(who, co, coPercent, v)
				share(who).Won += mine
				if second != nil {
					share(second).Won += theirs
				}
			}
			return nil
		}); err != nil {
			return out, err
		}
		out.Won.Expected = out.Won.Total
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN kind = 'refund' THEN -brand_amount_minor ELSE brand_amount_minor END), 0)::bigint FROM transactions
			WHERE workspace_id = $1 AND brand_id = $2 AND status = 'succeeded' AND kind IN ('payment', 'refund') AND brand_amount_minor IS NOT NULL
				AND occurred_at >= $3 AND occurred_at < $4`, a.WorkspaceID, a.Brand, start, endExclusive).Scan(&out.Collected); err != nil {
			return out, err
		}
	}

	// Renewals due in the quarter; in the current one, also those up to
	// 30 days late that still carry their old date.
	from := start
	if current {
		from = today.AddDate(0, 0, -30)
		if start.Before(from) {
			from = start
		}
	}
	band := revenue.HealthKey + "_band"
	rows, err := tx.Query(ctx, `SELECT r.id::text, r.record_type, r.name, r.attributes->>'renews_on',
			(r.attributes->'value'->>'amount_minor')::numeric::bigint, r.attributes->'value'->>'currency',
			c.id::text, c.name, rc.mrr_minor, c.attributes->'scores'->>$5, NULLIF(COALESCE(NULLIF(r.attributes->>'owner', ''), c.attributes->>'owner'), '')
		FROM records r
		LEFT JOIN records c ON c.workspace_id = r.workspace_id AND c.brand_id = r.brand_id AND c.deleted_at IS NULL AND c.merged_into IS NULL
			AND c.id::text = CASE WHEN r.record_type = 'account' THEN r.id::text ELSE r.attributes->>'company' END
		LEFT JOIN revenue_customers rc ON rc.record_id = c.id AND rc.mrr_minor > 0
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL
			AND r.attributes->>'renews_on' ~ '^\d{4}-\d{2}-\d{2}$'
			AND r.attributes->>'renews_on' >= $3 AND r.attributes->>'renews_on' <= $4
			AND COALESCE(r.attributes->>'stage', '') NOT IN ('ended', 'cancelled', 'canceled', 'lost')
			AND jsonb_typeof(COALESCE(r.attributes->'value'->'amount_minor', '0'::jsonb)) = 'number'
			-- A renewal with a deal of its own is counted as that deal.
			AND NOT EXISTS (SELECT 1 FROM deals rd WHERE rd.workspace_id = r.workspace_id AND rd.renewal_of = r.id
				AND rd.renewal_on = (r.attributes->>'renews_on')::date)
		ORDER BY r.attributes->>'renews_on', r.id`, a.WorkspaceID, a.Brand, from.Format(time.DateOnly), end.Format(time.DateOnly), band)
	if err != nil {
		return out, err
	}
	{
		var l forecastRenewal
		var value, mrr *int64
		var code *string
		if _, err := pgx.ForEachRow(rows, []any{&l.RecordID, &l.Type, &l.Name, &l.RenewsOn, &value, &code, &l.CompanyID, &l.CompanyName, &mrr, &l.HealthBand, &l.OwnerID}, func() error {
			line := l
			switch {
			case value != nil && *value > 0:
				from := out.Currency
				if code != nil && *code != "" {
					from = *code
				}
				v, ok := revenue.Convert(rates, *value, from, out.Currency)
				if !ok {
					out.Skipped.Renewals++
					return nil
				}
				line.Value = v
			case mrr != nil:
				// A company with a renewal date and no agreement value
				// renews a year of what it pays a month.
				line.Value = *mrr * 12
			default:
				out.Skipped.Renewals++
				return nil
			}
			if line.Type == "account" {
				line.CompanyID, line.CompanyName = nil, nil
			}
			key := "none"
			if line.HealthBand != nil && *line.HealthBand != "" {
				key = *line.HealthBand
			}
			line.Weight = out.Weights.Renewal[key]
			line.Expected = weighted(line.Value, line.Weight)
			line.Late = line.RenewsOn < today.Format(time.DateOnly)
			out.Renewals.Count++
			out.Renewals.Total += line.Value
			out.Renewals.Expected += line.Expected
			o := share(line.OwnerID)
			o.Renewals += line.Expected
			o.BestCase += line.Value
			o.Open++
			out.RenewalLines = append(out.RenewalLines, line)
			return nil
		}); err != nil {
			return out, err
		}
	}

	// Open deals: those dated in the quarter and, in the current one,
	// those with no close date or one that has passed.
	rows, err = tx.Query(ctx, `SELECT d.id::text, d.name, d.stage, d.amount_cents, d.currency, p.id::text, p.name, NULLIF(dr.attributes->>'close_date', ''),
			CASE WHEN jsonb_typeof(dr.attributes->'scores'->$3) = 'number' THEN (dr.attributes->'scores'->>$3)::numeric::int END,
			NULLIF(COALESCE(NULLIF(dr.attributes->>'owner', ''), NULLIF(p.attributes->>'owner', ''),
				(SELECT ac.attributes->>'owner' FROM records ac WHERE ac.workspace_id = d.workspace_id AND ac.deleted_at IS NULL
					AND ac.id::text = dr.attributes->>'associated_company')), ''),
			COALESCE(dr.attributes->>'co_owner', ''), CASE WHEN jsonb_typeof(dr.attributes->'co_owner_percent') = 'number' THEN (dr.attributes->>'co_owner_percent')::float8 END,
			d.renewal_of IS NOT NULL,
			(SELECT COALESCE(rc.attributes->'scores'->>$4, '') FROM records rn
				LEFT JOIN records rc ON rc.workspace_id = rn.workspace_id AND rc.deleted_at IS NULL AND rc.merged_into IS NULL
					AND rc.id::text = CASE WHEN rn.record_type = 'account' THEN rn.id::text ELSE rn.attributes->>'company' END
				WHERE rn.id = d.renewal_of AND rn.workspace_id = d.workspace_id),
			EXISTS (SELECT 1 FROM revenue_customers rc WHERE rc.workspace_id = d.workspace_id AND rc.brand_id = d.brand_id AND rc.mrr_minor > 0
				AND (rc.record_id = d.record_id OR rc.record_id IN (SELECT l.to_id FROM record_relationships l
					WHERE l.workspace_id = d.workspace_id AND l.from_id = d.record_id AND l.kind = 'companies' AND l.valid_to IS NULL)))
		FROM deals d
		LEFT JOIN records dr ON dr.id = d.id AND dr.workspace_id = d.workspace_id
		LEFT JOIN records p ON p.id = d.record_id AND p.workspace_id = d.workspace_id AND p.deleted_at IS NULL
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.state = 'open'
		ORDER BY d.amount_cents DESC NULLS LAST, d.id`, a.WorkspaceID, a.Brand, scores.WinKey, band)
	if err != nil {
		return out, err
	}
	{
		var l forecastDeal
		var amount *int64
		var code string
		var paying bool
		var win *int
		var renewal bool
		var renewalBand *string
		var co string
		var coPercent *float64
		if _, err := pgx.ForEachRow(rows, []any{&l.DealID, &l.Name, &l.Stage, &amount, &code, &l.RecordID, &l.RecordName, &l.CloseDate, &win, &l.OwnerID, &co, &coPercent, &renewal, &renewalBand, &paying}, func() error {
			line := l
			if line.CloseDate != nil {
				day := *line.CloseDate
				if len(day) > 10 {
					day = day[:10]
				}
				line.CloseDate = &day
				if day > out.EndsOn || (!current && day < out.StartsOn) {
					return nil
				}
			} else if !current {
				return nil
			}
			if amount == nil || *amount <= 0 {
				out.Skipped.Deals++
				return nil
			}
			v, ok := revenue.Convert(rates, *amount, code, out.Currency)
			if !ok {
				out.Skipped.Deals++
				return nil
			}
			line.Amount = v
			if key, ok := stageKey[strings.ToLower(line.Stage)]; ok && stageWeight[line.Stage] == 0 {
				line.Stage = key
			}
			line.Weight = 50 // a stage the brand's list no longer has
			if w, ok := stageWeight[line.Stage]; ok {
				line.Weight = w
			}
			line.WeightFrom = "stage"
			// A brand that scores its deals counts each at its own
			// chance of being won: the stage is one factor of it.
			if win != nil {
				line.Weight, line.WeightFrom = min(100, max(0, *win)), "score"
			}
			sum := &out.NewDeals
			line.Kind = "new"
			switch {
			case renewal:
				// A renewal deal counts as its renewal would: by the
				// health of the company, not by how far the deal has moved.
				key := "none"
				if renewalBand != nil && *renewalBand != "" {
					key = *renewalBand
				}
				sum, line.Kind, line.Weight, line.WeightFrom = &out.Renewals, "renewal", out.Weights.Renewal[key], "health"
			case paying:
				sum, line.Kind = &out.Expansion, "expansion"
			}
			line.Expected = weighted(v, line.Weight)
			sum.Count++
			sum.Total += v
			sum.Expected += line.Expected
			// A deal with two owners counts under each by their share.
			mine, theirs, second := splitOwners(line.OwnerID, co, coPercent, line.Expected)
			best, bestTheirs, _ := splitOwners(line.OwnerID, co, coPercent, v)
			line.CoOwnerID, line.CoOwnerPercent = second, nil
			if second != nil {
				_, pct := coOwnerShare("", *second, coPercent)
				line.CoOwnerPercent = &pct
			}
			credit := func(o *forecastOwner, expected, bestCase int64) {
				if renewal {
					o.Renewals += expected
				} else {
					o.Deals += expected
				}
				o.BestCase += bestCase
				o.Open++
			}
			credit(share(line.OwnerID), mine, best)
			if second != nil {
				credit(share(second), theirs, bestTheirs)
			}
			out.DealLines = append(out.DealLines, line)
			return nil
		}); err != nil {
			return out, err
		}
	}
	sort.SliceStable(out.DealLines, func(i, j int) bool { return out.DealLines[i].Expected > out.DealLines[j].Expected })
	if len(out.RenewalLines) > forecastLines {
		out.RenewalLines = out.RenewalLines[:forecastLines]
	}
	if len(out.DealLines) > forecastLines {
		out.DealLines = out.DealLines[:forecastLines]
	}

	for _, o := range owners {
		o.Forecast = o.Won + o.Renewals + o.Deals
		o.BestCase += o.Won
		out.Owners = append(out.Owners, *o)
	}
	// The biggest forecast first; what nobody owns last.
	sort.Slice(out.Owners, func(i, j int) bool {
		x, y := out.Owners[i], out.Owners[j]
		if (x.OwnerID == nil) != (y.OwnerID == nil) {
			return y.OwnerID == nil
		}
		if x.Forecast != y.Forecast {
			return x.Forecast > y.Forecast
		}
		return x.OwnerID != nil && *x.OwnerID < *y.OwnerID
	})

	out.Forecast = out.Won.Total + out.Renewals.Expected + out.NewDeals.Expected + out.Expansion.Expected
	out.BestCase = out.Won.Total + out.Renewals.Total + out.NewDeals.Total + out.Expansion.Total
	if out.Target != nil {
		gap := *out.Target - out.Forecast
		out.Gap = &gap
		if *out.Target > 0 {
			share := float64(out.Forecast) / float64(*out.Target)
			out.Attainment = &share
		}
	}
	return out, nil
}

// setRevenueForecast saves a quarter's target and, when given, the
// weights. A null target removes it; a null weight goes back to the
// default.
func (s *Server) setRevenueForecast(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Quarter        string          `json:"quarter"`
		Target         json.RawMessage `json:"target_minor"`
		RenewalWeights map[string]*int `json:"renewal_weights"`
		StageWeights   map[string]*int `json:"stage_weights"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	var out revenueForecast
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q, _, _, err := forecastQuarter(in.Quarter, today)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO revenue_forecast_settings (brand_id, workspace_id) VALUES ($1, $2) ON CONFLICT (brand_id) DO NOTHING`, a.Brand, a.WorkspaceID); err != nil {
			return err
		}
		// Lock the row, so two saves don't lose each other's part.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM revenue_forecast_settings WHERE brand_id = $1 AND workspace_id = $2 FOR UPDATE`, a.Brand, a.WorkspaceID); err != nil {
			return err
		}
		set, err := loadForecastSettings(r, tx, a)
		if err != nil {
			return err
		}
		if len(in.Target) > 0 {
			if string(in.Target) == "null" {
				delete(set.Targets, q)
			} else {
				var t int64
				if err := json.Unmarshal(in.Target, &t); err != nil || t < 0 || t > 1e15 {
					return errInvalid("target_minor is the quarter's target in the brand's minor unit (cents), or null to remove it")
				}
				set.Targets[q] = t
			}
			// Quarters gone by are dropped, so the row stays small.
			first := quarterOf(today)
			for k := range set.Targets {
				if k < first {
					delete(set.Targets, k)
				}
			}
		}
		for band, v := range in.RenewalWeights {
			if _, ok := defaultRenewalWeights[band]; !ok {
				return errInvalid("renewal_weights has the keys high, medium, low and none")
			}
			if v == nil {
				delete(set.Renewal, band)
				continue
			}
			if *v < 0 || *v > 100 {
				return errInvalid("a weight is a percent from 0 to 100")
			}
			set.Renewal[band] = *v
		}
		if len(in.StageWeights) > 0 {
			c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
			if err != nil {
				return err
			}
			known := map[string]bool{}
			for _, st := range forecastStages(c, nil) {
				known[st.Key] = true
			}
			for key, v := range in.StageWeights {
				if v == nil {
					delete(set.Stages, key)
					continue
				}
				if !known[key] {
					return errInvalid("stage_weights has the keys of the brand's open deal stages; " + key + " is not one")
				}
				if *v < 0 || *v > 100 {
					return errInvalid("a weight is a percent from 0 to 100")
				}
				set.Stages[key] = *v
			}
		}
		targets, _ := json.Marshal(set.Targets)
		renewal, _ := json.Marshal(set.Renewal)
		stages, _ := json.Marshal(set.Stages)
		if _, err := tx.Exec(ctx, `UPDATE revenue_forecast_settings SET targets = $3, renewal_weights = $4, stage_weights = $5, updated_at = now()
			WHERE brand_id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID, targets, renewal, stages); err != nil {
			return err
		}
		if out, err = buildForecast(r, tx, a, q); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "revenue_forecast.updated", "brand", a.Brand,
			map[string]any{"quarter": q, "has_target": out.Target != nil, "custom_weights": len(set.Renewal) + len(set.Stages)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
