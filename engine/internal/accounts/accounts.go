// Package accounts keeps the external accounts a brand's agent may act
// through (plans/brand-agent, "Accounts"): how each signs in, whether
// access works, what depends on what, and the vault its secrets live in.
// Tracking is the tools' job, not the prompt's: every managed sign-in,
// verification and session use records an attempt before acting and its
// evidenced outcome after, and the outcome keeps the account's health and
// opens, repeats or resolves its incidents, in the same transaction.
// Secrets are sealed with the server's key, opened only for an authorised
// use, and never returned over the API or shown to a model.
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Attempt purposes.
const (
	PurposeVerify            = "verify"
	PurposeSignIn            = "sign_in"
	PurposeSessionUse        = "session_use"
	PurposeOAuthRefresh      = "oauth_refresh"
	PurposeVerificationEmail = "verification_email"
)

// Attempt outcomes.
const (
	Authenticated       = "authenticated"
	VerificationPending = "verification_pending"
	MFARequired         = "mfa_required"
	WrongIdentity       = "wrong_identity"
	CredentialFailed    = "credential_failed"
	SessionExpired      = "session_expired"
	Locked              = "locked"
	RateLimited         = "rate_limited"
	Unresolved          = "unresolved"
)

// Purposes and Outcomes list the values, for the API's checks.
var (
	Purposes = []string{PurposeVerify, PurposeSignIn, PurposeSessionUse, PurposeOAuthRefresh, PurposeVerificationEmail}
	Outcomes = []string{Authenticated, VerificationPending, MFARequired, WrongIdentity, CredentialFailed, SessionExpired, Locked, RateLimited, Unresolved}
	// SecretKinds the vault holds.
	SecretKinds = []string{"password", "totp_seed", "recovery", "session"}
)

// incidentFor is the incident an outcome opens (or repeats), with its
// severity and the cool-down before the next try; "" opens none.
func incidentFor(outcome string) (kind, severity string, retry time.Duration) {
	switch outcome {
	case CredentialFailed:
		return "credential", "critical", 0 // a person updates or verifies the credential; no automatic retry
	case SessionExpired:
		return "session", "warning", 0 // reconnect or approve scopes
	case MFARequired:
		return "mfa", "warning", 0 // waiting for a person
	case VerificationPending:
		return "verification", "info", 6 * time.Hour
	case WrongIdentity:
		return "wrong_identity", "critical", 0 // blocked: never acts through another identity
	case Locked:
		return "locked", "critical", 0
	case RateLimited:
		return "rate_limit", "info", time.Hour
	}
	return "", "", 0
}

// Attempt is one managed use of an account.
type Attempt struct {
	Purpose, Outcome string
	// ActorType is user, token or system; ActorID the user, token or job.
	ActorType, ActorID string
	// Evidence is what was seen: a page title, an HTTP status, a provider's
	// error code. Never a secret, a code or page text.
	Evidence map[string]any
}

// ErrNotFound means the account isn't the brand's.
var ErrNotFound = errors.New("accounts: no such account")

// Record keeps an attempt and applies its outcome: an authenticated use
// makes the account healthy and resolves its open incidents; a failure
// opens an incident of its kind, or repeats the open one, and puts the
// account at needs attention; a wrong identity or a locked account blocks
// automated use. A failure on an account others depend on (an upstream
// identity) links their incidents to it as the root, so one upstream
// failure is one incident. It returns the attempt's id and the incident
// opened or repeated, if any.
func Record(ctx context.Context, tx pgx.Tx, ws, brand, accountID string, in Attempt) (attemptID, incidentID string, err error) {
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM brand_accounts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, accountID, ws, brand).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	ev, _ := json.Marshal(in.Evidence)
	if in.Evidence == nil {
		ev = []byte("{}")
	}
	if err := tx.QueryRow(ctx, `INSERT INTO brand_account_attempts (workspace_id, brand_id, account_id, purpose, outcome, actor_type, actor_id, evidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, ws, brand, accountID, in.Purpose, in.Outcome, in.ActorType, in.ActorID, ev).Scan(&attemptID); err != nil {
		return "", "", err
	}
	// A blocked or archived account keeps its status whatever the outcome:
	// a person took it out of use.
	keep := status == "blocked" || status == "archived"
	switch in.Outcome {
	case Authenticated:
		if _, err := tx.Exec(ctx, `UPDATE brand_accounts SET status = CASE WHEN $2 THEN status ELSE 'healthy' END, last_verified_at = now(), last_attempt_at = now(), updated_at = now() WHERE id = $1`, accountID, keep); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_account_incidents SET status = 'resolved', resolved_at = now(), resolution = 'The account authenticated again.'
			WHERE account_id = $1 AND status <> 'resolved' AND kind <> 'wrong_identity'`, accountID); err != nil {
			return "", "", err
		}
		return attemptID, "", nil
	case Unresolved:
		_, err := tx.Exec(ctx, `UPDATE brand_accounts SET last_attempt_at = now(), updated_at = now() WHERE id = $1`, accountID)
		return attemptID, "", err
	}
	kind, severity, retry := incidentFor(in.Outcome)
	next := (*time.Time)(nil)
	if retry > 0 {
		t := time.Now().Add(retry)
		next = &t
	}
	newStatus := "needs_attention"
	if in.Outcome == WrongIdentity || in.Outcome == Locked {
		newStatus = "blocked"
	}
	if _, err := tx.Exec(ctx, `UPDATE brand_accounts SET status = CASE WHEN $2 THEN status ELSE $3 END, last_attempt_at = now(), updated_at = now() WHERE id = $1`, accountID, keep, newStatus); err != nil {
		return "", "", err
	}
	// The root: an open incident on the identity this account depends on.
	var root *string
	_ = tx.QueryRow(ctx, `SELECT i.id::text FROM brand_accounts a JOIN brand_account_incidents i ON i.account_id = a.depends_on AND i.status <> 'resolved'
		WHERE a.id = $1 ORDER BY i.opened_at LIMIT 1`, accountID).Scan(&root)
	if err := tx.QueryRow(ctx, `INSERT INTO brand_account_incidents (workspace_id, brand_id, account_id, root_id, kind, severity, next_retry_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (account_id, kind) WHERE status <> 'resolved' DO UPDATE SET repeats = brand_account_incidents.repeats + 1, last_seen_at = now(),
			next_retry_at = EXCLUDED.next_retry_at, root_id = COALESCE(brand_account_incidents.root_id, EXCLUDED.root_id),
			status = CASE WHEN brand_account_incidents.status = 'snoozed' AND brand_account_incidents.next_retry_at < now() THEN 'open' ELSE brand_account_incidents.status END
		RETURNING id::text`, ws, brand, accountID, root, kind, severity, next).Scan(&incidentID); err != nil {
		return "", "", err
	}
	// Accounts that depend on this one share its failure: their open
	// incidents point at this one as the root.
	if _, err := tx.Exec(ctx, `UPDATE brand_account_incidents i SET root_id = $2 FROM brand_accounts a
		WHERE i.account_id = a.id AND a.depends_on = $1 AND i.status <> 'resolved' AND i.root_id IS NULL`, accountID, incidentID); err != nil {
		return "", "", err
	}
	return attemptID, incidentID, nil
}

// Vault seals and opens an account's secrets with the server's key. The
// account's id is the sealing's associated data, so a sealed value never
// opens for another account.
type Vault struct{ Box *secrets.Box }

// Set keeps a secret for the account, replacing the kind's earlier value.
func (v Vault) Set(ctx context.Context, tx pgx.Tx, ws, brand, accountID, kind, value, setBy string) error {
	var by *string
	if setBy != "" {
		by = &setBy
	}
	_, err := tx.Exec(ctx, `INSERT INTO brand_account_secrets (account_id, workspace_id, brand_id, kind, sealed, set_by)
		SELECT id, workspace_id, brand_id, $4, $5, $6 FROM brand_accounts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3
		ON CONFLICT (account_id, kind) DO UPDATE SET sealed = EXCLUDED.sealed, set_by = EXCLUDED.set_by, set_at = now()`,
		accountID, ws, brand, kind, v.Box.Seal([]byte(value), []byte(accountID+":"+kind)), by)
	return err
}

// Open gives a secret's value to an authorised use (a worker acting for
// the brand on the account), never to a request handler's response.
func (v Vault) Open(ctx context.Context, q pgx.Tx, ws, brand, accountID, kind string) (string, error) {
	var sealed []byte
	err := q.QueryRow(ctx, `SELECT sealed FROM brand_account_secrets WHERE account_id = $1 AND workspace_id = $2 AND brand_id = $3 AND kind = $4`, accountID, ws, brand, kind).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	plain, err := v.Box.Open(sealed, []byte(accountID+":"+kind))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Remove takes a secret out of the vault.
func (v Vault) Remove(ctx context.Context, tx pgx.Tx, ws, brand, accountID, kind string) (bool, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM brand_account_secrets WHERE account_id = $1 AND workspace_id = $2 AND brand_id = $3 AND kind = $4`, accountID, ws, brand, kind)
	return tag.RowsAffected() > 0, err
}
