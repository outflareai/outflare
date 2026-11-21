package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The query builder (docs/query.md, "Build it without SQL"): a list or an
// answer said as a table to start from, the columns to show, the filters
// that pick rows, the order and how many, which the engine writes as SQL.
// People build one by clicking, or by describing it in English
// (query_ask.go); the SQL is always the builder's own, so the builder can
// be opened, changed and saved again without anyone reading SQL.
//
// A column is one value per row of the starting table: one of its columns,
// a field from the brand's catalog, an identity (a person's user id), or a
// total over another table that points at the row (the sum of a person's
// transactions). Totals are subqueries, so rows never repeat and a list
// keeps one row per person.

type queryBuilder struct {
	From    string          `json:"from"`
	Columns []builderColumn `json:"columns"`
	Filters []builderFilter `json:"filters"`
	// Match is all (every filter) or any (at least one).
	Match string        `json:"match"`
	Sort  []builderSort `json:"sort"`
	Limit *int          `json:"limit"`
}

// builderSource is where a value comes from: exactly one of its parts.
type builderSource struct {
	Column   string          `json:"column,omitempty"`
	Field    string          `json:"field,omitempty"`
	Identity string          `json:"identity,omitempty"`
	Related  *builderRelated `json:"related,omitempty"`
}

// builderRelated is a total over another table's rows that point at this
// row: Link is the other table's column holding this row's LinkFrom
// (id unless said).
type builderRelated struct {
	Table    string        `json:"table"`
	Link     string        `json:"link"`
	LinkFrom string        `json:"link_from,omitempty"`
	Agg      string        `json:"agg"`
	Column   string        `json:"column,omitempty"`
	Filters  []builderCond `json:"filters,omitempty"`
}

// builderCond is a test on one of a related table's own columns.
type builderCond struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  any    `json:"value,omitempty"`
}

type builderColumn struct {
	Label  string        `json:"label"`
	Source builderSource `json:"source"`
	// DivideBy turns minor units into money (100) and the like.
	DivideBy *float64 `json:"divide_by,omitempty"`
}

// builderFilter tests a shown column (Column, its label) or any source.
type builderFilter struct {
	Column   string         `json:"column,omitempty"`
	Source   *builderSource `json:"source,omitempty"`
	DivideBy *float64       `json:"divide_by,omitempty"`
	Op       string         `json:"op"`
	Value    any            `json:"value,omitempty"`
}

type builderSort struct {
	Column string `json:"column"`
	Dir    string `json:"dir"`
}

const (
	builderMaxColumns = 30
	builderMaxFilters = 30
	builderMaxLimit   = 50000
)

var (
	builderAggs = []string{"count", "count_distinct", "sum", "avg", "min", "max"}
	builderOps  = []string{"eq", "neq", "gt", "gte", "lt", "lte", "contains", "not_contains", "starts_with", "in", "not_in", "is_set", "not_set", "within_days", "before_days"}
	// builderRecordTables are the starting tables whose rows are customer
	// records: their results carry record_id, so they can become a list.
	builderRecordTables = map[string]string{"people": "person", "companies": "account"}
	builderName         = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
)

// builderTable is what the builder knows of one table it may read.
type builderTable struct {
	Columns map[string]string // name → data type
	// RecordType is the catalog type whose fields and identities its rows
	// have ("" for tables that aren't records).
	RecordType string
}

// builderCatalog is everything a builder may name, for one caller.
type builderCatalog struct {
	Tables map[string]builderTable
	// Fields are each record type's catalog fields.
	Fields map[string]map[string]fieldView
}

// builderError is a builder that can't be written as SQL, said so the
// person (or the model) can fix it.
type builderError string

func (e builderError) Error() string { return string(e) }

func bad(format string, args ...any) error { return builderError(fmt.Sprintf(format, args...)) }

// compileBuilder writes b as one SELECT over the query tables.
func compileBuilder(b queryBuilder, cat builderCatalog) (string, error) {
	from, ok := cat.Tables[b.From]
	if !ok {
		return "", bad("from: %q is not a table you can read; pick one from GET .../query/tables", b.From)
	}
	if len(b.Columns) == 0 || len(b.Columns) > builderMaxColumns {
		return "", bad("columns: pick 1 to %d columns to show", builderMaxColumns)
	}
	if len(b.Filters) > builderMaxFilters {
		return "", bad("filters: at most %d", builderMaxFilters)
	}
	if b.Match != "" && b.Match != "all" && b.Match != "any" {
		return "", bad("match is all (every filter) or any (at least one)")
	}
	c := builderCompiler{cat: cat, from: from, fromName: b.From}
	var sel []string
	if from.RecordType != "" {
		if _, ok := from.Columns["id"]; ok {
			sel = append(sel, "t.id AS record_id")
		}
	}
	exprs := map[string]string{}
	for i, col := range b.Columns {
		label := strings.TrimSpace(col.Label)
		switch {
		case label == "" || len(label) > 63:
			return "", bad("columns[%d]: label is 1 to 63 characters", i)
		case strings.EqualFold(label, "record_id"):
			return "", bad("columns[%d]: record_id is added by itself; name the column something else", i)
		case exprs[strings.ToLower(label)] != "":
			return "", bad("columns[%d]: two columns are called %q; give each its own name", i, label)
		}
		e, err := c.source(col.Source, fmt.Sprintf("columns[%d]", i))
		if err != nil {
			return "", err
		}
		if e, err = divide(e, col.DivideBy, fmt.Sprintf("columns[%d]", i)); err != nil {
			return "", err
		}
		exprs[strings.ToLower(label)] = e
		sel = append(sel, e+" AS "+pgx.Identifier{label}.Sanitize())
	}
	var where []string
	for i, f := range b.Filters {
		at := fmt.Sprintf("filters[%d]", i)
		var e string
		switch {
		case f.Column != "" && f.Source != nil:
			return "", bad("%s: test a column (column) or a value (source), not both", at)
		case f.Column != "":
			e = exprs[strings.ToLower(strings.TrimSpace(f.Column))]
			if e == "" {
				return "", bad("%s: there is no column called %q", at, f.Column)
			}
		case f.Source != nil:
			var err error
			if e, err = c.source(*f.Source, at); err != nil {
				return "", err
			}
			if e, err = divide(e, f.DivideBy, at); err != nil {
				return "", err
			}
		default:
			return "", bad("%s: say what to test: a column or a source", at)
		}
		cond, err := condition(e, f.Op, f.Value, at)
		if err != nil {
			return "", err
		}
		where = append(where, cond)
	}
	var order []string
	for i, s := range b.Sort {
		label := strings.TrimSpace(s.Column)
		if exprs[strings.ToLower(label)] == "" {
			return "", bad("sort[%d]: there is no column called %q", i, s.Column)
		}
		// The label exactly as the column says it, so ORDER BY finds it.
		for _, col := range b.Columns {
			if strings.EqualFold(strings.TrimSpace(col.Label), label) {
				label = strings.TrimSpace(col.Label)
			}
		}
		dir := "ASC NULLS LAST"
		switch s.Dir {
		case "", "asc":
		case "desc":
			dir = "DESC NULLS LAST"
		default:
			return "", bad("sort[%d]: dir is asc or desc", i)
		}
		order = append(order, pgx.Identifier{label}.Sanitize()+" "+dir)
	}
	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(strings.Join(sel, ",\n  "))
	sb.WriteString("\nFROM " + pgx.Identifier{b.From}.Sanitize() + " t")
	if len(where) > 0 {
		join := "\n  AND "
		if b.Match == "any" {
			join = "\n  OR "
		}
		sb.WriteString("\nWHERE " + strings.Join(where, join))
	}
	if len(order) > 0 {
		sb.WriteString("\nORDER BY " + strings.Join(order, ", "))
	}
	if b.Limit != nil {
		if *b.Limit < 1 || *b.Limit > builderMaxLimit {
			return "", bad("limit is 1 to %d rows, or none for every row", builderMaxLimit)
		}
		sb.WriteString("\nLIMIT " + strconv.Itoa(*b.Limit))
	}
	return sb.String(), nil
}

type builderCompiler struct {
	cat      builderCatalog
	from     builderTable
	fromName string
}

func (c builderCompiler) source(s builderSource, at string) (string, error) {
	n := 0
	for _, set := range []bool{s.Column != "", s.Field != "", s.Identity != "", s.Related != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return "", bad("%s: a value comes from exactly one of column, field, identity or related", at)
	}
	switch {
	case s.Column != "":
		if _, ok := c.from.Columns[s.Column]; !ok {
			return "", bad("%s: %s has no column %q", at, c.fromName, s.Column)
		}
		return "t." + pgx.Identifier{s.Column}.Sanitize(), nil
	case s.Field != "":
		f, ok := c.cat.Fields[c.from.RecordType][s.Field]
		if c.from.RecordType == "" || !ok {
			return "", bad("%s: %s has no field %q", at, c.fromName, s.Field)
		}
		_, expr := queryFieldColumn(f)
		return strings.Replace(expr, "r.attributes", "t.attributes", -1), nil
	case s.Identity != "":
		if c.from.RecordType == "" {
			return "", bad("%s: only people, companies and other records have identities", at)
		}
		if !builderName.MatchString(s.Identity) || len(s.Identity) > 40 {
			return "", bad("%s: identity is a kind of identity, such as email, user_id, phone or domain", at)
		}
		if _, ok := c.cat.Tables["record_identities"]; !ok {
			return "", bad("%s: reading identities needs record_identities, which you can't read", at)
		}
		return "(SELECT i.value FROM record_identities i WHERE i.record_id = t.id AND i.namespace = '" + s.Identity +
			"' AND i.valid_to IS NULL ORDER BY i.verified DESC, i.created_at LIMIT 1)", nil
	}
	return c.related(*s.Related, at)
}

func (c builderCompiler) related(r builderRelated, at string) (string, error) {
	other, ok := c.cat.Tables[r.Table]
	if !ok {
		return "", bad("%s: %q is not a table you can read", at, r.Table)
	}
	if _, ok := other.Columns[r.Link]; !ok {
		return "", bad("%s: %s has no column %q to link by", at, r.Table, r.Link)
	}
	from := r.LinkFrom
	if from == "" {
		from = "id"
	}
	if _, ok := c.from.Columns[from]; !ok {
		return "", bad("%s: %s has no column %q to link from", at, c.fromName, from)
	}
	if !slices.Contains(builderAggs, r.Agg) {
		return "", bad("%s: agg is one of %s", at, strings.Join(builderAggs, ", "))
	}
	var agg string
	switch {
	case r.Agg == "count" && r.Column == "":
		agg = "count(*)"
	case r.Column == "":
		return "", bad("%s: %s needs the column to total", at, r.Agg)
	default:
		if _, ok := other.Columns[r.Column]; !ok {
			return "", bad("%s: %s has no column %q", at, r.Table, r.Column)
		}
		col := "x." + pgx.Identifier{r.Column}.Sanitize()
		if r.Agg == "count_distinct" {
			agg = "count(DISTINCT " + col + ")"
		} else {
			agg = r.Agg + "(" + col + ")"
		}
	}
	conds := []string{"x." + pgx.Identifier{r.Link}.Sanitize() + " = t." + pgx.Identifier{from}.Sanitize()}
	for i, f := range r.Filters {
		fat := fmt.Sprintf("%s.related.filters[%d]", at, i)
		if _, ok := other.Columns[f.Column]; !ok {
			return "", bad("%s: %s has no column %q", fat, r.Table, f.Column)
		}
		cond, err := condition("x."+pgx.Identifier{f.Column}.Sanitize(), f.Op, f.Value, fat)
		if err != nil {
			return "", err
		}
		conds = append(conds, cond)
	}
	e := "(SELECT " + agg + " FROM " + pgx.Identifier{r.Table}.Sanitize() + " x WHERE " + strings.Join(conds, " AND ") + ")"
	if r.Agg == "sum" || r.Agg == "count" || r.Agg == "count_distinct" {
		// A person with no rows totals 0, not nothing.
		e = "COALESCE(" + e + ", 0)"
	}
	return e, nil
}

func divide(e string, by *float64, at string) (string, error) {
	if by == nil {
		return e, nil
	}
	if *by <= 0 || math.IsInf(*by, 0) || math.IsNaN(*by) {
		return "", bad("%s: divide_by is a number above 0, such as 100 for minor units", at)
	}
	return "round((" + e + ")::numeric / " + strconv.FormatFloat(*by, 'f', -1, 64) + ", 2)", nil
}

// condition writes one test of e. Values are written as literals: the
// query role reads nothing a person couldn't SELECT anyway, and a literal
// can't escape its quotes.
func condition(e, op string, v any, at string) (string, error) {
	if !slices.Contains(builderOps, op) {
		return "", bad("%s: op is one of %s", at, strings.Join(builderOps, ", "))
	}
	switch op {
	case "is_set":
		return e + " IS NOT NULL", nil
	case "not_set":
		return e + " IS NULL", nil
	case "within_days", "before_days":
		n, ok := v.(float64)
		if !ok || n < 0 || n > 36600 || n != math.Trunc(n) {
			return "", bad("%s: value is a whole number of days", at)
		}
		if op == "within_days" {
			return fmt.Sprintf("%s >= now() - interval '%d days'", e, int(n)), nil
		}
		return fmt.Sprintf("%s < now() - interval '%d days'", e, int(n)), nil
	case "in", "not_in":
		list, ok := v.([]any)
		if !ok || len(list) == 0 || len(list) > 500 {
			return "", bad("%s: value is a list of 1 to 500 values", at)
		}
		parts := make([]string, len(list))
		for i, x := range list {
			lit, err := literal(x, at)
			if err != nil {
				return "", err
			}
			parts[i] = lit
		}
		not := ""
		if op == "not_in" {
			not = "NOT "
		}
		return e + " " + not + "IN (" + strings.Join(parts, ", ") + ")", nil
	case "contains", "not_contains", "starts_with":
		s, ok := v.(string)
		if !ok || s == "" {
			return "", bad("%s: value is the text to look for", at)
		}
		pattern := "%" + likeEscape(s) + "%"
		if op == "starts_with" {
			pattern = likeEscape(s) + "%"
		}
		not := ""
		if op == "not_contains" {
			not = "NOT "
		}
		return "(" + e + ")::text " + not + "ILIKE " + quoteText(pattern), nil
	}
	lit, err := literal(v, at)
	if err != nil {
		return "", err
	}
	sym := map[string]string{"eq": "=", "neq": "IS DISTINCT FROM", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[op]
	return e + " " + sym + " " + lit, nil
}

func literal(v any, at string) (string, error) {
	switch x := v.(type) {
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return "", bad("%s: value is not a number", at)
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	case string:
		if len(x) > 1000 {
			return "", bad("%s: value is at most 1000 characters", at)
		}
		return quoteText(x), nil
	case nil:
		return "", bad("%s: value is required (use is_set or not_set to test for nothing)", at)
	}
	return "", bad("%s: value is text, a number, true or false", at)
}

// quoteText writes s as a SQL string literal (standard_conforming_strings
// is on, so only quotes need doubling). NUL can't be in a Postgres string.
func quoteText(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\x00", ""), "'", "''") + "'"
}

// builderCatalogFor is what a caller's builders may name on the route's
// brand: the query tables it can read, the brand's record type tables, and
// the catalog fields of people and companies.
func (s *Server) builderCatalogFor(ctx context.Context, a *access) (builderCatalog, error) {
	cat := builderCatalog{Tables: map[string]builderTable{}, Fields: map[string]map[string]fieldView{}}
	cols, err := s.queryColumns(ctx)
	if err != nil {
		return cat, err
	}
	for _, t := range queryTables {
		if cols[t.Name] == nil || !queryReadable(a, t) {
			continue
		}
		bt := builderTable{Columns: map[string]string{}, RecordType: builderRecordTables[t.Name]}
		for _, c := range cols[t.Name] {
			bt.Columns[c.Name] = c.Type
		}
		cat.Tables[t.Name] = bt
	}
	types, err := s.queryTypeTables(ctx, a)
	if err != nil {
		return cat, err
	}
	for _, t := range types {
		if !queryReadable(a, t.queryTable) {
			continue
		}
		bt := builderTable{Columns: map[string]string{}}
		for _, c := range t.Columns {
			bt.Columns[c.Name] = c.Type
		}
		cat.Tables[t.Name] = bt
	}
	if a.Brand != "" && a.has("customers.read") {
		c, err := s.readCatalog(ctx, a.WorkspaceID, a.Brand, false)
		if err != nil {
			return cat, err
		}
		for _, t := range c.Types {
			if t.Key != "person" && t.Key != "account" {
				continue
			}
			m := map[string]fieldView{}
			for _, f := range t.Fields {
				if !f.Archived && queryTypeKey.MatchString(f.Key) {
					m[f.Key] = f
				}
			}
			cat.Fields[t.Key] = m
		}
	}
	return cat, nil
}

// queryColumns are the query schema's views and their columns.
func (s *Server) queryColumns(ctx context.Context) (map[string][]queryColumn, error) {
	rows, err := s.pool.Query(ctx, `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'query' ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, err
	}
	cols := map[string][]queryColumn{}
	var t string
	var c queryColumn
	_, err = pgx.ForEachRow(rows, []any{&t, &c.Name, &c.Type}, func() error {
		cols[t] = append(cols[t], c)
		return nil
	})
	return cols, err
}

// parseBuilder reads a builder sent as JSON, refusing unknown parts so a
// typo is said rather than ignored.
func parseBuilder(raw json.RawMessage) (queryBuilder, error) {
	var b queryBuilder
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return b, bad("builder: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	// Spell out the defaults, so a saved builder reads the same whoever
	// wrote it.
	if b.Filters == nil {
		b.Filters = []builderFilter{}
	}
	if b.Sort == nil {
		b.Sort = []builderSort{}
	}
	if b.Match == "" {
		b.Match = "all"
	}
	for i := range b.Sort {
		if b.Sort[i].Dir == "" {
			b.Sort[i].Dir = "asc"
		}
	}
	return b, nil
}

// buildQuery answers a builder's SQL without running it: the web app shows
// it beside the builder as it changes.
func (s *Server) buildQuery(w http.ResponseWriter, r *http.Request) {
	a, ok := s.queryAccess(w, r)
	if !ok {
		return
	}
	var in struct {
		Builder json.RawMessage `json:"builder"`
	}
	if !decode(w, r, &in) {
		return
	}
	sql, err := s.compileFor(r.Context(), a, in.Builder)
	if s.builderFailed(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sql": sql})
}

// compileFor parses and writes a builder for the caller.
func (s *Server) compileFor(ctx context.Context, a *access, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", bad("builder is required")
	}
	b, err := parseBuilder(raw)
	if err != nil {
		return "", err
	}
	cat, err := s.builderCatalogFor(ctx, a)
	if err != nil {
		return "", err
	}
	return compileBuilder(b, cat)
}

// builderFailed answers a builder that can't be written as 400
// query_builder_invalid, and anything else as an internal error.
func (s *Server) builderFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(builderError); ok {
		writeError(w, r, http.StatusBadRequest, "query_builder_invalid", string(e))
		return true
	}
	s.internal(w, r, err)
	return true
}
