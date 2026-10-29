package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/news"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/retry"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

// News watch: saved searches that run on a schedule through the
// workspace's own web search connection (Geonode), the articles they find,
// and coverage: articles about a brand, each on its outlet's company
// record and recorded as a mention (mention.found). A run costs one search
// on the connection and books it in the spend ledger, within the
// connection's usage limits; reading what was found costs nothing.

const jobNewsRun = "news.run"

func (s *Server) routesNews(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/news-searches", s.inBrand(read, s.listNewsSearches))
	mux.HandleFunc("POST /v1/workspaces/{ws}/news-searches", s.inBrand(manage, s.createNewsSearch))
	mux.HandleFunc("GET /v1/workspaces/{ws}/news-searches/{search}", s.inBrand(read, s.getNewsSearch))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/news-searches/{search}", s.inBrand(manage, s.updateNewsSearch))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/news-searches/{search}", s.inBrand(manage, s.deleteNewsSearch))
	mux.HandleFunc("POST /v1/workspaces/{ws}/news-searches/{search}/run", s.inBrand(manage, s.runNewsSearch))
	mux.HandleFunc("GET /v1/workspaces/{ws}/news-articles", s.inBrand(read, s.listNewsArticles))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journalists", s.inBrand(read, s.listJournalists))
	mux.HandleFunc("GET /v1/workspaces/{ws}/coverage", s.inBrand(read, s.listCoverage))
	mux.HandleFunc("POST /v1/workspaces/{ws}/coverage", s.inBrand(manage, s.addCoverage))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/coverage/{coverage}", s.inBrand(manage, s.updateCoverage))
}

func (s *Server) registerNewsJobs(q *jobs.Queue) {
	q.Handle(jobNewsRun, s.newsRunJob)
}

// ---- Searches ----

type newsSearchView struct {
	ID              string     `json:"id"`
	BrandID         *string    `json:"brand_id"`
	Name            string     `json:"name"`
	Query           string     `json:"query"`
	Purpose         string     `json:"purpose"`
	Locale          string     `json:"locale"`
	TimeRange       string     `json:"time_range"`
	CheckEveryHours int        `json:"check_every_hours"`
	Status          string     `json:"status"`
	LastCheckedAt   *time.Time `json:"last_checked_at"`
	NextCheckAt     time.Time  `json:"next_check_at"`
	LastErrorCode   *string    `json:"last_error_code"`
	Articles        int        `json:"articles"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

const newsSearchCols = `n.id::text, n.brand_id::text, n.name, n.query, n.purpose, n.locale, n.time_range, n.check_every_hours, n.status,
	n.last_checked_at, n.next_check_at, n.last_error_code,
	(SELECT count(*) FROM news_hits h WHERE h.search_id = n.id)::int, n.created_at, n.updated_at`

func scanNewsSearch(row pgx.Row) (newsSearchView, error) {
	var v newsSearchView
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Query, &v.Purpose, &v.Locale, &v.TimeRange, &v.CheckEveryHours, &v.Status,
		&v.LastCheckedAt, &v.NextCheckAt, &v.LastErrorCode, &v.Articles, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Server) newsSearchFor(w http.ResponseWriter, r *http.Request, a *access) (*newsSearchView, bool) {
	id := r.PathValue("search")
	if validUUID(id) {
		v, err := scanNewsSearch(s.pool.QueryRow(r.Context(), `SELECT `+newsSearchCols+` FROM news_searches n
			WHERE n.workspace_id = $1 AND n.id = $2`, a.WorkspaceID, id))
		switch {
		case err == nil && a.sees(nil, v.BrandID):
			return &v, true
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "news search not found")
	return nil, false
}

func (s *Server) listNewsSearches(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	where := `n.workspace_id = $1`
	if p := r.URL.Query().Get("purpose"); p != "" {
		if !slices.Contains(news.Purposes, p) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "purpose is topic or coverage")
			return
		}
		args = append(args, p)
		where += ` AND n.purpose = $` + strconv.Itoa(len(args))
	}
	where += sel.sql("n.brand_id", &args)
	rows, err := s.pool.Query(r.Context(), `SELECT `+newsSearchCols+` FROM news_searches n WHERE `+where+` ORDER BY n.created_at DESC, n.id`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (newsSearchView, error) { return scanNewsSearch(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []newsSearchView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getNewsSearch(w http.ResponseWriter, r *http.Request) {
	v, ok := s.newsSearchFor(w, r, accessFrom(r.Context()))
	if ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// newsSearchIn is what a create or update sets; nil leaves a field as it
// is.
type newsSearchIn struct {
	Name            *string `json:"name"`
	Query           *string `json:"query"`
	Locale          *string `json:"locale"`
	TimeRange       *string `json:"time_range"`
	CheckEveryHours *int    `json:"check_every_hours"`
	Status          *string `json:"status"`
}

// apply checks in and sets it on v, or says what is wrong.
func (in newsSearchIn) apply(v *newsSearchView) string {
	if in.Name != nil {
		v.Name = strings.TrimSpace(*in.Name)
	}
	if in.Query != nil {
		v.Query = strings.TrimSpace(*in.Query)
	}
	if in.Locale != nil {
		v.Locale = strings.TrimSpace(*in.Locale)
	}
	if in.TimeRange != nil {
		v.TimeRange = *in.TimeRange
	}
	if in.CheckEveryHours != nil {
		v.CheckEveryHours = *in.CheckEveryHours
	}
	if in.Status != nil {
		v.Status = *in.Status
	}
	switch {
	case v.Query == "" || len(v.Query) > 500:
		return "query is 1 to 500 characters"
	case len(v.Name) > 200:
		return "name is at most 200 characters"
	case len(v.Locale) > 10 || strings.ContainsAny(v.Locale, " /?&"):
		return "locale is a language code (en) or tag (en-GB), or empty"
	case !slices.Contains(news.TimeRanges, v.TimeRange):
		return "time_range is day, week or month"
	case v.CheckEveryHours < news.MinHours || v.CheckEveryHours > news.MaxHours:
		return "check_every_hours is " + strconv.Itoa(news.MinHours) + " to " + strconv.Itoa(news.MaxHours)
	case !slices.Contains(news.SearchStatuses, v.Status):
		return "status is active or paused"
	}
	if v.Name == "" {
		v.Name = truncate(v.Query, 200)
	}
	return ""
}

var errTooManyNewsSearches = errors.New("too many news searches")

func (s *Server) createNewsSearch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		newsSearchIn
		BrandID string `json:"brand_id"`
		Purpose string `json:"purpose"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	ctx := r.Context()
	v := newsSearchView{Purpose: req.Purpose, TimeRange: "week", CheckEveryHours: 24, Status: "active"}
	if v.Purpose == "" {
		v.Purpose = "topic"
	}
	if !slices.Contains(news.Purposes, v.Purpose) {
		bad("purpose is topic or coverage")
		return
	}
	if req.BrandID != "" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, req.BrandID)
		if errors.Is(err, errNotFound) || (err == nil && !a.sees(nil, &id)) {
			bad("brand_id: brand not found")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		v.BrandID = &id
	}
	if v.Purpose == "coverage" && v.BrandID == nil {
		bad("a coverage search needs brand_id: the brand whose coverage it finds")
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if msg := req.newsSearchIn.apply(&v); msg != "" {
		bad(msg)
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM news_searches WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= news.MaxSearches {
			return errTooManyNewsSearches
		}
		if err := tx.QueryRow(ctx, `INSERT INTO news_searches (workspace_id, brand_id, name, query, purpose, locale, time_range, check_every_hours,
				status, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
			a.WorkspaceID, v.BrandID, v.Name, v.Query, v.Purpose, v.Locale, v.TimeRange, v.CheckEveryHours, v.Status, actorRef(a)).Scan(&v.ID); err != nil {
			return err
		}
		// The first run goes now, when it can.
		if v.Status == "active" {
			if err := enqueueNewsRun(ctx, tx, a.WorkspaceID, v.ID, "created"); err != nil {
				return err
			}
		}
		data := map[string]any{"brand_id": v.BrandID, "purpose": v.Purpose, "time_range": v.TimeRange, "check_every_hours": v.CheckEveryHours}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "news_search.created", "news_search", v.ID, data, data)
	})
	if errors.Is(err, errTooManyNewsSearches) {
		writeError(w, r, http.StatusConflict, "conflict", "a workspace keeps at most "+strconv.Itoa(news.MaxSearches)+" news searches; delete one first")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	created, err := scanNewsSearch(s.pool.QueryRow(ctx, `SELECT `+newsSearchCols+` FROM news_searches n WHERE n.id = $1`, v.ID))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateNewsSearch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req newsSearchIn
	if !decode(w, r, &req) {
		return
	}
	v, ok := s.newsSearchFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	was := *v
	if msg := req.apply(v); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A new schedule counts from the last run.
		if _, err := tx.Exec(ctx, `UPDATE news_searches SET name = $3, query = $4, locale = $5, time_range = $6, check_every_hours = $7,
				status = $8, next_check_at = CASE WHEN check_every_hours = $7 THEN next_check_at
					ELSE coalesce(last_checked_at, now()) + make_interval(hours => $7) END, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID, v.Name, v.Query, v.Locale, v.TimeRange, v.CheckEveryHours, v.Status); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "purpose": v.Purpose, "status": v.Status, "was": was.Status,
			"query_changed": was.Query != v.Query, "check_every_hours": v.CheckEveryHours}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "news_search.updated", "news_search", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	updated, err := scanNewsSearch(s.pool.QueryRow(ctx, `SELECT `+newsSearchCols+` FROM news_searches n WHERE n.id = $1`, v.ID))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteNewsSearch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.newsSearchFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Articles only this search found go with it; coverage stays.
		if _, err := tx.Exec(ctx, `DELETE FROM news_articles a WHERE a.workspace_id = $1
				AND EXISTS (SELECT 1 FROM news_hits h WHERE h.article_id = a.id AND h.search_id = $2)
				AND NOT EXISTS (SELECT 1 FROM news_hits h WHERE h.article_id = a.id AND h.search_id <> $2)
				AND NOT EXISTS (SELECT 1 FROM coverage c WHERE c.article_id = a.id)`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM news_searches WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "purpose": v.Purpose}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "news_search.deleted", "news_search", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runNewsSearch queues a run now, whatever the schedule, when the
// workspace has a web search connection for the search's brand.
func (s *Server) runNewsSearch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.newsSearchFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	if _, err := s.openNewsSearcher(ctx, a.WorkspaceID, v.BrandID); err != nil {
		if errors.Is(err, connectors.ErrNoCapable) {
			writeError(w, r, http.StatusConflict, "connector_needed", "running a news search needs a web search connection, such as Geonode, under Connectors")
			return
		}
		s.internal(w, r, err)
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return enqueueNewsRun(ctx, tx, a.WorkspaceID, v.ID, "now"+strconv.FormatInt(time.Now().Unix()/60, 10))
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// ---- Articles ----

type newsArticleView struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Domain      string    `json:"domain"`
	Title       string    `json:"title"`
	Snippet     string    `json:"snippet"`
	SearchIDs   []string  `json:"search_ids"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	// The byline as the page gives it, when it says it was published,
	// how the read went (null until read), and the journalists' records.
	Author        string     `json:"author"`
	PublishedAt   *time.Time `json:"published_at"`
	ReadStatus    *string    `json:"read_status"`
	JournalistIDs []string   `json:"journalist_ids"`
}

func (s *Server) listNewsArticles(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := []any{a.WorkspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	// An article shows to whoever sees one of the searches that found it.
	hits := `h.article_id = x.id`
	if v := q.Get("search_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "search_id is a news search's id")
			return
		}
		hits += ` AND h.search_id = ` + arg(v) + `::uuid`
	}
	hits += sel.sql("n.brand_id", &args)
	where := `x.workspace_id = $1 AND EXISTS (SELECT 1 FROM news_hits h JOIN news_searches n ON n.id = h.search_id WHERE ` + hits + `)`
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "since is a time like 2026-10-28T00:00:00Z")
			return
		}
		where += ` AND x.first_seen_at >= ` + arg(t)
	}
	if v := q.Get("journalist_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "journalist_id is a journalist's person record id")
			return
		}
		where += ` AND EXISTS (SELECT 1 FROM news_article_authors aa WHERE aa.article_id = x.id AND aa.record_id = ` + arg(v) + `::uuid)`
	}
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM news_articles x WHERE `+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT x.id::text, x.url, x.title, x.snippet,
			ARRAY(SELECT h.search_id::text FROM news_hits h WHERE h.article_id = x.id ORDER BY h.found_at), x.first_seen_at, x.last_seen_at,
			x.author, x.published_at, x.read_status,
			ARRAY(SELECT DISTINCT aa.record_id::text FROM news_article_authors aa WHERE aa.article_id = x.id)
		FROM news_articles x WHERE `+where+` ORDER BY x.first_seen_at DESC, x.id LIMIT `+arg(limit)+` OFFSET `+arg(offset), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (newsArticleView, error) {
		var v newsArticleView
		err := row.Scan(&v.ID, &v.URL, &v.Title, &v.Snippet, &v.SearchIDs, &v.FirstSeenAt, &v.LastSeenAt, &v.Author, &v.PublishedAt, &v.ReadStatus,
			&v.JournalistIDs)
		_, v.Domain = news.ArticleURL(v.URL)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []newsArticleView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": nextOffset(offset, len(list), total)})
}

func nextOffset(offset, n, total int) *int {
	if offset+n >= total {
		return nil
	}
	next := offset + n
	return &next
}

// ---- Coverage ----

type coverageView struct {
	ID              string     `json:"id"`
	BrandID         string     `json:"brand_id"`
	ArticleID       string     `json:"article_id"`
	URL             string     `json:"url"`
	Domain          string     `json:"domain"`
	Title           string     `json:"title"`
	Snippet         string     `json:"snippet"`
	RecordID        string     `json:"record_id"`
	SearchID        *string    `json:"search_id"`
	Status          string     `json:"status"`
	Linked          *bool      `json:"linked"`
	Source          string     `json:"source"`
	FoundAt         time.Time  `json:"found_at"`
	StatusChangedAt time.Time  `json:"status_changed_at"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

const coverageCols = `c.id::text, c.brand_id::text, c.article_id::text, x.url, x.title, x.snippet, c.record_id::text, c.search_id::text,
	c.status, c.linked, c.source, c.found_at, c.status_changed_at, c.updated_at`

func scanCoverage(row pgx.Row) (coverageView, error) {
	var v coverageView
	err := row.Scan(&v.ID, &v.BrandID, &v.ArticleID, &v.URL, &v.Title, &v.Snippet, &v.RecordID, &v.SearchID,
		&v.Status, &v.Linked, &v.Source, &v.FoundAt, &v.StatusChangedAt, &v.UpdatedAt)
	_, v.Domain = news.ArticleURL(v.URL)
	return v, err
}

func (s *Server) loadCoverage(ctx context.Context, ws, id string) (*coverageView, error) {
	v, err := scanCoverage(s.pool.QueryRow(ctx, `SELECT `+coverageCols+` FROM coverage c JOIN news_articles x ON x.id = c.article_id
		WHERE c.workspace_id = $1 AND c.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (s *Server) listCoverage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := []any{a.WorkspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := `c.workspace_id = $1`
	// status takes several, comma-separated; by default all but dismissed.
	if v := q.Get("status"); v != "" {
		states := strings.Split(v, ",")
		for _, st := range states {
			if !slices.Contains(news.CoverageStates, st) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "status is new, confirmed or dismissed, or several separated by commas")
				return
			}
		}
		where += ` AND c.status = ANY(` + arg(states) + `)`
	} else {
		where += ` AND c.status <> 'dismissed'`
	}
	if v := q.Get("record_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id is a company record's id")
			return
		}
		where += ` AND c.record_id = ` + arg(v) + `::uuid`
	}
	where += sel.sql("c.brand_id", &args)
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM coverage c WHERE `+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT `+coverageCols+` FROM coverage c JOIN news_articles x ON x.id = c.article_id
		WHERE `+where+` ORDER BY c.found_at DESC, c.id LIMIT `+arg(limit)+` OFFSET `+arg(offset), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (coverageView, error) { return scanCoverage(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []coverageView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": nextOffset(offset, len(list), total)})
}

// newCoverage is an article to keep as a brand's coverage.
type newCoverage struct {
	URL, Domain, Title, Snippet string
	SearchID                    *string
	Source                      string
}

// keepCoverage adds the articles and their coverage for brand, on each
// outlet's company record, and records mention.found for each new item.
// It returns the new items' ids, and the article id of each one given.
func keepCoverage(ctx context.Context, tx pgx.Tx, p *principal, ws, brand string, items []newCoverage) ([]string, []string, error) {
	companies := make([]customers.Company, len(items))
	for i, c := range items {
		companies[i] = customers.Company{Domain: c.Domain}
	}
	records, err := customers.EnsureCompanies(ctx, tx, ws, &brand, "news_watch", companies)
	if err != nil {
		return nil, nil, err
	}
	var added []string
	articles := make([]string, len(items))
	for i, c := range items {
		if err := tx.QueryRow(ctx, `INSERT INTO news_articles (workspace_id, url, title, snippet) VALUES ($1, $2, $3, $4)
			ON CONFLICT (workspace_id, url) DO UPDATE SET last_seen_at = now(),
				title = CASE WHEN excluded.title = '' THEN news_articles.title ELSE excluded.title END,
				snippet = CASE WHEN excluded.snippet = '' THEN news_articles.snippet ELSE excluded.snippet END
			RETURNING id::text`, ws, c.URL, c.Title, c.Snippet).Scan(&articles[i]); err != nil {
			return nil, nil, err
		}
		record, ok := records[c.Domain]
		if !ok {
			continue
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO coverage (workspace_id, brand_id, article_id, record_id, search_id, source)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (brand_id, article_id) DO NOTHING RETURNING id::text`,
			ws, brand, articles[i], record, c.SearchID, c.Source).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		data := map[string]any{"brand_id": brand, "record_id": record, "article_id": articles[i], "search_id": c.SearchID, "source": c.Source, "kind": "news"}
		if err := recordWithProps(ctx, tx, p, ws, "mention.found", "coverage", id, data, data); err != nil {
			return nil, nil, err
		}
		added = append(added, id)
	}
	return added, articles, nil
}

// addCoverage keeps an article a person or agent found as a brand's
// coverage.
func (s *Server) addCoverage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		BrandID string `json:"brand_id"`
		URL     string `json:"url"`
		Title   string `json:"title"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	ctx := r.Context()
	brand, err := s.brandIDFor(ctx, a.WorkspaceID, req.BrandID)
	if errors.Is(err, errNotFound) || (err == nil && !a.sees(nil, &brand)) {
		bad("brand_id: brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(nil, &brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	u, d := news.ArticleURL(req.URL)
	if u == "" {
		bad("url is an article's address, like https://news.example.org/2026/10/story")
		return
	}
	var website string
	if err := s.pool.QueryRow(ctx, `SELECT website FROM brands WHERE id = $1`, brand).Scan(&website); err != nil {
		s.internal(w, r, err)
		return
	}
	if news.OwnSite(website, d) {
		bad("url is on the brand's own website")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if len(req.Title) > 500 {
		bad("title is at most 500 characters")
		return
	}
	var articles []string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, articles, err = keepCoverage(ctx, tx, &a.principal, a.WorkspaceID, brand, []newCoverage{{URL: u, Domain: d, Title: req.Title, Source: "manual"}})
		if err != nil {
			return err
		}
		return news.StartRead(ctx, tx, a.WorkspaceID, articles[0])
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := scanCoverage(s.pool.QueryRow(ctx, `SELECT `+coverageCols+` FROM coverage c JOIN news_articles x ON x.id = c.article_id
		WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.article_id = $3`, a.WorkspaceID, brand, articles[0]))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateCoverage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Status string `json:"status"`
		Linked *bool  `json:"linked"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Status == "" && req.Linked == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "set status or linked")
		return
	}
	if req.Status != "" && !slices.Contains(news.CoverageStates, req.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is new, confirmed or dismissed")
		return
	}
	ctx := r.Context()
	var c *coverageView
	if id := r.PathValue("coverage"); validUUID(id) {
		var err error
		if c, err = s.loadCoverage(ctx, a.WorkspaceID, id); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if c == nil || !a.sees(nil, &c.BrandID) {
		writeError(w, r, http.StatusNotFound, "not_found", "coverage not found")
		return
	}
	if !a.writes(nil, &c.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	moved := req.Status != "" && req.Status != c.Status
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE coverage SET status = CASE WHEN $3 = '' THEN status ELSE $3 END,
				status_changed_at = CASE WHEN $3 = '' OR $3 = status THEN status_changed_at ELSE now() END,
				linked = COALESCE($4, linked), updated_by = $5, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, c.ID, req.Status, req.Linked, actorRef(a)); err != nil {
			return err
		}
		action := "coverage.updated"
		if moved {
			action = "coverage." + req.Status
			if req.Status == "new" {
				action = "coverage.reopened"
			}
		}
		data := map[string]any{"brand_id": c.BrandID, "record_id": c.RecordID, "status": c.Status, "linked_set": req.Linked != nil}
		if moved {
			data["status"], data["was"] = req.Status, c.Status
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "coverage", c.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	c, err = s.loadCoverage(ctx, a.WorkspaceID, c.ID)
	if err != nil || c == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// ---- Running searches ----

// enqueueNewsRun queues a run of a search; key keeps it to one per moment.
func enqueueNewsRun(ctx context.Context, tx pgx.Tx, ws, searchID, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobNewsRun, WorkspaceID: &ws, Payload: map[string]string{"search_id": searchID},
		IdempotencyKey: "news.run:" + searchID + ":" + key, MaxAttempts: 3})
	return err
}

// QueueDueNewsSearches moves each due search's next run on and queues it,
// in one transaction, and reports how many it queued.
func (s *Server) QueueDueNewsSearches(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE news_searches n SET next_check_at = now() + make_interval(hours => n.check_every_hours)
			WHERE n.id IN (SELECT id FROM news_searches WHERE status = 'active' AND next_check_at <= now()
			               ORDER BY next_check_at LIMIT 100 FOR UPDATE SKIP LOCKED)
			RETURNING n.workspace_id::text, n.id::text, n.next_check_at`)
		if err != nil {
			return err
		}
		type due struct {
			ws, id string
			at     time.Time
		}
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.id, &d.at}, func() error { list = append(list, d); return nil }); err != nil {
			return err
		}
		for _, d := range list {
			if err := enqueueNewsRun(ctx, tx, d.ws, d.id, strconv.FormatInt(d.at.Unix(), 10)); err != nil {
				return err
			}
		}
		n = len(list)
		return nil
	})
	return n, err
}

func (s *Server) newsRunJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "search_id")
	if err != nil {
		return err
	}
	return s.RunNewsSearch(ctx, ws, id)
}

// openNewsSearcher opens the workspace's web search connection for brand
// (nil for shared searches), or connectors.ErrNoCapable.
func (s *Server) openNewsSearcher(ctx context.Context, ws string, brand *string) (*connectors.Opened, error) {
	if s.integrations == nil {
		return nil, connectors.ErrNoCapable
	}
	return s.integrations.OpenCapable(ctx, s.pool, ws, deref(brand), connectors.CapWebSearch)
}

// RunNewsSearch runs one search through the workspace's own web search
// connection, books what it cost, and keeps the articles it found: for a
// coverage search, those that name the brand's query and aren't on its own
// site become coverage. A failure is kept on the search (last_error_code)
// and is an event; the job is retried when the provider was unavailable or
// rate limited, and the search runs again soon (retry.After) either way.
func (s *Server) RunNewsSearch(ctx context.Context, ws, searchID string) error {
	start := time.Now()
	var v newsSearchView
	var website string
	err := s.pool.QueryRow(ctx, `SELECT n.brand_id::text, n.query, n.purpose, n.locale, n.time_range, coalesce(b.website, '')
		FROM news_searches n LEFT JOIN brands b ON b.id = n.brand_id WHERE n.workspace_id = $1 AND n.id = $2`, ws, searchID).
		Scan(&v.BrandID, &v.Query, &v.Purpose, &v.Locale, &v.TimeRange, &website)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted since
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "news_search.ran", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"search_id": searchID, "brand_id": v.BrandID, "purpose": v.Purpose}}
	fail := func(code string, err error) error {
		metrics.NewsSearchRun(v.Purpose, metricOutcome(code), 0, 0)
		ev.Name = "news_search.run_failed"
		ev.Props["duration_ms"] = time.Since(start).Milliseconds()
		track.Fail(ctx, s.log, ev, code, err)
		// A failed run is tried again soon (retry.After), never later than
		// the next regular run, unless only a person can fix it.
		retryable := code != "connector_needed" && code != "connector_"+connectors.ErrInvalid
		if _, uerr := s.pool.Exec(ctx, `UPDATE news_searches SET last_checked_at = now(), last_error_code = $3, failures = failures + 1,
			next_check_at = CASE WHEN $4 THEN LEAST(next_check_at, now() + `+retry.SQL("failures + 1")+`) ELSE next_check_at END
			WHERE workspace_id = $1 AND id = $2`,
			ws, searchID, code, retryable); uerr != nil {
			return uerr
		}
		if code == "connector_"+connectors.ErrUnavailable || code == "connector_"+connectors.ErrQuota {
			return err
		}
		return nil
	}
	o, err := s.openNewsSearcher(ctx, ws, v.BrandID)
	if errors.Is(err, connectors.ErrNoCapable) {
		return fail("connector_needed", err)
	}
	if err != nil {
		return fail("connector_"+connectors.KindOf(err), err)
	}
	searcher, ok := o.Connector.(connectors.WebSearcher)
	if !ok {
		return fail("connector_needed", errors.New("the connection does not search the web"))
	}
	if err := usagelimits.Check(ctx, s.pool, ws, o.ID, "", 0); err != nil {
		var le *usagelimits.Error
		if errors.As(err, &le) {
			return fail("usage_limit_reached", err)
		}
		return err
	}
	res, serr := searcher.WebSearch(ctx, o.Client, o.Creds, connectors.WebQuery{Query: v.Query, Locale: v.Locale, TimeRange: v.TimeRange})
	if err := s.bookNewsSearch(ctx, ws, o, searchID, res.Cost); err != nil {
		return err
	}
	if serr != nil {
		return fail("connector_"+connectors.KindOf(serr), serr)
	}

	// What to keep: each article once, best first.
	var found []newCoverage
	seen := map[string]bool{}
	for _, r := range res.Results {
		u, d := news.ArticleURL(r.URL)
		if u == "" || seen[u] || len(found) >= news.MaxResults {
			continue
		}
		seen[u] = true
		found = append(found, newCoverage{URL: u, Domain: d, Title: truncate(r.Title, 500), Snippet: truncate(r.Snippet, 1000),
			SearchID: &searchID, Source: "news_watch"})
	}
	newArticles, coverageAdded := 0, 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cov []newCoverage
		for i, f := range found {
			var inserted bool
			var article string
			if err := tx.QueryRow(ctx, `INSERT INTO news_articles (workspace_id, url, title, snippet) VALUES ($1, $2, $3, $4)
				ON CONFLICT (workspace_id, url) DO UPDATE SET last_seen_at = now(),
					title = CASE WHEN excluded.title = '' THEN news_articles.title ELSE excluded.title END,
					snippet = CASE WHEN excluded.snippet = '' THEN news_articles.snippet ELSE excluded.snippet END
				RETURNING id::text, xmax = 0`, ws, f.URL, f.Title, f.Snippet).Scan(&article, &inserted); err != nil {
				return err
			}
			if inserted {
				newArticles++
				// Read its byline, once; the first few of each run.
				if newArticles <= news.MaxReadsPerRun {
					if err := news.StartRead(ctx, tx, ws, article); err != nil {
						return err
					}
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO news_hits (search_id, article_id, workspace_id, position) VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, searchID, article, ws, i+1); err != nil {
				return err
			}
			if v.Purpose == "coverage" && v.BrandID != nil && !news.OwnSite(website, f.Domain) && news.Mentions(v.Query, f.Title, f.Snippet) {
				cov = append(cov, f)
			}
		}
		if len(cov) > 0 {
			added, _, err := keepCoverage(ctx, tx, nil, ws, *v.BrandID, cov)
			if err != nil {
				return err
			}
			coverageAdded = len(added)
		}
		_, err := tx.Exec(ctx, `UPDATE news_searches SET last_checked_at = now(), last_error_code = NULL, failures = 0 WHERE workspace_id = $1 AND id = $2`, ws, searchID)
		return err
	})
	if err != nil {
		return fail("internal", err)
	}
	metrics.NewsSearchRun(v.Purpose, "success", newArticles, coverageAdded)
	ev.Props["results"], ev.Props["kept"], ev.Props["new_articles"], ev.Props["new_coverage"] = len(res.Results), len(found), newArticles, coverageAdded
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	track.Emit(ctx, ev)
	return nil
}

// bookNewsSearch books a search's cost in the workspace's spend ledger,
// against the connection, so it counts toward its usage limits.
func (s *Server) bookNewsSearch(ctx context.Context, ws string, o *connectors.Opened, searchID string, cost connectors.SEOCost) error {
	if cost.USD <= 0 && cost.Units <= 0 {
		return nil
	}
	id, ref := o.ID, "news_search:"+searchID+":"+strconv.FormatInt(time.Now().UnixNano(), 10)
	e := outcomes.Entry{WorkspaceID: ws, Category: outcomes.CategoryData, Provider: o.Connector.Definition().ID, IntegrationID: &id,
		OccurredAt: time.Now(), Source: outcomes.SourceAuto, Ref: &ref, Note: "News search"}
	if cost.Units > 0 {
		u := cost.Units
		e.Units, e.UnitName = &u, "token"
	} else {
		cents := int64(math.Ceil(cost.USD * 100))
		e.AmountCents, e.Currency = &cents, "USD"
	}
	if cost.Estimated {
		e.Note += ", estimated from the price list"
	}
	_, _, err := outcomes.Record(ctx, s.pool, e)
	return err
}

// metricOutcome groups an error code for the runs metric.
func metricOutcome(code string) string {
	switch {
	case code == "connector_needed", code == "usage_limit_reached", code == "internal":
		return code
	case strings.HasPrefix(code, "connector_"):
		return "connector_error"
	}
	return "internal"
}
