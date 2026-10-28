package api

import (
	"context"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Attribution: which channel brought each visit, and which visits get the
// credit for each sign-up and payment. The work happens in the background
// (attribution.Runner); these routes read it and change what counts.

func (s *Server) routesAttribution(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/attribution/settings", s.inWorkspace(read, s.getAttributionSettings))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/attribution/settings", s.inWorkspace(manage, s.updateAttributionSettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/attribution/channels", s.inWorkspace(read, s.attributionChannels))
	mux.HandleFunc("GET /v1/workspaces/{ws}/attribution/campaigns", s.inWorkspace(read, s.attributionCampaigns))
	mux.HandleFunc("GET /v1/workspaces/{ws}/attribution/conversions", s.inWorkspace(read, s.listAttributionConversions))
}

type attributionSettings struct {
	Model            string     `json:"model"`
	LookbackDays     int        `json:"lookback_days"`
	ConversionEvents []string   `json:"conversion_events"`
	Models           []string   `json:"models"`
	BackfilledAt     *time.Time `json:"backfilled_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// loadAttributionSettings reads a workspace's settings, making the row
// with the defaults on first use.
func (s *Server) loadAttributionSettings(ctx context.Context, ws string) (attributionSettings, error) {
	v := attributionSettings{Models: attribution.Models}
	err := s.pool.QueryRow(ctx, `
		WITH ins AS (INSERT INTO attribution_settings (workspace_id) VALUES ($1) ON CONFLICT DO NOTHING
			RETURNING model, lookback_days, conversion_events, backfilled_at, updated_at)
		SELECT * FROM ins
		UNION ALL SELECT model, lookback_days, conversion_events, backfilled_at, updated_at FROM attribution_settings WHERE workspace_id = $1
		LIMIT 1`, ws).Scan(&v.Model, &v.LookbackDays, &v.ConversionEvents, &v.BackfilledAt, &v.UpdatedAt)
	return v, err
}

func (s *Server) getAttributionSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadAttributionSettings(r.Context(), accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

var customerEventName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

func (s *Server) updateAttributionSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Model            *string  `json:"model"`
		LookbackDays     *int     `json:"lookback_days"`
		ConversionEvents []string `json:"conversion_events"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Model != nil && !attribution.ValidModel(*in.Model) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "model is one of "+strings.Join(attribution.Models, ", "))
		return
	}
	if in.LookbackDays != nil && (*in.LookbackDays < 1 || *in.LookbackDays > 365) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "lookback_days must be 1 to 365")
		return
	}
	var events []string
	if in.ConversionEvents != nil {
		for _, e := range in.ConversionEvents {
			e = strings.TrimSpace(e)
			if len(e) > 100 || !customerEventName.MatchString(e) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "conversion_events are customer event names, such as signed_up or payment.succeeded")
				return
			}
			if e == "payment.refunded" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "payment.refunded is not a conversion: refunds are taken off payment.succeeded's revenue")
				return
			}
			if !slices.Contains(events, e) {
				events = append(events, e)
			}
		}
		if len(events) == 0 || len(events) > 20 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "conversion_events needs 1 to 20 event names")
			return
		}
	}
	ctx := r.Context()
	before, err := s.loadAttributionSettings(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	after := before
	if in.Model != nil {
		after.Model = *in.Model
	}
	if in.LookbackDays != nil {
		after.LookbackDays = *in.LookbackDays
	}
	if events != nil {
		after.ConversionEvents = events
	}
	// The model only picks which stored credits reports read; the lookback
	// and the events decide the credits, so changing them credits again.
	recredit := after.LookbackDays != before.LookbackDays || !slices.Equal(after.ConversionEvents, before.ConversionEvents)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE attribution_settings SET model = $2, lookback_days = $3, conversion_events = $4, updated_by = $5, updated_at = now(),
				backfilled_at = CASE WHEN $6 THEN NULL ELSE backfilled_at END
			WHERE workspace_id = $1`, a.WorkspaceID, after.Model, after.LookbackDays, after.ConversionEvents, nullIfEmpty(a.ActorID), recredit); err != nil {
			return err
		}
		if recredit {
			if _, err := tx.Exec(ctx, `DELETE FROM attribution_conversions WHERE workspace_id = $1`, a.WorkspaceID); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "attribution.settings_updated", "workspace", a.WorkspaceID,
			map[string]any{"model": after.Model, "lookback_days": after.LookbackDays, "events": len(after.ConversionEvents), "recredit": recredit})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.loadAttributionSettings(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// attributionFilter is what every attribution report reads: from and to
// (the last 28 days by default), model (the workspace's), brand.
type attributionFilter struct {
	from, to string
	model    string
	settings attributionSettings
	brand    brandSel
}

func (s *Server) attributionQuery(w http.ResponseWriter, r *http.Request) (attributionFilter, bool) {
	q := r.URL.Query()
	to := time.Now().UTC()
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "to should be a date like 2026-10-24")
			return attributionFilter{}, false
		}
		to = t
	}
	from := to.AddDate(0, 0, -27)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "from should be a date like 2026-09-27")
			return attributionFilter{}, false
		}
		from = t
	}
	if to.Before(from) || to.Sub(from) > 3*366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be on or before to, and at most three years before it")
		return attributionFilter{}, false
	}
	model := q.Get("model")
	if model != "" && !attribution.ValidModel(model) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "model is one of "+strings.Join(attribution.Models, ", "))
		return attributionFilter{}, false
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return attributionFilter{}, false
	}
	settings, err := s.loadAttributionSettings(r.Context(), accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return attributionFilter{}, false
	}
	if model == "" {
		model = settings.Model
	}
	return attributionFilter{from: from.Format(time.DateOnly), to: to.Format(time.DateOnly), model: model, settings: settings, brand: brand}, true
}

// money is an amount in one currency.
type money struct {
	Currency    string `json:"currency"`
	AmountCents int64  `json:"amount_cents"`
}

// attributionRow is one channel's, or one campaign's, numbers.
type attributionRow struct {
	Channel string `json:"channel"`
	Label   string `json:"label"`
	Visits  int    `json:"visits"`
	// Conversions is the credited share of conversions, so it can be
	// fractional under the shared models; refunds aren't conversions.
	Conversions float64            `json:"conversions"`
	ByEvent     map[string]float64 `json:"by_event"`
	// Revenue is credited payments net of refunds, per currency.
	Revenue []money `json:"revenue"`
	revenue map[string]float64
}

func newAttributionRow(channel string) *attributionRow {
	label := "Direct"
	if c, ok := utm.ChannelByID(channel); ok {
		label = c.Label
	} else if channel != attribution.Direct {
		label = channel
	}
	return &attributionRow{Channel: channel, Label: label, ByEvent: map[string]float64{}, Revenue: []money{}, revenue: map[string]float64{}}
}

func (x *attributionRow) addCredit(name string, weight float64, currency *string, value *float64) {
	if name != "payment.refunded" {
		x.Conversions += weight
		x.ByEvent[name] += weight
	}
	if value != nil && currency != nil && *currency != "" {
		x.revenue[*currency] += *value
	}
}

func (x *attributionRow) finish() {
	x.Conversions = round2(x.Conversions)
	for k, v := range x.ByEvent {
		x.ByEvent[k] = round2(v)
	}
	x.Revenue = moneyList(x.revenue)
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func moneyList(m map[string]float64) []money {
	out := []money{}
	for c, v := range m {
		out = append(out, money{Currency: c, AmountCents: int64(math.Round(v))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// creditRows reads credits under the filter's model, grouped by keys
// (columns of attribution_credits cr), calling add for each group.
func (s *Server) creditRows(ctx context.Context, ws string, f attributionFilter, keys, where string, args []any,
	add func(key []any, name string, weight float64, currency *string, value *float64) error) error {
	args = append([]any{ws, f.from, f.to, f.model}, args...)
	sql := `SELECT ` + keys + `, c.name, sum(cr.weight)::float8, c.currency, sum(cr.weight * c.value_minor)::float8
		FROM attribution_credits cr JOIN attribution_conversions c ON c.event_id = cr.event_id
		WHERE cr.workspace_id = $1 AND cr.model = $4 AND c.occurred_at >= $2::date AND c.occurred_at < $3::date + 1` + where +
		f.brand.sql(`c.brand_id`, &args) + `
		GROUP BY ` + keys + `, c.name, c.currency`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := strings.Count(keys, ",") + 1
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return err
		}
		var weight float64
		var value *float64
		var currency *string
		name, _ := vals[n].(string)
		weight, _ = vals[n+1].(float64)
		if c, ok := vals[n+2].(string); ok {
			currency = &c
		}
		if v, ok := vals[n+3].(float64); ok {
			value = &v
		}
		if err := add(vals[:n], name, weight, currency, value); err != nil {
			return err
		}
	}
	return rows.Err()
}

// attributionChannels is the CMO view: visits, conversions, revenue and
// cost for every channel, under one model.
func (s *Server) attributionChannels(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rows := map[string]*attributionRow{}
	order := []string{}
	for _, c := range utm.Channels {
		order = append(order, c.ID)
	}
	order = append(order, attribution.Direct)
	for _, id := range order {
		rows[id] = newAttributionRow(id)
	}
	row := func(ch string) *attributionRow {
		if rows[ch] == nil {
			rows[ch] = newAttributionRow(ch)
			order = append(order, ch)
		}
		return rows[ch]
	}

	args := []any{a.WorkspaceID, f.from, f.to}
	vq, err := s.pool.Query(ctx, `SELECT channel, count(*)::int FROM site_sessions
		WHERE workspace_id = $1 AND started_at >= $2::date AND started_at < $3::date + 1`+f.brand.sql(`brand_id`, &args)+`
		GROUP BY channel`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	pending := 0
	for vq.Next() {
		var ch string
		var n int
		if err := vq.Scan(&ch, &n); err != nil {
			vq.Close()
			s.internal(w, r, err)
			return
		}
		if ch == "" {
			pending = n
			continue
		}
		row(ch).Visits += n
	}
	vq.Close()
	if err := vq.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	err = s.creditRows(ctx, a.WorkspaceID, f, `cr.channel`, ``, nil, func(key []any, name string, weight float64, currency *string, value *float64) error {
		ch, _ := key[0].(string)
		row(ch).addCredit(name, weight, currency, value)
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}

	// Cost: spend the workspace recorded against a channel. Spend with no
	// channel, or shared across channels, is shown on its own.
	cost := map[string]map[string]float64{}
	args = []any{a.WorkspaceID, f.from, f.to}
	cq, err := s.pool.Query(ctx, `SELECT channel, currency, sum(amount_cents)::float8 FROM spend_entries
		WHERE workspace_id = $1 AND amount_cents IS NOT NULL AND occurred_at >= $2::date AND occurred_at < $3::date + 1`+f.brand.sql(`brand_id`, &args)+`
		GROUP BY channel, currency`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for cq.Next() {
		var ch, cur string
		var v float64
		if err := cq.Scan(&ch, &cur, &v); err != nil {
			cq.Close()
			s.internal(w, r, err)
			return
		}
		if ch == "shared" {
			ch = ""
		}
		if cost[ch] == nil {
			cost[ch] = map[string]float64{}
		}
		cost[ch][cur] += v
	}
	cq.Close()
	if err := cq.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	type channelView struct {
		*attributionRow
		Cost []money `json:"cost"`
	}
	out := []channelView{}
	for _, id := range order {
		x := rows[id]
		x.finish()
		out = append(out, channelView{x, moneyList(cost[id])})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model": f.model, "from": f.from, "to": f.to,
		"lookback_days": f.settings.LookbackDays, "conversion_events": f.settings.ConversionEvents,
		"pending_visits": pending, "data": out, "unallocated_cost": moneyList(cost[""]),
	})
}

// attributionCampaigns drills a channel (or all of them) down to source
// and campaign.
func (s *Server) attributionCampaigns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	channel := r.URL.Query().Get("channel")
	if _, ok := utm.ChannelByID(channel); channel != "" && channel != attribution.Direct && !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel is one of the channels in GET .../tracking-taxonomy, or direct")
		return
	}
	ctx := r.Context()
	type campaignRow struct {
		*attributionRow
		Source             string  `json:"source"`
		Campaign           string  `json:"campaign"`
		TrackingCampaignID *string `json:"tracking_campaign_id"`
		CampaignName       string  `json:"campaign_name"`
	}
	rows := map[string]*campaignRow{}
	row := func(key []any) *campaignRow {
		ch, _ := key[0].(string)
		src, _ := key[1].(string)
		camp, _ := key[2].(string)
		var id *string
		if s, ok := key[3].(string); ok && s != "" {
			id = &s
		}
		k := ch + "\x00" + src + "\x00" + camp + "\x00" + deref(id)
		if rows[k] == nil {
			rows[k] = &campaignRow{attributionRow: newAttributionRow(ch), Source: src, Campaign: camp, TrackingCampaignID: id}
		}
		return rows[k]
	}
	args := []any{a.WorkspaceID, f.from, f.to, channel}
	vq, err := s.pool.Query(ctx, `SELECT channel, source, campaign, tracking_campaign_id::text, count(*)::int FROM site_sessions
		WHERE workspace_id = $1 AND started_at >= $2::date AND started_at < $3::date + 1 AND channel <> '' AND ($4 = '' OR channel = $4)`+
		f.brand.sql(`brand_id`, &args)+`
		GROUP BY 1, 2, 3, 4`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for vq.Next() {
		var ch, src, camp string
		var id *string
		var n int
		if err := vq.Scan(&ch, &src, &camp, &id, &n); err != nil {
			vq.Close()
			s.internal(w, r, err)
			return
		}
		var idv any
		if id != nil {
			idv = *id
		}
		row([]any{ch, src, camp, idv}).Visits += n
	}
	vq.Close()
	if err := vq.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	err = s.creditRows(ctx, a.WorkspaceID, f, `cr.channel, cr.source, cr.campaign, cr.tracking_campaign_id::text`,
		` AND ($5 = '' OR cr.channel = $5)`, []any{channel},
		func(key []any, name string, weight float64, currency *string, value *float64) error {
			row(key).addCredit(name, weight, currency, value)
			return nil
		})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []*campaignRow{}
	ids := []string{}
	for _, x := range rows {
		x.finish()
		out = append(out, x)
		if x.TrackingCampaignID != nil {
			ids = append(ids, *x.TrackingCampaignID)
		}
	}
	if len(ids) > 0 {
		nq, err := s.pool.Query(ctx, `SELECT id::text, name FROM tracking_campaigns WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, a.WorkspaceID, ids)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		names := map[string]string{}
		for nq.Next() {
			var id, name string
			if err := nq.Scan(&id, &name); err != nil {
				nq.Close()
				s.internal(w, r, err)
				return
			}
			names[id] = name
		}
		nq.Close()
		for _, x := range out {
			if x.TrackingCampaignID != nil {
				x.CampaignName = names[*x.TrackingCampaignID]
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Conversions != out[j].Conversions {
			return out[i].Conversions > out[j].Conversions
		}
		if out[i].Visits != out[j].Visits {
			return out[i].Visits > out[j].Visits
		}
		return out[i].Source+out[i].Campaign < out[j].Source+out[j].Campaign
	})
	if len(out) > 500 {
		out = out[:500]
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": f.model, "from": f.from, "to": f.to, "data": out})
}

type attributionCredit struct {
	Channel            string     `json:"channel"`
	Source             string     `json:"source"`
	Campaign           string     `json:"campaign"`
	TrackingCampaignID *string    `json:"tracking_campaign_id"`
	SessionID          *string    `json:"session_id"`
	TouchAt            *time.Time `json:"touch_at"`
	TagMethod          string     `json:"tag_method"`
	Weight             float64    `json:"weight"`
}

type attributionConversion struct {
	EventID    string              `json:"event_id"`
	Name       string              `json:"name"`
	RecordID   *string             `json:"record_id"`
	BrandID    *string             `json:"brand_id"`
	OccurredAt time.Time           `json:"occurred_at"`
	ValueCents *int64              `json:"value_cents"`
	Currency   string              `json:"currency"`
	Touches    int                 `json:"touches"`
	MatchedBy  string              `json:"matched_by"`
	Credits    []attributionCredit `json:"credits"`
}

// listAttributionConversions lists credited conversions, newest first,
// with who got the credit under the model.
func (s *Server) listAttributionConversions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	name, channel, recordID := q.Get("name"), q.Get("channel"), q.Get("record_id")
	if recordID != "" && !isUUID(recordID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id is a record's id")
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	ctx := r.Context()
	args := []any{a.WorkspaceID, f.from, f.to, f.model, name, channel, recordID, before, limit}
	rows, err := s.pool.Query(ctx, `
		SELECT c.event_id::text, c.name, c.record_id::text, c.brand_id::text, c.occurred_at, c.value_minor, c.currency, c.touches, c.matched_by
		FROM attribution_conversions c
		WHERE c.workspace_id = $1 AND c.occurred_at >= $2::date AND c.occurred_at < $3::date + 1
		  AND ($5 = '' OR c.name = $5) AND ($7 = '' OR c.record_id::text = $7)
		  AND ($6 = '' OR EXISTS (SELECT 1 FROM attribution_credits cr WHERE cr.event_id = c.event_id AND cr.model = $4 AND cr.channel = $6))
		  AND ($8::timestamptz IS NULL OR c.occurred_at < $8)`+f.brand.sql(`c.brand_id`, &args)+`
		ORDER BY c.occurred_at DESC, c.event_id DESC LIMIT $9`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []attributionConversion{}
	byID := map[string]int{}
	for rows.Next() {
		var x attributionConversion
		if err := rows.Scan(&x.EventID, &x.Name, &x.RecordID, &x.BrandID, &x.OccurredAt, &x.ValueCents, &x.Currency, &x.Touches, &x.MatchedBy); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		x.Credits = []attributionCredit{}
		byID[x.EventID] = len(out)
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	if len(out) > 0 {
		ids := make([]string, 0, len(out))
		for _, x := range out {
			ids = append(ids, x.EventID)
		}
		cr, err := s.pool.Query(ctx, `SELECT event_id::text, channel, source, campaign, tracking_campaign_id::text, session_id::text, touch_at, tag_method, weight::float8
			FROM attribution_credits WHERE workspace_id = $1 AND model = $2 AND event_id = ANY($3::uuid[]) ORDER BY event_id, position`, a.WorkspaceID, f.model, ids)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for cr.Next() {
			var id string
			var c attributionCredit
			if err := cr.Scan(&id, &c.Channel, &c.Source, &c.Campaign, &c.TrackingCampaignID, &c.SessionID, &c.TouchAt, &c.TagMethod, &c.Weight); err != nil {
				cr.Close()
				s.internal(w, r, err)
				return
			}
			i := byID[id]
			out[i].Credits = append(out[i].Credits, c)
		}
		cr.Close()
		if err := cr.Err(); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].OccurredAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": f.model, "data": out, "next_before": next})
}
