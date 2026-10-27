package api_test

import "testing"

func TestAgencyClientsMembersAndTokens(t *testing.T) {
	h := freshServer(t)
	owner, ownWS := setupOwner(t, h)

	// Sam has an account through another workspace, nothing of the agency.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Scratch"})["id"].(string)
	inv := owner.must(201, "POST", "/v1/workspaces/"+other+"/invites", map[string]string{"email": "sam@example.com", "role": "viewer"})
	sam := &client{t: t, h: h}
	sam.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Sam", "password": "sams long password"})

	// An agency, bringing the owner's first workspace in as a client.
	ag := owner.must(201, "POST", "/v1/agencies", map[string]any{"name": "Northwind Agency", "workspace_ids": []string{ownWS}})
	base := "/v1/agencies/" + ag["id"].(string)
	if ag["clients"] != 1.0 || ag["role"] != "owner" {
		t.Fatalf("agency: %v", ag)
	}
	sam.must(404, "GET", base, nil)
	sam.must(400, "POST", "/v1/agencies", map[string]any{"name": "Grab", "workspace_ids": []string{ownWS}})

	// A new client has no direct members; the agency's people get in.
	contoso := owner.must(201, "POST", base+"/clients", map[string]any{"name": "Contoso"})
	cws := "/v1/workspaces/" + contoso["id"].(string)
	if members := owner.must(200, "GET", cws+"/members", nil)["data"].([]any); len(members) != 0 {
		t.Fatalf("client members: %v", members)
	}
	sam.must(404, "GET", cws, nil)
	owner.must(200, "POST", base+"/members", map[string]string{"email": "Sam@example.com", "role": "operator"})
	if got := sam.must(200, "GET", cws, nil); got["role"] != "operator" {
		t.Fatalf("agency role: %v", got)
	}
	sam.must(200, "GET", cws+"/leads", nil)
	sam.must(403, "POST", cws+"/api-tokens", map[string]any{"name": "x", "scopes": []string{"leads.read"}})
	sam.must(403, "PATCH", base, map[string]any{"brand_name": "Mine"})
	me := sam.must(200, "GET", "/v1/me", nil)
	if len(me["agencies"].([]any)) != 1 || len(me["workspaces"].([]any)) != 3 {
		t.Fatalf("me: %v", me)
	}

	// White label shows on every client workspace.
	owner.must(400, "PATCH", base, map[string]any{"logo_url": "http://insecure.example/logo.png"})
	owner.must(400, "PATCH", base, map[string]any{"accent_color": "blue"})
	owner.must(200, "PATCH", base, map[string]any{"brand_name": "Northwind", "accent_color": "#2563eb", "support_email": "help@northwind.example"})
	if b := sam.must(200, "GET", cws, nil)["brand"].(map[string]any); b["name"] != "Northwind" || b["accent_color"] != "#2563eb" {
		t.Fatalf("brand: %v", b)
	}

	// Cross-client view.
	clients := sam.must(200, "GET", base+"/clients", nil)["data"].([]any)
	if len(clients) != 2 {
		t.Fatalf("clients: %v", clients)
	}

	// One agency token works in every client, and nowhere else.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"workspace.read", "leads.read", "leads.manage"}, "can_create_clients": true})
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	agent.must(200, "GET", cws+"/leads", nil)
	agent.must(200, "GET", "/v1/workspaces/"+ownWS+"/leads", nil)
	agent.must(404, "GET", "/v1/workspaces/"+other+"/leads", nil)
	agent.must(403, "GET", cws+"/sequences", nil)
	agent.must(200, "GET", base+"/clients", nil)
	agent.must(403, "GET", base+"/members", nil)
	created := agent.must(201, "POST", base+"/clients", map[string]any{"name": "Fabrikam"})
	agent.must(200, "GET", "/v1/workspaces/"+created["id"].(string), nil)
	owner.must(204, "DELETE", base+"/api-tokens/"+tok["api_token"].(map[string]any)["id"].(string), nil)
	agent.must(401, "GET", cws+"/leads", nil)

	// Roles: an operator can't add people; the last owner can't leave.
	sam.must(403, "POST", base+"/members", map[string]string{"email": "jack@example.com", "role": "viewer"})
	me = owner.must(200, "GET", "/v1/me", nil)
	jack := me["user"].(map[string]any)["id"].(string)
	owner.must(400, "DELETE", base+"/members/"+jack, nil)

	// Taking a client out: data stays, agency access goes, and since nobody
	// owned it directly, the person removing it becomes its owner.
	owner.must(204, "DELETE", base+"/clients/"+contoso["id"].(string), nil)
	sam.must(404, "GET", cws, nil)
	if got := owner.must(200, "GET", cws, nil); got["role"] != "owner" || got["brand"] != nil {
		t.Fatalf("after removal: %v", got)
	}
	if ev := owner.must(200, "GET", base+"/events", nil)["data"].([]any); len(ev) < 6 {
		t.Fatalf("events: %v", ev)
	}
}

// An agency token reaches every client, so it stops when its maker leaves
// the agency.
func TestAgencyTokensFollowTheirMaker(t *testing.T) {
	h := freshServer(t)
	owner, ownWS := setupOwner(t, h)
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Scratch"})["id"].(string)
	inv := owner.must(201, "POST", "/v1/workspaces/"+other+"/invites", map[string]string{"email": "sam@example.com", "role": "viewer"})
	sam := &client{t: t, h: h}
	samID := sam.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Sam", "password": "sams long password"})["user_id"].(string)

	ag := owner.must(201, "POST", "/v1/agencies", map[string]any{"name": "Agency", "workspace_ids": []string{ownWS}})
	base := "/v1/agencies/" + ag["id"].(string)
	owner.must(200, "POST", base+"/members", map[string]string{"email": "sam@example.com", "role": "admin"})
	tok := sam.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Sam's", "scopes": []string{"workspace.read", "members.manage"}})
	bot := &client{t: t, h: h, bearer: tok["token"].(string)}
	bot.must(200, "GET", "/v1/workspaces/"+ownWS, nil)

	// A lower role trims the token; leaving the agency stops it.
	owner.must(200, "PATCH", base+"/members/"+samID, map[string]string{"role": "viewer"})
	bot.must(200, "GET", "/v1/workspaces/"+ownWS, nil)
	bot.must(403, "GET", "/v1/workspaces/"+ownWS+"/invites", nil)
	owner.must(204, "DELETE", base+"/members/"+samID, nil)
	bot.must(401, "GET", "/v1/workspaces/"+ownWS, nil)
}
