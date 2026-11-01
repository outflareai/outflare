package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type searchMapping struct {
	Source string `json:"source"`
	Target string `json:"target"`
}
type searchMigration struct {
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name"`
	TargetSiteID string          `json:"target_site_id"`
	GoLive       string          `json:"go_live"`
	Mappings     []searchMapping `json:"mappings"`
}

func (s *Server) migrationsFor(ctx context.Context, ws, brand, site string) ([]searchMigration, error) {
	rows, e := s.pool.Query(ctx, `SELECT id::text,name,target_site_id::text,go_live::text,mappings FROM search_migrations WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 ORDER BY go_live DESC`, ws, brand, site)
	if e != nil {
		return nil, e
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (searchMigration, error) {
		var m searchMigration
		var raw []byte
		e := r.Scan(&m.ID, &m.Name, &m.TargetSiteID, &m.GoLive, &raw)
		if e == nil {
			e = json.Unmarshal(raw, &m.Mappings)
		}
		return m, e
	})
}
func (s *Server) listSearchMigrations(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	list, e := s.migrationsFor(r.Context(), a.WorkspaceID, a.Brand, site.ID)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"data": list})
}
func (s *Server) saveSearchMigration(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var m searchMigration
	if !decode(w, r, &m) {
		return
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 120 || !validUUID(m.TargetSiteID) || dateOnly(m.GoLive) == "" || len(m.Mappings) > 1000 {
		writeError(w, r, 400, "invalid_request", "Give a name, target site, go-live date and up to 1,000 mappings.")
		return
	}
	var host string
	err := s.pool.QueryRow(r.Context(), `SELECT host FROM seo_sites WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, m.TargetSiteID).Scan(&host)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 404, "not_found", "target site not found in this brand")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	seen := map[string]bool{}
	for _, x := range m.Mappings {
		src, e := url.Parse(x.Source)
		dst, f := url.Parse(x.Target)
		if e != nil || f != nil || !migrationURL(src, site.Host) || !migrationURL(dst, host) || seen[x.Source] {
			writeError(w, r, 400, "invalid_request", "Mappings must use unique source URLs on this site and target URLs on the target site, with http or https and no credentials.")
			return
		}
		seen[x.Source] = true
	}
	if m.TargetSiteID == site.ID && len(m.Mappings) == 0 {
		writeError(w, r, 400, "invalid_request", "A restructure within one site needs source-to-target URL mappings.")
		return
	}
	raw, _ := json.Marshal(m.Mappings)
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if m.ID != "" {
			if !validUUID(m.ID) {
				return errInsightNotFound
			}
			tag, e := tx.Exec(r.Context(), `UPDATE search_migrations SET name=$5,target_site_id=$6,go_live=$7,mappings=$8 WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, site.ID, m.ID, m.Name, m.TargetSiteID, m.GoLive, raw)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				return errInsightNotFound
			}
		} else {
			if e := tx.QueryRow(r.Context(), `INSERT INTO search_migrations(workspace_id,brand_id,site_id,name,target_site_id,go_live,mappings) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(site_id,name) DO UPDATE SET target_site_id=EXCLUDED.target_site_id,go_live=EXCLUDED.go_live,mappings=EXCLUDED.mappings RETURNING id::text`, a.WorkspaceID, a.Brand, site.ID, m.Name, m.TargetSiteID, m.GoLive, raw).Scan(&m.ID); e != nil {
				return e
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_migration.saved", "search_migration", m.ID, map[string]any{"brand_id": a.Brand, "site_id": site.ID})
	})
	if errors.Is(err, errInsightNotFound) {
		writeError(w, r, 404, "not_found", "migration not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}
func migrationURL(u *url.URL, host string) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Fragment == "" && len(u.String()) <= 1000 && seointel.Bare(u.Hostname()) == seointel.Bare(host)
}
func (s *Server) insightMigrations(ctx context.Context, ws, brand string, site *seoSite, id string, end time.Time, out *seointel.InsightReport) error {
	list, e := s.migrationsFor(ctx, ws, brand, site.ID)
	if e != nil {
		return e
	}
	if len(list) == 0 {
		out.Status = "not_configured"
		return nil
	}
	var m *searchMigration
	for i := range list {
		if id == "" || list[i].ID == id {
			m = &list[i]
			break
		}
	}
	if m == nil {
		out.Status = "not_found"
		return nil
	}
	var targetHost string
	if e = s.pool.QueryRow(ctx, `SELECT host FROM seo_sites WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, m.TargetSiteID).Scan(&targetHost); e != nil {
		return e
	}
	live, _ := time.Parse(time.DateOnly, m.GoLive)
	if !end.After(live) {
		out.Status = "not_started"
		return nil
	}
	targetSrc, e := seointel.SourceForBrand(ctx, s.pool, ws, brand, targetHost)
	if e != nil {
		return e
	}
	oldSrc, e := seointel.SourceForBrand(ctx, s.pool, ws, brand, site.Host)
	if e != nil {
		return e
	}
	if oldSrc == nil || targetSrc == nil {
		out.Status = "not_connected"
		return nil
	}
	if targetSrc.NewestDay != nil {
		settled, _ := time.Parse(time.DateOnly, *targetSrc.NewestDay)
		if settled.Before(end) {
			end = settled
		}
	}
	days := min(28, int(end.Sub(live).Hours()/24))
	if days < 1 {
		out.Status = "insufficient_data"
		return nil
	}
	sourceURLs, targetURLs := []string{}, []string{}
	for _, x := range m.Mappings {
		sourceURLs = append(sourceURLs, x.Source)
		targetURLs = append(targetURLs, x.Target)
	}
	before, e := s.migrationSeries(ctx, ws, site.Host, oldSrc, sourceURLs, searchWindow{from: live.AddDate(0, 0, -days).Format(time.DateOnly), to: live.AddDate(0, 0, -1).Format(time.DateOnly)})
	if e != nil {
		return e
	}
	after, e := s.migrationSeries(ctx, ws, targetHost, targetSrc, targetURLs, searchWindow{from: end.AddDate(0, 0, -days+1).Format(time.DateOnly), to: end.Format(time.DateOnly)})
	if e != nil {
		return e
	}
	if len(before) < days || len(after) < days {
		out.Status = "insufficient_data"
		out.Notes = append(out.Notes, "Missing baseline or post-launch days; recovery is not calculated from partial windows.")
	} else {
		var b, a float64
		for _, p := range before {
			b += p.Value
		}
		for _, p := range after {
			a += p.Value
		}
		out.Summary["baseline_clicks_per_day"] = b / float64(days)
		out.Summary["current_clicks_per_day"] = a / float64(days)
		if b > 0 {
			recovery := a / b
			out.Summary["recovery"] = recovery
			out.Summary["risk_score"] = math.Max(0, math.Min(100, 100*(1-recovery)))
		}
		out.Summary["days_compared"] = float64(days)
	}
	// Read redirect evidence from existing crawls. No unchecked URLs are fetched.
	// Follow only known hops; absence is unknown, never a successful redirect.
	type evidence struct {
		status    *int
		redirect  string
		noindex   bool
		canonical string
	}
	load := func(siteID string) (map[string]evidence, error) {
		rows, e := s.pool.Query(ctx, `SELECT url,http_status,redirect_to,noindex,COALESCE(canonical,'') FROM crawl_pages WHERE workspace_id=$1 AND run_id=(SELECT id FROM crawl_runs WHERE workspace_id=$1 AND site_id=$2 AND status='done' ORDER BY finished_at DESC LIMIT 1)`, ws, siteID)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		data := map[string]evidence{}
		for rows.Next() {
			var u string
			var x evidence
			if e = rows.Scan(&u, &x.status, &x.redirect, &x.noindex, &x.canonical); e != nil {
				return nil, e
			}
			data[u] = x
		}
		return data, rows.Err()
	}
	old, e := load(site.ID)
	if e != nil {
		return e
	}
	dest, e := load(m.TargetSiteID)
	if e != nil {
		return e
	}
	for u, x := range dest {
		old[u] = x
	}
	passed, known := 0, 0
	for _, mapping := range m.Mappings {
		x := seointel.InsightRow{Key: mapping.Source, Source: mapping.Source, Target: mapping.Target, Status: "unknown", Detail: "Run a crawl of both sites to collect redirect evidence."}
		current := mapping.Source
		seen := map[string]bool{}
		hops := 0
		for hops < 10 {
			v, ok := old[current]
			if !ok || v.status == nil {
				break
			}
			if seen[current] {
				x.Status = "loop"
				break
			}
			seen[current] = true
			if *v.status >= 300 && *v.status < 400 {
				if *v.status != 301 && *v.status != 308 {
					x.Status = "temporary_redirect"
					break
				}
				if v.redirect == "" {
					x.Status = "missing_location"
					break
				}
				u, _ := url.Parse(current)
				next, e := url.Parse(v.redirect)
				if e != nil {
					break
				}
				current = u.ResolveReference(next).String()
				hops++
				continue
			}
			switch {
			case *v.status >= 400:
				x.Status = "broken"
			case current != mapping.Target:
				x.Status = "wrong_target"
			case hops == 0:
				x.Status = "missing_redirect"
			case *v.status != 200:
				x.Status = "unhealthy_target"
			case v.noindex:
				x.Status = "noindex_target"
			case v.canonical != "" && v.canonical != mapping.Target:
				x.Status = "canonical_mismatch"
			case hops > 1:
				x.Status = "redirect_chain"
			default:
				x.Status = "healthy"
			}
			break
		}
		if hops >= 10 {
			x.Status = "too_many_hops"
		}
		if x.Status != "unknown" {
			known++
			x.Detail = fmt.Sprintf("%d redirect hops from stored crawl evidence", hops)
		}
		if x.Status == "healthy" {
			passed++
		}
		out.Data = append(out.Data, x)
	}
	out.Summary["checked_mappings"] = float64(known)
	out.Summary["healthy_mappings"] = float64(passed)
	if known > 0 {
		out.Summary["url_success"] = float64(passed) / float64(known)
	}
	out.Notes = append(out.Notes, "Recovery compares mapped URLs (or whole sites without mappings) over equal settled windows, at most 28 days. Risk is a heuristic from click recovery, not a calibrated probability. Redirect evidence comes from the latest completed crawls; unknown mappings are excluded from success rate.")
	return nil
}

func (s *Server) migrationSeries(ctx context.Context, ws, host string, src *seointel.Source, pages []string, win searchWindow) ([]seointel.InsightPoint, error) {
	rows, e := s.pool.Query(ctx, `SELECT day::text,sum(clicks)::float8 FROM search_daily WHERE workspace_id=$1 AND integration_id=$2 AND site=$3 AND day BETWEEN $4 AND $5 AND `+seointel.PageHostSQL("page")+`=ANY($6) AND (cardinality($7::text[])=0 OR page=ANY($7)) GROUP BY day ORDER BY day`, ws, src.IntegrationID, src.Property, win.from, win.to, seointel.PageHosts(host), pages)
	if e != nil {
		return nil, e
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightPoint, error) {
		var x seointel.InsightPoint
		e := r.Scan(&x.Date, &x.Value)
		return x, e
	})
}
