package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// First-touch Organic Search signups and payments for an SEO site, from
// PostHog, rolled up by landing page or by the brand's current search
// groups. Reads only what the analytics sync stored.

func (s *Server) routesSEOOrganicConversions(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/seo-sites/{site}/organic-conversions", s.onBrand(auth.ScopeSEORead, s.siteOrganicConversions))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/organic-conversions", s.inBrand(auth.ScopeSEORead, s.siteOrganicConversions))
}

type organicTotals struct {
	OrganicSessions       int     `json:"organic_sessions"`
	Signups               int     `json:"signups"`
	SignupPeople          int     `json:"signup_people"`
	Payments              int     `json:"payments"`
	PaymentPeople         int     `json:"payment_people"`
	RevenueUSD            float64 `json:"revenue_usd"`
	SignupConversionRate  float64 `json:"signup_conversion_rate"`
	PaymentConversionRate float64 `json:"payment_conversion_rate"`
}

type organicPageView struct {
	URL                   string         `json:"url"`
	Path                  string         `json:"path"`
	CanonicalPath         string         `json:"canonical_path"`
	Groups                []organicGroup `json:"groups"`
	OrganicSessions       int            `json:"organic_sessions"`
	Signups               int            `json:"signups"`
	SignupPeople          int            `json:"signup_people"`
	Payments              int            `json:"payments"`
	PaymentPeople         int            `json:"payment_people"`
	RevenueUSD            float64        `json:"revenue_usd"`
	SignupConversionRate  float64        `json:"signup_conversion_rate"`
	PaymentConversionRate float64        `json:"payment_conversion_rate"`
	Clicks                *int           `json:"clicks"`
	Impressions           *int           `json:"impressions"`
	Position              *float64       `json:"position"`
}

type organicClusterView struct {
	ID                    *string `json:"id"`
	Name                  string  `json:"name"`
	Pages                 int     `json:"pages"`
	OrganicSessions       int     `json:"organic_sessions"`
	Signups               int     `json:"signups"`
	SignupPeople          int     `json:"signup_people"`
	Payments              int     `json:"payments"`
	PaymentPeople         int     `json:"payment_people"`
	RevenueUSD            float64 `json:"revenue_usd"`
	SignupConversionRate  float64 `json:"signup_conversion_rate"`
	PaymentConversionRate float64 `json:"payment_conversion_rate"`
}

type organicGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type organicGap struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type organicDayAgg struct {
	host, path, canon                                        string
	sessions, signups, signupPeople, payments, paymentPeople int
	revenue                                                  float64
	clicks                                                   *int
	impressions                                              *int
	position                                                 *float64
}

var organicSorts = map[string]bool{
	"revenue": true, "payments": true, "signups": true, "sessions": true,
	"signup_conversion_rate": true, "payment_conversion_rate": true, "clicks": true,
}

func (s *Server) siteOrganicConversions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	ctx, ws, hosts := r.Context(), a.WorkspaceID, seointel.PageHosts(site.Host)
	q := r.URL.Query()
	group := q.Get("group")
	if group == "" {
		group = "page"
	}
	if group != "page" && group != "cluster" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group is page or cluster")
		return
	}
	sortKey := q.Get("sort")
	if sortKey == "" {
		sortKey = "revenue"
	}
	if !organicSorts[sortKey] {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sort is revenue, payments, signups, sessions, signup_conversion_rate, payment_conversion_rate or clicks")
		return
	}
	limit, err := limitParam(r, 100, 1000)
	if s.allocError(w, r, err) {
		return
	}
	src, err := organicSourceFor(ctx, s.pool, ws, site.Host)
	out := map[string]any{"source": nil, "data": []any{}, "totals": organicTotals{}, "gaps": []organicGap{}}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if src == nil {
		out["gaps"] = []organicGap{{Code: "no_posthog", Message: "No PostHog connection has counted organic conversions for this site yet."}}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["source"] = src
	to := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if src.NewestDay != nil {
		to = *src.NewestDay
	}
	end, _ := time.Parse(time.DateOnly, to)
	win := searchWindow{to: to, from: end.AddDate(0, 0, -27).Format(time.DateOnly)}
	for name, dst := range map[string]*string{"from": &win.from, "to": &win.to} {
		if v := q.Get(name); v != "" {
			if dateOnly(v) == "" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date, YYYY-MM-DD")
				return
			}
			*dst = dateOnly(v)
		}
	}
	if win.from > win.to {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is after to")
		return
	}
	out["from"], out["to"] = win.from, win.to

	pages, totals, err := s.loadOrganicPages(ctx, ws, site.Host, src, hosts, win.from, win.to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	groups, err := loadContentGroups(ctx, s.pool, ws, strPtr(site.BrandID), site.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	mapped, unmatched := attachOrganicGroups(pages, groups)
	clusters := rollupOrganicClusters(mapped, groups)

	gaps := organicGaps(src, win.from, unmatched, len(mapped))
	out["gaps"] = gaps
	out["totals"] = totals

	switch group {
	case "cluster":
		sorted := sortOrganicClusters(clusters, sortKey)
		if len(sorted) > limit {
			sorted = sorted[:limit]
		}
		if sorted == nil {
			sorted = []organicClusterView{}
		}
		out["data"] = sorted
	default:
		sorted := sortOrganicPages(mapped, sortKey)
		if len(sorted) > limit {
			sorted = sorted[:limit]
		}
		if sorted == nil {
			sorted = []organicPageView{}
		}
		out["data"] = sorted
	}
	writeJSON(w, http.StatusOK, out)
}

type organicSource struct {
	IntegrationID        string     `json:"integration_id"`
	Name                 string     `json:"name"`
	Connector            string     `json:"connector"`
	Site                 string     `json:"site"`
	NewestDay            *string    `json:"newest_day"`
	LastRunAt            *time.Time `json:"last_run_at"`
	LastError            *string    `json:"last_error"`
	PaymentSucceededFrom *string    `json:"payment_succeeded_from"`
	SessionsSource       *string    `json:"sessions_source"`
	Attribution          string     `json:"attribution"`
	Currency             string     `json:"currency"`
}

func organicSourceFor(ctx context.Context, q seointel.Querier, ws, siteHost string) (*organicSource, error) {
	var src organicSource
	err := q.QueryRow(ctx, `
		SELECT s.integration_id::text, i.name, i.connector, s.site, s.newest_day::text, s.last_run_at, s.last_error,
			s.payment_succeeded_from::text, s.sessions_source
		FROM organic_conversion_syncs s
		JOIN integrations i ON i.id = s.integration_id AND i.workspace_id = s.workspace_id AND i.status = 'connected'
		WHERE s.workspace_id = $1 AND (
			EXISTS (
				SELECT 1 FROM organic_conversions c
				WHERE c.integration_id = s.integration_id AND c.site = s.site AND lower(c.host) = ANY($2)
			) OR EXISTS (
				SELECT 1 FROM page_conversions pc
				WHERE pc.integration_id = s.integration_id AND pc.site = s.site AND lower(pc.host) = ANY($2)
			)
		)
		ORDER BY s.newest_day DESC NULLS LAST, s.last_run_at DESC NULLS LAST
		LIMIT 1`, ws, seointel.PageHosts(siteHost)).Scan(
		&src.IntegrationID, &src.Name, &src.Connector, &src.Site, &src.NewestDay, &src.LastRunAt, &src.LastError,
		&src.PaymentSucceededFrom, &src.SessionsSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	src.Attribution = "first_touch_organic"
	src.Currency = "USD"
	return &src, nil
}

func (s *Server) loadOrganicPages(ctx context.Context, ws, siteHost string, src *organicSource, hosts []string, from, to string) ([]organicPageView, organicTotals, error) {
	sc, err := seointel.SourceFor(ctx, s.pool, ws, siteHost)
	if err != nil {
		return nil, organicTotals{}, err
	}
	scID, scSite := "", ""
	if sc != nil {
		scID, scSite = sc.IntegrationID, sc.Property
	}
	rows, err := s.pool.Query(ctx, `
		WITH pages AS (
			SELECT lower(host) AS host, path, canonical_path,
				sum(organic_sessions)::int AS organic_sessions,
				sum(signups)::int AS signups,
				sum(signup_people)::int AS signup_people,
				sum(payments)::int AS payments,
				sum(payment_people)::int AS payment_people,
				sum(revenue_usd)::float8 AS revenue_usd
			FROM organic_conversions
			WHERE workspace_id = $1 AND integration_id = $2 AND site = $3 AND lower(host) = ANY($4) AND day BETWEEN $5 AND $6
			GROUP BY 1, 2, 3
		), search AS (
			SELECT lower(substring(regexp_replace(regexp_replace(sd.page, '[?#].*$', ''), '^http://', 'https://') from '^[A-Za-z]+://([^/?#:]+)')) AS host,
			       regexp_replace(substring(regexp_replace(regexp_replace(sd.page, '[?#].*$', ''), '^http://', 'https://') from '^[A-Za-z]+://[^/?#:]+(.*)$'), '/$', '') AS path,
			       sum(sd.clicks)::int AS clicks, sum(sd.impressions)::int AS impressions,
			       sum(sd.position * sd.impressions) / NULLIF(sum(sd.impressions), 0) AS position
			FROM search_daily sd
			WHERE sd.workspace_id = $1 AND sd.integration_id::text = $7 AND sd.site = $8 AND sd.day BETWEEN $5 AND $6
			GROUP BY 1, 2
		)
		SELECT p.host, p.path, p.canonical_path, p.organic_sessions, p.signups, p.signup_people, p.payments, p.payment_people, p.revenue_usd,
			x.clicks, x.impressions, x.position
		FROM pages p
		LEFT JOIN search x ON x.host = p.host AND (x.path = p.path OR x.path = p.canonical_path OR (x.path = '' AND p.path = '/'))`,
		ws, src.IntegrationID, src.Site, hosts, from, to, scID, scSite)
	if err != nil {
		return nil, organicTotals{}, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (organicPageView, error) {
		var host, path, canon string
		var v organicPageView
		err := row.Scan(&host, &path, &canon, &v.OrganicSessions, &v.Signups, &v.SignupPeople, &v.Payments, &v.PaymentPeople, &v.RevenueUSD, &v.Clicks, &v.Impressions, &v.Position)
		if path == "" {
			path = "/"
		}
		if canon == "" {
			canon = seointel.CanonicalOrganicPath(path)
		}
		v.Path, v.CanonicalPath = path, canon
		v.URL = "https://" + host + path
		v.Groups = []organicGroup{}
		v.SignupConversionRate = conversionRate(v.Signups, v.OrganicSessions)
		v.PaymentConversionRate = conversionRate(v.Payments, v.OrganicSessions)
		return v, err
	})
	if err != nil {
		return nil, organicTotals{}, err
	}
	var t organicTotals
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(organic_sessions), 0)::int, COALESCE(sum(signups), 0)::int, COALESCE(sum(signup_people), 0)::int,
		       COALESCE(sum(payments), 0)::int, COALESCE(sum(payment_people), 0)::int, COALESCE(sum(revenue_usd), 0)::float8
		FROM organic_conversions
		WHERE workspace_id = $1 AND integration_id = $2 AND site = $3 AND lower(host) = ANY($4) AND day BETWEEN $5 AND $6`,
		ws, src.IntegrationID, src.Site, hosts, from, to).Scan(&t.OrganicSessions, &t.Signups, &t.SignupPeople, &t.Payments, &t.PaymentPeople, &t.RevenueUSD); err != nil {
		return nil, organicTotals{}, err
	}
	t.SignupConversionRate = conversionRate(t.Signups, t.OrganicSessions)
	t.PaymentConversionRate = conversionRate(t.Payments, t.OrganicSessions)
	if list == nil {
		list = []organicPageView{}
	}
	return list, t, nil
}

func loadContentGroups(ctx context.Context, q seointel.Querier, ws, brand, site string) ([]seointel.SearchGroup, error) {
	if brand == "" || site == "" {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id::text, name, kind, rules FROM search_groups WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND kind='content' ORDER BY name`, ws, brand, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []seointel.SearchGroup
	for rows.Next() {
		var g seointel.SearchGroup
		var raw []byte
		if err := rows.Scan(&g.ID, &g.Name, &g.Kind, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &g.Rules); err != nil {
			return nil, err
		}
		seointel.PrepareGroup(&g)
		out = append(out, g)
	}
	return out, rows.Err()
}

func attachOrganicGroups(pages []organicPageView, groups []seointel.SearchGroup) (out []organicPageView, unmatched int) {
	out = pages
	for i := range out {
		for _, g := range groups {
			if seointel.Matches(g, out[i].CanonicalPath, "") || seointel.Matches(g, out[i].Path, "") {
				out[i].Groups = append(out[i].Groups, organicGroup{ID: g.ID, Name: g.Name})
			}
		}
		if len(out[i].Groups) == 0 {
			unmatched++
		}
	}
	return out, unmatched
}

func rollupOrganicClusters(pages []organicPageView, groups []seointel.SearchGroup) []organicClusterView {
	type acc struct {
		id                                                       *string
		name                                                     string
		pages                                                    int
		sessions, signups, signupPeople, payments, paymentPeople int
		revenue                                                  float64
	}
	by := map[string]*acc{}
	add := func(id *string, name string, p organicPageView) {
		k := name
		if id != nil {
			k = *id
		}
		a := by[k]
		if a == nil {
			a = &acc{id: id, name: name}
			by[k] = a
		}
		a.pages++
		a.sessions += p.OrganicSessions
		a.signups += p.Signups
		a.signupPeople += p.SignupPeople
		a.payments += p.Payments
		a.paymentPeople += p.PaymentPeople
		a.revenue += p.RevenueUSD
	}
	used := false
	for _, p := range pages {
		if len(p.Groups) == 0 {
			add(nil, "Ungrouped", p)
			used = true
			continue
		}
		for _, g := range p.Groups {
			id := g.ID
			add(&id, g.Name, p)
			used = true
		}
	}
	// Groups with no pages still appear, so a newly added group is visible.
	for _, g := range groups {
		if _, ok := by[g.ID]; !ok {
			id := g.ID
			by[g.ID] = &acc{id: &id, name: g.Name}
		}
	}
	_ = used
	out := make([]organicClusterView, 0, len(by))
	for _, a := range by {
		out = append(out, organicClusterView{
			ID: a.id, Name: a.name, Pages: a.pages,
			OrganicSessions: a.sessions, Signups: a.signups, SignupPeople: a.signupPeople,
			Payments: a.payments, PaymentPeople: a.paymentPeople, RevenueUSD: a.revenue,
			SignupConversionRate:  conversionRate(a.signups, a.sessions),
			PaymentConversionRate: conversionRate(a.payments, a.sessions),
		})
	}
	return out
}

func sortOrganicPages(in []organicPageView, key string) []organicPageView {
	out := append([]organicPageView(nil), in...)
	less := func(i, j int) bool {
		a, b := out[i], out[j]
		cmp := 0
		switch key {
		case "payments":
			cmp = b.Payments - a.Payments
		case "signups":
			cmp = b.Signups - a.Signups
		case "sessions":
			cmp = b.OrganicSessions - a.OrganicSessions
		case "signup_conversion_rate":
			if b.SignupConversionRate > a.SignupConversionRate {
				cmp = 1
			} else if b.SignupConversionRate < a.SignupConversionRate {
				cmp = -1
			}
		case "payment_conversion_rate":
			if b.PaymentConversionRate > a.PaymentConversionRate {
				cmp = 1
			} else if b.PaymentConversionRate < a.PaymentConversionRate {
				cmp = -1
			}
		case "clicks":
			cmp = derefInt(b.Clicks) - derefInt(a.Clicks)
		default:
			if b.RevenueUSD > a.RevenueUSD {
				cmp = 1
			} else if b.RevenueUSD < a.RevenueUSD {
				cmp = -1
			}
		}
		if cmp != 0 {
			return cmp > 0
		}
		return a.Path < b.Path
	}
	sort.Slice(out, less)
	return out
}

func sortOrganicClusters(in []organicClusterView, key string) []organicClusterView {
	out := append([]organicClusterView(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		cmp := 0
		switch key {
		case "payments":
			cmp = b.Payments - a.Payments
		case "signups":
			cmp = b.Signups - a.Signups
		case "sessions":
			cmp = b.OrganicSessions - a.OrganicSessions
		case "signup_conversion_rate":
			if b.SignupConversionRate > a.SignupConversionRate {
				cmp = 1
			} else if b.SignupConversionRate < a.SignupConversionRate {
				cmp = -1
			}
		case "payment_conversion_rate":
			if b.PaymentConversionRate > a.PaymentConversionRate {
				cmp = 1
			} else if b.PaymentConversionRate < a.PaymentConversionRate {
				cmp = -1
			}
		default:
			if b.RevenueUSD > a.RevenueUSD {
				cmp = 1
			} else if b.RevenueUSD < a.RevenueUSD {
				cmp = -1
			}
		}
		if cmp != 0 {
			return cmp > 0
		}
		// Ungrouped last when names tie-break.
		if a.ID == nil && b.ID != nil {
			return false
		}
		if a.ID != nil && b.ID == nil {
			return true
		}
		return a.Name < b.Name
	})
	return out
}

func derefInt(v *int) int {
	if v == nil {
		return -1
	}
	return *v
}

func organicGaps(src *organicSource, from string, unmatched, pages int) []organicGap {
	gaps := []organicGap{
		{Code: "attribution_first_touch", Message: "Signups and payments are attributed to the person's first Organic Search landing page, not the page of the event itself."},
		{Code: "cvr_directional", Message: "Conversion rates compare first-touch conversions with organic sessions that started on the same page, so they are directional."},
		{Code: "groups_overlap", Message: "A landing page can match more than one search group. Group totals can overlap and must not be added together; use the site totals for a unique sum."},
		{Code: "currency_usd_only", Message: "Revenue is USD only. Payments in other currencies are left out."},
	}
	if src.PaymentSucceededFrom != nil && from < *src.PaymentSucceededFrom {
		gaps = append(gaps, organicGap{Code: "payment_succeeded_partial", Message: "payment_succeeded exists only from " + *src.PaymentSucceededFrom + ". Earlier days use paid service_purchased alone, still excluding trials and wallet top-ups."})
	}
	if src.SessionsSource != nil && *src.SessionsSource == "events_referrer" {
		gaps = append(gaps, organicGap{Code: "sessions_referrer_fallback", Message: "Organic sessions were counted from the starting referrer on page views, because the PostHog sessions table was not available."})
	}
	if unmatched > 0 && pages > 0 {
		gaps = append(gaps, organicGap{Code: "ungrouped_pages", Message: strconv.Itoa(unmatched) + " landing " + pluralPage(unmatched) + " did not match a content search group and " + are(unmatched) + " listed as Ungrouped."})
	}
	if src.LastError != nil && *src.LastError != "" {
		gaps = append(gaps, organicGap{Code: "last_sync_error", Message: "The last sync failed: " + *src.LastError})
	}
	return gaps
}

func pluralPage(n int) string {
	if n == 1 {
		return "page"
	}
	return "pages"
}

func are(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
