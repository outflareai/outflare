package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/track"
)

// An extension's workspace route gets the engine's workspace check, and
// can use the workspace's own connections and company records.
func TestExtensionWorkspaceRoutes(t *testing.T) {
	got := track.Capture(t)
	var e *env
	e = freshEnvWith(t, func(o *api.Options) {
		o.WorkspaceRoutes = []api.WorkspaceRoute{{
			Pattern: "POST /v1/workspaces/{ws}/finder/runs", Scope: "leads.manage",
			Handler: func(w http.ResponseWriter, r *http.Request, c *api.Caller) {
				var in struct {
					Query string `json:"query"`
				}
				if !c.Decode(w, r, &in) {
					return
				}
				ctx := r.Context()
				o, err := e.integrations.OpenCapable(ctx, e.pool, c.WorkspaceID, "", connectors.CapWebSearch)
				if err == connectors.ErrNoCapable {
					api.WriteError(w, r, http.StatusConflict, "connection_required", "connect Geonode to search the web")
					return
				}
				if err != nil {
					c.Internal(w, r, err)
					return
				}
				res, err := o.Connector.(connectors.WebSearcher).WebSearch(ctx, o.Client, o.Creds, connectors.WebQuery{Query: in.Query})
				if err != nil {
					c.Internal(w, r, err)
					return
				}
				tx, err := e.pool.Begin(ctx)
				if err != nil {
					c.Internal(w, r, err)
					return
				}
				defer tx.Rollback(ctx) //nolint:errcheck
				var list []customers.Company
				for _, x := range res.Results {
					list = append(list, customers.Company{Domain: x.URL, Name: x.Title})
				}
				ids, err := customers.EnsureCompanies(ctx, tx, c.WorkspaceID, nil, "finder", list)
				if err == nil {
					err = c.Record(ctx, tx, "finder.run_started", "workspace", c.WorkspaceID, map[string]any{"found": len(ids)})
				}
				if err == nil {
					err = tx.Commit(ctx)
				}
				if err != nil {
					c.Internal(w, r, err)
					return
				}
				api.WriteJSON(w, http.StatusCreated, map[string]any{"companies": ids, "sees_shared": c.Sees(nil), "user": c.UserID != ""})
			},
		}}
	})
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	owner.must(409, "POST", base+"/finder/runs", map[string]any{"query": "payroll"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	out := owner.must(201, "POST", base+"/finder/runs", map[string]any{"query": "payroll"})
	acme := out["companies"].(map[string]any)["acme.example"]
	if acme == nil || out["sees_shared"] != true || out["user"] != true {
		t.Fatalf("run = %v", out)
	}
	// The same domain again is the same company.
	if again := owner.must(201, "POST", base+"/finder/runs", map[string]any{"query": "payroll"}); again["companies"].(map[string]any)["acme.example"] != acme {
		t.Fatalf("again = %v", again)
	}
	rec := owner.must(200, "GET", base+"/records/"+acme.(string), nil)
	if rec["name"] != "Acme" || rec["origin"] != "prospect" {
		t.Fatalf("record = %v", rec)
	}
	owner.must(400, "POST", base+"/finder/runs", map[string]any{"nope": 1})

	// Others' workspaces are not found, and scopes hold.
	(&client{t: t, h: e.h}).must(401, "POST", base+"/finder/runs", map[string]any{"query": "x"})
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"leads.read"}})["token"].(string)
	(&client{t: t, h: e.h, bearer: tok}).must(403, "POST", base+"/finder/runs", map[string]any{"query": "x"})
	owner.must(404, "POST", "/v1/workspaces/00000000-0000-0000-0000-000000000000/finder/runs", map[string]any{"query": "x"})

	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'finder.run_started' AND actor_type = 'human'`, ws).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit = %d %v", n, err)
	}
	seen := false
	for _, ev := range got() {
		seen = seen || ev.Name == "finder.run_started"
	}
	if !seen {
		t.Fatal("no product event")
	}
}

// An extension's brand route finds the brand's workspace, keeps callers
// to brands they can see, and tells the handler the brand.
func TestExtensionBrandRoutes(t *testing.T) {
	e := freshEnvWith(t, func(o *api.Options) {
		o.WorkspaceRoutes = []api.WorkspaceRoute{{
			Pattern: "GET /v1/brands/{brand}/finder/where", Scope: "leads.read", Brand: true,
			Handler: func(w http.ResponseWriter, r *http.Request, c *api.Caller) {
				api.WriteJSON(w, http.StatusOK, map[string]any{"brand": c.Brand, "workspace": c.WorkspaceID})
			},
		}}
	})
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)

	out := owner.must(200, "GET", "/v1/brands/"+rp+"/finder/where", nil)
	if out["brand"] != rp || out["workspace"] != ws {
		t.Fatalf("where = %v", out)
	}
	owner.must(404, "GET", "/v1/brands/00000000-0000-0000-0000-000000000000/finder/where", nil)
	owner.must(404, "GET", "/v1/brands/nope/finder/where", nil)
	(&client{t: t, h: e.h}).must(401, "GET", "/v1/brands/"+rp+"/finder/where", nil)

	// A token kept to Geonode doesn't find Repocket, and scopes hold.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Geonode agent", "scopes": []string{"leads.read"}, "brand": "geonode"})["token"].(string)
	agent := &client{t: t, h: e.h, bearer: tok}
	if out := agent.must(200, "GET", "/v1/brands/"+geo+"/finder/where", nil); out["brand"] != geo {
		t.Fatalf("agent = %v", out)
	}
	agent.must(404, "GET", "/v1/brands/"+rp+"/finder/where", nil)
	noScope := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Mail", "scopes": []string{"mailboxes.read"}})["token"].(string)
	(&client{t: t, h: e.h, bearer: noScope}).must(403, "GET", "/v1/brands/"+rp+"/finder/where", nil)
}

func TestCompanyDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Acme.example/about?x=1": "acme.example", "acme.example:443": "acme.example", "localhost": "", "not a domain": "", "": "",
	} {
		if got := customers.CompanyDomain(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

// An extension can no longer start a mission: StartMission answers
// ErrMissionRetired, and nothing is recorded. Loops start agent runs
// (TestExtensionStartsAgentRun).
func TestExtensionMissionsRetired(t *testing.T) {
	got := track.Capture(t)
	var e *env
	e = freshEnvWith(t, func(o *api.Options) {
		o.WorkspaceRoutes = []api.WorkspaceRoute{{
			Pattern: "POST /v1/workspaces/{ws}/loop/start", Scope: "missions.read",
			Handler: func(w http.ResponseWriter, r *http.Request, c *api.Caller) {
				d := api.MissionDraft{Name: "Link autopilot", Goal: "Ten live links a month", Mode: "approve_writes"}
				ctx := r.Context()
				var id string
				err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
					var err error
					id, err = c.StartMission(ctx, tx, d)
					return err
				})
				switch {
				case errors.Is(err, api.ErrMissionRetired):
					api.WriteError(w, r, http.StatusGone, "mission_retired", err.Error())
				case err != nil:
					c.Internal(w, r, err)
				default:
					api.WriteJSON(w, http.StatusCreated, map[string]any{"mission_id": id})
				}
			},
		}}
	})
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	res := owner.must(410, "POST", base+"/loop/start", map[string]any{})
	if msg := res["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "StartAgentRun") {
		t.Fatalf("retired: %v", res)
	}
	if l := owner.must(200, "GET", base+"/missions", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("missions: %v", l)
	}
	if n := len(named(got(), "mission.created")); n != 0 {
		t.Fatalf("mission.created: %d", n)
	}
}

// An extension starts an agent run for the caller: a loop carried out by
// Outflare's agent on the action layer, with its token, its grants from the
// mode and its first turn due at once.
func TestExtensionStartsAgentRun(t *testing.T) {
	got := track.Capture(t)
	var e *env
	e = freshEnvWith(t, func(o *api.Options) {
		o.WorkspaceRoutes = []api.WorkspaceRoute{{
			Pattern: "POST /v1/workspaces/{ws}/loop/run", Scope: "agents.read",
			Handler: func(w http.ResponseWriter, r *http.Request, c *api.Caller) {
				var in struct {
					Name   string `json:"name"`
					Mode   string `json:"mode"`
					Budget *int64 `json:"budget"`
				}
				if !c.Decode(w, r, &in) {
					return
				}
				d := api.AgentRunDraft{Name: in.Name, Goal: "Ten live links a month", Instructions: "Keep the link playbooks running.", Plan: "1. Read the results.",
					Mode: in.Mode, MaxSpendCents: in.Budget, Note: "Started by the link autopilot."}
				ctx := r.Context()
				var id string
				err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
					var err error
					id, err = c.StartAgentRun(ctx, tx, d)
					return err
				})
				switch {
				case errors.Is(err, api.ErrAgentRunInvalid):
					api.WriteError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
				case errors.Is(err, api.ErrAgentRunDenied):
					api.WriteError(w, r, http.StatusForbidden, "forbidden", err.Error())
				case err != nil:
					c.Internal(w, r, err)
				default:
					api.WriteJSON(w, http.StatusCreated, map[string]any{"run_id": id})
				}
			},
		}}
	})
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	id := owner.must(201, "POST", base+"/loop/run", map[string]any{"name": "Link autopilot"})["run_id"].(string)
	run := owner.must(200, "GET", base+"/agent-runs/"+id, nil)["run"].(map[string]any)
	if run["status"] != "running" || run["name"] != "Link autopilot" || run["mode"] != "copilot" || run["token_id"] == nil || run["next_turn_at"] == nil {
		t.Fatalf("run: %v", run)
	}
	updates := owner.must(200, "GET", base+"/agent-runs/"+id+"/updates", nil)["data"].([]any)
	if len(updates) != 1 || updates[0].(map[string]any)["body"] != "Started by the link autopilot." {
		t.Fatalf("updates: %v", updates)
	}
	budget := int64(5000)
	auto := owner.must(201, "POST", base+"/loop/run", map[string]any{"name": "Autonomous", "mode": "autonomous", "budget": budget})["run_id"].(string)
	autoToken := owner.must(200, "GET", base+"/agent-runs/"+auto, nil)["run"].(map[string]any)["token_id"].(string)
	grants := owner.must(200, "GET", base+"/action-grants?token_id="+autoToken, nil)["data"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["mode"] != "run" || grants[0].(map[string]any)["total_spend_cents"] != 5000.0 {
		t.Fatalf("autonomous grants: %v", grants)
	}
	if res := owner.must(400, "POST", base+"/loop/run", map[string]any{"name": " "}); res["error"].(map[string]any)["message"] != "name is required" {
		t.Fatalf("no name: %v", res)
	}
	owner.must(400, "POST", base+"/loop/run", map[string]any{"name": "Wild", "mode": "wild"})
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"agents.read"}})
	reader := &client{t: t, h: e.h, bearer: out["token"].(string)}
	reader.must(403, "POST", base+"/loop/run", map[string]any{"name": "Not mine"})
	if n := len(named(got(), "agent_run.created")); n != 2 {
		t.Fatalf("agent_run.created: %d", n)
	}
}
