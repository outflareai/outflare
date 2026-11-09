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
	// Query for one brand: every customer table holds that brand's rows
	// only, and each of its record types is a table of its own
	// (query_brand.go).
	mux.HandleFunc("GET /v1/brands/{brand}/query/tables", s.onBrand("", s.listQueryTables))
	mux.HandleFunc("POST /v1/brands/{brand}/query", s.onBrand("", s.runQuery))
}

// readsOnly reports whether a POST only reads, so an agent kept to
// missions may send it outside one: a query's SQL is too long for a URL,
// and a question to the knowledge base can be too.
func readsOnly(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if b := r.PathValue("brand"); b != "" {
		// Running a saved query only reads too. A CSV download is not
		// here: personal data leaving the app in bulk waits for a mission.
		q := r.PathValue("saved_query")
		return r.URL.Path == "/v1/brands/"+b+"/query" || (q != "" && r.URL.Path == "/v1/brands/"+b+"/queries/"+q+"/run")
	}
	base := "/v1/workspaces/" + r.PathValue("ws")
	// Asking the knowledge only reads it (it keeps the question, for the gaps).
	return r.URL.Path == base+"/query" || r.URL.Path == base+"/knowledge/ask"
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
	{"leads", auth.ScopeLeadsRead, "Leads for outreach; each belongs to one brand (brand_id)."},
	{"sequences", auth.ScopeSequencesRead, "Outreach sequences."},
	{"enrollments", auth.ScopeSequencesRead, "Leads in sequences and where each is."},
	{"messages", auth.ScopeSequencesRead, "Outreach emails sent or queued, without bodies."},
	{"replies", auth.ScopeInboxRead, "Replies and other mail received, without bodies; category is the AI label."},
	{"deals", auth.ScopeOutcomesRead, "Deals; amount_cents in the deal's currency, state open, won or lost."},
	{"meetings", auth.ScopeOutcomesRead, "Meetings booked."},
	{"spend", auth.ScopeOutcomesRead, "Money spent, by channel, category and provider, in minor units."},
	{"rules", auth.ScopeCustomersRead, "Rules of the rules engine: each turns what happens to records into an event of its own."},
	{"rule_firings", auth.ScopeCustomersRead, "Each time a rule fired: for which record, in which episode, and the event it emitted."},
	{"transactions", auth.ScopeCustomersRead, "Payments, refunds, chargebacks and credits; amount_minor in currency, brand_amount_minor in the brand's currency (null until the day's rate is known)."},
	{"smart_list_changes", auth.ScopeCustomersRead, "Who joined (enter) and left (leave) each smart or static list, and when."},
	{"metrics", auth.ScopeCustomersRead, "The brand's live metrics: what each counts (aggregate, events, usage, property) over which window."},
	{"metric_values", auth.ScopeCustomersRead, "Each company's and person's current value of each metric; product_id is null for the overall value."},
	{"bulk_edits", auth.ScopeCustomersRead, "Bulk edits owners ran: where the records came from, what changed, how many, and the bulk edit that undid one (undone_by)."},
	{"products", auth.ScopeCustomersRead, "The brand's products: key never changes; churn_mode says whether cancelling or no usage churns a customer."},
	{"lifecycle_states", auth.ScopeCustomersRead, "Where each company (level company: billing) and person (level person: usage) stands with each product."},
	{"lifecycle_moves", auth.ScopeCustomersRead, "Each move between lifecycle stages, with the event that caused it and the event it emitted; level overall is a company's overall status."},
	{"lifecycle_overall", auth.ScopeCustomersRead, "Each company's overall status across products: its best stage, flags such as partly_churned, and how many products it pays for."},
	{"journeys", auth.ScopeCustomersRead, "Journeys: lifecycle flows people go through; steps is how many steps the live version has."},
	{"journey_runs", auth.ScopeCustomersRead, "People's ways through journeys: where each is (step), and how it ended."},
	{"emails", auth.ScopeEmailsRead, "Lifecycle and transactional emails, without bodies or addresses."},
	{"email_events", auth.ScopeEmailsRead, "What happened to each email: delivered, opened, clicked, bounced."},
	{"broadcasts", auth.ScopeEmailsRead, "Email broadcasts and how many they reached."},
	{"visits", auth.ScopeAnalyticsRead, "Website visits from the Outflare site snippet, with UTM tags."},
	{"social_posts", auth.ScopeSocialRead, "Posts on the workspace's own social accounts, with their numbers."},
	{"reviews", auth.ScopeSocialRead, "Reviews on the brands' review pages (Trustpilot, G2, Capterra, SourceForge, Google); rating is 1 to 5."},
	{"review_page_days", auth.ScopeSocialRead, "Each review page's numbers by day: the platform's score and count, new reviews, stars and Outflare's review score."},
	{"backlinks", auth.ScopeSEORead, "Links from other sites to the brands' websites: status live or lost, the linking domain's strength (source_rating, 0 to 100), traffic and spam score, and when each was found, lost or regained."},
	{"link_prospects", auth.ScopeSEORead, "Pages that could link to a website (link building): how each was found (method), its score, and where its pitch stands (stage: prospect, excluded, pitched, replied, agreed, live, declined, removed) with when."},
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
	return a.ViewID == "" || t.Name != "messages"
}

func (s *Server) listQueryTables(w http.ResponseWriter, r *http.Request) {
	a, ok := s.queryAccess(w, r)
	if !ok {
		return
	}
	types, err := s.queryTypeTables(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
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
		// A view a newer version dropped (after a rollback) is left out.
		if cols[t.Name] == nil {
			continue
		}
		out = append(out, queryTableView{Name: t.Name, Description: t.Description, Scope: t.Scope, Available: queryReadable(a, t), Columns: cols[t.Name]})
	}
	for _, t := range types {
		out = append(out, queryTableView{Name: t.Name, Description: t.Description, Scope: t.Scope, Available: queryReadable(a, t.queryTable), Columns: t.Columns})
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
	a, ok := s.queryAccess(w, r)
	if !ok {
		return
	}
	var req queryInput
	if !decode(w, r, &req) {
		return
	}
	limit, ok := queryLimit(w, r, req.Limit, queryDefaultRows, queryMaxRows)
	if !ok {
		return
	}
	res, used, err := s.execQuery(r.Context(), a, req.SQL, limit, queryMaxBytes)
	if s.queryFailed(w, r, err) {
		return
	}
	s.trackQueryRan(r, a, used, res, nil)
	writeJSON(w, http.StatusOK, res)
}

// queryLimit reads a request's row limit: def when it sends none.
func queryLimit(w http.ResponseWriter, r *http.Request, n *int, def, most int) (int, bool) {
	if n == nil {
		return def, true
	}
	if *n < 1 || *n > most {
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit is 1 to %d rows", most))
		return 0, false
	}
	return *n, true
}

// queryError is a query refused or failed for a reason the caller can fix,
// answered with its status and code.
type queryError struct {
	status  int
	code    string
	message string
}

func (e *queryError) Error() string { return e.code + ": " + e.message }

// queryFailed answers err, if there is one: a queryError as itself, and
// anything else as an internal error.
func (s *Server) queryFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var qe *queryError
	if errors.As(err, &qe) {
		writeError(w, r, qe.status, qe.code, qe.message)
		return true
	}
	s.internal(w, r, err)
	return true
}

// execQuery runs one query for a: the ad hoc query, a saved one and a CSV
// export all run through it, with the same checks, role and limits. It
// returns the result and the tables the SQL names; a *queryError is the
// caller's to fix.
func (s *Server) execQuery(ctx context.Context, a *access, sql string, limit, maxBytes int, args ...queryArg) (queryResult, []string, error) {
	sql = strings.TrimSpace(sql)
	for strings.HasSuffix(sql, ";") {
		sql = strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	}
	switch {
	case sql == "":
		return queryResult{}, nil, &queryError{http.StatusBadRequest, "invalid_request", "sql is one SELECT over the tables GET .../query/tables lists"}
	case len(sql) > queryMaxSQL:
		return queryResult{}, nil, &queryError{http.StatusBadRequest, "invalid_request", fmt.Sprintf("sql is at most %d characters", queryMaxSQL)}
	}
	if m := queryRefused.FindString(sql); m != "" {
		return queryResult{}, nil, &queryError{http.StatusBadRequest, "query_invalid", "queries can't call " + strings.TrimRight(m, `'"`) + ": read the tables with SELECT"}
	}
	types, err := s.queryTypeTables(ctx, a)
	if err != nil {
		return queryResult{}, nil, err
	}
	tables := slices.Clone(queryTables)
	for _, t := range types {
		tables = append(tables, t.queryTable)
	}
	// A clearer answer than no rows: name the table and the scope it needs.
	lower := strings.ToLower(sql)
	var scopes, used []string
	for _, t := range tables {
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
				msg = t.Name + " is not readable here"
			}
			return queryResult{}, nil, &queryError{http.StatusForbidden, "forbidden", "this query reads " + t.Name + ": " + msg}
		}
	}
	if len(scopes) == 0 {
		return queryResult{}, nil, &queryError{http.StatusForbidden, "forbidden", "you can't read any of the query tables: each needs its read scope, such as customers.read for people"}
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	start := time.Now()
	conn, err := s.queryConn(ctx, a.WorkspaceID)
	if err != nil && ctx.Err() == nil {
		metrics.QueryRan("unavailable", time.Since(start), 0)
		s.log.Error("query: no query role", "err", err, "request_id", RequestID(ctx))
		return queryResult{}, nil, &queryError{http.StatusServiceUnavailable, "query_unavailable", "Query needs its own read-only database role, and Outflare couldn't set it up. " +
			"The server's log says why; the database user needs to be able to create roles (CREATEROLE)"}
	}
	var res queryResult
	if err == nil {
		res, err = s.queryRun(ctx, conn, a, scopes, types, sql, limit, maxBytes, args)
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
		return res, nil, &queryError{http.StatusBadRequest, "query_timeout", fmt.Sprintf("the query ran for over %s: filter it more, or count instead of listing", queryTimeout)}
	case errors.As(err, &pgErr):
		msg := pgErr.Message
		if pgErr.Code == "42501" {
			msg += ": queries read only the tables GET .../query/tables lists, such as people or deals"
		}
		if pgErr.Hint != "" {
			msg += " (" + pgErr.Hint + ")"
		}
		return res, nil, &queryError{http.StatusBadRequest, "query_invalid", msg}
	case err != nil:
		return res, nil, err
	}
	return res, used, nil
}

// queryEventProps are a query's product event properties: the tables it
// read (type tables are named after the brand's own types, so counted, not
// named), how many rows and how long. Never the SQL.
func queryEventProps(r *http.Request, used []string, res queryResult) map[string]any {
	named, typed := []string{}, 0
	for _, u := range used {
		if slices.ContainsFunc(queryTables, func(t queryTable) bool { return t.Name == u }) {
			named = append(named, u)
		} else {
			typed++
		}
	}
	return map[string]any{"tables": named, "type_tables": typed, "brand_route": accessFrom(r.Context()).Brand != "", "rows": res.RowCount,
		"truncated": res.Truncated, "duration_ms": res.DurationMS, "columns": len(res.Columns)}
}

// trackQueryRan records query.ran for a query that ran, with extra
// properties (a saved query's id and version).
func (s *Server) trackQueryRan(r *http.Request, a *access, used []string, res queryResult, extra map[string]any) {
	ctx := r.Context()
	props := queryEventProps(r, used, res)
	for k, v := range extra {
		props[k] = v
	}
	ev := track.Event{Name: "query.ran", UserID: a.UserID, ActorID: a.ActorID, ActorType: a.ActorType, WorkspaceID: a.WorkspaceID, RequestID: RequestID(ctx), Props: props}
	if info := reqInfoFrom(ctx); info != nil {
		ev.Surface = surface(r, info)
	}
	track.Emit(ctx, ev)
}

// queryArg is one bound parameter: its text form (nil for null) and its
// Postgres type.
type queryArg struct {
	Value []byte
	OID   uint32
}

// queryRun runs sql on a query connection with the caller's scope.
func (s *Server) queryRun(ctx context.Context, conn *pgx.Conn, a *access, scopes []string, types []queryTypeTable, sql string, limit, maxBytes int, args []queryArg) (queryResult, error) {
	res := queryResult{Columns: []queryColumn{}, Rows: [][]any{}}
	pid := int(conn.PgConn().PID())
	// A query for one brand: brand keeps every customer view to that
	// brand's own rows (migration 0342), and brands keeps the views of
	// areas not yet moved to brands to it and the shared rows, as for a
	// brand-limited caller.
	brands := a.seenBrands()
	if _, err := s.pool.Exec(ctx, `INSERT INTO query_scopes (pid, workspace_id, brands, scopes, brand) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (pid) DO UPDATE SET workspace_id = EXCLUDED.workspace_id, brands = EXCLUDED.brands, scopes = EXCLUDED.scopes,
			brand = EXCLUDED.brand, created_at = now()`,
		pid, a.WorkspaceID, brands, scopes, nullIfEmpty(a.Brand)); err != nil {
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
	if err := s.makeTypeViews(ctx, conn, a, types); err != nil {
		return res, err
	}
	err := pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		// The first statement fixes the snapshot, so the query can no
		// longer make the transaction read-write.
		if _, err := tx.Exec(ctx, `SELECT 1`); err != nil {
			return err
		}
		// The extended protocol, always: it refuses a second statement,
		// where pgx would send a query without arguments as simple text.
		// A published endpoint's parameters are bound ($1, $2, …), never
		// written into the SQL.
		var values [][]byte
		var oids []uint32
		for _, arg := range args {
			values, oids = append(values, arg.Value), append(oids, arg.OID)
		}
		if res := tx.Conn().PgConn().ExecParams(ctx, "DECLARE outflare_query NO SCROLL CURSOR FOR "+sql+"\n", values, oids, nil, nil).Read(); res.Err != nil {
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
			if size > maxBytes {
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
