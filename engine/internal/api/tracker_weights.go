package api

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/routeweights"
)

// Suggested weights (plans/ad-tracker.md, T9): for each list of a link's or
// flow's paths (its landers, and the offers and pages after them), how each
// did over the last days and the weights that would send more clicks to the
// better ones. Nothing changes until a person applies them through the
// weights endpoints, which set only weights; Cloud's auto mode uses the
// same endpoints. Likely bots, suspect clicks and conversions (B7a) and
// declined conversions are left out.

const (
	weightDaysDefault = 14
	weightDaysMax     = 90
)

func (s *Server) routesTrackerWeights(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	link := "/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/"
	mux.HandleFunc("GET "+link+"weight-suggestions", s.onBrand(read, s.linkWeightSuggestions))
	mux.HandleFunc("PATCH "+link+"weights", s.onBrand(manage, s.patchLinkWeights))
	mux.HandleFunc("GET /v1/brands/{brand}/flows/{id}/weight-suggestions", s.onBrand(read, s.flowWeightSuggestions))
	mux.HandleFunc("PATCH /v1/brands/{brand}/flows/{id}/weights", s.onBrand(manage, s.patchFlowWeights))
}

type weightItemView struct {
	DestinationID string  `json:"destination_id"`
	Name          string  `json:"name"`
	URL           string  `json:"url"`
	OfferID       *string `json:"offer_id"`
	LanderID      *string `json:"lander_id"`
	Weight        int     `json:"weight"`
	// SuggestedWeight is the weight to apply; the weight now when the list
	// isn't ready.
	SuggestedWeight int `json:"suggested_weight"`
	// Clicks are the clicks it had in the window that count; ExcludedClicks
	// the likely bots and suspect ones left out.
	Clicks         int64  `json:"clicks"`
	ExcludedClicks int64  `json:"excluded_clicks"`
	Conversions    int64  `json:"conversions"`
	Revenue        string `json:"revenue"`
	// ConversionRate and RevenuePerClick are over Clicks.
	ConversionRate  float64 `json:"conversion_rate"`
	RevenuePerClick string  `json:"revenue_per_click"`
	// ProbabilityBest is its chance to be the best on the metric; nil when
	// the list isn't ready.
	ProbabilityBest *float64 `json:"probability_best"`
}

type weightListView struct {
	// PathID is the path the list belongs to, nil for the default path.
	PathID   *string `json:"path_id"`
	PathName string  `json:"path_name"`
	// Kind is landers (where the path's clicks go first) or destinations
	// (the offers and pages: straight after the click, or after a lander).
	Kind  string `json:"kind"`
	Ready bool   `json:"ready"`
	// Reason says why it isn't ready: too_few_arms, not_enough_clicks or
	// not_enough_conversions.
	Reason *string          `json:"reason"`
	Items  []weightItemView `json:"items"`
}

type weightSuggestionsView struct {
	// LinkID is the link asked about, nil for a flow; FlowID the flow whose
	// weights these are (a link with a flow routes by the flow's).
	LinkID   *string `json:"link_id"`
	FlowID   *string `json:"flow_id"`
	Metric   string  `json:"metric"`
	Days     int     `json:"days"`
	Since    string  `json:"since"`
	Currency string  `json:"currency"`
	// RevenueIncomplete says some conversions' currency had no exchange
	// rate yet, so their revenue is left out.
	RevenueIncomplete bool             `json:"revenue_incomplete"`
	MinClicks         int              `json:"min_clicks"`
	MinConversions    int              `json:"min_conversions"`
	Lists             []weightListView `json:"lists"`
}

// weightQuery reads ?metric= and ?days=.
func weightQuery(r *http.Request) (metric string, days int, err error) {
	q := r.URL.Query()
	metric = q.Get("metric")
	if metric == "" {
		metric = routeweights.Metrics[0]
	}
	if !slices.Contains(routeweights.Metrics, metric) {
		return "", 0, errInvalid("metric is revenue or conversions")
	}
	days = weightDaysDefault
	if v := q.Get("days"); v != "" {
		if days, err = strconv.Atoi(v); err != nil || days < 1 || days > weightDaysMax {
			return "", 0, errInvalid("days is 1 to 90")
		}
	}
	return metric, days, nil
}

func (s *Server) linkWeightSuggestions(w http.ResponseWriter, r *http.Request) {
	_, link, _, _, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	metric, days, err := weightQuery(r)
	if s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	var flow *string
	if err := s.pool.QueryRow(ctx, `SELECT flow_id::text FROM tracking_links WHERE workspace_id = $1 AND id = $2`,
		accessFrom(ctx).WorkspaceID, link).Scan(&flow); err != nil {
		s.internal(w, r, err)
		return
	}
	owner := linkOwner(link)
	if flow != nil {
		owner = flowOwner(*flow)
	}
	s.writeWeightSuggestions(w, r, owner, &link, metric, days)
}

func (s *Server) flowWeightSuggestions(w http.ResponseWriter, r *http.Request) {
	f, err := s.findFlow(r.Context(), r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "flow not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	metric, days, err := weightQuery(r)
	if s.allocError(w, r, err) {
		return
	}
	s.writeWeightSuggestions(w, r, flowOwner(f.ID), nil, metric, days)
}

func (s *Server) writeWeightSuggestions(w http.ResponseWriter, r *http.Request, o routeOwner, link *string, metric string, days int) {
	start := time.Now()
	v, err := s.weightSuggestions(r.Context(), o, link, metric, days, start)
	if err != nil {
		metrics.TrackerWeightSuggestions("error", time.Since(start))
		s.internal(w, r, err)
		return
	}
	metrics.TrackerWeightSuggestions("success", time.Since(start))
	writeJSON(w, http.StatusOK, v)
}

// weightArm is a destination with its numbers.
type weightArm struct {
	view                linkDestinationView
	clicks, excluded    int64
	conversions         int64
	revenue             float64 // in the brand's currency
	entry, afterLanders bool
}

func (s *Server) weightSuggestions(ctx context.Context, o routeOwner, link *string, metric string, days int, now time.Time) (weightSuggestionsView, error) {
	a := accessFrom(ctx)
	ws := a.WorkspaceID
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	v := weightSuggestionsView{LinkID: link, Metric: metric, Days: days, Since: since.UTC().Format(time.RFC3339),
		MinClicks: routeweights.MinClicks, MinConversions: routeweights.MinConversions, Lists: []weightListView{}}
	if o.col == "flow_id" {
		v.FlowID = &o.id
	}
	if err := s.pool.QueryRow(ctx, `SELECT currency FROM brands WHERE workspace_id = $1 AND id = $2`, ws, a.Brand).Scan(&v.Currency); err != nil {
		return v, err
	}

	// The lists: the default path's, then each path's.
	type list struct {
		path  *string
		name  string
		dests []linkDestinationView
	}
	def, err := s.destinationViews(ctx, ws, o, nil)
	if err != nil {
		return v, err
	}
	lists := []list{{name: "Default path", dests: def}}
	paths, err := s.pathViews(ctx, ws, o)
	if err != nil {
		return v, err
	}
	for _, p := range paths {
		lists = append(lists, list{path: &p.ID, name: p.Name, dests: p.Destinations})
	}
	arms := map[string]*weightArm{}
	var entryIDs, onwardIDs, landerIDs []string
	for _, l := range lists {
		landers := slices.ContainsFunc(l.dests, func(d linkDestinationView) bool { return d.LanderID != nil })
		for _, d := range l.dests {
			arm := &weightArm{view: d, entry: !landers || d.LanderID != nil, afterLanders: landers && d.LanderID == nil}
			arms[d.ID] = arm
			if arm.entry {
				entryIDs = append(entryIDs, d.ID)
			} else {
				onwardIDs = append(onwardIDs, d.ID)
			}
			if d.LanderID != nil {
				landerIDs = append(landerIDs, *d.LanderID)
			}
		}
	}

	// The links whose clicks count, and their campaigns: the link, or
	// every link routing by the flow.
	var links, campaigns []string
	rows, err := s.pool.Query(ctx, `SELECT id::text, campaign_id::text FROM tracking_links
		WHERE workspace_id = $1 AND (CASE WHEN $2 = 'flow_id' THEN flow_id = $3::uuid ELSE id = $3::uuid END)`, ws, o.col, o.id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var l, c string
		if err := rows.Scan(&l, &c); err != nil {
			rows.Close()
			return v, err
		}
		links = append(links, l)
		if !slices.Contains(campaigns, c) {
			campaigns = append(campaigns, c)
		}
	}
	if err := rows.Err(); err != nil {
		return v, err
	}

	if len(links) > 0 && len(arms) > 0 {
		count := func(sql string, args ...any) error {
			rows, err := s.pool.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				var n, excluded int64
				if err := rows.Scan(&id, &n, &excluded); err != nil {
					return err
				}
				if arm := arms[id]; arm != nil {
					arm.clicks, arm.excluded = n, excluded
				}
			}
			return rows.Err()
		}
		// Clicks straight to a lander or destination.
		if err := count(`SELECT destination_id::text,
				count(*) FILTER (WHERE NOT likely_bot AND fraud_reasons = '{}'), count(*) FILTER (WHERE likely_bot OR fraud_reasons <> '{}')
			FROM tracking_link_clicks
			WHERE workspace_id = $1 AND link_id = ANY($2::uuid[]) AND clicked_at >= $3 AND destination_id = ANY($4::uuid[])
			GROUP BY 1`, ws, links, since, entryIDs); err != nil {
			return v, err
		}
		// Lander clicks on to a destination, once per click.
		if len(onwardIDs) > 0 && len(landerIDs) > 0 {
			if err := count(`SELECT lc.destination_id::text,
					count(DISTINCT lc.click_id) FILTER (WHERE NOT lc.likely_bot AND k.fraud_reasons = '{}'),
					count(DISTINCT lc.click_id) FILTER (WHERE lc.likely_bot OR k.fraud_reasons <> '{}')
				FROM tracker_lander_clicks lc JOIN tracking_link_clicks k ON k.id = lc.click_id AND k.workspace_id = lc.workspace_id
				WHERE lc.workspace_id = $1 AND lc.lander_id = ANY($2::uuid[]) AND lc.clicked_at >= $3 AND lc.destination_id = ANY($4::uuid[])
				GROUP BY 1`, ws, landerIDs, since, onwardIDs); err != nil {
				return v, err
			}
		}

		// Conversions of the window's clicks, to the destination their
		// click went to and to the one its last lander click went to.
		rows, err := s.pool.Query(ctx, `SELECT k.destination_id::text, lc.destination_id::text, c.payout_micros, c.currency
			FROM tracker_conversions c
			JOIN tracking_link_clicks k ON k.id = c.click_id AND k.workspace_id = c.workspace_id
			LEFT JOIN LATERAL (SELECT l.destination_id FROM tracker_lander_clicks l
				WHERE l.click_key = c.click_key AND l.workspace_id = c.workspace_id AND l.destination_id IS NOT NULL
				ORDER BY l.clicked_at DESC LIMIT 1) lc ON true
			WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.campaign_id = ANY($3::uuid[]) AND c.converted_at >= $4 AND c.clicked_at >= $4
				AND k.link_id = ANY($5::uuid[]) AND c.status <> 'declined' AND c.fraud_reasons = '{}'
				AND NOT k.likely_bot AND k.fraud_reasons = '{}'`, ws, a.Brand, campaigns, since, links)
		if err != nil {
			return v, err
		}
		type money struct {
			arm      *weightArm
			micros   int64
			currency string
		}
		var revenue []money
		for rows.Next() {
			var direct, onward *string
			var micros int64
			var currency string
			if err := rows.Scan(&direct, &onward, &micros, &currency); err != nil {
				rows.Close()
				return v, err
			}
			for _, id := range []*string{direct, onward} {
				if id == nil {
					continue
				}
				if arm := arms[*id]; arm != nil {
					arm.conversions++
					revenue = append(revenue, money{arm, micros, currency})
				}
			}
		}
		if err := rows.Err(); err != nil {
			return v, err
		}
		var currencies []string
		for _, m := range revenue {
			if !slices.Contains(currencies, m.currency) {
				currencies = append(currencies, m.currency)
			}
		}
		rates, err := s.brandRates(ctx, v.Currency, currencies)
		if err != nil {
			return v, err
		}
		for _, m := range revenue {
			rate, ok := rates[m.currency]
			if !ok {
				v.RevenueIncomplete = true
				continue
			}
			m.arm.revenue += float64(m.micros) / 1e6 * rate
		}
	}

	seed := fnv.New64a()
	seed.Write([]byte(o.id + "|" + metric + "|" + strconv.Itoa(days)))
	for _, l := range lists {
		for _, kind := range []string{"landers", "destinations"} {
			var items []*weightArm
			for _, d := range l.dests {
				arm := arms[d.ID]
				if (kind == "landers") == (d.LanderID != nil) {
					items = append(items, arm)
				}
			}
			if len(items) == 0 {
				continue
			}
			in := make([]routeweights.Arm, len(items))
			for i, arm := range items {
				in[i] = routeweights.Arm{Clicks: arm.clicks, Conversions: arm.conversions, Revenue: arm.revenue, Weight: arm.view.Weight}
			}
			res := routeweights.Suggest(in, metric, seed.Sum64())
			lv := weightListView{PathID: l.path, PathName: l.name, Kind: kind, Ready: res.Ready, Items: make([]weightItemView, len(items))}
			if !res.Ready {
				lv.Reason = &res.Reason
			}
			for i, arm := range items {
				it := weightItemView{DestinationID: arm.view.ID, Name: arm.view.Name, URL: arm.view.URL, OfferID: arm.view.OfferID,
					LanderID: arm.view.LanderID, Weight: arm.view.Weight, SuggestedWeight: res.Weights[i],
					Clicks: arm.clicks, ExcludedClicks: arm.excluded, Conversions: arm.conversions,
					Revenue: decimalAmount(arm.revenue), RevenuePerClick: "0"}
				if arm.clicks > 0 {
					it.ConversionRate = math.Round(float64(arm.conversions)/float64(arm.clicks)*1e6) / 1e6
					it.RevenuePerClick = decimalAmount(arm.revenue / float64(arm.clicks))
				}
				if res.Ready {
					p := math.Round(res.Best[i]*1e4) / 1e4
					it.ProbabilityBest = &p
				}
				lv.Items[i] = it
			}
			v.Lists = append(v.Lists, lv)
		}
	}
	return v, nil
}

// decimalAmount writes an amount with at most four decimals.
func decimalAmount(x float64) string {
	return strconv.FormatFloat(math.Round(x*1e4)/1e4, 'f', -1, 64)
}

// brandRates are how many units of the brand's currency one unit of each
// currency is worth, at the latest rates (they only compare), for the
// currencies a rate is known for.
func (s *Server) brandRates(ctx context.Context, brandCurrency string, currencies []string) (map[string]float64, error) {
	out := map[string]float64{brandCurrency: 1}
	var others []string
	for _, c := range currencies {
		if c != brandCurrency {
			others = append(others, c)
		}
	}
	if len(others) == 0 {
		return out, nil
	}
	usd := map[string]float64{"USD": 1}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (currency) currency, usd_per_unit::float8 FROM exchange_rates
		WHERE currency = ANY($1::text[]) ORDER BY currency, day DESC`, append(others, brandCurrency))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var r float64
		if err := rows.Scan(&c, &r); err != nil {
			return nil, err
		}
		usd[c] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	base, ok := usd[brandCurrency]
	if !ok || base <= 0 {
		return out, nil
	}
	for _, c := range others {
		if r, ok := usd[c]; ok {
			out[c] = r / base
		}
	}
	return out, nil
}

// Applying weights.

type weightsInput struct {
	Weights []struct {
		DestinationID string `json:"destination_id"`
		Weight        *int   `json:"weight"`
	} `json:"weights"`
	// Source says where the weights came from, for the product events:
	// suggestion (applied from weight-suggestions) or manual, the default.
	Source string `json:"source"`
}

func (in *weightsInput) check() error {
	switch in.Source {
	case "":
		in.Source = "manual"
	case "manual", "suggestion":
	default:
		return errInvalid("source is suggestion or manual")
	}
	if len(in.Weights) == 0 || len(in.Weights) > maxLinkDestinations*11 {
		return errInvalid("weights has 1 to 220 destination_id and weight pairs")
	}
	seen := map[string]bool{}
	for _, w := range in.Weights {
		if !validUUID(w.DestinationID) {
			return errInvalid("weights[].destination_id is a destination's id")
		}
		if seen[w.DestinationID] {
			return errInvalid("weights names a destination twice")
		}
		seen[w.DestinationID] = true
		if w.Weight == nil || *w.Weight < 0 || *w.Weight > 10000 {
			return errInvalid("weights[].weight is 0 to 10000")
		}
	}
	return nil
}

// saveWeights sets the weights of the owner's destinations in the list,
// leaving everything else about them as it is. It returns errInvalid when
// one isn't the owner's.
func saveWeights(ctx context.Context, tx pgx.Tx, ws string, o routeOwner, in weightsInput) error {
	for _, w := range in.Weights {
		tag, err := tx.Exec(ctx, `UPDATE tracking_link_destinations SET weight = $4, updated_at = now()
			WHERE workspace_id = $1 AND `+o.col+` = $2 AND id = $3 AND removed_at IS NULL`, ws, o.id, w.DestinationID, *w.Weight)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errInvalid("destination " + w.DestinationID + " isn't one of its destinations")
		}
	}
	return nil
}

func (s *Server) patchLinkWeights(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	c, link, url, _, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	var in weightsInput
	if !decode(w, r, &in) || s.allocError(w, r, in.check()) {
		return
	}
	var flow *string
	if err := s.pool.QueryRow(ctx, `SELECT flow_id::text FROM tracking_links WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, link).Scan(&flow); err != nil {
		s.internal(w, r, err)
		return
	}
	if flow != nil {
		writeError(w, r, http.StatusConflict, "conflict", "this link routes by flow "+*flow+"; set the flow's weights instead")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := saveWeights(ctx, tx, a.WorkspaceID, linkOwner(link), in); err != nil {
			return err
		}
		data := map[string]any{"campaign_id": c.ID, "destinations": len(in.Weights), "source": in.Source}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "tracking_link.weights_updated", "tracking_link", link, data,
			map[string]any{"destinations": len(in.Weights), "source": in.Source})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	v, err := s.readLinkDestinations(ctx, a.WorkspaceID, link, url)
	if err == nil {
		v.NeedsDomain, err = s.needsDomain(ctx, c)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) patchFlowWeights(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	f, err := s.findFlow(ctx, r.PathValue("id"))
	if s.flowSaveError(w, r, err) {
		return
	}
	var in weightsInput
	if !decode(w, r, &in) || s.allocError(w, r, in.check()) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := saveWeights(ctx, tx, a.WorkspaceID, flowOwner(f.ID), in); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tracker_flows SET updated_at = now() WHERE id = $1`, f.ID); err != nil {
			return err
		}
		data := map[string]any{"brand_id": a.Brand, "links": f.Links, "destinations": len(in.Weights), "source": in.Source}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "tracker_flow.weights_updated", "tracker_flow", f.ID, data,
			map[string]any{"links": f.Links, "destinations": len(in.Weights), "source": in.Source})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	s.writeFlow(w, r, http.StatusOK, f.ID)
}
