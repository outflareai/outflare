package api

// The visual editor: someone who manages analytics asks for an edit link
// for one version of a flag on a page of their site. The link opens the
// page with that version previewed and ?of_edit=<token>, and the site
// script loads the editor (editor.js) over it. The editor reads and saves
// the version's visual changes with the token, which is signed, names the
// workspace, flag, version and person, and expires.

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/flags"
)

//go:embed editor.js
var editorScript []byte

// editTokenTTL is how long an edit link works.
const editTokenTTL = 2 * time.Hour

type editClaims struct {
	Workspace string `json:"w"`
	Flag      string `json:"f"`
	Variant   string `json:"v"`
	Actor     string `json:"a"`
	User      string `json:"u"`
	Expires   int64  `json:"e"`
}

func (s *Server) editToken(c editClaims) string {
	b, _ := json.Marshal(c)
	msg := base64.RawURLEncoding.EncodeToString(b)
	return msg + "." + s.box.Sign("flag_edit", msg)
}

var errEditToken = errors.New("edit link not valid")

// readEditToken checks a token's signature, expiry, and that its person
// can still manage analytics in its workspace, as inWorkspace would let
// them in on the flag routes: on every brand.
func (s *Server) readEditToken(r *http.Request, tok string) (editClaims, error) {
	var c editClaims
	msg, sig, ok := strings.Cut(tok, ".")
	if !ok || !s.box.Verify("flag_edit", msg, sig) {
		return c, errEditToken
	}
	b, err := base64.RawURLEncoding.DecodeString(msg)
	if err != nil || json.Unmarshal(b, &c) != nil || time.Now().Unix() > c.Expires {
		return c, errEditToken
	}
	var role string
	var brands, stored []string
	err = s.pool.QueryRow(r.Context(), `SELECT role, brand_ids::text[], scopes FROM (`+workspaceRoles+`) r ORDER BY rank DESC, cardinality(brand_ids) LIMIT 1`,
		c.Workspace, c.User).Scan(&role, &brands, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errEditToken
	}
	if err != nil {
		return c, err
	}
	scopes := effectiveScopes(role, stored)
	if !auth.Grants(scopes, auth.ScopeAnalyticsManage) || (len(brands) > 0 && auth.BrandLimitable(scopes)) {
		return c, errEditToken
	}
	return c, nil
}

// createFlagEditLink gives a link that opens a page of the site with the
// visual editor for one version of the flag.
func (s *Server) createFlagEditLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Variant string `json:"variant"`
		URL     string `json:"url"`
	}
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	f, ok := s.oneFlag(w, r)
	if !ok {
		return
	}
	if !a.writes(f.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	if a.ActorType != "human" || a.ViewID != "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "the visual editor is for people: sign in to the app to open it")
		return
	}
	if f.Status == "archived" || !slices.ContainsFunc(f.Variants, func(v flags.Variant) bool { return v.Key == in.Variant }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "variant is one of the flag's versions: "+strings.Join(variantKeys(f), ", "))
		return
	}
	page, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (page.Scheme != "https" && page.Scheme != "http") || page.Host == "" || len(in.URL) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is a page of your site that has Outflare's script, starting with https://")
		return
	}
	exp := time.Now().Add(editTokenTTL)
	tok := s.editToken(editClaims{Workspace: a.WorkspaceID, Flag: f.ID, Variant: in.Variant, Actor: a.ActorID, User: a.UserID, Expires: exp.Unix()})
	q := page.Query()
	q.Set("of_flag", f.Key+":"+in.Variant)
	q.Set("of_edit", tok)
	page.RawQuery = q.Encode()
	writeJSON(w, http.StatusOK, map[string]any{"url": page.String(), "expires_at": exp.UTC()})
}

// editorJS serves the editor the site script loads for an edit link.
func (s *Server) editorJS(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Write(editorScript)
}

// siteFlagEdits reads (GET ?token=) or saves (POST {token, changes}) the
// visual changes of the version an edit link names. Like the other site
// routes it is public: the signed token is the authority.
func (s *Server) siteFlagEdits(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Token   string          `json:"token"`
		Changes json.RawMessage `json:"changes"`
	}
	if r.Method == http.MethodGet {
		in.Token = r.URL.Query().Get("token")
	} else {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil || json.Unmarshal(body, &in) != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "send {token, changes} as JSON, under 64 KB")
			return
		}
	}
	c, err := s.readEditToken(r, in.Token)
	if errors.Is(err, errEditToken) {
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "this edit link has expired or isn't valid: open the editor again from Outflare")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	list, err := s.loadFlags(ctx, nil, c.Workspace, ` AND id = $2 AND status <> 'archived'`, c.Flag)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	i := -1
	if len(list) == 1 {
		i = slices.IndexFunc(list[0].Variants, func(v flags.Variant) bool { return v.Key == c.Variant })
	}
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "this version no longer exists")
		return
	}
	f := list[0]
	answer := func(changes []flags.Change) {
		if changes == nil {
			changes = []flags.Change{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"flag_key": f.Key, "flag_name": f.Name, "variant": c.Variant, "changes": changes,
			"expires_at": time.Unix(c.Expires, 0).UTC()})
	}
	if r.Method == http.MethodGet {
		cs, _ := flags.Changes(f.Variants[i].Payload)
		answer(cs)
		return
	}

	// Save: the changes replace the version's "changes", and the rest of
	// its payload stays.
	var cs []flags.Change
	if len(in.Changes) == 0 || json.Unmarshal(in.Changes, &cs) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "changes is a list of {selector, and text, hide, href, src or alt}")
		return
	}
	payload := map[string]json.RawMessage{}
	if p := f.Variants[i].Payload; len(p) > 0 && p[0] == '{' {
		_ = json.Unmarshal(p, &payload)
	}
	payload["changes"], _ = json.Marshal(cs)
	f.Variants[i].Payload, _ = json.Marshal(payload)
	if msg := flags.CheckVariants(f.Variants); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	p := principal{ActorID: c.Actor, ActorType: "human", UserID: c.User}
	variants, _ := json.Marshal(f.Variants)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE flags SET variants = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`, f.ID, c.Workspace, variants); err != nil {
			return err
		}
		data := flagAuditData(f)
		data["was"], data["visual_changes"] = f.Status, len(cs)
		return record(ctx, tx, &p, c.Workspace, "flag.updated", "flag", f.ID, data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.flagCache.forget(c.Workspace)
	answer(cs)
}
