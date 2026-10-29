package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Duplicate profiles. Two records that share an email or phone may be the
// same human: Outflare lists the pair for someone to merge, link or dismiss
// ("same human?"). It merges on its own only when someone proves they own
// an email another record holds and that record has no user id of its own.
// Two records with different user ids, two real accounts in the
// workspace's product, are never merged: they can be linked as the same
// human instead.

func (s *Server) routesMerges(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	base := "/v1/workspaces/{ws}/records"
	mux.HandleFunc("GET "+base+"/duplicates", s.inBrand(read, s.listDuplicates))
	mux.HandleFunc("POST "+base+"/duplicates/{candidate}/dismiss", s.inBrand(manage, s.decideDuplicate))
	mux.HandleFunc("POST "+base+"/duplicates/{candidate}/link", s.inBrand(manage, s.decideDuplicate))
	mux.HandleFunc("POST "+base+"/{record}/merge", s.inBrand(manage, s.mergeRecord))
}

// The identities that make two records look like one person.
var duplicateNamespaces = []string{"email", "phone"}

// flagDuplicates lists the records that share an identity with rec as
// possible duplicates. It reports how many pairs it added.
func flagDuplicates(ctx context.Context, tx pgx.Tx, ws, rec, namespace, value string) (int, error) {
	if !slices.Contains(duplicateNamespaces, namespace) {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO record_merge_candidates (workspace_id, record_a, record_b, record_type, reason, evidence, status)
		SELECT $1, LEAST(o.id, me.id), GREATEST(o.id, me.id), me.record_type, $3,
			jsonb_build_object('namespace', $3::text, 'verified', bool_or(i.verified) OR EXISTS (
				SELECT 1 FROM record_identities v WHERE v.record_id = me.id AND v.namespace = $3 AND v.value = $4 AND v.valid_to IS NULL AND v.verified)),
			CASE WHEN `+bothUserIDs("o.id", "me.id")+` THEN 'blocked' ELSE 'open' END
		FROM records me
		JOIN record_identities i ON i.workspace_id = $1 AND i.namespace = $3 AND i.value = $4 AND i.valid_to IS NULL AND i.record_id <> me.id
		JOIN records o ON o.id = i.record_id AND o.merged_into IS NULL AND o.record_type = me.record_type
		WHERE me.id = $2 AND me.workspace_id = $1
		GROUP BY o.id, me.id, me.record_type
		ON CONFLICT DO NOTHING`, ws, rec, namespace, value)
	return int(tag.RowsAffected()), err
}

// flagPair lists two records as a possible duplicate.
func flagPair(ctx context.Context, tx pgx.Tx, ws, x, y, reason string, verified bool) (int, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO record_merge_candidates (workspace_id, record_a, record_b, record_type, reason, evidence, status)
		SELECT $1, LEAST(r.id, o.id), GREATEST(r.id, o.id), r.record_type, $4, jsonb_build_object('namespace', $4::text, 'verified', $5::boolean),
			CASE WHEN `+bothUserIDs("r.id", "o.id")+` THEN 'blocked' ELSE 'open' END
		FROM records r JOIN records o ON o.id = $3 AND o.workspace_id = $1 AND o.record_type = r.record_type
		WHERE r.id = $2 AND r.workspace_id = $1
		ON CONFLICT DO NOTHING`, ws, x, y, reason, verified)
	return int(tag.RowsAffected()), err
}

func bothUserIDs(x, y string) string {
	has := func(id string) string {
		return `EXISTS (SELECT 1 FROM record_identities u WHERE u.record_id = ` + id + ` AND u.namespace = 'user_id' AND u.valid_to IS NULL)`
	}
	return has(x) + ` AND ` + has(y)
}

// userIDs returns a record's active user ids.
func userIDs(ctx context.Context, tx pgx.Tx, rec string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT value FROM record_identities WHERE record_id = $1 AND namespace = 'user_id' AND valid_to IS NULL ORDER BY value`, rec)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

type mergeResult struct {
	Identities    int `json:"identities"`
	Attributes    int `json:"attributes"`
	Relationships int `json:"relationships"`
	Events        int `json:"events"`
	Consents      int `json:"consents"`
	Subscriptions int `json:"subscriptions"`
	// Leads, emails, replies, meetings, deals, broadcast sends and journey runs.
	Activity int `json:"activity"`
}

// mergeRecords merges from into into: from's identities, the attributes
// into lacks (or holds from a lower-precedence source), its links, product
// events and consents move to into, and from is kept, archived and marked
// merged_into, so its history and old links still resolve. Both must be
// the same type and not two different product accounts.
func mergeRecords(ctx context.Context, tx pgx.Tx, a *access, c *catalog, intoID, fromID string) (mergeResult, error) {
	var res mergeResult
	if intoID == fromID {
		return res, errInvalid("a record cannot be merged into itself")
	}
	// Lock in id order, so two merges of the same pair cannot deadlock.
	first, second := intoID, fromID
	if second < first {
		first, second = second, first
	}
	locked := map[string]recordView{}
	for _, id := range []string{first, second} {
		rec, err := lockRecord(ctx, tx, a, id)
		if err != nil {
			return res, err
		}
		locked[id] = rec
	}
	into, from := locked[intoID], locked[fromID]
	if into.Type != from.Type {
		return res, errInvalid(fmt.Sprintf("only records of the same type merge: this is a %s and that a %s", into.Type, from.Type))
	}
	mine, err := userIDs(ctx, tx, into.ID)
	if err != nil {
		return res, err
	}
	theirs, err := userIDs(ctx, tx, from.ID)
	if err != nil {
		return res, err
	}
	if len(mine) > 0 && len(theirs) > 0 && !slices.Equal(mine, theirs) {
		return res, recordConflict{"merge_refused", "both records have their own user id, so they are two accounts in your product and are never merged; link them as the same human instead"}
	}

	// Identities: close each on from and give into the same one, keeping
	// when it was first seen and whether it was verified.
	rows, err := tx.Query(ctx, `UPDATE record_identities SET valid_to = now(), closed_reason = 'merged'
		WHERE record_id = $1 AND valid_to IS NULL RETURNING namespace, value, verified, verified_at, properties, source, valid_from`, from.ID)
	if err != nil {
		return res, err
	}
	type moved struct {
		namespace, value string
		verified         bool
		verifiedAt       *time.Time
		properties       map[string]any
		source           string
		validFrom        time.Time
	}
	ids, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (moved, error) {
		var m moved
		err := row.Scan(&m.namespace, &m.value, &m.verified, &m.verifiedAt, &m.properties, &m.source, &m.validFrom)
		return m, err
	})
	if err != nil {
		return res, err
	}
	for _, m := range ids {
		tag, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, properties, source, valid_from, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (record_id, namespace, value) WHERE valid_to IS NULL
			DO UPDATE SET verified = record_identities.verified OR EXCLUDED.verified,
				verified_at = COALESCE(record_identities.verified_at, EXCLUDED.verified_at),
				valid_from = LEAST(record_identities.valid_from, EXCLUDED.valid_from)`,
			a.WorkspaceID, into.ID, m.namespace, m.value, m.verified, m.verifiedAt, m.properties, m.source, m.validFrom, nullIfEmpty(a.ActorID))
		if err != nil {
			return res, claimError(err, m.namespace)
		}
		res.Identities += int(tag.RowsAffected())
	}

	// Attributes: into keeps its own unless from's came from a source that
	// outranks it; lists such as companies are joined.
	t := c.recordType(into.Type)
	if into.Attributes == nil {
		into.Attributes = map[string]any{}
	}
	if into.AttributeSources == nil {
		into.AttributeSources = map[string]string{}
	}
	keys := make([]string, 0, len(from.Attributes))
	for k := range from.Attributes {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		f := t.field(key)
		if f == nil {
			continue
		}
		next, src := from.Attributes[key], from.AttributeSources[key]
		prev, had := into.Attributes[key]
		switch {
		case f.Multiple && had:
			list, _ := prev.([]any)
			for _, v := range asList(next) {
				if v != any(from.ID) && v != any(into.ID) && !slices.ContainsFunc(list, func(x any) bool { return sameJSON(x, v) }) {
					list = append(list, v)
				}
			}
			if len(list) == len(asList(prev)) {
				continue
			}
			next, src = list, into.AttributeSources[key]
		case had && sourceRank(f, src) >= sourceRank(f, into.AttributeSources[key]):
			continue
		}
		into.Attributes[key], into.AttributeSources[key] = next, src
		if err := attributeChanged(ctx, tx, a, into.ID, key, prev, next, had, src); err != nil {
			return res, err
		}
		if f.ValueType == "record_reference" {
			if err := syncReferences(ctx, tx, a, into.ID, key, next); err != nil {
				return res, err
			}
		}
		res.Attributes++
	}
	if into.Name == "" && from.Name != "" {
		if err := attributeChanged(ctx, tx, a, into.ID, "name", into.Name, from.Name, true, "manual"); err != nil {
			return res, err
		}
		into.Name = from.Name
	}

	// Links: records that point at from through a field now point at into,
	// and from's other links move to into.
	rows, err = tx.Query(ctx, `SELECT DISTINCT from_id::text, field_key FROM record_relationships
		WHERE to_id = $1 AND valid_to IS NULL AND field_key IS NOT NULL AND from_id <> $2`, from.ID, into.ID)
	if err != nil {
		return res, err
	}
	type pointer struct{ id, field string }
	pointers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pointer, error) {
		var p pointer
		err := row.Scan(&p.id, &p.field)
		return p, err
	})
	if err != nil {
		return res, err
	}
	for _, p := range pointers {
		if err := repoint(ctx, tx, a, p.id, p.field, from.ID, into.ID); err != nil {
			return res, err
		}
		res.Relationships++
	}
	tag, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, properties, valid_from, created_by)
		SELECT workspace_id, CASE WHEN from_id = $1 THEN $2 ELSE from_id END, CASE WHEN to_id = $1 THEN $2 ELSE to_id END, kind, properties, valid_from, $3
		FROM record_relationships
		WHERE (from_id = $1 OR to_id = $1) AND valid_to IS NULL AND field_key IS NULL
			AND CASE WHEN from_id = $1 THEN to_id ELSE from_id END <> $2
		ON CONFLICT DO NOTHING`, from.ID, into.ID, nullIfEmpty(a.ActorID))
	if err != nil {
		return res, err
	}
	res.Relationships += int(tag.RowsAffected())
	if _, err := tx.Exec(ctx, `UPDATE record_relationships SET valid_to = now() WHERE (from_id = $1 OR to_id = $1) AND valid_to IS NULL`, from.ID); err != nil {
		return res, err
	}

	// Events, consents and everything Outflare sent or heard from the
	// person follow them.
	follow, err := customers.Follow(ctx, tx, a.WorkspaceID, from.ID, into.ID)
	if err != nil {
		return res, err
	}
	res.Events, res.Consents, res.Subscriptions = follow["customer_events"], follow["consents"], follow["payment_subscriptions"]
	res.Activity = follow["leads"] + follow["messages"] + follow["inbound_messages"] + follow["meetings"] + follow["deals"] + follow["emails"] + follow["email_broadcast_recipients"] + follow["journey_runs"]

	if _, err := tx.Exec(ctx, `UPDATE records SET name = $2, attributes = $3, attribute_sources = $4, version = version + 1, updated_at = now() WHERE id = $1`,
		into.ID, into.Name, into.Attributes, into.AttributeSources); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET merged_into = $2, archived_at = COALESCE(archived_at, now()), version = version + 1, updated_at = now() WHERE id = $1`,
		from.ID, into.ID); err != nil {
		return res, err
	}
	// Earlier merges into from now lead to into.
	if _, err := tx.Exec(ctx, `UPDATE records SET merged_into = $2 WHERE merged_into = $1`, from.ID, into.ID); err != nil {
		return res, err
	}
	// Billing fields are worked out again from both people's payments, not
	// carried over by precedence.
	if res.Subscriptions > 0 || res.Events > 0 {
		if err := recomputeBilling(ctx, tx, a, c, a.WorkspaceID, into.ID); err != nil {
			return res, err
		}
	}
	// So are first and last touch, from both people's visits.
	if follow["site_sessions"] > 0 {
		if err := recomputeTouches(ctx, tx, a, c, into.ID); err != nil {
			return res, err
		}
	}

	// The pair is settled; from's other pairs are asked again about into.
	if _, err := tx.Exec(ctx, `UPDATE record_merge_candidates SET status = 'merged', decided_by = $2, decided_at = now()
		WHERE (record_a = $1 OR record_b = $1) AND status IN ('open', 'blocked')`, from.ID, nullIfEmpty(a.ActorID)); err != nil {
		return res, err
	}
	rows, err = tx.Query(ctx, `SELECT namespace, value FROM record_identities WHERE record_id = $1 AND valid_to IS NULL AND namespace = ANY($2)`, into.ID, duplicateNamespaces)
	if err != nil {
		return res, err
	}
	type nv struct{ namespace, value string }
	again, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (nv, error) {
		var v nv
		err := row.Scan(&v.namespace, &v.value)
		return v, err
	})
	if err != nil {
		return res, err
	}
	for _, v := range again {
		if _, err := flagDuplicates(ctx, tx, a.WorkspaceID, into.ID, v.namespace, v.value); err != nil {
			return res, err
		}
	}

	summary, _ := json.Marshal(res)
	if _, err := tx.Exec(ctx, `INSERT INTO record_merges (workspace_id, into_id, from_id, moved, merged_by, request_id) VALUES ($1, $2, $3, $4, $5, $6)`,
		a.WorkspaceID, into.ID, from.ID, string(summary), nullIfEmpty(a.ActorID), RequestID(ctx)); err != nil {
		return res, err
	}
	return res, record(ctx, tx, &a.principal, a.WorkspaceID, "record.merged", "record", into.ID, map[string]any{
		"type": into.Type, "from_id": from.ID, "identities": res.Identities, "attributes": res.Attributes,
		"relationships": res.Relationships, "events": res.Events, "consents": res.Consents,
		"subscriptions": res.Subscriptions, "activity": res.Activity,
	})
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	if v == nil {
		return nil
	}
	return []any{v}
}

// repoint changes a record's reference field from one record to another.
func repoint(ctx context.Context, tx pgx.Tx, a *access, id, field, from, to string) error {
	var attrs map[string]any
	if err := tx.QueryRow(ctx, `SELECT attributes FROM records WHERE id = $1 FOR UPDATE`, id).Scan(&attrs); err != nil {
		return err
	}
	prev, had := attrs[field]
	var next any
	switch v := prev.(type) {
	case string:
		next = to
	case []any:
		list := []any{}
		for _, x := range v {
			if x == any(from) {
				x = to
			}
			if x != any(id) && !slices.Contains(list, x) {
				list = append(list, x)
			}
		}
		next = list
	default:
		return nil
	}
	attrs[field] = next
	if err := attributeChanged(ctx, tx, a, id, field, prev, next, had, "manual"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, version = version + 1, updated_at = now() WHERE id = $1`, id, attrs); err != nil {
		return err
	}
	return syncReferences(ctx, tx, a, id, field, next)
}

// ---- Handlers ----

type mergeInput struct {
	From string `json:"from"`
	// The version of the record kept, as read.
	Version *int `json:"version"`
}

func (s *Server) mergeRecord(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req mergeInput
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(req.From) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is the id of the record to merge into this one")
		return
	}
	if req.Version == nil && agentWrite(r) {
		writeError(w, r, http.StatusBadRequest, "version_required", versionRequired)
		return
	}
	var view recordView
	var moved mergeResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		into := r.PathValue("record")
		if req.Version != nil {
			cur, err := loadRecord(ctx, tx, a, into)
			if err != nil {
				return err
			}
			if cur.Version != *req.Version {
				return recordConflict{"record_changed", fmt.Sprintf("the record changed since version %d (it is at %d): read both records again, check they are still the same person, and send version %d", *req.Version, cur.Version, cur.Version)}
			}
		}
		if _, err := loadRecord(ctx, tx, a, req.From); errors.Is(err, errNotFound) {
			return errInvalid("from is not a record in this workspace")
		} else if err != nil {
			return err
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
		if err != nil {
			return err
		}
		if moved, err = mergeRecords(ctx, tx, a, c, into, req.From); err != nil {
			return err
		}
		view, err = loadRecord(ctx, tx, a, into)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": view, "merged_from": req.From, "moved": moved})
}

type duplicateView struct {
	ID         string         `json:"id"`
	RecordType string         `json:"record_type"`
	Reason     string         `json:"reason"`
	Evidence   map[string]any `json:"evidence"`
	Status     string         `json:"status"`
	Mergeable  bool           `json:"mergeable"`
	Records    []recordView   `json:"records"`
	DecidedBy  *string        `json:"decided_by"`
	DecidedAt  *time.Time     `json:"decided_at"`
	CreatedAt  time.Time      `json:"created_at"`
}

var duplicateStatuses = []string{"open", "blocked", "merged", "linked", "dismissed"}

func (s *Server) listDuplicates(w http.ResponseWriter, r *http.Request) {
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
	statuses := []string{"open", "blocked"}
	if v := q.Get("status"); v == "all" {
		statuses = duplicateStatuses
	} else if v != "" {
		statuses = strings.Split(v, ",")
		for _, st := range statuses {
			if !slices.Contains(duplicateStatuses, st) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "status is a comma-separated list of open, blocked, merged, linked and dismissed, or all")
				return
			}
		}
	}
	args := []any{a.WorkspaceID, statuses, beforeAt, beforeID}
	where := `c.workspace_id = $1 AND c.status = ANY($2) AND (c.created_at, c.id::text) < ($3, $4)`
	if v := q.Get("type"); v != "" {
		args = append(args, v)
		where += ` AND c.record_type = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("record"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record is a record's id")
			return
		}
		args = append(args, v)
		n := strconv.Itoa(len(args))
		where += ` AND (c.record_a = $` + n + ` OR c.record_b = $` + n + `)`
	}
	var list []duplicateView
	var next *string
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		// Brand-limited callers see a pair only when they see both records,
		// so the page is filled after that check.
		rows, err := tx.Query(ctx, `SELECT c.id::text, c.record_type, c.reason, c.evidence, c.status, c.record_a::text, c.record_b::text,
				c.decided_by::text, c.decided_at, c.created_at
			FROM record_merge_candidates c WHERE `+where+` ORDER BY c.created_at DESC, c.id::text DESC LIMIT `+strconv.Itoa(limit*4), args...)
		if err != nil {
			return err
		}
		type row struct {
			duplicateView
			a, b string
		}
		found, err := pgx.CollectRows(rows, func(rw pgx.CollectableRow) (row, error) {
			var v row
			err := rw.Scan(&v.ID, &v.RecordType, &v.Reason, &v.Evidence, &v.Status, &v.a, &v.b, &v.DecidedBy, &v.DecidedAt, &v.CreatedAt)
			return v, err
		})
		if err != nil {
			return err
		}
		for _, v := range found {
			if len(list) == limit {
				break
			}
			ra, errA := loadRecord(ctx, tx, a, v.a)
			rb, errB := loadRecord(ctx, tx, a, v.b)
			if errors.Is(errA, errNotFound) || errors.Is(errB, errNotFound) {
				continue
			}
			if errA != nil {
				return errA
			}
			if errB != nil {
				return errB
			}
			v.Records = []recordView{ra, rb}
			v.Mergeable = v.Status == "open" && ra.MergedInto == nil && rb.MergedInto == nil
			list = append(list, v.duplicateView)
		}
		if len(list) == limit {
			last := list[len(list)-1]
			c := last.CreatedAt.Format(time.RFC3339Nano) + "|" + last.ID
			next = &c
		}
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	if list == nil {
		list = []duplicateView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// decideDuplicate answers "same human?" without merging: dismiss (not the
// same) or link (the same human with two accounts).
func (s *Server) decideDuplicate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	link := strings.HasSuffix(r.URL.Path, "/link")
	id := r.PathValue("candidate")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	var view duplicateView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ra, rb, status string
		err := tx.QueryRow(ctx, `SELECT record_a::text, record_b::text, status FROM record_merge_candidates WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
			id, a.WorkspaceID).Scan(&ra, &rb, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		for _, rec := range []string{ra, rb} {
			v, err := loadRecord(ctx, tx, a, rec)
			if err != nil {
				return err
			}
			if !a.writes(v.BrandID) {
				return errSharedRecord
			}
		}
		if status != "open" && status != "blocked" {
			return recordConflict{"conflict", "this pair was already " + status}
		}
		next, action := "dismissed", "duplicate.dismissed"
		if link {
			next, action = "linked", "duplicate.linked"
			if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, created_by) VALUES ($1, $2, $3, 'same_human', $4)
				ON CONFLICT DO NOTHING`, a.WorkspaceID, ra, rb, nullIfEmpty(a.ActorID)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE record_merge_candidates SET status = $2, decided_by = $3, decided_at = now() WHERE id = $1`,
			id, next, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "record", ra, map[string]any{"record_b": rb, "was": status}); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text, record_type, reason, evidence, status, decided_by::text, decided_at, created_at FROM record_merge_candidates WHERE id = $1`, id).
			Scan(&view.ID, &view.RecordType, &view.Reason, &view.Evidence, &view.Status, &view.DecidedBy, &view.DecidedAt, &view.CreatedAt); err != nil {
			return err
		}
		for _, rec := range []string{ra, rb} {
			v, err := loadRecord(ctx, tx, a, rec)
			if err != nil {
				return err
			}
			view.Records = append(view.Records, v)
		}
		return nil
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}
