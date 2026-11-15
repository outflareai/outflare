package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/rules"
	"github.com/outflareai/outflare/engine/internal/scores"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Scores: a brand's own score on every company, worked out quietly when it
// is added or changed, again when a field it reads changes, and once a
// day; rules read it like any field, and the record says why it scores
// what it does.
func TestScores(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	captured := track.Capture(t)

	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Segment", "value_type": "text"})
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "attributes": map[string]any{"segment": "saas"}})["id"].(string)
	north := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)

	// Nothing yet, and scores to start from.
	list := owner.must(200, "GET", base+"/scores", nil)
	if len(list["data"].([]any)) != 0 || len(list["starters"].([]any)) != len(scores.Starters) {
		t.Fatalf("an empty list: %v", list)
	}
	for _, st := range scores.Starters {
		if msg := scores.Check(st); msg != "" {
			t.Fatalf("starter %s: %s", st.Key, msg)
		}
	}

	saas := map[string]any{"label": "Sells software", "points": 30, "conditions": []map[string]any{{"attribute": "segment", "op": "eq", "value": "saas"}}}
	quiet := map[string]any{"label": "No usage this week", "points": -20, "match": "any", "conditions": []map[string]any{
		{"attribute": "metrics.usage_7d", "op": "not_exists"}, {"attribute": "metrics.usage_7d", "op": "lt", "value": 1}}}
	for i, bad := range []map[string]any{
		{"key": "fit", "label": "Fit", "record_type": "account"},
		{"key": "fit", "label": "Fit", "record_type": "account", "factors": []any{}},
		{"key": "fit_band", "label": "Fit", "record_type": "account", "factors": []any{saas}},
		{"key": "fit", "label": "Fit", "record_type": "campaign", "factors": []any{saas}},
		{"key": "fit", "label": "Fit", "record_type": "account", "factors": []any{map[string]any{"label": "x", "points": 0, "conditions": saas["conditions"]}}},
		{"key": "fit", "label": "Fit", "record_type": "account", "factors": []any{map[string]any{"label": "x", "points": 5, "conditions": []map[string]any{{"attribute": "scores.health", "op": "gt", "value": 50}}}}},
		{"key": "fit", "label": "Fit", "record_type": "account", "factors": []any{map[string]any{"label": "x", "points": 5, "conditions": []map[string]any{{"property": "plan", "op": "exists"}}}}},
		{"key": "fit", "label": "Fit", "record_type": "account", "factors": []any{saas}, "high_from": 30, "low_below": 40},
	} {
		if code, body := owner.do("POST", base+"/scores", bad); code != 400 {
			t.Fatalf("bad score %d: %d %v", i, code, body)
		}
	}

	// A rule that reads the score's band.
	rule := owner.must(201, "POST", base+"/rules", map[string]any{"name": "A good fit", "record_type": "account",
		"trigger": map[string]any{"kind": "change"}, "emit_event": "account.fit_high",
		"conditions": []map[string]any{{"attribute": "scores.fit_band", "op": "eq", "value": "high"}}})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+rule+"/activate", nil)
	fired := func(rec string) int {
		return count(`SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = 'account.fit_high'`, ws, rec)
	}

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &scores.Service{Pool: e.pool, Log: log}
	svc.Register(q)
	(&rules.Service{Pool: e.pool, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	settled := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			var failed string
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('queued', 'running')),
				COALESCE(string_agg(kind || ': ' || last_error, '; ') FILTER (WHERE last_error IS NOT NULL), '')
				FROM jobs WHERE workspace_id = $1 AND (kind LIKE 'score.%' OR kind LIKE 'rule.%')`, ws).Scan(&n, &failed)
			if failed != "" {
				t.Fatalf("a job failed: %s", failed)
			}
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for jobs")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	scoreOf := func(rec string) map[string]any {
		t.Helper()
		got := owner.must(200, "GET", base+"/records/"+rec, nil)["attributes"].(map[string]any)
		s, _ := got["scores"].(map[string]any)
		return s
	}

	// Adding it scores every company, quietly: Acme is a high fit from the
	// start, and the rule doesn't fire for it.
	fit := owner.must(201, "POST", base+"/scores", map[string]any{"key": "fit", "label": "Fit", "record_type": "account", "base": 50,
		"high_from": 60, "low_below": 40, "factors": []any{saas, quiet}})
	id := fit["id"].(string)
	if fit["scoring"] != true || fit["version"] != float64(1) || fit["status"] != "active" {
		t.Fatalf("a new score: %v", fit)
	}
	owner.must(409, "POST", base+"/scores", map[string]any{"key": "fit", "label": "Fit", "record_type": "account", "factors": []any{saas}})
	settled()
	if s := scoreOf(acme); s["fit"] != float64(60) || s["fit_band"] != "high" {
		t.Fatalf("Acme's score: %v", s)
	}
	if s := scoreOf(north); s["fit"] != float64(30) || s["fit_band"] != "low" {
		t.Fatalf("Northpeak's score: %v", s)
	}
	if fired(acme) != 0 || fired(north) != 0 {
		t.Fatal("the first pass set a rule off")
	}
	got := owner.must(200, "GET", base+"/scores", nil)["data"].([]any)[0].(map[string]any)
	if c := got["counts"].(map[string]any); got["scoring"] != false || got["scored_at"] == nil || c["high"] != float64(1) || c["low"] != float64(1) || c["medium"] != float64(0) {
		t.Fatalf("after the pass: %v", got)
	}

	// A field the score reads changes: the company is scored again, the
	// rule that reads the score fires, and the move is in its history.
	owner.must(200, "PATCH", base+"/records/"+north, map[string]any{"attributes": map[string]any{"segment": "saas"}})
	settled()
	if s := scoreOf(north); s["fit"] != float64(60) || s["fit_band"] != "high" {
		t.Fatalf("Northpeak after the change: %v", s)
	}
	if fired(north) != 1 || fired(acme) != 0 {
		t.Fatalf("rule firings: Northpeak %d, Acme %d", fired(north), fired(acme))
	}
	if n := count(`SELECT count(*) FROM attribute_changes WHERE workspace_id = $1 AND record_id = $2 AND field_key = 'scores.fit_band'
		AND old_value = '"low"' AND new_value = '"high"' AND source = 'score'`, ws, north); n != 1 {
		t.Fatalf("history of the band: %d", n)
	}

	// The record says why.
	why := owner.must(200, "GET", base+"/records/"+north+"/scores", nil)["data"].([]any)
	if len(why) != 1 {
		t.Fatalf("Northpeak's scores: %v", why)
	}
	w := why[0].(map[string]any)
	fs := w["factors"].([]any)
	if w["value"] != float64(60) || w["band"] != "high" || w["base"] != float64(50) || len(fs) != 2 ||
		fs[0].(map[string]any)["holds"] != true || fs[1].(map[string]any)["points"] != float64(-20) {
		t.Fatalf("why: %v", w)
	}
	owner.must(404, "GET", base+"/records/00000000-0000-4000-8000-000000000000/scores", nil)

	// A new label changes nothing; new points make a new version and a
	// quiet pass.
	owner.must(400, "PATCH", base+"/scores/"+id, map[string]any{"key": "other"})
	if got := owner.must(200, "PATCH", base+"/scores/"+id, map[string]any{"label": "Fit for us"}); got["version"] != float64(1) || got["label"] != "Fit for us" {
		t.Fatalf("a new label: %v", got)
	}
	saas["points"] = 10
	if got := owner.must(200, "PATCH", base+"/scores/"+id, map[string]any{"factors": []any{saas, quiet}}); got["version"] != float64(2) || got["scoring"] != true {
		t.Fatalf("new points: %v", got)
	}
	settled()
	if s := scoreOf(acme); s["fit"] != float64(40) || s["fit_band"] != "medium" {
		t.Fatalf("Acme after new points: %v", s)
	}

	// Once a day every score is worked out again.
	if _, err := e.pool.Exec(ctx, `UPDATE score_definitions SET scored_at = now() - interval '2 days' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 0} {
		if n, err := svc.Daily(ctx); err != nil || n != want {
			t.Fatalf("daily %d: %d %v, want %d", i, n, err, want)
		}
	}
	settled()
	if n := count(`SELECT count(*) FROM score_definitions WHERE id = $1 AND scoring_since IS NULL AND scored_at > now() - interval '1 minute'`, id); n != 1 {
		t.Fatal("the daily pass didn't finish")
	}

	// Archived, it leaves every record and its key is free again.
	if got := owner.must(200, "POST", base+"/scores/"+id+"/archive", nil); got["status"] != "archived" {
		t.Fatalf("archived: %v", got)
	}
	owner.must(400, "PATCH", base+"/scores/"+id, map[string]any{"label": "x"})
	settled()
	if s := scoreOf(acme); s != nil {
		t.Fatalf("Acme after the archive: %v", s)
	}
	if n := len(owner.must(200, "GET", base+"/scores?archived=true", nil)["data"].([]any)); n != 1 {
		t.Fatalf("archived scores: %d", n)
	}
	owner.must(201, "POST", base+"/scores", map[string]any{"key": "fit", "label": "Fit", "record_type": "account", "factors": []any{saas}})
	owner.must(404, "PATCH", base+"/scores/00000000-0000-4000-8000-000000000000", map[string]any{"label": "x"})

	names := map[string]bool{}
	for _, ev := range captured() {
		names[ev.Name] = true
	}
	if !names["score.created"] || !names["score.updated"] || !names["score.archived"] {
		t.Fatalf("score events: %v", names)
	}
}

// A headline score is made of other scores; a record keeps where each
// score has been; a score with nothing to go on gives no value; deals are
// scored too, from their own facts.
func TestScoreParts(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&scores.Service{Pool: e.pool, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	settled := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			var failed string
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('queued', 'running')),
				COALESCE(string_agg(kind || ': ' || last_error, '; ') FILTER (WHERE last_error IS NOT NULL), '')
				FROM jobs WHERE workspace_id = $1 AND kind LIKE 'score.%'`, ws).Scan(&n, &failed)
			if failed != "" {
				t.Fatalf("a job failed: %s", failed)
			}
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for jobs")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, f := range []string{"Segment", "Mood"} {
		owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": f, "value_type": "text"})
	}
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "attributes": map[string]any{"segment": "saas"}})["id"].(string)
	factor := func(label, field, value string, points int) map[string]any {
		return map[string]any{"label": label, "points": points, "conditions": []map[string]any{{"attribute": field, "op": "eq", "value": value}}}
	}
	score := func(key string, more map[string]any) map[string]any {
		in := map[string]any{"key": key, "label": key, "record_type": "account"}
		for k, v := range more {
			in[k] = v
		}
		return in
	}
	owner.must(201, "POST", base+"/scores", score("fit", map[string]any{"factors": []any{factor("Sells software", "segment", "saas", 30)}}))
	// Mood is only known once someone wrote it down.
	owner.must(201, "POST", base+"/scores", score("mood", map[string]any{"nature": "ai", "needs": "mood",
		"factors": []any{factor("Sounds unhappy", "mood", "unhappy", -40)}}))
	parts := []map[string]any{{"key": "fit", "weight": 3}, {"key": "mood", "weight": 1}}
	// A headline score: not measured when a part isn't, never both parts
	// and factors, and only of scores that exist.
	for i, bad := range []map[string]any{
		score("overall", map[string]any{"parts": parts}),
		score("overall", map[string]any{"nature": "ai", "parts": parts, "factors": []any{factor("x", "segment", "saas", 5)}}),
		score("overall", map[string]any{"nature": "ai", "parts": []map[string]any{{"key": "fit", "weight": 1}, {"key": "gone", "weight": 1}}}),
		score("overall", map[string]any{"nature": "ai", "parts": parts[:1]}),
		score("watch", map[string]any{"factors": []any{factor("x", "scores.mood_band", "low", 5)}}),
	} {
		if code, body := owner.do("POST", base+"/scores", bad); code != 400 {
			t.Fatalf("bad score %d: %d %v", i, code, body)
		}
	}
	overall := owner.must(201, "POST", base+"/scores", score("overall", map[string]any{"nature": "ai", "parts": parts}))
	if overall["nature"] != "ai" || len(overall["parts"].([]any)) != 2 || overall["higher_is_better"] != true {
		t.Fatalf("a headline score: %v", overall)
	}
	risk := owner.must(201, "POST", base+"/scores", score("risk", map[string]any{"nature": "predicted", "higher_is_better": false, "base": 10,
		"factors": []any{factor("Overall is low", "scores.overall_band", "low", 60)}}))
	settled()
	why := func(rec string) map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, x := range owner.must(200, "GET", base+"/records/"+rec+"/scores", nil)["data"].([]any) {
			m := x.(map[string]any)
			out[m["key"].(string)] = m
		}
		return out
	}
	// No mood yet: no mood score, and overall is fit alone.
	got := why(acme)
	if _, has := got["mood"]; has || got["fit"]["value"] != float64(80) || got["overall"]["value"] != float64(80) || got["risk"]["value"] != float64(10) {
		t.Fatalf("before a mood: %v", got)
	}
	ps := got["overall"]["parts"].([]any)
	if len(ps) != 2 || ps[0].(map[string]any)["value"] != float64(80) || ps[1].(map[string]any)["value"] != nil || got["fit"]["part_of"] != "overall" ||
		got["risk"]["higher_is_better"] != false || got["risk"]["nature"] != "predicted" {
		t.Fatalf("parts: %v", got)
	}
	// Yesterday it was higher: the record keeps the trend.
	if _, err := e.pool.Exec(ctx, `UPDATE score_history SET day = day - 40 WHERE record_id = $1`, acme); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PATCH", base+"/records/"+acme, map[string]any{"attributes": map[string]any{"mood": "unhappy", "segment": "retail"}})
	settled()
	got = why(acme)
	// fit 50, mood 10: (150 + 10) / 4 = 40.
	if got["fit"]["value"] != float64(50) || got["mood"]["value"] != float64(10) || got["overall"]["value"] != float64(40) || got["overall"]["change"] != float64(-40) {
		t.Fatalf("after a mood: %v", got)
	}
	if h := got["overall"]["history"].([]any); len(h) != 2 || h[1].(map[string]any)["value"] != float64(40) {
		t.Fatalf("history: %v", h)
	}
	// A score others are made of stays until they let go of it.
	var fitID string
	for _, x := range owner.must(200, "GET", base+"/scores", nil)["data"].([]any) {
		if m := x.(map[string]any); m["key"] == "fit" {
			fitID = m["id"].(string)
		}
	}
	owner.must(400, "POST", base+"/scores/"+fitID+"/archive", nil)
	owner.must(400, "POST", base+"/scores/"+overall["id"].(string)+"/archive", nil)
	owner.must(200, "POST", base+"/scores/"+risk["id"].(string)+"/archive", nil)
	owner.must(200, "POST", base+"/scores/"+overall["id"].(string)+"/archive", nil)

	// Deals: the starter has a factor per stage, and a deal's own facts
	// count.
	var win map[string]any
	for _, x := range owner.must(200, "GET", base+"/scores", nil)["starters"].([]any) {
		if m := x.(map[string]any); m["key"] == "win" {
			win = m
		}
	}
	if win == nil || win["record_type"] != "deal" || win["nature"] != "predicted" || len(win["factors"].([]any)) < 9 {
		t.Fatalf("the win starter: %v", win)
	}
	owner.must(201, "POST", base+"/scores", win)
	deal := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme expansion",
		"attributes": map[string]any{"stage": "qualified", "close_date": time.Now().AddDate(0, 0, -3).Format(time.DateOnly)}})["id"].(string)
	settled()
	w := why(deal)["win"]
	if w == nil {
		t.Fatalf("a deal's scores: %v", why(deal))
	}
	held := map[string]bool{}
	for _, x := range w["factors"].([]any) {
		if f := x.(map[string]any); f["holds"] == true {
			held[f["label"].(string)] = true
		}
	}
	if !held["Past its close date"] || !held["Has a close date"] || !held["In the stage Qualified"] || held["In the same stage for over 30 days"] {
		t.Fatalf("what holds on the deal: %v", held)
	}
	// Checked against what happened: nothing closed yet, then a deal that
	// was scored before it was won counts in its band.
	var winID string
	for _, x := range owner.must(200, "GET", base+"/scores", nil)["data"].([]any) {
		if m := x.(map[string]any); m["key"] == "win" {
			winID = m["id"].(string)
		}
	}
	tr := owner.must(200, "GET", base+"/scores/"+winID+"/track-record", nil)
	if tr["outcome"] != "won" || tr["records"] != float64(0) || tr["enough"] != false || len(tr["bands"].([]any)) != 3 {
		t.Fatalf("an empty track record: %v", tr)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE score_history SET day = day - 2 WHERE record_id = $1`, deal); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE deals SET state = 'won', closed_at = now() WHERE id = $1`, deal); err != nil {
		t.Fatal(err)
	}
	tr = owner.must(200, "GET", base+"/scores/"+winID+"/track-record", nil)
	hit := 0
	for _, x := range tr["bands"].([]any) {
		if b := x.(map[string]any); b["records"] == float64(1) && b["happened"] == float64(1) && b["rate"] == float64(100) && b["band"] == w["band"] {
			hit++
		}
	}
	if tr["records"] != float64(1) || hit != 1 {
		t.Fatalf("a track record: %v (the deal was %v)", tr, w["band"])
	}
	if got := owner.must(200, "GET", base+"/scores/"+fitID+"/track-record", nil); got["outcome"] != nil {
		t.Fatalf("a measured score's track record: %v", got)
	}
	var stored int
	if err := e.pool.QueryRow(ctx, `SELECT (attributes->'scores'->>'win')::int FROM records WHERE id = $1`, deal).Scan(&stored); err != nil || float64(stored) != w["value"] {
		t.Fatalf("the deal's stored score: %d %v, want %v", stored, err, w["value"])
	}
}
