package api

// Network cost (plans/ad-tracker.md, Z2; the owner's decisions 16 and 17:
// "cost tracking needs to be granular and exact"). For each traffic source
// made from a network template whose network can report spend per target
// (Zeropark), a worker reads, on the workspace's own connection, each
// campaign's spend per target and day, and shares it out over the
// tracker's clicks from that campaign and target:
//
//   - in proportion to the cost each click carried ({visit_cost}), or
//     evenly when none carried any, so one click keeps its own weight;
//   - spend on targets no click named goes evenly over the campaign's
//     clicks that named no reported target ("per target, then even");
//   - what is left (a target's spend with no click at all to put it on)
//     is recorded as unattributed, so the totals can always be checked.
//
// The parts add up to the network's figure to the micro (largest
// remainder). Campaigns match by the network campaign id each click
// carries ({campaign_id}), so nothing is picked by hand. Each day is
// re-read until it is final, so late changes at the network land too.

import (
	"cmp"
	"context"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// networkCostNetworks maps a traffic-source template to the connector
// whose spend it reconciles.
var networkCostNetworks = map[string]string{"zeropark": "zeropark_ads"}

// networkCostDays is how many days back each pass re-reads (today
// included): a network settles a day's spend within a day or two.
const networkCostDays = 3

// RunNetworkCosts reconciles network cost every interval until ctx ends.
func (s *Server) RunNetworkCosts(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.SyncNetworkCosts(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "tracker.network_costs_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type networkCostSource struct {
	id, ws, brand, template    string
	campaignField, targetField int
}

// SyncNetworkCosts runs one pass over every active traffic source whose
// network reports spend, and reports how many campaign days it
// reconciled.
func (s *Server) SyncNetworkCosts(ctx context.Context) (int, error) {
	if s.integrations == nil {
		return 0, nil
	}
	templates := make([]string, 0, len(networkCostNetworks))
	for t := range networkCostNetworks {
		templates = append(templates, t)
	}
	rows, err := s.pool.Query(ctx, `SELECT ts.id::text, ts.workspace_id::text, ts.brand_id::text, ts.template,
			COALESCE((SELECT (p->>'field')::int FROM jsonb_array_elements(ts.parameters) p WHERE p->>'token' = '{campaign_id}' LIMIT 1), 0),
			COALESCE((SELECT (p->>'field')::int FROM jsonb_array_elements(ts.parameters) p WHERE p->>'token' = '{target}' LIMIT 1), 0)
		FROM tracker_traffic_sources ts
		WHERE ts.status = 'active' AND ts.template = ANY($1)
		  AND EXISTS (SELECT 1 FROM tracking_campaigns tc WHERE tc.traffic_source_id = ts.id)`, templates)
	if err != nil {
		return 0, err
	}
	var list []networkCostSource
	var src networkCostSource
	if _, err := pgx.ForEachRow(rows, []any{&src.id, &src.ws, &src.brand, &src.template, &src.campaignField, &src.targetField}, func() error {
		list = append(list, src)
		return nil
	}); err != nil {
		return 0, err
	}
	n := 0
	var first error
	for _, src := range list {
		done, err := s.syncNetworkSource(ctx, src)
		n += done
		if err != nil && first == nil {
			first = err
		}
	}
	return n, first
}

// syncNetworkSource reconciles one traffic source's recent days, holding
// a lock on it so two workers don't do it at once.
func (s *Server) syncNetworkSource(ctx context.Context, src networkCostSource) (int, error) {
	network := src.template
	if src.campaignField == 0 {
		// Without the campaign id on each click nothing can be matched.
		metrics.TrackerNetworkCostSync(network, "no_campaign_token")
		return 0, nil
	}
	list, err := s.integrations.List(ctx, s.pool, src.ws, networkCostNetworks[src.template])
	if err != nil {
		return 0, err
	}
	var integ *connectors.Integration
	for _, i := range connectors.ForBrand(list, src.brand) {
		if i.Status == connectors.StatusConnected {
			integ = &i
			break
		}
	}
	if integ == nil {
		metrics.TrackerNetworkCostSync(network, "not_connected")
		return 0, nil
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('network_cost:' || $1, 0))`, src.id).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, nil // another worker has it
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('network_cost:' || $1, 0))`, src.id)

	o, err := s.integrations.Open(ctx, s.pool, src.ws, integ.ID)
	if err != nil {
		return 0, s.networkCostFailed(ctx, src, err)
	}
	reader, ok1 := o.Connector.(connectors.AdsReader)
	targets, ok2 := o.Connector.(connectors.AdTargetReader)
	if !ok1 || !ok2 {
		metrics.TrackerNetworkCostSync(network, "not_connected")
		return 0, nil
	}
	accts, err := reader.AdAccounts(ctx, o.Client, o.Creds)
	if err != nil || len(accts) == 0 {
		if err == nil {
			err = connectors.Errf(connectors.ErrInvalid, "the connection reads no account")
		}
		return 0, s.networkCostFailed(ctx, src, err)
	}
	acct := accts[0]
	loc := time.UTC
	if acct.TimeZone != "" {
		if l, err := time.LoadLocation(acct.TimeZone); err == nil {
			loc = l
		}
	}
	now := time.Now().In(loc)
	done := 0
	for back := range networkCostDays {
		start := time.Date(now.Year(), now.Month(), now.Day()-back, 0, 0, 0, 0, loc)
		end := start.AddDate(0, 0, 1)
		day := start.Format("2006-01-02")
		refs, err := s.networkCampaignRefs(ctx, src, start, end)
		if err != nil {
			return done, err
		}
		for _, ref := range refs {
			list, err := targets.AdTargetDays(ctx, o.Client, o.Creds, acct, ref, day)
			if err != nil {
				return done, s.networkCostFailed(ctx, src, err)
			}
			if err := s.reconcileNetworkDay(ctx, src, integ.ID, acct.Currency, ref, day, start, end, list); err != nil {
				return done, err
			}
			done++
			metrics.TrackerNetworkCostSync(network, "synced")
		}
	}
	return done, nil
}

func (s *Server) networkCostFailed(ctx context.Context, src networkCostSource, err error) error {
	kind := connectors.KindOf(err)
	metrics.TrackerNetworkCostSync(src.template, "failed_"+kind)
	track.Fail(ctx, s.log, track.Event{Name: "tracker.network_cost_failed", WorkspaceID: src.ws, ActorType: "system", Surface: "system",
		Props: map[string]any{"brand_id": src.brand, "source_id": src.id, "network": src.template, "kind": kind}}, "connector_"+kind, err)
	if kind == connectors.ErrAuth || kind == connectors.ErrQuota {
		return nil // the workspace's own key or credits; it shows on the connection
	}
	return err
}

// networkCampaignRefs lists the network campaign ids the source's clicks
// carried in [start, end).
func (s *Server) networkCampaignRefs(ctx context.Context, src networkCostSource, start, end time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT k.fields->>$4 FROM tracking_link_clicks k
		JOIN tracking_links l ON l.id = k.link_id AND l.workspace_id = k.workspace_id
		JOIN tracking_campaigns tc ON tc.id = l.campaign_id
		WHERE k.workspace_id = $1 AND k.brand_id = $2 AND tc.traffic_source_id = $3 AND k.clicked_at >= $5 AND k.clicked_at < $6
		  AND COALESCE(k.fields->>$4, '') <> ''`, src.ws, src.brand, src.id, strconv.Itoa(src.campaignField), start, end)
	if err != nil {
		return nil, err
	}
	refs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := refs[:0]
	for _, r := range refs {
		if r = strings.TrimSpace(r); r != "" && len(r) <= 100 && !strings.ContainsAny(r, "{}") {
			out = append(out, r) // an unfilled {campaign_id} isn't a campaign
		}
	}
	return out, nil
}

// reconcileNetworkDay shares one campaign day's spend over its clicks and
// records how.
func (s *Server) reconcileNetworkDay(ctx context.Context, src networkCostSource, integ, currency, ref, day string, start, end time.Time, targets []connectors.AdTargetDay) error {
	targetKey := "''"
	if src.targetField > 0 {
		targetKey = "COALESCE(k.fields->>'" + strconv.Itoa(src.targetField) + "', '')"
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT k.id, k.cost_micros, `+targetKey+` FROM tracking_link_clicks k
			JOIN tracking_links l ON l.id = k.link_id AND l.workspace_id = k.workspace_id
			JOIN tracking_campaigns tc ON tc.id = l.campaign_id
			WHERE k.workspace_id = $1 AND k.brand_id = $2 AND tc.traffic_source_id = $3 AND k.clicked_at >= $5 AND k.clicked_at < $6
			  AND k.fields->>$4 = $7
			ORDER BY k.id`, src.ws, src.brand, src.id, strconv.Itoa(src.campaignField), start, end, ref)
		if err != nil {
			return err
		}
		var clicks []networkClick
		var c networkClick
		if _, err := pgx.ForEachRow(rows, []any{&c.id, &c.cost, &c.target}, func() error {
			clicks = append(clicks, c)
			return nil
		}); err != nil {
			return err
		}
		plan := planNetworkCost(clicks, targets)
		if len(plan.ids) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE tracking_link_clicks k SET network_cost_micros = v.cost
				FROM unnest($2::bigint[], $3::bigint[]) AS v(id, cost) WHERE k.id = v.id AND k.workspace_id = $1`, src.ws, plan.ids, plan.costs); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tracker_network_costs (workspace_id, brand_id, source_id, integration_id, network, campaign_ref, day, currency,
				spend_micros, matched_micros, spread_micros, unattributed_micros, clicks, targets)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (source_id, campaign_ref, day) DO UPDATE SET integration_id = $4, currency = $8, spend_micros = $9, matched_micros = $10,
				spread_micros = $11, unattributed_micros = $12, clicks = $13, targets = $14, synced_at = now()`,
			src.ws, src.brand, src.id, integ, src.template, ref, day, cmp.Or(currency, "USD"),
			plan.spend, plan.matched, plan.spread, plan.unattributed, len(clicks), len(targets)); err != nil {
			return err
		}
		metrics.TrackerNetworkCostMicros(src.template, "matched", plan.matched)
		metrics.TrackerNetworkCostMicros(src.template, "spread", plan.spread)
		metrics.TrackerNetworkCostMicros(src.template, "unattributed", plan.unattributed)
		return nil
	})
}

type networkClick struct {
	id     int64
	cost   int64
	target string
}

type networkCostPlan struct {
	ids, costs                           []int64
	spend, matched, spread, unattributed int64
}

// planNetworkCost works out each click's share of a campaign day's
// spend: a target's spend over the clicks that named it, by their own
// cost (evenly when none had one); the spend of targets no click named,
// evenly over the clicks that named no reported target; the rest
// unattributed. Every click of the day gets a value, 0 included, so a
// click whose target spent nothing in the end is corrected too.
func planNetworkCost(clicks []networkClick, targets []connectors.AdTargetDay) networkCostPlan {
	var p networkCostPlan
	byKey := map[string]int{}
	for i, t := range targets {
		p.spend += t.SpendMicros
		for _, k := range t.Keys {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
				if _, ok := byKey[k]; !ok {
					byKey[k] = i
				}
			}
		}
	}
	groups := make([][]int, len(targets))
	var loose []int
	for i, c := range clicks {
		if t, ok := byKey[strings.ToLower(strings.TrimSpace(c.target))]; ok && c.target != "" {
			groups[t] = append(groups[t], i)
		} else {
			loose = append(loose, i)
		}
	}
	cost := make([]int64, len(clicks))
	var left int64
	for t, g := range groups {
		if len(g) == 0 {
			left += targets[t].SpendMicros
			continue
		}
		for j, v := range share(targets[t].SpendMicros, weightsOf(clicks, g)) {
			cost[g[j]] = v
		}
		p.matched += targets[t].SpendMicros
	}
	if left > 0 {
		if len(loose) > 0 {
			for j, v := range share(left, weightsOf(clicks, loose)) {
				cost[loose[j]] = v
			}
			p.spread = left
		} else {
			p.unattributed = left
		}
	}
	p.ids, p.costs = make([]int64, len(clicks)), cost
	for i, c := range clicks {
		p.ids[i] = c.id
	}
	return p
}

func weightsOf(clicks []networkClick, idx []int) []int64 {
	w := make([]int64, len(idx))
	for j, i := range idx {
		w[j] = max(clicks[i].cost, 0)
	}
	return w
}

// share splits total over weights in proportion, by largest remainder, so
// the parts add up to total exactly; all-zero weights share it evenly.
// Ties go to the earlier part.
func share(total int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	if len(weights) == 0 || total <= 0 {
		return out
	}
	var sum int64
	for _, w := range weights {
		sum += w
	}
	if sum <= 0 {
		weights = slices.Repeat([]int64{1}, len(weights))
		sum = int64(len(weights))
	}
	type rem struct {
		i int
		r *big.Int
	}
	rems := make([]rem, len(weights))
	bt, bs := big.NewInt(total), big.NewInt(sum)
	var given int64
	for i, w := range weights {
		q, r := new(big.Int).QuoRem(new(big.Int).Mul(bt, big.NewInt(w)), bs, new(big.Int))
		out[i] = q.Int64()
		given += out[i]
		rems[i] = rem{i, r}
	}
	slices.SortStableFunc(rems, func(a, b rem) int { return b.r.Cmp(a.r) })
	for k := int64(0); k < total-given; k++ {
		out[rems[k%int64(len(rems))].i]++
	}
	return out
}

// ---- The record: what each network reported and where it went ----

func (s *Server) routesNetworkCosts(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/network-costs", s.onBrand(auth.ScopeAnalyticsRead, s.listNetworkCosts))
}

type networkCostView struct {
	SourceID     string    `json:"source_id"`
	Network      string    `json:"network"`
	CampaignRef  string    `json:"campaign_ref"`
	Day          string    `json:"day"`
	Currency     string    `json:"currency"`
	Spend        string    `json:"spend"`
	Matched      string    `json:"matched"`
	Spread       string    `json:"spread"`
	Unattributed string    `json:"unattributed"`
	Clicks       int       `json:"clicks"`
	Targets      int       `json:"targets"`
	SyncedAt     time.Time `json:"synced_at"`
}

func (s *Server) listNetworkCosts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	for _, d := range []string{from, to} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "from and to are days, YYYY-MM-DD")
				return
			}
		}
	}
	source := q.Get("source_id")
	if source != "" && !validUUID(source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source_id is a traffic source's id")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT source_id::text, network, campaign_ref, day::text, currency, spend_micros, matched_micros, spread_micros,
			unattributed_micros, clicks, targets, synced_at
		FROM tracker_network_costs WHERE workspace_id = $1 AND brand_id = $2 AND ($3 = '' OR day >= $3::date) AND ($4 = '' OR day <= $4::date)
		  AND ($5 = '' OR source_id::text = $5)
		ORDER BY day DESC, spend_micros DESC LIMIT 1000`, a.WorkspaceID, a.Brand, from, to, source)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []networkCostView{}
	var v networkCostView
	var spend, matched, spread, unattributed int64
	if _, err := pgx.ForEachRow(rows, []any{&v.SourceID, &v.Network, &v.CampaignRef, &v.Day, &v.Currency, &spend, &matched, &spread, &unattributed,
		&v.Clicks, &v.Targets, &v.SyncedAt}, func() error {
		v.Spend, v.Matched, v.Spread, v.Unattributed = formatMicros(spend), formatMicros(matched), formatMicros(spread), formatMicros(unattributed)
		list = append(list, v)
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
