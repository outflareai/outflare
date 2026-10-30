package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Ideal customer profiles: who the workspace sells to, kept once. Lead
// lists and their fit scores, the lead finder and visitor scoring read the
// same profile.

const maxProfiles = 500

func (s *Server) routesCustomerProfiles(mux *http.ServeMux) {
	read, manage := auth.ScopeProfilesRead, auth.ScopeProfilesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ideal-customer-profiles", s.inBrand(read, s.listCustomerProfiles))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ideal-customer-profiles", s.inBrand(manage, s.createCustomerProfile))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ideal-customer-profiles/{id}", s.inBrand(read, s.getCustomerProfile))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/ideal-customer-profiles/{id}", s.inBrand(manage, s.updateCustomerProfile))
}

type customerProfileView struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	BrandID     *string             `json:"brand_id"`
	Description string              `json:"description"`
	Filters     map[string][]string `json:"filters"`
	Status      string              `json:"status"`
	Version     int                 `json:"version"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

const customerProfileSelect = `
	SELECT c.id::text, c.name, c.brand_id::text, c.description, c.filters, c.status, c.version,
	       c.created_at, c.updated_at
	FROM ideal_customer_profiles c
	WHERE c.workspace_id = $1`

func scanCustomerProfile(row pgx.Row) (customerProfileView, error) {
	var v customerProfileView
	err := row.Scan(&v.ID, &v.Name, &v.BrandID, &v.Description, &v.Filters, &v.Status, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	v.Filters = normalICP(v.Filters)
	return v, err
}

func (s *Server) listCustomerProfiles(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "active" && status != "archived" && status != "all" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, archived or all")
		return
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), customerProfileSelect+`
		AND ($2 = 'all' OR c.status = COALESCE(NULLIF($2, ''), 'active'))
		ORDER BY lower(c.name)`, a.WorkspaceID, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (customerProfileView, error) { return scanCustomerProfile(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = slices.DeleteFunc(list, func(v customerProfileView) bool { return !brand.matches(v.BrandID) })
	if list == nil {
		list = []customerProfileView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// customerProfileFor loads one profile the caller may see, or errNotFound.
func (s *Server) customerProfileFor(ctx context.Context, q pgx.Tx, a *access, id string) (customerProfileView, error) {
	if !validUUID(id) {
		return customerProfileView{}, errNotFound
	}
	var row pgx.Row
	if q != nil {
		row = q.QueryRow(ctx, customerProfileSelect+` AND c.id = $2`, a.WorkspaceID, id)
	} else {
		row = s.pool.QueryRow(ctx, customerProfileSelect+` AND c.id = $2`, a.WorkspaceID, id)
	}
	v, err := scanCustomerProfile(row)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
		return customerProfileView{}, errNotFound
	}
	return v, err
}

func (s *Server) getCustomerProfile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.customerProfileFor(r.Context(), nil, a, r.PathValue("id"))
	if s.profileError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// profileError answers a profile lookup's error; true when it did.
func (s *Server) profileError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "ideal customer profile not found")
		return true
	}
	return s.allocError(w, r, err)
}

type customerProfileInput struct {
	Name        *string                       `json:"name"`
	Description *string                       `json:"description"`
	Filters     optional[map[string][]string] `json:"filters"`
	// The brand's id or handle; null for every brand.
	BrandID optional[string] `json:"brand_id"`
	Status  *string          `json:"status"`
}

func (in *customerProfileInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 100 {
			return errInvalid("name must be 1 to 100 characters")
		}
	}
	if in.Description != nil {
		*in.Description = strings.TrimSpace(*in.Description)
		if len(*in.Description) > maxShortField {
			return errInvalid("description must be at most 4000 characters")
		}
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "archived" {
		return errInvalid("status is active or archived")
	}
	if in.Filters.Set {
		clean, err := cleanICP(in.Filters.Value, "filters")
		if err != nil {
			return err
		}
		in.Filters.Value = &clean
	}
	return nil
}

// cleanICP checks ICP filters and returns them trimmed, without empty
// values or repeats. field names the input in errors.
func cleanICP(in *map[string][]string, field string) (map[string][]string, error) {
	clean := map[string][]string{}
	if in == nil {
		return clean, nil
	}
	for k, vals := range *in {
		if !slices.Contains(icpKeys, k) {
			return nil, errInvalid(field + " keys are " + strings.Join(icpKeys, ", "))
		}
		if len(vals) > 200 {
			return nil, errInvalid(field + "." + k + " may hold at most 200 values")
		}
		var out []string
		for _, v := range vals {
			v = strings.TrimSpace(v)
			if len(v) > 200 {
				return nil, errInvalid(field + " values must be at most 200 characters")
			}
			if v != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, v) }) {
				out = append(out, v)
			}
		}
		if len(out) > 0 {
			clean[k] = out
		}
	}
	return clean, nil
}

func (s *Server) createCustomerProfile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in customerProfileInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if in.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new profile is active; archive it with PATCH")
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	brand, err := s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	filters := map[string][]string{}
	if in.Filters.Value != nil {
		filters = *in.Filters.Value
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if brand, err = brandWrite(ctx, tx, a, brand); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ideal_customer_profiles WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxProfiles {
			return errInvalid("a workspace can have at most 500 ideal customer profiles; archive old ones and reuse them")
		}
		id, err = insertCustomerProfile(ctx, tx, a, brand, *in.Name, deref(in.Description), filters)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "ideal_customer_profile.created", "ideal_customer_profile", id,
			map[string]any{"brand_id": brand, "filters": filterCounts(filters), "via": "manual"})
	})
	if s.profileError(w, r, err) {
		return
	}
	v, err := s.customerProfileFor(ctx, nil, a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func insertCustomerProfile(ctx context.Context, tx pgx.Tx, a *access, brand *string, name, description string, filters map[string][]string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO ideal_customer_profiles (workspace_id, brand_id, name, description, filters, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id::text`,
		a.WorkspaceID, brand, name, description, filters, nullIfEmpty(a.ActorID)).Scan(&id)
	if isUnique(err) {
		return "", errInvalid("an ideal customer profile with that name already exists")
	}
	return id, err
}

// filterCounts says how many values each filter holds, for events: never
// the values, which can name companies.
func filterCounts(f map[string][]string) map[string]int {
	out := map[string]int{}
	for k, v := range f {
		out[k] = len(v)
	}
	return out
}

func (s *Server) updateCustomerProfile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in customerProfileInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	cur, err := s.customerProfileFor(ctx, nil, a, r.PathValue("id"))
	if s.profileError(w, r, err) {
		return
	}
	if !a.writes(cur.BrandID) {
		s.allocError(w, r, errSharedRecord)
		return
	}
	brand := cur.BrandID
	if in.BrandID.Set {
		if brand, err = s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value); s.allocError(w, r, err) {
			return
		}
		if a.Brands != nil && (brand == nil || !a.writes(brand)) {
			s.allocError(w, r, errInvalid("you can only move profiles between your own brands"))
			return
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// New filters bump the version in setProfileFilters.
		changed := in.Description != nil && *in.Description != cur.Description
		if _, err := tx.Exec(ctx, `
			UPDATE ideal_customer_profiles SET name = COALESCE($3, name), description = COALESCE($4, description),
				brand_id = $5, status = COALESCE($6, status), version = version + CASE WHEN $7 THEN 1 ELSE 0 END,
				updated_by = $8, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, in.Name, in.Description, brand, in.Status, changed, nullIfEmpty(a.ActorID)); err != nil {
			if isUnique(err) {
				return errInvalid("an ideal customer profile with that name already exists")
			}
			return err
		}
		if in.Filters.Set {
			if err := setProfileFilters(ctx, tx, a, cur.ID, *in.Filters.Value); err != nil {
				return err
			}
		}
		action := "ideal_customer_profile.updated"
		if in.Status != nil && *in.Status != cur.Status {
			action = map[string]string{"archived": "ideal_customer_profile.archived", "active": "ideal_customer_profile.restored"}[*in.Status]
		}
		data := map[string]any{"brand_id": brand}
		if in.Filters.Set {
			data["filters"] = filterCounts(*in.Filters.Value)
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "ideal_customer_profile", cur.ID, data)
	})
	if s.profileError(w, r, err) {
		return
	}
	v, err := s.customerProfileFor(ctx, nil, a, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// setProfileFilters sets a profile's filters, bumping its version when
// they change.
func setProfileFilters(ctx context.Context, tx pgx.Tx, a *access, profileID string, filters map[string][]string) error {
	_, err := tx.Exec(ctx, `
		UPDATE ideal_customer_profiles SET filters = $3,
			version = version + CASE WHEN filters = $3::jsonb THEN 0 ELSE 1 END, updated_at = now()
		WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, profileID, filters)
	return err
}
