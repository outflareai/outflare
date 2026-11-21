package api

// Per-target results (plans/ad-tracker.md, B8: placement blocks): for the
// traffic sources whose network reports spend per target (Zeropark), what
// each target of each network campaign did over a range of days: the
// tracker's clicks, their cost (each click's share of the network's
// spend, else its own cost token), the conversions and payouts they
// brought, and whether the target is paused. It is what a person, or a
// rule in an extension, reads before pausing a target that loses money
// (POST /v1/brands/{brand}/ads/campaigns/{id}/exclusions).

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// networkTargetLimit is how many targets one answer lists, most costly
// first.
const networkTargetLimit = 500

type networkTargetView struct {
	SourceID    string `json:"source_id"`
	Network     string `json:"network"`
	CampaignRef string `json:"campaign_ref"`
	// AdCampaignID is the network campaign as Paid ads knows it, when its
	// connection has synced it: the id the exclusions endpoint takes.
	AdCampaignID *string `json:"ad_campaign_id"`
	CampaignName string  `json:"campaign_name"`
	Target       string  `json:"target"`
	Clicks       int64   `json:"clicks"`
	Conversions  int64   `json:"conversions"`
	Currency     string  `json:"currency"`
	Cost         string  `json:"cost"`
	Revenue      string  `json:"revenue"`
	Profit       string  `json:"profit"`
	// ROI is profit over cost; null when nothing was spent.
	ROI *float64 `json:"roi"`
	// State is the target as the network last listed it (active, paused,
	// or stopped when the network holds it back), or as a change made
	// here since says: pausing and resuming while one waits.
	State string `json:"state"`
	// StateDetail is the network's own word for a stopped target.
	StateDetail string `json:"state_detail"`
	// Bid is the target's bid as the network last listed it, or as a bid
	// change applied here since; null when neither is known. AutoBid says
	// the network sets it itself. BidQueued is a bid change that waits.
	Bid       *string `json:"bid"`
	AutoBid   bool    `json:"auto_bid"`
	BidQueued *string `json:"bid_queued"`
	MinBid    *string `json:"min_bid"`
	// SeenAt is when the network last listed the target; null when it
	// hasn't.
	SeenAt *time.Time `json:"seen_at"`
}

func (s *Server) listNetworkTargets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	var tz string
	if err := s.pool.QueryRow(ctx, `SELECT timezone FROM brands WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, a.Brand).Scan(&tz); err != nil {
		s.internal(w, r, err)
		return
	}
	loc := zone(tz)
	now := time.Now().In(loc)
	day := func(name string, def time.Time) (time.Time, bool) {
		v := q.Get(name)
		if v == "" {
			return def, true
		}
		t, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "from and to are days, YYYY-MM-DD")
			return t, false
		}
		return t, true
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	from, ok := day("from", today.AddDate(0, 0, -6))
	if !ok {
		return
	}
	to, ok := day("to", today)
	if !ok {
		return
	}
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "to is on or after from, and at most a year later")
		return
	}
	source, ref := q.Get("source_id"), strings.TrimSpace(q.Get("campaign_ref"))
	if source != "" && !validUUID(source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source_id is a traffic source's id")
		return
	}
	templates := make([]string, 0, len(networkCostNetworks))
	for t := range networkCostNetworks {
		templates = append(templates, t)
	}
	rows, err := s.pool.Query(ctx, `SELECT ts.id::text, ts.workspace_id::text, ts.brand_id::text, ts.template, ts.currency,
			COALESCE((SELECT (p->>'field')::int FROM jsonb_array_elements(ts.parameters) p WHERE p->>'token' = '{campaign_id}' LIMIT 1), 0),
			COALESCE((SELECT (p->>'field')::int FROM jsonb_array_elements(ts.parameters) p WHERE p->>'token' = '{target}' LIMIT 1), 0)
		FROM tracker_traffic_sources ts
		WHERE ts.workspace_id = $1 AND ts.brand_id = $2 AND ts.status = 'active' AND ts.template = ANY($3) AND ($4 = '' OR ts.id::text = $4)`,
		a.WorkspaceID, a.Brand, templates, source)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type src struct {
		networkCostSource
		currency string
	}
	var sources []src
	var one src
	if _, err := pgx.ForEachRow(rows, []any{&one.id, &one.ws, &one.brand, &one.template, &one.currency, &one.campaignField, &one.targetField}, func() error {
		sources = append(sources, one)
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	list := []networkTargetView{}
	for _, sc := range sources {
		if sc.campaignField == 0 || sc.targetField == 0 {
			continue // its campaign URL doesn't carry the campaign or the target
		}
		rows, err := s.pool.Query(ctx, `SELECT k.fields->>$4 AS ref, COALESCE(k.fields->>$5, '') AS target, count(*),
				COALESCE(sum(COALESCE(k.network_cost_micros, k.cost_micros)), 0)::bigint, COALESCE(sum(cv.n), 0)::bigint, COALESCE(sum(cv.payout), 0)::bigint
			FROM tracking_link_clicks k
			JOIN tracking_links l ON l.id = k.link_id AND l.workspace_id = k.workspace_id
			JOIN tracking_campaigns tc ON tc.id = l.campaign_id
			LEFT JOIN LATERAL (SELECT count(*) AS n, COALESCE(sum(c.payout_micros) FILTER (WHERE c.currency = $9), 0) AS payout
				FROM tracker_conversions c WHERE c.click_id = k.id AND c.workspace_id = k.workspace_id AND c.status <> 'declined') cv ON true
			WHERE k.workspace_id = $1 AND k.brand_id = $2 AND tc.traffic_source_id = $3 AND k.clicked_at >= $6 AND k.clicked_at < $7
			  AND COALESCE(k.fields->>$4, '') <> '' AND COALESCE(k.fields->>$5, '') <> '' AND ($8 = '' OR k.fields->>$4 = $8)
			GROUP BY 1, 2 ORDER BY 4 DESC, 3 DESC, 2 LIMIT $10`,
			sc.ws, sc.brand, sc.id, strconv.Itoa(sc.campaignField), strconv.Itoa(sc.targetField), from, to.AddDate(0, 0, 1), ref, sc.currency, networkTargetLimit)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		var v networkTargetView
		var cost, payout int64
		if _, err := pgx.ForEachRow(rows, []any{&v.CampaignRef, &v.Target, &v.Clicks, &cost, &v.Conversions, &payout}, func() error {
			v.SourceID, v.Network, v.Currency, v.State = sc.id, sc.template, sc.currency, "active"
			v.Cost, v.Revenue, v.Profit = formatMicros(cost), formatMicros(payout), formatMicros(payout-cost)
			v.ROI = nil
			if cost > 0 {
				roi := float64(payout-cost) / float64(cost)
				v.ROI = &roi
			}
			list = append(list, v)
			return nil
		}); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if err := s.networkTargetStates(ctx, a, list); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.networkTargetCampaigns(r, a, list); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "from": from.Format("2006-01-02"), "to": to.Format("2006-01-02")})
}

// networkTargetStates fills in each row's state and bid as the network
// last listed them (tracker_network_targets).
func (s *Server) networkTargetStates(ctx context.Context, a *access, list []networkTargetView) error {
	if len(list) == 0 {
		return nil
	}
	at := map[string][]int{} // source, campaign and target to its rows
	var sources, refs, targets []string
	for i, v := range list {
		k := v.SourceID + "|" + v.CampaignRef + "|" + v.Target
		at[k] = append(at[k], i)
		if !slices.Contains(sources, v.SourceID) {
			sources = append(sources, v.SourceID)
		}
		if !slices.Contains(refs, v.CampaignRef) {
			refs = append(refs, v.CampaignRef)
		}
		targets = append(targets, v.Target)
	}
	rows, err := s.pool.Query(ctx, `SELECT t.source_id::text, t.campaign_ref, t.keys, t.state, t.state_detail, t.bid_micros, t.auto_bid, t.min_bid_micros, t.seen_at
		FROM tracker_network_targets t
		WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.source_id = ANY($3::uuid[]) AND t.campaign_ref = ANY($4) AND t.keys && $5::text[]
		ORDER BY t.seen_at`, a.WorkspaceID, a.Brand, sources, refs, targets)
	if err != nil {
		return err
	}
	var source, ref, state, detail string
	var keys []string
	var bid, minBid *int64
	var auto bool
	var seen time.Time
	_, err = pgx.ForEachRow(rows, []any{&source, &ref, &keys, &state, &detail, &bid, &auto, &minBid, &seen}, func() error {
		for _, k := range keys {
			for _, i := range at[source+"|"+ref+"|"+k] {
				v := &list[i]
				when := seen
				v.State, v.StateDetail, v.AutoBid, v.SeenAt = state, detail, auto, &when
				v.Bid, v.MinBid = nil, nil
				if bid != nil {
					b := formatMicros(*bid)
					v.Bid = &b
				}
				if minBid != nil {
					m := formatMicros(*minBid)
					v.MinBid = &m
				}
			}
		}
		return nil
	})
	return err
}

// networkTargetCampaigns fills in each row's campaign as Paid ads knows
// it (the brand's own or a shared account's), and lays the changes made
// here over what the network last listed: a change that waits always
// shows, an applied one until the network has been read again.
func (s *Server) networkTargetCampaigns(r *http.Request, a *access, list []networkTargetView) error {
	if len(list) == 0 {
		return nil
	}
	ctx := r.Context()
	var refs, connectorsOf []string
	for _, v := range list {
		if !slices.Contains(refs, v.CampaignRef) {
			refs = append(refs, v.CampaignRef)
		}
	}
	for _, c := range networkCostNetworks {
		connectorsOf = append(connectorsOf, c)
	}
	rows, err := s.pool.Query(ctx, `SELECT e.external_id, e.id::text, e.name, a.connector FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE e.workspace_id = $1 AND e.level = 'campaign' AND e.external_id = ANY($2) AND a.connector = ANY($3) AND a.status = 'active'
		  AND (COALESCE(e.brand_id, a.brand_id) IS NULL OR COALESCE(e.brand_id, a.brand_id) = $4)`, a.WorkspaceID, refs, connectorsOf, a.Brand)
	if err != nil {
		return err
	}
	type camp struct{ id, name string }
	camps := map[string]camp{}
	var ext, id, name, connector string
	if _, err := pgx.ForEachRow(rows, []any{&ext, &id, &name, &connector}, func() error {
		camps[connector+"|"+ext] = camp{id, name}
		return nil
	}); err != nil {
		return err
	}
	var ids []string
	at := map[string][]int{} // campaign and target to its rows
	for i := range list {
		if c, ok := camps[networkCostNetworks[list[i].Network]+"|"+list[i].CampaignRef]; ok {
			id := c.id
			list[i].AdCampaignID, list[i].CampaignName = &id, c.name
			ids = append(ids, id)
			at[id+"|"+list[i].Target] = append(at[id+"|"+list[i].Target], i)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// The latest pause or resume of each target, and its latest bid.
	rows, err = s.pool.Query(ctx, `SELECT * FROM (
			SELECT DISTINCT ON (c.ad_entity_id, c.after->'exclusion'->>'value') c.ad_entity_id::text, c.after->'exclusion'->>'value', c.kind, c.status,
				COALESCE(c.applied_at, c.created_at), 0::bigint
			FROM ad_changes c WHERE c.workspace_id = $1 AND c.ad_entity_id = ANY($2::uuid[]) AND c.kind IN ('exclusion_add', 'exclusion_remove')
			  AND c.status IN ('queued', 'applied') AND c.after->'exclusion'->>'kind' = $3
			ORDER BY c.ad_entity_id, c.after->'exclusion'->>'value', c.created_at DESC) x
		UNION ALL SELECT * FROM (
			SELECT DISTINCT ON (c.ad_entity_id, c.after->'bid'->>'placement') c.ad_entity_id::text, c.after->'bid'->>'placement', c.kind, c.status,
				COALESCE(c.applied_at, c.created_at), COALESCE((c.after->'bid'->>'micros')::bigint, 0)
			FROM ad_changes c WHERE c.workspace_id = $1 AND c.ad_entity_id = ANY($2::uuid[]) AND c.kind = 'bid'
			  AND c.status IN ('queued', 'applied') AND COALESCE(c.after->'bid'->>'placement', '') <> ''
			ORDER BY c.ad_entity_id, c.after->'bid'->>'placement', c.created_at DESC) y`, a.WorkspaceID, ids, connectors.ExcludePlacement)
	if err != nil {
		return err
	}
	var value, kind, status string
	var when time.Time
	var micros int64
	_, err = pgx.ForEachRow(rows, []any{&id, &value, &kind, &status, &when, &micros}, func() error {
		for _, i := range at[id+"|"+value] {
			v := &list[i]
			// The network's listing is newer than an applied change: it
			// already shows it, or what someone did there since.
			stale := status == "applied" && v.SeenAt != nil && v.SeenAt.After(when)
			switch {
			case kind == adsuggest.ChangeBid && status == "queued":
				b := formatMicros(micros)
				v.BidQueued = &b
			case kind == adsuggest.ChangeBid:
				if !stale {
					b := formatMicros(micros)
					v.Bid, v.AutoBid = &b, false
				}
			case status == "queued" && kind == "exclusion_add":
				v.State, v.StateDetail = "pausing", ""
			case status == "queued":
				v.State, v.StateDetail = "resuming", ""
			case stale:
			case kind == "exclusion_add":
				v.State, v.StateDetail = connectors.AdTargetPaused, ""
			default:
				v.State, v.StateDetail = connectors.AdTargetActive, ""
			}
		}
		return nil
	})
	return err
}

// networkTargetKeep is how long a target the network no longer lists
// keeps its row.
const networkTargetKeep = 90 * 24 * time.Hour

// networkTargetBatch is how many targets one statement writes.
const networkTargetBatch = 2000

// syncNetworkTargets stores, for each campaign the source's recent clicks
// named, the state and bid the network holds for its targets now, so the
// Targets table shows a target paused in the network's own panel and the
// bid a change would replace.
func (s *Server) syncNetworkTargets(ctx context.Context, src networkCostSource, lister connectors.AdTargetStateReader, o *connectors.Opened, acct connectors.AdAccount, refs []string) error {
	type row struct {
		ID          string   `json:"id"`
		Keys        []string `json:"keys"`
		State       string   `json:"state"`
		StateDetail string   `json:"state_detail"`
		BidMicros   *int64   `json:"bid_micros"`
		Auto        bool     `json:"auto"`
		MinMicros   *int64   `json:"min_bid_micros"`
	}
	clip := func(v string, n int) string {
		if len(v) > n {
			return strings.ToValidUTF8(v[:n], "")
		}
		return v
	}
	for _, ref := range refs {
		list, err := lister.AdTargetStates(ctx, o.Client, o.Creds, acct, ref)
		if err != nil {
			return s.networkCostFailed(ctx, src, err)
		}
		seen := map[string]bool{}
		rows := make([]row, 0, len(list))
		for _, t := range list {
			id := clip(strings.TrimSpace(t.ID), 300)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			r := row{ID: id, State: t.State, StateDetail: clip(t.StateDetail, 60), Auto: t.Bid.Auto}
			switch r.State {
			case connectors.AdTargetActive, connectors.AdTargetPaused, connectors.AdTargetStopped:
			default:
				r.State = connectors.AdTargetActive
			}
			for _, k := range t.Keys {
				if k = clip(strings.TrimSpace(k), 300); k != "" {
					r.Keys = append(r.Keys, k)
				}
			}
			if t.Bid.Micros > 0 {
				r.BidMicros = &t.Bid.Micros
			}
			if t.MinBidMicros > 0 {
				r.MinMicros = &t.MinBidMicros
			}
			rows = append(rows, r)
		}
		for from := 0; from < len(rows); from += networkTargetBatch {
			raw, err := json.Marshal(rows[from:min(len(rows), from+networkTargetBatch)])
			if err != nil {
				return err
			}
			if _, err := s.pool.Exec(ctx, `INSERT INTO tracker_network_targets (workspace_id, brand_id, source_id, network, campaign_ref, target_ref, keys,
					state, state_detail, bid_micros, auto_bid, min_bid_micros)
				SELECT $1, $2, $3, $4, $5, t.id, ARRAY(SELECT jsonb_array_elements_text(COALESCE(t.keys, '[]'))), t.state, t.state_detail, t.bid_micros, t.auto, t.min_bid_micros
				FROM jsonb_to_recordset($6::jsonb) AS t(id text, keys jsonb, state text, state_detail text, bid_micros bigint, auto boolean, min_bid_micros bigint)
				ON CONFLICT (source_id, campaign_ref, target_ref) DO UPDATE SET keys = EXCLUDED.keys, state = EXCLUDED.state, state_detail = EXCLUDED.state_detail,
					bid_micros = EXCLUDED.bid_micros, auto_bid = EXCLUDED.auto_bid, min_bid_micros = EXCLUDED.min_bid_micros, seen_at = now()`,
				src.ws, src.brand, src.id, src.template, ref, string(raw)); err != nil {
				return err
			}
		}
		metrics.TrackerNetworkCostSync(src.template, "targets_read")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM tracker_network_targets WHERE workspace_id = $1 AND source_id = $2 AND seen_at < $3`,
		src.ws, src.id, time.Now().Add(-networkTargetKeep))
	return err
}
