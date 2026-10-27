package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Request logs: every API call made in a workspace, whoever made it (the
// web app, a script, the CLI, an MCP agent or a mission), for the Logs
// page, GET .../logs and the CLI's logs commands. Only what describes the
// call is kept, never bodies, query values, headers or keys, so a log can
// be read and exported without leaking customer data or secrets.
//
// Rows are written in batches after the response, so logging never slows a
// request down, and kept for the retention period (DefaultLogRetention).

// DefaultLogRetention is how long request logs are kept unless the
// operator sets OUTFLARE_LOG_RETENTION_DAYS.
const DefaultLogRetention = 30 * 24 * time.Hour

func (s *Server) routesLogs(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/logs", s.inBrand(auth.ScopeAuditRead, s.listLogs))
	mux.HandleFunc("GET /v1/workspaces/{ws}/logs/export", s.inBrand(auth.ScopeAuditRead, s.exportLogs))
	mux.HandleFunc("GET /v1/workspaces/{ws}/logs/histogram", s.inBrand(auth.ScopeAuditRead, s.logHistogram))
	mux.HandleFunc("GET /v1/workspaces/{ws}/logs/{id}", s.inBrand(auth.ScopeAuditRead, s.getLog))
}

// requestLog is one row as logRequests hands it over.
type requestLog struct {
	WorkspaceID string
	RequestID   string
	Method      string
	Route       string
	Operation   string
	Path        string
	QueryKeys   []string
	Status      int
	DurationMS  int64
	ErrorCode   string
	ErrorParam  string
	Surface     string
	Client      string
	MCPTool     string
	Agent       string
	UserAgent   string
	MissionID   string
	ActorID     string
	ActorType   string
	TokenID     string
	BrandID     string
	ProjectID   string
	At          time.Time
}

// logged reports whether a request goes in the workspace's request log:
// every call in a workspace except reading the logs themselves, which the
// Logs page does every few seconds while it follows new calls.
func logged(r *http.Request, route string) bool {
	if r.Method == http.MethodGet && strings.HasPrefix(route, "GET /v1/workspaces/{ws}/logs") {
		return false
	}
	return true
}

// logWriter collects rows and inserts them in batches: once a second, or
// as soon as a batch is full. It runs no goroutine while idle.
type logWriter struct {
	s       *Server
	mu      sync.Mutex
	buf     []requestLog
	timer   *time.Timer
	writing sync.Mutex // one insert at a time, so rows land in order
}

const (
	logBatch    = 200
	logMaxQueue = 20000 // past this, rows are dropped rather than use unbounded memory
)

func (lw *logWriter) add(e requestLog) {
	lw.mu.Lock()
	if len(lw.buf) >= logMaxQueue {
		lw.mu.Unlock()
		lw.s.log.Warn("request log queue full; dropping a row", "request_id", e.RequestID)
		return
	}
	lw.buf = append(lw.buf, e)
	switch {
	case len(lw.buf) >= logBatch:
		go lw.flush(context.Background()) //nolint:errcheck
	case lw.timer == nil:
		lw.timer = time.AfterFunc(time.Second, func() { lw.flush(context.Background()) }) //nolint:errcheck
	}
	lw.mu.Unlock()
}

// flush writes what is queued.
func (lw *logWriter) flush(ctx context.Context) error {
	lw.writing.Lock()
	defer lw.writing.Unlock()
	lw.mu.Lock()
	rows := lw.buf
	lw.buf = nil
	if lw.timer != nil {
		lw.timer.Stop()
		lw.timer = nil
	}
	lw.mu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	err := lw.s.insertLogs(ctx, rows)
	if err != nil && ctx.Err() == nil {
		track.Fail(ctx, lw.s.log, track.Event{Name: "request_logs.write_failed", ActorType: "system", Surface: "system",
			Props: map[string]any{"rows": len(rows)}}, "internal", err)
	}
	return err
}

func (s *Server) insertLogs(ctx context.Context, rows []requestLog) error {
	n := len(rows)
	cols := make([][]string, 19)
	for i := range cols {
		cols[i] = make([]string, n)
	}
	qk := make([]string, n)
	durations := make([]int64, n)
	statuses := make([]int, n)
	ats := make([]time.Time, n)
	for i, e := range rows {
		for j, v := range []string{e.WorkspaceID, e.RequestID, e.Method, e.Route, e.Operation, e.Path, e.ErrorCode, e.Surface,
			e.Client, e.MCPTool, e.Agent, e.UserAgent, e.MissionID, e.ActorID, e.ActorType, e.TokenID, e.BrandID, e.ProjectID, e.ErrorParam} {
			cols[j][i] = v
		}
		b, _ := json.Marshal(e.QueryKeys)
		qk[i] = string(b)
		durations[i], statuses[i], ats[i] = e.DurationMS, e.Status, e.At
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO request_logs (workspace_id, request_id, method, route, operation, path, error_code, surface, client, mcp_tool,
			agent, user_agent, mission_id, actor_id, actor_type, token_id, brand_id, project_id, error_param, query_keys, status, duration_ms, created_at)
		SELECT t.ws::uuid, t.rid, t.m, t.route, t.op, t.path, t.ec, t.sf, t.cl, t.tool, t.ag, t.ua,
			NULLIF(t.mission, '')::uuid, NULLIF(t.actor, '')::uuid, t.at, NULLIF(t.tok, '')::uuid,
			NULLIF(t.brand, '')::uuid, NULLIF(t.proj, '')::uuid, t.ep,
			ARRAY(SELECT jsonb_array_elements_text(t.qk::jsonb)), t.st, t.dur, t.ts
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[],
			$10::text[], $11::text[], $12::text[], $13::text[], $14::text[], $15::text[], $16::text[], $17::text[], $18::text[],
			$19::text[], $20::text[], $21::int[], $22::bigint[], $23::timestamptz[])
			WITH ORDINALITY AS t(ws, rid, m, route, op, path, ec, sf, cl, tool, ag, ua, mission, actor, at, tok, brand, proj, ep, qk, st, dur, ts, n)
		WHERE EXISTS (SELECT 1 FROM workspaces w WHERE w.id = t.ws::uuid)
		ORDER BY t.n`,
		cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7], cols[8], cols[9], cols[10], cols[11],
		cols[12], cols[13], cols[14], cols[15], cols[16], cols[17], cols[18], qk, statuses, durations, ats)
	return err
}

// FlushLogs writes request logs still queued, for a clean shutdown.
func (s *Server) FlushLogs(ctx context.Context) error { return s.logs.flush(ctx) }

// PruneRequestLogs deletes request logs older than keep (DefaultLogRetention
// when zero), a batch at a time so a large backlog never holds a long lock.
func (s *Server) PruneRequestLogs(ctx context.Context, keep time.Duration) (int64, error) {
	if keep <= 0 {
		keep = DefaultLogRetention
	}
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `DELETE FROM request_logs WHERE id IN (
			SELECT id FROM request_logs WHERE created_at < $1 LIMIT 5000)`, time.Now().Add(-keep))
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 5000 {
			return total, nil
		}
	}
}

// noteBrand and noteProject say which brand and project a request was
// about, for its log row. The first one noted wins.
func noteBrand(ctx context.Context, id string) {
	if i := reqInfoFrom(ctx); i != nil && i.brandID == "" && validUUID(id) {
		i.brandID = id
	}
}

func noteProject(ctx context.Context, id string, brand *string) {
	if i := reqInfoFrom(ctx); i != nil && i.projectID == "" && validUUID(id) {
		i.projectID = id
		if brand != nil {
			noteBrand(ctx, *brand)
		}
	}
}

// noteAccessScope notes the brand or project a caller is bound to.
func noteAccessScope(r *http.Request, a *access) {
	if a.ProjectID != nil {
		noteProject(r.Context(), *a.ProjectID, nil)
	}
	if len(a.Brands) == 1 {
		noteBrand(r.Context(), a.Brands[0])
	}
}

// queryKeys lists a query string's parameter names, never their values,
// which can be search terms or email addresses.
func queryKeys(r *http.Request) []string {
	keys := []string{}
	for k := range r.URL.Query() {
		if k = clean(k); k != "" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys[:min(len(keys), 20)]
}

// ---- Reading ----

type logView struct {
	ID         string    `json:"id"`
	RequestID  string    `json:"request_id"`
	CreatedAt  time.Time `json:"created_at"`
	Level      string    `json:"level"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	Operation  string    `json:"operation"`
	Path       string    `json:"path"`
	QueryKeys  []string  `json:"query_keys"`
	Status     int       `json:"status"`
	DurationMS int       `json:"duration_ms"`
	ErrorCode  *string   `json:"error_code"`
	ErrorParam *string   `json:"error_param"`
	Surface    string    `json:"surface"`
	Client     *string   `json:"client"`
	MCPTool    *string   `json:"mcp_tool"`
	Agent      *string   `json:"agent"`
	UserAgent  *string   `json:"user_agent"`
	MissionID  *string   `json:"mission_id"`
	ActorID    *string   `json:"actor_id"`
	ActorType  *string   `json:"actor_type"`
	ActorName  *string   `json:"actor_name"`
	TokenID    *string   `json:"token_id"`
	BrandID    *string   `json:"brand_id"`
	ProjectID  *string   `json:"project_id"`
}

const logCols = `l.id, l.request_id, l.created_at, l.method, l.route, l.operation, l.path, l.query_keys, l.status, l.duration_ms,
	NULLIF(l.error_code, ''), NULLIF(l.error_param, ''), l.surface, NULLIF(l.client, ''), NULLIF(l.mcp_tool, ''), NULLIF(l.agent, ''), NULLIF(l.user_agent, ''),
	l.mission_id::text, l.actor_id::text, NULLIF(l.actor_type, ''), a.display_name, l.token_id::text, l.brand_id::text, l.project_id::text`

const logFrom = ` FROM request_logs l LEFT JOIN actors a ON a.id = l.actor_id`

func scanLog(row pgx.Row) (logView, error) {
	var v logView
	var id int64
	err := row.Scan(&id, &v.RequestID, &v.CreatedAt, &v.Method, &v.Route, &v.Operation, &v.Path, &v.QueryKeys, &v.Status, &v.DurationMS,
		&v.ErrorCode, &v.ErrorParam, &v.Surface, &v.Client, &v.MCPTool, &v.Agent, &v.UserAgent, &v.MissionID, &v.ActorID, &v.ActorType, &v.ActorName,
		&v.TokenID, &v.BrandID, &v.ProjectID)
	v.ID = "log_" + strconv.FormatInt(id, 10)
	v.Level = logLevel(v.Status)
	return v, err
}

func logLevel(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "warning"
	}
	return "info"
}

var (
	logLevels   = []string{"info", "warning", "error"}
	logSurfaces = []string{"web", "api", "mcp", "cli", "sdk", "mission"}
)

// logFilter turns the shared query parameters of the log endpoints into a
// WHERE clause on request_logs l, or writes a 400 or 404.
func (s *Server) logFilter(w http.ResponseWriter, r *http.Request) (string, []any, bool) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	args := []any{a.WorkspaceID}
	where := `l.workspace_id = $1`
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	bad := func(msg string) (string, []any, bool) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return "", nil, false
	}

	if v := splitList(q.Get("level")); v != nil {
		var conds []string
		for _, l := range v {
			switch l {
			case "info":
				conds = append(conds, "l.status < 400")
			case "warning":
				conds = append(conds, "(l.status >= 400 AND l.status < 500)")
			case "error":
				conds = append(conds, "l.status >= 500")
			default:
				return bad("level is info, warning or error (comma-separated for more than one)")
			}
		}
		where += " AND (" + strings.Join(conds, " OR ") + ")"
	}
	if v := splitList(q.Get("status")); v != nil {
		var conds []string
		for _, st := range v {
			if len(st) == 3 && st[1:] == "xx" && st[0] >= '1' && st[0] <= '5' {
				lo := int(st[0]-'0') * 100
				conds = append(conds, fmt.Sprintf("(l.status >= %d AND l.status < %d)", lo, lo+100))
				continue
			}
			n, err := strconv.Atoi(st)
			if err != nil || n < 100 || n > 599 {
				return bad("status is a status code (404) or class (4xx), comma-separated for more than one")
			}
			conds = append(conds, "l.status = "+strconv.Itoa(n))
		}
		where += " AND (" + strings.Join(conds, " OR ") + ")"
	}
	if v := splitList(q.Get("surface")); v != nil {
		for _, sf := range v {
			if !slices.Contains(logSurfaces, sf) {
				return bad("surface is web, api, mcp, cli, sdk or mission (comma-separated for more than one)")
			}
		}
		where += " AND l.surface = ANY(" + arg(v) + "::text[])"
	}
	if v := splitList(q.Get("method")); v != nil {
		for i := range v {
			v[i] = strings.ToUpper(v[i])
		}
		where += " AND l.method = ANY(" + arg(v) + "::text[])"
	}
	if v := splitList(q.Get("operation")); v != nil {
		where += " AND l.operation = ANY(" + arg(v) + "::text[])"
	}
	if v := splitList(q.Get("error_code")); v != nil {
		where += " AND l.error_code = ANY(" + arg(v) + "::text[])"
	}
	if v := q.Get("mcp_tool"); v != "" {
		where += " AND l.mcp_tool = " + arg(v)
	}
	if v := q.Get("request_id"); v != "" {
		where += " AND l.request_id = " + arg(v)
	}
	for _, p := range []struct{ name, col string }{{"actor_id", "actor_id"}, {"token_id", "token_id"}, {"mission_id", "mission_id"}, {"project_id", "project_id"}} {
		if v := q.Get(p.name); v != "" {
			if !validUUID(v) {
				return bad(p.name + " is an id")
			}
			where += " AND l." + p.col + " = " + arg(v) + "::uuid"
		}
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return "", nil, false
	}
	// A brand-limited caller sees only calls about its brands: a call that
	// named no brand could have been about any of them.
	if sel.in != nil {
		where += " AND l.brand_id = ANY(" + arg(sel.in) + "::uuid[])"
	}
	sel.in = nil
	where += sel.sql("l.brand_id", &args)
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		if len(v) > 200 {
			return bad("q is at most 200 characters")
		}
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v) + "%"
		p := arg(like)
		where += fmt.Sprintf(` AND (l.path ILIKE %[1]s OR l.operation ILIKE %[1]s OR l.route ILIKE %[1]s OR l.error_code ILIKE %[1]s
			OR l.request_id ILIKE %[1]s OR l.mcp_tool ILIKE %[1]s OR l.agent ILIKE %[1]s OR l.client ILIKE %[1]s)`, p)
	}
	for _, p := range []struct{ name, op string }{{"since", ">="}, {"until", "<"}} {
		if v := q.Get(p.name); v != "" {
			t, err := parseLogTime(v)
			if err != nil {
				return bad(p.name + " is a time (2026-10-26T09:00:00Z) or a duration ago (15m, 24h, 7d)")
			}
			where += " AND l.created_at " + p.op + " " + arg(t)
		}
	}
	return where, args, true
}

// splitList splits a comma-separated parameter, or gives nil when it is empty.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseLogTime reads a time or how long ago: 30s, 15m, 24h, 7d.
func parseLogTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		d, err := strconv.Atoi(n)
		if err != nil || d < 0 || d > 3650 {
			return time.Time{}, errors.New("bad duration")
		}
		return time.Now().Add(-time.Duration(d) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return time.Time{}, errors.New("bad duration")
	}
	return time.Now().Add(-d), nil
}

func logID(v string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(v, "log_"), 10, 64)
	return n, err == nil && n > 0 && strings.HasPrefix(v, "log_")
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	before, after := q.Get("before"), q.Get("after")
	if before != "" && after != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "pass before or after, not both")
		return
	}
	where, args, ok := s.logFilter(w, r)
	if !ok {
		return
	}
	// Calls made a moment ago may still be queued on this server.
	s.logs.flush(r.Context()) //nolint:errcheck
	order := "DESC"
	switch {
	case before != "":
		id, ok := logID(before)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before is a log id (log_...) from next_before")
			return
		}
		args = append(args, id)
		where += " AND l.id < $" + strconv.Itoa(len(args))
	case after == "latest":
		// Start following from now: nothing yet, and next_after at the newest.
		var newest int64
		if err := s.pool.QueryRow(r.Context(), `SELECT coalesce(max(id), 0) FROM request_logs WHERE workspace_id = $1`, args[0]).Scan(&newest); err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []logView{}, "next_before": nil, "next_after": "log_" + strconv.FormatInt(newest, 10), "next_cursor": nil})
		return
	case after != "":
		id, err := strconv.ParseInt(strings.TrimPrefix(after, "log_"), 10, 64)
		if err != nil || !strings.HasPrefix(after, "log_") || id < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "after is a log id (log_...) from next_after, or latest")
			return
		}
		args = append(args, id)
		where += " AND l.id > $" + strconv.Itoa(len(args))
		order = "ASC"
	}
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+logCols+logFrom+` WHERE `+where+` ORDER BY l.id `+order+` LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (logView, error) { return scanLog(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	body := map[string]any{"data": list, "next_before": nil}
	if order == "ASC" {
		// Following: next_after is always set, so polling never loses its place.
		next := after
		if len(list) > 0 {
			next = list[len(list)-1].ID
		}
		body["next_after"] = next
		body["next_cursor"] = nil
		if len(list) == limit {
			body["next_cursor"] = encodeCursor("after", next)
		}
	} else {
		var next *string
		if len(list) == limit {
			next = &list[len(list)-1].ID
		}
		body["next_before"] = next
		body["next_after"] = nil
	}
	writeJSON(w, http.StatusOK, body)
}

// getLog is one call with what it caused: the audit log entries it wrote.
// id is a log id (log_...) or the call's request id.
func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ref := r.PathValue("id")
	var row pgx.Row
	if id, ok := logID(ref); ok {
		row = s.pool.QueryRow(r.Context(), `SELECT `+logCols+logFrom+` WHERE l.workspace_id = $1 AND l.id = $2`, a.WorkspaceID, id)
	} else {
		s.logs.flush(r.Context()) //nolint:errcheck
		row = s.pool.QueryRow(r.Context(), `SELECT `+logCols+logFrom+` WHERE l.workspace_id = $1 AND l.request_id = $2 ORDER BY l.id LIMIT 1`, a.WorkspaceID, ref)
	}
	v, err := scanLog(row)
	if err == nil && a.Brands != nil && (v.BrandID == nil || !slices.Contains(a.Brands, *v.BrandID)) {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "log not found: it may be older than the retention period")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id, e.actor_id::text, e.actor_type, ac.display_name, e.action, e.target_type, e.target_id, e.request_id, e.data, e.created_at, e.brand_id::text
		FROM audit_events e LEFT JOIN actors ac ON ac.id = e.actor_id
		WHERE e.workspace_id = $1 AND e.request_id = $2 ORDER BY e.id LIMIT 100`, a.WorkspaceID, v.RequestID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	audits, err := pgx.CollectRows(rows, pgx.RowToStructByPos[auditView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if a.Brands != nil {
		// Audit entries carry no brand; a brand-limited caller sees the call only.
		audits = []auditView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": v, "audit_events": audits})
}

func (s *Server) exportLogs(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format != "" && format != "csv" && format != "ndjson" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "format is csv or ndjson")
		return
	}
	where, args, ok := s.logFilter(w, r)
	if !ok {
		return
	}
	s.logs.flush(r.Context()) //nolint:errcheck
	const maxRows = 50000
	rows, err := s.pool.Query(r.Context(), `SELECT `+logCols+logFrom+` WHERE `+where+` ORDER BY l.id DESC LIMIT `+strconv.Itoa(maxRows), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (logView, error) { return scanLog(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	name := "outflare-logs-" + time.Now().UTC().Format("2006-01-02T150405Z")
	if format == "ndjson" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.ndjson"`)
		enc := json.NewEncoder(w)
		for _, v := range list {
			enc.Encode(v) //nolint:errcheck
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "time", "level", "method", "path", "operation", "status", "duration_ms", "error_code", "error_param", "surface", "client", //nolint:errcheck
		"mcp_tool", "agent", "actor_type", "actor", "token_id", "mission_id", "brand_id", "project_id", "request_id", "query_keys"})
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, v := range list {
		row := []string{v.ID, v.CreatedAt.UTC().Format(time.RFC3339Nano), v.Level, v.Method, v.Path, v.Operation, strconv.Itoa(v.Status),
			strconv.Itoa(v.DurationMS), str(v.ErrorCode), str(v.ErrorParam), v.Surface, str(v.Client), str(v.MCPTool), str(v.Agent), str(v.ActorType),
			str(v.ActorName), str(v.TokenID), str(v.MissionID), str(v.BrandID), str(v.ProjectID), v.RequestID, strings.Join(v.QueryKeys, " ")}
		for i, c := range row {
			row[i] = csvSafe(c)
		}
		cw.Write(row) //nolint:errcheck
	}
	cw.Flush()
}

// logHistogram counts calls by level over time, for the chart above the
// list. The bucket size follows the range: a minute for an hour, an hour
// for a day or two, a day beyond.
func (s *Server) logHistogram(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("since") == "" {
		q.Set("since", "24h")
		r.URL.RawQuery = q.Encode()
	}
	where, args, ok := s.logFilter(w, r)
	if !ok {
		return
	}
	since, _ := parseLogTime(q.Get("since"))
	until := time.Now()
	if v := q.Get("until"); v != "" {
		until, _ = parseLogTime(v)
	}
	span := until.Sub(since)
	bucket := time.Minute
	switch {
	case span > 3*24*time.Hour:
		bucket = 24 * time.Hour
	case span > 6*time.Hour:
		bucket = time.Hour
	case span > 90*time.Minute:
		bucket = 5 * time.Minute
	case span <= 20*time.Minute:
		bucket = 10 * time.Second // a zoomed-in chart still has bars to read
	}
	s.logs.flush(r.Context()) //nolint:errcheck
	args = append(args, int64(bucket.Seconds()))
	rows, err := s.pool.Query(r.Context(), `
		SELECT to_timestamp(floor(extract(epoch FROM l.created_at) / $`+strconv.Itoa(len(args))+`) * $`+strconv.Itoa(len(args))+`) AS t,
			count(*) FILTER (WHERE l.status < 400), count(*) FILTER (WHERE l.status >= 400 AND l.status < 500), count(*) FILTER (WHERE l.status >= 500)
		FROM request_logs l WHERE `+where+` GROUP BY t ORDER BY t`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type point struct {
		At      time.Time `json:"at"`
		Info    int       `json:"info"`
		Warning int       `json:"warning"`
		Error   int       `json:"error"`
	}
	points, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (point, error) {
		var p point
		err := row.Scan(&p.At, &p.Info, &p.Warning, &p.Error)
		p.At = p.At.UTC()
		return p, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket_seconds": int(bucket.Seconds()), "since": since.UTC(), "until": until.UTC(), "data": points})
}
