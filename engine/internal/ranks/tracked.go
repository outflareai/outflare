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
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Keywords a workspace already tracks in its own rank tracker (SE
// Ranking's projects) are imported, not checked: once a day each site's
// tracked keywords and their positions come in from every connection that
// can read them and serves the site's brand. They land in the same
// tracked_keywords and rank_observations as Outflare's own checks, marked
// with the connection, so Rankings, history and regressions read them the
// same way, and Outflare never pays to check them again.

const (
	JobImport = "seo.ranks.import"
	// importDays is how far back a daily import reads, so a day the
	// tracker checked late is still picked up; the first import of a site
	// reads firstImportDays.
	importDays      = 7
	firstImportDays = 90
	// maxImported is how many keywords one site keeps, as for keywords
	// added by hand.
	maxImported = 1000
)

// querier is a pool or a transaction.
type querier interface {
	jobs.Querier
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type importPayload struct {
	Site        string `json:"site_id"`
	Integration string `json:"integration_id"`
}

// TrackedConnectors are the connectors that can import tracked keywords,
// by name.
func TrackedConnectors(reg *connectors.Registry) []string {
	var ids []string
	for _, d := range reg.All() {
		if c, ok := reg.Get(d.ID); ok && d.Status == connectors.Available {
			if _, ok := c.(connectors.TrackedRankReader); ok {
				ids = append(ids, d.ID)
			}
		}
	}
	return ids
}

// importable lists the connections that import tracked keywords for a
// site (or every active site, with site ""): site, workspace and
// connection.
func importable(ctx context.Context, q querier, reg *connectors.Registry, siteID string) ([][3]string, error) {
	rows, err := q.Query(ctx, `SELECT ss.id::text, ss.workspace_id::text, i.id::text FROM seo_sites ss
		JOIN integrations i ON i.workspace_id = ss.workspace_id AND i.connector = ANY($1) AND i.status = 'connected' AND integration_serves(i, ss.brand_id)
		WHERE ss.status = 'active' AND ($2 = '' OR ss.id::text = $2)
		ORDER BY 1, 3`, TrackedConnectors(reg), siteID)
	if err != nil {
		return nil, err
	}
	var out [][3]string
	var x [3]string
	_, err = pgx.ForEachRow(rows, []any{&x[0], &x[1], &x[2]}, func() error { out = append(out, x); return nil })
	return out, err
}

// EnqueueImports asks for an import of a site's tracked keywords from each
// connection that can read them, and says how many it queued. key
// de-duplicates, as for Enqueue.
func EnqueueImports(ctx context.Context, q querier, reg *connectors.Registry, ws, siteID, key string) (int, error) {
	list, err := importable(ctx, q, reg, siteID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		if x[1] != ws {
			continue
		}
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobImport, WorkspaceID: &ws, Payload: importPayload{Site: x[0], Integration: x[2]},
			IdempotencyKey: "ranks-import:" + x[0] + ":" + x[2] + ":" + key, MaxAttempts: 3}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// QueueImports queues the day's import for every site and connection that
// can import tracked keywords, and reports how many it queued.
func (s *Service) QueueImports(ctx context.Context) (int, error) {
	list, err := importable(ctx, s.Pool, s.Store.Registry, "")
	if err != nil {
		return 0, err
	}
	day := s.now().UTC().Format(time.DateOnly)
	for _, x := range list {
		ws := x[1]
		if _, err := jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: JobImport, WorkspaceID: &ws, Payload: importPayload{Site: x[0], Integration: x[2]},
			IdempotencyKey: "ranks-import:" + x[0] + ":" + x[2] + ":" + day, MaxAttempts: 3}); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

func (s *Service) importJob(ctx context.Context, job jobs.Job) error {
	var p importPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	_, err := s.Import(ctx, *job.WorkspaceID, p.Site, p.Integration)
	// Only an outage is worth another try; the event says what failed.
	if err != nil && connectors.KindOf(err) != connectors.ErrUnavailable {
		return nil
	}
	return err
}

// Imported is what one import kept.
type Imported struct {
	Project  string   `json:"project"`
	Keywords int      `json:"keywords"`
	Added    int      `json:"added"`
	Days     int      `json:"days"`
	Skipped  []string `json:"skipped"`
}

// Import reads a site's tracked keywords from a connection and keeps them,
// one site at a time, like checks.
func (s *Service) Import(ctx context.Context, ws, siteID, integrationID string) (Imported, error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return Imported{}, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('ranks:' || $1, 0))`, siteID); err != nil {
		return Imported{}, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('ranks:' || $1, 0))`, siteID) //nolint:errcheck
	return s.importLocked(ctx, ws, siteID, integrationID)
}

func (s *Service) importLocked(ctx context.Context, ws, siteID, integrationID string) (Imported, error) {
	var res Imported
	ev := track.Event{Name: "rank.imported", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"site_id": siteID, "integration_id": integrationID}}
	fail := func(err error) (Imported, error) {
		code, outcome := "connector_"+connectors.KindOf(err), track.Failure
		if connectors.KindOf(err) == connectors.ErrUnavailable {
			outcome = track.Error
		}
		ev.Outcome = outcome
		track.Fail(ctx, s.Log, ev, code, fmt.Errorf("import tracked keywords of site %s: %w", siteID, err))
		return res, err
	}
	var host string
	if err := s.Pool.QueryRow(ctx, `SELECT host FROM seo_sites WHERE id = $1 AND workspace_id = $2 AND status = 'active'`, siteID, ws).Scan(&host); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, nil
		}
		return res, err
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return fail(err)
	}
	r, ok := o.Connector.(connectors.TrackedRankReader)
	if !ok {
		return fail(connectors.Errf(connectors.ErrInvalid, "%s cannot import tracked keywords", o.Name))
	}
	ev.Props["connector"] = o.Connector.Definition().ID
	var seen bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rank_observations o JOIN tracked_keywords t ON t.id = o.tracked_id
		WHERE t.site_id = $1 AND o.integration_id = $2)`, siteID, integrationID).Scan(&seen); err != nil {
		return res, err
	}
	days := firstImportDays
	if seen {
		days = importDays
	}
	today := s.now().UTC()
	q := connectors.TrackedRankQuery{Hosts: seointel.PageHosts(host), From: today.AddDate(0, 0, -days).Format(time.DateOnly), To: today.Format(time.DateOnly)}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	got, err := r.TrackedRanks(cctx, o.Client, o.Creds, q)
	cancel()
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrAuth {
			_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
		}
		return fail(err)
	}
	res.Project, res.Skipped = got.Project, got.Skipped
	if res.Skipped == nil {
		res.Skipped = []string{}
	}
	ev.Props["project_found"] = got.Project != ""
	if got.Project == "" {
		track.Emit(ctx, ev)
		return res, nil
	}

	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM tracked_keywords WHERE site_id = $1`, siteID).Scan(&count); err != nil {
		return res, err
	}
	var written []string
	for _, k := range got.Ranks {
		kw := strings.Join(strings.Fields(strings.ToLower(k.Keyword)), " ")
		if kw == "" {
			continue
		}
		var id string
		var mine bool
		err := s.Pool.QueryRow(ctx, `SELECT id::text, integration_id IS NOT DISTINCT FROM $6::uuid AND status = 'active' FROM tracked_keywords
			WHERE site_id = $1 AND workspace_id = $2 AND keyword = $3 AND country = $4 AND device = $5 AND engine = $7`,
			siteID, ws, kw, k.Country, k.Device, o.ID, k.Engine).Scan(&id, &mine)
		if errors.Is(err, pgx.ErrNoRows) {
			if count >= maxImported {
				res.Skipped = append(res.Skipped, fmt.Sprintf("keyword %q: the site already follows %d keywords", kw, maxImported))
				continue
			}
			err = s.Pool.QueryRow(ctx, `INSERT INTO tracked_keywords (workspace_id, site_id, keyword, country, device, engine, integration_id, frequency)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 'daily') RETURNING id::text`, ws, siteID, kw, k.Country, k.Device, k.Engine, o.ID).Scan(&id)
			mine = true
			count++
			res.Added++
		}
		if err != nil {
			return res, err
		}
		// A keyword someone checks with another connection, or paused,
		// keeps its own source.
		if !mine {
			continue
		}
		b := &pgx.Batch{}
		for _, d := range k.Days {
			b.Queue(`INSERT INTO rank_observations (workspace_id, tracked_id, day, position, url, depth, engine, integration_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (tracked_id, day) DO UPDATE SET position = EXCLUDED.position, url = EXCLUDED.url, depth = EXCLUDED.depth,
					engine = EXCLUDED.engine, integration_id = EXCLUDED.integration_id, checked_at = now()`,
				ws, id, d.Day, d.Position, d.URL, Depth, k.Engine, o.ID)
		}
		if err := s.Pool.SendBatch(ctx, b).Close(); err != nil {
			return res, err
		}
		res.Keywords++
		res.Days += len(k.Days)
		written = append(written, id)
	}
	if len(written) > 0 {
		if _, err := s.Pool.Exec(ctx, `UPDATE tracked_keywords SET last_checked_at = now(), last_error = NULL, failures = 0, updated_at = now()
			WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, written); err != nil {
			return res, err
		}
		seointel.CheckRegressions(ctx, s.Pool, s.Log, ws, "rank_import", []string{siteID})
	}
	ev.Props["keywords"], ev.Props["added"], ev.Props["days"], ev.Props["skipped"] = res.Keywords, res.Added, res.Days, len(res.Skipped)
	track.Emit(ctx, ev)
	return res, nil
}
