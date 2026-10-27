package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Ideal customer profiles: who the workspace sells to, kept once. Projects,
// lead lists and their fit scores, the lead finder and visitor scoring read
// the same profile. A project links one with profile_id, and its icp is
// kept equal to the profile's filters, so a change made on either side
// shows on both.

const maxProfiles = 500

func (s *Server) routesCustomerProfiles(mux *http.ServeMux) {
	read, manage := auth.ScopeProjectsRead, auth.ScopeProjectsManage
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
	// The projects that use it.
	ProjectIDs []string  `json:"project_ids"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const customerProfileSelect = `
	SELECT c.id::text, c.name, c.brand_id::text, c.description, c.filters, c.status, c.version,
	       COALESCE((SELECT array_agg(p.id::text ORDER BY lower(p.name)) FROM projects p WHERE p.profile_id = c.id), '{}'),
	       c.created_at, c.updated_at
	FROM ideal_customer_profiles c
	WHERE c.workspace_id = $1`

func scanCustomerProfile(row pgx.Row) (customerProfileView, error) {
	var v customerProfileView
	err := row.Scan(&v.ID, &v.Name, &v.BrandID, &v.Description, &v.Filters, &v.Status, &v.Version, &v.ProjectIDs, &v.CreatedAt, &v.UpdatedAt)
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
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, v.BrandID)) {
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

func sameICP(a, b map[string][]string) bool {
	x, _ := json.Marshal(normalICP(a))
	y, _ := json.Marshal(normalICP(b))
	return string(x) == string(y)
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
		if brand, err = brandWrite(ctx, tx, a, nil, brand); err != nil {
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
	if !a.writes(nil, cur.BrandID) {
		s.allocError(w, r, errSharedRecord)
		return
	}
	brand := cur.BrandID
	if in.BrandID.Set {
		if brand, err = s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value); s.allocError(w, r, err) {
			return
		}
		if a.Brands != nil && (brand == nil || !a.writes(nil, brand)) {
			s.allocError(w, r, errInvalid("you can only move profiles between your own brands"))
			return
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if brand != nil && (cur.BrandID == nil || *brand != *cur.BrandID) {
			// Its projects must belong to the new brand, or be shared.
			var other bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE profile_id = $1 AND brand_id IS DISTINCT FROM $2 AND brand_id IS NOT NULL)`,
				cur.ID, *brand).Scan(&other); err != nil {
				return err
			}
			if other {
				return errInvalid("projects of another brand use this profile; give them their own profile first")
			}
		}
		// New filters bump the version in syncProfileFilters.
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
			if err := syncProfileFilters(ctx, tx, a, cur.ID, *in.Filters.Value, ""); err != nil {
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

// syncProfileFilters sets a profile's filters and copies them to every
// project that uses it, except skipProject (the one being saved, which
// writes its own). A project whose icp changes gets a new brief version,
// so agents see their brief moved.
func syncProfileFilters(ctx context.Context, tx pgx.Tx, a *access, profileID string, filters map[string][]string, skipProject string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE ideal_customer_profiles SET filters = $3,
			version = version + CASE WHEN filters = $3::jsonb THEN 0 ELSE 1 END, updated_at = now()
		WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, profileID, filters); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE projects SET icp = $3, brief_version = brief_version + 1, brief_updated_at = now(),
			brief_updated_by = $4, updated_at = now()
		WHERE workspace_id = $1 AND profile_id = $2 AND id::text <> $5 AND icp <> $3::jsonb`,
		a.WorkspaceID, profileID, filters, nullIfEmpty(a.ActorID), skipProject)
	return err
}

// projectProfile links a project to its profile as it is saved. With
// profileSet the caller chose a profile (or none); its filters become the
// project's icp unless icp was given too, which then updates the profile.
// Otherwise an icp change goes to the profile already linked, or makes the
// project a profile of its own. It returns the icp to store, nil to leave
// it as it is.
func projectProfile(ctx context.Context, tx pgx.Tx, a *access, projectID, projectName string, projectBrand *string,
	profileSet bool, profileID *string, icp *map[string][]string, current *string) (link *string, store *map[string][]string, err error) {
	link = current
	if profileSet {
		link = profileID
	}
	if link == nil {
		if profileSet || icp == nil || len(*icp) == 0 {
			return nil, icp, nil
		}
		// A project with an ICP gets a profile of its own, named after it.
		// A profile someone made by hand may have its name already.
		var name string
		for _, n := range []string{projectName, truncate(projectName, 88) + " (project)"} {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ideal_customer_profiles WHERE workspace_id = $1 AND lower(name) = lower($2))`,
				a.WorkspaceID, n).Scan(&taken); err != nil {
				return nil, nil, err
			}
			if !taken {
				name = n
				break
			}
		}
		if name == "" {
			return nil, icp, nil
		}
		id, err := insertCustomerProfile(ctx, tx, a, projectBrand, name, "", *icp)
		if err != nil {
			return nil, nil, err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "ideal_customer_profile.created", "ideal_customer_profile", id,
			map[string]any{"brand_id": projectBrand, "filters": filterCounts(*icp), "via": "project", "project_id": projectID}); err != nil {
			return nil, nil, err
		}
		return &id, icp, nil
	}
	var brand *string
	var filters map[string][]string
	err = tx.QueryRow(ctx, `SELECT brand_id::text, filters FROM ideal_customer_profiles WHERE workspace_id = $1 AND id = $2 FOR UPDATE`,
		a.WorkspaceID, *link).Scan(&brand, &filters)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, brand)) {
		return nil, nil, errInvalid("ideal customer profile not found")
	}
	if err != nil {
		return nil, nil, err
	}
	if brand != nil && (projectBrand == nil || *projectBrand != *brand) {
		return nil, nil, errInvalid("the profile belongs to another brand; use one of this project's brand or a shared one")
	}
	if icp == nil {
		return link, &filters, nil
	}
	if !sameICP(*icp, filters) {
		if !a.writes(nil, brand) {
			return nil, nil, errSharedRecord
		}
		if err := syncProfileFilters(ctx, tx, a, *link, *icp, projectID); err != nil {
			return nil, nil, err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "ideal_customer_profile.updated", "ideal_customer_profile", *link,
			map[string]any{"filters": filterCounts(*icp), "via": "project", "project_id": projectID}); err != nil {
			return nil, nil, err
		}
	}
	return link, icp, nil
}
