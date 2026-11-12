package linkorders

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Candidate statuses join a saved link gap to the catalog and open orders.
const (
	CandidateNotInCatalog = "not_in_catalog"
	CandidateInCatalog    = "in_catalog"
	CandidateOrdered      = "ordered"
)

// Candidate is a referring domain from a stored competitor link gap.
// Traffic is not stored on the gap, so it is always nil (the UI shows an em dash).
type Candidate struct {
	Domain      string   `json:"domain"`
	Rating      *float64 `json:"rating"`
	Traffic     *int64   `json:"traffic"`
	Competitors []string `json:"competitors"`
	Status      string   `json:"status"`
	DonorID     *string  `json:"donor_profile_id"`
}

// ListCandidates reads only stored seo_snapshots (report backlinks) and
// live backlinks. It never calls a provider. siteID, when set, limits the
// gap to that SEO site; otherwise every site of the brand is included.
func ListCandidates(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, siteID *uuid.UUID) ([]Candidate, error) {
	gaps, err := savedLinkGaps(ctx, q, workspaceID, brandID, siteID)
	if err != nil {
		return nil, err
	}
	type row struct {
		id     string
		domain string
		open   bool
	}
	rows, err := q.Query(ctx, `
		SELECT d.id::text, lower(pub.domain),
		       EXISTS(
		           SELECT 1 FROM coverage_link_orders o
		           WHERE o.workspace_id = d.workspace_id AND o.brand_id = d.brand_id
		             AND o.donor_profile_id = d.id AND o.status <> $3
		       )
		FROM coverage_donor_profiles d
		JOIN coverage_publishers pub ON pub.id = d.publisher_id
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.archived_at IS NULL`,
		workspaceID, brandID, StatusLost)
	if err != nil {
		return nil, err
	}
	catalog, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		return v, r.Scan(&v.id, &v.domain, &v.open)
	})
	if err != nil {
		return nil, err
	}
	byDomain := map[string]row{}
	for _, c := range catalog {
		byDomain[c.domain] = c
	}
	out := make([]Candidate, 0, len(gaps))
	for _, g := range gaps {
		c := Candidate{Domain: g.Domain, Rating: g.Rating, Competitors: g.Competitors, Status: CandidateNotInCatalog}
		if hit, ok := byDomain[g.Domain]; ok {
			c.DonorID = &hit.id
			if hit.open {
				c.Status = CandidateOrdered
			} else {
				c.Status = CandidateInCatalog
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// DomainInSavedGap reports whether domain is in the stored link gap for
// this brand (optionally one SEO site). Used to refuse add-to-catalog
// for domains the saved gap does not contain.
func DomainInSavedGap(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, domain string, siteID *uuid.UUID) (bool, error) {
	domain = customers.CompanyDomain(domain)
	if domain == "" {
		return false, fmt.Errorf("%w: domain is not a valid website host", ErrInvalid)
	}
	gaps, err := savedLinkGaps(ctx, q, workspaceID, brandID, siteID)
	if err != nil {
		return false, err
	}
	for _, g := range gaps {
		if g.Domain == domain {
			return true, nil
		}
	}
	return false, nil
}

type savedGap struct {
	Domain      string
	Rating      *float64
	Competitors []string
}

func savedLinkGaps(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, siteID *uuid.UUID) ([]savedGap, error) {
	var siteArg any
	if siteID != nil {
		siteArg = *siteID
	}
	siteRows, err := q.Query(ctx, `
		SELECT id, host FROM seo_sites
		WHERE workspace_id = $1 AND brand_id = $2 AND ($3::uuid IS NULL OR id = $3::uuid)`,
		workspaceID, brandID, siteArg)
	if err != nil {
		return nil, err
	}
	type site struct {
		id   uuid.UUID
		host string
	}
	sites, err := pgx.CollectRows(siteRows, func(r pgx.CollectableRow) (site, error) {
		var v site
		return v, r.Scan(&v.id, &v.host)
	})
	if err != nil {
		return nil, err
	}
	if siteID != nil && len(sites) == 0 {
		return nil, ErrNotFound
	}
	if len(sites) == 0 {
		return []savedGap{}, nil
	}

	gaps := map[string]*savedGap{}
	for _, s := range sites {
		if err := collectSiteLinkGaps(ctx, q, workspaceID, s.id, s.host, gaps); err != nil {
			return nil, err
		}
	}
	list := make([]savedGap, 0, len(gaps))
	for _, g := range gaps {
		slices.Sort(g.Competitors)
		list = append(list, *g)
	}
	slices.SortFunc(list, func(a, b savedGap) int {
		if d := len(b.Competitors) - len(a.Competitors); d != 0 {
			return d
		}
		if d := cmpRating(b.Rating, a.Rating); d != 0 {
			return d
		}
		return strings.Compare(a.Domain, b.Domain)
	})
	return list, nil
}

func collectSiteLinkGaps(ctx context.Context, q Querier, workspaceID, siteID uuid.UUID, host string, gaps map[string]*savedGap) error {
	target := seosync.LinkTarget(host)
	compRows, err := q.Query(ctx, `
		SELECT host FROM seo_competitors
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'tracked'
		ORDER BY host`, workspaceID, siteID)
	if err != nil {
		return err
	}
	comps, err := pgx.CollectRows(compRows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(comps) == 0 {
		return nil
	}
	theirs, err := latestBacklinkRows(ctx, q, workspaceID, comps)
	if err != nil {
		return err
	}
	haveRows, err := q.Query(ctx, `
		SELECT DISTINCT source_domain FROM backlinks
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'live'`, workspaceID, siteID)
	if err != nil {
		return err
	}
	have, err := pgx.CollectRows(haveRows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	skip := map[string]bool{}
	for _, d := range have {
		skip[strings.TrimPrefix(strings.ToLower(d), "www.")] = true
	}
	for _, c := range comps {
		skip[strings.TrimPrefix(strings.ToLower(c), "www.")] = true
	}
	for _, t := range theirs {
		d := strings.TrimPrefix(strings.ToLower(t.key), "www.")
		if skip[d] || d == target || strings.HasSuffix(d, "."+target) {
			continue
		}
		g := gaps[d]
		if g == nil {
			g = &savedGap{Domain: d}
			gaps[d] = g
		}
		if !slices.Contains(g.Competitors, t.host) {
			g.Competitors = append(g.Competitors, t.host)
		}
		if cmpRating(t.rating, g.Rating) > 0 {
			g.Rating = t.rating
		}
	}
	return nil
}

type backlinkRow struct {
	host, key string
	rating    *float64
}

// latestBacklinkRows is the stored-snapshot half of GET /seo-competitors/gaps?kind=links:
// the newest day of report=backlinks for each competitor host. It only SELECTs seo_snapshots.
func latestBacklinkRows(ctx context.Context, q Querier, workspaceID uuid.UUID, hosts []string) ([]backlinkRow, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (s.host, s.key) s.host, s.key, s.metrics
		FROM seo_snapshots s
		WHERE s.workspace_id = $1 AND s.report = $2 AND s.host = ANY($3) AND s.key <> ''
		  AND s.day = (SELECT max(day) FROM seo_snapshots x WHERE x.workspace_id = $1 AND x.report = $2 AND x.host = s.host)
		ORDER BY s.host, s.key, s.connector`, workspaceID, connectors.SEOBacklinks, hosts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (backlinkRow, error) {
		var v backlinkRow
		var raw []byte
		err := row.Scan(&v.host, &v.key, &raw)
		var m connectors.SEOMetrics
		_ = json.Unmarshal(raw, &m)
		v.rating = m.DomainRating
		return v, err
	})
}

func cmpRating(a, b *float64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case *a > *b:
		return 1
	case *a < *b:
		return -1
	default:
		return 0
	}
}
