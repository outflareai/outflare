package cms

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// WordPress publishes posts and pages through the REST API of the
// workspace's own site, signed in as one of its users with an application
// password (WordPress 5.6 and later, self-hosted or WordPress.com Business
// and up). The user needs to publish posts and pages (an Editor or an
// Author for posts).
type WordPress struct{}

func (WordPress) Definition() c.Definition {
	d := def("wordpress", "WordPress", "https://wordpress.org",
		"https://developer.wordpress.org/rest-api/reference/posts/", "https://wordpress.org/documentation/article/application-passwords/",
		"Publish posts and pages on your WordPress site, as drafts for review or live.",
		"Connects your own WordPress site through its REST API with an application password: in WordPress, Users, your profile, Application Passwords, add one named Outflare. The user needs to publish posts and pages (Editor, or Author for posts only). Outflare creates and updates posts and pages with the title, slug, body, excerpt and status you or an agent set; it never deletes anything. Tags and categories stay as the site has them for now.")
	d.Fields = []c.Field{
		{Name: "site_url", Label: "Site address", Placeholder: "https://www.example.com", Help: "Your site's address. WordPress.com sites need a Business plan or higher for the REST API."},
		{Name: "username", Label: "Username", Help: "The WordPress user the application password belongs to."},
		{Name: "api_key", Label: "Application password", Secret: true, Placeholder: "xxxx xxxx xxxx xxxx xxxx xxxx",
			Help: "Users, Profile, Application Passwords: add one named Outflare. Spaces don't matter."},
	}
	return d
}

func (WordPress) Targets() []string { return []string{c.CMSPosts, c.CMSPages} }

func (w WordPress) base(cl *c.Client, creds c.Creds) string {
	return cl.Base("wordpress", siteURL(creds)) + "/wp-json/wp/v2"
}

func (WordPress) call(cl *c.Client, creds c.Creds) *call {
	auth := creds["username"] + ":" + strings.ReplaceAll(c.Token(creds), " ", "")
	return newCall(cl, "WordPress").set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(auth)))
}

type wpPost struct {
	ID     int64  `json:"id"`
	Link   string `json:"link"`
	Status string `json:"status"`
	Slug   string `json:"slug"`
}

func (w WordPress) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if siteURL(creds) == "" {
		return c.Account{}, c.Errf(c.ErrInvalid, "the site address is required")
	}
	var me struct {
		ID           int64           `json:"id"`
		Name         string          `json:"name"`
		Capabilities map[string]bool `json:"capabilities"`
		Roles        []string        `json:"roles"`
	}
	if err := w.call(cl, creds).get(ctx, w.base(cl, creds)+"/users/me?context=edit", &me); err != nil {
		return c.Account{}, err
	}
	details := map[string]string{"roles": strings.Join(me.Roles, ", ")}
	if !me.Capabilities["edit_posts"] {
		details["warning"] = "this user cannot write posts; use an Editor or Author"
	}
	return c.Account{ID: fmt.Sprint(me.ID), Name: me.Name, Details: details}, nil
}

func (w WordPress) Publish(ctx context.Context, cl *c.Client, creds c.Creds, in c.CMSPostInput) (c.CMSResult, error) {
	target, err := wpTarget(in.Target)
	if err != nil {
		return c.CMSResult{}, err
	}
	body := map[string]any{"title": in.Title, "content": in.HTML, "excerpt": in.Excerpt}
	if in.Live {
		body["status"] = "publish"
	} else {
		body["status"] = "draft"
	}
	if in.Slug != "" {
		body["slug"] = in.Slug
	}
	u := w.base(cl, creds) + "/" + target
	if in.ExternalID != "" {
		u += "/" + url.PathEscape(in.ExternalID)
	}
	var p wpPost
	if err := w.call(cl, creds).post(ctx, u, body, &p); err != nil {
		return c.CMSResult{}, err
	}
	return wpResult(p), nil
}

func (w WordPress) ReadPost(ctx context.Context, cl *c.Client, creds c.Creds, target, externalID string) (c.CMSResult, error) {
	t, err := wpTarget(target)
	if err != nil {
		return c.CMSResult{}, err
	}
	var p wpPost
	if err := w.call(cl, creds).get(ctx, w.base(cl, creds)+"/"+t+"/"+url.PathEscape(externalID)+"?context=edit", &p); err != nil {
		return c.CMSResult{}, err
	}
	return wpResult(p), nil
}

func wpTarget(t string) (string, error) {
	switch t {
	case c.CMSPosts, c.CMSPages:
		return t, nil
	}
	return "", c.Errf(c.ErrInvalid, "WordPress publishes posts and pages, not %q", t)
}

func wpResult(p wpPost) c.CMSResult {
	st := "draft"
	if p.Status == "publish" {
		st = "published"
	}
	return c.CMSResult{ExternalID: fmt.Sprint(p.ID), URL: p.Link, Status: st, Slug: p.Slug}
}
