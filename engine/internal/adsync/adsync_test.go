package adsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/paidads"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// fakeAds is an ad platform with one account and the campaigns a test
// gives it, which records the windows and days it was asked for.
type fakeAds struct {
	mu        sync.Mutex
	campaigns []connectors.AdCampaign
	// windows are the from..to ranges AdDays was asked for.
	windows [][2]string
	// clickDays are the days AdClicks was asked for; clicks answers each
	// with the same clicks; failClicksOn makes one day fail with a quota
	// error.
	clickDays    []string
	clicks       []connectors.AdClick
	failClicksOn string
	// events answers AdChangeEvents; sinces records what it was asked.
	events []connectors.AdChangeEvent
	sinces []time.Time
	// autoApplied answers AutoApplied.
	autoApplied []string
	tz          string
}

func (f *fakeAds) Definition() connectors.Definition {
	return connectors.Definition{ID: "fake_ads", Name: "Fake Ads", Category: connectors.CategoryAds, Auth: connectors.AuthAPIKey, Status: connectors.Available,
		Fields: []connectors.Field{{Name: "api_key", Label: "API key", Secret: true}}, Capabilities: []string{connectors.CapAdsRead}}
}

func (f *fakeAds) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{Name: "Fake"}, nil
}

func (f *fakeAds) AdAccounts(context.Context, *connectors.Client, connectors.Creds) ([]connectors.AdAccount, error) {
	return []connectors.AdAccount{{ID: "acct-1", Name: "Acme ads", Currency: "USD", TimeZone: f.tz}}, nil
}

func (f *fakeAds) AdCampaigns(context.Context, *connectors.Client, connectors.Creds, connectors.AdAccount) ([]connectors.AdCampaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]connectors.AdCampaign(nil), f.campaigns...), nil
}

func (f *fakeAds) AdDays(_ context.Context, _ *connectors.Client, _ connectors.Creds, _ connectors.AdAccount, from, to string) ([]connectors.AdDay, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = append(f.windows, [2]string{from, to})
	var out []connectors.AdDay
	for _, c := range f.campaigns {
		out = append(out, connectors.AdDay{CampaignID: c.ID, Date: to, SpendMicros: 1_000_000, Impressions: 10, Clicks: 1})
	}
	return out, nil
}

func (f *fakeAds) AdClicks(_ context.Context, _ *connectors.Client, _ connectors.Creds, _ connectors.AdAccount, day string) ([]connectors.AdClick, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if day == f.failClicksOn {
		return nil, connectors.Errf(connectors.ErrQuota, "too many requests today")
	}
	f.clickDays = append(f.clickDays, day)
	out := make([]connectors.AdClick, len(f.clicks))
	for i, k := range f.clicks {
		k.ClickID = k.ClickID + "-" + day
		out[i] = k
	}
	return out, nil
}

func (f *fakeAds) AdChangeEvents(_ context.Context, _ *connectors.Client, _ connectors.Creds, _ connectors.AdAccount, since time.Time) ([]connectors.AdChangeEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinces = append(f.sinces, since)
	var out []connectors.AdChangeEvent
	for _, e := range f.events {
		if !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeAds) AutoApplied(context.Context, *connectors.Client, connectors.Creds, connectors.AdAccount) ([]string, error) {
	return f.autoApplied, nil
}

type env struct {
	pool *pgxpool.Pool
	s    *Syncer
	f    *fakeAds
	ws   string
	// integration is the connection; acct the ad account's id once synced.
	integration string
	now         time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE workspaces, jobs, audit_events CASCADE`); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAds{tz: "UTC", campaigns: []connectors.AdCampaign{{ID: "c1", Name: "Brand search", Status: connectors.AdActive, DailyBudgetMicros: ptr(int64(50_000_000))}}}
	store := &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry([]connectors.Connector{f}, nil), Client: &connectors.Client{}}
	e := &env{pool: pool, f: f, now: time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)}
	e.s = &Syncer{Pool: pool, Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now }}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme') RETURNING id::text`).Scan(&e.ws); err != nil {
			return err
		}
		i, err := store.Insert(ctx, tx, e.ws, "fake_ads", connectors.AuthAPIKey, "Fake Ads", connectors.Creds{"api_key": "k"}, connectors.Account{Email: "ana@acme.example"}, "")
		e.integration = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// entity is a campaign's id in ad_entities by its external id.
func (e *env) entity(t *testing.T, external string) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM ad_entities WHERE workspace_id = $1 AND level = 'campaign' AND external_id = $2`, e.ws, external).Scan(&id); err != nil {
		t.Fatalf("campaign %s: %v", external, err)
	}
	return id
}

func TestPickWindow(t *testing.T) {
	now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for name, tc := range map[string]struct {
		w    watermarks
		want string
	}{
		"never read":           {watermarks{}, WindowMonthly},
		"all fresh":            {watermarks{daily: ago(time.Hour), weekly: ago(time.Hour), monthly: ago(time.Hour)}, WindowHourly},
		"a day on":             {watermarks{daily: ago(25 * time.Hour), weekly: ago(time.Hour), monthly: ago(time.Hour)}, WindowDaily},
		"a week on":            {watermarks{daily: ago(25 * time.Hour), weekly: ago(8 * 24 * time.Hour), monthly: ago(time.Hour)}, WindowWeekly},
		"a month on":           {watermarks{daily: ago(time.Hour), weekly: ago(time.Hour), monthly: ago(31 * 24 * time.Hour)}, WindowMonthly},
		"weekly never, others": {watermarks{daily: ago(time.Hour), monthly: ago(time.Hour)}, WindowWeekly},
	} {
		if got := tc.w.pick(now); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		window string
		from   string
	}{{WindowHourly, "2026-11-08"}, {WindowDaily, "2026-11-03"}, {WindowWeekly, "2026-10-06"}, {WindowMonthly, "2026-08-07"}, {WindowFirst, "2026-08-12"}, {"", "2026-11-08"}} {
		from, to := Window(now, "UTC", tc.window)
		if from != tc.from || to != "2026-11-09" {
			t.Errorf("%s: %s..%s", tc.window, from, to)
		}
	}
	// In the account's time zone it is already the 10th.
	if from, to := Window(now, "Pacific/Kiritimati", WindowHourly); from != "2026-11-09" || to != "2026-11-10" {
		t.Errorf("Kiritimati: %s..%s", from, to)
	}
}

// The first sync reads 90 days and marks every window read; an hour later
// only two days are read; a day later seven; and the watermarks move.
func TestSyncWindowsAndWatermarks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	res, err := e.s.SyncOnce(ctx, e.ws, e.integration)
	if err != nil || res.Window != WindowMonthly || res.Campaigns != 1 {
		t.Fatalf("first: %+v %v", res, err)
	}
	if w := e.f.windows[0]; w != [2]string{"2026-08-12", "2026-11-09"} {
		t.Fatalf("first window %v", w)
	}
	var daily, weekly, monthly *time.Time
	if err := e.pool.QueryRow(ctx, `SELECT last_full_at, last_week_at, last_month_at FROM ad_syncs WHERE integration_id = $1`, e.integration).Scan(&daily, &weekly, &monthly); err != nil {
		t.Fatal(err)
	}
	if daily == nil || weekly == nil || monthly == nil || !daily.Equal(e.now) {
		t.Fatalf("watermarks after the first run: %v %v %v", daily, weekly, monthly)
	}
	// Queued for the next hour: a suggestions run and an alert check.
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE kind = $1 AND idempotency_key = $2`, JobAlerts, "ads.alerts:"+e.ws+":2026-11-09T15"); n != 1 {
		t.Fatalf("alert job queued %d times", n)
	}

	e.now = e.now.Add(time.Hour)
	res, err = e.s.SyncOnce(ctx, e.ws, e.integration)
	if err != nil || res.Window != WindowHourly {
		t.Fatalf("hourly: %+v %v", res, err)
	}
	if w := e.f.windows[1]; w != [2]string{"2026-11-08", "2026-11-09"} {
		t.Fatalf("hourly window %v", w)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE kind = $1`, JobAlerts); n != 2 {
		t.Fatalf("a new hour, a new alert job: %d", n)
	}
	e.now = e.now.Add(24 * time.Hour)
	if res, err = e.s.SyncOnce(ctx, e.ws, e.integration); err != nil || res.Window != WindowDaily {
		t.Fatalf("daily: %+v %v", res, err)
	}
	if w := e.f.windows[2]; w != [2]string{"2026-11-04", "2026-11-10"} {
		t.Fatalf("daily window %v", w)
	}
	e.now = e.now.Add(6 * 24 * time.Hour)
	if res, err = e.s.SyncOnce(ctx, e.ws, e.integration); err != nil || res.Window != WindowWeekly {
		t.Fatalf("weekly: %+v %v", res, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT last_full_at, last_week_at, last_month_at FROM ad_syncs WHERE integration_id = $1`, e.integration).Scan(&daily, &weekly, &monthly); err != nil {
		t.Fatal(err)
	}
	if !daily.Equal(e.now) || !weekly.Equal(e.now) || monthly.Equal(e.now) {
		t.Fatalf("watermarks after the weekly run: %v %v %v (now %v)", daily, weekly, monthly, e.now)
	}
}

// The first sync harvests 90 days of clicks, one query per day; later runs
// only the days since; a refused day stops where it is and the next run
// carries on from there.
func TestClickHarvest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.clicks = []connectors.AdClick{{ClickID: "g", Kind: "gclid", CampaignID: "c1", AdGroupID: "71", AdID: "81", Keyword: "acme crm", MatchType: "exact", Device: "mobile"}}
	e.f.failClicksOn = "2026-10-01"
	res, err := e.s.SyncOnce(ctx, e.ws, e.integration)
	if err != nil {
		t.Fatalf("a refused click read doesn't fail the sync: %+v %v", res, err)
	}
	// 2026-08-11 is the first of the 90 days before yesterday; it stopped at
	// the failing day.
	if len(e.f.clickDays) != 51 || e.f.clickDays[0] != "2026-08-11" || e.f.clickDays[50] != "2026-09-30" || res.Clicks != 51 {
		t.Fatalf("days read: %d %v..%v, clicks %d", len(e.f.clickDays), e.f.clickDays[0], e.f.clickDays[len(e.f.clickDays)-1], res.Clicks)
	}
	var mark *time.Time
	if err := e.pool.QueryRow(ctx, `SELECT last_clicks_day FROM ad_syncs WHERE integration_id = $1`, e.integration).Scan(&mark); err != nil || mark == nil || mark.Format(time.DateOnly) != "2026-09-30" {
		t.Fatalf("watermark %v %v", mark, err)
	}
	if n := e.count(t, `SELECT count(*) FROM ad_clicks WHERE workspace_id = $1`, e.ws); n != 51 {
		t.Fatalf("%d clicks stored", n)
	}
	var kw, device, acct string
	if err := e.pool.QueryRow(ctx, `SELECT keyword, device, a.external_id FROM ad_clicks k JOIN ad_accounts a ON a.id = k.ad_account_id WHERE click_id = 'g-2026-09-30'`).Scan(&kw, &device, &acct); err != nil || kw != "acme crm" || device != "mobile" || acct != "acct-1" {
		t.Fatalf("a click: %s %s %s %v", kw, device, acct, err)
	}

	// The next run carries on from October and finishes yesterday.
	e.f.failClicksOn = ""
	e.f.clickDays = nil
	e.now = e.now.Add(time.Hour)
	if res, err = e.s.SyncOnce(ctx, e.ws, e.integration); err != nil || res.Clicks != 39 {
		t.Fatalf("resume: %+v %v", res, err)
	}
	if e.f.clickDays[0] != "2026-10-01" || e.f.clickDays[len(e.f.clickDays)-1] != "2026-11-08" {
		t.Fatalf("resumed days %v..%v", e.f.clickDays[0], e.f.clickDays[len(e.f.clickDays)-1])
	}
	// Same day again: nothing to read.
	e.f.clickDays = nil
	e.now = e.now.Add(time.Hour)
	if res, err = e.s.SyncOnce(ctx, e.ws, e.integration); err != nil || res.Clicks != 0 || len(e.f.clickDays) != 0 {
		t.Fatalf("same day: %+v %v %v", res, err, e.f.clickDays)
	}
	// Tomorrow: yesterday only.
	e.now = e.now.Add(24 * time.Hour)
	if res, err = e.s.SyncOnce(ctx, e.ws, e.integration); err != nil || res.Clicks != 1 || len(e.f.clickDays) != 1 || e.f.clickDays[0] != "2026-11-09" {
		t.Fatalf("next day: %+v %v %v", res, err, e.f.clickDays)
	}
}

// Change events are stored once each, with the user hashed, Google's own
// automation marked google and Outflare's own changes marked outflare;
// the watermark moves and later reads start just before it.
func TestChangeEvents(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.s.SyncOnce(ctx, e.ws, e.integration); err != nil {
		t.Fatal(err)
	}
	campaign := e.entity(t, "c1")
	at := e.now.Add(-2 * time.Hour)
	// A pause Outflare applied two hours ago.
	e.exec(t, `INSERT INTO ad_changes (workspace_id, ad_entity_id, kind, status, applied_at, created_at) VALUES ($1, $2, 'pause', 'applied', $3, $3)`, e.ws, campaign, at.Add(-time.Minute))
	ana := connectors.HashEmail("ana@acme.example")
	e.f.events = []connectors.AdChangeEvent{
		{ID: "ev-1", At: at.Add(-5 * time.Hour), UserHash: connectors.HashEmail("bo@acme.example"), ClientType: "GOOGLE_ADS_WEB_CLIENT", ResourceType: "CAMPAIGN_BUDGET", Operation: "UPDATE", ChangedFields: []string{"campaign_budget.amount_micros"}, CampaignID: "c1",
			Old: map[string]any{"campaignBudget": map[string]any{"amountMicros": "40000000"}}, New: map[string]any{"campaignBudget": map[string]any{"amountMicros": "50000000"}}},
		{ID: "ev-2", At: at, UserHash: ana, ClientType: "GOOGLE_ADS_API", ResourceType: "CAMPAIGN", Operation: "UPDATE", ChangedFields: []string{"campaign.status"}, CampaignID: "c1"},
		{ID: "ev-3", At: at.Add(time.Hour), UserHash: ana, ClientType: "GOOGLE_ADS_API", ResourceType: "AD_GROUP", Operation: "UPDATE", ChangedFields: []string{"ad_group.name"}, CampaignID: "c9"},
		{ID: "ev-4", At: at.Add(90 * time.Minute), ClientType: "GOOGLE_ADS_RECOMMENDATIONS_SUBSCRIPTION", ResourceType: "AD_GROUP_CRITERION", Operation: "CREATE", CampaignID: "c1"},
		{ID: "ev-5", At: at.Add(100 * time.Minute), UserHash: connectors.HashEmail("cy@acme.example"), ClientType: "GOOGLE_ADS_SCRIPTS", ResourceType: "AD_GROUP_AD", Operation: "REMOVE", CampaignID: "c1"},
	}
	n, err := e.s.ReadChangeEvents(ctx, e.ws, e.integration)
	if err != nil || n != 5 {
		t.Fatalf("read: %d %v", n, err)
	}
	if since := e.f.sinces[0]; !since.Equal(e.now.Add(-changeEventKeep)) {
		t.Fatalf("first read since %v", since)
	}
	rows, err := e.pool.Query(ctx, `SELECT external_id, actor, source, summary, operation FROM ad_change_events WHERE workspace_id = $1 ORDER BY changed_at`, e.ws)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ id, actor, source, summary, op string }
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.actor, &x.source, &x.summary, &x.op)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"ev-1", connectors.HashEmail("bo@acme.example"), "web", "updated campaign budget: amount_micros", "update"},
		{"ev-2", actorOutflare, "api", "updated campaign: status", "update"}, // an ad_changes row around then
		{"ev-3", actorOutflare, "api", "updated ad group: name", "update"},   // the connection's own sign-in
		{"ev-4", actorGoogle, "recommendation_auto_apply", "created ad group criterion", "create"},
		{"ev-5", connectors.HashEmail("cy@acme.example"), "script", "removed ad group ad", "remove"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if e.count(t, `SELECT count(*) FROM ad_change_events WHERE actor LIKE '%@%'`) != 0 {
		t.Fatal("an email address was stored")
	}
	var mark time.Time
	if err := e.pool.QueryRow(ctx, `SELECT last_change_event_at FROM ad_syncs WHERE integration_id = $1`, e.integration).Scan(&mark); err != nil || !mark.Equal(at.Add(100*time.Minute)) {
		t.Fatalf("watermark %v %v", mark, err)
	}
	// Read again: it starts just before the watermark and stores nothing twice.
	e.now = e.now.Add(15 * time.Minute)
	if n, err := e.s.ReadChangeEvents(ctx, e.ws, e.integration); err != nil || n != 0 {
		t.Fatalf("second read: %d %v", n, err)
	}
	if since := e.f.sinces[1]; !since.Equal(mark.Add(-changeEventOverlap)) {
		t.Fatalf("second read since %v, want %v", since, mark.Add(-changeEventOverlap))
	}
	// The scheduler queues one job per connection per quarter hour.
	if err := e.s.QueueChangeEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.s.QueueChangeEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE kind = $1`, JobChangeEvents); n != 1 {
		t.Fatalf("%d change event jobs", n)
	}
}

func TestAnomalyMaths(t *testing.T) {
	// Four Mondays at 100 a day; 12 hours in: 50 expected. 300 is far off,
	// 60 is within the band, and before 4 hours nothing fires.
	base := []int64{100_000_000, 100_000_000, 100_000_000, 100_000_000}
	if exp, sigma, fire, dir := Anomaly(300_000_000, base, 12); !fire || dir != "above" || exp != 50_000_000 || sigma != 0 {
		t.Fatalf("far above: %v %v %v %s", exp, sigma, fire, dir)
	}
	if _, _, fire, _ := Anomaly(60_000_000, base, 12); fire {
		t.Fatal("inside the band fired")
	}
	if _, _, fire, dir := Anomaly(10_000_000, base, 12); !fire || dir != "below" {
		t.Fatal("far below didn't fire")
	}
	if _, _, fire, _ := Anomaly(300_000_000, base, 3); fire {
		t.Fatal("fired before the day was 4 hours in")
	}
	if _, _, fire, _ := Anomaly(300_000_000, base[:2], 12); fire {
		t.Fatal("fired on two weeks of baseline")
	}
	// Noisy baseline: 3 sigma matters. 20, 80, 20, 80 a day scaled to
	// half: expected 25, sigma 15; 70 is 45 off, 3 sigma exactly, so no.
	noisy := []int64{20_000_000, 80_000_000, 20_000_000, 80_000_000}
	if _, sigma, fire, _ := Anomaly(70_000_000, noisy, 12); fire || sigma != 15_000_000 {
		t.Fatalf("within three sigma fired (sigma %v)", sigma)
	}
	if _, _, fire, _ := Anomaly(71_000_000, noisy, 12); !fire {
		t.Fatal("beyond three sigma didn't fire")
	}
	// Small money: 1.00 vs 0.10 expected is off by 0.90, under 20 units.
	if _, _, fire, _ := Anomaly(1_000_000, []int64{200_000, 200_000, 200_000, 200_000}, 12); fire {
		t.Fatal("fired under the 20 unit floor")
	}
}

// Each alert rule on fixture days, then expiry and the protective pause.
func TestAlerts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	budget := func(n int64) *int64 { v := n * 1_000_000; return &v }
	e.f.campaigns = []connectors.AdCampaign{
		{ID: "anomaly", Name: "Spiky", Status: connectors.AdActive, DailyBudgetMicros: budget(500)},
		{ID: "flat", Name: "Quiet today", Status: connectors.AdActive, DailyBudgetMicros: budget(100)},
		{ID: "noconv", Name: "Acme brand", Status: connectors.AdActive, DailyBudgetMicros: budget(100)},
		{ID: "over", Name: "Runaway", Status: connectors.AdActive, DailyBudgetMicros: budget(10)},
		{ID: "paused", Name: "Resting", Status: connectors.AdPaused, DailyBudgetMicros: budget(1000)},
	}
	e.f.autoApplied = []string{"TARGET_CPA_OPT_IN", "RESPONSIVE_SEARCH_AD"}
	if _, err := e.s.SyncOnce(ctx, e.ws, e.integration); err != nil {
		t.Fatal(err)
	}
	// The account belongs to the Acme brand.
	var brand string
	if err := e.pool.QueryRow(ctx, `INSERT INTO brands (workspace_id, name, handle) VALUES ($1, 'Acme', 'acme') RETURNING id`, e.ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE ad_accounts SET brand_id = $2 WHERE workspace_id = $1`, e.ws, brand)
	e.exec(t, `DELETE FROM ad_metrics_daily WHERE workspace_id = $1`, e.ws)
	// day writes a campaign's numbers for one day, d days ago.
	day := func(id string, d int, spend int64, impressions, clicks int64, conv float64) {
		t.Helper()
		e.exec(t, `INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks, conversions)
			VALUES ($1, $2::date - $3::int, $4, 'USD', $5, $6, $7, $8)
			ON CONFLICT (ad_entity_id, day) DO UPDATE SET spend_micros = EXCLUDED.spend_micros, impressions = EXCLUDED.impressions, clicks = EXCLUDED.clicks, conversions = EXCLUDED.conversions`,
			id, e.now.Format(time.DateOnly), d, e.ws, spend*1_000_000, impressions, clicks, conv)
	}
	anomaly, flat, noconv, over, paused := e.entity(t, "anomaly"), e.entity(t, "flat"), e.entity(t, "noconv"), e.entity(t, "over"), e.entity(t, "paused")
	// Spiky: 100 a day on the last four Mondays (today is Monday 9 Nov
	// 2026), 300 so far today at 15:00 (12 lagged hours in: 50 expected).
	// Under 300 clicks in all, so the zero-conversions rule leaves it be.
	for _, d := range []int{7, 14, 21, 28} {
		day(anomaly, d, 100, 1000, 30, 2)
	}
	day(anomaly, 0, 300, 3000, 150, 1)
	// Quiet today: served every day last week, nothing today.
	for d := 1; d <= 7; d++ {
		day(flat, d, 20, 500, 20, 1)
	}
	day(flat, 0, 0, 0, 0, 0)
	// Acme brand: a brand campaign, 120 clicks in 30 days, no conversion.
	for d := 0; d < 30; d++ {
		day(noconv, d, 5, 100, 4, 0)
	}
	// Runaway: 25 today on a budget of 10.
	day(over, 0, 25, 100, 10, 0)
	// The month to date (days 1-9 of November) so far: spiky's 300 today
	// and the rest add up to over 110% of 710 a day's worth.
	for d := 1; d <= 8; d++ {
		day(paused, d, 900, 100, 10, 0)
	}
	// A live ad under Spiky the platform disapproved.
	var group string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_entities (workspace_id, ad_account_id, level, parent_id, external_id, name, status)
		SELECT $1, ad_account_id, 'ad_group', id, 'g1', 'Exact', 'active' FROM ad_entities WHERE id = $2 RETURNING id`, e.ws, anomaly).Scan(&group); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO ad_entities (workspace_id, ad_account_id, level, parent_id, external_id, name, status, details)
		SELECT $1, ad_account_id, 'ad', $2, 'a1', 'Best CRM ever', 'active', '{"policy_status": "disapproved"}' FROM ad_entities WHERE id = $2`, e.ws, group)
	// Conversions sent back: 2 of 10 refused.
	var acct, rule string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_accounts WHERE workspace_id = $1`, e.ws).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_conversion_rules (workspace_id, ad_account_id, event_name, target_id) VALUES ($1, $2, 'signed_up', 't1') RETURNING id`, e.ws, acct).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		status := "sent"
		if i < 2 {
			status = "failed"
		}
		var ev string
		if err := e.pool.QueryRow(ctx, `INSERT INTO customer_events (workspace_id, kind, name, occurred_at, message_id) VALUES ($1, 'track', 'signed_up', now() - interval '1 hour', $2) RETURNING id`, e.ws, fmt.Sprintf("m-%d", i)).Scan(&ev); err != nil {
			t.Fatal(err)
		}
		e.exec(t, `INSERT INTO ad_conversion_sends (workspace_id, rule_id, event_row_id, event_id, occurred_at, status, reason) VALUES ($1, $2, $3, $4, now() - interval '1 hour', $5, $6)`,
			e.ws, rule, ev, fmt.Sprintf("m-%d", i), status, map[bool]string{true: "click too old", false: ""}[status == "failed"])
	}
	// Paid visits: 25 in the last day, 5 with a click id.
	for i := 0; i < 25; i++ {
		ids := `{}`
		if i < 5 {
			ids = `{"gclid": "g"}`
		}
		e.exec(t, `INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, last_seen_at, channel, click_ids, ad) VALUES ($1, $2, now() - interval '2 hours', now() - interval '2 hours', 'paid_ads', $3, '{"campaign": "anomaly"}')`,
			e.ws, fmt.Sprintf("v-%d", i), ids)
	}

	res, err := e.s.RunAlerts(ctx, e.ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{AlertSpendAnomaly, AlertFlatline, AlertZeroConversions, AlertPacing, AlertDisapproved, AlertSendsFailing, AlertTrackingBroken, AlertAutoApplyOn, OverspendPause} {
		if res.Raised[k] != 1 {
			t.Errorf("%s raised %d times: %+v", k, res.Raised[k], res.Raised)
		}
	}
	if res.Paused != 0 {
		t.Fatalf("paused without the brand's say-so: %+v", res)
	}
	rows, err := e.pool.Query(ctx, `SELECT kind, ad_entity_id::text, autonomy, unit, status, rule, rule_version, evidence, reason FROM ad_suggestions WHERE workspace_id = $1`, e.ws)
	if err != nil {
		t.Fatal(err)
	}
	type f struct {
		entity, autonomy, unit, status, rule string
		version                              int
		evidence                             map[string]any
		reason                               string
	}
	got := map[string]f{}
	for rows.Next() {
		var k string
		var x f
		var ev []byte
		if err := rows.Scan(&k, &x.entity, &x.autonomy, &x.unit, &x.status, &x.rule, &x.version, &ev, &x.reason); err != nil {
			t.Fatal(err)
		}
		x.evidence = map[string]any{}
		_ = json.Unmarshal(ev, &x.evidence)
		got[k] = x
	}
	rows.Close()
	if len(got) != 9 {
		t.Fatalf("%d findings: %v", len(got), got)
	}
	for k, x := range got {
		if x.autonomy != "alert" || x.status != "open" || x.rule != k || x.version != alertRuleVersion || x.reason == "" || x.evidence["window"] == nil {
			t.Errorf("%s: %+v", k, x)
		}
	}
	if x := got[AlertSpendAnomaly]; x.entity != anomaly || x.evidence["direction"] != "above" || x.evidence["expected_micros"] != 50_000_000.0 || x.evidence["spend_micros"] != 300_000_000.0 {
		t.Errorf("anomaly: %+v", x)
	}
	if x := got[AlertFlatline]; x.entity != flat || x.evidence["serving_days"] != 7.0 {
		t.Errorf("flatline: %+v", x)
	}
	// With the paid ads ledger in the schema the rule counts sign-ups;
	// without it, the platform's conversions.
	wantUnit := "platform_conversions"
	if ok, _ := tableExists(ctx, e.pool, "ad_acquisitions"); ok {
		wantUnit = "signups"
	}
	if x := got[AlertZeroConversions]; x.entity != noconv || x.unit != wantUnit || x.evidence["brand_campaign"] != true || x.evidence["clicks"] != 120.0 {
		t.Errorf("zero conversions: %+v", x)
	}
	if x := got[AlertPacing]; x.entity != anomaly || x.evidence["scope"] != "account" || x.evidence["ratio"].(float64) <= 1.1 {
		t.Errorf("pacing: %+v", x)
	}
	if x := got[AlertDisapproved]; x.entity != anomaly || x.evidence["ads"] != 1.0 {
		t.Errorf("disapproved: %+v", x)
	}
	if x := got[AlertSendsFailing]; x.evidence["failed"] != 2.0 || x.evidence["sent"] != 10.0 || x.evidence["account_id"] != acct {
		t.Errorf("sends: %+v", x)
	}
	if x := got[AlertTrackingBroken]; x.unit != "signups" || x.evidence["check"] != "capture" || x.evidence["paid_visits"] != 25.0 {
		t.Errorf("tracking: %+v", x)
	}
	if x := got[AlertAutoApplyOn]; fmt.Sprint(x.evidence["types"]) != "[target_cpa_opt_in]" {
		t.Errorf("auto-apply: %+v", x)
	}
	if x := got[OverspendPause]; x.entity != over || x.evidence["multiple"] != 2.5 {
		t.Errorf("overspend: %+v", x)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'ad_alert.raised'`, e.ws); n != 9 {
		t.Fatalf("%d ad_alert.raised audit events", n)
	}

	// Run again: everything still holds, nothing new; then Spiky calms
	// down and its anomaly expires while the others stay.
	if res, err = e.s.RunAlerts(ctx, e.ws); err != nil || len(res.Raised) != 0 || res.Held != 9 || res.Expired != 0 {
		t.Fatalf("second run: %+v %v", res, err)
	}
	day(anomaly, 0, 55, 3000, 150, 1)
	if res, err = e.s.RunAlerts(ctx, e.ws); err != nil || res.Expired != 1 || res.Held != 8 {
		t.Fatalf("third run: %+v %v", res, err)
	}
	if e.count(t, `SELECT count(*) FROM ad_suggestions WHERE workspace_id = $1 AND kind = $2 AND status = 'expired'`, e.ws, AlertSpendAnomaly) != 1 {
		t.Fatal("the anomaly didn't expire")
	}
	// An acknowledged alert isn't raised again for a day while it holds.
	e.exec(t, `UPDATE ad_suggestions SET status = 'acknowledged', updated_at = now() WHERE workspace_id = $1 AND kind = $2`, e.ws, AlertFlatline)
	if res, err = e.s.RunAlerts(ctx, e.ws); err != nil || res.Raised[AlertFlatline] != 0 || res.Held != 7 {
		t.Fatalf("acknowledged: %+v %v", res, err)
	}

	// Without an extension's hook nothing is paused on its own, even with
	// the brand's rules live: the overspend stays an alert for a person.
	if ok, _ := tableExists(ctx, e.pool, "ad_brand_economics"); !ok {
		e.exec(t, `CREATE TABLE ad_brand_economics (brand_id uuid PRIMARY KEY, workspace_id uuid, shadow_mode boolean NOT NULL DEFAULT true)`)
		t.Cleanup(func() { e.pool.Exec(ctx, `DROP TABLE ad_brand_economics`) })
	}
	e.exec(t, `INSERT INTO ad_brand_economics (brand_id, workspace_id, shadow_mode) VALUES ($1, $2, false)`, brand, e.ws)
	if res, err = e.s.RunAlerts(ctx, e.ws); err != nil || res.Paused != 0 || res.Raised[OverspendPause] != 0 {
		t.Fatalf("no hook: %+v %v", res, err)
	}
	var alertAutonomy, alertStatus string
	var alertShadow bool
	if err := e.pool.QueryRow(ctx, `SELECT autonomy, status, shadow FROM ad_suggestions WHERE ad_entity_id = $1 AND kind = $2 AND status <> 'expired'`, over, OverspendPause).
		Scan(&alertAutonomy, &alertStatus, &alertShadow); err != nil || alertAutonomy != "alert" || alertStatus != "open" || alertShadow {
		t.Fatalf("no hook, the overspend: %s %s %v %v", alertAutonomy, alertStatus, alertShadow, err)
	}
	if n := e.count(t, `SELECT count(*) FROM ad_changes WHERE ad_entity_id = $1`, over); n != 0 {
		t.Fatalf("no hook, %d changes", n)
	}

	// With a hook that lets savings apply, the runaway campaign is paused
	// once the brand's rules are live: a queued change with its audit
	// event, and the finding is applied and measuring. The hook is only
	// asked about the pause.
	var asked []string
	paidads.SetAutoApprove(func(_ context.Context, w, b, kind string) bool {
		asked = append(asked, kind)
		return w == e.ws && b == brand && paidads.Saves(kind)
	})
	t.Cleanup(func() { paidads.SetAutoApprove(nil) })
	e.exec(t, `UPDATE ad_brand_economics SET shadow_mode = true WHERE brand_id = $1`, brand)
	if res, err = e.s.RunAlerts(ctx, e.ws); err != nil || res.Paused != 0 {
		t.Fatalf("shadow: %+v %v", res, err)
	}
	for _, k := range asked {
		if k != OverspendPause {
			t.Fatalf("the hook was asked about %v", asked)
		}
	}
	var shadow bool
	var reason string
	if err := e.pool.QueryRow(ctx, `SELECT shadow, reason FROM ad_suggestions WHERE workspace_id = $1 AND kind = $2 AND status = 'open'`, e.ws, OverspendPause).Scan(&shadow, &reason); err != nil || !shadow || reason == "" {
		t.Fatalf("shadow finding: %v %q %v", shadow, reason, err)
	}
	e.exec(t, `UPDATE ad_brand_economics SET shadow_mode = false WHERE brand_id = $1`, brand)
	if res, err = e.s.RunAlerts(ctx, e.ws); err != nil || res.Paused != 1 || res.Raised[OverspendPause] != 1 {
		t.Fatalf("pause: %+v %v", res, err)
	}
	var changeKind, changeStatus string
	var actor *string
	if err := e.pool.QueryRow(ctx, `SELECT kind, status, actor_id FROM ad_changes WHERE ad_entity_id = $1`, over).Scan(&changeKind, &changeStatus, &actor); err != nil || changeKind != "pause" || changeStatus != "queued" || actor != nil {
		t.Fatalf("the pause change: %s %s %v %v", changeKind, changeStatus, actor, err)
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE kind = $1`, JobChange); n != 1 {
		t.Fatalf("%d change jobs", n)
	}
	var status, autonomy string
	var applied bool
	var changeID *string
	if err := e.pool.QueryRow(ctx, `SELECT status, autonomy, applied_auto, change_id FROM ad_suggestions WHERE ad_entity_id = $1 AND kind = $2 AND status <> 'expired'`, over, OverspendPause).
		Scan(&status, &autonomy, &applied, &changeID); err != nil || status != "measuring" || autonomy != "auto" || !applied || changeID == nil {
		t.Fatalf("the applied finding: %s %s %v %v %v", status, autonomy, applied, changeID, err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'ad_campaign.paused' AND actor_type = 'system'`, e.ws); n != 1 {
		t.Fatalf("%d ad_campaign.paused events", n)
	}
	// Once more: the pause is already asked for, so nothing happens twice.
	if res, err = e.s.RunAlerts(ctx, e.ws); err != nil || res.Paused != 1 || e.count(t, `SELECT count(*) FROM ad_changes WHERE ad_entity_id = $1`, over) != 1 {
		t.Fatalf("again: %+v %v", res, err)
	}
	// The shadow alert expired when the pause took over.
	if e.count(t, `SELECT count(*) FROM ad_suggestions WHERE ad_entity_id = $1 AND kind = $2 AND status = 'open'`, over, OverspendPause) != 0 {
		t.Fatal("the alert stayed open beside the applied pause")
	}
}

// The zero-conversions rule reads the ledger when it exists.
func TestAlertsFirstParty(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if ok, _ := tableExists(ctx, e.pool, "ad_acquisitions"); !ok {
		t.Skip("the paid ads ledger (ad_acquisitions) is not in this schema")
	}
	e.f.campaigns = []connectors.AdCampaign{{ID: "c1", Name: "Search", Status: connectors.AdActive, DailyBudgetMicros: ptr(int64(100_000_000))},
		{ID: "c2", Name: "Display", Status: connectors.AdActive, DailyBudgetMicros: ptr(int64(100_000_000))}}
	if _, err := e.s.SyncOnce(ctx, e.ws, e.integration); err != nil {
		t.Fatal(err)
	}
	// The ledger's rows belong to a brand and a person.
	var brand, record string
	if err := e.pool.QueryRow(ctx, `INSERT INTO brands (workspace_id, name, handle) VALUES ($1, 'Acme', 'acme') RETURNING id`, e.ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO records (workspace_id, brand_id, record_type, name) VALUES ($1, $2, 'person', 'Ana') RETURNING id`, e.ws, brand).Scan(&record); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE ad_accounts SET brand_id = $2 WHERE workspace_id = $1`, e.ws, brand)
	e.exec(t, `DELETE FROM ad_metrics_daily WHERE workspace_id = $1`, e.ws)
	c1, c2 := e.entity(t, "c1"), e.entity(t, "c2")
	for _, id := range []string{c1, c2} {
		// 600 clicks and 30 platform conversions each in 30 days.
		e.exec(t, `INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks, conversions)
			SELECT $1, $2::date - d, $3, 'USD', 5000000, 100, 20, 1 FROM generate_series(0, 29) d`, id, e.now.Format(time.DateOnly), e.ws)
	}
	// Only Search brought a sign-up.
	e.exec(t, `INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, campaign_entity_id, clicked_at, signed_up_at, window_days)
		VALUES ($1, $2, $3, 'fake_ads', $4, now() - interval '3 days', now() - interval '3 days', 30)`, e.ws, brand, record, c1)
	res, err := e.s.RunAlerts(ctx, e.ws)
	if err != nil || res.Raised[AlertZeroConversions] != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	var entity, unit string
	if err := e.pool.QueryRow(ctx, `SELECT ad_entity_id::text, unit FROM ad_suggestions WHERE workspace_id = $1 AND kind = $2`, e.ws, AlertZeroConversions).Scan(&entity, &unit); err != nil || entity != c2 || unit != "signups" {
		t.Fatalf("%s %s %v", entity, unit, err)
	}
}
