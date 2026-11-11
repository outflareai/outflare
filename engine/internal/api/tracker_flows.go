package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Flows (plans/ad-tracker.md, T6c). A flow is a brand's reusable set of
// rule-based paths and default destinations (pages, offers and landers),
// shared by any number of campaign links: one edit changes every link that
// uses it. Its paths and destinations are rows of the same tables as a
// link's, owned by the flow (routeOwner). A link given a flow routes by the
// flow's paths and destinations instead of its own, which are kept for when
// the flow is taken off again.

const maxFlows = 500

func (s *Server) routesTrackerFlows(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/flows", s.onBrand(read, s.listFlows))
	mux.HandleFunc("POST /v1/brands/{brand}/flows", s.onBrand(manage, s.createFlow))
	mux.HandleFunc("GET /v1/brands/{brand}/flows/{id}", s.onBrand(read, s.getFlow))
	mux.HandleFunc("PATCH /v1/brands/{brand}/flows/{id}", s.onBrand(manage, s.updateFlow))
	mux.HandleFunc("PUT /v1/brands/{brand}/flows/{id}/destinations", s.onBrand(manage, s.putFlowDestinations))
	mux.HandleFunc("PUT /v1/brands/{brand}/flows/{id}/paths", s.onBrand(manage, s.putFlowPaths))
	link := "/v1/brands/{brand}/tracking-campaigns/{id}/links/{link}/flow"
	mux.HandleFunc("GET "+link, s.onBrand(read, s.getLinkFlow))
	mux.HandleFunc("PUT "+link, s.onBrand(manage, s.putLinkFlow))
}

type trackerFlowView struct {
	ID      string `json:"id"`
	BrandID string `json:"brand_id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	// Links is how many campaign links route by it, so an edit can say
	// what it changes.
	Links int `json:"links"`
	// Paths and Destinations are its rule-based paths, in order, and its
	// default destinations, as a link's are.
	Paths        []linkPathView        `json:"paths"`
	Destinations []linkDestinationView `json:"destinations"`
	// NeedsDomain says the brand has no verified tracking domain, so the
	// flow isn't used.
	NeedsDomain bool      `json:"needs_domain"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const flowSelect = `
	SELECT f.id::text, f.brand_id::text, f.name, f.status, (SELECT count(*) FROM tracking_links l WHERE l.flow_id = f.id)::int, f.created_at, f.updated_at
	FROM tracker_flows f WHERE f.workspace_id = $1 AND f.brand_id = $2`

func scanFlow(row pgx.Row) (trackerFlowView, error) {
	var v trackerFlowView
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Status, &v.Links, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// withRouting fills in a flow's paths and destinations.
func (s *Server) withRouting(ctx context.Context, ws string, v *trackerFlowView) error {
	var err error
	if v.Paths, err = s.pathViews(ctx, ws, flowOwner(v.ID)); err != nil {
		return err
	}
	v.Destinations, err = s.destinationViews(ctx, ws, flowOwner(v.ID), nil)
	return err
}

func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = "active"
	case "active", "archived", "all":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, archived or all")
		return
	}
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, flowSelect+` AND ($3 = 'all' OR f.status = $3) ORDER BY lower(f.name)`, a.WorkspaceID, a.Brand, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackerFlowView, error) { return scanFlow(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	needs, err := s.brandNeedsDomain(ctx, a.WorkspaceID, a.Brand)
	for i := range list {
		if err == nil {
			list[i].NeedsDomain = needs
			err = s.withRouting(ctx, a.WorkspaceID, &list[i])
		}
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []trackerFlowView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findFlow finds one of the brand's flows.
func (s *Server) findFlow(ctx context.Context, id string) (trackerFlowView, error) {
	a := accessFrom(ctx)
	if !validUUID(id) {
		return trackerFlowView{}, errNotFound
	}
	v, err := scanFlow(s.pool.QueryRow(ctx, flowSelect+` AND f.id = $3`, a.WorkspaceID, a.Brand, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) writeFlow(w http.ResponseWriter, r *http.Request, status int, id string) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, err := s.findFlow(ctx, id)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "flow not found")
		return
	}
	if err == nil {
		err = s.withRouting(ctx, a.WorkspaceID, &v)
	}
	if err == nil {
		v.NeedsDomain, err = s.brandNeedsDomain(ctx, a.WorkspaceID, a.Brand)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) getFlow(w http.ResponseWriter, r *http.Request) {
	s.writeFlow(w, r, http.StatusOK, r.PathValue("id"))
}

type trackerFlowInput struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
}

func (in *trackerFlowInput) check() error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 120 {
			return errInvalid("name is 1 to 120 characters")
		}
		in.Name = &n
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "archived" {
		return errInvalid("status is active or archived")
	}
	return nil
}

func (s *Server) flowSaveError(w http.ResponseWriter, r *http.Request, err error) bool {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "flow not found")
		return true
	case errors.As(err, &pe) && pe.Code == "23505":
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a flow with that name")
		return true
	}
	return s.allocError(w, r, err)
}

// createFlow makes a flow, with its paths and default destinations if
// given.
func (s *Server) createFlow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		trackerFlowInput
		Paths        []linkPathInput        `json:"paths"`
		Destinations []linkDestinationInput `json:"destinations"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Name == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	case in.Status != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new flow is active; archive it with PATCH")
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	conditions, err := checkPaths(in.Paths)
	if s.allocError(w, r, err) {
		return
	}
	if _, err := checkDestinations("destinations", in.Destinations); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracker_flows WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxFlows {
			return errInvalid("a brand has at most 500 flows; archive old ones")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tracker_flows (workspace_id, brand_id, name, created_by) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			a.WorkspaceID, a.Brand, *in.Name, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if _, err := savePaths(ctx, tx, a.WorkspaceID, flowOwner(id), in.Paths); err != nil {
			return err
		}
		if _, err := saveDestinations(ctx, tx, a.WorkspaceID, flowOwner(id), nil, "destinations", in.Destinations); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracker_flow.created", "tracker_flow", id,
			map[string]any{"brand_id": a.Brand, "paths": len(in.Paths), "conditions": conditions, "destinations": len(in.Destinations)})
	})
	if s.flowSaveError(w, r, err) {
		return
	}
	s.writeFlow(w, r, http.StatusCreated, id)
}

// updateFlow renames, archives or restores a flow; archiving is refused
// while a link routes by it.
func (s *Server) updateFlow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in trackerFlowInput
	if !decode(w, r, &in) {
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	ctx := r.Context()
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var was string
			var links int
			err := tx.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM tracking_links l WHERE l.flow_id = f.id)
				FROM tracker_flows f WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`, a.WorkspaceID, a.Brand, id).Scan(&was, &links)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if in.Status != nil && *in.Status == "archived" && was != "archived" && links > 0 {
				return errInvalid("the flow routes " + strconv.Itoa(links) + " link(s); take it off them before archiving it")
			}
			if _, err := tx.Exec(ctx, `UPDATE tracker_flows SET name = COALESCE($3, name), status = COALESCE($4, status), updated_at = now()
				WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, in.Name, in.Status); err != nil {
				return err
			}
			action := "tracker_flow.updated"
			if in.Status != nil && *in.Status != was {
				action = map[string]string{"archived": "tracker_flow.archived", "active": "tracker_flow.restored"}[*in.Status]
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, action, "tracker_flow", id, map[string]any{"brand_id": a.Brand, "links": links})
		})
	}
	if s.flowSaveError(w, r, err) {
		return
	}
	s.writeFlow(w, r, http.StatusOK, id)
}

// putFlowDestinations replaces a flow's default destinations, for every
// link that routes by it.
func (s *Server) putFlowDestinations(w http.ResponseWriter, r *http.Request) {
	s.putFlowRouting(w, r, "destinations")
}

// putFlowPaths replaces a flow's paths, for every link that routes by it.
func (s *Server) putFlowPaths(w http.ResponseWriter, r *http.Request) {
	s.putFlowRouting(w, r, "paths")
}

func (s *Server) putFlowRouting(w http.ResponseWriter, r *http.Request, what string) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	f, err := s.findFlow(ctx, r.PathValue("id"))
	if s.flowSaveError(w, r, err) {
		return
	}
	var in struct {
		Paths        []linkPathInput        `json:"paths"`
		Destinations []linkDestinationInput `json:"destinations"`
	}
	if !decode(w, r, &in) {
		return
	}
	props := map[string]any{"brand_id": a.Brand, "links": f.Links}
	if what == "paths" {
		n, err := checkPaths(in.Paths)
		if s.allocError(w, r, err) {
			return
		}
		props["paths"], props["conditions"] = len(in.Paths), n
	} else {
		capped, err := checkDestinations("destinations", in.Destinations)
		if s.allocError(w, r, err) {
			return
		}
		props["destinations"], props["capped"] = len(in.Destinations), capped
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var removed int
		var err error
		if what == "paths" {
			removed, err = savePaths(ctx, tx, a.WorkspaceID, flowOwner(f.ID), in.Paths)
		} else {
			removed, err = saveDestinations(ctx, tx, a.WorkspaceID, flowOwner(f.ID), nil, "destinations", in.Destinations)
		}
		if err != nil {
			return err
		}
		props["removed"] = removed
		if _, err := tx.Exec(ctx, `UPDATE tracker_flows SET updated_at = now() WHERE id = $1`, f.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracker_flow."+what+"_updated", "tracker_flow", f.ID, props)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	s.writeFlow(w, r, http.StatusOK, f.ID)
}

// A link's flow.

type linkFlowView struct {
	LinkID string `json:"link_id"`
	// FlowID is the flow the link routes by; nil for its own paths and
	// destinations.
	FlowID *string `json:"flow_id"`
}

func (s *Server) getLinkFlow(w http.ResponseWriter, r *http.Request) {
	_, link, _, _, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	v := linkFlowView{LinkID: link}
	if err := s.pool.QueryRow(r.Context(), `SELECT flow_id::text FROM tracking_links WHERE id = $1`, link).Scan(&v.FlowID); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putLinkFlow makes a link route by one of its brand's active flows, or
// by its own paths and destinations again (flow_id null).
func (s *Server) putLinkFlow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, link, _, code, ok := s.findRedirectLink(w, r)
	if !ok {
		return
	}
	var in struct {
		FlowID *string `json:"flow_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.FlowID != nil && !validUUID(*in.FlowID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "flow_id is one of the brand's active flows, or null for the link's own paths and destinations")
		return
	case in.FlowID != nil && code == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a flow routes a redirect link's clicks, and this link has none; make it again with redirect: true first")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if in.FlowID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tracker_flows f, `+linkOwner(link).source()+`
					WHERE f.workspace_id = o.w AND f.brand_id = o.b AND f.id = $3 AND f.status = 'active')`, a.WorkspaceID, link, *in.FlowID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("flow_id isn't one of the brand's active flows")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE tracking_links SET flow_id = $3 WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, link, in.FlowID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_link.flow_set", "tracking_link", link,
			map[string]any{"campaign_id": c.ID, "flow_id": in.FlowID})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	writeJSON(w, http.StatusOK, linkFlowView{LinkID: link, FlowID: in.FlowID})
}
