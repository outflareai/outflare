package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/lists"
)

// Lead lists: manual lists filled by import or the API, and lists built
// from a search at a connected data provider.
func (s *Server) routesLists(mux *http.ServeMux) {
	read, manage := auth.ScopeLeadsRead, auth.ScopeLeadsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/lead-lists", s.inBrand(read, s.listLeadLists))
	mux.HandleFunc("POST /v1/workspaces/{ws}/lead-lists", s.inBrand(manage, s.createLeadList))
	mux.HandleFunc("POST /v1/workspaces/{ws}/lead-lists/preview", s.inBrand(manage, s.previewLeadList))
	mux.HandleFunc("GET /v1/workspaces/{ws}/lead-lists/{id}", s.inBrand(read, s.getLeadList))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/lead-lists/{id}", s.inBrand(manage, s.updateLeadList))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/lead-lists/{id}", s.inBrand(manage, s.deleteLeadList))
	mux.HandleFunc("GET /v1/workspaces/{ws}/lead-lists/{id}/members", s.inBrand(read, s.listLeadListMembers))
	mux.HandleFunc("POST /v1/workspaces/{ws}/lead-lists/{id}/leads", s.inBrand(manage, s.addToLeadList))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/lead-lists/{id}/leads/{lead}", s.inBrand(manage, s.removeFromLeadList))
	mux.HandleFunc("POST /v1/workspaces/{ws}/lead-lists/{id}/runs", s.inBrand(manage, s.startListRun))
	mux.HandleFunc("GET /v1/workspaces/{ws}/lead-lists/{id}/runs", s.inBrand(read, s.listListRuns))
	mux.HandleFunc("GET /v1/workspaces/{ws}/list-runs/{run}", s.inBrand(read, s.getListRun))
	mux.HandleFunc("POST /v1/workspaces/{ws}/list-runs/{run}/cancel", s.inBrand(manage, s.cancelListRun))
	mux.HandleFunc("GET /v1/workspaces/{ws}/data-providers", s.inBrand(read, s.listDataProviders))
	mux.HandleFunc("GET /v1/workspaces/{ws}/leads/{id}/sources", s.inWorkspace(read, s.listLeadSources))
}

type listView struct {
	ID                   string                  `json:"id"`
	Name                 string                  `json:"name"`
	Description          string                  `json:"description"`
	BrandID              *string                 `json:"brand_id"`
	ProfileID            *string                 `json:"profile_id"`
	Kind                 string                  `json:"kind"`
	Leads                int                     `json:"leads"`
	SearchIntegrationID  *string                 `json:"search_integration_id"`
	Filters              connectors.PeopleFilter `json:"filters"`
	EnrichIntegrationIDs []string                `json:"enrich_integration_ids"`
	MaxLeads             int                     `json:"max_leads"`
	CreditCap            *float64                `json:"credit_cap"`
	Verify               bool                    `json:"verify"`
	VerifiedEmailsOnly   bool                    `json:"verified_emails_only"`
	SkipContactedDays    int                     `json:"skip_contacted_days"`
	SkipInSequences      bool                    `json:"skip_in_sequences"`
	MinScore             int                     `json:"min_score"`
	RefreshEveryDays     *int                    `json:"refresh_every_days"`
	NextRefreshAt        *time.Time              `json:"next_refresh_at"`
	LastRun              *lists.Run              `json:"last_run"`
	CreatedAt            time.Time               `json:"created_at"`
	UpdatedAt            time.Time               `json:"updated_at"`
}

const listSelect = `
	SELECT l.id, l.name, l.description, l.brand_id, l.profile_id, l.kind,
	       (SELECT count(*)::int FROM lead_list_members m WHERE m.list_id = l.id),
	       l.search_integration_id, l.filters, l.enrich_integration_ids::text[], l.max_leads, l.credit_cap::float8,
	       l.verify, l.verified_emails_only, l.skip_contacted_days, l.skip_in_sequences, l.min_score,
	       l.refresh_every_days, l.next_refresh_at, l.created_at, l.updated_at,
	       (SELECT r.id FROM list_runs r WHERE r.list_id = l.id ORDER BY r.created_at DESC LIMIT 1)
	FROM lead_lists l`

func (s *Server) loadLists(ctx context.Context, where string, args ...any) ([]listView, error) {
	rows, err := s.pool.Query(ctx, listSelect+where, args...)
	if err != nil {
		return nil, err
	}
	var out []listView
	var runIDs []string
	runOf := map[int]string{}
	for rows.Next() {
		var v listView
		var filters []byte
		var lastRun *string
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.BrandID, &v.ProfileID, &v.Kind, &v.Leads, &v.SearchIntegrationID, &filters,
			&v.EnrichIntegrationIDs, &v.MaxLeads, &v.CreditCap, &v.Verify, &v.VerifiedEmailsOnly, &v.SkipContactedDays,
			&v.SkipInSequences, &v.MinScore, &v.RefreshEveryDays, &v.NextRefreshAt, &v.CreatedAt, &v.UpdatedAt, &lastRun); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(filters, &v.Filters)
		if lastRun != nil {
			runOf[len(out)] = *lastRun
			runIDs = append(runIDs, *lastRun)
		}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(runIDs) > 0 {
		rr, err := s.pool.Query(ctx, `SELECT `+lists.RunCols+` FROM list_runs WHERE id = ANY($1)`, runIDs)
		if err != nil {
			return nil, err
		}
		runs := map[string]lists.Run{}
		for rr.Next() {
			run, err := lists.ScanRun(rr)
			if err != nil {
				rr.Close()
				return nil, err
			}
			run.Sample = []connectors.Person{}
			runs[run.ID] = run
		}
		rr.Close()
		for i, id := range runOf {
			if run, ok := runs[id]; ok {
				out[i].LastRun = &run
			}
		}
	}
	if out == nil {
		out = []listView{}
	}
	return out, nil
}

// listScope returns the SQL condition limiting lists (alias l) or their
// runs (alias r) to a brand-limited caller's brands and the shared ones.
func listScope(a *access, alias string, args *[]any) string {
	if a.Brands == nil {
		return ""
	}
	col := alias + `.brand_id`
	if alias == "r" {
		col = `(SELECT b.brand_id FROM lead_lists b WHERE b.id = r.list_id)`
	}
	return brandSel{in: a.Brands}.sql(col, args)
}

func (s *Server) listLeadLists(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	where := ` WHERE l.workspace_id = $1`
	args := []any{a.WorkspaceID}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	where += brand.sql("l.brand_id", &args)
	if k := r.URL.Query().Get("kind"); k != "" {
		if k != "manual" && k != "search" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is manual or search")
			return
		}
		args = append(args, k)
		where += ` AND l.kind = $` + strconv.Itoa(len(args))
	}
	out, err := s.loadLists(r.Context(), where+` ORDER BY lower(l.name)`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// findList loads one list the caller may see, or writes a 404.
func (s *Server) findList(w http.ResponseWriter, r *http.Request, a *access, id string) (*listView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "list not found")
		return nil, false
	}
	args := []any{a.WorkspaceID, id}
	out, err := s.loadLists(r.Context(), ` WHERE l.workspace_id = $1 AND l.id = $2`+listScope(a, "l", &args), args...)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if len(out) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "list not found")
		return nil, false
	}
	return &out[0], true
}

// changeList loads one list the caller may change, or writes a 404 or 403.
func (s *Server) changeList(w http.ResponseWriter, r *http.Request, a *access, id string) (*listView, bool) {
	v, ok := s.findList(w, r, a, id)
	if ok && !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, false
	}
	return v, ok
}

func (s *Server) getLeadList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if l, ok := s.findList(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, l)
	}
}

// listInput is a list's settings, for create and update. Absent fields
// keep their value on update.
type listInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	// Ignored: projects were removed. Accepted so older clients still work.
	ProjectID            optional[string]         `json:"project_id"`
	ProfileID            optional[string]         `json:"profile_id"`
	SearchIntegrationID  optional[string]         `json:"search_integration_id"`
	Filters              *connectors.PeopleFilter `json:"filters"`
	EnrichIntegrationIDs *[]string                `json:"enrich_integration_ids"`
	MaxLeads             *int                     `json:"max_leads"`
	CreditCap            optional[float64]        `json:"credit_cap"`
	Verify               *bool                    `json:"verify"`
	VerifiedEmailsOnly   *bool                    `json:"verified_emails_only"`
	SkipContactedDays    *int                     `json:"skip_contacted_days"`
	SkipInSequences      *bool                    `json:"skip_in_sequences"`
	MinScore             *int                     `json:"min_score"`
	RefreshEveryDays     optional[int]            `json:"refresh_every_days"`
	// Create only: queue the first build straight away.
	Build bool `json:"build"`
}

// apply checks in and writes it onto v.
func (s *Server) applyList(ctx context.Context, a *access, in *listInput, v *listView) error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 100 {
			return errInvalid("name must be 1 to 100 characters")
		}
		v.Name = n
	}
	if in.Description != nil {
		if len(*in.Description) > 2000 {
			return errInvalid("description must be at most 2000 characters")
		}
		v.Description = strings.TrimSpace(*in.Description)
	}
	if in.ProfileID.Set {
		if p := in.ProfileID.Value; p != nil {
			var ok bool
			if validUUID(*p) {
				if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ideal_customer_profiles WHERE workspace_id = $1 AND id = $2)`, a.WorkspaceID, *p).Scan(&ok); err != nil {
					return err
				}
			}
			if !ok {
				return errInvalid("profile_id is not an ideal customer profile in this workspace")
			}
		}
		v.ProfileID = in.ProfileID.Value
	}
	integration := func(id, capability, field string) (connectors.Definition, error) {
		if !validUUID(id) || s.integrations == nil {
			return connectors.Definition{}, errInvalid(field + " is not a connected integration in this workspace")
		}
		i, err := s.integrations.Get(ctx, s.pool, a.WorkspaceID, id)
		if errors.Is(err, connectors.ErrNotFound) {
			return connectors.Definition{}, errInvalid(field + " is not a connected integration in this workspace")
		}
		if err != nil {
			return connectors.Definition{}, err
		}
		d, _ := s.integrations.Registry.Definition(i.Connector)
		if !d.Has(capability) {
			what := map[string]string{connectors.CapSearchPeople: "search for people", connectors.CapEnrichPerson: "find emails or enrich people"}[capability]
			return d, errInvalid(d.Name + " cannot " + what)
		}
		return d, nil
	}
	var searchDef *connectors.Definition
	if in.SearchIntegrationID.Set {
		v.SearchIntegrationID = in.SearchIntegrationID.Value
	}
	if v.SearchIntegrationID != nil && (in.SearchIntegrationID.Set || in.Filters != nil) {
		d, err := integration(*v.SearchIntegrationID, connectors.CapSearchPeople, "search_integration_id")
		if err != nil {
			return err
		}
		searchDef = &d
	}
	if in.Filters != nil {
		f, err := lists.CleanFilter(*in.Filters)
		if err != nil {
			return errInvalid(err.Error())
		}
		v.Filters = f
	}
	if searchDef != nil {
		if supported, ok := s.searchFilters(*searchDef); ok {
			for _, used := range v.Filters.Used() {
				if !slices.Contains(supported, used) {
					return errInvalid(searchDef.Name + " cannot filter by " + strings.ReplaceAll(used, "_", " "))
				}
			}
		}
	}
	if in.EnrichIntegrationIDs != nil {
		ids := *in.EnrichIntegrationIDs
		if len(ids) > 10 {
			return errInvalid("enrich_integration_ids: at most 10 providers")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				return errInvalid("enrich_integration_ids: each provider once")
			}
			seen[id] = true
			if _, err := integration(id, connectors.CapEnrichPerson, "enrich_integration_ids"); err != nil {
				return err
			}
		}
		v.EnrichIntegrationIDs = ids
	}
	if in.MaxLeads != nil {
		if *in.MaxLeads < 1 || *in.MaxLeads > 50000 {
			return errInvalid("max_leads must be 1 to 50000")
		}
		v.MaxLeads = *in.MaxLeads
	}
	if in.CreditCap.Set {
		if c := in.CreditCap.Value; c != nil && *c < 0 {
			return errInvalid("credit_cap must be 0 or more, or null for no cap")
		}
		v.CreditCap = in.CreditCap.Value
	}
	set := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	set(&v.Verify, in.Verify)
	set(&v.VerifiedEmailsOnly, in.VerifiedEmailsOnly)
	set(&v.SkipInSequences, in.SkipInSequences)
	if in.SkipContactedDays != nil {
		if *in.SkipContactedDays < 0 || *in.SkipContactedDays > 3650 {
			return errInvalid("skip_contacted_days must be 0 (off) to 3650")
		}
		v.SkipContactedDays = *in.SkipContactedDays
	}
	if in.MinScore != nil {
		if *in.MinScore < 0 || *in.MinScore > 100 {
			return errInvalid("min_score must be 0 to 100")
		}
		v.MinScore = *in.MinScore
	}
	v.Kind = "manual"
	if v.SearchIntegrationID != nil {
		v.Kind = "search"
	}
	if in.RefreshEveryDays.Set {
		d := in.RefreshEveryDays.Value
		if d != nil && (*d < 1 || *d > 365) {
			return errInvalid("refresh_every_days must be 1 to 365, or null to refresh only when asked")
		}
		v.RefreshEveryDays = d
		v.NextRefreshAt = nil
		if d != nil {
			t := time.Now().Add(time.Duration(*d) * 24 * time.Hour)
			v.NextRefreshAt = &t
		}
	}
	if v.RefreshEveryDays != nil && v.Kind != "search" {
		return errInvalid("only lists built from a search can refresh; set search_integration_id first")
	}
	return nil
}

// searchFilters returns the filters a connector's search understands.
func (s *Server) searchFilters(d connectors.Definition) ([]string, bool) {
	c, ok := s.integrations.Registry.Get(d.ID)
	if !ok {
		return nil, false
	}
	ps, ok := c.(connectors.PeopleSearcher)
	if !ok {
		return nil, false
	}
	return ps.SearchFilters(), true
}

func (s *Server) createLeadList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in listInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
		return
	}
	ctx := r.Context()
	v := listView{MaxLeads: 500, Verify: true, SkipContactedDays: 90, SkipInSequences: true, EnrichIntegrationIDs: []string{}}
	if err := s.applyList(ctx, a, &in, &v); err != nil {
		s.listError(w, r, err)
		return
	}
	if in.Build && v.Kind != "search" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "build needs search_integration_id and filters")
		return
	}
	var run *lists.Run
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		brand, berr := brandWrite(ctx, tx, a, nil)
		if berr != nil {
			return berr
		}
		filters, _ := json.Marshal(v.Filters)
		if err := tx.QueryRow(ctx, `
			INSERT INTO lead_lists (workspace_id, name, description, kind, search_integration_id, filters,
				enrich_integration_ids, max_leads, credit_cap, verify, verified_emails_only, skip_contacted_days,
				skip_in_sequences, min_score, refresh_every_days, next_refresh_at, created_by, brand_id, profile_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7::uuid[], $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
			RETURNING id`,
			a.WorkspaceID, v.Name, v.Description, v.Kind, v.SearchIntegrationID, filters, v.EnrichIntegrationIDs,
			v.MaxLeads, v.CreditCap, v.Verify, v.VerifiedEmailsOnly, v.SkipContactedDays, v.SkipInSequences, v.MinScore,
			v.RefreshEveryDays, v.NextRefreshAt, nullIfEmpty(a.ActorID), brand, v.ProfileID).Scan(&v.ID); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "lead_list.created", "lead_list", v.ID,
			map[string]any{"name": v.Name, "kind": v.Kind, "project_id": nil}); err != nil {
			return err
		}
		if !in.Build {
			return nil
		}
		var err error
		run, err = s.queueListRun(ctx, tx, a, v.ID, lists.KindBuild, nil)
		return err
	})
	if err != nil {
		s.listError(w, r, err)
		return
	}
	l, ok := s.findList(w, r, a, v.ID)
	if !ok {
		return
	}
	if run != nil {
		l.LastRun = run
	}
	writeJSON(w, http.StatusCreated, l)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) listError(w http.ResponseWriter, r *http.Request, err error) {
	var inv errInvalid
	var lerr *lists.ErrInvalid
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", inv.Error())
	case errors.As(err, &lerr):
		writeError(w, r, http.StatusBadRequest, "invalid_request", lerr.Msg)
	case errors.Is(err, lists.ErrBusy):
		writeError(w, r, http.StatusConflict, "conflict", "this list already has a build, refresh or enrichment in progress; wait for it or cancel it")
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "list not found")
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		writeError(w, r, http.StatusConflict, "conflict", "a list with that name already exists")
	default:
		s.internal(w, r, err)
	}
}

func (s *Server) updateLeadList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.changeList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var in listInput
	if !decode(w, r, &in) {
		return
	}
	if in.Build {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "to build, POST to the list's runs")
		return
	}
	ctx := r.Context()
	if err := s.applyList(ctx, a, &in, v); err != nil {
		s.listError(w, r, err)
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		filters, _ := json.Marshal(v.Filters)
		if _, err := tx.Exec(ctx, `
			UPDATE lead_lists SET name = $3, description = $4, kind = $5, search_integration_id = $6,
				filters = $7, enrich_integration_ids = $8::uuid[], max_leads = $9, credit_cap = $10, verify = $11,
				verified_emails_only = $12, skip_contacted_days = $13, skip_in_sequences = $14, min_score = $15,
				refresh_every_days = $16, next_refresh_at = $17, profile_id = $18, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, v.ID, v.Name, v.Description, v.Kind, v.SearchIntegrationID, filters, v.EnrichIntegrationIDs,
			v.MaxLeads, v.CreditCap, v.Verify, v.VerifiedEmailsOnly, v.SkipContactedDays, v.SkipInSequences, v.MinScore,
			v.RefreshEveryDays, v.NextRefreshAt, v.ProfileID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead_list.updated", "lead_list", v.ID, map[string]any{"name": v.Name})
	})
	if err != nil {
		s.listError(w, r, err)
		return
	}
	if l, ok := s.findList(w, r, a, v.ID); ok {
		writeJSON(w, http.StatusOK, l)
	}
}

func (s *Server) deleteLeadList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.changeList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM lead_lists WHERE id = $1 AND workspace_id = $2`, v.ID, a.WorkspaceID); err != nil {
			return err
		}
		// Leads stay: they may be in other lists or sequences.
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead_list.deleted", "lead_list", v.ID, map[string]any{"name": v.Name, "leads": v.Leads})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type listMemberView struct {
	leadView
	Score        *int      `json:"score"`
	ScoreReasons []string  `json:"score_reasons"`
	AddedAt      time.Time `json:"added_at"`
	RunID        *string   `json:"run_id"`
}

func (s *Server) listLeadListMembers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, offset := 100, 0
	if x := q.Get("limit"); x != "" {
		n, err := strconv.Atoi(x)
		if err != nil || n < 1 || n > maxPage {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	if x := q.Get("offset"); x != "" {
		n, err := strconv.Atoi(x)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "offset must be 0 or more")
			return
		}
		offset = n
	}
	order := ` ORDER BY m.score DESC NULLS LAST, m.added_at, l.id`
	switch q.Get("sort") {
	case "", "score":
	case "added":
		order = ` ORDER BY m.added_at DESC, l.id`
	case "email":
		order = ` ORDER BY lower(l.email)`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sort is score, added or email")
		return
	}
	args := []any{a.WorkspaceID, v.ID}
	where := ` JOIN lead_list_members m ON m.lead_id = l.id AND m.list_id = $2 WHERE l.workspace_id = $1`
	if x := strings.TrimSpace(q.Get("q")); x != "" {
		args = append(args, "%"+escapeLike(strings.ToLower(x))+"%")
		p := "$" + strconv.Itoa(len(args))
		where += ` AND (lower(l.email) LIKE ` + p + ` OR lower(l.first_name || ' ' || l.last_name) LIKE ` + p + ` OR lower(l.company) LIKE ` + p + ` OR lower(l.title) LIKE ` + p + `)`
	}
	if x := q.Get("min_score"); x != "" {
		n, err := strconv.Atoi(x)
		if err != nil || n < 0 || n > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "min_score must be 0 to 100")
			return
		}
		args = append(args, n)
		where += ` AND m.score >= $` + strconv.Itoa(len(args))
	}
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM leads l`+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	args = append(args, limit, offset)
	sel := strings.Replace(leadSelect, "FROM leads l", ", m.score, m.score_reasons, m.added_at, m.run_id::text FROM leads l", 1)
	rows, err := s.pool.Query(ctx, sel+where+order+` LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []listMemberView{}
	for rows.Next() {
		var m listMemberView
		var reasons []byte
		l := &m.leadView
		if err := rows.Scan(&l.ID, &l.Email, &l.Domain, &l.FirstName, &l.LastName, &l.Company, &l.Title, &l.Phone,
			&l.LinkedInURL, &l.Website, &l.Custom, &l.Source, &l.Suppressed, &l.Verification, &l.ListIDs, &l.CreatedAt, &l.UpdatedAt, &l.RecordID,
			&m.Score, &reasons, &m.AddedAt, &m.RunID); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		m.ScoreReasons = []string{}
		_ = json.Unmarshal(reasons, &m.ScoreReasons)
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if offset+len(out) < total {
		n := offset + len(out)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "total": total, "next_offset": next})
}

func (s *Server) addToLeadList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.changeList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		LeadIDs []string `json:"lead_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.LeadIDs) == 0 || len(req.LeadIDs) > 10_000 || slices.ContainsFunc(req.LeadIDs, func(s string) bool { return !validUUID(s) }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "lead_ids must hold 1 to 10000 lead ids")
		return
	}
	ctx := r.Context()
	var added int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Only leads in this workspace are added; unknown ids are ignored.
		tag, err := tx.Exec(ctx, `
			INSERT INTO lead_list_members (list_id, lead_id)
			SELECT $1, l.id FROM leads l WHERE l.workspace_id = $2 AND l.id = ANY($3::uuid[])
			ON CONFLICT DO NOTHING`, v.ID, a.WorkspaceID, req.LeadIDs)
		if err != nil {
			return err
		}
		added = tag.RowsAffected()
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead_list.leads_added", "lead_list", v.ID, map[string]any{"added": added})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

func (s *Server) removeFromLeadList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.changeList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	lead := r.PathValue("lead")
	if !validUUID(lead) {
		writeError(w, r, http.StatusNotFound, "not_found", "not in list")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM lead_list_members WHERE list_id = $1 AND lead_id = $2`, v.ID, lead)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead_list.lead_removed", "lead_list", v.ID, map[string]any{"lead_id": lead})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "not in list")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Runs ----

// runOverrides change one run without changing the list.
type runOverrides struct {
	MaxLeads  *int     `json:"max_leads"`
	CreditCap *float64 `json:"credit_cap"`
}

func (s *Server) queueListRun(ctx context.Context, tx pgx.Tx, a *access, listID, kind string, o *runOverrides) (*lists.Run, error) {
	if s.lists == nil {
		return nil, errInvalid("list building is not set up on this server")
	}
	p, err := lists.ParamsForList(ctx, tx, a.WorkspaceID, listID)
	if err != nil {
		return nil, err
	}
	if o != nil {
		if o.MaxLeads != nil {
			if *o.MaxLeads < 1 || *o.MaxLeads > 50000 {
				return nil, errInvalid("max_leads must be 1 to 50000")
			}
			p.MaxLeads = *o.MaxLeads
		}
		if o.CreditCap != nil {
			if *o.CreditCap < 0 {
				return nil, errInvalid("credit_cap must be 0 or more")
			}
			p.CreditCap = o.CreditCap
		}
	}
	switch kind {
	case lists.KindBuild, lists.KindRefresh:
		if p.SearchIntegrationID == "" {
			return nil, errInvalid("this list has no search; set search_integration_id and filters first")
		}
		if p.Filters.Empty() {
			return nil, errInvalid("add at least one search filter, such as titles or company domains")
		}
	case lists.KindEnrich:
		if len(p.EnrichIntegrationIDs) == 0 && p.SearchIntegrationID == "" {
			return nil, errInvalid("choose providers to enrich with in enrich_integration_ids")
		}
	default:
		return nil, errInvalid("kind is build, refresh or enrich")
	}
	run, err := lists.Enqueue(ctx, tx, a.WorkspaceID, &listID, kind, p, a.ActorID)
	if err != nil {
		return nil, err
	}
	if err := record(ctx, tx, &a.principal, a.WorkspaceID, "lead_list.run_started", "lead_list", listID,
		map[string]any{"run_id": run.ID, "kind": kind, "max_leads": p.MaxLeads, "credit_cap": p.CreditCap}); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Server) startListRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.changeList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Kind string `json:"kind"`
		runOverrides
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Kind == "" {
		req.Kind = lists.KindBuild
	}
	ctx := r.Context()
	var run *lists.Run
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = s.queueListRun(ctx, tx, a, v.ID, req.Kind, &req.runOverrides)
		return err
	})
	if err != nil {
		s.listError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) listListRuns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+lists.RunCols+` FROM list_runs
		WHERE workspace_id = $1 AND list_id = $2 ORDER BY created_at DESC LIMIT 50`, a.WorkspaceID, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lists.Run, error) { return lists.ScanRun(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []lists.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) findRun(w http.ResponseWriter, r *http.Request, a *access) (*lists.Run, bool) {
	id := r.PathValue("run")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return nil, false
	}
	args := []any{a.WorkspaceID, id}
	run, err := lists.ScanRun(s.pool.QueryRow(r.Context(), `SELECT `+lists.RunCols+` FROM list_runs r
		WHERE r.workspace_id = $1 AND r.id = $2`+listScope(a, "r", &args), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &run, true
}

func (s *Server) getListRun(w http.ResponseWriter, r *http.Request) {
	if run, ok := s.findRun(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, run)
	}
}

func (s *Server) cancelListRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	run, ok := s.findRun(w, r, a)
	if !ok {
		return
	}
	if a.Brands != nil && run.ListID != nil {
		if _, ok := s.changeList(w, r, a, *run.ListID); !ok {
			return
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE list_runs SET status = 'cancelled', finished_at = now()
			WHERE id = $1 AND status IN ('queued', 'running')`, run.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errInvalid("this run has already finished")
		}
		target := ""
		if run.ListID != nil {
			target = *run.ListID
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead_list.run_cancelled", "lead_list", target, map[string]any{"run_id": run.ID})
	})
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusConflict, "conflict", inv.Error())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if run, ok = s.findRun(w, r, a); ok {
		writeJSON(w, http.StatusOK, run)
	}
}

// previewLeadList runs one page of a search without saving anything, so
// people and agents can check a search before building a list from it.
func (s *Server) previewLeadList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		SearchIntegrationID string                  `json:"search_integration_id"`
		Filters             connectors.PeopleFilter `json:"filters"`
		// Ignored: projects were removed.
		ProjectID *string `json:"project_id"`
		ProfileID *string `json:"profile_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	in := listInput{SearchIntegrationID: optional[string]{Set: true, Value: &req.SearchIntegrationID}, Filters: &req.Filters}
	if req.ProfileID != nil {
		in.ProfileID = optional[string]{Set: true, Value: req.ProfileID}
	}
	v := listView{}
	if err := s.applyList(ctx, a, &in, &v); err != nil {
		s.listError(w, r, err)
		return
	}
	if v.Filters.Empty() {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "add at least one search filter, such as titles or company domains")
		return
	}
	if s.lists == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "list building is not set up on this server")
		return
	}
	var icp connectors.PeopleFilter
	if v.ProfileID != nil {
		var raw []byte
		if err := s.pool.QueryRow(ctx, `SELECT filters FROM ideal_customer_profiles WHERE id = $1`, *v.ProfileID).Scan(&raw); err == nil {
			_ = json.Unmarshal(raw, &icp)
		}
	}
	p := lists.Params{SearchIntegrationID: req.SearchIntegrationID, Filters: v.Filters, ICP: lists.MergeICP(icp, v.Filters)}
	var run lists.Run
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = lists.Enqueue(ctx, tx, a.WorkspaceID, nil, lists.KindPreview, p, a.ActorID)
		return err
	})
	if err != nil {
		s.listError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// ---- Data providers ----

type dataProviderView struct {
	Connector    string             `json:"connector"`
	Name         string             `json:"name"`
	Summary      string             `json:"summary"`
	Website      string             `json:"website"`
	DocsURL      string             `json:"docs_url"`
	KeyURL       string             `json:"key_url"` // where to find the API key
	Auth         string             `json:"auth"`
	Fields       []connectors.Field `json:"fields"`
	Pricing      string             `json:"pricing"`
	Status       string             `json:"status"` // available or planned
	Capabilities []string           `json:"capabilities"`
	Filters      []string           `json:"filters"`
	// The workspace's connected accounts for this provider.
	Integrations []dataIntegrationView `json:"integrations"`
}

type dataIntegrationView struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Status  string             `json:"status"`
	Account connectors.Account `json:"account"`
}

// listDataProviders lists every connector that can search for people, find
// emails or enrich, with the workspace's connected accounts for each, and
// the filters each search understands.
func (s *Server) listDataProviders(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if s.integrations == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "filters": connectors.AllFilters,
			"seniorities": connectors.Seniorities, "employee_ranges": connectors.EmployeeRanges})
		return
	}
	connected, err := s.integrations.List(r.Context(), s.pool, a.WorkspaceID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []dataProviderView{}
	for _, d := range s.integrations.Registry.All() {
		if !d.Has(connectors.CapSearchPeople) && !d.Has(connectors.CapEnrichPerson) && !d.Has(connectors.CapFindEmail) {
			continue
		}
		fields := d.Fields
		if fields == nil {
			fields = []connectors.Field{}
		}
		v := dataProviderView{Connector: d.ID, Name: d.Name, Summary: d.Summary, Website: d.Website, DocsURL: d.DocsURL, KeyURL: d.KeyURL,
			Auth: d.Auth, Fields: fields, Pricing: d.Pricing,
			Status: d.Status, Capabilities: d.Capabilities, Filters: []string{}, Integrations: []dataIntegrationView{}}
		if c, ok := s.integrations.Registry.Get(d.ID); ok {
			if ps, ok := c.(connectors.PeopleSearcher); ok {
				v.Filters = ps.SearchFilters()
			}
		}
		for _, i := range connected {
			if i.Connector == d.ID {
				v.Integrations = append(v.Integrations, dataIntegrationView{ID: i.ID, Name: i.Name, Status: i.Status, Account: i.Account})
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "filters": connectors.AllFilters,
		"seniorities": connectors.Seniorities, "employee_ranges": connectors.EmployeeRanges})
}

type leadSourceView struct {
	Provider      string    `json:"provider"`
	ExternalID    string    `json:"external_id"`
	IntegrationID *string   `json:"integration_id"`
	ListID        *string   `json:"list_id"`
	RunID         *string   `json:"run_id"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

// listLeadSources says where a lead's data came from, for people who ask
// and for compliance.
func (s *Server) listLeadSources(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	l, ok := s.findLead(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT provider, external_id, integration_id::text, list_id::text, run_id::text, first_seen_at, last_seen_at
		FROM lead_sources WHERE workspace_id = $1 AND lead_id = $2 ORDER BY first_seen_at`, a.WorkspaceID, l.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[leadSourceView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []leadSourceView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "imported_as": l.Source})
}
