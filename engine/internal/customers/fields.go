package customers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
)

// Fields a tool keeps on records: what it learned about a person or
// company (a summary of what the company does, why it fits, where it was
// found), as ordinary fields in the catalog so people see them, filter on
// them, use them in merge tags and lists, and other tools reuse them. A
// field is made in the record's brand the first time a value is written,
// and a value is written like any other source's: in precedence order, so
// a person's own edit or a better source is never overwritten, with its
// history and an audit event.

// Field is a field a tool writes. Type is text, number, url or timestamp.
type Field struct {
	Key         string
	Label       string
	Description string
	Type        string
}

var fieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

// ErrBadField is a Field that can't be made: a key, label or type the
// catalog doesn't allow.
var ErrBadField = errors.New("customers: bad field")

func (f Field) valid() bool {
	return fieldKey.MatchString(f.Key) && f.Label != "" && slices.Contains([]string{"text", "number", "url", "timestamp"}, f.Type)
}

// defaultPrecedence is the precedence a field has when made: a person's
// edit, first-party data, a CRM sync, then enrichment.
var defaultPrecedence = []string{"manual", "first_party", "crm_sync", "enrichment"}

// SetFields writes values (by field key, from fields) onto a record with
// source, in tx. A field the record's brand lacks is made first. A value
// is left as it is when a source ahead of this one in the field's
// precedence set it, when the field is computed or owned by its source, or
// when the brand has a field of that key of another type. Empty strings
// and nil are skipped. It reports whether anything changed.
func SetFields(ctx context.Context, tx pgx.Tx, workspaceID, recordID, source string, fields []Field, values map[string]any) (bool, error) {
	var recordType string
	var brand *string
	var attrs map[string]any
	var sources map[string]string
	err := tx.QueryRow(ctx, `SELECT record_type, COALESCE(brand_id, outflare_first_brand(workspace_id))::text, attributes, attribute_sources
		FROM records WHERE id = $1 AND workspace_id = $2 AND merged_into IS NULL FOR UPDATE`, recordID, workspaceID).
		Scan(&recordType, &brand, &attrs, &sources)
	if err != nil {
		return false, err
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	if sources == nil {
		sources = map[string]string{}
	}
	changed := false
	for _, f := range fields {
		v, ok := values[f.Key]
		if !ok || v == nil {
			continue
		}
		if s, isText := v.(string); isText && s == "" {
			continue
		}
		if !f.valid() {
			return changed, fmt.Errorf("%w: %q", ErrBadField, f.Key)
		}
		// The brand's field, made when it has none. A shared record's
		// fields are the workspace's first brand's, as everywhere else.
		if _, err := tx.Exec(ctx, `INSERT INTO field_definitions (workspace_id, brand_id, record_type, key, label, description, value_type, filterable)
			SELECT $1, $2::uuid, $3, $4, $5, $6, $7, $7 IN ('number', 'timestamp')
			WHERE NOT EXISTS (SELECT 1 FROM field_definitions WHERE brand_id = $2::uuid AND record_type = $3 AND key = $4)
			ON CONFLICT DO NOTHING`, workspaceID, brand, recordType, f.Key, f.Label, f.Description, f.Type); err != nil {
			return changed, err
		}
		var valueType, policy string
		var precedence []string
		if err := tx.QueryRow(ctx, `SELECT value_type, write_policy, precedence FROM field_definitions
			WHERE brand_id = $1::uuid AND record_type = $2 AND key = $3 AND archived_at IS NULL`, brand, recordType, f.Key).
			Scan(&valueType, &policy, &precedence); errors.Is(err, pgx.ErrNoRows) {
			continue // archived: the brand chose not to keep it
		} else if err != nil {
			return changed, err
		}
		if valueType != f.Type || policy == "computed" || policy == "source_owned" {
			continue
		}
		prev, had := attrs[f.Key]
		if had && outranks(precedence, sources[f.Key], source) {
			continue
		}
		if had && sameValue(prev, v) {
			if sources[f.Key] != source {
				sources[f.Key], changed = source, true
			}
			continue
		}
		var old *string
		if had {
			b, _ := json.Marshal(prev)
			s := string(b)
			old = &s
		}
		next, _ := json.Marshal(v)
		if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source)
			VALUES ($1, $2, $3, $4, $5, $6)`, workspaceID, recordID, f.Key, old, string(next), source); err != nil {
			return changed, err
		}
		if err := FieldChanged(ctx, tx, workspaceID, FieldChange{RecordID: recordID, Field: f.Key, Old: prev, HadOld: had, Source: source}); err != nil {
			return changed, err
		}
		attrs[f.Key], sources[f.Key], changed = v, source, true
	}
	if !changed {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now()
		WHERE id = $1`, recordID, attrs, sources); err != nil {
		return true, err
	}
	return true, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "record.updated", TargetType: "record", TargetID: recordID,
		ActorType: "system", Data: map[string]any{"type": recordType, "via": source}})
}

// outranks says whether the source that set a value comes ahead of next
// in precedence, so next must leave it. A source missing from the list
// ranks after every listed one; an unknown setter never blocks a listed
// source, and two unlisted sources replace each other.
func outranks(precedence []string, set, next string) bool {
	if len(precedence) == 0 {
		precedence = defaultPrecedence
	}
	rank := func(s string) int {
		if i := slices.Index(precedence, s); i >= 0 {
			return i
		}
		return len(precedence)
	}
	return set != "" && rank(set) < rank(next)
}
