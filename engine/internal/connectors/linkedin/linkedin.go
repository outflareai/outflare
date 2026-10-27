// Package linkedin connects a workspace to LinkedIn: the person signs in
// with their own LinkedIn account, which reaches their own profile and the
// company pages they administer. It uses LinkedIn's Community Management
// API (pages: posts, page followers and post statistics) and Share on
// LinkedIn (posting to the person's own profile).
//
// The OAuth app is the server's (CONNECTOR_LINKEDIN_CLIENT_ID and _SECRET):
// LinkedIn approves each app for the Community Management API, so the
// server registers one and every workspace signs in to its own accounts
// through it. The tokens belong to the workspace's own LinkedIn account,
// and LinkedIn's API costs nothing.
package linkedin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id.
const ID = "linkedin"

const (
	apiBase = "https://api.linkedin.com"
	// version is the LinkedIn-Version header for /rest calls (YYYYMM).
	// LinkedIn keeps each version for about a year.
	version = "202603"
)

// scopes the person grants: who they are, posting to their own profile,
// and reading and posting for the pages they administer.
var scopes = []string{"r_basicprofile", "w_member_social", "r_organization_social", "w_organization_social", "rw_organization_admin"}

// LinkedIn is the connector.
type LinkedIn struct{}

// All returns the LinkedIn connectors.
func All() []c.Connector { return []c.Connector{LinkedIn{}} }

func (LinkedIn) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "LinkedIn", Category: c.CategorySocial,
		Summary: "Track followers and posts of your company pages and profile, and post from them.",
		Description: "Sign in with your own LinkedIn account: Outflare reaches your profile and every company page you administer. " +
			"LinkedIn's API is free; on a self-hosted server, register a LinkedIn app with the Community Management API and set " +
			"CONNECTOR_LINKEDIN_CLIENT_ID and CONNECTOR_LINKEDIN_CLIENT_SECRET.",
		Website: "https://www.linkedin.com", DocsURL: "https://learn.microsoft.com/en-us/linkedin/marketing/community-management/community-management-overview",
		Auth: c.AuthOAuth2,
		OAuth: &c.OAuth{
			AuthURL: "https://www.linkedin.com/oauth/v2/authorization", TokenURL: "https://www.linkedin.com/oauth/v2/accessToken",
			Scopes: scopes,
		},
		Capabilities: []string{c.CapSocialMetrics},
		Status:       c.Available,
		Pricing:      "Free",
	}
}

// Test reads the signed-in person.
func (l LinkedIn) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	me, err := l.me(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.ID, Name: me.name()}, nil
}

// ---- Calls ----

// get calls the API and reports the status, so optional reads can treat
// a refusal as "not available" instead of a broken connection.
func (LinkedIn) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q string, out any) (int, error) {
	u := cl.Base(ID, apiBase) + path
	if q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	if strings.HasPrefix(path, "/rest/") {
		req.Header.Set("LinkedIn-Version", version)
		req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	}
	return c.DoJSON(ctx, cl, "LinkedIn", req, out)
}

type person struct {
	ID        string `json:"id"`
	First     string `json:"localizedFirstName"`
	Last      string `json:"localizedLastName"`
	VanityURL string `json:"vanityName"`
}

func (p person) name() string { return strings.TrimSpace(p.First + " " + p.Last) }

func (l LinkedIn) me(ctx context.Context, cl *c.Client, creds c.Creds) (person, error) {
	var p person
	_, err := l.get(ctx, cl, creds, "/v2/me", "", &p)
	if err == nil && p.ID == "" {
		err = c.Errf(c.ErrUnavailable, "LinkedIn answered without the account")
	}
	return p, err
}

// SocialAccounts is the person's own profile and every page they
// administer.
func (l LinkedIn) SocialAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.SocialAccount, error) {
	me, err := l.me(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	prof := c.SocialAccount{ExternalID: "urn:li:person:" + me.ID, Kind: c.SocialProfile, Handle: me.VanityURL, Name: me.name()}
	if me.VanityURL != "" {
		prof.URL = "https://www.linkedin.com/in/" + me.VanityURL
	}
	out := []c.SocialAccount{prof}
	var acls struct {
		Elements []struct {
			Organization string `json:"organization"`
		} `json:"elements"`
	}
	status, err := l.get(ctx, cl, creds, "/rest/organizationAcls", "q=roleAssignee&role=ADMINISTRATOR&state=APPROVED&count=100", &acls)
	if status == http.StatusForbidden {
		// Not granted page access: the profile alone.
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, a := range acls.Elements {
		id := strings.TrimPrefix(a.Organization, "urn:li:organization:")
		if id == a.Organization || id == "" {
			continue
		}
		var org struct {
			Name   string `json:"localizedName"`
			Vanity string `json:"vanityName"`
		}
		if _, err := l.get(ctx, cl, creds, "/rest/organizations/"+url.PathEscape(id), "", &org); err != nil {
			return nil, err
		}
		page := c.SocialAccount{ExternalID: a.Organization, Kind: c.SocialPage, Handle: org.Vanity, Name: org.Name}
		if org.Vanity != "" {
			page.URL = "https://www.linkedin.com/company/" + org.Vanity
		}
		out = append(out, page)
	}
	return out, nil
}

// SocialStats reads followers: a page's from its network size, a
// profile's from the member followers count when LinkedIn grants it.
func (l LinkedIn) SocialStats(ctx context.Context, cl *c.Client, creds c.Creds, a c.SocialAccount) (c.SocialStats, error) {
	if a.Kind == c.SocialPage {
		var n struct {
			Size *int64 `json:"firstDegreeSize"`
		}
		if _, err := l.get(ctx, cl, creds, "/rest/networkSizes/"+url.PathEscape(a.ExternalID), "edgeType=COMPANY_FOLLOWED_BY_MEMBER", &n); err != nil {
			return c.SocialStats{}, err
		}
		return c.SocialStats{Followers: n.Size}, nil
	}
	var r struct {
		Elements []struct {
			Count *int64 `json:"memberFollowersCount"`
		} `json:"elements"`
	}
	status, err := l.get(ctx, cl, creds, "/rest/memberFollowersCount", "q=me", &r)
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return c.SocialStats{}, nil
	}
	if err != nil {
		return c.SocialStats{}, err
	}
	if len(r.Elements) == 0 {
		return c.SocialStats{}, nil
	}
	return c.SocialStats{Followers: r.Elements[0].Count}, nil
}

// SocialPosts reads a page's own posts with their statistics. LinkedIn
// does not let apps list a person's own posts, so a profile has none
// here; posts Outflare publishes for it are tracked when it posts them.
func (l LinkedIn) SocialPosts(ctx context.Context, cl *c.Client, creds c.Creds, a c.SocialAccount, since time.Time, limit int) ([]c.SocialOwnPost, error) {
	if a.Kind != c.SocialPage {
		return nil, nil
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	var r struct {
		Elements []struct {
			ID          string `json:"id"`
			Commentary  string `json:"commentary"`
			PublishedAt int64  `json:"publishedAt"`
			CreatedAt   int64  `json:"createdAt"`
			State       string `json:"lifecycleState"`
		} `json:"elements"`
	}
	q := "q=author&author=" + url.QueryEscape(a.ExternalID) + "&count=" + strconv.Itoa(limit) + "&sortBy=CREATED"
	if _, err := l.get(ctx, cl, creds, "/rest/posts", q, &r); err != nil {
		return nil, err
	}
	var posts []c.SocialOwnPost
	var shares, ugc []string
	for _, p := range r.Elements {
		at := p.PublishedAt
		if at == 0 {
			at = p.CreatedAt
		}
		t := time.UnixMilli(at)
		if (p.State != "" && p.State != "PUBLISHED") || t.Before(since) {
			continue
		}
		posts = append(posts, c.SocialOwnPost{ExternalID: p.ID, URL: "https://www.linkedin.com/feed/update/" + p.ID + "/", Body: p.Commentary, PostedAt: t})
		switch {
		case strings.HasPrefix(p.ID, "urn:li:share:"):
			shares = append(shares, p.ID)
		case strings.HasPrefix(p.ID, "urn:li:ugcPost:"):
			ugc = append(ugc, p.ID)
		}
	}
	if len(posts) == 0 {
		return posts, nil
	}
	stats, err := l.postStats(ctx, cl, creds, a.ExternalID, shares, ugc)
	if err != nil {
		return nil, err
	}
	for i := range posts {
		posts[i].Metrics = stats[posts[i].ExternalID]
	}
	return posts, nil
}

// postStats reads lifetime statistics for a page's posts.
func (l LinkedIn) postStats(ctx context.Context, cl *c.Client, creds c.Creds, org string, shares, ugc []string) (map[string]c.SocialPostMetrics, error) {
	q := "q=organizationalEntity&organizationalEntity=" + url.QueryEscape(org)
	if len(shares) > 0 {
		q += "&shares=" + restliList(shares)
	}
	if len(ugc) > 0 {
		q += "&ugcPosts=" + restliList(ugc)
	}
	var r struct {
		Elements []struct {
			Share   string `json:"share"`
			UGCPost string `json:"ugcPost"`
			Total   struct {
				Impressions *int64 `json:"impressionCount"`
				Clicks      *int64 `json:"clickCount"`
				Likes       *int64 `json:"likeCount"`
				Comments    *int64 `json:"commentCount"`
				Shares      *int64 `json:"shareCount"`
			} `json:"totalShareStatistics"`
		} `json:"elements"`
	}
	if _, err := l.get(ctx, cl, creds, "/rest/organizationalEntityShareStatistics", q, &r); err != nil {
		return nil, err
	}
	out := map[string]c.SocialPostMetrics{}
	for _, e := range r.Elements {
		id := e.Share
		if id == "" {
			id = e.UGCPost
		}
		t := e.Total
		out[id] = c.SocialPostMetrics{Impressions: t.Impressions, Clicks: t.Clicks, Likes: t.Likes, Replies: t.Comments, Reposts: t.Shares}
	}
	return out, nil
}

// restliList is a Rest.li 2.0 list of URNs: List(urn%3Ali%3Ashare%3A1,...).
func restliList(urns []string) string {
	parts := make([]string, len(urns))
	for i, u := range urns {
		parts[i] = url.QueryEscape(u)
	}
	return fmt.Sprintf("List(%s)", strings.Join(parts, ","))
}
