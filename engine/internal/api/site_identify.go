package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Verified browser identify. A browser may never say who its visitor is on
// its own word: anyone can open the console. So the site's own server
// vouches for it. The site key gets an identify secret (ofid_, shown once,
// kept on the site's server); when a person signs in, the server signs
// their user id with it, hex(HMAC-SHA256(secret, userId)), and gives the
// page the id and the signature. outflare.identify(userId, signature)
// sends both here, and the browser's visits join that person only when the
// signature is right. A browser can't make one for another id, and it
// sends no traits: a person's fields still come only from a server.

// prefixSiteIdentify starts an identify secret.
const prefixSiteIdentify = "ofid_"

func (s *Server) routesSiteIdentify(mux *http.ServeMux) {
	manage := auth.ScopeCustomersManage
	mux.HandleFunc("POST /v1/brands/{brand}/site-keys/{id}/identify-secret", s.onBrand(manage, s.createSiteIdentifySecret))
	mux.HandleFunc("DELETE /v1/brands/{brand}/site-keys/{id}/identify-secret", s.onBrand(manage, s.removeSiteIdentifySecret))
	// Public: the key in the body and the signature are the authority.
	mux.HandleFunc("POST /v1/site/identify", s.siteIdentify)
	mux.HandleFunc("OPTIONS /v1/site/identify", s.siteEventsPreflight)
}

// identifySignature is what a site's server sends with a user id.
func identifySignature(secret, userID string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(userID))
	return hex.EncodeToString(m.Sum(nil))
}

func identifyAAD(keyID string) []byte { return []byte("site_key.identify_secret:" + keyID) }

type siteIdentifySecretView struct {
	// Shown once: keep it on the site's server, never in a page.
	Secret  string      `json:"secret"`
	SiteKey siteKeyView `json:"site_key"`
}

// createSiteIdentifySecret makes the key a new identify secret, replacing
// any it had at once.
func (s *Server) createSiteIdentifySecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	if s.box == nil {
		writeError(w, r, http.StatusConflict, "not_configured", "this server has no secret key set, so it can't keep an identify secret")
		return
	}
	ctx := r.Context()
	secret, _ := auth.NewToken(prefixSiteIdentify)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_keys SET identify_secret = $3, identify_secret_at = now() WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID, s.box.Seal([]byte(secret), identifyAAD(cur.ID))); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.identify_secret_created", "site_key", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := s.loadSiteKeys(ctx, a.WorkspaceID, &cur.ID)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, siteIdentifySecretView{Secret: secret, SiteKey: list[0]})
}

// removeSiteIdentifySecret turns browser identify off for the key.
func (s *Server) removeSiteIdentifySecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_keys SET identify_secret = NULL, identify_secret_at = NULL WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.identify_secret_removed", "site_key", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// siteIdentify joins a browser to the person its site's server vouched
// for. Every refusal of a signature reads the same, so the answer never
// says whether an id exists.
func (s *Server) siteIdentify(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 4 KB")
		return
	}
	var in struct {
		Key         string `json:"key"`
		AnonymousID string `json:"anonymousId"`
		UserID      string `json:"userId"`
		Signature   string `json:"signature"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"key": "ofk_...", "anonymousId": "...", "userId": "...", "signature": "..."}`)
		return
	}
	k, failed := s.siteBanner(w, r, in.Key)
	if failed != "" {
		return
	}
	if !s.siteOriginOK(r, k.siteKeyRow, r.Header.Get("Origin")) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this site key does not take requests from this origin; add it to the key's origins")
		return
	}
	// Guessing signatures is hopeless, but each visitor still gets a limit.
	if !s.siteVisitorLimit.allow(w, r, k.id+" "+siteClientIP(r, k.siteKeyRow)) {
		return
	}
	anon, userID, sig := strings.TrimSpace(in.AnonymousID), strings.TrimSpace(in.UserID), strings.ToLower(strings.TrimSpace(in.Signature))
	switch {
	case !siteCookieRe.MatchString(anon):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "anonymousId is required: the visitor's id from the script")
		return
	case userID == "" || len(userID) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "userId is required: your own id for the person, at most 200 characters")
		return
	}
	ctx := r.Context()
	var sealed []byte
	if err := s.pool.QueryRow(ctx, `SELECT identify_secret FROM site_keys WHERE id = $1 AND workspace_id = $2`, k.id, k.workspaceID).Scan(&sealed); err != nil {
		s.internal(w, r, err)
		return
	}
	if sealed == nil || s.box == nil {
		writeError(w, r, http.StatusForbidden, "forbidden", "browser identify is off for this site: make an identify secret for it in Website tracking, or send identify from your server")
		return
	}
	secret, err := s.box.Open(sealed, identifyAAD(k.id))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !hmac.Equal([]byte(identifySignature(string(secret), userID)), []byte(sig)) {
		writeError(w, r, http.StatusForbidden, "forbidden", "the signature is not right for this userId: sign the id on your server with the site's identify secret")
		return
	}
	a := &access{principal: principal{ActorType: "site_key", TokenID: k.id}, WorkspaceID: k.workspaceID}
	now := time.Now().UTC()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCatalog(ctx, tx, k.workspaceID, deref(k.brandID), false)
		if err != nil {
			return err
		}
		totals := ingestTotals{names: map[string]*seenName{}}
		var res eventResult
		m := eventMessage{"type": raw("identify"), "userId": raw(userID), "anonymousId": raw(anon)}
		if err := s.ingestOne(ctx, tx, a, c, k.brandID, "browser", nil, m, now, &res, &totals); err != nil {
			return err
		}
		return saveEventNames(ctx, tx, k.workspaceID, c.Brand, totals.names, false)
	})
	var bad errInvalid
	switch {
	case errors.As(err, &bad):
		writeError(w, r, http.StatusBadRequest, "invalid_request", bad.Error())
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
