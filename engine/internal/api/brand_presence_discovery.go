package api

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Presence discovery runs by itself: once a day each brand's finds from
// the modules that already watch the web come into its review list, and
// each of its web searches runs on the workspace's own search results
// connection, within that connection's budget. Nothing here contacts
// anyone or confirms a placement: a person (or an agent) reviews finds.

const (
	jobCoverageDiscover = "coverage.discover"
	jobCoverageSearch   = "coverage.search"
	// coverageDiscoverRounds caps the batches one daily run brings in; the
	// rest come the next day or with Refresh discoveries.
	coverageDiscoverRounds = 5
)

func (s *Server) registerPresenceJobs(q *jobs.Queue) {
	q.Handle(jobCoverageDiscover, s.coverageDiscoverJob)
	q.Handle(jobCoverageSearch, s.coverageSearchJob)
	q.Handle(jobCompetitorScan, s.coverageCompetitorScanJob)
}

func (s *Server) routesPresenceDiscovery(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/coverage/searches/{id}/run", s.onBrand(auth.ScopeCoverageManage, s.coverageRunSearch))
	s.routesPresenceCompetitors(mux)
	s.routesPresenceOpportunities(mux)
}

// queueCoverageDiscovery queues each active brand's daily discovery and
// its due web searches.
func (s *Server) queueCoverageDiscovery(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH due AS (SELECT id, coverage_discovered_at IS NULL AS first FROM brands
 WHERE status='active' AND (coverage_discovered_at IS NULL OR coverage_discovered_at<=now()-interval '1 day')
 ORDER BY coverage_discovered_at NULLS FIRST LIMIT 50 FOR UPDATE SKIP LOCKED)
 UPDATE brands b SET coverage_discovered_at=now() FROM due WHERE b.id=due.id RETURNING b.workspace_id::text,b.id::text,due.first`)
		if err != nil {
			return err
		}
		type brand struct {
			ws, id string
			first  bool
		}
		list := []brand{}
		for rows.Next() {
			var b brand
			if err = rows.Scan(&b.ws, &b.id, &b.first); err != nil {
				rows.Close()
				return err
			}
			list = append(list, b)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		day := time.Now().UTC().Format("2006-01-02")
		for _, b := range list {
			first := "false"
			if b.first {
				first = "true"
			}
			if _, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCoverageDiscover, WorkspaceID: &b.ws, Payload: map[string]string{"brand_id": b.id, "first": first},
				IdempotencyKey: "coverage.discover:" + b.id + ":" + day, MaxAttempts: 3}); err != nil {
				return err
			}
		}
		rows, err = tx.Query(ctx, `UPDATE coverage_searches s SET next_run_at=now()+make_interval(days=>s.every_days)
 WHERE s.id IN (SELECT id FROM coverage_searches WHERE enabled AND next_run_at<=now() ORDER BY next_run_at LIMIT 100 FOR UPDATE SKIP LOCKED)
 RETURNING s.workspace_id::text,s.brand_id::text,s.id::text`)
		if err != nil {
			return err
		}
		type search struct{ ws, brand, id string }
		due := []search{}
		for rows.Next() {
			var d search
			if err = rows.Scan(&d.ws, &d.brand, &d.id); err != nil {
				rows.Close()
				return err
			}
			due = append(due, d)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, d := range due {
			if err = enqueueCoverageSearch(ctx, tx, d.ws, d.brand, d.id, day); err != nil {
				return err
			}
		}
		return nil
	})
}

func enqueueCoverageSearch(ctx context.Context, tx pgx.Tx, ws, brand, id, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCoverageSearch, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brand, "search_id": id},
		IdempotencyKey: "coverage.search:" + id + ":" + key, MaxAttempts: 3})
	return err
}

// coverageDiscoverJob brings a brand's new finds into its review list. On
// a brand's first run it also adds a web search for the brand's name, so
// discovery reaches the open web from the start; the search only runs
// once the workspace connects a search results connection.
func (s *Server) coverageDiscoverJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Brand string `json:"brand_id"`
		First string `json:"first"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.Brand) {
		return fmt.Errorf("%w: invalid coverage discovery job", jobs.ErrPermanent)
	}
	a := &access{WorkspaceID: *j.WorkspaceID, Brand: req.Brand}
	if req.First == "true" {
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return coverageSeedSearch(ctx, tx, a) }); err != nil {
			return err
		}
	}
	for range coverageDiscoverRounds {
		var found map[string]int
		var processed int
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var err error
			found, processed, err = coverageDiscover(ctx, tx, a)
			return err
		})
		if err != nil {
			track.Fail(ctx, s.log, track.Event{Name: "coverage_discovery.failed", WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": a.Brand}}, "internal", err)
			return err
		}
		for source, n := range found {
			metrics.CoverageFound(source, n)
		}
		if processed < coverageDiscoverBatch {
			break
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		added, won, err := coverageRefreshOpportunities(ctx, tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if _, err := coverageRefreshTraction(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, nil, a.WorkspaceID, "coverage_opportunities.refreshed", "brands", a.Brand, nil, map[string]any{"brand_id": a.Brand, "added": added, "won": won})
	})
}

// coverageSeedSearch adds a web search for the brand's name, quoted, when
// the brand has none.
func coverageSeedSearch(ctx context.Context, tx pgx.Tx, a *access) error {
	var name string
	var has bool
	if err := tx.QueryRow(ctx, `SELECT b.name, EXISTS(SELECT 1 FROM coverage_searches s WHERE s.workspace_id=b.workspace_id AND s.brand_id=b.id)
 FROM brands b WHERE b.workspace_id=$1 AND b.id=$2`, a.WorkspaceID, a.Brand).Scan(&name, &has); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	name = strings.TrimSpace(strings.ReplaceAll(name, `"`, ""))
	if has || name == "" {
		return nil
	}
	query := `"` + name + `"`
	if len(query) > 300 {
		return nil
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO coverage_searches(workspace_id,brand_id,query) VALUES($1,$2,$3) ON CONFLICT DO NOTHING RETURNING id::text`, a.WorkspaceID, a.Brand, query).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return coverageHistory(ctx, tx, a, "searches", id, "created", map[string]any{"seeded": true})
}

// coverageRunSearch queues one of the brand's web searches now.
func (s *Server) coverageRunSearch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "Search not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var found string
		if err := tx.QueryRow(r.Context(), "SELECT id::text FROM coverage_searches WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", a.WorkspaceID, a.Brand, id).Scan(&found); err != nil {
			return err
		}
		if err := enqueueCoverageSearch(r.Context(), tx, a.WorkspaceID, a.Brand, id, time.Now().UTC().Format("200601021504")); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "coverage_search.queued", "coverage_searches", id, nil, map[string]any{"brand_id": a.Brand})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 202, map[string]any{"queued": true})
}

// coverageSearchJob runs one web search and adds every result that isn't
// the brand's own page to the review list.
func (s *Server) coverageSearchJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Brand  string `json:"brand_id"`
		Search string `json:"search_id"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.Brand) || !validUUID(req.Search) {
		return fmt.Errorf("%w: invalid coverage search job", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	a := &access{WorkspaceID: ws, Brand: req.Brand}
	var query, country string
	var depth int
	var domains []string
	var website string
	err := s.pool.QueryRow(ctx, `SELECT s.query,s.country,s.depth,b.domains,b.website FROM coverage_searches s JOIN brands b ON b.id=s.brand_id AND b.workspace_id=s.workspace_id
 WHERE s.workspace_id=$1 AND s.brand_id=$2 AND s.id=$3`, ws, req.Brand, req.Search).Scan(&query, &country, &depth, &domains, &website)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	own := map[string]bool{}
	for _, d := range append(domains, website) {
		if u, e := url.Parse(d); e == nil && u.Hostname() != "" {
			d = u.Hostname()
		}
		if d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "www."); d != "" {
			own[d] = true
		}
	}
	ownHost := func(h string) bool {
		h = strings.TrimPrefix(strings.ToLower(h), "www.")
		for d := range own {
			if h == d || strings.HasSuffix(h, "."+d) {
				return true
			}
		}
		return false
	}
	finish := func(outcome string, found int) error {
		metrics.CoverageSearch(outcome)
		errCode := outcome
		if outcome == "done" {
			errCode = ""
		}
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE coverage_searches SET last_run_at=now(),last_error=$4,last_found=$5 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", ws, req.Brand, req.Search, errCode, found)
			return err
		})
	}
	var integration string
	if s.integrations != nil {
		if integration, err = ranks.DefaultConnection(ctx, s.pool, s.integrations.Registry, ws); err != nil {
			return err
		}
	}
	if integration == "" {
		// No key, no feature: the search waits for a search results
		// connection, and says so on the search.
		return finish("connector_needed", 0)
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, integration)
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "coverage_search.failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": req.Brand}}, "connector_"+connectors.KindOf(err), err)
		return finish("connector_unavailable", 0)
	}
	if country == "" {
		country = "US"
	}
	res, err := ranks.Search(ctx, s.pool, s.integrations, o, ranks.SearchQuery{SERPQuery: connectors.SERPQuery{Keyword: query, Country: country, Device: "desktop", Depth: depth}})
	if err != nil {
		code := ranks.SearchErrorCode(err)
		track.Fail(ctx, s.log, track.Event{Name: "coverage_search.failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": req.Brand, "connector": o.Connector.Definition().ID}}, code, err)
		return finish(code, 0)
	}
	added := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, item := range res.Items {
			u, e := url.Parse(item.URL)
			if e != nil || u.Hostname() == "" || ownHost(u.Hostname()) {
				continue
			}
			sum := md5.Sum([]byte(item.URL))
			created, err := coverageAddFind(ctx, tx, a, coverageFind{URL: item.URL, Title: item.Title, Source: "web_search", ID: hex.EncodeToString(sum[:])})
			if err != nil {
				return err
			}
			if created {
				added++
			}
		}
		return recordWithProps(ctx, tx, nil, ws, "coverage_search.ran", "coverage_searches", req.Search, nil,
			map[string]any{"brand_id": req.Brand, "results": len(res.Items), "added": added, "connector": o.Connector.Definition().ID, "spent": res.Spent, "unit": res.Unit})
	})
	if err != nil {
		return err
	}
	metrics.CoverageFound("web_search", added)
	return finish("done", added)
}
