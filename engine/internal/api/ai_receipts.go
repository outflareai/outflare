package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aiaccounts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/inbox"
)

// AI receipts (plans/ai-accounts, step 3): receipts people forward to the
// workspace's inbound address are read and matched to an AI account
// (aiaccounts.ApplyReceipt); the rest wait under Needs review.

// handleAIReceipt is inbound email's handler for the ai_receipts purpose.
func (s *Server) handleAIReceipt(ctx context.Context, tx pgx.Tx, e inboundemail.Email) (string, string, error) {
	p, err := inbox.Parse(e.Raw)
	if err != nil {
		return "rejected", "unreadable", nil
	}
	r, ok := aiaccounts.ReadReceipt(aiaccounts.Message{From: e.From, To: p.To, Subject: e.Subject, Body: p.Body, Date: p.Date, DKIM: e.DKIM})
	if !ok {
		return "ignored", "not_a_receipt", nil
	}
	applied, err := aiaccounts.ApplyReceipt(ctx, tx, e.WorkspaceID, e.ID, e.ForwarderID, r)
	if err != nil {
		return "", "", err
	}
	return "processed", applied.Status, nil
}

func (s *Server) routesAIReceipts(mux *http.ServeMux) {
	s.routesInboundForwarders(mux)
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-accounts/receipts", s.inBrand(auth.ScopeAISpendRead, s.aiAccountsOn(s.listAIReceipts)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ai-accounts/receipts/{id}/assign", s.inBrand(auth.ScopeAIAccountsManage, s.aiAccountsOn(s.assignAIReceipt)))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ai-accounts/receipts/{id}/ignore", s.inBrand(auth.ScopeAIAccountsManage, s.aiAccountsOn(s.ignoreAIReceipt)))
}

type aiReceiptView struct {
	ID           string    `json:"id"`
	Vendor       string    `json:"vendor"`
	Provider     *string   `json:"provider"`
	ForwardedBy  *string   `json:"forwarded_by"`
	ChargedOn    *string   `json:"charged_on"`
	AmountMinor  *int64    `json:"amount_minor"`
	Currency     *string   `json:"currency"`
	USDCents     *int64    `json:"usd_cents"`
	Plan         *string   `json:"plan"`
	Seats        *int      `json:"seats"`
	PeriodStart  *string   `json:"period_start"`
	PeriodEnd    *string   `json:"period_end"`
	AccountEmail *string   `json:"account_email"`
	AccountID    *string   `json:"account_id"`
	AccountName  *string   `json:"account_name"`
	MatchedBy    *string   `json:"matched_by"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// listAIReceipts is the latest 200 receipts, or those with status.
func (s *Server) listAIReceipts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "matched" && status != "needs_review" && status != "ignored" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be matched, needs_review or ignored")
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT x.id::text, x.vendor, x.provider, u.email, x.charged_on::text, x.amount_minor, x.currency, x.usd_cents, x.plan, x.seats,
			x.period_start::text, x.period_end::text, x.account_email, x.account_id::text, a.name, x.matched_by, x.status, x.created_at
		FROM ai_receipts x LEFT JOIN users u ON u.id = x.forwarded_by LEFT JOIN ai_accounts a ON a.id = x.account_id
		WHERE x.workspace_id = $1 AND ($2 = '' OR x.status = $2)
		ORDER BY x.created_at DESC LIMIT 200`, a.WorkspaceID, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (aiReceiptView, error) {
		var v aiReceiptView
		return v, row.Scan(&v.ID, &v.Vendor, &v.Provider, &v.ForwardedBy, &v.ChargedOn, &v.AmountMinor, &v.Currency, &v.USDCents, &v.Plan, &v.Seats,
			&v.PeriodStart, &v.PeriodEnd, &v.AccountEmail, &v.AccountID, &v.AccountName, &v.MatchedBy, &v.Status, &v.CreatedAt)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) assignAIReceipt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID string `json:"account_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.AccountID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is required: the AI account this receipt is for (POST .../ai-accounts first to add a new one)")
		return
	}
	s.resolveAIReceipt(w, r, req.AccountID)
}

func (s *Server) ignoreAIReceipt(w http.ResponseWriter, r *http.Request) {
	s.resolveAIReceipt(w, r, "")
}

func (s *Server) resolveAIReceipt(w http.ResponseWriter, r *http.Request, accountID string) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		rec, err := aiaccounts.ResolveReceipt(r.Context(), tx, a.WorkspaceID, id, accountID, a.personID())
		if err != nil {
			return err
		}
		action, props := "ai_receipt.ignored", map[string]any{"vendor": rec.Vendor, "provider": rec.Provider}
		if accountID != "" {
			action = "ai_receipt.assigned"
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, action, "ai_receipt", id, map[string]any{"account_id": accountID}, props)
	})
	switch {
	case errors.Is(err, aiaccounts.ErrReceiptNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, aiaccounts.ErrNotFound):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is not an AI account in this workspace")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": map[bool]string{true: "matched", false: "ignored"}[accountID != ""]})
	}
}
