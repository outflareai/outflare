package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/coverage"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Where competitors appear: each competitor the brand tracks (its SEO
// competitors) is scanned on a schedule, once a month unless the brand
// (for its top competitors or the rest) or the competitor says otherwise. A scan reads search results for the competitor's name and
// who links to its site, on the workspace's own connections and within
// their budgets, and the pages AI answers cite when they name it. Each
// page is kept with its type, whether it looks paid for (only with
// evidence) and the site's numbers. Nothing here contacts anyone.

const (
	jobCompetitorScan = "coverage.competitor_scan"
	// competitorScanSpacing is the gap between the daily scans of one
	// brand's competitors.
	competitorScanSpacing = 30 * time.Second
	// competitorLinkRows is how many linking pages one scan reads.
	competitorLinkRows = 100
	// competitorSearchDepth is how many search results one scan reads.
	competitorSearchDepth = 50
	// presenceDefaultEveryDays is how often a new brand scans its top
	// competitors (the rest already default to monthly): once a month.
	// It is written on insert rather than as the column's default, so
	// the previous version's own default still holds after a rollback.
	presenceDefaultEveryDays = 30
)

func (s *Server) routesPresenceCompetitors(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/coverage/competitors", s.onBrand(auth.ScopeCoverageRead, s.coverageCompetitors))
	mux.HandleFunc("PATCH /v1/brands/{brand}/coverage/competitors/settings", s.onBrand(auth.ScopeCoverageManage, s.coverageCompetitorSettings))
	mux.HandleFunc("PATCH /v1/brands/{brand}/coverage/competitors/{id}", s.onBrand(auth.ScopeCoverageManage, s.coverageCompetitorUpdate))
	mux.HandleFunc("POST /v1/brands/{brand}/coverage/competitors/{id}/scan", s.onBrand(auth.ScopeCoverageManage, s.coverageCompetitorScanNow))
	mux.HandleFunc("GET /v1/brands/{brand}/coverage/competitor_placements", s.onBrand(auth.ScopeCoverageRead, s.coverageCompetitorPlacements))
}

// competitorCadenceSQL ranks each tracked competitor within its brand and
// gives the days between its scans: its own, or the brand's for its rank.
const competitorCadenceSQL = `SELECT c.id, c.workspace_id, c.brand_id, c.host, c.name, c.presence_every_days, c.presence_queued_at, c.presence_scanned_at, c.presence_outcome,
 row_number() OVER (PARTITION BY c.workspace_id, c.brand_id ORDER BY c.common_keywords DESC NULLS LAST, c.created_at, c.id) AS rank,
 b.presence_top_competitors AS top_n, b.presence_top_every_days AS top_days, b.presence_rest_every_days AS rest_days
 FROM seo_competitors c JOIN brands b ON b.id=c.brand_id AND b.workspace_id=c.workspace_id
 WHERE c.status='tracked'`

const competitorEverySQL = `coalesce(presence_every_days, CASE WHEN rank<=top_n THEN top_days ELSE rest_days END)`

// queueCompetitorScans queues the scans that are due.
func (s *Server) queueCompetitorScans(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH ranked AS (`+competitorCadenceSQL+` AND b.status='active'),
 due AS (SELECT id FROM ranked WHERE presence_queued_at IS NULL OR presence_queued_at<=now()-make_interval(days=>`+competitorEverySQL+`)
  ORDER BY presence_queued_at NULLS FIRST LIMIT 100)
 UPDATE seo_competitors c SET presence_queued_at=now() WHERE c.id IN (SELECT id FROM due)
 RETURNING c.workspace_id::text, c.brand_id::text, c.id::text`)
		if err != nil {
			return err
		}
		type due struct{ ws, brand, id string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.ws, &d.brand, &d.id)
		})
		if err != nil {
			return err
		}
		now := time.Now()
		day := now.UTC().Format("2006-01-02")
		// Scans of one brand's competitors are spread out rather than all
		// started at once, so a long list never takes every worker.
		perBrand := map[string]int{}
		for _, d := range list {
			at := now.Add(time.Duration(perBrand[d.brand]) * competitorScanSpacing)
			perBrand[d.brand]++
			if err := enqueueCompetitorScanAt(ctx, tx, d.ws, d.brand, d.id, day, at); err != nil {
				return err
			}
		}
		return nil
	})
}

func enqueueCompetitorScan(ctx context.Context, tx pgx.Tx, ws, brand, id, key string) error {
	return enqueueCompetitorScanAt(ctx, tx, ws, brand, id, key, time.Time{})
}

func enqueueCompetitorScanAt(ctx context.Context, tx pgx.Tx, ws, brand, id, key string, at time.Time) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCompetitorScan, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brand, "competitor_id": id},
		IdempotencyKey: "coverage.competitor_scan:" + id + ":" + key, RunAt: at, MaxAttempts: 3})
	return err
}

// competitorFind is one page a scan found.
type competitorFind struct {
	url, host, title           string
	sources                    []string
	linksToThem                bool
	nofollow                   *bool
	anchor                     string
	rating, urlRating, traffic *float64
	lost                       *bool
}

// coverageCompetitorScanJob scans where one competitor appears.
func (s *Server) coverageCompetitorScanJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Brand      string `json:"brand_id"`
		Competitor string `json:"competitor_id"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.Brand) || !validUUID(req.Competitor) {
		return fmt.Errorf("%w: invalid competitor scan job", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	// What its searches cost is the brand's.
	ctx = outcomes.WithBrand(ctx, req.Brand)
	var host, name, siteID, website string
	var aliases, domains []string
	err := s.pool.QueryRow(ctx, `SELECT c.host, c.name, c.aliases, c.site_id::text, b.website, b.domains FROM seo_competitors c
 JOIN brands b ON b.id=c.brand_id AND b.workspace_id=c.workspace_id
 WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.id=$3 AND c.status='tracked'`, ws, req.Brand, req.Competitor).Scan(&host, &name, &aliases, &siteID, &website, &domains)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	skip := append(coverageBrandHosts("", website, domains), strings.TrimPrefix(strings.ToLower(host), "www."))
	finds := map[string]*competitorFind{}
	add := func(raw, title, source string) *competitorFind {
		canonical, err := coverageURL(raw)
		if err != nil || canonical == "" || len(canonical) > 2048 {
			return nil
		}
		u, _ := url.Parse(canonical)
		h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if h == "" || coverageHostIn(h, skip) {
			return nil
		}
		f := finds[canonical]
		if f == nil {
			f = &competitorFind{url: canonical, host: h}
			finds[canonical] = f
		}
		if f.title == "" {
			f.title = truncRunes(strings.TrimSpace(title), 500)
		}
		if !slices.Contains(f.sources, source) {
			f.sources = append(f.sources, source)
		}
		return f
	}
	evidence := map[string][]string{}
	outcome := map[string]string{}
	fail := func(source, code string, err error) {
		outcome[source] = code
		metrics.CompetitorScan(source, code)
		if err != nil {
			track.Fail(ctx, s.log, track.Event{Name: "coverage_competitor.scan_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"brand_id": req.Brand, "competitor_id": req.Competitor, "source": source}}, code, err)
		}
	}
	done := func(source string) {
		outcome[source] = ""
		metrics.CompetitorScan(source, "done")
	}

	// Search results for the competitor's name, on the workspace's own
	// search results connection.
	query := strings.TrimSpace(strings.ReplaceAll(name, `"`, ""))
	if query == "" {
		query = host
	}
	integration := ""
	if s.integrations != nil {
		if integration, err = ranks.DefaultConnection(ctx, s.pool, s.integrations.Registry, ws); err != nil {
			return err
		}
	}
	if integration == "" {
		fail("web_search", "connector_needed", nil)
	} else if o, err := s.integrations.Open(ctx, s.pool, ws, integration); err != nil {
		fail("web_search", "connector_unavailable", err)
	} else if res, err := ranks.Search(ctx, s.pool, s.integrations, o, ranks.SearchQuery{SERPQuery: connectors.SERPQuery{Keyword: `"` + query + `"`, Country: "US", Device: "desktop", Depth: competitorSearchDepth}}); err != nil {
		fail("web_search", ranks.SearchErrorCode(err), err)
	} else {
		for _, item := range res.Items {
			if f := add(item.URL, item.Title, "web_search"); f != nil {
				evidence[f.url] = append(evidence[f.url], coverage.PaidEvidence(item.URL, item.Title, "", false)...)
			}
		}
		done("web_search")
	}

	// Who links to the competitor's site, on the workspace's own SEO
	// connection; a copy under a day old is reused.
	readerID := ""
	if s.integrations != nil {
		if readerID, err = s.linkReaderID(ctx, ws, ""); err != nil {
			return err
		}
	}
	if readerID == "" {
		fail("backlinks", "connector_needed", nil)
	} else if o, err := s.integrations.Open(ctx, s.pool, ws, readerID); err != nil {
		fail("backlinks", "connector_unavailable", err)
	} else if reader, ok := o.Connector.(connectors.SEOReader); !ok {
		fail("backlinks", "connector_invalid", nil)
	} else {
		brand, competitor := req.Brand, req.Competitor
		read, err := seosync.ReadLinks(ctx, s.pool, o, reader, ws, seosync.LinkReadRequest{Target: host, Limit: competitorLinkRows, SiteID: &siteID, BrandID: &brand,
			Reason: "competitor_presence", CompetitorID: &competitor, Rec: seosync.SystemRecorder(ws)})
		if err != nil {
			fail("backlinks", seosync.LinkReadErrorCode(err), err)
		} else {
			rows, err := s.pool.Query(ctx, `SELECT source_url, title, target_url, anchor, nofollow, lost, source_rating, url_rating, domain_traffic
 FROM link_read_rows WHERE workspace_id=$1 AND read_id=$2`, ws, read.ID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var src, title, target, anchor string
				var nofollow, lost bool
				var rating, urlRating, traffic *float64
				if err := rows.Scan(&src, &title, &target, &anchor, &nofollow, &lost, &rating, &urlRating, &traffic); err != nil {
					rows.Close()
					return err
				}
				f := add(src, title, "backlinks")
				if f == nil {
					continue
				}
				f.linksToThem, f.nofollow, f.anchor = true, &nofollow, truncRunes(anchor, 500)
				f.rating, f.urlRating, f.traffic, f.lost = rating, urlRating, traffic, &lost
				evidence[f.url] = append(evidence[f.url], coverage.PaidEvidence(src, title, target, false)...)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			done("backlinks")
		}
	}

	// Pages AI answers cite when they name the competitor, from the
	// brand's AI visibility samples; this needs no connection of its own.
	names := []string{}
	for _, n := range append([]string{name}, aliases...) {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (ci.url) ci.url, ci.title FROM ai_citations ci
 JOIN ai_cohorts co ON co.id=ci.cohort_id AND co.workspace_id=ci.workspace_id
 WHERE ci.workspace_id=$1 AND co.brand_id=$2 AND EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id=ci.sample_id AND m.entity='competitor' AND lower(m.name)=ANY($3))
 AND EXISTS (SELECT 1 FROM ai_samples sa WHERE sa.id=ci.sample_id AND sa.sampled_at>now()-interval '90 days')
 ORDER BY ci.url LIMIT 200`, ws, req.Brand, names)
		if err != nil {
			fail("ai_citations", "internal", err)
		} else {
			for rows.Next() {
				var u, title string
				if err := rows.Scan(&u, &title); err != nil {
					rows.Close()
					return err
				}
				if f := add(u, title, "ai_citations"); f != nil {
					evidence[f.url] = append(evidence[f.url], coverage.PaidEvidence(u, title, "", false)...)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				fail("ai_citations", "internal", err)
			} else {
				done("ai_citations")
			}
		}
	}

	added := map[string]int{}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, f := range finds {
			ev := dedupe(evidence[f.url])
			arrangement := "unknown"
			if len(ev) > 0 {
				arrangement = "likely_paid"
			}
			var inserted bool
			if err := tx.QueryRow(ctx, `INSERT INTO coverage_competitor_placements AS p (workspace_id,brand_id,competitor_id,url,host,title,kind,arrangement,evidence,sources,
 links_to_them,nofollow,anchor,domain_rating,url_rating,domain_traffic,gone_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,CASE WHEN $17::boolean THEN now() END)
 ON CONFLICT (competitor_id,url) DO UPDATE SET
  title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE p.title END,
  kind=CASE WHEN p.kind='' THEN excluded.kind ELSE p.kind END,
  evidence=ARRAY(SELECT DISTINCT e FROM unnest(p.evidence||excluded.evidence) e ORDER BY e),
  arrangement=CASE WHEN cardinality(p.evidence||excluded.evidence)>0 THEN 'likely_paid' ELSE 'unknown' END,
  sources=ARRAY(SELECT DISTINCT x FROM unnest(p.sources||excluded.sources) x ORDER BY x),
  links_to_them=p.links_to_them OR excluded.links_to_them,
  nofollow=coalesce(excluded.nofollow,p.nofollow),
  anchor=CASE WHEN excluded.anchor<>'' THEN excluded.anchor ELSE p.anchor END,
  domain_rating=coalesce(excluded.domain_rating,p.domain_rating),
  url_rating=coalesce(excluded.url_rating,p.url_rating),
  domain_traffic=coalesce(excluded.domain_traffic,p.domain_traffic),
  gone_at=CASE WHEN $17::boolean IS NULL THEN p.gone_at WHEN $17 THEN coalesce(p.gone_at,now()) END,
  last_seen_at=now(), updated_at=now()
 RETURNING xmax=0`, ws, req.Brand, req.Competitor, f.url, f.host, f.title, coverage.Classify(f.url), arrangement, ev, f.sources,
				f.linksToThem, f.nofollow, f.anchor, f.rating, f.urlRating, f.traffic, f.lost).Scan(&inserted); err != nil {
				return err
			}
			if inserted {
				added[arrangement]++
			}
		}
		// What each page is worth to the brand, now that this competitor's
		// finds (and the count of competitors on each page) are in.
		urls := make([]string, 0, len(finds))
		for _, f := range finds {
			urls = append(urls, f.url)
		}
		if _, err := valueCompetitorPlacements(ctx, tx, ws, req.Brand, urls); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE seo_competitors SET presence_scanned_at=now(), presence_outcome=$3 WHERE workspace_id=$1 AND id=$2`, ws, req.Competitor, outcome); err != nil {
			return err
		}
		props := map[string]any{"brand_id": req.Brand, "competitor_id": req.Competitor, "found": len(finds), "added": added["likely_paid"] + added["unknown"], "likely_paid": added["likely_paid"]}
		for source, code := range outcome {
			if code == "" {
				code = "done"
			}
			props[source] = code
		}
		if err := recordWithProps(ctx, tx, nil, ws, "coverage_competitor.scanned", "seo_competitors", req.Competitor, nil, props); err != nil {
			return err
		}
		// The brand's opportunities are refreshed once for all the scans
		// that finish close together, not inside each one: a refresh
		// rewrites every opportunity the brand has, so dozens of scans
		// doing it at once queue on the same rows and hold the database.
		return enqueueOpportunitiesRefresh(ctx, tx, ws, req.Brand, time.Now())
	})
	if err != nil {
		return err
	}
	for arrangement, n := range added {
		metrics.CompetitorFound(arrangement, n)
	}
	return nil
}

func dedupe(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// coverageCompetitors lists the brand's tracked competitors with when
// each is scanned, and the brand's scan settings.
func (s *Server) coverageCompetitors(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var top, topDays, restDays int
	if err := s.pool.QueryRow(r.Context(), "SELECT presence_top_competitors,presence_top_every_days,presence_rest_every_days FROM brands WHERE workspace_id=$1 AND id=$2",
		a.WorkspaceID, a.Brand).Scan(&top, &topDays, &restDays); s.coverageError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(r.Context(), `WITH ranked AS (`+competitorCadenceSQL+` AND c.workspace_id=$1 AND c.brand_id=$2)
 SELECT jsonb_build_object('id',id,'host',host,'name',name,'rank',rank,'top',rank<=top_n,'every_days',`+competitorEverySQL+`,
  'own_every_days',presence_every_days,'queued_at',presence_queued_at,'scanned_at',presence_scanned_at,'outcome',presence_outcome,
  'next_scan_at',CASE WHEN presence_queued_at IS NULL THEN NULL ELSE presence_queued_at+make_interval(days=>`+competitorEverySQL+`) END,
  'placements',(SELECT count(*) FROM coverage_competitor_placements p WHERE p.workspace_id=$1 AND p.brand_id=$2 AND p.competitor_id=ranked.id AND p.gone_at IS NULL),
  'likely_paid',(SELECT count(*) FROM coverage_competitor_placements p WHERE p.workspace_id=$1 AND p.brand_id=$2 AND p.competitor_id=ranked.id AND p.gone_at IS NULL AND p.arrangement='likely_paid'))
 FROM ranked ORDER BY rank`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	data, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": map[string]int{"top_competitors": top, "top_every_days": topDays, "rest_every_days": restDays}, "data": data})
}

// coverageCompetitorSettings changes how often the brand's competitors are
// scanned: how many count as top, and the days between scans for the top
// ones and the rest.
func (s *Server) coverageCompetitorSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Top      *int `json:"top_competitors"`
		TopDays  *int `json:"top_every_days"`
		RestDays *int `json:"rest_every_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Top != nil && (*in.Top < 0 || *in.Top > 1000)) || (in.TopDays != nil && (*in.TopDays < 1 || *in.TopDays > 90)) || (in.RestDays != nil && (*in.RestDays < 1 || *in.RestDays > 90)) {
		writeError(w, r, 400, "invalid_request", "top_competitors is 0 to 1000; top_every_days and rest_every_days are 1 to 90")
		return
	}
	var out map[string]int
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var top, topDays, restDays int
		if err := tx.QueryRow(r.Context(), `UPDATE brands SET presence_top_competitors=coalesce($3,presence_top_competitors),
 presence_top_every_days=coalesce($4,presence_top_every_days), presence_rest_every_days=coalesce($5,presence_rest_every_days)
 WHERE workspace_id=$1 AND id=$2 RETURNING presence_top_competitors,presence_top_every_days,presence_rest_every_days`,
			a.WorkspaceID, a.Brand, in.Top, in.TopDays, in.RestDays).Scan(&top, &topDays, &restDays); err != nil {
			return err
		}
		out = map[string]int{"top_competitors": top, "top_every_days": topDays, "rest_every_days": restDays}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "coverage_competitor_settings.updated", "brands", a.Brand,
			map[string]any{"top_competitors": top, "top_every_days": topDays, "rest_every_days": restDays},
			map[string]any{"brand_id": a.Brand, "top_competitors": top, "top_every_days": topDays, "rest_every_days": restDays})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}

// coverageCompetitorUpdate sets one competitor's own scan cadence, or
// clears it (null) so it follows the brand's.
func (s *Server) coverageCompetitorUpdate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "Competitor not found")
		return
	}
	var in struct {
		Every json.RawMessage `json:"every_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	var every *int
	if len(in.Every) > 0 && string(in.Every) != "null" {
		var n int
		if err := json.Unmarshal(in.Every, &n); err != nil || n < 1 || n > 90 {
			writeError(w, r, 400, "invalid_request", "every_days is a whole number from 1 to 90, or null to follow the brand")
			return
		}
		every = &n
	} else if len(in.Every) == 0 {
		writeError(w, r, 400, "invalid_request", "every_days is required")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var found string
		if err := tx.QueryRow(r.Context(), `UPDATE seo_competitors SET presence_every_days=$4, updated_at=now()
 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND status='tracked' RETURNING id::text`, a.WorkspaceID, a.Brand, id, every).Scan(&found); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "coverage_competitor.updated", "seo_competitors", id,
			map[string]any{"every_days": every}, map[string]any{"brand_id": a.Brand, "own_cadence": every != nil})
	})
	if s.coverageError(w, r, err) {
		return
	}
	s.coverageCompetitorOne(w, r, id)
}

func (s *Server) coverageCompetitorOne(w http.ResponseWriter, r *http.Request, id string) {
	a := accessFrom(r.Context())
	var data json.RawMessage
	err := s.pool.QueryRow(r.Context(), `WITH ranked AS (`+competitorCadenceSQL+` AND c.workspace_id=$1 AND c.brand_id=$2)
 SELECT jsonb_build_object('id',id,'host',host,'name',name,'rank',rank,'top',rank<=top_n,'every_days',`+competitorEverySQL+`,
  'own_every_days',presence_every_days,'queued_at',presence_queued_at,'scanned_at',presence_scanned_at,'outcome',presence_outcome)
 FROM ranked WHERE id=$3`, a.WorkspaceID, a.Brand, id).Scan(&data)
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, data)
}

// coverageCompetitorScanNow queues a scan of one competitor now.
func (s *Server) coverageCompetitorScanNow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "Competitor not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var found string
		if err := tx.QueryRow(r.Context(), `UPDATE seo_competitors SET presence_queued_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND status='tracked' RETURNING id::text`,
			a.WorkspaceID, a.Brand, id).Scan(&found); err != nil {
			return err
		}
		if err := enqueueCompetitorScan(r.Context(), tx, a.WorkspaceID, a.Brand, id, time.Now().UTC().Format("200601021504")); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "coverage_competitor.scan_queued", "seo_competitors", id, nil, map[string]any{"brand_id": a.Brand})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 202, map[string]any{"queued": true})
}

// coverageCompetitorPlacements lists where the brand's competitors
// appear. gap=true keeps the sites where the brand has no publisher yet:
// the places to win.
func (s *Server) coverageCompetitorPlacements(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	args := []any{a.WorkspaceID, a.Brand}
	where := "p.workspace_id=$1 AND p.brand_id=$2"
	add := func(clause string, v any) {
		args = append(args, v)
		where += " AND " + fmt.Sprintf(clause, len(args))
	}
	if v := q.Get("competitor_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, 400, "invalid_request", "competitor_id must be an id")
			return
		}
		add("p.competitor_id=$%d", v)
	}
	if v := q.Get("kind"); v != "" {
		add("p.kind=$%d", v)
	}
	if v := q.Get("arrangement"); v != "" {
		if v != "likely_paid" && v != "unknown" {
			writeError(w, r, 400, "invalid_request", "arrangement is likely_paid or unknown")
			return
		}
		add("p.arrangement=$%d", v)
	}
	if v := q.Get("access"); v != "" {
		if !slices.Contains(coverage.Accesses, v) {
			writeError(w, r, 400, "invalid_request", "access is cant_buy, earned, paid, free or unknown")
			return
		}
		add("p.access=$%d", v)
	}
	order := "p.value DESC NULLS LAST, p.domain_rating DESC NULLS LAST"
	switch q.Get("order") {
	case "", "value":
	case "domain_rating":
		order = "p.domain_rating DESC NULLS LAST, p.value DESC NULLS LAST"
	case "recent":
		order = "p.first_seen_at DESC"
	default:
		writeError(w, r, 400, "invalid_request", "order is value, domain_rating or recent")
		return
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		add("(p.host ILIKE '%%'||$%[1]d||'%%' OR p.title ILIKE '%%'||$%[1]d||'%%')", v)
	}
	brandHere := `EXISTS (SELECT 1 FROM coverage_publishers pub WHERE pub.workspace_id=p.workspace_id AND pub.brand_id=p.brand_id
 AND (p.host=regexp_replace(lower(pub.domain),'^www\.','') OR p.host LIKE '%.'||regexp_replace(lower(pub.domain),'^www\.','')))`
	switch q.Get("gap") {
	case "":
	case "true":
		where += " AND NOT " + brandHere
	case "false":
		where += " AND " + brandHere
	default:
		writeError(w, r, 400, "invalid_request", "gap is true or false")
		return
	}
	if q.Get("include_gone") != "true" {
		where += " AND p.gone_at IS NULL"
	}
	limit := 50
	if q.Has("limit") {
		var n int
		if _, err := fmt.Sscan(q.Get("limit"), &n); err != nil || n < 1 || n > 200 {
			writeError(w, r, 400, "invalid_request", "limit must be a whole number from 1 to 200")
			return
		}
		limit = n
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	// Placements found before values existed are valued on first read.
	if _, err := valueCompetitorPlacements(r.Context(), s.pool, a.WorkspaceID, a.Brand, nil); err != nil {
		s.internal(w, r, err)
		return
	}
	var total int
	if err := s.pool.QueryRow(r.Context(), "SELECT count(*) FROM coverage_competitor_placements p WHERE "+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(r.Context(), fmt.Sprintf(`SELECT to_jsonb(p)-'workspace_id' || jsonb_build_object('competitor_host',c.host,'competitor_name',c.name,'brand_present',`+strings.ReplaceAll(brandHere, "%", "%%")+`)
 FROM coverage_competitor_placements p JOIN seo_competitors c ON c.id=p.competitor_id AND c.workspace_id=p.workspace_id
 WHERE %s ORDER BY `+order+`, p.last_seen_at DESC, p.id LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	data, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if offset+len(data) < total {
		n := offset + len(data)
		next = &n
	}
	writeJSON(w, 200, map[string]any{"data": data, "total": total, "next_cursor": nextCursor(pageOffset, next)})
}
