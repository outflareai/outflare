package smartlists

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Kinds of list. A smart list fills itself from its rules; a static list
// holds the people someone, the API or a journey step puts in it.
const (
	KindSmart  = "smart"
	KindStatic = "static"
)

// NobodyRules is what a static list keeps as its rules. Static members live
// in static_list_members, so a version from before static lists, reading
// one as a smart list, refreshes it to nobody rather than to everyone.
var NobodyRules = []Rule{{Attribute: "outflare_static_list", Op: "exists"}}

// Never is a static list's refreshed_at, so an older version's sweep never
// picks it.
var Never = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrNotStatic means members were added to or removed from a smart list.
var ErrNotStatic = errors.New("smart lists fill themselves from their rules; only a static list takes people")

// Change is who a static list add or remove touched.
type Change struct {
	Changed int `json:"changed"`
	Members int `json:"members"`
}

// staticList reads a list's kind and brand, locking it until tx ends so
// counts stay right.
func staticList(ctx context.Context, tx pgx.Tx, ws, listID string) (brand *string, err error) {
	var kind string
	err = tx.QueryRow(ctx, `SELECT kind, brand_id::text FROM smart_lists WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, ws, listID).Scan(&kind, &brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGone
	}
	if err != nil {
		return nil, err
	}
	if kind != KindStatic {
		return nil, ErrNotStatic
	}
	return brand, nil
}

// AddStatic puts people in a static list in tx. Only people count: not
// companies, merged or archived records, or, for a brand's list, another
// brand's people. via says who did it (manual, api, journey, import) and
// ref what (a journey run). Rules acting on people joining the list hear
// of it.
func AddStatic(ctx context.Context, tx pgx.Tx, ws, listID string, recordIDs []string, via, ref string) (Change, error) {
	brand, err := staticList(ctx, tx, ws, listID)
	if err != nil {
		return Change{}, err
	}
	rows, err := tx.Query(ctx, `INSERT INTO static_list_members (workspace_id, list_id, record_id, added_via, added_ref)
		SELECT $1, $2, r.id, $4, NULLIF($5, '') FROM records r
		WHERE r.workspace_id = $1 AND r.id = ANY ($3::uuid[]) AND r.record_type = 'person' AND r.merged_into IS NULL AND r.archived_at IS NULL
		  AND ($6::uuid IS NULL OR r.brand_id IS NULL OR r.brand_id = $6)
		ON CONFLICT (list_id, record_id) DO NOTHING RETURNING record_id::text`, ws, listID, recordIDs, via, ref, brand)
	if err != nil {
		return Change{}, err
	}
	added, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return Change{}, err
	}
	return staticChanged(ctx, tx, ws, listID, "enter", added)
}

// RemoveStatic takes people out of a static list in tx.
func RemoveStatic(ctx context.Context, tx pgx.Tx, ws, listID string, recordIDs []string) (Change, error) {
	if _, err := staticList(ctx, tx, ws, listID); err != nil {
		return Change{}, err
	}
	rows, err := tx.Query(ctx, `DELETE FROM static_list_members WHERE workspace_id = $1 AND list_id = $2 AND record_id = ANY ($3::uuid[])
		RETURNING record_id::text`, ws, listID, recordIDs)
	if err != nil {
		return Change{}, err
	}
	gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return Change{}, err
	}
	return staticChanged(ctx, tx, ws, listID, "leave", gone)
}

func staticChanged(ctx context.Context, tx pgx.Tx, ws, listID, on string, ids []string) (Change, error) {
	c := Change{Changed: len(ids)}
	if err := tx.QueryRow(ctx, `UPDATE smart_lists SET members = (SELECT count(*) FROM static_list_members WHERE list_id = $2), updated_at = now()
		WHERE workspace_id = $1 AND id = $2 RETURNING members`, ws, listID).Scan(&c.Members); err != nil {
		return c, err
	}
	if len(ids) == 0 {
		return c, nil
	}
	watched, err := customers.ListWatched(ctx, tx, ws, listID)
	if err != nil || !watched {
		return c, err
	}
	return c, customers.ListChanged(ctx, tx, ws, customers.ListChange{ListID: listID, On: on, RecordIDs: ids, At: time.Now()})
}

// InStatic reports whether a person is in a static list.
func InStatic(ctx context.Context, q jobs.Querier, listID, recordID string) (bool, error) {
	var in bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM static_list_members WHERE list_id = $1 AND record_id = $2)`, listID, recordID).Scan(&in)
	return in, err
}

// membersTable is where a list's members are kept.
func membersTable(ctx context.Context, q Querier, ws, listID string) (string, error) {
	var kind string
	err := q.QueryRow(ctx, `SELECT kind FROM smart_lists WHERE workspace_id = $1 AND id = $2`, ws, listID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "smart_list_members", nil
	}
	if kind == KindStatic {
		return "static_list_members", err
	}
	return "smart_list_members", err
}

// MembersSQL is SQL for the record ids in a list of either kind, with the
// list id at the given placeholder (such as "$2").
func MembersSQL(list string) string {
	return `(SELECT record_id FROM smart_list_members WHERE list_id = ` + list + ` UNION ALL
		SELECT record_id FROM static_list_members WHERE list_id = ` + list + `)`
}
