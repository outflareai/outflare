package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Published query endpoints (plans/customer-data-platform, step 6, and
// docs/query-endpoints.md): a saved query, pinned to one version, that
// another system calls with an API token to read its rows, with
// parameters bound as $1, $2, ….
//
// The owner's rules (2026-11-01 and 2026-11-02):
//   - Only owners and admins, signed in, publish or change one.
//   - Only API tokens call one: a token holding query.endpoints and made
//     for the endpoint's brand alone. No secret links, no sessions.
//   - Every call is logged (query_endpoint_calls), with a rate limit per
//     token and a row cap.
//
// What an endpoint reads is fixed when it is published: the publisher's
// read scopes. The caller's token needs only query.endpoints, so a
// reporting tool never holds customers.read.

const (
	queryEndpointMaxParams = 20
	queryEndpointMaxRows   = 10000
	queryEndpointMaxRate   = 600
	queryEndpointsShown    = 200
	// How long the call log is kept (PruneQueryEndpointCalls).
	queryEndpointCallsKept = 90 * 24 * time.Hour
)

var (
	endpointSlugRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	endpointParamRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,40}$`)
	sqlPlaceholder  = regexp.MustCompile(`\$([0-9]+)`)
)

// The parameter types and the Postgres type each is bound as.
var endpointParamOIDs = map[string]uint32{
	"text": 25, "integer": 20, "number": 1700, "boolean": 16, "date": 1082, "timestamp": 1184, "uuid": 2950,
}

func (s *Server) routesQueryEndpoints(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/query-endpoints", s.onBrand(read, s.listQueryEndpoints))
	mux.HandleFunc("POST /v1/brands/{brand}/query-endpoints", s.onBrand(manage, s.createQueryEndpoint))
	mux.HandleFunc("GET /v1/brands/{brand}/query-endpoints/{endpoint}", s.onBrand(read, s.getQueryEndpoint))
	mux.HandleFunc("PATCH /v1/brands/{brand}/query-endpoints/{endpoint}", s.onBrand(manage, s.updateQueryEndpoint))
	mux.HandleFunc("GET /v1/brands/{brand}/query-endpoints/{endpoint}/calls", s.onBrand(read, s.listQueryEndpointCalls))
	mux.HandleFunc("POST /v1/brands/{brand}/query-endpoints/{endpoint}/run", s.onBrand(auth.ScopeQueryEndpoints, s.runQueryEndpoint))
}

// publisher reports whether the caller may publish or change endpoints:
// an owner or admin, signed in.
func publisher(a *access) bool {
	return (a.Role == auth.RoleOwner || a.Role == auth.RoleAdmin) && a.TokenID == "" && a.UserID != ""
}

type endpointParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	// Default is used when a call leaves the parameter out.
	Default any `json:"default"`
}

type queryEndpointView struct {
	ID            string          `json:"id"`
	BrandID       string          `json:"brand_id"`
	SavedQueryID  string          `json:"saved_query_id"`
	Version       int             `json:"version"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Params        []endpointParam `json:"params"`
	Scopes        []string        `json:"scopes"`
	RowCap        int             `json:"row_cap"`
	RatePerMinute int             `json:"rate_per_minute"`
	Status        string          `json:"status"`
	CreatedBy     *string         `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	// Calls in the last 24 hours, and the last call.
	Calls24h   int        `json:"calls_24h"`
	LastCallAt *time.Time `json:"last_call_at"`
}

const queryEndpointColumns = `e.id::text, e.brand_id::text, e.saved_query_id::text, e.version, e.slug, e.name, e.description, e.params, e.scopes,
	e.row_cap, e.rate_per_minute, e.status, e.created_by::text, e.created_at, e.updated_at,
	(SELECT count(*) FROM query_endpoint_calls c WHERE c.endpoint_id = e.id AND c.at > now() - interval '24 hours')::int,
	(SELECT max(c.at) FROM query_endpoint_calls c WHERE c.endpoint_id = e.id)`

func scanQueryEndpoint(row pgx.Row) (queryEndpointView, error) {
	var v queryEndpointView
	err := row.Scan(&v.ID, &v.BrandID, &v.SavedQueryID, &v.Version, &v.Slug, &v.Name, &v.Description, &v.Params, &v.Scopes,
		&v.RowCap, &v.RatePerMinute, &v.Status, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt, &v.Calls24h, &v.LastCallAt)
	if v.Params == nil {
		v.Params = []endpointParam{}
	}
	return v, err
}

// findQueryEndpoint reads one of the brand's endpoints by id or slug.
func findQueryEndpoint(ctx context.Context, q querier, a *access, ref string) (queryEndpointView, error) {
	id := ""
	if validUUID(ref) {
		id = ref
	}
	v, err := scanQueryEndpoint(q.QueryRow(ctx, `SELECT `+queryEndpointColumns+` FROM query_endpoints e
		WHERE e.workspace_id = $1 AND e.brand_id = $2 AND (e.id = $3::uuid OR e.slug = $4)`, a.WorkspaceID, a.Brand, nullIfEmpty(id), strings.ToLower(ref)))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

type queryEndpointInput struct {
	SavedQueryID  *string          `json:"saved_query_id"`
	Version       *int             `json:"version"`
	Slug          *string          `json:"slug"`
	Name          *string          `json:"name"`
	Description   *string          `json:"description"`
	Params        *[]endpointParam `json:"params"`
	RowCap        *int             `json:"row_cap"`
	RatePerMinute *int             `json:"rate_per_minute"`
	Status        *string          `json:"status"`
}

// checkEndpointParams checks the declared parameters and that the SQL uses
// no placeholder beyond them.
func checkEndpointParams(params []endpointParam, sql string) error {
	if len(params) > queryEndpointMaxParams {
		return errInvalid(fmt.Sprintf("an endpoint takes up to %d parameters", queryEndpointMaxParams))
	}
	seen := map[string]bool{}
	for i := range params {
		p := &params[i]
		if !endpointParamRE.MatchString(p.Name) || seen[p.Name] {
			return errInvalid("each parameter has its own name: lowercase letters, digits and _, such as country or since")
		}
		seen[p.Name] = true
		if _, ok := endpointParamOIDs[p.Type]; !ok {
			return errInvalid(fmt.Sprintf("parameter %s: type is text, integer, number, boolean, date, timestamp or uuid", p.Name))
		}
		if p.Default != nil {
			v, err := endpointValue(*p, p.Default)
			if err != nil {
				return errInvalid(fmt.Sprintf("parameter %s: the default %s", p.Name, err))
			}
			p.Default = v
		}
	}
	most := 0
	for _, m := range sqlPlaceholder.FindAllStringSubmatch(sql, -1) {
		if n, _ := strconv.Atoi(m[1]); n > most {
			most = n
		}
	}
	if most > len(params) {
		return errInvalid(fmt.Sprintf("the query uses $%d but the endpoint declares %d parameters: declare one for each $n, in order", most, len(params)))
	}
	return nil
}

// endpointValue checks a parameter's value against its type and returns it
// in its stored form: strings for text, dates, timestamps and ids, numbers
// and booleans as JSON has them.
func endpointValue(p endpointParam, v any) (any, error) {
	bad := func(want string) (any, error) { return nil, fmt.Errorf("must be %s", want) }
	switch p.Type {
	case "text":
		s, ok := v.(string)
		if !ok || len(s) > 2000 {
			return bad("text, up to 2,000 characters")
		}
		return s, nil
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return bad("a whole number")
		}
		return f, nil
	case "number":
		if _, ok := v.(float64); !ok {
			return bad("a number")
		}
		return v, nil
	case "boolean":
		if _, ok := v.(bool); !ok {
			return bad("true or false")
		}
		return v, nil
	case "date":
		s, ok := v.(string)
		if _, err := time.Parse(time.DateOnly, s); !ok || err != nil {
			return bad("a date such as 2026-11-02")
		}
		return s, nil
	case "timestamp":
		s, ok := v.(string)
		if _, err := time.Parse(time.RFC3339, s); !ok || err != nil {
			return bad("a time such as 2026-11-02T09:00:00Z")
		}
		return s, nil
	case "uuid":
		s, ok := v.(string)
		if !ok || !validUUID(s) {
			return bad("an id")
		}
		return strings.ToLower(s), nil
	}
	return bad("a supported value")
}

// endpointArgs turns a call's parameters into bound arguments, in order.
func endpointArgs(params []endpointParam, in map[string]any) ([]queryArg, error) {
	for k := range in {
		if !slices.ContainsFunc(params, func(p endpointParam) bool { return p.Name == k }) {
			return nil, errInvalid(fmt.Sprintf("this endpoint has no parameter %s", k))
		}
	}
	args := make([]queryArg, 0, len(params))
	for _, p := range params {
		v, ok := in[p.Name]
		if !ok || v == nil {
			v = p.Default
		}
		if v == nil {
			if p.Required {
				return nil, errInvalid(fmt.Sprintf("parameter %s is required", p.Name))
			}
			args = append(args, queryArg{OID: endpointParamOIDs[p.Type]})
			continue
		}
		v, err := endpointValue(p, v)
		if err != nil {
			return nil, errInvalid(fmt.Sprintf("parameter %s %s", p.Name, err))
		}
		var text string
		switch x := v.(type) {
		case string:
			text = x
		case bool:
			text = strconv.FormatBool(x)
		case float64:
			text = strconv.FormatFloat(x, 'f', -1, 64)
		}
		args = append(args, queryArg{Value: []byte(text), OID: endpointParamOIDs[p.Type]})
	}
	return args, nil
}

// endpointSQL is the SQL of a saved query's version, or errNotFound.
func endpointSQL(ctx context.Context, q querier, a *access, queryID string, version int) (string, error) {
	var sql string
	err := q.QueryRow(ctx, `SELECT v.sql FROM saved_query_versions v JOIN saved_queries q ON q.id = v.query_id
		WHERE q.workspace_id = $1 AND q.brand_id = $2 AND q.id = $3 AND v.version = $4 AND q.archived_at IS NULL`,
		a.WorkspaceID, a.Brand, queryID, version).Scan(&sql)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errInvalid("saved_query_id and version are a saved query of this brand that isn't archived, and one of its versions")
	}
	return sql, err
}

// readScopes are the caller's scopes that read Query tables: what an
// endpoint they publish may read.
func readScopes(a *access) []string {
	var out []string
	for _, t := range queryTables {
		if a.has(t.Scope) && !slices.Contains(out, t.Scope) {
			out = append(out, t.Scope)
		}
	}
	slices.Sort(out)
	return out
}

func notPublisher(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusForbidden, "admin_only", "only the workspace's owners and admins, signed in, publish or change query endpoints")
}

func (s *Server) createQueryEndpoint(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !publisher(a) {
		notPublisher(w, r)
		return
	}
	var in queryEndpointInput
	if !decode(w, r, &in) {
		return
	}
	if in.SavedQueryID == nil || !validUUID(*in.SavedQueryID) || in.Slug == nil || in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "saved_query_id, slug and name are required")
		return
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		version := 0
		if in.Version != nil {
			version = *in.Version
		} else if err := tx.QueryRow(ctx, `SELECT current_version FROM saved_queries WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`,
			a.WorkspaceID, a.Brand, *in.SavedQueryID).Scan(&version); errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("saved_query_id is not a saved query of this brand")
		} else if err != nil {
			return err
		}
		v := queryEndpointView{SavedQueryID: *in.SavedQueryID, Version: version, Params: []endpointParam{}, RowCap: 1000, RatePerMinute: 60, Status: "active"}
		if err := applyEndpointInput(&v, &in); err != nil {
			return err
		}
		sql, err := endpointSQL(ctx, tx, a, v.SavedQueryID, v.Version)
		if err != nil {
			return err
		}
		if err := checkEndpointParams(v.Params, sql); err != nil {
			return err
		}
		scopes := readScopes(a)
		params, _ := json.Marshal(v.Params)
		err = tx.QueryRow(ctx, `INSERT INTO query_endpoints (workspace_id, brand_id, saved_query_id, version, slug, name, description, params, scopes,
			row_cap, rate_per_minute, status, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13) RETURNING id::text`,
			a.WorkspaceID, a.Brand, v.SavedQueryID, v.Version, v.Slug, v.Name, v.Description, params, scopes,
			v.RowCap, v.RatePerMinute, v.Status, nullIfEmpty(a.ActorID)).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return recordConflict{"conflict", "this brand already has an endpoint at " + v.Slug + ": pick another slug"}
		}
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query_endpoint.published", "query_endpoint", id,
			map[string]any{"brand_id": a.Brand, "saved_query_id": v.SavedQueryID, "version": v.Version, "slug": v.Slug, "scopes": scopes},
			map[string]any{"saved_query_id": v.SavedQueryID, "version": v.Version, "params": len(v.Params), "row_cap": v.RowCap, "rate_per_minute": v.RatePerMinute})
	})
	if s.recordError(w, r, err) {
		return
	}
	v, err := findQueryEndpoint(ctx, s.pool, a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// applyEndpointInput copies what the request sets onto v, checked.
func applyEndpointInput(v *queryEndpointView, in *queryEndpointInput) error {
	if in.Slug != nil {
		if !endpointSlugRE.MatchString(*in.Slug) {
			return errInvalid("slug is lowercase letters, digits and dashes, such as weekly-signups (it is the endpoint's address)")
		}
		v.Slug = *in.Slug
	}
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n == "" || len(n) > 200 {
			return errInvalid("name is 1 to 200 characters")
		}
		v.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		if len(*in.Description) > 2000 {
			return errInvalid("description is up to 2,000 characters")
		}
		v.Description = *in.Description
	}
	if in.Version != nil {
		v.Version = *in.Version
	}
	if in.Params != nil {
		v.Params = *in.Params
		if v.Params == nil {
			v.Params = []endpointParam{}
		}
	}
	if in.RowCap != nil {
		if *in.RowCap < 1 || *in.RowCap > queryEndpointMaxRows {
			return errInvalid(fmt.Sprintf("row_cap is 1 to %d", queryEndpointMaxRows))
		}
		v.RowCap = *in.RowCap
	}
	if in.RatePerMinute != nil {
		if *in.RatePerMinute < 1 || *in.RatePerMinute > queryEndpointMaxRate {
			return errInvalid(fmt.Sprintf("rate_per_minute is 1 to %d", queryEndpointMaxRate))
		}
		v.RatePerMinute = *in.RatePerMinute
	}
	if in.Status != nil {
		if *in.Status != "active" && *in.Status != "disabled" {
			return errInvalid("status is active or disabled")
		}
		v.Status = *in.Status
	}
	return nil
}

func (s *Server) updateQueryEndpoint(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !publisher(a) {
		notPublisher(w, r)
		return
	}
	var in queryEndpointInput
	if !decode(w, r, &in) {
		return
	}
	if in.SavedQueryID != nil || in.Slug != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an endpoint's saved query and slug never change: publish a new endpoint")
		return
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := findQueryEndpoint(ctx, tx, a, r.PathValue("endpoint"))
		if err != nil {
			return err
		}
		id = v.ID
		if _, err := tx.Exec(ctx, `SELECT 1 FROM query_endpoints WHERE id = $1 FOR UPDATE`, v.ID); err != nil {
			return err
		}
		was := v.Status
		if err := applyEndpointInput(&v, &in); err != nil {
			return err
		}
		sql, err := endpointSQL(ctx, tx, a, v.SavedQueryID, v.Version)
		if err != nil {
			return err
		}
		if err := checkEndpointParams(v.Params, sql); err != nil {
			return err
		}
		params, _ := json.Marshal(v.Params)
		// A change by another owner or admin also fixes what it reads to
		// their read scopes now.
		scopes := readScopes(a)
		if _, err := tx.Exec(ctx, `UPDATE query_endpoints SET version = $2, name = $3, description = $4, params = $5, row_cap = $6,
			rate_per_minute = $7, status = $8, scopes = $9, updated_by = $10, updated_at = now() WHERE id = $1`,
			v.ID, v.Version, v.Name, v.Description, params, v.RowCap, v.RatePerMinute, v.Status, scopes, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		action := "query_endpoint.updated"
		if was != v.Status {
			action = "query_endpoint." + map[string]string{"active": "enabled", "disabled": "disabled"}[v.Status]
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "query_endpoint", v.ID,
			map[string]any{"brand_id": a.Brand, "version": v.Version, "status": v.Status, "scopes": scopes},
			map[string]any{"version": v.Version, "status": v.Status, "params": len(v.Params), "row_cap": v.RowCap, "rate_per_minute": v.RatePerMinute})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "query endpoint not found")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	v, err := findQueryEndpoint(ctx, s.pool, a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) listQueryEndpoints(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT `+queryEndpointColumns+` FROM query_endpoints e
		WHERE e.workspace_id = $1 AND e.brand_id = $2 ORDER BY e.status, e.name, e.id LIMIT $3`, a.WorkspaceID, a.Brand, queryEndpointsShown)
	var list []queryEndpointView
	if err == nil {
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (queryEndpointView, error) { return scanQueryEndpoint(row) })
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []queryEndpointView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getQueryEndpoint(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := findQueryEndpoint(r.Context(), s.pool, a, r.PathValue("endpoint"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "query endpoint not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type endpointCallView struct {
	ID         int64          `json:"id"`
	Version    int            `json:"version"`
	TokenID    *string        `json:"token_id"`
	Outcome    string         `json:"outcome"`
	ErrorCode  *string        `json:"error_code"`
	Rows       int            `json:"rows"`
	Truncated  bool           `json:"truncated"`
	DurationMS int            `json:"duration_ms"`
	Params     map[string]any `json:"params"`
	RequestID  *string        `json:"request_id"`
	At         time.Time      `json:"at"`
}

func (s *Server) listQueryEndpointCalls(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	var before int64 = 1<<63 - 1
	if v, ok := pageCursor(w, r, pageBefore); !ok {
		return
	} else if v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil {
			invalidCursor(w, r)
			return
		}
	}
	e, err := findQueryEndpoint(ctx, s.pool, a, r.PathValue("endpoint"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "query endpoint not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT id, version, token_id::text, outcome, error_code, rows, truncated, duration_ms, params, request_id, at
		FROM query_endpoint_calls WHERE endpoint_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, e.ID, before, limit)
	var list []endpointCallView
	if err == nil {
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[endpointCallView])
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []endpointCallView{}
	}
	var next *int64
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

type endpointRunResult struct {
	queryResult
	EndpointID string `json:"endpoint_id"`
	Version    int    `json:"version"`
}

// runQueryEndpoint runs a published endpoint for an API token, and logs
// the call whatever happens.
func (s *Server) runQueryEndpoint(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if a.TokenID == "" {
		writeError(w, r, http.StatusForbidden, "token_required", "query endpoints are called with an API token holding query.endpoints, made for this brand only; preview the query in Query Studio instead")
		return
	}
	if a.TokenBrandID == nil || *a.TokenBrandID != a.Brand {
		writeError(w, r, http.StatusForbidden, "brand_token_required", "make the token for this brand only: an endpoint answers only tokens limited to its brand")
		return
	}
	var in struct {
		Params map[string]any `json:"params"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	e, err := findQueryEndpoint(ctx, s.pool, a, r.PathValue("endpoint"))
	if errors.Is(err, errNotFound) || (err == nil && e.Status != "active") {
		writeError(w, r, http.StatusNotFound, "not_found", "query endpoint not found, or turned off")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	start := time.Now()
	logCall := func(outcome, code string, res queryResult) {
		params, _ := json.Marshal(in.Params)
		if in.Params == nil {
			params = []byte("{}")
		}
		metrics.QueryEndpointCalled(outcome, code, time.Since(start), res.RowCount)
		if _, err := s.pool.Exec(context.WithoutCancel(ctx), `INSERT INTO query_endpoint_calls (workspace_id, brand_id, endpoint_id, version, token_id, actor_id,
			outcome, error_code, rows, truncated, duration_ms, params, request_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			a.WorkspaceID, a.Brand, e.ID, e.Version, a.TokenID, nullIfEmpty(a.ActorID), outcome, nullIfEmpty(code), res.RowCount, res.Truncated,
			time.Since(start).Milliseconds(), params, nullIfEmpty(RequestID(ctx))); err != nil {
			s.log.Error("query endpoint: call not logged", "err", err, "request_id", RequestID(ctx))
		}
	}
	var recent int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM query_endpoint_calls WHERE endpoint_id = $1 AND token_id = $2 AND at > now() - interval '1 minute'`,
		e.ID, a.TokenID).Scan(&recent); err != nil {
		s.internal(w, r, err)
		return
	}
	if recent >= e.RatePerMinute {
		logCall("failure", "rate_limited", queryResult{})
		w.Header().Set("Retry-After", "60")
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("this endpoint answers each token %d times a minute: try again in a minute", e.RatePerMinute))
		return
	}
	args, err := endpointArgs(e.Params, in.Params)
	var inv errInvalid
	if errors.As(err, &inv) {
		logCall("failure", "invalid_request", queryResult{})
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	sql, err := endpointSQL(ctx, s.pool, a, e.SavedQueryID, e.Version)
	if errors.As(err, &inv) {
		logCall("error", "endpoint_broken", queryResult{})
		writeError(w, r, http.StatusConflict, "endpoint_broken", "the saved query behind this endpoint was archived: ask an admin to publish it again")
		return
	}
	if err != nil {
		logCall("error", "internal", queryResult{})
		s.internal(w, r, err)
		return
	}
	// The query reads what the publisher could, inside this brand.
	run := &access{principal: a.principal, WorkspaceID: a.WorkspaceID, Brand: a.Brand, Brands: []string{a.Brand}, Scopes: e.Scopes}
	res, used, err := s.execQuery(ctx, run, sql, e.RowCap, queryMaxBytes, args...)
	if err != nil {
		code, outcome := "internal", "error"
		var qe *queryError
		if errors.As(err, &qe) {
			code = qe.code
			if qe.status < 500 && qe.code != "query_unavailable" {
				outcome = "failure"
			}
		}
		logCall(outcome, code, res)
		s.queryFailed(w, r, err)
		return
	}
	logCall("success", "", res)
	s.trackQueryRan(r, a, used, res, map[string]any{"endpoint_id": e.ID, "version": e.Version, "saved_query_id": e.SavedQueryID})
	writeJSON(w, http.StatusOK, endpointRunResult{queryResult: res, EndpointID: e.ID, Version: e.Version})
}

// PruneQueryEndpointCalls deletes the call log of query endpoints after 90
// days.
func (s *Server) PruneQueryEndpointCalls(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM query_endpoint_calls WHERE at < now() - make_interval(secs => $1)`, queryEndpointCallsKept.Seconds())
	return tag.RowsAffected(), err
}
