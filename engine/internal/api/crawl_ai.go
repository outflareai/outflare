package api

import (
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// AI crawlability of a crawled site: what robots.txt lets each AI
// crawler read, and whether the site has an llms.txt. The audit turns the
// problems into rules (ai_search_blocked, llms_txt_missing) on the fixes
// list; this is the whole picture, crawler by crawler.

type crawlAIBotView struct {
	crawl.AIBot
	RootAllowed  bool `json:"root_allowed"`
	Pages        int  `json:"pages"`
	BlockedPages int  `json:"blocked_pages"`
}

func (s *Server) getCrawlAIAccess(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	run, _, ok := s.crawlFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	var status *int
	var size int
	if err := s.pool.QueryRow(ctx, `SELECT llms_txt_status, llms_txt_bytes FROM crawl_runs WHERE id = $1 AND workspace_id = $2`, run.ID, a.WorkspaceID).
		Scan(&status, &size); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT token, root_allowed, pages, blocked_pages FROM crawl_ai_bots WHERE run_id = $1 AND workspace_id = $2`, run.ID, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	got := map[string]crawlAIBotView{}
	var v crawlAIBotView
	if _, err := pgx.ForEachRow(rows, []any{&v.Token, &v.RootAllowed, &v.Pages, &v.BlockedPages}, func() error {
		got[v.Token] = v
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	// In the catalog's order; a crawl from before these checks has none.
	bots := []crawlAIBotView{}
	for _, b := range crawl.AIBots {
		if g, ok := got[b.Token]; ok {
			g.AIBot = b
			bots = append(bots, g)
		}
	}
	searchBlocked := slices.ContainsFunc(bots, func(b crawlAIBotView) bool { return b.Purpose != "training" && b.BlockedPages > 0 })
	writeJSON(w, http.StatusOK, map[string]any{
		"crawl_id":       run.ID,
		"checked":        len(bots) > 0,
		"llms_txt":       map[string]any{"status": status, "found": status != nil && *status == 200, "bytes": size},
		"search_blocked": searchBlocked,
		"bots":           bots,
	})
}
