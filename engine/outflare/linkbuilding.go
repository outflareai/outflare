package outflare

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Link building for extensions: the workspace's SEO sites with what their
// brand is about, SEO reports on the workspace's own SEO account, and
// adding pages to the link prospect list. Like the rest of the extension
// API, every paid call runs on the workspace's own connection.

// CapSEO finds the workspace's SEO data account (Ahrefs, DataForSEO) with
// Connections.Open.
const CapSEO = connectors.CapSEO

// SEO reports and what they hold.
type (
	SEOQuery   = connectors.SEOQuery
	SEOReport  = connectors.SEOReport
	SEORow     = connectors.SEORow
	SEOLink    = connectors.SEOLink
	SEOMetrics = connectors.SEOMetrics
)

// The reports ReadSEO answers.
const (
	SEOOverview  = connectors.SEOOverview
	SEOKeywords  = connectors.SEOKeywords
	SEOBacklinks = connectors.SEOBacklinks
	SEOLinks     = connectors.SEOLinks
)

// ErrSEOBudget means a read would take the connection over its monthly SEO
// budget or a usage limit (errors.As with *SEOBudgetError).
type SEOBudgetError = seosync.BudgetError

// ReadSEO answers one SEO report about a domain on the connection, from a
// copy under a day old when there is one. It checks the connection's SEO
// budget first and books what the call cost, as the engine's own SEO
// reads do. A connection without the report answers an error.
func (c *Connection) ReadSEO(ctx context.Context, q SEOQuery) (SEOReport, error) {
	r, ok := c.o.Connector.(connectors.SEOReader)
	if !ok {
		return SEOReport{}, ErrNotConnected
	}
	q, err := connectors.CheckSEOQuery(q)
	if err != nil {
		return SEOReport{}, err
	}
	a, err := seosync.Fetch(ctx, c.h.Pool, c.o, r, q, seosync.Options{})
	return a.SEOReport, err
}

// LinkSite is one of the workspace's SEO sites, which link prospects point
// at, with what its brand is about for judging what fits it.
type LinkSite struct {
	ID      string
	Host    string
	BrandID *string
	// The brand's name, what it is and does, and who it is for; empty for
	// a site shared by every brand.
	BrandName, About, Audience string
	// Competitors are the domains tracked as the site's competitors, and
	// Keywords the search terms tracked for it (at most 50).
	Competitors []string
	Keywords    []string
}

// LinkSites lists the workspace's SEO sites of brand (nil for every
// brand). It is for background jobs: workspaceID and brand must come from
// a row the job already holds for that workspace. Request handlers use
// LinkSitesFor.
func (h *Host) LinkSites(ctx context.Context, workspaceID string, brand *string) ([]LinkSite, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT s.id::text, s.host, s.brand_id::text, COALESCE(b.name, ''), COALESCE(bc.about, ''), COALESCE(bc.audience, ''),
		       ARRAY(SELECT c.host FROM seo_competitors c WHERE c.site_id = s.id AND c.workspace_id = s.workspace_id
		             AND c.status = 'tracked' ORDER BY c.host),
		       ARRAY(SELECT k.keyword FROM tracked_keywords k WHERE k.site_id = s.id AND k.workspace_id = s.workspace_id
		             ORDER BY k.created_at LIMIT 50)
		FROM seo_sites s
		LEFT JOIN brands b ON b.id = s.brand_id AND b.workspace_id = s.workspace_id
		LEFT JOIN brand_context bc ON bc.brand_id = s.brand_id AND bc.workspace_id = s.workspace_id
		WHERE s.workspace_id = $1 AND ($2::uuid IS NULL OR s.brand_id = $2)
		ORDER BY s.host`, workspaceID, brand)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (LinkSite, error) {
		var s LinkSite
		err := row.Scan(&s.ID, &s.Host, &s.BrandID, &s.BrandName, &s.About, &s.Audience, &s.Competitors, &s.Keywords)
		return s, err
	})
}

// LinkSitesFor is LinkSites in the caller's workspace, keeping only the
// sites of brands the caller sees.
func (h *Host) LinkSitesFor(ctx context.Context, c *Caller, brand *string) ([]LinkSite, error) {
	all, err := h.LinkSites(ctx, c.WorkspaceID, brand)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(s LinkSite) bool { return !c.Sees(s.BrandID) }), nil
}

// LinkProspect is a page to add to a site's link prospect list, and
// LinkProspectsAdded what adding them did.
type (
	LinkProspect       = seosync.Prospect
	LinkProspectsAdded = seosync.ProspectsAdded
)

// ErrBadProspect means a page can't be a prospect: not a web page, on the
// site itself, or found by a method the engine doesn't know.
var ErrBadProspect = errors.New("not a link prospect")

// AddLinkProspects adds pages to the list of the caller's site siteID in
// tx, found by source (such as "expired_domains"). Each new one is
// recorded as link_prospect.found by the caller. Pages already on the
// list, or on a site that is a competitor, customer, partner or opted out,
// are kept out as the engine's own adds are. A page's TargetURL stays
// empty: a person picks the page of ours to offer. The caller must be able to
// change the site's brand (ErrNotYours otherwise).
func (h *Host) AddLinkProspects(ctx context.Context, tx pgx.Tx, c *Caller, siteID, source string, list []LinkProspect) (LinkProspectsAdded, error) {
	if len(list) > seosync.MaxProspectsAdded {
		return LinkProspectsAdded{}, fmt.Errorf("%w: at most %d pages at once", ErrBadProspect, seosync.MaxProspectsAdded)
	}
	var site seosync.ProspectSite
	err := tx.QueryRow(ctx, `SELECT id::text, brand_id::text, host FROM seo_sites WHERE workspace_id = $1 AND id::text = $2`,
		c.WorkspaceID, siteID).Scan(&site.ID, &site.BrandID, &site.Host)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !c.Sees(site.BrandID)) {
		return LinkProspectsAdded{}, fmt.Errorf("%w: no such site", ErrBadProspect)
	}
	if err != nil {
		return LinkProspectsAdded{}, err
	}
	if !c.Writes(site.BrandID) {
		return LinkProspectsAdded{}, ErrNotYours
	}
	clean := make([]seosync.Prospect, len(list))
	for i, p := range list {
		page, d := seosync.ProspectPage(p.PageURL)
		if page == "" || len(page) > 2048 || seosync.OwnDomain(site, d) || !slices.Contains(seosync.ProspectMethods, p.Method) || p.TargetURL != "" {
			return LinkProspectsAdded{}, fmt.Errorf("%w: %d", ErrBadProspect, i)
		}
		p.PageURL = page
		clean[i] = p
	}
	return c.AddLinkProspects(ctx, tx, site, source, clean)
}
