package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aiaccounts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/features"
)

// AI Accounts (plans/ai-accounts): the AI subscriptions and API accounts
// the workspace's people pay for. It is a workspace feature: every route
// answers 404 until it is switched on for the workspace.
//
// Who sees what: my_ai_accounts.* covers the accounts the caller belongs
// to, ai_accounts.* everyone's (and includes the former). The price on an
// account the caller doesn't belong to needs ai_spend.read.

func (s *Server) routesAIAccounts(mux *http.ServeMux) {
	read, manage := auth.ScopeMyAIAccountsRead, auth.ScopeMyAIAccountsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-accounts", s.inBrand(read, s.aiAccountsOn(s.listAIAccounts)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ai-accounts", s.inBrand(manage, s.aiAccountsOn(s.createAIAccount)))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-accounts/outflare", s.inBrand(auth.ScopeAISpendRead, s.aiAccountsOn(s.getOutflareAIAccount)))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-accounts/{id}", s.inBrand(read, s.aiAccountsOn(s.getAIAccount)))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/ai-accounts/{id}", s.inBrand(manage, s.aiAccountsOn(s.updateAIAccount)))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/ai-accounts/{id}", s.inBrand(manage, s.aiAccountsOn(s.deleteAIAccount)))
}

// aiAccountsOn answers 404 while AI Accounts is off for the workspace, as
// if the route didn't exist.
func (s *Server) aiAccountsOn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		on, err := features.Enabled(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, features.AIAccounts)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !on {
			writeError(w, r, http.StatusNotFound, "not_found", "AI Accounts isn't switched on for this workspace")
			return
		}
		next(w, r)
	}
}

// aiAccountAccess is what the caller may do with AI accounts.
type aiAccountAccess struct {
	me                string // the person behind the caller; empty for a token nobody owns
	seeAll, manageAll bool
	spend             bool
}

func aiAccess(a *access) aiAccountAccess {
	return aiAccountAccess{
		me:        a.personID(),
		seeAll:    auth.Grants(a.Scopes, auth.ScopeAIAccountsRead),
		manageAll: auth.Grants(a.Scopes, auth.ScopeAIAccountsManage),
		spend:     auth.Grants(a.Scopes, auth.ScopeAISpendRead),
	}
}

func (x aiAccountAccess) member(acc aiaccounts.Account) bool {
	for _, m := range acc.Members {
		if x.me != "" && m.UserID == x.me {
			return true
		}
	}
	return false
}

// shown hides the price on an account the caller doesn't belong to unless
// they may see spend.
func (x aiAccountAccess) shown(acc aiaccounts.Account) aiaccounts.Account {
	if !x.spend && !x.member(acc) && acc.PriceCents != nil {
		acc.PriceCents, acc.PriceHidden = nil, true
	}
	return acc
}

func (s *Server) listAIAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	x := aiAccess(a)
	f := aiaccounts.Filter{Provider: r.URL.Query().Get("provider")}
	if !x.seeAll || r.URL.Query().Get("mine") == "true" {
		if x.me == "" {
			writeJSON(w, http.StatusOK, map[string]any{"data": []aiaccounts.Account{}})
			return
		}
		f.UserID = x.me
	}
	list, err := aiaccounts.List(r.Context(), s.pool, a.WorkspaceID, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		list[i] = x.shown(list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// visibleAIAccount loads an account the caller may see, or writes 404.
func (s *Server) visibleAIAccount(w http.ResponseWriter, r *http.Request, x aiAccountAccess) (aiaccounts.Account, bool) {
	acc, err := aiaccounts.Get(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, r.PathValue("id"))
	if errors.Is(err, aiaccounts.ErrNotFound) || (err == nil && !x.seeAll && !x.member(acc)) {
		writeError(w, r, http.StatusNotFound, "not_found", aiaccounts.ErrNotFound.Error())
		return acc, false
	}
	if err != nil {
		s.internal(w, r, err)
		return acc, false
	}
	return acc, true
}

func (s *Server) getAIAccount(w http.ResponseWriter, r *http.Request) {
	x := aiAccess(accessFrom(r.Context()))
	acc, ok := s.visibleAIAccount(w, r, x)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, x.shown(acc))
}

func aiAccountProps(acc aiaccounts.Account) map[string]any {
	return map[string]any{"provider": acc.Provider, "paid_by": acc.PaidBy, "members": len(acc.Members), "cancelled": acc.Cancelled}
}

func (s *Server) createAIAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	x := aiAccess(a)
	var in aiaccounts.Input
	if !decode(w, r, &in) {
		return
	}
	if in.Members == nil && x.me != "" {
		in.Members = &[]aiaccounts.MemberInput{{UserID: x.me, Role: aiaccounts.RoleOwner}}
	}
	if err := in.Validate(true); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !x.manageAll && (in.Members == nil || !aiaccounts.HasMember(*in.Members, x.me)) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only add AI accounts you belong to: put yourself in members, or ask someone with ai_accounts.manage")
		return
	}
	var acc aiaccounts.Account
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		id, err := aiaccounts.Create(r.Context(), tx, a.WorkspaceID, in, nullIfEmpty(x.me))
		if err != nil {
			return err
		}
		if acc, err = aiaccounts.Get(r.Context(), tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ai_account.created", "ai_account", id,
			map[string]any{"provider": acc.Provider, "name": acc.Name}, aiAccountProps(acc))
	})
	if s.aiAccountWriteFailed(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, x.shown(acc))
}

func (s *Server) updateAIAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	x := aiAccess(a)
	cur, ok := s.visibleAIAccount(w, r, x)
	if !ok {
		return
	}
	if !x.manageAll && !x.member(cur) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only change AI accounts you belong to")
		return
	}
	var in aiaccounts.Input
	if !decode(w, r, &in) {
		return
	}
	if err := in.Validate(false); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !x.manageAll && in.Members != nil && !aiaccounts.HasMember(*in.Members, x.me) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can't take yourself off an AI account: ask someone with ai_accounts.manage")
		return
	}
	var acc aiaccounts.Account
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := aiaccounts.Update(r.Context(), tx, a.WorkspaceID, cur.ID, in); err != nil {
			return err
		}
		var err error
		if acc, err = aiaccounts.Get(r.Context(), tx, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ai_account.updated", "ai_account", cur.ID,
			map[string]any{"provider": acc.Provider, "name": acc.Name}, aiAccountProps(acc))
	})
	if s.aiAccountWriteFailed(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, x.shown(acc))
}

func (s *Server) deleteAIAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	x := aiAccess(a)
	cur, ok := s.visibleAIAccount(w, r, x)
	if !ok {
		return
	}
	if !x.manageAll && !x.member(cur) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only remove AI accounts you belong to")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		acc, err := aiaccounts.Delete(r.Context(), tx, a.WorkspaceID, cur.ID)
		if err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ai_account.deleted", "ai_account", cur.ID,
			map[string]any{"provider": acc.Provider, "name": acc.Name}, aiAccountProps(acc))
	})
	if s.aiAccountWriteFailed(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// aiAccountWriteFailed writes the error for a failed change, if any.
func (s *Server) aiAccountWriteFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, aiaccounts.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, aiaccounts.ErrNotMember):
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.internal(w, r, err)
	}
	return true
}

// outflareAIProvider is what Outflare's own AI calls spent this month with
// one provider, on the workspace's own key (bring your own keys).
type outflareAIProvider struct {
	Provider  string `json:"provider"`
	Calls     int64  `json:"calls"`
	CostCents int64  `json:"cost_cents"`
}

// getOutflareAIAccount is the AI account Outflare itself uses: what its
// agent and tools spent this month on the workspace's own AI keys, by
// provider (from ai_calls, as on the AI usage page).
func (s *Server) getOutflareAIAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	from := monthStart(time.Now())
	rows, err := s.pool.Query(r.Context(), `
		SELECT provider, count(*), COALESCE(sum(cost_cents), 0)::bigint FROM ai_calls
		WHERE workspace_id = $1 AND occurred_at >= $2 AND occurred_at < $3
		GROUP BY provider ORDER BY 3 DESC, 1`, a.WorkspaceID, from, from.AddDate(0, 1, 0))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[outflareAIProvider])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var calls, cents int64
	for _, p := range list {
		calls, cents = calls+p.Calls, cents+p.CostCents
	}
	writeJSON(w, http.StatusOK, map[string]any{"month": from.Format("2006-01"), "calls": calls, "cost_cents": cents, "providers": list})
}
