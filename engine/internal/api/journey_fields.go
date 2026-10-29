package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// WriteField changes one field of a record for a journey step, with the
// same checks, history and source precedence as PATCH .../records/{id}.
// change is set, clear or increment, or add and remove for a field that
// holds a list (tags). The write's source is first_party, so it never
// overwrites a value a person set by hand, unless overwriteManual; then the
// step reports it skipped with why. ref is the journey run, kept in the
// audit data.
//
// It reports done or skipped with a reason, and failed (with a message)
// for a step that can't work, such as an unknown field or a bad value.
func (s *Server) WriteField(ctx context.Context, tx pgx.Tx, ws, recordID, field, change string, value any, overwriteManual bool, ref string) (string, string, error) {
	a := &access{principal: principal{ActorType: "system"}, WorkspaceID: ws}
	rec, err := lockRecord(ctx, tx, a, recordID)
	if errors.Is(err, errNotFound) {
		return "skipped", "the record is gone", nil
	}
	if err != nil {
		return "", "", err
	}
	if rec.MergedInto != nil || rec.Archived {
		return "skipped", "the record is merged or archived", nil
	}
	c, err := loadCatalog(ctx, tx, ws, false)
	if err != nil {
		return "", "", err
	}
	t := c.recordType(rec.Type)
	if t == nil {
		return "skipped", "the record's type is gone", nil
	}
	f := t.field(field)
	if f == nil {
		return "failed", fmt.Sprintf("%s has no field %s", t.Name, field), nil
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	prev, had := rec.Attributes[field]
	var next any
	switch change {
	case "", "set":
		next = value
	case "clear":
		next = nil
	case "increment":
		by, ok := asNumber(value)
		if !ok {
			return "failed", "increment needs a number", nil
		}
		cur, _ := asNumber(prev)
		next = cur + by
	case "add", "remove":
		if !f.Multiple {
			return "failed", field + " holds one value; add and remove are for lists such as tags", nil
		}
		list, _ := prev.([]any)
		list = slices.Clone(list)
		item := value
		if f.Builtin && f.Key == "tags" {
			if item, err = NormalizeTag(value); err != nil {
				return "failed", err.Error(), nil
			}
		}
		at := slices.IndexFunc(list, func(v any) bool { return sameJSON(v, item) })
		switch {
		case change == "add" && at < 0:
			list = append(list, item)
		case change == "remove" && at >= 0:
			list = slices.Delete(list, at, at+1)
		default:
			return "skipped", "no change", nil
		}
		next = list
		// Adding or taking off one label is a deliberate step: it keeps a
		// list someone made by hand, rather than being refused by it.
		overwriteManual = true
	default:
		return "failed", "change is set, clear, increment, add or remove", nil
	}
	raw, _ := json.Marshal(next)
	source := "first_party"
	if overwriteManual && had {
		if cur := rec.AttributeSources[field]; cur == "manual" {
			delete(rec.AttributeSources, field)
			if change == "add" || change == "remove" {
				source = "manual" // the list stays a person's
			}
		}
	}
	changed, err := writeAttributes(ctx, tx, c, t, a, &rec, map[string]json.RawMessage{field: raw}, source, false)
	var inv errInvalid
	if errors.As(err, &inv) {
		return "failed", string(inv), nil
	}
	if err != nil {
		return "", "", err
	}
	if len(rec.Skipped) > 0 {
		return "skipped", "set by " + rec.Skipped[0].KeptSource + ", which a journey doesn't overwrite", nil
	}
	if !changed {
		return "skipped", "no change", nil
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
		rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
		return "", "", err
	}
	return "done", "", record(ctx, tx, &a.principal, ws, "record.updated", "record", rec.ID,
		map[string]any{"type": rec.Type, "via": "journey", "run_id": ref})
}

func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}
