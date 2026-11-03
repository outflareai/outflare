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

	"github.com/outflareai/outflare/engine/internal/accounts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Accounts (docs/accounts.md): the external accounts a brand's agent may
// act through, how each signs in, whether access works, what depends on
// what, the attempts made on them and the incidents that stop them. The
// vault keeps their secrets: set by a person (always held for a person
// when an agent asks), never returned.

func (s *Server) routesAccounts(mux *http.ServeMux) {
	read, manage := auth.ScopeAccountsRead, auth.ScopeAccountsManage
	mux.HandleFunc("GET /v1/brands/{brand}/accounts", s.onBrand(read, s.listAccounts))
	mux.HandleFunc("POST /v1/brands/{brand}/accounts", s.onBrand(manage, s.createAccount))
	mux.HandleFunc("GET /v1/brands/{brand}/accounts/{id}", s.onBrand(read, s.getAccount))
	mux.HandleFunc("PATCH /v1/brands/{brand}/accounts/{id}", s.onBrand(manage, s.updateAccount))
	mux.HandleFunc("PUT /v1/brands/{brand}/accounts/{id}/secrets/{kind}", s.onBrand(manage, s.setAccountSecret))
	mux.HandleFunc("DELETE /v1/brands/{brand}/accounts/{id}/secrets/{kind}", s.onBrand(manage, s.removeAccountSecret))
	mux.HandleFunc("GET /v1/brands/{brand}/accounts/{id}/attempts", s.onBrand(read, s.listAccountAttempts))
	mux.HandleFunc("POST /v1/brands/{brand}/accounts/{id}/attempts", s.onBrand(manage, s.recordAccountAttempt))
	mux.HandleFunc("GET /v1/brands/{brand}/account-incidents", s.onBrand(read, s.listAccountIncidents))
	mux.HandleFunc("PATCH /v1/brands/{brand}/account-incidents/{id}", s.onBrand(manage, s.updateAccountIncident))
}

var (
	accountSignIns  = []string{"local_password", "social_sign_in", "api_oauth", "browser_session", "mailbox_verification"}
	accountStatuses = []string{"unverified", "healthy", "needs_attention", "blocked", "archived"}
)

type accountView struct {
	ID             string     `json:"id"`
	BrandID        string     `json:"brand_id"`
	Platform       string     `json:"platform"`
	Label          string     `json:"label"`
	Identity       string     `json:"identity"`
	SignIn         string     `json:"sign_in"`
	DependsOn      *string    `json:"depends_on"`
	IntegrationID  *string    `json:"integration_id"`
	MailboxID      *string    `json:"mailbox_id"`
	URL            string     `json:"url"`
	Status         string     `json:"status"`
	OwnerID        *string    `json:"owner_id"`
	Notes          string     `json:"notes"`
	LastVerifiedAt *time.Time `json:"last_verified_at"`
	LastAttemptAt  *time.Time `json:"last_attempt_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// Secrets the vault holds for it, by kind; never their values.
	Secrets []string `json:"secrets"`
	// OpenIncidents on it, and Dependents: accounts that sign in through it.
	OpenIncidents int `json:"open_incidents"`
	Dependents    int `json:"dependents"`
	// On one account: its latest attempts and open incidents.
	Attempts  []accountAttemptView  `json:"attempts,omitempty"`
	Incidents []accountIncidentView `json:"incidents,omitempty"`
}

type accountAttemptView struct {
	ID        string         `json:"id"`
	AccountID string         `json:"account_id"`
	Purpose   string         `json:"purpose"`
	Outcome   string         `json:"outcome"`
	ActorType string         `json:"actor_type"`
	ActorID   string         `json:"actor_id"`
	Evidence  map[string]any `json:"evidence"`
	CreatedAt time.Time      `json:"created_at"`
}

type accountIncidentView struct {
	ID          string     `json:"id"`
	AccountID   string     `json:"account_id"`
	RootID      *string    `json:"root_id"`
	Kind        string     `json:"kind"`
	Severity    string     `json:"severity"`
	Status      string     `json:"status"`
	OwnerID     *string    `json:"owner_id"`
	Repeats     int        `json:"repeats"`
	NextRetryAt *time.Time `json:"next_retry_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	OpenedAt    time.Time  `json:"opened_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
	Resolution  string     `json:"resolution"`
}

const accountCols = `a.id::text, a.brand_id::text, a.platform, a.label, a.identity, a.sign_in, a.depends_on::text, a.integration_id::text, a.mailbox_id::text, a.url, a.status, a.owner_id::text, a.notes,
	a.last_verified_at, a.last_attempt_at, a.created_at, a.updated_at,
	COALESCE((SELECT array_agg(kind ORDER BY kind) FROM brand_account_secrets s WHERE s.account_id = a.id), '{}'),
	(SELECT count(*) FROM brand_account_incidents i WHERE i.account_id = a.id AND i.status <> 'resolved')::int,
	(SELECT count(*) FROM brand_accounts d WHERE d.depends_on = a.id AND d.status <> 'archived')::int`

func scanAccount(row pgx.CollectableRow) (accountView, error) {
	var v accountView
	err := row.Scan(&v.ID, &v.BrandID, &v.Platform, &v.Label, &v.Identity, &v.SignIn, &v.DependsOn, &v.IntegrationID, &v.MailboxID, &v.URL, &v.Status, &v.OwnerID, &v.Notes,
		&v.LastVerifiedAt, &v.LastAttemptAt, &v.CreatedAt, &v.UpdatedAt, &v.Secrets, &v.OpenIncidents, &v.Dependents)
	if v.Secrets == nil {
		v.Secrets = []string{}
	}
	return v, err
}

const incidentCols = `i.id::text, i.account_id::text, i.root_id::text, i.kind, i.severity, i.status, i.owner_id::text, i.repeats, i.next_retry_at, i.last_seen_at, i.opened_at, i.resolved_at, i.resolution`

func scanIncident(row pgx.CollectableRow) (accountIncidentView, error) {
	var v accountIncidentView
	err := row.Scan(&v.ID, &v.AccountID, &v.RootID, &v.Kind, &v.Severity, &v.Status, &v.OwnerID, &v.Repeats, &v.NextRetryAt, &v.LastSeenAt, &v.OpenedAt, &v.ResolvedAt, &v.Resolution)
	return v, err
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	where := `a.workspace_id = $1 AND a.brand_id = $2`
	args := []any{a.WorkspaceID, a.Brand}
	switch view := qs.Get("view"); view {
	case "", "all":
		where += ` AND a.status <> 'archived'`
	case "needs_attention":
		where += ` AND a.status IN ('needs_attention', 'blocked')`
	case "dependencies":
		where += ` AND EXISTS (SELECT 1 FROM brand_accounts d WHERE d.depends_on = a.id AND d.status <> 'archived')`
	case "archived":
		where += ` AND a.status = 'archived'`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "view is all, needs_attention, dependencies or archived")
		return
	}
	if v := qs.Get("platform"); v != "" {
		args = append(args, v)
		where += ` AND a.platform = $3`
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+accountCols+` FROM brand_accounts a WHERE `+where+` ORDER BY (a.status IN ('needs_attention', 'blocked')) DESC, a.platform, a.label`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanAccount)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []accountView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

type accountInput struct {
	Platform      *string          `json:"platform"`
	Label         *string          `json:"label"`
	Identity      *string          `json:"identity"`
	SignIn        *string          `json:"sign_in"`
	DependsOn     optional[string] `json:"depends_on"`
	IntegrationID optional[string] `json:"integration_id"`
	MailboxID     optional[string] `json:"mailbox_id"`
	URL           *string          `json:"url"`
	Notes         *string          `json:"notes"`
	OwnerID       optional[string] `json:"owner_id"`
	// unverified (back in use), blocked (a person takes it out of use) or
	// archived; healthy and needs_attention come from attempts.
	Status *string `json:"status"`
}

func (in *accountInput) validate() string {
	trim := func(p *string, max int, name string) string {
		if p == nil {
			return ""
		}
		*p = strings.TrimSpace(*p)
		if len(*p) > max {
			return name + " is at most " + strconv.Itoa(max) + " characters"
		}
		return ""
	}
	for _, c := range []struct {
		p    *string
		max  int
		name string
	}{{in.Platform, 100, "platform"}, {in.Label, 200, "label"}, {in.Identity, 320, "identity"}, {in.URL, 2000, "url"}, {in.Notes, 5000, "notes"}} {
		if msg := trim(c.p, c.max, c.name); msg != "" {
			return msg
		}
	}
	if in.Platform != nil && *in.Platform == "" {
		return "platform names the site or service"
	}
	if in.Label != nil && *in.Label == "" {
		return "label is what the account is called"
	}
	if in.SignIn != nil && !slices.Contains(accountSignIns, *in.SignIn) {
		return "sign_in is local_password, social_sign_in, api_oauth, browser_session or mailbox_verification"
	}
	if in.Status != nil && !slices.Contains([]string{"unverified", "blocked", "archived"}, *in.Status) {
		return "status is unverified (back in use), blocked or archived; healthy and needs_attention come from attempts"
	}
	for _, o := range []optional[string]{in.DependsOn, in.IntegrationID, in.MailboxID, in.OwnerID} {
		if o.Set && o.Value != nil && !validUUID(*o.Value) {
			return "ids are uuids"
		}
	}
	return ""
}

// accountRefs checks the account's references are the brand's: the
// account it depends on, its integration and its mailbox.
func (s *Server) accountRefs(ctx context.Context, tx pgx.Tx, a *access, id string, in accountInput) error {
	if in.DependsOn.Set && in.DependsOn.Value != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brand_accounts WHERE id = $1 AND brand_id = $2 AND id::text <> $3 AND status <> 'archived')`, *in.DependsOn.Value, a.Brand, id).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("depends_on is another of the brand's accounts")
		}
	}
	if in.IntegrationID.Set && in.IntegrationID.Value != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM integrations WHERE id = $1 AND workspace_id = $2 AND (brand_id IS NULL OR brand_id = $3 OR $3 = ANY(brand_ids)))`, *in.IntegrationID.Value, a.WorkspaceID, a.Brand).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("integration_id is a connection of the brand")
		}
	}
	if in.MailboxID.Set && in.MailboxID.Value != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE id = $1 AND workspace_id = $2)`, *in.MailboxID.Value, a.WorkspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("mailbox_id is one of the workspace's mailboxes")
		}
	}
	return nil
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req accountInput
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Platform == nil || req.Label == nil || req.SignIn == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "platform, label and sign_in are required")
		return
	case req.Status != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new account is unverified until it is used")
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.accountRefs(ctx, tx, a, "", req); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_accounts WHERE brand_id = $1 AND status <> 'archived'`, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= 500 {
			return errInvalid("the brand already has 500 accounts; archive ones it no longer uses")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO brand_accounts (workspace_id, brand_id, platform, label, identity, sign_in, depends_on, integration_id, mailbox_id, url, notes, owner_id, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id::text`,
			a.WorkspaceID, a.Brand, *req.Platform, *req.Label, str(req.Identity), *req.SignIn, req.DependsOn.Value, req.IntegrationID.Value, req.MailboxID.Value, str(req.URL), str(req.Notes), req.OwnerID.Value, actorRef(a)).Scan(&id); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.created", "account", id,
			map[string]any{"brand_id": a.Brand, "platform": *req.Platform, "sign_in": *req.SignIn, "depends_on": req.DependsOn.Value},
			map[string]any{"brand_id": a.Brand, "platform": *req.Platform, "sign_in": *req.SignIn})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeAccount(w, r, a, id, http.StatusCreated)
}

func (s *Server) loadAccount(ctx context.Context, a *access, id string) (accountView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountCols+` FROM brand_accounts a WHERE a.id = $1 AND a.brand_id = $2 AND a.workspace_id = $3`, id, a.Brand, a.WorkspaceID)
	if err != nil {
		return accountView{}, err
	}
	v, err := pgx.CollectOneRow(rows, scanAccount)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	v.Attempts, err = s.loadAttempts(ctx, id, 10)
	if err != nil {
		return v, err
	}
	irows, err := s.pool.Query(ctx, `SELECT `+incidentCols+` FROM brand_account_incidents i WHERE i.account_id = $1 AND i.status <> 'resolved' ORDER BY i.opened_at DESC`, id)
	if err != nil {
		return v, err
	}
	if v.Incidents, err = pgx.CollectRows(irows, scanIncident); v.Incidents == nil {
		v.Incidents = []accountIncidentView{}
	}
	return v, err
}

func (s *Server) loadAttempts(ctx context.Context, id string, limit int) ([]accountAttemptView, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, account_id::text, purpose, outcome, actor_type, actor_id, evidence, created_at FROM brand_account_attempts WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (accountAttemptView, error) {
		var v accountAttemptView
		err := row.Scan(&v.ID, &v.AccountID, &v.Purpose, &v.Outcome, &v.ActorType, &v.ActorID, &v.Evidence, &v.CreatedAt)
		if v.Evidence == nil {
			v.Evidence = map[string]any{}
		}
		return v, err
	})
	if list == nil {
		list = []accountAttemptView{}
	}
	return list, err
}

func (s *Server) writeAccount(w http.ResponseWriter, r *http.Request, a *access, id string, status int) {
	v, err := s.loadAccount(r.Context(), a, id)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "account not found")
		return
	}
	s.writeAccount(w, r, a, id, http.StatusOK)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "account not found")
		return
	}
	var req accountInput
	if !decode(w, r, &req) {
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `SELECT status FROM brand_accounts WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 FOR UPDATE`, id, a.Brand, a.WorkspaceID).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := s.accountRefs(ctx, tx, a, id, req); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_accounts SET platform = COALESCE($2, platform), label = COALESCE($3, label), identity = COALESCE($4, identity), sign_in = COALESCE($5, sign_in),
				depends_on = CASE WHEN $6 THEN $7::uuid ELSE depends_on END, integration_id = CASE WHEN $8 THEN $9::uuid ELSE integration_id END, mailbox_id = CASE WHEN $10 THEN $11::uuid ELSE mailbox_id END,
				url = COALESCE($12, url), notes = COALESCE($13, notes), owner_id = CASE WHEN $14 THEN $15::uuid ELSE owner_id END, status = COALESCE($16, status), updated_at = now()
			WHERE id = $1`, id, req.Platform, req.Label, req.Identity, req.SignIn, req.DependsOn.Set, req.DependsOn.Value, req.IntegrationID.Set, req.IntegrationID.Value,
			req.MailboxID.Set, req.MailboxID.Value, req.URL, req.Notes, req.OwnerID.Set, req.OwnerID.Value, req.Status); err != nil {
			return err
		}
		action := "account.updated"
		data := map[string]any{"brand_id": a.Brand}
		if req.Status != nil && *req.Status != cur {
			data["status"], data["was"] = *req.Status, cur
			switch *req.Status {
			case "archived":
				action = "account.archived"
			case "blocked":
				action = "account.blocked"
			case "unverified":
				action = "account.unblocked"
			}
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "account", id, data, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeAccount(w, r, a, id, http.StatusOK)
}

// setAccountSecret puts a secret in the vault. An agent asking for this
// always waits for a person (heldForBuiltin); the value is never recorded
// anywhere but the vault.
func (s *Server) setAccountSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id, kind := r.PathValue("id"), r.PathValue("kind")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "account not found")
		return
	}
	if !slices.Contains(accounts.SecretKinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is password, totp_seed, recovery or session")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Value == "" || len(req.Value) > 65536 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "value is the secret, at most 64 KB")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brand_accounts WHERE id = $1 AND brand_id = $2 AND workspace_id = $3)`, id, a.Brand, a.WorkspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		if err := (accounts.Vault{Box: s.box}).Set(ctx, tx, a.WorkspaceID, a.Brand, id, kind, req.Value, a.ActorID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.secret_set", "account", id, map[string]any{"brand_id": a.Brand, "kind": kind}, map[string]any{"brand_id": a.Brand, "kind": kind})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeAccount(w, r, a, id, http.StatusOK)
}

func (s *Server) removeAccountSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id, kind := r.PathValue("id"), r.PathValue("kind")
	if !validUUID(id) || !slices.Contains(accounts.SecretKinds, kind) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such secret")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		removed, err := (accounts.Vault{Box: s.box}).Remove(ctx, tx, a.WorkspaceID, a.Brand, id, kind)
		if err != nil {
			return err
		}
		if !removed {
			return errNotFound
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.secret_removed", "account", id, map[string]any{"brand_id": a.Brand, "kind": kind}, map[string]any{"brand_id": a.Brand, "kind": kind})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAccountAttempts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if _, err := s.loadAccount(r.Context(), a, id); !validUUID(id) || s.allocError(w, r, err) {
		if validUUID(id) {
			return
		}
		writeError(w, r, http.StatusNotFound, "not_found", "account not found")
		return
	}
	list, err := s.loadAttempts(r.Context(), id, 100)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// recordAccountAttempt is how a tool records a managed use of the account
// and what came of it; the outcome keeps the account's health and its
// incidents (accounts.Record).
func (s *Server) recordAccountAttempt(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "account not found")
		return
	}
	var req struct {
		Purpose  string         `json:"purpose"`
		Outcome  string         `json:"outcome"`
		Evidence map[string]any `json:"evidence"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case !slices.Contains(accounts.Purposes, req.Purpose):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "purpose is verify, sign_in, session_use, oauth_refresh or verification_email")
		return
	case !slices.Contains(accounts.Outcomes, req.Outcome):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "outcome is authenticated, verification_pending, mfa_required, wrong_identity, credential_failed, session_expired, locked, rate_limited or unresolved")
		return
	}
	if len(req.Evidence) > 20 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "evidence has at most 20 fields: what was seen, never a secret or a code")
		return
	}
	actorType, actorID := "system", ""
	switch {
	case a.TokenID != "":
		actorType, actorID = "token", a.TokenID
	case a.UserID != "":
		actorType, actorID = "user", a.UserID
	}
	ctx := r.Context()
	var attemptID, incidentID string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) (err error) {
		attemptID, incidentID, err = accounts.Record(ctx, tx, a.WorkspaceID, a.Brand, id, accounts.Attempt{Purpose: req.Purpose, Outcome: req.Outcome, ActorType: actorType, ActorID: actorID, Evidence: req.Evidence})
		if errors.Is(err, accounts.ErrNotFound) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "purpose": req.Purpose, "outcome": req.Outcome, "incident": incidentID != ""}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.attempt_recorded", "account", id,
			map[string]any{"brand_id": a.Brand, "purpose": req.Purpose, "outcome": req.Outcome, "attempt_id": attemptID, "incident_id": incidentID}, props)
	})
	if s.allocError(w, r, err) {
		return
	}
	metrics.AccountAttempt(req.Purpose, req.Outcome)
	v, err := s.loadAccount(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attempt_id": attemptID, "incident_id": nullableString(incidentID), "account": v})
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) listAccountIncidents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	where := `i.workspace_id = $1 AND i.brand_id = $2`
	switch v := r.URL.Query().Get("status"); v {
	case "":
		where += ` AND i.status <> 'resolved'`
	case "open", "snoozed", "resolved":
		where += ` AND i.status = '` + v + `'`
	case "all":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, snoozed, resolved or all")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+incidentCols+` FROM brand_account_incidents i WHERE `+where+` ORDER BY (i.root_id IS NULL) DESC, i.severity = 'critical' DESC, i.opened_at DESC LIMIT 200`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanIncident)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []accountIncidentView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) updateAccountIncident(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	var req struct {
		Status      *string          `json:"status"`
		OwnerID     optional[string] `json:"owner_id"`
		NextRetryAt *time.Time       `json:"next_retry_at"`
		Resolution  *string          `json:"resolution"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Status != nil && !slices.Contains([]string{"open", "snoozed", "resolved"}, *req.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, snoozed or resolved")
		return
	}
	if req.Status != nil && *req.Status == "snoozed" && req.NextRetryAt == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "snoozing takes next_retry_at; monitoring goes on meanwhile")
		return
	}
	if req.Resolution != nil && len(*req.Resolution) > 1000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "resolution is at most 1000 characters")
		return
	}
	if req.OwnerID.Set && req.OwnerID.Value != nil && !validUUID(*req.OwnerID.Value) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "owner_id is a user's id")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE brand_account_incidents SET status = COALESCE($4, status), owner_id = CASE WHEN $5 THEN $6::uuid ELSE owner_id END,
				next_retry_at = COALESCE($7, next_retry_at), resolution = COALESCE($8, resolution),
				resolved_at = CASE WHEN $4 = 'resolved' THEN now() WHEN $4 IS NOT NULL THEN NULL ELSE resolved_at END
			WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand, req.Status, req.OwnerID.Set, req.OwnerID.Value, req.NextRetryAt, req.Resolution)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		// A resolved incident lets its account be used again; blocked
		// stays a person's call on the account itself.
		if req.Status != nil && *req.Status == "resolved" {
			if _, err := tx.Exec(ctx, `UPDATE brand_accounts a SET status = 'unverified', updated_at = now() WHERE a.id = (SELECT account_id FROM brand_account_incidents WHERE id = $1)
				AND a.status = 'needs_attention' AND NOT EXISTS (SELECT 1 FROM brand_account_incidents i WHERE i.account_id = a.id AND i.status <> 'resolved')`, id); err != nil {
				return err
			}
		}
		data := map[string]any{"brand_id": a.Brand, "status": req.Status, "owner_set": req.OwnerID.Set}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "account.incident_updated", "account_incident", id, data, map[string]any{"brand_id": a.Brand, "status": req.Status})
	})
	if s.allocError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT `+incidentCols+` FROM brand_account_incidents i WHERE i.id = $1`, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := pgx.CollectOneRow(rows, scanIncident)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
