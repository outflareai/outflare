package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Field sources (plans/customer-data-platform, the owner's decision of
// 2026-11-02; docs/customers.md, "Where a value comes from"). Every
// source's latest value of a field is kept in field_values, won or not. A
// hand edit wins over the other sources for the brand's manual_edit_days
// (180 by default; a field can set its own, 0 is forever) unless it is
// pinned. Once it expires, the next source to write the field replaces
// it, and the sweep (ExpireHandEdits) hands the field to the best value
// another source already gave.

// keepFieldValue stores what source said about a record's field: next, or
// nothing when it cleared it.
func keepFieldValue(ctx context.Context, tx pgx.Tx, ws, recordID, key, source string, next any) error {
	if next == nil {
		_, err := tx.Exec(ctx, `DELETE FROM field_values WHERE record_id = $1 AND field_key = $2 AND source = $3`, recordID, key, source)
		return err
	}
	b, _ := json.Marshal(next)
	_, err := tx.Exec(ctx, `INSERT INTO field_values (workspace_id, brand_id, record_id, field_key, source, value)
		SELECT $1, COALESCE(r.brand_id, outflare_first_brand($1)), r.id, $3, $4, $5 FROM records r WHERE r.id = $2 AND r.workspace_id = $1 AND COALESCE(r.brand_id, outflare_first_brand($1)) IS NOT NULL
		ON CONFLICT (record_id, field_key, source) DO UPDATE SET value = EXCLUDED.value, seen_at = now()`, ws, recordID, key, source, string(b))
	return err
}

// handEditExpired says whether the hand edit behind a field's value has
// stopped winning. A value set by hand before hand edits expired has no
// row and keeps winning.
func handEditExpired(ctx context.Context, tx pgx.Tx, recordID, key string) (bool, error) {
	var expired bool
	err := tx.QueryRow(ctx, `SELECT NOT pinned AND (expired_at IS NOT NULL OR COALESCE(expires_at <= now(), false)) FROM hand_edits WHERE record_id = $1 AND field_key = $2`,
		recordID, key).Scan(&expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return expired, err
}

// handEditDays is how long a hand edit of a record's field wins, as SQL:
// the field's own setting, else its brand's. $1 is the workspace, $2 the
// record and $3 the field.
const handEditDays = `COALESCE(
	(SELECT f.manual_edit_days FROM field_definitions f WHERE f.brand_id = COALESCE(r.brand_id, outflare_first_brand($1)) AND f.record_type = r.record_type AND f.key = $3),
	(SELECT b.manual_edit_days FROM brands b WHERE b.id = COALESCE(r.brand_id, outflare_first_brand($1))), 180)`

// noteHandEdit records that a person (or agent) set a field by hand now:
// it wins until its days run out, or for good when it is pinned.
func noteHandEdit(ctx context.Context, tx pgx.Tx, a *access, recordID, key string) error {
	_, err := tx.Exec(ctx, `INSERT INTO hand_edits (workspace_id, brand_id, record_id, field_key, set_by, expires_at)
		SELECT $1, COALESCE(r.brand_id, outflare_first_brand($1)), r.id, $3, $4,
			CASE WHEN d.days = 0 THEN NULL ELSE now() + d.days * interval '1 day' END
		FROM records r, LATERAL (SELECT `+handEditDays+` AS days) d WHERE r.id = $2 AND r.workspace_id = $1
			AND COALESCE(r.brand_id, outflare_first_brand($1)) IS NOT NULL
		ON CONFLICT (record_id, field_key) DO UPDATE SET set_at = now(), set_by = EXCLUDED.set_by, expired_at = NULL,
			expires_at = CASE WHEN hand_edits.pinned THEN NULL ELSE EXCLUDED.expires_at END`,
		a.WorkspaceID, recordID, key, nullIfEmpty(a.ActorID))
	return err
}

// dropHandEdit forgets the hand edit behind a field: another source
// replaced it, or the person cleared the field.
func dropHandEdit(ctx context.Context, tx pgx.Tx, recordID, key string) error {
	_, err := tx.Exec(ctx, `DELETE FROM hand_edits WHERE record_id = $1 AND field_key = $2`, recordID, key)
	return err
}

// RunHandEditExpiry hands expired hand edits over every interval, until
// ctx ends.
func (s *Server) RunHandEditExpiry(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.ExpireHandEdits(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "hand_edits.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ExpireHandEdits handles the hand edits whose time ran out, a batch at a
// time, and reports how many. Each goes in its own transaction: the field
// takes the value of the best other source that gave one (by the field's
// precedence, then the newest), as a normal change in the record's history
// that rules hear of. With no other value it stays, marked expired, and the
// next source to write the field replaces it.
func (s *Server) ExpireHandEdits(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT workspace_id::text, record_id::text, field_key FROM hand_edits
		WHERE expired_at IS NULL AND NOT pinned AND expires_at <= now() ORDER BY expires_at LIMIT 500`)
	if err != nil {
		return 0, err
	}
	type due struct{ ws, record, key string }
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.ws, &d.record, &d.key)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range all {
		outcome, err := s.expireHandEdit(ctx, d.ws, d.record, d.key)
		if err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			metrics.HandEditExpired("error")
			track.Fail(ctx, s.log, track.Event{Name: "hand_edit.expire_failed", WorkspaceID: d.ws, ActorType: "system", Surface: "job"}, "internal", err)
			continue
		}
		if outcome != "" {
			metrics.HandEditExpired(outcome)
			n++
		}
	}
	return n, nil
}

func (s *Server) expireHandEdit(ctx context.Context, ws, recordID, key string) (string, error) {
	outcome := ""
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT true FROM hand_edits WHERE record_id = $1 AND field_key = $2
			AND expired_at IS NULL AND NOT pinned AND expires_at <= now() FOR UPDATE SKIP LOCKED`, recordID, key).Scan(&ok); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		a := &access{WorkspaceID: ws}
		rec, err := lockRecord(ctx, tx, a, recordID)
		if errors.Is(err, errNotFound) || errors.Is(err, errSharedRecord) || (err == nil && rec.MergedInto != nil) {
			return dropHandEdit(ctx, tx, recordID, key)
		}
		if err != nil {
			return err
		}
		if rec.AttributeSources == nil || rec.AttributeSources[key] != "manual" {
			return dropHandEdit(ctx, tx, recordID, key)
		}
		c, err := loadCatalog(ctx, tx, ws, deref(rec.BrandID), false)
		if err != nil {
			return err
		}
		t := c.recordType(rec.Type)
		var f *fieldView
		if t != nil {
			f = t.field(key)
		}
		if f == nil || f.Archived || f.WritePolicy == "computed" {
			outcome = "kept"
			_, err := tx.Exec(ctx, `UPDATE hand_edits SET expired_at = now() WHERE record_id = $1 AND field_key = $2`, recordID, key)
			return err
		}
		vals, err := tx.Query(ctx, `SELECT source, value FROM field_values WHERE record_id = $1 AND field_key = $2 AND source <> 'manual' ORDER BY seen_at DESC`, recordID, key)
		if err != nil {
			return err
		}
		best, value := "", json.RawMessage(nil)
		for vals.Next() {
			var src string
			var v json.RawMessage
			if err := vals.Scan(&src, &v); err != nil {
				vals.Close()
				return err
			}
			if best == "" || sourceRank(f, src) < sourceRank(f, best) {
				best, value = src, v
			}
		}
		vals.Close()
		if err := vals.Err(); err != nil {
			return err
		}
		if best == "" {
			outcome = "kept"
			_, err := tx.Exec(ctx, `UPDATE hand_edits SET expired_at = now() WHERE record_id = $1 AND field_key = $2`, recordID, key)
			return err
		}
		outcome = "switched"
		changed, err := writeAttributes(ctx, tx, c, t, a, &rec, map[string]json.RawMessage{key: value}, best, false)
		if err != nil {
			return err
		}
		if rec.AttributeSources[key] == "manual" {
			// The other source says the same: it is now that source's value.
			rec.AttributeSources[key] = best
		}
		if err := dropHandEdit(ctx, tx, recordID, key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
			rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
			return err
		}
		return record(ctx, tx, nil, ws, "record.hand_edit_expired", "record", rec.ID,
			map[string]any{"type": rec.Type, "field": key, "source": best, "changed": changed})
	})
	return outcome, err
}

// fieldValueView is what one source last said about a field.
type fieldValueView struct {
	Field  string    `json:"field"`
	Source string    `json:"source"`
	Value  any       `json:"value"`
	SeenAt time.Time `json:"seen_at"`
	// Whether it is the record's value now.
	Current bool `json:"current"`
}

// handEditView is the hand edit behind a field's value.
type handEditView struct {
	Field     string     `json:"field"`
	SetAt     time.Time  `json:"set_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	Pinned    bool       `json:"pinned"`
	// Its time ran out: the next source to write the field replaces it.
	Expired bool `json:"expired"`
}

// listFieldValues shows every source's latest value of each of a record's
// fields, and the hand edits behind its values: what "Use this" offers.
func (s *Server) listFieldValues(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	values, edits := []fieldValueView{}, []handEditView{}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT field_key, source, value, seen_at FROM field_values WHERE record_id = $1 ORDER BY field_key, seen_at DESC`, rec.ID)
		if err != nil {
			return err
		}
		if values, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (fieldValueView, error) {
			var v fieldValueView
			err := row.Scan(&v.Field, &v.Source, &v.Value, &v.SeenAt)
			v.Current = rec.AttributeSources[v.Field] == v.Source && sameJSON(rec.Attributes[v.Field], v.Value)
			return v, err
		}); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT field_key, set_at, expires_at, pinned, NOT pinned AND (expired_at IS NOT NULL OR COALESCE(expires_at <= now(), false))
			FROM hand_edits WHERE record_id = $1 ORDER BY field_key`, rec.ID)
		if err != nil {
			return err
		}
		edits, err = pgx.CollectRows(rows, pgx.RowToStructByPos[handEditView])
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": values, "hand_edits": edits})
}

// pinField pins a field's hand edit, so it wins for good, or unpins it, so
// its time runs again from now.
func (s *Server) pinField(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Pinned *bool `json:"pinned"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Pinned == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "pinned is true or false")
		return
	}
	key := r.PathValue("field")
	var out handEditView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		value, had := rec.Attributes[key]
		if !had {
			return recordConflict{"field_empty", "the record has no value for " + key + " to pin"}
		}
		if *in.Pinned && rec.AttributeSources[key] != "manual" {
			return recordConflict{"not_a_hand_edit", "only a value set by hand can be pinned: set it by hand first (Use this)"}
		}
		var tag pgconn.CommandTag
		if *in.Pinned {
			tag, err = tx.Exec(ctx, `INSERT INTO hand_edits (workspace_id, brand_id, record_id, field_key, set_by, pinned, pinned_by)
				SELECT $1, COALESCE(r.brand_id, outflare_first_brand($1)), r.id, $3, $4, true, $4 FROM records r WHERE r.id = $2 AND COALESCE(r.brand_id, outflare_first_brand($1)) IS NOT NULL
				ON CONFLICT (record_id, field_key) DO UPDATE SET pinned = true, pinned_by = $4, expires_at = NULL, expired_at = NULL
				WHERE NOT hand_edits.pinned`, a.WorkspaceID, rec.ID, key, nullIfEmpty(a.ActorID))
		} else {
			tag, err = tx.Exec(ctx, `UPDATE hand_edits h SET pinned = false, pinned_by = NULL, expired_at = NULL,
				expires_at = CASE WHEN d.days = 0 THEN NULL ELSE now() + d.days * interval '1 day' END
				FROM records r, LATERAL (SELECT `+handEditDays+` AS days) d
				WHERE h.record_id = $2 AND h.field_key = $3 AND h.pinned AND r.id = h.record_id AND r.workspace_id = $1`, a.WorkspaceID, rec.ID, key)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			// In the record's own history too (AGENTS.md): the value stays,
			// what changes is whether it can be replaced.
			source := map[bool]string{true: "pinned", false: "unpinned"}[*in.Pinned]
			if err := attributeHistory(ctx, tx, a, rec.ID, key, value, source); err != nil {
				return err
			}
			action := map[bool]string{true: "record.field_pinned", false: "record.field_unpinned"}[*in.Pinned]
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "record", rec.ID, map[string]any{"type": rec.Type, "field": key}); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `SELECT field_key, set_at, expires_at, pinned, NOT pinned AND (expired_at IS NOT NULL OR COALESCE(expires_at <= now(), false))
			FROM hand_edits WHERE record_id = $1 AND field_key = $2`, rec.ID, key).Scan(&out.Field, &out.SetAt, &out.ExpiresAt, &out.Pinned, &out.Expired)
		if errors.Is(err, pgx.ErrNoRows) {
			// Unpinning a value with no hand edit behind it.
			return recordConflict{"not_a_hand_edit", "only a value set by hand can be pinned or unpinned"}
		}
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// attributeHistory writes a row in the record's history that keeps the
// value as it is (a pin, an unpin), without telling rules: nothing they
// act on changed.
func attributeHistory(ctx context.Context, tx pgx.Tx, a *access, recordID, key string, value any, source string) error {
	b, _ := json.Marshal(value)
	_, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source, actor_id, request_id)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7)`, a.WorkspaceID, recordID, key, string(b), source, nullIfEmpty(a.ActorID), RequestID(ctx))
	return err
}
