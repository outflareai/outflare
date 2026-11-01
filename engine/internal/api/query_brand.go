package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Query for one brand (plans/brands/README.md, "Data pages").
//
// A query for a brand runs with the brand fixed in its query_scopes row,
// which the query role can't read or change: every customer view shows
// only rows whose own brand_id is that brand (migration 0342), so no SQL
// text reaches another brand's rows, whatever it filters on or joins. The
// role reads nothing but the query views, so a join to a table or a
// subquery around a view sees no more than the view does.
//
// Each of the brand's record types is a table too: a subscription type is
// queried as subscription, with one column per field. They are temporary
// views the query's own connection makes before the query, over
// query.records (so they hold no more than it does), and they go with the
// connection, closed after every query.

// queryAccess is the access a query runs with. On a brand route it is the
// route's. The workspace routes, kept until the contract step, take
// ?brand= (a brand's id or handle) and default to the workspace's first
// brand; ?brand=all keeps the old query over every brand the caller sees.
func (s *Server) queryAccess(w http.ResponseWriter, r *http.Request) (*access, bool) {
	a := accessFrom(r.Context())
	if a.Brand != "" {
		return a, true
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("brand")), "all") {
		return a, true
	}
	sel, ok := s.dataBrand(w, r)
	if !ok {
		return nil, false
	}
	if sel.id == "" || sel.id == "none" {
		return a, true
	}
	b := *a
	b.Brand = sel.id
	return &b, true
}

// queryTypeTable is one record type's table in a brand's query.
type queryTypeTable struct {
	queryTable
	Type    string
	Columns []queryColumn
	exprs   []string
}

// queryTypeKey is what a type's key must look like to name a view: the
// catalog's own rule, checked again since the view's text is built here.
var queryTypeKey = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

// queryTypeTables are the tables of a brand query's record types: every
// active type of the brand's catalog but person and account (people and
// companies), and none whose name another table has.
func (s *Server) queryTypeTables(ctx context.Context, a *access) ([]queryTypeTable, error) {
	if a.Brand == "" {
		return nil, nil
	}
	c, err := s.readCatalog(ctx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return nil, err
	}
	var out []queryTypeTable
	for _, t := range c.Types {
		if t.Archived || t.Key == "person" || t.Key == "account" || !queryTypeKey.MatchString(t.Key) || strings.HasPrefix(t.Key, "pg_") ||
			slices.ContainsFunc(queryTables, func(q queryTable) bool { return q.Name == t.Key }) {
			continue
		}
		v := queryTypeTable{
			queryTable: queryTable{t.Key, auth.ScopeCustomersRead, fmt.Sprintf("%s: records of the %s type, without merged or archived ones; one column per field.", t.Plural, t.Key)},
			Type:       t.Key,
		}
		v.Columns = append(v.Columns, queryColumn{"id", "uuid"}, queryColumn{"brand_id", "uuid"}, queryColumn{"name", "text"})
		v.exprs = append(v.exprs, "r.id", "r.brand_id", "r.name")
		for _, f := range t.Fields {
			if f.Archived || !queryTypeKey.MatchString(f.Key) {
				continue
			}
			col, expr := queryFieldColumn(f)
			v.Columns = append(v.Columns, queryColumn{f.Key, col})
			v.exprs = append(v.exprs, expr+" AS "+pgx.Identifier{f.Key}.Sanitize())
		}
		v.Columns = append(v.Columns, queryColumn{"attributes", "jsonb"}, queryColumn{"origin", "text"},
			queryColumn{"created_at", "timestamp with time zone"}, queryColumn{"updated_at", "timestamp with time zone"})
		v.exprs = append(v.exprs, "r.attributes", "r.origin", "r.created_at", "r.updated_at")
		out = append(out, v)
	}
	return out, nil
}

// queryFieldColumn is a field's column type and the expression reading it
// from a record's attributes. A value of an unexpected kind reads as null,
// never as an error that stops the query.
func queryFieldColumn(f fieldView) (string, string) {
	key := "'" + f.Key + "'" // queryTypeKey: no quotes or backslashes
	switch {
	case f.Multiple, f.ValueType == "currency", f.ValueType == "location":
		return "jsonb", "r.attributes->" + key
	case f.ValueType == "number" || f.ValueType == "rating":
		return "numeric", "CASE WHEN jsonb_typeof(r.attributes->" + key + ") = 'number' THEN (r.attributes->>" + key + ")::numeric END"
	case f.ValueType == "checkbox":
		return "boolean", "CASE WHEN jsonb_typeof(r.attributes->" + key + ") = 'boolean' THEN (r.attributes->>" + key + ")::boolean END"
	}
	return "text", "r.attributes->>" + key
}

// makeTypeViews makes the type tables on a query's connection, before its
// read-only transaction. Only text built here runs in this transaction.
// Without them (a database that refuses temporary views) the query still
// runs, and naming a type's table is an unknown table.
func (s *Server) makeTypeViews(ctx context.Context, conn *pgx.Conn, a *access, types []queryTypeTable) error {
	if len(types) == 0 {
		return nil
	}
	err := pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: pgx.ReadWrite}, func(tx pgx.Tx) error {
		for _, t := range types {
			if !queryTypeKey.MatchString(t.Type) {
				continue
			}
			sql := `CREATE TEMPORARY VIEW ` + pgx.Identifier{t.Name}.Sanitize() + ` AS SELECT ` + strings.Join(t.exprs, ", ") +
				` FROM query.records r WHERE r.record_type = '` + t.Type + `' AND r.merged_into IS NULL AND r.archived_at IS NULL`
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		metrics.QueryTypeTablesFailed()
		track.Fail(ctx, s.log, track.Event{Name: "query.type_tables_failed", WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "system"}, "query_unavailable", err)
		return nil
	}
	return err
}
