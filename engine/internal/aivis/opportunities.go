package aivis

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage"
)

// Opportunities: the pages AI answers to a brand's questions cite, and the
// ones the assistants read but didn't cite, are places worth being on.
// After each run they go into Presence's one list of opportunities
// (coverage.Suggest, reason ai_cited), scored by how many answers cited
// them and how many competitors those answers named. Pages the brand's
// answers already name it on every time are left out.

const (
	// opportunityDays is how far back citations count.
	opportunityDays = 90
	// maxCitedOpportunities and maxReadOpportunities are how many pages one
	// run suggests of each.
	maxCitedOpportunities = 20
	maxReadOpportunities  = 10
	// minReads is how many answers must have read a page it didn't cite.
	minReads = 2
)

// OpportunityChange is what one run did to the list.
type OpportunityChange struct{ New, Refreshed int }

func syncOpportunities(ctx context.Context, tx pgx.Tx, ws, cohortID, runID string) (OpportunityChange, error) {
	var out OpportunityChange
	var brand *string
	if err := tx.QueryRow(ctx, `SELECT brand_id::text FROM ai_cohorts WHERE id = $1 AND workspace_id = $2`, cohortID, ws).Scan(&brand); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		return out, err
	}
	if brand == nil {
		return out, nil // the company's own cohort: Presence lists brands' places
	}
	type page struct {
		url, title         string
		answers, unnamed   int
		competitors, reads int
		cited              bool
	}
	// Pages the cohort's answers cited, with how many of those answers
	// didn't name the brand and how many competitors they named.
	rows, err := tx.Query(ctx, `
		SELECT c.url, max(c.title), count(DISTINCT c.sample_id),
		       count(DISTINCT c.sample_id) FILTER (WHERE NOT EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = c.sample_id AND m.entity = 'brand')),
		       (SELECT count(DISTINCT m.name) FROM ai_mentions m JOIN ai_citations c2 ON c2.sample_id = m.sample_id
		        WHERE c2.workspace_id = $1 AND c2.cohort_id = $2 AND c2.url = c.url AND m.entity = 'competitor' AND c2.sampled_at >= now() - make_interval(days => $3))
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND c.cohort_id = $2 AND c.owner = 'other' AND s.status = 'ok' AND c.sampled_at >= now() - make_interval(days => $3)
		GROUP BY c.url
		ORDER BY count(DISTINCT c.sample_id) DESC, c.url LIMIT $4`, ws, cohortID, opportunityDays, maxCitedOpportunities*2)
	if err != nil {
		return out, err
	}
	pages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (page, error) {
		p := page{cited: true}
		return p, row.Scan(&p.url, &p.title, &p.answers, &p.unnamed, &p.competitors)
	})
	if err != nil {
		return out, err
	}
	// Pages the assistants read and never cited.
	rows, err = tx.Query(ctx, `
		SELECT r.url, max(r.title), count(DISTINCT r.sample_id)
		FROM ai_pages_read r JOIN ai_samples s ON s.id = r.sample_id
		WHERE r.workspace_id = $1 AND s.cohort_id = $2 AND r.owner = 'other' AND s.status = 'ok' AND s.sampled_at >= now() - make_interval(days => $3)
		  AND NOT EXISTS (SELECT 1 FROM ai_citations c WHERE c.workspace_id = $1 AND c.cohort_id = $2 AND c.url = r.url)
		GROUP BY r.url HAVING count(DISTINCT r.sample_id) >= $4
		ORDER BY count(DISTINCT r.sample_id) DESC, r.url LIMIT $5`, ws, cohortID, opportunityDays, minReads, maxReadOpportunities)
	if err != nil {
		return out, err
	}
	read, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (page, error) {
		var p page
		return p, row.Scan(&p.url, &p.title, &p.reads)
	})
	if err != nil {
		return out, err
	}
	cited := 0
	for _, p := range append(pages, read...) {
		ev := map[string]any{"cohort_id": cohortID, "run_id": runID}
		f := coverage.Factors{}
		if p.cited {
			// Every answer citing it named the brand already: it is there.
			if p.unnamed == 0 || cited >= maxCitedOpportunities {
				continue
			}
			cited++
			ev["answers_citing"], ev["answers_without_brand"], ev["state"] = p.answers, p.unnamed, "cited"
			f.AICitations, f.Competitors = p.answers, p.competitors
		} else {
			ev["answers_reading"], ev["state"] = p.reads, "read_not_cited"
		}
		_, created, err := coverage.Suggest(ctx, tx, ws, *brand, coverage.Suggestion{URL: p.url, Title: p.title, Reason: "ai_cited", Source: "ai_visibility", Evidence: ev, Factors: f})
		if errors.Is(err, coverage.ErrNoURL) {
			continue
		}
		if err != nil {
			return out, err
		}
		if created {
			out.New++
		} else {
			out.Refreshed++
		}
	}
	return out, nil
}
