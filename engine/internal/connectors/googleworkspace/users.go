package googleworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Profile changes for existing users, only when the connection opted in
// (manage_users = "yes") and the tenant authorized the write scopes for the
// service account. The administrator's token changes names and photos; each
// user's own delegated token (Gmail settings) changes that user's signature.
// Nothing here creates, deletes or suspends a user or touches a password.

const (
	userScope      = "https://www.googleapis.com/auth/admin.directory.user"
	signatureScope = "https://www.googleapis.com/auth/gmail.settings.basic"
	maxPhoto       = 512 << 10
	maxSignature   = 10 << 10
)

// Result states for each part of an update.
const (
	PartUpdated   = "updated"
	PartFailed    = "failed"
	PartUntouched = ""
)

var _ c.WorkspaceUserManager = Connector{}

func writesOn(cr c.Creds) bool { return strings.TrimSpace(cr["manage_users"]) == "yes" }

func (Connector) UpdateWorkspaceUser(ctx context.Context, cl *c.Client, cr c.Creds, u c.WorkspaceUserUpdate) (c.WorkspaceUserResult, error) {
	out := c.WorkspaceUserResult{}
	admin, err := checkCreds(cr)
	if err != nil {
		return out, err
	}
	if !writesOn(cr) {
		return out, c.Codedf(c.CodeWorkspaceWritesOff, c.ErrAuth, "this Google Workspace connection is read-only; turn on profile changes in its settings and authorize the user and Gmail settings scopes for its service account")
	}
	email := strings.ToLower(strings.TrimSpace(u.Email))
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || strings.Count(email, "@") != 1 || !validDomain(email[strings.Index(email, "@")+1:]) {
		return out, c.Errf(c.ErrInvalid, "provide the mailbox's address")
	}
	if u.Photo != nil {
		if len(u.Photo) == 0 || len(u.Photo) > maxPhoto || (u.PhotoType != "JPEG" && u.PhotoType != "PNG") {
			return out, c.Errf(c.ErrInvalid, "a photo is a JPEG or PNG of at most 512 KB")
		}
	}
	if u.Signature != nil && len(*u.Signature) > maxSignature {
		return out, c.Errf(c.ErrInvalid, "a signature is at most 10 KB")
	}
	for _, n := range []*string{u.GivenName, u.FamilyName} {
		if n != nil && (strings.TrimSpace(*n) == "" || len(*n) > 60 || strings.ContainsAny(*n, "\r\n<>")) {
			return out, c.Errf(c.ErrInvalid, "a name is 1 to 60 characters")
		}
	}
	if u.GivenName != nil || u.FamilyName != nil || u.Photo != nil {
		bearer, key, err := delegated(ctx, cl, cr, []string{userScope}, admin)
		if err != nil {
			return out, writeDenied(err)
		}
		// Only a user of this connection's own tenant is ever changed.
		var who struct {
			CustomerID string `json:"customerId"`
		}
		if err = read(ctx, cl, bearer, key, "users/"+url.PathEscape(email)+"?fields=customerId", &who); err != nil {
			return out, err
		}
		if who.CustomerID != cr["customer_id"] {
			return out, c.Errf(c.ErrInvalid, "this mailbox is not a user of the connected Google Workspace")
		}
		if u.GivenName != nil || u.FamilyName != nil {
			name := map[string]string{}
			if u.GivenName != nil {
				name["givenName"] = strings.TrimSpace(*u.GivenName)
			}
			if u.FamilyName != nil {
				name["familyName"] = strings.TrimSpace(*u.FamilyName)
			}
			out.Name = part(write(ctx, cl, bearer, key, http.MethodPatch, cl.Base(ID, "https://admin.googleapis.com")+"/admin/directory/v1/users/"+url.PathEscape(email), map[string]any{"name": name}))
		}
		if u.Photo != nil {
			out.Photo = part(write(ctx, cl, bearer, key, http.MethodPut, cl.Base(ID, "https://admin.googleapis.com")+"/admin/directory/v1/users/"+url.PathEscape(email)+"/photos/thumbnail",
				map[string]any{"photoData": base64.URLEncoding.EncodeToString(u.Photo), "mimeType": u.PhotoType}))
		}
	}
	if u.Signature != nil {
		bearer, key, err := delegated(ctx, cl, cr, []string{signatureScope}, email)
		if err != nil {
			out.Signature = PartFailed
		} else {
			out.Signature = part(write(ctx, cl, bearer, key, http.MethodPatch, cl.Base(ID+".gmail", "https://gmail.googleapis.com")+"/gmail/v1/users/me/settings/sendAs/"+url.PathEscape(email),
				map[string]any{"signature": *u.Signature}))
		}
	}
	return out, nil
}

func part(err error) string {
	if err != nil {
		return PartFailed
	}
	return PartUpdated
}

// writeDenied explains a refused write token in terms of the write scopes.
func writeDenied(err error) error {
	if codeOfErr(err) == c.CodeWorkspaceDelegationDenied {
		return c.Codedf(c.CodeWorkspaceDelegationDenied, c.ErrAuth, "Google refused the profile-change scopes: in Google Admin, authorize the service account's client ID for the Directory user and Gmail settings (basic) scopes as well")
	}
	return err
}

func write(ctx context.Context, cl *c.Client, bearer, key, method, endpoint string, body any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(raw))
	if err != nil {
		return c.Errf(c.ErrInvalid, "invalid Google Workspace request")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.Errf(c.ErrUnavailable, "could not reach Google")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && key != "" {
			tokens.Delete(key)
		}
		return c.HTTPError("Google Workspace", resp.StatusCode, "")
	}
	return nil
}
