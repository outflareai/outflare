package ads

import (
	"context"
	"strconv"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Tests (step 4 of the paid ads plan) run as Meta split tests (ad studies
// of type SPLIT_TEST): Outflare copies the campaign (paused, with its ad
// sets and ads), gives the copy the trial's daily budget, and asks Meta to
// split the audience between the campaign and the copy, so no one sees
// both. Keeping it gives the campaign the trial's budget; either way the
// copy is paused and the study ended.

var _ c.AdTester = MetaAds{}

func (MetaAds) TestKinds() []string { return []string{c.AdTestBudget} }

// metaTime is midnight at the start of day in the account's time zone.
func metaTime(day, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	t, _ := time.ParseInLocation(time.DateOnly, day, loc)
	return t
}

func (m MetaAds) StartTest(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, spec c.AdTestSpec) (c.AdTestRun, error) {
	if spec.Kind != c.AdTestBudget || spec.DailyBudgetMicros == nil || *spec.DailyBudgetMicros <= 0 {
		return c.AdTestRun{}, c.Errf(c.ErrInvalid, "Meta tests here try a different daily budget")
	}
	if !metaID(spec.CampaignID) {
		return c.AdTestRun{}, c.Errf(c.ErrInvalid, "Meta campaign ids are numbers")
	}
	if spec.SplitPercent < 10 || spec.SplitPercent > 90 {
		return c.AdTestRun{}, c.Errf(c.ErrInvalid, "the trial gets between 10%% and 90%% of the audience")
	}
	// Split tests belong to the business that owns the ad account.
	var owner struct {
		Business struct {
			ID string `json:"id"`
		} `json:"business"`
	}
	if err := m.call(cl, creds).get(ctx, m.base(cl)+"/act_"+esc(acct.ID)+"?fields=business", &owner); err != nil {
		return c.AdTestRun{}, err
	}
	if !metaID(owner.Business.ID) {
		return c.AdTestRun{}, c.Errf(c.ErrInvalid, "Meta split tests need the ad account to belong to a business (Business Settings, Accounts, Ad accounts)")
	}
	var cp struct {
		CopiedCampaignID string `json:"copied_campaign_id"`
	}
	if err := m.call(cl, creds).post(ctx, m.base(cl)+"/"+esc(spec.CampaignID)+"/copies", map[string]any{"deep_copy": true, "status_option": "PAUSED",
		"rename_options": map[string]any{"rename_suffix": " (test)"}}, &cp); err != nil {
		return c.AdTestRun{}, err
	}
	if !metaID(cp.CopiedCampaignID) {
		return c.AdTestRun{}, c.Errf(c.ErrUnavailable, "Meta made no copy of the campaign")
	}
	run := c.AdTestRun{TrialCampaignID: cp.CopiedCampaignID}
	if err := m.SetCampaignDailyBudget(ctx, cl, creds, acct, run.TrialCampaignID, *spec.DailyBudgetMicros); err != nil {
		return run, err
	}
	start := metaTime(spec.StartDate, acct.TimeZone)
	if soon := time.Now().Add(10 * time.Minute); start.Before(soon) {
		start = soon
	}
	end := metaTime(spec.EndDate, acct.TimeZone).AddDate(0, 0, 1)
	var study struct {
		ID string `json:"id"`
	}
	err := m.call(cl, creds).post(ctx, m.base(cl)+"/"+esc(owner.Business.ID)+"/ad_studies", map[string]any{
		"name": spec.Name, "type": "SPLIT_TEST", "start_time": start.Unix(), "end_time": end.Unix(),
		"cells": []any{
			map[string]any{"name": "Control", "treatment_percentage": 100 - spec.SplitPercent, "campaigns": []string{spec.CampaignID}},
			map[string]any{"name": "Trial", "treatment_percentage": spec.SplitPercent, "campaigns": []string{run.TrialCampaignID}},
		}}, &study)
	if err != nil {
		return run, err
	}
	if !metaID(study.ID) {
		return run, c.Errf(c.ErrUnavailable, "Meta made no split test")
	}
	run.ID = study.ID
	return run, m.SetCampaignStatus(ctx, cl, creds, acct, run.TrialCampaignID, true)
}

func (m MetaAds) TestDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, spec c.AdTestSpec, run c.AdTestRun, from, to string) ([]c.AdTestDay, error) {
	days, err := m.AdDays(ctx, cl, creds, acct, from, to)
	if err != nil {
		return nil, err
	}
	arm := map[string]string{spec.CampaignID: c.ArmControl, run.TrialCampaignID: c.ArmTrial}
	var out []c.AdTestDay
	for _, d := range days {
		a := arm[d.CampaignID]
		if a == "" || d.CampaignID == "" {
			continue
		}
		var conv float64
		if d.Conversions != nil {
			conv = *d.Conversions
		}
		out = append(out, c.AdTestDay{Arm: a, Date: d.Date, SpendMicros: d.SpendMicros, Impressions: d.Impressions, Clicks: d.Clicks, Conversions: conv})
	}
	return out, nil
}

func (m MetaAds) EndTest(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, spec c.AdTestSpec, run c.AdTestRun, keep bool) error {
	if !metaID(run.TrialCampaignID) {
		return c.Errf(c.ErrInvalid, "the test has no trial campaign")
	}
	if keep {
		if spec.DailyBudgetMicros == nil {
			return c.Errf(c.ErrInvalid, "the test names no budget to keep")
		}
		if err := m.SetCampaignDailyBudget(ctx, cl, creds, acct, spec.CampaignID, *spec.DailyBudgetMicros); err != nil {
			return err
		}
	}
	if err := m.SetCampaignStatus(ctx, cl, creds, acct, run.TrialCampaignID, false); err != nil {
		return err
	}
	if !metaID(run.ID) {
		return nil
	}
	// End the study now, so Meta stops splitting the audience.
	return m.call(cl, creds).post(ctx, m.base(cl)+"/"+esc(run.ID), map[string]any{"end_time": strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)}, nil)
}
