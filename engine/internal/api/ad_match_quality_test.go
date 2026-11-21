package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/adconv"
)

// Match quality: a daily job keeps what Meta says about how well a pixel's
// events match, the send log's route shows the latest score with how it
// moved, and an event a rule sends that reads low or fell raises an alert
// once a week.
func TestAdMatchQuality(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	route := "/v1/brands/" + brand + "/ads/match-quality"
	if page := owner.must(200, "GET", route, nil); len(page["data"].([]any)) != 0 || page["low_under"] != 6.0 || page["fell_by"] != 1.0 {
		t.Fatalf("empty: %v", page)
	}

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	// Ten days ago, so the later reads are still in the past.
	now := time.Now().UTC().AddDate(0, 0, -10)
	s := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	queued := func() int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, adconv.JobMatchQuality, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	alerts := func() int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE kind = $1 AND workspace_id = $2`, adconv.AlertMatchQualityLow, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Without a rule there is nothing to read.
	if err := s.Tick(ctx); err != nil || queued() != 0 {
		t.Fatalf("no rule: %d queued, %v", queued(), err)
	}
	owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "order_completed", "target_id": "777", "event_type": "Purchase"})
	if err := s.Tick(ctx); err != nil || queued() != 1 {
		t.Fatalf("one pixel: %d queued, %v", queued(), err)
	}

	read := func(score float64) adconv.MatchQualityResult {
		t.Helper()
		metaQuality.Lock()
		metaQuality.score = score
		metaQuality.Unlock()
		res, err := s.ReadMatchQuality(ctx, ws, acct, "777")
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// A good score: both scored events are kept, nobody is told. PageView
	// reads low, but no rule sends it.
	if res := read(8.4); res.Events != 2 || res.Low != 0 || res.Alerts != 0 || res.Connector != "meta_ads" {
		t.Fatalf("first read: %+v", res)
	}
	// Today's row exists, so the next tick asks for nothing more.
	if err := s.Tick(ctx); err != nil || queued() != 1 {
		t.Fatalf("read today: %d queued, %v", queued(), err)
	}
	rows := owner.must(200, "GET", route, nil)["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows: %v", rows)
	}
	first := rows[0].(map[string]any)
	if first["event"] != "Purchase" || first["sent"] != true || first["score"] != 8.4 || first["status"] != "ok" || first["score_before"] != nil ||
		first["target_name"] != "Northpeak pixel" || first["day"] != now.Format(time.DateOnly) || len(first["history"].([]any)) != 1 {
		t.Fatalf("purchase: %v", first)
	}
	if k := first["keys"].([]any)[0].(map[string]any); k["key"] != "email" || k["percent"] != 92.5 {
		t.Fatalf("keys: %v", first["keys"])
	}
	if is := first["issues"].([]any)[0].(map[string]any); is["name"] != "Server sending mismatched IP addresses" || is["percent"] != 61.5 || is["solution"] == "" {
		t.Fatalf("issues: %v", first["issues"])
	}
	if other := rows[1].(map[string]any); other["event"] != "PageView" || other["sent"] != false || other["status"] != "low" {
		t.Fatalf("page view: %v", other)
	}

	// The next day it fell by more than a point: an alert, once.
	now = now.AddDate(0, 0, 1)
	if res := read(7.1); res.Low != 1 || res.Alerts != 1 {
		t.Fatalf("fell: %+v", res)
	}
	if res := read(7.1); res.Alerts != 0 || alerts() != 1 {
		t.Fatalf("told twice: %+v, %d alerts", res, alerts())
	}
	first = owner.must(200, "GET", route, nil)["data"].([]any)[0].(map[string]any)
	if first["status"] != "fell" || first["score"] != 7.1 || first["score_before"] != 8.4 || len(first["history"].([]any)) != 2 {
		t.Fatalf("fell row: %v", first)
	}
	list := owner.must(200, "GET", base+"/alerts", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["kind"] != adconv.AlertMatchQualityLow || list[0].(map[string]any)["path"] != "ads/sends" {
		t.Fatalf("alerts: %v", list)
	}
	// Under six a week later: low, and told again.
	now = now.AddDate(0, 0, 7)
	if res := read(5.2); res.Low != 1 || res.Alerts != 1 || alerts() != 2 {
		t.Fatalf("low: %+v, %d alerts", res, alerts())
	}
	// A rule switched off: nothing to read.
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET enabled = false WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadMatchQuality(ctx, ws, acct, "777"); err == nil {
		t.Fatal("read a pixel no rule sends to")
	}
}
