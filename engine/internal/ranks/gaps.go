package ranks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Gap results: the results pages of a site's keyword gaps (keywords its
// tracked competitors rank in the top 20 for), read on the workspace's
// own SERP connection within its budget. They are kept, so a person can
// open a gap keyword and see who ranks for it, competitor or not, and the
// domains that rank across several of them are suggested as competitors
// (source keyword_gaps) to say Yes or No to. Read once a day for the
// gaps not read in the last week, or when someone asks.

const (
	JobGapSERPs = "seo.gap_serps"
	// GapDepth is how many results a gap read keeps: the first two pages.
	GapDepth = 20
	// gapTop is the best position a tracked competitor needs on a keyword
	// for it to count as a gap (as on the Keyword gaps tab).
	gapTop = 20
	// GapKeywordsPerRun is how many gap keywords one run reads, the ones
	// most competitors rank for first.
	GapKeywordsPerRun = 25
	// gapFresh is how long a read result stands before it is read again.
	gapFresh = 7 * 24 * time.Hour
	// gapSuggestions is how many domains one run suggests, and
	// gapMinKeywords how many read gap keywords a domain must rank for.
	gapSuggestions = 25
	gapMinKeywords = 2
)

type gapPayload struct {
	Site string `json:"site_id"`
	// Keywords reads these gap keywords now, read lately or not.
	Keywords []string `json:"keywords,omitempty"`
}

// EnqueueGapReads asks for a read of a site's gap keywords (or, with
// keywords, of those). key de-duplicates; "" is one a minute.
func EnqueueGapReads(ctx context.Context, q jobs.Querier, ws, siteID string, keywords []string, key string) (int64, error) {
	if key == "" {
		key = fmt.Sprintf("gapserps:%s:now:%d:%s", siteID, time.Now().Unix()/60, strings.Join(keywords, "|"))
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobGapSERPs, WorkspaceID: &ws, Payload: gapPayload{Site: siteID, Keywords: keywords}, IdempotencyKey: key, MaxAttempts: 2})
}

// QueueGapReads queues today's gap read of each active site that tracks
// a competitor whose keywords were read, in a workspace with a SERP
// connection.
func (s *Service) QueueGapReads(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT t.workspace_id::text, t.id::text FROM seo_sites t
		WHERE t.status = 'active'
		  AND EXISTS (SELECT 1 FROM seo_competitors c JOIN seo_snapshots x ON x.workspace_id = c.workspace_id AND x.host = c.host AND x.report = $1
			WHERE c.workspace_id = t.workspace_id AND c.site_id = t.id AND c.status = 'tracked')
		  AND EXISTS (SELECT 1 FROM integrations i WHERE i.workspace_id = t.workspace_id AND i.status = 'connected' AND i.connector = ANY($2))`,
		connectors.SEOKeywords, SERPConnectors(s.Store.Registry))
	if err != nil {
		return 0, err
	}
	type site struct{ ws, id string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (site, error) {
		var v site
		return v, row.Scan(&v.ws, &v.id)
	})
	if err != nil {
		return 0, err
	}
	day := s.now().UTC().Format(time.DateOnly)
	for _, v := range list {
		if _, err := EnqueueGapReads(ctx, s.Pool, v.ws, v.id, nil, "gapserps:"+v.id+":"+day); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// GapRead is what one gap run did.
type GapRead struct {
	Read      int     `json:"read"`
	Suggested int     `json:"suggested"`
	CostUSD   float64 `json:"cost_usd"`
}

type gapKeyword struct{ keyword, country string }

// gapKeywords are the site's gap keywords to read: those asked for, or
// those most tracked competitors rank in the top 20 for, then the most
// searched, that weren't read in the last week. Each comes with the
// country its SEO connection reads (US when it says none).
func (s *Service) gapKeywords(ctx context.Context, ws, siteID string, asked []string) ([]gapKeyword, error) {
	rows, err := s.Pool.Query(ctx, `
		WITH theirs AS (
			SELECT DISTINCT ON (x.host, lower(x.key)) x.host, lower(x.key) AS k, x.key, x.connector, x.country,
				(x.metrics->>'position')::float AS pos, (x.metrics->>'volume')::float AS vol
			FROM seo_competitors c JOIN seo_snapshots x ON x.workspace_id = c.workspace_id AND x.host = c.host AND x.report = $3
			WHERE c.workspace_id = $1 AND c.site_id = $2 AND c.status = 'tracked' AND x.key <> ''
			  AND x.day = (SELECT max(day) FROM seo_snapshots y WHERE y.workspace_id = $1 AND y.report = $3 AND y.host = c.host)
			ORDER BY x.host, lower(x.key), x.connector
		)
		SELECT min(t.key), COALESCE(nullif(upper(min(t.country)), ''), upper(min(i.config->>'country')), 'US')
		FROM theirs t
		LEFT JOIN integrations i ON i.workspace_id = $1 AND i.connector = t.connector AND i.status = 'connected'
		WHERE t.pos IS NOT NULL AND t.pos <= $4 AND (cardinality($5::text[]) = 0 OR t.k = ANY($5))
		  AND (cardinality($5::text[]) > 0 OR NOT EXISTS (SELECT 1 FROM keyword_serps r
			WHERE r.workspace_id = $1 AND r.site_id = $2 AND r.keyword = t.k AND r.read_at > now() - $6::interval
			  AND r.items::text NOT LIKE '%google.%/goto%' AND r.items::text NOT LIKE '%google.%/url?%'))
		GROUP BY t.k
		ORDER BY count(*) DESC, max(t.vol) DESC NULLS LAST, t.k
		LIMIT $7`, ws, siteID, connectors.SEOKeywords, gapTop, lowerAll(asked), fmt.Sprintf("%d seconds", int(gapFresh.Seconds())), GapKeywordsPerRun)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (gapKeyword, error) {
		var g gapKeyword
		return g, row.Scan(&g.keyword, &g.country)
	})
}

func lowerAll(list []string) []string {
	out := make([]string, 0, len(list))
	for _, k := range list {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// ReadGaps reads a site's gap keywords' results pages on the workspace's
// SERP connection, keeps them, then suggests the domains that rank for
// several of the gaps read in the last month.
func (s *Service) ReadGaps(ctx context.Context, ws, siteID string, asked []string) (GapRead, error) {
	var res GapRead
	var site seosync.LinkSite
	if err := s.Pool.QueryRow(ctx, `SELECT id::text, host, brand_id::text FROM seo_sites WHERE id = $1 AND workspace_id = $2 AND status = 'active'`,
		siteID, ws).Scan(&site.ID, &site.Host, &site.BrandID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, nil
		}
		return res, err
	}
	list, err := s.gapKeywords(ctx, ws, siteID, asked)
	if err != nil || len(list) == 0 {
		return res, err
	}
	id, err := GapConnection(ctx, s.Pool, s.Store.Registry, ws)
	if err != nil {
		return res, err
	}
	if id == "" {
		return res, ErrNoConnection
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, id)
	if err != nil {
		return res, err
	}
	var readErr error
	for _, g := range list {
		out, err := Search(ctx, s.Pool, s.Store, o, SearchQuery{SERPQuery: connectors.SERPQuery{Keyword: g.keyword, Country: g.country, Device: "desktop", Depth: GapDepth}})
		res.CostUSD += out.Cost.USD
		if err != nil {
			readErr = err
			var be *seosync.BudgetError
			if errors.As(err, &be) || connectors.KindOf(err) == connectors.ErrAuth || connectors.KindOf(err) == connectors.ErrQuota {
				break
			}
			continue
		}
		items := out.Items[:min(len(out.Items), GapDepth)]
		raw, _ := json.Marshal(items)
		if _, err := s.Pool.Exec(ctx, `INSERT INTO keyword_serps (workspace_id, brand_id, site_id, keyword, country, device, engine, items, integration_id, cost_usd)
			VALUES ($1, $2, $3, $4, $5, 'desktop', $6, $7, $8, $9)
			ON CONFLICT (site_id, keyword, country, device) DO UPDATE SET engine = EXCLUDED.engine, items = EXCLUDED.items,
				integration_id = EXCLUDED.integration_id, cost_usd = EXCLUDED.cost_usd, read_at = now()`,
			ws, site.BrandID, siteID, strings.ToLower(g.keyword), g.country, out.Engine, raw, o.ID, out.Cost.USD); err != nil {
			return res, err
		}
		res.Read++
	}
	n, err := s.suggestFromGaps(ctx, ws, site)
	res.Suggested = n
	if err != nil {
		return res, err
	}
	return res, readErr
}

// GapConnection is the SERP connection keyword gap results are read on:
// Geonode first when connected (its results come back as real pages),
// else the workspace's default.
func GapConnection(ctx context.Context, q jobs.Querier, reg *connectors.Registry, ws string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM integrations WHERE workspace_id = $1 AND connector = 'geonode' AND status = 'connected'
		AND connector = ANY($2) ORDER BY created_at, id LIMIT 1`, ws, SERPConnectors(reg)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultConnection(ctx, q, reg, ws)
	}
	return id, err
}

func (s *Service) suggestFromGaps(ctx context.Context, ws string, site seosync.LinkSite) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT lower(it->>'domain'), count(DISTINCT r.keyword)::int
		FROM keyword_serps r CROSS JOIN LATERAL jsonb_array_elements(r.items) it
		WHERE r.workspace_id = $1 AND r.site_id = $2 AND r.read_at > now() - interval '30 days' AND coalesce(it->>'domain', '') <> ''
		  AND lower(it->>'domain') NOT LIKE 'google.%' AND lower(it->>'domain') NOT LIKE 'www.google.%'
		GROUP BY 1 HAVING count(DISTINCT r.keyword) >= $3 ORDER BY 2 DESC, 1 LIMIT 200`, ws, site.ID, gapMinKeywords)
	if err != nil {
		return 0, err
	}
	hits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (seosync.HostHit, error) {
		var h seosync.HostHit
		return h, row.Scan(&h.Host, &h.Keywords)
	})
	if err != nil {
		return 0, err
	}
	return seosync.SuggestHosts(ctx, s.Pool, ws, site, "keyword_gaps", hits, gapSuggestions)
}

func (s *Service) gapJob(ctx context.Context, job jobs.Job) error {
	var p gapPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	res, err := s.ReadGaps(ctx, ws, p.Site, p.Keywords)
	ev := track.Event{Name: "seo_competitors.gap_results_read", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"site_id": p.Site, "read": res.Read, "suggested": res.Suggested, "asked": len(p.Keywords), "cost_usd": res.CostUSD}}
	if err != nil {
		code := SearchErrorCode(err)
		if errors.Is(err, ErrNoConnection) {
			code = "connector_needed"
		}
		metrics.CompetitorSuggest("keyword_gaps", "error", res.Suggested)
		ev.Outcome = track.Failure
		if code == "connector_unavailable" {
			ev.Outcome = track.Error
		}
		track.Fail(ctx, s.Log, ev, code, fmt.Errorf("gap results of site %s: %w", p.Site, err))
		// What failed is spent or refused; a retry would only spend again.
		return nil
	}
	metrics.CompetitorSuggest("keyword_gaps", "ok", res.Suggested)
	ev.Outcome = track.Success
	track.Emit(ctx, ev)
	if res.Suggested > 0 {
		track.Emit(ctx, track.Event{Name: "seo_competitors.suggested", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success,
			Props: map[string]any{"source": "keyword_gaps", "site_id": p.Site, "suggested": res.Suggested}})
	}
	return nil
}
