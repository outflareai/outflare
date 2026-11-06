package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Importing the business's own website into its knowledge: a run reads up
// to max_pages pages (the sitemap's, else what the home page links to),
// politely as OutflareBot, and keeps each page with enough words as an
// article in a "Website" folder. Running it again updates the articles it
// made, found by their address, rather than adding them twice.

const jobWebsiteImport = "knowledge.website_import"

// A page with fewer words than this has nothing to learn from.
const (
	importMinRunes = 200
	importMaxText  = 60000
)

type websiteImportView struct {
	ID           string     `json:"id"`
	BrandID      *string    `json:"brand_id"`
	URL          string     `json:"url"`
	MaxPages     int        `json:"max_pages"`
	Visibility   string     `json:"visibility"`
	Publish      bool       `json:"publish"`
	CollectionID *string    `json:"collection_id"`
	Status       string     `json:"status"`
	PagesFound   int        `json:"pages_found"`
	PagesAdded   int        `json:"pages_added"`
	PagesUpdated int        `json:"pages_updated"`
	PagesSkipped int        `json:"pages_skipped"`
	PagesRemoved int        `json:"pages_removed"`
	Scheduled    bool       `json:"scheduled"`
	Error        string     `json:"error"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

const websiteImportSelect = `SELECT id::text, brand_id::text, url, max_pages, visibility, publish, collection_id::text, status, pages_found,
	pages_added, pages_updated, pages_skipped, pages_removed, scheduled, error, created_at, finished_at FROM knowledge_website_imports`

func (s *Server) routesWebsiteImport(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/website-imports", s.inBrand(read, s.listWebsiteImports))
	mux.HandleFunc("POST /v1/workspaces/{ws}/knowledge/website-imports", s.inBrand(manage, s.startWebsiteImport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/website-imports/{id}", s.inBrand(read, s.getWebsiteImport))
}

func (s *Server) registerWebsiteImportJobs(q *jobs.Queue) {
	q.Handle(jobWebsiteImport, s.websiteImportJob)
}

func (s *Server) listWebsiteImports(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), websiteImportSelect+` WHERE workspace_id = $1`+brand.sql("brand_id", &args)+
		` ORDER BY created_at DESC LIMIT 20`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[websiteImportView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getWebsiteImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	rows, err := s.pool.Query(r.Context(), websiteImportSelect+` WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[websiteImportView])
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !a.sees(v.BrandID) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// startWebsiteImport starts reading the site: url, else the brand's
// website, else the workspace's.
func (s *Server) startWebsiteImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		URL        string  `json:"url"`
		Brand      *string `json:"brand"`
		MaxPages   *int    `json:"max_pages"`
		Visibility string  `json:"visibility"`
		Publish    *bool   `json:"publish"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	var brand *string
	if req.Brand != nil && *req.Brand != "" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, *req.Brand)
		if errors.Is(err, errNotFound) || err == nil && !a.writes(&id) {
			writeError(w, r, http.StatusNotFound, "not_found", "no such brand")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	} else if len(a.Brands) == 1 {
		brand = &a.Brands[0]
	} else if a.Brands != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "say which of your brands the website is for (brand)")
		return
	}
	maxPages := 100
	if req.MaxPages != nil {
		maxPages = *req.MaxPages
	}
	if maxPages < 1 || maxPages > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "max_pages is 1 to 500")
		return
	}
	if req.Visibility == "" {
		req.Visibility = "customers_ai"
	}
	if req.Visibility != "public" && req.Visibility != "customers_ai" && req.Visibility != "team" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "visibility is public, customers_ai or team")
		return
	}
	site := strings.TrimSpace(req.URL)
	if site == "" {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(
				(SELECT COALESCE(NULLIF(website, ''), CASE WHEN cardinality(domains) > 0 THEN 'https://' || domains[1] END) FROM brands WHERE id = COALESCE($2::uuid, outflare_first_brand($1)) AND workspace_id = $1), '')`, a.WorkspaceID, brand).Scan(&site); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if site != "" && !strings.Contains(site, "://") {
		site = "https://" + site
	}
	u, err := url.Parse(site)
	if site == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(site) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is the website to import, such as https://northpeak.example; or set the brand's website first")
		return
	}
	site = u.Scheme + "://" + u.Host + "/"
	if err := crawl.CheckURL(site, s.crawler.AllowPrivate); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "that address can't be read: it must be a public website")
		return
	}
	publish := req.Publish == nil || *req.Publish
	var v websiteImportView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var running bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_website_imports WHERE workspace_id = $1 AND status IN ('queued', 'running'))`,
			a.WorkspaceID).Scan(&running); err != nil {
			return err
		}
		if running {
			return errAlreadyRunning
		}
		// The articles go in a "Website" folder, made once per brand.
		var folder string
		err := tx.QueryRow(ctx, `SELECT id::text FROM knowledge_collections WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid
			AND name = 'Website' AND parent_id IS NULL ORDER BY created_at LIMIT 1`, a.WorkspaceID, brand).Scan(&folder)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO knowledge_collections (workspace_id, brand_id, name, description)
				VALUES ($1, $2, 'Website', 'Pages of your website, imported and kept up to date by Knowledge → Import website.') RETURNING id::text`,
				a.WorkspaceID, brand).Scan(&folder)
		}
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO knowledge_website_imports (workspace_id, brand_id, url, max_pages, visibility, publish, collection_id, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
			a.WorkspaceID, brand, site, maxPages, req.Visibility, publish, folder, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobWebsiteImport, WorkspaceID: &a.WorkspaceID, MaxAttempts: 2,
			Payload: map[string]any{"import_id": id}, IdempotencyKey: jobWebsiteImport + ":" + id}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, websiteImportSelect+` WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if v, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[websiteImportView]); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.website_import_started", "knowledge_website_import", id,
			map[string]any{"brand": brand != nil, "max_pages": maxPages, "visibility": req.Visibility, "publish": publish})
	})
	if errors.Is(err, errAlreadyRunning) {
		writeError(w, r, http.StatusConflict, "website_import_running", "a website import is already running in this workspace; wait for it to finish")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

var errAlreadyRunning = errors.New("already running")

// websiteImportJob reads the site and keeps its pages as articles.
func (s *Server) websiteImportJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ID string `json:"import_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return err
	}
	if job.WorkspaceID == nil {
		return errors.New("no workspace")
	}
	ws := *job.WorkspaceID
	start := time.Now()
	var brand, folder, createdBy *string
	var site, visibility string
	var maxPages int
	var publish, test, scheduled bool
	err := s.pool.QueryRow(ctx, `UPDATE knowledge_website_imports i SET status = 'running' FROM workspaces w
		WHERE i.id = $1 AND i.workspace_id = $2 AND w.id = i.workspace_id AND i.status IN ('queued', 'running')
		RETURNING i.brand_id::text, i.collection_id::text, i.created_by::text, i.url, i.visibility, i.max_pages, i.publish, w.test, i.scheduled`, p.ID, ws).
		Scan(&brand, &folder, &createdBy, &site, &visibility, &maxPages, &publish, &test, &scheduled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "knowledge.website_import_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{}}
	fail := func(code, msg string, cause error) error {
		metrics.KnowledgeWebsiteImport("failed", time.Since(start))
		track.Fail(ctx, s.log, ev, code, cause)
		_, err := s.pool.Exec(ctx, `UPDATE knowledge_website_imports SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`, p.ID, msg)
		return err
	}
	u, _ := url.Parse(site)
	if opted, err := crawl.OptedOut(ctx, s.pool, u.Hostname()); err != nil {
		return err
	} else if opted {
		return fail("blocked", "This website has asked OutflareBot not to read it.", crawl.ErrBlocked)
	}
	f, wait := s.crawler.ReadFetcher(test)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	list, err := crawl.ListSite(ctx, f, wait, site, maxPages)
	urls := list.Pages
	if err == nil && len(urls) == 0 {
		err = errors.New("no pages")
	}
	if err != nil {
		return fail("unavailable", "Outflare couldn't find pages to read on this website: no sitemap, and the home page couldn't be read.", err)
	}
	pages, err := crawl.ReadSitePages(ctx, f, wait, urls, importMaxText)
	if errors.Is(err, crawl.ErrBlocked) {
		return fail("blocked", "This website's robots.txt doesn't let OutflareBot read it.", err)
	}
	if err != nil && len(pages) == 0 {
		return fail("unavailable", "Outflare couldn't read this website.", err)
	}
	status := "draft"
	if publish {
		status = "published"
	}
	var added, updated, skipped int
	skipped = len(urls) - len(pages)
	for _, pg := range pages {
		if utf8.RuneCountInString(pg.Text) < importMinRunes || pg.Title == "" {
			skipped++
			continue
		}
		result, err := s.importWebsitePage(ctx, ws, brand, folder, createdBy, visibility, status, pg)
		if err != nil {
			return err
		}
		switch result {
		case "added":
			added++
		case "updated":
			updated++
		default:
			skipped++
		}
		metrics.KnowledgeWebsitePage(result)
	}
	removed := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Pages no longer on the site: only when the sitemaps listed every
		// page, its articles are unpublished (a person can still publish
		// them), and published again if the page comes back.
		if list.Complete {
			tag, err := tx.Exec(ctx, `UPDATE knowledge_items SET status = 'draft', source_missing_at = now(), updated_at = now()
				WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid AND source_url IS NOT NULL AND deleted_at IS NULL
					AND source_missing_at IS NULL AND status = 'published' AND split_part(source_url, '/', 3) = $3 AND NOT (source_url = ANY ($4))`,
				ws, brand, u.Host, urls)
			if err != nil {
				return err
			}
			removed = int(tag.RowsAffected())
			for range removed {
				metrics.KnowledgeWebsitePage("removed")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE knowledge_website_imports SET status = 'done', pages_found = $2, pages_added = $3, pages_updated = $4,
			pages_skipped = $5, pages_removed = $6, finished_at = now() WHERE id = $1`, p.ID, len(urls), added, updated, skipped, removed); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "knowledge.website_imported",
			TargetType: "knowledge_website_import", TargetID: p.ID,
			Data: map[string]any{"pages_found": len(urls), "pages_added": added, "pages_updated": updated, "pages_skipped": skipped,
				"pages_removed": removed, "scheduled": scheduled},
			Props: map[string]any{"pages_found": len(urls), "pages_added": added, "pages_updated": updated, "pages_skipped": skipped,
				"pages_removed": removed, "scheduled": scheduled, "duration_ms": time.Since(start).Milliseconds()}})
	})
	if err != nil {
		return err
	}
	metrics.KnowledgeWebsiteImport("done", time.Since(start))
	return nil
}

// importWebsitePage adds a page as an article, or updates the one made from
// it before when its words changed: added, updated or unchanged. One the
// team moved to the bin stays there.
func (s *Server) importWebsitePage(ctx context.Context, ws string, brand, folder, createdBy *string, visibility, status string, pg crawl.SitePage) (string, error) {
	title := truncate(pg.Title, 300)
	body := pg.Text + "\n\nFrom " + pg.URL
	result := "unchanged"
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id, curTitle, curBody string
		var missing bool
		err := tx.QueryRow(ctx, `SELECT id::text, title, body, source_missing_at IS NOT NULL FROM knowledge_items WHERE workspace_id = $1
			AND brand_id IS NOT DISTINCT FROM $2::uuid AND source_url = $3 AND deleted_at IS NULL FOR UPDATE`, ws, brand, pg.URL).Scan(&id, &curTitle, &curBody, &missing)
		if err == nil && missing {
			// The page is back: so is the article, as it was before.
			if _, err := tx.Exec(ctx, `UPDATE knowledge_items SET source_missing_at = NULL, status = $2, updated_at = now() WHERE id = $1`, id, status); err != nil {
				return err
			}
			result = "updated"
		}
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			var binned bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_items WHERE workspace_id = $1 AND source_url = $2 AND deleted_at IS NOT NULL)`,
				ws, pg.URL).Scan(&binned); err != nil || binned {
				return err
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM knowledge_items WHERE workspace_id = $1`, ws).Scan(&n); err != nil {
				return err
			}
			if n >= maxKnowledgeItems {
				return nil
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO knowledge_items (workspace_id, brand_id, collection_id, kind, title, body, visibility, status, source_url, created_by, updated_by)
				VALUES ($1, $2, $3, 'article', $4, $5, $6, $7, $8, $9, $9) RETURNING id::text`,
				ws, brand, folder, title, body, visibility, status, pg.URL, createdBy).Scan(&id); err != nil {
				return err
			}
			result = "added"
		case err != nil:
			return err
		case curTitle == title && curBody == body:
			if result == "updated" {
				return writeKnowledgeChunks(ctx, tx, id)
			}
			return nil
		default:
			if _, err := tx.Exec(ctx, `UPDATE knowledge_items SET title = $2, body = $3, version = version + 1, updated_at = now() WHERE id = $1`,
				id, title, body); err != nil {
				return err
			}
			result = "updated"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_versions (workspace_id, item_id, version, title, body, created_by)
			SELECT workspace_id, id, version, title, body, created_by FROM knowledge_items WHERE id = $1`, id); err != nil {
			return err
		}
		if err := writeKnowledgeChunks(ctx, tx, id); err != nil {
			return err
		}
		action := "knowledge.created"
		if result == "updated" {
			action = "knowledge.updated"
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: action, TargetType: "knowledge", TargetID: id,
			Data:  map[string]any{"kind": "article", "title": title, "visibility": visibility, "status": status, "collection_id": folder, "brand_id": brand, "source": "website"},
			Props: map[string]any{"kind": "article", "visibility": visibility, "status": status, "source": "website", "body_chars": utf8.RuneCountInString(body)}})
	})
	return result, err
}

// ---- The daily re-check ----

// RunWebsiteRechecks keeps imported websites current: every tick, each
// website imported into knowledge whose last import finished more than a
// day ago is read again (a scheduled import with the same settings), so
// changed pages update, new ones are added and removed ones unpublished.
func (s *Server) RunWebsiteRechecks(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.QueueWebsiteRechecks(ctx); err != nil && ctx.Err() == nil {
			metrics.KnowledgeWebsiteRecheck("error")
			track.Fail(ctx, s.log, track.Event{Name: "knowledge.website_recheck_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueWebsiteRechecks runs one tick of RunWebsiteRechecks and says how
// many re-checks it queued. One workspace gets one at a time, and servers
// ticking together queue each once (the lock).
func (s *Server) QueueWebsiteRechecks(ctx context.Context) (int, error) {
	queued := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('knowledge.website_recheck'))`).Scan(&got); err != nil || !got {
			return err
		}
		// Each workspace's and brand's latest import, when it finished over
		// a day ago and nothing runs in the workspace.
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT ON (i.workspace_id) i.workspace_id::text, i.brand_id::text, i.url, i.max_pages, i.visibility, i.publish,
				i.collection_id::text, i.created_by::text
			FROM (SELECT DISTINCT ON (workspace_id, brand_id) * FROM knowledge_website_imports ORDER BY workspace_id, brand_id, created_at DESC) i
			JOIN workspaces w ON w.id = i.workspace_id
			WHERE i.status IN ('done', 'failed') AND i.finished_at < now() - interval '23 hours'
				AND NOT EXISTS (SELECT 1 FROM knowledge_website_imports o WHERE o.workspace_id = i.workspace_id AND o.status IN ('queued', 'running'))
			ORDER BY i.workspace_id, i.finished_at
			LIMIT 200`)
		if err != nil {
			return err
		}
		type due struct {
			ws, url, visibility string
			brand, folder, by   *string
			max                 int
			publish             bool
		}
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.brand, &d.url, &d.max, &d.visibility, &d.publish, &d.folder, &d.by}, func() error {
			list = append(list, d)
			return nil
		}); err != nil {
			return err
		}
		for _, d := range list {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO knowledge_website_imports (workspace_id, brand_id, url, max_pages, visibility, publish, collection_id,
				created_by, scheduled) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true) RETURNING id::text`,
				d.ws, d.brand, d.url, d.max, d.visibility, d.publish, d.folder, d.by).Scan(&id); err != nil {
				return err
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobWebsiteImport, WorkspaceID: &d.ws, MaxAttempts: 2,
				Payload: map[string]any{"import_id": id}, IdempotencyKey: jobWebsiteImport + ":" + id}); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for range queued {
		metrics.KnowledgeWebsiteRecheck("queued")
	}
	return queued, nil
}
