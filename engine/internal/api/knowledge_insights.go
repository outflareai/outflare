package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// What the knowledge base holds and how it is doing: counts for the
// Knowledge overview, and the questions AI couldn't answer from it (the
// gaps worth writing), which someone can set aside once answered.

func (s *Server) routesKnowledgeInsights(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/stats", s.inBrand(read, s.getKnowledgeStats))
	mux.HandleFunc("GET /v1/workspaces/{ws}/knowledge/gaps", s.inBrand(read, s.listKnowledgeGaps))
	mux.HandleFunc("POST /v1/workspaces/{ws}/knowledge/gaps/dismiss", s.inBrand(manage, s.dismissKnowledgeGap))
}

func (s *Server) getKnowledgeStats(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	args := []any{a.WorkspaceID}
	filter := brand.sql("i.brand_id", &args)
	var out struct {
		Items       int `json:"items"`
		Articles    int `json:"articles"`
		Snippets    int `json:"snippets"`
		Live        int `json:"live"`
		Drafts      int `json:"drafts"`
		CheckDue    int `json:"check_due"`
		TeamAI      int `json:"team_ai"`
		CustomersAI int `json:"customers_ai"`
		HelpCentre  int `json:"help_centre"`
		Bin         int `json:"bin"`
		Folders     int `json:"folders"`
		Questions   int `json:"questions_30d"`
		Answered    int `json:"answered_30d"`
		Gaps        int `json:"gaps"`
	}
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE i.deleted_at IS NULL),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.kind = 'article'),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.kind = 'snippet'),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.status = 'published'),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.status = 'draft'),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.review_by <= current_date),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.status = 'published'),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.status = 'published' AND i.visibility IN ('public', 'customers_ai')),
		       count(*) FILTER (WHERE i.deleted_at IS NULL AND i.status = 'published' AND i.visibility = 'public'),
		       count(*) FILTER (WHERE i.deleted_at IS NOT NULL)
		FROM knowledge_items i WHERE i.workspace_id = $1`+filter, args...).Scan(
		&out.Items, &out.Articles, &out.Snippets, &out.Live, &out.Drafts, &out.CheckDue, &out.TeamAI, &out.CustomersAI, &out.HelpCentre, &out.Bin)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	args = []any{a.WorkspaceID}
	filter = brand.sql("c.brand_id", &args)
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_collections c WHERE c.workspace_id = $1`+filter, args...).Scan(&out.Folders); err != nil {
		s.internal(w, r, err)
		return
	}
	args = []any{a.WorkspaceID}
	filter = brand.sql("q.brand_id", &args)
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE q.created_at > now() - interval '30 days'),
		       count(*) FILTER (WHERE q.created_at > now() - interval '30 days' AND q.answered),
		       count(DISTINCT lower(btrim(q.question))) FILTER (WHERE NOT q.answered AND q.dismissed_at IS NULL AND q.created_at > now() - interval '90 days')
		FROM knowledge_questions q WHERE q.workspace_id = $1`+filter, args...).Scan(&out.Questions, &out.Answered, &out.Gaps); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type knowledgeGap struct {
	Question  string    `json:"question"`
	Asked     int       `json:"asked"`
	Customers int       `json:"customers"`
	LastAsked time.Time `json:"last_asked"`
}

// listKnowledgeGaps lists the questions of the last 90 days that the
// knowledge didn't answer and nobody set aside, the most asked first.
// The same question in other capitals or spacing counts once, shown
// the way it was written most often.
func (s *Server) listKnowledgeGaps(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	filter := brand.sql("q.brand_id", &args)
	rows, err := s.pool.Query(r.Context(), `
		SELECT mode() WITHIN GROUP (ORDER BY q.question), count(*)::int,
		       count(*) FILTER (WHERE q.audience = 'customers')::int, max(q.created_at)
		FROM knowledge_questions q
		WHERE q.workspace_id = $1 AND NOT q.answered AND q.dismissed_at IS NULL AND q.created_at > now() - interval '90 days'`+filter+`
		GROUP BY lower(btrim(q.question))
		ORDER BY count(*) DESC, max(q.created_at) DESC LIMIT 100`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (knowledgeGap, error) {
		var g knowledgeGap
		err := row.Scan(&g.Question, &g.Asked, &g.Customers, &g.LastAsked)
		return g, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []knowledgeGap{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// dismissKnowledgeGap sets an unanswered question aside, every time it
// was asked: someone wrote the answer, or it needs none.
func (s *Server) dismissKnowledgeGap(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Question string `json:"question"`
		// answered: someone wrote the answer. ignored: it needs none.
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" || utf8.RuneCountInString(in.Question) > 1000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "question is the unanswered question, as the list of gaps gives it")
		return
	}
	if in.Reason == "" {
		in.Reason = "ignored"
	}
	if in.Reason != "answered" && in.Reason != "ignored" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is answered (someone wrote the answer) or ignored (it needs none)")
		return
	}
	ctx := r.Context()
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE knowledge_questions SET dismissed_at = now()
			WHERE workspace_id = $1 AND NOT answered AND dismissed_at IS NULL AND lower(btrim(question)) = lower($2)
			  AND ($3::uuid[] IS NULL OR brand_id IS NULL OR brand_id = ANY($3::uuid[]))`, a.WorkspaceID, in.Question, a.Brands)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge_gap.dismissed", "", "",
			map[string]any{"question": in.Question, "reason": in.Reason, "times_asked": n},
			map[string]any{"reason": in.Reason, "times_asked": n})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": n})
}
