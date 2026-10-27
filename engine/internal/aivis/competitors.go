package aivis

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// MaxCompetitors is how many competitors a run counts.
const MaxCompetitors = 25

// SEOCompetitors reads the competitors the workspace tracks for a brand's
// SEO sites (the company's when brand is nil), so AI visibility counts the
// same list as the rest of SEO. Hosts of one company (the same customer
// record, or the same name) are one competitor with several domains.
func SEOCompetitors(ctx context.Context, q rowsQuerier, ws string, brand *string) ([]Entity, error) {
	rows, err := q.Query(ctx, `
		SELECT COALESCE(record_id::text, ''), name, host, aliases FROM seo_competitors
		WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid AND status = 'tracked'
		ORDER BY common_keywords DESC NULLS LAST, created_at, host`, ws, brand)
	if err != nil {
		return nil, err
	}
	out := []Entity{}
	index := map[string]int{}
	named := []bool{}
	var record, name, host string
	var aliases []string
	_, err = pgx.ForEachRow(rows, []any{&record, &name, &host, &aliases}, func() error {
		name = strings.TrimSpace(name)
		keys := []string{}
		if record != "" {
			keys = append(keys, "r:"+record)
		}
		if name != "" {
			keys = append(keys, "n:"+strings.ToLower(name))
		}
		keys = append(keys, "h:"+host)
		i, found := -1, false
		for _, k := range keys {
			if i, found = index[k]; found {
				break
			}
		}
		if !found {
			if len(out) == MaxCompetitors {
				return nil
			}
			label := name
			if label == "" {
				label = host
			}
			out = append(out, Entity{Name: label, Domains: []string{}, Aliases: []string{}})
			named = append(named, name != "")
			i = len(out) - 1
		}
		e := &out[i]
		if !named[i] && name != "" {
			e.Name, named[i] = name, true // a later host of the company has its name
		}
		e.Domains = dedupe(append(e.Domains, host))
		e.Aliases = dedupe(append(e.Aliases, aliases...))
		for _, k := range keys {
			index[k] = i
		}
		return nil
	})
	return out, err
}

// MergeCompetitors puts a cohort's own competitors first and adds the
// tracked ones it doesn't already name or share a domain with, up to
// MaxCompetitors.
func MergeCompetitors(own, tracked []Entity) []Entity {
	out := append([]Entity{}, own...)
	names, hosts := map[string]bool{}, map[string]bool{}
	for _, e := range own {
		names[strings.ToLower(e.Name)] = true
		for _, a := range e.Aliases {
			names[strings.ToLower(a)] = true
		}
		for _, d := range e.Domains {
			hosts[strings.ToLower(Host(d))] = true
		}
	}
	for _, e := range tracked {
		if len(out) >= MaxCompetitors {
			break
		}
		dup := names[strings.ToLower(e.Name)]
		for _, d := range e.Domains {
			dup = dup || hosts[strings.ToLower(Host(d))]
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out
}
