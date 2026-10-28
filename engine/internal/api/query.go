package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Query runs read-only SQL over the workspace's own data, for people and
// agents (run_query), so an answer such as "win rate by source this
// quarter" is computed exactly instead of paged together from lists.
//
// A query runs on its own connections, as a database role that can read
// only the views in the query schema (migration 0199). Each view shows one
// workspace's rows, the brands the caller may see, and nothing without the
// read scope the API asks for the same data. Which workspace, brands and
// scopes apply is a query_scopes row keyed by the connection's backend,
// written before the query and deleted after; the role can't read or
// change it. The transaction is read-only, a cursor takes a single SELECT,
// and the request's deadline cancels a slow one.

func (s *Server) routesQuery(mux *http.ServeMux) {
	// Any caller in the workspace: each table needs its own read scope.
	mux.HandleFunc("GET /v1/workspaces/{ws}/query/tables", s.inBrand("", s.listQueryTables))
	mux.HandleFunc("POST /v1/workspaces/{ws}/query", s.inBrand("", s.runQuery))
}

// readsOnly reports whether a POST only reads, so an agent kept to
// missions may send it outside one: a query's SQL is too long for a URL.
func readsOnly(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+r.PathValue("ws")+"/query"
}

const (
	queryDefaultRows = 100
	queryMaxRows     = 1000
	queryTimeout     = 15 * time.Second
	queryMaxBytes    = 4 << 20 // of the answer; rows past it are cut
	queryMaxSQL      = 20000
)

// queryTable is one view a query can read, and the scope it needs.
type queryTable struct {
	Name        string
	Scope       string
	Description string
}

// queryTables are the views in the query schema. A test checks the list
// and the schema agree.
var queryTables = []queryTable{
	{"people", auth.ScopeCustomersRead, "People, without merged or archived ones; email is their main email."},
	{"companies", auth.ScopeCustomersRead, "Companies, without merged or archived ones; domain is their main domain."},
	{"records", auth.ScopeCustomersRead, "Every customer record of every type, including merged (merged_into) and archived ones."},
	{"record_identities", auth.ScopeCustomersRead, "Emails, phones, user ids and domains of records; valid_to is set once closed."},
	{"record_relationships", auth.ScopeCustomersRead, "Links between records, such as a person's companies (field_key) or partner_of."},
	{"customer_events", auth.ScopeCustomersRead, "Product events: track, identify, page, screen, group and alias calls."},
	{"consents", auth.ScopeCustomersRead, "Every consent grant and withdrawal; the latest per record, channel, topic and purpose is current."},
	{"subscriptions", auth.ScopeCustomersRead, "Payment subscriptions from Stripe and other payment connections; monthly_minor is MRR in minor units."},
	{"brands", auth.ScopeWorkspaceRead, "The workspace's brands."},
	{"projects", auth.ScopeProjectsRead, "Projects and their offers."},
	{"leads", auth.ScopeLeadsRead, "Leads for outreach; hidden from callers kept to some brands."},
	{"sequences", auth.ScopeSequencesRead, "Outreach sequences."},
	{"enrollments", auth.ScopeSequencesRead, "Leads in sequences and where each is."},
	{"messages", auth.ScopeSequencesRead, "Outreach emails sent or queued, without bodies."},
	{"replies", auth.ScopeInboxRead, "Replies and other mail received, without bodies; category is the AI label."},
	{"deals", auth.ScopeOutcomesRead, "Deals; amount_cents in the deal's currency, state open, won or lost."},
	{"meetings", auth.ScopeOutcomesRead, "Meetings booked."},
	{"spend", auth.ScopeOutcomesRead, "Money spent, by channel, category and provider, in minor units."},
	{"emails", auth.ScopeEmailsRead, "Lifecycle and transactional emails, without bodies or addresses."},
	{"email_events", auth.ScopeEmailsRead, "What happened to each email: delivered, opened, clicked, bounced."},
	{"broadcasts", auth.ScopeEmailsRead, "Email broadcasts and how many they reached."},
	{"visits", auth.ScopeAnalyticsRead, "Website visits from the Outflare site snippet, with UTM tags."},
	{"social_posts", auth.ScopeSocialRead, "Posts on the workspace's own social accounts, with their numbers."},
	{"reviews", auth.ScopeSocialRead, "Reviews on the brands' review pages (Trustpilot, G2, Capterra, Google); rating is 1 to 5."},
	{"review_page_days", auth.ScopeSocialRead, "Each review page's numbers by day: the platform's score and count, new reviews, stars and Outflare's review score."},
}

type queryColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type queryTableView struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Scope       string        `json:"scope"`
	Available   bool          `json:"available"`
	Columns     []queryColumn `json:"columns"`
}

// queryReadable reports whether the caller may read a table.
func queryReadable(a *access, t queryTable) bool {
	if !a.has(t.Scope) {
		return false
	}
	// A support view never reads email content, sent or received.
	if a.ViewID != "" && t.Name == "messages" {
		return false
	}
	// Leads have no brand, so a caller kept to some brands sees none.
	return t.Name != "leads" || a.Brands == nil
}

func (s *Server) listQueryTables(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'query' ORDER BY table_name, ordinal_position`)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	cols := map[string][]queryColumn{}
	for rows.Next() {
		var t string
		var c queryColumn
		if err := rows.Scan(&t, &c.Name, &c.Type); err != nil {
			s.internal(w, r, err)
			return
		}
		cols[t] = append(cols[t], c)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]queryTableView, 0, len(queryTables))
	for _, t := range queryTables {
		out = append(out, queryTableView{Name: t.Name, Description: t.Description, Scope: t.Scope, Available: queryReadable(a, t), Columns: cols[t.Name]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

type queryInput struct {
	SQL   string `json:"sql"`
	Limit *int   `json:"limit"`
}

type queryResult struct {
	Columns    []queryColumn `json:"columns"`
	Rows       [][]any       `json:"rows"`
	RowCount   int           `json:"row_count"`
	Truncated  bool          `json:"truncated"`
	DurationMS int64         `json:"duration_ms"`
}

// queryRefused are functions a query may not call: changing settings
// (set_config can't escape the role, but could lift limits), holding locks
// other work waits on, signalling other sessions, server statistics, large
// objects and the functions that run SQL from a string. This is a courtesy
// with clear messages, not the boundary: the role, the read-only
// transaction, the deadline and a fresh connection per query are. U&
// escapes would hide the names.
var queryRefused = regexp.MustCompile(`(?i)\b(set_config|pg_(try_)?advisory\w*|pg_sleep\w*|pg_terminate_backend|pg_cancel_backend|pg_reload_conf|pg_rotate_logfile|pg_stat\w*|lo_\w+|dblink\w*|txid_current\w*|pg_notify|\w*_to_xml\w*|ts_stat)\b|\bu&['"]`)

func (s *Server) runQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req queryInput
	if !decode(w, r, &req) {
		return
	}
	sql := strings.TrimSpace(req.SQL)
	for strings.HasSuffix(sql, ";") {
		sql = strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	}
	switch {
	case sql == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sql is one SELECT over the tables GET .../query/tables lists")
		return
	case len(sql) > queryMaxSQL:
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("sql is at most %d characters", queryMaxSQL))
		return
	}
	limit := queryDefaultRows
	if req.Limit != nil {
		if *req.Limit < 1 || *req.Limit > queryMaxRows {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit is 1 to %d rows", queryMaxRows))
			return
		}
		limit = *req.Limit
	}
	if m := queryRefused.FindString(sql); m != "" {
		writeError(w, r, http.StatusBadRequest, "query_invalid", "queries can't call "+strings.TrimRight(m, `'"`)+": read the tables with SELECT")
		return
	}
	// A clearer answer than no rows: name the table and the scope it needs.
	lower := strings.ToLower(sql)
	var scopes, used []string
	for _, t := range queryTables {
		mentioned := regexp.MustCompile(`\b` + t.Name + `\b`).MatchString(lower)
		if queryReadable(a, t) {
			if !slices.Contains(scopes, t.Scope) {
				scopes = append(scopes, t.Scope)
			}
			if mentioned {
				used = append(used, t.Name)
			}
			continue
		}
		if mentioned {
			msg := s.missingScope(a, t.Scope)
			if a.has(t.Scope) {
				msg = t.Name + " has no brand, so it is hidden from callers kept to some brands"
			}
			writeError(w, r, http.StatusForbidden, "forbidden", "this query reads "+t.Name+": "+msg)
			return
		}
	}

	if len(scopes) == 0 {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can't read any of the query tables: each needs its read scope, such as customers.read for people")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	start := time.Now()
	conn, err := s.queryConn(ctx, a.WorkspaceID)
	if err != nil && ctx.Err() == nil {
		metrics.QueryRan("unavailable", time.Since(start), 0)
		s.log.Error("query: no query role", "err", err, "request_id", RequestID(ctx))
		writeError(w, r, http.StatusServiceUnavailable, "query_unavailable", "Query needs its own read-only database role, and Outflare couldn't set it up. "+
			"The server's log says why; the database user needs to be able to create roles (CREATEROLE)")
		return
	}
	var res queryResult
	if err == nil {
		res, err = s.queryRun(ctx, conn, a, scopes, sql, limit)
		conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck
		s.query.slots <- struct{}{}
	}
	res.DurationMS = time.Since(start).Milliseconds()
	var pgErr *pgconn.PgError
	outcome := "success"
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		outcome = "timeout"
	case errors.As(err, &pgErr):
		outcome = "invalid"
	case err != nil:
		outcome = "error"
	}
	metrics.QueryRan(outcome, time.Since(start), res.RowCount)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		writeError(w, r, http.StatusBadRequest, "query_timeout", fmt.Sprintf("the query ran for over %s: filter it more, or count instead of listing", queryTimeout))
		return
	case errors.As(err, &pgErr):
		msg := pgErr.Message
		if pgErr.Code == "42501" {
			msg += ": queries read only the tables GET .../query/tables lists, such as people or deals"
		}
		if pgErr.Hint != "" {
			msg += " (" + pgErr.Hint + ")"
		}
		writeError(w, r, http.StatusBadRequest, "query_invalid", msg)
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	info := reqInfoFrom(ctx)
	ev := track.Event{Name: "query.ran", UserID: a.UserID, ActorID: a.ActorID, ActorType: a.ActorType, WorkspaceID: a.WorkspaceID, RequestID: RequestID(ctx),
		Props: map[string]any{"tables": used, "rows": res.RowCount, "truncated": res.Truncated, "duration_ms": res.DurationMS, "columns": len(res.Columns)}}
	if info != nil {
		ev.Surface = surface(r, info)
	}
	track.Emit(r.Context(), ev)
	writeJSON(w, http.StatusOK, res)
}

// queryRun runs sql on a query connection with the caller's scope.
func (s *Server) queryRun(ctx context.Context, conn *pgx.Conn, a *access, scopes []string, sql string, limit int) (queryResult, error) {
	res := queryResult{Columns: []queryColumn{}, Rows: [][]any{}}
	pid := int(conn.PgConn().PID())
	var brands []string
	if a.Brands != nil {
		brands = a.Brands
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO query_scopes (pid, workspace_id, brands, scopes) VALUES ($1, $2, $3, $4)
		ON CONFLICT (pid) DO UPDATE SET workspace_id = EXCLUDED.workspace_id, brands = EXCLUDED.brands, scopes = EXCLUDED.scopes, created_at = now()`,
		pid, a.WorkspaceID, brands, scopes); err != nil {
		return res, err
	}
	defer func() {
		// A fresh context: the request's may be done, and the row must go.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := s.pool.Exec(dctx, `DELETE FROM query_scopes WHERE pid = $1`, pid); err != nil {
			s.log.Error("query: scope not removed", "err", err, "pid", pid)
		}
	}()
	err := pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		// The first statement fixes the snapshot, so the query can no
		// longer make the transaction read-write.
		if _, err := tx.Exec(ctx, `SELECT 1`); err != nil {
			return err
		}
		// The extended protocol, always: it refuses a second statement,
		// where pgx would send a query without arguments as simple text.
		if res := tx.Conn().PgConn().ExecParams(ctx, "DECLARE outflare_query NO SCROLL CURSOR FOR "+sql+"\n", nil, nil, nil, nil).Read(); res.Err != nil {
			return res.Err
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("FETCH FORWARD %d FROM outflare_query", limit+1))
		if err != nil {
			return err
		}
		defer rows.Close()
		tm := conn.TypeMap()
		for _, f := range rows.FieldDescriptions() {
			name := "unknown"
			if t, ok := tm.TypeForOID(f.DataTypeOID); ok {
				name = t.Name
			}
			res.Columns = append(res.Columns, queryColumn{Name: f.Name, Type: name})
		}
		size := 0
		for rows.Next() {
			if len(res.Rows) == limit {
				res.Truncated = true
				break
			}
			vals, err := rows.Values()
			if err != nil {
				return err
			}
			row := make([]any, len(vals))
			for i, v := range vals {
				row[i] = queryValue(v)
				if b, err := json.Marshal(row[i]); err == nil {
					size += len(b)
				}
			}
			if size > queryMaxBytes {
				res.Truncated = true
				break
			}
			res.Rows = append(res.Rows, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return errQueryDone
	})
	if errors.Is(err, errQueryDone) {
		err = nil
	}
	res.RowCount = len(res.Rows)
	return res, err
}

// errQueryDone rolls a query's transaction back: nothing it did is kept.
var errQueryDone = errors.New("query done")

// queryValue turns a value pgx read into one JSON can carry.
func queryValue(v any) any {
	switch t := v.(type) {
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x", t[0:4], t[4:6], t[6:8], t[8:10], t[10:16])
	case nil, bool, string, int16, int32, int64, float32, float64, time.Time, map[string]any, []any:
		return t
	}
	if _, err := json.Marshal(v); err != nil {
		return fmt.Sprint(v)
	}
	return v
}

// queryState limits how many queries run at once, and remembers which
// workspaces' roles this server has set up.
type queryState struct {
	size  int // Options.QueryConnections
	once  sync.Once
	slots chan struct{}
	mu    sync.Mutex
	ready map[string]bool // workspace ids
}

// queryConcurrency is how many queries one server runs at once by
// default, each on its own database connection.
const queryConcurrency = 3

// queryConn opens a connection for one query, as the workspace's query
// role. Every workspace has its own role, so the database shows no query
// another workspace is running (pg_stat_activity hides other roles'
// queries), and every query gets a new connection, closed after it, so
// nothing a query sets lasts. The caller closes it and frees the slot.
func (s *Server) queryConn(ctx context.Context, ws string) (*pgx.Conn, error) {
	q := &s.query
	q.once.Do(func() {
		n := q.size
		if n == 0 {
			n = queryConcurrency
		}
		q.slots = make(chan struct{}, max(n, 0))
		for range n {
			q.slots <- struct{}{}
		}
		q.ready = map[string]bool{}
	})
	if cap(q.slots) == 0 {
		return nil, errors.New("query: turned off (OUTFLARE_QUERY_CONNECTIONS=0)")
	}
	select {
	case <-q.slots:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	conn, err := s.openQueryConn(ctx, ws)
	if err != nil {
		q.slots <- struct{}{}
		if ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "query.setup_failed", WorkspaceID: ws, ActorType: "system", Surface: "system"}, "query_unavailable", err)
		}
		return nil, err
	}
	return conn, nil
}

func (s *Server) openQueryConn(ctx context.Context, ws string) (*pgx.Conn, error) {
	if s.box == nil {
		return nil, errors.New("query: no secret key to derive the role's password from")
	}
	var db string
	if err := s.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil {
		return nil, err
	}
	// Roles belong to the whole database server, so the name also says
	// which database it is for.
	sum := sha256.Sum256([]byte(db))
	role := "outflare_q_" + hex.EncodeToString(sum[:4]) + "_" + strings.ReplaceAll(ws, "-", "")
	password := s.box.Sign("query-role", db+"/"+ws)
	s.query.mu.Lock()
	ready := s.query.ready[ws]
	s.query.mu.Unlock()
	if !ready {
		if err := s.setupQueryRole(ctx, db, role, password); err != nil {
			return nil, err
		}
		s.query.mu.Lock()
		s.query.ready[ws] = true
		s.query.mu.Unlock()
	}
	cfg := s.pool.Config().ConnConfig.Copy()
	cfg.User, cfg.Password = role, password
	cfg.RuntimeParams = map[string]string{"search_path": "query", "application_name": "outflare-query"}
	cfg.Tracer = nil
	// Describe every statement afresh: FETCH has the same text for every
	// query, so a cached description would carry another query's columns.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	cfg.StatementCacheCapacity, cfg.DescriptionCacheCapacity = 0, 0
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		// The password changes with the secret key: set the role up again.
		s.query.mu.Lock()
		delete(s.query.ready, ws)
		s.query.mu.Unlock()
		return nil, fmt.Errorf("query: connect as %s: %w", role, err)
	}
	return conn, nil
}

// setupQueryRole makes or updates a workspace's query role: it logs in,
// reads the query views and nothing else.
func (s *Server) setupQueryRole(ctx context.Context, db, role, password string) error {
	ident := pgx.Identifier{role}.Sanitize()
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return err
	}
	verb := "ALTER"
	if !exists {
		verb = "CREATE"
	}
	var stmt string
	if err := s.pool.QueryRow(ctx, `SELECT format('%s ROLE %I WITH LOGIN NOINHERIT NOCREATEDB NOCREATEROLE CONNECTION LIMIT 8 PASSWORD %L', $1::text, $2::text, $3::text)`,
		verb, role, password).Scan(&stmt); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, stmt); err != nil {
		var pgErr *pgconn.PgError
		// Another server made it at the same moment.
		if !(errors.As(err, &pgErr) && (pgErr.Code == "42710" || pgErr.Code == "23505")) {
			return fmt.Errorf("query: %s role: %w", strings.ToLower(verb), err)
		}
	}
	for _, sql := range []string{
		`ALTER ROLE ` + ident + ` SET search_path = query`,
		`ALTER ROLE ` + ident + ` SET default_transaction_read_only = on`,
		`ALTER ROLE ` + ident + ` SET statement_timeout = '20s'`,
		`ALTER ROLE ` + ident + ` SET idle_in_transaction_session_timeout = '30s'`,
		`GRANT CONNECT ON DATABASE ` + pgx.Identifier{db}.Sanitize() + ` TO ` + ident,
		`GRANT USAGE ON SCHEMA query TO ` + ident,
		// Views added since are granted too: a server sets a role up once.
		`GRANT SELECT ON ALL TABLES IN SCHEMA query TO ` + ident,
	} {
		if _, err := s.pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("query: set up role: %w", err)
		}
	}
	return nil
}
