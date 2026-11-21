package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Named clicks. The website script's autocapture sends every click on a
// link or button as Element Clicked. A brand names the ones it cares about
// here ("Signup Clicked": the button labelled Start free), with no change
// to its site: a click that matches is stored a second time under that
// name, so funnels, trends and A/B test goals can use it like an event the
// site sent itself. Naming works from the moment it is saved; earlier
// clicks keep only their Element Clicked event.

func (s *Server) routesClickActions(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/click-actions", s.onBrand(read, s.listClickActions))
	mux.HandleFunc("POST /v1/brands/{brand}/click-actions", s.onBrand(manage, s.createClickAction))
	mux.HandleFunc("GET /v1/brands/{brand}/click-actions/seen", s.onBrand(read, s.listSeenClicks))
	mux.HandleFunc("PATCH /v1/brands/{brand}/click-actions/{id}", s.onBrand(manage, s.updateClickAction))
	mux.HandleFunc("DELETE /v1/brands/{brand}/click-actions/{id}", s.onBrand(manage, s.deleteClickAction))
}

// clickEvent is the catalog name of the script's Element Clicked.
const clickEvent = "element_clicked"

// clickActionLimit is how many named clicks a brand may have.
const clickActionLimit = 200

type clickAction struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// The event's catalog name, as funnels and trends know it.
	Event        string `json:"event"`
	Label        string `json:"label"`
	LabelMatch   string `json:"label_match"`
	HrefContains string `json:"href_contains"`
	ElementID    string `json:"element_id"`
	Class        string `json:"class"`
	Path         string `json:"path"`
	Enabled      bool   `json:"enabled"`
	// How many of the last 7 days' captured clicks it matches.
	Matches7d int       `json:"matches_7d"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// click is what a captured click carries, as the script sent it.
type click struct {
	Tag     string `json:"tag"`
	Text    string `json:"text"`
	Href    string `json:"href"`
	ID      string `json:"id"`
	Classes string `json:"classes"`
	Path    string `json:"path"`
}

// matches says whether a click is the one the action names: every part of
// the rule that is set must match.
func (a clickAction) matches(c click) bool {
	if a.Label != "" {
		have, want := strings.ToLower(strings.TrimSpace(c.Text)), strings.ToLower(a.Label)
		if (a.LabelMatch == "contains" && !strings.Contains(have, want)) || (a.LabelMatch != "contains" && have != want) {
			return false
		}
	}
	if a.HrefContains != "" && !strings.Contains(strings.ToLower(c.Href), strings.ToLower(a.HrefContains)) {
		return false
	}
	if a.ElementID != "" && c.ID != a.ElementID {
		return false
	}
	if a.Class != "" {
		found := false
		for _, cl := range strings.Fields(c.Classes) {
			found = found || cl == a.Class
		}
		if !found {
			return false
		}
	}
	return a.Path == "" || clickPathMatches(a.Path, c.Path)
}

// clickPathMatches compares a page with a rule's path, where * stands for
// any run of characters. A trailing slash makes no difference.
func clickPathMatches(rule, path string) bool {
	trim := func(p string) string {
		if len(p) > 1 {
			p = strings.TrimRight(p, "/")
		}
		if p == "" {
			return "/"
		}
		return p
	}
	rule, path = trim(rule), trim(path)
	if !strings.Contains(rule, "*") {
		return rule == path
	}
	parts := strings.Split(rule, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	ok, err := regexp.MatchString("^"+strings.Join(parts, ".*")+"$", path)
	return err == nil && ok
}

const clickActionColumns = `id::text, name, label, label_match, href_contains, element_id, class, path, enabled, created_at, updated_at`

func scanClickAction(row pgx.CollectableRow) (clickAction, error) {
	var a clickAction
	err := row.Scan(&a.ID, &a.Name, &a.Label, &a.LabelMatch, &a.HrefContains, &a.ElementID, &a.Class, &a.Path, &a.Enabled, &a.CreatedAt, &a.UpdatedAt)
	a.Event, _ = eventNameFor(a.Name)
	return a, err
}

func loadClickActions(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ws, brand string, onlyEnabled bool) ([]clickAction, error) {
	rows, err := q.Query(ctx, `SELECT `+clickActionColumns+` FROM click_actions WHERE workspace_id = $1 AND brand_id = $2 AND (enabled OR NOT $3) ORDER BY lower(name)`, ws, brand, onlyEnabled)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanClickAction)
	if list == nil {
		list = []clickAction{}
	}
	return list, err
}

// recentClicks are the brand's captured clicks of the last days, newest
// first, at most 20,000: enough to say what a rule would match.
func (s *Server) recentClicks(ctx context.Context, ws, brand string, days int) ([]click, error) {
	rows, err := s.pool.Query(ctx, `SELECT coalesce(properties->>'tag', ''), coalesce(properties->>'text', ''), coalesce(properties->>'href', ''),
			coalesce(properties->>'id', ''), coalesce(properties->>'classes', ''), coalesce(properties->>'path', context->'page'->>'path', '')
		FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= $4 ORDER BY occurred_at DESC LIMIT 20000`,
		ws, brand, clickEvent, time.Now().AddDate(0, 0, -days))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (click, error) {
		var c click
		return c, row.Scan(&c.Tag, &c.Text, &c.Href, &c.ID, &c.Classes, &c.Path)
	})
}

func (s *Server) listClickActions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := loadClickActions(r.Context(), s.pool, a.WorkspaceID, a.Brand, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	clicks, err := s.recentClicks(r.Context(), a.WorkspaceID, a.Brand, 7)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		for _, c := range clicks {
			if list[i].matches(c) {
				list[i].Matches7d++
			}
		}
	}
	var capturing bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM site_keys WHERE workspace_id = $1 AND brand_id = $2 AND revoked_at IS NULL AND capture_clicks)`,
		a.WorkspaceID, a.Brand).Scan(&capturing); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "capturing": capturing, "clicks_7d": len(clicks)})
}

type seenClick struct {
	click
	Count int `json:"count"`
	// The named click it already counts as, if any.
	Action string `json:"action"`
}

// listSeenClicks groups the brand's captured clicks of the last days by
// what was clicked, most clicked first, so a person can name one from a
// list instead of writing a rule.
func (s *Server) listSeenClicks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days, ok := intParam(w, r, "days", 7, 1, 90)
	if !ok {
		return
	}
	clicks, err := s.recentClicks(r.Context(), a.WorkspaceID, a.Brand, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	actions, err := loadClickActions(r.Context(), s.pool, a.WorkspaceID, a.Brand, true)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type key struct{ tag, text, href, id string }
	at := map[key]int{}
	out := []seenClick{}
	for _, c := range clicks {
		k := key{c.Tag, strings.ToLower(strings.TrimSpace(c.Text)), c.Href, c.ID}
		i, seen := at[k]
		if !seen {
			i = len(out)
			at[k] = i
			out = append(out, seenClick{click: c})
		}
		out[i].Count++
		// The same button can sit on many pages: the group has a name
		// when any of its clicks does.
		if out[i].Action == "" {
			for _, act := range actions {
				if act.matches(c) {
					out[i].Action = act.Name
					break
				}
			}
		}
	}
	// Most clicked first; the newest first among equals, as they came.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Count > out[j-1].Count; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > 200 {
		out = out[:200]
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "days": days, "clicks": len(clicks)})
}

type clickActionInput struct {
	Name         *string `json:"name"`
	Label        *string `json:"label"`
	LabelMatch   *string `json:"label_match"`
	HrefContains *string `json:"href_contains"`
	ElementID    *string `json:"element_id"`
	Class        *string `json:"class"`
	Path         *string `json:"path"`
	Enabled      *bool   `json:"enabled"`
}

// reservedClickNames are the script's own events: naming a click one of
// them would count it twice.
var reservedClickNames = map[string]bool{"element_clicked": true, "form_submitted": true, "error_caught": true, "experiment_viewed": true, "experiment_change_missed": true}

// apply puts the input on an action and checks the result.
func (in clickActionInput) apply(a *clickAction) error {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&a.Name, in.Name)
	set(&a.Label, in.Label)
	set(&a.LabelMatch, in.LabelMatch)
	set(&a.HrefContains, in.HrefContains)
	set(&a.ElementID, in.ElementID)
	set(&a.Class, in.Class)
	set(&a.Path, in.Path)
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if a.LabelMatch == "" {
		a.LabelMatch = "exact"
	}
	event, ok := eventNameFor(a.Name)
	switch {
	case a.Name == "" || len(a.Name) > 100 || !ok:
		return errInvalid("name is the event's name, 1 to 100 characters with letters in it, such as Signup Clicked")
	case strings.Contains(event, "."):
		return errInvalid("name can't have a dot in it: dotted names are Outflare's own")
	case reservedClickNames[event]:
		return errInvalid("that name is one the website script already uses: pick another")
	case a.LabelMatch != "exact" && a.LabelMatch != "contains":
		return errInvalid("label_match is exact or contains")
	case len(a.Label) > 200 || len(a.ElementID) > 200 || len(a.Class) > 200 || len(a.HrefContains) > 500:
		return errInvalid("label, element_id and class are at most 200 characters, href_contains 500")
	case strings.ContainsAny(a.Class, " \t\n"):
		return errInvalid("class is one class name, without spaces")
	case a.Path != "" && (!strings.HasPrefix(a.Path, "/") || len(a.Path) > 200 || strings.ContainsAny(a.Path, "?# \t\n")):
		return errInvalid("path starts with /, such as /pricing or /blog/*, without a query string")
	case a.Label == "" && a.HrefContains == "" && a.ElementID == "" && a.Class == "":
		return errInvalid("say which click it is: its label, a part of where it goes (href_contains), its element_id or a class")
	}
	a.Event = event
	return nil
}

func (s *Server) createClickAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in clickActionInput
	if !decode(w, r, &in) {
		return
	}
	act := clickAction{Enabled: true}
	if err := in.apply(&act); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM click_actions WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= clickActionLimit {
			return errInvalid("a brand has at most 200 named clicks: delete one you no longer use")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO click_actions (workspace_id, brand_id, name, label, label_match, href_contains, element_id, class, path, enabled, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text, created_at, updated_at`,
			a.WorkspaceID, a.Brand, act.Name, act.Label, act.LabelMatch, act.HrefContains, act.ElementID, act.Class, act.Path, act.Enabled, nullableString(a.ActorID)).
			Scan(&act.ID, &act.CreatedAt, &act.UpdatedAt); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "click_action.created", "click_action", act.ID, map[string]any{"brand_id": a.Brand, "name": act.Name})
	})
	if isUniqueViolation(err) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "this brand already has a named click called that")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, act)
}

func (s *Server) oneClickAction(w http.ResponseWriter, r *http.Request) (clickAction, bool) {
	a := accessFrom(r.Context())
	if id := r.PathValue("id"); validUUID(id) {
		rows, err := s.pool.Query(r.Context(), `SELECT `+clickActionColumns+` FROM click_actions WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, a.WorkspaceID, a.Brand, id)
		if err == nil {
			var list []clickAction
			if list, err = pgx.CollectRows(rows, scanClickAction); err == nil && len(list) == 1 {
				return list[0], true
			}
		}
		if err != nil {
			s.internal(w, r, err)
			return clickAction{}, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "named click not found")
	return clickAction{}, false
}

func (s *Server) updateClickAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	act, ok := s.oneClickAction(w, r)
	if !ok {
		return
	}
	var in clickActionInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.apply(&act); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `UPDATE click_actions SET name = $3, label = $4, label_match = $5, href_contains = $6, element_id = $7, class = $8, path = $9, enabled = $10, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 RETURNING updated_at`,
			act.ID, a.WorkspaceID, act.Name, act.Label, act.LabelMatch, act.HrefContains, act.ElementID, act.Class, act.Path, act.Enabled).Scan(&act.UpdatedAt); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "click_action.updated", "click_action", act.ID, map[string]any{"brand_id": a.Brand, "name": act.Name, "enabled": act.Enabled},
			map[string]any{"enabled": act.Enabled})
	})
	if isUniqueViolation(err) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "this brand already has a named click called that")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, act)
}

func (s *Server) deleteClickAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	act, ok := s.oneClickAction(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM click_actions WHERE id = $1 AND workspace_id = $2`, act.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "click_action.deleted", "click_action", act.ID, map[string]any{"brand_id": a.Brand, "name": act.Name})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// namedClicks are the copies of a captured click to store under the names
// the brand gave it: the same message with another event name and its own
// messageId, so a retry stores each once.
func namedClicks(raw json.RawMessage, actions []clickAction) []json.RawMessage {
	var m map[string]json.RawMessage
	if len(actions) == 0 || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var c click
	_ = json.Unmarshal(m["properties"], &c)
	if c.Path == "" {
		var ctx struct {
			Page struct {
				Path string `json:"path"`
			} `json:"page"`
		}
		_ = json.Unmarshal(m["context"], &ctx)
		c.Path = ctx.Page.Path
	}
	var msgID string
	_ = json.Unmarshal(m["messageId"], &msgID)
	var out []json.RawMessage
	for _, a := range actions {
		if !a.matches(c) {
			continue
		}
		cp := map[string]json.RawMessage{}
		for k, v := range m {
			cp[k] = v
		}
		cp["event"], _ = json.Marshal(a.Name)
		if msgID != "" {
			cp["messageId"], _ = json.Marshal(cutUTF8(msgID, 150) + ":" + a.ID)
		} else {
			delete(cp, "messageId")
		}
		if b, err := json.Marshal(cp); err == nil {
			out = append(out, b)
		}
	}
	return out
}
