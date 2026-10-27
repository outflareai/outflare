package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/deploys"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeGitHub serves two repositories: acme/website records deployments,
// acme/blog doesn't and deploys on push.
type fakeGitHub struct {
	mu     sync.Mutex
	status string // the newest deployment's newest status
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer github_pat_test_not_a_real_token" {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	status := f.status
	f.mu.Unlock()
	now := time.Now().UTC()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/user":
		io.WriteString(w, `{"id":7,"login":"acme-bot"}`)
	case "/user/repos":
		io.WriteString(w, `[{"full_name":"acme/website","html_url":"https://github.com/acme/website","default_branch":"main","homepage":"https://www.acme.example"},
			{"full_name":"acme/blog","html_url":"https://github.com/acme/blog","default_branch":"main","homepage":""},
			{"full_name":"acme/old","archived":true}]`)
	case "/repos/acme/website/deployments":
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 2, "sha": "bbb", "ref": "main", "environment": "Production", "description": "Fix titles\nmore", "created_at": now.Add(-10 * time.Minute)},
			{"id": 1, "sha": "aaa", "ref": "main", "environment": "Preview", "created_at": now.Add(-time.Hour)},
		})
	case "/repos/acme/website/deployments/2/statuses":
		json.NewEncoder(w).Encode([]map[string]any{{"state": status, "log_url": "https://github.com/acme/website/actions/runs/1", "created_at": now.Add(-5 * time.Minute)}})
	case "/repos/acme/blog/deployments":
		io.WriteString(w, `[]`)
	case "/repos/acme/blog":
		io.WriteString(w, `{"full_name":"acme/blog","default_branch":"main"}`)
	case "/repos/acme/blog/commits":
		if r.URL.Query().Get("sha") != "main" {
			http.Error(w, "branch", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{{"sha": "ccc", "html_url": "https://github.com/acme/blog/commit/ccc",
			"commit": map[string]any{"message": "New post", "committer": map[string]any{"date": now.Add(-2 * time.Hour)}}}})
	default:
		http.NotFound(w, r)
	}
}

func fakeVercel(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vercel_test_not_a_real_token" || r.URL.Query().Get("teamId") != "team_test" {
			http.Error(w, `{"error":{"code":"forbidden"}}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/user":
			io.WriteString(w, `{"user":{"id":"u1","username":"acme"}}`)
		case "/v10/projects":
			io.WriteString(w, `{"projects":[{"id":"prj_1","name":"docs","link":{"productionBranch":"main"},"targets":{"production":{"alias":["docs-acme.vercel.app","docs.acme.example"]}}}]}`)
		case "/v6/deployments":
			if r.URL.Query().Get("projectId") != "prj_1" || r.URL.Query().Get("target") != "production" {
				http.Error(w, "query", http.StatusBadRequest)
				return
			}
			ms := time.Now().Add(-30 * time.Minute).UnixMilli()
			json.NewEncoder(w).Encode(map[string]any{"deployments": []map[string]any{
				{"uid": "dpl_2", "state": "READY", "created": ms, "ready": ms + 60000, "inspectorUrl": "https://vercel.com/acme/docs/dpl_2",
					"meta": map[string]string{"githubCommitSha": "ddd", "githubCommitRef": "main", "githubCommitMessage": "Docs"}},
				{"uid": "dpl_1", "state": "ERROR", "created": ms - 60000},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDeployTargets(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	gh := &fakeGitHub{status: "in_progress"}
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(ghSrv.Close)
	e.integrations.Client.BaseURLs["github"] = ghSrv.URL
	e.integrations.Client.BaseURLs["vercel"] = fakeVercel(t).URL

	// A site for the repository's homepage, so the target finds it.
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://acme.example/"})
	siteID := site["id"].(string)

	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "github", "credentials": map[string]string{"api_key": "github_pat_test_not_a_real_token"}})
	if conn["category"] != "code" {
		t.Fatalf("category %v", conn["category"])
	}
	ghID := conn["id"].(string)
	opts := owner.must(200, "GET", base+"/integrations/"+ghID+"/deploy-options", nil)["data"].([]any)
	if len(opts) != 2 || opts[0].(map[string]any)["domains"].([]any)[0] != "acme.example" {
		t.Fatalf("options %v", opts)
	}
	owner.must(400, "POST", base+"/deploy-targets", map[string]any{"integration_id": ghID, "target": "acme/secret"})
	tgt := owner.must(201, "POST", base+"/deploy-targets", map[string]any{"integration_id": ghID, "target": "acme/website"})
	if tgt["site_id"] != siteID || tgt["connector"] != "github" || tgt["name"] != "acme/website" {
		t.Fatalf("target %v", tgt)
	}
	owner.must(409, "POST", base+"/deploy-targets", map[string]any{"integration_id": ghID, "target": "acme/website"})
	tid := tgt["id"].(string)

	// The deploy is still building: recorded, nothing detected.
	svc := &deploys.Service{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var hooked []deploys.Deploy
	svc.OnReady = append(svc.OnReady, func(_ context.Context, _ pgx.Tx, d deploys.Deploy) error { hooked = append(hooked, d); return nil })
	res, err := svc.Check(context.Background(), ws, tid)
	if err != nil || res.Seen != 1 || res.New != 1 || res.Live != 0 {
		t.Fatalf("check %v %v", res, err)
	}
	// Live now: detected once, with the site, and the hook runs.
	gh.mu.Lock()
	gh.status = "success"
	gh.mu.Unlock()
	if res, err = svc.Check(context.Background(), ws, tid); err != nil || res.New != 0 || res.Live != 1 {
		t.Fatalf("check %v %v", res, err)
	}
	if res, _ = svc.Check(context.Background(), ws, tid); res.Live != 0 {
		t.Fatalf("detected twice: %v", res)
	}
	if len(hooked) != 1 || hooked[0].SiteID == nil || *hooked[0].SiteID != siteID || hooked[0].Commit != "bbb" {
		t.Fatalf("hooked %v", hooked)
	}
	list := owner.must(200, "GET", base+"/deploys?site_id="+siteID, nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["state"] != "ready" || list[0].(map[string]any)["message"] != "Fix titles" {
		t.Fatalf("deploys %v", list)
	}
	var n int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'deploy.detected'`, ws).Scan(&n)
	if n != 1 {
		t.Fatalf("deploy.detected %d times", n)
	}

	// A repository without deployments deploys on push.
	blog := owner.must(201, "POST", base+"/deploy-targets", map[string]any{"integration_id": ghID, "target": "acme/blog", "site_id": nil})
	if blog["site_id"] != nil {
		t.Fatalf("blog %v", blog)
	}
	if res, err := svc.Check(context.Background(), ws, blog["id"].(string)); err != nil || res.Live != 1 {
		t.Fatalf("blog check %v %v", res, err)
	}
	if d := owner.must(200, "GET", base+"/deploys?target_id="+blog["id"].(string), nil)["data"].([]any); len(d) != 1 || d[0].(map[string]any)["kind"] != "push" {
		t.Fatalf("blog deploys %v", d)
	}

	// Vercel, with a team.
	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "vercel", "credentials": map[string]string{"api_key": "vercel_test_not_a_real_token"}})
	vc := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "vercel", "credentials": map[string]string{"api_key": "vercel_test_not_a_real_token", "team_id": "team_test"}})
	vopts := owner.must(200, "GET", base+"/integrations/"+vc["id"].(string)+"/deploy-options", nil)["data"].([]any)
	if d := vopts[0].(map[string]any)["domains"].([]any); len(d) != 1 || d[0] != "docs.acme.example" {
		t.Fatalf("vercel options %v", vopts)
	}
	docs := owner.must(201, "POST", base+"/deploy-targets", map[string]any{"integration_id": vc["id"], "target": "prj_1", "site_id": siteID})
	if res, err := svc.Check(context.Background(), ws, docs["id"].(string)); err != nil || res.Seen != 2 || res.Live != 1 {
		t.Fatalf("vercel check %v %v", res, err)
	}
	if d := owner.must(200, "GET", base+"/deploys?target_id="+docs["id"].(string)+"&state=failed", nil)["data"].([]any); len(d) != 1 {
		t.Fatalf("failed deploys %v", d)
	}

	// Pause, re-site, check now, stop watching.
	up := owner.must(200, "PATCH", base+"/deploy-targets/"+tid, map[string]any{"status": "paused", "site_id": nil})
	if up["status"] != "paused" || up["site_id"] != nil {
		t.Fatalf("update %v", up)
	}
	owner.must(202, "POST", base+"/deploy-targets/"+tid+"/check", nil)
	if l := owner.must(200, "GET", base+"/deploy-targets", nil)["data"].([]any); len(l) != 3 {
		t.Fatalf("targets %v", l)
	}
	owner.must(204, "DELETE", base+"/deploy-targets/"+tid, nil)
	owner.must(404, "GET", base+"/deploy-targets/"+tid, nil)

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
	}
	for _, want := range []string{"deploy_target.added", "deploy_target.updated", "deploy_target.removed", "deploy_target.check_requested", "deploy.detected", "deploy.failed", "deploys.checked"} {
		if !names[want] {
			t.Errorf("no %s event", want)
		}
	}
}
