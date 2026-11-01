package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

func (s *Server) routesSearchInsights(mux *http.ServeMux) {
	b := "/v1/brands/{brand}/seo-sites/{site}"
	s.routesSearchWorkspace(mux)
	mux.HandleFunc("GET "+b+"/insights", s.onBrand(auth.ScopeSEORead, s.searchInsights))
	mux.HandleFunc("POST "+b+"/insights/refresh", s.onBrand(auth.ScopeSEOManage, s.refreshSearchInsights))
	mux.HandleFunc("GET "+b+"/search-groups", s.onBrand(auth.ScopeSEORead, s.listSearchGroups))
	mux.HandleFunc("POST "+b+"/search-groups", s.onBrand(auth.ScopeSEOManage, s.saveSearchGroup))
	mux.HandleFunc("DELETE "+b+"/search-groups/{item}", s.onBrand(auth.ScopeSEOManage, s.deleteSearchGroup))
	mux.HandleFunc("GET "+b+"/search-migrations", s.onBrand(auth.ScopeSEORead, s.listSearchMigrations))
	mux.HandleFunc("POST "+b+"/search-migrations", s.onBrand(auth.ScopeSEOManage, s.saveSearchMigration))
	mux.HandleFunc("DELETE "+b+"/search-migrations/{item}", s.onBrand(auth.ScopeSEOManage, s.deleteSearchMigration))
}

// A brand URL must never load a neighbouring brand's site, even for owners.
func (s *Server) insightSite(w http.ResponseWriter, r *http.Request) (*seoSite, bool) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return nil, false
	}
	if site.BrandID == nil || *site.BrandID != a.Brand {
		writeError(w, r, 404, "not_found", "site not found in this brand")
		return nil, false
	}
	return site, true
}
func (s *Server) groupsFor(ctx context.Context, ws, brand, site string) ([]seointel.SearchGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,name,kind,rules FROM search_groups WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 ORDER BY name`, ws, brand, site)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.SearchGroup, error) {
		var g seointel.SearchGroup
		var raw []byte
		e := r.Scan(&g.ID, &g.Name, &g.Kind, &raw)
		if e == nil {
			e = json.Unmarshal(raw, &g.Rules)
			seointel.PrepareGroup(&g)
		}
		return g, e
	})
}
func (s *Server) listSearchGroups(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	g, e := s.groupsFor(r.Context(), a.WorkspaceID, a.Brand, site.ID)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"data": g})
}
func (s *Server) saveSearchGroup(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var g seointel.SearchGroup
	if !decode(w, r, &g) {
		return
	}
	if !seointel.ValidRules(g) {
		writeError(w, r, 400, "invalid_request", "Give a name, content or topic kind, and 1–100 valid rules (prefix, contains, equals or regex).")
		return
	}
	raw, _ := json.Marshal(g.Rules)
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if g.ID != "" {
			if !validUUID(g.ID) {
				return errInsightNotFound
			}
			tag, e := tx.Exec(r.Context(), `UPDATE search_groups SET name=$5,kind=$6,rules=$7 WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, site.ID, g.ID, g.Name, g.Kind, raw)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				return errInsightNotFound
			}
		} else {
			if e := tx.QueryRow(r.Context(), `INSERT INTO search_groups(workspace_id,brand_id,site_id,name,kind,rules) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(site_id,name) DO UPDATE SET kind=EXCLUDED.kind,rules=EXCLUDED.rules RETURNING id::text`, a.WorkspaceID, a.Brand, site.ID, g.Name, g.Kind, raw).Scan(&g.ID); e != nil {
				return e
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_group.saved", "search_group", g.ID, map[string]any{"brand_id": a.Brand, "site_id": site.ID})
	})
	if errors.Is(err, errInsightNotFound) {
		writeError(w, r, 404, "not_found", "group not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, 200, g)
}

var errInsightNotFound = errors.New("search insight resource not found")

func (s *Server) deleteSearchGroup(w http.ResponseWriter, r *http.Request) {
	s.deleteInsightResource(w, r, "search_groups", "search_group.deleted")
}
func (s *Server) deleteSearchMigration(w http.ResponseWriter, r *http.Request) {
	s.deleteInsightResource(w, r, "search_migrations", "search_migration.deleted")
}
func (s *Server) deleteInsightResource(w http.ResponseWriter, r *http.Request, table, event string) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("item")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "resource not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(), `DELETE FROM `+table+` WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, site.ID, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return errInsightNotFound
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, event, table, id, map[string]any{"brand_id": a.Brand})
	})
	if errors.Is(err, errInsightNotFound) {
		writeError(w, r, 404, "not_found", "resource not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) searchInsights(w http.ResponseWriter, r *http.Request) {
	if out, ok := s.buildSearchInsights(w, r); ok {
		writeJSON(w, 200, out)
	}
}
func (s *Server) buildSearchInsights(w http.ResponseWriter, r *http.Request) (*seointel.InsightReport, bool) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return nil, false
	}
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	report := q.Get("report")
	if report == "" {
		report = "opportunities"
	}
	valid := map[string]bool{}
	for _, v := range []string{"opportunities", "ctr", "decay", "segments", "groups", "forecast", "completeness", "changes", "attribution", "ai-referrals", "migrations", "vitals"} {
		valid[v] = true
	}
	if !valid[report] {
		writeError(w, r, 400, "invalid_request", "unknown insight report")
		return nil, false
	}
	src, err := seointel.SourceForBrand(ctx, s.pool, a.WorkspaceID, a.Brand, site.Host)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	dateSource := src
	if report == "vitals" || report == "attribution" || report == "ai-referrals" {
		dateSource = nil
	}
	win, ok := searchDates(w, r, dateSource)
	if !ok {
		return nil, false
	}
	out := seointel.InsightReport{Report: report, From: win.from, To: win.to, Status: "ready", Source: src, Notes: []string{}, Data: []seointel.InsightRow{}, Summary: map[string]float64{}}
	from, _ := time.Parse(time.DateOnly, win.from)
	to, _ := time.Parse(time.DateOnly, win.to)
	if to.Sub(from) > 486*24*time.Hour {
		writeError(w, r, 400, "invalid_request", "Use at most 486 days.")
		return nil, false
	}
	if q.Get("to") == "" {
		settled := time.Now().UTC().AddDate(0, 0, -3).Format(time.DateOnly)
		if win.to > settled {
			win.to = settled
			if q.Get("from") == "" {
				d, _ := time.Parse(time.DateOnly, settled)
				win.from = d.AddDate(0, 0, -27).Format(time.DateOnly)
			}
			out.From, out.To = win.from, win.to
			from, _ = time.Parse(time.DateOnly, win.from)
			to, _ = time.Parse(time.DateOnly, win.to)
		}
	}
	// Reports with independent sources use their own useful default window.
	if report == "vitals" || report == "attribution" || report == "ai-referrals" {
		if q.Get("to") == "" {
			win.to = time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
		}
		if q.Get("from") == "" {
			end, _ := time.Parse(time.DateOnly, win.to)
			win.from = end.AddDate(0, 0, -27).Format(time.DateOnly)
			if report == "vitals" {
				win.from = end.AddDate(0, -9, 0).Format(time.DateOnly)
			}
		}
	}
	if report == "decay" && q.Get("from") == "" {
		end, _ := time.Parse(time.DateOnly, win.to)
		win.from = end.AddDate(0, 0, -83).Format(time.DateOnly)
		if q.Get("bucket") == "month" {
			win.from = end.AddDate(0, -6, 0).Format(time.DateOnly)
		}
	}
	out.From, out.To = win.from, win.to
	from, _ = time.Parse(time.DateOnly, win.from)
	to, _ = time.Parse(time.DateOnly, win.to)
	if win.from > win.to {
		writeError(w, r, 400, "invalid_request", "from is after the latest settled day")
		return nil, false
	}
	groups, err := s.groupsFor(ctx, a.WorkspaceID, a.Brand, site.ID)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	var selected *seointel.SearchGroup
	if id := q.Get("group_id"); id != "" {
		for _, g := range groups {
			if g.ID == id {
				gg := g
				selected = &gg
			}
		}
		if selected == nil {
			writeError(w, r, 404, "not_found", "group not found")
			return nil, false
		}
	}
	// Dimensions not collected historically are empty, not reconstructed from
	// current data. Country/device reports aggregate independently of queries.
	country, device := strings.ToLower(q.Get("country")), strings.ToUpper(q.Get("device"))
	if country != "" && (len(country) != 3 || strings.IndexFunc(country, func(c rune) bool { return c < 'a' || c > 'z' }) >= 0) {
		writeError(w, r, 400, "invalid_request", "country is a three-letter Search Console code")
		return nil, false
	}
	if device != "" && device != "MOBILE" && device != "DESKTOP" && device != "TABLET" {
		writeError(w, r, 400, "invalid_request", "device is MOBILE, DESKTOP or TABLET")
		return nil, false
	}
	if (country != "" || device != "") && report != "segments" && report != "opportunities" && report != "ctr" {
		writeError(w, r, 400, "invalid_request", "country and device filters apply to segments, opportunities and ctr")
		return nil, false
	}
	switch report {
	case "vitals":
		err = s.insightVitals(ctx, a.WorkspaceID, a.Brand, site.ID, win, &out)
	case "migrations":
		err = s.insightMigrations(ctx, a.WorkspaceID, a.Brand, site, q.Get("migration_id"), to, &out)
	case "attribution", "ai-referrals":
		err = s.insightAttribution(ctx, a.WorkspaceID, a.Brand, site, win, report, q.Get("integration_id"), &out)
	default:
		if src == nil {
			out.Status = "not_connected"
			out.Notes = append(out.Notes, "Connect Google Search Console for this site's host.")
			break
		}
		switch report {
		case "segments":
			err = s.insightSegments(ctx, a.WorkspaceID, a.Brand, site.Host, src, win, country, device, selected, &out)
		case "completeness":
			err = s.insightCompleteness(ctx, a.WorkspaceID, a.Brand, src, win, &out)
		case "forecast", "changes":
			// Forecasts and breaks use a longer history than the table's default window.
			history := win
			if q.Get("from") == "" {
				history.from = to.AddDate(0, 0, -485).Format(time.DateOnly)
			}
			out.From, out.To = history.from, history.to
			points, e := s.insightSeries(ctx, a.WorkspaceID, site.Host, src, history)
			err = e
			if e == nil && report == "changes" {
				out.Data = seointel.ChangePoints(points)
				err = s.insightUpdateContext(ctx, a.WorkspaceID, a.Brand, site.ID, history, &out)
			}
			if e == nil && report == "forecast" {
				weeks, pe := insightNumber(q, "weeks", 12, 1, 52)
				growth, ge := insightNumber(q, "growth", 0, -.9, 5)
				cr, ce := insightNumber(q, "conversion_rate", 0, 0, 1)
				ov, oe := insightNumber(q, "order_value", 0, 0, 1000000)
				if pe != nil || ge != nil || ce != nil || oe != nil {
					writeError(w, r, 400, "invalid_request", "weeks: 1–52, growth: -0.9–5 per year, conversion_rate: 0–1, order_value: 0–1000000")
					return nil, false
				}
				currency := strings.ToUpper(q.Get("currency"))
				if currency == "" {
					currency = "USD"
				}
				if len(currency) != 3 || strings.IndexFunc(currency, func(c rune) bool { return c < 'A' || c > 'Z' }) >= 0 {
					writeError(w, r, 400, "invalid_request", "currency is a three-letter ISO currency code")
					return nil, false
				}
				out.Data = seointel.Forecast(points, int(weeks), growth, cr, ov)
				for i := range out.Data {
					out.Data[i].Currency = currency
				}
				out.Notes = append(out.Notes, "Model estimate using weekday seasonality and a bounded linear trend; range is approximate, not a guarantee. Revenue uses your supplied conversion rate and order value.")
				out.Summary["history_days"] = float64(len(seointel.ContiguousTail(points)))
				if len(seointel.ContiguousTail(points)) < 56 {
					out.Status = "insufficient_data"
				}
			}
		case "decay":
			threshold, e := insightNumber(q, "threshold", .1, 0, 1)
			if e != nil {
				writeError(w, r, 400, "invalid_request", "threshold is a fraction from 0 to 1")
				return nil, false
			}
			bucket := q.Get("bucket")
			if bucket == "" {
				bucket = "week"
			}
			if bucket != "week" && bucket != "month" {
				writeError(w, r, 400, "invalid_request", "bucket is week or month")
				return nil, false
			}
			err = s.insightDecay(ctx, a.WorkspaceID, site.Host, src, win, bucket, threshold, selected, &out)
		default:
			var rows []seointel.InsightRow
			if country != "" || device != "" {
				err = s.insightSegments(ctx, a.WorkspaceID, a.Brand, site.Host, src, win, country, device, selected, &out)
				rows = out.Data
				out.Notes = append(out.Notes, "Segment opportunities are page-level; country/device data does not include query text.")
			} else {
				rows, err = s.insightRows(ctx, a.WorkspaceID, site.Host, src, win)
				if len(rows) >= 100001 {
					out.Notes = append(out.Notes, "Analysis is limited to the 100,001 most-impressed page/query pairs; narrow the dates or site for complete results.")
				}
				if selected != nil {
					filtered := []seointel.InsightRow{}
					for _, x := range rows {
						if seointel.Matches(*selected, x.Page, x.Query) {
							filtered = append(filtered, x)
						}
					}
					rows = filtered
				}
			}
			if err != nil {
				break
			}
			if report == "groups" {
				out.Data = aggregateInsightGroups(groups, rows)
				out.Notes = append(out.Notes, "Groups can overlap. Their totals must not be added together.")
			} else {
				ops, curve := seointel.Opportunities(rows)
				out.Data = ops
				if report == "ctr" {
					out.Data = curve
				}
				out.Notes = append(out.Notes, "Click gains are estimates at unchanged impressions. CTR uses this site's observed position bucket at 1,000+ impressions, otherwise a generic reference curve.")
			}
		}
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if len(out.Data) == 0 && out.Status == "ready" {
		out.Status = "no_data"
	}
	if len(out.Data) > 1000 {
		out.Summary["matching_rows"] = float64(len(out.Data))
		out.Data = out.Data[:1000]
		out.Notes = append(out.Notes, "Showing the first 1,000 results.")
	}
	return &out, true
}
func insightNumber(q url.Values, key string, def, lo, hi float64) (float64, error) {
	v := q.Get(key)
	if v == "" {
		return def, nil
	}
	n, e := strconv.ParseFloat(v, 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < lo || n > hi {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return n, nil
}

func (s *Server) insightRows(ctx context.Context, ws, host string, src *seointel.Source, win searchWindow) ([]seointel.InsightRow, error) {
	rows, e := s.pool.Query(ctx, `SELECT page,query,sum(clicks)::float8,sum(impressions)::float8,COALESCE(sum(position*impressions)/NULLIF(sum(impressions),0),0) FROM search_daily WHERE workspace_id=$1 AND integration_id=$2 AND site=$3 AND day BETWEEN $4 AND $5 AND `+seointel.PageHostSQL("page")+`=ANY($6) GROUP BY page,query ORDER BY sum(impressions) DESC,page,query LIMIT 100001`, ws, src.IntegrationID, src.Property, win.from, win.to, seointel.PageHosts(host))
	if e != nil {
		return nil, e
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightRow, error) {
		var x seointel.InsightRow
		e := r.Scan(&x.Page, &x.Query, &x.Clicks, &x.Impressions, &x.Position)
		x.Key = x.Page + " · " + x.Query
		if x.Impressions > 0 {
			x.CTR = x.Clicks / x.Impressions
		}
		return x, e
	})
}
func aggregateInsightGroups(groups []seointel.SearchGroup, rows []seointel.InsightRow) []seointel.InsightRow {
	out := []seointel.InsightRow{}
	unc := seointel.InsightRow{Key: "Uncategorised", Status: "uncategorised"}
	matched := map[string]bool{}
	for _, g := range groups {
		x := seointel.InsightRow{Key: g.ID, Detail: g.Name, Status: g.Kind}
		for _, r := range rows {
			if seointel.Matches(g, r.Page, r.Query) {
				x.Clicks += r.Clicks
				x.Impressions += r.Impressions
				x.Position += r.Position * r.Impressions
				matched[r.Key] = true
			}
		}
		if x.Impressions > 0 {
			x.Position /= x.Impressions
			x.CTR = x.Clicks / x.Impressions
		}
		out = append(out, x)
	}
	for _, r := range rows {
		if !matched[r.Key] {
			unc.Clicks += r.Clicks
			unc.Impressions += r.Impressions
			unc.Position += r.Position * r.Impressions
		}
	}
	if unc.Impressions > 0 {
		unc.Position /= unc.Impressions
		unc.CTR = unc.Clicks / unc.Impressions
	}
	return append(out, unc)
}
func (s *Server) insightSeries(ctx context.Context, ws, host string, src *seointel.Source, win searchWindow) ([]seointel.InsightPoint, error) {
	rows, e := s.pool.Query(ctx, `SELECT day::text,sum(clicks)::float8 FROM search_daily WHERE workspace_id=$1 AND integration_id=$2 AND site=$3 AND day BETWEEN $4 AND $5 AND `+seointel.PageHostSQL("page")+`=ANY($6) GROUP BY day ORDER BY day`, ws, src.IntegrationID, src.Property, win.from, win.to, seointel.PageHosts(host))
	if e != nil {
		return nil, e
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightPoint, error) {
		var x seointel.InsightPoint
		e := r.Scan(&x.Date, &x.Value)
		return x, e
	})
}
func (s *Server) insightSegments(ctx context.Context, ws, brand, host string, src *seointel.Source, win searchWindow, country, device string, group *seointel.SearchGroup, out *seointel.InsightReport) error {
	if group != nil && group.Kind == "topic" {
		out.Status = "unsupported"
		out.Notes = append(out.Notes, "Topic filters require query data, which is not present in country/device segments.")
		return nil
	}
	rows, e := s.pool.Query(ctx, `SELECT page,country,device,COALESCE(sum(clicks) FILTER(WHERE day >= $5),0)::float8,COALESCE(sum(impressions) FILTER(WHERE day >= $5),0)::float8,COALESCE(sum(position*impressions) FILTER(WHERE day >= $5)/NULLIF(sum(impressions) FILTER(WHERE day >= $5),0),0),COALESCE(sum(clicks) FILTER(WHERE day < $5),0)::float8 FROM search_segments WHERE workspace_id=$1 AND (brand_id=$2 OR brand_id IS NULL) AND integration_id=$3 AND site=$4 AND day BETWEEN $5::date-($6::date-$5::date+1) AND $6 AND `+seointel.PageHostSQL("page")+`=ANY($7) AND ($8='' OR country=$8) AND ($9='' OR device=$9) GROUP BY page,country,device ORDER BY COALESCE(sum(clicks) FILTER(WHERE day >= $5),0) DESC LIMIT 10000`, ws, brand, src.IntegrationID, src.Property, win.from, win.to, seointel.PageHosts(host), country, device)
	if e != nil {
		return e
	}
	list, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightRow, error) {
		var x seointel.InsightRow
		e := r.Scan(&x.Page, &x.Country, &x.Device, &x.Clicks, &x.Impressions, &x.Position, &x.Before)
		x.Key = x.Page + " · " + x.Country + " · " + x.Device
		x.After = x.Clicks
		if x.Before > 0 {
			x.Change = (x.After - x.Before) / x.Before
			x.Status = "compared"
		} else {
			x.Status = "no_baseline"
		}
		if x.Impressions > 0 {
			x.CTR = x.Clicks / x.Impressions
		}
		return x, e
	})
	if e != nil {
		return e
	}
	end, _ := time.Parse(time.DateOnly, win.to)
	begin, _ := time.Parse(time.DateOnly, win.from)
	days := int(end.Sub(begin).Hours()/24) + 1
	var baselineDays int
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM search_completeness WHERE workspace_id=$1 AND (brand_id=$2 OR brand_id IS NULL) AND integration_id=$3 AND site=$4 AND day BETWEEN $5::date-$6::int AND $5::date-1`, ws, brand, src.IntegrationID, src.Property, win.from, days).Scan(&baselineDays); e != nil {
		return e
	}
	for _, x := range list {
		if baselineDays < days {
			x.Status = "partial_history"
			x.Change = 0
		}
		if group == nil || seointel.Matches(*group, x.Page, "") {
			out.Data = append(out.Data, x)
		}
	}
	out.Notes = append(out.Notes, "Before/after compares clicks with the preceding equal-length window. Country/device collection has its own backfill; missing history is not zero traffic. Up to 50,000 segment rows are collected per day and 10,000 aggregate rows analysed.")
	return nil
}
func (s *Server) insightCompleteness(ctx context.Context, ws, brand string, src *seointel.Source, win searchWindow, out *seointel.InsightReport) error {
	// Independent GSC totals cannot be split by host or brand. Do not expose
	// another brand's traffic through a shared domain property's aggregate.
	var spansBrands bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM brands b CROSS JOIN LATERAL unnest(b.domains) d WHERE b.workspace_id=$1 AND b.id<>$2 AND b.status='active' AND (lower(d)=$3 OR ($4 AND lower(d) LIKE '%.'||$3)))`, ws, brand, src.Host, strings.HasPrefix(src.Property, "sc-domain:")).Scan(&spansBrands); err != nil {
		return err
	}
	if spansBrands {
		out.Status = "property_spans_brands"
		out.Notes = append(out.Notes, "Property-wide totals also cover another brand. Use a narrower Search Console property to measure completeness without mixing brands.")
		return nil
	}

	rows, e := s.pool.Query(ctx, `SELECT day::text,clicks,impressions,visible_clicks,visible_impressions,rows_read,capped FROM search_completeness WHERE workspace_id=$1 AND brand_id=$2 AND integration_id=$3 AND site=$4 AND day BETWEEN $5 AND $6 ORDER BY day`, ws, brand, src.IntegrationID, src.Property, win.from, win.to)
	if e != nil {
		return e
	}
	data, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightRow, error) {
		var x seointel.InsightRow
		var vi float64
		var n int
		var cap bool
		e := r.Scan(&x.Date, &x.Clicks, &x.Impressions, &x.Before, &vi, &n, &cap)
		x.Key = x.Date
		x.After = math.Max(0, x.Clicks-x.Before)
		if x.Clicks > 0 {
			x.Share = x.After / x.Clicks
		}
		x.Value = float64(n)
		x.Status = "not_capped"
		if cap {
			x.Status = "capped"
		}
		return x, e
	})
	out.Data = data
	out.Notes = append(out.Notes, "Property-wide totals versus page/query rows: the gap combines anonymised queries, API omissions and collection limits. It cannot be attributed to sampling alone. Only explicitly assigned properties are shown.")
	return e
}
func (s *Server) insightDecay(ctx context.Context, ws, host string, src *seointel.Source, win searchWindow, bucket string, threshold float64, group *seointel.SearchGroup, out *seointel.InsightReport) error {
	// Exclude open calendar buckets; partial weeks/months would look like decay.
	start, _ := time.Parse(time.DateOnly, win.from)
	end, _ := time.Parse(time.DateOnly, win.to)
	if src.OldestDay != nil && start.Format(time.DateOnly) < *src.OldestDay {
		start, _ = time.Parse(time.DateOnly, *src.OldestDay)
	}
	if src.NewestDay != nil && end.Format(time.DateOnly) > *src.NewestDay {
		end, _ = time.Parse(time.DateOnly, *src.NewestDay)
	}
	if r := end.AddDate(0, 0, 1); bucket == "month" {
		end = time.Date(r.Year(), r.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
		if start.Day() != 1 {
			start = time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		}
	} else {
		r := end.AddDate(0, 0, 1)
		end = r.AddDate(0, 0, -(int(r.Weekday())+6)%7-1)
		if start.Weekday() != time.Monday {
			start = start.AddDate(0, 0, (8-int(start.Weekday()))%7)
		}
	}
	rows, e := s.pool.Query(ctx, `SELECT page,query,date_trunc($7,day)::date::text,sum(clicks)::float8 FROM search_daily WHERE workspace_id=$1 AND integration_id=$2 AND site=$3 AND day BETWEEN $4 AND $5 AND `+seointel.PageHostSQL("page")+`=ANY($6) GROUP BY page,query,3 ORDER BY page,query,3 LIMIT 100001`, ws, src.IntegrationID, src.Property, start, end, seointel.PageHosts(host), bucket)
	if e != nil {
		return e
	}
	type cell struct {
		page, query, date string
		clicks            float64
	}
	cells, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cell, error) {
		var x cell
		e := r.Scan(&x.page, &x.query, &x.date, &x.clicks)
		return x, e
	})
	if e != nil {
		return e
	}
	pages := map[string]map[string]float64{}
	for _, c := range cells {
		if group != nil && !seointel.Matches(*group, c.page, c.query) {
			continue
		}
		if pages[c.page] == nil {
			pages[c.page] = map[string]float64{}
		}
		pages[c.page][c.date] += c.clicks
	}
	buckets := []string{}
	for d := start; !d.After(end); {
		buckets = append(buckets, d.Format(time.DateOnly))
		if bucket == "week" {
			d = d.AddDate(0, 0, 7)
		} else {
			d = d.AddDate(0, 1, 0)
		}
	}
	for page, values := range pages {
		if len(buckets) < 2 {
			break
		}
		x := seointel.InsightRow{Key: page, Page: page}
		for _, d := range buckets {
			x.Values = append(x.Values, seointel.InsightPoint{Date: d, Value: values[d]})
		}
		x.Before = values[buckets[0]]
		x.After = values[buckets[len(buckets)-1]]
		if x.Before >= 10 {
			x.Change = (x.After - x.Before) / x.Before
			if x.Change <= -threshold {
				x.Score = x.Before - x.After
				x.Status = "declining"
				out.Data = append(out.Data, x)
			}
		}
	}
	sort.Slice(out.Data, func(i, j int) bool { return out.Data[i].Score > out.Data[j].Score })
	out.Notes = append(out.Notes, "Complete calendar buckets only; first versus last bucket. Missing query/page rows can also reflect privacy or collection limits.")
	return nil
}
