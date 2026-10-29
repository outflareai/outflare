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

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// Brand facts for AI visibility: what the workspace says is true about a
// brand. Each run checks the answers that name the brand against them
// (aivis.checkFacts), so the list shows how often AI gets each fact right
// or wrong, with the wrong words.

func (s *Server) routesAIFacts(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-facts", s.inBrand(read, s.listAIFacts))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ai-facts", s.inBrand(manage, s.createAIFact))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-facts/{fact}", s.inBrand(read, s.getAIFact))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/ai-facts/{fact}", s.inBrand(manage, s.updateAIFact))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/ai-facts/{fact}", s.inBrand(manage, s.deleteAIFact))
}

var factTopics = []string{"", "pricing", "product", "company", "availability", "integrations", "other"}

type aiFactWrong struct {
	SampleID  string    `json:"sample_id"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Quote     string    `json:"quote"`
	SampledAt time.Time `json:"sampled_at"`
}

type aiFactView struct {
	ID        string  `json:"id"`
	BrandID   *string `json:"brand_id"`
	Topic     string  `json:"topic"`
	Statement string  `json:"statement"`
	Status    string  `json:"status"`
	// Over the period asked for: answers that agree with the fact, answers
	// that contradict it, and agreeing answers out of both.
	Supported    int          `json:"supported"`
	Contradicted int          `json:"contradicted"`
	Accuracy     *float64     `json:"accuracy"`
	LatestWrong  *aiFactWrong `json:"latest_wrong"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

func (s *Server) loadAIFacts(ctx context.Context, ws string, id *string, sel brandSel, from time.Time) ([]aiFactView, error) {
	args := []any{ws, id, from}
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.brand_id::text, f.topic, f.statement, f.status,
		       count(k.sample_id) FILTER (WHERE k.verdict = 'supported'), count(k.sample_id) FILTER (WHERE k.verdict = 'contradicted'),
		       f.created_at, f.updated_at
		FROM ai_brand_facts f LEFT JOIN ai_fact_checks k ON k.fact_id = f.id AND k.sampled_at >= $3
		WHERE f.workspace_id = $1 AND ($2::uuid IS NULL OR f.id = $2)`+sel.sql("f.brand_id", &args)+`
		GROUP BY f.id ORDER BY f.created_at, f.id`, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (aiFactView, error) {
		var v aiFactView
		err := row.Scan(&v.ID, &v.BrandID, &v.Topic, &v.Statement, &v.Status, &v.Supported, &v.Contradicted, &v.CreatedAt, &v.UpdatedAt)
		v.Accuracy = rate(v.Supported, v.Supported+v.Contradicted)
		return v, err
	})
	if err != nil || len(list) == 0 {
		if list == nil {
			list = []aiFactView{}
		}
		return list, err
	}
	ids := make([]string, len(list))
	at := map[string]int{}
	for i := range list {
		ids[i], at[list[i].ID] = list[i].ID, i
	}
	rows, err = s.pool.Query(ctx, `
		SELECT DISTINCT ON (k.fact_id) k.fact_id, k.sample_id, s.provider, s.model, k.quote, k.sampled_at
		FROM ai_fact_checks k JOIN ai_samples s ON s.id = k.sample_id
		WHERE k.workspace_id = $1 AND k.fact_id = ANY($2) AND k.verdict = 'contradicted' AND k.sampled_at >= $3
		ORDER BY k.fact_id, k.sampled_at DESC, k.sample_id`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var fid string
	var wr aiFactWrong
	if _, err := pgx.ForEachRow(rows, []any{&fid, &wr.SampleID, &wr.Provider, &wr.Model, &wr.Quote, &wr.SampledAt}, func() error {
		w := wr
		list[at[fid]].LatestWrong = &w
		return nil
	}); err != nil {
		return nil, err
	}
	return list, nil
}

// factDays reads ?days= (1 to 365, default 28) as the start of the period.
func factDays(r *http.Request) (time.Time, bool) {
	days := 28
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			return time.Time{}, false
		}
		days = n
	}
	return time.Now().UTC().AddDate(0, 0, -days), true
}

func (s *Server) listAIFacts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	from, ok := factDays(r)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
		return
	}
	list, err := s.loadAIFacts(r.Context(), a.WorkspaceID, nil, sel, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// aiFactFor loads the {fact} path value's fact, or writes a 404.
func (s *Server) aiFactFor(w http.ResponseWriter, r *http.Request, a *access) (*aiFactView, bool) {
	id := r.PathValue("fact")
	if validUUID(id) {
		from, ok := factDays(r)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return nil, false
		}
		list, err := s.loadAIFacts(r.Context(), a.WorkspaceID, &id, brandSel{in: a.Brands}, from)
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if len(list) == 1 {
			return &list[0], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "fact not found")
	return nil, false
}

func (s *Server) getAIFact(w http.ResponseWriter, r *http.Request) {
	if f, ok := s.aiFactFor(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, f)
	}
}

type aiFactInput struct {
	BrandID   optional[string] `json:"brand_id"`
	Topic     *string          `json:"topic"`
	Statement *string          `json:"statement"`
	Status    *string          `json:"status"`
}

func (in *aiFactInput) check() error {
	if in.Statement != nil {
		v := strings.TrimSpace(*in.Statement)
		if !textOK(v, 5, 500) {
			return errInvalid("statement is 5 to 500 characters: one plain sentence, such as \"Plans start at $49 a month\"")
		}
		in.Statement = &v
	}
	if in.Topic != nil && !slices.Contains(factTopics, *in.Topic) {
		return errInvalid("topic is one of pricing, product, company, availability, integrations, other, or empty")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "paused" {
		return errInvalid("status is active or paused")
	}
	return nil
}

func (s *Server) createAIFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req aiFactInput
	if !decode(w, r, &req) {
		return
	}
	if req.Statement == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "statement is required")
		return
	}
	if err := req.check(); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var brand *string
		if req.BrandID.Set {
			b, err := s.brandRef(ctx, a.WorkspaceID, req.BrandID.Value)
			if err != nil {
				return err
			}
			brand = b
		}
		brand, err := brandWrite(ctx, tx, a, brand)
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ai_brand_facts WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid`,
			a.WorkspaceID, brand).Scan(&n); err != nil {
			return err
		}
		if n >= aivis.MaxFacts {
			return errInvalid("a brand has at most 50 facts; delete or merge some first")
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO ai_brand_facts (workspace_id, brand_id, topic, statement, status, created_by)
			VALUES ($1, $2, COALESCE($3, ''), $4, COALESCE($5, 'active'), $6) RETURNING id`,
			a.WorkspaceID, brand, req.Topic, *req.Statement, req.Status, a.ActorID).Scan(&id)
		if isUnique(err) {
			return errInvalid("this brand already has that fact")
		}
		if err != nil {
			return err
		}
		topic := ""
		if req.Topic != nil {
			topic = *req.Topic
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "ai_fact.created", "ai_fact", id, map[string]any{"brand_id": brand, "topic": topic})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadAIFacts(ctx, a.WorkspaceID, &id, brandSel{}, time.Now())
	if err != nil || len(list) != 1 {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) updateAIFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req aiFactInput
	if !decode(w, r, &req) {
		return
	}
	if req.BrandID.Set {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a fact's brand can't change; add the fact to the other brand instead")
		return
	}
	f, ok := s.aiFactFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(f.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if err := req.check(); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE ai_brand_facts SET topic = COALESCE($3, topic), statement = COALESCE($4, statement), status = COALESCE($5, status), updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, f.ID, a.WorkspaceID, req.Topic, req.Statement, req.Status)
		if isUnique(err) {
			return errInvalid("this brand already has that fact")
		}
		if err != nil {
			return err
		}
		// A changed statement is a new claim: what answers said about the
		// old one no longer applies.
		if req.Statement != nil && *req.Statement != f.Statement {
			if _, err := tx.Exec(ctx, `DELETE FROM ai_fact_checks WHERE fact_id = $1 AND workspace_id = $2`, f.ID, a.WorkspaceID); err != nil {
				return err
			}
		}
		var fields []string
		for name, set := range map[string]bool{"topic": req.Topic != nil, "statement": req.Statement != nil, "status": req.Status != nil} {
			if set {
				fields = append(fields, name)
			}
		}
		if len(fields) == 0 {
			return nil
		}
		slices.Sort(fields)
		return record(ctx, tx, &a.principal, a.WorkspaceID, "ai_fact.updated", "ai_fact", f.ID, map[string]any{"fields": fields, "brand_id": f.BrandID})
	})
	if s.allocError(w, r, err) {
		return
	}
	if f, ok := s.aiFactFor(w, r, a); ok {
		writeJSON(w, http.StatusOK, f)
	}
}

func (s *Server) deleteAIFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.aiFactFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(f.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM fixes WHERE workspace_id = $1 AND source = $3 AND subject_id = $2`, a.WorkspaceID, f.ID, aivis.FixSource); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ai_brand_facts WHERE id = $1 AND workspace_id = $2`, f.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "ai_fact.deleted", "ai_fact", f.ID, map[string]any{"brand_id": f.BrandID})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
