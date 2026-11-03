package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/accounts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/browser"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Human takeover and offboarding (docs/accounts.md). A takeover is what
// the tools ask for when a managed use of an account meets MFA, a
// CAPTCHA, a passkey or a consent screen: short-lived (15 minutes unless
// the person asks for up to an hour), account-scoped, exclusive (one
// waiting or under way per account), and the identity signed in is
// checked again on handback through an attempt, so a wrong identity
// blocks the account like any other. Codes are never shown or stored.
// Offboarding a brand's accounts archives them, empties their vault,
// cancels takeovers, resolves incidents, revokes the brand's grants and
// lists what still needs an owner's action at the platforms.

var (
	takeoverReasons  = []string{"mfa", "captcha", "passkey", "consent", "other"}
	takeoverStatuses = []string{"requested", "active", "handed_back", "cancelled", "expired"}
)

// errTakeoverState is a change the takeover's or the account's state
// doesn't allow: 409.
type errTakeoverState string

func (e errTakeoverState) Error() string { return string(e) }

func (s *Server) routesAccountTakeovers(mux *http.ServeMux, read, manage string) {
	mux.HandleFunc("GET /v1/brands/{brand}/account-takeovers", s.onBrand(read, s.listAccountTakeovers))
	mux.HandleFunc("POST /v1/brands/{brand}/accounts/{id}/takeovers", s.onBrand(manage, s.requestAccountTakeover))
	mux.HandleFunc("GET /v1/brands/{brand}/account-takeovers/{id}", s.onBrand(read, s.getAccountTakeover))
	mux.HandleFunc("POST /v1/brands/{brand}/account-takeovers/{id}/start", s.onBrand(manage, s.startAccountTakeover))
	mux.HandleFunc("POST /v1/brands/{brand}/account-takeovers/{id}/handback", s.onBrand(manage, s.handBackAccountTakeover))
	mux.HandleFunc("POST /v1/brands/{brand}/account-takeovers/{id}/cancel", s.onBrand(manage, s.cancelAccountTakeover))
	mux.HandleFunc("POST /v1/brands/{brand}/accounts/offboard", s.onBrand(manage, s.offboardAccounts))
}

type accountTakeoverView struct {
	ID              string     `json:"id"`
	AccountID       string     `json:"account_id"`
	IncidentID      *string    `json:"incident_id"`
	Reason          string     `json:"reason"`
	Note            string     `json:"note"`
	Status          string     `json:"status"`
	RequestedBy     *string    `json:"requested_by"`
	RequestedAt     time.Time  `json:"requested_at"`
	HolderID        *string    `json:"holder_id"`
	StartedAt       *time.Time `json:"started_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
	EndedAt         *time.Time `json:"ended_at"`
	HandbackOutcome string     `json:"handback_outcome"`
	HandbackNote    string     `json:"handback_note"`
}

const takeoverCols = `t.id::text, t.account_id::text, t.incident_id::text, t.reason, t.note, t.status, t.requested_by::text, t.requested_at, t.holder_id::text, t.started_at, t.expires_at, t.ended_at, t.handback_outcome, t.handback_note`

func scanTakeover(row pgx.CollectableRow) (accountTakeoverView, error) {
	var v accountTakeoverView
	err := row.Scan(&v.ID, &v.AccountID, &v.IncidentID, &v.Reason, &v.Note, &v.Status, &v.RequestedBy, &v.RequestedAt, &v.HolderID, &v.StartedAt, &v.ExpiresAt, &v.EndedAt, &v.HandbackOutcome, &v.HandbackNote)
	return v, err
}

// expireTakeovers ends the brand's takeovers whose time ran out, so a
// person who forgot never holds an account for good and the next request
// can go through. It runs before every read and change.
func expireTakeovers(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, ws, brand string) error {
	_, err := q.Exec(ctx, `UPDATE brand_account_takeovers SET status = 'expired', ended_at = expires_at
		WHERE workspace_id = $1 AND brand_id = $2 AND status = 'active' AND expires_at < now()`, ws, brand)
	return err
}

func (s *Server) loadTakeover(ctx context.Context, a *access, id string) (accountTakeoverView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+takeoverCols+` FROM brand_account_takeovers t WHERE t.id = $1 AND t.workspace_id = $2 AND t.brand_id = $3`, id, a.WorkspaceID, a.Brand)
	if err != nil {
		return accountTakeoverView{}, err
	}
	v, err := pgx.CollectOneRow(rows, scanTakeover)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) writeTakeover(w http.ResponseWriter, r *http.Request, a *access, id string, status int) {
	v, err := s.loadTakeover(r.Context(), a, id)
	if s.takeoverError(w, r, err) {
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) takeoverError(w http.ResponseWriter, r *http.Request, err error) bool {
	var st errTakeoverState
	if errors.As(err, &st) {
		writeError(w, r, http.StatusConflict, "conflict", string(st))
		return true
	}
	return s.allocError(w, r, err)
}

func (s *Server) listAccountTakeovers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if err := expireTakeovers(ctx, s.pool, a.WorkspaceID, a.Brand); err != nil {
		s.internal(w, r, err)
		return
	}
	where := `t.workspace_id = $1 AND t.brand_id = $2`
	args := []any{a.WorkspaceID, a.Brand}
	switch v := r.URL.Query().Get("status"); {
	case v == "":
		where += ` AND t.status IN ('requested', 'active')`
	case v == "all":
	case slices.Contains(takeoverStatuses, v):
		where += ` AND t.status = $3`
		args = append(args, v)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is requested, active, handed_back, cancelled, expired or all")
		return
	}
	if id := r.URL.Query().Get("account_id"); id != "" {
		if !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is an account's id")
			return
		}
		args = append(args, id)
		where += ` AND t.account_id = $` + strconv.Itoa(len(args))
	}
	rows, err := s.pool.Query(ctx, `SELECT `+takeoverCols+` FROM brand_account_takeovers t WHERE `+where+` ORDER BY t.status = 'requested' DESC, t.requested_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanTakeover)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []accountTakeoverView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) getAccountTakeover(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "takeover not found")
		return
	}
	if err := expireTakeovers(r.Context(), s.pool, a.WorkspaceID, a.Brand); err != nil {
		s.internal(w, r, err)
		return
	}
	s.writeTakeover(w, r, a, id, http.StatusOK)
}

// requestAccountTakeover is what a tool (or a person) calls when a use of
// the account needs a person: it opens the request and the incident it
// is for, if any, stays as it is. One per account at a time.
func (s *Server) requestAccountTakeover(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	accountID := r.PathValue("id")
	if !validUUID(accountID) {
		writeError(w, r, http.StatusNotFound, "not_found", "account not found")
		return
	}
	var req struct {
		Reason     string  `json:"reason"`
		Note       string  `json:"note"`
		IncidentID *string `json:"incident_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case !slices.Contains(takeoverReasons, req.Reason):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is mfa, captcha, passkey, consent or other")
		return
	case len(req.Note) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 1000 characters: what the person will see, never a code")
		return
	case req.IncidentID != nil && !validUUID(*req.IncidentID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "incident_id is an incident's id")
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expireTakeovers(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM brand_accounts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, accountID, a.WorkspaceID, a.Brand).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status == "archived" {
			return errTakeoverState("the account is archived")
		}
		if req.IncidentID != nil {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_account_incidents WHERE id = $1 AND account_id = $2`, *req.IncidentID, accountID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return errInvalid("incident_id is one of the account's incidents")
			}
		}
		var open string
		if err := tx.QueryRow(ctx, `SELECT status FROM brand_account_takeovers WHERE account_id = $1 AND status IN ('requested', 'active') LIMIT 1`, accountID).Scan(&open); err == nil {
			if open == "active" {
				return errTakeoverState("a person holds the account already")
			}
			return errTakeoverState("a takeover is already waiting for a person")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO brand_account_takeovers (workspace_id, brand_id, account_id, incident_id, reason, note, requested_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, a.WorkspaceID, a.Brand, accountID, req.IncidentID, req.Reason, strings.TrimSpace(req.Note), actorRef(a)).Scan(&id); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "account_id": accountID, "reason": req.Reason}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.takeover_requested", "account_takeover", id, props, props)
	})
	if s.takeoverError(w, r, err) {
		return
	}
	metrics.AccountTakeover("requested")
	s.writeTakeover(w, r, a, id, http.StatusCreated)
}

// startAccountTakeover is a person taking the account: from now, for
// `minutes` (15 unless given, at most 60), nothing else uses it.
func (s *Server) startAccountTakeover(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "takeover not found")
		return
	}
	var req struct {
		Minutes int `json:"minutes"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if req.Minutes == 0 {
		req.Minutes = 15
	}
	if req.Minutes < 1 || req.Minutes > 60 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "minutes is 1 to 60: a takeover is short")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if a.ActorType != "human" || a.UserID == "" {
			return errDenied("only a person takes an account over")
		}
		if err := expireTakeovers(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var status, accountID string
		err := tx.QueryRow(ctx, `SELECT status, account_id::text FROM brand_account_takeovers WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, id, a.WorkspaceID, a.Brand).Scan(&status, &accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != "requested" {
			return errTakeoverState("the takeover is " + status)
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_account_takeovers SET status = 'active', holder_id = $2, started_at = now(), expires_at = now() + make_interval(mins => $3) WHERE id = $1`, id, a.UserID, req.Minutes); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "account_id": accountID, "minutes": req.Minutes}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.takeover_started", "account_takeover", id, props, props)
	})
	if s.takeoverError(w, r, err) {
		return
	}
	metrics.AccountTakeover("started")
	s.writeTakeover(w, r, a, id, http.StatusOK)
}

// handBackAccountTakeover ends a takeover the person holds. The identity
// they left signed in, when given, is checked against the account's and
// recorded as an attempt: the brand's own identity makes the account
// healthy and resolves what it waited for; another identity blocks it.
func (s *Server) handBackAccountTakeover(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "takeover not found")
		return
	}
	var req struct {
		Identity *string `json:"identity"`
		Note     string  `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if len(req.Note) > 1000 || (req.Identity != nil && len(*req.Identity) > 320) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 1000 characters and identity at most 320")
		return
	}
	ctx := r.Context()
	var outcome string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if a.ActorType != "human" || a.UserID == "" {
			return errDenied("only a person hands an account back")
		}
		if err := expireTakeovers(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var status, accountID, identity string
		var holder *string
		err := tx.QueryRow(ctx, `SELECT t.status, t.account_id::text, t.holder_id::text, a.identity FROM brand_account_takeovers t JOIN brand_accounts a ON a.id = t.account_id
			WHERE t.id = $1 AND t.workspace_id = $2 AND t.brand_id = $3 FOR UPDATE OF t`, id, a.WorkspaceID, a.Brand).Scan(&status, &accountID, &holder, &identity)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return errTakeoverState("the takeover is " + status + ", so there is nothing to hand back")
		}
		if holder != nil && *holder != a.UserID && a.Role != auth.RoleOwner {
			return errDenied("only the person holding the account, or an owner, hands it back")
		}
		outcome = accounts.Unresolved
		evidence := map[string]any{"takeover_id": id, "identity_checked": false}
		if req.Identity != nil {
			evidence["identity_checked"] = true
			if strings.EqualFold(strings.TrimSpace(*req.Identity), identity) && identity != "" {
				outcome = accounts.Authenticated
			} else {
				outcome = accounts.WrongIdentity
			}
		}
		if _, _, err := accounts.Record(ctx, tx, a.WorkspaceID, a.Brand, accountID, accounts.Attempt{Purpose: accounts.PurposeVerify, Outcome: outcome, ActorType: "user", ActorID: a.UserID, Evidence: evidence}); err != nil {
			return err
		}
		metrics.AccountAttempt(accounts.PurposeVerify, outcome)
		if _, err := tx.Exec(ctx, `UPDATE brand_account_takeovers SET status = 'handed_back', ended_at = now(), handback_outcome = $2, handback_note = $3 WHERE id = $1`, id, outcome, strings.TrimSpace(req.Note)); err != nil {
			return err
		}
		// Browser tasks that waited on this takeover run again.
		if _, err := browser.Resume(ctx, tx, id); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "account_id": accountID, "outcome": outcome}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.takeover_handed_back", "account_takeover", id, props, props)
	})
	if s.takeoverError(w, r, err) {
		return
	}
	metrics.AccountTakeover("handed_back")
	s.writeTakeover(w, r, a, id, http.StatusOK)
}

// cancelAccountTakeover withdraws a request or ends a takeover without
// a handback check (the account is left as it was).
func (s *Server) cancelAccountTakeover(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "takeover not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expireTakeovers(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var status, accountID string
		err := tx.QueryRow(ctx, `SELECT status, account_id::text FROM brand_account_takeovers WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, id, a.WorkspaceID, a.Brand).Scan(&status, &accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != "requested" && status != "active" {
			return errTakeoverState("the takeover is " + status + " already")
		}
		if status == "active" && a.ActorType != "human" {
			return errDenied("only a person ends a takeover under way")
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_account_takeovers SET status = 'cancelled', ended_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "account_id": accountID, "was": status}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.takeover_cancelled", "account_takeover", id, props, props)
	})
	if s.takeoverError(w, r, err) {
		return
	}
	metrics.AccountTakeover("cancelled")
	s.writeTakeover(w, r, a, id, http.StatusOK)
}

// An account an owner still has to act on at the platform after
// offboarding, since Outflare can only forget what it held.
type offboardAction struct {
	AccountID string `json:"account_id"`
	Platform  string `json:"platform"`
	Label     string `json:"label"`
	SignIn    string `json:"sign_in"`
	// Action: change_password, remove_access, disconnect_integration,
	// sign_out or reconnect_mailbox; Why says what for.
	Action string `json:"action"`
	Why    string `json:"why"`
}

func offboardActionFor(signIn string) (string, string) {
	switch signIn {
	case "local_password":
		return "change_password", "Change the password at the platform: the vault's copy is gone, but the old password still works there."
	case "social_sign_in":
		return "remove_access", "Remove the platform from the upstream identity's connected apps, so the sign-in path is closed."
	case "api_oauth":
		return "disconnect_integration", "Disconnect the integration (or revoke its access at the platform) if no other brand uses it."
	case "browser_session":
		return "sign_out", "Sign out of every session at the platform: a saved session may still be valid there."
	default:
		return "reconnect_mailbox", "The account verifies through a mailbox: decide whether the mailbox stays connected."
	}
}

// offboardAccounts takes a brand's accounts out of automated use in one
// transaction: every account is archived, its secrets leave the vault,
// takeovers waiting or under way are cancelled, open incidents are
// resolved, and the brand's action grants are revoked so no agent acts
// for it. It always waits for a person when an agent asks
// (heldForBuiltin). The response lists what Outflare could not do itself:
// the owner's actions at each platform.
func (s *Server) offboardAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if len(req.Note) > 1000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 1000 characters")
		return
	}
	ctx := r.Context()
	report := map[string]any{}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, platform, label, sign_in FROM brand_accounts WHERE workspace_id = $1 AND brand_id = $2 AND status <> 'archived' ORDER BY platform, label FOR UPDATE`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		needs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (offboardAction, error) {
			var o offboardAction
			err := row.Scan(&o.AccountID, &o.Platform, &o.Label, &o.SignIn)
			o.Action, o.Why = offboardActionFor(o.SignIn)
			return o, err
		})
		if err != nil {
			return err
		}
		if needs == nil {
			needs = []offboardAction{}
		}
		count := func(q string, args ...any) (int64, error) {
			tag, err := tx.Exec(ctx, q, args...)
			return tag.RowsAffected(), err
		}
		takeovers, err := count(`UPDATE brand_account_takeovers SET status = 'cancelled', ended_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND status IN ('requested', 'active')`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		secretsRemoved, err := count(`DELETE FROM brand_account_secrets WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		incidents, err := count(`UPDATE brand_account_incidents SET status = 'resolved', resolved_at = now(), resolution = 'The brand was offboarded.' WHERE workspace_id = $1 AND brand_id = $2 AND status <> 'resolved'`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		archived, err := count(`UPDATE brand_accounts SET status = 'archived', updated_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND status <> 'archived'`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		grants, err := count(`UPDATE action_grants SET revoked_at = now(), revoked_by = $3 WHERE workspace_id = $1 AND brand_id = $2 AND revoked_at IS NULL`, a.WorkspaceID, a.Brand, actorRef(a))
		if err != nil {
			return err
		}
		report = map[string]any{"archived": archived, "secrets_removed": secretsRemoved, "incidents_resolved": incidents, "takeovers_cancelled": takeovers, "grants_revoked": grants, "needs_owner": needs}
		props := map[string]any{"brand_id": a.Brand, "archived": archived, "secrets_removed": secretsRemoved, "incidents_resolved": incidents, "takeovers_cancelled": takeovers, "grants_revoked": grants}
		data := map[string]any{"note": strings.TrimSpace(req.Note)}
		for k, v := range props {
			data[k] = v
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "brand.accounts_offboarded", "brand", a.Brand, data, props)
	})
	if s.takeoverError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, report)
}
