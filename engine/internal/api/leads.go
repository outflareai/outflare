package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
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
	// A lead belongs to one brand (brand_outreach.go): served under the
	// brand, and under the workspace until the contract step.
	for _, b := range s.activationBases("leads", s.inBrand) {
		mux.HandleFunc("GET "+b.path, b.on(read, s.listLeads))
		mux.HandleFunc("POST "+b.path, b.on(manage, s.upsertLead))
		mux.HandleFunc("POST "+b.path+"/import", b.on(manage, s.importLeads))
		mux.HandleFunc("GET "+b.path+"/{id}", b.on(read, s.getLead))
	}
	mux.HandleFunc("PATCH /v1/brands/{brand}/leads/{id}", s.onBrand(manage, s.updateLead))
	mux.HandleFunc("DELETE /v1/brands/{brand}/leads/{id}", s.onBrand(manage, s.deleteLead))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/leads/{id}", s.inWorkspace(manage, s.updateLead))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/leads/{id}", s.inWorkspace(manage, s.deleteLead))
	// A suppression is one brand's, or every brand's: the brand route adds
	// to its brand, and the workspace route (for admins) to every brand.
	for _, b := range s.activationBases("suppressions", s.inBrand) {
		mux.HandleFunc("GET "+b.path, b.on(read, s.listSuppressions))
		mux.HandleFunc("POST "+b.path, b.on(manage, s.addSuppressions))
	}
	mux.HandleFunc("DELETE /v1/brands/{brand}/suppressions/{id}", s.onBrand(auth.ScopeSuppressionsManage, s.removeSuppression))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/suppressions/{id}", s.inWorkspace(auth.ScopeSuppressionsManage, s.removeSuppression))
	mux.HandleFunc("POST /v1/brands/{brand}/recipients/check", s.onBrand(read, s.checkRecipients))
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
	// The brand the lead belongs to.
	BrandID *string `json:"brand_id"`
}

var leadSelect = `
	SELECT l.id, l.email, l.domain, l.first_name, l.last_name, l.company, l.title, l.phone,
	       l.linkedin_url, l.website, l.custom, l.source,
	       EXISTS (` + leads.SuppressedInSQL("l.workspace_id", "l.brand_id", "l.email") + `),
	       (SELECT v.status FROM email_verifications v WHERE v.workspace_id = l.workspace_id AND v.email = lower(l.email)),
	       ARRAY(SELECT m.list_id::text FROM lead_list_members m WHERE m.lead_id = l.id ORDER BY m.added_at),
	       l.created_at, l.updated_at, l.record_id::text, l.brand_id::text
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
	if q.Has("list_id") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "list a list's leads with GET /lead-lists/{id}/members")
		return
	}
	where := ` WHERE l.workspace_id = $1`
	args := []any{a.WorkspaceID}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	// One brand's leads: the route's, or on the workspace route the one
	// ?brand= picks, else the first brand's.
	sel, ok := s.dataBrand(w, r)
	if !ok {
		return
	}
	where += sel.sql("l.brand_id", &args)
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		p := arg("%" + escapeLike(strings.ToLower(v)) + "%")
		where += ` AND (lower(l.email) LIKE ` + p + ` OR lower(l.first_name || ' ' || l.last_name) LIKE ` + p + ` OR lower(l.company) LIKE ` + p + `)`
	}
	more, msg := leadStateFilters(q, arg)
	if msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	where += more
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM leads l`+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	page := where
	if v, ok := pageCursor(w, r, pageAfter); !ok {
		return
	} else if v != "" {
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
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_cursor": nextCursor(pageAfter, next)})
}

// leadStateFilters is the SQL for the suppressed and verification query
// parameters, which listing leads and listing a list's leads share, or
// what is wrong with them. arg adds a query argument and returns its
// placeholder.
func leadStateFilters(q url.Values, arg func(any) string) (string, string) {
	where := ""
	switch q.Get("suppressed") {
	case "":
	case "true":
		where += ` AND EXISTS (` + leads.SuppressedInSQL("l.workspace_id", "l.brand_id", "l.email") + `)`
	case "false":
		where += ` AND NOT EXISTS (` + leads.SuppressedInSQL("l.workspace_id", "l.brand_id", "l.email") + `)`
	default:
		return "", "suppressed must be true or false"
	}
	switch v := q.Get("verification"); v {
	case "":
	case "unverified":
		where += ` AND NOT EXISTS (SELECT 1 FROM email_verifications v WHERE v.workspace_id = l.workspace_id AND v.email = lower(l.email))`
	case "deliverable", "undeliverable", "risky", "unknown":
		where += ` AND EXISTS (SELECT 1 FROM email_verifications v WHERE v.workspace_id = l.workspace_id AND v.email = lower(l.email) AND v.status = ` + arg(v) + `)`
	default:
		return "", "verification must be deliverable, undeliverable, risky, unknown or unverified"
	}
	return where, ""
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Server) getLead(w http.ResponseWriter, r *http.Request) {
	if l, ok := s.findLead(w, r, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, l)
	}
}

// findLead loads a lead the caller may see: on a brand route only the
// route's brand's.
func (s *Server) findLead(w http.ResponseWriter, r *http.Request, id string) (*leadView, bool) {
	a := accessFrom(r.Context())
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "lead not found")
		return nil, false
	}
	rows, err := s.pool.Query(r.Context(), leadSelect+` WHERE l.workspace_id = $1 AND l.id = $2`, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	l, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[leadView])
	if err == nil && !a.sees(l.BrandID) {
		err = pgx.ErrNoRows
	}
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
	// The leads are one brand's: the route's, or on the workspace route the
	// one ?brand= names, else the first brand.
	var ref *string
	if b := r.URL.Query().Get("brand"); b != "" {
		ref = &b
	}
	brand, err := s.activationBrand(ctx, a, ref)
	if !s.activationError(w, r, err) {
		return
	}
	var res leads.ImportResult
	var listID *string
	var batchID string
	listBrand := brand
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if listName != "" {
			// The list is the brand's: the brand's list of that name, or a
			// new one. Until the contract step a list name is used in one
			// brand of a workspace at a time, so another brand's is refused.
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO lead_lists (workspace_id, name, brand_id) VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING RETURNING id`, a.WorkspaceID, listName, brand).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				// A list from before brands had one becomes this brand's.
				err = tx.QueryRow(ctx, `UPDATE lead_lists SET brand_id = $3
					WHERE workspace_id = $1 AND lower(name) = lower($2) AND brand_id = $3 RETURNING id`,
					a.WorkspaceID, listName, brand).Scan(&id)
				if errors.Is(err, pgx.ErrNoRows) {
					return errInvalid("a list with that name belongs to another brand; pick another name")
				}
			}
			if err != nil {
				return err
			}
			listID = &id
		}
		// The import is one batch: what it writes carries it, so it can be
		// undone in one step (batches.go). It belongs to the brand its
		// people are made in: the list's, or the workspace's first.
		var err error
		if len(unique) > 0 {
			if batchID, err = startBatch(ctx, tx, &a.principal, a.WorkspaceID, listBrand, batchCSVImport, "csv", listName, ""); err != nil {
				return err
			}
		}
		if res, err = leads.ImportIn(ctx, tx, a.WorkspaceID, brand, "csv", listID, unique); err != nil {
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
			"suppressed": res.Suppressed, "other_brand": res.OtherBrand, "failed": len(res.Errors), "batch_id": batchID, "brand_id": brand,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": res.Created, "updated": res.Updated, "duplicates": res.Duplicates,
		"suppressed": res.Suppressed, "other_brand": res.OtherBrand, "errors": res.Errors, "list_id": listID, "batch_id": nullIfEmpty(batchID),
		"brand_id": brand,
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type leadInput struct {
	CompanyRecordID *string           `json:"company_record_id"`
	Email           string            `json:"email"`
	FirstName       string            `json:"first_name"`
	LastName        string            `json:"last_name"`
	Company         string            `json:"company"`
	Title           string            `json:"title"`
	Phone           string            `json:"phone"`
	LinkedInURL     string            `json:"linkedin_url"`
	Website         string            `json:"website"`
	Custom          map[string]string `json:"custom"`
	ListID          *string           `json:"list_id"`
	BrandID         *string           `json:"brand_id"`
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
		// A lead is one brand's: the list's, the route's, the one named, or
		// the workspace's first.
		ref := req.BrandID
		if req.ListID != nil {
			var brand *string
			err := tx.QueryRow(ctx, `SELECT brand_id::text FROM lead_lists WHERE id = $1 AND workspace_id = $2`, *req.ListID, a.WorkspaceID).Scan(&brand)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!a.writes(brand) || brand == nil)) {
				return errInvalid("list not found")
			}
			if err != nil {
				return err
			}
			if ref != nil && *ref != "" {
				if id, err := s.brandIDFor(ctx, a.WorkspaceID, *ref); err != nil || id != *brand {
					return errInvalid("brand_id must be the list's brand")
				}
			}
			ref = brand
		}
		var company recordView
		if req.CompanyRecordID != nil {
			var err error
			company, err = loadRecord(ctx, tx, a, *req.CompanyRecordID)
			if err != nil {
				return err
			}
			if company.Type != "account" || company.Archived || !a.writes(company.BrandID) {
				return errInvalid("choose an active company in your brand")
			}
			if ref == nil {
				ref = company.BrandID
			}
			l.Company = company.Name
			if l.Website == "" {
				l.Website, _ = company.Attributes["website"].(string)
			}
		}
		brand, err := s.activationBrand(ctx, a, ref)
		if err == nil && req.CompanyRecordID != nil && deref(company.BrandID) != brand {
			return errInvalid("the lead and company must belong to the same brand")
		}
		if err != nil {
			return err
		}
		if res, err = leads.ImportIn(ctx, tx, a.WorkspaceID, brand, "api", req.ListID, []leads.Lead{l}); err != nil {
			return err
		}
		if res.OtherBrand > 0 {
			return errLeadInOtherBrand
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM leads WHERE workspace_id = $1 AND brand_id = $2 AND lower(email) = lower($3)`,
			a.WorkspaceID, brand, l.Email).Scan(&id); err != nil {
			return err
		}
		if req.CompanyRecordID != nil {
			if err := s.linkLeadCompany(ctx, tx, a, id, company); err != nil {
				return err
			}
		}

		action := "lead.updated"
		if res.Created == 1 {
			action = "lead.created"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "lead", id, map[string]any{"email": l.Email})
	})
	if errors.Is(err, errLeadInOtherBrand) {
		writeError(w, r, http.StatusConflict, "lead_in_other_brand", errLeadInOtherBrand.Error())
		return
	}
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	lv, ok := s.findLead(w, r, id)
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
		CompanyRecordID *string            `json:"company_record_id"`
		FirstName       *string            `json:"first_name"`
		LastName        *string            `json:"last_name"`
		Company         *string            `json:"company"`
		Title           *string            `json:"title"`
		Phone           *string            `json:"phone"`
		LinkedInURL     *string            `json:"linkedin_url"`
		Website         *string            `json:"website"`
		Custom          map[string]*string `json:"custom"` // null removes a field
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
		if _, err := s.lockLead(ctx, tx, a, id); err != nil {
			return err
		}
		var company recordView
		if req.CompanyRecordID != nil {
			var err error
			company, err = loadRecord(ctx, tx, a, *req.CompanyRecordID)
			if err != nil {
				return err
			}
			if company.Type != "account" || company.Archived || !a.writes(company.BrandID) {
				return errInvalid("choose an active company in your brand")
			}
			req.Company = &company.Name
		}
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
		if req.CompanyRecordID != nil {
			if err := s.linkLeadCompany(ctx, tx, a, id, company); err != nil {
				return err
			}
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
		brand, err := s.lockLead(ctx, tx, a, id)
		if err != nil {
			return err
		}
		var email string
		err = tx.QueryRow(ctx, `DELETE FROM leads WHERE id = $1 AND workspace_id = $2 RETURNING email`, id, a.WorkspaceID).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		data := map[string]any{"email": email, "suppressed": suppress, "brand_id": brand}
		if suppress {
			// The lead's brand stops contacting them (decision 5).
			if _, err := leads.Suppress(ctx, tx, a.WorkspaceID, leads.Added{Reason: "do_not_contact", Source: "lead_deleted",
				Method: leads.MethodManual, ActorID: a.ActorID, ActorType: a.ActorType, BrandID: deref(brand)},
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
	// The brand the entry applies to; null for every brand.
	BrandID *string `json:"brand_id"`
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
	// What applies in one brand: its own entries and those for every
	// brand. The workspace route keeps ?brand=all for every entry.
	sel, ok := s.dataBrand(w, r)
	if !ok {
		return
	}
	if sel.id != "" && sel.id != "none" {
		where += ` AND (brand_id IS NULL OR brand_id = ` + arg(sel.id) + `)`
	} else if sel.id == "none" {
		where += ` AND brand_id IS NULL`
	}
	if sel.in != nil {
		where += ` AND (brand_id IS NULL OR brand_id = ANY(` + arg(sel.in) + `::uuid[]))`
	}
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
	if v, ok := pageCursor(w, r, pageAfter); !ok {
		return
	} else if v != "" {
		where += ` AND value > ` + arg(strings.ToLower(v))
	}
	rows, err := s.pool.Query(ctx, `SELECT id, kind, value, reason, note, source, method, added_by::text, added_by_type,
		integration_id::text, confidence::float8, evidence, record_id::text, created_at, brand_id::text FROM suppressions`+where+
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
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_cursor": nextCursor(pageAfter, next)})
}

func (s *Server) addSuppressions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Values []string `json:"values"`
		Reason string   `json:"reason"`
		Note   string   `json:"note"`
		Source string   `json:"source"`
		Method string   `json:"method"`
		// AllBrands applies the entries to every brand of the workspace,
		// for someone who can see every brand. On the workspace route it
		// is the default.
		AllBrands *bool `json:"all_brands"`
	}
	if !decodeLarge(w, r, &req, 10<<20) {
		return
	}
	// An entry is the brand's it was added in (decision 5); a workspace
	// admin can apply it to every brand.
	brand := ""
	everyBrand := a.Brand == "" && a.Brands == nil
	if req.AllBrands != nil {
		everyBrand = *req.AllBrands
	}
	if everyBrand && a.Brands != nil {
		writeError(w, r, http.StatusForbidden, "forbidden", "only someone who sees every brand can apply a suppression to every brand")
		return
	}
	if !everyBrand {
		id, err := s.defaultBrand(r.Context(), a)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if id == "none" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the workspace has no brand yet: add one first")
			return
		}
		brand = id
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
		by := leads.Added{Reason: req.Reason, Source: req.Source, Note: req.Note,
			Method: req.Method, ActorID: a.ActorID, ActorType: a.ActorType, BrandID: brand}
		if everyBrand {
			// One brand's opt-in never clears what an admin applied to
			// every brand (customers.OptedIn).
			by.Evidence = map[string]any{"all_brands": true}
		}
		if added, err = leads.Suppress(ctx, tx, a.WorkspaceID, by, entries); err != nil {
			return err
		}
		if added == 0 {
			return nil
		}
		data := map[string]any{"added": added, "reason": req.Reason, "method": req.Method, "source": req.Source, "all_brands": everyBrand}
		if brand != "" {
			data["brand_id"] = brand
		}
		if len(entries) == 1 {
			data["value"] = entries[0].Value
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "suppressions.added", "suppression", "", data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "existing": len(entries) - added, "invalid": invalids, "brand_id": nullIfEmpty(brand)})
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
		// A brand removes only its own entries; one for every brand is
		// removed on the workspace route.
		var kind, value, reason string
		var brand *string
		err := tx.QueryRow(ctx, `DELETE FROM suppressions WHERE id = $1 AND workspace_id = $2
			AND ($3 = '' OR brand_id = NULLIF($3, '')::uuid) RETURNING kind, value, reason, brand_id::text`,
			id, a.WorkspaceID, a.Brand).Scan(&kind, &value, &reason, &brand)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "suppression.removed", "suppression", id,
			map[string]any{"kind": kind, "value": value, "reason": reason, "brand_id": brand})
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
	// On a brand route the brand is the route's.
	b := strings.TrimSpace(req.Brand)
	if a.Brand != "" {
		b = a.Brand
	}
	brand := ""
	if b != "" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, strings.ToLower(b))
		if err == nil && !a.sees(&id) {
			err = errNotFound
		}
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "brand not found: brand is a brand's id or handle")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = id
	}
	// A brand's own opt-outs count, and those for every brand.
	out, err := policy.CheckContactsIn(ctx, s.pool, a.WorkspaceID, brand, req.Purpose, req.Emails)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if brand != "" {
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

// A chosen company is a relationship, committed with the lead's own fields.
func (s *Server) linkLeadCompany(ctx context.Context, tx pgx.Tx, a *access, id string, company recordView) error {
	if _, err := customers.LinkLeads(ctx, tx, a.WorkspaceID, []string{id}, 1); err != nil {
		return err
	}
	var personID string
	if err := tx.QueryRow(ctx, `SELECT record_id::text FROM leads WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id).Scan(&personID); err != nil {
		return err
	}
	person, err := lockRecord(ctx, tx, a, personID)
	if err != nil {
		return err
	}
	if deref(person.BrandID) != deref(company.BrandID) {
		return errInvalid("the person and company must belong to the same brand")
	}
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, deref(company.BrandID), false)
	if err != nil {
		return err
	}
	refs := []string{}
	if old, ok := person.Attributes["companies"].([]any); ok {
		for _, v := range old {
			if key, ok := v.(string); ok {
				refs = append(refs, key)
			}
		}
	}
	if !slices.Contains(refs, company.ID) {
		refs = append(refs, company.ID)
	}
	raw, _ := json.Marshal(refs)
	changed, err := applyAttributes(ctx, tx, c, c.recordType("person"), a, &person, map[string]json.RawMessage{"companies": raw}, "manual")
	if err != nil {
		return err
	}
	if changed {
		if _, err = tx.Exec(ctx, `UPDATE records SET attributes=$3,attribute_sources=$4,version=version+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, person.ID, person.Attributes, person.AttributeSources); err != nil {
			return err
		}
		if err = record(ctx, tx, &a.principal, a.WorkspaceID, "record.updated", "record", person.ID, map[string]any{"type": "person", "fields": []string{"companies"}}); err != nil {
			return err
		}
	}
	return nil
}

// errLeadInOtherBrand: until the brands contract step drops the
// per-workspace unique index on leads (the version before 0371 upserts on
// it), an address is a lead in one brand of a workspace at a time.
var errLeadInOtherBrand = errors.New("this address is a lead of another brand in this workspace; until brands are fully separated an address can be a lead in one brand at a time")

// lockLead locks a lead the caller may change and returns its brand, or
// errNotFound: on a brand route only the route's brand's.
func (s *Server) lockLead(ctx context.Context, tx pgx.Tx, a *access, id string) (*string, error) {
	var brand *string
	err := tx.QueryRow(ctx, `SELECT brand_id::text FROM leads WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.writes(brand)) {
		return nil, errNotFound
	}
	return brand, err
}
