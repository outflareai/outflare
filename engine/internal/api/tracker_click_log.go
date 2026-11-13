package api

// The tracker's click logs (plans/ad-tracker.md, T5c; plans/ad-tracker-ui.md
// B6): the brand's redirect clicks and lander clicks, newest first, with
// where each came from and where it went, to find a click id, check a
// test click or see what a bot did. They read the click tables the redirect
// and lander handlers write; nothing here writes.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

func (s *Server) routesTrackerClickLog(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/tracker-clicks", s.onBrand(auth.ScopeAnalyticsRead, s.listTrackerClicks))
	mux.HandleFunc("GET /v1/brands/{brand}/tracker-lander-clicks", s.onBrand(auth.ScopeAnalyticsRead, s.listTrackerLanderClicks))
}

type trackerClickView struct {
	ClickKey      string            `json:"click_key"`
	CampaignID    *string           `json:"campaign_id"`
	LinkID        string            `json:"link_id"`
	PathID        *string           `json:"path_id"`
	DestinationID *string           `json:"destination_id"`
	LanderID      *string           `json:"lander_id"`
	OfferID       *string           `json:"offer_id"`
	Direct        bool              `json:"direct"`
	Country       string            `json:"country"`
	Region        string            `json:"region"`
	City          string            `json:"city"`
	ISP           string            `json:"isp"`
	ASN           int64             `json:"asn"`
	Device        string            `json:"device"`
	OS            string            `json:"os"`
	Browser       string            `json:"browser"`
	Language      string            `json:"language"`
	Fields        map[string]string `json:"fields"`
	SourceClickID string            `json:"source_click_id"`
	Cost          string            `json:"cost"`
	LikelyBot     bool              `json:"likely_bot"`
	FraudReasons  []string          `json:"fraud_reasons"`
	LandedAt      *time.Time        `json:"landed_at"`
	ClickedAt     time.Time         `json:"clicked_at"`
}

// clickLogFilter reads the filters both logs share. ok is false when it
// has written a 400.
type clickLogFilter struct {
	campaign, offer, lander, click string
	bots                           bool
	since, before                  *time.Time
	limit                          int
}

func (s *Server) clickLogFilter(w http.ResponseWriter, r *http.Request) (clickLogFilter, bool) {
	q := r.URL.Query()
	f := clickLogFilter{campaign: q.Get("campaign"), offer: q.Get("offer"), lander: q.Get("lander"), click: q.Get("click_id")}
	for name, v := range map[string]string{"campaign": f.campaign, "offer": f.offer, "lander": f.lander} {
		if v != "" && !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is an id")
			return f, false
		}
	}
	if len(f.click) > 32 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "click_id is a click's key, 16 letters and digits")
		return f, false
	}
	if v := q.Get("bots"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "bots is true or false")
			return f, false
		}
		f.bots = b
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "since is a time such as 2026-11-12T09:00:00Z")
			return f, false
		}
		f.since = &t
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return f, false
	}
	f.limit = limit
	before, ok := beforeTime(w, r)
	if !ok {
		return f, false
	}
	f.before = before
	return f, true
}

// listTrackerClicks is the brand's redirect clicks, newest first. Likely
// bots are left out unless bots=true asks for them, as in the report.
func (s *Server) listTrackerClicks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.clickLogFilter(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT k.click_key, l.campaign_id::text, k.link_id::text, k.path_id::text, k.destination_id::text,
			k.lander_id::text, k.offer_id::text, k.direct, k.country, k.region, k.city, k.isp, k.asn, k.device, k.os, k.browser, k.language,
			k.fields, k.source_click_id, k.cost_micros, k.likely_bot, k.fraud_reasons, k.landed_at, k.clicked_at
		FROM tracking_link_clicks k JOIN tracking_links l ON l.id = k.link_id AND l.workspace_id = k.workspace_id
		WHERE k.workspace_id = $1 AND k.brand_id = $2
		  AND ($3 = '' OR l.campaign_id = $3::uuid) AND ($4 = '' OR k.offer_id = $4::uuid) AND ($5 = '' OR k.lander_id = $5::uuid)
		  AND ($6 = '' OR k.click_key = lower($6)) AND ($7 OR NOT k.likely_bot)
		  AND ($8::timestamptz IS NULL OR k.clicked_at >= $8) AND ($9::timestamptz IS NULL OR k.clicked_at < $9)
		ORDER BY k.clicked_at DESC LIMIT $10`,
		a.WorkspaceID, a.Brand, f.campaign, f.offer, f.lander, f.click, f.bots, f.since, f.before, f.limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackerClickView, error) {
		var v trackerClickView
		var fields []byte
		var cost int64
		err := row.Scan(&v.ClickKey, &v.CampaignID, &v.LinkID, &v.PathID, &v.DestinationID, &v.LanderID, &v.OfferID, &v.Direct,
			&v.Country, &v.Region, &v.City, &v.ISP, &v.ASN, &v.Device, &v.OS, &v.Browser, &v.Language,
			&fields, &v.SourceClickID, &cost, &v.LikelyBot, &v.FraudReasons, &v.LandedAt, &v.ClickedAt)
		if err == nil {
			err = json.Unmarshal(fields, &v.Fields)
		}
		v.Cost = strconv.FormatFloat(float64(cost)/1e6, 'f', -1, 64)
		if v.FraudReasons == nil {
			v.FraudReasons = []string{}
		}
		if v.Fields == nil {
			v.Fields = map[string]string{}
		}
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *time.Time
	if len(list) > f.limit {
		next, list = &list[f.limit-1].ClickedAt, list[:f.limit]
	}
	if list == nil {
		list = []trackerClickView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

type trackerLanderClickView struct {
	ClickKey      string    `json:"click_key"`
	LanderID      *string   `json:"lander_id"`
	CTA           int       `json:"cta"`
	DestinationID *string   `json:"destination_id"`
	OfferID       *string   `json:"offer_id"`
	LikelyBot     bool      `json:"likely_bot"`
	ClickedAt     time.Time `json:"clicked_at"`
}

// listTrackerLanderClicks is the clicks on the brand's landers' calls to
// action, newest first.
func (s *Server) listTrackerLanderClicks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.clickLogFilter(w, r)
	if !ok {
		return
	}
	if f.campaign != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "lander clicks filter by lander, offer or click_id")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT click_key, lander_id::text, cta, destination_id::text, offer_id::text, likely_bot, clicked_at
		FROM tracker_lander_clicks
		WHERE workspace_id = $1 AND brand_id = $2 AND ($3 = '' OR offer_id = $3::uuid) AND ($4 = '' OR lander_id = $4::uuid)
		  AND ($5 = '' OR click_key = lower($5)) AND ($6 OR NOT likely_bot)
		  AND ($7::timestamptz IS NULL OR clicked_at >= $7) AND ($8::timestamptz IS NULL OR clicked_at < $8)
		ORDER BY clicked_at DESC LIMIT $9`,
		a.WorkspaceID, a.Brand, f.offer, f.lander, f.click, f.bots, f.since, f.before, f.limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[trackerLanderClickView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *time.Time
	if len(list) > f.limit {
		next, list = &list[f.limit-1].ClickedAt, list[:f.limit]
	}
	if list == nil {
		list = []trackerLanderClickView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}
