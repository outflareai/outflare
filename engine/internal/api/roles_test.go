package api_test

import (
	"slices"
	"testing"
)

func joinAs(t *testing.T, by *client, ws, email string, body map[string]any) (*client, string) {
	t.Helper()
	body["email"] = email
	inv := by.must(201, "POST", "/v1/workspaces/"+ws+"/invites", body)
	c := &client{t: t, h: by.h}
	out := c.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Teammate", "password": "a long enough password"})
	return c, out["user_id"].(string)
}

func scopesOf(v map[string]any) []string {
	var out []string
	for _, s := range v["scopes"].([]any) {
		out = append(out, s.(string))
	}
	return out
}

func roleByID(t *testing.T, c *client, ws, id string) map[string]any {
	t.Helper()
	for _, r := range c.must(200, "GET", "/v1/workspaces/"+ws+"/roles", nil)["data"].([]any) {
		if r.(map[string]any)["id"] == id {
			return r.(map[string]any)
		}
	}
	t.Fatalf("role %s not listed", id)
	return nil
}

func TestAdminsDoEverythingButRemovePeople(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	admin, adminID := joinAs(t, owner, ws, "ada@acme.example", map[string]any{"role": "admin"})
	// Admins invite admins, see tokens and the audit log, and change settings.
	_, otherAdminID := joinAs(t, admin, ws, "abe@acme.example", map[string]any{"role": "admin"})
	admin.must(200, "GET", base+"/api-tokens", nil)
	admin.must(200, "GET", base+"/audit-events", nil)
	admin.must(200, "PATCH", base, map[string]any{"name": "Acme Outbound 2"})

	// They raise someone's role, but don't remove or demote anyone.
	_, memberID := joinAs(t, admin, ws, "mo@acme.example", map[string]any{"role": "member"})
	admin.must(200, "PATCH", base+"/members/"+memberID, map[string]any{"role": "admin"})
	admin.must(403, "PATCH", base+"/members/"+memberID, map[string]any{"role": "viewer"})
	admin.must(403, "DELETE", base+"/members/"+otherAdminID, nil)
	admin.must(403, "POST", base+"/invites", map[string]any{"email": "own@acme.example", "role": "owner"})

	// Owners remove people and invite owners.
	owner.must(204, "DELETE", base+"/members/"+otherAdminID, nil)
	owner.must(201, "POST", base+"/invites", map[string]any{"email": "co@acme.example", "role": "owner"})

	// An owner can give admins the Remove people switch.
	adminRole := roleByID(t, owner, ws, "admin")
	if slices.Contains(scopesOf(adminRole), "members.remove") || adminRole["name"] != "Admin" {
		t.Fatalf("admin default: %v", adminRole)
	}
	owner.must(200, "PATCH", base+"/roles/admin", map[string]any{"scopes": append(scopesOf(adminRole), "members.remove")})
	admin.must(204, "DELETE", base+"/members/"+memberID, nil)
	// Still never another owner.
	me := owner.must(200, "GET", "/v1/me", nil)
	ownerID := me["user"].(map[string]any)["id"].(string)
	admin.must(403, "DELETE", base+"/members/"+ownerID, nil)

	// Everyone can leave; the last owner can't.
	admin.must(204, "POST", base+"/leave", nil)
	admin.must(404, "GET", base, nil)
	_ = adminID
	owner.must(409, "POST", base+"/leave", nil)
}

func TestCustomRoles(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	// A writer: SEO and social only.
	role := owner.must(201, "POST", base+"/roles", map[string]any{"name": "Content writer", "description": "SEO and social", "scopes": []string{"seo.manage", "social.manage", "workspace.read"}})
	id := role["id"].(string)
	if role["brand_limitable"] != true {
		t.Fatalf("writer should be brand-limitable: %v", role)
	}
	owner.must(409, "POST", base+"/roles", map[string]any{"name": "content WRITER", "scopes": []string{}})
	owner.must(409, "POST", base+"/roles", map[string]any{"name": "Admin", "scopes": []string{}})
	owner.must(400, "POST", base+"/roles", map[string]any{"name": "Bad", "scopes": []string{"nope.read"}})
	owner.must(403, "PATCH", base+"/roles/owner", map[string]any{"name": "Boss"})

	writer, writerID := joinAs(t, owner, ws, "wren@acme.example", map[string]any{"role_id": id})
	got := writer.must(200, "GET", base, nil)
	if got["role_id"] != id || !slices.Contains(scopesOf(got), "seo.manage") || slices.Contains(scopesOf(got), "leads.read") {
		t.Fatalf("writer workspace view: %v", got)
	}
	writer.must(403, "GET", base+"/lead-lists", nil)
	members := owner.must(200, "GET", base+"/members", nil)["data"].([]any)
	if m := members[len(members)-1].(map[string]any); m["role_name"] != "Content writer" || m["role"] != "viewer" {
		t.Fatalf("member view: %v", m)
	}

	// Editing the role changes what its people can do straight away.
	owner.must(200, "PATCH", base+"/roles/"+id, map[string]any{"scopes": []string{"seo.manage", "social.manage", "workspace.read", "leads.read"}})
	writer.must(200, "GET", base+"/lead-lists", nil)

	// An admin can't make or edit a role stronger than Admin, or touch Owner.
	admin, _ := joinAs(t, owner, ws, "ada@acme.example", map[string]any{"role": "admin"})
	admin.must(403, "POST", base+"/roles", map[string]any{"name": "Remover", "scopes": []string{"members.remove"}})
	analyst := admin.must(201, "POST", base+"/roles", map[string]any{"name": "Analyst", "scopes": []string{"analytics.read", "workspace.read"}})
	// Narrowing a role people are on lowers them all, which needs Remove people.
	member := roleByID(t, owner, ws, "operator")
	admin.must(403, "PATCH", base+"/roles/admin", map[string]any{"scopes": []string{"workspace.read"}})
	admin.must(403, "PATCH", base+"/roles/"+id, map[string]any{"scopes": []string{"workspace.read"}})
	admin.must(200, "PATCH", base+"/roles/"+id, map[string]any{"scopes": append(scopesOf(roleByID(t, owner, ws, id)), "analytics.read")})
	admin.must(200, "PATCH", base+"/roles/"+analyst["id"].(string), map[string]any{"scopes": []string{"workspace.read"}})
	owner.must(200, "PATCH", base+"/roles/admin", map[string]any{"name": "Team"})
	if r := roleByID(t, owner, ws, "admin"); r["name"] != "Team" || r["edited"] != true {
		t.Fatalf("renamed admin: %v", r)
	}

	// Members can connect tools by default; it can be switched off.
	member = roleByID(t, owner, ws, "operator")
	if member["name"] != "Member" || !slices.Contains(scopesOf(member), "integrations.manage") {
		t.Fatalf("member default: %v", member)
	}

	// A role in use moves its people before it goes.
	owner.must(409, "DELETE", base+"/roles/"+id, nil)
	owner.must(204, "DELETE", base+"/roles/"+id+"?move_to=viewer", nil)
	members = owner.must(200, "GET", base+"/members", nil)["data"].([]any)
	for _, m := range members {
		if m := m.(map[string]any); m["user_id"] == writerID && (m["role_id"] != nil || m["role_name"] != "Viewer") {
			t.Fatalf("moved member: %v", m)
		}
	}
	// A starting role resets to its defaults.
	owner.must(204, "DELETE", base+"/roles/admin", nil)
	if r := roleByID(t, owner, ws, "admin"); r["name"] != "Admin" || r["edited"] != false {
		t.Fatalf("reset admin: %v", r)
	}
}

func TestRoleBrandsAndTokens(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "https://repocket.com"})["id"].(string)

	role := owner.must(201, "POST", base+"/roles", map[string]any{"name": "Brand helper", "scopes": []string{"workspace.read", "leads.read", "leads.manage"}})
	id := role["id"].(string)
	helper, _ := joinAs(t, owner, ws, "hal@acme.example", map[string]any{"role_id": id, "brand_ids": []string{brand}})
	if got := helper.must(200, "GET", base, nil)["brand_ids"].([]any); len(got) != 1 {
		t.Fatalf("brands: %v", got)
	}
	// A role with workspace-wide permissions can't hold brand-limited people.
	owner.must(400, "PATCH", base+"/roles/"+id, map[string]any{"scopes": []string{"workspace.read", "audit.read"}})

	// Tokens never hold the person-only permissions, and follow role edits.
	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "x", "scopes": []string{"members.remove"}})
	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "x", "scopes": []string{"roles.manage"}})
	_ = helper

	// Editing a role trims the tokens its people made.
	maker := owner.must(201, "POST", base+"/roles", map[string]any{"name": "Integrator", "scopes": []string{"workspace.read", "tokens.manage", "leads.manage"}})
	mid := maker["id"].(string)
	dev, _ := joinAs(t, owner, ws, "dev@acme.example", map[string]any{"role_id": mid})
	tok := dev.must(201, "POST", base+"/api-tokens", map[string]any{"name": "sync", "scopes": []string{"leads.manage"}})
	bot := &client{t: t, h: h, bearer: tok["token"].(string)}
	bot.must(200, "GET", base+"/lead-lists", nil)
	owner.must(200, "PATCH", base+"/roles/"+mid, map[string]any{"scopes": []string{"workspace.read", "tokens.manage"}})
	bot.must(401, "GET", base+"/lead-lists", nil)
}
