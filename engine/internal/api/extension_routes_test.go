package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

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

func TestCompanyDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Acme.example/about?x=1": "acme.example", "acme.example:443": "acme.example", "localhost": "", "not a domain": "", "": "",
	} {
		if got := customers.CompanyDomain(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

// An extension starts a mission for the caller, such as a loop's brief: a
// draft that waits for a person, through the same checks as the API.
func TestExtensionStartsMission(t *testing.T) {
	got := track.Capture(t)
	var e *env
	e = freshEnvWith(t, func(o *api.Options) {
		o.WorkspaceRoutes = []api.WorkspaceRoute{{
			Pattern: "POST /v1/workspaces/{ws}/loop/start", Scope: "missions.read",
			Handler: func(w http.ResponseWriter, r *http.Request, c *api.Caller) {
				var in struct {
					Name   string `json:"name"`
					Past   bool   `json:"past"`
					Budget int64  `json:"budget"`
				}
				if !c.Decode(w, r, &in) {
					return
				}
				d := api.MissionDraft{Name: in.Name, Goal: "Ten live links a month", Instructions: "Keep the link playbooks running.", Plan: "1. Read the results.",
					Mode: "approve_writes", MaxSpendCents: &in.Budget}
				if in.Past {
					at := time.Now().Add(-time.Hour)
					d.EndsAt = &at
				}
				ctx := r.Context()
				var id string
				err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
					var err error
					id, err = c.StartMission(ctx, tx, d)
					return err
				})
				switch {
				case errors.Is(err, api.ErrMissionInvalid):
					api.WriteError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
				case errors.Is(err, api.ErrMissionDenied):
					api.WriteError(w, r, http.StatusForbidden, "forbidden", err.Error())
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

	id := owner.must(201, "POST", base+"/loop/start", map[string]any{"name": "Link autopilot", "budget": 5000})["mission_id"].(string)
	m := owner.must(200, "GET", base+"/missions/"+id, nil)["mission"].(map[string]any)
	if m["status"] != "draft" || m["name"] != "Link autopilot" || m["mode"] != "approve_writes" || m["limits"].(map[string]any)["max_spend_cents"].(float64) != 5000 {
		t.Fatalf("mission: %v", m)
	}
	if res := owner.must(400, "POST", base+"/loop/start", map[string]any{"name": " "}); res["error"].(map[string]any)["message"] != "name is required, up to 200 characters" {
		t.Fatalf("no name: %v", res)
	}
	owner.must(400, "POST", base+"/loop/start", map[string]any{"name": "Late", "past": true})
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"missions.read"}})
	reader := &client{t: t, h: e.h, bearer: out["token"].(string)}
	reader.must(403, "POST", base+"/loop/start", map[string]any{"name": "Not mine"})
	if n := len(named(got(), "mission.created")); n != 1 {
		t.Fatalf("mission.created: %d", n)
	}
}
