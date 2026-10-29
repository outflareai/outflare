package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/stories"
)

// Digital PR stories: the brand's experts (person records it quotes, one
// the default), the stories it pitches, the facts behind every number and
// the quotes its experts approved. A story's release is checked (package
// stories) and only a person can approve it, after the check passes; any
// change after that moves it back to draft. Nothing here costs money.

func (s *Server) routesStories(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/pr-experts", s.inBrand(read, s.listPRExperts))
	mux.HandleFunc("POST /v1/workspaces/{ws}/pr-experts", s.inBrand(manage, s.createPRExpert))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/pr-experts/{expert}", s.inBrand(manage, s.updatePRExpert))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/pr-experts/{expert}", s.inBrand(manage, s.deletePRExpert))
	mux.HandleFunc("GET /v1/workspaces/{ws}/stories", s.inBrand(read, s.listStories))
	mux.HandleFunc("POST /v1/workspaces/{ws}/stories", s.inBrand(manage, s.createStory))
	mux.HandleFunc("GET /v1/workspaces/{ws}/stories/{story}", s.inBrand(read, s.getStory))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/stories/{story}", s.inBrand(manage, s.updateStory))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/stories/{story}", s.inBrand(manage, s.deleteStory))
	mux.HandleFunc("POST /v1/workspaces/{ws}/stories/{story}/facts", s.inBrand(manage, s.addStoryFact))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/stories/{story}/facts/{fact}", s.inBrand(manage, s.deleteStoryFact))
	mux.HandleFunc("POST /v1/workspaces/{ws}/stories/{story}/quotes", s.inBrand(manage, s.addStoryQuote))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/stories/{story}/quotes/{quote}", s.inBrand(manage, s.updateStoryQuote))
	mux.HandleFunc("POST /v1/workspaces/{ws}/stories/{story}/check", s.inBrand(read, s.checkStory))
	mux.HandleFunc("POST /v1/workspaces/{ws}/stories/{story}/approve", s.inBrand(manage, s.approveStory))
}

// ---- Experts ----

type prExpertView struct {
	ID        string    `json:"id"`
	BrandID   string    `json:"brand_id"`
	RecordID  string    `json:"record_id"`
	Name      string    `json:"name"`
	Email     *string   `json:"email"`
	Title     string    `json:"title"`
	Topics    []string  `json:"topics"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const prExpertCols = `x.id::text, x.brand_id::text, x.record_id::text, r.name,
	(SELECT i.value FROM record_identities i WHERE i.record_id = x.record_id AND i.namespace = 'email' AND i.valid_to IS NULL
		ORDER BY i.verified DESC, i.created_at LIMIT 1),
	x.title, x.topics, x.is_default, x.created_at, x.updated_at`

const prExpertFrom = ` FROM pr_experts x JOIN records r ON r.id = x.record_id`

func scanPRExpert(row pgx.Row) (prExpertView, error) {
	var v prExpertView
	err := row.Scan(&v.ID, &v.BrandID, &v.RecordID, &v.Name, &v.Email, &v.Title, &v.Topics, &v.IsDefault, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Server) loadPRExpert(ctx context.Context, ws, id string) (*prExpertView, error) {
	v, err := scanPRExpert(s.pool.QueryRow(ctx, `SELECT `+prExpertCols+prExpertFrom+` WHERE x.workspace_id = $1 AND x.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (s *Server) prExpertFor(w http.ResponseWriter, r *http.Request, a *access) (*prExpertView, bool) {
	if id := r.PathValue("expert"); validUUID(id) {
		v, err := s.loadPRExpert(r.Context(), a.WorkspaceID, id)
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if v != nil && a.sees(&v.BrandID) {
			return v, true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "expert not found")
	return nil, false
}

func (s *Server) listPRExperts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), `SELECT `+prExpertCols+prExpertFrom+` WHERE x.workspace_id = $1`+sel.sql("x.brand_id", &args)+`
		ORDER BY x.is_default DESC, r.name, x.id`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (prExpertView, error) { return scanPRExpert(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []prExpertView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// cleanTopics trims, drops empty and repeated topics, or says what is
// wrong.
func cleanTopics(in []string) ([]string, string) {
	out := []string{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || slices.Contains(out, t) {
			continue
		}
		if len(t) > 100 {
			return nil, "each topic is at most 100 characters"
		}
		out = append(out, t)
	}
	if len(out) > 30 {
		return nil, "an expert has at most 30 topics"
	}
	return out, ""
}

// setDefaultExpert makes id its brand's only default expert.
func setDefaultExpert(ctx context.Context, tx pgx.Tx, ws, brand, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE pr_experts SET is_default = false, updated_at = now()
		WHERE workspace_id = $1 AND brand_id = $2 AND is_default AND id <> $3`, ws, brand, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE pr_experts SET is_default = true, updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, id)
	return err
}

func (s *Server) createPRExpert(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		BrandID   string   `json:"brand_id"`
		RecordID  string   `json:"record_id"`
		Name      string   `json:"name"`
		Email     string   `json:"email"`
		Title     string   `json:"title"`
		Topics    []string `json:"topics"`
		IsDefault *bool    `json:"is_default"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	ctx := r.Context()
	brand, err := s.brandIDFor(ctx, a.WorkspaceID, req.BrandID)
	if errors.Is(err, errNotFound) || (err == nil && !a.sees(&brand)) {
		bad("brand_id: brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(&brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	req.Title, req.Name, req.Email = strings.TrimSpace(req.Title), strings.TrimSpace(req.Name), strings.ToLower(strings.TrimSpace(req.Email))
	topics, msg := cleanTopics(req.Topics)
	switch {
	case msg != "":
		bad(msg)
		return
	case len(req.Title) > 200:
		bad("title is at most 200 characters")
		return
	case req.RecordID == "" && (req.Email == "" || !strings.Contains(req.Email, "@") || len(req.Email) > 320):
		bad("give record_id (a person record) or email (and name) for the expert")
		return
	case req.RecordID != "" && !validUUID(req.RecordID):
		bad("record_id is a person record's id")
		return
	case len(req.Name) > 200:
		bad("name is at most 200 characters")
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		record := req.RecordID
		if record != "" {
			var kind string
			err := tx.QueryRow(ctx, `SELECT record_type FROM records WHERE workspace_id = $1 AND id = $2 AND merged_into IS NULL`, a.WorkspaceID, record).Scan(&kind)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != "person") {
				return errInvalid("record_id is a person record in this workspace")
			}
			if err != nil {
				return err
			}
		} else {
			people, err := customers.EnsurePeople(ctx, tx, a.WorkspaceID, "manual", "pr_experts", []customers.Person{{Email: req.Email, Name: req.Name}})
			if err != nil {
				return err
			}
			record = people[req.Email]
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pr_experts WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, brand).Scan(&n); err != nil {
			return err
		}
		if n >= 50 {
			return errInvalid("a brand has at most 50 experts")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO pr_experts (workspace_id, brand_id, record_id, title, topics, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, a.WorkspaceID, brand, record, req.Title, topics, actorRef(a)).Scan(&id); err != nil {
			return err
		}
		// The brand's first expert is its default.
		if (req.IsDefault != nil && *req.IsDefault) || (req.IsDefault == nil && n == 0) {
			if err := setDefaultExpert(ctx, tx, a.WorkspaceID, brand, id); err != nil {
				return err
			}
		}
		data := map[string]any{"brand_id": brand, "record_id": record, "topics": len(topics)}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "pr_expert.created", "pr_expert", id, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadPRExpert(ctx, a.WorkspaceID, id)
	if err != nil || v == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updatePRExpert(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Title     *string   `json:"title"`
		Topics    *[]string `json:"topics"`
		IsDefault *bool     `json:"is_default"`
	}
	if !decode(w, r, &req) {
		return
	}
	v, ok := s.prExpertFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(&v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	title, topics := v.Title, v.Topics
	if req.Title != nil {
		if title = strings.TrimSpace(*req.Title); len(title) > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "title is at most 200 characters")
			return
		}
	}
	if req.Topics != nil {
		var msg string
		if topics, msg = cleanTopics(*req.Topics); msg != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
			return
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pr_experts SET title = $3, topics = $4, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, v.ID, title, topics); err != nil {
			return err
		}
		if req.IsDefault != nil && *req.IsDefault != v.IsDefault {
			if *req.IsDefault {
				if err := setDefaultExpert(ctx, tx, a.WorkspaceID, v.BrandID, v.ID); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, `UPDATE pr_experts SET is_default = false WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
				return err
			}
		}
		data := map[string]any{"brand_id": v.BrandID, "record_id": v.RecordID, "topics": len(topics), "is_default_set": req.IsDefault != nil}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "pr_expert.updated", "pr_expert", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err = s.loadPRExpert(ctx, a.WorkspaceID, v.ID)
	if err != nil || v == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deletePRExpert(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.prExpertFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(&v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Their quotes go with them, and stories that used one need a new
		// check and approval.
		if _, err := tx.Exec(ctx, `UPDATE stories SET status = 'draft', approved_by = NULL, approved_at = NULL, updated_at = now()
			WHERE workspace_id = $1 AND status = 'approved' AND id IN (SELECT story_id FROM story_quotes WHERE expert_id = $2)`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pr_experts WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "record_id": v.RecordID}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "pr_expert.deleted", "pr_expert", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Stories ----

type storyFactView struct {
	ID         string    `json:"id"`
	Statement  string    `json:"statement"`
	SourceKind string    `json:"source_kind"`
	SourceURL  string    `json:"source_url"`
	SourceNote string    `json:"source_note"`
	CreatedAt  time.Time `json:"created_at"`
}

type storyQuoteView struct {
	ID           string     `json:"id"`
	ExpertID     string     `json:"expert_id"`
	Body         string     `json:"body"`
	Status       string     `json:"status"`
	ApprovalNote string     `json:"approval_note"`
	ApprovedAt   *time.Time `json:"approved_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type storyView struct {
	ID           string          `json:"id"`
	BrandID      string          `json:"brand_id"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Summary      string          `json:"summary"`
	Release      string          `json:"release"`
	ArticleID    *string         `json:"article_id"`
	Status       string          `json:"status"`
	EmbargoUntil *time.Time      `json:"embargo_until"`
	LastCheck    *stories.Result `json:"last_check"`
	CheckedAt    *time.Time      `json:"checked_at"`
	ApprovedAt   *time.Time      `json:"approved_at"`
	Source       string          `json:"source"`
	// Facts and quotes come with one story, not with a list.
	Facts     *[]storyFactView  `json:"facts,omitempty"`
	Quotes    *[]storyQuoteView `json:"quotes,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

const storyCols = `s.id::text, s.brand_id::text, s.kind, s.title, s.summary, s.release, s.article_id::text, s.status, s.embargo_until,
	s.last_check, s.checked_at, s.approved_at, s.source, s.created_at, s.updated_at`

func scanStory(row pgx.Row) (storyView, error) {
	var v storyView
	err := row.Scan(&v.ID, &v.BrandID, &v.Kind, &v.Title, &v.Summary, &v.Release, &v.ArticleID, &v.Status, &v.EmbargoUntil,
		&v.LastCheck, &v.CheckedAt, &v.ApprovedAt, &v.Source, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// loadStory loads a story with its facts and quotes.
func (s *Server) loadStory(ctx context.Context, ws, id string) (*storyView, error) {
	v, err := scanStory(s.pool.QueryRow(ctx, `SELECT `+storyCols+` FROM stories s WHERE s.workspace_id = $1 AND s.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, statement, source_kind, source_url, source_note, created_at FROM story_facts
		WHERE workspace_id = $1 AND story_id = $2 ORDER BY created_at, id`, ws, id)
	if err != nil {
		return nil, err
	}
	facts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storyFactView, error) {
		var f storyFactView
		err := row.Scan(&f.ID, &f.Statement, &f.SourceKind, &f.SourceURL, &f.SourceNote, &f.CreatedAt)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT id::text, expert_id::text, body, status, approval_note, approved_at, created_at, updated_at FROM story_quotes
		WHERE workspace_id = $1 AND story_id = $2 ORDER BY created_at, id`, ws, id)
	if err != nil {
		return nil, err
	}
	quotes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storyQuoteView, error) {
		var q storyQuoteView
		err := row.Scan(&q.ID, &q.ExpertID, &q.Body, &q.Status, &q.ApprovalNote, &q.ApprovedAt, &q.CreatedAt, &q.UpdatedAt)
		return q, err
	})
	if err != nil {
		return nil, err
	}
	if facts == nil {
		facts = []storyFactView{}
	}
	if quotes == nil {
		quotes = []storyQuoteView{}
	}
	v.Facts, v.Quotes = &facts, &quotes
	return &v, nil
}

func (s *Server) storyFor(w http.ResponseWriter, r *http.Request, a *access) (*storyView, bool) {
	if id := r.PathValue("story"); validUUID(id) {
		v, err := s.loadStory(r.Context(), a.WorkspaceID, id)
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if v != nil && a.sees(&v.BrandID) {
			return v, true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "story not found")
	return nil, false
}

// storyWriter loads the story a write is for, or answers.
func (s *Server) storyWriter(w http.ResponseWriter, r *http.Request, a *access) (*storyView, bool) {
	v, ok := s.storyFor(w, r, a)
	if !ok {
		return nil, false
	}
	if !a.writes(&v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, false
	}
	return v, true
}

// storyChanged moves an approved story back to draft after a change to
// what was approved.
func storyChanged(ctx context.Context, tx pgx.Tx, ws, id string) error {
	_, err := tx.Exec(ctx, `UPDATE stories SET status = CASE WHEN status = 'approved' THEN 'draft' ELSE status END,
		approved_by = NULL, approved_at = NULL, updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, id)
	return err
}

func (s *Server) listStories(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	where := `s.workspace_id = $1`
	if v := r.URL.Query().Get("status"); v != "" {
		states := strings.Split(v, ",")
		for _, st := range states {
			if !slices.Contains(stories.Statuses, st) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "status is idea, draft, approved or archived, or several separated by commas")
				return
			}
		}
		args = append(args, states)
		where += ` AND s.status = ANY($` + strconv.Itoa(len(args)) + `)`
	} else {
		where += ` AND s.status <> 'archived'`
	}
	where += sel.sql("s.brand_id", &args)
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
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM stories s WHERE `+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, `SELECT `+storyCols+` FROM stories s WHERE `+where+` ORDER BY s.updated_at DESC, s.id
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (storyView, error) { return scanStory(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []storyView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": nextOffset(offset, len(list), total)})
}

func (s *Server) getStory(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.storyFor(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// storyIn is what a create or update sets; nil leaves a field as it is.
type storyIn struct {
	Kind         *string    `json:"kind"`
	Title        *string    `json:"title"`
	Summary      *string    `json:"summary"`
	Release      *string    `json:"release"`
	ArticleID    *string    `json:"article_id"`
	EmbargoUntil *time.Time `json:"embargo_until"`
}

// apply checks in and sets it on v, or says what is wrong.
func (in storyIn) apply(v *storyView) string {
	if in.Kind != nil {
		v.Kind = *in.Kind
	}
	if in.Title != nil {
		v.Title = strings.TrimSpace(*in.Title)
	}
	if in.Summary != nil {
		v.Summary = strings.TrimSpace(*in.Summary)
	}
	if in.Release != nil {
		v.Release = strings.TrimSpace(*in.Release)
	}
	if in.ArticleID != nil {
		v.ArticleID = in.ArticleID
		if *in.ArticleID == "" {
			v.ArticleID = nil
		}
	}
	if in.EmbargoUntil != nil {
		v.EmbargoUntil = in.EmbargoUntil
	}
	switch {
	case !slices.Contains(stories.Kinds, v.Kind):
		return "kind is data, reactive or comment"
	case v.Title == "" || len(v.Title) > 300:
		return "title is 1 to 300 characters"
	case len(v.Summary) > 2000:
		return "summary is at most 2000 characters"
	case len(v.Release) > 20000:
		return "release is at most 20000 characters"
	case v.ArticleID != nil && !validUUID(*v.ArticleID):
		return "article_id is a news article's id"
	}
	return ""
}

// checkArticle reports whether the article is the workspace's.
func (s *Server) checkArticle(ctx context.Context, ws string, id *string) (bool, error) {
	if id == nil {
		return true, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM news_articles WHERE workspace_id = $1 AND id = $2)`, ws, *id).Scan(&ok)
	return ok, err
}

func (s *Server) createStory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		storyIn
		BrandID string `json:"brand_id"`
		Source  string `json:"source"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	ctx := r.Context()
	brand, err := s.brandIDFor(ctx, a.WorkspaceID, req.BrandID)
	if errors.Is(err, errNotFound) || (err == nil && !a.sees(&brand)) {
		bad("brand_id: brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(&brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	v := storyView{BrandID: brand, Kind: "data", Status: "idea"}
	if msg := req.storyIn.apply(&v); msg != "" {
		bad(msg)
		return
	}
	if v.Release != "" {
		v.Status = "draft"
	}
	if req.Source = strings.TrimSpace(req.Source); req.Source == "" {
		req.Source = "api"
		if a.ActorType == "human" {
			req.Source = "manual"
		}
	}
	if len(req.Source) > 100 {
		bad("source is at most 100 characters")
		return
	}
	if ok, err := s.checkArticle(ctx, a.WorkspaceID, v.ArticleID); err != nil || !ok {
		if err != nil {
			s.internal(w, r, err)
			return
		}
		bad("article_id: news article not found")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO stories (workspace_id, brand_id, kind, title, summary, release, article_id, status, embargo_until, source, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`, a.WorkspaceID, brand, v.Kind, v.Title, v.Summary, v.Release,
			v.ArticleID, v.Status, v.EmbargoUntil, req.Source, actorRef(a)).Scan(&v.ID); err != nil {
			return err
		}
		data := map[string]any{"brand_id": brand, "kind": v.Kind, "status": v.Status, "article_id": v.ArticleID}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story.created", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	created, err := s.loadStory(ctx, a.WorkspaceID, v.ID)
	if err != nil || created == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateStory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		storyIn
		Status *string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	was := *v
	if msg := req.storyIn.apply(v); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	// status moves a story between idea, draft and archived; approving is
	// its own call.
	if req.Status != nil {
		if *req.Status == "approved" || !slices.Contains(stories.Statuses, *req.Status) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is idea, draft or archived; approve with POST .../approve")
			return
		}
		v.Status = *req.Status
	}
	ctx := r.Context()
	if ok, err := s.checkArticle(ctx, a.WorkspaceID, v.ArticleID); err != nil || !ok {
		if err != nil {
			s.internal(w, r, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, "invalid_request", "article_id: news article not found")
		return
	}
	// A change to what was approved takes the approval away.
	changed := v.Release != was.Release || v.Title != was.Title || v.Kind != was.Kind || !sameTime(v.EmbargoUntil, was.EmbargoUntil)
	if was.Status == "approved" && req.Status == nil && changed {
		v.Status = "draft"
	}
	if v.Status == "idea" && v.Release != "" && was.Release == "" && req.Status == nil {
		v.Status = "draft"
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE stories SET kind = $3, title = $4, summary = $5, release = $6, article_id = $7, embargo_until = $8, status = $9,
				approved_by = CASE WHEN $9 = 'approved' THEN approved_by END, approved_at = CASE WHEN $9 = 'approved' THEN approved_at END,
				updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID, v.Kind, v.Title, v.Summary, v.Release, v.ArticleID, v.EmbargoUntil, v.Status); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "kind": v.Kind, "status": v.Status, "was": was.Status, "release_changed": v.Release != was.Release}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story.updated", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err = s.loadStory(ctx, a.WorkspaceID, v.ID)
	if err != nil || v == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (s *Server) deleteStory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM stories WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "kind": v.Kind, "status": v.Status}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story.deleted", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Facts ----

func (s *Server) addStoryFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Statement  string `json:"statement"`
		SourceKind string `json:"source_kind"`
		SourceURL  string `json:"source_url"`
		SourceNote string `json:"source_note"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	req.Statement, req.SourceURL, req.SourceNote = strings.TrimSpace(req.Statement), strings.TrimSpace(req.SourceURL), strings.TrimSpace(req.SourceNote)
	switch {
	case req.Statement == "" || len(req.Statement) > 1000:
		bad("statement is 1 to 1000 characters")
		return
	case !slices.Contains(stories.SourceKinds, req.SourceKind):
		bad("source_kind is " + strings.Join(stories.SourceKinds, ", "))
		return
	case len(req.SourceURL) > 2048 || (req.SourceURL != "" && !strings.HasPrefix(req.SourceURL, "https://") && !strings.HasPrefix(req.SourceURL, "http://")):
		bad("source_url is a web address, or empty")
		return
	case len(req.SourceNote) > 1000:
		bad("source_note is at most 1000 characters")
		return
	}
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM story_facts WHERE story_id = $1`, v.ID).Scan(&n); err != nil {
			return err
		}
		if n >= 100 {
			return errInvalid("a story has at most 100 facts")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO story_facts (story_id, workspace_id, statement, source_kind, source_url, source_note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, v.ID, a.WorkspaceID, req.Statement, req.SourceKind, req.SourceURL, req.SourceNote,
			actorRef(a)).Scan(&id); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "story_id": v.ID, "source_kind": req.SourceKind, "sourced": req.SourceURL != "" || req.SourceNote != ""}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story_fact.added", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeStory(w, r, a, v.ID, http.StatusCreated)
}

func (s *Server) deleteStoryFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	fact := r.PathValue("fact")
	if !validUUID(fact) || !slices.ContainsFunc(*v.Facts, func(f storyFactView) bool { return f.ID == fact }) {
		writeError(w, r, http.StatusNotFound, "not_found", "fact not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM story_facts WHERE workspace_id = $1 AND story_id = $2 AND id = $3`, a.WorkspaceID, v.ID, fact); err != nil {
			return err
		}
		if err := storyChanged(ctx, tx, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "story_id": v.ID}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story_fact.removed", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeStory(w, r, a, v.ID, http.StatusOK)
}

// writeStory answers with the story as it is now.
func (s *Server) writeStory(w http.ResponseWriter, r *http.Request, a *access, id string, status int) {
	v, err := s.loadStory(r.Context(), a.WorkspaceID, id)
	if err != nil || v == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, status, v)
}

// ---- Quotes ----

func (s *Server) addStoryQuote(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		ExpertID string `json:"expert_id"`
		Body     string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Body = strings.TrimSpace(req.Body); req.Body == "" || len(req.Body) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is 1 to 2000 characters")
		return
	}
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	// No expert_id: the brand's default expert.
	var expert string
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM pr_experts WHERE workspace_id = $1 AND brand_id = $2
		AND ((NULLIF($3, '') IS NULL AND is_default) OR id::text = $3)`, a.WorkspaceID, v.BrandID, req.ExpertID).Scan(&expert)
	if errors.Is(err, pgx.ErrNoRows) {
		msg := "expert_id: an expert of the story's brand"
		if req.ExpertID == "" {
			msg = "the brand has no default expert; add one under PR experts, or give expert_id"
		}
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO story_quotes (story_id, workspace_id, expert_id, body, created_by) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, v.ID, a.WorkspaceID, expert, req.Body, actorRef(a)).Scan(&id); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "story_id": v.ID, "quote_id": id, "expert_id": expert}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story_quote.added", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeStory(w, r, a, v.ID, http.StatusCreated)
}

// updateStoryQuote changes a quote's words (which takes its approval
// away), or records the expert's approval or withdrawal. Only a person can
// record an approval: it says the expert agreed to these exact words.
func (s *Server) updateStoryQuote(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Body         *string `json:"body"`
		Status       *string `json:"status"`
		ApprovalNote *string `json:"approval_note"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if req.Body == nil && req.Status == nil && req.ApprovalNote == nil {
		bad("set body, status or approval_note")
		return
	}
	if req.Body != nil {
		if *req.Body = strings.TrimSpace(*req.Body); *req.Body == "" || len(*req.Body) > 2000 {
			bad("body is 1 to 2000 characters")
			return
		}
	}
	if req.Status != nil && !slices.Contains(stories.QuoteStates, *req.Status) {
		bad("status is draft, approved or withdrawn")
		return
	}
	if req.Body != nil && req.Status != nil && *req.Status == "approved" {
		bad("change the words and approve them in two steps, so the approval is for the words the expert saw")
		return
	}
	if req.ApprovalNote != nil && len(strings.TrimSpace(*req.ApprovalNote)) > 500 {
		bad("approval_note is at most 500 characters")
		return
	}
	if req.Status != nil && *req.Status == "approved" && a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person can record that an expert approved a quote, in Outflare under Stories")
		return
	}
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	id := r.PathValue("quote")
	i := slices.IndexFunc(*v.Quotes, func(q storyQuoteView) bool { return q.ID == id })
	if !validUUID(id) || i < 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "quote not found")
		return
	}
	q := (*v.Quotes)[i]
	status, body, note := q.Status, q.Body, q.ApprovalNote
	if req.Body != nil && *req.Body != q.Body {
		body, status = *req.Body, "draft"
	}
	if req.Status != nil {
		status = *req.Status
	}
	if req.ApprovalNote != nil {
		note = strings.TrimSpace(*req.ApprovalNote)
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE story_quotes SET body = $3, status = $4, approval_note = $5,
				approved_by = CASE WHEN $4 = 'approved' THEN CASE WHEN status = 'approved' THEN approved_by ELSE $6 END END,
				approved_at = CASE WHEN $4 = 'approved' THEN CASE WHEN status = 'approved' THEN approved_at ELSE now() END END,
				updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, q.ID, body, status, note, actorRef(a)); err != nil {
			return err
		}
		// The story's approval covered the old words.
		if body != q.Body || (q.Status == "approved" && status != "approved") {
			if err := storyChanged(ctx, tx, a.WorkspaceID, v.ID); err != nil {
				return err
			}
		}
		action := "story_quote.updated"
		if status != q.Status {
			action = "story_quote." + status
			if status == "draft" {
				action = "story_quote.reopened"
			}
		}
		data := map[string]any{"brand_id": v.BrandID, "story_id": v.ID, "quote_id": q.ID, "expert_id": q.ExpertID, "status": status, "was": q.Status,
			"body_changed": body != q.Body}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeStory(w, r, a, v.ID, http.StatusOK)
}

// ---- The check and approval ----

// runStoryCheck checks a story and keeps the result on it.
func (s *Server) runStoryCheck(ctx context.Context, q pgx.Tx, ws string, v *storyView) (stories.Result, error) {
	facts := make([]stories.Fact, len(*v.Facts))
	for i, f := range *v.Facts {
		facts[i] = stories.Fact{Statement: f.Statement, SourceURL: f.SourceURL, SourceNote: f.SourceNote}
	}
	quotes := make([]stories.Quote, len(*v.Quotes))
	for i, x := range *v.Quotes {
		quotes[i] = stories.Quote{Body: x.Body, Approved: x.Status == "approved"}
	}
	res := stories.Check(v.Release, facts, quotes)
	_, err := q.Exec(ctx, `UPDATE stories SET last_check = $3, checked_at = now() WHERE workspace_id = $1 AND id = $2`, ws, v.ID, res)
	return res, err
}

func (s *Server) checkStory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.storyFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	var res stories.Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		res, err = s.runStoryCheck(ctx, tx, a.WorkspaceID, v)
		if err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "ok": res.OK, "errors": len(res.Errors), "warnings": len(res.Warnings)}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story.checked", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) approveStory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person can approve a story's release, in Outflare under Stories")
		return
	}
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	if v.Status == "archived" {
		writeError(w, r, http.StatusConflict, "conflict", "the story is archived; move it back to draft first")
		return
	}
	ctx := r.Context()
	var res stories.Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if res, err = s.runStoryCheck(ctx, tx, a.WorkspaceID, v); err != nil || !res.OK {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE stories SET status = 'approved', approved_by = $3, approved_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID, actorRef(a)); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "kind": v.Kind, "facts": len(*v.Facts), "quotes": len(*v.Quotes), "warnings": len(res.Warnings)}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story.approved", "story", v.ID, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	if !res.OK {
		codes := make([]string, 0, len(res.Errors))
		for _, p := range res.Errors {
			if !slices.Contains(codes, p.Code) {
				codes = append(codes, p.Code)
			}
		}
		writeError(w, r, http.StatusConflict, "check_failed", "the release didn't pass the check ("+strings.Join(codes, ", ")+"); see last_check on the story, fix it and approve again")
		return
	}
	s.writeStory(w, r, a, v.ID, http.StatusOK)
}
