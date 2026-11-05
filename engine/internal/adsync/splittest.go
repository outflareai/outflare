package adsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Tests inside the networks (step 4 of the paid ads plan): an ad_tests
// row a person started (the API writes it with its audit event), carried
// out by this job in three steps on the workspace's own account. start
// creates the test on the network (a Google Ads experiment, a Meta split
// test); check reads both arms after each sync and, once the test has run
// its days and the conversion lag, gives a verdict; end keeps the trial's
// change or rolls it back, as the person chose. Every network call is the
// side effect, so it runs here and never in a request handler.

// JobTest carries out one step of an ad test.
const JobTest = "ads.test"

// Test steps.
const (
	TestStart = "start"
	TestCheck = "check"
	TestEnd   = "end"
)

// TestSettings is what a test arm does: its daily budget or the cost per
// conversion it bids to, as kept in an ad_tests row's control and trial.
type TestSettings struct {
	DailyBudgetMicros *int64 `json:"daily_budget_micros,omitempty"`
	TargetCPAMicros   *int64 `json:"target_cpa_micros,omitempty"`
}

type testPayload struct {
	Test string `json:"test_id"`
	Step string `json:"step"`
}

// EnqueueTest queues one step of a test, once per key: start and end once
// per test, a check once per test and day.
func EnqueueTest(ctx context.Context, q jobs.Querier, ws, testID, step, day string) error {
	key := "ads.test:" + step + ":" + testID
	if step == TestCheck {
		key += ":" + day
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobTest, WorkspaceID: &ws, Payload: testPayload{Test: testID, Step: step},
		IdempotencyKey: key, MaxAttempts: 6})
	return err
}

func (s *Syncer) testJob(ctx context.Context, job jobs.Job) error {
	var p testPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	return s.RunTestStep(ctx, *job.WorkspaceID, p.Test, p.Step, job.Attempt >= job.MaxAttempts)
}

type adTest struct {
	id, kind, status, decision, connector, integrationID, campaign, campaignExternal string
	brand, suggestion                                                                *string
	acct                                                                             account
	split                                                                            int
	trial                                                                            TestSettings
	run                                                                              connectors.AdTestRun
	name                                                                             string
	startsOn, endsOn, measureFrom                                                    time.Time
	target                                                                           *int64
	verdict                                                                          string
}

func (t adTest) spec() connectors.AdTestSpec {
	return connectors.AdTestSpec{Name: t.name, CampaignID: t.campaignExternal, Kind: t.kind, SplitPercent: t.split,
		StartDate: t.startsOn.Format(time.DateOnly), EndDate: t.endsOn.Format(time.DateOnly),
		DailyBudgetMicros: t.trial.DailyBudgetMicros, TargetCPAMicros: t.trial.TargetCPAMicros}
}

func (s *Syncer) loadTest(ctx context.Context, ws, id string) (adTest, error) {
	var t adTest
	var trial, meta []byte
	var runID, trialCampaign *string
	err := s.Pool.QueryRow(ctx, `
		SELECT x.id, x.kind, x.status, x.decision, x.brand_id, x.suggestion_id, x.name, x.split_percent, x.trial, x.platform_test_id, x.trial_campaign_id,
			x.starts_on, x.ends_on, x.measure_from, x.verdict, e.id, e.external_id,
			COALESCE(e.target_cost_per_conversion_micros, a.target_cost_per_conversion_micros, e.platform_target_cpa_micros),
			a.connector, a.integration_id, a.id, a.external_id, a.name, a.currency, a.time_zone, a.meta
		FROM ad_tests x JOIN ad_entities e ON e.id = x.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE x.workspace_id = $1 AND x.id = $2`, ws, id).
		Scan(&t.id, &t.kind, &t.status, &t.decision, &t.brand, &t.suggestion, &t.name, &t.split, &trial, &runID, &trialCampaign,
			&t.startsOn, &t.endsOn, &t.measureFrom, &t.verdict, &t.campaign, &t.campaignExternal, &t.target,
			&t.connector, &t.integrationID, &t.acct.id, &t.acct.externalID, &t.acct.name, &t.acct.currency, &t.acct.timeZone, &meta)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal(trial, &t.trial)
	_ = json.Unmarshal(meta, &t.acct.meta)
	if runID != nil {
		t.run.ID = *runID
	}
	if trialCampaign != nil {
		t.run.TrialCampaignID = *trialCampaign
	}
	return t, nil
}

// RunTestStep carries out one step of a test. last says this is the job's
// last try, so an unreachable network ends the step instead of retrying.
func (s *Syncer) RunTestStep(ctx context.Context, ws, id, step string, last bool) error {
	t, err := s.loadTest(ctx, ws, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	want := map[string]string{TestStart: "starting", TestCheck: "running", TestEnd: "ending"}[step]
	if want == "" || t.status != want {
		return nil
	}
	ev := track.Event{Name: "ad_test." + map[string]string{TestStart: "started", TestCheck: "checked", TestEnd: "ended"}[step], WorkspaceID: ws,
		ActorType: "system", Surface: "job", Props: map[string]any{"connector": t.connector, "kind": t.kind, "test_id": t.id, "step": step}}
	if t.brand != nil {
		ev.Props["brand_id"] = *t.brand
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, t.integrationID)
	var tester connectors.AdTester
	if err == nil {
		var ok bool
		if tester, ok = o.Connector.(connectors.AdTester); !ok {
			err = connectors.Errf(connectors.ErrInvalid, "%s can't run tests", o.Connector.Definition().Name)
		}
	} else if errors.Is(err, connectors.ErrNotFound) {
		err = connectors.Errf(connectors.ErrInvalid, "the ad platform connection is gone")
	}
	if err == nil {
		switch step {
		case TestStart:
			err = s.startTest(ctx, ws, t, o, tester)
		case TestCheck:
			err = s.checkTest(ctx, ws, t, o, tester)
		case TestEnd:
			err = s.endTest(ctx, ws, t, o, tester)
		}
	}
	if err == nil {
		metrics.AdTestStep(t.connector, step, "success")
		if step != TestCheck {
			track.Emit(ctx, ev)
		}
		return nil
	}
	var db dbError
	retry := errors.As(err, &db) || (connectors.KindOf(err) == connectors.ErrUnavailable && !last)
	outcome, code := "failed", "connector_"+connectors.KindOf(err)
	if errors.As(err, &db) {
		code = "internal"
	}
	if retry {
		outcome = "error"
	}
	metrics.AdTestStep(t.connector, step, outcome)
	ev.Name, ev.Outcome = "ad_test.failed", track.Failure
	if retry {
		ev.Outcome = track.Error
	}
	track.Fail(ctx, s.Log, ev, code, err)
	if retry {
		return err
	}
	return s.testFailed(ctx, ws, t, step, err)
}

// testFailed records a step the network refused. A refused start fails the
// test (and the suggestion it tested); a refused end leaves it as it was,
// with the error, so a person can try again; a refused check waits for
// the next sync.
func (s *Syncer) testFailed(ctx context.Context, ws string, t adTest, step string, cause error) error {
	msg := trim(cause.Error(), 500)
	switch step {
	case TestStart:
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE ad_tests SET status = 'failed', error = $3, platform_test_id = $4, trial_campaign_id = $5, ended_at = now(), updated_at = now()
				WHERE workspace_id = $1 AND id = $2 AND status = 'starting'`, ws, t.id, msg, nullable(t.run.ID), nullable(t.run.TrialCampaignID)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE ad_suggestions SET status = 'judged', verdict = 'not_sure', judged_at = now(), updated_at = now(),
				verdict_note = 'The network refused the test, so nothing was changed: ' || $2::text
				WHERE test_id = $1 AND status = 'testing'`, t.id, msg)
			return err
		})
	case TestEnd:
		_, err := s.Pool.Exec(ctx, `UPDATE ad_tests SET status = CASE WHEN verdict = '' THEN 'running' ELSE 'judged' END, decision = '', error = $3, updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'ending'`, ws, t.id, msg)
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE ad_tests SET error = $3, checked_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, t.id, msg)
	return err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Syncer) startTest(ctx context.Context, ws string, t adTest, o *connectors.Opened, tester connectors.AdTester) error {
	run, err := tester.StartTest(ctx, o.Client, o.Creds, t.acct.ad(), t.spec())
	if err != nil {
		if run.ID != "" || run.TrialCampaignID != "" {
			// Something was made before the network refused the rest: undo
			// it so no half-made test serves, and never retry, which would
			// make another.
			t.run = run
			if endErr := tester.EndTest(ctx, o.Client, o.Creds, t.acct.ad(), t.spec(), run, false); endErr != nil {
				err = fmt.Errorf("%w (and ending what was made failed: %v)", err, endErr)
			}
			return connectors.Errf(connectors.ErrInvalid, "%v", err)
		}
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE ad_tests SET status = 'running', platform_test_id = $3, trial_campaign_id = $4, error = NULL, updated_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status = 'starting'`, ws, t.id, nullable(run.ID), nullable(run.TrialCampaignID)); err != nil {
		return dbError{err}
	}
	return nil
}

// checkTest reads both arms since the trial settled and, once the test has
// run its days and the conversion lag (or the trial costs over twice the
// target three days running), judges it.
func (s *Syncer) checkTest(ctx context.Context, ws string, t adTest, o *connectors.Opened, tester connectors.AdTester) error {
	yesterday := today(s.now(), t.acct.timeZone).AddDate(0, 0, -1)
	to := t.endsOn
	if yesterday.Before(to) {
		to = yesterday
	}
	if to.Before(t.measureFrom) {
		_, err := s.Pool.Exec(ctx, `UPDATE ad_tests SET checked_at = now(), error = NULL, updated_at = now() WHERE id = $1`, t.id)
		if err != nil {
			return dbError{err}
		}
		return nil
	}
	days, err := tester.TestDays(ctx, o.Client, o.Creds, t.acct.ad(), t.spec(), t.run, t.measureFrom.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return err
	}
	var control, trial adsuggest.Arm
	// The trial's totals as of each day, for the early stop.
	byDay := map[string]adsuggest.Arm{}
	for _, d := range days {
		a := &control
		if d.Arm == connectors.ArmTrial {
			a = &trial
			x := byDay[d.Date]
			x.Spend += d.SpendMicros
			x.Conv += d.Conversions
			byDay[d.Date] = x
		}
		a.Spend += d.SpendMicros
		a.Conv += d.Conversions
		a.Clicks += d.Clicks
		a.Impressions += d.Impressions
	}
	n := int(to.Sub(t.measureFrom).Hours()/24) + 1
	control.Days, trial.Days = n, n
	var running []adsuggest.Arm
	var sum adsuggest.Arm
	for d := t.measureFrom; !d.After(to); d = d.AddDate(0, 0, 1) {
		x := byDay[d.Format(time.DateOnly)]
		sum.Spend += x.Spend
		sum.Conv += x.Conv
		running = append(running, sum)
	}
	verdict, note, score := adsuggest.SplitVerdict(t.kind, control, trial, t.target)
	early := adsuggest.SplitOverTarget(running, t.target)
	done := early || !yesterday.Before(t.endsOn.AddDate(0, 0, adsuggest.SplitLag))
	if early {
		verdict, note = adsuggest.DidNotWork, "Judged early: the trial's conversions have cost more than twice the target three days running. Roll it back. "+note
	}
	res, _ := json.Marshal(map[string]any{"control": control, "trial": trial, "z": score, "through": to.Format(time.DateOnly), "early": early,
		"verdict_so_far": verdict, "note_so_far": note})
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if !done {
			_, err := tx.Exec(ctx, `UPDATE ad_tests SET result = $2, checked_at = now(), error = NULL, updated_at = now() WHERE id = $1 AND status = 'running'`, t.id, res)
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE ad_tests SET status = 'judged', result = $2, verdict = $3, verdict_note = $4, checked_at = now(), judged_at = now(), error = NULL, updated_at = now()
			WHERE id = $1 AND status = 'running'`, t.id, res, verdict, note)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		data := map[string]any{"test_id": t.id, "campaign_id": t.campaign, "connector": t.connector, "kind": t.kind, "verdict": verdict, "early": early}
		if t.brand != nil {
			data["brand_id"] = *t.brand
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_test.judged", TargetType: "ad_test", TargetID: t.id,
			Data: data, Props: map[string]any{"connector": t.connector, "kind": t.kind, "verdict": verdict, "early": early}})
	})
	if err != nil {
		return dbError{err}
	}
	return nil
}

// endTest keeps the trial's change or rolls it back, then closes the test
// and the suggestion it tested.
func (s *Syncer) endTest(ctx context.Context, ws string, t adTest, o *connectors.Opened, tester connectors.AdTester) error {
	keep := t.decision == "keep"
	if err := tester.EndTest(ctx, o.Client, o.Creds, t.acct.ad(), t.spec(), t.run, keep); err != nil {
		return err
	}
	status := "rolled_back"
	if keep {
		status = "kept"
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ad_tests SET status = $3, ended_at = now(), error = NULL, updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'ending'`, ws, t.id, status); err != nil {
			return err
		}
		if keep && t.trial.DailyBudgetMicros != nil {
			// The mirror shows the kept budget until the next sync reads it.
			if _, err := tx.Exec(ctx, `UPDATE ad_entities SET daily_budget_micros = $2, updated_at = now() WHERE id = $1`, t.campaign, *t.trial.DailyBudgetMicros); err != nil {
				return err
			}
		}
		word := map[bool]string{true: "Tested, and kept. ", false: "Tested, and rolled back. "}[keep]
		_, err := tx.Exec(ctx, `UPDATE ad_suggestions s SET status = 'judged', verdict = COALESCE(NULLIF(x.verdict, ''), 'not_sure'),
				verdict_note = $2 || x.verdict_note, result = x.result, judged_at = now(), updated_at = now()
			FROM ad_tests x WHERE x.id = $1 AND s.test_id = x.id AND s.status = 'testing'`, t.id, word)
		return err
	})
	if err != nil {
		return dbError{err}
	}
	return nil
}

// queueTestChecks queues today's check of every running test on the
// connection's accounts, after a sync brought fresh numbers.
func (s *Syncer) queueTestChecks(ctx context.Context, ws, integrationID string) error {
	rows, err := s.Pool.Query(ctx, `SELECT x.id FROM ad_tests x JOIN ad_entities e ON e.id = x.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE x.workspace_id = $1 AND a.integration_id = $2 AND x.status = 'running'`, ws, integrationID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	day := s.now().UTC().Format(time.DateOnly)
	for _, id := range ids {
		if err := EnqueueTest(ctx, s.Pool, ws, id, TestCheck, day); err != nil {
			return err
		}
	}
	return nil
}
