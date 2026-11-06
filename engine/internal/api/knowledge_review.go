package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// The knowledge review queue: answers Outflare found for questions the
// knowledge didn't cover (from a teammate's reply in support, the website,
// Slack or an agent), and items it noticed disagree. A person approves,
// edits or rejects each one; nothing reaches customers or any AI before.
// An approved answer becomes a knowledge item; an approved conflict
// unpublishes the items that were wrong. A rejected one isn't made again.

type knowledgeSuggestionView struct {
	ID          string     `json:"id"`
	BrandID     *string    `json:"brand_id"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Question    string     `json:"question"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Visibility  string     `json:"visibility"`
	Source      string     `json:"source"`
	SourceRef   string     `json:"source_ref"`
	SourceQuote string     `json:"source_quote"`
	ItemIDs     []string   `json:"item_ids"`
	Sensitive   bool       `json:"sensitive"`
	Reason      string     `json:"reason"`
	ItemID      *string    `json:"item_id"`
	DecidedAt   *time.Time `json:"decided_at"`
	CreatedAt   time.Time  `json:"created_at"`
	// conflict: the items that disagree, as they read now.
	Items []knowledgeConflictItem `json:"items"`
}

type knowledgeConflictItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Excerpt    string `json:"excerpt"`
	Visibility string `json:"visibility"`
	Status     string `json:"status"`
}

const knowledgeSuggestionSelect = `SELECT id::text, brand_id::text, kind, status, question, title, body, visibility, source, source_ref, source_quote,
	item_ids::text[], sensitive, reason, item_id::text, decided_at, created_at FROM knowledge_suggestions`

func scanKnowledgeSuggestion(row pgx.CollectableRow) (knowledgeSuggestionView, error) {
	var v knowledgeSuggestionView
	err := row.Scan(&v.ID, &v.BrandID, &v.Kind, &v.Status, &v.Question, &v.Title, &v.Body, &v.Visibility, &v.Source, &v.SourceRef, &v.SourceQuote,
		&v.ItemIDs, &v.Sensitive, &v.Reason, &v.ItemID, &v.DecidedAt, &v.CreatedAt)
	if v.ItemIDs == nil {
		v.ItemIDs = []string{}
	}
	v.Items = []knowledgeConflictItem{}
	return v, err
}

// sensitiveRe finds what a person should read twice before customers are
// told it: money, refunds, contracts, legal, security and personal data.
var sensitiveRe = regexp.MustCompile(`(?i)(\$|€|£|\bprice|\bpricing|\bcost|\brefund|\bdiscount|\bcancel|\bcontract|\binvoice|\blegal|\bterms\b|\bliabil|\bwarrant|\bgdpr|\bprivacy|\bsecurity|\bpassword|\bsoc ?2|\bhipaa|\bcompliance|\bdata (is )?stored)`)

// knowledgeSuggestion is one suggestion to file.
type knowledgeSuggestion struct {
	WorkspaceID string
	BrandID     *string
	Kind        string // answer or conflict
	Question    string
	Title, Body string
	Visibility  string
	Source      string
	SourceRef   string
	SourceQuote string
	ItemIDs     []string
	Sensitive   bool
	CreatedBy   string
}

// fileKnowledgeSuggestion adds a suggestion to the queue, once: the same
// one waiting already, or one a person rejected, is left alone. ok says
// whether it was added.
func fileKnowledgeSuggestion(ctx context.Context, tx pgx.Tx, s knowledgeSuggestion) (string, bool, error) {
	// Brands share no suggestions: one with no brand is the workspace's
	// first brand's.
	key := s.Kind + ":" + strings.ToLower(strings.Join(strings.Fields(s.Question), " "))
	if s.Kind == "conflict" {
		ids := slices.Clone(s.ItemIDs)
		slices.Sort(ids)
		key = "conflict:" + strings.Join(ids, ",")
	}
	key = truncate(key, 1200)
	if s.Visibility == "" {
		s.Visibility = "customers_ai"
	}
	if s.ItemIDs == nil {
		s.ItemIDs = []string{}
	}
	s.Sensitive = s.Sensitive || sensitiveRe.MatchString(s.Question+" "+s.Title+" "+s.Body)
	var rejected bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_suggestions WHERE workspace_id = $1 AND dedupe_key = $2 AND status = 'rejected')`,
		s.WorkspaceID, key).Scan(&rejected); err != nil || rejected {
		return "", false, err
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO knowledge_suggestions (workspace_id, brand_id, kind, question, title, body, visibility, source, source_ref, source_quote,
			item_ids, sensitive, dedupe_key, created_by)
		VALUES ($1, COALESCE($2::uuid, outflare_first_brand($1)), $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid[], $12, $13, $14)
		ON CONFLICT (workspace_id, dedupe_key) WHERE status = 'pending' DO NOTHING RETURNING id::text`,
		s.WorkspaceID, s.BrandID, s.Kind, truncate(s.Question, 1000), truncate(s.Title, 300), truncate(s.Body, 20000), s.Visibility, s.Source,
		truncate(s.SourceRef, 1000), truncate(s.SourceQuote, 4000), s.ItemIDs, s.Sensitive, key, nullIfEmpty(s.CreatedBy)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	metrics.KnowledgeSuggestion(s.Source, "created")
	// Tell the team once a day that answers are waiting for review.
	if _, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: s.WorkspaceID, BrandID: s.BrandID, Kind: alertReviewWaiting,
		DedupeKey: "knowledge-review:" + time.Now().UTC().Format(time.DateOnly), TargetType: "knowledge_suggestion", TargetID: id,
		Data: map[string]any{"kind": s.Kind, "source": s.Source}}); err != nil {
		return "", false, err
	}
	actor := "system"
	if s.CreatedBy != "" {
		actor = "agent"
	}
	return id, true, audit.Record(ctx, tx, audit.Event{WorkspaceID: s.WorkspaceID, ActorType: actor, Action: "knowledge.suggestion_created",
		TargetType: "knowledge_suggestion", TargetID: id,
		Data:  map[string]any{"kind": s.Kind, "source": s.Source, "sensitive": s.Sensitive, "items": len(s.ItemIDs)},
		Props: map[string]any{"kind": s.Kind, "source": s.Source, "sensitive": s.Sensitive}})
}

func (s *Server) routesKnowledgeReview(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	base := "/v1/workspaces/{ws}/knowledge/suggestions"
	mux.HandleFunc("GET "+base, s.inBrand(read, s.listKnowledgeSuggestions))
	mux.HandleFunc("POST "+base, s.inBrand(manage, s.createKnowledgeSuggestion))
	mux.HandleFunc("POST "+base+"/{id}/approve", s.inBrand(manage, s.approveKnowledgeSuggestion))
	mux.HandleFunc("POST "+base+"/{id}/reject", s.inBrand(manage, s.rejectKnowledgeSuggestion))
}

func (s *Server) listKnowledgeSuggestions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	status := qs.Get("status")
	if status == "" {
		status = "pending"
	}
	if !slices.Contains([]string{"pending", "approved", "rejected", "all"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is pending, approved, rejected or all")
		return
	}
	kind := qs.Get("kind")
	if kind != "" && kind != "answer" && kind != "conflict" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is answer or conflict")
		return
	}
	args := []any{a.WorkspaceID}
	where := ` WHERE workspace_id = $1` + brand.sql("brand_id", &args)
	countArgs := []any{a.WorkspaceID}
	if status != "all" {
		args = append(args, status)
		where += ` AND status = $` + itoa(len(args))
	}
	if kind != "" {
		args = append(args, kind)
		where += ` AND kind = $` + itoa(len(args))
	}
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, knowledgeSuggestionSelect+where+` ORDER BY sensitive DESC, created_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, scanKnowledgeSuggestion)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.loadConflictItems(ctx, a.WorkspaceID, list); err != nil {
		s.internal(w, r, err)
		return
	}
	var answers, conflicts int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'answer'), count(*) FILTER (WHERE kind = 'conflict')
		FROM knowledge_suggestions WHERE workspace_id = $1 AND status = 'pending'`+brand.sql("brand_id", &countArgs), countArgs...).
		Scan(&answers, &conflicts); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "counts": map[string]int{"answers": answers, "conflicts": conflicts}})
}

// loadConflictItems fills in the items each conflict is about.
func (s *Server) loadConflictItems(ctx context.Context, ws string, list []knowledgeSuggestionView) error {
	var ids []string
	for _, v := range list {
		ids = append(ids, v.ItemIDs...)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, title, body, visibility, status FROM knowledge_items
		WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL`, ws, ids)
	if err != nil {
		return err
	}
	byID := map[string]knowledgeConflictItem{}
	var it knowledgeConflictItem
	var body string
	if _, err := pgx.ForEachRow(rows, []any{&it.ID, &it.Title, &body, &it.Visibility, &it.Status}, func() error {
		it.Excerpt = helpExcerpt(body)
		byID[it.ID] = it
		return nil
	}); err != nil {
		return err
	}
	for i := range list {
		for _, id := range list[i].ItemIDs {
			if v, ok := byID[id]; ok {
				list[i].Items = append(list[i].Items, v)
			}
		}
	}
	return nil
}

// createKnowledgeSuggestion files an answer someone or something found
// (an agent reading Slack or the website, or a person), for review.
func (s *Server) createKnowledgeSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Question    string  `json:"question"`
		Title       string  `json:"title"`
		Body        string  `json:"body"`
		Visibility  string  `json:"visibility"`
		Source      string  `json:"source"`
		SourceRef   string  `json:"source_ref"`
		SourceQuote string  `json:"source_quote"`
		BrandID     *string `json:"brand_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Question, in.Title, in.Body = strings.TrimSpace(in.Question), strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	switch {
	case in.Question == "" || utf8.RuneCountInString(in.Question) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "question is the question it answers, 1 to 1,000 characters")
		return
	case in.Body == "" || utf8.RuneCountInString(in.Body) > 20000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is the proposed answer, 1 to 20,000 characters")
		return
	case utf8.RuneCountInString(in.Title) > 300 || utf8.RuneCountInString(in.SourceQuote) > 4000 || utf8.RuneCountInString(in.SourceRef) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title is at most 300 characters, source_quote 4,000 and source_ref 1,000")
		return
	case !slices.Contains([]string{"website", "slack", "agent", "person"}, in.Source):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is website, slack, agent or person")
		return
	case in.Visibility != "" && !slices.Contains(knowledgeVisibilities, in.Visibility):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "visibility is public, customers_ai or team")
		return
	}
	ctx := r.Context()
	var brand *string
	if in.BrandID != nil && *in.BrandID != "" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, *in.BrandID)
		if errors.Is(err, errNotFound) || err == nil && !a.writes(&id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id is not one of your brands")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	} else if a.Brands != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id: name one of your brands")
		return
	}
	if in.Title == "" {
		in.Title = in.Question
	}
	var id string
	var added bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, added, err = fileKnowledgeSuggestion(ctx, tx, knowledgeSuggestion{WorkspaceID: a.WorkspaceID, BrandID: brand, Kind: "answer",
			Question: in.Question, Title: in.Title, Body: in.Body, Visibility: in.Visibility, Source: in.Source, SourceRef: in.SourceRef,
			SourceQuote: in.SourceQuote, CreatedBy: a.ActorID})
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !added {
		writeError(w, r, http.StatusConflict, "already_suggested", "an answer to this question is already waiting for review, or a person rejected it")
		return
	}
	v, err := s.knowledgeSuggestion(ctx, a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// knowledgeSuggestion loads one the caller can see.
func (s *Server) knowledgeSuggestion(ctx context.Context, a *access, id string) (knowledgeSuggestionView, error) {
	if !validUUID(id) {
		return knowledgeSuggestionView{}, errNotFound
	}
	rows, err := s.pool.Query(ctx, knowledgeSuggestionSelect+` WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID)
	if err != nil {
		return knowledgeSuggestionView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanKnowledgeSuggestion)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !a.sees(v.BrandID) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	list := []knowledgeSuggestionView{v}
	err = s.loadConflictItems(ctx, a.WorkspaceID, list)
	return list[0], err
}

func (s *Server) suggestionError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no such suggestion")
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", inv.Error())
	case errors.Is(err, errSuggestionDecided):
		writeError(w, r, http.StatusConflict, "already_decided", "someone already approved or rejected this suggestion")
	default:
		s.internal(w, r, err)
	}
	return true
}

var errSuggestionDecided = errors.New("already decided")

// approveKnowledgeSuggestion puts an answer into the knowledge (as a
// person edited it), or settles a conflict by keeping one item and
// unpublishing the others.
func (s *Server) approveKnowledgeSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Title        *string `json:"title"`
		Body         *string `json:"body"`
		Visibility   *string `json:"visibility"`
		CollectionID *string `json:"collection_id"`
		Keep         *string `json:"keep_item_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	v, err := s.knowledgeSuggestion(ctx, a, r.PathValue("id"))
	if s.suggestionError(w, r, err) {
		return
	}
	if !a.writes(v.BrandID) && v.BrandID != nil {
		s.suggestionError(w, r, errNotFound)
		return
	}
	var itemID *string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM knowledge_suggestions WHERE id = $1 FOR UPDATE`, v.ID).Scan(&status); err != nil {
			return err
		}
		if status != "pending" {
			return errSuggestionDecided
		}
		data := map[string]any{"kind": v.Kind, "source": v.Source, "sensitive": v.Sensitive}
		switch v.Kind {
		case "answer":
			title, body, vis := v.Title, v.Body, v.Visibility
			if in.Title != nil {
				title = strings.TrimSpace(*in.Title)
			}
			if in.Body != nil {
				body = strings.TrimSpace(*in.Body)
			}
			if in.Visibility != nil {
				vis = *in.Visibility
			}
			if title == "" || utf8.RuneCountInString(title) > 300 || body == "" || utf8.RuneCountInString(body) > 20000 {
				return errInvalid("the answer needs a title of up to 300 characters and a body")
			}
			if !slices.Contains(knowledgeVisibilities, vis) {
				return errInvalid("visibility is public, customers_ai or team")
			}
			if in.CollectionID != nil {
				var ok bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_collections WHERE id::text = $1 AND workspace_id = $2)`,
					*in.CollectionID, a.WorkspaceID).Scan(&ok); err != nil {
					return err
				}
				if !ok {
					return errInvalid("collection_id is not a folder in this workspace")
				}
			}
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO knowledge_items (workspace_id, brand_id, collection_id, kind, title, body, visibility, status, created_by, updated_by)
				VALUES ($1, $2, $3, 'snippet', $4, $5, $6, 'published', $7, $7) RETURNING id::text`,
				a.WorkspaceID, v.BrandID, in.CollectionID, title, body, vis, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO knowledge_versions (workspace_id, item_id, version, title, body, created_by)
				SELECT workspace_id, id, version, title, body, created_by FROM knowledge_items WHERE id = $1`, id); err != nil {
				return err
			}
			if err := writeKnowledgeChunks(ctx, tx, id); err != nil {
				return err
			}
			// The question it answers is no longer a gap.
			if _, err := tx.Exec(ctx, `UPDATE knowledge_questions SET dismissed_at = now()
				WHERE workspace_id = $1 AND NOT answered AND dismissed_at IS NULL AND lower(btrim(question)) = lower(btrim($2))`,
				a.WorkspaceID, v.Question); err != nil {
				return err
			}
			itemID = &id
			data["item_id"], data["edited"] = id, title != v.Title || body != v.Body
		case "conflict":
			if in.Keep == nil || !slices.Contains(v.ItemIDs, *in.Keep) {
				return errInvalid("keep_item_id is the item that is right: one of item_ids")
			}
			others := []string{}
			for _, id := range v.ItemIDs {
				if id != *in.Keep {
					others = append(others, id)
				}
			}
			// The others stop being used: back to drafts, to fix or delete.
			if _, err := tx.Exec(ctx, `UPDATE knowledge_items SET status = 'draft', updated_at = now(), updated_by = $3
				WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, a.WorkspaceID, others, nullIfEmpty(a.ActorID)); err != nil {
				return err
			}
			itemID = in.Keep
			data["kept"], data["unpublished"] = *in.Keep, len(others)
		}
		if _, err := tx.Exec(ctx, `UPDATE knowledge_suggestions SET status = 'approved', decided_by = $2, decided_at = now(), item_id = $3, updated_at = now()
			WHERE id = $1`, v.ID, nullIfEmpty(a.ActorID), itemID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.suggestion_approved", "knowledge_suggestion", v.ID, data,
			map[string]any{"kind": v.Kind, "source": v.Source, "sensitive": v.Sensitive, "edited": data["edited"] == true})
	})
	if s.suggestionError(w, r, err) {
		return
	}
	metrics.KnowledgeSuggestion(v.Source, "approved")
	out, err := s.knowledgeSuggestion(ctx, a, v.ID)
	if s.suggestionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) rejectKnowledgeSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if utf8.RuneCountInString(in.Reason) > 1000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is at most 1,000 characters")
		return
	}
	ctx := r.Context()
	v, err := s.knowledgeSuggestion(ctx, a, r.PathValue("id"))
	if s.suggestionError(w, r, err) {
		return
	}
	if !a.writes(v.BrandID) && v.BrandID != nil {
		s.suggestionError(w, r, errNotFound)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE knowledge_suggestions SET status = 'rejected', decided_by = $2, decided_at = now(), reason = $3, updated_at = now()
			WHERE id = $1 AND status = 'pending'`, v.ID, nullIfEmpty(a.ActorID), strings.TrimSpace(in.Reason))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errSuggestionDecided
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "knowledge.suggestion_rejected", "knowledge_suggestion", v.ID,
			map[string]any{"kind": v.Kind, "source": v.Source, "reason": strings.TrimSpace(in.Reason)},
			map[string]any{"kind": v.Kind, "source": v.Source, "with_reason": strings.TrimSpace(in.Reason) != ""})
	})
	if s.suggestionError(w, r, err) {
		return
	}
	metrics.KnowledgeSuggestion(v.Source, "rejected")
	out, err := s.knowledgeSuggestion(ctx, a, v.ID)
	if s.suggestionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// alertReviewWaiting tells the team that suggested answers or conflicts
// wait for review in Knowledge: once a day at most, when the first one of
// the day is filed.
const alertReviewWaiting = "knowledge.review_waiting"

func init() {
	alerts.Register(alerts.Kind{ID: alertReviewWaiting, Title: "Answers waiting for review",
		Description: "Outflare found answers to questions your knowledge couldn't answer, or articles that disagree, and they wait for a person to approve them. At most once a day; it goes to the owners and admins.",
		Scope:       auth.ScopeWorkspaceRead, Describe: describeReviewWaiting})
}

func describeReviewWaiting(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var answers, conflicts int
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'answer'), count(*) FILTER (WHERE kind = 'conflict')
		FROM knowledge_suggestions WHERE workspace_id = $1 AND status = 'pending'`, a.WorkspaceID).Scan(&answers, &conflicts); err != nil {
		return alerts.View{}, err
	}
	v := alerts.View{Path: "knowledge", Title: "Answers are waiting for review"}
	switch {
	case answers > 0 && conflicts > 0:
		v.Text = itoa(answers) + " suggested answers and " + itoa(conflicts) + " conflicts to settle in Knowledge. Approved answers are used by support from then on."
	case conflicts > 0:
		v.Title, v.Text = "Articles disagree", itoa(conflicts)+" conflicts to settle in Knowledge, so answers don't say two different things."
	case answers > 0:
		v.Text = itoa(answers) + " suggested answers in Knowledge. Approved answers are used by support from then on."
	default:
		v.Title, v.Text = "Nothing waiting for review", "Someone has already gone through the suggestions in Knowledge."
	}
	return v, nil
}
