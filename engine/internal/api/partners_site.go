package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Browser sign-ups: outflare.signup(email) on a website tells Outflare a
// visitor signed up, for sites whose backend reports nothing. It records a
// signup conversion for the partner whose link the visitor came from (by
// the visitor's own visits with a ref, under each program's window and
// credit rule), so the lead shows at once. Anyone can call it with any
// address, so it never pays: a sign-up reward is made only when the
// program turns on pay_browser_signups, and sales still come only from the
// server or billing. The answer says nothing about partners or rewards.

func (s *Server) routesPartnerSite(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/site/signup", s.sitePartnerSignup)
	mux.HandleFunc("OPTIONS /v1/site/signup", s.siteEventsPreflight)
}

func (s *Server) sitePartnerSignup(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 4 KB")
		return
	}
	var in struct {
		Key         string `json:"key"`
		AnonymousID string `json:"anonymousId"`
		Email       string `json:"email"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"key": "ofk_...", "anonymousId": "...", "email": "..."}`)
		return
	}
	k, failed := s.siteBanner(w, r, in.Key)
	if failed != "" {
		return
	}
	if !originAllowed(k.origins, r.Header.Get("Origin")) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this site key does not take requests from this origin; add it to the key's origins")
		return
	}
	if k.proxyHash != nil && !s.siteVisitorLimit.allow(w, r, k.id+" "+siteClientIP(r, k.siteKeyRow)) {
		return
	}
	anon := strings.TrimSpace(in.AnonymousID)
	email, err := normalizeEmail(in.Email)
	switch {
	case !siteCookieRe.MatchString(anon):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "anonymousId is required: the visitor's id from the script")
		return
	case err != nil || email == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email is not a valid address")
		return
	}
	conv := conversionInput{Type: "signup", Email: email, AnonymousID: anon}
	if err := conv.check(time.Now()); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	p := &principal{ActorType: "site_key", TokenID: k.id}
	if _, err := s.recordPartnerConversion(r.Context(), k.workspaceID, p, conv, "browser"); err != nil && !errors.As(err, new(errInvalid)) {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
