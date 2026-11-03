package cms

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Ghost publishes posts and pages through the Admin API of the workspace's
// own Ghost site (self-hosted or Ghost(Pro)), with a custom integration's
// Admin API key. The key signs a short-lived token for each call, as the
// Admin API asks; the key itself never leaves the connection.
type Ghost struct{}

func (Ghost) Definition() c.Definition {
	d := def("ghost", "Ghost", "https://ghost.org",
		"https://ghost.org/docs/admin-api/", "https://ghost.org/docs/admin-api/#token-authentication",
		"Publish posts and pages on your Ghost site, as drafts for review or live.",
		"Connects your own Ghost site through its Admin API: in Ghost, Settings, Integrations, add a custom integration named Outflare and copy its Admin API key. Outflare creates and updates posts and pages with the title, slug, body, excerpt, tags and status you or an agent set, signing each call with a short-lived token from the key; it never deletes anything.")
	d.Fields = []c.Field{
		{Name: "site_url", Label: "Site address", Placeholder: "https://blog.example.com", Help: "Your Ghost site's address (the admin lives under it)."},
		{Name: "api_key", Label: "Admin API key", Secret: true, Placeholder: "id:secret",
			Help: "Settings, Integrations, your custom integration: the Admin API key, as id:secret."},
	}
	return d
}

func (Ghost) Targets() []string { return []string{c.CMSPosts, c.CMSPages} }

func (g Ghost) base(cl *c.Client, creds c.Creds) string {
	return cl.Base("ghost", siteURL(creds)) + "/ghost/api/admin"
}

// token signs a 5-minute Admin API token with the key (JWT, HS256, the
// key id in the header and its hex secret as the signing key).
func ghostToken(key string, now time.Time) (string, error) {
	id, secret, ok := strings.Cut(strings.TrimSpace(key), ":")
	if !ok || id == "" || secret == "" {
		return "", c.Errf(c.ErrAuth, "the Admin API key is id:secret")
	}
	raw, err := hex.DecodeString(secret)
	if err != nil {
		return "", c.Errf(c.ErrAuth, "the Admin API key's secret is not hex")
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "HS256", "typ": "JWT", "kid": id})
	claims := enc(map[string]any{"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "aud": "/admin/"})
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte(head + "." + claims))
	return head + "." + claims + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (g Ghost) call(cl *c.Client, creds c.Creds) (*call, error) {
	tok, err := ghostToken(c.Token(creds), time.Now())
	if err != nil {
		return nil, err
	}
	return newCall(cl, "Ghost").set("Authorization", "Ghost "+tok).set("Accept-Version", "v5.0"), nil
}

type ghostPost struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	Slug      string `json:"slug"`
	UpdatedAt string `json:"updated_at"`
}

type ghostPosts struct {
	Posts []ghostPost `json:"posts"`
	Pages []ghostPost `json:"pages"`
}

func (p ghostPosts) first(target string) (ghostPost, bool) {
	list := p.Posts
	if target == c.CMSPages {
		list = p.Pages
	}
	if len(list) == 0 {
		return ghostPost{}, false
	}
	return list[0], true
}

func (g Ghost) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if siteURL(creds) == "" {
		return c.Account{}, c.Errf(c.ErrInvalid, "the site address is required")
	}
	k, err := g.call(cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	var site struct {
		Site struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Version string `json:"version"`
		} `json:"site"`
	}
	if err := k.get(ctx, g.base(cl, creds)+"/site/", &site); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: site.Site.URL, Name: site.Site.Title, Details: map[string]string{"version": site.Site.Version}}, nil
}

func (g Ghost) Publish(ctx context.Context, cl *c.Client, creds c.Creds, in c.CMSPostInput) (c.CMSResult, error) {
	target, err := ghostTarget(in.Target)
	if err != nil {
		return c.CMSResult{}, err
	}
	k, err := g.call(cl, creds)
	if err != nil {
		return c.CMSResult{}, err
	}
	post := map[string]any{"title": in.Title, "html": in.HTML, "status": status(in.Live), "custom_excerpt": in.Excerpt}
	if in.Slug != "" {
		post["slug"] = in.Slug
	}
	if in.CanonicalURL != "" {
		post["canonical_url"] = in.CanonicalURL
	}
	if len(in.Tags) > 0 {
		tags := []map[string]string{}
		for _, t := range in.Tags {
			tags = append(tags, map[string]string{"name": t})
		}
		post["tags"] = tags
	}
	base := g.base(cl, creds) + "/" + target + "/"
	var out ghostPosts
	if in.ExternalID == "" {
		if err := k.post(ctx, base+"?source=html", map[string]any{target: []any{post}}, &out); err != nil {
			return c.CMSResult{}, err
		}
	} else {
		// An update carries the post's current updated_at, which Ghost
		// checks so two editors never overwrite each other.
		var cur ghostPosts
		if err := k.get(ctx, base+url.PathEscape(in.ExternalID)+"/", &cur); err != nil {
			return c.CMSResult{}, err
		}
		p, ok := cur.first(target)
		if !ok {
			return c.CMSResult{}, c.Errf(c.ErrInvalid, "Ghost has no %s %s", strings.TrimSuffix(target, "s"), in.ExternalID)
		}
		post["updated_at"] = p.UpdatedAt
		if err := k.put(ctx, base+url.PathEscape(in.ExternalID)+"/?source=html", map[string]any{target: []any{post}}, &out); err != nil {
			return c.CMSResult{}, err
		}
	}
	p, ok := out.first(target)
	if !ok {
		return c.CMSResult{}, c.Errf(c.ErrUnavailable, "Ghost answered without the %s", strings.TrimSuffix(target, "s"))
	}
	return ghostResult(p), nil
}

func (g Ghost) ReadPost(ctx context.Context, cl *c.Client, creds c.Creds, target, externalID string) (c.CMSResult, error) {
	t, err := ghostTarget(target)
	if err != nil {
		return c.CMSResult{}, err
	}
	k, err := g.call(cl, creds)
	if err != nil {
		return c.CMSResult{}, err
	}
	var out ghostPosts
	if err := k.get(ctx, g.base(cl, creds)+"/"+t+"/"+url.PathEscape(externalID)+"/", &out); err != nil {
		return c.CMSResult{}, err
	}
	p, ok := out.first(t)
	if !ok {
		return c.CMSResult{}, c.Errf(c.ErrInvalid, "Ghost has no %s %s", strings.TrimSuffix(t, "s"), externalID)
	}
	return ghostResult(p), nil
}

func ghostTarget(t string) (string, error) {
	switch t {
	case c.CMSPosts, c.CMSPages:
		return t, nil
	}
	return "", c.Errf(c.ErrInvalid, "Ghost publishes posts and pages, not %q", t)
}

func ghostResult(p ghostPost) c.CMSResult {
	st := "draft"
	if p.Status == "published" {
		st = "published"
	}
	return c.CMSResult{ExternalID: p.ID, URL: p.URL, Status: st, Slug: p.Slug}
}
