package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/track"
)

type salesGoogleCredentials struct {
	Grant oauth.PersonalGoogleGrant `json:"grant"`
}

func (s *Server) authorizeSalesGoogle(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	if m.Role != "sdr" && m.Role != "ae" {
		writeError(w, r, 403, "forbidden", "ask a manager to assign your Sales role first")
		return
	}
	if !s.crmGoogleConfigured() {
		writeError(w, r, 503, "unavailable", "configure the Google OAuth app before connecting Sales email")
		return
	}
	id, state, nonce, verifier := newID(), randomMailToken(), randomMailToken(), randomMailToken()
	expires := time.Now().Add(10 * time.Minute)
	digest := sha256.Sum256([]byte(state))
	var email string
	if err := s.pool.QueryRow(r.Context(), `SELECT email FROM users WHERE id=$1`, a.UserID).Scan(&email); s.allocError(w, r, err) {
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM sales_oauth_flows WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, a.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO sales_oauth_flows(id,workspace_id,brand_id,owner_id,session_id,state_hash,verifier_sealed,nonce,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, a.WorkspaceID, a.Brand, a.UserID, a.SessionID, hex.EncodeToString(digest[:]), s.box.Seal([]byte(verifier), []byte(id)), nonce, expires); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales.mail_consent_started", "sales_member", a.UserID, map[string]any{"brand_id": a.Brand})
	})
	if !s.allocError(w, r, err) {
		writeJSON(w, 200, map[string]any{"url": s.oauth.SalesGoogleURL(s.publicURL+"/v1/sales/google/callback", state, nonce, verifier, email), "expires_at": expires})
	}
}
func (s *Server) finishSalesGoogle(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.UserID == "" || p.TokenID != "" || p.ViewID != "" {
		writeError(w, r, 403, "forbidden", "use your own signed-in session")
		return
	}
	if !s.crmGoogleConfigured() {
		writeError(w, r, 503, "unavailable", "Google OAuth is not configured")
		return
	}
	var id, ws, brand, owner, nonce string
	var sealed []byte
	digest := sha256.Sum256([]byte(r.URL.Query().Get("state")))
	err := s.pool.QueryRow(r.Context(), `UPDATE sales_oauth_flows SET consumed_at=now() WHERE state_hash=$1 AND owner_id=$2 AND session_id=$3 AND consumed_at IS NULL AND expires_at>now() RETURNING id::text,workspace_id::text,brand_id::text,owner_id::text,nonce,verifier_sealed`, hex.EncodeToString(digest[:]), p.UserID, p.SessionID).Scan(&id, &ws, &brand, &owner, &nonce, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 400, "invalid_request", "consent expired; connect again from Sales → My email")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	done := func(code string) {
		if code != "connected" {
			track.Fail(r.Context(), s.log, track.Event{Name: "sales.mail_consent_failed", WorkspaceID: ws, ActorType: "human", Surface: "web", Props: map[string]any{"brand_id": brand, "reason": code}}, "invalid_request", errors.New("Sales Google consent was not completed"))
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, s.publicURL+s.brandAppPath(r.Context(), ws, brand, "crm/sales:email")+"?sales_google="+url.QueryEscape(code), 302)
	}
	if r.URL.Query().Get("error") != "" {
		done("denied")
		return
	}
	verifier, err := s.box.Open(sealed, []byte(id))
	if s.allocError(w, r, err) {
		return
	}
	grant, err := s.oauth.ExchangeSalesGoogle(r.Context(), r.URL.Query().Get("code"), s.publicURL+"/v1/sales/google/callback", nonce, string(verifier))
	if err != nil {
		done("consent_failed")
		return
	}
	var email string
	if err = s.pool.QueryRow(r.Context(), `SELECT lower(email) FROM users WHERE id=$1`, owner).Scan(&email); s.allocError(w, r, err) {
		return
	}
	if email != grant.Email {
		done("wrong_account")
		return
	}
	a := &access{principal: *p, WorkspaceID: ws, Brand: brand}
	connectionID := newID()
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, e := tx.Exec(r.Context(), `SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2 FOR UPDATE`, ws, owner); e != nil {
			return e
		}
		m, e := salesMemberAt(r.Context(), tx, a, owner)
		if e != nil {
			return e
		}
		if m.Role != "sdr" && m.Role != "ae" {
			return errDenied("your Sales role changed; ask your manager")
		}
		var valid bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1 AND user_id=$2 AND expires_at>now())`, p.SessionID, owner).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return errDenied("your session expired")
		}
		_, e = tx.Exec(r.Context(), `INSERT INTO sales_mail_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)ON CONFLICT(workspace_id,brand_id,owner_id)DO UPDATE SET id=$1,credentials_sealed=$5,status='connected',error_code=NULL,generation=sales_mail_connections.generation+1`, connectionID, ws, brand, owner, s.salesSeal(connectionID, salesGoogleCredentials{grant}))
		if e != nil {
			return e
		}
		if e = s.enqueueSales(r.Context(), tx, a, jobSalesSync, connectionID, time.Now()); e != nil {
			return e
		}
		return record(r.Context(), tx, p, ws, "sales.mail_connected", "sales_member", owner, map[string]any{"brand_id": brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	done("connected")
}
func (s *Server) disconnectSalesGoogle(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesPerson(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM sales_mail_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, a.UserID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales.mail_disconnected", "sales_member", a.UserID, map[string]any{"brand_id": a.Brand})
	})
	if !s.allocError(w, r, err) {
		writeJSON(w, 200, map[string]bool{"deleted": true})
	}
}
func (s *Server) queueSalesSync(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesPerson(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	var id string
	err := s.pool.QueryRow(r.Context(), `SELECT id::text FROM sales_mail_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND status='connected'`, a.WorkspaceID, a.Brand, a.UserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errInvalid("connect your own Sales email first")
	}
	if s.allocError(w, r, err) {
		return
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if e := s.enqueueSales(r.Context(), tx, a, jobSalesSync, id, time.Now()); e != nil {
			return e
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales.sync_requested", "sales_member", a.UserID, map[string]any{"brand_id": a.Brand})
	})
	if !s.allocError(w, r, err) {
		writeJSON(w, 200, map[string]bool{"queued": true})
	}
}
func (s *Server) salesGrant(ctx context.Context, a *access, owner string) (string, salesGoogleCredentials, string, error) {
	var id string
	var sealed []byte
	c := salesGoogleCredentials{}
	m, err := salesMemberAt(ctx, s.pool, a, owner)
	if err != nil {
		return id, c, "", err
	}
	if !m.Connected || m.Role != "sdr" && m.Role != "ae" {
		return id, c, "", errDenied("connect or reconnect your Sales email")
	}
	err = s.pool.QueryRow(ctx, `SELECT id::text,credentials_sealed FROM sales_mail_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND status='connected'`, a.WorkspaceID, a.Brand, owner).Scan(&id, &sealed)
	if err != nil {
		return id, c, "", err
	}
	if err = s.salesOpen(sealed, id, &c); err != nil {
		return id, c, "", err
	}
	token, refresh, err := s.oauth.RefreshSalesGoogle(ctx, c.Grant.RefreshToken)
	if err != nil {
		var denied *oauth.Error
		if errors.Is(err, oauth.ErrSalesConsent) || errors.As(err, &denied) && slices.Contains([]string{"invalid_grant", "unauthorized_client", "invalid_client", "access_denied", "consent_required", "interaction_required"}, denied.Code) {
			if _, saveErr := s.pool.Exec(ctx, `UPDATE sales_mail_connections SET status='reconnect_required',error_code='google_connection_failed' WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id); saveErr != nil {
				return id, c, "", saveErr
			}
		}
		return id, c, "", errors.New("Sales Google connection could not refresh")
	}
	if refresh != "" {
		c.Grant.RefreshToken = refresh
		if _, err = s.pool.Exec(ctx, `UPDATE sales_mail_connections SET credentials_sealed=$4 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id, s.salesSeal(id, c)); err != nil {
			return id, c, "", err
		}
	}
	return id, c, token, nil
}
