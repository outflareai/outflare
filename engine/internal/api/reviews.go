package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/reviews"
)

// Reviews: each brand's review pages (Trustpilot, G2, Capterra, Google),
// every review on them, the platforms' numbers day by day, and Outflare's
// own review score and momentum across platforms. Review pages are brand
// links; this is where they are read, summed up and answered. Reviews use
// the Social scopes: social.read to read, social.manage to change.

func (s *Server) routesReviews(mux *http.ServeMux) {
	read, manage := auth.ScopeSocialRead, auth.ScopeSocialManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/reviews", s.inBrand(read, s.listReviews))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reviews/summary", s.inBrand(read, s.reviewsSummary))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reviews/pages", s.inBrand(read, s.listReviewPages))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reviews/pages/sync", s.inBrand(manage, s.syncReviewPagesNow))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/reviews/pages/{id}", s.inBrand(manage, s.updateReviewPage))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reviews/pages/{id}/read", s.inBrand(manage, s.readReviewPageNow))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reviews/pages/{id}/days", s.inBrand(read, s.listReviewPageDays))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reviews/{id}", s.inBrand(read, s.getReview))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reviews/{id}/draft", s.inBrand(manage, s.draftReviewReply))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reviews/{id}/replied", s.inBrand(manage, s.markReviewReplied))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/reviews/discover", s.inBrand(manage, s.discoverReviewPages))
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands/{brand}/review-guide", s.inBrand(read, s.getReviewGuide))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/brands/{brand}/review-guide", s.inBrand(manage, s.putReviewGuide))
}

// ---- Review pages ----

type reviewPageView struct {
	ID          string   `json:"id"`
	BrandID     *string  `json:"brand_id"`
	Platform    string   `json:"platform"`
	ExternalID  string   `json:"external_id"`
	URL         string   `json:"url"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Score       *float64 `json:"score"`
	ReviewCount *int64   `json:"review_count"`
	// The platform's score and count 28 days ago, from the daily rows;
	// null until there is a row that old.
	ScoreChange28d *float64 `json:"score_change_28d"`
	CountChange28d *int64   `json:"review_count_change_28d"`
	// Outflare's review score for the page and where it is heading, from
	// the reviews kept; null and too_few until there are reviews.
	OutflareScore *int             `json:"outflare_score"`
	Momentum      reviews.Momentum `json:"momentum"`
	Kept          int              `json:"reviews_kept"`
	Unanswered    int              `json:"unanswered_negative"`
	LastReadAt    *time.Time       `json:"last_read_at"`
	NextReadAt    time.Time        `json:"next_read_at"`
	LastError     *string          `json:"last_error"`
	// Why the last read failed in a word, when it did, how many reads in a
	// row failed, and when.
	LastErrorCode *string         `json:"last_error_code"`
	Failures      int             `json:"failures"`
	LastFailedAt  *time.Time      `json:"last_failed_at"`
	BackfilledAt  *time.Time      `json:"backfilled_at"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	ratings       []reviews.Rated `json:"-"`
}

const reviewPageSelect = `
	SELECT p.id::text, p.brand_id::text, p.platform, p.external_id, p.url, p.name, p.status, p.score::float8, p.review_count,
		p.score::float8 - (SELECT d.score::float8 FROM review_page_days d WHERE d.page_id = p.id AND d.score IS NOT NULL
			AND d.day <= (now() AT TIME ZONE 'UTC')::date - 28 ORDER BY d.day DESC LIMIT 1),
		p.review_count - (SELECT d.review_count FROM review_page_days d WHERE d.page_id = p.id AND d.review_count IS NOT NULL
			AND d.day <= (now() AT TIME ZONE 'UTC')::date - 28 ORDER BY d.day DESC LIMIT 1),
		(SELECT count(*) FROM reviews r WHERE r.page_id = p.id AND r.state = 'live')::int,
		(SELECT count(*) FROM reviews r WHERE r.page_id = p.id AND r.state = 'live' AND r.sentiment = 'negative' AND r.replied_at IS NULL
			AND r.posted_at > now() - interval '90 days')::int,
		p.last_read_at, p.next_read_at, p.last_error, p.last_error_code, p.failures, p.last_failed_at, p.backfilled_at, p.created_at, p.updated_at
	FROM review_pages p WHERE p.workspace_id = $1`

func scanReviewPage(row pgx.Row) (reviewPageView, error) {
	var v reviewPageView
	err := row.Scan(&v.ID, &v.BrandID, &v.Platform, &v.ExternalID, &v.URL, &v.Name, &v.Status, &v.Score, &v.ReviewCount,
		&v.ScoreChange28d, &v.CountChange28d, &v.Kept, &v.Unanswered, &v.LastReadAt, &v.NextReadAt, &v.LastError, &v.LastErrorCode, &v.Failures, &v.LastFailedAt, &v.BackfilledAt, &v.CreatedAt, &v.UpdatedAt)
	if v.ScoreChange28d != nil {
		c := math.Round(*v.ScoreChange28d*100) / 100
		v.ScoreChange28d = &c
	}
	return v, err
}

// withScores adds Outflare's review score and momentum to each page.
func (s *Server) withScores(ctx context.Context, ws string, list []reviewPageView) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	rows, err := s.pool.Query(ctx, `SELECT page_id::text, rating::float8, posted_at FROM reviews WHERE workspace_id = $1 AND page_id::text = ANY($2) AND state = 'live'`, ws, ids)
	if err != nil {
		return err
	}
	by := map[string][]reviews.Rated{}
	var id string
	var x reviews.Rated
	if _, err := pgx.ForEachRow(rows, []any{&id, &x.Stars, &x.At}, func() error { by[id] = append(by[id], x); return nil }); err != nil {
		return err
	}
	now := time.Now()
	for i := range list {
		p := &list[i]
		p.ratings = by[p.ID]
		if sc, ok := reviews.ScoreAt(p.ratings, now, reviews.HalfLife); ok {
			p.OutflareScore = &sc.Score
		}
		p.Momentum = reviews.MomentumAt(p.ratings, now, reviews.HalfLife)
	}
	return nil
}

func (s *Server) loadReviewPages(ctx context.Context, a *access, sel brandSel, platform, status string) ([]reviewPageView, error) {
	args := []any{a.WorkspaceID, platform, status}
	rows, err := s.pool.Query(ctx, reviewPageSelect+` AND ($2 = '' OR p.platform = $2) AND (($3 = '' AND p.status <> 'removed') OR p.status = $3)`+
		sel.sql("p.brand_id", &args)+` ORDER BY p.platform, p.created_at LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reviewPageView, error) { return scanReviewPage(row) })
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []reviewPageView{}
	}
	return list, s.withScores(ctx, a.WorkspaceID, list)
}

// reviewPlatform checks ?platform=.
func reviewPlatform(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := r.URL.Query().Get("platform")
	if p != "" && !reviews.IsPlatform(p) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "platform is one of "+strings.Join(reviews.Platforms, ", "))
		return "", false
	}
	return p, true
}

func (s *Server) listReviewPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	platform, ok := reviewPlatform(w, r)
	if !ok {
		return
	}
	st := r.URL.Query().Get("status")
	if st != "" && st != "active" && st != "paused" && st != "removed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, paused or removed")
		return
	}
	list, err := s.loadReviewPages(r.Context(), a, sel, platform, st)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findReviewPage loads a review page the caller may see.
func (s *Server) findReviewPage(w http.ResponseWriter, r *http.Request, a *access, id string) (*reviewPageView, bool) {
	if validUUID(id) {
		v, err := scanReviewPage(s.pool.QueryRow(r.Context(), reviewPageSelect+` AND p.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(v.BrandID) {
			list := []reviewPageView{v}
			if err := s.withScores(r.Context(), a.WorkspaceID, list); err != nil {
				s.internal(w, r, err)
				return nil, false
			}
			return &list[0], true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "review page not found")
	return nil, false
}

// updateReviewPage pauses or resumes reading a page. Adding, changing and
// removing pages is done in the brand's links.
func (s *Server) updateReviewPage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findReviewPage(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Status *string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if req.Status == nil || (*req.Status != "active" && *req.Status != "paused") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or paused")
		return
	}
	if cur.Status == "removed" {
		writeError(w, r, http.StatusConflict, "conflict", "this page is no longer in the brand's links; add it to the brand again to read it")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE review_pages SET status = $3, next_read_at = CASE WHEN $3 = 'active' THEN now() ELSE next_read_at END, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, *req.Status); err != nil {
			return err
		}
		if err := syncReviewFixes(ctx, tx, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		if *req.Status == "active" {
			if err := s.enqueueReviewRead(ctx, tx, a.WorkspaceID, "resume:"+cur.ID+":"+newID()); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "review_page.updated", "review_page", cur.ID, map[string]any{"platform": cur.Platform, "status": *req.Status})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findReviewPage(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// syncReviewPagesNow reads every review page the caller can see now.
func (s *Server) syncReviewPagesNow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	readers, err := s.pageReaders(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(readers) == 0 {
		writeError(w, r, http.StatusConflict, "connector_needed", "connect Geonode first: review pages are read with your own Geonode key")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		args := []any{a.WorkspaceID}
		sel := brandSel{in: a.Brands}
		if _, err := tx.Exec(ctx, `UPDATE review_pages p SET next_read_at = now() WHERE p.workspace_id = $1 AND p.status = 'active'`+sel.sql("p.brand_id", &args), args...); err != nil {
			return err
		}
		if err := s.enqueueReviewRead(ctx, tx, a.WorkspaceID, "asked:"+newID()); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "review_pages.read_requested", "workspace", a.WorkspaceID, map[string]any{})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// readReviewPageNow reads one review page now, such as one whose last read
// failed, instead of waiting for its next try.
func (s *Server) readReviewPageNow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findReviewPage(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if cur.Status != "active" {
		writeError(w, r, http.StatusConflict, "conflict", "this page is "+cur.Status+"; resume it to read it")
		return
	}
	ctx := r.Context()
	readers, err := s.pageReaders(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(readers) == 0 {
		writeError(w, r, http.StatusConflict, "connector_needed", "connect Geonode first: review pages are read with your own Geonode key")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE review_pages SET next_read_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		if err := s.enqueueReviewRead(ctx, tx, a.WorkspaceID, "page:"+cur.ID+":"+newID()); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "review_page.read_requested", "review_page", cur.ID,
			map[string]any{"platform": cur.Platform, "failures": cur.Failures, "last_error_code": deref(cur.LastErrorCode)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

type reviewPageDay struct {
	Day           string   `json:"day"`
	Score         *float64 `json:"score"`
	ReviewCount   *int64   `json:"review_count"`
	NewReviews    int      `json:"new_reviews"`
	Stars         [5]int   `json:"stars"`
	OutflareScore *int     `json:"outflare_score"`
	Momentum      *string  `json:"momentum"`
}

func (s *Server) listReviewPageDays(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := s.findReviewPage(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	days, ok := intParam(w, r, "days", 90, 1, 730)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT day::text, score::float8, review_count, new_reviews, stars_1, stars_2, stars_3, stars_4, stars_5, outflare_score, momentum
		FROM review_page_days WHERE workspace_id = $1 AND page_id = $2 AND day > (now() AT TIME ZONE 'UTC')::date - $3::int ORDER BY day`, a.WorkspaceID, p.ID, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []reviewPageDay{}
	var d reviewPageDay
	if _, err := pgx.ForEachRow(rows, []any{&d.Day, &d.Score, &d.ReviewCount, &d.NewReviews, &d.Stars[0], &d.Stars[1], &d.Stars[2], &d.Stars[3], &d.Stars[4], &d.OutflareScore, &d.Momentum},
		func() error { out = append(out, d); return nil }); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// intParam reads an integer query parameter between lo and hi.
func intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a whole number from "+strconv.Itoa(lo)+" to "+strconv.Itoa(hi))
		return 0, false
	}
	return n, true
}

func (s *Server) discoverReviewPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		queued, err := s.enqueueReviewDiscovery(ctx, tx, a.WorkspaceID, b.ID, "asked:"+newID())
		if err != nil {
			return err
		}
		if !queued {
			return errNoPageReader
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "review_pages.discovery_requested", "brand", b.ID, map[string]any{})
	})
	switch {
	case errors.Is(err, errNoPageReader):
		writeError(w, r, http.StatusConflict, "connector_needed", "connect Geonode first: review pages are found with your own Geonode key")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
	}
}

// ---- Reviews ----

type reviewDraftView struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	Body         string    `json:"body"`
	Hint         string    `json:"hint"`
	StatusReason *string   `json:"status_reason"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type reviewView struct {
	ID              string     `json:"id"`
	BrandID         *string    `json:"brand_id"`
	PageID          string     `json:"page_id"`
	Platform        string     `json:"platform"`
	URL             string     `json:"url"`
	Rating          float64    `json:"rating"`
	Title           string     `json:"title"`
	Body            string     `json:"body"`
	Language        string     `json:"language"`
	AuthorName      string     `json:"author_name"`
	AuthorCountry   string     `json:"author_country"`
	Verified        bool       `json:"verified"`
	PostedAt        time.Time  `json:"posted_at"`
	EditedAt        *time.Time `json:"edited_at"`
	Sentiment       string     `json:"sentiment"`
	SentimentSource string     `json:"sentiment_source"`
	ReplyBody       string     `json:"reply_body"`
	RepliedAt       *time.Time `json:"replied_at"`
	// Minutes from the review to the reply, when both are known.
	ReplyMinutes *int             `json:"reply_minutes"`
	State        string           `json:"state"`
	Draft        *reviewDraftView `json:"draft"`
	FirstSeenAt  time.Time        `json:"first_seen_at"`
}

const reviewSelect = `
	SELECT r.id::text, r.brand_id::text, r.page_id::text, r.platform, r.url, r.rating::float8, r.title, r.body, r.language, r.author_name, r.author_country,
		r.verified, r.posted_at, r.edited_at, r.sentiment, r.sentiment_source, r.reply_body, r.replied_at,
		CASE WHEN r.replied_at >= r.posted_at THEN (extract(epoch FROM r.replied_at - r.posted_at) / 60)::int END,
		r.state, r.first_seen_at,
		d.id::text, d.status, d.body, d.hint, d.status_reason, d.created_at, d.updated_at
	FROM reviews r
	LEFT JOIN LATERAL (SELECT * FROM review_replies x WHERE x.review_id = r.id ORDER BY x.created_at DESC LIMIT 1) d ON true
	WHERE r.workspace_id = $1`

func scanReview(row pgx.Row) (reviewView, error) {
	var v reviewView
	var dID, dStatus, dBody, dHint *string
	var dReason *string
	var dCreated, dUpdated *time.Time
	err := row.Scan(&v.ID, &v.BrandID, &v.PageID, &v.Platform, &v.URL, &v.Rating, &v.Title, &v.Body, &v.Language, &v.AuthorName, &v.AuthorCountry,
		&v.Verified, &v.PostedAt, &v.EditedAt, &v.Sentiment, &v.SentimentSource, &v.ReplyBody, &v.RepliedAt, &v.ReplyMinutes, &v.State, &v.FirstSeenAt,
		&dID, &dStatus, &dBody, &dHint, &dReason, &dCreated, &dUpdated)
	if err == nil && dID != nil {
		v.Draft = &reviewDraftView{ID: *dID, Status: *dStatus, Body: *dBody, Hint: *dHint, StatusReason: dReason, CreatedAt: *dCreated, UpdatedAt: *dUpdated}
	}
	return v, err
}

func (s *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	platform, ok := reviewPlatform(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page := q.Get("page_id")
	if page != "" && !validUUID(page) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "page_id is a review page id")
		return
	}
	sentiment := q.Get("sentiment")
	if sentiment != "" && !slices.Contains([]string{"positive", "neutral", "negative", "mixed"}, sentiment) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sentiment is positive, neutral, negative or mixed")
		return
	}
	replied := q.Get("replied")
	if replied != "" && replied != "true" && replied != "false" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "replied is true or false")
		return
	}
	stars, ok := intParam(w, r, "stars", 0, 1, 5)
	if !ok {
		return
	}
	days, ok := intParam(w, r, "days", 0, 1, 3650)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	var before *time.Time
	if b := q.Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before is a time, like 2026-10-01T00:00:00Z")
			return
		}
		before = &t
	}
	args := []any{a.WorkspaceID, platform, page, sentiment, replied, stars, days, before, limit}
	rows, err := s.pool.Query(r.Context(), reviewSelect+` AND r.state = 'live' AND ($2 = '' OR r.platform = $2) AND ($3 = '' OR r.page_id::text = $3)
		AND ($4 = '' OR r.sentiment = $4) AND ($5 = '' OR (r.replied_at IS NOT NULL OR r.reply_body <> '') = ($5 = 'true'))
		AND ($6 = 0 OR round(r.rating) = $6) AND ($7 = 0 OR r.posted_at > now() - make_interval(days => $7))
		AND ($8::timestamptz IS NULL OR r.posted_at < $8)
		AND EXISTS (SELECT 1 FROM review_pages p WHERE p.id = r.page_id AND p.status <> 'removed')`+
		sel.sql("r.brand_id", &args)+` ORDER BY r.posted_at DESC, r.id LIMIT $9`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reviewView, error) { return scanReview(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []reviewView{}
	}
	out := map[string]any{"data": list, "next_before": nil}
	if len(list) == limit {
		out["next_before"] = list[len(list)-1].PostedAt
	}
	writeJSON(w, http.StatusOK, out)
}

// findReview loads a review the caller may see.
func (s *Server) findReview(w http.ResponseWriter, r *http.Request, a *access, id string) (*reviewView, bool) {
	if validUUID(id) {
		v, err := scanReview(s.pool.QueryRow(r.Context(), reviewSelect+` AND r.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "review not found")
	return nil, false
}

func (s *Server) getReview(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findReview(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// markReviewReplied records that the business answered a review on the
// platform, with the answer it posted when given. The next read keeps the
// platform's own copy.
func (s *Server) markReviewReplied(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findReview(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	body := strings.TrimSpace(req.Body)
	if len([]rune(body)) > 10000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is at most 10,000 characters")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE reviews SET replied_at = COALESCE(replied_at, now()),
			reply_body = CASE WHEN $3 <> '' THEN $3 ELSE reply_body END, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID, body); err != nil {
			return err
		}
		if err := syncReviewFixes(ctx, tx, a.WorkspaceID, v.PageID); err != nil {
			return err
		}
		if v.RepliedAt != nil {
			return nil
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "review.replied", "review", v.ID, map[string]any{
			"page_id": v.PageID, "platform": v.Platform, "via": "outflare", "drafted": v.Draft != nil && v.Draft.Status == "draft"})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findReview(w, r, a, v.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// ---- Summary ----

type reviewPlatformSummary struct {
	Platform      string           `json:"platform"`
	Pages         int              `json:"pages"`
	Score         *float64         `json:"score"`
	ReviewCount   *int64           `json:"review_count"`
	OutflareScore *int             `json:"outflare_score"`
	Momentum      reviews.Momentum `json:"momentum"`
}

type reviewWeek struct {
	Week          string `json:"week"`
	OutflareScore *int   `json:"outflare_score"`
	NewReviews    int    `json:"new_reviews"`
}

func (s *Server) reviewsSummary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	weeks, ok := intParam(w, r, "weeks", 26, 4, 104)
	if !ok {
		return
	}
	ctx := r.Context()
	pages, err := s.loadReviewPages(ctx, a, sel, "", "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := time.Now().UTC()
	var all []reviews.Rated
	byPlatform := map[string]*reviewPlatformSummary{}
	var ratings = map[string][]reviews.Rated{}
	for _, p := range pages {
		if p.Status != "active" && p.Status != "paused" {
			continue
		}
		all = append(all, p.ratings...)
		x := byPlatform[p.Platform]
		if x == nil {
			x = &reviewPlatformSummary{Platform: p.Platform}
			byPlatform[p.Platform] = x
		}
		x.Pages++
		ratings[p.Platform] = append(ratings[p.Platform], p.ratings...)
		if p.ReviewCount != nil {
			c := deref64(x.ReviewCount) + *p.ReviewCount
			if p.Score != nil {
				// Several pages on one platform: their scores weighted by count.
				prev := 0.0
				if x.Score != nil {
					prev = *x.Score * float64(deref64(x.ReviewCount))
				}
				sc := math.Round((prev+*p.Score*float64(*p.ReviewCount))/math.Max(float64(c), 1)*100) / 100
				x.Score = &sc
			}
			x.ReviewCount = &c
		} else if x.Score == nil {
			x.Score = p.Score
		}
	}
	platforms := []reviewPlatformSummary{}
	for _, id := range reviews.Platforms {
		x := byPlatform[id]
		if x == nil {
			continue
		}
		if sc, ok := reviews.ScoreAt(ratings[id], now, reviews.HalfLife); ok {
			x.OutflareScore = &sc.Score
		}
		x.Momentum = reviews.MomentumAt(ratings[id], now, reviews.HalfLife)
		platforms = append(platforms, *x)
	}
	var score *int
	var average *float64
	if sc, ok := reviews.ScoreAt(all, now, reviews.HalfLife); ok {
		score, average = &sc.Score, &sc.Average
	}
	// Week by week: the score at each week's end and that week's new reviews.
	series := make([]reviewWeek, 0, weeks)
	sort.Slice(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	end := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	for i := weeks - 1; i >= 0; i-- {
		to := end.AddDate(0, 0, -7*i)
		from := to.AddDate(0, 0, -7)
		wk := reviewWeek{Week: from.Format("2006-01-02")}
		if sc, ok := reviews.ScoreAt(all, to, reviews.HalfLife); ok {
			wk.OutflareScore = &sc.Score
		}
		for _, x := range all {
			if !x.At.Before(from) && x.At.Before(to) {
				wk.NewReviews++
			}
		}
		series = append(series, wk)
	}
	// Sentiment, replies and reply time over the last 28 days.
	args := []any{a.WorkspaceID}
	var pos, neu, neg, total, answered int
	var medianMin *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE r.sentiment = 'positive'), count(*) FILTER (WHERE r.sentiment = 'neutral'),
			count(*) FILTER (WHERE r.sentiment IN ('negative', 'mixed')), count(*),
			count(*) FILTER (WHERE r.replied_at IS NOT NULL OR r.reply_body <> ''),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM r.replied_at - r.posted_at) / 60) FILTER (WHERE r.replied_at >= r.posted_at)
		FROM reviews r JOIN review_pages p ON p.id = r.page_id AND p.status <> 'removed'
		WHERE r.workspace_id = $1 AND r.state = 'live' AND r.posted_at > now() - interval '28 days'`+sel.sql("r.brand_id", &args), args...).
		Scan(&pos, &neu, &neg, &total, &answered, &medianMin); err != nil {
		s.internal(w, r, err)
		return
	}
	share := func(n int) *float64 {
		if total == 0 {
			return nil
		}
		v := math.Round(float64(n)/float64(total)*100) / 100
		return &v
	}
	var replyHours *float64
	if medianMin != nil {
		h := math.Round(*medianMin/60*10) / 10
		replyHours = &h
	}
	readers, err := s.pageReaders(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ai, err := s.hasAIModel(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"outflare_score": score,
		"average":        average,
		"reviews":        len(all),
		"momentum":       reviews.MomentumAt(all, now, reviews.HalfLife),
		"platforms":      platforms,
		"weeks":          series,
		"last_28_days": map[string]any{
			"reviews": total, "positive": share(pos), "neutral": share(neu), "negative": share(neg),
			"reply_rate": share(answered), "median_reply_hours": replyHours,
		},
		"half_life_days": int(reviews.HalfLife.Hours() / 24),
		"can_read":       len(readers) > 0,
		"can_draft":      ai,
	})
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---- Reply guide ----

type reviewGuideView struct {
	BrandID        string `json:"brand_id"`
	Tone           string `json:"tone"`
	SignedBy       string `json:"signed_by"`
	SupportContact string `json:"support_contact"`
	NeverSay       string `json:"never_say"`
	Notes          string `json:"notes"`
	// Prefilled is true until someone saves it: the tone and rules come
	// from the brand's context.
	Prefilled bool       `json:"prefilled"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func (s *Server) loadReviewGuide(ctx context.Context, ws, brandID string) (reviewGuideView, error) {
	g := reviewGuideView{BrandID: brandID}
	err := s.pool.QueryRow(ctx, `SELECT tone, signed_by, support_contact, never_say, notes, updated_at FROM review_reply_guides
		WHERE workspace_id = $1 AND brand_id = $2`, ws, brandID).Scan(&g.Tone, &g.SignedBy, &g.SupportContact, &g.NeverSay, &g.Notes, &g.UpdatedAt)
	if err == nil {
		return g, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return g, err
	}
	// Not saved yet: start from what the brand's context says.
	g.Prefilled = true
	var name string
	err = s.pool.QueryRow(ctx, `SELECT b.name, COALESCE(c.voice, ''), COALESCE(c.rules, '') FROM brands b
		LEFT JOIN brand_context c ON c.brand_id = b.id WHERE b.workspace_id = $1 AND b.id = $2`, ws, brandID).Scan(&name, &g.Tone, &g.NeverSay)
	if err != nil {
		return g, err
	}
	g.Tone, g.NeverSay = truncate(g.Tone, 2000), truncate(g.NeverSay, 2000)
	if g.SignedBy == "" && name != "" {
		g.SignedBy = "The " + name + " team"
	}
	return g, nil
}

func (s *Server) getReviewGuide(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	g, err := s.loadReviewGuide(r.Context(), a.WorkspaceID, b.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) putReviewGuide(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Tone           string `json:"tone"`
		SignedBy       string `json:"signed_by"`
		SupportContact string `json:"support_contact"`
		NeverSay       string `json:"never_say"`
		Notes          string `json:"notes"`
	}
	if !decode(w, r, &req) {
		return
	}
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(&b.ID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	for name, v := range map[string]string{"tone": req.Tone, "never_say": req.NeverSay, "notes": req.Notes} {
		if len([]rune(v)) > 4000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is at most 4,000 characters")
			return
		}
	}
	if len([]rune(req.SignedBy)) > 200 || len([]rune(req.SupportContact)) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "signed_by is at most 200 characters and support_contact at most 500")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO review_reply_guides (workspace_id, brand_id, tone, signed_by, support_contact, never_say, notes, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (brand_id) DO UPDATE SET tone = EXCLUDED.tone, signed_by = EXCLUDED.signed_by, support_contact = EXCLUDED.support_contact,
				never_say = EXCLUDED.never_say, notes = EXCLUDED.notes, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			a.WorkspaceID, b.ID, strings.TrimSpace(req.Tone), strings.TrimSpace(req.SignedBy), strings.TrimSpace(req.SupportContact),
			strings.TrimSpace(req.NeverSay), strings.TrimSpace(req.Notes), a.ActorID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "review_guide.updated", "brand", b.ID, map[string]any{})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	g, err := s.loadReviewGuide(ctx, a.WorkspaceID, b.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}
