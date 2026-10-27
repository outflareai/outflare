package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// The fixes list: one list of work for the whole workspace, shared by
// people and agents. Features that find work (site audits first) create
// and close fixes; people and agents take them, mark them done or ignore
// them. A fix's subject says what it is about; its source's rules say why
// it matters and how to fix it.

type fixAssignee struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type fixSubject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type fixView struct {
	ID          string       `json:"id"`
	Source      string       `json:"source"`
	Subject     fixSubject   `json:"subject"`
	BrandID     *string      `json:"brand_id"`
	Rule        crawl.Rule   `json:"rule"`
	Group       string       `json:"group"`
	Status      string       `json:"status"`
	Assignee    *fixAssignee `json:"assignee"`
	Note        string       `json:"note"`
	Count       int          `json:"count"`
	Unit        string       `json:"unit"`
	FoundBy     *string      `json:"found_by"`
	LastFoundBy *string      `json:"last_found_by"`
	ConfirmedBy *string      `json:"confirmed_by"`
	FixedAt     *time.Time   `json:"fixed_at"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// fixSources are the sources the fixes list holds.
var fixSources = []string{crawl.FixSource, seointel.FixSource, aivis.FixSource, socialFixSource, seosync.LinkFixSource}

// fixRules looks up a source's rule: why it matters and how to fix it.
var fixRules = map[string]func(string) (crawl.Rule, bool){
	crawl.FixSource:       crawl.RuleFor,
	seointel.FixSource:    seointel.RuleFor,
	aivis.FixSource:       aivis.RuleFor,
	seosync.LinkFixSource: seosync.LinkRuleFor,
	socialFixSource: func(rule string) (crawl.Rule, bool) {
		r, ok := socialFixRules[rule]
		return r, ok
	},
}

// socialFixRules are the social watches' rules (source social).
var socialFixRules = map[string]crawl.Rule{
	"social_items_waiting": {ID: "social_items_waiting", Severity: "notice", Category: "social", Title: "Posts waiting for a reply",
		Why: "A watch found posts where the brand may be able to help. They go stale within days.",
		Fix: "Open the watch's posts: draft a reply (yourself or with your AI), or ignore the ones that don't fit."},
	"social_actions_to_approve": {ID: "social_actions_to_approve", Severity: "warning", Category: "social", Title: "Replies waiting for approval",
		Why: "Drafted replies are only posted once a person approves each one.",
		Fix: "Read each draft, edit it if needed, then approve or reject it."},
}

var fixStatuses = []string{"open", "in_progress", "done", "ignored"}

const fixCols = `f.id::text, f.source, f.subject_type, f.subject_id::text, f.brand_id::text, f.rule, f.severity, f.group_key, f.status,
	a.id::text, a.display_name, a.type, f.note, f.count, f.unit, f.found_by::text, f.last_found_by::text, f.confirmed_by::text,
	f.fixed_at, f.created_at, f.updated_at`

func scanFix(row pgx.Row) (fixView, error) {
	var v fixView
	var rule, sev string
	var aID, aName, aType *string
	err := row.Scan(&v.ID, &v.Source, &v.Subject.Type, &v.Subject.ID, &v.BrandID, &rule, &sev, &v.Group, &v.Status,
		&aID, &aName, &aType, &v.Note, &v.Count, &v.Unit, &v.FoundBy, &v.LastFoundBy, &v.ConfirmedBy,
		&v.FixedAt, &v.CreatedAt, &v.UpdatedAt)
	if def, ok := ruleFor(v.Source, rule); ok {
		v.Rule = def
	} else {
		v.Rule = crawl.Rule{ID: rule, Severity: sev}
	}
	if aID != nil {
		v.Assignee = &fixAssignee{ID: *aID, Name: *aName, Type: *aType}
	}
	return v, err
}

func ruleFor(source, rule string) (crawl.Rule, bool) {
	if f := fixRules[source]; f != nil {
		return f(rule)
	}
	return crawl.Rule{}, false
}

func (s *Server) listFixes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status, sev, source, subject := q.Get("status"), q.Get("severity"), q.Get("source"), q.Get("subject_id")
	if source != "" && !slices.Contains(fixSources, source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is "+strings.Join(fixSources, ", "))
		return
	}
	if subject != "" && !validUUID(subject) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "subject_id is an id")
		return
	}
	if status != "" && !slices.Contains(fixStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(fixStatuses, ", "))
		return
	}
	if _, ok := issueSeverityRank[sev]; sev != "" && !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "severity is error, warning or notice")
		return
	}
	args := []any{a.WorkspaceID, status, sev, source, subject}
	rows, err := s.pool.Query(r.Context(), `SELECT `+fixCols+`
		FROM fixes f LEFT JOIN actors a ON a.id = f.assignee_id
		WHERE f.workspace_id = $1 AND ($2 = '' OR f.status = $2) AND ($3 = '' OR f.severity = $3) AND ($4 = '' OR f.source = $4)
		  AND ($5 = '' OR f.subject_id = NULLIF($5, '')::uuid)`+sel.sql("f.brand_id", &args)+`
		ORDER BY CASE f.status WHEN 'in_progress' THEN 0 WHEN 'open' THEN 1 WHEN 'done' THEN 2 ELSE 3 END,
		         CASE f.severity WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, f.count DESC, f.rule, f.group_key
		LIMIT 1000`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (fixView, error) { return scanFix(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []fixView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// fixFor loads a fix the caller may see.
func (s *Server) fixFor(ctx context.Context, w http.ResponseWriter, r *http.Request, a *access) (*fixView, bool) {
	id := r.PathValue("fix")
	if validUUID(id) {
		v, err := scanFix(s.pool.QueryRow(ctx, `SELECT `+fixCols+`
			FROM fixes f LEFT JOIN actors a ON a.id = f.assignee_id
			WHERE f.id = $1 AND f.workspace_id = $2`, id, a.WorkspaceID))
		if err == nil && a.sees(nil, v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "fix not found")
	return nil, false
}

func (s *Server) getFix(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.fixFor(r.Context(), w, r, a); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) updateFix(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Status     *string          `json:"status"`
		AssigneeID optional[string] `json:"assignee_id"`
		Note       *string          `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	fix, ok := s.fixFor(ctx, w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, fix.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if req.Status != nil && !slices.Contains(fixStatuses, *req.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(fixStatuses, ", "))
		return
	}
	if req.Note != nil && len(*req.Note) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2000 characters")
		return
	}
	assignee := (*string)(nil)
	if fix.Assignee != nil {
		assignee = &fix.Assignee.ID
	}
	if req.AssigneeID.Set {
		assignee = req.AssigneeID.Value
		if assignee != nil && *assignee == "me" {
			assignee = &a.ActorID
		}
		if assignee != nil {
			var member bool
			if validUUID(*assignee) {
				if err := s.pool.QueryRow(ctx, `
					SELECT EXISTS (SELECT 1 FROM actors x WHERE x.id = $1 AND (x.workspace_id = $2 OR EXISTS (
						SELECT 1 FROM users u JOIN memberships m ON m.user_id = u.id WHERE u.actor_id = x.id AND m.workspace_id = $2)))`,
					*assignee, a.WorkspaceID).Scan(&member); err != nil {
					s.internal(w, r, err)
					return
				}
			}
			if !member {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "assignee_id is a member's or an agent's actor id in this workspace, or me")
				return
			}
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Someone marking a fix done clears what a crawl confirmed; the
		// next complete crawl checks it again.
		_, err := tx.Exec(ctx, `
			UPDATE fixes SET status = COALESCE($3, status), assignee_id = $4::uuid, note = COALESCE($5, note),
				confirmed_by = CASE WHEN $3::text IS NOT NULL AND $3 <> status THEN NULL ELSE confirmed_by END,
				fixed_at = CASE WHEN $3::text IS NOT NULL AND $3 <> status THEN NULL ELSE fixed_at END, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, fix.ID, a.WorkspaceID, req.Status, assignee, req.Note)
		if err != nil {
			return err
		}
		data := map[string]any{"source": fix.Source, "rule": fix.Rule.ID, "subject_type": fix.Subject.Type, "subject_id": fix.Subject.ID}
		if req.Status != nil {
			data["status"] = *req.Status
		}
		if req.AssigneeID.Set {
			data["assignee_id"] = assignee
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "fix.updated", "fix", fix.ID, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.fixFor(ctx, w, r, a); ok {
		writeJSON(w, http.StatusOK, v)
	}
}
