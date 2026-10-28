package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/policy"
)

func (s *Server) routesLeads(mux *http.ServeMux) {
	read, manage := auth.ScopeLeadsRead, auth.ScopeLeadsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/leads", s.inBrand(read, s.listLeads))
	mux.HandleFunc("POST /v1/workspaces/{ws}/leads", s.inBrand(manage, s.upsertLead))
	mux.HandleFunc("POST /v1/workspaces/{ws}/leads/import", s.inBrand(manage, s.importLeads))
	mux.HandleFunc("GET /v1/workspaces/{ws}/leads/{id}", s.inBrand(read, s.getLead))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/leads/{id}", s.inWorkspace(manage, s.updateLead))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/leads/{id}", s.inWorkspace(manage, s.deleteLead))
	mux.HandleFunc("GET /v1/workspaces/{ws}/suppressions", s.inBrand(read, s.listSuppressions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/suppressions", s.inBrand(manage, s.addSuppressions))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/suppressions/{id}", s.inWorkspace(auth.ScopeSuppressionsManage, s.removeSuppression))
	mux.HandleFunc("GET /v1/workspaces/{ws}/suppression-syncs", s.inWorkspace(read, s.listSuppressionSyncs))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/suppression-syncs/{integration}", s.inWorkspace(auth.ScopeSuppressionsManage, s.updateSuppressionSync))
	mux.HandleFunc("GET /v1/workspaces/{ws}/contact-rules", s.inWorkspace(auth.ScopeWorkspaceRead, s.getContactRules))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/contact-rules", s.inWorkspace(auth.ScopeWorkspaceManage, s.updateContactRules))
	mux.HandleFunc("POST /v1/workspaces/{ws}/recipients/check", s.inBrand(read, s.checkRecipients))
}

// ---- Leads ----

type leadView struct {
	ID          string            `json:"id"`
	Email       string            `json:"email"`
	Domain      string            `json:"domain"`
	FirstName   string            `json:"first_name"`
	LastName    string            `json:"last_name"`
	Company     string            `json:"company"`
	Title       string            `json:"title"`
	Phone       string            `json:"phone"`
	LinkedInURL string            `json:"linkedin_url"`
	Website     string            `json:"website"`
	Custom      map[string]string `json:"custom"`
	Source      string            `json:"source"`
	Suppressed  bool              `json:"suppressed"`
	// Latest verification result, nil until the address has been checked.
	Verification *string   `json:"verification"`
	ListIDs      []string  `json:"list_ids"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// The person record this lead is, once linked.
	RecordID *string `json:"record_id"`
}

var leadSelect = `
	SELECT l.id, l.email, l.domain, l.first_name, l.last_name, l.company, l.title, l.phone,
	       l.linkedin_url, l.website, l.custom, l.source,
	       EXISTS (` + leads.SuppressedSQL("l.workspace_id", "l.email") + `),
	       (SELECT v.status FROM email_verifications v WHERE v.workspace_id = l.workspace_id AND v.email = lower(l.email)),
	       ARRAY(SELECT m.list_id::text FROM lead_list_members m WHERE m.lead_id = l.id ORDER BY m.added_at),
	       l.created_at, l.updated_at, l.record_id::text
	FROM leads l`

const maxPage = 500

func (s *Server) listLeads(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	where := ` WHERE l.workspace_id = $1`
	args := []any{a.WorkspaceID}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	// A lead is a brand's when it is on one of the brand's lists; with
	// brand=none, when it is on no brand's list.
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	switch {
	case sel.id == "none":
		where += ` AND NOT EXISTS (SELECT 1 FROM lead_list_members m JOIN lead_lists x ON x.id = m.list_id WHERE m.lead_id = l.id AND x.brand_id IS NOT NULL)`
	case sel.id != "":
		where += ` AND EXISTS (SELECT 1 FROM lead_list_members m JOIN lead_lists x ON x.id = m.list_id WHERE m.lead_id = l.id AND x.brand_id = ` + arg(sel.id) + `)`
	}
	if v := q.Get("list_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "list_id must be a UUID")
			return
		}
		where += ` AND EXISTS (SELECT 1 FROM lead_list_members m WHERE m.lead_id = l.id AND m.list_id = ` + arg(v) + `)`
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		p := arg("%" + escapeLike(strings.ToLower(v)) + "%")
		where += ` AND (lower(l.email) LIKE ` + p + ` OR lower(l.first_name || ' ' || l.last_name) LIKE ` + p + ` OR lower(l.company) LIKE ` + p + `)`
	}
	switch q.Get("suppressed") {
	case "true":
		where += ` AND EXISTS (` + leads.SuppressedSQL("l.workspace_id", "l.email") + `)`
	case "false":
		where += ` AND NOT EXISTS (` + leads.SuppressedSQL("l.workspace_id", "l.email") + `)`
	}
	switch v := q.Get("verification"); v {
	case "":
	case "unverified":
		where += ` AND NOT EXISTS (SELECT 1 FROM email_verifications v WHERE v.workspace_id = l.workspace_id AND v.email = lower(l.email))`
	case "deliverable", "undeliverable", "risky", "unknown":
		where += ` AND EXISTS (SELECT 1 FROM email_verifications v WHERE v.workspace_id = l.workspace_id AND v.email = lower(l.email) AND v.status = ` + arg(v) + `)`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "verification must be deliverable, undeliverable, risky, unknown or unverified")
		return
	}
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM leads l`+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	page := where
	if v := q.Get("after"); v != "" {
		page += ` AND lower(l.email) > ` + arg(strings.ToLower(v))
	}
	rows, err := s.pool.Query(ctx, leadSelect+page+` ORDER BY lower(l.email) LIMIT `+arg(limit+1), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[leadView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		e := strings.ToLower(list[limit-1].Email)
		next = &e
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_after": next})
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Server) getLead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if l, ok := s.findLead(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, l)
	}
}

func (s *Server) findLead(w http.ResponseWriter, r *http.Request, ws, id string) (*leadView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return nil, false
	}
	rows, err := s.pool.Query(r.Context(), leadSelect+` WHERE l.workspace_id = $1 AND l.id = $2`, ws, id)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	l, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[leadView])
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return l, true
}

// maxLeadImportBytes bounds a CSV upload; 100,000 rows of a typical export fit.
const maxLeadImportBytes = 50 << 20

func (s *Server) importLeads(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "text/csv" && mt != "text/plain" {
		writeError(w, r, http.StatusUnsupportedMediaType, "invalid_request", "send the CSV file as the body with Content-Type: text/csv")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxLeadImportBytes)
	parsed, bad, err := leads.ParseCSV(body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the file is larger than 50 MB")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	io.Copy(io.Discard, body) //nolint:errcheck
	unique, dups := leads.Dedupe(parsed)
	listName := strings.TrimSpace(r.URL.Query().Get("list"))
	if len(listName) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "list name must be at most 100 characters")
		return
	}

	ctx := r.Context()
	var res leads.ImportResult
	var listID *string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if listName != "" {
			// Someone kept to some brands imports into their brands' lists.
			var brand *string
			if a.Brands != nil {
				var cur *string
				err := tx.QueryRow(ctx, `SELECT brand_id::text FROM lead_lists WHERE workspace_id = $1 AND lower(name) = lower($2)`, a.WorkspaceID, listName).Scan(&cur)
				switch {
				case err == nil && !a.writes(nil, cur):
					return errInvalid("a list with that name belongs to another brand, or to every brand; pick another name")
				case err == nil:
				case errors.Is(err, pgx.ErrNoRows):
					if brand, err = brandWrite(ctx, tx, a, nil, nil); err != nil {
						return err
					}
				default:
					return err
				}
			}
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO lead_lists (workspace_id, name, brand_id) VALUES ($1, $2, $3)
				ON CONFLICT (workspace_id, lower(name)) DO UPDATE SET name = lead_lists.name
				RETURNING id`, a.WorkspaceID, listName, brand).Scan(&id); err != nil {
				return err
			}
			listID = &id
		}
		var err error
		if res, err = leads.Import(ctx, tx, a.WorkspaceID, "csv", listID, unique); err != nil {
			return err
		}
		res.Duplicates = dups
		res.Errors = append(bad, res.Errors...)
		if res.Errors == nil {
			res.Errors = []leads.RowError{}
		}
		if res.Created+res.Updated == 0 {
			return nil
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "leads.imported", "lead_list", deref(listID), map[string]any{
			"created": res.Created, "updated": res.Updated, "duplicates": res.Duplicates,
			"suppressed": res.Suppressed, "failed": len(res.Errors),
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": res.Created, "updated": res.Updated, "duplicates": res.Duplicates,
		"suppressed": res.Suppressed, "errors": res.Errors, "list_id": listID,
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type leadInput struct {
	Email       string            `json:"email"`
	FirstName   string            `json:"first_name"`
	LastName    string            `json:"last_name"`
	Company     string            `json:"company"`
	Title       string            `json:"title"`
	Phone       string            `json:"phone"`
	LinkedInURL string            `json:"linkedin_url"`
	Website     string            `json:"website"`
	Custom      map[string]string `json:"custom"`
	ListID      *string           `json:"list_id"`
}

// upsertLead adds one lead, or updates the lead with that email.
func (s *Server) upsertLead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req leadInput
	if !decode(w, r, &req) {
		return
	}
	l := leads.Lead{
		Email: req.Email, FirstName: req.FirstName, LastName: req.LastName, Company: req.Company,
		Title: req.Title, Phone: req.Phone, LinkedInURL: req.LinkedInURL, Website: req.Website,
	}
	if err := l.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.Custom) > 50 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 50 custom fields")
		return
	}
	for k, v := range req.Custom {
		if key := leads.NormaliseKey(k); key != "" {
			if l.Custom == nil {
				l.Custom = map[string]string{}
			}
			l.Custom[key] = v
		}
	}
	if req.ListID != nil && !validUUID(*req.ListID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "list not found")
		return
	}
	ctx := r.Context()
	var res leads.ImportResult
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if req.ListID != nil {
			var brand *string
			err := tx.QueryRow(ctx, `SELECT brand_id::text FROM lead_lists WHERE id = $1 AND workspace_id = $2`, *req.ListID, a.WorkspaceID).Scan(&brand)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.writes(nil, brand)) {
				return errInvalid("list not found")
			}
			if err != nil {
				return err
			}
		}
		var err error
		if res, err = leads.Import(ctx, tx, a.WorkspaceID, "api", req.ListID, []leads.Lead{l}); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM leads WHERE workspace_id = $1 AND lower(email) = lower($2)`, a.WorkspaceID, l.Email).Scan(&id); err != nil {
			return err
		}
		action := "lead.updated"
		if res.Created == 1 {
			action = "lead.created"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "lead", id, map[string]any{"email": l.Email})
	})
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	lv, ok := s.findLead(w, r, a.WorkspaceID, id)
	if !ok {
		return
	}
	status := http.StatusOK
	if res.Created == 1 {
		status = http.StatusCreated
	}
	writeJSON(w, status, lv)
}

func (s *Server) updateLead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return
	}
	var req struct {
		FirstName   *string            `json:"first_name"`
		LastName    *string            `json:"last_name"`
		Company     *string            `json:"company"`
		Title       *string            `json:"title"`
		Phone       *string            `json:"phone"`
		LinkedInURL *string            `json:"linkedin_url"`
		Website     *string            `json:"website"`
		Custom      map[string]*string `json:"custom"` // null removes a field
	}
	if !decode(w, r, &req) {
		return
	}
	set := map[string]string{}
	unset := []string{} // never null: removing nothing keeps custom as it is
	for k, v := range req.Custom {
		key := leads.NormaliseKey(k)
		if key == "" {
			continue
		}
		if v == nil {
			unset = append(unset, key)
		} else {
			set[key] = *v
		}
	}
	setJSON, _ := json.Marshal(set)
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE leads SET
				first_name = COALESCE($3, first_name), last_name = COALESCE($4, last_name),
				company = COALESCE($5, company), title = COALESCE($6, title), phone = COALESCE($7, phone),
				linkedin_url = COALESCE($8, linkedin_url), website = COALESCE($9, website),
				custom = (custom || $10::jsonb) - $11::text[],
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			id, a.WorkspaceID, req.FirstName, req.LastName, req.Company, req.Title, req.Phone,
			req.LinkedInURL, req.Website, string(setJSON), unset)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		if _, err := customers.SyncLeads(ctx, tx, a.WorkspaceID, []string{id}); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead.updated", "lead", id, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getLead(w, r)
}

// deleteLead removes a lead. With suppress=true its address is also added to
// the suppression list, which is how an erasure request ("forget me") is
// honoured without the person being re-imported and emailed later.
func (s *Server) deleteLead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return
	}
	suppress := r.URL.Query().Get("suppress") == "true"
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var email string
		err := tx.QueryRow(ctx, `DELETE FROM leads WHERE id = $1 AND workspace_id = $2 RETURNING email`, id, a.WorkspaceID).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		data := map[string]any{"email": email, "suppressed": suppress}
		if suppress {
			if _, err := leads.Suppress(ctx, tx, a.WorkspaceID, leads.Added{Reason: "do_not_contact", Source: "lead_deleted",
				Method: leads.MethodManual, ActorID: a.ActorID, ActorType: a.ActorType},
				[]leads.Suppression{{Kind: "email", Value: strings.ToLower(email)}}); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead.deleted", "lead", id, data)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Suppression ----

var suppressionSource = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

type suppressionView struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Value         string         `json:"value"`
	Reason        string         `json:"reason"`
	Note          string         `json:"note"`
	Source        string         `json:"source"`
	Method        string         `json:"method"`
	AddedBy       *string        `json:"added_by"`
	AddedByType   string         `json:"added_by_type"`
	IntegrationID *string        `json:"integration_id"`
	Confidence    *float64       `json:"confidence"`
	Evidence      map[string]any `json:"evidence"`
	RecordID      *string        `json:"record_id"`
	CreatedAt     time.Time      `json:"created_at"`
}

func (s *Server) listSuppressions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	where := ` WHERE workspace_id = $1`
	args := []any{a.WorkspaceID}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	if v := q.Get("kind"); v != "" {
		where += ` AND kind = ` + arg(v)
	}
	if v := q.Get("reason"); v != "" {
		where += ` AND reason = ` + arg(v)
	}
	if v := q.Get("method"); v != "" {
		where += ` AND method = ` + arg(v)
	}
	if v := q.Get("source"); v != "" {
		where += ` AND source = ` + arg(v)
	}
	if v := q.Get("integration_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id must be a connection id")
			return
		}
		where += ` AND integration_id = ` + arg(v)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		where += ` AND value LIKE ` + arg("%"+escapeLike(strings.ToLower(v))+"%")
	}
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM suppressions`+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	if v := q.Get("after"); v != "" {
		where += ` AND value > ` + arg(strings.ToLower(v))
	}
	rows, err := s.pool.Query(ctx, `SELECT id, kind, value, reason, note, source, method, added_by::text, added_by_type,
		integration_id::text, confidence::float8, evidence, record_id::text, created_at FROM suppressions`+where+
		` ORDER BY value LIMIT `+arg(limit+1), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[suppressionView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		next = &list[limit-1].Value
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_after": next})
}

func (s *Server) addSuppressions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Values []string `json:"values"`
		Reason string   `json:"reason"`
		Note   string   `json:"note"`
		Source string   `json:"source"`
		Method string   `json:"method"`
	}
	if !decodeLarge(w, r, &req, 10<<20) {
		return
	}
	if req.Reason == "" {
		req.Reason = "manual"
	}
	// Where it came from: the surface by default (web, api, mcp, cli,
	// mission), or a label the caller gives, such as the old tool a list
	// is moved from.
	if req.Source == "" {
		req.Source = "manual"
		if i := reqInfoFrom(r.Context()); i != nil {
			if sf := surface(r, i); sf != "web" {
				req.Source = sf
			}
		}
	} else if !suppressionSource.MatchString(req.Source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source must be 1 to 64 lowercase letters, digits, dots, dashes or underscores, such as hubspot_export")
		return
	}
	if req.Method == "" {
		req.Method = leads.MethodManual
		if req.Source == "mission" {
			req.Method = leads.MethodAISuggested
		}
	}
	if !slices.Contains(leads.Methods, req.Method) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "method must be one of "+strings.Join(leads.Methods, ", "))
		return
	}
	if !slices.Contains(leads.Reasons, req.Reason) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason must be one of "+strings.Join(leads.Reasons, ", "))
		return
	}
	if len(req.Values) == 0 || len(req.Values) > 100_000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "values must hold 1 to 100000 emails or domains")
		return
	}
	if len(req.Note) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note must be at most 500 characters")
		return
	}
	var entries []leads.Suppression
	type invalid struct {
		Value string `json:"value"`
		Error string `json:"error"`
	}
	invalids := []invalid{}
	seen := map[leads.Suppression]bool{}
	for _, v := range req.Values {
		kind, value, err := leads.ParseSuppression(v)
		if err != nil {
			invalids = append(invalids, invalid{v, err.Error()})
			continue
		}
		e := leads.Suppression{Kind: kind, Value: value}
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}
	ctx := r.Context()
	var added int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if added, err = leads.Suppress(ctx, tx, a.WorkspaceID, leads.Added{Reason: req.Reason, Source: req.Source, Note: req.Note,
			Method: req.Method, ActorID: a.ActorID, ActorType: a.ActorType}, entries); err != nil {
			return err
		}
		if added == 0 {
			return nil
		}
		data := map[string]any{"added": added, "reason": req.Reason, "method": req.Method, "source": req.Source}
		if len(entries) == 1 {
			data["value"] = entries[0].Value
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "suppressions.added", "suppression", "", data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "existing": len(entries) - added, "invalid": invalids})
}

func (s *Server) removeSuppression(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "suppression not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var kind, value, reason string
		err := tx.QueryRow(ctx, `DELETE FROM suppressions WHERE id = $1 AND workspace_id = $2 RETURNING kind, value, reason`,
			id, a.WorkspaceID).Scan(&kind, &value, &reason)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "suppression.removed", "suppression", id,
			map[string]any{"kind": kind, "value": value, "reason": reason})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "suppression not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkRecipients runs the send policy for a set of addresses without
// sending, so agents and integrations can filter before they enrol anyone.
func (s *Server) checkRecipients(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Emails []string `json:"emails"`
		// The brand that would email them, for the cross-brand rule.
		Brand string `json:"brand"`
		// What the email would be: cold (the default), lifecycle or
		// transactional.
		Purpose string `json:"purpose"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Purpose == "" {
		req.Purpose = policy.PurposeCold
	}
	if req.Purpose != policy.PurposeCold && req.Purpose != policy.PurposeLifecycle && req.Purpose != policy.PurposeTransactional {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "purpose must be cold, lifecycle or transactional")
		return
	}
	if len(req.Emails) == 0 || len(req.Emails) > 1000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "emails must hold 1 to 1000 addresses")
		return
	}
	ctx := r.Context()
	out, err := policy.CheckContacts(ctx, s.pool, a.WorkspaceID, req.Purpose, req.Emails)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if b := strings.TrimSpace(req.Brand); b != "" {
		brand, err := s.brandIDFor(ctx, a.WorkspaceID, strings.ToLower(b))
		if err == nil && !a.sees(nil, &brand) {
			err = errNotFound
		}
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "brand not found: brand is a brand's id or handle")
			return
		}
		var days *int
		if err == nil {
			err = s.pool.QueryRow(ctx, `SELECT cross_brand_days FROM workspaces WHERE id = $1`, a.WorkspaceID).Scan(&days)
		}
		var other map[string]string
		if err == nil && days != nil {
			other, err = policy.OtherBrands(ctx, s.pool, a.WorkspaceID, req.Emails, brand, *days, time.Now())
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for i := range out {
			if h, ok := other[strings.ToLower(out[i].Email)]; ok && out[i].Allowed {
				out[i].Allowed, out[i].Reasons = false, []string{policy.OtherBrand + ":" + h}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
