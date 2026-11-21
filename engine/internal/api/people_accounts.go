package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"
)

// A person's preferred email and their role in a customer account.

// accountRoles are the roles a person can have in a customer account (the
// account_role option set).
var accountRoles = []string{"owner", "admin", "member", "billing", "viewer"}

// preferIdentity makes one of a person's emails the one sends go to. The
// others stay on the person, and an opt-out still covers every one.
func (s *Server) preferIdentity(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var view identityView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		id, err := activeIdentity(ctx, tx, rec.ID, r.PathValue("identity"))
		if err != nil {
			return err
		}
		if id.Namespace != "email" {
			return errInvalid("only an email can be preferred")
		}
		if id.Preferred {
			view = id
			return nil
		}
		var before *string
		if err := tx.QueryRow(ctx, `SELECT value FROM record_identities WHERE record_id = $1 AND namespace = 'email' AND preferred AND valid_to IS NULL`,
			rec.ID).Scan(&before); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE record_identities SET preferred = false WHERE record_id = $1 AND namespace = 'email' AND preferred`, rec.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE record_identities i SET preferred = true WHERE i.id = $1 RETURNING `+identityColumns, id.ID)
		if err != nil {
			return err
		}
		if view, err = pgx.CollectExactlyOneRow(rows, scanIdentity); err != nil {
			return err
		}
		// The person's history shows the change like any other.
		var old *string
		if before != nil {
			b, _ := json.Marshal(*before)
			o := string(b)
			old = &o
		}
		next, _ := json.Marshal(view.Value)
		if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source, actor_id, request_id)
			VALUES ($1, $2, 'preferred_email', $3, $4, 'manual', $5, $6)`, a.WorkspaceID, rec.ID, old, string(next), nullIfEmpty(a.ActorID), RequestID(ctx)); err != nil {
			return err
		}
		if err := touch(ctx, tx, rec.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "identity.preferred", "identity", view.ID, map[string]any{"record_id": rec.ID})
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// updateRelationship changes a link's properties, such as a member's role
// in a customer account (properties.role). The link itself, and a field's
// value, stay as they are.
func (s *Server) updateRelationship(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Properties == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "properties is required: the keys to set, null to remove one")
		return
	}
	var view relationshipView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := lockRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		id := r.PathValue("relationship")
		if !validUUID(id) {
			return errNotFound
		}
		var field *string
		var props map[string]any
		var ended bool
		var person string
		err = tx.QueryRow(ctx, `SELECT field_key, properties, valid_to IS NOT NULL, from_id::text FROM record_relationships
			WHERE id = $1 AND (from_id = $2 OR to_id = $2) FOR UPDATE`, id, rec.ID).Scan(&field, &props, &ended, &person)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if ended {
			return recordConflict{"conflict", "this link has ended"}
		}
		if props == nil {
			props = map[string]any{}
		}
		before := props["role"]
		for k, v := range req.Properties {
			if v == nil {
				delete(props, k)
				continue
			}
			props[k] = v
		}
		if field != nil && *field == "accounts" {
			if role, ok := props["role"]; ok {
				if r, _ := role.(string); !slices.Contains(accountRoles, r) {
					return errInvalid("role is owner, admin, member, billing or viewer")
				}
			}
		}
		if b, _ := json.Marshal(props); len(b) > 4000 {
			return errInvalid("properties must be under 4 KB")
		}
		if _, err := tx.Exec(ctx, `UPDATE record_relationships SET properties = $2 WHERE id = $1`, id, props); err != nil {
			return err
		}
		// A member's role is part of the person's history.
		if field != nil && *field == "accounts" && !reflect.DeepEqual(props["role"], before) {
			var old *string
			if before != nil {
				b, _ := json.Marshal(before)
				o := string(b)
				old = &o
			}
			var next *string
			if v, ok := props["role"]; ok {
				b, _ := json.Marshal(v)
				n := string(b)
				next = &n
			}
			if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source, actor_id, request_id)
				VALUES ($1, $2, 'account_role', $3, $4, 'manual', $5, $6)`, a.WorkspaceID, person, old, next, nullIfEmpty(a.ActorID), RequestID(ctx)); err != nil {
				return err
			}
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "relationship.updated", "relationship", id, map[string]any{"record_id": rec.ID}); err != nil {
			return err
		}
		list, err := loadRelationships(ctx, tx, a, rec.ID, false, &id)
		if err == nil && len(list) == 1 {
			view = list[0]
		}
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}
