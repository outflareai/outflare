package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/flags"
)

// checkFlagLists makes sure every People list a flag's rules name is one
// of the flag's brand (or shared by every brand), so a flag can't be
// gated on another brand's people.
func (s *Server) checkFlagLists(ctx context.Context, ws string, brand *string, rules []flags.Rule) error {
	_, lists, _ := flags.Person(rules)
	if len(lists) == 0 {
		return nil
	}
	for _, id := range lists {
		if !validUUID(id) {
			return errInvalid("a list rule's values are People list ids, from GET .../smart-lists")
		}
	}
	slices.Sort(lists)
	lists = slices.Compact(lists)
	var found int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM smart_lists WHERE workspace_id = $1 AND id = ANY($2::uuid[])
		AND (brand_id IS NULL OR brand_id = $3::uuid) AND record_type = 'person'`, ws, lists, brand).Scan(&found); err != nil {
		return err
	}
	if found != len(lists) {
		return errInvalid("a list rule names a list that isn't a People list of this flag's brand")
	}
	return nil
}

// flagPerson tells v who the visitor is, when a flag that will be decided
// has a rule about them (user_id, list or person.<field>): the brand's
// person holding userID, else the one anonymousID was linked to. A
// visitor nobody knows stays unknown. It reads only the lists and fields
// the rules name, and costs nothing when no rule asks.
func (s *Server) flagPerson(ctx context.Context, ws string, brand *string, list []liveFlag, decided func(liveFlag) bool, v *flags.Visitor, anonymousID, userID string) error {
	v.UserID = userID
	if brand == nil {
		// People belong to a brand: without one the visitor stays unknown.
		return nil
	}
	var lists, fields []string
	needed := false
	for _, f := range list {
		if f.Status != "on" || !decided(f) {
			continue
		}
		n, l, fl := flags.Person(f.Rules)
		needed = needed || n
		lists, fields = append(lists, l...), append(fields, fl...)
	}
	if !needed {
		return nil
	}
	const q = `SELECT r.attributes,
			ARRAY(SELECT m.list_id::text FROM smart_list_members m WHERE m.record_id = r.id AND m.list_id = ANY($5::uuid[])
				UNION SELECT t.list_id::text FROM static_list_members t WHERE t.record_id = r.id AND t.list_id = ANY($5::uuid[])),
			COALESCE((SELECT u.value FROM record_identities u WHERE u.record_id = r.id AND u.namespace = 'user_id' AND u.valid_to IS NULL
				ORDER BY u.valid_from DESC LIMIT 1), '')
		FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.brand_id = $2 AND i.namespace = $3 AND i.value = $4 AND i.valid_to IS NULL
			AND r.workspace_id = $1 AND r.brand_id = $2 AND r.merged_into IS NULL AND r.archived_at IS NULL
		ORDER BY i.verified DESC, i.valid_from DESC LIMIT 1`
	for _, id := range [][2]string{{"user_id", userID}, {"anonymous_id", anonymousID}} {
		if id[1] == "" {
			continue
		}
		var raw []byte
		var in []string
		var known string
		err := s.pool.QueryRow(ctx, q, ws, *brand, id[0], id[1], lists).Scan(&raw, &in, &known)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		v.Lists = in
		if v.UserID == "" {
			v.UserID = known
		}
		var all map[string]json.RawMessage
		if json.Unmarshal(raw, &all) == nil && len(fields) > 0 {
			v.Person = map[string]string{}
			for _, k := range fields {
				if text, ok := attributeText(all[k]); ok {
					v.Person[k] = text
				}
			}
		}
		return nil
	}
	return nil
}

// attributeText is a record field's value as the text a rule compares: a
// string as it is, a number or checkbox as written, a list joined with
// commas. Empty and structured values (a currency, a location) have none.
func attributeText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var x any
	if json.Unmarshal(raw, &x) != nil {
		return "", false
	}
	switch t := x.(type) {
	case string:
		return clip(t, 500), t != ""
	case bool, float64:
		return strings.Trim(string(raw), `"`), true
	case []any:
		parts := []string{}
		for _, e := range t {
			if str, ok := e.(string); ok {
				parts = append(parts, str)
			}
		}
		return clip(strings.Join(parts, ","), 500), len(parts) > 0
	}
	return "", false
}
