package linkorders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The Ahrefs check of a catalog site reads only what judges it for a
// link (connectors.SEODonor): rating, worldwide search traffic, live
// referring and linked domains, and its three pages with the most search
// traffic. Five requests, about 250 Ahrefs API units, on the workspace's
// own Ahrefs connection and within its budget; a copy under a day old is
// free. The numbers replace the intermediary's DR and traffic, and Assess
// turns them into a verdict.

// JobAhrefsCheck checks one catalog site.
const JobAhrefsCheck = "link_orders.ahrefs_check"

// MaxAhrefsChecks is how many sites one request may check.
const MaxAhrefsChecks = 50

// AhrefsCheckUnits is about what one check costs in Ahrefs API units.
const AhrefsCheckUnits = 250

// AhrefsFreshDays is how long a successful check stays current: a site
// checked within it is not checked again, its stored numbers are used.
// The web app's green light uses the same window.
const AhrefsFreshDays = 30

// Ahrefs check outcomes (ahrefs_check_status).
const (
	AhrefsChecked      = "checked"
	AhrefsNoConnection = "no_connection"
	AhrefsBudget       = "budget"
	AhrefsFailed       = "failed"
)

// ErrNoAhrefs means the brand has no connected Ahrefs account.
var ErrNoAhrefs = errors.New("connect the brand's own Ahrefs account under Connectors to check sites in Ahrefs")

// Checker runs the checks.
type Checker struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
}

// Register adds the job handler.
func (c *Checker) Register(q *jobs.Queue) { q.Handle(JobAhrefsCheck, c.check) }

type checkPayload struct {
	DonorID string `json:"donor_id"`
	ActorID string `json:"actor_id,omitempty"`
}

// openAhrefs opens the brand's connected Ahrefs account.
func openAhrefs(ctx context.Context, store *connectors.Store, q connectors.Querier, ws, brand string) (*connectors.Opened, error) {
	list, err := store.List(ctx, q, ws, "")
	if err != nil {
		return nil, err
	}
	for _, i := range connectors.ForBrand(list, brand) {
		if i.Connector == "ahrefs" && i.Status == connectors.StatusConnected {
			return store.Open(ctx, q, ws, i.ID)
		}
	}
	return nil, ErrNoAhrefs
}

// QueueAhrefsChecks queues a check of each site, at most one a day per
// site, after making sure the brand has an Ahrefs connection. A site
// checked successfully in the last AhrefsFreshDays days is not checked
// again: its stored numbers stand. It returns how many were queued and how
// many were left alone as fresh.
func QueueAhrefsChecks(ctx context.Context, tx pgx.Tx, store *connectors.Store, workspaceID, brandID uuid.UUID, ids []uuid.UUID, actorID string, now time.Time) (queued, fresh int, err error) {
	if len(ids) == 0 || len(ids) > MaxAhrefsChecks {
		return 0, 0, fmt.Errorf("%w: send 1 to %d sites", ErrInvalid, MaxAhrefsChecks)
	}
	if _, err := openAhrefs(ctx, store, tx, workspaceID.String(), brandID.String()); err != nil {
		return 0, 0, err
	}
	day := now.UTC().Format("2006-01-02")
	since := now.Add(-AhrefsFreshDays * 24 * time.Hour)
	// A site checked successfully within AhrefsFreshDays keeps its stored
	// numbers (fresh); a failed or older check, or none, is checked again.
	rows, err := tx.Query(ctx, `SELECT d.id,
			(d.ahrefs_check_status = 'checked' AND d.ahrefs_checked_at >= $4) AS fresh,
			EXISTS (SELECT 1 FROM jobs j WHERE j.kind = $5 AND j.idempotency_key = 'ahrefs_check:' || d.id::text || ':' || $6) AS queued
		FROM coverage_donor_profiles d
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.id = ANY($3)`,
		workspaceID, brandID, ids, since, JobAhrefsCheck, day)
	if err != nil {
		return 0, 0, err
	}
	var todo []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var isFresh, isQueued bool
		if err := rows.Scan(&id, &isFresh, &isQueued); err != nil {
			rows.Close()
			return 0, 0, err
		}
		switch {
		case isFresh:
			fresh++
		case !isQueued:
			todo = append(todo, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	ws := workspaceID.String()
	for _, id := range todo {
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobAhrefsCheck, Payload: checkPayload{DonorID: id.String(), ActorID: actorID},
			WorkspaceID: &ws, MaxAttempts: 2, IdempotencyKey: "ahrefs_check:" + id.String() + ":" + day}); err != nil {
			return 0, 0, err
		}
	}
	return len(todo), fresh, nil
}

func (c *Checker) check(ctx context.Context, job jobs.Job) error {
	var p checkPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	return c.CheckOne(ctx, p.DonorID, p.ActorID)
}

// CheckOne checks one catalog site now and stores the outcome; actorID
// (may be empty) is who asked, for the spend ledger.
func (c *Checker) CheckOne(ctx context.Context, donorID, actorID string) error {
	p := checkPayload{DonorID: donorID, ActorID: actorID}
	var ws, brand, domain string
	err := c.Pool.QueryRow(ctx, `SELECT d.workspace_id::text, d.brand_id::text, pub.domain
		FROM coverage_donor_profiles d JOIN coverage_publishers pub ON pub.id = d.publisher_id WHERE d.id = $1`, p.DonorID).Scan(&ws, &brand, &domain)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	start := time.Now()
	status, rep, runErr := c.read(ctx, ws, brand, domain, p.ActorID)
	metrics.DonorAhrefsCheck(status, time.Since(start))
	ev := track.Event{Name: "link_order.ahrefs_checked", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"brand_id": brand, "donor_id": p.DonorID, "status": status}}
	if err := c.store(ctx, p.DonorID, status, rep); err != nil {
		track.Fail(ctx, c.Log, ev, "internal", fmt.Errorf("store the Ahrefs check of donor %s: %w", p.DonorID, err))
		return err
	}
	if status != AhrefsChecked {
		ev.Outcome = track.Failure
		code := map[string]string{AhrefsNoConnection: "connector_needed", AhrefsBudget: "budget_reached"}[status]
		if code == "" {
			code = "connector_" + connectors.KindOf(runErr)
		}
		track.Fail(ctx, c.Log, ev, code, fmt.Errorf("Ahrefs check of donor %s: %v", p.DonorID, runErr))
		return nil
	}
	track.Emit(ctx, ev)
	return nil
}

// read asks Ahrefs, within the connection's budget.
func (c *Checker) read(ctx context.Context, ws, brand, domain, actor string) (string, *connectors.SEOReport, error) {
	o, err := openAhrefs(ctx, c.Store, c.Pool, ws, brand)
	if err != nil {
		return AhrefsNoConnection, nil, err
	}
	r, ok := o.Connector.(connectors.SEOReader)
	if !ok || !connectors.SupportsSEO(r, connectors.SEODonor) {
		return AhrefsNoConnection, nil, ErrNoAhrefs
	}
	q, err := connectors.CheckSEOQuery(connectors.SEOQuery{Report: connectors.SEODonor, Target: domain, Limit: 3})
	if err != nil {
		return AhrefsFailed, nil, err
	}
	opt := seosync.Options{}
	if actor != "" {
		opt.ActorID = &actor
	}
	a, err := seosync.Fetch(outcomes.WithBrand(ctx, brand), c.Pool, o, r, q, opt)
	var be *seosync.BudgetError
	switch {
	case errors.As(err, &be):
		return AhrefsBudget, nil, err
	case err != nil:
		return AhrefsFailed, nil, err
	}
	return AhrefsChecked, &a.SEOReport, nil
}

// homePath is a home page: the root, or a language's root (/en/, /pt-br/).
var homePath = regexp.MustCompile(`^/?(?:[a-z]{2}(?:[-_][a-z]{2})?/?)?$`)

func isHome(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.RawQuery == "" && homePath.MatchString(u.Path)
}

func count(v *float64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(math.Round(math.Max(0, *v)))
	return &n
}

// store writes the outcome and, after a successful read, the numbers.
func (c *Checker) store(ctx context.Context, id, status string, rep *connectors.SEOReport) error {
	if rep == nil {
		_, err := c.Pool.Exec(ctx, `UPDATE coverage_donor_profiles SET ahrefs_checked_at = now(), ahrefs_check_status = $2 WHERE id = $1`, id, status)
		return err
	}
	m := map[string]any{}
	set := func(k string, v *int64) {
		if v != nil {
			m[k] = *v
		}
	}
	set(MetricReferringDomains, count(rep.Totals.ReferringDomains))
	set(MetricLinkedDomains, count(rep.Totals.LinkedDomains))
	var top3, home int64
	for i, row := range rep.Rows {
		t := count(row.OrganicTraffic)
		if t == nil {
			continue
		}
		if i == 0 {
			m[MetricTopPageURL] = row.URL
			m[MetricTopPageTraffic] = *t
		}
		top3 += *t
		if isHome(row.URL) {
			home += *t
		}
	}
	if len(rep.Rows) > 0 {
		// A home page outside the top three has less than the third page.
		m[MetricTop3Traffic] = top3
		m[MetricHomeTraffic] = home
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	traffic := count(rep.Totals.OrganicTraffic)
	var dr *int64
	if v := count(rep.Totals.DomainRating); v != nil && *v <= 100 {
		dr = v
	}
	_, err = c.Pool.Exec(ctx, `UPDATE coverage_donor_profiles SET
			ahrefs_checked_at = now(), ahrefs_check_status = $2,
			domain_rating = COALESCE($3, domain_rating), traffic = COALESCE($4, traffic),
			metrics = (metrics - 'top_page_url' - 'top_page_traffic' - 'top3_traffic' - 'home_traffic') || $5::jsonb,
			metrics_source = 'ahrefs', metrics_at = now(), updated_at = now()
		WHERE id = $1`, id, status, dr, traffic, string(raw))
	return err
}
