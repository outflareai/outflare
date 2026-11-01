package api_test

import (
	"context"
	"encoding/json"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestPartnerTasksAndWebsiteProposals(t *testing.T) {
	captured := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})
	p := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak partners", "kind": "affiliate", "brand": "northpeak", "auto_accept": true})
	program := p["id"].(string)
	admin := base + "/partner-programs/" + program
	portal := "/p/" + program + "/api"
	session := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "user-a"})
	partner := &client{t: t, h: e.h, bearer: session["token"].(string)}
	sessionB := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "user-b"})
	other := &client{t: t, h: e.h, bearer: sessionB["token"].(string)}
	newTask := func(input map[string]any) string {
		t.Helper()
		return owner.must(200, "POST", admin+"/tasks", input)["id"].(string)
	}
	task := newTask(map[string]any{"title": "Share a tutorial", "description": "Publish a useful, disclosed tutorial.", "reward_kind": "cash", "amount_cents": 5000, "slots": 1})
	owner.must(400, "PATCH", admin, map[string]any{"currency": "EUR"})
	assignment := partner.must(200, "POST", portal+"/tasks/"+task, map[string]any{"action": "apply"})
	if assignment["status"] != "accepted" {
		t.Fatalf("claim=%v", assignment)
	}
	other.must(400, "POST", portal+"/tasks/"+task, map[string]any{"action": "apply"})
	id := assignment["id"].(string)
	owner.must(400, "POST", admin+"/task-submissions/"+id, map[string]any{"action": "approve"})
	partner.must(200, "POST", portal+"/tasks/"+task, map[string]any{"action": "submit", "evidence_url": "https://example.com/tutorial"})
	owner.must(200, "POST", admin+"/task-submissions/"+id, map[string]any{"action": "approve"})
	owner.must(400, "POST", admin+"/task-submissions/"+id, map[string]any{"action": "approve"})
	var rewards int
	var amount int64
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*),COALESCE(sum(amount_cents),0) FROM partner_rewards WHERE workspace_id=$1 AND membership_id=$2`, ws, session["partner_id"]).Scan(&rewards, &amount); err != nil {
		t.Fatal(err)
	}
	if rewards != 1 || amount != 5000 {
		t.Fatalf("duplicate/incorrect task reward: %d / %d", rewards, amount)
	}
	free := newTask(map[string]any{"title": "Visit the project", "description": "Voluntary task.", "reward_kind": "free", "amount_cents": 1000})
	f := partner.must(200, "POST", portal+"/tasks/"+free, map[string]any{"action": "apply"})
	partner.must(200, "POST", portal+"/tasks/"+free, map[string]any{"action": "submit", "note": "Reviewed the project and shared my feedback."})
	owner.must(200, "POST", admin+"/task-submissions/"+f["id"].(string), map[string]any{"action": "approve"})
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM partner_rewards WHERE workspace_id=$1`, ws).Scan(&rewards); err != nil || rewards != 1 {
		t.Fatalf("unpaid task created a reward: %d %v", rewards, err)
	}
	// A private proposal is entirely absent until ownership of the exact host is proved.
	target := newTask(map[string]any{"title": "Private editorial placement", "description": "An offer for your website.", "target_host": "publisher.example.com", "reward_kind": "cash", "pricing": "range", "amount_cents": 10000, "max_amount_cents": 30000})
	count := func(c *client) int { t.Helper(); return len(c.must(200, "GET", portal+"/tasks", nil)["data"].([]any)) }
	if count(partner) != 2 {
		t.Fatal("private proposal leaked before verification")
	}
	asset := partner.must(200, "POST", portal+"/audiences", map[string]any{"url": "https://publisher.example.com", "kind": "website", "reach": 5000})["id"].(string)
	if count(partner) != 2 {
		t.Fatal("self-report unlocked a proposal")
	}
	partner.must(400, "POST", portal+"/tasks/"+target, map[string]any{"action": "apply", "audience_id": asset})
	// DNS checker tests the proof independently; simulate a completed ownership check here.
	if _, err := e.pool.Exec(context.Background(), `UPDATE partner_audiences SET verified_at=now() WHERE workspace_id=$1 AND id=$2`, ws, asset); err != nil {
		t.Fatal(err)
	}
	if count(partner) != 3 || count(other) != 2 {
		t.Fatal("website proposal isolation failed")
	}
	if n := len(other.must(200, "GET", portal+"/audiences", nil)["data"].([]any)); n != 0 {
		t.Fatal("another partner's profiles leaked")
	}
	other.must(404, "POST", portal+"/audiences/"+asset, map[string]any{"action": "verify"})
	app := partner.must(200, "POST", portal+"/tasks/"+target, map[string]any{"action": "apply", "audience_id": asset})
	appID := app["id"].(string)
	partner.must(400, "POST", portal+"/tasks/"+target, map[string]any{"action": "submit", "audience_id": asset, "evidence_url": "https://publisher.example.com/post"})
	owner.must(400, "POST", admin+"/task-submissions/"+appID, map[string]any{"action": "offer", "amount_cents": 50000})
	owner.must(200, "POST", admin+"/task-submissions/"+appID, map[string]any{"action": "offer", "amount_cents": 20000})
	partner.must(200, "POST", portal+"/tasks/"+target, map[string]any{"action": "accept", "audience_id": asset})
	partner.must(400, "POST", portal+"/audiences/"+asset, map[string]any{"action": "remove"})
	partner.must(200, "POST", portal+"/tasks/"+target, map[string]any{"action": "submit", "audience_id": asset, "evidence_url": "https://publisher.example.com/post"})
	owner.must(200, "POST", admin+"/task-submissions/"+appID, map[string]any{"action": "approve"})
	partner.must(400, "POST", portal+"/audiences/"+asset, map[string]any{"action": "refresh"}) // workspace hasn't connected its own page reader
	// A platform session is not a workspace API token.
	partner.must(401, "GET", admin+"/tasks", nil)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme"})
	scopedToken := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Acme team", "scopes": []string{"partners.read", "partners.manage"}, "brand": "acme"})["token"].(string)
	scoped := &client{t: t, h: e.h, bearer: scopedToken}
	scoped.must(404, "GET", admin+"/tasks", nil)
	scoped.must(404, "GET", admin+"/audiences", nil)
	scoped.must(404, "POST", admin+"/tasks/"+task, map[string]any{"action": "pause"})
	// Moving the program moves the new brand-owned rows with it.
	owner.must(200, "PATCH", admin, map[string]any{"brand": "acme"})
	scoped.must(200, "GET", admin+"/tasks", nil)
	scoped.must(200, "GET", admin+"/audiences", nil)
	// The agreed task remains submittable if audience reach changes after acceptance.
	reachTask := newTask(map[string]any{"title": "An agreed placement", "description": "Complete the agreed placement.", "asset_kind": "website", "min_reach": 5000})
	partner.must(200, "POST", portal+"/tasks/"+reachTask, map[string]any{"action": "apply", "audience_id": asset})
	if _, err := e.pool.Exec(context.Background(), `UPDATE partner_audiences SET reach=0 WHERE workspace_id=$1 AND id=$2`, ws, asset); err != nil {
		t.Fatal(err)
	}
	partner.must(200, "POST", portal+"/tasks/"+reachTask, map[string]any{"action": "submit", "audience_id": asset, "evidence_url": "https://publisher.example.com/agreed-placement"})
	for _, name := range []string{"partner_task.created", "partner_task_submission.reviewed", "partner_audience.created"} {
		found := false
		for _, ev := range captured() {
			if ev.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing event %s", name)
		}
	}
	req := httptest.NewRequest("GET", "/p/"+program+"/embed?view=portal", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "function profiles()") || rec.Header().Get("X-Frame-Options") != "" {
		t.Fatal("full embedded task portal unavailable")
	}
}

func TestPartnerTaskRestrictions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	p := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak", "kind": "affiliate", "auto_accept": true})
	admin := base + "/partner-programs/" + p["id"].(string)
	for _, bad := range []map[string]any{
		{"title": "Bad", "description": "Task", "verification": "auto"},
		{"title": "Bad", "description": "Task", "reward_kind": "cash", "pricing": "range", "amount_cents": 200, "max_amount_cents": 100},
		{"title": "Bad", "description": "Task", "target_host": "https://example.com"},
	} {
		owner.must(400, "POST", admin+"/tasks", bad)
	}
	task := owner.must(200, "POST", admin+"/tasks", map[string]any{"title": "Try the product", "description": "A voluntary task"})["id"].(string)
	session := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "u1"})
	partner := &client{t: t, h: e.h, bearer: session["token"].(string)}
	portal := "/p/" + p["id"].(string) + "/api"
	owner.must(200, "POST", admin+"/tasks/"+task, map[string]any{"action": "pause"})
	partner.must(400, "POST", portal+"/tasks/"+task, map[string]any{"action": "apply"})
	owner.must(200, "POST", admin+"/tasks/"+task, map[string]any{"action": "resume"})
	owner.must(200, "PATCH", base+"/partners/"+session["partner_id"].(string), map[string]any{"status": "suspended"})
	partner.must(403, "POST", portal+"/tasks/"+task, map[string]any{"action": "apply"})
	partner.must(403, "POST", portal+"/audiences", map[string]any{"url": "https://example.com", "kind": "website"})
}

func TestPartnerTaskAutomaticChecks(t *testing.T) {
	captured := track.Capture(t)
	var brokenPage atomic.Bool
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	// The fake reader requires the workspace's own key, as the real connector does.
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != "gn-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/v1/usage/concurrency" {
			io.WriteString(w, `{"work_concurrency_in_use":0,"work_concurrency_limit":5}`)
			return
		}
		var in struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		status := 200
		if brokenPage.Load() {
			status = 500
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"html": `<p>12.5K members</p><a href="https://brand.example/product">Product</a>`}, "metadata": map[string]any{"http_status": status, "url": in.URL}, "tokens_charged": 1})
	}))
	defer reader.Close()
	e.integrations.Client.BaseURLs["geonode"] = reader.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	p := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak", "kind": "affiliate", "auto_accept": true})
	program := p["id"].(string)
	admin := base + "/partner-programs/" + program
	portal := "/p/" + program + "/api"
	session := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "u1"})
	partner := &client{t: t, h: e.h, bearer: session["token"].(string)}
	asset := partner.must(200, "POST", portal+"/audiences", map[string]any{"url": "https://publisher.example", "kind": "website", "reach": 10})["id"].(string)
	// Read the generated challenge and publish it in the fake DNS before starting workers.
	a := partner.must(200, "GET", portal+"/audiences", nil)["data"].([]any)[0].(map[string]any)
	e.dns.txt["_outflare-partner.publisher.example"] = []string{"outflare-partner=" + a["challenge"].(string)}
	partner.must(200, "POST", portal+"/audiences/"+asset, map[string]any{"action": "verify"})
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET run_at=now()+interval '1 hour' WHERE kind<>'partners.task_check'`); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	e.srv.RegisterJobs(q)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(run, 1); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, partner, portal+"/audiences", func(out map[string]any) bool {
		v := out["data"].([]any)[0].(map[string]any)
		return v["verified_at"] != nil && v["reach"] == 12500.0 && v["reach_source"] == "scraped"
	})
	for _, mode := range []string{"auto", "auto_hold"} {
		task := owner.must(200, "POST", admin+"/tasks", map[string]any{"title": "Website link", "description": "Publish a disclosed placement.", "reward_kind": "cash", "amount_cents": 1000, "asset_kind": "website", "verification": mode, "hold_days": 1, "required_link": "https://brand.example/product"})["id"].(string)
		sub := partner.must(200, "POST", portal+"/tasks/"+task, map[string]any{"action": "apply", "audience_id": asset})["id"].(string)
		partner.must(200, "POST", portal+"/tasks/"+task, map[string]any{"action": "submit", "audience_id": asset, "evidence_url": "https://publisher.example/post"})
		desired := "approved"
		if mode == "auto_hold" {
			desired = "held"
		}
		waitFor(t, owner, admin+"/tasks", func(out map[string]any) bool {
			for _, raw := range out["submissions"].([]any) {
				v := raw.(map[string]any)
				if v["id"] == sub {
					return v["status"] == desired || v["check_status"] == desired
				}
			}
			return false
		})
		if mode == "auto_hold" {
			var n int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_rewards WHERE workspace_id=$1`, ws).Scan(&n); err != nil || n != 1 {
				t.Fatalf("held task paid before release: %d %v", n, err)
			}
			if _, err := e.pool.Exec(ctx, `UPDATE partner_task_submissions SET hold_until=now()-interval '1 minute' WHERE workspace_id=$1 AND id=$2`, ws, sub); err != nil {
				t.Fatal(err)
			}
			if _, err := e.pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE workspace_id=$1 AND kind='partners.task_check' AND payload->>'action'='release'`, ws); err != nil {
				t.Fatal(err)
			}
			waitFor(t, owner, admin+"/tasks", func(out map[string]any) bool {
				for _, raw := range out["submissions"].([]any) {
					v := raw.(map[string]any)
					if v["id"] == sub {
						return v["status"] == "approved"
					}
				}
				return false
			})
		}
	}
	// An unreadable page stays submitted for a person and never pays automatically.
	brokenPage.Store(true)
	unreadable := owner.must(200, "POST", admin+"/tasks", map[string]any{"title": "Unreadable placement", "description": "Human review on a failed read.", "reward_kind": "cash", "amount_cents": 1000, "asset_kind": "website", "verification": "auto", "required_link": "https://brand.example/product"})["id"].(string)
	failed := partner.must(200, "POST", portal+"/tasks/"+unreadable, map[string]any{"action": "apply", "audience_id": asset})["id"].(string)
	partner.must(200, "POST", portal+"/tasks/"+unreadable, map[string]any{"action": "submit", "audience_id": asset, "evidence_url": "https://publisher.example/unreadable"})
	waitFor(t, owner, admin+"/tasks", func(out map[string]any) bool {
		for _, raw := range out["submissions"].([]any) {
			v := raw.(map[string]any)
			if v["id"] == failed {
				return v["status"] == "submitted" && v["check_status"] == "human_required"
			}
		}
		return false
	})
	if len(named(captured(), "partner_task.check_failed")) == 0 {
		t.Fatal("page read failure was not recorded")
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_rewards WHERE workspace_id=$1`, ws).Scan(&n); err != nil || n != 2 {
		t.Fatalf("automatic reward count: %d %v", n, err)
	}
}
