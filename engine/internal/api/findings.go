package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Findings are what an agent, or a person, learned about a brand's
// marketing: "Comparison pages convert search visitors twice as well as
// guides", with the evidence it rests on, how sure it is, where it holds
// (a channel, an audience, an objective) and a date to review it. They
// are the third kind of memory the brand agent plan keeps apart: facts
// (context and memories), operational state (objectives, actions, runs)
// and learnings. Agents read findings as data and never as instructions;
// the brand's context carries the active ones so every agent reading the
// brand sees what was learned.

func (s *Server) routesFindings(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/brands/{brand}/findings", s.onBrand(read, s.listFindings))
	mux.HandleFunc("POST /v1/brands/{brand}/findings", s.onBrand(manage, s.createFinding))
	mux.HandleFunc("GET /v1/brands/{brand}/findings/{id}", s.onBrand(read, s.getFinding))
	mux.HandleFunc("PATCH /v1/brands/{brand}/findings/{id}", s.onBrand(manage, s.updateFinding))
}

var (
	findingConfidences  = []string{"low", "medium", "high"}
	findingEvidenceKind = []string{"metric", "action", "experiment", "observation"}
)

// maxFindingsPerBrand keeps a brand's findings a list worth reading.
const maxFindingsPerBrand = 500

// findingEvidence is one reason a finding is believed.
type findingEvidence struct {
	Kind string `json:"kind"`
	// What it points at: a metric name, an action id, an experiment id or
	// a page. Optional for an observation.
	Ref  string `json:"ref,omitempty"`
	Note string `json:"note"`
}

// findingScope is where a finding holds; each part is optional.
type findingScope struct {
	Channel     string `json:"channel,omitempty"`
	Audience    string `json:"audience,omitempty"`
	ObjectiveID string `json:"objective_id,omitempty"`
}

type findingView struct {
	ID            string            `json:"id"`
	BrandID       string            `json:"brand_id"`
	Text          string            `json:"text"`
	Evidence      []findingEvidence `json:"evidence"`
	Confidence    string            `json:"confidence"`
	Scope         findingScope      `json:"scope"`
	Status        string            `json:"status"`
	ReviewAt      time.Time         `json:"review_at"`
	Due           bool              `json:"due"`
	RetiredReason string            `json:"retired_reason"`
	RetiredAt     *time.Time        `json:"retired_at"`
	CreatedBy     *string           `json:"created_by"`
	CreatedByID   *string           `json:"created_by_id"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

const findingCols = `f.id, f.brand_id, f.text, f.evidence, f.confidence, f.scope, f.status, f.review_at, f.review_at <= now(),
	f.retired_reason, f.retired_at, ac.display_name, f.created_by, f.created_at, f.updated_at`

const findingFrom = `FROM brand_findings f LEFT JOIN actors ac ON ac.id = f.created_by`

func scanFinding(row pgx.CollectableRow) (findingView, error) {
	var v findingView
	var evidence, scope []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Text, &evidence, &v.Confidence, &scope, &v.Status, &v.ReviewAt, &v.Due,
		&v.RetiredReason, &v.RetiredAt, &v.CreatedBy, &v.CreatedByID, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(evidence, &v.Evidence); err != nil {
		return v, err
	}
	if v.Evidence == nil {
		v.Evidence = []findingEvidence{}
	}
	return v, json.Unmarshal(scope, &v.Scope)
}

// loadFindings lists a brand's findings, newest first, or the active ones
// nearest their review date first when active is set (what the brand's
// context carries).
func (s *Server) loadFindings(ctx context.Context, ws, brand string, active bool, limit int) ([]findingView, error) {
	order := `f.created_at DESC, f.id DESC`
	if active {
		order = `f.review_at, f.created_at DESC`
	}
	rows, err := s.pool.Query(ctx, `SELECT `+findingCols+` `+findingFrom+`
		WHERE f.workspace_id = $1 AND f.brand_id = $2 AND (NOT $3 OR f.status = 'active') ORDER BY `+order+` LIMIT $4`, ws, brand, active, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanFinding)
	if list == nil {
		list = []findingView{}
	}
	return list, err
}

func (s *Server) listFindings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	args := []any{a.WorkspaceID, a.Brand}
	where := `f.workspace_id = $1 AND f.brand_id = $2`
	if v := qs.Get("status"); v != "" {
		if v != "active" && v != "retired" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or retired")
			return
		}
		args = append(args, v)
		where += ` AND f.status = $` + strconv.Itoa(len(args))
	}
	if v := qs.Get("channel"); v != "" {
		args = append(args, v)
		where += ` AND f.scope->>'channel' = $` + strconv.Itoa(len(args))
	}
	if v := qs.Get("objective_id"); v != "" {
		args = append(args, v)
		where += ` AND f.scope->>'objective_id' = $` + strconv.Itoa(len(args))
	}
	if v := qs.Get("due"); v != "" {
		due, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "due is true or false")
			return
		}
		if due {
			where += ` AND f.status = 'active' AND f.review_at <= now()`
		}
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
	where += ` AND ($` + strconv.Itoa(len(args)) + `::timestamptz IS NULL OR f.created_at < $` + strconv.Itoa(len(args)) + `)`
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+findingCols+` `+findingFrom+` WHERE `+where+` ORDER BY f.created_at DESC, f.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanFinding)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []findingView{}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}

type findingInput struct {
	Text       *string            `json:"text"`
	Evidence   *[]findingEvidence `json:"evidence"`
	Confidence *string            `json:"confidence"`
	Scope      *findingScope      `json:"scope"`
	ReviewAt   *time.Time         `json:"review_at"`
	// Only active and retired; retiring takes retired_reason.
	Status        *string `json:"status"`
	RetiredReason *string `json:"retired_reason"`
}

func (in *findingInput) validate() string {
	if in.Text != nil {
		*in.Text = strings.TrimSpace(*in.Text)
		if *in.Text == "" || len(*in.Text) > 1000 {
			return "text must be 1 to 1000 characters: what was learned, in a sentence or two"
		}
	}
	if in.Evidence != nil {
		if len(*in.Evidence) == 0 || len(*in.Evidence) > 20 {
			return "evidence needs 1 to 20 items: a finding without evidence is a guess"
		}
		for i := range *in.Evidence {
			e := &(*in.Evidence)[i]
			if !slices.Contains(findingEvidenceKind, e.Kind) {
				return "evidence kind is metric, action, experiment or observation"
			}
			e.Ref, e.Note = strings.TrimSpace(e.Ref), strings.TrimSpace(e.Note)
			if e.Note == "" || len(e.Note) > 500 {
				return "each evidence item needs a note of 1 to 500 characters saying what it showed"
			}
			if len(e.Ref) > 200 {
				return "an evidence ref is at most 200 characters"
			}
		}
	}
	if in.Confidence != nil && !slices.Contains(findingConfidences, *in.Confidence) {
		return "confidence is low, medium or high"
	}
	if in.Scope != nil {
		for name, v := range map[string]*string{"channel": &in.Scope.Channel, "audience": &in.Scope.Audience, "objective_id": &in.Scope.ObjectiveID} {
			*v = strings.TrimSpace(*v)
			if len(*v) > 200 {
				return "scope." + name + " is at most 200 characters"
			}
		}
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "retired" {
		return "status is active or retired"
	}
	if in.RetiredReason != nil {
		*in.RetiredReason = strings.TrimSpace(*in.RetiredReason)
		if len(*in.RetiredReason) > 500 {
			return "retired_reason is at most 500 characters"
		}
	}
	return ""
}

func (s *Server) createFinding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req findingInput
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Text == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "text is required")
		return
	case req.Evidence == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "evidence is required: at least one metric, action, experiment or observation it rests on")
		return
	case req.Confidence == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "confidence is required: low, medium or high")
		return
	case req.Status != nil || req.RetiredReason != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new finding is active; retire it with a PATCH")
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	scope := findingScope{}
	if req.Scope != nil {
		scope = *req.Scope
	}
	// Without a review date, a finding is looked at again in 90 days.
	review := time.Now().Add(90 * 24 * time.Hour)
	if req.ReviewAt != nil {
		review = *req.ReviewAt
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_findings WHERE brand_id = $1 AND status = 'active'`, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxFindingsPerBrand {
			return errInvalid(fmt.Sprintf("the brand already has %d active findings, the most it keeps; retire ones that no longer hold first", maxFindingsPerBrand))
		}
		err := tx.QueryRow(ctx, `INSERT INTO brand_findings (workspace_id, brand_id, text, evidence, confidence, scope, review_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			a.WorkspaceID, a.Brand, *req.Text, *req.Evidence, *req.Confidence, scope, review, actorRef(a)).Scan(&id)
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "finding.recorded", "finding", id,
			map[string]any{"brand_id": a.Brand, "text": *req.Text, "confidence": *req.Confidence, "scope": scope, "evidence": len(*req.Evidence), "review_at": review},
			map[string]any{"brand_id": a.Brand, "confidence": *req.Confidence, "evidence": len(*req.Evidence)})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeFinding(w, r, a, id, http.StatusCreated)
}

func (s *Server) writeFinding(w http.ResponseWriter, r *http.Request, a *access, id string, status int) {
	rows, err := s.pool.Query(r.Context(), `SELECT `+findingCols+` `+findingFrom+` WHERE f.id = $1 AND f.brand_id = $2`, id, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, scanFinding)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(list) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "finding not found")
		return
	}
	writeJSON(w, status, list[0])
}

func (s *Server) getFinding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "finding not found")
		return
	}
	s.writeFinding(w, r, a, id, http.StatusOK)
}

func (s *Server) updateFinding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "finding not found")
		return
	}
	var req findingInput
	if !decode(w, r, &req) {
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if req.RetiredReason != nil && (req.Status == nil || *req.Status != "retired") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "retired_reason goes with status retired")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM brand_findings WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 FOR UPDATE`, id, a.Brand, a.WorkspaceID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		var scope *findingScope
		if req.Scope != nil {
			scope = req.Scope
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_findings SET
				text = COALESCE($2, text), evidence = COALESCE($3, evidence), confidence = COALESCE($4, confidence),
				scope = COALESCE($5, scope), review_at = COALESCE($6, review_at), updated_at = now()
			WHERE id = $1`, id, req.Text, req.Evidence, req.Confidence, scope, req.ReviewAt); err != nil {
			return err
		}
		changed := map[string]any{"brand_id": a.Brand}
		for k, set := range map[string]bool{"text": req.Text != nil, "evidence": req.Evidence != nil, "confidence": req.Confidence != nil, "scope": req.Scope != nil, "review_at": req.ReviewAt != nil} {
			if set {
				changed[k] = true
			}
		}
		action := "finding.updated"
		switch {
		case req.Status != nil && *req.Status == "retired" && status != "retired":
			reason := ""
			if req.RetiredReason != nil {
				reason = *req.RetiredReason
			}
			if _, err := tx.Exec(ctx, `UPDATE brand_findings SET status = 'retired', retired_reason = $2, retired_at = now() WHERE id = $1`, id, reason); err != nil {
				return err
			}
			action, changed["retired_reason"] = "finding.retired", reason
		case req.Status != nil && *req.Status == "active" && status == "retired":
			if _, err := tx.Exec(ctx, `UPDATE brand_findings SET status = 'active', retired_reason = '', retired_at = NULL WHERE id = $1`, id); err != nil {
				return err
			}
			changed["status"] = "active"
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "finding", id, changed, map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeFinding(w, r, a, id, http.StatusOK)
}
