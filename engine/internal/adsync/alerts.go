package adsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/paidads"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Alerts (plans/paid-ads/plan.md, sync and alerts): after each hourly sync
// a job reads every campaign of the workspace and writes what needs a
// person's eye into the findings queue (ad_suggestions) with autonomy
// alert: spend far off its usual for the hour, a month pacing off its
// budgets, a campaign that stopped serving, clicks that bring no sign-ups,
// disapproved ads, Google applying its own recommendations, conversions
// refused by the platform, and tracking that stopped working. A campaign
// that spent more than twice its daily budget today is an alert for a
// person; it is paused on its own (an ad_changes row like a person's,
// actor system, the finding marked applied so a person can revert it)
// only where an extension's hook allows it (paidads.AutoApprove,
// Host.SetAdAutoApprove). Every alert is one open row per campaign (or per
// account, pinned to its biggest campaign) and kind, updated while it
// holds and expired when it no longer does. The job is keyed per hour, so
// a sync run twice in an hour checks once.
//
// The numbers come from ad_metrics_daily, which the hourly sync keeps
// current for today, so the intraday rules (spend anomaly, flatline) read
// today's row as "so far today" and scale full-day baselines to the hour.

const (
	JobAlerts = "ads.alerts"

	// Alert kinds, as kept in ad_suggestions.kind and rule.
	AlertSpendAnomaly    = "alert.spend_anomaly"
	AlertPacing          = "alert.pacing"
	AlertFlatline        = "alert.flatline"
	AlertZeroConversions = "alert.zero_conversions"
	AlertDisapproved     = "alert.disapproved"
	AlertAutoApplyOn     = "alert.auto_apply_on"
	AlertSendsFailing    = "alert.sends_failing"
	AlertTrackingBroken  = "alert.tracking_broken"
	// OverspendPause is the protective change: pause a campaign that spent
	// more than twice its daily budget today.
	OverspendPause = "overspend_pause"

	// alertRuleVersion is written to rule_version; bump it when a rule's
	// maths change, so old rows keep saying which version made them.
	alertRuleVersion = 1

	// Spend anomaly: today's spend so far against the same weekday over
	// the previous anomalyWeeks, the platform's numbers lagging
	// anomalyLagHours; fires outside the band and beyond anomalySigmas
	// standard deviations, for at least anomalyMinMicros of difference,
	// and not before anomalyMinHour hours of the day (after the lag) are
	// in.
	anomalyWeeks     = 4
	anomalyLagHours  = 3
	anomalyMinHour   = 4
	anomalyBand      = 0.5
	anomalySigmas    = 3.0
	anomalyMinMicros = 20_000_000
	// Pacing: month-to-date spend against monthMultiple times the daily
	// budgets, scaled to the day of the month, outside pacingLow to
	// pacingHigh, from pacingAfterDay on.
	monthMultiple  = 30.4
	pacingLow      = 0.9
	pacingHigh     = 1.1
	pacingAfterDay = 7
	// Flatline: an active campaign with no impressions today once the
	// day is flatlineMinHour hours in (six hours of a day it serves),
	// when it served on flatlineServingDays of the last 7.
	flatlineMinHour     = 12
	flatlineServingDays = 6
	// Zero conversions: zeroConvClicks clicks (zeroConvBrandClicks for a
	// campaign named after the brand) over zeroConvDays with no sign-up.
	zeroConvClicks      = 300
	zeroConvBrandClicks = 100
	zeroConvDays        = 30
	// Sends failing: the platform refused sendsFailingShare of the
	// conversions sent in 24 hours, out of at least sendsMin.
	sendsFailingShare = 0.10
	sendsMin          = 10
	// Tracking broken: under trackingCapture of paid visits in 24 hours
	// carried a click id (out of at least trackingMinVisits), or paid
	// visits with campaign ids brought no sign-up while the 7-day median
	// of daily sign-ups was trackingMedianSignups or more.
	trackingCapture       = 0.5
	trackingMinVisits     = 20
	trackingMedianSignups = 3
	// Overspend: today's spend over overspendMultiple times the daily budget.
	overspendMultiple = 2.0
)

// AlertKinds lists every kind this job writes, for the API's filters and
// the status blocker.
var AlertKinds = []string{AlertSpendAnomaly, AlertPacing, AlertFlatline, AlertZeroConversions, AlertDisapproved, AlertAutoApplyOn, AlertSendsFailing, AlertTrackingBroken, OverspendPause}

// CriticalAlertKinds are the kinds the status blocker calls critical.
var CriticalAlertKinds = []string{AlertTrackingBroken, AlertSendsFailing, OverspendPause, AlertSpendAnomaly}

type alertsPayload struct {
	Workspace string `json:"workspace_id"`
}

// EnqueueAlerts queues the alert check for a workspace, at most once an
// hour.
func EnqueueAlerts(ctx context.Context, q jobs.Querier, ws string, now time.Time) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobAlerts, WorkspaceID: &ws, Payload: alertsPayload{Workspace: ws},
		IdempotencyKey: "ads.alerts:" + ws + ":" + now.UTC().Format("2006-01-02T15"), MaxAttempts: 3})
	return err
}

func (s *Syncer) alertsJob(ctx context.Context, job jobs.Job) error {
	if job.WorkspaceID == nil {
		return nil
	}
	_, err := s.RunAlerts(ctx, *job.WorkspaceID)
	return err
}

// AlertResult is what one run did.
type AlertResult struct {
	// Raised is new findings by kind; Held how many open ones still hold;
	// Expired how many no longer do; Paused how many campaigns the
	// protective pause stopped.
	Raised  map[string]int
	Held    int
	Expired int
	Paused  int
}

// finding is one row the run wants in the queue.
type finding struct {
	c        campaignRow
	kind     string
	autonomy string
	unit     string
	reason   string
	evidence map[string]any
	proposed map[string]any
	shadow   bool
	// account is set for an account-wide finding (pacing, sends, tracking,
	// auto-apply), pinned to the account's biggest campaign c.
	account string
}

// campaignRow is a campaign with its account, as the rules see it.
type campaignRow struct {
	id, externalID, name, status string
	budget                       int64
	brand                        *string
	brandName                    string
	acctID, acctName, connector  string
	integrationID, currency, tz  string
	acctExt                      string
	acctMeta                     map[string]string
}

// ad is the campaign's account as the platform calls take it.
func (c campaignRow) ad() connectors.AdAccount {
	return connectors.AdAccount{ID: c.acctExt, Name: c.acctName, Currency: c.currency, TimeZone: c.tz, Meta: c.acctMeta}
}

// dayRow is one campaign-day's numbers.
type dayRow struct {
	spend, impressions, clicks int64
	conv                       float64
}

// RunAlerts checks one workspace now.
func (s *Syncer) RunAlerts(ctx context.Context, ws string) (AlertResult, error) {
	res := AlertResult{Raised: map[string]int{}}
	ev := track.Event{Name: "ad_alerts.checked", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{}}
	err := s.runAlerts(ctx, ws, &res)
	if err != nil {
		metrics.AdAlertRun("error")
		ev.Name = "ad_alerts.failed"
		track.Fail(ctx, s.Log, ev, "internal", err)
		return res, err
	}
	metrics.AdAlertRun("success")
	raised := 0
	for _, n := range res.Raised {
		raised += n
	}
	ev.Props["raised"], ev.Props["held"], ev.Props["expired"], ev.Props["paused"] = raised, res.Held, res.Expired, res.Paused
	track.Emit(ctx, ev)
	return res, nil
}

func (s *Syncer) runAlerts(ctx context.Context, ws string, res *AlertResult) error {
	now := s.now()
	camps, err := s.alertCampaigns(ctx, ws)
	if err != nil {
		return err
	}
	if len(camps) == 0 {
		return s.expireAlerts(ctx, ws, nil, res)
	}
	days, err := s.alertDays(ctx, ws, camps)
	if err != nil {
		return err
	}
	firstParty, err := tableExists(ctx, s.Pool, "ad_acquisitions")
	if err != nil {
		return err
	}
	var findings []finding
	var overspent []campaignRow
	byAccount := map[string][]campaignRow{}
	for _, c := range camps {
		byAccount[c.acctID] = append(byAccount[c.acctID], c)
		if c.status != connectors.AdActive {
			continue
		}
		d := days[c.id]
		loc := location(c.tz)
		local := now.In(loc)
		today := local.Format(time.DateOnly)
		if f, ok := spendAnomaly(c, d, local); ok {
			findings = append(findings, f)
		}
		if f, ok := flatline(c, d, local); ok {
			findings = append(findings, f)
		}
		if f, ok, err := s.zeroConversions(ctx, c, d, today, firstParty); err != nil {
			return err
		} else if ok {
			findings = append(findings, f)
		}
		if c.budget > 0 && float64(d[today].spend) > overspendMultiple*float64(c.budget) {
			overspent = append(overspent, c)
		}
	}
	for _, list := range byAccount {
		if f, ok := pacing(list, days, now); ok {
			findings = append(findings, f)
		}
	}
	more, err := s.disapproved(ctx, ws, camps)
	if err != nil {
		return err
	}
	findings = append(findings, more...)
	more, err = s.sendsFailing(ctx, ws, byAccount, days)
	if err != nil {
		return err
	}
	findings = append(findings, more...)
	more, err = s.trackingBroken(ctx, ws, camps, days, firstParty)
	if err != nil {
		return err
	}
	findings = append(findings, more...)
	findings = append(findings, s.autoApplyOn(ctx, ws, byAccount, days)...)
	for _, c := range overspent {
		f, paused, err := s.overspend(ctx, ws, c, days[c.id], now)
		if err != nil {
			return err
		}
		if paused {
			res.Paused++
			res.Raised[OverspendPause]++
			continue
		}
		findings = append(findings, f)
	}
	keep, err := s.writeFindings(ctx, ws, findings, res)
	if err != nil {
		return err
	}
	return s.expireAlerts(ctx, ws, keep, res)
}

// alertCampaigns lists the workspace's campaigns on syncing accounts.
func (s *Syncer) alertCampaigns(ctx context.Context, ws string) ([]campaignRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT e.id, e.external_id, e.name, e.status, COALESCE(e.daily_budget_micros, 0), `+entityBrand+`, COALESCE(b.name, ''),
			a.id, a.name, a.connector, a.integration_id, a.currency, a.time_zone, a.external_id, a.meta
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		LEFT JOIN brands b ON b.id = `+entityBrand+`
		WHERE e.workspace_id = $1 AND e.level = 'campaign' AND a.status = 'active' AND a.sync_on AND e.status IN ('active', 'paused')
		ORDER BY a.id, e.name, e.id`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []campaignRow
	for rows.Next() {
		var c campaignRow
		var meta []byte
		if err := rows.Scan(&c.id, &c.externalID, &c.name, &c.status, &c.budget, &c.brand, &c.brandName, &c.acctID, &c.acctName, &c.connector, &c.integrationID, &c.currency, &c.tz, &c.acctExt, &meta); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(meta, &c.acctMeta)
		out = append(out, c)
	}
	return out, rows.Err()
}

// alertDays reads the last 36 days of every campaign, by campaign and day.
func (s *Syncer) alertDays(ctx context.Context, ws string, camps []campaignRow) (map[string]map[string]dayRow, error) {
	ids := make([]string, len(camps))
	for i, c := range camps {
		ids[i] = c.id
	}
	rows, err := s.Pool.Query(ctx, `SELECT ad_entity_id, to_char(day, 'YYYY-MM-DD'), spend_micros, impressions, clicks, COALESCE(conversions, 0)
		FROM ad_metrics_daily WHERE workspace_id = $1 AND ad_entity_id = ANY($2::uuid[]) AND day >= current_date - 37`, ws, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]dayRow{}
	for rows.Next() {
		var id, day string
		var d dayRow
		if err := rows.Scan(&id, &day, &d.spend, &d.impressions, &d.clicks, &d.conv); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]dayRow{}
		}
		out[id][day] = d
	}
	for _, c := range camps {
		if out[c.id] == nil {
			out[c.id] = map[string]dayRow{}
		}
	}
	return out, rows.Err()
}

func location(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil && tz != "" {
		return loc
	}
	return time.UTC
}

// Anomaly is the spend anomaly maths: today's spend so far against the
// same weekday's full-day spend over the previous weeks, scaled to the
// hours of the day in (after the lag). It returns the expected spend so
// far, the standard deviation of the baseline, and whether today is far
// enough off to alert, with the direction (above, below).
func Anomaly(today int64, baseline []int64, hour int) (expected, sigma float64, fire bool, direction string) {
	if len(baseline) < anomalyWeeks-1 || hour < anomalyMinHour {
		return 0, 0, false, ""
	}
	share := float64(hour) / 24
	var sum float64
	scaled := make([]float64, len(baseline))
	for i, b := range baseline {
		scaled[i] = float64(b) * share
		sum += scaled[i]
	}
	expected = sum / float64(len(scaled))
	var sq float64
	for _, v := range scaled {
		sq += (v - expected) * (v - expected)
	}
	sigma = math.Sqrt(sq / float64(len(scaled)))
	diff := float64(today) - expected
	if diff > 0 {
		direction = "above"
	} else {
		direction = "below"
	}
	off := float64(today) > expected*(1+anomalyBand) || float64(today) < expected*(1-anomalyBand)
	fire = off && math.Abs(diff) > anomalySigmas*sigma && math.Abs(diff) >= anomalyMinMicros
	return expected, sigma, fire, direction
}

// spendAnomaly applies Anomaly to a campaign now.
func spendAnomaly(c campaignRow, d map[string]dayRow, local time.Time) (finding, bool) {
	hour := local.Hour() - anomalyLagHours
	today := local.Format(time.DateOnly)
	var baseline []int64
	for w := 1; w <= anomalyWeeks; w++ {
		day := local.AddDate(0, 0, -7*w).Format(time.DateOnly)
		if row, ok := d[day]; ok {
			baseline = append(baseline, row.spend)
		}
	}
	spend := d[today].spend
	expected, sigma, fire, direction := Anomaly(spend, baseline, hour)
	if !fire {
		return finding{}, false
	}
	reason := fmt.Sprintf("%s has spent %s so far today, %s its usual %s for a %s by this hour (the last %d weeks).",
		c.name, money(spend, c.currency), direction, money(int64(expected), c.currency), local.Weekday(), len(baseline))
	return finding{c: c, kind: AlertSpendAnomaly, autonomy: "alert", unit: "platform_conversions", reason: reason,
		evidence: map[string]any{"spend_micros": spend, "expected_micros": int64(expected), "sigma_micros": int64(sigma), "direction": direction,
			"hour": local.Hour(), "lag_hours": anomalyLagHours, "weeks": len(baseline), "weekday": local.Weekday().String(), "window": "today so far", "currency": c.currency}}, true
}

// flatline fires for an active campaign that usually serves and showed
// nothing today once the day is well in.
func flatline(c campaignRow, d map[string]dayRow, local time.Time) (finding, bool) {
	if local.Hour() < flatlineMinHour {
		return finding{}, false
	}
	today := local.Format(time.DateOnly)
	if d[today].impressions > 0 {
		return finding{}, false
	}
	served, total := 0, int64(0)
	for i := 1; i <= 7; i++ {
		row := d[local.AddDate(0, 0, -i).Format(time.DateOnly)]
		if row.impressions > 0 {
			served++
			total += row.impressions
		}
	}
	if served < flatlineServingDays {
		return finding{}, false
	}
	reason := fmt.Sprintf("%s has shown no ads today by %02d:00, though it showed about %d a day over the last week. Check its budget, billing and approvals.",
		c.name, local.Hour(), total/int64(served))
	return finding{c: c, kind: AlertFlatline, autonomy: "alert", unit: "platform_conversions", reason: reason,
		evidence: map[string]any{"impressions_today": 0, "hour": local.Hour(), "serving_days": served, "usual_impressions": total / int64(served), "window": "today so far"}}, true
}

// zeroConversions fires for clicks that brought no sign-up (first-party
// when the ledger exists, else no platform conversion) over 30 days.
func (s *Syncer) zeroConversions(ctx context.Context, c campaignRow, d map[string]dayRow, today string, firstParty bool) (finding, bool, error) {
	var clicks int64
	var conv float64
	end, _ := time.Parse(time.DateOnly, today)
	for i := 0; i < zeroConvDays; i++ {
		row := d[end.AddDate(0, 0, -i).Format(time.DateOnly)]
		clicks += row.clicks
		conv += row.conv
	}
	need, brand := int64(zeroConvClicks), false
	if n := strings.ToLower(strings.TrimSpace(c.brandName)); len(n) >= 3 && strings.Contains(strings.ToLower(c.name), n) {
		need, brand = zeroConvBrandClicks, true
	}
	if clicks < need {
		return finding{}, false, nil
	}
	unit := "platform_conversions"
	var signups int64
	if firstParty {
		unit = "signups"
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM ad_acquisitions WHERE campaign_entity_id = $1 AND clicked_at >= now() - make_interval(days => $2)`, c.id, zeroConvDays).Scan(&signups); err != nil {
			return finding{}, false, err
		}
	} else if conv > 0 {
		return finding{}, false, nil
	}
	if signups > 0 {
		return finding{}, false, nil
	}
	what := "no platform conversion"
	if firstParty {
		what = "no sign-up"
	}
	reason := fmt.Sprintf("%s got %d clicks in %d days with %s.", c.name, clicks, zeroConvDays, what)
	return finding{c: c, kind: AlertZeroConversions, autonomy: "alert", unit: unit, reason: reason,
		evidence: map[string]any{"clicks": clicks, "signups": signups, "platform_conversions": conv, "days": zeroConvDays, "brand_campaign": brand, "clicks_needed": need, "window": fmt.Sprintf("last %d days", zeroConvDays)}}, true, nil
}

// pacing checks an account's month against its daily budgets.
func pacing(list []campaignRow, days map[string]map[string]dayRow, now time.Time) (finding, bool) {
	if len(list) == 0 {
		return finding{}, false
	}
	local := now.In(location(list[0].tz))
	day := local.Day()
	if day <= pacingAfterDay {
		return finding{}, false
	}
	first := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.UTC)
	inMonth := time.Date(local.Year(), local.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	var spent, budgets int64
	var top campaignRow
	for _, c := range list {
		for i := 0; i < day; i++ {
			spent += days[c.id][first.AddDate(0, 0, i).Format(time.DateOnly)].spend
		}
		if c.status == connectors.AdActive {
			budgets += c.budget
			if c.budget > top.budget {
				top = c
			}
		}
	}
	if budgets <= 0 || top.id == "" {
		return finding{}, false
	}
	expected := float64(budgets) * monthMultiple * float64(day) / float64(inMonth)
	ratio := float64(spent) / expected
	if ratio >= pacingLow && ratio <= pacingHigh {
		return finding{}, false
	}
	word := "under"
	if ratio > 1 {
		word = "over"
	}
	reason := fmt.Sprintf("%s has spent %s this month by day %d, %d%% %s the %s its daily budgets add up to.",
		top.acctName, money(spent, top.currency), day, int(math.Round(math.Abs(ratio-1)*100)), word, money(int64(expected), top.currency))
	return finding{c: top, kind: AlertPacing, autonomy: "alert", unit: "platform_conversions", reason: reason, account: top.acctID,
		evidence: map[string]any{"account_id": top.acctID, "account_name": top.acctName, "scope": "account", "spend_micros": spent, "expected_micros": int64(expected),
			"ratio": math.Round(ratio*100) / 100, "day_of_month": day, "days_in_month": inMonth, "daily_budgets_micros": budgets, "window": "month to date", "currency": top.currency}}, true
}

// disapproved lists campaigns with live ads the platform disapproved.
func (s *Syncer) disapproved(ctx context.Context, ws string, camps []campaignRow) ([]finding, error) {
	by := map[string]campaignRow{}
	for _, c := range camps {
		by[c.id] = c
	}
	rows, err := s.Pool.Query(ctx, `SELECT c.id, count(*)::int, (array_agg(e.name ORDER BY e.name))[1:5]
		FROM ad_entities e JOIN ad_entities g ON g.id = e.parent_id JOIN ad_entities c ON c.id = g.parent_id
		WHERE e.workspace_id = $1 AND e.level = 'ad' AND e.status = 'active' AND e.details->>'`+connectors.AdDetailPolicy+`' = 'disapproved'
		GROUP BY c.id`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finding
	for rows.Next() {
		var id string
		var n int
		var names []string
		if err := rows.Scan(&id, &n, &names); err != nil {
			return nil, err
		}
		c, ok := by[id]
		if !ok {
			continue
		}
		reason := fmt.Sprintf("%s has %s the platform disapproved: %s.", c.name, countOf(n, "live ad", "live ads"), strings.Join(names, ", "))
		out = append(out, finding{c: c, kind: AlertDisapproved, autonomy: "alert", unit: "platform_conversions", reason: reason,
			evidence: map[string]any{"ads": n, "ad_names": names, "window": "now"}})
	}
	return out, rows.Err()
}

// sendsFailing checks each account's conversions sent in 24 hours.
func (s *Syncer) sendsFailing(ctx context.Context, ws string, byAccount map[string][]campaignRow, days map[string]map[string]dayRow) ([]finding, error) {
	rows, err := s.Pool.Query(ctx, `SELECT r.ad_account_id, count(*) FILTER (WHERE q.status = 'failed')::int, count(*)::int,
			COALESCE((SELECT q2.reason FROM ad_conversion_sends q2 WHERE q2.rule_id = r.id AND q2.status = 'failed' AND q2.updated_at >= now() - interval '24 hours'
				GROUP BY q2.reason ORDER BY count(*) DESC LIMIT 1), '')
		FROM ad_conversion_sends q JOIN ad_conversion_rules r ON r.id = q.rule_id
		WHERE q.workspace_id = $1 AND q.updated_at >= now() - interval '24 hours' AND q.status IN ('sent', 'failed')
		GROUP BY r.ad_account_id, r.id`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type sums struct {
		failed, total int
		reason        string
	}
	acct := map[string]*sums{}
	for rows.Next() {
		var id, reason string
		var failed, total int
		if err := rows.Scan(&id, &failed, &total, &reason); err != nil {
			return nil, err
		}
		x := acct[id]
		if x == nil {
			x = &sums{}
			acct[id] = x
		}
		x.failed += failed
		x.total += total
		if reason != "" && x.reason == "" {
			x.reason = reason
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []finding
	for id, x := range acct {
		if x.total < sendsMin || float64(x.failed)/float64(x.total) < sendsFailingShare {
			continue
		}
		top, ok := topCampaign(byAccount[id], days, 7)
		if !ok {
			continue
		}
		reason := fmt.Sprintf("%s refused %d of the %d conversions sent to it in the last 24 hours", top.acctName, x.failed, x.total)
		if x.reason != "" {
			reason += " (" + trim(x.reason, 120) + ")"
		}
		out = append(out, finding{c: top, kind: AlertSendsFailing, autonomy: "alert", unit: "platform_conversions", reason: reason + ".", account: id,
			evidence: map[string]any{"account_id": id, "account_name": top.acctName, "scope": "account", "failed": x.failed, "sent": x.total, "share": math.Round(float64(x.failed)/float64(x.total)*100) / 100, "window": "last 24 hours"}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].account < out[j].account })
	return out, nil
}

// trackingBroken checks each brand's paid visits in 24 hours: whether they
// carried click ids, and whether they still bring sign-ups.
func (s *Syncer) trackingBroken(ctx context.Context, ws string, camps []campaignRow, days map[string]map[string]dayRow, firstParty bool) ([]finding, error) {
	byBrand := map[string][]campaignRow{}
	var order []string
	for _, c := range camps {
		k := ""
		if c.brand != nil {
			k = *c.brand
		}
		if _, ok := byBrand[k]; !ok {
			order = append(order, k)
		}
		byBrand[k] = append(byBrand[k], c)
	}
	var out []finding
	for _, k := range order {
		top, ok := topCampaign(byBrand[k], days, 7)
		if !ok {
			continue
		}
		var brand *string
		if k != "" {
			brand = &k
		}
		var visits, withID, withCampaign int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE click_ids ?| $3::text[])::int, count(*) FILTER (WHERE ad->>'campaign' <> '')::int
			FROM site_sessions WHERE workspace_id = $1 AND started_at >= now() - interval '24 hours' AND channel = 'paid_ads'
			  AND ($2::uuid IS NULL OR brand_id = $2 OR brand_id IS NULL)`, ws, brand, adtrack.Params()).Scan(&visits, &withID, &withCampaign); err != nil {
			return nil, err
		}
		if visits < trackingMinVisits {
			continue
		}
		rate := float64(withID) / float64(visits)
		if rate < trackingCapture {
			reason := fmt.Sprintf("Only %d of %d paid visits in the last 24 hours carried a click id, so most paid clicks can't be tied to their ads. Check the tracking template and the site script.", withID, visits)
			out = append(out, finding{c: top, kind: AlertTrackingBroken, autonomy: "alert", unit: "signups", reason: reason, account: "brand:" + k,
				evidence: map[string]any{"account_id": "brand:" + k, "scope": "brand", "brand_id": brand, "paid_visits": visits, "with_click_id": withID, "capture_rate": math.Round(rate*100) / 100, "check": "capture", "window": "last 24 hours"}})
			continue
		}
		if withCampaign < trackingMinVisits {
			continue
		}
		today, median, err := s.signups(ctx, ws, brand, firstParty)
		if err != nil {
			return nil, err
		}
		if today == 0 && median >= trackingMedianSignups {
			reason := fmt.Sprintf("%d paid visits with campaign ids in the last 24 hours brought no sign-up, though the last week's days brought about %d each. Check that sign-ups still reach Outflare.", withCampaign, median)
			out = append(out, finding{c: top, kind: AlertTrackingBroken, autonomy: "alert", unit: "signups", reason: reason, account: "brand:" + k,
				evidence: map[string]any{"account_id": "brand:" + k, "scope": "brand", "brand_id": brand, "paid_visits": visits, "with_campaign": withCampaign, "signups": 0, "median_daily_signups": median, "check": "signups", "window": "last 24 hours"}})
		}
	}
	return out, nil
}

// signups counts a brand's sign-ups in the last 24 hours and the median
// of the 7 days before: from the ledger when it exists, else from the
// signed_up events.
func (s *Syncer) signups(ctx context.Context, ws string, brand *string, firstParty bool) (today int, median int, err error) {
	q := `SELECT date_trunc('day', occurred_at AT TIME ZONE 'UTC'), count(*)::int FROM customer_events
		WHERE workspace_id = $1 AND kind = 'track' AND name = 'signed_up' AND occurred_at >= now() - interval '9 days'
		  AND ($2::uuid IS NULL OR brand_id = $2 OR brand_id IS NULL) GROUP BY 1`
	if firstParty {
		q = `SELECT date_trunc('day', signed_up_at AT TIME ZONE 'UTC'), count(*)::int FROM ad_acquisitions
		WHERE workspace_id = $1 AND signed_up_at >= now() - interval '9 days' AND ($2::uuid IS NULL OR brand_id = $2) GROUP BY 1`
	}
	rows, err := s.Pool.Query(ctx, q, ws, brand)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	daily := map[string]int{}
	for rows.Next() {
		var day time.Time
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return 0, 0, err
		}
		daily[day.Format(time.DateOnly)] += n
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	// Today and yesterday stand in for the last 24 hours; the median is
	// over the 7 whole days before them, counting days with none.
	now := s.now().UTC()
	today = daily[now.Format(time.DateOnly)] + daily[now.AddDate(0, 0, -1).Format(time.DateOnly)]
	vals := make([]int, 0, 7)
	for i := 2; i <= 8; i++ {
		vals = append(vals, daily[now.AddDate(0, 0, -i).Format(time.DateOnly)])
	}
	sort.Ints(vals)
	return today, vals[len(vals)/2], nil
}

// autoApplyWatched says whether a platform recommendation type changes
// bidding, keywords or match types: the ones a person should know Google
// applies on its own.
func autoApplyWatched(t string) bool {
	t = strings.ToUpper(t)
	for _, w := range []string{"TARGET_CPA", "TARGET_ROAS", "MAXIMIZE_", "ENHANCED_CPC", "SET_TARGET", "KEYWORD", "MATCH_TYPE", "BROAD_MATCH", "BID"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// autoApplyOn asks each platform that can say which of its own
// recommendations an account applies by itself. A platform that can't be
// read now is skipped, not failed: the run's other alerts still land.
func (s *Syncer) autoApplyOn(ctx context.Context, ws string, byAccount map[string][]campaignRow, days map[string]map[string]dayRow) []finding {
	byIntegration := map[string][]string{}
	for id, list := range byAccount {
		byIntegration[list[0].integrationID] = append(byIntegration[list[0].integrationID], id)
	}
	var out []finding
	for integrationID, accts := range byIntegration {
		o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
		if err != nil {
			if !errors.Is(err, connectors.ErrNotFound) {
				track.Fail(ctx, s.Log, track.Event{Name: "ad_alerts.read_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Failure,
					Props: map[string]any{"check": AlertAutoApplyOn}}, "connector_"+connectors.KindOf(err), err)
			}
			continue
		}
		r, ok := o.Connector.(connectors.AdAutoApplyReader)
		if !ok {
			continue
		}
		sort.Strings(accts)
		for _, id := range accts {
			top, ok := topCampaign(byAccount[id], days, 30)
			if !ok {
				continue
			}
			types, err := r.AutoApplied(ctx, o.Client, o.Creds, top.ad())
			if err != nil {
				track.Fail(ctx, s.Log, track.Event{Name: "ad_alerts.read_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Failure,
					Props: map[string]any{"check": AlertAutoApplyOn, "connector": top.connector}}, "connector_"+connectors.KindOf(err), err)
				continue
			}
			var watched []string
			for _, t := range types {
				if autoApplyWatched(t) {
					watched = append(watched, strings.ToLower(t))
				}
			}
			if len(watched) == 0 {
				continue
			}
			sort.Strings(watched)
			reason := fmt.Sprintf("%s lets the platform apply its own recommendations to bidding, keywords or match types (%s). Outflare's rules and the platform's would both be changing the account; turn auto-apply off there.",
				top.acctName, strings.Join(watched, ", "))
			out = append(out, finding{c: top, kind: AlertAutoApplyOn, autonomy: "alert", unit: "platform_conversions", reason: reason, account: id,
				evidence: map[string]any{"account_id": id, "account_name": top.acctName, "scope": "account", "types": watched, "window": "now"}})
		}
	}
	return out
}

// overspend pauses a campaign that spent over twice its budget today
// where the extension's hook allows it (paidads.AutoApprove), writing the
// change like a person's (actor system) and the finding as applied;
// otherwise it returns the alert to write. A campaign on an account every
// brand shares is never paused on its own.
func (s *Syncer) overspend(ctx context.Context, ws string, c campaignRow, d map[string]dayRow, now time.Time) (finding, bool, error) {
	today := now.In(location(c.tz)).Format(time.DateOnly)
	spend := d[today].spend
	evidence := map[string]any{"spend_micros": spend, "daily_budget_micros": c.budget, "multiple": math.Round(float64(spend)/float64(c.budget)*100) / 100, "window": "today so far", "currency": c.currency}
	f := finding{c: c, kind: OverspendPause, autonomy: "alert", unit: "platform_conversions", evidence: evidence, proposed: map[string]any{"status": connectors.AdPaused}}
	auto := c.brand != nil && paidads.AutoApprove(ctx, ws, *c.brand, OverspendPause)
	shadow := false
	if auto {
		var err error
		if shadow, err = s.shadowMode(ctx, *c.brand); err != nil {
			return f, false, err
		}
	}
	base := fmt.Sprintf("%s has spent %s today, over twice its daily budget of %s.", c.name, money(spend, c.currency), money(c.budget, c.currency))
	switch {
	case auto && shadow:
		f.shadow = true
		f.reason = base + " Outflare would have paused it; rules are in shadow mode for this brand."
		return f, false, nil
	case !auto:
		f.reason = base + " Pause it if the budget is right; Outflare changes nothing on the account without your approval."
		return f, false, nil
	}
	// A pause already asked for today is enough.
	var queued bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = 'pause' AND status IN ('queued', 'applied') AND created_at >= now() - interval '24 hours')`, c.id).Scan(&queued); err != nil {
		return f, false, err
	}
	if queued {
		return f, true, nil
	}
	paused := false
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		budget := c.budget
		before, _ := json.Marshal(ChangeState{Status: c.status, DailyBudgetMicros: &budget})
		after, _ := json.Marshal(ChangeState{Status: connectors.AdPaused})
		var changeID string
		if err := tx.QueryRow(ctx, `INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after) VALUES ($1, $2, $3, 'pause', $4, $5) RETURNING id`,
			ws, c.brand, c.id, before, after).Scan(&changeID); err != nil {
			return err
		}
		if err := EnqueueChange(ctx, tx, ws, changeID); err != nil {
			return err
		}
		data := map[string]any{"change_id": changeID, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand,
			"before": ChangeState{Status: c.status, DailyBudgetMicros: &budget}, "after": ChangeState{Status: connectors.AdPaused}, "rule": OverspendPause}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_campaign.paused", TargetType: "ad_campaign", TargetID: c.id, Data: data,
			Props: map[string]any{"connector": c.connector, "kind": ChangePause, "auto": true, "rule": OverspendPause}}); err != nil {
			return err
		}
		ev, _ := json.Marshal(evidence)
		pr, _ := json.Marshal(f.proposed)
		reason := base + " Outflare paused it; revert the finding to resume it."
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, applied_auto, rule, rule_version, reason, evidence, proposed,
				change_id, decided_at, measure_from, window_note)
			VALUES ($1, $2, $3, $4, 'measuring', 'auto', 'platform_conversions', true, $4, $5, $6, $7, $8, $9, now(), current_date, 'A protective pause: revert it when the day is over and the budget is right.')
			RETURNING id`, ws, c.brand, c.id, OverspendPause, alertRuleVersion, reason, ev, pr, changeID).Scan(&id); err != nil {
			return err
		}
		metrics.AdAlertRaised(OverspendPause)
		paused = true
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_alert.raised", TargetType: "ad_suggestion", TargetID: id,
			Data:  map[string]any{"suggestion_id": id, "campaign_id": c.id, "connector": c.connector, "kind": OverspendPause, "brand_id": c.brand, "change_id": changeID, "applied": true},
			Props: map[string]any{"connector": c.connector, "kind": OverspendPause, "autonomy": "auto", "applied": true}})
	})
	return f, paused, err
}

// shadowMode reads whether a brand's rules are in shadow mode (recording
// what they would do), from ad_brand_economics. A brand that hasn't
// saved its economics is in shadow mode, as paidads.Defaults says, and so
// is every brand on a schema without that table.
func (s *Syncer) shadowMode(ctx context.Context, brand string) (bool, error) {
	ok, err := tableExists(ctx, s.Pool, "ad_brand_economics")
	if err != nil || !ok {
		return true, err
	}
	var shadow bool
	err = s.Pool.QueryRow(ctx, `SELECT shadow_mode FROM ad_brand_economics WHERE brand_id = $1`, brand).Scan(&shadow)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return shadow, err
}

// writeFindings upserts the run's findings: a kind already open on the
// campaign (or the account) is updated, a new one inserted with its
// audit event. It returns the ids to keep open.
func (s *Syncer) writeFindings(ctx context.Context, ws string, findings []finding, res *AlertResult) ([]string, error) {
	var keep []string
	for _, f := range findings {
		id, inserted, err := s.writeFinding(ctx, ws, f)
		if err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		keep = append(keep, id)
		if inserted {
			res.Raised[f.kind]++
		} else {
			res.Held++
		}
	}
	return keep, nil
}

func (s *Syncer) writeFinding(ctx context.Context, ws string, f finding) (id string, inserted bool, err error) {
	ev, _ := json.Marshal(f.evidence)
	pr := []byte(`{}`)
	if f.proposed != nil {
		pr, _ = json.Marshal(f.proposed)
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// An alert a person acknowledged within a day isn't raised again
		// while it holds.
		var snoozed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ad_suggestions WHERE workspace_id = $1 AND kind = $2 AND status = 'acknowledged'
			AND updated_at >= now() - interval '24 hours' AND (ad_entity_id = $3 OR ($4 <> '' AND evidence->>'account_id' = $4)))`, ws, f.kind, f.c.id, f.account).Scan(&snoozed); err != nil {
			return err
		}
		if snoozed {
			return nil
		}
		if f.account != "" {
			// Account-wide: the open row may sit on another campaign of
			// the account from an earlier run.
			err := tx.QueryRow(ctx, `UPDATE ad_suggestions SET reason = $4, evidence = $5, shadow = $6, updated_at = now()
				WHERE workspace_id = $1 AND kind = $2 AND status = 'open' AND evidence->>'account_id' = $3 RETURNING id`, ws, f.kind, f.account, f.reason, ev, f.shadow).Scan(&id)
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, shadow, rule, rule_version, reason, evidence, proposed)
			VALUES ($1, $2, $3, $4, 'open', $5, $6, $7, $4, $8, $9, $10, $11)
			ON CONFLICT (ad_entity_id, kind) WHERE status = 'open'
			DO UPDATE SET reason = EXCLUDED.reason, evidence = EXCLUDED.evidence, shadow = EXCLUDED.shadow, updated_at = now()
			RETURNING id, xmax = 0`, ws, f.c.brand, f.c.id, f.kind, f.autonomy, f.unit, f.shadow, alertRuleVersion, f.reason, ev, pr).Scan(&id, &inserted); err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		metrics.AdAlertRaised(f.kind)
		data := map[string]any{"suggestion_id": id, "campaign_id": f.c.id, "connector": f.c.connector, "kind": f.kind, "brand_id": f.c.brand, "shadow": f.shadow}
		if f.account != "" {
			data["account_id"] = f.account
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_alert.raised", TargetType: "ad_suggestion", TargetID: id, Data: data,
			Props: map[string]any{"connector": f.c.connector, "kind": f.kind, "autonomy": f.autonomy, "shadow": f.shadow, "applied": false}})
	})
	return id, inserted, err
}

// expireAlerts closes the open alerts this run didn't raise or hold.
func (s *Syncer) expireAlerts(ctx context.Context, ws string, keep []string, res *AlertResult) error {
	if keep == nil {
		keep = []string{}
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE ad_suggestions SET status = 'expired', updated_at = now()
		WHERE workspace_id = $1 AND status = 'open' AND rule = ANY($2) AND NOT (id = ANY($3::uuid[]))`, ws, AlertKinds, keep)
	if err != nil {
		return err
	}
	res.Expired = int(tag.RowsAffected())
	return nil
}

// topCampaign is the campaign of a list with the most spend over the last
// days: where an account-wide finding is pinned.
func topCampaign(list []campaignRow, days map[string]map[string]dayRow, n int) (campaignRow, bool) {
	var top campaignRow
	var best int64 = -1
	end := time.Now().UTC()
	for _, c := range list {
		var spend int64
		for i := 0; i < n; i++ {
			spend += days[c.id][end.AddDate(0, 0, -i).Format(time.DateOnly)].spend
		}
		if spend > best || (spend == best && c.status == connectors.AdActive && top.status != connectors.AdActive) {
			top, best = c, spend
		}
	}
	return top, best >= 0
}

// tableExists says whether a table another workstream adds is there yet.
func tableExists(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, name string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&ok)
	return ok, err
}

// money is an amount in micros as a person reads it: 12.50 USD.
func money(micros int64, currency string) string {
	return fmt.Sprintf("%.2f %s", float64(micros)/1e6, currency)
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
