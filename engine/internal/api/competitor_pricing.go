package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/pricing"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Prices: one pricing page for the brand and each tracked competitor, read
// once a week through the workspace's own page reader (Geonode). The plans
// are read off the page as it is (engine/internal/pricing, no AI), and a
// read that finds different plans is kept with what changed, so the brand
// sees when a competitor raised, cut, added or dropped a plan. Only that one
// page, on the competitor's own site, is read: a light look, never a crawl.

const (
	jobPricingRead = "pricing.read"
	// pricingReadEvery is how often a page is read; pricingRefreshEvery
	// how soon Refresh reads a page again.
	pricingReadEvery    = 7 * 24 * time.Hour
	pricingRefreshEvery = 24 * time.Hour
	// A page that fails is tried again the next day.
	pricingRetryAfter = 24 * time.Hour
	// pricingNoPrices is a page that answered with no bot check on which
	// no price was found: the address may not be a pricing page.
	pricingNoPrices = "no_prices_found"
)

func (s *Server) routesCompetitorPricing(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/brands/{brand}/competition/pricing", s.onBrand(read, s.listPricing))
	mux.HandleFunc("PUT /v1/brands/{brand}/competition/pricing", s.onBrand(manage, s.setPricingPage))
	mux.HandleFunc("POST /v1/brands/{brand}/competition/pricing/refresh", s.onBrand(manage, s.refreshPricing))
	mux.HandleFunc("GET /v1/brands/{brand}/competition/pricing/{page}/history", s.onBrand(read, s.pricingHistory))
}

func (s *Server) registerPricingJobs(q *jobs.Queue) {
	q.Handle(jobPricingRead, s.pricingReadJob)
}

type pricingPageView struct {
	ID         string           `json:"id"`
	URL        string           `json:"url"`
	Plans      []pricing.Plan   `json:"plans"`
	Changes    []pricing.Change `json:"changes"`
	ChangedAt  *time.Time       `json:"changed_at"`
	LastReadAt *time.Time       `json:"last_read_at"`
	NextReadAt time.Time        `json:"next_read_at"`
	LastError  *string          `json:"last_error"`
}

type pricingEntry struct {
	// CompetitorID is null for the brand itself.
	CompetitorID *string          `json:"competitor_id"`
	Name         string           `json:"name"`
	Host         string           `json:"host"`
	Page         *pricingPageView `json:"page"`
}

type pricingView struct {
	Entries []pricingEntry `json:"entries"`
	// NeedsReader is set while the workspace has no page reader connected:
	// pages are kept but not read until one is.
	NeedsReader bool `json:"needs_reader"`
}

// pricingHost is the host a page address must be on: the brand's website
// or the competitor's domain, without www.
func pricingHost(raw string) string {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

func (s *Server) pricing(ctx context.Context, ws, brand string) (pricingView, error) {
	out := pricingView{Entries: []pricingEntry{}}
	var name, website string
	if err := s.pool.QueryRow(ctx, `SELECT name, website FROM brands WHERE workspace_id = $1 AND id = $2`, ws, brand).Scan(&name, &website); err != nil {
		return out, err
	}
	out.Entries = append(out.Entries, pricingEntry{Name: name, Host: pricingHost(website)})
	rows, err := s.pool.Query(ctx, `SELECT id::text, coalesce(nullif(name, ''), host), host FROM seo_competitors
		WHERE workspace_id = $1 AND brand_id = $2 AND status = 'tracked' ORDER BY lower(coalesce(nullif(name, ''), host))`, ws, brand)
	if err != nil {
		return out, err
	}
	var e pricingEntry
	var id string
	if _, err := pgx.ForEachRow(rows, []any{&id, &e.Name, &e.Host}, func() error {
		x := id
		out.Entries = append(out.Entries, pricingEntry{CompetitorID: &x, Name: e.Name, Host: e.Host})
		return nil
	}); err != nil {
		return out, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT p.id::text, coalesce(p.competitor_id::text, ''), p.url, p.last_read_at, p.next_read_at, p.last_error,
			coalesce(r.plans, '[]'), coalesce(c.changes, '[]'), c.read_at
		FROM pricing_pages p
		LEFT JOIN LATERAL (SELECT plans FROM pricing_reads WHERE page_id = p.id ORDER BY read_at DESC LIMIT 1) r ON true
		LEFT JOIN LATERAL (SELECT changes, read_at FROM pricing_reads WHERE page_id = p.id AND changes <> '[]'
			ORDER BY read_at DESC LIMIT 1) c ON true
		WHERE p.workspace_id = $1 AND p.brand_id = $2`, ws, brand)
	if err != nil {
		return out, err
	}
	pages := map[string]*pricingPageView{}
	var who string
	var v pricingPageView
	var plans, changes []byte
	if _, err := pgx.ForEachRow(rows, []any{&v.ID, &who, &v.URL, &v.LastReadAt, &v.NextReadAt, &v.LastError, &plans, &changes, &v.ChangedAt}, func() error {
		p := v
		if err := json.Unmarshal(plans, &p.Plans); err != nil {
			return err
		}
		if err := json.Unmarshal(changes, &p.Changes); err != nil {
			return err
		}
		pages[who] = &p
		return nil
	}); err != nil {
		return out, err
	}
	for i := range out.Entries {
		k := ""
		if out.Entries[i].CompetitorID != nil {
			k = *out.Entries[i].CompetitorID
		}
		out.Entries[i].Page = pages[k]
	}
	readers, err := s.pageReaders(ctx, s.pool, ws)
	out.NeedsReader = len(readers) == 0
	return out, err
}

func (s *Server) listPricing(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	out, err := s.pricing(r.Context(), a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// enqueuePricingRead reads the workspace's due pricing pages through its
// first page reader. It reports whether one is connected.
func (s *Server) enqueuePricingRead(ctx context.Context, tx pgx.Tx, ws, key string) (bool, error) {
	readers, err := s.pageReaders(ctx, tx, ws)
	if err != nil || len(readers) == 0 {
		return false, err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPricingRead, WorkspaceID: &ws, Payload: map[string]string{"integration_id": readers[0]},
		IdempotencyKey: "pricing.read:" + readers[0] + ":" + key, MaxAttempts: 3})
	return err == nil, err
}

// setPricingPage sets (or, with an empty url, clears) the pricing page of
// the brand or one of its tracked competitors, and reads it now.
func (s *Server) setPricingPage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		CompetitorID *string `json:"competitor_id"`
		URL          string  `json:"url"`
	}
	if !decode(w, r, &req) {
		return
	}
	competitor := ""
	if req.CompetitorID != nil {
		competitor = *req.CompetitorID
		if !validUUID(competitor) {
			writeError(w, r, http.StatusNotFound, "not_found", "competitor not found")
			return
		}
	}
	raw := strings.TrimSpace(req.URL)
	var page *url.URL
	if raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || len(raw) > 2000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "url is the full address of a pricing page, starting with https://")
			return
		}
		u.Fragment = ""
		page = u
	}
	var status, host string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if competitor == "" {
			var website string
			err = tx.QueryRow(ctx, `SELECT website FROM brands WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, a.Brand).Scan(&website)
			host, status = pricingHost(website), "tracked"
		} else {
			err = tx.QueryRow(ctx, `SELECT host, status FROM seo_competitors WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`,
				a.WorkspaceID, a.Brand, competitor).Scan(&host, &status)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			status = ""
			return errHandled
		}
		if err != nil {
			return err
		}
		if status != "tracked" {
			return errHandled
		}
		if page != nil {
			h := strings.TrimPrefix(strings.ToLower(page.Hostname()), "www.")
			if host == "" || (h != host && !strings.HasSuffix(h, "."+host)) {
				status = "off_site"
				return errHandled
			}
		}
		if page == nil {
			tag, err := tx.Exec(ctx, `DELETE FROM pricing_pages WHERE workspace_id = $1 AND brand_id = $2 AND coalesce(competitor_id::text, '') = $3`,
				a.WorkspaceID, a.Brand, competitor)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "pricing_page.removed", "brand", a.Brand, map[string]any{
				"brand_id": a.Brand, "competitor": competitor != "",
			})
		}
		var competitorArg *string
		if competitor != "" {
			competitorArg = &competitor
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO pricing_pages (workspace_id, brand_id, competitor_id, url) VALUES ($1, $2, $3, $4)
			ON CONFLICT (brand_id, coalesce(competitor_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE
				SET url = EXCLUDED.url, next_read_at = now(), last_error = NULL, failures = 0, updated_at = now()
			RETURNING id::text`, a.WorkspaceID, a.Brand, competitorArg, page.String()).Scan(&id); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "pricing_page.set", "brand", a.Brand, map[string]any{
			"brand_id": a.Brand, "page_id": id, "competitor": competitor != "",
		}); err != nil {
			return err
		}
		_, err = s.enqueuePricingRead(ctx, tx, a.WorkspaceID, "set:"+id+":"+strconv.FormatInt(time.Now().UnixNano(), 10))
		return err
	})
	switch {
	case errors.Is(err, errHandled) && status == "":
		writeError(w, r, http.StatusNotFound, "not_found", "competitor not found")
		return
	case errors.Is(err, errHandled) && status == "off_site":
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("the pricing page has to be on %s: only that one page of the site is read", orNone(host)))
		return
	case errors.Is(err, errHandled):
		writeError(w, r, http.StatusConflict, "conflict", "track this competitor first: only tracked competitors' prices are read")
		return
	case s.allocError(w, r, err):
		return
	}
	s.listPricing(w, r)
}

func orNone(host string) string {
	if host == "" {
		return "the brand's website (add it to the brand first)"
	}
	return host
}

type pricingRefreshView struct {
	Queued bool `json:"queued"`
	// Pages is how many pricing pages are read now; Skipped how many
	// were read in the last day and wait for their next turn.
	Pages      int  `json:"pages"`
	Skipped    int  `json:"skipped"`
	PageReader bool `json:"page_reader"`
}

// refreshPricing reads the brand's pricing pages now, except those read
// (or tried) in the last day.
func (s *Server) refreshPricing(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var out pricingRefreshView
	total := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pricing_pages WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&total); err != nil {
			return err
		}
		if total == 0 {
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE pricing_pages SET next_read_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2
			  AND coalesce(greatest(last_read_at, last_failed_at), 'epoch') < now() - make_interval(secs => $3)`,
			a.WorkspaceID, a.Brand, pricingRefreshEvery.Seconds())
		if err != nil {
			return err
		}
		out.Pages = int(tag.RowsAffected())
		out.Skipped = total - out.Pages
		if out.Pages > 0 {
			if out.PageReader, err = s.enqueuePricingRead(ctx, tx, a.WorkspaceID, "refresh:"+a.Brand+":"+strconv.FormatInt(time.Now().Unix()/60, 10)); err != nil {
				return err
			}
		} else {
			readers, err := s.pageReaders(ctx, tx, a.WorkspaceID)
			if err != nil {
				return err
			}
			out.PageReader = len(readers) > 0
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "pricing.refresh_requested", "brand", a.Brand, map[string]any{
			"brand_id": a.Brand, "pages": out.Pages, "skipped": out.Skipped, "page_reader": out.PageReader,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	if total == 0 {
		writeError(w, r, http.StatusConflict, "conflict", "add a pricing page first: only the pages you name are read")
		return
	}
	out.Queued = out.Pages > 0 && out.PageReader
	writeJSON(w, http.StatusAccepted, out)
}

type pricingReadView struct {
	ReadAt  time.Time        `json:"read_at"`
	Plans   []pricing.Plan   `json:"plans"`
	Changes []pricing.Change `json:"changes"`
}

// pricingHistory is a page's kept reads, newest first: each time its plans
// changed, over the last year.
func (s *Server) pricingHistory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("page")
	var exists bool
	if validUUID(id) {
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM pricing_pages WHERE workspace_id = $1 AND brand_id = $2 AND id = $3)`,
			a.WorkspaceID, a.Brand, id).Scan(&exists); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if !exists {
		writeError(w, r, http.StatusNotFound, "not_found", "pricing page not found")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT read_at, plans, changes FROM pricing_reads
		WHERE workspace_id = $1 AND brand_id = $2 AND page_id = $3 ORDER BY read_at DESC LIMIT 53`, a.WorkspaceID, a.Brand, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	reads := []pricingReadView{}
	var v pricingReadView
	var plans, changes []byte
	if _, err := pgx.ForEachRow(rows, []any{&v.ReadAt, &plans, &changes}, func() error {
		x := pricingReadView{ReadAt: v.ReadAt}
		if err := json.Unmarshal(plans, &x.Plans); err != nil {
			return err
		}
		if err := json.Unmarshal(changes, &x.Changes); err != nil {
			return err
		}
		reads = append(reads, x)
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page_id": id, "reads": reads})
}

// QueueDuePricingReads queues a read through the first page reader of each
// workspace with a pricing page due, once an hour. A read only reads the
// pages that are due.
func (s *Server) QueueDuePricingReads(ctx context.Context) (int, error) {
	_, readers := s.socialMetricsConnectors()
	if len(readers) == 0 {
		return 0, nil
	}
	hour := "h" + strconv.FormatInt(time.Now().Unix()/3600, 10)
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT ON (i.workspace_id) i.workspace_id::text, i.id::text FROM integrations i
			WHERE i.connector = ANY($1) AND i.status = 'connected'
			  AND EXISTS (SELECT 1 FROM pricing_pages p WHERE p.workspace_id = i.workspace_id AND p.next_read_at <= now()
				AND (p.competitor_id IS NULL OR EXISTS (SELECT 1 FROM seo_competitors c WHERE c.id = p.competitor_id AND c.status = 'tracked')))
			ORDER BY i.workspace_id, i.created_at LIMIT 500`, readers)
		if err != nil {
			return err
		}
		type due struct{ ws, id string }
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.id}, func() error { list = append(list, d); return nil }); err != nil {
			return err
		}
		for _, d := range list {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPricingRead, WorkspaceID: &d.ws, Payload: map[string]string{"integration_id": d.id},
				IdempotencyKey: "pricing.read:" + d.id + ":" + hour, MaxAttempts: 3}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func (s *Server) pricingReadJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "integration_id")
	if err != nil {
		return err
	}
	return s.ReadPricingPages(ctx, ws, id)
}

type duePricingPage struct {
	id, brand, url string
	competitor     bool
}

// ReadPricingPages reads the workspace's due pricing pages through one page
// reader. One page's failure doesn't stop the others; an outage or a
// used-up quota of the reader stops the round and is retried with the job.
func (s *Server) ReadPricingPages(ctx context.Context, ws, integrationID string) error {
	if s.integrations == nil {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, brand_id::text, url, competitor_id IS NOT NULL FROM pricing_pages
		WHERE workspace_id = $1 AND next_read_at <= now() + interval '1 minute'
		  AND (competitor_id IS NULL OR EXISTS (SELECT 1 FROM seo_competitors c WHERE c.id = competitor_id AND c.status = 'tracked'))
		ORDER BY next_read_at LIMIT 100`, ws)
	if err != nil {
		return err
	}
	var due []duePricingPage
	var d duePricingPage
	if _, err := pgx.ForEachRow(rows, []any{&d.id, &d.brand, &d.url, &d.competitor}, func() error { due = append(due, d); return nil }); err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		track.Fail(ctx, s.log, track.Event{Name: "pricing_page.read_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"integration_id": integrationID}}, code, err)
		metrics.PricingPageRead(readConnectorError)
		if k := connectors.KindOf(err); k == connectors.ErrUnavailable || k == connectors.ErrQuota {
			return err
		}
		return nil
	}
	f, ok := o.Connector.(connectors.PageFetcher)
	if !ok {
		return nil
	}
	var outage, broken error
	for _, p := range due {
		err := s.readPricingPage(ctx, ws, o, f, p)
		if err == nil {
			continue
		}
		var bug *reviewBug
		if errors.As(err, &bug) {
			broken = err
			continue
		}
		if k := connectors.KindOf(err); k == connectors.ErrUnavailable || k == connectors.ErrQuota {
			outage = err
			break
		}
	}
	if outage != nil {
		return outage
	}
	return broken
}

// readPricingPage reads one pricing page: as served, then once in a
// browser when that showed no prices (most pricing pages are built in the
// browser). Plans that differ from the last kept read are kept, with what
// changed.
func (s *Server) readPricingPage(ctx context.Context, ws string, o *connectors.Opened, f connectors.PageFetcher, p duePricingPage) error {
	start := time.Now()
	ev := track.Event{Name: "pricing_page.read", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"page_id": p.id, "brand_id": p.brand, "competitor": p.competitor}}
	tokens := 0.0
	render := false
	fail := func(code string, err error) error {
		ev.Name = "pricing_page.read_failed"
		ev.Props["tokens"], ev.Props["render_js"], ev.Props["duration_ms"] = tokens, render, time.Since(start).Milliseconds()
		ev.Outcome = track.Failure
		if code == readSaveFailed {
			ev.Outcome = track.Error
		}
		track.Fail(ctx, s.log, ev, code, err)
		metrics.PricingPageRead(readOutcome(code))
		wait := pricingRetryAfter
		if code == pricingNoPrices || code == readNotFound {
			wait = pricingReadEvery
		}
		if _, uerr := s.pool.Exec(ctx, `UPDATE pricing_pages SET last_error = $3, last_failed_at = now(), failures = failures + 1,
			next_read_at = now() + make_interval(secs => $4), updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			ws, p.id, code, wait.Seconds()); uerr != nil {
			return &reviewBug{uerr}
		}
		if code == readSaveFailed {
			return &reviewBug{err}
		}
		if strings.HasPrefix(code, "connector_") {
			return err
		}
		return nil
	}
	res, err := f.FetchPage(ctx, o.Client, o.Creds, p.url, false)
	tokens += res.Cost.Units
	var plans []pricing.Plan
	if err == nil {
		plans = pricing.Extract(res.HTML)
	}
	if len(plans) == 0 && (err == nil || isPageError(err)) {
		render = true
		res, err = f.FetchPage(ctx, o.Client, o.Creds, p.url, true)
		tokens += res.Cost.Units
		if err == nil {
			plans = pricing.Extract(res.HTML)
		}
	}
	switch {
	case err != nil:
		err = pageFailed(err)
		return fail(readCode(err), err)
	case len(plans) == 0 && (res.Status == 404 || res.Status == 410):
		return fail(readNotFound, fmt.Errorf("the site says the pricing page doesn't exist (HTTP %d): check its address", res.Status))
	case len(plans) == 0 && res.Blocked():
		return fail(readBlocked, errors.New("the site showed a bot check instead of the pricing page; Outflare tries again tomorrow"))
	case len(plans) == 0:
		return fail(pricingNoPrices, errors.New("no prices were found on the page: it may not be the pricing page, or it shows prices only after a choice"))
	}
	changed, err := s.savePricingRead(ctx, ws, p, plans)
	if err != nil {
		return fail(readSaveFailed, err)
	}
	metrics.PricingPageRead("success")
	ev.Props["plans"], ev.Props["changes"], ev.Props["tokens"], ev.Props["render_js"] = len(plans), changed, tokens, render
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	track.Emit(ctx, ev)
	return nil
}

func isPageError(err error) bool {
	var pe *connectors.PageError
	return errors.As(err, &pe)
}

// savePricingRead keeps a read whose plans differ from the last kept one
// and moves the page on a week. It returns how many changes it kept.
func (s *Server) savePricingRead(ctx context.Context, ws string, p duePricingPage, plans []pricing.Plan) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var last []byte
		err := tx.QueryRow(ctx, `SELECT plans FROM pricing_reads WHERE workspace_id = $1 AND page_id = $2 ORDER BY read_at DESC LIMIT 1`, ws, p.id).Scan(&last)
		first := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !first {
			return err
		}
		var before []pricing.Plan
		if !first {
			if err := json.Unmarshal(last, &before); err != nil {
				return err
			}
		}
		if first || !pricing.Same(before, plans) {
			changes := pricing.Diff(before, plans)
			if first {
				changes = []pricing.Change{}
			}
			n = len(changes)
			if _, err := tx.Exec(ctx, `INSERT INTO pricing_reads (workspace_id, brand_id, page_id, plans, changes) VALUES ($1, $2, $3, $4, $5)`,
				ws, p.brand, p.id, plans, changes); err != nil {
				return err
			}
			if !first {
				kinds := map[string]int{}
				for _, c := range changes {
					kinds[c.Kind]++
				}
				if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "pricing_page.changed", TargetType: "brand",
					TargetID: p.brand, Data: map[string]any{"page_id": p.id, "brand_id": p.brand, "competitor": p.competitor, "changes": len(changes),
						"added": kinds["added"], "removed": kinds["removed"], "price_up": kinds["price_up"], "price_down": kinds["price_down"]}}); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE pricing_pages SET last_read_at = now(), last_error = NULL, failures = 0,
			next_read_at = now() + make_interval(secs => $3), updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, p.id, pricingReadEvery.Seconds())
		return err
	})
	return n, err
}
