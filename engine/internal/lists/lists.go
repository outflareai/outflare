// Package lists builds and refreshes lead lists from connected data
// providers. A run searches one provider, scores each person against the
// list's ideal customer profile, finds missing emails through a waterfall of
// email finders, leaves out people who are suppressed, recently contacted,
// already in a sequence or undeliverable, and adds the rest as leads. Every
// credit spent is recorded on the run and in the spend ledger.
//
// Runs call providers, so they happen in jobs. Each page commits on its
// own, so a long build shows progress and a retry continues cheaply:
// provider answers are cached, and people already found are recognised by
// their provider id without paying again.
package lists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// JobRun runs one list run.
const JobRun = "lists.run"

// Run kinds.
const (
	KindPreview = "preview"
	KindBuild   = "build"
	KindRefresh = "refresh"
	KindEnrich  = "enrich"
)

// Run statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Reasons people are left out of a list.
const (
	SkipNoEmail       = "no_email"
	SkipSuppressed    = "suppressed"
	SkipContacted     = "contacted"
	SkipInSequence    = "in_sequence"
	SkipUndeliverable = "undeliverable"
	SkipUnverified    = "unverified"
	SkipLowScore      = "low_score"
	SkipAlreadyInList = "already_in_list"
)

// SkipReasons lists every reason, in display order.
var SkipReasons = []string{SkipAlreadyInList, SkipLowScore, SkipNoEmail, SkipUnverified, SkipSuppressed, SkipContacted, SkipInSequence, SkipUndeliverable}

const (
	pageSize        = 100
	previewSize     = 10
	maxPagesPerRun  = 200
	cacheMaxAge     = 90 * 24 * time.Hour
	cacheKeepFor    = 180 * 24 * time.Hour
	maxFilterValues = 50
)

// Service runs list builds.
type Service struct {
	Pool   *pgxpool.Pool
	Store  *c.Store
	Verify *verify.Service // nil skips verification
	Log    *slog.Logger
}

// Register adds the run handler to the queue.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobRun, s.handle) }

// Params is what a run does, copied onto the run when it is queued.
type Params struct {
	SearchIntegrationID string         `json:"search_integration_id,omitempty"`
	Filters             c.PeopleFilter `json:"filters"`
	// Profile the run scores people against: the list's filters on top of
	// its ideal customer profile.
	ICP                  c.PeopleFilter `json:"icp"`
	EnrichIntegrationIDs []string       `json:"enrich_integration_ids"`
	MaxLeads             int            `json:"max_leads"`
	CreditCap            *float64       `json:"credit_cap"`
	Verify               bool           `json:"verify"`
	VerifiedEmailsOnly   bool           `json:"verified_emails_only"`
	SkipContactedDays    int            `json:"skip_contacted_days"`
	SkipInSequences      bool           `json:"skip_in_sequences"`
	MinScore             int            `json:"min_score"`
}

// Run is one list run.
type Run struct {
	ID           string             `json:"id"`
	ListID       *string            `json:"list_id"`
	ProjectID    *string            `json:"project_id"`
	Kind         string             `json:"kind"`
	Status       string             `json:"status"`
	Params       Params             `json:"params"`
	Found        int                `json:"found"`
	TotalMatches *int               `json:"total_matches"`
	Added        int                `json:"added"`
	Enriched     int                `json:"enriched"`
	Skipped      map[string]int     `json:"skipped"`
	Credits      float64            `json:"credits"`
	CreditsBy    map[string]float64 `json:"credits_by"`
	Sample       []c.Person         `json:"sample"`
	StopReason   *string            `json:"stop_reason"`
	Error        *string            `json:"error"`
	RequestedBy  *string            `json:"requested_by"`
	CreatedAt    time.Time          `json:"created_at"`
	StartedAt    *time.Time         `json:"started_at"`
	FinishedAt   *time.Time         `json:"finished_at"`
}

// RunCols are the columns ScanRun reads, in order.
const RunCols = `id, list_id, project_id, kind, status, params, found, total_matches, added, enriched, skipped,
	credits::float8, credits_by, sample, stop_reason, error, requested_by, created_at, started_at, finished_at`

// ScanRun reads a run selected with RunCols.
func ScanRun(row pgx.Row) (Run, error) {
	var r Run
	var params, skipped, by, sample []byte
	err := row.Scan(&r.ID, &r.ListID, &r.ProjectID, &r.Kind, &r.Status, &params, &r.Found, &r.TotalMatches, &r.Added,
		&r.Enriched, &skipped, &r.Credits, &by, &sample, &r.StopReason, &r.Error, &r.RequestedBy, &r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal(params, &r.Params)
	r.Skipped, r.CreditsBy, r.Sample = map[string]int{}, map[string]float64{}, []c.Person{}
	_ = json.Unmarshal(skipped, &r.Skipped)
	_ = json.Unmarshal(by, &r.CreditsBy)
	_ = json.Unmarshal(sample, &r.Sample)
	return r, nil
}

// ErrInvalid is a request problem worded for the person who sent it.
type ErrInvalid struct{ Msg string }

func (e *ErrInvalid) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ErrInvalid{Msg: fmt.Sprintf(format, a...)} }

// ErrBusy means the list already has a build, refresh or enrichment queued
// or running.
var ErrBusy = errors.New("this list already has a run in progress")

// CleanFilter trims and checks a filter, and returns it with values
// normalised (lower-case seniorities, bare domains).
func CleanFilter(f c.PeopleFilter) (c.PeopleFilter, error) {
	clean := func(name string, vals []string) ([]string, error) {
		var out []string
		seen := map[string]bool{}
		for _, v := range vals {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if len(v) > 200 {
				return nil, invalid("%s: values must be at most 200 characters", name)
			}
			switch name {
			case c.FilterSeniorities:
				v = strings.ToLower(v)
				if !containsFold(c.Seniorities, v) {
					return nil, invalid("seniorities: %q is not one of %s", v, strings.Join(c.Seniorities, ", "))
				}
			case c.FilterEmployeeRanges:
				if _, _, ok := parseRange(v); !ok {
					return nil, invalid("employee_ranges: %q should look like 11-50 or 10001+", v)
				}
			case c.FilterCompanyDomains:
				kind, d, err := leads.ParseSuppression(v)
				if err != nil || kind != "domain" {
					return nil, invalid("company_domains: %q is not a domain", v)
				}
				v = strings.TrimPrefix(d, "www.")
			}
			if k := strings.ToLower(v); !seen[k] {
				seen[k] = true
				out = append(out, v)
			}
		}
		if len(out) > maxFilterValues {
			return nil, invalid("%s: at most %d values", name, maxFilterValues)
		}
		return out, nil
	}
	var err error
	ptrs := map[string]*[]string{
		c.FilterTitles: &f.Titles, c.FilterSeniorities: &f.Seniorities, c.FilterIndustries: &f.Industries,
		c.FilterEmployeeRanges: &f.EmployeeRanges, c.FilterLocations: &f.Locations, c.FilterCompanyLocations: &f.CompanyLocations,
		c.FilterCompanyDomains: &f.CompanyDomains, c.FilterKeywords: &f.Keywords,
	}
	for _, name := range c.AllFilters {
		if *ptrs[name], err = clean(name, *ptrs[name]); err != nil {
			return f, err
		}
	}
	return f, nil
}

// MergeICP lays a list's filters over its ideal customer profile: a
// dimension the list sets replaces the profile's.
func MergeICP(project, list c.PeopleFilter) c.PeopleFilter {
	out := project
	pick := func(dst *[]string, v []string) {
		if len(v) > 0 {
			*dst = v
		}
	}
	pick(&out.Titles, list.Titles)
	pick(&out.Seniorities, list.Seniorities)
	pick(&out.Industries, list.Industries)
	pick(&out.EmployeeRanges, list.EmployeeRanges)
	pick(&out.Locations, list.Locations)
	pick(&out.CompanyLocations, list.CompanyLocations)
	pick(&out.CompanyDomains, list.CompanyDomains)
	pick(&out.Keywords, list.Keywords)
	return out
}

// parseRange reads "11-50" or "10001+"; hi is 0 for open-ended ranges.
func parseRange(r string) (lo, hi int, ok bool) {
	r = strings.TrimSpace(r)
	if strings.HasSuffix(r, "+") {
		n, err := strconv.Atoi(strings.TrimSuffix(r, "+"))
		return n, 0, err == nil && n >= 0
	}
	a, b, found := strings.Cut(r, "-")
	if !found {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(a))
	y, err2 := strconv.Atoi(strings.TrimSpace(b))
	return x, y, err1 == nil && err2 == nil && x >= 0 && x <= y
}

// ParamsForList builds a run's params from the list's settings and its
// ideal customer profile (its project's ICP when it has none).
func ParamsForList(ctx context.Context, q c.Querier, ws, listID string) (Params, *string, error) {
	var p Params
	var projectID, search *string
	var filters, icp []byte
	var cap *float64
	err := q.QueryRow(ctx, `
		SELECT l.project_id, l.search_integration_id, l.filters, COALESCE(icp.filters, pr.icp, '{}'),
		       l.enrich_integration_ids::text[], l.max_leads, l.credit_cap::float8, l.verify, l.verified_emails_only,
		       l.skip_contacted_days, l.skip_in_sequences, l.min_score
		FROM lead_lists l LEFT JOIN projects pr ON pr.id = l.project_id
		LEFT JOIN ideal_customer_profiles icp ON icp.id = l.profile_id AND icp.workspace_id = l.workspace_id
		WHERE l.workspace_id = $1 AND l.id = $2`, ws, listID).
		Scan(&projectID, &search, &filters, &icp, &p.EnrichIntegrationIDs, &p.MaxLeads, &cap, &p.Verify,
			&p.VerifiedEmailsOnly, &p.SkipContactedDays, &p.SkipInSequences, &p.MinScore)
	if err != nil {
		return p, nil, err
	}
	if search != nil {
		p.SearchIntegrationID = *search
	}
	p.CreditCap = cap
	_ = json.Unmarshal(filters, &p.Filters)
	var proj c.PeopleFilter
	_ = json.Unmarshal(icp, &proj)
	p.ICP = MergeICP(proj, p.Filters)
	return p, projectID, nil
}

// Enqueue records a run and queues its job in the caller's transaction.
func Enqueue(ctx context.Context, tx pgx.Tx, ws string, listID, projectID *string, kind string, p Params, actorID string) (Run, error) {
	params, _ := json.Marshal(p)
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	run, err := ScanRun(tx.QueryRow(ctx, `
		INSERT INTO list_runs (workspace_id, list_id, project_id, kind, params, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+RunCols, ws, listID, projectID, kind, params, actor))
	if err != nil {
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
			return run, ErrBusy
		}
		return run, err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: runPayload{run.ID}, IdempotencyKey: run.ID, MaxAttempts: 3})
	return run, err
}

type runPayload struct {
	RunID string `json:"run_id"`
}

// Loop queues refreshes for lists that are due, until ctx ends.
func (s *Service) Loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.RefreshDue(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("queue list refreshes", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RefreshDue queues a refresh for every list whose next refresh has come,
// moves its next refresh on, and drops expired cache entries.
func (s *Service) RefreshDue(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM enrichment_cache WHERE fetched_at < $1`, time.Now().Add(-cacheKeepFor)); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT workspace_id, id FROM lead_lists
		WHERE next_refresh_at <= now() AND kind = 'search' AND refresh_every_days IS NOT NULL
		ORDER BY next_refresh_at LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct{ ws, id string }
	var all []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.ws, &d.id); err != nil {
			rows.Close()
			return err
		}
		all = append(all, d)
	}
	rows.Close()
	for _, d := range all {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE lead_lists SET next_refresh_at = now() + make_interval(days => refresh_every_days)
				WHERE id = $1`, d.id); err != nil {
				return err
			}
			p, project, err := ParamsForList(ctx, tx, d.ws, d.id)
			if err != nil {
				return err
			}
			if p.SearchIntegrationID == "" {
				return nil
			}
			_, err = Enqueue(ctx, tx, d.ws, &d.id, project, KindRefresh, p, "")
			if errors.Is(err, ErrBusy) {
				return nil // the next refresh will pick up what this one would have
			}
			return err
		})
		if err != nil {
			s.Log.Error("queue list refresh", "list", d.id, "err", err)
		}
	}
	return nil
}

func (s *Service) handle(ctx context.Context, job jobs.Job) error {
	var p runPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	ws := ""
	if job.WorkspaceID != nil {
		ws = *job.WorkspaceID
	}
	run, err := ScanRun(s.Pool.QueryRow(ctx, `
		UPDATE list_runs SET status = 'running', started_at = COALESCE(started_at, now())
		WHERE id = $1 AND workspace_id = $2 AND status IN ('queued', 'running')
		RETURNING `+RunCols, p.RunID, ws))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // cancelled or already finished
	}
	if err != nil {
		return err
	}
	r := &runner{s: s, ws: ws, run: run}
	err = r.execute(ctx)
	if err != nil && job.Attempt < job.MaxAttempts && c.KindOf(err) == c.ErrUnavailable && !errors.Is(err, errStop) {
		return err // a provider blip: retry; cached answers keep it cheap
	}
	return r.finish(ctx, err)
}

// RunPending runs every queued run in the workspace now, in this
// goroutine. Tests and command-line tools use it instead of the job queue.
func (s *Service) RunPending(ctx context.Context, ws string) error {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM list_runs WHERE workspace_id = $1 AND status = 'queued' ORDER BY created_at`, ws)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		payload, _ := json.Marshal(runPayload{id})
		if err := s.handle(ctx, jobs.Job{Kind: JobRun, Payload: payload, WorkspaceID: &ws, Attempt: 3, MaxAttempts: 3}); err != nil {
			return err
		}
	}
	return nil
}
