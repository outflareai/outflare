package seosync

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/fixes"
)

// Link opportunities: what the backlinks, their spam scores, the site's
// latest crawl and the competitors' links say is worth doing, as fixes on
// the workspace's fixes list (source seo_links). They are suggestions for
// people and their agents: Outflare never contacts a site, asks for a link
// or disavows anything on its own.

// LinkFixSource is the fixes list's source for link opportunities.
const LinkFixSource = "seo_links"

// LinkRules is the link opportunities' catalog. Ids are a promise: never
// rename or remove one, add new ones.
var LinkRules = []crawl.Rule{
	{ID: "link_reclaim", Severity: crawl.SevWarning, Category: "links", Title: "Strong link lost",
		Why: "Links from strong sites carry the most weight, and a lost one is often a moved page or a removed mention that is easy to win back.",
		Fix: "See why it went on the Backlinks page. If your page moved, redirect the old URL to the new one; otherwise ask the site's owner whether they would restore it."},
	{ID: "link_broken_target", Severity: crawl.SevError, Category: "links", Title: "Links point to a broken page",
		Why: "Other sites link to a page of yours that no longer works, so visitors hit an error and the links' value is lost.",
		Fix: "Redirect the URL (301) to the page that best replaces it, or bring the page back."},
	{ID: "link_gap_domain", Severity: crawl.SevNotice, Category: "links", Title: "Links to your competitors, not to you",
		Why: "A strong site that links to several of your competitors writes about your topic and may link to you too.",
		Fix: "Look at what it links to at your competitors. If you have something as useful, it may be worth getting in touch, or publishing what is missing."},
	{ID: "toxic_links", Severity: crawl.SevWarning, Category: "links", Title: "Toxic links to review",
		Why: "Links that look like spam can make search engines trust a site less if there are many of them.",
		Fix: "Review them on the Backlinks page: keep the real ones, mark the spam to disavow, then upload the disavow file in Google Search Console yourself."},
}

var linkRuleByID = func() map[string]crawl.Rule {
	m := make(map[string]crawl.Rule, len(LinkRules))
	for _, r := range LinkRules {
		m[r.ID] = r
	}
	return m
}()

// LinkRuleFor looks a link rule up by id.
func LinkRuleFor(id string) (crawl.Rule, bool) { r, ok := linkRuleByID[id]; return r, ok }

// Thresholds for link opportunities.
const (
	// ReclaimRating is the weakest linking domain a lost link is worth
	// winning back from, and ReclaimDays how long it stays worth it.
	ReclaimRating = 30
	ReclaimDays   = 90
	// GapRating is the weakest domain a link gap is suggested for, and
	// GapCompetitors how many tracked competitors it must link to.
	GapRating      = 30
	GapCompetitors = 2
	// MaxLinkFixes caps the fixes one rule adds a site, strongest first.
	MaxLinkFixes = 25
)

// SyncLinkFixes works out a site's link opportunities and puts them on the
// fixes list. runID is the backlinks read that prompted it (or a fresh id
// after someone reviewed links); reopen opens again what someone marked
// done when a links read still finds it.
func SyncLinkFixes(ctx context.Context, tx pgx.Tx, ws string, site LinkSite, runID string, reopen bool) (fixes.Changes, error) {
	var found []fixes.Found
	add := func(rule, sev, unit, query string, args ...any) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		var group string
		var count int
		_, err = pgx.ForEachRow(rows, []any{&group, &count}, func() error {
			found = append(found, fixes.Found{Rule: rule, Severity: sev, Group: group, Count: count, Unit: unit})
			return nil
		})
		return err
	}
	// Strong links lost lately, by linking domain, unless the domain still
	// links to the site or someone marked it spam.
	if err := add("link_reclaim", crawl.SevWarning, "links", `
		SELECT b.source_domain, count(*)::int FROM backlinks b
		WHERE b.workspace_id = $1 AND b.site_id = $2 AND b.status = 'lost' AND b.review <> 'disavow'
		  AND b.source_rating >= $3 AND b.lost_at >= now() - make_interval(days => $4)
		  AND NOT EXISTS (SELECT 1 FROM backlinks l WHERE l.workspace_id = $1 AND l.site_id = $2 AND l.status = 'live' AND l.source_domain = b.source_domain)
		GROUP BY b.source_domain ORDER BY max(b.source_rating) DESC, b.source_domain LIMIT $5`,
		ws, site.ID, ReclaimRating, ReclaimDays, MaxLinkFixes); err != nil {
		return fixes.Changes{}, err
	}
	// Live links to pages the latest complete crawl found broken.
	if err := add("link_broken_target", crawl.SevError, "links", `
		WITH run AS (SELECT id FROM crawl_runs WHERE workspace_id = $1 AND site_id = $2 AND status = 'done' ORDER BY finished_at DESC NULLS LAST LIMIT 1),
		broken AS (
			SELECT DISTINCT rtrim(p.url, '/') AS url FROM crawl_pages p JOIN run ON run.id = p.run_id
			WHERE p.workspace_id = $1 AND p.http_status >= 400
		)
		SELECT b.target_url, count(*)::int FROM backlinks b JOIN broken k ON k.url = rtrim(b.target_url, '/')
		WHERE b.workspace_id = $1 AND b.site_id = $2 AND b.status = 'live' AND b.review <> 'disavow'
		GROUP BY b.target_url ORDER BY count(*) DESC, b.target_url LIMIT $3`,
		ws, site.ID, MaxLinkFixes); err != nil {
		return fixes.Changes{}, err
	}
	// Strong domains linking to several tracked competitors, in their
	// latest snapshots, that don't link to the site.
	target := LinkTarget(site.Host)
	if err := add("link_gap_domain", crawl.SevNotice, "competitors", `
		WITH comp AS (SELECT host FROM seo_competitors WHERE workspace_id = $1 AND site_id = $2 AND status = 'tracked'),
		rows AS (
			SELECT regexp_replace(lower(s.key), '^www\.', '') AS domain, s.host, (s.metrics->>'domain_rating')::float AS rating
			FROM seo_snapshots s JOIN comp ON comp.host = s.host
			WHERE s.workspace_id = $1 AND s.report = 'backlinks' AND s.key <> ''
			  AND s.day = (SELECT max(day) FROM seo_snapshots x WHERE x.workspace_id = $1 AND x.report = 'backlinks' AND x.host = s.host)
		)
		SELECT domain, count(DISTINCT host)::int FROM rows
		WHERE domain <> $3 AND domain NOT LIKE '%.' || $3 AND domain NOT IN (SELECT host FROM comp)
		  AND domain NOT IN (SELECT source_domain FROM backlinks WHERE workspace_id = $1 AND site_id = $2 AND status = 'live')
		GROUP BY domain HAVING count(DISTINCT host) >= $4 AND max(rating) >= $5
		ORDER BY count(DISTINCT host) DESC, max(rating) DESC, domain LIMIT $6`,
		ws, site.ID, target, GapCompetitors, GapRating, MaxLinkFixes); err != nil {
		return fixes.Changes{}, err
	}
	// Toxic links nobody reviewed yet, as one fix for the site.
	if err := add("toxic_links", crawl.SevWarning, "links", `
		SELECT '', count(*)::int FROM backlinks
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'live' AND spam_score >= $3 AND review = ''
		HAVING count(*) > 0`, ws, site.ID, SpamToxic); err != nil {
		return fixes.Changes{}, err
	}
	rules := make([]string, len(LinkRules))
	for i, r := range LinkRules {
		rules[i] = r.ID
	}
	return fixes.Sync(ctx, tx, fixes.Run{Workspace: ws, Source: LinkFixSource, SubjectType: "seo_site", SubjectID: site.ID,
		BrandID: site.BrandID, RunID: runID, Rules: rules, Reopen: reopen}, found)
}
