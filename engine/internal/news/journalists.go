package news

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// Journalists are people on customer records: the catalog's journalist
// field marks them, beats holds the topics they cover, and an outlet's
// outlet_tier is on its company. The news watch marks the authors it finds
// and suggests their beats from the topic searches their articles came up
// in; a person can add journalists by hand or from a file, and edit their
// beats, and the news watch never overrides what a person set.

// AutoSource is the attribute source of what the news watch works out: it
// ranks below a person's own edit (manual), so it never overrides one.
const AutoSource = "enrichment"

// Tiers are the outlet_tier options, in order.
var Tiers = []string{"national", "trade", "regional", "local", "newsletter"}

const (
	// MaxBeats is the most beats a journalist keeps.
	MaxBeats = 30
	// maxBeat is the longest beat, in bytes.
	maxBeat = 100
)

// CleanBeat is a beat as stored: trimmed, with single spaces, at most 100
// bytes, or "" when there is nothing left.
func CleanBeat(s string) string {
	return clip(strings.Join(strings.Fields(s), " "), maxBeat)
}

// MergeBeats adds beats to have, keeping have's order and case and leaving
// out a beat already there in any case, up to MaxBeats.
func MergeBeats(have []string, add ...string) []string {
	out := make([]string, 0, len(have)+len(add))
	seen := map[string]bool{}
	for _, b := range slices.Concat(have, add) {
		b = CleanBeat(b)
		k := strings.ToLower(b)
		if b == "" || seen[k] || len(out) >= MaxBeats {
			continue
		}
		seen[k] = true
		out = append(out, b)
	}
	return out
}

// Beats reads a record's beats attribute.
func Beats(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// markJournalist marks a person the news watch found writing an article as
// a journalist, and adds the names of the topic searches that found the
// article to their beats, unless a person set either. A workspace that
// made its own field of either key keeps it as it is.
func markJournalist(ctx context.Context, tx pgx.Tx, ws, person, articleID string) error {
	var attrs map[string]any
	var sources map[string]string
	var brand *string
	err := tx.QueryRow(ctx, `SELECT attributes, attribute_sources, brand_id::text FROM records
		WHERE id = $1 AND workspace_id = $2 AND merged_into IS NULL FOR UPDATE`, person, ws).Scan(&attrs, &sources, &brand)
	if err != nil {
		return err
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	if sources == nil {
		sources = map[string]string{}
	}
	rows, err := tx.Query(ctx, `SELECT key FROM field_definitions WHERE workspace_id = $1 AND record_type = 'person'
		AND key IN ('journalist', 'beats') AND brand_id IS NOT DISTINCT FROM $2::uuid`, ws, brand)
	if err != nil {
		return err
	}
	own, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	type change struct {
		key       string
		old, next any
		had       bool
	}
	var changes []change
	if _, ok := attrs["journalist"]; !ok && !slices.Contains(own, "journalist") {
		changes = append(changes, change{key: "journalist", next: true})
	}
	if s := sources["beats"]; (s == "" || s == AutoSource) && !slices.Contains(own, "beats") {
		rows, err := tx.Query(ctx, `SELECT DISTINCT n.name FROM news_hits h JOIN news_searches n ON n.id = h.search_id
			WHERE h.article_id = $1 AND n.workspace_id = $2 AND n.purpose = 'topic' ORDER BY n.name`, articleID, ws)
		if err != nil {
			return err
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		prev, had := attrs["beats"]
		have := Beats(prev)
		if next := MergeBeats(have, names...); len(next) != len(have) {
			list := make([]any, len(next))
			for i, b := range next {
				list[i] = b
			}
			changes = append(changes, change{key: "beats", old: prev, had: had, next: list})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	for _, c := range changes {
		attrs[c.key], sources[c.key] = c.next, AutoSource
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now()
		WHERE id = $1`, person, attrs, sources); err != nil {
		return err
	}
	for _, c := range changes {
		var old *string
		if c.had {
			b, _ := json.Marshal(c.old)
			s := string(b)
			old = &s
		}
		next, _ := json.Marshal(c.next)
		if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source)
			VALUES ($1, $2, $3, $4, $5, $6)`, ws, person, c.key, old, string(next), AutoSource); err != nil {
			return err
		}
		if err := customers.FieldChanged(ctx, tx, ws, customers.FieldChange{RecordID: person, Field: c.key, Old: c.old, HadOld: c.had, Source: AutoSource}); err != nil {
			return err
		}
	}
	return nil
}
