package googleworkspace

import (
	"context"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestCreateWorkspaceUser(t *testing.T) {
	resetCache(t)
	ctx := context.Background()
	f := newUsersFake(t)
	cr := keylessCreds("test-only-refresh-create")
	u := c.WorkspaceUserCreate{Email: "Ana.Lee@Northpeak.example", GivenName: "Ana", FamilyName: "Lee", Password: "test-only-not-a-real-password", RecoveryEmail: "ops@northpeak.example"}

	// Allowing profile changes does not allow creating users.
	cr["manage_users"] = "yes"
	if _, err := (Connector{}).CreateWorkspaceUser(ctx, f.client(), cr, u); codeOf(err) != c.CodeWorkspaceCreateOff || len(f.calls) != 0 {
		t.Fatalf("connection without create_users: %v, calls %v", err, f.calls)
	}
	bad := keylessCreds("x")
	bad["create_users"] = "true"
	if _, err := checkCreds(bad); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("create_users other than yes: %v", err)
	}
	if err := (Connector{}).CheckWorkspaceCreate(ctx, f.client(), cr); codeOf(err) != c.CodeWorkspaceCreateOff || len(f.calls) != 0 {
		t.Fatalf("check without create_users: %v, calls %v", err, f.calls)
	}
	cr["create_users"] = "yes"
	// The check reads one user with the Directory user scope and changes nothing.
	if err := (Connector{}).CheckWorkspaceCreate(ctx, f.client(), cr); err != nil || strings.Join(f.calls, "\n") != "GET /admin/directory/v1/users" || f.scopes[len(f.scopes)-1] != userScope {
		t.Fatalf("check: %v, calls %v", err, f.calls)
	}
	f.calls = nil

	for name, in := range map[string]c.WorkspaceUserCreate{
		"address":  {Email: "not an address", GivenName: "Ana", FamilyName: "Lee", Password: u.Password},
		"name":     {Email: u.Email, GivenName: "", FamilyName: "Lee", Password: u.Password},
		"password": {Email: u.Email, GivenName: "Ana", FamilyName: "Lee", Password: "short"},
		"recovery": {Email: u.Email, GivenName: "Ana", FamilyName: "Lee", Password: u.Password, RecoveryEmail: "ana.lee@northpeak.example"},
	} {
		if _, err := (Connector{}).CreateWorkspaceUser(ctx, f.client(), cr, in); c.KindOf(err) != c.ErrInvalid || len(f.calls) != 0 {
			t.Fatalf("bad %s: %v, calls %v", name, err, f.calls)
		}
	}

	got, err := (Connector{}).CreateWorkspaceUser(ctx, f.client(), cr, u)
	if err != nil || !got.Created || got.ID != "test-user-1" {
		t.Fatalf("create %+v: %v", got, err)
	}
	if strings.Join(f.calls, "\n") != "POST /admin/directory/v1/users" {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
	// The administrator's token, with the Directory user scope only.
	if f.scopes[len(f.scopes)-1] != userScope || f.subjects[len(f.subjects)-1] != cr["delegated_admin"] {
		t.Fatalf("scope %q as %q", f.scopes[len(f.scopes)-1], f.subjects[len(f.subjects)-1])
	}
	body := f.bodies["POST /admin/directory/v1/users"]
	if body["primaryEmail"] != "ana.lee@northpeak.example" || body["password"] != u.Password || body["changePasswordAtNextLogin"] != false ||
		body["recoveryEmail"] != "ops@northpeak.example" || body["name"].(map[string]any)["familyName"] != "Lee" {
		t.Fatalf("body %v", body)
	}

	// A retry finds the user in this tenant and changes nothing.
	f.userTaken, f.calls = true, nil
	got, err = (Connector{}).CreateWorkspaceUser(ctx, f.client(), cr, u)
	if err != nil || got.Created || got.ID != "test-user-1" {
		t.Fatalf("retry %+v: %v", got, err)
	}
	if strings.Join(f.calls, "\n") != "POST /admin/directory/v1/users\nGET /admin/directory/v1/users/ana.lee@northpeak.example" {
		t.Fatalf("retry calls:\n%s", strings.Join(f.calls, "\n"))
	}

	// The same address in somebody else's tenant is refused.
	f.customer = "Cother999"
	if _, err = (Connector{}).CreateWorkspaceUser(ctx, f.client(), cr, u); codeOf(err) != c.CodeWorkspaceUserElsewhere {
		t.Fatalf("user of another tenant: %v", err)
	}
}
