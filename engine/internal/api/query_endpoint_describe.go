package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Describing a query endpoint in plain English (docs/query-endpoints.md):
// one or two sentences on what it returns, written from the saved query
// itself: the table it reads, its filters, the parameters callers send, its
// order and its row limit. It is written by rules, not by an AI model, so it
// costs nothing, needs no key and says the same thing every time. Publishing
// without a description uses it; people change it as they like.
//
// A builder query is described from the builder. Hand-written SQL is read
// with a light scanner that knows the usual shapes (col = $1, col >= now() -
// interval '30 days', ORDER BY, LIMIT). What it can't read it counts ("and 2
// more conditions") instead of guessing.

func (s *Server) describeQueryEndpoint(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		SavedQueryID string          `json:"saved_query_id"`
		Version      *int            `json:"version"`
		Params       []endpointParam `json:"params"`
		RowCap       *int            `json:"row_cap"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validUUID(in.SavedQueryID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "saved_query_id is required: the saved query to describe")
		return
	}
	if len(in.Params) > queryEndpointMaxParams {
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("an endpoint takes up to %d parameters", queryEndpointMaxParams))
		return
	}
	rowCap := 1000
	if in.RowCap != nil {
		if *in.RowCap < 1 || *in.RowCap > queryEndpointMaxRows {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("row_cap is 1 to %d", queryEndpointMaxRows))
			return
		}
		rowCap = *in.RowCap
	}
	sql, b, err := endpointSource(ctx, s.pool, a, in.SavedQueryID, in.Version)
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"description": describeEndpoint(sql, b, in.Params, rowCap, s.fieldLabels(ctx, a, b))})
}

// endpointSource reads a saved query version's SQL and builder (nil for
// hand-written SQL): its current version when version is nil.
func endpointSource(ctx context.Context, q querier, a *access, queryID string, version *int) (string, *queryBuilder, error) {
	var sql string
	var raw []byte
	err := q.QueryRow(ctx, `SELECT v.sql, v.builder FROM saved_query_versions v JOIN saved_queries q ON q.id = v.query_id
		WHERE q.workspace_id = $1 AND q.brand_id = $2 AND q.id = $3 AND v.version = COALESCE($4::int, q.current_version) AND q.archived_at IS NULL
		AND (q.visibility = 'brand' OR (q.owner_id IS NOT NULL AND q.owner_id::text = $5::text))`,
		a.WorkspaceID, a.Brand, queryID, version, a.personID()).Scan(&sql, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, errInvalid("saved_query_id and version are a saved query of this brand you can see that isn't archived, and one of its versions")
	}
	if err != nil {
		return "", nil, err
	}
	var b *queryBuilder
	if len(raw) > 0 && string(raw) != "null" {
		b = &queryBuilder{}
		if json.Unmarshal(raw, b) != nil {
			b = nil
		}
	}
	return sql, b, nil
}

// fieldLabels names the catalog fields a builder uses, by key. A failure
// to read the catalog only costs the labels: keys are humanized instead.
func (s *Server) fieldLabels(ctx context.Context, a *access, b *queryBuilder) map[string]string {
	out := map[string]string{}
	if b == nil {
		return out
	}
	uses := false
	for _, c := range b.Columns {
		uses = uses || c.Source.Field != ""
	}
	for _, f := range b.Filters {
		uses = uses || (f.Source != nil && f.Source.Field != "")
	}
	if !uses {
		return out
	}
	cat, err := s.builderCatalogFor(ctx, a)
	if err != nil {
		return out
	}
	for _, f := range cat.Fields[builderRecordTables[b.From]] {
		out[f.Key] = f.Label
	}
	return out
}

// describeEndpoint is the description itself.
func describeEndpoint(sql string, b *queryBuilder, params []endpointParam, rowCap int, fields map[string]string) string {
	var d endpointDescription
	if b != nil {
		d = describeBuilder(b, fields)
	} else {
		d = describeSQL(sql, params)
	}
	return d.text(params, rowCap)
}

// endpointDescription is what a description says, before it is written.
type endpointDescription struct {
	from    string   // what a row is: "people"
	where   []string // each filter, in words
	join    string   // "and" or "or"
	more    int      // conditions it couldn't read
	group   []string
	shows   []string
	showAll bool
	sort    string
	limit   int    // 0: none
	limitBy string // a parameter's name, when LIMIT $n
}

func (d endpointDescription) text(params []endpointParam, rowCap int) string {
	var b strings.Builder
	from := d.from
	if from == "" {
		from = "Rows"
	}
	b.WriteString(capitalize(from))
	if len(d.where) > 0 {
		join := d.join
		if join == "" {
			join = "and"
		}
		b.WriteString(" where ")
		b.WriteString(listWords(d.where, join))
	}
	if d.more > 0 {
		if len(d.where) > 0 {
			fmt.Fprintf(&b, ", plus %d more condition%s", d.more, plural1(d.more))
		} else {
			fmt.Fprintf(&b, " that pass %d condition%s", d.more, plural1(d.more))
		}
	}
	if len(d.group) > 0 {
		b.WriteString(", grouped by ")
		b.WriteString(listWords(d.group, "and"))
	}
	if d.sort != "" {
		b.WriteString(", ")
		b.WriteString(d.sort)
	}
	b.WriteString(".")
	switch {
	case d.showAll:
		b.WriteString(" Returns every column")
	case len(d.shows) > 6:
		fmt.Fprintf(&b, " Returns %s and %d more", strings.Join(d.shows[:5], ", "), len(d.shows)-5)
	case len(d.shows) > 0:
		b.WriteString(" Returns ")
		b.WriteString(listWords(d.shows, "and"))
	default:
		b.WriteString(" Returns")
	}
	rows := rowCap
	if d.limit > 0 && d.limit < rows {
		rows = d.limit
	}
	if d.limitBy != "" {
		fmt.Fprintf(&b, ", as many rows as %s says, up to %s.", d.limitBy, thousands(rowCap))
	} else {
		fmt.Fprintf(&b, ", up to %s row%s a call.", thousands(rows), plural1(rows))
	}
	if len(params) > 0 {
		var ps []string
		for _, p := range params {
			s := p.Name + " (" + p.Type
			if !p.Required {
				s += ", optional"
			}
			if p.Default != nil {
				s += fmt.Sprintf(", %v when left out", p.Default)
			}
			ps = append(ps, s+")")
		}
		b.WriteString(" Callers send ")
		b.WriteString(listWords(ps, "and"))
		b.WriteString(".")
	}
	return b.String()
}

// ---- Builders ----

var builderOpWords = map[string]string{
	"eq": "is", "neq": "is not", "gt": "is more than", "gte": "is at least", "lt": "is less than", "lte": "is at most",
	"contains": "contains", "not_contains": "doesn't contain", "starts_with": "starts with", "in": "is one of", "not_in": "is none of",
	"is_set": "has a value", "not_set": "is empty",
}

var builderAggWords = map[string]string{"count": "number of", "count_distinct": "number of different", "sum": "total", "avg": "average", "min": "lowest", "max": "highest"}

func describeBuilder(b *queryBuilder, fields map[string]string) endpointDescription {
	d := endpointDescription{from: words(b.From), join: "and"}
	if b.Match == "any" {
		d.join = "or"
	}
	source := func(s builderSource) string {
		switch {
		case s.Column != "":
			return words(s.Column)
		case s.Field != "":
			if l := fields[s.Field]; l != "" {
				return strings.ToLower(l)
			}
			return words(s.Field)
		case s.Identity != "":
			return words(s.Identity)
		case s.Related != nil:
			r := s.Related
			what := words(r.Table)
			if r.Column != "" && !(r.Agg == "count" || r.Agg == "count_distinct") {
				what = words(r.Column) + " of " + what
			} else if r.Column != "" {
				what = words(r.Column) + " in " + what
			}
			var conds []string
			for _, c := range r.Filters {
				conds = append(conds, filterWords(words(c.Column), c.Op, c.Value))
			}
			out := builderAggWords[r.Agg] + " " + what
			if len(conds) > 0 {
				out += " (" + strings.Join(conds, ", ") + ")"
			}
			return out
		}
		return "a value"
	}
	labels := map[string]string{}
	for _, c := range b.Columns {
		d.shows = append(d.shows, strings.ToLower(c.Label))
		labels[c.Label] = source(c.Source)
	}
	for _, f := range b.Filters {
		what := "a value"
		if f.Source != nil {
			what = source(*f.Source)
		} else if f.Column != "" {
			what = strings.ToLower(f.Column)
		}
		d.where = append(d.where, filterWords(what, f.Op, f.Value))
	}
	if len(b.Sort) > 0 {
		var parts []string
		for _, s := range b.Sort {
			parts = append(parts, sortWords(strings.ToLower(s.Column), s.Dir == "desc"))
		}
		d.sort = listWords(parts, "then")
	}
	if b.Limit != nil {
		d.limit = *b.Limit
	}
	return d
}

func filterWords(what, op string, v any) string {
	switch op {
	case "is_set", "not_set":
		return what + " " + builderOpWords[op]
	case "within_days":
		return fmt.Sprintf("%s is within the last %s days", what, valueWords(v))
	case "before_days":
		return fmt.Sprintf("%s is more than %s days ago", what, valueWords(v))
	}
	w := builderOpWords[op]
	if w == "" {
		w = op
	}
	return what + " " + w + " " + valueWords(v)
}

func valueWords(v any) string {
	switch x := v.(type) {
	case nil:
		return "nothing"
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []any:
		var parts []string
		for _, e := range x {
			parts = append(parts, valueWords(e))
		}
		return listWords(parts, "or")
	}
	return fmt.Sprint(v)
}

// ---- Hand-written SQL ----

var (
	sqlComment    = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)
	sqlSpace      = regexp.MustCompile(`\s+`)
	sqlCast       = regexp.MustCompile(`::[a-z_ ]+(\[\])?`)
	sqlIdent      = `(?:"?[a-z_][a-z0-9_]*"?\.)?"?([a-z_][a-z0-9_]*)"?`
	sqlParam      = regexp.MustCompile(`^\$([0-9]+)$`)
	sqlNumber     = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	sqlString     = regexp.MustCompile(`^'((?:[^']|'')*)'$`)
	sqlCompare    = regexp.MustCompile(`^` + sqlIdent + `\s*(=|<>|!=|>=|<=|>|<|not ilike|not like|ilike|like)\s*(.+)$`)
	sqlNull       = regexp.MustCompile(`^` + sqlIdent + `\s+is\s+(not\s+)?null$`)
	sqlAny        = regexp.MustCompile(`^` + sqlIdent + `\s*=\s*any\s*\(\s*(\$[0-9]+)\s*\)$`)
	sqlIn         = regexp.MustCompile(`^` + sqlIdent + `\s+(not\s+)?in\s*\((.+)\)$`)
	sqlRecent     = regexp.MustCompile(`^` + sqlIdent + `\s*(>=|>|<=|<)\s*(?:now\(\)|current_date|current_timestamp)\s*-\s*(?:interval\s*)?'([0-9]+)\s*(day|days|week|weeks|month|months|hour|hours)'$`)
	sqlBetween    = regexp.MustCompile(`^` + sqlIdent + `\s+between\s+(.+)\s+and\s+(.+)$`)
	sqlAlias      = regexp.MustCompile(`(?:\bas\s+|\s)"?([a-z_][a-z0-9_]*)"?$`)
	sqlBareColumn = regexp.MustCompile(`^` + sqlIdent + `$`)
	sqlTable      = regexp.MustCompile(`\b(?:from|join)\s+(?:"?query"?\.)?"?([a-z_][a-z0-9_]*)"?`)
)

func describeSQL(sql string, params []endpointParam) endpointDescription {
	var d endpointDescription
	s, quoted := maskStrings(strings.TrimSpace(sqlComment.ReplaceAllString(sql, " ")))
	s = strings.TrimSuffix(strings.TrimSpace(sqlSpace.ReplaceAllString(strings.ToLower(s), " ")), ";")
	unmask := func(x string) string { return unmaskStrings(x, quoted) }
	paramName := func(ref string) string {
		m := sqlParam.FindStringSubmatch(ref)
		if m == nil {
			return ""
		}
		n, _ := strconv.Atoi(m[1])
		if n >= 1 && n <= len(params) {
			return params[n-1].Name
		}
		return "parameter " + m[1]
	}
	value := func(raw string) (string, bool) {
		raw = strings.TrimSpace(sqlCast.ReplaceAllString(raw, ""))
		raw = strings.Trim(raw, "() ")
		if p := paramName(raw); p != "" {
			return "the " + p + " you send", true
		}
		if sqlNumber.MatchString(raw) {
			return raw, true
		}
		if m := sqlString.FindStringSubmatch(unmask(raw)); m != nil {
			return strings.ReplaceAll(m[1], "''", "'"), true
		}
		switch raw {
		case "true", "false":
			return raw, true
		case "now()", "current_date", "current_timestamp":
			return "now", true
		}
		return "", false
	}

	// The main SELECT: past any WITH, the last top-level SELECT.
	body := s
	if strings.HasPrefix(body, "with ") {
		if i := lastTopLevel(body, "select "); i >= 0 {
			body = body[i:]
		}
	}
	clauses := topLevelClauses(body)

	// What a row is: the first table read.
	if m := sqlTable.FindStringSubmatch(clauses["from"]); m != nil {
		d.from = words(m[1])
	} else if m := sqlTable.FindStringSubmatch(s); m != nil {
		d.from = words(m[1])
	}

	// The columns.
	for _, col := range splitTopLevel(strings.TrimPrefix(clauses["select"], "distinct "), ",") {
		col = strings.TrimSpace(col)
		switch {
		case col == "*" || strings.HasSuffix(col, ".*"):
			d.showAll = true
		case sqlBareColumn.MatchString(col):
			d.shows = append(d.shows, words(sqlBareColumn.FindStringSubmatch(col)[1]))
		default:
			if m := sqlAlias.FindStringSubmatch(col); m != nil && !strings.HasSuffix(col, ")") {
				d.shows = append(d.shows, words(m[1]))
			} else if m := sqlAlias.FindStringSubmatch(col); m != nil && strings.Contains(col, " as ") {
				d.shows = append(d.shows, words(m[1]))
			} else {
				d.shows = append(d.shows, "a computed value")
			}
		}
	}
	if d.showAll {
		d.shows = nil
	}

	// The filters.
	where := clauses["where"]
	if where != "" {
		conds := splitTopLevel(where, " and ")
		if len(conds) == 1 {
			if ors := splitTopLevel(where, " or "); len(ors) > 1 {
				conds, d.join = ors, "or"
			}
		}
		for _, c := range conds {
			if w, ok := condWords(stripOuter(strings.TrimSpace(c)), value, unmask); ok {
				d.where = append(d.where, w)
			} else {
				d.more++
			}
		}
	}

	for _, g := range splitTopLevel(clauses["group by"], ",") {
		if m := sqlBareColumn.FindStringSubmatch(strings.TrimSpace(g)); m != nil {
			d.group = append(d.group, words(m[1]))
		} else if strings.TrimSpace(g) != "" {
			d.group = append(d.group, "a computed value")
		}
	}

	var sorts []string
	for _, o := range splitTopLevel(clauses["order by"], ",") {
		o = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(o, " nulls last", ""), " nulls first", ""))
		desc := strings.HasSuffix(o, " desc")
		o = strings.TrimSuffix(strings.TrimSuffix(o, " desc"), " asc")
		o = strings.TrimSpace(o)
		if n, err := strconv.Atoi(o); err == nil && n >= 1 && n <= len(d.shows) {
			sorts = append(sorts, sortWords(d.shows[n-1], desc))
		} else if m := sqlBareColumn.FindStringSubmatch(o); m != nil {
			sorts = append(sorts, sortWords(words(m[1]), desc))
		}
	}
	if len(sorts) > 0 {
		d.sort = listWords(sorts, "then")
	}

	if lim := strings.TrimSpace(clauses["limit"]); lim != "" {
		lim = strings.Fields(sqlCast.ReplaceAllString(lim, ""))[0]
		if p := paramName(lim); p != "" {
			d.limitBy = p
		} else if n, err := strconv.Atoi(lim); err == nil {
			d.limit = n
		}
	}
	return d
}

// condWords says one WHERE condition in words, or false.
func condWords(c string, value func(string) (string, bool), unmask func(string) string) (string, bool) {
	if m := sqlNull.FindStringSubmatch(c); m != nil {
		if m[2] != "" {
			return words(m[1]) + " has a value", true
		}
		return words(m[1]) + " is empty", true
	}
	if m := sqlAny.FindStringSubmatch(c); m != nil {
		v, _ := value(m[2])
		return words(m[1]) + " is one of " + strings.TrimPrefix(v, "the "), true
	}
	if m := sqlRecent.FindStringSubmatch(strings.ToLower(unmask(c))); m != nil {
		n, unit := m[3], strings.TrimSuffix(m[4], "s")
		if n != "1" {
			unit += "s"
		}
		if m[2] == ">=" || m[2] == ">" {
			return fmt.Sprintf("%s is within the last %s %s", words(m[1]), n, unit), true
		}
		return fmt.Sprintf("%s is more than %s %s ago", words(m[1]), n, unit), true
	}
	if m := sqlBetween.FindStringSubmatch(c); m != nil {
		lo, ok1 := value(m[2])
		hi, ok2 := value(m[3])
		if ok1 && ok2 {
			return fmt.Sprintf("%s is between %s and %s", words(m[1]), lo, hi), true
		}
		return "", false
	}
	if m := sqlIn.FindStringSubmatch(c); m != nil {
		var vs []string
		for _, x := range splitTopLevel(m[3], ",") {
			v, ok := value(x)
			if !ok {
				return "", false
			}
			vs = append(vs, v)
		}
		if m[2] != "" {
			return words(m[1]) + " is none of " + listWords(vs, "or"), true
		}
		return words(m[1]) + " is one of " + listWords(vs, "or"), true
	}
	if m := sqlCompare.FindStringSubmatch(c); m != nil {
		col, op := m[1], m[2]
		if strings.HasSuffix(op, "like") {
			lit := sqlString.FindStringSubmatch(unmask(strings.TrimSpace(sqlCast.ReplaceAllString(m[3], ""))))
			neg := strings.HasPrefix(op, "not")
			if lit != nil {
				pat := strings.ReplaceAll(lit[1], "''", "'")
				inner := strings.Trim(pat, "%")
				switch {
				case strings.HasPrefix(pat, "%") && strings.HasSuffix(pat, "%") && !neg:
					return words(col) + " contains " + inner, true
				case strings.HasPrefix(pat, "%") && strings.HasSuffix(pat, "%"):
					return words(col) + " doesn't contain " + inner, true
				case strings.HasSuffix(pat, "%") && !neg:
					return words(col) + " starts with " + inner, true
				case strings.HasPrefix(pat, "%") && !neg:
					return words(col) + " ends with " + inner, true
				}
			}
			if v, ok := value(m[3]); ok {
				if neg {
					return words(col) + " doesn't match " + v, true
				}
				return words(col) + " matches " + v, true
			}
			return "", false
		}
		v, ok := value(m[3])
		if !ok {
			return "", false
		}
		return words(col) + " " + compareWords(col, op) + " " + v, true
	}
	return "", false
}

// compareWords reads a comparison as dates for time columns.
func compareWords(col, op string) string {
	timeish := strings.HasSuffix(col, "_at") || strings.HasSuffix(col, "_on") || strings.HasSuffix(col, "date") || col == "day" || col == "week" || col == "month"
	if timeish {
		switch op {
		case ">":
			return "is after"
		case ">=":
			return "is on or after"
		case "<":
			return "is before"
		case "<=":
			return "is on or before"
		}
	}
	return map[string]string{"=": "is", "<>": "is not", "!=": "is not", ">": "is more than", ">=": "is at least", "<": "is less than", "<=": "is at most"}[op]
}

func sortWords(col string, desc bool) string {
	timeish := strings.Contains(col, "created") || strings.Contains(col, "date") || strings.HasPrefix(col, "last ") || strings.HasPrefix(col, "first ") ||
		strings.Contains(col, "when") || col == "day" || col == "week" || col == "month" || strings.HasSuffix(col, " time")
	switch {
	case timeish && desc:
		return "newest " + col + " first"
	case timeish:
		return "oldest " + col + " first"
	case desc:
		return "highest " + col + " first"
	}
	return "by " + col
}

// stripOuter takes off brackets around the whole of s.
func stripOuter(s string) string {
	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		depth := 0
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 && i < len(s)-1 {
				return s
			}
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// topLevelClauses splits a SELECT into its clauses, outside brackets.
func topLevelClauses(s string) map[string]string {
	keys := []string{"select", "from", "where", "group by", "having", "order by", "limit", "offset"}
	out := map[string]string{}
	type at struct {
		key string
		i   int
	}
	var found []at
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 || (i > 0 && isWordByte(s[i-1])) {
			continue
		}
		for _, k := range keys {
			if strings.HasPrefix(s[i:], k) && (i+len(k) == len(s) || !isWordByte(s[i+len(k)])) {
				found = append(found, at{k, i})
				i += len(k) - 1
				break
			}
		}
	}
	for n, f := range found {
		end := len(s)
		if n+1 < len(found) {
			end = found[n+1].i
		}
		if _, ok := out[f.key]; !ok {
			out[f.key] = strings.TrimSpace(s[f.i+len(f.key) : end])
		}
	}
	return out
}

func isWordByte(c byte) bool {
	return c == '_' || c == '.' || c == '"' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// splitTopLevel splits s on sep outside brackets.
func splitTopLevel(s, sep string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && strings.HasPrefix(s[i:], sep) {
				// BETWEEN x AND y is one condition.
				if sep == " and " && strings.Contains(s[start:i], " between ") && !strings.Contains(s[strings.LastIndex(s[start:i], " between ")+start:i], " and ") {
					continue
				}
				out = append(out, s[start:i])
				start = i + len(sep)
				i += len(sep) - 1
			}
		}
	}
	return append(out, s[start:])
}

func lastTopLevel(s, word string) int {
	depth, last := 0, -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && strings.HasPrefix(s[i:], word) && (i == 0 || !isWordByte(s[i-1])) {
				last = i
			}
		}
	}
	return last
}

// maskStrings swaps quoted strings for placeholders, so lowercasing and
// splitting leave their text alone.
func maskStrings(s string) (string, []string) {
	var quoted []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			continue
		}
		j := i + 1
		for j < len(s) {
			if s[j] == '\'' {
				if j+1 < len(s) && s[j+1] == '\'' {
					j += 2
					continue
				}
				break
			}
			j++
		}
		if j >= len(s) {
			b.WriteString(s[i:])
			break
		}
		quoted = append(quoted, s[i:j+1])
		fmt.Fprintf(&b, "'\x00%d'", len(quoted)-1)
		i = j
	}
	return b.String(), quoted
}

var maskedString = regexp.MustCompile("'\x00([0-9]+)'")

func unmaskStrings(s string, quoted []string) string {
	return maskedString.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(maskedString.FindStringSubmatch(m)[1])
		if n < len(quoted) {
			return quoted[n]
		}
		return m
	})
}

// ---- Words ----

var knownWords = map[string]string{
	"brand_amount_minor": "amount", "amount_minor": "amount", "record_id": "person or company", "created_at": "created",
	"updated_at": "last changed", "occurred_at": "when it happened", "anonymous_id": "anonymous id", "user_id": "user id",
	"monthly_minor": "monthly amount", "customer_events": "product events", "record_identities": "identities",
}

// words turns a column or table name into words: created_at → created.
func words(s string) string {
	if w, ok := knownWords[s]; ok {
		return w
	}
	for _, suffix := range []string{"_minor", "_cents", "_at"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.ReplaceAll(s, "_", " ")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func listWords(items []string, join string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	if join == "then" {
		return strings.Join(items, ", then ")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + join + " " + items[len(items)-1]
}

func plural1(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
