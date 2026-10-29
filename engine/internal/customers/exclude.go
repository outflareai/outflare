package customers

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// Exclusion is who an automation (a journey, a broadcast or a sequence)
// leaves out: people on any of Lists (smart or static), and people with
// any of Tags, or who work at a company with one. Unlike suppression,
// which blocks all email to an address, an exclusion only keeps people out
// of the automations that name it, such as "Active deal: hands off" or
// "Competitors".
type Exclusion struct {
	Lists []string `json:"lists"`
	Tags  []string `json:"tags"`
}

// ExcludedPrefix starts every reason Excluded gives.
const ExcludedPrefix = "excluded: "

var (
	excludeUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	excludeTag  = regexp.MustCompile(`^[a-z0-9][a-z0-9 _.:-]{0,49}$`)
)

// Empty reports whether it leaves nobody out.
func (x Exclusion) Empty() bool { return len(x.Lists) == 0 && len(x.Tags) == 0 }

// Clean returns x with nil slices made empty, for storing and showing.
func (x Exclusion) Clean() Exclusion {
	if x.Lists == nil {
		x.Lists = []string{}
	}
	if x.Tags == nil {
		x.Tags = []string{}
	}
	return x
}

// Check is what's wrong with an exclusion's shape, or "". The caller
// checks its lists are the workspace's (CheckLists).
func (x Exclusion) Check() string {
	if len(x.Lists) > 20 || len(x.Tags) > 20 {
		return "exclude takes at most 20 lists and 20 tags"
	}
	for _, l := range x.Lists {
		if !excludeUUID.MatchString(l) {
			return "exclude.lists are list ids"
		}
	}
	for _, t := range x.Tags {
		if !excludeTag.MatchString(t) {
			return "exclude.tags are tags such as competitor: lowercase, up to 50 characters"
		}
	}
	return ""
}

// CheckLists reports whether every list in x is one of the workspace's.
func CheckLists(ctx context.Context, q Querier, ws string, x Exclusion) (bool, error) {
	if len(x.Lists) == 0 {
		return true, nil
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(DISTINCT id) FROM smart_lists WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, x.Lists).Scan(&n)
	return n == len(uniq(x.Lists)), err
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// ExcludedSQL is a SQL condition true when the record (an expression for
// its id, which may be NULL) is left out by an exclusion whose lists and
// tags are at the given placeholders (uuid[] and text[]).
func ExcludedSQL(record, lists, tags string) string {
	return `(` + record + ` IS NOT NULL AND (
		EXISTS (SELECT 1 FROM smart_list_members xm WHERE xm.list_id = ANY(` + lists + `) AND xm.record_id = ` + record + `)
		OR EXISTS (SELECT 1 FROM static_list_members xs WHERE xs.list_id = ANY(` + lists + `) AND xs.record_id = ` + record + `)
		OR (cardinality(` + tags + `) > 0 AND EXISTS (SELECT 1 FROM records xr WHERE xr.id IN (SELECT ` + record + `
			UNION ALL SELECT xc.to_id FROM record_relationships xc WHERE xc.from_id = ` + record + ` AND xc.kind = 'companies' AND xc.valid_to IS NULL)
			AND jsonb_typeof(xr.attributes->'tags') = 'array' AND xr.attributes->'tags' ?| ` + tags + `))))`
}

// Excluded is why x leaves a record out, or "": the list it is on, its
// tag, or its company's tag.
func Excluded(ctx context.Context, q Querier, ws, recordID string, x Exclusion) (string, error) {
	if x.Empty() || recordID == "" {
		return "", nil
	}
	x = x.Clean()
	var list string
	err := q.QueryRow(ctx, `SELECT l.name FROM smart_lists l WHERE l.workspace_id = $1 AND l.id = ANY($2::uuid[]) AND (
			EXISTS (SELECT 1 FROM smart_list_members m WHERE m.list_id = l.id AND m.record_id = $3)
			OR EXISTS (SELECT 1 FROM static_list_members s WHERE s.list_id = l.id AND s.record_id = $3))
		ORDER BY l.name LIMIT 1`, ws, x.Lists, recordID).Scan(&list)
	switch {
	case err == nil:
		return ExcludedPrefix + "on the list " + list, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	if len(x.Tags) == 0 {
		return "", nil
	}
	var self bool
	var tag string
	err = q.QueryRow(ctx, `SELECT r.id = $2::uuid, (SELECT t FROM unnest($3::text[]) t WHERE r.attributes->'tags' ? t LIMIT 1) FROM records r
		WHERE r.workspace_id = $1 AND r.id IN (SELECT $2::uuid UNION ALL SELECT c.to_id FROM record_relationships c
			WHERE c.from_id = $2::uuid AND c.kind = 'companies' AND c.valid_to IS NULL)
		AND jsonb_typeof(r.attributes->'tags') = 'array' AND r.attributes->'tags' ?| $3::text[]
		ORDER BY 1 DESC LIMIT 1`, ws, recordID, x.Tags).Scan(&self, &tag)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	case self:
		return ExcludedPrefix + "tagged " + tag, nil
	}
	return ExcludedPrefix + "their company is tagged " + tag, nil
}

// ExcludedByColumnSQL is ExcludedSQL for an exclusion stored as JSON in a
// column (such as s.exclude).
func ExcludedByColumnSQL(record, column string) string {
	return ExcludedSQL(record, `ARRAY(SELECT jsonb_array_elements_text(`+column+`->'lists'))::uuid[]`,
		`ARRAY(SELECT jsonb_array_elements_text(`+column+`->'tags'))::text[]`)
}
