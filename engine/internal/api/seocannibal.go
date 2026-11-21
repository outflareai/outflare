package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/seointel"
)

// Cannibalisation across a brand: every search two or more of its pages
// compete for over the 28 days to the newest stored Search Console day,
// set against the 28 days before, so new searches, ones that resolved and
// ones that gained a page stand out. Each names the page to keep (the one
// a person chose, else the one that targets the search, else the one
// with the most clicks), whether the pages share a language or not, the
// clicks the other pages take, a modeled gain from one page holding the
// search, and its place on the fixes list. Reads only what is stored.

type cannibalCluster struct {
	SiteID      string                   `json:"site_id"`
	Host        string                   `json:"host"`
	Query       string                   `json:"query"`
	Clicks      int                      `json:"clicks"`
	Impressions int                      `json:"impressions"`
	Pages       []seointel.CompetingPage `json:"pages"`
	Keep        string                   `json:"keep"`
	// chosen (a person picked it), target or clicks.
	KeepReason string `json:"keep_reason"`
	// new, ongoing, gained_page; resolved ones are listed apart.
	State         string `json:"state"`
	PreviousPages int    `json:"previous_pages"`
	// same_locale when every page is in one language, else cross_locale.
	Locale string `json:"locale"`
	// Clicks that went to the pages other than the one to keep.
	SplitClicks int `json:"split_clicks"`
	// Clicks if the page to keep got every impression at its own CTR,
	// less the clicks now; 0 when it would not gain.
	EstimatedGain int        `json:"estimated_gain"`
	Fix           *fixMarker `json:"fix"`
}

type fixMarker struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type resolvedCluster struct {
	SiteID string `json:"site_id"`
	Host   string `json:"host"`
	Query  string `json:"query"`
	Pages  int    `json:"pages"`
}

// localePath is a path's first segment when it names a language, such as
// de or pt-br.
var localePath = regexp.MustCompile(`^/([a-z]{2}(?:-[a-z]{2})?)(?:/|$)`)

func pageLocale(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if m := localePath.FindStringSubmatch(strings.ToLower(u.Path)); m != nil {
		return m[1]
	}
	return ""
}

func (s *Server) seoCannibalisation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := accessFrom(ctx)
	ws, brand := a.WorkspaceID, a.Brand
	var name string
	if err := s.pool.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&name); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, host FROM seo_sites WHERE workspace_id = $1 AND brand_id = $2 ORDER BY host`, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type site struct{ id, host string }
	var sites []site
	for rows.Next() {
		var x site
		if err := rows.Scan(&x.id, &x.host); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		sites = append(sites, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	clusters := []cannibalCluster{}
	resolved := []resolvedCluster{}
	var newest *string
	for _, st := range sites {
		src, err := seointel.SourceForBrand(ctx, s.pool, ws, brand, st.host)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if src == nil || src.NewestDay == nil {
			continue
		}
		if newest == nil || *src.NewestDay > *newest {
			newest = src.NewestDay
		}
		cur, err := seointel.Cannibalised(ctx, s.pool, ws, st.id, st.host, name, src, maxCannibalReport)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		// The 28 days before: the same reading, ending 28 days earlier.
		end, err := time.Parse(time.DateOnly, *src.NewestDay)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		earlier := *src
		before := end.AddDate(0, 0, -28).Format(time.DateOnly)
		earlier.NewestDay = &before
		prev, err := seointel.Cannibalised(ctx, s.pool, ws, st.id, st.host, name, &earlier, maxCannibalReport)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		prevPages := map[string]int{}
		for _, c := range prev {
			prevPages[c.Query] = len(c.Pages)
		}
		chosen, err := s.cannibalPrimaries(ctx, ws, st.id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		fixesBy, err := s.cannibalFixes(ctx, ws, st.id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		seen := map[string]bool{}
		for _, c := range cur {
			seen[c.Query] = true
			k := cannibalCluster{SiteID: st.id, Host: st.host, Query: c.Query, Clicks: c.Clicks, Impressions: c.Impressions, Pages: c.Pages, Keep: c.Keep, KeepReason: "clicks"}
			for _, p := range c.Pages {
				if p.Target && p.URL == c.Keep {
					k.KeepReason = "target"
				}
			}
			if u, ok := chosen[c.Query]; ok && slices.ContainsFunc(c.Pages, func(p seointel.CompetingPage) bool { return p.URL == u }) {
				k.Keep, k.KeepReason = u, "chosen"
			}
			n, was := prevPages[c.Query]
			switch {
			case !was:
				k.State = "new"
			case len(c.Pages) > n:
				k.State, k.PreviousPages = "gained_page", n
			default:
				k.State, k.PreviousPages = "ongoing", n
			}
			locales := map[string]bool{}
			var keep *seointel.CompetingPage
			for i := range c.Pages {
				locales[pageLocale(c.Pages[i].URL)] = true
				if c.Pages[i].URL == k.Keep {
					keep = &c.Pages[i]
				} else {
					k.SplitClicks += c.Pages[i].Clicks
				}
			}
			k.Locale = "same_locale"
			if len(locales) > 1 {
				k.Locale = "cross_locale"
			}
			if keep != nil && keep.Impressions > 0 {
				ctr := float64(keep.Clicks) / float64(keep.Impressions)
				if g := int(float64(c.Impressions)*ctr) - c.Clicks; g > 0 {
					k.EstimatedGain = g
				}
			}
			k.Fix = fixesBy[c.Query]
			clusters = append(clusters, k)
		}
		for _, c := range prev {
			if !seen[c.Query] {
				resolved = append(resolved, resolvedCluster{SiteID: st.id, Host: st.host, Query: c.Query, Pages: len(c.Pages)})
			}
		}
	}
	summary := map[string]int{"open": len(clusters), "new": 0, "gained_page": 0, "resolved": len(resolved),
		"on_fixes": 0, "not_on_fixes": 0, "three_or_more": 0, "cross_locale": 0, "split_clicks": 0, "estimated_gain": 0}
	for _, c := range clusters {
		if c.State != "ongoing" {
			summary[c.State]++
		}
		if c.Fix != nil {
			summary["on_fixes"]++
		} else {
			summary["not_on_fixes"]++
		}
		if len(c.Pages) >= 3 {
			summary["three_or_more"]++
		}
		if c.Locale == "cross_locale" {
			summary["cross_locale"]++
		}
		summary["split_clicks"] += c.SplitClicks
		summary["estimated_gain"] += c.EstimatedGain
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"search_through": newest,
		"clusters":       clusters,
		"resolved":       resolved,
		"summary":        summary,
		"notes": []string{
			"A search competes when two or more pages each had at least a tenth of its impressions (at least 10) at an average position of 30 or better, over the 28 days to the newest Search Console day, with at least 100 impressions in all. Searches naming the brand are left out.",
			"New: not competing in the 28 days before. Gained a page: one more page competes than before. Resolved: competing before, not now.",
			"The page to keep is the one you chose, else the one that targets the search, else the one with the most clicks.",
			"Split clicks went to the other pages: an estimate of what is shared, not of what is lost. The estimated gain is the clicks if the page to keep got every impression at its own CTR, less the clicks now.",
			"Cross-locale clusters mix languages (such as / and /de/): often hreflang or targeting, not duplicate content.",
		},
	})
}

// maxCannibalReport is how many competing searches a site reports.
const maxCannibalReport = 200

func (s *Server) cannibalPrimaries(ctx context.Context, ws, site string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT query, url FROM seo_cannibal_primaries WHERE workspace_id = $1 AND site_id = $2`, ws, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var q, u string
		if err := rows.Scan(&q, &u); err != nil {
			return nil, err
		}
		out[q] = u
	}
	return out, rows.Err()
}

func (s *Server) cannibalFixes(ctx context.Context, ws, site string) (map[string]*fixMarker, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (group_key) group_key, id::text, status FROM fixes
		WHERE workspace_id = $1 AND subject_id = $2 AND rule = 'keyword_cannibalised' AND status <> 'ignored'
		ORDER BY group_key, (status IN ('open', 'in_progress')) DESC, created_at DESC`, ws, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*fixMarker{}
	for rows.Next() {
		var q string
		var f fixMarker
		if err := rows.Scan(&q, &f.ID, &f.Status); err != nil {
			return nil, err
		}
		out[q] = &f
	}
	return out, rows.Err()
}

// setCannibalPrimary sets or clears (an empty url) the page to keep for a
// search on one of the brand's sites.
func (s *Server) setCannibalPrimary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Query string `json:"query"`
		URL   string `json:"url"`
	}
	if !decode(w, r, &in) {
		return
	}
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	q := seointel.Keyword(in.Query)
	if q == "" || len(q) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "query is 1 to 500 characters")
		return
	}
	page := ""
	if strings.TrimSpace(in.URL) != "" {
		u, ok := targetURL(site, in.URL)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "url is a page of the site ("+site.Host+")")
			return
		}
		page = u
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var actor *string
		if a.ActorID != "" {
			actor = &a.ActorID
		}
		if page == "" {
			if _, err := tx.Exec(ctx, `DELETE FROM seo_cannibal_primaries WHERE workspace_id = $1 AND site_id = $2 AND query = $3`, a.WorkspaceID, site.ID, q); err != nil {
				return err
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_cannibal_primary.cleared", "seo_site", site.ID, nil)
		}
		if site.BrandID == nil {
			return errNoBrand
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO seo_cannibal_primaries (workspace_id, brand_id, site_id, query, url, set_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (site_id, query) DO UPDATE SET url = EXCLUDED.url, set_by = EXCLUDED.set_by, updated_at = now()`,
			a.WorkspaceID, *site.BrandID, site.ID, q, page, actor); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_cannibal_primary.set", "seo_site", site.ID, nil)
	})
	if errors.Is(err, errNoBrand) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the site belongs to no brand")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "url": page})
}

var errNoBrand = errors.New("site without a brand")
