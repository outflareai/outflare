// Package tagmanager puts Outflare's website script on a site through the
// workspace's own Google Tag Manager, signed in with Google, so installing
// it needs no developer.
package tagmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const (
	gtmAPI     = "https://tagmanager.googleapis.com/tagmanager/v2"
	googleUser = "https://openidconnect.googleapis.com"
	// allPages is Tag Manager's built-in All Pages trigger.
	allPages = "2147479553"
)

// All returns the tag managers that can be connected.
func All() []c.Connector { return []c.Connector{GTM{}} }

// GTM is Google Tag Manager.
type GTM struct{}

func (GTM) Definition() c.Definition {
	return c.Definition{
		ID: "google_tag_manager", Name: "Google Tag Manager", Category: c.CategoryTags, Website: "https://tagmanager.google.com",
		DocsURL: "https://developers.google.com/tag-platform/tag-manager/api/v2",
		Summary: "Add Outflare's website script to your site in one click, through your own Tag Manager container.",
		Description: "Outflare adds one Custom HTML tag named \"Outflare\" that fires on all pages, in a Tag Manager workspace of its own, " +
			"so nobody else's unpublished changes go live with it. It never edits or removes your other tags.",
		Auth: c.AuthOAuth2, Status: c.Available, Pricing: "Free",
		OAuth: &c.OAuth{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
			Scopes: []string{
				"https://www.googleapis.com/auth/tagmanager.edit.containers",
				"https://www.googleapis.com/auth/tagmanager.edit.containerversions",
				"https://www.googleapis.com/auth/tagmanager.publish",
				"openid", "email",
			},
			Params: map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		Capabilities: []string{c.CapTagInstall},
	}
}

func call(ctx context.Context, cl *c.Client, creds c.Creds, method, url string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds["access_token"])
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	_, err = c.DoJSON(ctx, cl, "Google Tag Manager", req, out)
	return err
}

func (GTM) api(cl *c.Client) string { return cl.Base("google_tag_manager", gtmAPI) }

type account struct {
	Path, AccountID, Name string
}

func (g GTM) accounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]account, error) {
	var resp struct {
		Account []account `json:"account"`
	}
	err := call(ctx, cl, creds, "GET", g.api(cl)+"/accounts", nil, &resp)
	return resp.Account, err
}

func (g GTM) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := call(ctx, cl, creds, "GET", cl.Base("google.userinfo", googleUser)+"/v1/userinfo", nil, &me); err != nil {
		return c.Account{}, err
	}
	accts, err := g.accounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{ID: me.Sub, Name: me.Email, Email: me.Email}
	if len(accts) == 0 {
		return acct, c.Errf(c.ErrAuth, "%s has no Tag Manager account: sign in with the Google account that manages your container", me.Email)
	}
	names := make([]string, 0, len(accts))
	for _, a := range accts {
		names = append(names, a.Name)
	}
	acct.Details = map[string]string{"accounts": strings.Join(names, ", ")}
	return acct, nil
}

type container struct {
	Path         string   `json:"path"`
	ContainerID  string   `json:"containerId"`
	AccountID    string   `json:"accountId"`
	Name         string   `json:"name"`
	PublicID     string   `json:"publicId"`
	DomainName   []string `json:"domainName"`
	UsageContext []string `json:"usageContext"`
}

// Containers lists the web containers of every account the sign-in sees.
func (g GTM) Containers(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.TagContainer, error) {
	accts, err := g.accounts(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	out := []c.TagContainer{}
	for _, a := range accts {
		var resp struct {
			Container []container `json:"container"`
		}
		if err := call(ctx, cl, creds, "GET", g.api(cl)+"/"+a.Path+"/containers", nil, &resp); err != nil {
			return nil, err
		}
		for _, ct := range resp.Container {
			if len(ct.UsageContext) > 0 && !contains(ct.UsageContext, "web") {
				continue // app and server containers can't run a page script
			}
			domains := ct.DomainName
			if domains == nil {
				domains = []string{}
			}
			out = append(out, c.TagContainer{ID: ct.Path, Name: ct.Name, PublicID: ct.PublicID, Account: a.Name, Domains: domains})
		}
	}
	return out, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

type workspace struct {
	Path        string `json:"path"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
}

type parameter struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type tag struct {
	Path            string      `json:"path,omitempty"`
	Name            string      `json:"name"`
	Type            string      `json:"type"`
	Parameter       []parameter `json:"parameter"`
	FiringTriggerID []string    `json:"firingTriggerId"`
	Notes           string      `json:"notes,omitempty"`
}

// workspaceName is the Tag Manager workspace Outflare makes its change in.
const workspaceName = "Outflare website script"

// InstallTag adds or updates the tag in a workspace of Outflare's own, then
// either publishes it as a new version or leaves it for a person to.
func (g GTM) InstallTag(ctx context.Context, cl *c.Client, creds c.Creds, containerID, name, html string, publish bool) (c.TagResult, error) {
	var res c.TagResult
	if !strings.HasPrefix(containerID, "accounts/") || strings.Contains(containerID, "..") {
		return res, c.Errf(c.ErrInvalid, "pick a container from the list")
	}
	base := g.api(cl)
	var ws struct {
		Workspace []workspace `json:"workspace"`
	}
	if err := call(ctx, cl, creds, "GET", base+"/"+containerID+"/workspaces", nil, &ws); err != nil {
		return res, err
	}
	var w *workspace
	for i := range ws.Workspace {
		if ws.Workspace[i].Name == workspaceName {
			w = &ws.Workspace[i]
		}
	}
	if w == nil {
		w = &workspace{}
		err := call(ctx, cl, creds, "POST", base+"/"+containerID+"/workspaces",
			map[string]string{"name": workspaceName, "description": "Adds Outflare's website script. Made by Outflare."}, w)
		if err != nil {
			if c.KindOf(err) == c.ErrInvalid {
				return res, c.Errf(c.ErrInvalid, "Tag Manager would not add a workspace to this container; free containers allow three, so publish or delete one and try again")
			}
			return res, err
		}
	}
	var tags struct {
		Tag []tag `json:"tag"`
	}
	if err := call(ctx, cl, creds, "GET", base+"/"+w.Path+"/tags", nil, &tags); err != nil {
		return res, err
	}
	want := tag{
		Name: name, Type: "html", FiringTriggerID: []string{allPages},
		Parameter: []parameter{{Type: "template", Key: "html", Value: html}, {Type: "boolean", Key: "supportDocumentWrite", Value: "false"}},
		Notes:     "Outflare website script. Added by Outflare; see outflare.ai/docs.",
	}
	var existing *tag
	for i := range tags.Tag {
		if tags.Tag[i].Name == name {
			existing = &tags.Tag[i]
		}
	}
	if existing != nil {
		res.Updated = true
		if err := call(ctx, cl, creds, "PUT", base+"/"+existing.Path, want, nil); err != nil {
			return res, err
		}
	} else if err := call(ctx, cl, creds, "POST", base+"/"+w.Path+"/tags", want, nil); err != nil {
		return res, err
	}
	parts := strings.Split(w.Path, "/") // accounts/1/containers/2/workspaces/3
	res.Link = "https://tagmanager.google.com/"
	if len(parts) == 6 {
		res.Link += "#/container/accounts/" + parts[1] + "/containers/" + parts[3] + "/workspaces/" + parts[5]
	}
	if !publish {
		return res, nil
	}
	var made struct {
		ContainerVersion struct {
			Path               string `json:"path"`
			ContainerVersionID string `json:"containerVersionId"`
		} `json:"containerVersion"`
		CompilerError bool `json:"compilerError"`
	}
	if err := call(ctx, cl, creds, "POST", base+"/"+w.Path+":create_version",
		map[string]string{"name": "Outflare website script", "notes": "Adds Outflare's website script on all pages."}, &made); err != nil {
		return res, err
	}
	if made.CompilerError || made.ContainerVersion.Path == "" {
		return res, c.Errf(c.ErrInvalid, "Tag Manager could not build a version from the change; open the workspace and publish it there")
	}
	if err := call(ctx, cl, creds, "POST", base+"/"+made.ContainerVersion.Path+":publish", nil, nil); err != nil {
		return res, err
	}
	res.Published, res.Version = true, made.ContainerVersion.ContainerVersionID
	if len(parts) == 6 {
		res.Link = "https://tagmanager.google.com/#/versions/accounts/" + parts[1] + "/containers/" + parts[3] + "/versions/" + res.Version
	}
	return res, nil
}
