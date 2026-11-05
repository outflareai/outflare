package ads

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Tests (step 4 of the paid ads plan) run as Google Ads experiments: a
// custom experiment on one search or display campaign, with a control arm
// (the campaign) and a trial arm (a copy Google makes and serves to a share
// of the campaign's traffic) that bids to a different target cost per
// conversion. Keeping it promotes the trial's settings to the campaign;
// rolling back ends the experiment and leaves the campaign as it was.

var _ c.AdTester = GoogleAds{}

// googleExperimentTypes maps a campaign's channel to its experiment type.
var googleExperimentTypes = map[string]string{"search": "SEARCH_CUSTOM", "display": "DISPLAY_CUSTOM"}

func (GoogleAds) TestKinds() []string { return []string{c.AdTestBidding} }

// experiment checks that run names one of the account's experiments, so
// it can go in a path.
func (g GoogleAds) experiment(acct c.AdAccount, run c.AdTestRun) (string, error) {
	prefix := "customers/" + acct.ID + "/experiments/"
	id := strings.TrimPrefix(run.ID, prefix)
	if !strings.HasPrefix(run.ID, prefix) || id == "" {
		return "", c.Errf(c.ErrInvalid, "not a Google Ads experiment of account %s", acct.ID)
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return "", c.Errf(c.ErrInvalid, "not a Google Ads experiment of account %s", acct.ID)
	}
	return run.ID, nil
}

func (g GoogleAds) StartTest(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, spec c.AdTestSpec) (c.AdTestRun, error) {
	if spec.Kind != c.AdTestBidding || spec.TargetCPAMicros == nil || *spec.TargetCPAMicros <= 0 {
		return c.AdTestRun{}, c.Errf(c.ErrInvalid, "Google Ads tests try a different target cost per conversion; test a budget as a change instead")
	}
	if spec.SplitPercent < 10 || spec.SplitPercent > 90 {
		return c.AdTestRun{}, c.Errf(c.ErrInvalid, "the trial gets between 10%% and 90%% of the traffic")
	}
	cp, _, err := g.googleCampaign(ctx, cl, creds, acct, spec.CampaignID)
	if err != nil {
		return c.AdTestRun{}, err
	}
	typ := googleExperimentTypes[cp.Objective]
	if typ == "" {
		return c.AdTestRun{}, c.Errf(c.ErrInvalid, "Google Ads experiments here run on search and display campaigns, not %s", cp.Objective)
	}
	exp, err := g.mutate(ctx, cl, creds, acct, "experiments", map[string]any{"name": spec.Name, "suffix": "[test]", "type": typ, "status": "SETUP",
		"startDate": spec.StartDate, "endDate": spec.EndDate})
	if err != nil {
		return c.AdTestRun{}, err
	}
	run := c.AdTestRun{ID: exp}
	if _, err := g.experiment(acct, run); err != nil {
		return c.AdTestRun{}, err
	}
	// Both arms at once: their traffic splits must add up to 100.
	var arms struct {
		Results []struct {
			ExperimentArm struct {
				InDesignCampaigns []string `json:"inDesignCampaigns"`
			} `json:"experimentArm"`
		} `json:"results"`
	}
	err = g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/experimentArms:mutate", map[string]any{
		"responseContentType": "MUTABLE_RESOURCE",
		"operations": []any{
			map[string]any{"create": map[string]any{"experiment": exp, "name": "Control", "control": true, "trafficSplit": 100 - spec.SplitPercent,
				"campaigns": []string{g.resource(acct, "campaigns", spec.CampaignID)}}},
			map[string]any{"create": map[string]any{"experiment": exp, "name": "Trial", "control": false, "trafficSplit": spec.SplitPercent}},
		}}, &arms)
	if err != nil {
		return run, err
	}
	var draft string
	for _, a := range arms.Results {
		if len(a.ExperimentArm.InDesignCampaigns) > 0 {
			draft = a.ExperimentArm.InDesignCampaigns[0]
		}
	}
	if !strings.HasPrefix(draft, "customers/"+acct.ID+"/campaigns/") {
		return run, c.Errf(c.ErrUnavailable, "Google Ads made no trial campaign for the experiment")
	}
	run.TrialCampaignID = draft[strings.LastIndex(draft, "/")+1:]
	// The trial's one difference: what it bids to.
	op := map[string]any{"update": map[string]any{"resourceName": draft, "maximizeConversions": map[string]any{"targetCpaMicros": strconv.FormatInt(*spec.TargetCPAMicros, 10)}},
		"updateMask": "maximize_conversions.target_cpa_micros"}
	if err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/campaigns:mutate",
		map[string]any{"operations": []any{op}}, nil); err != nil {
		return run, err
	}
	return run, g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/"+exp+":scheduleExperiment", map[string]any{}, nil)
}

func (g GoogleAds) TestDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, spec c.AdTestSpec, run c.AdTestRun, from, to string) ([]c.AdTestDay, error) {
	exp, err := g.experiment(acct, run)
	if err != nil {
		return nil, err
	}
	login := acct.Meta["login_customer_id"]
	// Which campaign serves each arm now: Google may have made the trial
	// campaign anew when it scheduled the experiment.
	rows, err := g.search(ctx, cl, creds, acct.ID, login,
		"SELECT experiment_arm.control, experiment_arm.campaigns, experiment_arm.in_design_campaigns FROM experiment_arm WHERE experiment_arm.experiment = '"+exp+"'")
	if err != nil {
		return nil, err
	}
	arm := map[string]string{spec.CampaignID: c.ArmControl}
	if run.TrialCampaignID != "" {
		arm[run.TrialCampaignID] = c.ArmTrial
	}
	for _, r := range rows {
		if r.ExperimentArm.Control {
			continue
		}
		for _, cp := range r.ExperimentArm.Campaigns {
			if id := cp[strings.LastIndex(cp, "/")+1:]; strings.HasPrefix(cp, "customers/"+acct.ID+"/campaigns/") {
				arm[id] = c.ArmTrial
			}
		}
	}
	ids := make([]string, 0, len(arm))
	for id := range arm {
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return nil, c.Errf(c.ErrInvalid, "Google Ads campaign ids are numbers")
		}
		ids = append(ids, id)
	}
	q := fmt.Sprintf("SELECT campaign.id, segments.date, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions FROM campaign WHERE campaign.id IN (%s) AND segments.date BETWEEN '%s' AND '%s'",
		strings.Join(ids, ", "), mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	days, err := g.search(ctx, cl, creds, acct.ID, login, q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdTestDay, 0, len(days))
	for _, r := range days {
		a := arm[fmt.Sprint(int64(r.Campaign.ID))]
		if a == "" {
			continue
		}
		m := r.Metrics
		out = append(out, c.AdTestDay{Arm: a, Date: r.Segments.Date, SpendMicros: int64(m.CostMicros), Impressions: int64(m.Impressions),
			Clicks: int64(m.Clicks), Conversions: float64(m.Conversions)})
	}
	return out, nil
}

func (g GoogleAds) EndTest(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, spec c.AdTestSpec, run c.AdTestRun, keep bool) error {
	exp, err := g.experiment(acct, run)
	if err != nil {
		return err
	}
	verb := ":endExperiment"
	if keep {
		verb = ":promoteExperiment"
	}
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/"+exp+verb, map[string]any{}, nil)
}
