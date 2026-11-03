package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/actionctx"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/missions"
)

// The action layer (ADR 13). Every change an agent token makes goes
// through actionGate when the token's action_gate is on: the most specific
// grant a person set for the token runs it, holds it for a person or
// refuses it, and every change is kept as an action. Approving a held
// change replays it through the full handler as the agent, so every
// scope, limit, policy check and audit rule applies when it runs. It
// replaces the mission gate (ADR 7), which keeps working beside it until
// missions are retired.

func (s *Server) routesActions(mux *http.ServeMux) {
	read, approve, manage := auth.ScopeActionsRead, auth.ScopeActionsApprove, auth.ScopeActionsManage
	base := "/v1/workspaces/{ws}/actions"
	// Brand-limited callers are let in, kept to their brands: an agent
	// kept to one brand reads its own held changes and their decisions.
	mux.HandleFunc("GET "+base, s.inBrand(read, s.listActions))
	mux.HandleFunc("POST "+base+"/decide", s.inBrand(approve, s.decideActions))
	mux.HandleFunc("GET "+base+"/{id}", s.inBrand(read, s.getAction))
	mux.HandleFunc("POST "+base+"/{id}/approve", s.inBrand(approve, s.approveAction))
	mux.HandleFunc("POST "+base+"/{id}/reject", s.inBrand(approve, s.rejectAction))
	grants := "/v1/workspaces/{ws}/action-grants"
	mux.HandleFunc("GET "+grants, s.inBrand(read, s.listActionGrants))
	mux.HandleFunc("POST "+grants, s.inWorkspace(manage, s.createActionGrant))
	mux.HandleFunc("DELETE "+grants+"/{id}", s.inWorkspace(manage, s.revokeActionGrant))
}

// A held change nobody decides by then counts as expired.
const actionHoldFor = 30 * 24 * time.Hour

// The largest body kept with an action, so a person can approve exactly
// what will run.
const actionBodyMax = 1 << 20

// actionStatusSQL is an action's status as shown: a held change past its
// expiry is expired, without a sweep.
const actionStatusSQL = `CASE WHEN x.status = 'held' AND x.expires_at < now() THEN 'expired' ELSE x.status END`

// Action statuses.
const (
	actionHeld      = "held"
	actionRunning   = "running"
	actionExecuted  = "executed"
	actionFailed    = "failed"
	actionRefused   = "refused"
	actionRejected  = "rejected"
	actionExpired   = "expired"
	actionCancelled = "cancelled"
)

// Why the gate decided what it did.
const (
	ruleNoGrant      = "no_grant"
	ruleGrant        = "grant"
	ruleDailyLimit   = "daily_limit"
	ruleSpendLimit   = "spend_limit"
	ruleAlwaysPerson = "always_person"
)

// isActionRoute reports whether r reads or decides actions or grants.
// Those calls are not gated: deciding is a person's (or an approver
// token's), and setting grants is a signed-in person's alone.
func isActionRoute(r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"+r.PathValue("ws"))
	return strings.HasPrefix(rest, "/actions") || strings.HasPrefix(rest, "/action-grants")
}

// actionPath is a request's path as grants match it: the part after
// /v1/workspaces/{ws}/, or after /v1/ for other routes ("brands/{id}/...").
func actionPath(r *http.Request) string { return pathPattern(r.URL.Path) }

// pathPattern is a URL path as grants match it: the part after
// /v1/workspaces/{ws}/, or after /v1/ for other routes.
func pathPattern(urlPath string) string {
	p := strings.TrimPrefix(urlPath, "/v1/")
	if rest, ok := strings.CutPrefix(p, "workspaces/"); ok {
		if _, after, ok := strings.Cut(rest, "/"); ok {
			p = after
		}
	}
	return strings.Trim(p, "/")
}

// ---- Grants ----

type actionGrantView struct {
	ID         string  `json:"id"`
	TokenID    string  `json:"token_id"`
	TokenName  *string `json:"token_name"`
	BrandID    *string `json:"brand_id"`
	Method     *string `json:"method"`
	Path       string  `json:"path"`
	Mode       string  `json:"mode"`
	DailyLimit *int    `json:"daily_limit"`
	// What changes under this grant may spend on the workspace's own
	// accounts a day and a month, in cents; null for no limit.
	DailySpendCents   *int64 `json:"daily_spend_cents"`
	MonthlySpendCents *int64 `json:"monthly_spend_cents"`
	TotalSpendCents   *int64 `json:"total_spend_cents"`
	// A template: fields every matching change's body must contain.
	Match     map[string]any `json:"match"`
	Note      string         `json:"note"`
	CreatedBy *string        `json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
	RevokedAt *time.Time     `json:"revoked_at"`
}

const grantColumns = `g.id, g.token_id::text, t.name, g.brand_id::text, g.method, g.path, g.mode, g.daily_limit, g.daily_spend_cents, g.monthly_spend_cents, g.total_spend_cents, g.body_match, g.note,
	g.created_by::text, g.created_at, g.revoked_at`

const grantFrom = `FROM action_grants g LEFT JOIN api_tokens t ON t.id = g.token_id`

func scanGrant(row pgx.Row) (actionGrantView, error) {
	var v actionGrantView
	err := row.Scan(&v.ID, &v.TokenID, &v.TokenName, &v.BrandID, &v.Method, &v.Path, &v.Mode, &v.DailyLimit, &v.DailySpendCents, &v.MonthlySpendCents, &v.TotalSpendCents, &v.Match, &v.Note,
		&v.CreatedBy, &v.CreatedAt, &v.RevokedAt)
	return v, err
}

// validGrantPath checks a grant's path pattern: segments of letters,
// digits, - and _, or * for one segment, with an optional trailing **.
func validGrantPath(p string) bool {
	if p == "" || len(p) > 300 {
		return false
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		switch {
		case seg == "**":
			if i != len(segs)-1 {
				return false
			}
		case seg == "*":
		case seg == "":
			return false
		default:
			for _, c := range seg {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
					return false
				}
			}
		}
	}
	return true
}

// grantMatches reports whether pattern matches path, and how specific
// the match is: literal segments count most, then pattern length.
func grantMatches(pattern, path string) (bool, int) {
	ps, xs := strings.Split(pattern, "/"), strings.Split(path, "/")
	score := 0
	for i, seg := range ps {
		if i >= len(xs) {
			return false, 0
		}
		if seg == "**" {
			// The rest, one segment or more: "leads/**" is what is under
			// leads, not the collection itself.
			return true, score + i
		}
		switch seg {
		case "*":
			score++
		default:
			if seg != xs[i] {
				return false, 0
			}
			score += 10
		}
	}
	if len(ps) != len(xs) {
		return false, 0
	}
	return true, score + len(ps)
}

type grantRule struct {
	id         string
	method     *string
	path       string
	mode       string
	dailyLimit *int
	dailySpend *int64
	monthSpend *int64
	totalSpend *int64
	match      map[string]any
}

// bodyMatches reports whether a change's body holds every field of match
// with the same value: a template approved once.
func bodyMatches(match map[string]any, body map[string]any) bool {
	for k, want := range match {
		got, ok := body[k]
		if !ok {
			return false
		}
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		if string(a) != string(b) {
			return false
		}
	}
	return true
}

// pickGrant chooses the grant that decides a change: the one naming the
// method beats one that doesn't, then a template (matching the body)
// beats one without, then the most specific path. On a tie the stricter
// mode wins (refuse, then hold, then run).
func pickGrant(grants []grantRule, method, path string, body map[string]any) (grantRule, bool) {
	strict := map[string]int{"refuse": 2, "hold": 1, "run": 0}
	var best grantRule
	bestScore, found := -1, false
	for _, g := range grants {
		if g.method != nil && *g.method != method {
			continue
		}
		ok, score := grantMatches(g.path, path)
		if !ok {
			continue
		}
		if g.match != nil {
			if !bodyMatches(g.match, body) {
				continue
			}
			score += 1000 * len(g.match)
		}
		if g.method != nil {
			score += 100000
		}
		if !found || score > bestScore || (score == bestScore && strict[g.mode] > strict[best.mode]) {
			best, bestScore, found = g, score, true
		}
	}
	return best, found
}

func (s *Server) listActionGrants(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	where, args := "g.workspace_id = $1 AND g.revoked_at IS NULL", []any{a.WorkspaceID}
	where += actionBrandSQL(sel, "g.brand_id", &args)
	if t := r.URL.Query().Get("token_id"); t != "" {
		if !validUUID(t) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "token_id is not an id")
			return
		}
		args = append(args, t)
		where += " AND g.token_id = $" + itoa(len(args))
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+grantColumns+` `+grantFrom+` WHERE `+where+` ORDER BY g.created_at DESC LIMIT 1000`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (actionGrantView, error) { return scanGrant(r) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createActionGrant(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.TokenID != "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a signed-in person sets what an agent may do")
		return
	}
	var req struct {
		TokenID    string         `json:"token_id"`
		Brand      *string        `json:"brand"`
		Method     *string        `json:"method"`
		Path       string         `json:"path"`
		Mode       string         `json:"mode"`
		DailyLimit *int           `json:"daily_limit"`
		DailySpend *int64         `json:"daily_spend_cents"`
		MonthSpend *int64         `json:"monthly_spend_cents"`
		TotalSpend *int64         `json:"total_spend_cents"`
		Match      map[string]any `json:"match"`
		Note       string         `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	for name, v := range map[string]*int64{"daily_spend_cents": req.DailySpend, "monthly_spend_cents": req.MonthSpend, "total_spend_cents": req.TotalSpend} {
		switch {
		case v != nil && (*v < 1 || *v > 1_000_000_000):
			writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is 1 to 1,000,000,000 cents")
			return
		case v != nil && req.Mode != "run":
			writeError(w, r, http.StatusBadRequest, "invalid_request", name+" only applies to a grant that runs changes")
			return
		}
	}
	if msg := checkMatch(req.Match); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	req.Path = strings.Trim(strings.TrimSpace(req.Path), "/")
	switch {
	case !validUUID(req.TokenID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "token_id is required: the API token this grant is for")
		return
	case !validGrantPath(req.Path):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "path is the part after /v1/workspaces/{ws}/, with * for one segment and a trailing ** for the rest, such as sequences/*/enrollments or **")
		return
	case req.Mode != "run" && req.Mode != "hold" && req.Mode != "refuse":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "mode is run, hold or refuse")
		return
	case req.DailyLimit != nil && (*req.DailyLimit < 1 || *req.DailyLimit > 100000):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit is 1 to 100,000")
		return
	case req.DailyLimit != nil && req.Mode != "run":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit only applies to a grant that runs changes")
		return
	case len(req.Note) > 500:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is up to 500 characters")
		return
	}
	if req.Method != nil {
		m := strings.ToUpper(*req.Method)
		if m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch && m != http.MethodDelete {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "method is POST, PUT, PATCH or DELETE, or leave it out for any")
			return
		}
		req.Method = &m
	}
	ctx := r.Context()
	var brand *string
	if req.Brand != nil {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, *req.Brand)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found: brand is a brand's id or handle")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	}
	v, err := s.insertGrant(ctx, a, grantInput{tokenID: req.TokenID, brand: brand, method: req.Method, path: req.Path, mode: req.Mode,
		dailyLimit: req.DailyLimit, dailySpend: req.DailySpend, monthSpend: req.MonthSpend, totalSpend: req.TotalSpend, match: req.Match, note: strings.TrimSpace(req.Note)})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "token_id names no active API token in this workspace")
		return
	}
	var invalid errInvalid
	if errors.As(err, &invalid) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", invalid.Error())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

type grantInput struct {
	tokenID    string
	brand      *string
	method     *string
	path, mode string
	dailyLimit *int
	dailySpend *int64
	monthSpend *int64
	totalSpend *int64
	match      map[string]any
	note       string
	fromAction string
}

// checkMatch checks a template's fields: up to 20 top-level fields of a
// JSON body, each a string, number, boolean or null.
func checkMatch(m map[string]any) string {
	if m == nil {
		return ""
	}
	if len(m) == 0 || len(m) > 20 {
		return "match lists 1 to 20 fields of the request body, such as {\"template_id\": \"...\"}"
	}
	for k, v := range m {
		switch v.(type) {
		case string, float64, bool, nil:
		default:
			return "match field " + k + " must be a string, number, boolean or null"
		}
	}
	return ""
}

// insertGrant stores a grant and records it, in one transaction.
func (s *Server) insertGrant(ctx context.Context, a *access, in grantInput) (actionGrantView, error) {
	var v actionGrantView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) (err error) {
		v, err = insertGrantTx(ctx, tx, a, in)
		return err
	})
	return v, err
}

// insertGrantTx makes a grant in tx, with its audit event.
// Paid ads spend caps a run grant on the ads routes gets unless the person
// sets its own: $50 a day and $500 a month (the owner's defaults), small
// enough that a first autonomous grant can't surprise anyone; a person
// raises them per grant. Only grants on paid ads paths get them: a `**`
// grant (an autonomous run's) keeps its one budget.
const (
	adsDailySpendDefault   int64 = 5000
	adsMonthlySpendDefault int64 = 50000
)

// adsPath reports whether a grant path is paid ads: ads, what is under it,
// or the same under a brand.
func adsPath(p string) bool {
	p = strings.TrimPrefix(p, "brands/*/")
	return p == "ads" || strings.HasPrefix(p, "ads/")
}

func insertGrantTx(ctx context.Context, tx pgx.Tx, a *access, in grantInput) (actionGrantView, error) {
	var v actionGrantView
	var match []byte
	if in.match != nil {
		match, _ = json.Marshal(in.match)
	}
	if in.mode == "run" && adsPath(in.path) {
		if in.dailySpend == nil {
			d := adsDailySpendDefault
			in.dailySpend = &d
		}
		if in.monthSpend == nil {
			m := adsMonthlySpendDefault
			in.monthSpend = &m
		}
	}
	err := func() error {
		if err := checkGrantToken(ctx, tx, a.WorkspaceID, in.tokenID, in.brand); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO action_grants (workspace_id, brand_id, token_id, method, path, mode, daily_limit, daily_spend_cents, monthly_spend_cents, total_spend_cents, body_match, note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
			a.WorkspaceID, in.brand, in.tokenID, in.method, in.path, in.mode, in.dailyLimit, in.dailySpend, in.monthSpend, in.totalSpend, match, in.note, a.ActorID).Scan(&id); err != nil {
			return err
		}
		var err error
		if v, err = scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` `+grantFrom+` WHERE g.id = $1`, id)); err != nil {
			return err
		}
		data := map[string]any{"token_id": in.tokenID, "brand_id": in.brand, "method": in.method, "path": in.path, "mode": in.mode, "daily_limit": in.dailyLimit,
			"daily_spend_cents": in.dailySpend, "monthly_spend_cents": in.monthSpend, "total_spend_cents": in.totalSpend, "match": in.match}
		if in.fromAction != "" {
			data["action_id"] = in.fromAction
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "action_grant.created", "action_grant", id, data,
			map[string]any{"mode": in.mode, "has_daily_limit": in.dailyLimit != nil, "template": in.match != nil, "from_action": in.fromAction != ""})
	}()
	return v, err
}

// checkGrantToken checks that a grant's token is an active token of the
// workspace, and that a grant kept to a brand is for a token bound to
// that brand: the gate matches grants by the token's brand, so any other
// brand would never match.
func checkGrantToken(ctx context.Context, tx pgx.Tx, ws, tokenID string, brand *string) error {
	var tokenBrand *string
	err := tx.QueryRow(ctx, `SELECT brand_id::text FROM api_tokens WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, tokenID, ws).Scan(&tokenBrand)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if brand != nil && (tokenBrand == nil || *tokenBrand != *brand) {
		return errInvalid("brand must be the brand this token is bound to; leave it out for a token that isn't bound to one")
	}
	return nil
}

func (s *Server) revokeActionGrant(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.TokenID != "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a signed-in person sets what an agent may do")
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "grant not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE action_grants SET revoked_at = now(), revoked_by = $3 WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL`,
			id, a.WorkspaceID, a.ActorID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "action_grant.revoked", "action_grant", id, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "grant not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- The gate ----

type ctxActionReplayKey struct{}

// actionHash is what a person approves: the exact method, path, query
// and body.
func actionHash(method, path, query string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method + "\n" + path + "\n" + query + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// alwaysPerson is why a change always waits for a person, whatever the
// token's grants say, or "" when it doesn't.
func (s *Server) alwaysPerson(ctx context.Context, ws string, r *http.Request) (string, error) {
	if heldForBuiltin(r.Method, r.URL.Path) {
		return "changes to mailboxes, sending, sign-ins, tokens, webhooks, connections, suppressions and company context always need a person", nil
	}
	if missionDecision.MatchString(r.URL.Path) {
		return "approving or resuming a mission, or deciding its held changes, always needs a person", nil
	}
	return s.fixHold(ctx, ws, r)
}

// missionDecision is a mission's approval, resumption or a decision on
// one of its held changes: an agent never decides, on the action layer or
// the mission gate.
var missionDecision = regexp.MustCompile(`^/v1/workspaces/[^/]+/missions/[^/]+/(approve|resume|actions/[^/]+/(approve|reject))/?$`)

// actionGate runs one change made by an agent token whose action_gate is
// on: it decides the change by the token's grants, keeps it as an action,
// and runs it, holds it or refuses it.
func (s *Server) actionGate(w http.ResponseWriter, r *http.Request, a *access, next http.HandlerFunc) {
	ctx := r.Context()
	reason := cutRunes(strings.TrimSpace(r.Header.Get(missions.ReasonHeader)), 1000)
	if r.Body == nil {
		r.Body = http.NoBody
	}
	// A body up to 1 MiB is kept, so a person can approve exactly it. A
	// bigger one (an HTML broadcast) can still run under a grant: it is
	// passed through unread, and can't wait for a person.
	head, err := io.ReadAll(io.LimitReader(r.Body, actionBodyMax+1))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	big := len(head) > actionBodyMax
	var body []byte
	if big {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), r.Body))
	} else {
		body = head
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	path := actionPath(r)

	why, err := s.alwaysPerson(ctx, a.WorkspaceID, r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var tokenBrand *string
	if len(a.Brands) == 1 {
		tokenBrand = &a.Brands[0]
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	rows, err := s.pool.Query(ctx, `SELECT id, method, path, mode, daily_limit, daily_spend_cents, monthly_spend_cents, total_spend_cents, body_match FROM action_grants
		WHERE token_id = $1 AND workspace_id = $2 AND revoked_at IS NULL AND (brand_id IS NULL OR brand_id = $3)`, a.TokenID, a.WorkspaceID, tokenBrand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	grants, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (grantRule, error) {
		var g grantRule
		err := row.Scan(&g.id, &g.method, &g.path, &g.mode, &g.dailyLimit, &g.dailySpend, &g.monthSpend, &g.totalSpend, &g.match)
		return g, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	g, found := pickGrant(grants, r.Method, path, parsed)

	status, rule, holdWhy := actionHeld, ruleNoGrant, "no grant lets this agent make this change on its own"
	var grantID *string
	if found {
		grantID = &g.id
		switch g.mode {
		case "run":
			status, rule, holdWhy = actionRunning, ruleGrant, ""
		case "hold":
			status, rule, holdWhy = actionHeld, ruleGrant, "this agent's grant holds this change for a person"
		case "refuse":
			status, rule, holdWhy = actionRefused, ruleGrant, "this agent's grant refuses this change"
		}
	}
	if status == actionRunning && why == "" {
		// Paid ads: a budget raise past a fifth always needs a person, and
		// a change that would put the brand's campaigns over the grant's
		// spend caps waits for one.
		adsRule, adsWhy, err := s.adsHold(ctx, a.WorkspaceID, r, parsed, g)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if adsWhy != "" {
			status, rule, holdWhy = actionHeld, adsRule, adsWhy
		}
	}
	if why != "" && status != actionRefused {
		status, rule, holdWhy = actionHeld, ruleAlwaysPerson, why
	}
	var tokenID *string
	if a.TokenID != "" {
		tokenID = &a.TokenID
	}
	// What a person approves is exactly what runs: the hash covers the
	// body as it is kept (valid UTF-8).
	hash := ""
	stored := ""
	if !big {
		clean := bytes.ToValidUTF8(body, nil)
		hash = actionHash(r.Method, r.URL.Path, r.URL.RawQuery, clean)
		if status == actionHeld || status == actionRunning {
			stored = string(clean)
		}
	}
	brand, err := s.actionBrand(ctx, a, r, tokenBrand, big, body)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var actionID string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if big && status == actionHeld {
			return errBigHeld(holdWhy)
		}
		if status == actionRunning && g.dailyLimit != nil {
			// One change at a time per grant while counting today's.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('action_grant:' || $1))`, g.id); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM actions WHERE grant_id = $1 AND status IN ('running', 'executed', 'failed')
				AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, g.id).Scan(&n); err != nil {
				return err
			}
			if n >= *g.dailyLimit {
				status, rule, holdWhy = actionHeld, ruleDailyLimit, "this agent reached its grant's daily limit, so this change waits for a person"
				if big {
					return errBigHeld(holdWhy)
				}
			}
		}
		if status == actionRunning && (g.dailySpend != nil || g.monthSpend != nil || g.totalSpend != nil) {
			// One change at a time per grant while adding up its spend: what
			// its changes have spent so far, plus what the next one is
			// likely to (the average of its recent changes), must fit.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('action_grant:' || $1))`, g.id); err != nil {
				return err
			}
			var day, month, total, reserve int64
			if err := tx.QueryRow(ctx, `SELECT
				coalesce(sum(e.amount_cents) FILTER (WHERE e.occurred_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0),
				coalesce(sum(e.amount_cents) FILTER (WHERE e.occurred_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0),
				coalesce(sum(e.amount_cents), 0),
				coalesce((SELECT avg(t.cents) FROM (SELECT sum(e2.amount_cents) AS cents FROM actions x2 JOIN spend_entries e2 ON e2.action_id = x2.id
					WHERE x2.grant_id = $1 AND x2.created_at > now() - interval '7 days' GROUP BY x2.id) t), 0)::bigint
				FROM actions x JOIN spend_entries e ON e.action_id = x.id WHERE x.grant_id = $1`, g.id).Scan(&day, &month, &total, &reserve); err != nil {
				return err
			}
			switch {
			case g.dailySpend != nil && day+reserve > *g.dailySpend:
				status, rule, holdWhy = actionHeld, ruleSpendLimit, fmt.Sprintf("this agent's changes have spent $%.2f of the grant's $%.2f a day, so this change waits for a person", float64(day)/100, float64(*g.dailySpend)/100)
			case g.monthSpend != nil && month+reserve > *g.monthSpend:
				status, rule, holdWhy = actionHeld, ruleSpendLimit, fmt.Sprintf("this agent's changes have spent $%.2f of the grant's $%.2f a month, so this change waits for a person", float64(month)/100, float64(*g.monthSpend)/100)
			case g.totalSpend != nil && total+reserve > *g.totalSpend:
				status, rule, holdWhy = actionHeld, ruleSpendLimit, fmt.Sprintf("this agent's changes have spent $%.2f of the grant's $%.2f in all, so this change waits for a person", float64(total)/100, float64(*g.totalSpend)/100)
			}
			if status == actionHeld && big {
				return errBigHeld(holdWhy)
			}
		}
		var expires *time.Time
		if status == actionHeld {
			t := time.Now().Add(actionHoldFor)
			expires = &t
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO actions (workspace_id, brand_id, actor_id, token_id, grant_id, method, path, query, content_type, body, body_hash,
			                     reason, status, decided_by_rule, hold_reason, request_id, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
			a.WorkspaceID, brand, a.ActorID, tokenID, grantID, r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"),
			stored, hash, reason, status, rule, holdWhy, RequestID(ctx), expires).Scan(&actionID); err != nil {
			return err
		}
		if status == actionRunning {
			return nil
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "action."+status, "action", actionID,
			map[string]any{"method": r.Method, "path": r.URL.Path, "rule": rule},
			map[string]any{"rule": rule})
	})
	var bigHeld errBigHeld
	if errors.As(err, &bigHeld) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request",
			"a change with a body over 1 MiB can't wait for a person: "+string(bigHeld)+"; a person can make it, or grant this agent changes like it")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	metrics.ActionDecided(status, rule)

	switch status {
	case actionRefused:
		// A refusal or a hold is this moment's answer: a retry with the same
		// idempotency key after a person changed the grants, or decided,
		// goes through the gate again.
		noIdempotentReplay(r)
		writeError(w, r, http.StatusForbidden, "action_refused", holdWhy+"; a person can change the token's grants (createActionGrant)")
		return
	case actionHeld:
		noIdempotentReplay(r)
		v, err := s.findGateAction(ctx, a, actionID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"action":            v,
			"approval_required": true,
			"message":           "Held for approval: " + holdWhy + ". Nothing changed yet. Read the action to see the decision and result.",
		})
		return
	}
	rec := &teeRecorder{ResponseWriter: w, status: http.StatusOK}
	finished := func(final string, code int, result []byte) {
		if _, err := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE actions SET status = $2, result_status = $3, result = $4 WHERE id = $1`,
			actionID, final, code, result); err != nil {
			s.log.Error("action result", "err", err, "action_id", actionID, "request_id", RequestID(ctx))
		}
		metrics.ActionFinished(final)
	}
	defer func() {
		// A handler that panics leaves the action failed, not running for
		// good (and counting against the grant's daily limit).
		if p := recover(); p != nil {
			finished(actionFailed, http.StatusInternalServerError, nil)
			panic(p)
		}
	}()
	next(rec, r.WithContext(actionctx.WithID(r.Context(), actionID)))
	final := actionExecuted
	var result []byte
	if rec.status >= 300 {
		// The answer is kept with a failed run, so a person can see what
		// went wrong.
		final = actionFailed
		result = rec.buf.Bytes()
		if !json.Valid(result) {
			result, _ = json.Marshal(string(result))
		}
	}
	finished(final, rec.status, result)
}

// actionBrand is the brand a change is about, kept on the action so a
// person kept to some brands decides only their own: the token's brand
// when it is kept to one, else the brand route's, ?brand=, or the body's
// brand_id; nil for a change about shared records or no brand in
// particular.
func (s *Server) actionBrand(ctx context.Context, a *access, r *http.Request, tokenBrand *string, big bool, body []byte) (*string, error) {
	if tokenBrand != nil {
		return tokenBrand, nil
	}
	ref := r.PathValue("brand")
	if ref == "" {
		ref = r.URL.Query().Get("brand")
	}
	if ref == "" && !big && len(body) > 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var in struct {
			BrandID string `json:"brand_id"`
		}
		if json.Unmarshal(body, &in) == nil {
			ref = in.BrandID
		}
	}
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" || ref == "none" || ref == "all" {
		return nil, nil
	}
	id, err := s.brandIDFor(ctx, a.WorkspaceID, ref)
	if errors.Is(err, errNotFound) {
		// The handler answers the bad brand; the action carries none.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// errBigHeld is a change with a body over 1 MiB that would be held.
type errBigHeld string

func (e errBigHeld) Error() string { return string(e) }

// cutRunes keeps at most n characters of s.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// actionBrandSQL is the brand filter for actions and grants. An action or
// grant without a brand belongs to a workspace-wide agent, which works for
// every brand, so a picked brand shows its own agents' rows and those
// shared ones, as a brand-limited caller sees shared records; none shows
// only the shared ones.
func actionBrandSQL(b brandSel, col string, args *[]any) string {
	out := ""
	switch b.id {
	case "":
	case "none":
		out = ` AND ` + col + ` IS NULL`
	default:
		*args = append(*args, b.id)
		out = fmt.Sprintf(` AND (%s IS NULL OR %s = $%d)`, col, col, len(*args))
	}
	if b.in != nil {
		*args = append(*args, b.in)
		out += fmt.Sprintf(` AND (%s IS NULL OR %s = ANY($%d::uuid[]))`, col, col, len(*args))
	}
	return out
}

// ---- Views ----

type actionView struct {
	ID            string          `json:"id"`
	Summary       string          `json:"summary"`
	ActorID       *string         `json:"actor_id"`
	ActorName     *string         `json:"actor_name"`
	TokenID       *string         `json:"token_id"`
	BrandID       *string         `json:"brand_id"`
	GrantID       *string         `json:"grant_id"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	Query         string          `json:"query"`
	Body          json.RawMessage `json:"body"`
	BodyHash      string          `json:"body_hash"`
	Reason        string          `json:"reason"`
	Status        string          `json:"status"`
	DecidedByRule string          `json:"decided_by_rule"`
	HoldReason    string          `json:"hold_reason"`
	DecidedBy     *string         `json:"decided_by"`
	DecidedName   *string         `json:"decided_by_name"`
	DecidedAt     *time.Time      `json:"decided_at"`
	DecisionNote  string          `json:"decision_note"`
	ResultStatus  *int            `json:"result_status"`
	Result        json.RawMessage `json:"result"`
	RequestID     *string         `json:"request_id"`
	CreatedAt     time.Time       `json:"created_at"`
	// When a held change counts as expired if nobody decides it.
	ExpiresAt *time.Time `json:"expires_at"`
	// What the change spent on the workspace's own accounts, in cents.
	SpendCents  int64 `json:"spend_cents"`
	contentType string
	rawBody     string
}

const actionViewColumns = `x.id, x.actor_id::text, ac.display_name, x.token_id::text, x.brand_id::text, x.grant_id::text, x.method, x.path,
	x.query, x.body, x.content_type, x.body_hash, x.reason, ` + actionStatusSQL + `, x.decided_by_rule, x.hold_reason, x.decided_by::text, dc.display_name,
	x.decided_at, x.decision_note, x.result_status, x.result, x.request_id, x.created_at, x.expires_at,
	(SELECT coalesce(sum(e.amount_cents), 0) FROM spend_entries e WHERE e.action_id = x.id)`

const actionViewFrom = `FROM actions x LEFT JOIN actors ac ON ac.id = x.actor_id LEFT JOIN actors dc ON dc.id = x.decided_by`

func scanActionView(row pgx.Row) (actionView, error) {
	var v actionView
	var result []byte
	err := row.Scan(&v.ID, &v.ActorID, &v.ActorName, &v.TokenID, &v.BrandID, &v.GrantID, &v.Method, &v.Path,
		&v.Query, &v.rawBody, &v.contentType, &v.BodyHash, &v.Reason, &v.Status, &v.DecidedByRule, &v.HoldReason, &v.DecidedBy, &v.DecidedName,
		&v.DecidedAt, &v.DecisionNote, &v.ResultStatus, &result, &v.RequestID, &v.CreatedAt, &v.ExpiresAt, &v.SpendCents)
	if err != nil {
		return v, err
	}
	switch {
	case v.rawBody == "":
	case json.Valid([]byte(v.rawBody)):
		v.Body = json.RawMessage(v.rawBody)
	default:
		b, _ := json.Marshal(v.rawBody)
		v.Body = b
	}
	if len(result) > 0 {
		v.Result = result
	}
	v.Summary = summarize(v.Method, v.Path)
	return v, nil
}

func (s *Server) findGateAction(ctx context.Context, a *access, id string) (actionView, error) {
	if !validUUID(id) {
		return actionView{}, errNotFound
	}
	v, err := scanActionView(s.pool.QueryRow(ctx, `SELECT `+actionViewColumns+` `+actionViewFrom+` WHERE x.id = $1 AND x.workspace_id = $2`, id, a.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !seesAction(a, v)) {
		return actionView{}, errNotFound
	}
	return v, err
}

// seesAction reports whether the caller may see an action. A caller kept
// to some brands sees only changes about one of them: a change about no
// brand in particular may touch any brand's data, so it is theirs to
// decide only with every brand.
func seesAction(a *access, v actionView) bool {
	if a.seenBrands() != nil && v.BrandID == nil {
		return false
	}
	return a.sees(v.BrandID)
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	where, args := []string{"x.workspace_id = $1"}, []any{a.WorkspaceID}
	if c := strings.TrimPrefix(actionBrandSQL(sel, "x.brand_id", &args), " AND "); c != "" {
		where = append(where, c)
	}
	if a.seenBrands() != nil {
		where = append(where, "x.brand_id IS NOT NULL")
	}
	if st := q.Get("status"); st != "" {
		args = append(args, strings.Split(st, ","))
		where = append(where, actionStatusSQL+" = ANY($"+itoa(len(args))+")")
	}
	if t := q.Get("token_id"); t != "" {
		if !validUUID(t) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "token_id is not an id")
			return
		}
		args = append(args, t)
		where = append(where, "x.token_id = $"+itoa(len(args)))
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	args = append(args, limit+1, offset)
	rows, err := s.pool.Query(r.Context(), `SELECT `+actionViewColumns+` `+actionViewFrom+` WHERE `+strings.Join(where, " AND ")+
		` ORDER BY x.created_at DESC, x.id DESC LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (actionView, error) { return scanActionView(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageOffset, next)})
}

func (s *Server) getAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.findGateAction(r.Context(), a, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "action not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- Deciding ----

// errActionState is a decision the action's state doesn't allow.
type errActionState string

func (e errActionState) Error() string { return string(e) }

func (s *Server) actionError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if status, code, msg, ok := decisionError(err); ok {
		writeError(w, r, status, code, msg)
	} else {
		s.internal(w, r, err)
	}
	return true
}

// decisionError sorts an error from decide into a status, a code and a
// message; ok is false for an internal error.
func decisionError(err error) (status int, code, msg string, ok bool) {
	var st errActionState
	var denied errDenied
	switch {
	case errors.Is(err, errNotFound):
		return http.StatusNotFound, "not_found", "action not found", true
	case errors.As(err, &st):
		return http.StatusConflict, "action_state", st.Error(), true
	case errors.As(err, &denied):
		return http.StatusForbidden, "forbidden", denied.Error(), true
	}
	return 0, "", "", false
}

// decide approves (and runs) or rejects one held action. also runs in the
// decision's transaction once the action is marked, before anything runs,
// for what the decision carries (a remembered template).
func (s *Server) decide(ctx context.Context, a *access, id string, approve bool, note string, also func(ctx context.Context, tx pgx.Tx, act actionView) error) (actionView, error) {
	var act actionView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if act, err = scanActionView(tx.QueryRow(ctx, `SELECT `+actionViewColumns+` `+actionViewFrom+`
			WHERE x.id = $1 AND x.workspace_id = $2 FOR UPDATE OF x`, id, a.WorkspaceID)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		if !seesAction(a, act) {
			return errNotFound
		}
		if act.Status == actionExpired {
			// Lazily recorded, so the row says what the list showed.
			if _, err := tx.Exec(ctx, `UPDATE actions SET status = 'expired' WHERE id = $1 AND status = 'held'`, id); err != nil {
				return err
			}
			return errActionState("this action expired: nobody decided it within 30 days, so the agent has to make the change again")
		}
		if act.Status != actionHeld {
			return errActionState("this action is already " + act.Status)
		}
		if act.ActorID != nil && *act.ActorID == a.ActorID {
			return errDenied("an agent cannot decide on its own changes")
		}
		if a.TokenID != "" && a.TokenActionGate {
			// An agent never decides on changes, its own or another agent's:
			// deciding is a person's, or an approver token a person set up
			// outside the action layer.
			return errDenied("an agent never decides on changes; a person or an approver token does")
		}
		next := actionRejected
		if approve {
			next = actionRunning
		}
		if _, err := tx.Exec(ctx, `UPDATE actions SET status = $2, decided_by = $3, decided_at = now(), decision_note = $4 WHERE id = $1`,
			id, next, a.ActorID, note); err != nil {
			return err
		}
		verb := "rejected"
		if approve {
			verb = "approved"
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "action."+verb, "action", id,
			map[string]any{"method": act.Method, "path": act.Path, "note": note}, map[string]any{"rule": act.DecidedByRule}); err != nil {
			return err
		}
		if also != nil {
			return also(ctx, tx, act)
		}
		return nil
	})
	if err != nil {
		return act, err
	}
	if !approve {
		metrics.ActionFinished(actionRejected)
		return s.findGateAction(ctx, a, id)
	}
	code, result, runErr := s.replayAction(ctx, act)
	status := actionExecuted
	if runErr != nil || code >= 300 {
		status = actionFailed
	}
	if runErr != nil {
		code = 0
		result, _ = json.Marshal(map[string]any{"error": map[string]string{"code": "not_run", "message": runErr.Error()}})
	}
	if !json.Valid(result) {
		result, _ = json.Marshal(string(result))
	}
	if _, err := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE actions SET status = $2, result_status = NULLIF($3, 0), result = $4 WHERE id = $1`,
		id, status, code, result); err != nil {
		return act, err
	}
	metrics.ActionFinished(status)
	return s.findGateAction(ctx, a, id)
}

// replayAction runs a held change as the agent that made it, through the
// full handler, so every rule applies again at the moment it runs.
func (s *Server) replayAction(ctx context.Context, act actionView) (int, []byte, error) {
	var tokenID string
	if act.TokenID != nil {
		tokenID = *act.TokenID
	}
	if tokenID == "" {
		return 0, nil, errors.New("the agent's token no longer exists, so the change was not run")
	}
	p, err := s.agentPrincipal(ctx, tokenID)
	if errors.Is(err, ErrAgentGone) {
		return 0, nil, errors.New("the agent's token was revoked or expired, so the change was not run")
	}
	if err != nil {
		return 0, nil, err
	}
	target := act.Path
	if act.Query != "" {
		target += "?" + act.Query
	}
	rctx := actionctx.WithID(context.WithValue(context.WithValue(context.WithoutCancel(ctx), ctxInjectedKey{}, p), ctxActionReplayKey{}, act.ID), act.ID)
	req, err := http.NewRequestWithContext(rctx, act.Method, target, strings.NewReader(act.rawBody))
	if err != nil {
		return 0, nil, err
	}
	if act.contentType != "" {
		req.Header.Set("Content-Type", act.contentType)
	}
	req.Header.Set(missions.ReasonHeader, "Approved: "+act.Reason)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// approveAction runs a held change. With remember, a signed-in person
// also approves the pattern: a run grant for the same agent, method and
// path whose template is the fields of this change's body they name, so
// changes like it run from then on.
func (s *Server) approveAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Note     string `json:"note"`
		Remember *struct {
			Fields     []string `json:"fields"`
			DailyLimit *int     `json:"daily_limit"`
		} `json:"remember"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if len(req.Note) > 50000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is up to 50,000 characters")
		return
	}
	ctx := r.Context()
	var also func(ctx context.Context, tx pgx.Tx, act actionView) error
	if req.Remember != nil {
		if a.TokenID != "" || !a.has(auth.ScopeActionsManage) {
			writeError(w, r, http.StatusForbidden, "forbidden", "approving a template sets what an agent may do: a signed-in person with actions.manage only")
			return
		}
		if d := req.Remember.DailyLimit; d != nil && (*d < 1 || *d > 100000) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "remember.daily_limit is 1 to 100,000")
			return
		}
		act, err := s.findGateAction(ctx, a, r.PathValue("id"))
		if s.actionError(w, r, err) {
			return
		}
		in, msg := templateFrom(act, req.Remember.Fields)
		if msg != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		in.dailyLimit = req.Remember.DailyLimit
		// The template is made in the decision's transaction: approved
		// and remembered together, or neither.
		also = func(ctx context.Context, tx pgx.Tx, _ actionView) error {
			_, err := insertGrantTx(ctx, tx, a, in)
			return err
		}
	}
	v, err := s.decide(ctx, a, r.PathValue("id"), true, strings.TrimSpace(req.Note), also)
	if s.actionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// templateFrom turns a held action into a run grant for the same agent,
// method and exact path, matching the named fields of its body.
func templateFrom(act actionView, fields []string) (grantInput, string) {
	if act.TokenID == nil {
		return grantInput{}, "the agent's token no longer exists"
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(act.rawBody), &body); err != nil && len(fields) > 0 {
		return grantInput{}, "remember.fields needs a JSON body; leave fields out to approve every change to this path"
	}
	var match map[string]any
	for _, f := range fields {
		val, ok := body[f]
		if !ok {
			return grantInput{}, "remember.fields names " + f + ", which this change's body doesn't have"
		}
		if match == nil {
			match = map[string]any{}
		}
		match[f] = val
	}
	if msg := checkMatch(match); msg != "" {
		return grantInput{}, strings.Replace(msg, "match", "remember.fields", 1)
	}
	path := pathPattern(act.Path)
	if !validGrantPath(path) {
		return grantInput{}, "this change's path can't be made into a template"
	}
	method := act.Method
	return grantInput{tokenID: *act.TokenID, brand: act.BrandID, method: &method, path: path, mode: "run", match: match,
		note: "Approved as a template from a held change", fromAction: act.ID}, ""
}

func (s *Server) rejectAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	note, ok := readText(w, r, "note")
	if !ok {
		return
	}
	v, err := s.decide(r.Context(), a, r.PathValue("id"), false, note, nil)
	if s.actionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// decideActions approves and rejects held actions in one request: a batch.
// Each is decided on its own, so one that can't be decided doesn't stop
// the rest; the answer says what happened to each.
func (s *Server) decideActions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Approve []string `json:"approve"`
		Reject  []string `json:"reject"`
		Note    string   `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	n := len(req.Approve) + len(req.Reject)
	switch {
	case n == 0:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send approve, reject or both: lists of held action ids")
		return
	case n > 100:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "decide up to 100 actions at a time")
		return
	case len(req.Note) > 50000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is up to 50,000 characters")
		return
	}
	for _, id := range req.Approve {
		if slices.Contains(req.Reject, id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "action "+id+" is in both approve and reject")
			return
		}
	}
	type batchError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	type outcome struct {
		ID     string      `json:"id"`
		Action *actionView `json:"action"`
		Error  *batchError `json:"error"`
	}
	seen := map[string]bool{}
	var out []outcome
	run := func(ids []string, approve bool) {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			v, err := s.decide(r.Context(), a, id, approve, strings.TrimSpace(req.Note), nil)
			o := outcome{ID: id}
			if err == nil {
				o.Action = &v
			} else if _, code, msg, ok := decisionError(err); ok {
				o.Error = &batchError{Code: code, Message: msg}
			} else {
				s.log.Error("deciding an action", "err", err, "action_id", id, "request_id", RequestID(r.Context()))
				o.Error = &batchError{Code: "internal", Message: "internal error"}
			}
			out = append(out, o)
		}
	}
	run(req.Approve, true)
	run(req.Reject, false)
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// ---- paid ads rules ----

var (
	adCampaignChange = regexp.MustCompile(`^/v1/workspaces/[^/]+/ads/campaigns/([0-9a-fA-F-]{36})(/resume)?/?$`)
	// adBudgetRaiseMax is the largest raise of a campaign's daily budget
	// an agent makes on its own: a fifth. More always needs a person.
	adBudgetRaiseMax = 0.2
)

// adsHold decides the paid ads rules for an agent's change to a campaign
// (PATCH .../ads/campaigns/{id} with a daily budget, or POST .../resume):
// the rule and why it waits for a person, or "" to let it run. A pause
// spends nothing and is never held here. The brand's total is what its
// active campaigns would be budgeted a day after the change, in cents of
// their accounts' currencies.
func (s *Server) adsHold(ctx context.Context, ws string, r *http.Request, body map[string]any, g grantRule) (string, string, error) {
	m := adCampaignChange.FindStringSubmatch(strings.TrimSuffix(r.URL.Path, "/"))
	if m == nil {
		return "", "", nil
	}
	resume := m[2] != ""
	if (resume && r.Method != http.MethodPost) || (!resume && r.Method != http.MethodPatch) {
		return "", "", nil
	}
	var newBudget *int64
	if !resume {
		v, ok := body["daily_budget_micros"].(float64)
		if !ok {
			return "", "", nil
		}
		b := int64(v)
		newBudget = &b
	}
	var current *int64
	var brand *string
	var status string
	err := s.pool.QueryRow(ctx, `SELECT e.daily_budget_micros, e.status, COALESCE(e.brand_id, a.brand_id) FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE e.workspace_id = $1 AND e.id = $2 AND e.level = 'campaign'`, ws, m[1]).Scan(&current, &status, &brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil // the handler answers the missing campaign
	}
	if err != nil {
		return "", "", err
	}
	if newBudget != nil {
		switch {
		case current == nil || *current <= 0:
			return ruleAlwaysPerson, "setting a budget on a campaign that had none is a new paid commitment, so a person always approves it", nil
		case float64(*newBudget) > float64(*current)*(1+adBudgetRaiseMax):
			return ruleAlwaysPerson, fmt.Sprintf("raising a campaign's daily budget by more than a fifth (from %s to %s a day) always needs a person", micros(*current), micros(*newBudget)), nil
		case *newBudget <= *current:
			return "", "", nil // lowering spends less
		}
	}
	if g.dailySpend == nil && g.monthSpend == nil {
		return "", "", nil
	}
	// The brand's active campaigns' budgets a day after the change.
	var others int64
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(sum(e.daily_budget_micros), 0) FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE e.workspace_id = $1 AND e.level = 'campaign' AND e.status = 'active' AND a.status = 'active' AND e.id <> $2
		AND COALESCE(e.brand_id, a.brand_id) IS NOT DISTINCT FROM $3`, ws, m[1], brand).Scan(&others); err != nil {
		return "", "", err
	}
	mine := int64(0)
	if current != nil {
		mine = *current
	}
	if newBudget != nil {
		mine = *newBudget
	}
	dailyCents := (others + mine) / 10_000
	if g.dailySpend != nil && dailyCents > *g.dailySpend {
		return ruleSpendLimit, fmt.Sprintf("with this change the brand's campaigns would be budgeted $%.2f a day, over the grant's $%.2f, so it waits for a person", float64(dailyCents)/100, float64(*g.dailySpend)/100), nil
	}
	if g.monthSpend != nil {
		// What the month has spent so far, plus the daily total for the
		// rest of it (today included).
		now := time.Now().UTC()
		var spent int64
		if err := s.pool.QueryRow(ctx, `SELECT coalesce(sum(amount_cents), 0) FROM spend_entries WHERE workspace_id = $1 AND channel = $2
			AND occurred_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AND brand_id IS NOT DISTINCT FROM $3`, ws, adsync.Channel, brand).Scan(&spent); err != nil {
			return "", "", err
		}
		monthEnd := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		left := int64(monthEnd.Sub(now).Hours()/24) + 1
		if projected := spent + dailyCents*left; projected > *g.monthSpend {
			return ruleSpendLimit, fmt.Sprintf("with this change the brand's ads would come to about $%.2f this month ($%.2f spent, $%.2f a day for %d more days), over the grant's $%.2f, so it waits for a person",
				float64(projected)/100, float64(spent)/100, float64(dailyCents)/100, left, float64(*g.monthSpend)/100), nil
		}
	}
	return "", "", nil
}

// micros writes a money amount in micros as $12 or $12.50.
func micros(v int64) string {
	cents := v / 10_000
	if cents%100 == 0 {
		return fmt.Sprintf("$%d", cents/100)
	}
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}
