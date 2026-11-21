package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// A brand's own playbooks (docs/playbooks.md, "Your own playbooks"): a
// name and a purpose around rules, journeys, lists and signal types the
// brand already has, with the same home a community playbook gets: what
// is on, what each part has done and how its signals ended. A playbook
// owns none of its parts, and nothing runs because of it.

const (
	maxOwnPlaybooks     = 50
	maxOwnPlaybookParts = 50
)

type ownPlaybookPart struct {
	Kind string `json:"kind"`
	// ID is the rule's, journey's or list's id, or the signal type's key.
	ID string `json:"id"`
}

type ownPlaybook struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Parts are the things it groups that the brand still has.
	Parts   []homePart `json:"parts"`
	Signals struct {
		Open      int `json:"open"`
		Late      int `json:"late"`
		Won       int `json:"won"`
		NoChange  int `json:"no_change"`
		NotUseful int `json:"not_useful"`
	} `json:"signals"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ownPlaybookInput struct {
	Title   *string            `json:"title"`
	Summary *string            `json:"summary"`
	Parts   *[]ownPlaybookPart `json:"parts"`
}

func (s *Server) routesOwnPlaybooks(mux *http.ServeMux) {
	const base = "/v1/brands/{brand}/own-playbooks"
	mux.HandleFunc("GET "+base, s.onBrand(auth.ScopeCustomersRead, s.listOwnPlaybooks))
	mux.HandleFunc("POST "+base, s.onBrand(auth.ScopeCatalogManage, s.createOwnPlaybook))
	mux.HandleFunc("GET "+base+"/{id}", s.onBrand(auth.ScopeCustomersRead, s.getOwnPlaybook))
	mux.HandleFunc("GET "+base+"/{id}/lift", s.onBrand(auth.ScopeAnalyticsRead, s.getOwnPlaybookLift))
	mux.HandleFunc("PATCH "+base+"/{id}", s.onBrand(auth.ScopeCatalogManage, s.updateOwnPlaybook))
	mux.HandleFunc("DELETE "+base+"/{id}", s.onBrand(auth.ScopeCatalogManage, s.deleteOwnPlaybook))
}

// ownPlaybookHome fills in a playbook's parts as the brand has them now
// and the signals they raised. A part the brand no longer has is left
// out.
func ownPlaybookHome(r *http.Request, tx pgx.Tx, a *access, p *ownPlaybook, parts []ownPlaybookPart) error {
	ctx := r.Context()
	ws, brand := a.WorkspaceID, a.Brand
	ids := map[string][]string{}
	for _, part := range parts {
		ids[part.Kind] = append(ids[part.Kind], part.ID)
	}
	found := map[string]homePart{}
	add := func(kind, sql string) error {
		if len(ids[kind]) == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, sql, ws, brand, ids[kind])
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			part := homePart{Kind: kind}
			if err := rows.Scan(&part.Key, &part.ID, &part.Name, &part.Status, &part.Count); err != nil {
				return err
			}
			found[kind+" "+part.Key] = part
		}
		return rows.Err()
	}
	if err := add("rule", `SELECT x.id::text, x.id::text, x.name, x.status,
			(SELECT count(*)::int FROM rule_firings f WHERE f.workspace_id = x.workspace_id AND f.rule_id = x.id AND f.fired_at > now() - interval '30 days')
		FROM rules x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.id::text = ANY ($3) AND x.status <> 'archived'`); err != nil {
		return err
	}
	if err := add("journey", `SELECT x.id::text, x.id::text, x.name, x.status,
			(SELECT count(*)::int FROM journey_runs n WHERE n.workspace_id = x.workspace_id AND n.journey_id = x.id AND n.status = 'active')
		FROM journeys x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.id::text = ANY ($3) AND x.status <> 'archived'`); err != nil {
		return err
	}
	if err := add("smart_list", `SELECT x.id::text, x.id::text, x.name, x.kind,
			(SELECT count(*)::int FROM smart_list_members m WHERE m.workspace_id = x.workspace_id AND m.list_id = x.id)
		FROM smart_lists x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.id::text = ANY ($3)`); err != nil {
		return err
	}
	if err := add("signal_type", `SELECT t.key, NULL::text, t.label, CASE WHEN t.archived_at IS NULL THEN 'active' ELSE 'archived' END,
			(SELECT count(*)::int FROM signals g WHERE g.workspace_id = t.workspace_id AND g.brand_id = t.brand_id AND g.type = t.key AND g.status IN ('open', 'accepted'))
		FROM signal_types t WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.key = ANY ($3)`); err != nil {
		return err
	}
	if err := add("score", `SELECT x.id::text, x.id::text, x.label, x.status,
			(SELECT count(*)::int FROM records r WHERE r.workspace_id = x.workspace_id AND r.brand_id = x.brand_id AND r.record_type = x.record_type
				AND r.attributes->'scores' ? x.key AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.deleted_at IS NULL)
		FROM score_definitions x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.id::text = ANY ($3)`); err != nil {
		return err
	}
	if err := add("routing_rule", `SELECT x.id::text, x.id::text, x.name, x.status, LEAST(x.assigned, 2147483647)::int
		FROM routing_rules x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.id::text = ANY ($3)`); err != nil {
		return err
	}
	if err := add("email", `SELECT x.id::text, x.id::text, x.name, x.status,
			(SELECT count(*)::int FROM emails e WHERE e.workspace_id = x.workspace_id AND e.template_id = x.id AND e.created_at > now() - interval '30 days')
		FROM email_templates x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.id::text = ANY ($3)`); err != nil {
		return err
	}
	if err := add("metric", `SELECT x.id::text, x.id::text, x.name, x.status,
			(SELECT count(DISTINCT v.record_id)::int FROM metric_values v WHERE v.metric_id = x.id)
		FROM metric_definitions x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.id::text = ANY ($3)`); err != nil {
		return err
	}
	// In the order the playbook lists them.
	p.Parts = []homePart{}
	for _, part := range parts {
		if x, ok := found[part.Kind+" "+part.ID]; ok {
			p.Parts = append(p.Parts, x)
			delete(found, part.Kind+" "+part.ID)
		}
	}
	if len(ids["signal_type"]) == 0 && len(ids["journey"]) == 0 {
		return nil
	}
	return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE g.status IN ('open', 'accepted'))::int,
			count(*) FILTER (WHERE g.status IN ('open', 'accepted') AND g.due_at < now())::int,
			count(*) FILTER (WHERE g.outcome = 'won' AND g.closed_at > now() - interval '90 days')::int,
			count(*) FILTER (WHERE g.outcome = 'no_change' AND g.closed_at > now() - interval '90 days')::int,
			count(*) FILTER (WHERE g.outcome = 'not_useful' AND g.closed_at > now() - interval '90 days')::int
		FROM signals g WHERE g.workspace_id = $1 AND g.brand_id = $2 AND (g.type = ANY ($3) OR g.data->>'journey_id' = ANY ($4))`,
		ws, brand, nonNil(ids["signal_type"]), nonNil(ids["journey"])).Scan(&p.Signals.Open, &p.Signals.Late, &p.Signals.Won, &p.Signals.NoChange, &p.Signals.NotUseful)
}

func loadOwnPlaybooks(r *http.Request, tx pgx.Tx, a *access, id string) ([]ownPlaybook, error) {
	rows, err := tx.Query(r.Context(), `SELECT id::text, title, summary, parts, created_at, updated_at FROM own_playbooks
		WHERE workspace_id = $1 AND brand_id = $2 AND ($3 = '' OR id::text = $3) ORDER BY created_at, id`, a.WorkspaceID, a.Brand, id)
	if err != nil {
		return nil, err
	}
	type row struct {
		p     ownPlaybook
		parts []ownPlaybookPart
	}
	list, err := pgx.CollectRows(rows, func(x pgx.CollectableRow) (row, error) {
		var v row
		var raw []byte
		if err := x.Scan(&v.p.ID, &v.p.Title, &v.p.Summary, &raw, &v.p.CreatedAt, &v.p.UpdatedAt); err != nil {
			return v, err
		}
		return v, json.Unmarshal(raw, &v.parts)
	})
	if err != nil {
		return nil, err
	}
	out := make([]ownPlaybook, 0, len(list))
	for _, v := range list {
		if err := ownPlaybookHome(r, tx, a, &v.p, v.parts); err != nil {
			return nil, err
		}
		out = append(out, v.p)
	}
	return out, nil
}

func (s *Server) listOwnPlaybooks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var out []ownPlaybook
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		out, err = loadOwnPlaybooks(r, tx, a, "")
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) getOwnPlaybook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var out []ownPlaybook
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		var err error
		if out, err = loadOwnPlaybooks(r, tx, a, id); err == nil && len(out) == 0 {
			err = errNotFound
		}
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "this brand has no playbook of its own with that id")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out[0])
}

// checkOwnPlaybookParts says what is wrong with a playbook's parts, or
// nothing: every part is a rule, journey, list or signal type the brand
// has, listed once.
func checkOwnPlaybookParts(r *http.Request, tx pgx.Tx, a *access, parts []ownPlaybookPart) error {
	if len(parts) > maxOwnPlaybookParts {
		return errInvalid("a playbook groups at most 50 things")
	}
	seen := map[string]bool{}
	ids := map[string][]string{}
	for _, part := range parts {
		switch part.Kind {
		case "rule", "journey", "smart_list", "score", "routing_rule", "email", "metric":
			if !validUUID(part.ID) {
				return errInvalid("a " + strings.ReplaceAll(part.Kind, "_", " ") + " part's id is its id")
			}
		case "signal_type":
			if part.ID == "" || len(part.ID) > 80 {
				return errInvalid("a signal_type part's id is the type's key, such as churn_risk")
			}
		default:
			return errInvalid("a part's kind is rule, journey, smart_list, signal_type, score, routing_rule, email or metric")
		}
		if seen[part.Kind+" "+part.ID] {
			return errInvalid("each part is listed once")
		}
		seen[part.Kind+" "+part.ID] = true
		ids[part.Kind] = append(ids[part.Kind], part.ID)
	}
	for kind, sql := range map[string]string{
		"rule":         `SELECT count(*) FROM rules WHERE workspace_id = $1 AND brand_id = $2 AND id::text = ANY ($3) AND status <> 'archived'`,
		"journey":      `SELECT count(*) FROM journeys WHERE workspace_id = $1 AND brand_id = $2 AND id::text = ANY ($3) AND status <> 'archived'`,
		"smart_list":   `SELECT count(*) FROM smart_lists WHERE workspace_id = $1 AND brand_id = $2 AND id::text = ANY ($3)`,
		"signal_type":  `SELECT count(*) FROM signal_types WHERE workspace_id = $1 AND brand_id = $2 AND key = ANY ($3)`,
		"score":        `SELECT count(*) FROM score_definitions WHERE workspace_id = $1 AND brand_id = $2 AND id::text = ANY ($3) AND status <> 'archived'`,
		"routing_rule": `SELECT count(*) FROM routing_rules WHERE workspace_id = $1 AND brand_id = $2 AND id::text = ANY ($3)`,
		"email":        `SELECT count(*) FROM email_templates WHERE workspace_id = $1 AND brand_id = $2 AND id::text = ANY ($3) AND status <> 'archived'`,
		"metric":       `SELECT count(*) FROM metric_definitions WHERE workspace_id = $1 AND brand_id = $2 AND id::text = ANY ($3) AND status <> 'archived'`,
	} {
		if len(ids[kind]) == 0 {
			continue
		}
		var n int
		if err := tx.QueryRow(r.Context(), sql, a.WorkspaceID, a.Brand, ids[kind]).Scan(&n); err != nil {
			return err
		}
		if n != len(ids[kind]) {
			return errInvalid("every " + strings.ReplaceAll(kind, "_", " ") + " in a playbook must be one this brand has")
		}
	}
	return nil
}

func (s *Server) createOwnPlaybook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in ownPlaybookInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var out ownPlaybook
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		title, summary := "", ""
		if in.Title != nil {
			title = strings.TrimSpace(*in.Title)
		}
		if in.Summary != nil {
			summary = strings.TrimSpace(*in.Summary)
		}
		if title == "" || len(title) > 80 || len(summary) > 300 {
			return errInvalid("title is 1 to 80 characters and summary at most 300")
		}
		parts := []ownPlaybookPart{}
		if in.Parts != nil {
			parts = *in.Parts
		}
		if err := checkOwnPlaybookParts(r, tx, a, parts); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM own_playbooks WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxOwnPlaybooks {
			return errInvalid("a brand keeps at most 50 playbooks of its own; remove one first")
		}
		raw, _ := json.Marshal(parts)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO own_playbooks (workspace_id, brand_id, title, summary, parts, created_by) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
			a.WorkspaceID, a.Brand, title, summary, raw, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		list, err := loadOwnPlaybooks(r, tx, a, id)
		if err != nil {
			return err
		}
		out = list[0]
		return record(ctx, tx, &a.principal, a.WorkspaceID, "own_playbook.created", "own_playbook", id, map[string]any{"parts": len(parts)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateOwnPlaybook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in ownPlaybookInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var out ownPlaybook
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		var title, summary string
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT title, summary, parts FROM own_playbooks WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`,
			id, a.WorkspaceID, a.Brand).Scan(&title, &summary, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if in.Title != nil {
			title = strings.TrimSpace(*in.Title)
		}
		if in.Summary != nil {
			summary = strings.TrimSpace(*in.Summary)
		}
		if title == "" || len(title) > 80 || len(summary) > 300 {
			return errInvalid("title is 1 to 80 characters and summary at most 300")
		}
		if in.Parts != nil {
			if err := checkOwnPlaybookParts(r, tx, a, *in.Parts); err != nil {
				return err
			}
			raw, _ = json.Marshal(*in.Parts)
		}
		if _, err := tx.Exec(ctx, `UPDATE own_playbooks SET title = $4, summary = $5, parts = $6, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`,
			id, a.WorkspaceID, a.Brand, title, summary, raw); err != nil {
			return err
		}
		list, err := loadOwnPlaybooks(r, tx, a, id)
		if err != nil {
			return err
		}
		out = list[0]
		return record(ctx, tx, &a.principal, a.WorkspaceID, "own_playbook.updated", "own_playbook", id, map[string]any{"parts": len(out.Parts)})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "this brand has no playbook of its own with that id")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteOwnPlaybook removes the playbook and leaves every rule, journey,
// list and signal type it grouped as it is.
func (s *Server) deleteOwnPlaybook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		tag, err := tx.Exec(ctx, `DELETE FROM own_playbooks WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "own_playbook.deleted", "own_playbook", id, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "this brand has no playbook of its own with that id")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getOwnPlaybookLift answers "did it work?" for one of the brand's own
// playbooks: each journey in it that holds some people back at random is
// compared with the ones it held back (playbook_lift.go).
func (s *Server) getOwnPlaybookLift(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var raw []byte
	if validUUID(id) {
		err := s.pool.QueryRow(r.Context(), `SELECT parts FROM own_playbooks WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
	}
	if raw == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "this brand has no playbook of its own with that id")
		return
	}
	var parts []ownPlaybookPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		s.internal(w, r, err)
		return
	}
	ids := []string{}
	for _, part := range parts {
		if part.Kind == "journey" {
			ids = append(ids, part.ID)
		}
	}
	days, event, ok := liftQuery(w, r)
	if !ok {
		return
	}
	list, measured, err := s.journeysLift(r.Context(), a, `id::text = ANY ($3)`, ids, days, event)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"playbook_id": id, "days": days, "event": event, "measured": measured,
		"min_people": liftMinPeople, "journeys": list})
}
