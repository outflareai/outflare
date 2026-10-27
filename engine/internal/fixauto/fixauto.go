// Package fixauto decides how far agents may go on their own with the
// fixes list, and re-checks a site's fixes when it deploys.
//
// An agent takes a fix (POST .../fixes/{fix}/start) before it changes the
// site. In a mission, the workspace's fix autonomy decides whether that
// runs or waits for a person, through the mission's approval gate:
// copilot (the default) holds every fix, rules lets the listed crawl rules
// and categories through, and autonomous lets every fix through. Fixes
// whose change touches noindex, canonicals, redirects or robots.txt can
// move or hide a whole site in search, so they always wait for a person.
package fixauto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/deploys"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Modes.
const (
	Copilot    = "copilot"
	Rules      = "rules"
	Autonomous = "autonomous"
)

// alwaysPerson are the crawl rules whose fix changes redirects,
// canonicals, indexing or URLs: a mistake there can drop or move pages
// across the whole site, so a person always decides.
var alwaysPerson = []string{
	"page_broken", "redirect_chain", "redirect_loop", "redirect_to_broken", "redirect_temporary",
	"canonical_broken", "canonical_multiple", "canonical_elsewhere", "canonical_missing", "canonical_to_noindex",
	"noindex", "noindex_in_sitemap", "nofollow_page", "blocked_in_sitemap", "sitemap_non_canonical",
	"http_page", "url_uppercase", "url_underscores", "url_parameters",
	// Unblocking AI crawlers means editing robots.txt; a wrong fact in AI
	// answers often means asking another site to correct a page.
	"ai_search_blocked", "ai_fact_wrong",
}

// AlwaysPerson reports whether a rule's fixes always wait for a person.
// Rules added later that touch the same things are caught by their name.
func AlwaysPerson(rule string) bool {
	if slices.Contains(alwaysPerson, rule) {
		return true
	}
	for _, w := range []string{"redirect", "canonical", "noindex", "robots"} {
		if strings.Contains(rule, w) {
			return true
		}
	}
	return false
}

// AlwaysPersonRules lists the catalog's rules that always wait.
func AlwaysPersonRules() []string {
	out := []string{"ai_fact_wrong"}
	for _, r := range crawl.Rules {
		if AlwaysPerson(r.ID) {
			out = append(out, r.ID)
		}
	}
	return out
}

// Categories are the crawl rule categories rules mode can allow.
func Categories() []string {
	out := []string{}
	for _, r := range crawl.Rules {
		if !slices.Contains(out, r.Category) {
			out = append(out, r.Category)
		}
	}
	return out
}

// Settings are a workspace's or a site's fix autonomy.
type Settings struct {
	Mode           string     `json:"mode"`
	Allowed        []string   `json:"allowed"`
	VerifyOnDeploy bool       `json:"verify_on_deploy"`
	UpdatedAt      *time.Time `json:"updated_at"`
	// For a site: whether it uses the workspace's settings.
	Inherited bool `json:"inherited"`
}

// Defaults: every fix waits for a person; deploys re-check fixes.
func Defaults() Settings {
	return Settings{Mode: Copilot, Allowed: []string{}, VerifyOnDeploy: true}
}

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Load reads the settings that apply: the site's own (site non-empty),
// else the workspace's, else the defaults.
func Load(ctx context.Context, q Querier, ws, site string) (Settings, error) {
	s := Defaults()
	var at time.Time
	var own bool
	err := q.QueryRow(ctx, `SELECT mode, allowed, verify_on_deploy, updated_at, site_id IS NOT NULL FROM fix_autonomy
		WHERE workspace_id = $1 AND (site_id IS NULL OR site_id = NULLIF($2, '')::uuid)
		ORDER BY site_id IS NULL LIMIT 1`, ws, site).Scan(&s.Mode, &s.Allowed, &s.VerifyOnDeploy, &at, &own)
	if errors.Is(err, pgx.ErrNoRows) {
		s.Inherited = site != ""
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.UpdatedAt = &at
	s.Inherited = site != "" && !own
	if s.Allowed == nil {
		s.Allowed = []string{}
	}
	return s, nil
}

var ruleID = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// Check validates settings sent by a caller.
func (s Settings) Check() error {
	if s.Mode != Copilot && s.Mode != Rules && s.Mode != Autonomous {
		return errors.New("mode is copilot, rules or autonomous")
	}
	if len(s.Allowed) > 100 {
		return errors.New("allowed holds at most 100 rules and categories")
	}
	cats := Categories()
	for _, a := range s.Allowed {
		// Rule ids from other sources on the fixes list (AI visibility's
		// ai_brand_absent) are allowed too, so only the shape is checked.
		if _, ok := crawl.RuleFor(a); !ok && !slices.Contains(cats, a) && !ruleID.MatchString(a) {
			return fmt.Errorf("allowed holds rule ids or crawl rule categories (%s); %q is neither", strings.Join(cats, ", "), a)
		}
		if AlwaysPerson(a) {
			return fmt.Errorf("%s always waits for a person: its fix changes redirects, canonicals, indexing or URLs", a)
		}
	}
	return nil
}

// Decision is whether an agent may take a fix on its own.
type Decision struct {
	Run    bool   `json:"run"`
	Reason string `json:"reason"`
}

// Decide is what the settings say about a fix of rule.
func (s Settings) Decide(rule string) Decision {
	if AlwaysPerson(rule) {
		return Decision{Reason: "always_person"}
	}
	switch s.Mode {
	case Autonomous:
		return Decision{Run: true, Reason: "autonomous"}
	case Rules:
		r, _ := crawl.RuleFor(rule)
		if slices.Contains(s.Allowed, rule) || (r.Category != "" && slices.Contains(s.Allowed, r.Category)) {
			return Decision{Run: true, Reason: "allowed"}
		}
		return Decision{Reason: "not_allowed"}
	}
	return Decision{Reason: "copilot"}
}

// ForFix decides for one fix of a workspace, from its site's setting when
// it is about a site and the workspace's otherwise. ok is false when the
// fix doesn't exist.
func ForFix(ctx context.Context, q Querier, ws, fixID string) (Decision, bool, error) {
	var rule, subjectType, subject string
	err := q.QueryRow(ctx, `SELECT rule, subject_type, subject_id::text FROM fixes WHERE id = $1 AND workspace_id = $2`, fixID, ws).Scan(&rule, &subjectType, &subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, false, nil
	}
	if err != nil {
		return Decision{}, false, err
	}
	if subjectType != "seo_site" {
		subject = ""
	}
	s, err := Load(ctx, q, ws, subject)
	if err != nil {
		return Decision{}, false, err
	}
	return s.Decide(rule), true, nil
}

// JobVerify starts the crawl that re-checks a site's fixes after a deploy.
const JobVerify = "fixes.verify_deploy"

// Service re-checks fixes on deploys.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

type verifyPayload struct {
	Site    string `json:"site_id"`
	Deploy  string `json:"deploy_id"`
	Attempt int    `json:"attempt"`
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobVerify, s.Verify) }

// OnDeploy queues a re-check of the site's fixes, in the transaction that
// records the deploy going live, when the site has fixes to check and its
// settings allow it. Register it as a deploys hook. The crawl starts two
// minutes later, so caches have the new pages.
func (s *Service) OnDeploy(ctx context.Context, tx pgx.Tx, d deploys.Deploy) error {
	if d.SiteID == nil {
		return nil
	}
	set, err := Load(ctx, tx, d.WorkspaceID, *d.SiteID)
	if err != nil || !set.VerifyOnDeploy {
		return err
	}
	var any bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fixes WHERE workspace_id = $1 AND source = $3 AND subject_id = $2
		AND (status IN ('open', 'in_progress') OR (status = 'done' AND confirmed_by IS NULL)))`, d.WorkspaceID, *d.SiteID, crawl.FixSource).Scan(&any); err != nil || !any {
		return err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobVerify, WorkspaceID: &d.WorkspaceID, RunAt: time.Now().Add(2 * time.Minute),
		Payload: verifyPayload{Site: *d.SiteID, Deploy: d.ID}, IdempotencyKey: "deploy-verify:" + d.ID + ":0", MaxAttempts: 3})
	return err
}

// tries is how many times a re-check waits for a running crawl.
const tries = 6

// Verify is the job handler: it starts the crawl that re-checks the site.
func (s *Service) Verify(ctx context.Context, job jobs.Job) error {
	var p verifyPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	ws := *job.WorkspaceID
	ev := track.Event{Name: "fixes.deploy_verify_skipped", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"site_id": p.Site, "deploy_id": p.Deploy}}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		run, err := crawl.StartRun(ctx, tx, ws, p.Site, "manual", nil)
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crawl.started", TargetType: "crawl", TargetID: run,
			Data: map[string]any{"site_id": p.Site, "trigger": "deploy", "deploy_id": p.Deploy}}); err != nil {
			return err
		}
		// Fixes someone reported out are being checked again.
		rows, err := tx.Query(ctx, `SELECT id::text, rule FROM fixes WHERE workspace_id = $1 AND source = $3 AND subject_id = $2
			AND status = 'done' AND confirmed_by IS NULL`, ws, p.Site, crawl.FixSource)
		if err != nil {
			return err
		}
		type fix struct{ id, rule string }
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (fix, error) {
			var f fix
			return f, r.Scan(&f.id, &f.rule)
		})
		if err != nil {
			return err
		}
		for _, f := range list {
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.verify_requested", TargetType: "fix", TargetID: f.id,
				Data: map[string]any{"source": crawl.FixSource, "rule": f.rule, "subject_type": "seo_site", "subject_id": p.Site, "run_id": run,
					"by": "deploy", "deploy_id": p.Deploy}}); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, crawl.ErrRunning) && p.Attempt+1 < tries:
		// A crawl that started before the deploy may miss the change: try
		// again once it has had time to finish.
		p.Attempt++
		_, err := jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: JobVerify, WorkspaceID: &ws, RunAt: time.Now().Add(10 * time.Minute),
			Payload: p, IdempotencyKey: fmt.Sprintf("deploy-verify:%s:%d", p.Deploy, p.Attempt), MaxAttempts: 3})
		return err
	case errors.Is(err, crawl.ErrRunning), errors.Is(err, crawl.ErrSitePause), errors.Is(err, crawl.ErrOptedOut), errors.Is(err, crawl.ErrNoSite):
		ev.Outcome, ev.ErrorCode = track.Failure, map[bool]string{true: "crawl_running", false: "not_available"}[errors.Is(err, crawl.ErrRunning)]
		track.Emit(ctx, ev)
		return nil
	}
	return err
}
