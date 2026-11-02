package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/track"
)

type crmMailRollout struct {
	Enabled  bool     `json:"enabled"`
	Allowed  []string `json:"allowed_user_ids"`
	Excluded []string `json:"excluded_user_ids"`
	Version  int      `json:"version"`
}
type mailQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Server) routesCRMGoogle(mux *http.ServeMux) {
	b := "/v1/brands/{brand}/crm/mail/"
	mux.HandleFunc("GET "+b+"rollout", s.onBrand(auth.ScopeMembersManage, s.getCRMMailRollout))
	mux.HandleFunc("PUT "+b+"rollout", s.onBrand(auth.ScopeMembersManage, s.putCRMMailRollout))
	mux.HandleFunc("GET "+b+"connection", s.onBrand(auth.ScopeCustomersRead, s.getCRMGoogleConnection))
	mux.HandleFunc("DELETE "+b+"connection", s.onBrand(auth.ScopeCustomersRead, s.disconnectCRMGoogle))
	mux.HandleFunc("POST "+b+"google/authorize", s.onBrand(auth.ScopeCustomersManage, s.authorizeCRMGoogle))
	mux.HandleFunc("POST "+b+"sync", s.onBrand(auth.ScopeCustomersManage, s.queueCRMGoogleSync))
	mux.HandleFunc("GET "+b+"records/{record}/capture", s.onBrand(auth.ScopeCustomersRead, s.getCRMMailCapture))
	mux.HandleFunc("PUT "+b+"records/{record}/capture", s.onBrand(auth.ScopeCustomersManage, s.putCRMMailCapture))
	mux.HandleFunc("GET /v1/crm-mail/google/callback", s.requireUser(s.finishCRMGoogle))
}
func readMailRollout(ctx context.Context, q mailQuery, ws, brand string) (crmMailRollout, error) {
	out := crmMailRollout{Allowed: []string{}, Excluded: []string{}}
	var raw []byte
	var version int
	err := q.QueryRow(ctx, `SELECT config,version FROM crm_mail_rollout WHERE workspace_id=$1 AND brand_id=$2`, ws, brand).Scan(&raw, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	out.Version = version
	return out, nil
}

// Eligibility is explicit, revocable and separate from message visibility.
func eligibleCRMMail(ctx context.Context, q mailQuery, ws, brand, user string) (bool, error) {
	rollout, err := readMailRollout(ctx, q, ws, brand)
	if err != nil {
		return false, err
	}
	if !rollout.Enabled || !slices.Contains(rollout.Allowed, user) || slices.Contains(rollout.Excluded, user) {
		return false, nil
	}
	var role string
	var brands, stored []string
	var disabled bool
	var email string
	err = q.QueryRow(ctx, `SELECT m.role,m.brand_ids::text[],`+memberRoleScopes+`,u.disabled_at IS NOT NULL,lower(u.email) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=$1 AND m.user_id=$2 FOR SHARE OF m,u`, ws, user).Scan(&role, &brands, &stored, &disabled, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	scopes := effectiveScopes(role, stored)
	if disabled || !auth.Grants(scopes, auth.ScopeCustomersManage) || (auth.BrandLimitable(scopes) && len(brands) > 0 && !slices.Contains(brands, brand)) {
		return false, nil
	}
	cfg, err := mailConfig(ctx, q, "crm_mail_policy", ws, brand, "", "")
	if err != nil {
		return false, err
	}
	raw, _ := json.Marshal(cfg)
	var p struct {
		Internal  []string `json:"internal_domains"`
		Addresses []string `json:"blocked_addresses"`
		Domains   []string `json:"blocked_domains"`
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return false, err
	}
	if len(p.Internal) == 0 || slices.Contains(p.Addresses, email) {
		return false, nil
	}
	_, domain, _ := strings.Cut(email, "@")
	for _, d := range p.Domains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return false, nil
		}
	}
	return true, nil
}
func (s *Server) getCRMMailRollout(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	out, err := readMailRollout(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) putCRMMailRollout(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var in crmMailRollout
	if !decode(w, r, &in) {
		return
	}
	if len(in.Allowed) > 500 || len(in.Excluded) > 500 {
		writeError(w, r, 400, "invalid_request", "eligibility lists must have at most 500 members")
		return
	}
	if in.Allowed == nil {
		in.Allowed = []string{}
	}
	if in.Excluded == nil {
		in.Excluded = []string{}
	}
	for _, id := range append(append([]string{}, in.Allowed...), in.Excluded...) {
		if !validUUID(id) {
			writeError(w, r, 400, "invalid_request", "eligibility lists must contain member ids")
			return
		}
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		cur, err := readMailRollout(r.Context(), tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if cur.Version != in.Version {
			return errConflict
		}
		for _, id := range append(append([]string{}, in.Allowed...), in.Excluded...) {
			var member bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2 AND (brand_ids IS NULL OR cardinality(brand_ids)=0 OR $3::uuid=ANY(brand_ids)))`, a.WorkspaceID, id, a.Brand).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errInvalid("eligibility lists must contain members of this brand")
			}
		}
		raw, _ := json.Marshal(in)
		_, err = tx.Exec(r.Context(), `INSERT INTO crm_mail_rollout(workspace_id,brand_id,config)VALUES($1,$2,$3) ON CONFLICT(brand_id) DO UPDATE SET config=$3,version=crm_mail_rollout.version+1,updated_at=now()`, a.WorkspaceID, a.Brand, raw)
		if err != nil {
			return err
		}
		// Removing eligibility destroys the local grant; re-enabling needs consent again.
		if _, err = tx.Exec(r.Context(), `DELETE FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND (NOT $3 OR NOT(owner_id=ANY($4::uuid[])) OR owner_id=ANY($5::uuid[]))`, a.WorkspaceID, a.Brand, in.Enabled, in.Allowed, in.Excluded); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `DELETE FROM crm_mail_oauth_flows WHERE workspace_id=$1 AND brand_id=$2 AND (NOT $3 OR NOT(owner_id=ANY($4::uuid[])) OR owner_id=ANY($5::uuid[]))`, a.WorkspaceID, a.Brand, in.Enabled, in.Allowed, in.Excluded); err != nil {
			return err
		}
		in.Version = cur.Version + 1
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.rollout_updated", "crm_mail_rollout", a.Brand, map[string]any{"brand_id": a.Brand, "enabled": in.Enabled, "allowed_count": len(in.Allowed), "excluded_count": len(in.Excluded)})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, 409, "conflict", "rollout changed; reload before saving")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, in)
}
func (s *Server) crmGoogleConfigured() bool {
	return s.oauth != nil && s.box != nil && s.oauth.Google.ID != "" && s.oauth.Google.Secret != ""
}
func (s *Server) getCRMGoogleConnection(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	eligible, err := eligibleCRMMail(r.Context(), s.pool, a.WorkspaceID, a.Brand, a.UserID)
	if s.allocError(w, r, err) {
		return
	}
	var state string
	var at *time.Time
	var code *string
	err = s.pool.QueryRow(r.Context(), `SELECT status,last_synced_at,error_code FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, a.UserID).Scan(&state, &at, &code)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	connected := err == nil
	var count int
	if err = s.pool.QueryRow(r.Context(), `SELECT count(*) FROM crm_mail_capture_targets WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND enabled`, a.WorkspaceID, a.Brand, a.UserID).Scan(&count); s.allocError(w, r, err) {
		return
	}
	if !connected {
		state = "not_connected"
	}
	if !eligible {
		state = "not_eligible"
	}
	if !s.crmGoogleConfigured() {
		state = "not_configured"
	}
	clientID := ""
	if s.oauth != nil {
		clientID = s.oauth.Google.ID
	}
	writeJSON(w, 200, map[string]any{"redirect_uri": s.publicURL + "/v1/crm-mail/google/callback", "client_id": clientID, "configured": s.crmGoogleConfigured(), "eligible": eligible, "connected": connected, "status": state, "last_synced_at": at, "error_code": code, "capture_count": count})
}
func (s *Server) authorizeCRMGoogle(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	if !s.crmGoogleConfigured() {
		writeError(w, r, 503, "unavailable", "Google OAuth is not configured; ask your administrator to configure the Google app")
		return
	}
	allowed, err := eligibleCRMMail(r.Context(), s.pool, a.WorkspaceID, a.Brand, a.UserID)
	if s.allocError(w, r, err) {
		return
	}
	if !allowed {
		writeError(w, r, 403, "forbidden", "your administrator must enable you for personal CRM mail and configure internal domains")
		return
	}
	id, state, nonce, verifier := newID(), randomMailToken(), randomMailToken(), randomMailToken()
	expires := time.Now().Add(10 * time.Minute)
	sealed := s.box.Seal([]byte(verifier), []byte(id))
	digest := sha256.Sum256([]byte(state))
	var email string
	if err = s.pool.QueryRow(r.Context(), `SELECT email FROM users WHERE id=$1`, a.UserID).Scan(&email); s.allocError(w, r, err) {
		return
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM crm_mail_oauth_flows WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, a.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO crm_mail_oauth_flows(id,workspace_id,brand_id,owner_id,session_id,state_hash,verifier_sealed,nonce,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, a.WorkspaceID, a.Brand, a.UserID, a.SessionID, hex.EncodeToString(digest[:]), sealed, nonce, expires); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.consent_started", "crm_mail_connection", id, map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"url": s.oauth.ReadonlyGoogleURL(s.publicURL+"/v1/crm-mail/google/callback", state, nonce, verifier, email), "expires_at": expires})
}
func randomMailToken() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (s *Server) finishCRMGoogle(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.UserID == "" || p.TokenID != "" || p.ViewID != "" {
		writeError(w, r, 403, "forbidden", "use your own signed-in session")
		return
	}
	if !s.crmGoogleConfigured() {
		writeError(w, r, 503, "unavailable", "Google OAuth is not configured")
		return
	}
	digest := sha256.Sum256([]byte(r.URL.Query().Get("state")))
	var id, ws, brand, user, nonce string
	var sealed []byte
	// Atomic one-use consumption; even provider denial consumes this flow.
	err := s.pool.QueryRow(r.Context(), `UPDATE crm_mail_oauth_flows SET consumed_at=now() WHERE state_hash=$1 AND owner_id=$2 AND session_id=$3 AND expires_at>now() AND consumed_at IS NULL RETURNING id::text,workspace_id::text,brand_id::text,owner_id::text,nonce,verifier_sealed`, hex.EncodeToString(digest[:]), p.UserID, p.SessionID).Scan(&id, &ws, &brand, &user, &nonce, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 400, "invalid_request", "consent link expired or was already used; start again from CRM settings")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	done := func(code string) {
		if code != "connected" {
			track.Fail(r.Context(), s.log, track.Event{Name: "crm_mail.consent_failed", WorkspaceID: ws, ActorType: "human", Surface: "web", Props: map[string]any{"brand_id": brand, "reason": code}}, "invalid_request", errors.New("personal Google consent was not completed"))
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, s.publicURL+s.brandAppPath(r.Context(), ws, brand, "crm/settings")+"?crm_google="+url.QueryEscape(code), 302)
	}
	if r.URL.Query().Get("error") != "" {
		done("denied")
		return
	}
	allowed, err := eligibleCRMMail(r.Context(), s.pool, ws, brand, user)
	if s.allocError(w, r, err) {
		return
	}
	if !allowed {
		done("not_eligible")
		return
	}
	verifier, err := s.box.Open(sealed, []byte(id))
	if s.allocError(w, r, err) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	grant, err := s.oauth.ExchangePersonalGoogle(ctx, r.URL.Query().Get("code"), s.publicURL+"/v1/crm-mail/google/callback", nonce, string(verifier))
	if err != nil {
		done("consent_failed")
		return
	}
	var email string
	if err = s.pool.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id=$1`, user).Scan(&email); s.allocError(w, r, err) {
		return
	}
	if grant.Email != email {
		done("wrong_account")
		return
	}
	connectionID := newID()
	raw, _ := json.Marshal(crmGoogleCredentials{Grant: grant, Generation: 1})
	credentials := s.box.Seal(raw, []byte(connectionID))
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize with rollout changes; never store a grant after admin removal.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+brand); err != nil {
			return err
		}
		ok, err := eligibleCRMMail(ctx, tx, ws, brand, user)
		if err != nil {
			return err
		}
		if !ok {
			return errDenied("you are no longer eligible for personal mail")
		}
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1 AND user_id=$2 AND expires_at>now())`, p.SessionID, user).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errDenied("your session expired; connect again")
		}
		if _, err = tx.Exec(ctx, `DELETE FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, ws, brand, user); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO crm_google_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, connectionID, ws, brand, user, credentials); err != nil {
			return err
		}
		if err = s.enqueueCRMGoogle(ctx, tx, ws, connectionID, time.Now()); err != nil {
			return err
		}
		return record(ctx, tx, p, ws, "crm_mail.connected", "crm_mail_connection", connectionID, map[string]any{"brand_id": brand, "provider": "google"})
	})
	if s.allocError(w, r, err) {
		return
	}
	done("connected")
}
func (s *Server) disconnectCRMGoogle(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	purge := r.URL.Query().Get("purge")
	if purge != "" && purge != "true" && purge != "false" {
		writeError(w, r, 400, "invalid_request", "purge must be true or false")
		return
	}
	var deleted int64
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM crm_mail_oauth_flows WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, a.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, a.UserID); err != nil {
			return err
		}
		if purge == "true" {
			count, err := s.deleteCRMMailCopies(r.Context(), tx, a, "")
			if err != nil {
				return err
			}
			deleted = count
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.disconnected", "crm_mail_connection", a.Brand, map[string]any{"brand_id": a.Brand, "purged_count": deleted})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"disconnected": true, "deleted_count": deleted})
}
func readMailCapture(ctx context.Context, q mailQuery, a *access, rec string) (bool, int, error) {
	var enabled bool
	var version int
	err := q.QueryRow(ctx, `SELECT enabled,version FROM crm_mail_capture_targets WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND record_id=$4`, a.WorkspaceID, a.Brand, a.UserID, rec).Scan(&enabled, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return enabled, version, err
}
func (s *Server) getCRMMailCapture(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	if s.allocError(w, r, s.mailRecord(r.Context(), s.pool, a, r.PathValue("record"))) {
		return
	}
	enabled, v, err := readMailCapture(r.Context(), s.pool, a, r.PathValue("record"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": enabled, "version": v})
}
func (s *Server) putCRMMailCapture(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	rec := r.PathValue("record")
	var in struct {
		Enabled bool `json:"enabled"`
		Version int  `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+a.Brand); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-capture:"+a.Brand+":"+a.UserID+":"+rec); err != nil {
			return err
		}
		if err := s.mailRecord(r.Context(), tx, a, rec); err != nil {
			return err
		}
		wasEnabled, version, err := readMailCapture(r.Context(), tx, a, rec)
		if err != nil {
			return err
		}
		if version != in.Version {
			return errConflict
		}
		if in.Enabled {
			var matchable bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM records r JOIN record_identities i ON i.record_id=r.id AND i.workspace_id=r.workspace_id WHERE r.workspace_id=$1 AND r.brand_id=$2 AND r.id=$3 AND r.record_type IN('person','account') AND i.namespace IN('email','domain') AND (i.valid_to IS NULL OR i.valid_to>now()))`, a.WorkspaceID, a.Brand, rec).Scan(&matchable); err != nil {
				return err
			}
			if !matchable {
				return errInvalid("add an email address to this contact or a domain to this company before enabling capture")
			}
			targetContext, err := s.crmMailContextWith(r.Context(), tx, a, a.UserID, rec)
			if err != nil {
				return err
			}
			if len(targetContext.emails) == 0 && len(targetContext.domains) == 0 {
				return errInvalid("use individual contacts for public email services; company capture needs its own email domain")
			}

			allowed, err := eligibleCRMMail(r.Context(), tx, a.WorkspaceID, a.Brand, a.UserID)
			if err != nil {
				return err
			}
			if !allowed {
				return errDenied("your administrator must enable you before customer capture can be configured")
			}
			var count int
			if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM crm_mail_capture_targets WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND enabled`, a.WorkspaceID, a.Brand, a.UserID).Scan(&count); err != nil {
				return err
			}
			if count >= 100 && !wasEnabled {
				return errInvalid("capture supports up to 100 selected records per person")
			}
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO crm_mail_capture_targets(workspace_id,brand_id,owner_id,record_id,enabled)VALUES($1,$2,$3,$4,$5) ON CONFLICT(owner_id,record_id) DO UPDATE SET enabled=$5,version=crm_mail_capture_targets.version+1,updated_at=now()`, a.WorkspaceID, a.Brand, a.UserID, rec, in.Enabled); err != nil {
			return err
		}
		in.Version = version + 1
		if _, err = tx.Exec(r.Context(), `UPDATE crm_google_connections SET version=version+1,last_synced_at=NULL WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, a.UserID); err != nil {
			return err
		}
		var connectionID string
		err = tx.QueryRow(r.Context(), `SELECT id::text FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND status='connected'`, a.WorkspaceID, a.Brand, a.UserID).Scan(&connectionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if connectionID != "" {
			if err = s.enqueueCRMGoogle(r.Context(), tx, a.WorkspaceID, connectionID, time.Now(), newID()); err != nil {
				return err
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.capture_updated", "crm_mail_capture", a.Brand, map[string]any{"brand_id": a.Brand, "enabled": in.Enabled})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, 409, "conflict", "capture settings changed; reload before saving")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, in)
}
