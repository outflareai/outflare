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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The 7-day recovery window (plans/customer-data-platform/README.md, the
// owner's decision of 2026-10-28). Deleting a record, a field value, an
// identity, a link or a product event (payments are product events) hides
// it everywhere at once and makes a deletion: what one person's action
// removed, restored together. Recently deleted lists a brand's deletions
// for 7 days; then RunRecoveryWindow purges them for good.
//
// How each kind is hidden (migration 0328 has the reasons):
//   - a record keeps its row with deleted_at and deletion_id, and is
//     archived too, so every path that leaves out archived records leaves
//     it out; the record API, lists and Query check deleted_at. Its
//     identities, links and events move to deleted_rows, and its active
//     journey runs end (exit_reason deleted) so nothing more is sent.
//   - identities, links and events move out of their tables into
//     deleted_rows, so nothing can read them by mistake.
//   - a field value leaves the record's attributes and waits in
//     deleted_rows.
//
// Restoring puts rows back with plain inserts and updates: no job, rule,
// journey, webhook or email hears of it, so nothing fires or is sent
// again. A row that can't come back (its unique value was taken since, or
// what it points at is gone) stays in the deletion and the answer says
// why; the rest comes back. Erasure (DELETE .../records/{record}) is
// separate: permanent at once, and it deletes a record's deletions too.

const (
	// recoveryDays is how long a deletion can be restored.
	recoveryDays = 7
	// purgeBatch is how many deletions one purge round takes.
	purgeBatch = 100
)

// Deletion kinds.
const (
	deletedRecord       = "record"
	deletedFieldValue   = "field_value"
	deletedIdentity     = "identity"
	deletedRelationship = "relationship"
	deletedEvent        = "event"
)

// fieldValueRows is the source_table of a field value in deleted_rows.
const fieldValueRows = "attributes"

// deletedTables are the tables rows move out of, in the order a restore
// puts them back: an event before what hangs off it.
var deletedTables = []string{
	"record_identities", "record_relationships", "customer_events",
	"attribution_conversions", "attribution_credits", "ad_conversion_sends",
}

// rowID is each table's id, as text, over alias t.
var rowID = map[string]string{
	"record_identities":       "t.id::text",
	"record_relationships":    "t.id::text",
	"customer_events":         "t.id::text",
	"ad_conversion_sends":     "t.id::text",
	"attribution_conversions": "t.event_id::text",
	"attribution_credits":     "t.event_id::text || ':' || t.model || ':' || t.position::text",
}

func (s *Server) routesRecoveryWindow(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("POST /v1/brands/{brand}/records/{record}/delete", s.onBrand(manage, s.softDeleteRecord))
	mux.HandleFunc("POST /v1/brands/{brand}/records/{record}/fields/{field}/delete", s.onBrand(manage, s.deleteFieldValue))
	mux.HandleFunc("POST /v1/brands/{brand}/records/{record}/identities/{identity}/delete", s.onBrand(manage, s.deleteIdentity))
	mux.HandleFunc("POST /v1/brands/{brand}/records/{record}/relationships/{relationship}/delete", s.onBrand(manage, s.deleteRelationship))
	mux.HandleFunc("POST /v1/brands/{brand}/customer-events/{event}/delete", s.onBrand(manage, s.deleteCustomerEvent))
	mux.HandleFunc("GET /v1/brands/{brand}/recently-deleted", s.onBrand(read, s.listRecentlyDeleted))
	mux.HandleFunc("GET /v1/brands/{brand}/recently-deleted/{deletion}", s.onBrand(read, s.getDeletion))
	mux.HandleFunc("POST /v1/brands/{brand}/recently-deleted/{deletion}/restore", s.onBrand(manage, s.restoreDeletion))
}

// ---- Views ----

type deletedBy struct {
	ActorType string  `json:"actor_type"`
	UserID    *string `json:"user_id"`
	TokenID   *string `json:"token_id"`
	// The member's name; null for a token or someone who left.
	Name *string `json:"name"`
}

type notRestored struct {
	Table  string `json:"table"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type deletionView struct {
	ID         string  `json:"id"`
	BrandID    string  `json:"brand_id"`
	Kind       string  `json:"kind"`
	RecordID   *string `json:"record_id"`
	RecordType *string `json:"record_type"`
	// What was deleted, as people know it: the record's name, the field's
	// key, the identity's value, the link's kind or the event's name.
	Label       string         `json:"label"`
	Target      string         `json:"target"`
	Counts      map[string]int `json:"counts"`
	DeletedBy   deletedBy      `json:"deleted_by"`
	DeletedAt   time.Time      `json:"deleted_at"`
	PurgeAfter  time.Time      `json:"purge_after"`
	RestoredAt  *time.Time     `json:"restored_at"`
	NotRestored []notRestored  `json:"not_restored"`
}

const deletionColumns = `d.id::text, d.brand_id::text, d.kind, d.record_id::text, r.record_type,
	COALESCE(CASE d.kind
		WHEN 'record' THEN r.name
		WHEN 'field_value' THEN d.target
		WHEN 'identity' THEN (SELECT x.data->>'value' FROM deleted_rows x WHERE x.deletion_id = d.id AND x.source_table = 'record_identities' LIMIT 1)
		WHEN 'relationship' THEN (SELECT x.data->>'kind' FROM deleted_rows x WHERE x.deletion_id = d.id AND x.source_table = 'record_relationships' LIMIT 1)
		WHEN 'event' THEN (SELECT x.data->>'name' FROM deleted_rows x WHERE x.deletion_id = d.id AND x.source_table = 'customer_events' LIMIT 1)
	END, ''),
	d.target, d.counts, d.actor_type, d.user_id::text, d.token_id::text, u.name, d.deleted_at, d.purge_after, d.restored_at, d.not_restored`

const deletionFrom = ` FROM deletions d LEFT JOIN records r ON r.id = d.record_id LEFT JOIN users u ON u.id = d.user_id`

func scanDeletion(row pgx.CollectableRow) (deletionView, error) {
	var v deletionView
	err := row.Scan(&v.ID, &v.BrandID, &v.Kind, &v.RecordID, &v.RecordType, &v.Label, &v.Target, &v.Counts,
		&v.DeletedBy.ActorType, &v.DeletedBy.UserID, &v.DeletedBy.TokenID, &v.DeletedBy.Name, &v.DeletedAt, &v.PurgeAfter, &v.RestoredAt, &v.NotRestored)
	if v.Counts == nil {
		v.Counts = map[string]int{}
	}
	if v.NotRestored == nil {
		v.NotRestored = []notRestored{}
	}
	return v, err
}

func loadDeletion(ctx context.Context, q pgx.Tx, a *access, id string) (deletionView, error) {
	if !validUUID(id) {
		return deletionView{}, errNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+deletionColumns+deletionFrom+` WHERE d.id = $1 AND d.brand_id = $2 AND d.workspace_id = $3`,
		id, a.Brand, a.WorkspaceID)
	if err != nil {
		return deletionView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanDeletion)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

// ---- Deleting ----

// deletion is one deletion being made.
type deletion struct {
	id, ws, brand string
	counts        map[string]int
}

// startDeletion makes the deletion one action's rows go into.
func startDeletion(ctx context.Context, tx pgx.Tx, a *access, kind string, recordID *string, target string) (*deletion, error) {
	d := &deletion{ws: a.WorkspaceID, brand: a.Brand, counts: map[string]int{}}
	err := tx.QueryRow(ctx, `INSERT INTO deletions (workspace_id, brand_id, kind, record_id, target, actor_id, actor_type, user_id, token_id, request_id, purge_after)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now() + make_interval(days => $11)) RETURNING id::text`,
		a.WorkspaceID, a.Brand, kind, recordID, target, nullIfEmpty(a.ActorID), a.ActorType, nullIfEmpty(a.personID()), nullIfEmpty(a.TokenID),
		nullIfEmpty(RequestID(ctx)), recoveryDays).Scan(&d.id)
	return d, err
}

// move moves the rows of table that match where (over alias t, with args
// from $1) into deleted_rows, with record (SQL over the deleted row, alias
// gone) as the record each is about, and returns how many moved.
func (d *deletion) move(ctx context.Context, tx pgx.Tx, table, record, where string, args ...any) (int, error) {
	n := len(args)
	id := strings.ReplaceAll(rowID[table], "t.", "gone.")
	sql := `WITH gone AS (DELETE FROM ` + pgx.Identifier{table}.Sanitize() + ` t WHERE ` + where + ` RETURNING t.*)
		INSERT INTO deleted_rows (deletion_id, workspace_id, brand_id, record_id, source_table, row_id, data)
		SELECT $` + strconv.Itoa(n+1) + `, $` + strconv.Itoa(n+2) + `, $` + strconv.Itoa(n+3) + `, ` + record + `, '` + table + `', ` + id + `, to_jsonb(gone) FROM gone`
	tag, err := tx.Exec(ctx, sql, append(args, d.id, d.ws, d.brand)...)
	if err != nil {
		return 0, err
	}
	if k := int(tag.RowsAffected()); k > 0 {
		d.counts[table] += k
	}
	return int(tag.RowsAffected()), nil
}

// moveEvents moves product events, and what hangs off each (its ad
// conversion sends and attribution, which would otherwise be deleted with
// it), into the deletion. Inbox rows that point at an event lose the link
// while it is deleted; the deletion keeps it to put back.
func (d *deletion) moveEvents(ctx context.Context, tx pgx.Tx, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := d.move(ctx, tx, "attribution_credits", `(SELECT c.record_id FROM attribution_conversions c WHERE c.event_id = gone.event_id)`,
		`t.workspace_id = $1 AND t.event_id = ANY($2::uuid[])`, d.ws, ids); err != nil {
		return 0, err
	}
	for table, col := range map[string]string{"attribution_conversions": "event_id", "ad_conversion_sends": "event_row_id"} {
		if _, err := d.move(ctx, tx, table, `gone.record_id`, `t.workspace_id = $1 AND t.`+col+` = ANY($2::uuid[])`, d.ws, ids); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deleted_rows (deletion_id, workspace_id, brand_id, record_id, source_table, row_id, data)
		SELECT $1, $2, $4, e.record_id, 'event_inbox', e.id::text, jsonb_build_object('id', e.id, 'customer_event_id', e.customer_event_id)
		FROM event_inbox e WHERE e.workspace_id = $2 AND e.customer_event_id = ANY($3::uuid[])`, d.id, d.ws, ids, d.brand); err != nil {
		return 0, err
	}
	return d.move(ctx, tx, "customer_events", `gone.record_id`, `t.workspace_id = $1 AND t.id = ANY($2::uuid[])`, d.ws, ids)
}

// finish saves what the deletion holds and counts it.
func (d *deletion) finish(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE deletions SET counts = $2 WHERE id = $1`, d.id, d.counts)
	return err
}

// recordDeleted records a deletion in the audit log (also its product
// event and, for a record, its webhook event).
func recordDeleted(ctx context.Context, tx pgx.Tx, a *access, action, targetType, targetID string, d *deletion, data map[string]any) error {
	data["deletion_id"] = d.id
	data["brand_id"] = a.Brand
	data["counts"] = d.counts
	props := map[string]any{"deletion_id": d.id}
	for _, k := range []string{"kind", "type", "record_id", "field", "namespace", "name"} {
		if v, ok := data[k]; ok {
			props[k] = v
		}
	}
	return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, targetType, targetID, data, props)
}

// liveBrandRecord locks a live record of the route's brand.
func liveBrandRecord(ctx context.Context, tx pgx.Tx, a *access, id string) (recordView, error) {
	if !validUUID(id) {
		return recordView{}, errNotFound
	}
	v, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM records r
		WHERE r.id = $1 AND r.workspace_id = $2 AND r.brand_id = $3 AND r.deleted_at IS NULL FOR UPDATE`, id, a.WorkspaceID, a.Brand))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	if v.MergedInto != nil {
		return v, recordConflict{"conflict", "this record was merged into " + *v.MergedInto + "; change that one instead"}
	}
	return v, nil
}

// deleteBody is the optional body of a delete.
type deleteBody struct {
	// The record's version, as read: the delete fails if it changed.
	Version *int `json:"version"`
}

func readDeleteBody(w http.ResponseWriter, r *http.Request) (deleteBody, bool) {
	var body deleteBody
	if !decodeOptional(w, r, &body) {
		return body, false
	}
	if body.Version == nil && agentWrite(r) {
		writeError(w, r, http.StatusBadRequest, "version_required", versionRequired)
		return body, false
	}
	return body, true
}

func checkVersion(rec recordView, version *int) error {
	if version != nil && *version != rec.Version {
		return recordConflict{"record_changed", fmt.Sprintf("the record changed since version %d (it is at %d): read it again before deleting", *version, rec.Version)}
	}
	return nil
}

// writeDeletion answers a delete with its deletion.
func (s *Server) writeDeletion(w http.ResponseWriter, r *http.Request, err error, id string, what string) {
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", what+" not found")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	var v deletionView
	err = s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		v, err = loadDeletion(r.Context(), tx, accessFrom(r.Context()), id)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// softDeleteRecord deletes a record with its identities, links and events,
// restorable for 7 days.
func (s *Server) softDeleteRecord(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	body, ok := readDeleteBody(w, r)
	if !ok {
		return
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := liveBrandRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if err := checkVersion(rec, body.Version); err != nil {
			return err
		}
		d, err := startDeletion(ctx, tx, a, deletedRecord, &rec.ID, rec.ID)
		if err != nil {
			return err
		}
		id = d.id
		if _, err := d.move(ctx, tx, "record_identities", "gone.record_id", `t.workspace_id = $1 AND t.record_id = $2`, a.WorkspaceID, rec.ID); err != nil {
			return err
		}
		if _, err := d.move(ctx, tx, "record_relationships", "$2::uuid", `t.workspace_id = $1 AND (t.from_id = $2 OR t.to_id = $2)`, a.WorkspaceID, rec.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM customer_events WHERE workspace_id = $1 AND record_id = $2`, a.WorkspaceID, rec.ID)
		if err != nil {
			return err
		}
		events, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if _, err := d.moveEvents(ctx, tx, events); err != nil {
			return err
		}
		// Nothing more goes out to them: their journeys end, and stay
		// ended if they come back.
		tag, err := tx.Exec(ctx, `UPDATE journey_runs SET status = 'exited', exit_reason = 'deleted', finished_at = now(), next_at = NULL, updated_at = now()
			WHERE workspace_id = $1 AND record_id = $2 AND status = 'active'`, a.WorkspaceID, rec.ID)
		if err != nil {
			return err
		}
		if n := int(tag.RowsAffected()); n > 0 {
			d.counts["journey_runs_ended"] = n
		}
		// Archived too, unless it was: every path that leaves archived
		// records out leaves it out. Restoring undoes only what this did.
		if _, err := tx.Exec(ctx, `UPDATE records SET deleted_at = now(), deletion_id = $2, archived_at = COALESCE(archived_at, now()),
			version = version + 1, updated_at = now() WHERE id = $1`, rec.ID, d.id); err != nil {
			return err
		}
		d.counts["records"] = 1
		if err := d.finish(ctx, tx); err != nil {
			return err
		}
		metrics.RecoveryDeleted(deletedRecord)
		return recordDeleted(ctx, tx, a, "record.soft_deleted", "record", rec.ID, d, map[string]any{"kind": deletedRecord, "type": rec.Type, "record_id": rec.ID})
	})
	s.writeDeletion(w, r, err, id, "record")
}

// deleteFieldValue deletes one field's value from a record.
func (s *Server) deleteFieldValue(w http.ResponseWriter, r *http.Request) {
	{
		a := accessFrom(r.Context())
		ctx := r.Context()
		body, ok := readDeleteBody(w, r)
		if !ok {
			return
		}
		key := r.PathValue("field")
		var id string
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			rec, err := liveBrandRecord(ctx, tx, a, r.PathValue("record"))
			if err != nil {
				return err
			}
			if err := checkVersion(rec, body.Version); err != nil {
				return err
			}
			value, had := rec.Attributes[key]
			if !had {
				return errNotFound
			}
			c, err := loadCatalog(ctx, tx, a.WorkspaceID, false)
			if err != nil {
				return err
			}
			if t := c.recordType(rec.Type); t != nil {
				if f := t.field(key); f != nil && f.WritePolicy == "computed" {
					return errInvalid(fmt.Sprintf("field %s is computed by Outflare from other data; delete that data instead", key))
				}
			}
			d, err := startDeletion(ctx, tx, a, deletedFieldValue, &rec.ID, key)
			if err != nil {
				return err
			}
			id = d.id
			source := rec.AttributeSources[key]
			stored, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO deleted_rows (deletion_id, workspace_id, brand_id, record_id, source_table, row_id, data)
				VALUES ($1, $2, $8, $3, $4, $5, jsonb_build_object('key', $5::text, 'value', $6::jsonb, 'source', $7::text))`,
				d.id, a.WorkspaceID, rec.ID, fieldValueRows, key, string(stored), source, a.Brand); err != nil {
				return err
			}
			d.counts[fieldValueRows] = 1
			// A reference field's value is also its links.
			if _, err := d.move(ctx, tx, "record_relationships", "gone.from_id", `t.workspace_id = $1 AND t.from_id = $2 AND t.field_key = $3 AND t.valid_to IS NULL`,
				a.WorkspaceID, rec.ID, key); err != nil {
				return err
			}
			// Straight to the row and its history, not through the
			// change hooks: deleting bad data doesn't fire rules.
			if err := setFieldValue(ctx, tx, a, rec.ID, key, value, nil, ""); err != nil {
				return err
			}
			if err := d.finish(ctx, tx); err != nil {
				return err
			}
			metrics.RecoveryDeleted(deletedFieldValue)
			return recordDeleted(ctx, tx, a, "field_value.deleted", "record", rec.ID, d, map[string]any{"kind": deletedFieldValue, "type": rec.Type, "record_id": rec.ID, "field": key})
		})
		s.writeDeletion(w, r, err, id, "field value")
	}
}

// setFieldValue sets or (next nil) removes one field of a record, with its
// source, and records the change in the field's history. It tells no rule
// or journey: deleting and restoring never fire them.
func setFieldValue(ctx context.Context, tx pgx.Tx, a *access, recordID, key string, prev, next any, source string) error {
	var err error
	if next == nil {
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = attributes - $2, attribute_sources = attribute_sources - $2,
			version = version + 1, updated_at = now() WHERE id = $1`, recordID, key)
	} else {
		b, merr := json.Marshal(next)
		if merr != nil {
			return merr
		}
		_, err = tx.Exec(ctx, `UPDATE records SET attributes = attributes || jsonb_build_object($2::text, $3::jsonb),
			attribute_sources = attribute_sources || jsonb_build_object($2::text, $4::text), version = version + 1, updated_at = now() WHERE id = $1`,
			recordID, key, string(b), source)
	}
	if err != nil {
		return err
	}
	asJSON := func(v any) *string {
		if v == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		s := string(b)
		return &s
	}
	src := "manual"
	if next != nil && source != "" {
		src = source
	}
	_, err = tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source, actor_id, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, a.WorkspaceID, recordID, key, asJSON(prev), asJSON(next), src, nullIfEmpty(a.ActorID), RequestID(ctx))
	return err
}

// deleteIdentity deletes one of a record's identities, open or closed.
func (s *Server) deleteIdentity(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	body, ok := readDeleteBody(w, r)
	if !ok {
		return
	}
	ident := r.PathValue("identity")
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := liveBrandRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if err := checkVersion(rec, body.Version); err != nil {
			return err
		}
		if !validUUID(ident) {
			return errNotFound
		}
		var namespace string
		if err := tx.QueryRow(ctx, `SELECT namespace FROM record_identities WHERE id = $1 AND record_id = $2 AND workspace_id = $3`,
			ident, rec.ID, a.WorkspaceID).Scan(&namespace); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		d, err := startDeletion(ctx, tx, a, deletedIdentity, &rec.ID, ident)
		if err != nil {
			return err
		}
		id = d.id
		if _, err := d.move(ctx, tx, "record_identities", "gone.record_id", `t.workspace_id = $1 AND t.id = $2`, a.WorkspaceID, ident); err != nil {
			return err
		}
		if err := touch(ctx, tx, rec.ID); err != nil {
			return err
		}
		if err := d.finish(ctx, tx); err != nil {
			return err
		}
		metrics.RecoveryDeleted(deletedIdentity)
		return recordDeleted(ctx, tx, a, "identity.deleted", "identity", ident, d, map[string]any{"kind": deletedIdentity, "type": rec.Type, "record_id": rec.ID, "namespace": namespace})
	})
	s.writeDeletion(w, r, err, id, "identity")
}

// deleteRelationship deletes one of a record's links, either way round.
func (s *Server) deleteRelationship(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	body, ok := readDeleteBody(w, r)
	if !ok {
		return
	}
	link := r.PathValue("relationship")
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := liveBrandRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		if err := checkVersion(rec, body.Version); err != nil {
			return err
		}
		if !validUUID(link) {
			return errNotFound
		}
		var kind string
		var field *string
		var open bool
		if err := tx.QueryRow(ctx, `SELECT kind, field_key, valid_to IS NULL FROM record_relationships WHERE id = $1 AND workspace_id = $2 AND (from_id = $3 OR to_id = $3)`,
			link, a.WorkspaceID, rec.ID).Scan(&kind, &field, &open); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if field != nil && open {
			return errInvalid(fmt.Sprintf("this link is the value of the field %s; delete that field's value instead (POST .../records/{record}/fields/%s/delete)", *field, *field))
		}
		d, err := startDeletion(ctx, tx, a, deletedRelationship, &rec.ID, link)
		if err != nil {
			return err
		}
		id = d.id
		if _, err := d.move(ctx, tx, "record_relationships", "$3::uuid", `t.workspace_id = $1 AND t.id = $2`, a.WorkspaceID, link, rec.ID); err != nil {
			return err
		}
		if err := d.finish(ctx, tx); err != nil {
			return err
		}
		metrics.RecoveryDeleted(deletedRelationship)
		return recordDeleted(ctx, tx, a, "relationship.deleted", "relationship", link, d, map[string]any{"kind": deletedRelationship, "type": rec.Type, "record_id": rec.ID})
	})
	s.writeDeletion(w, r, err, id, "relationship")
}

// paymentEvents are the events a record's billing fields are worked out
// from; deleting or restoring one works them out again, quietly.
var paymentEvents = []string{evPaymentSucceeded, evPaymentRefunded}

// deleteCustomerEvent deletes one product event (a payment is one) of the
// brand. Reports and rule history that counted it change.
func (s *Server) deleteCustomerEvent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !decodeOptional(w, r, &struct{}{}) {
		return
	}
	ev := r.PathValue("event")
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(ev) {
			return errNotFound
		}
		var name string
		var rec *string
		if err := tx.QueryRow(ctx, `SELECT name, record_id::text FROM customer_events WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`,
			ev, a.WorkspaceID, a.Brand).Scan(&name, &rec); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		d, err := startDeletion(ctx, tx, a, deletedEvent, rec, ev)
		if err != nil {
			return err
		}
		id = d.id
		if _, err := d.moveEvents(ctx, tx, []string{ev}); err != nil {
			return err
		}
		if rec != nil && slices.Contains(paymentEvents, name) {
			if err := quietBilling(ctx, tx, a.WorkspaceID, *rec); err != nil {
				return err
			}
		}
		if err := d.finish(ctx, tx); err != nil {
			return err
		}
		metrics.RecoveryDeleted(deletedEvent)
		data := map[string]any{"kind": deletedEvent, "name": name}
		if rec != nil {
			data["record_id"] = *rec
		}
		return recordDeleted(ctx, tx, a, "customer_event.deleted", "customer_event", ev, d, data)
	})
	s.writeDeletion(w, r, err, id, "event")
}

// quietKey marks a context whose field changes tell no rule or journey.
type quietKey struct{}

// quietly is ctx for changes that must not fire rules or journeys: the
// fields a deletion or restore works out again.
func quietly(ctx context.Context) context.Context { return context.WithValue(ctx, quietKey{}, true) }

func isQuiet(ctx context.Context) bool { q, _ := ctx.Value(quietKey{}).(bool); return q }

// quietBilling works a record's billing fields out again after one of its
// payments was deleted or restored, without firing rules on the change.
func quietBilling(ctx context.Context, tx pgx.Tx, ws, recordID string) error {
	c, err := loadCatalog(ctx, tx, ws, false)
	if err != nil {
		return err
	}
	return recomputeBilling(quietly(ctx), tx, &access{principal: principal{ActorType: "system"}, WorkspaceID: ws}, c, ws, recordID)
}

// ---- Recently deleted ----

func (s *Server) listRecentlyDeleted(w http.ResponseWriter, r *http.Request) {
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
	where := []string{"d.brand_id = $1", "d.workspace_id = $2", "d.restored_at IS NULL", "d.purge_after > now()", "(d.deleted_at, d.id::text) < ($3, $4)"}
	args := []any{a.Brand, a.WorkspaceID, beforeAt, beforeID}
	if v := q.Get("kind"); v != "" {
		if !slices.Contains([]string{deletedRecord, deletedFieldValue, deletedIdentity, deletedRelationship, deletedEvent}, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is record, field_value, identity, relationship or event")
			return
		}
		args = append(args, v)
		where = append(where, "d.kind = $"+strconv.Itoa(len(args)))
	}
	if v := q.Get("record"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record is a record's id")
			return
		}
		args = append(args, v)
		where = append(where, "d.record_id = $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit)
	var list []deletionView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+deletionColumns+deletionFrom+` WHERE `+strings.Join(where, " AND ")+`
			ORDER BY d.deleted_at DESC, d.id::text DESC LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanDeletion)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	if list == nil {
		list = []deletionView{}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.DeletedAt.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) getDeletion(w http.ResponseWriter, r *http.Request) {
	var v deletionView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		v, err = loadDeletion(r.Context(), tx, accessFrom(r.Context()), r.PathValue("deletion"))
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "deletion not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- Restoring ----

// Why a row could not be restored.
const (
	// A unique value it holds (an email, a message id) is someone else's now.
	reasonTaken = "taken"
	// What it points at (the other record of a link) was erased or purged.
	reasonGone = "gone"
	// The field has a new value since; the deleted one would overwrite it.
	reasonChanged = "changed"
	// Its record is deleted: restore the record first.
	reasonRecordDeleted = "record_deleted"
)

var (
	errNothingToRestore = errors.New("nothing to restore")
	errDeletionExpired  = errors.New("deletion expired")
)

type restoreResult struct {
	deletionView
	// Rows that came back, by table.
	Restored map[string]int `json:"restored"`
	// restored, partly_restored or not_restored.
	Outcome string `json:"outcome"`
}

func (s *Server) restoreDeletion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !decodeOptional(w, r, &struct{}{}) {
		return
	}
	id := r.PathValue("deletion")
	var out restoreResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		var kind, target string
		var rec *string
		var restored *time.Time
		var expired bool
		err := tx.QueryRow(ctx, `SELECT kind, target, record_id::text, restored_at, purge_after <= now() FROM deletions
			WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 FOR UPDATE`, id, a.Brand, a.WorkspaceID).Scan(&kind, &target, &rec, &restored, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		switch {
		case err != nil:
			return err
		case restored != nil:
			return errNothingToRestore
		case expired:
			return errDeletionExpired
		}
		res, failed, err := restoreRows(ctx, tx, a, id, kind, rec)
		if err != nil {
			return err
		}
		total := 0
		for _, n := range res {
			total += n
		}
		if total == 0 && len(failed) == 0 {
			return errNothingToRestore
		}
		out.Restored, out.Outcome = res, "restored"
		switch {
		case total == 0:
			out.Outcome = "not_restored"
		case len(failed) > 0:
			out.Outcome = "partly_restored"
		}
		if failed == nil {
			failed = []notRestored{}
		}
		if _, err := tx.Exec(ctx, `UPDATE deletions SET not_restored = $2, restored_at = CASE WHEN $3 THEN now() END,
			restored_by = CASE WHEN $3 THEN $4::uuid END WHERE id = $1`, id, failed, len(failed) == 0, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		metrics.RecoveryRestored(kind, out.Outcome)
		data := map[string]any{"kind": kind, "brand_id": a.Brand, "restored": res, "not_restored": len(failed), "outcome": out.Outcome}
		props := map[string]any{"kind": kind, "result": out.Outcome, "not_restored": len(failed)}
		if rec != nil {
			data["record_id"], props["record_id"] = *rec, *rec
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "deletion.restored", "deletion", id, data, props); err != nil {
			return err
		}
		out.deletionView, err = loadDeletion(ctx, tx, a, id)
		return err
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "deletion not found: it was restored and is past its 7 days, purged, or erased")
		return
	case errors.Is(err, errNothingToRestore):
		writeError(w, r, http.StatusConflict, "nothing_to_restore", "this deletion was already restored; nothing of it is left to restore")
		return
	case errors.Is(err, errDeletionExpired):
		writeError(w, r, http.StatusGone, "deletion_expired", "this deletion's 7 days are over and it is being purged; it can't be restored")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// restoreRows puts back what deletion id still holds and returns what came
// back, by table, and what couldn't, with why. A row that can't come back
// stays in the deletion.
func restoreRows(ctx context.Context, tx pgx.Tx, a *access, id, kind string, rec *string) (map[string]int, []notRestored, error) {
	res := map[string]int{}
	var failed []notRestored
	if kind == deletedRecord && rec != nil {
		// The record first, so what hangs off it has somewhere to go. Its
		// archive is undone only when the deletion made it.
		tag, err := tx.Exec(ctx, `UPDATE records SET deleted_at = NULL, deletion_id = NULL,
			archived_at = CASE WHEN archived_at = deleted_at THEN NULL ELSE archived_at END, version = version + 1, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND deletion_id = $3`, *rec, a.WorkspaceID, id)
		if err != nil {
			return nil, nil, err
		}
		if tag.RowsAffected() > 0 {
			res["records"] = 1
		}
	}
	// A record still deleted by another deletion keeps what belongs to it
	// hidden: restore that first.
	recordLive := true
	if rec != nil {
		if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM records WHERE id = $1`, *rec).Scan(&recordLive); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
	}
	if kind != deletedRecord && !recordLive {
		rows, err := tx.Query(ctx, `SELECT source_table, row_id FROM deleted_rows WHERE deletion_id = $1 AND source_table <> 'event_inbox' ORDER BY id`, id)
		if err != nil {
			return nil, nil, err
		}
		left, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (notRestored, error) {
			n := notRestored{Reason: reasonRecordDeleted}
			return n, row.Scan(&n.Table, &n.ID)
		})
		return res, left, err
	}
	// A field value comes back only onto an empty field.
	var value struct {
		Key    string          `json:"key"`
		Value  json.RawMessage `json:"value"`
		Source string          `json:"source"`
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT data FROM deleted_rows WHERE deletion_id = $1 AND source_table = $2`, id, fieldValueRows).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, nil, err
	default:
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, nil, err
		}
		var has bool
		if err := tx.QueryRow(ctx, `SELECT attributes ? $2 FROM records WHERE id = $1 FOR UPDATE`, *rec, value.Key).Scan(&has); err != nil {
			return nil, nil, err
		}
		if has {
			failed = append(failed, notRestored{Table: fieldValueRows, ID: value.Key, Reason: reasonChanged})
			// Its links stay deleted with it.
			rows, err := tx.Query(ctx, `SELECT source_table, row_id FROM deleted_rows WHERE deletion_id = $1 AND source_table = 'record_relationships'`, id)
			if err != nil {
				return nil, nil, err
			}
			more, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (notRestored, error) {
				n := notRestored{Reason: reasonChanged}
				return n, row.Scan(&n.Table, &n.ID)
			})
			return res, append(failed, more...), err
		}
		var v any
		if err := json.Unmarshal(value.Value, &v); err != nil {
			return nil, nil, err
		}
		if err := setFieldValue(ctx, tx, a, *rec, value.Key, nil, v, value.Source); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM deleted_rows WHERE deletion_id = $1 AND source_table = $2`, id, fieldValueRows); err != nil {
			return nil, nil, err
		}
		res[fieldValueRows] = 1
	}
	payments := map[string]bool{}
	for _, table := range deletedTables {
		ids, left, err := restoreTable(ctx, tx, a.WorkspaceID, id, table)
		if err != nil {
			return nil, nil, err
		}
		if len(ids) > 0 {
			res[table] = len(ids)
		}
		failed = append(failed, left...)
		if table == "customer_events" && len(ids) > 0 {
			rows, err := tx.Query(ctx, `SELECT DISTINCT record_id::text FROM customer_events WHERE id = ANY($1::uuid[]) AND record_id IS NOT NULL AND name = ANY($2)`,
				ids, paymentEvents)
			if err != nil {
				return nil, nil, err
			}
			recs, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, nil, err
			}
			for _, r := range recs {
				payments[r] = true
			}
		}
	}
	// Inbox rows get their link to a restored event back.
	if _, err := tx.Exec(ctx, `WITH back AS (
			UPDATE event_inbox e SET customer_event_id = (x.data->>'customer_event_id')::uuid
			FROM deleted_rows x WHERE x.deletion_id = $1 AND x.source_table = 'event_inbox' AND e.id = (x.data->>'id')::uuid
				AND e.workspace_id = $2 AND e.customer_event_id IS NULL
				AND EXISTS (SELECT 1 FROM customer_events c WHERE c.id = (x.data->>'customer_event_id')::uuid)
			RETURNING x.id)
		DELETE FROM deleted_rows WHERE id IN (SELECT id FROM back)`, id, a.WorkspaceID); err != nil {
		return nil, nil, err
	}
	// Links whose event came back, or whose inbox row is gone, are done.
	if _, err := tx.Exec(ctx, `DELETE FROM deleted_rows x WHERE x.deletion_id = $1 AND x.source_table = 'event_inbox'
		AND (EXISTS (SELECT 1 FROM customer_events c WHERE c.id = (x.data->>'customer_event_id')::uuid)
			OR NOT EXISTS (SELECT 1 FROM event_inbox e WHERE e.id = (x.data->>'id')::uuid))`, id); err != nil {
		return nil, nil, err
	}
	for r := range payments {
		if err := quietBilling(ctx, tx, a.WorkspaceID, r); err != nil {
			return nil, nil, err
		}
	}
	return res, failed, nil
}

// restorableColumns are the columns of table a restore writes: the ones
// the deleted rows hold and the table still has, leaving out generated
// ones. An event's updated_at is left to its default, so the event mirror
// copies it again.
func restorableColumns(ctx context.Context, tx pgx.Tx, deletionID, table string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT c.column_name FROM information_schema.columns c
		WHERE c.table_schema = current_schema() AND c.table_name = $2 AND c.is_generated = 'NEVER'
			AND COALESCE(c.identity_generation, '') <> 'ALWAYS' AND NOT ($2 = 'customer_events' AND c.column_name = 'updated_at')
			AND EXISTS (SELECT 1 FROM deleted_rows x WHERE x.deletion_id = $1 AND x.source_table = $2 AND x.data ? c.column_name)
		ORDER BY c.ordinal_position`, deletionID, table)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// restoreTable inserts a deletion's rows of one table again and removes
// them from deleted_rows. It returns the ids that came back and the rows
// that couldn't, which stay.
func restoreTable(ctx context.Context, tx pgx.Tx, ws, deletionID, table string) ([]string, []notRestored, error) {
	cols, err := restorableColumns(ctx, tx, deletionID, table)
	if err != nil || len(cols) == 0 {
		return nil, nil, err
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = pgx.Identifier{c}.Sanitize()
	}
	colSQL := strings.Join(quoted, ", ")
	id := rowID[table]
	insert := func(q pgx.Tx, only *int64) ([]string, error) {
		sql := `INSERT INTO ` + pgx.Identifier{table}.Sanitize() + ` AS t (` + colSQL + `)
			SELECT ` + colSQL + ` FROM (SELECT p.*, x.id AS outflare_seq FROM deleted_rows x,
				jsonb_populate_record(NULL::` + pgx.Identifier{table}.Sanitize() + `, x.data) p
				WHERE x.deletion_id = $1 AND x.source_table = $2 AND x.workspace_id = $3 AND ($4::bigint IS NULL OR x.id = $4)
				ORDER BY x.id) r
			ON CONFLICT DO NOTHING RETURNING ` + id
		rows, err := q.Query(ctx, sql, deletionID, table, ws, only)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[string])
	}
	// All at once; if one row's foreign key fails, one at a time.
	var back []string
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	back, err = insert(sp, nil)
	if err == nil {
		err = sp.Commit(ctx)
	} else {
		_ = sp.Rollback(ctx)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23503" {
			return nil, nil, err
		}
		back, err = restoreOneByOne(ctx, tx, deletionID, table, insert)
	}
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM deleted_rows WHERE deletion_id = $1 AND source_table = $2 AND row_id = ANY($3)`, deletionID, table, back); err != nil {
		return nil, nil, err
	}
	// What is left couldn't come back: gone if what it points at is gone,
	// else taken.
	rows, err := tx.Query(ctx, `SELECT id, row_id FROM deleted_rows WHERE deletion_id = $1 AND source_table = $2 ORDER BY id`, deletionID, table)
	if err != nil {
		return nil, nil, err
	}
	type left struct {
		seq int64
		id  string
	}
	rest, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (left, error) {
		var l left
		return l, row.Scan(&l.seq, &l.id)
	})
	if err != nil {
		return nil, nil, err
	}
	var failed []notRestored
	for _, l := range rest {
		reason := reasonTaken
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		if _, err := insert(sp, &l.seq); err != nil {
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23503" {
				_ = sp.Rollback(ctx)
				return nil, nil, err
			}
			reason = reasonGone
		}
		// A probe only: it changes nothing either way.
		_ = sp.Rollback(ctx)
		failed = append(failed, notRestored{Table: table, ID: l.id, Reason: reason})
	}
	return back, failed, nil
}

// restoreOneByOne restores rows one at a time, each in its own savepoint,
// skipping those whose foreign key fails.
func restoreOneByOne(ctx context.Context, tx pgx.Tx, deletionID, table string, insert func(pgx.Tx, *int64) ([]string, error)) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM deleted_rows WHERE deletion_id = $1 AND source_table = $2 ORDER BY id`, deletionID, table)
	if err != nil {
		return nil, err
	}
	seqs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	var back []string
	for _, seq := range seqs {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		got, err := insert(sp, &seq)
		if err != nil {
			_ = sp.Rollback(ctx)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23503" {
				continue
			}
			return nil, err
		}
		if err := sp.Commit(ctx); err != nil {
			return nil, err
		}
		back = append(back, got...)
	}
	return back, nil
}

// ---- Purging ----

// RunRecoveryWindow purges deletions past their 7 days until ctx ends.
func (s *Server) RunRecoveryWindow(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if _, err := s.PurgeDeletions(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "deletion.purge_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PurgeDeletions removes deletions past their 7 days for good, and returns
// how many: what they hold is deleted, and a deleted record is erased as
// DELETE .../records/{record} would (with the records merged into it). A
// restored deletion's row goes too.
func (s *Server) PurgeDeletions(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := s.purgeRound(ctx)
		total += n
		if err != nil {
			return total, err
		}
		if n < purgeBatch {
			break
		}
	}
	var oldest *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT min(purge_after) FROM deletions`).Scan(&oldest); err != nil {
		return total, err
	}
	overdue := time.Duration(0)
	if oldest != nil {
		overdue = time.Since(*oldest)
	}
	metrics.SetRecoveryPurgeOverdue(overdue)
	return total, nil
}

func (s *Server) purgeRound(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, workspace_id::text, kind, record_id::text, restored_at IS NOT NULL, counts FROM deletions
			WHERE purge_after <= now() ORDER BY purge_after LIMIT $1 FOR UPDATE SKIP LOCKED`, purgeBatch)
		if err != nil {
			return err
		}
		type due struct {
			id, ws, kind string
			rec          *string
			restored     bool
			counts       map[string]int
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.id, &d.ws, &d.kind, &d.rec, &d.restored, &d.counts)
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			if d.kind == deletedRecord && !d.restored && d.rec != nil {
				if err := purgeRecord(ctx, tx, d.ws, d.id, *d.rec); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM deletions WHERE id = $1`, d.id); err != nil {
				return err
			}
			n++
			if d.restored {
				continue
			}
			metrics.RecoveryPurged(d.kind)
			p := principal{ActorType: "system"}
			data := map[string]any{"kind": d.kind, "counts": d.counts}
			props := map[string]any{"kind": d.kind}
			if d.rec != nil {
				data["record_id"], props["record_id"] = *d.rec, *d.rec
			}
			if err := recordWithProps(ctx, tx, &p, d.ws, "deletion.purged", "deletion", d.id, data, props); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// purgeRecord erases a deleted record and the records merged into it, if
// it is still deleted by this deletion.
func purgeRecord(ctx context.Context, tx pgx.Tx, ws, deletionID, rec string) error {
	var recType string
	err := tx.QueryRow(ctx, `SELECT record_type FROM records WHERE id = $1 AND workspace_id = $2 AND deletion_id = $3 AND deleted_at IS NOT NULL`,
		rec, ws, deletionID).Scan(&recType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE m AS (SELECT id FROM records WHERE id = $1
			UNION SELECT r.id FROM records r JOIN m ON r.merged_into = m.id WHERE r.workspace_id = $2)
		SELECT id::text FROM m`, rec, ws)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	// A person's emails, from the identities the deletion holds, so their
	// leads go too and the link job can't make them again.
	var emails []string
	if recType == "person" {
		rows, err := tx.Query(ctx, `SELECT DISTINCT data->>'value' FROM deleted_rows
			WHERE deletion_id = $1 AND source_table = 'record_identities' AND data->>'namespace' = 'email'`, deletionID)
		if err != nil {
			return err
		}
		if emails, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
	}
	_, err = customers.Erase(ctx, tx, ws, ids, emails)
	return err
}
