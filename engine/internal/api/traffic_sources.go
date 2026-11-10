package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/tokens"
	"github.com/outflareai/outflare/engine/internal/trafficsources"
)

// Traffic sources (plans/ad-tracker.md, T3): where a brand buys clicks. A
// source says which URL parameters of a tracker click carry the network's
// click id, its cost and up to twenty values to report on (custom fields).
// A tracking campaign names its source; its redirect clicks then keep those
// values, and its links show the campaign URL to paste into the network.
// Most sources start from a template in integrations/traffic-sources/.

const maxTrafficSources = 200

func (s *Server) routesTrafficSources(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/traffic-source-templates", s.onBrand(read, s.listTrafficSourceTemplates))
	mux.HandleFunc("GET /v1/brands/{brand}/traffic-sources", s.onBrand(read, s.listTrafficSources))
	mux.HandleFunc("POST /v1/brands/{brand}/traffic-sources", s.onBrand(manage, s.createTrafficSource))
	mux.HandleFunc("GET /v1/brands/{brand}/traffic-sources/{id}", s.onBrand(read, s.getTrafficSource))
	mux.HandleFunc("PATCH /v1/brands/{brand}/traffic-sources/{id}", s.onBrand(manage, s.updateTrafficSource))
}

func (s *Server) listTrafficSourceTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := trafficsources.All()
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "categories": trafficsources.Categories,
		"destination_tokens": trafficsources.DestinationTokens, "postback_tokens": trafficsources.PostbackTokens})
}

type trafficSourcePostback struct {
	URL    string `json:"url"`
	Method string `json:"method"`
}

type trafficSourceView struct {
	ID         string                     `json:"id"`
	BrandID    string                     `json:"brand_id"`
	Name       string                     `json:"name"`
	Template   string                     `json:"template"`
	Currency   string                     `json:"currency"`
	ClickID    trafficsources.Param       `json:"click_id"`
	Cost       trafficsources.Param       `json:"cost"`
	Parameters []trafficsources.Parameter `json:"parameters"`
	Postback   trafficSourcePostback      `json:"postback"`
	Status     string                     `json:"status"`
	// Campaigns is how many campaigns use the source, so a change to it
	// can say what it affects.
	Campaigns int       `json:"campaigns"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const trafficSourceSelect = `
	SELECT t.id::text, t.brand_id::text, t.name, t.template, t.currency, t.click_id_param, t.click_id_token, t.cost_param, t.cost_token,
	       t.parameters, t.postback_url, t.postback_method, t.status,
	       (SELECT count(*) FROM tracking_campaigns c WHERE c.traffic_source_id = t.id)::int, t.created_at, t.updated_at
	FROM tracker_traffic_sources t
	WHERE t.workspace_id = $1 AND t.brand_id = $2`

func scanTrafficSource(row pgx.Row) (trafficSourceView, error) {
	var v trafficSourceView
	var params []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Template, &v.Currency, &v.ClickID.Param, &v.ClickID.Token, &v.Cost.Param, &v.Cost.Token,
		&params, &v.Postback.URL, &v.Postback.Method, &v.Status, &v.Campaigns, &v.CreatedAt, &v.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(params, &v.Parameters)
	}
	for i := range v.Parameters {
		if v.Parameters[i].Aliases == nil {
			v.Parameters[i].Aliases = []string{}
		}
	}
	if v.Parameters == nil {
		v.Parameters = []trafficsources.Parameter{}
	}
	return v, err
}

func (s *Server) listTrafficSources(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "active" && status != "archived" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or archived")
		return
	}
	rows, err := s.pool.Query(r.Context(), trafficSourceSelect+` AND ($3 = '' OR t.status = $3) ORDER BY lower(t.name)`, a.WorkspaceID, a.Brand, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trafficSourceView, error) { return scanTrafficSource(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []trafficSourceView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findTrafficSource(ctx context.Context, ws, brand, id string) (trafficSourceView, error) {
	if !validUUID(id) {
		return trafficSourceView{}, errNotFound
	}
	v, err := scanTrafficSource(s.pool.QueryRow(ctx, trafficSourceSelect+` AND t.id = $3`, ws, brand, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) getTrafficSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.findTrafficSource(r.Context(), a.WorkspaceID, a.Brand, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "traffic source not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type trafficSourceInput struct {
	// Template starts a new source from a template's values; the other
	// fields change them. Only on create.
	Template   *string                     `json:"template"`
	Name       *string                     `json:"name"`
	Currency   *string                     `json:"currency"`
	ClickID    *trafficsources.Param       `json:"click_id"`
	Cost       *trafficsources.Param       `json:"cost"`
	Parameters *[]trafficsources.Parameter `json:"parameters"`
	Postback   *trafficSourcePostback      `json:"postback"`
	Status     *string                     `json:"status"`
}

var currencyCodeRe = regexp.MustCompile(`^[A-Z]{3}$`)

// apply puts in's values over v and checks the result.
func (in trafficSourceInput) apply(v *trafficSourceView) error {
	if in.Name != nil {
		v.Name = strings.TrimSpace(*in.Name)
	}
	if in.Currency != nil {
		v.Currency = strings.ToUpper(strings.TrimSpace(*in.Currency))
	}
	if in.ClickID != nil {
		v.ClickID = trimParam(*in.ClickID)
	}
	if in.Cost != nil {
		v.Cost = trimParam(*in.Cost)
	}
	if in.Parameters != nil {
		v.Parameters = make([]trafficsources.Parameter, len(*in.Parameters))
		for i, p := range *in.Parameters {
			p.Name, p.Param, p.Token = strings.TrimSpace(p.Name), strings.TrimSpace(p.Param), strings.TrimSpace(p.Token)
			if p.Aliases == nil {
				p.Aliases = []string{}
			}
			v.Parameters[i] = p
		}
	}
	if in.Postback != nil {
		v.Postback = trafficSourcePostback{URL: strings.TrimSpace(in.Postback.URL), Method: strings.ToUpper(strings.TrimSpace(in.Postback.Method))}
	}
	if in.Status != nil {
		v.Status = *in.Status
	}
	if v.Postback.Method == "" {
		v.Postback.Method = "GET"
	}
	switch {
	case v.Name == "" || len(v.Name) > 120:
		return errInvalid("name is 1 to 120 characters")
	case !currencyCodeRe.MatchString(v.Currency):
		return errInvalid("currency is a three-letter code such as USD")
	case v.Status != "active" && v.Status != "archived":
		return errInvalid("status is active or archived")
	case v.Postback.Method != "GET" && v.Postback.Method != "POST":
		return errInvalid("postback.method is GET or POST")
	}
	for what, p := range map[string]trafficsources.Param{"click_id": v.ClickID, "cost": v.Cost} {
		if p.Param == "" {
			if p.Token != "" {
				return errInvalid(what + ".param is required with a token")
			}
			continue
		}
		if err := trafficsources.CheckParam(what+".param", p.Param); err != nil {
			return errInvalid(err.Error())
		}
		if err := trafficsources.CheckToken(what+".token", p.Token); err != nil {
			return errInvalid(err.Error())
		}
	}
	if v.Cost.Param != "" && strings.EqualFold(v.Cost.Param, v.ClickID.Param) {
		return errInvalid("click_id and cost need different parameters")
	}
	if err := trafficsources.CheckParameters(v.Parameters, v.ClickID.Param, v.Cost.Param); err != nil {
		return errInvalid(err.Error())
	}
	if v.Postback.URL != "" {
		if len(v.Postback.URL) > 2000 {
			return errInvalid("postback.url is at most 2,000 characters")
		}
		// Some networks still give http:// postback addresses.
		if err := checkTokenURL("postback.url", v.Postback.URL, trafficsources.PostbackTokens, true,
			"postback.url is the network's postback address, such as https://network.example/postback?clickid={source_click_id}&payout={payout}"); err != nil {
			return err
		}
	}
	return nil
}

func trimParam(p trafficsources.Param) trafficsources.Param {
	return trafficsources.Param{Param: strings.TrimSpace(p.Param), Token: strings.TrimSpace(p.Token)}
}

func (s *Server) createTrafficSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in trafficSourceInput
	if !decode(w, r, &in) {
		return
	}
	v := trafficSourceView{Currency: "USD", Status: "active", Parameters: []trafficsources.Parameter{}}
	if in.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new traffic source is active; archive it with PATCH")
		return
	}
	if ref := strings.TrimSpace(deref(in.Template)); ref != "" {
		t, ok := trafficsources.Get(ref)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "template not found: "+truncate(ref, 40)+"; GET traffic-source-templates lists them")
			return
		}
		v.Template, v.Name, v.Currency, v.ClickID, v.Cost = t.ID, t.Name, t.Currency, t.ClickID, t.Cost
		v.Parameters = append([]trafficsources.Parameter{}, t.Parameters...)
		if t.Postback != nil {
			v.Postback = trafficSourcePostback{URL: t.Postback.URL, Method: t.Postback.Method}
		}
		// A postback that needs the brand's own value (REPLACE) is filled
		// in now or left off: one isn't saved half done.
		if t.HasPlaceholder() && (in.Postback == nil || strings.Contains(in.Postback.URL, tokens.Placeholder)) {
			v.Postback = trafficSourcePostback{}
		}
	}
	if s.allocError(w, r, in.apply(&v)) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracker_traffic_sources WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxTrafficSources {
			return errInvalid("a brand can have at most 200 traffic sources; archive old ones and reuse them")
		}
		params, err := json.Marshal(v.Parameters)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO tracker_traffic_sources (workspace_id, brand_id, name, template, currency, click_id_param, click_id_token,
				cost_param, cost_token, parameters, postback_url, postback_method, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id::text`,
			a.WorkspaceID, a.Brand, v.Name, v.Template, v.Currency, v.ClickID.Param, v.ClickID.Token, v.Cost.Param, v.Cost.Token,
			params, v.Postback.URL, v.Postback.Method, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "traffic_source.created", "traffic_source", id,
			map[string]any{"brand_id": a.Brand, "template": v.Template, "parameters": len(v.Parameters), "postback": v.Postback.URL != ""})
	})
	if s.sourceSaveError(w, r, err, v.Name) {
		return
	}
	s.writeTrafficSource(w, r, http.StatusCreated, id)
}

// updateTrafficSource changes a source or archives it. Its campaigns'
// clicks follow the change within a few minutes (the redirect cache).
func (s *Server) updateTrafficSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	cur, err := s.findTrafficSource(ctx, a.WorkspaceID, a.Brand, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "traffic source not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var in trafficSourceInput
	if !decode(w, r, &in) {
		return
	}
	if in.Template != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a source's template can't change; change its fields, or make another source")
		return
	}
	v := cur
	if s.allocError(w, r, in.apply(&v)) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		params, err := json.Marshal(v.Parameters)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE tracker_traffic_sources SET name = $3, currency = $4, click_id_param = $5, click_id_token = $6, cost_param = $7,
				cost_token = $8, parameters = $9, postback_url = $10, postback_method = $11, status = $12, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, v.Name, v.Currency, v.ClickID.Param, v.ClickID.Token, v.Cost.Param, v.Cost.Token,
			params, v.Postback.URL, v.Postback.Method, v.Status); err != nil {
			return err
		}
		action := "traffic_source.updated"
		if v.Status != cur.Status {
			action = map[string]string{"archived": "traffic_source.archived", "active": "traffic_source.restored"}[v.Status]
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "traffic_source", cur.ID,
			map[string]any{"brand_id": a.Brand, "parameters": len(v.Parameters), "campaigns": cur.Campaigns, "postback": v.Postback.URL != ""})
	})
	if s.sourceSaveError(w, r, err, v.Name) {
		return
	}
	s.redirects.clear()
	s.writeTrafficSource(w, r, http.StatusOK, cur.ID)
}

func (s *Server) sourceSaveError(w http.ResponseWriter, r *http.Request, err error, name string) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a traffic source named "+truncate(name, 120)+"; pick another name")
		return true
	}
	return s.allocError(w, r, err)
}

func (s *Server) writeTrafficSource(w http.ResponseWriter, r *http.Request, status int, id string) {
	a := accessFrom(r.Context())
	v, err := s.findTrafficSource(r.Context(), a.WorkspaceID, a.Brand, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

// campaignSource is what a redirect needs of a campaign's traffic source.
type campaignSource struct {
	clickID, cost trafficsources.Param
	params        []trafficsources.Parameter
}

// readCampaignSource builds a campaign's source from its joined columns;
// nil without one.
func readCampaignSource(id *string, clickParam, clickToken, costParam, costToken string, params []byte) *campaignSource {
	if id == nil {
		return nil
	}
	src := &campaignSource{clickID: trafficsources.Param{Param: clickParam, Token: clickToken},
		cost: trafficsources.Param{Param: costParam, Token: costToken}}
	_ = json.Unmarshal(params, &src.params)
	return src
}

// maxFieldLen is the longest value a custom field keeps; a network's click
// id can be longer (ExoClick's run to 700 characters).
const (
	maxFieldLen         = 255
	maxSourceClickIDLen = 1000
)

// capture reads a click's custom fields, the source's click id and the
// cost from the request's query. Without a source, sub1 to sub20 fill the
// fields of the same number, as most trackers do.
func (src *campaignSource) capture(q url.Values) (fields map[string]string, clickID, cost string) {
	set := func(n int, v string) {
		if v = strings.TrimSpace(v); v != "" {
			if fields == nil {
				fields = map[string]string{}
			}
			fields[strconv.Itoa(n)] = cutUTF8(v, maxFieldLen)
		}
	}
	if src == nil {
		for n := 1; n <= trafficsources.Fields; n++ {
			set(n, q.Get("sub"+strconv.Itoa(n)))
		}
		return fields, "", ""
	}
	for _, p := range src.params {
		v := q.Get(p.Param)
		for _, a := range p.Aliases {
			if v == "" {
				v = q.Get(a)
			}
		}
		set(p.Field, v)
	}
	if src.clickID.Param != "" {
		clickID = cutUTF8(strings.TrimSpace(q.Get(src.clickID.Param)), maxSourceClickIDLen)
	}
	if src.cost.Param != "" {
		cost = strings.TrimSpace(q.Get(src.cost.Param))
	}
	return fields, clickID, cost
}

// costMicros reads a cost a network filled in ("0.0125") as millionths;
// 0 for anything that isn't a non-negative amount under a million.
func costMicros(v string) int64 {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || n >= 1e6 || math.IsNaN(n) {
		return 0
	}
	return int64(math.Round(n * 1e6))
}

// campaignURL is the address to paste into the network: the redirect link
// with the source's parameters, each set to the network's token.
func (src *campaignSource) campaignURL(redirect string) string {
	var parts []string
	add := func(p trafficsources.Param) {
		if p.Param != "" && p.Token != "" {
			parts = append(parts, url.QueryEscape(p.Param)+"="+p.Token)
		}
	}
	add(src.clickID)
	add(src.cost)
	for _, p := range src.params {
		add(trafficsources.Param{Param: p.Param, Token: p.Token})
	}
	if len(parts) == 0 {
		return redirect
	}
	return redirect + "?" + strings.Join(parts, "&")
}

// destinationValues are a redirect click's token values for its
// destination URL.
func destinationValues(t redirectTarget, c bufferedClick, cost string) tokens.Values {
	d := c.detail
	v := tokens.Values{"click_id": c.key, "source_click_id": c.sourceClickID, "cost": cost,
		"campaign": t.campaign.Slug, "campaign_code": t.campaign.Code, "source": t.campaign.Source, "content": t.link.Content,
		"country": d.place.Country, "region": d.place.Region, "city": d.place.City, "isp": d.place.Org, "device": d.device,
		"os": d.os, "browser": d.browser, "language": d.language}
	if d.place.ASN != 0 {
		v["asn"] = strconv.FormatUint(uint64(d.place.ASN), 10)
	}
	for n, f := range c.fields {
		v["field"+n] = f
	}
	return v
}
