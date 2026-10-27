package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
)

// Passkeys: signing in with Face ID, Touch ID, Windows Hello, a phone or a
// security key instead of a password. A passkey belongs to one web address
// (the relying party, PUBLIC_URL's host), so they only turn on once the
// server has its real address; see Options.Passkeys. People add passkeys
// after signing in some other way, so they never create an account.

const (
	passkeyCookie   = "outflare_passkey"
	passkeyLifetime = 5 * time.Minute
	maxPasskeys     = 10
)

func (s *Server) routesPasskeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/passkeys", s.requireUser(s.listPasskeys))
	mux.HandleFunc("POST /v1/me/passkeys/options", s.requireUser(s.passkeyRegisterOptions))
	mux.HandleFunc("POST /v1/me/passkeys", s.requireUser(s.addPasskey))
	mux.HandleFunc("PATCH /v1/me/passkeys/{id}", s.requireUser(s.renamePasskey))
	mux.HandleFunc("DELETE /v1/me/passkeys/{id}", s.requireUser(s.removePasskey))
	mux.HandleFunc("POST /v1/auth/passkey/options", s.passkeyLoginOptions)
	mux.HandleFunc("POST /v1/auth/passkey", s.passkeyLogin)
}

// newWebAuthn configures the relying party from the public URL. It returns
// nil when passkeys are off or the URL can't name one.
func newWebAuthn(publicURL string, on bool) *webauthn.WebAuthn {
	if !on {
		return nil
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	w, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: "Outflare",
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
	})
	if err != nil {
		return nil
	}
	return w
}

func (s *Server) passkeysOff(w http.ResponseWriter, r *http.Request) bool {
	if s.webauthn != nil {
		return false
	}
	writeError(w, r, http.StatusNotFound, "passkeys_off", "passkeys are off on this server: they turn on once PUBLIC_URL is its https address")
	return true
}

// passkeyUser is a person as go-webauthn sees them.
type passkeyUser struct {
	id          string // the user's id, also the WebAuthn user handle
	email, name string
	creds       []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return []byte(u.id) }
func (u *passkeyUser) WebAuthnName() string                       { return u.email }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Server) loadPasskeyUser(r *http.Request, userID string) (*passkeyUser, error) {
	ctx := r.Context()
	if !validUUID(userID) {
		return nil, pgx.ErrNoRows
	}
	u := &passkeyUser{id: userID}
	if err := s.pool.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1 AND disabled_at IS NULL`, userID).Scan(&u.email, &u.name); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT credential FROM user_passkeys WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	raws, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	for _, raw := range raws {
		var c webauthn.Credential
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		u.creds = append(u.creds, c)
	}
	return u, nil
}

// A ceremony is the challenge between asking for options and the browser's
// answer, sealed in a short-lived cookie so the server keeps no state.
type ceremony struct {
	Purpose string               `json:"p"`
	UserID  string               `json:"u,omitempty"` // registration only
	Session webauthn.SessionData `json:"s"`
	Expires int64                `json:"e"`
}

func (s *Server) setCeremony(w http.ResponseWriter, c ceremony) error {
	c.Expires = time.Now().Add(passkeyLifetime).Unix()
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: passkeyCookie, Value: base64.RawURLEncoding.EncodeToString(s.box.Seal(b, []byte(passkeyCookie))),
		Path: "/v1/", MaxAge: int(passkeyLifetime / time.Second), HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

var errNoCeremony = errors.New("no passkey ceremony")

// takeCeremony reads and clears the ceremony cookie for purpose.
func (s *Server) takeCeremony(w http.ResponseWriter, r *http.Request, purpose string) (*ceremony, error) {
	http.SetCookie(w, &http.Cookie{Name: passkeyCookie, Value: "", Path: "/v1/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode})
	ck, err := r.Cookie(passkeyCookie)
	if err != nil {
		return nil, errNoCeremony
	}
	raw, err := base64.RawURLEncoding.DecodeString(ck.Value)
	if err != nil {
		return nil, errNoCeremony
	}
	b, err := s.box.Open(raw, []byte(passkeyCookie))
	if err != nil {
		return nil, errNoCeremony
	}
	var c ceremony
	if json.Unmarshal(b, &c) != nil || c.Purpose != purpose || time.Now().Unix() > c.Expires {
		return nil, errNoCeremony
	}
	return &c, nil
}

func (s *Server) ceremonyExpired(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusBadRequest, "passkey_expired", "that took too long or started in another tab; try again")
}

type passkeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// Synced passkeys (iCloud Keychain, Google Password Manager, 1Password)
	// survive losing the device.
	Synced bool `json:"synced"`
}

const passkeyCols = `id, name, created_at, last_used_at, coalesce((credential->'flags'->>'backupEligible')::boolean, false)`

func (s *Server) listPasskeys(w http.ResponseWriter, r *http.Request) {
	if s.passkeysOff(w, r) {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+passkeyCols+` FROM user_passkeys WHERE user_id = $1 ORDER BY created_at`, principalFrom(r.Context()).UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[passkeyView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out})
}

func (s *Server) passkeyRegisterOptions(w http.ResponseWriter, r *http.Request) {
	if s.passkeysOff(w, r) {
		return
	}
	p := principalFrom(r.Context())
	u, err := s.loadPasskeyUser(r, p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(u.creds) >= maxPasskeys {
		writeError(w, r, http.StatusConflict, "too_many_passkeys", "you have 10 passkeys already; remove one first")
		return
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, session, err := s.webauthn.BeginRegistration(u,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithExclusions(exclude),
	)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.setCeremony(w, ceremony{Purpose: "register", UserID: p.UserID, Session: *session}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

// addPasskey finishes registration. The body is the browser's credential
// (PublicKeyCredential.toJSON()); ?name= labels it.
func (s *Server) addPasskey(w http.ResponseWriter, r *http.Request) {
	if s.passkeysOff(w, r) {
		return
	}
	p := principalFrom(r.Context())
	c, err := s.takeCeremony(w, r, "register")
	if err != nil || c.UserID != p.UserID {
		s.ceremonyExpired(w, r)
		return
	}
	u, err := s.loadPasskeyUser(r, p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	cred, err := s.webauthn.FinishRegistration(u, c.Session, r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "passkey_rejected", "the passkey couldn't be checked; try again, or use another device")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	var v passkeyView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO user_passkeys (user_id, credential_id, credential, name) VALUES ($1, $2, $3, $4) RETURNING `+passkeyCols,
			p.UserID, cred.ID, raw, truncate(name, 80))
		if err := row.Scan(&v.ID, &v.Name, &v.CreatedAt, &v.LastUsedAt, &v.Synced); err != nil {
			return err
		}
		return record(ctx, tx, p, "", "user.passkey_added", "user", p.UserID, map[string]any{"name": v.Name})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

type renamePasskeyRequest struct {
	Name string `json:"name"`
}

func (s *Server) renamePasskey(w http.ResponseWriter, r *http.Request) {
	if s.passkeysOff(w, r) {
		return
	}
	var req renamePasskeyRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such passkey")
		return
	}
	var v passkeyView
	err := s.pool.QueryRow(r.Context(), `UPDATE user_passkeys SET name = $3 WHERE id = $1 AND user_id = $2 RETURNING `+passkeyCols,
		r.PathValue("id"), principalFrom(r.Context()).UserID, truncate(name, 80)).Scan(&v.ID, &v.Name, &v.CreatedAt, &v.LastUsedAt, &v.Synced)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such passkey")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) removePasskey(w http.ResponseWriter, r *http.Request) {
	if s.passkeysOff(w, r) {
		return
	}
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such passkey")
		return
	}
	p := principalFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		if err := tx.QueryRow(ctx, `DELETE FROM user_passkeys WHERE id = $1 AND user_id = $2 RETURNING name`, r.PathValue("id"), p.UserID).Scan(&name); err != nil {
			return err
		}
		return record(ctx, tx, p, "", "user.passkey_removed", "user", p.UserID, map[string]any{"name": name})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such passkey")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// passkeyLoginOptions starts a sign-in where the browser offers whichever
// passkeys it has for this site, so no email is typed.
func (s *Server) passkeyLoginOptions(w http.ResponseWriter, r *http.Request) {
	if s.passkeysOff(w, r) {
		return
	}
	assertion, session, err := s.webauthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.setCeremony(w, ceremony{Purpose: "login", Session: *session}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

func (s *Server) passkeyLogin(w http.ResponseWriter, r *http.Request) {
	if s.passkeysOff(w, r) {
		return
	}
	c, err := s.takeCeremony(w, r, "login")
	if err != nil {
		s.ceremonyExpired(w, r)
		return
	}
	key := "passkey:" + networkKey(clientIP(r))
	if s.limiter.blocked(r.Context(), key) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many failed attempts; try again in 15 minutes")
		return
	}
	var found *passkeyUser
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	cred, err := s.webauthn.FinishDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := s.loadPasskeyUser(r, string(userHandle))
		found = u
		return u, err
	}, c.Session, r)
	if err != nil || found == nil {
		s.limiter.fail(r.Context(), key)
		writeError(w, r, http.StatusUnauthorized, "passkey_rejected", "that passkey isn't linked to an Outflare account here; sign in another way and add it")
		return
	}
	raw, err := json.Marshal(cred) // carries the new signature counter
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	userID := found.id
	var secret string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE user_passkeys SET credential = $3, last_used_at = now() WHERE user_id = $1 AND credential_id = $2`, userID, cred.ID, raw); err != nil {
			return err
		}
		var actorID string
		if err := tx.QueryRow(ctx, `SELECT actor_id FROM users WHERE id = $1`, userID).Scan(&actorID); err != nil {
			return err
		}
		var err error
		if secret, err = createSession(ctx, tx, userID, r); err != nil {
			return err
		}
		p := &principal{ActorID: actorID, ActorType: "human", UserID: userID}
		return record(ctx, tx, p, "", "user.login", "user", userID, map[string]any{"via": "passkey"})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.limiter.reset(r.Context(), key)
	s.setSessionCookie(w, secret)
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID})
}
