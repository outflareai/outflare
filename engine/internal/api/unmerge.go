package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Unmerge (plans/brands/README.md, "Undo for bad data"): a merge can be
// undone, and every moved row goes back. A merge writes down what it
// changed (record_merge_moves, migration 0330): the rows that followed the
// merged record, the identities and links it closed and opened, the kept
// record's fields it filled, other records' references it repointed and
// the duplicate pairs it settled. Undoing it walks that ledger backwards.
//
// What was added to the kept record after the merge stays there: only the
// rows the merge moved go back, and a field goes back only while it still
// holds what the merge put there. Merges made before the ledger existed
// can't be undone (409 merge_not_reversible). Chained merges (A into B,
// then B into C) undo newest first: while B is merged into C, undoing A
// into B answers 409 merge_not_latest.
//
// Undoing fires nothing: no rule, journey or email hears of the rows that
// move back (the recovery window's rule). The audit event record.unmerged
// is the one thing that goes out, as a webhook like record.merged.

func (s *Server) routesUnmerge(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/merges", s.onBrand(auth.ScopeCustomersRead, s.listMerges))
	mux.HandleFunc("POST /v1/brands/{brand}/merges/{merge}/undo", s.onBrand(auth.ScopeCustomersManage, s.undoMerge))
}

// ---- The ledger ----

type ledgerField struct {
	Key        string `json:"key"`
	Prev       any    `json:"prev"`
	Next       any    `json:"next"`
	Had        bool   `json:"had"`
	PrevSource string `json:"prev_source"`
}

type ledgerPointer struct {
	Record string `json:"record"`
	Key    string `json:"key"`
	Prev   any    `json:"prev"`
	Next   any    `json:"next"`
}

type ledgerCandidate struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// mergeLedger is what one merge changed.
type mergeLedger struct {
	identClosed, identAdded []string
	linkClosed, linkAdded   []string
	mergedInto              []string
	fields                  []ledgerField
	pointers                []ledgerPointer
	candidates              []ledgerCandidate
	follows                 []customers.Moved
}

// Ledger kinds (record_merge_moves.kind).
const (
	moveFollow         = "follow"
	moveIdentityClosed = "identity_closed"
	moveIdentityAdded  = "identity_added"
	moveLinkClosed     = "link_closed"
	moveLinkAdded      = "link_added"
	moveMergedInto     = "merged_into"
	moveField          = "field"
	movePointer        = "pointer"
	moveCandidate      = "candidate"
)

func (l *mergeLedger) save(ctx context.Context, tx pgx.Tx, ws, mergeID string) error {
	add := func(kind string, table, column, row *string, data any) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO record_merge_moves (merge_id, workspace_id, brand_id, kind, table_name, column_name, row_id, data)
			SELECT m.id, $2, m.brand_id, $3, $4, $5, $6, $7::jsonb FROM record_merges m WHERE m.id = $1`, mergeID, ws, kind, table, column, row, string(b))
		return err
	}
	for kind, ids := range map[string][]string{moveIdentityClosed: l.identClosed, moveIdentityAdded: l.identAdded,
		moveLinkClosed: l.linkClosed, moveLinkAdded: l.linkAdded, moveMergedInto: l.mergedInto} {
		if len(ids) > 0 {
			if err := add(kind, nil, nil, nil, ids); err != nil {
				return err
			}
		}
	}
	for _, f := range l.follows {
		if err := add(moveFollow, &f.Table, &f.Column, nil, f.Keys); err != nil {
			return err
		}
	}
	for _, f := range l.fields {
		if err := add(moveField, nil, nil, &f.Key, f); err != nil {
			return err
		}
	}
	for _, p := range l.pointers {
		if err := add(movePointer, nil, nil, &p.Record, p); err != nil {
			return err
		}
	}
	if len(l.candidates) > 0 {
		if err := add(moveCandidate, nil, nil, nil, l.candidates); err != nil {
			return err
		}
	}
	return nil
}

func loadLedger(ctx context.Context, tx pgx.Tx, mergeID string) (mergeLedger, error) {
	var l mergeLedger
	rows, err := tx.Query(ctx, `SELECT kind, COALESCE(table_name, ''), COALESCE(column_name, ''), data FROM record_merge_moves WHERE merge_id = $1 ORDER BY id`, mergeID)
	if err != nil {
		return l, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, table, column string
		var data []byte
		if err := rows.Scan(&kind, &table, &column, &data); err != nil {
			return l, err
		}
		var ids []string
		switch kind {
		case moveIdentityClosed, moveIdentityAdded, moveLinkClosed, moveLinkAdded, moveMergedInto:
			if err := json.Unmarshal(data, &ids); err != nil {
				return l, err
			}
		}
		switch kind {
		case moveIdentityClosed:
			l.identClosed = ids
		case moveIdentityAdded:
			l.identAdded = ids
		case moveLinkClosed:
			l.linkClosed = ids
		case moveLinkAdded:
			l.linkAdded = ids
		case moveMergedInto:
			l.mergedInto = ids
		case moveFollow:
			l.follows = append(l.follows, customers.Moved{Table: table, Column: column, Keys: data})
		case moveField:
			var f ledgerField
			if err := json.Unmarshal(data, &f); err != nil {
				return l, err
			}
			l.fields = append(l.fields, f)
		case movePointer:
			var p ledgerPointer
			if err := json.Unmarshal(data, &p); err != nil {
				return l, err
			}
			l.pointers = append(l.pointers, p)
		case moveCandidate:
			if err := json.Unmarshal(data, &l.candidates); err != nil {
				return l, err
			}
		}
	}
	return l, rows.Err()
}

// ---- Views ----

type mergeView struct {
	ID      string         `json:"id"`
	BrandID *string        `json:"brand_id"`
	IntoID  string         `json:"into_id"`
	FromID  string         `json:"from_id"`
	Moved   map[string]int `json:"moved"`
	// The actor who merged, or null for Outflare itself.
	MergedBy *string   `json:"merged_by"`
	MergedAt time.Time `json:"merged_at"`
	// False for a merge made before Outflare kept what merges move: it
	// can't be undone.
	Reversible bool       `json:"reversible"`
	UndoneAt   *time.Time `json:"undone_at"`
	// Whether POST .../undo would undo it now: reversible, not undone, and
	// the newest merge of both records.
	Undoable bool `json:"undoable"`
}

const mergeColumns = `m.id::text, m.brand_id::text, m.into_id::text, m.from_id::text, m.moved, m.merged_by::text, m.merged_at, m.reversible, m.undone_at,
	m.reversible AND m.undone_at IS NULL AND f.merged_into = m.into_id AND i.merged_into IS NULL AND f.deleted_at IS NULL AND i.deleted_at IS NULL`

const mergeFrom = ` FROM record_merges m JOIN records f ON f.id = m.from_id JOIN records i ON i.id = m.into_id`

func scanMerge(row pgx.CollectableRow) (mergeView, error) {
	var v mergeView
	err := row.Scan(&v.ID, &v.BrandID, &v.IntoID, &v.FromID, &v.Moved, &v.MergedBy, &v.MergedAt, &v.Reversible, &v.UndoneAt, &v.Undoable)
	if v.Moved == nil {
		v.Moved = map[string]int{}
	}
	return v, err
}

func loadMerge(ctx context.Context, q pgx.Tx, a *access, id string) (mergeView, error) {
	rows, err := q.Query(ctx, `SELECT `+mergeColumns+mergeFrom+` WHERE m.id = $1 AND m.workspace_id = $2 AND m.brand_id = $3`, id, a.WorkspaceID, a.Brand)
	if err != nil {
		return mergeView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanMerge)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) listMerges(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, err := timeCursor(q.Get("before"))
	if s.allocError(w, r, err) {
		return
	}
	where := []string{"m.brand_id = $1", "m.workspace_id = $2", "(m.merged_at, m.id::text) < ($3, $4)"}
	args := []any{a.Brand, a.WorkspaceID, beforeAt, beforeID}
	if v := q.Get("record"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record is a record's id")
			return
		}
		args = append(args, v)
		n := strconv.Itoa(len(args))
		where = append(where, "(m.into_id = $"+n+" OR m.from_id = $"+n+")")
	}
	args = append(args, limit)
	var list []mergeView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+mergeColumns+mergeFrom+` WHERE `+strings.Join(where, " AND ")+`
			ORDER BY m.merged_at DESC, m.id::text DESC LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanMerge)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	if list == nil {
		list = []mergeView{}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.MergedAt.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// ---- Undoing ----

type unmergeResult struct {
	Merge mergeView `json:"merge"`
	// Rows that went back to the merged record, by table, and fields of
	// the kept record put back as they were.
	Restored map[string]int `json:"restored"`
	// What could not go back, and why: a field changed since the merge
	// (changed), an identity someone else holds now (taken).
	NotRestored []notRestored `json:"not_restored"`
}

func (s *Server) undoMerge(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !decodeOptional(w, r, &struct{}{}) {
		return
	}
	id := r.PathValue("merge")
	var out unmergeResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		var err error
		if out.Restored, out.NotRestored, err = unmerge(ctx, tx, a, id); err != nil {
			return err
		}
		out.Merge, err = loadMerge(ctx, tx, a, id)
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "merge not found")
		return
	}
	var c recordConflict
	if errors.As(err, &c) {
		metrics.Unmerged("refused")
	}
	if s.recordError(w, r, err) {
		return
	}
	metrics.Unmerged("undone")
	writeJSON(w, http.StatusOK, out)
}

// unmerge undoes merge id of the route's brand.
func unmerge(ctx context.Context, tx pgx.Tx, a *access, id string) (map[string]int, []notRestored, error) {
	var intoID, fromID string
	var reversible bool
	var undone *time.Time
	var snap struct {
		FromArchived bool   `json:"from_archived"`
		IntoName     string `json:"into_name"`
		Name         string `json:"name"`
	}
	err := tx.QueryRow(ctx, `SELECT into_id::text, from_id::text, reversible, undone_at, snapshot FROM record_merges
		WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, id, a.WorkspaceID, a.Brand).Scan(&intoID, &fromID, &reversible, &undone, &snap)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil, errNotFound
	case err != nil:
		return nil, nil, err
	case undone != nil:
		return nil, nil, recordConflict{"merge_already_undone", "this merge was already undone"}
	case !reversible:
		return nil, nil, recordConflict{"merge_not_reversible", "this merge was made before Outflare kept a record of what merges move, so it can't be undone; split the records by hand instead"}
	}
	// Lock both in id order, as a merge does.
	type state struct {
		mergedInto *string
		deleted    bool
		name       string
	}
	got := map[string]state{}
	first, second := intoID, fromID
	if second < first {
		first, second = second, first
	}
	for _, rid := range []string{first, second} {
		var st state
		if err := tx.QueryRow(ctx, `SELECT merged_into::text, deleted_at IS NOT NULL, name FROM records WHERE id = $1 FOR UPDATE`, rid).
			Scan(&st.mergedInto, &st.deleted, &st.name); err != nil {
			return nil, nil, err
		}
		got[rid] = st
	}
	into, from := got[intoID], got[fromID]
	switch {
	case into.deleted || from.deleted:
		return nil, nil, recordConflict{"conflict", "one of the two records is in Recently deleted; restore it first"}
	case into.mergedInto != nil:
		return nil, nil, recordConflict{"merge_not_latest", "the record kept was itself merged into " + *into.mergedInto + " since; undo that merge first"}
	case from.mergedInto == nil || *from.mergedInto != intoID:
		return nil, nil, recordConflict{"merge_not_latest", "the merged record no longer leads to the record kept; undo the later merge first"}
	}
	led, err := loadLedger(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	restored := map[string]int{}
	var failed []notRestored
	qctx := quietly(ctx)

	// Rows that followed the merged record go back to it.
	back, err := customers.Unfollow(ctx, tx, a.WorkspaceID, fromID, intoID, led.follows)
	if err != nil {
		return nil, nil, err
	}
	for t, n := range back {
		if n > 0 {
			restored[t] += n
		}
	}

	// Identities: the copies on the kept record go, the merged record's
	// own open again.
	tag, err := tx.Exec(ctx, `DELETE FROM record_identities WHERE id = ANY($1::uuid[]) AND record_id = $2`, led.identAdded, intoID)
	if err != nil {
		return nil, nil, err
	}
	if n := int(tag.RowsAffected()); n > 0 {
		restored["identities_removed_from_kept"] = n
	}
	n, left, err := reopen(ctx, tx, "record_identities", `UPDATE record_identities SET valid_to = NULL, closed_reason = NULL
		WHERE id = $1 AND record_id = $2 AND closed_reason = 'merged'`, led.identClosed, fromID)
	if err != nil {
		return nil, nil, err
	}
	restored["record_identities"] += n
	failed = append(failed, left...)

	// Links: the copies go, the merged record's own open again.
	if _, err := tx.Exec(ctx, `DELETE FROM record_relationships WHERE id = ANY($1::uuid[]) AND (from_id = $2 OR to_id = $2)`, led.linkAdded, intoID); err != nil {
		return nil, nil, err
	}
	n, left, err = reopen(ctx, tx, "record_relationships", `UPDATE record_relationships SET valid_to = NULL
		WHERE id = $1 AND (from_id = $2 OR to_id = $2) AND valid_to IS NOT NULL`, led.linkClosed, fromID)
	if err != nil {
		return nil, nil, err
	}
	restored["record_relationships"] += n
	failed = append(failed, left...)

	// The kept record's fields the merge filled, newest first, while they
	// still hold what the merge put there.
	slices.Reverse(led.fields)
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range led.fields {
		ok, err := revertField(qctx, tx, a, c, intoID, f.Key, f.Next, f.Prev, f.Had, f.PrevSource)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			failed = append(failed, notRestored{Table: fieldValueRows, ID: intoID + ":" + f.Key, Reason: reasonChanged})
			continue
		}
		restored["fields"]++
	}
	if snap.IntoName != snap.Name && into.name == snap.Name {
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $2 WHERE id = $1`, intoID, snap.IntoName); err != nil {
			return nil, nil, err
		}
		restored["fields"]++
	}
	// Other records' references point at the merged record again.
	for _, p := range led.pointers {
		ok, err := revertField(qctx, tx, a, c, p.Record, p.Key, p.Next, p.Prev, true, "")
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			failed = append(failed, notRestored{Table: fieldValueRows, ID: p.Record + ":" + p.Key, Reason: reasonChanged})
			continue
		}
		restored["references"]++
	}

	// Records merged into the merged record earlier lead to it again, and
	// it is a record of its own: no longer merged, and archived only if it
	// was before.
	if _, err := tx.Exec(ctx, `UPDATE records SET merged_into = $2 WHERE id = ANY($1::uuid[]) AND merged_into = $3`, led.mergedInto, fromID, intoID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET merged_into = NULL, archived_at = CASE WHEN $2 THEN archived_at END,
		version = version + 1, updated_at = now() WHERE id = $1`, fromID, snap.FromArchived); err != nil {
		return nil, nil, err
	}
	restored["records"] = 1
	if err := touch(ctx, tx, intoID); err != nil {
		return nil, nil, err
	}
	// The duplicate pairs it settled are open again.
	for _, cand := range led.candidates {
		if _, err := tx.Exec(ctx, `UPDATE record_merge_candidates SET status = $2, decided_by = NULL, decided_at = NULL WHERE id = $1 AND status = 'merged'`,
			cand.ID, cand.Status); err != nil {
			return nil, nil, err
		}
	}
	// Billing and first and last touch are worked out again for both,
	// quietly.
	for _, rid := range []string{intoID, fromID} {
		if err := recomputeBilling(qctx, tx, a, c, a.WorkspaceID, rid); err != nil {
			return nil, nil, err
		}
		if err := recomputeTouches(qctx, tx, a, c, rid); err != nil {
			return nil, nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE record_merges SET undone_at = now(), undone_by = $2 WHERE id = $1`, id, nullIfEmpty(a.ActorID)); err != nil {
		return nil, nil, err
	}
	total := 0
	for _, n := range restored {
		total += n
	}
	metrics.UndoRowsReverted("unmerge", total)
	if failed == nil {
		failed = []notRestored{}
	}
	return restored, failed, recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "record.unmerged", "record", fromID,
		map[string]any{"merge_id": id, "into_id": intoID, "from_id": fromID, "brand_id": a.Brand, "restored": restored, "not_restored": len(failed)},
		map[string]any{"merge_id": id, "not_restored": len(failed)})
}

// reopen runs update (with $1 the row's id and $2 arg) for each id in its
// own savepoint, and returns how many changed and the rows whose unique
// value someone else holds now.
func reopen(ctx context.Context, tx pgx.Tx, table, update string, ids []string, arg string) (int, []notRestored, error) {
	n := 0
	var failed []notRestored
	for _, id := range ids {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return 0, nil, err
		}
		tag, err := sp.Exec(ctx, update, id, arg)
		if err != nil {
			_ = sp.Rollback(ctx)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				failed = append(failed, notRestored{Table: table, ID: id, Reason: reasonTaken})
				continue
			}
			return 0, nil, err
		}
		if err := sp.Commit(ctx); err != nil {
			return 0, nil, err
		}
		n += int(tag.RowsAffected())
	}
	return n, failed, nil
}

// revertField puts a record's field back to prev (or removes it when had
// is false), but only while it still holds want (with want nil: while it
// is empty). It tells no rule or journey when ctx is quiet, and keeps a
// reference field's links in step. source is the field's source to put
// back; empty keeps the current one.
func revertField(ctx context.Context, tx pgx.Tx, a *access, c *catalog, recordID, key string, want, prev any, had bool, source string) (bool, error) {
	var attrs map[string]any
	var sources map[string]string
	var recType string
	err := tx.QueryRow(ctx, `SELECT record_type, attributes, attribute_sources FROM records WHERE id = $1 FOR UPDATE`, recordID).Scan(&recType, &attrs, &sources)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cur, has := attrs[key]
	switch {
	case want == nil && has && cur != nil:
		return false, nil
	case want != nil && (!has || !sameJSON(cur, want)):
		return false, nil
	case want == nil && !had:
		// Empty, and to stay empty: nothing to do.
		return true, nil
	}
	if !had {
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = attributes - $2, attribute_sources = attribute_sources - $2,
			version = version + 1, updated_at = now() WHERE id = $1`, recordID, key)
	} else {
		b, merr := json.Marshal(prev)
		if merr != nil {
			return false, merr
		}
		if source == "" {
			source = sources[key]
		}
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = attributes || jsonb_build_object($2::text, $3::jsonb),
			attribute_sources = CASE WHEN $4 = '' THEN attribute_sources - $2 ELSE attribute_sources || jsonb_build_object($2::text, $4::text) END,
			version = version + 1, updated_at = now() WHERE id = $1`, recordID, key, string(b), source)
	}
	if err != nil {
		return false, err
	}
	var next any
	if had {
		next = prev
	}
	if err := attributeChanged(ctx, tx, a, recordID, key, cur, next, has, "manual"); err != nil {
		return false, err
	}
	if t := c.recordType(recType); t != nil {
		if f := t.field(key); f != nil && f.ValueType == "record_reference" {
			if err := syncReferences(ctx, tx, a, recordID, key, next); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}
