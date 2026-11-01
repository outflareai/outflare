package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// These definitions only contain compile-time SQL identifiers. Request data is
// always a parameter. Each resource has its own table, contract and permissions.
type coverageField struct {
	Default            any
	Kind               string
	Required, Nullable bool
	Ref                string
	Choices            []string
}
type coverageResource struct {
	Table   string
	Finance bool
	Fields  map[string]coverageField
}

var errCoverageConflict = errors.New("coverage changed; refresh before saving")

func (s *Server) routesBrandCoverage(mux *http.ServeMux) {
	for name, resource := range coverageResources {
		base := "/v1/brands/{brand}/coverage/" + name
		scope := "coverage"
		if resource.Finance {
			scope += ".finance"
		}
		mux.HandleFunc("GET "+base, s.onBrand(scope+".read", func(w http.ResponseWriter, r *http.Request) { s.coverageList(w, r, name, resource) }))
		mux.HandleFunc("POST "+base, s.onBrand(scope+".manage", func(w http.ResponseWriter, r *http.Request) { s.coverageWrite(w, r, name, resource, false) }))
		mux.HandleFunc("GET "+base+"/{id}", s.onBrand(scope+".read", func(w http.ResponseWriter, r *http.Request) { s.coverageGet(w, r, name, resource) }))
		if name != "payments" {
			mux.HandleFunc("PATCH "+base+"/{id}", s.onBrand(scope+".manage", func(w http.ResponseWriter, r *http.Request) { s.coverageWrite(w, r, name, resource, true) }))
		}
	}
	s.routesCoverageActions(mux)
}

func coverageVisible(name string, a *access) (string, []any) {
	where := "workspace_id=$1 AND brand_id=$2"
	args := []any{a.WorkspaceID, a.Brand}
	if name == "views" {
		where += " AND (visibility='team' OR created_by=$3)"
		if a.personID() == "" {
			args = append(args, nil)
		} else {
			args = append(args, a.personID())
		}
	}
	return where, args
}
func (s *Server) coverageList(w http.ResponseWriter, r *http.Request, name string, res coverageResource) {
	a := accessFrom(r.Context())
	where, args := coverageVisible(name, a)
	add := func(clause string, value any) {
		args = append(args, value)
		where += " AND " + fmt.Sprintf(clause, len(args))
	}
	q := r.URL.Query()
	if search := strings.TrimSpace(q.Get("q")); search != "" {
		col := "title"
		if name == "publishers" {
			col = "domain"
		}
		add(col+" ILIKE '%%' || $%d || '%%'", search)
	}
	for _, key := range []string{"status", "stage", "kind", "arrangement", "publisher_id", "placement_id", "agreement_id"} {
		if value := q.Get(key); value != "" {
			if _, ok := res.Fields[key]; !ok {
				writeError(w, r, 400, "invalid_request", "unsupported filter: "+key)
				return
			}
			add(key+"::text = $%d", value)
		}
	}
	extra, filterErr := coverageFilterSQL(q.Get("filters"), res, &args)
	if s.coverageError(w, r, filterErr) {
		return
	}
	where += extra
	limit, offset := 50, 0
	for key, dest := range map[string]*int{"limit": &limit, "offset": &offset} {
		if q.Has(key) {
			n, err := strconv.Atoi(q.Get(key))
			if err != nil || n < 0 || (key == "limit" && (n < 1 || n > 200)) {
				writeError(w, r, 400, "invalid_request", "invalid pagination")
				return
			}
			*dest = n
		}
	}
	var total int
	if err := s.pool.QueryRow(r.Context(), "SELECT count(*) FROM "+res.Table+" WHERE "+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(r.Context(), fmt.Sprintf("SELECT to_jsonb(t)-'workspace_id' FROM %s t WHERE %s ORDER BY created_at DESC,id LIMIT $%d OFFSET $%d", res.Table, where, len(args)-1, len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	data := []json.RawMessage{}
	for rows.Next() {
		var b json.RawMessage
		if err = rows.Scan(&b); err != nil {
			s.internal(w, r, err)
			return
		}
		data = append(data, b)
	}
	if err = rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if offset+len(data) < total {
		n := offset + len(data)
		next = &n
	}
	writeJSON(w, 200, map[string]any{"data": data, "total": total, "next_offset": next})
}
func (s *Server) coverageGet(w http.ResponseWriter, r *http.Request, name string, res coverageResource) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	where, args := coverageVisible(name, a)
	args = append(args, r.PathValue("id"))
	where += fmt.Sprintf(" AND id=$%d", len(args))
	var data json.RawMessage
	err := s.pool.QueryRow(r.Context(), "SELECT to_jsonb(t)-'workspace_id' FROM "+res.Table+" t WHERE "+where, args...).Scan(&data)
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) coverageError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, errNotFound):
		writeError(w, r, 404, "not_found", "not found")
	case errors.Is(err, errCoverageConflict):
		writeError(w, r, 409, "conflict", err.Error())
	case errors.As(err, &pe) && (pe.Code == "23505" || pe.Code == "40001"):
		writeError(w, r, 409, "conflict", "This record already exists or changed. Refresh before saving.")
	case errors.As(err, &pe) && (pe.Code == "23514" || pe.Code == "23502" || pe.Code == "22P02" || pe.Code == "22007" || pe.Code == "23503"):
		writeError(w, r, 400, "invalid_request", "A field or reference is invalid.")
	default:
		s.allocError(w, r, err)
	}
	return true
}
func coverageURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", errInvalid("Use an http(s) URL without credentials")
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	values := u.Query()
	for k := range values {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || k == "gclid" || k == "fbclid" {
			values.Del(k)
		}
	}
	u.RawQuery = values.Encode()
	return u.String(), nil
}
func coverageValidate(res coverageResource, body map[string]any, update bool) error {
	if body == nil {
		return errInvalid("A JSON object is required")
	}
	for key, f := range res.Fields {
		if _, present := body[key]; !present && !update && f.Default != nil {
			body[key] = f.Default
		}
		if f.Required && !update {
			if v, ok := body[key]; !ok || v == nil || v == "" {
				return errInvalid(key + " is required")
			}
		}
	}
	for key, v := range body {
		if key == "version" && update {
			n, ok := v.(float64)
			if !ok || n < 1 || n != math.Trunc(n) {
				return errInvalid("version is required")
			}
			continue
		}
		f, ok := res.Fields[key]
		if !ok {
			return errInvalid("unknown field: " + key)
		}
		if v == nil {
			if !f.Nullable {
				return errInvalid(key + " cannot be null")
			}
			continue
		}
		switch f.Kind {
		case "bool":
			if _, ok := v.(bool); !ok {
				return errInvalid(key + " must be true or false")
			}
		case "money", "number", "hours":
			n, ok := v.(float64)
			if !ok || n < 0 || n > 9000000000000000 || ((f.Kind == "money" || f.Kind == "hours") && math.Trunc(n) != n) || (f.Kind == "hours" && (n < 1 || n > 8760)) {
				return errInvalid(key + " is out of range")
			}
		default:
			value, ok := v.(string)
			if !ok {
				return errInvalid(key + " must be text")
			}
			value = strings.TrimSpace(value)
			maxLen := 500
			if f.Kind == "long" {
				maxLen = 20000
			}
			if f.Kind == "url" {
				maxLen = 2048
			}
			if len(value) > maxLen || (f.Required && value == "") {
				return errInvalid(key + " has an invalid length")
			}
			if f.Kind == "uuid" && !validUUID(value) {
				return errInvalid(key + " must be an ID")
			}
			if f.Kind == "date" {
				if _, err := time.Parse("2006-01-02", value); err != nil {
					return errInvalid(key + " must be YYYY-MM-DD")
				}
			}
			if f.Kind == "enum" && !slices.Contains(f.Choices, value) {
				return errInvalid(key + " has an invalid value")
			}
			if f.Kind == "currency" {
				if len(value) != 3 || strings.Trim(value, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
					return errInvalid("currency must be a three-letter uppercase code")
				}
			}
			if f.Kind == "domain" {
				value = strings.ToLower(value)
				u, err := url.Parse("https://" + value)
				if err != nil || u.Hostname() != value || !strings.Contains(value, ".") || strings.ContainsAny(value, " /?#@:") {
					return errInvalid("domain must be a hostname")
				}
			}
			if f.Kind == "url" {
				canonical, err := coverageURL(value)
				if err != nil {
					return err
				}
				if key == "url" && (res.Table == "coverage_placements" || res.Table == "coverage_candidates") {
					value = canonical
				}
			}
			body[key] = value
		}
	}
	if update {
		if _, ok := body["version"]; !ok {
			return errInvalid("version is required; reload before editing")
		}
	}
	return nil
}
func coverageReferences(ctx context.Context, tx pgx.Tx, a *access, res coverageResource, body map[string]any) error {
	for key, f := range res.Fields {
		value := body[key]
		if f.Ref == "" || value == nil {
			continue
		}
		var exists bool
		query := "SELECT EXISTS(SELECT 1 FROM " + f.Ref + " WHERE workspace_id=$1 AND brand_id=$2 AND id=$3)"
		args := []any{a.WorkspaceID, a.Brand, value}
		switch f.Ref {
		case "users":
			query = "SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2 AND (role IN ('owner','admin') OR cardinality(brand_ids)=0 OR $3=ANY(brand_ids)))"
			args = []any{a.WorkspaceID, value, a.Brand}
		case "records":
			if !auth.Grants(a.Scopes, auth.ScopeCustomersRead) {
				return errDenied("Linking CRM records requires customers.read")
			}
			query = "SELECT EXISTS(SELECT 1 FROM records WHERE workspace_id=$1 AND (brand_id=$2 OR brand_id IS NULL) AND id=$3 AND merged_into IS NULL)"
			if key == "record_id" {
				query = strings.TrimSuffix(query, ")") + " AND record_type='account')"
			}
		case "coverage_agreements", "coverage_invoices":
			if !auth.Grants(a.Scopes, auth.ScopeCoverageFinanceRead) {
				return errDenied("Linking agreements requires coverage.finance.read")
			}
		case "deals":
			if !auth.Grants(a.Scopes, auth.ScopeOutcomesRead) {
				return errDenied("Linking deals requires outcomes.read")
			}
		}
		if err := tx.QueryRow(ctx, query, args...).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errInvalid(key + " must belong to this brand and workspace")
		}
	}
	return nil
}
func coverageHistory(ctx context.Context, tx pgx.Tx, a *access, name, id, action string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := tx.Exec(ctx, "INSERT INTO coverage_history(workspace_id,brand_id,resource,resource_id,action,detail) VALUES($1,$2,$3,$4,$5,$6)", a.WorkspaceID, a.Brand, name, id, action, detail)
	if err != nil {
		return err
	}
	props := map[string]any{"brand_id": a.Brand, "resource": name}
	var actor *principal
	if a.ActorType != "" {
		actor = &a.principal
	}
	return recordWithProps(ctx, tx, actor, a.WorkspaceID, "coverage_record."+action, "coverage_"+name, id, detail, props)
}
func (s *Server) coverageWrite(w http.ResponseWriter, r *http.Request, name string, res coverageResource, update bool) {
	a := accessFrom(r.Context())
	var body map[string]any
	if !decode(w, r, &body) {
		return
	}
	if err := coverageValidate(res, body, update); s.coverageError(w, r, err) {
		return
	}
	if update && !validUUID(r.PathValue("id")) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	var output json.RawMessage
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		var old map[string]any
		if update {
			where, args := coverageVisible(name, a)
			args = append(args, r.PathValue("id"))
			where += fmt.Sprintf(" AND id=$%d", len(args))
			var raw []byte
			if err := tx.QueryRow(ctx, "SELECT to_jsonb(t) FROM "+res.Table+" t WHERE "+where+" FOR UPDATE", args...).Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if old["version"] != body["version"] {
				return errCoverageConflict
			}
			if name == "views" && old["created_by"] != a.personID() {
				return errDenied("Only the view's author can edit it")
			}
		}
		if err := coverageReferences(ctx, tx, a, res, body); err != nil {
			return err
		}
		merged := map[string]any{}
		for k, v := range old {
			merged[k] = v
		}
		for k, v := range body {
			merged[k] = v
		}
		if err := coverageFinancialRules(ctx, tx, a, name, merged); err != nil {
			return err
		}
		keys := []string{}
		for k := range body {
			if k != "version" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		args := []any{a.WorkspaceID, a.Brand}
		columns := []string{"workspace_id", "brand_id"}
		values := []string{"$1", "$2"}
		sets := []string{}
		for _, key := range keys {
			args = append(args, body[key])
			columns = append(columns, key)
			v := fmt.Sprintf("$%d", len(args))
			values = append(values, v)
			sets = append(sets, key+"="+v)
		}
		var query string
		action := "created"
		if update {
			sets = append(sets, "version=version+1", "updated_at=now()")
			if name == "placements" && body["url"] != nil && body["url"] != old["url"] {
				sets = append(sets, "health='unchecked'", "next_check_at=now()", "last_check_at=NULL", "failure_count=0")
			}
			if name == "agreements" {
				sets = append(sets, "approved_version=NULL", "approved_by=NULL", "approved_at=NULL")
			}
			args = append(args, r.PathValue("id"))
			query = "UPDATE " + res.Table + " t SET " + strings.Join(sets, ",") + fmt.Sprintf(" WHERE workspace_id=$1 AND brand_id=$2 AND id=$%d", len(args))
			action = "updated"
		} else {
			if name == "views" {
				if a.personID() == "" {
					return errDenied("Saved views need a person")
				}
				args = append(args, a.personID())
				columns = append(columns, "created_by")
				values = append(values, fmt.Sprintf("$%d", len(args)))
			}
			query = "INSERT INTO " + res.Table + " AS t (" + strings.Join(columns, ",") + ") VALUES(" + strings.Join(values, ",") + ")"
		}
		if err := tx.QueryRow(ctx, query+" RETURNING to_jsonb(t)-'workspace_id'", args...).Scan(&output); err != nil {
			return err
		}
		var saved struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(output, &saved); err != nil {
			return err
		}
		return coverageHistory(ctx, tx, a, name, saved.ID, action, map[string]any{"fields": keys})
	})
	if s.coverageError(w, r, err) {
		return
	}
	status := 201
	if update {
		status = 200
	}
	writeJSON(w, status, output)
}

// Lock the parent before checking totals: concurrent allocations cannot each
// spend the same remaining balance. Amounts in different currencies never mix.
func coverageFinancialRules(ctx context.Context, tx pgx.Tx, a *access, name string, v map[string]any) error {
	for _, pair := range [][2]string{{"starts_on", "ends_on"}} {
		if x, ok := v[pair[0]].(string); ok {
			if y, ok := v[pair[1]].(string); ok && x > y {
				return errInvalid("end must be on or after start")
			}
		}
	}
	if (name == "tasks" || name == "finance_tasks") && slices.Contains([]any{"resolved", "dismissed"}, v["status"]) && v["resolution"] == "" {
		return errInvalid("Add a resolution before closing this task")
	}
	if (name == "tasks" || name == "finance_tasks") && v["status"] == "snoozed" && v["snooze_until"] == nil {
		return errInvalid("Choose when this task should return")
	}
	if name == "finance_tasks" && v["invoice_id"] != nil {
		var agreement string
		if err := tx.QueryRow(ctx, "SELECT agreement_id::text FROM coverage_invoices WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", a.WorkspaceID, a.Brand, v["invoice_id"]).Scan(&agreement); err != nil {
			return err
		}
		if v["agreement_id"] != nil && v["agreement_id"] != agreement {
			return errInvalid("Invoice must belong to the linked agreement")
		}
	}
	if name == "delivery_reports" {
		count := 0
		for _, k := range []string{"impressions", "clicks", "sessions", "leads", "conversions"} {
			if v[k] != nil {
				count++
			}
		}
		if count == 0 {
			return errInvalid("Record at least one measured count; leave unknown counts blank")
		}
	}
	if name == "deliverables" || name == "invoices" || name == "placements" {
		if id := v["agreement_id"]; id != nil {
			var amount int64
			var currency, publisher string
			if err := tx.QueryRow(ctx, "SELECT amount_minor,currency,publisher_id::text FROM coverage_agreements WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE", a.WorkspaceID, a.Brand, id).Scan(&amount, &currency, &publisher); err != nil {
				return err
			}
			if name == "invoices" && v["currency"] != nil && v["currency"] != currency {
				return errInvalid("Invoice currency must match the agreement")
			}
			if name == "placements" && v["publisher_id"] != publisher {
				return errInvalid("Placement and agreement must have the same publisher")
			}
			if name == "deliverables" {
				var allocated float64
				if err := tx.QueryRow(ctx, "SELECT coalesce(sum(allocation_minor),0) FROM coverage_deliverables WHERE workspace_id=$1 AND brand_id=$2 AND agreement_id=$3 AND id IS DISTINCT FROM $4::uuid AND status<>'cancelled'", a.WorkspaceID, a.Brand, id, v["id"]).Scan(&allocated); err != nil {
					return err
				}
				n, _ := v["allocation_minor"].(float64)
				if v["status"] != "cancelled" && allocated+n > float64(amount) {
					return errInvalid("Allocations exceed the agreement total")
				}
				accepted, _ := v["accepted_quantity"].(float64)
				qty, ok := v["quantity"].(float64)
				if !ok {
					qty = 1
				}
				if accepted > qty {
					return errInvalid("Accepted quantity exceeds promised quantity")
				}
				if pid := v["placement_id"]; pid != nil {
					var matches bool
					if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM coverage_placements WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND agreement_id=$4)", a.WorkspaceID, a.Brand, pid, id).Scan(&matches); err != nil {
						return err
					}
					if !matches {
						return errInvalid("Deliverable placement must belong to the agreement")
					}
				}
			}
		}
	}
	if name == "agreements" && v["id"] != nil {
		var mismatch bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM coverage_placements WHERE workspace_id=$1 AND brand_id=$2 AND agreement_id=$3 AND publisher_id<>$4)", a.WorkspaceID, a.Brand, v["id"], v["publisher_id"]).Scan(&mismatch); err != nil {
			return err
		}
		if mismatch {
			return errInvalid("Move linked placements before changing the agreement publisher")
		}
		var allocated float64
		var linked bool
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT sum(allocation_minor) FROM coverage_deliverables WHERE workspace_id=$1 AND brand_id=$2 AND agreement_id=$3 AND status<>'cancelled'),0), EXISTS(SELECT 1 FROM coverage_invoices WHERE workspace_id=$1 AND brand_id=$2 AND agreement_id=$3 AND currency<>$4)`, a.WorkspaceID, a.Brand, v["id"], v["currency"]).Scan(&allocated, &linked); err != nil {
			return err
		}
		n, _ := v["amount_minor"].(float64)
		if n < allocated || linked {
			return errInvalid("Terms conflict with existing allocations or invoices")
		}
	}
	if name == "invoices" && v["id"] != nil {
		var paid float64
		if err := tx.QueryRow(ctx, "SELECT coalesce(sum(CASE WHEN kind='reversal' THEN -amount_minor ELSE amount_minor END),0) FROM coverage_payments WHERE workspace_id=$1 AND brand_id=$2 AND invoice_id=$3", a.WorkspaceID, a.Brand, v["id"]).Scan(&paid); err != nil {
			return err
		}
		amount, _ := v["amount_minor"].(float64)
		if paid > 0 {
			var currency, kind, agreement string
			if err := tx.QueryRow(ctx, "SELECT currency,kind,agreement_id::text FROM coverage_invoices WHERE workspace_id=$1 AND brand_id=$2 AND id=$3", a.WorkspaceID, a.Brand, v["id"]).Scan(&currency, &kind, &agreement); err != nil {
				return err
			}
			if amount < paid || v["currency"] != currency || v["kind"] != kind || v["agreement_id"] != agreement || v["status"] == "void" || v["status"] == "draft" {
				return errInvalid("Reverse recorded payments before changing settled invoice terms")
			}
		}
	}
	if name == "payments" {
		var duplicate bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM coverage_payments WHERE workspace_id=$1 AND brand_id=$2 AND invoice_id=$3 AND reference=$4)", a.WorkspaceID, a.Brand, v["invoice_id"], v["reference"]).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate {
			return errCoverageConflict
		}
		var currency, status, invoiceKind string
		var amount int64
		if err := tx.QueryRow(ctx, "SELECT currency,amount_minor,status,kind FROM coverage_invoices WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE", a.WorkspaceID, a.Brand, v["invoice_id"]).Scan(&currency, &amount, &status, &invoiceKind); err != nil {
			return err
		}
		if v["currency"] != nil && v["currency"] != currency {
			return errInvalid("Payment currency must match the invoice")
		}
		if invoiceKind == "credit" {
			return errInvalid("Record a refund as a reversal against the original invoice")
		}
		if status == "void" || status == "draft" {
			return errInvalid("Receive the invoice before recording a payment")
		}
		var paid float64
		if err := tx.QueryRow(ctx, "SELECT coalesce(sum(CASE WHEN kind='reversal' THEN -amount_minor ELSE amount_minor END),0) FROM coverage_payments WHERE workspace_id=$1 AND brand_id=$2 AND invoice_id=$3", a.WorkspaceID, a.Brand, v["invoice_id"]).Scan(&paid); err != nil {
			return err
		}
		n, _ := v["amount_minor"].(float64)
		if v["kind"] == "reversal" {
			n = -n
		}
		if paid+n < 0 || paid+n > float64(amount) {
			return errInvalid("Payment exceeds the outstanding balance or reversal exceeds payments")
		}
	}
	return nil
}
