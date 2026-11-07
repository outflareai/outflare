package api

// AI inside the visual editor: on a version of a no-code A/B test, the
// person asks for a change to the whole page or to the part they picked,
// and the workspace's own AI model writes it from the page as the editor
// sees it. The editor shows the answer on the page to keep or undo; nothing
// is saved until the person saves. Like the editor's other routes, the
// signed edit token is the authority.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/flags"
)

// editorTest is the no-code test a flag runs, when there is one: the AI
// in the editor writes page changes only for those.
func (s *Server) editorTest(ctx context.Context, ws, flagID string) (*abTestView, error) {
	list, err := loadABTests(ctx, s.pool, ws, ` AND t.flag_id = $2 AND t.page <> ''`, flagID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// siteEditorAI starts an AI run from the editor (POST {token, variant,
// prompt, outline, focus}), or reads one (GET ?token=&run=).
func (s *Server) siteEditorAI(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	var in struct {
		Token   string        `json:"token"`
		Variant string        `json:"variant"`
		Prompt  string        `json:"prompt"`
		Outline []pageElement `json:"outline"`
		Focus   string        `json:"focus"`
		Run     string        `json:"run"`
	}
	if r.Method == http.MethodGet {
		in.Token, in.Run = r.URL.Query().Get("token"), r.URL.Query().Get("run")
	} else if !readSiteJSON(w, r, &in) {
		return
	}
	c, f, ok := s.editorFlag(w, r, in.Token)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		s.answerEditorRun(w, r, c, f, in.Run)
		return
	}
	test, err := s.editorTest(ctx, c.Workspace, f.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if test == nil {
		writeError(w, r, http.StatusConflict, "conflict", "the AI here writes changes for an A/B test of a page: make one under A/B tests, then open its versions in the editor")
		return
	}
	variant := strings.TrimSpace(in.Variant)
	if variant == "" {
		variant = c.Variant
	}
	if variant == test.Control || !slices.ContainsFunc(f.Variants, func(v flags.Variant) bool { return v.Key == variant }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "pick one of the test's new versions to change; today's page stays as it is")
		return
	}
	prompt := strings.TrimSpace(in.Prompt)
	switch {
	case prompt == "" || len(prompt) > maxABPrompt:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "say what to change, up to 2,000 characters, such as \"make this button green and say Start free\"")
		return
	case len(in.Outline) == 0 || len(in.Outline) > 2*maxOutline:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "outline is the page's parts as the editor sees them, up to 400")
		return
	case len(in.Focus) > 300:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "focus is the selector of the part picked, up to 300 characters")
		return
	}
	ok, err = s.hasAIModel(ctx, c.Workspace)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusConflict, "connector_needed", "this uses your own AI model: connect one in Outflare under Settings, AI model")
		return
	}
	input := abVersionsInput{Flag: f.Key, Prompt: prompt, Count: 1, Test: test.ID, Version: variant, Outline: in.Outline, Focus: strings.TrimSpace(in.Focus),
		Page: test.Page, Mode: "page", Editor: true}
	p := principal{ActorID: c.Actor, ActorType: "human", UserID: c.User}
	run, err := s.queueABAIRunView(ctx, &p, c.Workspace, c.Actor, "versions", f.BrandID, &test.ID, &f.ID, input)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// siteEditorAIStop stops a run the editor started (POST {token, run}).
func (s *Server) siteEditorAIStop(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Token string `json:"token"`
		Run   string `json:"run"`
	}
	if !readSiteJSON(w, r, &in) {
		return
	}
	c, f, ok := s.editorFlag(w, r, in.Token)
	if !ok {
		return
	}
	ctx := r.Context()
	var list []abAIRunView
	err := errNotFound
	if validUUID(in.Run) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			runs, err := loadABAIRuns(ctx, tx, c.Workspace, ` AND id = $2 AND flag_id = $3`, in.Run, f.ID)
			if err != nil {
				return err
			}
			if len(runs) != 1 {
				return errNotFound
			}
			p := principal{ActorID: c.Actor, ActorType: "human", UserID: c.User}
			list, err = stopABAIRunTx(ctx, tx, &p, c.Workspace, runs[0])
			return err
		})
	}
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "AI run not found")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, list[0])
	}
}

// answerEditorRun writes a run of the token's flag.
func (s *Server) answerEditorRun(w http.ResponseWriter, r *http.Request, c editClaims, f flagView, id string) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "AI run not found")
		return
	}
	list, err := loadABAIRuns(r.Context(), s.pool, c.Workspace, ` AND id = $2 AND flag_id = $3`, id, f.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(list) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "AI run not found")
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// editorFlag checks an edit token and loads its flag, answering the
// request itself when it can't.
func (s *Server) editorFlag(w http.ResponseWriter, r *http.Request, tok string) (editClaims, flagView, bool) {
	c, err := s.readEditToken(r, tok)
	if errors.Is(err, errEditToken) {
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "this edit link has expired or isn't valid: open the editor again from Outflare")
		return c, flagView{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return c, flagView{}, false
	}
	list, err := s.loadFlags(r.Context(), nil, c.Workspace, ` AND id = $2 AND status <> 'archived'`, c.Flag)
	if err != nil {
		s.internal(w, r, err)
		return c, flagView{}, false
	}
	if len(list) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "this flag no longer exists")
		return c, flagView{}, false
	}
	return c, list[0], true
}

// readSiteJSON reads a site route's JSON body, sent as text/plain to skip
// the CORS preflight.
func readSiteJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil || json.Unmarshal(body, v) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send JSON, under 128 KB")
		return false
	}
	return true
}
