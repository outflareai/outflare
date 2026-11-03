package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/cmspublish"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Content: posts and pages written in Outflare for a brand's own website
// (docs/content.md). A person or an agent writes an item (Markdown), a
// person approves it, and a publication puts it on the brand's own CMS
// connection (WordPress, Ghost) as a draft there or live. Each
// publication is a content_publishes row made with its audit event and
// the content.publish job queued in the same transaction (cmspublish):
// the CMS call is the side effect, so it never runs here. For an agent,
// the action layer holds the publication like any other change.

func (s *Server) routesContent(mux *http.ServeMux) {
	read, manage := auth.ScopeContentRead, auth.ScopeContentManage
	mux.HandleFunc("GET /v1/brands/{brand}/content", s.onBrand(read, s.listContent))
	mux.HandleFunc("POST /v1/brands/{brand}/content", s.onBrand(manage, s.createContent))
	mux.HandleFunc("GET /v1/brands/{brand}/content/{id}", s.onBrand(read, s.getContent))
	mux.HandleFunc("PATCH /v1/brands/{brand}/content/{id}", s.onBrand(manage, s.updateContent))
	mux.HandleFunc("POST /v1/brands/{brand}/content/{id}/publish", s.onBrand(manage, s.publishContent))
}

var (
	contentStatuses = []string{"draft", "approved", "published", "archived"}
	contentTargets  = []string{connectors.CMSPosts, connectors.CMSPages}
	slugRe          = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type contentView struct {
	ID            string     `json:"id"`
	BrandID       string     `json:"brand_id"`
	Title         string     `json:"title"`
	Slug          string     `json:"slug"`
	Target        string     `json:"target"`
	Body          string     `json:"body,omitempty"`
	Excerpt       string     `json:"excerpt"`
	Tags          []string   `json:"tags"`
	CanonicalURL  string     `json:"canonical_url"`
	Status        string     `json:"status"`
	IntegrationID *string    `json:"integration_id"`
	ExternalID    string     `json:"external_id"`
	URL           string     `json:"url"`
	CMSStatus     string     `json:"cms_status"`
	PublishedAt   *time.Time `json:"published_at"`
	Notes         string     `json:"notes"`
	CreatedBy     *string    `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	// The latest publications, newest first; only on one item.
	Publishes []contentPublishView `json:"publishes,omitempty"`
}

type contentPublishView struct {
	ID         string     `json:"id"`
	ItemID     string     `json:"item_id"`
	Kind       string     `json:"kind"`
	Live       bool       `json:"live"`
	Status     string     `json:"status"`
	Error      string     `json:"error"`
	ExternalID string     `json:"external_id"`
	URL        string     `json:"url"`
	CMSStatus  string     `json:"cms_status"`
	Verified   *bool      `json:"verified"`
	CreatedAt  time.Time  `json:"created_at"`
	AppliedAt  *time.Time `json:"applied_at"`
}

const contentCols = `i.id, i.brand_id, i.title, i.slug, i.target, i.body, i.excerpt, i.tags, i.canonical_url, i.status, i.integration_id, i.external_id, i.url, i.cms_status,
	i.published_at, i.notes, ac.display_name, i.created_at, i.updated_at`

const contentFrom = `FROM content_items i LEFT JOIN actors ac ON ac.id = i.created_by`

func scanContent(row pgx.CollectableRow) (contentView, error) {
	var v contentView
	err := row.Scan(&v.ID, &v.BrandID, &v.Title, &v.Slug, &v.Target, &v.Body, &v.Excerpt, &v.Tags, &v.CanonicalURL, &v.Status, &v.IntegrationID, &v.ExternalID, &v.URL, &v.CMSStatus,
		&v.PublishedAt, &v.Notes, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return v, err
}

func (s *Server) listContent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	args := []any{a.WorkspaceID, a.Brand}
	where := `i.workspace_id = $1 AND i.brand_id = $2`
	if v := qs.Get("status"); v != "" {
		if !slices.Contains(contentStatuses, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is draft, approved, published or archived")
			return
		}
		args = append(args, v)
		where += ` AND i.status = $` + strconv.Itoa(len(args))
	}
	if v := qs.Get("target"); v != "" {
		if !slices.Contains(contentTargets, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target is posts or pages")
			return
		}
		args = append(args, v)
		where += ` AND i.target = $` + strconv.Itoa(len(args))
	}
	if v := strings.TrimSpace(qs.Get("q")); v != "" {
		args = append(args, likeEscape(v))
		where += ` AND (i.title ILIKE '%' || $` + strconv.Itoa(len(args)) + ` || '%' OR i.slug ILIKE '%' || $` + strconv.Itoa(len(args)) + ` || '%')`
	}
	if v := qs.Get("published_since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "published_since is an RFC 3339 time")
			return
		}
		args = append(args, t)
		where += ` AND i.published_at >= $` + strconv.Itoa(len(args))
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	args = append(args, before)
	where += ` AND ($` + strconv.Itoa(len(args)) + `::timestamptz IS NULL OR i.created_at < $` + strconv.Itoa(len(args)) + `)`
	args = append(args, limit)
	// The list leaves bodies out; read one item for its body.
	cols := strings.Replace(contentCols, "i.body,", "''::text,", 1)
	rows, err := s.pool.Query(r.Context(), `SELECT `+cols+` `+contentFrom+` WHERE `+where+` ORDER BY i.created_at DESC, i.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanContent)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []contentView{}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}

type contentInput struct {
	Title        *string   `json:"title"`
	Slug         *string   `json:"slug"`
	Target       *string   `json:"target"`
	Body         *string   `json:"body"`
	Excerpt      *string   `json:"excerpt"`
	Tags         *[]string `json:"tags"`
	CanonicalURL *string   `json:"canonical_url"`
	Notes        *string   `json:"notes"`
	// draft, approved or archived; published is set by a publication.
	Status *string `json:"status"`
}

func (in *contentInput) validate() string {
	if in.Title != nil {
		*in.Title = strings.Join(strings.Fields(*in.Title), " ")
		if *in.Title == "" || len(*in.Title) > 300 {
			return "title must be 1 to 300 characters"
		}
	}
	if in.Slug != nil {
		*in.Slug = strings.TrimSpace(*in.Slug)
		if *in.Slug != "" && (len(*in.Slug) > 200 || !slugRe.MatchString(*in.Slug)) {
			return "slug is lowercase letters, digits and hyphens, at most 200 characters; leave it empty to let the CMS choose"
		}
	}
	if in.Target != nil && !slices.Contains(contentTargets, *in.Target) {
		return "target is posts or pages"
	}
	if in.Body != nil && len(*in.Body) > 200000 {
		return "body is at most 200000 characters of Markdown"
	}
	if in.Excerpt != nil {
		*in.Excerpt = strings.TrimSpace(*in.Excerpt)
		if len(*in.Excerpt) > 1000 {
			return "excerpt is at most 1000 characters"
		}
	}
	if in.Tags != nil {
		if len(*in.Tags) > 20 {
			return "at most 20 tags"
		}
		for i, t := range *in.Tags {
			t = strings.TrimSpace(t)
			if t == "" || len(t) > 100 {
				return "each tag is 1 to 100 characters"
			}
			(*in.Tags)[i] = t
		}
	}
	if in.CanonicalURL != nil {
		*in.CanonicalURL = strings.TrimSpace(*in.CanonicalURL)
		if *in.CanonicalURL != "" && (len(*in.CanonicalURL) > 2000 || !strings.HasPrefix(*in.CanonicalURL, "https://") && !strings.HasPrefix(*in.CanonicalURL, "http://")) {
			return "canonical_url is a full address starting with https://"
		}
	}
	if in.Notes != nil && len(*in.Notes) > 5000 {
		return "notes are at most 5000 characters"
	}
	if in.Status != nil && !slices.Contains([]string{"draft", "approved", "archived"}, *in.Status) {
		return "status is draft, approved or archived; published is set when it goes live"
	}
	return ""
}

func (s *Server) createContent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req contentInput
	if !decode(w, r, &req) {
		return
	}
	if req.Title == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title is required")
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	target := connectors.CMSPosts
	if req.Target != nil {
		target = *req.Target
	}
	status := "draft"
	if req.Status != nil {
		status = *req.Status
	}
	tags := []string{}
	if req.Tags != nil {
		tags = *req.Tags
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO content_items (workspace_id, brand_id, title, slug, target, body, excerpt, tags, canonical_url, notes, status, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
			a.WorkspaceID, a.Brand, *req.Title, str(req.Slug), target, str(req.Body), str(req.Excerpt), tags, str(req.CanonicalURL), str(req.Notes), status, actorRef(a)).Scan(&id); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "content.created", "content", id,
			map[string]any{"brand_id": a.Brand, "title": *req.Title, "target": target, "status": status},
			map[string]any{"brand_id": a.Brand, "target": target, "status": status, "body_chars": len(str(req.Body))})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeContent(w, r, a, id, http.StatusCreated)
}

func (s *Server) loadContent(ctx context.Context, a *access, id string) (contentView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+contentCols+` `+contentFrom+` WHERE i.id = $1 AND i.brand_id = $2 AND i.workspace_id = $3`, id, a.Brand, a.WorkspaceID)
	if err != nil {
		return contentView{}, err
	}
	v, err := pgx.CollectOneRow(rows, scanContent)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	prow, err := s.pool.Query(ctx, `SELECT id, item_id, kind, live, status, error, external_id, url, cms_status, verified, created_at, applied_at
		FROM content_publishes WHERE item_id = $1 ORDER BY created_at DESC LIMIT 10`, id)
	if err != nil {
		return v, err
	}
	v.Publishes, err = pgx.CollectRows(prow, func(row pgx.CollectableRow) (contentPublishView, error) {
		var p contentPublishView
		err := row.Scan(&p.ID, &p.ItemID, &p.Kind, &p.Live, &p.Status, &p.Error, &p.ExternalID, &p.URL, &p.CMSStatus, &p.Verified, &p.CreatedAt, &p.AppliedAt)
		return p, err
	})
	if v.Publishes == nil {
		v.Publishes = []contentPublishView{}
	}
	return v, err
}

func (s *Server) writeContent(w http.ResponseWriter, r *http.Request, a *access, id string, status int) {
	v, err := s.loadContent(r.Context(), a, id)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) getContent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "content not found")
		return
	}
	s.writeContent(w, r, a, id, http.StatusOK)
}

func (s *Server) updateContent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "content not found")
		return
	}
	var req contentInput
	if !decode(w, r, &req) {
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `SELECT status FROM content_items WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 FOR UPDATE`, id, a.Brand, a.WorkspaceID).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		status := req.Status
		// A live item that is edited goes back to approved: the change is
		// only on the site once it is published again.
		if status == nil && cur == "published" && (req.Title != nil || req.Body != nil || req.Slug != nil || req.Excerpt != nil || req.Tags != nil || req.CanonicalURL != nil) {
			s := "approved"
			status = &s
		}
		if _, err := tx.Exec(ctx, `UPDATE content_items SET title = COALESCE($2, title), slug = COALESCE($3, slug), target = COALESCE($4, target), body = COALESCE($5, body),
				excerpt = COALESCE($6, excerpt), tags = COALESCE($7, tags), canonical_url = COALESCE($8, canonical_url), notes = COALESCE($9, notes), status = COALESCE($10, status), updated_at = now()
			WHERE id = $1`, id, req.Title, req.Slug, req.Target, req.Body, req.Excerpt, req.Tags, req.CanonicalURL, req.Notes, status); err != nil {
			return err
		}
		changed := map[string]any{"brand_id": a.Brand}
		for k, set := range map[string]bool{"title": req.Title != nil, "slug": req.Slug != nil, "target": req.Target != nil, "body": req.Body != nil, "excerpt": req.Excerpt != nil,
			"tags": req.Tags != nil, "canonical_url": req.CanonicalURL != nil, "notes": req.Notes != nil} {
			if set {
				changed[k] = true
			}
		}
		action := "content.updated"
		if status != nil && *status != cur {
			changed["status"] = *status
			if *status == "approved" && req.Status != nil {
				action = "content.approved"
			}
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "content", id, changed, map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeContent(w, r, a, id, http.StatusOK)
}

// publishContent queues a publication: the item goes to the CMS as a
// draft there, or live when it is approved.
func (s *Server) publishContent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "content not found")
		return
	}
	var req struct {
		IntegrationID string `json:"integration_id"`
		Live          bool   `json:"live"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	item, err := s.loadContent(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	if item.Status == "archived" {
		writeError(w, r, http.StatusConflict, "conflict", "an archived item is not published; set it back to draft or approved first")
		return
	}
	if req.Live && item.Status != "approved" && item.Status != "published" {
		writeError(w, r, http.StatusConflict, "conflict", "only an approved item goes live; publish it as a draft on the CMS, or approve it first (status: approved)")
		return
	}
	// The CMS connection: the one asked for, the one it is already on,
	// or the brand's one CMS.
	integrationID := req.IntegrationID
	if integrationID == "" && item.IntegrationID != nil {
		integrationID = *item.IntegrationID
	}
	var o *connectors.Opened
	if integrationID != "" {
		if !validUUID(integrationID) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id is a connection's id")
			return
		}
		o, err = s.integrations.Open(ctx, s.pool, a.WorkspaceID, integrationID)
		if errors.Is(err, connectors.ErrNotFound) || (err == nil && len(connectors.ForBrand([]connectors.Integration{o.Integration}, a.Brand)) == 0) {
			writeError(w, r, http.StatusNotFound, "not_found", "no such connection for this brand")
			return
		}
	} else {
		o, err = s.integrations.OpenCapable(ctx, s.pool, a.WorkspaceID, a.Brand, connectors.CapCMSPublish)
		if errors.Is(err, connectors.ErrNoCapable) {
			writeError(w, r, http.StatusConflict, "connector_needed", "connect the brand's CMS first (WordPress or Ghost, under Connectors)")
			return
		}
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	pub, ok := o.Connector.(connectors.CMSPublisher)
	if !ok {
		writeError(w, r, http.StatusConflict, "connector_needed", o.Connector.Definition().Name+" does not publish content; connect WordPress or Ghost")
		return
	}
	if !slices.Contains(pub.Targets(), item.Target) {
		writeError(w, r, http.StatusConflict, "conflict", o.Connector.Definition().Name+" does not publish "+item.Target)
		return
	}
	kind := "create"
	if item.ExternalID != "" && item.IntegrationID != nil && *item.IntegrationID == o.ID {
		kind = "update"
	}
	var pubID string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var queued int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM content_publishes WHERE item_id = $1 AND status = 'queued'`, id).Scan(&queued); err != nil {
			return err
		}
		if queued > 0 {
			return errInvalid("a publication of this item is already queued; wait for it")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO content_publishes (workspace_id, brand_id, item_id, integration_id, kind, live, requested_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, a.WorkspaceID, a.Brand, id, o.ID, kind, req.Live, actorRef(a)).Scan(&pubID); err != nil {
			return err
		}
		if err := cmspublish.Enqueue(ctx, tx, a.WorkspaceID, pubID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "content.publish_requested", "content", id,
			map[string]any{"brand_id": a.Brand, "publish_id": pubID, "integration_id": o.ID, "connector": o.Connector.Definition().ID, "kind": kind, "live": req.Live},
			map[string]any{"brand_id": a.Brand, "connector": o.Connector.Definition().ID, "kind": kind, "live": req.Live})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadContent(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	var queued contentPublishView
	for _, p := range v.Publishes {
		if p.ID == pubID {
			queued = p
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "publish": queued, "item": v})
}
