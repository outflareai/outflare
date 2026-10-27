package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// Projects: one motion each, an offer sold to one ideal customer in one
// market. A project holds the brief people and agents work from and owns
// domains, mailboxes, lead lists and sequences. Records without a project
// are shared by the workspace.

func (s *Server) routesProjects(mux *http.ServeMux) {
	read, manage := auth.ScopeProjectsRead, auth.ScopeProjectsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/projects", s.inProject(read, s.listProjects))
	mux.HandleFunc("POST /v1/workspaces/{ws}/projects", s.inBrand(manage, s.createProject))
	mux.HandleFunc("GET /v1/workspaces/{ws}/projects/{id}", s.inProject(read, s.getProject))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/projects/{id}", s.inProject(manage, s.updateProject))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/projects/{id}", s.inWorkspace(manage, s.deleteProject))
	mux.HandleFunc("GET /v1/workspaces/{ws}/projects/{id}/brief", s.inProject(read, s.getProjectBrief))
	mux.HandleFunc("POST /v1/workspaces/{ws}/projects/{id}/add", s.inWorkspace(manage, s.addToProject))
	mux.HandleFunc("POST /v1/workspaces/{ws}/projects/{id}/remove", s.inWorkspace(manage, s.removeFromProject))
}

// icpKeys are the filters a project's ideal customer profile may hold, in
// the order they are shown.
var icpKeys = []string{"titles", "seniorities", "industries", "employee_ranges", "locations", "keywords"}

var projectStatuses = []string{"active", "paused", "archived"}

type projectCounts struct {
	Domains         int `json:"domains"`
	Mailboxes       int `json:"mailboxes"`
	LeadLists       int `json:"lead_lists"`
	Sequences       int `json:"sequences"`
	ActiveSequences int `json:"active_sequences"`
}

type projectView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// The brand the project markets, or null when it is not one brand's.
	BrandID  *string             `json:"brand_id"`
	Offer    string              `json:"offer"`
	Audience string              `json:"audience"`
	Market   string              `json:"market"`
	Brief    string              `json:"brief"`
	ICP      map[string][]string `json:"icp"`
	// The ideal customer profile the icp belongs to; icp is always its
	// filters. Null when the project has no ICP.
	ProfileID      *string       `json:"profile_id"`
	BriefVersion   int           `json:"brief_version"`
	BriefUpdatedAt time.Time     `json:"brief_updated_at"`
	DailySendLimit *int          `json:"daily_send_limit"`
	Counts         projectCounts `json:"counts"`
	// Cold sends from the project's sequences in the last 24 hours.
	Sent24h   int       `json:"sent_24h"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// loadProjects returns the workspace's projects, or one when id is set.
// status filters; empty means every status but archived, "all" means all.
func (s *Server) loadProjects(ctx context.Context, ws string, id *string, status string) ([]projectView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, p.status, p.brand_id::text, p.offer, p.audience, p.market, p.brief, p.icp, p.profile_id::text,
		       p.brief_version, p.brief_updated_at, p.daily_send_limit,
		       (SELECT count(*) FROM sending_domains WHERE project_id = p.id)::int,
		       (SELECT count(*) FROM mailboxes WHERE project_id = p.id)::int,
		       (SELECT count(*) FROM lead_lists WHERE project_id = p.id)::int,
		       (SELECT count(*) FROM sequences WHERE project_id = p.id AND status <> 'archived')::int,
		       (SELECT count(*) FROM sequences WHERE project_id = p.id AND status = 'active')::int,
		       (SELECT count(*) FROM messages g JOIN sequences q ON q.id = g.sequence_id
		        WHERE q.project_id = p.id AND g.status IN ('sending', 'sent', 'unknown')
		          AND g.created_at > now() - interval '24 hours')::int,
		       p.created_at, p.updated_at
		FROM projects p
		WHERE p.workspace_id = $1 AND ($2::uuid IS NULL OR p.id = $2)
		  AND ($3 = 'all' OR ($3 = '' AND p.status <> 'archived') OR p.status = $3)
		ORDER BY (p.status = 'archived'), lower(p.name)`, ws, id, status)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (projectView, error) {
		var v projectView
		err := row.Scan(&v.ID, &v.Name, &v.Status, &v.BrandID, &v.Offer, &v.Audience, &v.Market, &v.Brief, &v.ICP, &v.ProfileID,
			&v.BriefVersion, &v.BriefUpdatedAt, &v.DailySendLimit,
			&v.Counts.Domains, &v.Counts.Mailboxes, &v.Counts.LeadLists, &v.Counts.Sequences, &v.Counts.ActiveSequences,
			&v.Sent24h, &v.CreatedAt, &v.UpdatedAt)
		v.ICP = normalICP(v.ICP)
		return v, err
	})
}

// normalICP returns every ICP key, empty lists included, so clients never
// have to check for missing keys.
func normalICP(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(icpKeys))
	for _, k := range icpKeys {
		out[k] = in[k]
		if out[k] == nil {
			out[k] = []string{}
		}
	}
	return out
}

// projectFor loads one project the caller may see, or errNotFound. A
// project token sees only its own project, a brand-limited caller its
// brands' and the shared ones.
func (s *Server) projectFor(ctx context.Context, a *access, id string) (projectView, error) {
	if !validUUID(id) || (a.ProjectID != nil && *a.ProjectID != id) {
		return projectView{}, errNotFound
	}
	list, err := s.loadProjects(ctx, a.WorkspaceID, &id, "all")
	if err != nil {
		return projectView{}, err
	}
	if len(list) == 0 || !a.sees(&id, list[0].BrandID) {
		return projectView{}, errNotFound
	}
	noteProject(ctx, id, list[0].BrandID)
	return list[0], nil
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "all" && !slices.Contains(projectStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, paused, archived or all")
		return
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.loadProjects(r.Context(), a.WorkspaceID, a.ProjectID, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = slices.DeleteFunc(list, func(p projectView) bool { return !brand.matches(p.BrandID) })
	if list == nil {
		list = []projectView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.projectFor(r.Context(), a, r.PathValue("id"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type projectInput struct {
	Name           *string                       `json:"name"`
	Status         *string                       `json:"status"`
	Offer          *string                       `json:"offer"`
	Audience       *string                       `json:"audience"`
	Market         *string                       `json:"market"`
	Brief          *string                       `json:"brief"`
	ICP            optional[map[string][]string] `json:"icp"`
	DailySendLimit optional[int]                 `json:"daily_send_limit"`
	// The ideal customer profile to use; null for none. Its filters become
	// the project's icp.
	ProfileID optional[string] `json:"profile_id"`
	// The brand's id or handle; null makes the project shared by every brand.
	BrandID optional[string] `json:"brand_id"`
	// The brief_version the edit was made against. When set and the brief
	// has changed since, the update fails with 409 instead of overwriting.
	BriefVersion *int `json:"brief_version"`
}

const (
	maxShortField = 4000
	maxBrief      = 100000
)

func (in *projectInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 100 {
			return errInvalid("name must be 1 to 100 characters")
		}
	}
	if in.Status != nil && !slices.Contains(projectStatuses, *in.Status) {
		return errInvalid("status is active, paused or archived")
	}
	for name, f := range map[string]*string{"offer": in.Offer, "audience": in.Audience, "market": in.Market} {
		if f != nil {
			*f = strings.TrimSpace(*f)
			if len(*f) > maxShortField {
				return errInvalid(fmt.Sprintf("%s must be at most %d characters", name, maxShortField))
			}
		}
	}
	if in.Brief != nil {
		*in.Brief = strings.TrimSpace(*in.Brief)
		if len(*in.Brief) > maxBrief {
			return errInvalid(fmt.Sprintf("brief must be at most %d characters", maxBrief))
		}
	}
	if in.ICP.Set {
		clean, err := cleanICP(in.ICP.Value, "icp")
		if err != nil {
			return err
		}
		in.ICP.Value = &clean
	}
	if in.ProfileID.Value != nil && !validUUID(*in.ProfileID.Value) {
		return errInvalid("ideal customer profile not found")
	}
	if v := in.DailySendLimit.Value; v != nil && (*v < 1 || *v > 1000000) {
		return errInvalid("daily_send_limit must be 1 to 1000000, or null for no limit")
	}
	return nil
}

func (in *projectInput) touchesBrief() bool {
	return in.Offer != nil || in.Audience != nil || in.Market != nil || in.Brief != nil || in.ICP.Set || in.ProfileID.Set
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req projectInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if req.BriefVersion != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brief_version is only for updates")
		return
	}
	if err := req.validate(); err != nil {
		s.allocError(w, r, err)
		return
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	status := "active"
	if req.Status != nil {
		status = *req.Status
	}
	icp := map[string][]string{}
	if req.ICP.Value != nil {
		icp = *req.ICP.Value
	}
	ctx := r.Context()
	brand, err := s.brandRef(ctx, a.WorkspaceID, req.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if brand, err = brandWrite(ctx, tx, a, nil, brand); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO projects (workspace_id, name, status, offer, audience, market, brief, icp, daily_send_limit,
			                      created_by, brief_updated_by, brand_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $11) RETURNING id`,
			a.WorkspaceID, *req.Name, status, str(req.Offer), str(req.Audience), str(req.Market), str(req.Brief),
			icp, req.DailySendLimit.Value, a.ActorID, brand).Scan(&id)
		if isUnique(err) {
			return errInvalid("a project with that name already exists")
		}
		if err != nil {
			return err
		}
		var given *map[string][]string
		if req.ICP.Set {
			given = &icp
		}
		link, store, err := projectProfile(ctx, tx, a, id, *req.Name, brand, req.ProfileID.Set, req.ProfileID.Value, given, nil)
		if err != nil {
			return err
		}
		if link != nil {
			if _, err := tx.Exec(ctx, `UPDATE projects SET profile_id = $2, icp = $3 WHERE id = $1`, id, *link, *store); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "project.created", "project", id, map[string]any{"name": *req.Name, "brand_id": brand, "profile_id": link})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.projectFor(ctx, a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// errStaleBrief: the brief changed after the caller read it.
var errStaleBrief = errors.New("stale brief")

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req projectInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	p, err := s.projectFor(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	if !a.writes(&id, p.BrandID) {
		s.allocError(w, r, errSharedRecord)
		return
	}
	if req.BrandID.Set && a.ProjectID != nil {
		writeError(w, r, http.StatusForbidden, "forbidden", "a project token cannot move its project to another brand")
		return
	}
	brand, err := s.brandRef(ctx, a.WorkspaceID, req.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	if req.BrandID.Set && a.Brands != nil && (brand == nil || !a.writes(nil, brand)) {
		s.allocError(w, r, errInvalid("you can only move projects between your own brands"))
		return
	}
	var icp any
	if req.ICP.Set {
		icp = *req.ICP.Value
	}
	var changed []string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur struct {
			status, offer, audience, market, brief string
			icp                                    map[string][]string
			profile                                *string
			version                                int
		}
		err := tx.QueryRow(ctx, `
			SELECT status, offer, audience, market, brief, icp, profile_id::text, brief_version
			FROM projects WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID,
		).Scan(&cur.status, &cur.offer, &cur.audience, &cur.market, &cur.brief, &cur.icp, &cur.profile, &cur.version)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if req.BriefVersion != nil && *req.BriefVersion != cur.version && req.touchesBrief() {
			return errStaleBrief
		}
		profile := cur.profile
		if req.ProfileID.Set || req.ICP.Set || req.BrandID.Set {
			projectBrand := p.BrandID
			if req.BrandID.Set {
				projectBrand = brand
			}
			name := p.Name
			if req.Name != nil {
				name = *req.Name
			}
			var given *map[string][]string
			if req.ICP.Set {
				given = req.ICP.Value
			}
			link, store, err := projectProfile(ctx, tx, a, id, name, projectBrand, req.ProfileID.Set, req.ProfileID.Value, given, cur.profile)
			if err != nil {
				return err
			}
			profile = link
			if store != nil {
				req.ICP.Set, req.ICP.Value, icp = true, store, *store
			}
		}
		// Only real changes bump the brief version, so saving an unchanged
		// form does not tell every agent its brief moved.
		diff := func(name string, p *string, old string) {
			if p != nil && *p != old {
				changed = append(changed, name)
			}
		}
		diff("offer", req.Offer, cur.offer)
		diff("audience", req.Audience, cur.audience)
		diff("market", req.Market, cur.market)
		diff("brief", req.Brief, cur.brief)
		if req.ICP.Set {
			a, _ := json.Marshal(normalICP(*req.ICP.Value))
			b, _ := json.Marshal(normalICP(cur.icp))
			if string(a) != string(b) {
				changed = append(changed, "icp")
			}
		}
		briefChanged := len(changed) > 0
		_, err = tx.Exec(ctx, `
			UPDATE projects SET
				name = COALESCE($3, name),
				status = COALESCE($4, status),
				offer = COALESCE($5, offer),
				audience = COALESCE($6, audience),
				market = COALESCE($7, market),
				brief = COALESCE($8, brief),
				icp = CASE WHEN $9 THEN $10::jsonb ELSE icp END,
				daily_send_limit = CASE WHEN $11 THEN $12 ELSE daily_send_limit END,
				brief_version = brief_version + CASE WHEN $13 THEN 1 ELSE 0 END,
				brief_updated_at = CASE WHEN $13 THEN now() ELSE brief_updated_at END,
				brief_updated_by = CASE WHEN $13 THEN $14::uuid ELSE brief_updated_by END,
				brand_id = CASE WHEN $15 THEN $16::uuid ELSE brand_id END,
				profile_id = $17::uuid,
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			id, a.WorkspaceID, req.Name, req.Status, req.Offer, req.Audience, req.Market, req.Brief,
			req.ICP.Set, icp, req.DailySendLimit.Set, req.DailySendLimit.Value, briefChanged, a.ActorID, req.BrandID.Set, brand, profile)
		if isUnique(err) {
			return errInvalid("a project with that name already exists")
		}
		if err != nil {
			return err
		}
		data := map[string]any{}
		if briefChanged {
			data["brief_fields"] = changed
			data["brief_version"] = cur.version + 1
		}
		if req.Status != nil && *req.Status != cur.status {
			data["status"] = map[string]string{"from": cur.status, "to": *req.Status}
		}
		if req.DailySendLimit.Set {
			data["daily_send_limit"] = req.DailySendLimit.Value
		}
		if req.Name != nil {
			data["name"] = *req.Name
		}
		if (profile == nil) != (cur.profile == nil) || (profile != nil && *profile != *cur.profile) {
			data["profile_id"] = profile
		}
		if req.BrandID.Set {
			data["brand_id"] = brand
			// Its mailboxes and sequences moved brand with it.
			if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "project.updated", "project", id, data)
	})
	if errors.Is(err, errStaleBrief) {
		writeError(w, r, http.StatusConflict, "brief_changed", "the brief changed since you loaded it; reload it and apply your edit again")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.projectFor(ctx, a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteProject removes a project. Its domains, mailboxes, lists and
// sequences stay and become shared. Refused while live project tokens exist,
// so nobody's agent silently loses access.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	var tokens int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM api_tokens WHERE project_id = $1 AND workspace_id = $2
			AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, id, a.WorkspaceID).Scan(&tokens); err != nil {
			return err
		}
		if tokens > 0 {
			return nil
		}
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM projects WHERE id = $1 AND workspace_id = $2 RETURNING name`, id, a.WorkspaceID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "project.deleted", "project", id, map[string]any{"name": name})
	})
	if s.allocError(w, r, err) {
		return
	}
	if tokens > 0 {
		writeError(w, r, http.StatusConflict, "project_has_tokens",
			fmt.Sprintf("revoke this project's %d API token(s) first, or archive the project instead", tokens))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectBrief is what an agent reads before working on a project.
type projectBrief struct {
	ProjectID string              `json:"project_id"`
	Name      string              `json:"name"`
	Status    string              `json:"status"`
	Version   int                 `json:"version"`
	UpdatedAt time.Time           `json:"updated_at"`
	Offer     string              `json:"offer"`
	Audience  string              `json:"audience"`
	Market    string              `json:"market"`
	ICP       map[string][]string `json:"icp"`
	Brief     string              `json:"brief"`
	// Everything above as one Markdown document, ready for a prompt.
	Markdown string `json:"markdown"`
}

var icpLabels = map[string]string{
	"titles": "Titles", "seniorities": "Seniorities", "industries": "Industries",
	"employee_ranges": "Company size (employees)", "locations": "Locations", "keywords": "Keywords",
}

func briefMarkdown(v projectView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", v.Name)
	section := func(title, body string) {
		if body != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", title, body)
		}
	}
	section("Offer", v.Offer)
	section("Audience", v.Audience)
	var icp []string
	for _, k := range icpKeys {
		if len(v.ICP[k]) > 0 {
			icp = append(icp, fmt.Sprintf("- %s: %s", icpLabels[k], strings.Join(v.ICP[k], ", ")))
		}
	}
	section("Ideal customer filters", strings.Join(icp, "\n"))
	section("Market", v.Market)
	section("Brief", v.Brief)
	if v.Offer == "" && v.Audience == "" && len(icp) == 0 && v.Market == "" && v.Brief == "" {
		b.WriteString("\nNo brief written yet.\n")
	}
	return b.String()
}

func (s *Server) getProjectBrief(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.projectFor(r.Context(), a, r.PathValue("id"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, projectBrief{
		ProjectID: v.ID, Name: v.Name, Status: v.Status, Version: v.BriefVersion, UpdatedAt: v.BriefUpdatedAt,
		Offer: v.Offer, Audience: v.Audience, Market: v.Market, ICP: v.ICP, Brief: v.Brief,
		Markdown: briefMarkdown(v),
	})
}

// projectMembers names records to move into or out of a project.
type projectMembers struct {
	Domains     []string `json:"domains"`
	MailboxIDs  []string `json:"mailbox_ids"`
	LeadListIDs []string `json:"lead_list_ids"`
	SequenceIDs []string `json:"sequence_ids"`
	// Moving a domain moves its mailboxes too, unless this is false.
	WithMailboxes *bool `json:"with_mailboxes"`
}

type projectMoved struct {
	Domains   int `json:"domains"`
	Mailboxes int `json:"mailboxes"`
	LeadLists int `json:"lead_lists"`
	Sequences int `json:"sequences"`
}

func (m *projectMembers) check(a *access) error {
	if len(m.Domains)+len(m.MailboxIDs)+len(m.LeadListIDs)+len(m.SequenceIDs) == 0 {
		return errInvalid("name at least one of domains, mailbox_ids, lead_list_ids or sequence_ids")
	}
	if len(m.Domains)+len(m.MailboxIDs)+len(m.LeadListIDs)+len(m.SequenceIDs) > 10000 {
		return errInvalid("move at most 10000 records at a time")
	}
	for i, d := range m.Domains {
		m.Domains[i] = strings.ToLower(strings.TrimSpace(d))
	}
	for _, ids := range [][]string{m.MailboxIDs, m.LeadListIDs, m.SequenceIDs} {
		for _, id := range ids {
			if !validUUID(id) {
				return errInvalid("ids must be UUIDs")
			}
		}
	}
	need := func(n int, scope string) error {
		if n > 0 && !a.has(scope) {
			return errDenied("moving these also needs scope " + scope)
		}
		return nil
	}
	return errors.Join(
		need(len(m.Domains)+len(m.MailboxIDs), auth.ScopeMailboxesManage),
		need(len(m.LeadListIDs), auth.ScopeLeadsManage),
		need(len(m.SequenceIDs), auth.ScopeSequencesManage),
	)
}

func (s *Server) addToProject(w http.ResponseWriter, r *http.Request) {
	s.moveProjectMembers(w, r, true)
}

func (s *Server) removeFromProject(w http.ResponseWriter, r *http.Request) {
	s.moveProjectMembers(w, r, false)
}

// moveProjectMembers sets project_id on the named records: to the project
// when adding (taking them from any other project), to null when removing
// (only records in this project). Unknown names are ignored and not counted.
func (s *Server) moveProjectMembers(w http.ResponseWriter, r *http.Request, add bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req projectMembers
	if !decode(w, r, &req) {
		return
	}
	if err := req.check(a); err != nil {
		var denied errDenied
		if errors.As(err, &denied) {
			writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
			return
		}
		s.allocError(w, r, err)
		return
	}
	withMailboxes := req.WithMailboxes == nil || *req.WithMailboxes
	ctx := r.Context()
	var moved projectMoved
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if validUUID(id) {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2)`, id, a.WorkspaceID).Scan(&exists); err != nil {
				return err
			}
		}
		if !exists {
			return errNotFound
		}
		// $3 is the new project; $4 the project records must be in now
		// (null: any). Adding moves from anywhere, removing only from here.
		var to, from *string
		if add {
			to = &id
		} else {
			from = &id
		}
		run := func(n *int, sql string, args ...any) error {
			tag, err := tx.Exec(ctx, sql, append([]any{a.WorkspaceID, args[0], to, from}, args[1:]...)...)
			if err != nil {
				return err
			}
			*n += int(tag.RowsAffected())
			return nil
		}
		const notSame = `project_id IS DISTINCT FROM $3 AND ($4::uuid IS NULL OR project_id = $4)`
		if err := run(&moved.Domains, `UPDATE sending_domains SET project_id = $3, updated_at = now()
			WHERE workspace_id = $1 AND domain = ANY($2::text[]) AND `+notSame, req.Domains); err != nil {
			return err
		}
		if withMailboxes && len(req.Domains) > 0 {
			if err := run(&moved.Mailboxes, `UPDATE mailboxes SET project_id = $3, updated_at = now()
				WHERE workspace_id = $1 AND domain = ANY($2::text[]) AND `+notSame, req.Domains); err != nil {
				return err
			}
		}
		if err := run(&moved.Mailboxes, `UPDATE mailboxes SET project_id = $3, updated_at = now()
			WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND `+notSame, req.MailboxIDs); err != nil {
			return err
		}
		if err := run(&moved.LeadLists, `UPDATE lead_lists SET project_id = $3
			WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND `+notSame, req.LeadListIDs); err != nil {
			return err
		}
		if err := run(&moved.Sequences, `UPDATE sequences SET project_id = $3, updated_at = now()
			WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND `+notSame, req.SequenceIDs); err != nil {
			return err
		}
		if moved == (projectMoved{}) {
			return nil
		}
		// Mailboxes and sequences changing project changes which mailbox
		// may serve which sequence.
		if moved.Mailboxes > 0 || moved.Sequences > 0 {
			if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
				return err
			}
		}
		action := "project.records_added"
		if !add {
			action = "project.records_removed"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "project", id, map[string]any{
			"domains": req.Domains, "mailbox_ids": req.MailboxIDs, "lead_list_ids": req.LeadListIDs,
			"sequence_ids": req.SequenceIDs, "moved": moved,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved})
}

// checkProjectRef checks that a project_id given on another record names a
// project in the workspace. Nil (shared) is always fine.
func checkProjectRef(ctx context.Context, tx pgx.Tx, ws string, id *string) error {
	if id == nil {
		return nil
	}
	var ok bool
	if validUUID(*id) {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2)`, *id, ws).Scan(&ok); err != nil {
			return err
		}
	}
	if !ok {
		return errInvalid("project not found")
	}
	return nil
}

// projectFilter reads ?project_id= for list endpoints: a project's id, or
// "none" for records shared by the workspace. A project token is always
// held to its own project.
func projectFilter(w http.ResponseWriter, r *http.Request) (string, bool) {
	a := accessFrom(r.Context())
	p := r.URL.Query().Get("project_id")
	if p != "" && p != "none" && !validUUID(p) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "project_id is a project's id, or none for shared records")
		return "", false
	}
	if a != nil && a.ProjectID != nil {
		if p != "" && p != *a.ProjectID {
			writeError(w, r, http.StatusNotFound, "not_found", "project not found")
			return "", false
		}
		p = *a.ProjectID
	}
	return strings.ToLower(p), true
}
