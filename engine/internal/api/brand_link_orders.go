package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/coverage"
	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

func (s *Server) routesLinkOrders(mux *http.ServeMux) {
	s.routesLinkOrderTargets(mux)
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/settings", s.onBrand(auth.ScopeCoverageRead, s.getLinkOrderSettings))
	mux.HandleFunc("PATCH /v1/brands/{brand}/link-orders/settings", s.onBrand(auth.ScopeCoverageApprove, s.updateLinkOrderSettings))

	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/catalog", s.onBrand(auth.ScopeCoverageRead, s.listLinkOrderCatalog))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/catalog", s.onBrand(auth.ScopeCoverageManage, s.createLinkOrderCatalog))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/catalog/import", s.onBrand(auth.ScopeCoverageManage, s.importLinkOrderCatalog))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/catalog/items/{id}", s.onBrand(auth.ScopeCoverageRead, s.getLinkOrderCatalogItem))
	mux.HandleFunc("PATCH /v1/brands/{brand}/link-orders/catalog/items/{id}", s.onBrand(auth.ScopeCoverageManage, s.updateLinkOrderCatalogItem))

	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/summary", s.onBrand(auth.ScopeCoverageRead, s.getLinkOrderSummary))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/budget", s.onBrand(auth.ScopeCoverageFinanceRead, s.getLinkOrderBudget))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/candidates", s.onBrand(auth.ScopeCoverageRead, s.listLinkOrderCandidates))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/candidates/{domain}/add-to-catalog", s.onBrand(auth.ScopeCoverageManage, s.addLinkOrderCandidateToCatalog))

	mux.HandleFunc("GET /v1/brands/{brand}/link-orders", s.onBrand(auth.ScopeCoverageRead, s.listLinkOrders))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders", s.onBrand(auth.ScopeCoverageManage, s.createLinkOrder))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/{id}", s.onBrand(auth.ScopeCoverageRead, s.getLinkOrder))
	mux.HandleFunc("PATCH /v1/brands/{brand}/link-orders/{id}", s.onBrand(auth.ScopeCoverageManage, s.updateLinkOrder))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/{id}/history", s.onBrand(auth.ScopeCoverageRead, s.listLinkOrderHistory))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/{id}/equivalents", s.onBrand(auth.ScopeCoverageRead, s.listLinkOrderEquivalents))
	mux.HandleFunc("GET /v1/brands/{brand}/link-orders/{id}/impact", s.onBrand(auth.ScopeCoverageRead, s.getLinkOrderImpact))
	mux.HandleFunc("GET /v1/brands/{brand}/link-roi", s.onBrand(auth.ScopeCoverageRead, s.getLinkROI))

	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/quote", s.onBrand(auth.ScopeCoverageManage, s.quoteLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/negotiate", s.onBrand(auth.ScopeCoverageManage, s.negotiateLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/approve", s.onBrand(auth.ScopeCoverageApprove, s.approveLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/publish", s.onBrand(auth.ScopeCoverageManage, s.publishLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/check", s.onBrand(auth.ScopeCoverageManage, s.checkLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/pay", s.onBrand(auth.ScopeCoverageApprove, s.payLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/verify", s.onBrand(auth.ScopeCoverageManage, s.verifyLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/lost", s.onBrand(auth.ScopeCoverageManage, s.loseLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/replace", s.onBrand(auth.ScopeCoverageManage, s.replaceLinkOrder))
	mux.HandleFunc("POST /v1/brands/{brand}/link-orders/{id}/credit", s.onBrand(auth.ScopeCoverageApprove, s.creditLinkOrder))
}

func (s *Server) getLinkOrderSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := linkorders.LoadSettings(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateLinkOrderSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		ApproverUserID   json.RawMessage `json:"approver_user_id"`
		EquivalentDRBand *int            `json:"equivalent_dr_band"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	before, err := linkorders.LoadSettings(ctx, s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	after := before
	if len(in.ApproverUserID) > 0 {
		id, ok := parseOptionalUUID(w, r, in.ApproverUserID, "approver_user_id")
		if !ok {
			return
		}
		if id != nil {
			if err := s.ensureLinkOrderApprover(ctx, a.WorkspaceID, a.Brand, *id); err != nil {
				if errors.Is(err, errLinkOrderApprover) {
					writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
					return
				}
				s.internal(w, r, err)
				return
			}
		}
		after.ApproverUserID = id
	}
	if in.EquivalentDRBand != nil {
		if *in.EquivalentDRBand < 1 || *in.EquivalentDRBand > 50 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "equivalent_dr_band must be 1 to 50")
			return
		}
		after.EquivalentDRBand = *in.EquivalentDRBand
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := linkorders.SaveSettings(ctx, tx, a.WorkspaceID, a.Brand, after, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		data := map[string]any{"brand_id": a.Brand, "equivalent_dr_band": after.EquivalentDRBand}
		if after.ApproverUserID != nil {
			data["approver_set"] = true
		} else {
			data["approver_set"] = false
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "link_orders.settings_updated", "brand", a.Brand, data, data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := linkorders.LoadSettings(ctx, s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

var errLinkOrderApprover = errors.New("approver_user_id must be a member of this workspace who can approve coverage")

func (s *Server) ensureLinkOrderApprover(ctx context.Context, workspaceID, brandID, userID string) error {
	var role string
	var brands, stored []string
	var disabled bool
	err := s.pool.QueryRow(ctx, `SELECT m.role, m.brand_ids::text[], `+memberRoleScopes+`, u.disabled_at IS NOT NULL
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.user_id = $2`, workspaceID, userID).Scan(&role, &brands, &stored, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return errLinkOrderApprover
	}
	if err != nil {
		return err
	}
	scopes := effectiveScopes(role, stored)
	if disabled || !auth.Grants(scopes, auth.ScopeCoverageApprove) {
		return errLinkOrderApprover
	}
	if auth.BrandLimitable(scopes) && len(brands) > 0 && !slices.Contains(brands, brandID) {
		return errLinkOrderApprover
	}
	return nil
}

func parseOptionalUUID(w http.ResponseWriter, r *http.Request, raw json.RawMessage, field string) (*string, bool) {
	trim := strings.TrimSpace(string(raw))
	if trim == "null" {
		return nil, true
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || !validUUID(id) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", field+" must be a UUID or null")
		return nil, false
	}
	return &id, true
}

func parseLinkOrderIDs(a *access) (ws, brand, actor uuid.UUID, err error) {
	ws, err = uuid.Parse(a.WorkspaceID)
	if err != nil {
		return
	}
	brand, err = uuid.Parse(a.Brand)
	if err != nil {
		return
	}
	if a.ActorID != "" {
		actor, err = uuid.Parse(a.ActorID)
	}
	return
}

func parsePathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.PathValue(name)
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) writeLinkOrderError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, linkorders.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, linkorders.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, coverage.ErrNoURL):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "published_url must be an http(s) address")
	case errors.Is(err, linkorders.ErrForbidden), errors.Is(err, linkorders.ErrNotApprover):
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, linkorders.ErrConflict),
		errors.Is(err, linkorders.ErrOverBudget),
		errors.Is(err, linkorders.ErrNoApprover),
		errors.Is(err, linkorders.ErrNotLive),
		errors.Is(err, linkorders.ErrNotPublished),
		errors.Is(err, linkorders.ErrAmountFrozen),
		errors.Is(err, linkorders.ErrOpenReplacement),
		errors.Is(err, linkorders.ErrReplaceNotLost),
		errors.Is(err, linkorders.ErrReplaceCycle):
		writeError(w, r, http.StatusConflict, "conflict", err.Error())
	default:
		s.internal(w, r, err)
	}
}

func (s *Server) requireLinkOrderApprover(w http.ResponseWriter, r *http.Request, a *access, finance bool) bool {
	if a.ActorType != "human" || a.UserID == "" {
		writeError(w, r, http.StatusForbidden, "forbidden", linkorders.ErrNotApprover.Error())
		return false
	}
	if finance && !a.has(auth.ScopeCoverageFinanceManage) {
		writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, auth.ScopeCoverageFinanceManage))
		return false
	}
	set, err := linkorders.LoadSettings(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if set.ApproverUserID == nil {
		writeError(w, r, http.StatusConflict, "conflict", linkorders.ErrNoApprover.Error())
		return false
	}
	if *set.ApproverUserID != a.UserID {
		writeError(w, r, http.StatusForbidden, "forbidden", linkorders.ErrNotApprover.Error())
		return false
	}
	return true
}

func linkOrderCursor(w http.ResponseWriter, r *http.Request, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, table string, workspaceID, brandID uuid.UUID) (*linkorders.Cursor, bool) {
	before, ok := pageCursor(w, r, pageBefore)
	if !ok {
		return nil, false
	}
	if before == "" {
		return nil, true
	}
	if !validUUID(before) {
		invalidCursor(w, r)
		return nil, false
	}
	var cur linkorders.Cursor
	err := q.QueryRow(r.Context(), `SELECT created_at, id FROM `+table+` WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, before, workspaceID, brandID).Scan(&cur.At, &cur.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		invalidCursor(w, r)
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return &cur, true
}

func writeLinkOrderList(w http.ResponseWriter, list any, next *string) {
	if list == nil {
		list = []any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}
