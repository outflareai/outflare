package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
)

func (s *Server) routesCRM(mux *http.ServeMux) {
	s.routesCRMMail(mux)
	base := "/v1/brands/{brand}/crm/"
	mux.HandleFunc("GET "+base+"summary", s.onBrand(auth.ScopeCustomersRead, s.crmSummary))
	for _, resource := range []string{"activities", "views"} {
		mux.HandleFunc("GET "+base+resource, s.onBrand(auth.ScopeCustomersRead, s.listCRM))
		mux.HandleFunc("POST "+base+resource, s.onBrand(auth.ScopeCustomersManage, s.createCRM))
		mux.HandleFunc("PATCH "+base+resource+"/{id}", s.onBrand(auth.ScopeCustomersManage, s.updateCRM))
		mux.HandleFunc("DELETE "+base+resource+"/{id}", s.onBrand(auth.ScopeCustomersManage, s.deleteCRM))
	}
}

type crmItem struct {
	ID         string          `json:"id"`
	BrandID    string          `json:"brand_id"`
	RecordID   string          `json:"record_id,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Title      string          `json:"title,omitempty"`
	Body       string          `json:"body,omitempty"`
	Status     string          `json:"status,omitempty"`
	Priority   string          `json:"priority,omitempty"`
	AssignedTo *string         `json:"assigned_to,omitempty"`
	DueAt      *time.Time      `json:"due_at,omitempty"`
	Name       string          `json:"name,omitempty"`
	RecordType string          `json:"record_type,omitempty"`
	Shared     bool            `json:"shared"`
	Config     json.RawMessage `json:"config,omitempty"`
	CreatedBy  *string         `json:"created_by"`
	Version    int             `json:"version"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type crmInput struct {
	BrandID    *string         `json:"brand_id"`
	RecordID   *string         `json:"record_id"`
	Kind       *string         `json:"kind"`
	Title      *string         `json:"title"`
	Body       *string         `json:"body"`
	Status     *string         `json:"status"`
	Priority   *string         `json:"priority"`
	AssignedTo json.RawMessage `json:"assigned_to"`
	DueAt      json.RawMessage `json:"due_at"`
	Name       *string         `json:"name"`
	RecordType *string         `json:"record_type"`
	Shared     *bool           `json:"shared"`
	Config     json.RawMessage `json:"config"`
	Version    *int            `json:"version"`
}

// Resource names come from registered routes, never from user input.
func crmResource(r *http.Request) (string, bool) {
	views := strings.Contains(r.Pattern, "/crm/views")
	if views {
		return "crm_views", true
	}
	return "crm_activities", false
}

func scanCRM(row pgx.Row) (crmItem, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return crmItem{}, err
	}
	var item crmItem
	err := json.Unmarshal(raw, &item)
	return item, err
}

// Keep nullable activity fields in responses; clients use null to clear them.
func crmJSON(v crmItem, view bool) map[string]any {
	out := map[string]any{"id": v.ID, "brand_id": v.BrandID, "created_by": v.CreatedBy, "version": v.Version, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt}
	if view {
		out["name"], out["record_type"], out["shared"], out["config"] = v.Name, v.RecordType, v.Shared, v.Config
	} else {
		out["record_id"], out["kind"], out["title"], out["body"] = v.RecordID, v.Kind, v.Title, v.Body
		out["status"], out["priority"], out["assigned_to"], out["due_at"] = v.Status, v.Priority, v.AssignedTo, v.DueAt
	}
	return out
}

func (s *Server) listCRM(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	table, view := crmResource(r)
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	args := []any{a.WorkspaceID, a.Brand}
	where := "c.workspace_id = $1 AND c.brand_id=$2"
	if view {
		args = append(args, nullIfEmpty(a.personID()))
		where += fmt.Sprintf(" AND (c.shared OR c.created_by = $%d)", len(args))
	}
	keys := []string{"record_id", "kind", "status", "assigned_to"}
	if view {
		keys = []string{"record_type"}
	}
	for _, key := range keys {
		if value := r.URL.Query().Get(key); value != "" {
			if (key == "record_id" || key == "assigned_to") && !validUUID(value) {
				writeError(w, r, 400, "invalid_request", key+" must be a record id")
				return
			}
			args = append(args, value)
			where += fmt.Sprintf(" AND c.%s = $%d", key, len(args))
		}
	}
	before, ok := pageCursor(w, r, pageBefore)
	if !ok {
		return
	}
	if before != "" {
		if !validUUID(before) {
			writeError(w, r, 400, "invalid_request", "before must be an activity id")
			return
		}
		args = append(args, before)
		where += fmt.Sprintf(" AND (c.created_at,c.id) < (SELECT created_at,id FROM %s WHERE workspace_id=$1 AND brand_id=$2 AND id=$%d)", table, len(args))
	}
	if !view {
		where += " AND EXISTS (SELECT 1 FROM records r WHERE r.id=c.record_id AND r.workspace_id=c.workspace_id AND r.brand_id=c.brand_id AND r.deleted_at IS NULL AND r.merged_into IS NULL)"
	}
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), "SELECT to_jsonb(c) FROM "+table+" c WHERE "+where+fmt.Sprintf(" ORDER BY c.created_at DESC,c.id DESC LIMIT $%d", len(args)), args...)
	if s.allocError(w, r, err) {
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmItem, error) { return scanCRM(row) })
	if s.allocError(w, r, err) {
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, v := range items {
		data = append(data, crmJSON(v, view))
	}
	out := map[string]any{"data": data}
	var next *string
	if len(items) == limit {
		next = &items[len(items)-1].ID
	}
	out["next_cursor"] = nextCursor(pageBefore, next)
	writeJSON(w, 200, out)
}

func (s *Server) validateCRM(r *http.Request, tx pgx.Tx, in crmInput, v *crmItem, view bool, create bool) error {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if in.RecordID != nil {
		if !create && *in.RecordID != v.RecordID {
			return errInvalid("an activity's record cannot change")
		}
		v.RecordID = *in.RecordID
	}
	if in.Kind != nil {
		if !create && *in.Kind != v.Kind {
			return errInvalid("an activity's kind cannot change")
		}
		v.Kind = *in.Kind
	}
	if in.Title != nil {
		v.Title = strings.TrimSpace(*in.Title)
	}
	if in.Body != nil {
		v.Body = *in.Body
	}
	if in.Status != nil {
		v.Status = *in.Status
	}
	if in.Priority != nil {
		v.Priority = *in.Priority
	}
	if len(in.AssignedTo) > 0 {
		if err := json.Unmarshal(in.AssignedTo, &v.AssignedTo); err != nil {
			return errInvalid("assigned_to must be a member id or null")
		}
	}
	if len(in.DueAt) > 0 {
		if err := json.Unmarshal(in.DueAt, &v.DueAt); err != nil {
			return errInvalid("due_at must be a timestamp or null")
		}
	}
	if view {
		if in.Name != nil {
			v.Name = strings.TrimSpace(*in.Name)
		}
		if in.RecordType != nil {
			v.RecordType = *in.RecordType
		}
		if in.Shared != nil {
			v.Shared = *in.Shared
		}
		if len(in.Config) > 0 {
			v.Config = in.Config
		}
		var config map[string]any
		if len(v.Name) == 0 || len(v.Name) > 100 {
			return errInvalid("name must be between 1 and 100 characters")
		}
		if len(v.Config) > 32000 || json.Unmarshal(v.Config, &config) != nil || config == nil {
			return errInvalid("config must be an object under 32 KB")
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
		if err != nil {
			return err
		}
		if c.recordType(v.RecordType) == nil {
			return errInvalid("record type not found")
		}
		if create {
			if in.BrandID == nil {
				in.BrandID = &a.Brand
			}
			b, err := s.brandRef(ctx, a.WorkspaceID, in.BrandID)
			if err != nil {
				return err
			}
			if !a.writes(b) {
				return errNotFound
			}
			if deref(b) != a.Brand {
				return errNotFound
			}
			v.BrandID = a.Brand
		} else if in.BrandID != nil && *in.BrandID != v.BrandID {
			return errInvalid("a view's brand cannot change")
		}
	} else {
		if !slices.Contains([]string{"task", "note"}, v.Kind) || v.Title == "" || len(v.Title) > 300 || len(v.Body) > 20000 {
			return errInvalid("choose task or note, a title up to 300 characters and a body up to 20000 characters")
		}
		if !slices.Contains([]string{"open", "done"}, v.Status) || !slices.Contains([]string{"low", "normal", "high"}, v.Priority) {
			return errInvalid("invalid status or priority")
		}
		rec, err := lockRecord(ctx, tx, a, v.RecordID)
		if err != nil {
			return err
		}
		if rec.BrandID == nil || *rec.BrandID != a.Brand {
			return errInvalid("assign the record to a brand first")
		}
		v.BrandID = *rec.BrandID
		if v.AssignedTo != nil {
			if !validUUID(*v.AssignedTo) {
				return errInvalid("assignee must be a workspace member")
			}
			var member bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2 AND (brand_ids IS NULL OR cardinality(brand_ids)=0 OR $3::uuid=ANY(brand_ids)))`, a.WorkspaceID, *v.AssignedTo, a.Brand).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errInvalid("assignee must be a workspace member")
			}
		}
	}
	return nil
}

func (s *Server) createCRM(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	table, view := crmResource(r)
	var in crmInput
	if !decode(w, r, &in) {
		return
	}
	v := crmItem{Status: "open", Priority: "normal", Config: json.RawMessage(`{}`)}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := s.validateCRM(r, tx, in, &v, view, true); err != nil {
			return err
		}
		var row pgx.Row
		if view {
			row = tx.QueryRow(r.Context(), `INSERT INTO crm_views(workspace_id,brand_id,name,record_type,shared,config,created_by) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING to_jsonb(crm_views)`, a.WorkspaceID, v.BrandID, v.Name, v.RecordType, v.Shared, v.Config, nullIfEmpty(a.personID()))
		} else {
			row = tx.QueryRow(r.Context(), `INSERT INTO crm_activities(workspace_id,brand_id,record_id,kind,title,body,status,priority,assigned_to,due_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING to_jsonb(crm_activities)`, a.WorkspaceID, v.BrandID, v.RecordID, v.Kind, v.Title, v.Body, v.Status, v.Priority, v.AssignedTo, v.DueAt, nullIfEmpty(a.personID()))
		}
		var err error
		v, err = scanCRM(row)
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, strings.TrimPrefix(table, "crm_")+".created", table, v.ID, map[string]any{"brand_id": v.BrandID, "kind": v.Kind})
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, 201, crmJSON(v, view))
}

func (s *Server) mutateCRM(w http.ResponseWriter, r *http.Request, remove bool) {
	a := accessFrom(r.Context())
	table, view := crmResource(r)
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "CRM item not found")
		return
	}
	var in crmInput
	if !remove && !decode(w, r, &in) {
		return
	}
	var v crmItem
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		v, err = scanCRM(tx.QueryRow(r.Context(), "SELECT to_jsonb(c) FROM "+table+" c WHERE c.workspace_id=$1 AND c.id=$2 FOR UPDATE", a.WorkspaceID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if v.BrandID != a.Brand || !a.writes(&v.BrandID) || (view && deref(v.CreatedBy) != a.personID()) {
			return errNotFound
		}
		if !view {
			if _, err := lockRecord(r.Context(), tx, a, v.RecordID); err != nil {
				return err
			}
		}
		if remove {
			if _, err = tx.Exec(r.Context(), "DELETE FROM "+table+" WHERE workspace_id=$1 AND id=$2", a.WorkspaceID, id); err != nil {
				return err
			}
		} else {
			if in.Version == nil || *in.Version != v.Version {
				return recordConflict{"record_changed", "This item changed. Refresh it before saving again."}
			}
			if err = s.validateCRM(r, tx, in, &v, view, false); err != nil {
				return err
			}
			var row pgx.Row
			if view {
				row = tx.QueryRow(r.Context(), `UPDATE crm_views SET name=$3,record_type=$4,shared=$5,config=$6,version=version+1,updated_at=now() WHERE workspace_id=$1 AND id=$2 RETURNING to_jsonb(crm_views)`, a.WorkspaceID, id, v.Name, v.RecordType, v.Shared, v.Config)
			} else {
				row = tx.QueryRow(r.Context(), `UPDATE crm_activities SET title=$3,body=$4,status=$5,priority=$6,assigned_to=$7,due_at=$8,version=version+1,updated_at=now() WHERE workspace_id=$1 AND id=$2 RETURNING to_jsonb(crm_activities)`, a.WorkspaceID, id, v.Title, v.Body, v.Status, v.Priority, v.AssignedTo, v.DueAt)
			}
			v, err = scanCRM(row)
			if err != nil {
				return err
			}
		}
		action := ".updated"
		if remove {
			action = ".deleted"
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, strings.TrimPrefix(table, "crm_")+action, table, id, map[string]any{"brand_id": v.BrandID, "kind": v.Kind})
	})
	if s.recordError(w, r, err) {
		return
	}
	if remove {
		writeJSON(w, 200, map[string]any{"deleted": true})
	} else {
		writeJSON(w, 200, crmJSON(v, view))
	}
}
func (s *Server) updateCRM(w http.ResponseWriter, r *http.Request) { s.mutateCRM(w, r, false) }
func (s *Server) deleteCRM(w http.ResponseWriter, r *http.Request) { s.mutateCRM(w, r, true) }

// crmSummary reports actual brand data; currencies are never added together.
func (s *Server) crmSummary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	type stage struct {
		Stage    string `json:"stage"`
		Currency string `json:"currency"`
		Count    int    `json:"count"`
		Amount   int64  `json:"amount_minor"`
	}
	type stale struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		UpdatedAt time.Time `json:"updated_at"`
		Stage     string    `json:"stage"`
	}
	out := struct {
		People       int     `json:"people"`
		Companies    int     `json:"companies"`
		OpenTasks    int     `json:"open_tasks"`
		OverdueTasks int     `json:"overdue_tasks"`
		Pipeline     []stage `json:"pipeline"`
		Stale        []stale `json:"stale_deals"`
	}{Pipeline: []stage{}, Stale: []stale{}}
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE record_type='person'),count(*) FILTER (WHERE record_type='account') FROM records WHERE workspace_id=$1 AND brand_id=$2 AND merged_into IS NULL AND deleted_at IS NULL AND archived_at IS NULL`, a.WorkspaceID, a.Brand).Scan(&out.People, &out.Companies); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `SELECT count(*),count(*) FILTER(WHERE c.due_at < now()) FROM crm_activities c JOIN records r ON r.id=c.record_id AND r.workspace_id=c.workspace_id AND r.brand_id=c.brand_id WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.kind='task' AND c.status='open' AND r.deleted_at IS NULL AND r.merged_into IS NULL`, a.WorkspaceID, a.Brand).Scan(&out.OpenTasks, &out.OverdueTasks); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT COALESCE(attributes->>'stage',''),COALESCE(attributes->'value'->>'currency',''),count(*),COALESCE(sum(CASE WHEN jsonb_typeof(attributes->'value'->'amount_minor')='number' THEN (attributes->'value'->>'amount_minor')::bigint ELSE 0 END),0)::bigint FROM records WHERE workspace_id=$1 AND brand_id=$2 AND record_type='deal' AND merged_into IS NULL AND deleted_at IS NULL AND archived_at IS NULL GROUP BY 1,2 ORDER BY 1,2`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v stage
			if err := rows.Scan(&v.Stage, &v.Currency, &v.Count, &v.Amount); err != nil {
				rows.Close()
				return err
			}
			out.Pipeline = append(out.Pipeline, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(r.Context(), `SELECT r.id::text,r.name,r.updated_at,COALESCE(r.attributes->>'stage','') FROM records r LEFT JOIN deals d ON d.id=r.id AND d.workspace_id=r.workspace_id WHERE r.workspace_id=$1 AND r.brand_id=$2 AND r.record_type='deal' AND r.merged_into IS NULL AND r.deleted_at IS NULL AND r.archived_at IS NULL AND COALESCE(d.state,'open')='open' AND r.updated_at < now()-interval '14 days' ORDER BY r.updated_at LIMIT 20`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v stale
			if err := rows.Scan(&v.ID, &v.Name, &v.UpdatedAt, &v.Stage); err != nil {
				rows.Close()
				return err
			}
			out.Stale = append(out.Stale, v)
		}
		rows.Close()
		return rows.Err()
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}
