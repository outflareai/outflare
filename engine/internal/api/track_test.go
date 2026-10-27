package api_test

import (
	"net/http/httptest"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func named(events []track.Event, name string) []track.Event {
	var out []track.Event
	for _, e := range events {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func TestEveryActionAndErrorIsAnEvent(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)

	ev := got()
	up := named(ev, "user.signed_up")
	if len(up) != 1 || up[0].UserID == "" || up[0].Props["via"] != "setup" || up[0].Props["method"] != "password" {
		t.Fatalf("signed up: %+v", up)
	}
	userID := up[0].UserID
	if in := named(ev, "user.signed_in"); len(in) != 1 || in[0].UserID != userID {
		t.Fatalf("signed in: %+v", in)
	}
	if c := named(ev, "workspace.created"); len(c) != 1 || c[0].WorkspaceID != ws {
		t.Fatalf("workspace.created: %+v", c)
	}

	// A write from the web app: its action and the call, tied to the person.
	base := "/v1/workspaces/" + ws
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "agent", "scopes": []string{"leads.read"}})
	created := named(got(), "token.created")
	if len(created) != 1 || created[0].UserID != userID || created[0].Surface != "web" || created[0].WorkspaceID != ws {
		t.Fatalf("token.created: %+v", created)
	}
	calls := named(got(), "api.called")
	last := calls[len(calls)-1]
	if last.Props["operation"] != "createAPIToken" || last.Outcome != track.Success || last.UserID != userID {
		t.Fatalf("api.called: %+v", last)
	}

	// The web app's own successful reads are not calls; its pageviews say
	// what people looked at.
	n := len(named(got(), "api.called"))
	owner.must(200, "GET", base+"/leads", nil)
	if len(named(got(), "api.called")) != n {
		t.Fatal("a web read was recorded as a call")
	}

	// An agent through MCP: the token's maker, the tool and the agent's app.
	req := httptest.NewRequest("GET", base+"/leads", nil)
	req.Header.Set("Authorization", "Bearer "+tok["token"].(string))
	req.Header.Set("Outflare-Client", "mcp/0.1.0")
	req.Header.Set("Outflare-Tool", "list_leads")
	req.Header.Set("Outflare-Agent", "claude-ai")
	h.ServeHTTP(httptest.NewRecorder(), req)
	calls = named(got(), "api.called")
	mcp := calls[len(calls)-1]
	if mcp.Surface != "mcp" || mcp.Props["mcp_tool"] != "list_leads" || mcp.Props["agent"] != "claude-ai" ||
		mcp.UserID != userID || mcp.ActorType != "api_token" || mcp.WorkspaceID != ws {
		t.Fatalf("mcp call: %+v", mcp)
	}

	// A refused request: the call carries the error code, and nothing it
	// would have recorded is sent.
	before := len(named(got(), "token.created"))
	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "", "scopes": []string{"nope"}})
	calls = named(got(), "api.called")
	bad := calls[len(calls)-1]
	if bad.Outcome != track.Failure || bad.ErrorCode == "" || bad.Props["status"] != 400 {
		t.Fatalf("refused call: %+v", bad)
	}
	if len(named(got(), "token.created")) != before {
		t.Fatal("a refused request's action was sent")
	}

	// Members: removing someone is an event.
	inv := owner.must(201, "POST", base+"/invites", map[string]string{"email": "sam@example.com", "role": "operator"})
	sam := &client{t: t, h: h}
	sam.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Sam", "password": "long enough password"})
	samUp := named(got(), "user.signed_up")
	if len(samUp) != 2 || samUp[1].Props["via"] != "invite" {
		t.Fatalf("invitee signed up: %+v", samUp)
	}
	owner.must(204, "DELETE", base+"/members/"+samUp[1].UserID, nil)
	if r := named(got(), "member.removed"); len(r) != 1 || r[0].UserID != userID {
		t.Fatalf("member.removed: %+v", r)
	}

	owner.must(204, "POST", "/v1/auth/logout", nil)
	if out := named(got(), "user.signed_out"); len(out) != 1 || out[0].UserID != userID {
		t.Fatalf("signed out: %+v", out)
	}
	owner.must(401, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "wrong password!!"})
	calls = named(got(), "api.called")
	if l := calls[len(calls)-1]; l.Props["operation"] != "login" || l.Outcome != track.Failure {
		t.Fatalf("failed sign-in: %+v", l)
	}
}
