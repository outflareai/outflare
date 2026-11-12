// Package adsuggest is the rule engine of the paid ads intelligence layer
// (plans/paid-ads/plan.md, "Rules (adsuggest, rebuilt)"): it reads every
// campaign Outflare can change after each ad sync, writes what it would
// change, what it changed and what a person should look at into the
// findings queue (ad_suggestions), and judges each change once its window
// has run.
//
// Everything is scored in one unit: cost per new paying customer on
// Outflare's own attribution (paidads.Daily), for a brand that has set
// what a new customer may cost; a brand without a target is judged on the
// network's own conversions and every finding says so (unit). Every
// change waits for a person. Savings (a budget cut, a pause, a negative
// keyword, a keyword or ad paused, a target tightened, an exclusion) apply
// on their own only where an extension's hook allows it
// (paidads.AutoApprove, Host.SetAdAutoApprove), as an ad_changes row like
// a person's, with an undo; anything that can spend more always waits for
// a person, and the hook is never asked about it. While a
// brand's shadow mode is on, every change-class rule only records what it
// would have done (paidads.Shadow). Verdicts compare the judgement window
// (two conversion cycles, at least 14 days, the trailing lag days left
// out) with as many days before, and each writes a brand finding so the
// agent remembers what the lever did on this brand and network.
package adsuggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/paidads"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Job reads one workspace's campaigns: it judges what has been measured
// long enough, then finds.
const Job = "ads.suggest"

// Kinds of finding the lever catalogue names (paidads.Levers); the rest
// are in rules.go.
const (
	BudgetUp   = "budget_up"
	BudgetDown = "budget_down"
	Pause      = "pause"
	Resume     = "resume"
	// Keywords (Google search campaigns).
	KeywordAdd  = "keyword_add"
	NegativeAdd = "negative_add"
	// KeywordSplit is retired (single-keyword ad groups are obsolete;
	// consolidate replaces it): no longer generated, kept so rows made
	// before still measure, judge and revert.
	KeywordSplit = "keyword_split"
	// BiddingTarget gives a campaign that maximises conversions with no
	// target a target cost per conversion, once it has the payers to bid
	// to one (the second rung of the bidding ladder).
	BiddingTarget = "bidding_target"
)

// SavingKinds are the kinds that can only spend less (paidads.Saves): the
// only class an extension's hook may let apply without a person, or let
// an agent approve (paidads.AutoApprove, api.Options.AdAutoApprove);
// every other kind waits for a person.
func SavingKinds() []string {
	var out []string
	for _, l := range paidads.Levers {
		if paidads.Saves(l.Kind) && !l.Protective {
			out = append(out, l.Kind)
		}
	}
	return out
}

// Saves says whether a kind can only spend less (SavingKinds).
func Saves(kind string) bool { return slices.Contains(SavingKinds(), kind) }

// BiddingNetworks are the platforms whose bidding Outflare can change
// (connectors.AdTargetingManager with bidding methods).
var BiddingNetworks = map[string]bool{"google_ads": true}

// KeywordNetworks are the platforms whose keywords Outflare can change
// (connectors.AdKeywordManager).
var KeywordNetworks = map[string]bool{"google_ads": true}

// Verdicts.
const (
	Worked     = "worked"
	DidNotWork = "did_not_work"
	NotSure    = "not_sure"
)

// Manageable are the platforms whose campaigns Outflare can change
// (connectors.AdsManager); only they get findings.
var Manageable = map[string]bool{"google_ads": true, "meta_ads": true, "microsoft_ads": true, "linkedin_ads": true, "tiktok_ads": true}

// Runner runs the jobs. Store opens the networks for the exclusion rules'
// reads and for the changes this package applies itself; without one,
// those rules stay quiet and its changes fail as connector_invalid.
type Runner struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	Now   func() time.Time

	// signalsRead remembers, per campaign, the day its network report was
	// last read, so the hourly run reads each once a day.
	mu          sync.Mutex
	signalsRead map[string]string
	signalsMemo map[string]connectors.AdCampaignSignals
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

type payload struct{}

// Register adds the job handlers: the run, and the changes this package
// carries out itself (apply.go).
func (r *Runner) Register(q *jobs.Queue) {
	q.Handle(Job, func(ctx context.Context, j jobs.Job) error {
		if j.WorkspaceID == nil {
			return nil
		}
		_, err := r.Run(ctx, *j.WorkspaceID)
		return err
	})
	q.Handle(JobApply, func(ctx context.Context, j jobs.Job) error {
		var p changePayload
		if err := json.Unmarshal(j.Payload, &p); err != nil || j.WorkspaceID == nil {
			return nil
		}
		return r.ApplyFindingChange(ctx, *j.WorkspaceID, p.Change, j.Attempt >= j.MaxAttempts)
	})
}

// runKey is the hour's idempotency key: at most one run an hour per
// workspace.
func runKey(ws string, now time.Time) string {
	return "ads.suggest:" + ws + ":" + now.UTC().Format("2006-01-02T15")
}

// Enqueue queues a run for the workspace, at most one an hour.
func Enqueue(ctx context.Context, q jobs.Querier, ws string, now time.Time) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: Job, WorkspaceID: &ws, Payload: payload{}, IdempotencyKey: runKey(ws, now), MaxAttempts: 3})
	return err
}

// EnqueueOnce queues a run unless this hour's already is: it reports
// whether it queued one and, when it didn't, when the next may be.
func EnqueueOnce(ctx context.Context, q jobs.Querier, ws string, now time.Time) (queued bool, nextAt time.Time, err error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND idempotency_key = $2)`, Job, runKey(ws, now)).Scan(&exists); err != nil {
		return false, time.Time{}, err
	}
	next := now.UTC().Truncate(time.Hour).Add(time.Hour)
	if exists {
		return false, next, nil
	}
	return true, next, Enqueue(ctx, q, ws, now)
}

// Result says what a run did.
type Result struct {
	Judged    int `json:"judged"`
	Suggested int `json:"suggested"`
	// Applied: savings made on their own; Shadowed: what rules would have
	// done, recorded only.
	Applied  int `json:"applied"`
	Shadowed int `json:"shadowed"`
	Expired  int `json:"expired"`
}

// Run judges and finds for one workspace.
func (r *Runner) Run(ctx context.Context, ws string) (Result, error) {
	start := r.now()
	var res Result
	var err error
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "error"
			track.Fail(ctx, r.log(), track.Event{Name: "ad_suggestions.failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Error}, "internal", err)
		} else {
			track.Emit(ctx, track.Event{Name: "ad_suggestions.refreshed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success,
				Props: map[string]any{"judged": res.Judged, "suggested": res.Suggested, "applied": res.Applied, "shadowed": res.Shadowed, "expired": res.Expired}})
		}
		metrics.AdSuggestRun(outcome, r.now().Sub(start))
	}()
	if res.Judged, err = r.judge(ctx, ws); err != nil {
		return res, err
	}
	err = r.find(ctx, ws, &res)
	if err != nil {
		return res, err
	}
	var open int
	if err = r.Pool.QueryRow(ctx, `SELECT count(*) FROM ad_suggestions WHERE status = 'open' AND autonomy = 'approve'`).Scan(&open); err == nil {
		metrics.AdFindingsOpen(open)
	}
	return res, err
}

// lastDay is the last whole day: today is still being reported.
func (r *Runner) lastDay() time.Time {
	t := r.now().UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
}

// find writes the findings for one workspace: each campaign's rules, then
// the keyword, ad and segment rules where the network has them.
func (r *Runner) find(ctx context.Context, ws string, res *Result) error {
	now := r.now()
	camps, brands, err := r.loadCampaigns(ctx, ws)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for i := range camps {
		c := &camps[i]
		if !Manageable[c.connector] {
			continue
		}
		b := brands[brandKey(c.brand)]
		var list []*proposal
		add := func(p *proposal) {
			if p != nil {
				list = append(list, p)
			}
		}
		// Alerts first: they fire whatever the brand's state.
		add(staleTarget(*c, now))
		// Change-class rules: never while tracking or sends are broken,
		// at most one lever per campaign per cycle.
		if !b.alertOpen && !c.busy {
			r.readSignals(ctx, c)
			if c.changeAllowed(now, false) {
				add(budgetRaise(*c))
				add(budgetCut(*c))
				add(pause(*c))
				add(resume(*c, now))
			}
			add(targetTighten(*c, now))
			add(targetLoosen(*c, now))
			add(biddingLadder(*c, now))
			if c.changeAllowed(now, false) && c.status == "active" {
				if c.caps.HasKeywords || c.pmax() {
					terms, err := r.loadTerms(ctx, ws, *c)
					if err != nil {
						return err
					}
					for _, p := range negativeKeyword(*c, terms) {
						add(p)
					}
					add(paymentQualityNegative(*c, terms))
					add(keywordPromote(*c, terms))
					add(brandLeak(*c, terms))
					kws, groups, err := r.loadKeywords(ctx, ws, *c)
					if err != nil {
						return err
					}
					add(keywordPause(*c, kws))
					add(consolidate(*c, groups))
				}
				ads, err := r.loadAds(ctx, ws, *c)
				if err != nil {
					return err
				}
				add(adPause(*c, ads))
				segs, err := r.loadSegments(ctx, ws, *c)
				if err != nil {
					return err
				}
				add(exclusion(*c, segs))
			}
		}
		// One finding per kind per campaign: the first rule to name a
		// kind wins (rules are ordered by how much they save).
		seen := map[string]bool{}
		for _, p := range list {
			if seen[p.kind] {
				continue
			}
			seen[p.kind] = true
			id, err := r.write(ctx, ws, *c, b, p, res)
			if err != nil {
				return err
			}
			if id != "" {
				keep[id] = true
			}
		}
	}
	ids := make([]string, 0, len(keep))
	for id := range keep {
		ids = append(ids, id)
	}
	tag, err := r.Pool.Exec(ctx, `UPDATE ad_suggestions SET status = 'expired', updated_at = now()
		WHERE workspace_id = $1 AND status IN ('open', 'shadow') AND (rule = ANY($2) OR rule = '') AND NOT (id = ANY($3::uuid[]))`, ws, Rules, ids)
	if err != nil {
		return err
	}
	res.Expired = int(tag.RowsAffected())
	return nil
}

// write puts one proposal in the queue: as a shadow row while the brand's
// rules are in shadow mode, applied at once when it is a saving the
// extension's hook allows (paidads.AutoApprove), else open for a person
// (or, for an alert, acknowledged). It returns the row's id, or "" when
// nothing was written.
func (r *Runner) write(ctx context.Context, ws string, c campaign, b brandCtx, p *proposal, res *Result) (string, error) {
	allowed := c.brand != nil && paidads.AutoApprove(ctx, ws, *c.brand, p.kind)
	autonomy := paidads.Autonomy(b.econ, c.caps, p.kind, allowed)
	if c.brand == nil {
		// A campaign on an account every brand shares has no economics:
		// nothing applies on its own and nothing is shadowed.
		autonomy = paidads.AutonomyApprove
		if paidads.IsAlert(p.kind) {
			autonomy = paidads.AutonomyAlert
		}
	}
	if autonomy == paidads.AutonomyShadow {
		live := paidads.AutonomyApprove
		if allowed {
			live = paidads.AutonomyAuto
		}
		err := paidads.Shadow(ctx, r.Pool, paidads.Finding{WorkspaceID: ws, BrandID: *c.brand, EntityID: c.id, Kind: p.kind, Autonomy: live, Unit: c.unit,
			Rule: p.rule, RuleVersion: RuleVersion, ImpactMicros: p.impact, Reason: p.reason, Evidence: p.evidence, Proposed: p.proposed})
		if err != nil {
			return "", err
		}
		var id string
		if err := r.Pool.QueryRow(ctx, `SELECT id::text FROM ad_suggestions WHERE ad_entity_id = $1 AND kind = $2 AND rule = $3 AND status = 'shadow' LIMIT 1`, c.id, p.kind, p.rule).Scan(&id); err != nil {
			return "", err
		}
		res.Shadowed++
		metrics.AdFinding(p.kind, "shadow", "recorded")
		return id, nil
	}
	ev, _ := json.Marshal(p.evidence)
	pr := []byte(`{}`)
	if p.proposed != nil {
		pr, _ = json.Marshal(p.proposed)
	}
	var id string
	var inserted bool
	err := pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		if autonomy == paidads.AutonomyAlert {
			// An alert a person acknowledged within a week isn't raised again.
			var snoozed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ad_suggestions WHERE ad_entity_id = $1 AND kind = $2 AND status = 'acknowledged' AND updated_at >= now() - interval '7 days')`, c.id, p.kind).Scan(&snoozed); err != nil {
				return err
			}
			if snoozed {
				return nil
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, reason, evidence, proposed, autonomy, unit, impact_micros, rule, rule_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (ad_entity_id, kind) WHERE status = 'open'
			DO UPDATE SET reason = EXCLUDED.reason, evidence = EXCLUDED.evidence, proposed = EXCLUDED.proposed, autonomy = EXCLUDED.autonomy, unit = EXCLUDED.unit,
				impact_micros = EXCLUDED.impact_micros, rule = EXCLUDED.rule, rule_version = EXCLUDED.rule_version, updated_at = now()
			RETURNING id, xmax = 0`, ws, c.brand, c.id, p.kind, p.reason, ev, pr, autonomy, c.unit, p.impact, p.rule, RuleVersion).Scan(&id, &inserted); err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		res.Suggested++
		metrics.AdSuggested(c.connector, p.kind)
		metrics.AdFinding(p.kind, autonomy, "created")
		data := map[string]any{"suggestion_id": id, "campaign_id": c.id, "connector": c.connector, "kind": p.kind, "proposed": p.proposed, "autonomy": autonomy, "rule": p.rule, "unit": c.unit}
		if c.brand != nil {
			data["brand_id"] = *c.brand
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_suggestion.created",
			TargetType: "ad_suggestion", TargetID: id, Data: data, Props: map[string]any{"connector": c.connector, "kind": p.kind, "autonomy": autonomy, "rule": p.rule, "unit": c.unit}}); err != nil {
			return err
		}
		if autonomy != paidads.AutonomyAuto || !paidads.Saves(p.kind) {
			return nil
		}
		applied, err := r.autoApply(ctx, tx, ws, c, p, id)
		if err != nil {
			return err
		}
		if applied {
			res.Applied++
			metrics.AdFinding(p.kind, autonomy, "applied")
		}
		return nil
	})
	return id, err
}

// Window is how long a change is measured: the network's settling time
// for the kind, then the brand's judgement window plus its lag days, at
// most six weeks. Performance Max is judged on days 15 to 42 after any
// change (Google reallocates for two weeks first).
func Window(kind string, windowDays, lagDays int, pmax bool) (learn, days int, note string) {
	switch kind {
	case BudgetUp, BudgetDown:
		learn = 3 // a fifth either way: networks adjust within days
	case Resume, KeywordAdd, KeywordSplit, Consolidate:
		learn = 7 // a restarted campaign, a new keyword or ad group learns again
	case BiddingTarget, TargetTighten, TargetLoosen:
		learn = 14 // Google's bidding takes one to two weeks to settle
	case NegativeAdd, KeywordPause, AdPause, ExclusionAdd:
		learn = 1
	}
	if windowDays < int(paidads.MinJudgementWindow/(24*time.Hour)) {
		windowDays = int(paidads.MinJudgementWindow / (24 * time.Hour))
	}
	days = windowDays + lagDays
	if learn+days > maxWindow {
		days = maxWindow - learn
	}
	note = fmt.Sprintf("%d days to settle, then %d days measured (two conversion cycles, at least two weeks, and %d days for late payments).", learn, days, lagDays)
	if pmax {
		learn, days = pmaxLearnDays, pmaxToDay-pmaxLearnDays
		note = fmt.Sprintf("%d days to settle, then %d days measured: Performance Max is judged on days %d to %d after a change.", learn, days, pmaxFromDay, pmaxToDay)
	}
	if kind == Pause {
		note = fmt.Sprintf("%d days measured: what the account brings without it.", days)
	}
	return learn, days, note
}

// Started sets the window of a finding whose change was made, in tx: the
// API calls it when a person approves, the run when a saving applies on
// its own. convPerDay is kept for callers that planned on it; the window
// is the brand's.
func Started(ctx context.Context, tx pgx.Tx, id, kind string, convPerDay float64, today time.Time) error {
	var brand *string
	var objective string
	err := tx.QueryRow(ctx, `SELECT s.brand_id::text, e.objective FROM ad_suggestions s JOIN ad_entities e ON e.id = s.ad_entity_id WHERE s.id = $1`, id).Scan(&brand, &objective)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("suggestion not found")
	}
	if err != nil {
		return err
	}
	windowDays, lagDays := int(paidads.MinJudgementWindow/(24*time.Hour)), 0
	if brand != nil {
		lag, err := paidads.ClickToPaymentLag(ctx, tx, *brand)
		if err != nil {
			return err
		}
		windowDays, lagDays = windowOf(lag)
	}
	learn, days, note := Window(kind, windowDays, lagDays, objective == "performance_max")
	d := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	from := d.AddDate(0, 0, learn)
	until := from.AddDate(0, 0, days-1)
	tag, err := tx.Exec(ctx, `UPDATE ad_suggestions SET measure_from = $2, measure_until = $3, window_note = $4, updated_at = now() WHERE id = $1`, id, from, until, note)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("suggestion not found")
	}
	return err
}

// windowOf is the brand's judgement window in days and the trailing days
// left out for late payments (the median lag, at most two weeks).
func windowOf(lag paidads.Lag) (windowDays, lagDays int) {
	w := paidads.JudgementWindow(lag)
	windowDays = int(w / (24 * time.Hour))
	if w%(24*time.Hour) != 0 {
		windowDays++
	}
	lagDays = int(lag.Median / (24 * time.Hour))
	if lag.Median%(24*time.Hour) != 0 && lag.Median > 0 {
		lagDays++
	}
	return windowDays, min(lagDays, maxLagDays)
}
