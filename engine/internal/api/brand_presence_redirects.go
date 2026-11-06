package api

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/coverage"
)

// googleRedirectSQL matches a Google redirect link (google.com/goto?url=...)
// that a search result brought in instead of the page itself.
const googleRedirectSQL = `url ~* '^https?://(www\.)?google\.[a-z.]+/(url|goto|aclk|link)\?'`

// coverageResolveRedirects replaces the Google redirect links among a
// brand's unreviewed finds with the pages they lead to. A link that can't
// be followed, or leads to a page already on the list, is removed: nobody
// has reviewed it, and its source is remembered, so it doesn't come back.
// It reports how many it fixed and removed.
func (s *Server) coverageResolveRedirects(ctx context.Context, a *access) (fixed, removed int, err error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,url FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND status='new' AND `+googleRedirectSQL+` ORDER BY created_at LIMIT 300`, a.WorkspaceID, a.Brand)
	if err != nil {
		return 0, 0, err
	}
	var ids, urls []string
	for rows.Next() {
		var id, u string
		if err = rows.Scan(&id, &u); err != nil {
			rows.Close()
			return 0, 0, err
		}
		ids, urls = append(ids, id), append(urls, u)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(ids) == 0 {
		return 0, 0, err
	}
	var client *connectors.Client
	if s.integrations != nil {
		client = s.integrations.Client
	}
	pages := connectors.ResolveGoogleRedirects(ctx, client, urls)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for i, id := range ids {
			page := ""
			if pages[i] != "" && !connectors.IsGoogleRedirect(pages[i]) {
				if u, e := coverageURL(pages[i]); e == nil {
					page = u
				}
			}
			if page != "" {
				var taken bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND url=$3)`, a.WorkspaceID, a.Brand, page).Scan(&taken); err != nil {
					return err
				}
				if !taken {
					if _, err := tx.Exec(ctx, `UPDATE coverage_candidates SET url=$4,kind=CASE WHEN kind='' THEN $5 ELSE kind END,version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`,
						a.WorkspaceID, a.Brand, id, page, coverage.Classify(page)); err != nil {
						return err
					}
					if err := coverageHistory(ctx, tx, a, "candidates", id, "resolved", map[string]any{"from": "google_redirect"}); err != nil {
						return err
					}
					fixed++
					continue
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id); err != nil {
				return err
			}
			removed++
		}
		return recordWithProps(ctx, tx, coverageActor(a), a.WorkspaceID, "coverage_discovery.redirects_resolved", "brand", a.Brand, nil,
			map[string]any{"brand_id": a.Brand, "fixed": fixed, "removed": removed})
	})
	return fixed, removed, err
}
