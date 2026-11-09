package api

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/appsignin"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Signing in to the Outflare desktop app (docs/desktop-app.md). The app
// never shows a sign-in form: it starts a sign-in here with a PKCE
// challenge and opens the browser on /signin/app, where the person, signed
// in however they usually are, approves it. The browser hands the app a
// one-time code through the app's URL scheme, and the app completes the
// sign-in with the code and its verifier, which gets it the ordinary
// session cookie. Nothing about workspaces: the session is a person's.

// Sign-ins one network may start, and completions it may get wrong, in
// loginWindow.
const appSignInsPerNetwork = 30

// appSignInScheme is the app's URL scheme the browser hands the code to.
const appSignInScheme = "outflare://signed-in"

func (s *Server) routesAppSignIn(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/app/start", s.appSignInStart)
	mux.HandleFunc("GET /v1/auth/app/signins/{state}", s.requireUser(s.appSignInLookup))
	mux.HandleFunc("POST /v1/auth/app/signins/{state}/approve", s.requireUser(s.appSignInApprove))
	mux.HandleFunc("GET /v1/auth/app/complete", s.appSignInComplete)
}

func (s *Server) appSignInStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CodeChallenge string `json:"code_challenge"`
		Name          string `json:"name"`
		Platform      string `json:"platform"`
		AppVersion    string `json:"app_version"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	challenge, err := appsignin.DecodeChallenge(req.CodeChallenge)
	switch {
	case err != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "code_challenge is base64url(SHA-256(verifier)), without padding")
		return
	case req.Name == "" || len(req.Name) > 100:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is the computer's name, 1 to 100 characters")
		return
	case !slices.Contains(appsignin.Platforms, req.Platform):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "platform is macos, windows or linux")
		return
	case req.AppVersion == "" || len(req.AppVersion) > 40:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "app_version is 1 to 40 characters")
		return
	}
	key := "app-signin:" + networkKey(clientIP(r))
	if s.limiter.over(r.Context(), key, appSignInsPerNetwork) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many sign-ins started from this network; try again in 15 minutes")
		return
	}
	s.limiter.fail(r.Context(), key)
	p, err := appsignin.Start(r.Context(), s.pool, challenge, req.Name, req.Platform, req.AppVersion)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	track.Emit(r.Context(), track.Event{Name: "app_signin.started", ActorType: "human", RequestID: RequestID(r.Context()),
		Props: map[string]any{"platform": req.Platform, "app_version": req.AppVersion}})
	writeJSON(w, http.StatusCreated, map[string]any{
		"state": p.State, "expires_in": p.ExpiresIn,
		"url": s.publicURL + "/signin/app?state=" + url.QueryEscape(p.State),
	})
}

func (s *Server) appSignInLookup(w http.ResponseWriter, r *http.Request) {
	in, err := appsignin.Lookup(r.Context(), s.pool, r.PathValue("state"))
	if s.appSignInErr(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) appSignInApprove(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r.Context()).UserID
	key := "app-signin-approve:" + me
	if s.limiter.blocked(r.Context(), key) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many unknown sign-ins; try again in 15 minutes")
		return
	}
	var code string
	var in appsignin.Info
	err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		code, in, err = appsignin.Approve(r.Context(), tx, r.PathValue("state"), me)
		return err
	})
	if errors.Is(err, appsignin.ErrNotFound) {
		s.limiter.fail(r.Context(), key)
	}
	if s.appSignInErr(w, r, err) {
		return
	}
	emitUser(r.Context(), "app_signin.approved", me, map[string]any{"platform": in.Platform, "app_version": in.AppVersion})
	writeJSON(w, http.StatusCreated, map[string]any{
		"code": code,
		"url":  appSignInScheme + "?state=" + url.QueryEscape(r.PathValue("state")) + "&code=" + url.QueryEscape(code),
	})
}

// appSignInErr answers a lookup's or approval's failure; true when it did.
func (s *Server) appSignInErr(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, appsignin.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no sign-in is waiting with this state; start again in the Outflare app")
	case errors.Is(err, appsignin.ErrExpired):
		writeError(w, r, http.StatusGone, "app_signin_expired", "this sign-in expired; start again in the Outflare app")
	case errors.Is(err, appsignin.ErrUsed):
		writeError(w, r, http.StatusGone, "app_signin_expired", "this sign-in was already approved; start again in the Outflare app")
	default:
		s.internal(w, r, err)
	}
	return true
}

// appSignInComplete is where the app sends its window with the code the
// browser handed it and the verifier it kept. It answers with a redirect
// either way, since a window shows it: into the app with the session
// cookie set, or to the sign-in page with the error code, which says what
// went wrong and offers to start again.
func (s *Server) appSignInComplete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	code, verifier := q.Get("code"), q.Get("code_verifier")
	if code == "" || verifier == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}
	fail := func(errorCode string) {
		track.Emit(r.Context(), track.Event{Name: "app_signin.failed", ActorType: "human", Outcome: track.Failure, ErrorCode: errorCode,
			RequestID: RequestID(r.Context())})
		http.Redirect(w, r, s.publicURL+"/signin/app?error="+errorCode, http.StatusSeeOther)
	}
	key := "app-signin-complete:" + networkKey(clientIP(r))
	if s.limiter.over(r.Context(), key, appSignInsPerNetwork) {
		fail("rate_limited")
		return
	}
	// Redeeming is its own statement, so the code is gone even when what
	// follows fails.
	userID, _, err := appsignin.Complete(r.Context(), s.pool, code, verifier)
	switch {
	case errors.Is(err, appsignin.ErrNotFound), errors.Is(err, appsignin.ErrNotApproved):
		s.limiter.fail(r.Context(), key)
		fail("not_found")
		return
	case errors.Is(err, appsignin.ErrExpired):
		fail("app_signin_expired")
		return
	case errors.Is(err, appsignin.ErrMismatch):
		s.limiter.fail(r.Context(), key)
		fail("app_signin_mismatch")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	var secret string
	if err := s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		secret, err = createSessionFor(r.Context(), tx, userID, r, "app")
		return err
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	s.setSessionCookie(w, secret)
	http.Redirect(w, r, s.publicURL+"/", http.StatusSeeOther)
}
