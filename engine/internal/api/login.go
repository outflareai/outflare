package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Signing in with Google. This is separate from connecting a Gmail mailbox:
// it asks only for openid, email and profile, and it uses its own callback,
// /v1/auth/google/callback, which must be added to the Google OAuth client
// next to the mailbox one.
//
// A Google account signs in to the user it was linked to, or to the user
// with the same (Google-verified) email, which links it. Nobody gets an
// account just by having a Google account: an unknown person is sent back
// to the page that started, with a short-lived identity token that an
// invite (or the hosted Cloud's sign-up) can redeem instead of a password.

const (
	loginStatePurpose = "login-state"
	loginNonceCookie  = "outflare_login"
	identityAAD       = "login-identity"
	identityLifetime  = 15 * time.Minute
)

func (s *Server) routesLogin(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/auth/google/start", s.googleStart)
	mux.HandleFunc("GET /v1/auth/google/callback", s.googleCallback)
}

// googleLogin reports whether "Continue with Google" is set up.
func (s *Server) googleLogin() bool {
	return s.oauth != nil && s.oauth.Box != nil && s.oauth.Google.ID != ""
}

func (s *Server) loginMethods() []string {
	m := []string{"password"}
	if s.googleLogin() {
		m = append(m, "google")
	}
	if s.webauthn != nil {
		m = append(m, "passkey")
	}
	return m
}

func (s *Server) loginRedirectURI() string { return s.publicURL + "/v1/auth/google/callback" }

type loginState struct {
	Return  string `json:"r"`
	Nonce   string `json:"n"`
	Expires int64  `json:"e"`
}

// safeReturn keeps the return path on this site.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func (s *Server) googleStart(w http.ResponseWriter, r *http.Request) {
	if !s.googleLogin() {
		writeError(w, r, http.StatusNotFound, "not_configured", "signing in with Google is not set up on this server")
		return
	}
	st := loginState{Return: safeReturn(r.URL.Query().Get("return")), Nonce: newID(), Expires: time.Now().Add(10 * time.Minute).Unix()}
	b, _ := json.Marshal(st)
	msg := base64.RawURLEncoding.EncodeToString(b)
	// The nonce cookie ties the callback to this browser, so nobody can sign
	// someone else in to their own account by sending them a callback link.
	http.SetCookie(w, &http.Cookie{Name: loginNonceCookie, Value: st.Nonce, Path: "/v1/auth/google", MaxAge: 600,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, s.oauth.LoginURL(s.loginRedirectURI(), msg+"."+s.oauth.Box.Sign(loginStatePurpose, msg)), http.StatusFound)
}

func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	back := "/"
	finish := func(params url.Values) {
		http.SetCookie(w, &http.Cookie{Name: loginNonceCookie, Value: "", Path: "/v1/auth/google", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies})
		// Query parameters go before a #fragment (the invite page keeps its
		// token there, out of server logs).
		target, frag, hasFrag := strings.Cut(back, "#")
		if len(params) > 0 {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + params.Encode()
		}
		if hasFrag {
			target += "#" + frag
		}
		http.Redirect(w, r, s.publicURL+target, http.StatusFound)
	}
	fail := func(code, msg string) {
		track.Emit(r.Context(), track.Event{Name: "user.sign_in_failed", ActorType: "human", Outcome: track.Failure, ErrorCode: code,
			RequestID: RequestID(r.Context()), Props: map[string]any{"via": "google"}})
		finish(url.Values{"login_error": {msg}})
	}
	if !s.googleLogin() {
		fail("google_not_configured", "Signing in with Google is not set up on this server.")
		return
	}
	q := r.URL.Query()
	msg, sig, _ := strings.Cut(q.Get("state"), ".")
	var st loginState
	raw, err := base64.RawURLEncoding.DecodeString(msg)
	if err != nil || !s.oauth.Box.Verify(loginStatePurpose, msg, sig) || json.Unmarshal(raw, &st) != nil {
		fail("invalid_state", "That sign-in link isn't valid. Try again.")
		return
	}
	back = safeReturn(st.Return)
	c, err := r.Cookie(loginNonceCookie)
	if err != nil || c.Value != st.Nonce {
		fail("other_browser", "Sign-in has to finish in the browser that started it. Try again.")
		return
	}
	if time.Now().Unix() > st.Expires {
		fail("expired", "Sign-in took too long. Try again.")
		return
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			fail("cancelled", "Google sign-in was cancelled.")
		} else {
			fail("google_refused", truncate("Google refused the sign-in: "+e, 200))
		}
		return
	}
	id, err := s.oauth.ExchangeLogin(r.Context(), q.Get("code"), s.loginRedirectURI())
	if err != nil {
		fail("google_exchange_failed", truncate(err.Error(), 200))
		return
	}

	ctx := r.Context()
	var secret string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		userID, err := linkIdentity(ctx, tx, id)
		if err != nil || userID == "" {
			return err
		}
		secret, err = createSession(ctx, tx, userID, r)
		return err
	})
	if err != nil {
		s.log.Error("google sign-in", "err", err)
		fail("internal", "Something went wrong signing you in. Try again.")
		return
	}
	if secret != "" {
		s.setSessionCookie(w, secret)
		// Signed in: into the app, unless an invite page started this and
		// still has its invite to accept.
		if !strings.HasPrefix(back, "/invite") {
			back = "/"
		}
		finish(nil)
		return
	}
	// Nobody uses this Google account yet: hand back a sealed identity that
	// an invite can redeem.
	token, err := sealIdentity(s.oauth.Box, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	finish(url.Values{"identity": {token}, "identity_email": {id.Email}, "identity_name": {id.Name}})
}

// linkIdentity finds the user a Google account signs in to: the one it is
// linked to, else the one with the same email, which it then links. It
// returns "" when there is none. Disabled users never sign in.
func linkIdentity(ctx context.Context, tx pgx.Tx, id *oauth.Identity) (string, error) {
	var userID string
	err := tx.QueryRow(ctx, `
		UPDATE user_identities i SET last_used_at = now(), email = $2
		FROM users u WHERE i.provider = 'google' AND i.subject = $1 AND u.id = i.user_id AND u.disabled_at IS NULL
		RETURNING i.user_id`, id.Subject, id.Email).Scan(&userID)
	if err == nil {
		return userID, markVerified(ctx, tx, userID, id.Email)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(email) = lower($1) AND disabled_at IS NULL`, id.Email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// Nobody had proved this address yet, so whoever set the account up may
	// not own it: someone could sign up with another person's email, wait
	// for them to sign in with Google, and keep a way in. Google proves the
	// address now, so drop every way in it didn't: the password, passkeys,
	// open sessions and API tokens. The owner can set a password with a
	// reset link.
	var verified bool
	if err := tx.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&verified); err != nil {
		return "", err
	}
	if !verified {
		for _, q := range []string{
			`UPDATE users SET password_hash = '' WHERE id = $1`,
			`DELETE FROM user_passkeys WHERE user_id = $1`,
			`DELETE FROM sessions WHERE user_id = $1`,
			`UPDATE api_tokens SET revoked_at = now() WHERE created_by = $1 AND revoked_at IS NULL`,
		} {
			if _, err := tx.Exec(ctx, q, userID); err != nil {
				return "", err
			}
		}
		emitUser(ctx, "user.unproven_access_removed", userID, map[string]any{"via": "google"})
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO user_identities (provider, subject, user_id, email) VALUES ('google', $1, $2, $3)
		ON CONFLICT (provider, subject) DO NOTHING`, id.Subject, userID, id.Email)
	if err != nil {
		return "", err
	}
	return userID, markVerified(ctx, tx, userID, id.Email)
}

type sealedIdentity struct {
	oauth.Identity
	Expires int64 `json:"exp"`
}

func sealIdentity(box *secrets.Box, id *oauth.Identity) (string, error) {
	b, err := json.Marshal(sealedIdentity{Identity: *id, Expires: time.Now().Add(identityLifetime).Unix()})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(box.Seal(b, []byte(identityAAD))), nil
}

var errBadIdentity = &InputError{"Your Google sign-in has expired. Choose Continue with Google again."}

// openIdentity reads a token from sealIdentity.
func openIdentity(box *secrets.Box, token string) (*oauth.Identity, error) {
	if box == nil {
		return nil, errBadIdentity
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, errBadIdentity
	}
	b, err := box.Open(raw, []byte(identityAAD))
	if err != nil {
		return nil, errBadIdentity
	}
	var si sealedIdentity
	if json.Unmarshal(b, &si) != nil || time.Now().Unix() > si.Expires || si.Subject == "" {
		return nil, errBadIdentity
	}
	return &si.Identity, nil
}
