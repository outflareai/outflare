package googleworkspace

import (
	"context"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Creating users, only when the connection opted in (create_users = "yes")
// and the tenant authorized the Directory user scope for the service account.
// A new user takes one of the tenant's own licences. The password is sent to
// Google once, in the create call, and is never stored, logged or returned.
// Nothing here deletes or suspends a user, changes an existing user's
// password, or buys a licence.

var _ c.WorkspaceUserCreator = Connector{}

func createOn(cr c.Creds) bool { return strings.TrimSpace(cr["create_users"]) == "yes" }

func (Connector) CreateWorkspaceUser(ctx context.Context, cl *c.Client, cr c.Creds, u c.WorkspaceUserCreate) (c.WorkspaceUserCreated, error) {
	out := c.WorkspaceUserCreated{}
	admin, err := checkCreds(cr)
	if err != nil {
		return out, err
	}
	if !createOn(cr) {
		return out, c.Codedf(c.CodeWorkspaceCreateOff, c.ErrAuth, "this Google Workspace connection may not create users; turn on creating users in its settings and authorize the Directory user scope for its service account")
	}
	email := strings.ToLower(strings.TrimSpace(u.Email))
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || strings.Count(email, "@") != 1 || !validDomain(email[strings.Index(email, "@")+1:]) {
		return out, c.Errf(c.ErrInvalid, "provide the new mailbox's address")
	}
	given, family := strings.TrimSpace(u.GivenName), strings.TrimSpace(u.FamilyName)
	for _, n := range []string{given, family} {
		if n == "" || len(n) > 60 || strings.ContainsAny(n, "\r\n<>") {
			return out, c.Errf(c.ErrInvalid, "a first and last name are 1 to 60 characters each")
		}
	}
	// Google's own limits: 8 to 100 characters.
	if len(u.Password) < 8 || len(u.Password) > 100 {
		return out, c.Errf(c.ErrInvalid, "a password is 8 to 100 characters")
	}
	recovery := strings.ToLower(strings.TrimSpace(u.RecoveryEmail))
	if recovery != "" {
		if p, err := mail.ParseAddress(recovery); err != nil || p.Address != recovery || recovery == email {
			return out, c.Errf(c.ErrInvalid, "a recovery address is another valid email address")
		}
	}
	bearer, key, err := delegated(ctx, cl, cr, []string{userScope}, admin)
	if err != nil {
		return out, createDenied(err)
	}
	body := map[string]any{
		"primaryEmail":              email,
		"name":                      map[string]string{"givenName": given, "familyName": family},
		"password":                  u.Password,
		"changePasswordAtNextLogin": false,
	}
	if recovery != "" {
		body["recoveryEmail"] = recovery
	}
	var made struct {
		ID         string `json:"id"`
		CustomerID string `json:"customerId"`
	}
	status, err := send(ctx, cl, bearer, key, http.MethodPost, cl.Base(ID, "https://admin.googleapis.com")+"/admin/directory/v1/users", body, &made)
	if status == http.StatusConflict {
		// The address exists. A retry of our own create finds the user in this
		// tenant and changes nothing; anything else is somebody else's user.
		var who struct {
			ID         string `json:"id"`
			CustomerID string `json:"customerId"`
		}
		if err = read(ctx, cl, bearer, key, "users/"+url.PathEscape(email)+"?fields=id,customerId", &who); err != nil || who.CustomerID != cr["customer_id"] || who.ID == "" {
			return out, c.Codedf(c.CodeWorkspaceUserElsewhere, c.ErrInvalid, "this address already belongs to a user outside the connected Google Workspace")
		}
		out.ID = who.ID
		return out, nil
	}
	if err != nil {
		return out, err
	}
	// Google creates users only in the administrator's own tenant; a different
	// answer means the connection's customer ID is wrong, so say so.
	if made.ID == "" || (made.CustomerID != "" && made.CustomerID != cr["customer_id"]) {
		return out, c.Errf(c.ErrUnavailable, "Google Workspace created the user outside the connected tenant; check the connection's customer ID")
	}
	out.ID, out.Created = made.ID, true
	return out, nil
}

// createDenied explains a refused token in terms of the scope creating needs.
func createDenied(err error) error {
	if codeOfErr(err) == c.CodeWorkspaceDelegationDenied {
		return c.Codedf(c.CodeWorkspaceDelegationDenied, c.ErrAuth, "Google refused the Directory user scope: in Google Admin, authorize the service account's client ID for it as well")
	}
	return err
}

var _ c.WorkspaceCreateChecker = Connector{}

// CheckWorkspaceCreate says whether this connection could create a user now,
// without creating or changing anything: creating is turned on, Google gives
// the administrator a token for the Directory user scope, and that token can
// read the connected tenant's users. It returns nil when it could.
func (Connector) CheckWorkspaceCreate(ctx context.Context, cl *c.Client, cr c.Creds) error {
	admin, err := checkCreds(cr)
	if err != nil {
		return err
	}
	if !createOn(cr) {
		return c.Codedf(c.CodeWorkspaceCreateOff, c.ErrAuth, "this Google Workspace connection may not create users; turn on creating users in its settings and authorize the Directory user scope for its service account")
	}
	bearer, key, err := delegated(ctx, cl, cr, []string{userScope}, admin)
	if err != nil {
		return createDenied(err)
	}
	var page struct {
		Users []struct {
			CustomerID string `json:"customerId"`
		} `json:"users"`
	}
	if err = read(ctx, cl, bearer, key, "users?customer="+url.QueryEscape(cr["customer_id"])+"&maxResults=1&fields=users(customerId)", &page); err != nil {
		return err
	}
	return nil
}
