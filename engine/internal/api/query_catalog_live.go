package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// The live half of the data catalog (query_catalog.go has the written
// half): how many rows each query table holds for the brand, the dates
// they cover, how often each column is filled and the values it takes.
// Every number is read with the caller's own query access, through the
// same role, brand and scopes as run_query, so the catalog never shows
// more than a query could. Answers are kept for a few minutes, since an
// agent often reads the catalog several times in one task. Counting every
// table of a large brand takes seconds, so numbers=false answers with the
// written half at once (and whatever numbers are already kept): the app
// shows the list first and fills the numbers in after.

func (s *Server) routesDataCatalog(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/data/catalog", s.onBrand("", s.getDataCatalog))
	mux.HandleFunc("GET /v1/brands/{brand}/data/catalog/{table}", s.onBrand("", s.getDataCatalogTable))
}

const (
	dataCatalogKeep       = 5 * time.Minute
	dataCatalogSampleRows = 100000 // fill rates and values are read from up to this many rows
	dataCatalogValues     = 8      // common values listed per column
)

type dataCatalogTableView struct {
	Name        string               `json:"name"`
	Area        string               `json:"area"`
	Description string               `json:"description"`
	Source      string               `json:"source"`
	Scope       string               `json:"scope"`
	Available   bool                 `json:"available"`
	TimeColumn  string               `json:"time_column,omitempty"`
	Rows        *int64               `json:"rows"`
	FirstAt     *string              `json:"first_at"`
	LastAt      *string              `json:"last_at"`
	Columns     int                  `json:"columns"`
	Examples    []dataCatalogExample `json:"examples"`
}

type dataCatalogColumnView struct {
	dataCatalogColumn
	Filled *float64           `json:"filled"`
	Values []dataCatalogValue `json:"values,omitempty"`
}

type dataCatalogValue struct {
	Value any   `json:"value"`
	Rows  int64 `json:"rows"`
}

// dataTable is a table as the catalog sees it: the query tables and the
// brand's record types alike.
type dataTable struct {
	queryTable
	cols []queryColumn
	kind string // "", or the record type a type table is for
}

// dataTables are every table the caller's brand query has, in area order.
func (s *Server) dataTables(ctx context.Context, a *access) ([]dataTable, error) {
	cols, err := s.queryColumns(ctx)
	if err != nil {
		return nil, err
	}
	types, err := s.queryTypeTables(ctx, a)
	if err != nil {
		return nil, err
	}
	var out []dataTable
	for _, t := range queryTables {
		if cols[t.Name] == nil {
			continue // dropped by a newer version, after a rollback
		}
		out = append(out, dataTable{queryTable: t, cols: cols[t.Name]})
	}
	for _, t := range types {
		out = append(out, dataTable{queryTable: t.queryTable, cols: t.Columns, kind: t.Type})
	}
	slices.SortStableFunc(out, func(x, y dataTable) int {
		return slices.Index(dataCatalogAreas, x.area()) - slices.Index(dataCatalogAreas, y.area())
	})
	return out, nil
}

func (e dataTable) area() string {
	if e.kind != "" {
		return "customers"
	}
	return queryCatalog[e.Name].Area
}

func (e dataTable) source() string {
	if e.kind != "" {
		return "The brand's own record type " + e.kind + ": one row per record, one column per field, from the CRM."
	}
	return queryCatalog[e.Name].Source
}

func (e dataTable) describe(col queryColumn) dataCatalogColumn {
	if e.kind == "" {
		return describeDataColumn(e.Name, col)
	}
	c := describeDataColumn("records", col)
	if c.Description == "" {
		c.Description = "The record's " + strings.ReplaceAll(col.Name, "_", " ") + " field."
	}
	return c
}

func (s *Server) getDataCatalog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	entries, err := s.dataTables(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]dataCatalogTableView, 0, len(entries))
	var readable []dataTable
	for _, e := range entries {
		v := dataCatalogTableView{
			Name: e.Name, Area: e.area(), Description: e.Description, Source: e.source(), Scope: e.Scope,
			Available: queryReadable(a, e.queryTable), TimeColumn: dataCatalogTime(e.Name, e.cols), Columns: len(e.cols),
			Examples: queryCatalog[e.Name].Examples,
		}
		if v.Examples == nil {
			v.Examples = []dataCatalogExample{}
		}
		if v.Available {
			readable = append(readable, e)
		}
		out = append(out, v)
	}
	var counts map[string]dataCatalogCount
	var at time.Time
	var note string
	if r.URL.Query().Get("numbers") == "false" {
		counts, at = map[string]dataCatalogCount{}, time.Now().UTC()
		if v, ok := dataCatalogCache.get(dataCatalogKey(a, readable, "")); ok {
			c := v.(dataCatalogCountsAt)
			counts, at, note = c.counts, c.at, c.note
		}
	} else {
		counts, at, note = s.dataCatalogCounts(r.Context(), a, readable, r.URL.Query().Get("fresh") == "true")
	}
	for i := range out {
		if c, ok := counts[out[i].Name]; ok {
			out[i].Rows, out[i].FirstAt, out[i].LastAt = c.rows, c.first, c.last
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brand_id": a.Brand, "areas": dataCatalogAreas, "data": out, "computed_at": at, "note": nullIfEmpty(note),
		"how_to_use": "Read a table's columns, what they mean and their values with GET .../data/catalog/{table} " +
			"(MCP: describe_brand_data with table), then query it with POST .../query (MCP: run_query). " +
			"Amounts ending _minor or _cents are minor units of the row's currency; times are UTC.",
	})
}

type dataCatalogCount struct {
	rows        *int64
	first, last *string
}

// dataCatalogCounts reads each readable table's rows and dates in one query.
func (s *Server) dataCatalogCounts(ctx context.Context, a *access, tables []dataTable, fresh bool) (map[string]dataCatalogCount, time.Time, string) {
	key := dataCatalogKey(a, tables, "")
	if !fresh {
		if v, ok := dataCatalogCache.get(key); ok {
			c := v.(dataCatalogCountsAt)
			return c.counts, c.at, c.note
		}
	}
	counts := map[string]dataCatalogCount{}
	if len(tables) == 0 {
		return counts, time.Now().UTC(), ""
	}
	var parts []string
	for _, t := range tables {
		tc := dataCatalogTime(t.Name, t.cols)
		lo, hi := "NULL::text", "NULL::text"
		if tc != "" {
			lo, hi = fmt.Sprintf("min(%s)::text", tc), fmt.Sprintf("max(%s)::text", tc)
		}
		parts = append(parts, fmt.Sprintf("SELECT '%s' AS t, count(*)::bigint AS n, %s AS lo, %s AS hi FROM %s", t.Name, lo, hi, t.Name))
	}
	// One statement per batch of tables that fits in a query, run one after
	// another, so the catalog never holds more than one query slot.
	var rows [][]any
	var err error
	for len(parts) > 0 && err == nil {
		n, size := 0, 0
		for n < len(parts) && (n == 0 || size+len(parts[n])+11 <= queryMaxSQL) {
			size += len(parts[n]) + 11
			n++
		}
		var res queryResult
		res, _, err = s.execQuery(ctx, a, strings.Join(parts[:n], "\nUNION ALL\n"), n, queryMaxBytes)
		rows = append(rows, res.Rows...)
		parts = parts[n:]
	}
	at := time.Now().UTC()
	note := ""
	if err != nil {
		var qe *queryError
		if errors.As(err, &qe) {
			s.log.Info("data catalog: counts refused", "code", qe.code, "message", qe.message, "request_id", RequestID(ctx))
		} else {
			s.log.Warn("data catalog: counts failed", "err", err, "request_id", RequestID(ctx))
		}
		note = "Counting rows took too long or failed, so rows and dates are missing for some tables: read one table with GET .../data/catalog/{table}."
	}
	for _, row := range rows {
		name, _ := row[0].(string)
		c := dataCatalogCount{}
		if n, ok := row[1].(int64); ok {
			c.rows = &n
		}
		if v, ok := row[2].(string); ok {
			c.first = &v
		}
		if v, ok := row[3].(string); ok {
			c.last = &v
		}
		counts[name] = c
	}
	if err == nil {
		dataCatalogCache.put(key, dataCatalogCountsAt{counts, at, note})
	}
	return counts, at, note
}

type dataCatalogCountsAt struct {
	counts map[string]dataCatalogCount
	at     time.Time
	note   string
}

func (s *Server) getDataCatalogTable(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	entries, err := s.dataTables(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	name := r.PathValue("table")
	i := slices.IndexFunc(entries, func(e dataTable) bool { return e.Name == name })
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "no query table "+name+": GET .../data/catalog lists them")
		return
	}
	e := entries[i]
	view := dataCatalogTableView{
		Name: e.Name, Area: e.area(), Description: e.Description, Source: e.source(), Scope: e.Scope,
		Available: queryReadable(a, e.queryTable), TimeColumn: dataCatalogTime(e.Name, e.cols), Columns: len(e.cols),
		Examples: queryCatalog[e.Name].Examples,
	}
	if view.Examples == nil {
		view.Examples = []dataCatalogExample{}
	}
	cols := make([]dataCatalogColumnView, 0, len(e.cols))
	for _, c := range e.cols {
		cols = append(cols, dataCatalogColumnView{dataCatalogColumn: e.describe(c)})
	}
	body := map[string]any{"table": view, "columns": cols, "computed_at": time.Now().UTC(), "note": nil}
	if !view.Available {
		body["note"] = "You can't read this table: it needs " + e.Scope + ". " + s.missingScope(a, e.Scope)
		writeJSON(w, http.StatusOK, body)
		return
	}
	if r.URL.Query().Get("numbers") == "false" {
		if _, ok := dataCatalogCache.get(dataCatalogKey(a, entries, e.Name)); !ok {
			writeJSON(w, http.StatusOK, body)
			return
		}
	}
	st, at, note := s.dataCatalogTableStats(r.Context(), a, entries, e, r.URL.Query().Get("fresh") == "true")
	body["computed_at"], body["note"] = at, nullIfEmpty(note)
	view.Rows, view.FirstAt, view.LastAt = st.rows, st.first, st.last
	body["table"] = view
	for i := range cols {
		if n, ok := st.filled[cols[i].Name]; ok && st.sampled > 0 {
			f := float64(n) / float64(st.sampled)
			cols[i].Filled = &f
		} else if st.sampled > 0 {
			zero := 0.0
			cols[i].Filled = &zero
		}
		cols[i].Values = st.values[cols[i].Name]
	}
	writeJSON(w, http.StatusOK, body)
}

type dataCatalogStats struct {
	dataCatalogCount
	sampled int64
	filled  map[string]int64
	values  map[string][]dataCatalogValue
}

// dataCatalogTableStats reads one table's rows, dates, fill rates and common
// values in one query. The SQL names no column: fill rates and values come
// from each row as JSON, so a column that shares its name with a table the
// caller can't read doesn't make the query refuse.
func (s *Server) dataCatalogTableStats(ctx context.Context, a *access, entries []dataTable, e dataTable, fresh bool) (dataCatalogStats, time.Time, string) {
	key := dataCatalogKey(a, entries, e.Name)
	if !fresh {
		if v, ok := dataCatalogCache.get(key); ok {
			c := v.(dataCatalogStatsAt)
			return c.stats, c.at, c.note
		}
	}
	tc := dataCatalogTime(e.Name, e.cols)
	lo, hi := "NULL::text", "NULL::text"
	if tc != "" {
		lo, hi = fmt.Sprintf("(SELECT min(%s)::text FROM %s)", tc, e.Name), fmt.Sprintf("(SELECT max(%s)::text FROM %s)", tc, e.Name)
	}
	// Columns whose values are listed, as SQL strings; a name that is also
	// a table's is left out, since the query's check reads it as one.
	var listed []string
	for _, c := range e.cols {
		if dataCatalogLists(e.Name, c) && !slices.ContainsFunc(entries, func(x dataTable) bool { return x.Name == c.Name }) {
			listed = append(listed, "'"+c.Name+"'")
		}
	}
	vals := "'{}'::jsonb"
	if len(listed) > 0 {
		vals = fmt.Sprintf(`(SELECT coalesce(jsonb_object_agg(k, vs), '{}') FROM (
			SELECT k, jsonb_agg(jsonb_build_object('value', v, 'rows', n) ORDER BY n DESC) AS vs FROM (
				SELECT e.key AS k, e.value AS v, count(*) AS n, row_number() OVER (PARTITION BY e.key ORDER BY count(*) DESC) AS rn
				FROM sample x, jsonb_each(to_jsonb(x)) e WHERE e.key IN (%s) AND e.value <> 'null'::jsonb GROUP BY 1, 2) g
			WHERE rn <= %d GROUP BY k) h)`, strings.Join(listed, ", "), dataCatalogValues)
	}
	sql := fmt.Sprintf(`WITH sample AS (SELECT * FROM %[1]s LIMIT %[2]d)
SELECT (SELECT count(*) FROM %[1]s)::bigint, %[3]s, %[4]s, (SELECT count(*) FROM sample)::bigint,
	(SELECT coalesce(jsonb_object_agg(k, n), '{}') FROM (SELECT e.key AS k, count(*) AS n FROM sample x, jsonb_each(to_jsonb(x)) e
		WHERE e.value <> 'null'::jsonb GROUP BY 1) f),
	%[5]s`, e.Name, dataCatalogSampleRows, lo, hi, vals)
	res, _, err := s.execQuery(ctx, a, sql, 1, queryMaxBytes)
	at := time.Now().UTC()
	st := dataCatalogStats{filled: map[string]int64{}, values: map[string][]dataCatalogValue{}}
	if err != nil || len(res.Rows) != 1 {
		var qe *queryError
		if err != nil && !errors.As(err, &qe) {
			s.log.Warn("data catalog: table stats failed", "err", err, "table", e.Name, "request_id", RequestID(ctx))
		}
		return st, at, "Reading this table's numbers took too long or failed; its columns are described, without counts."
	}
	row := res.Rows[0]
	if n, ok := row[0].(int64); ok {
		st.rows = &n
	}
	if v, ok := row[1].(string); ok {
		st.first = &v
	}
	if v, ok := row[2].(string); ok {
		st.last = &v
	}
	st.sampled, _ = row[3].(int64)
	if m, ok := row[4].(map[string]any); ok {
		for k, v := range m {
			if f, ok := v.(float64); ok {
				st.filled[k] = int64(f)
			}
		}
	}
	if m, ok := row[5].(map[string]any); ok {
		for k, v := range m {
			list, _ := v.([]any)
			for _, item := range list {
				o, _ := item.(map[string]any)
				n, _ := o["rows"].(float64)
				st.values[k] = append(st.values[k], dataCatalogValue{Value: o["value"], Rows: int64(n)})
			}
		}
	}
	note := ""
	if st.rows != nil && *st.rows > st.sampled {
		note = fmt.Sprintf("Filled shares and common values are read from %d of the table's rows.", st.sampled)
	}
	dataCatalogCache.put(key, dataCatalogStatsAt{st, at, note})
	return st, at, note
}

type dataCatalogStatsAt struct {
	stats dataCatalogStats
	at    time.Time
	note  string
}

// dataCatalogKey names one caller's view of a brand's catalog: the workspace,
// the brand and which tables it may read, so two callers who may read
// different tables never share an answer.
func dataCatalogKey(a *access, tables []dataTable, table string) string {
	var b strings.Builder
	b.WriteString(a.WorkspaceID + "/" + a.Brand + "/" + table + "/")
	for _, t := range tables {
		if queryReadable(a, t.queryTable) {
			b.WriteString(t.Name + ",")
		}
	}
	return b.String()
}

// dataCatalogCache keeps catalog answers for dataCatalogKeep.
var dataCatalogCache = &dataCatalogStore{items: map[string]dataCatalogItem{}}

type dataCatalogStore struct {
	mu    sync.Mutex
	items map[string]dataCatalogItem
}

type dataCatalogItem struct {
	v  any
	at time.Time
}

func (c *dataCatalogStore) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || time.Since(it.at) > dataCatalogKeep {
		return nil, false
	}
	return it.v, true
}

func (c *dataCatalogStore) put(key string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Drop what has gone stale, so the cache never grows past the callers
	// of the last few minutes.
	for k, it := range c.items {
		if time.Since(it.at) > dataCatalogKeep {
			delete(c.items, k)
		}
	}
	c.items[key] = dataCatalogItem{v, time.Now()}
}
