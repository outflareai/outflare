package api

// A test version's visual changes over the API: what the visual editor
// saves, for people and agents alike. An agent (or a playbook, or the
// brand agent) can build a draft test's versions on its own; anything that
// changes what visitors see (editing a version of a running test or a flag
// that is on, starting or shipping a test, turning a flag on) always waits
// for a person on the action layer (testHold).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/flags"
)

type abVersionView struct {
	TestID   string         `json:"test_id"`
	FlagKey  string         `json:"flag_key"`
	Version  string         `json:"version"`
	Live     bool           `json:"live"`
	Changes  []flags.Change `json:"changes"`
	CSS      string         `json:"css"`
	Redirect string         `json:"redirect"`
}

func abVersion(t abTestView, f flagView, v flags.Variant) abVersionView {
	out := abVersionView{TestID: t.ID, FlagKey: f.Key, Version: v.Key, Live: t.Status == "running" || f.Status == "on"}
	out.Changes, _ = flags.Changes(v.Payload)
	if out.Changes == nil {
		out.Changes = []flags.Change{}
	}
	if len(v.Payload) > 0 && v.Payload[0] == '{' {
		var extra struct {
			CSS      string `json:"css"`
			Redirect string `json:"redirect"`
		}
		_ = json.Unmarshal(v.Payload, &extra)
		out.CSS, out.Redirect = extra.CSS, extra.Redirect
	}
	return out
}

// abTestVersionTarget finds the test, its flag and the version named in
// the path.
func (s *Server) abTestVersionTarget(w http.ResponseWriter, r *http.Request) (abTestView, flagView, int, bool) {
	a := accessFrom(r.Context())
	t, ok := s.oneABTest(w, r)
	if !ok {
		return t, flagView{}, 0, false
	}
	f, err := s.abFlag(r.Context(), nil, a.WorkspaceID, t.FlagID)
	if err != nil {
		s.internal(w, r, err)
		return t, f, 0, false
	}
	key := r.PathValue("version")
	i := slices.IndexFunc(f.Variants, func(v flags.Variant) bool { return v.Key == key })
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "the test's versions are "+strings.Join(variantKeys(f), ", "))
		return t, f, 0, false
	}
	return t, f, i, true
}

func (s *Server) getABTestVersion(w http.ResponseWriter, r *http.Request) {
	if t, f, i, ok := s.abTestVersionTarget(w, r); ok {
		writeJSON(w, http.StatusOK, abVersion(t, f, f.Variants[i]))
	}
}

// setABTestVersion replaces a version's visual changes, and its css and
// redirect when sent, as the visual editor saves them.
func (s *Server) setABTestVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Changes  json.RawMessage `json:"changes"`
		CSS      *string         `json:"css"`
		Redirect *string         `json:"redirect"`
	}
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	t, f, i, ok := s.abTestVersionTarget(w, r)
	if !ok {
		return
	}
	if !a.writes(f.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	switch {
	case t.Status == "done" || f.Status == "archived":
		writeError(w, r, http.StatusConflict, "conflict", "this test has ended: duplicate it to try new versions")
		return
	case f.Variants[i].Key == t.Control:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the control is today's page: change one of the other versions")
		return
	}
	if len(in.Changes) == 0 {
		in.Changes = json.RawMessage(`[]`)
	}
	if err := s.saveVisualEdit(r.Context(), &a.principal, a.WorkspaceID, &f, i, in.Changes, in.CSS, in.Redirect); err != nil {
		var bad errInvalid
		if errors.As(err, &bad) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", string(bad))
			return
		}
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, abVersion(t, f, f.Variants[i]))
}

var (
	abGoLivePath    = regexp.MustCompile(`^/v1/(?:workspaces|brands)/[^/]+/ab-tests/([^/]+)/(start|ship)/?$`)
	abVersionPath   = regexp.MustCompile(`^/v1/(?:workspaces|brands)/[^/]+/ab-tests/([^/]+)/versions/[^/]+/?$`)
	flagWritePath   = regexp.MustCompile(`^/v1/(?:workspaces|brands)/[^/]+/flags/([^/]+)/?$`)
	flagCreatePath  = regexp.MustCompile(`^/v1/(?:workspaces|brands)/[^/]+/flags/?$`)
	flagLiveChanges = []string{"variants", "rollout", "rules", "on_site", "status"}
)

// testHold is why an agent's change to an A/B test or flag waits for a
// person: anything that changes what visitors see. Building a draft (a
// test, its versions, a flag that is off) runs on the agent's grants.
func (s *Server) testHold(ctx context.Context, ws string, r *http.Request, body []byte) (string, error) {
	path := r.URL.Path
	if r.Method == http.MethodPost && abGoLivePath.MatchString(path) {
		return "starting or shipping an A/B test changes what visitors see, so a person always approves it", nil
	}
	var parsed map[string]json.RawMessage
	_ = json.Unmarshal(body, &parsed)
	if r.Method == http.MethodPost && flagCreatePath.MatchString(path) {
		if strings.Trim(string(parsed["status"]), `"`) == "on" {
			return "a flag that starts on changes what visitors see, so a person always approves it", nil
		}
		return "", nil
	}
	var live bool
	switch m := abVersionPath.FindStringSubmatch(path); {
	case m != nil && r.Method == http.MethodPut:
		if !validUUID(m[1]) {
			return "", nil
		}
		if err := s.pool.QueryRow(ctx, `SELECT t.status = 'running' OR f.status = 'on' FROM ab_tests t JOIN flags f ON f.id = t.flag_id
			WHERE t.workspace_id = $1 AND t.id = $2`, ws, m[1]).Scan(&live); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	default:
		m := flagWritePath.FindStringSubmatch(path)
		if m == nil || (r.Method != http.MethodPatch && r.Method != http.MethodDelete) {
			return "", nil
		}
		if strings.Trim(string(parsed["status"]), `"`) == "on" {
			return "turning a flag on changes what visitors see, so a person always approves it", nil
		}
		touches := r.Method == http.MethodDelete
		for _, k := range flagLiveChanges {
			if _, ok := parsed[k]; ok {
				touches = true
			}
		}
		if !touches {
			return "", nil
		}
		if err := s.pool.QueryRow(ctx, `SELECT status = 'on' FROM flags WHERE workspace_id = $1 AND (key = $2 OR id::text = $2) AND status <> 'archived'`,
			ws, m[1]).Scan(&live); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if live {
		return "this flag is live, so an agent's change to what visitors see waits for a person", nil
	}
	return "", nil
}
