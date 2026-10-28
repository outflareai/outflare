// Package x connects a workspace to X with its own X developer app, so
// every call runs on the workspace's own API access and X bills the
// workspace's own account, never Outflare's.
//
// Connecting takes two steps. The person pastes the OAuth 2.0 client id
// and secret of an app they made in the X developer console. Signing in
// to the brand's X account through that app (with PKCE) then lets
// Outflare read the account's followers and posts, and later post for it.
// X replaces the refresh token every time it is used, so the connector
// renews tokens itself and the store keeps the new one.
package x

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id.
const ID = "x"

const (
	apiBase = "https://api.x.com"
	// authBase is where the consent page lives.
	authBase = "https://x.com"
	// Scopes the brand's account grants. Writing and following are asked
	// for now, so posting and approved actions later need no new sign-in.
	scopes = "tweet.read tweet.write users.read follows.read follows.write like.read like.write offline.access"
)

// X is the connector.
type X struct{}

// All returns the X connectors.
func All() []c.Connector { return []c.Connector{X{}} }

func (X) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "X", Category: c.CategorySocial,
		Summary: "Track your brand's X followers and posts, and post from its own account.",
		Description: "Uses your own X developer app, so X bills your account for API use (pay per use). " +
			"Make an app in the X developer console with OAuth 2.0 turned on (type: web app) and this server's redirect URI, " +
			"paste its OAuth 2.0 client id and secret, then sign in with the brand's X account.",
		Website: "https://x.com", DocsURL: "https://docs.x.com/x-api/introduction", KeyURL: "https://developer.x.com/en/portal/dashboard",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "client_id", Label: "OAuth 2.0 client id", Help: "Under Keys and tokens in the X developer console."},
			{Name: "client_secret", Label: "OAuth 2.0 client secret", Secret: true},
		},
		Capabilities: []string{c.CapSocialMetrics, c.CapSocialPublish},
		Status:       c.Available,
		Pricing:      "X bills your developer account per use",
	}
}

// Test reads the signed-in account when there is one. Before sign-in X
// has no call that proves a client id on its own, so it only checks that
// both are there.
func (x X) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["client_id"] == "" || creds["client_secret"] == "" {
		return c.Account{}, c.Errf(c.ErrInvalid, "paste the app's OAuth 2.0 client id and secret")
	}
	if !SignedIn(creds) {
		return c.Account{Name: "X app " + truncate(creds["client_id"], 8), Details: map[string]string{"account": "sign in to the brand's X account"}}, nil
	}
	u, err := x.me(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: u.ID, Name: "@" + u.Username, Details: map[string]string{"account": "signed in"}}, nil
}

// SignedIn reports whether the connection is signed in to an account.
func SignedIn(creds c.Creds) bool { return creds["refresh_token"] != "" }

// ---- Signing in to the brand's account ----

// SignInURL is never used: X needs PKCE (SignInURLPKCE).
func (X) SignInURL(cl *c.Client, creds c.Creds, redirectURI, state string) string { return "" }

// SignIn is never used: X needs PKCE (SignInPKCE).
func (X) SignIn(ctx context.Context, cl *c.Client, creds c.Creds, code, redirectURI string) (c.Creds, c.Account, error) {
	return nil, c.Account{}, c.Errf(c.ErrInvalid, "X sign-in needs PKCE")
}

// SignInURLPKCE is X's consent page for the workspace's own app.
func (X) SignInURLPKCE(cl *c.Client, creds c.Creds, redirectURI, state, verifier string) string {
	q := url.Values{
		"response_type": {"code"}, "client_id": {creds["client_id"]}, "redirect_uri": {redirectURI}, "state": {state},
		"scope": {scopes}, "code_challenge": {c.PKCEChallenge(verifier)}, "code_challenge_method": {"S256"},
	}
	return cl.Base(ID+".auth", authBase) + "/i/oauth2/authorize?" + q.Encode()
}

// SignInPKCE trades the consent page's code for the account's tokens and
// returns the connection's new credentials.
func (x X) SignInPKCE(ctx context.Context, cl *c.Client, creds c.Creds, code, redirectURI, verifier string) (c.Creds, c.Account, error) {
	t, err := x.token(ctx, cl, creds, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"code_verifier": {verifier}, "client_id": {creds["client_id"]}})
	if err != nil {
		if t.Error == "invalid_request" || t.Error == "invalid_grant" {
			return nil, c.Account{}, c.Errf(c.ErrAuth, "X did not accept the sign-in; check the app's redirect URI and try again")
		}
		return nil, c.Account{}, err
	}
	if t.RefreshToken == "" {
		return nil, c.Account{}, c.Errf(c.ErrAuth, "X gave no lasting sign-in; check that the app asks for offline access, then try again")
	}
	out := withTokens(creds, t)
	u, err := x.me(ctx, cl, out)
	if err != nil {
		return nil, c.Account{}, err
	}
	out["user_id"], out["username"] = u.ID, u.Username
	return out, c.Account{ID: u.ID, Name: "@" + u.Username, Details: map[string]string{"account": "signed in"}}, nil
}

// ---- Tokens ----

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (X) token(ctx context.Context, cl *c.Client, creds c.Creds, form url.Values) (tokenResp, error) {
	var t tokenResp
	if creds["client_id"] == "" || creds["client_secret"] == "" {
		return t, c.Errf(c.ErrAuth, "the X connection needs its app's OAuth 2.0 client id and secret")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(ID, apiBase)+"/2/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(creds["client_id"])+":"+url.QueryEscape(creds["client_secret"]))))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		return t, c.Errf(c.ErrUnavailable, "could not reach X: %v", err)
	}
	defer resp.Body.Close()
	_ = decode(resp, &t)
	switch {
	case t.Error == "invalid_grant" || t.Error == "invalid_request":
		return t, c.Errf(c.ErrAuth, "X no longer accepts the brand's sign-in; sign in again")
	case t.Error == "unauthorized_client" || resp.StatusCode == http.StatusUnauthorized:
		return t, c.Errf(c.ErrAuth, "X refused the app's client id or secret")
	case t.Error != "":
		return t, c.Errf(c.ErrAuth, "X refused the app: %s", t.Error)
	case resp.StatusCode != http.StatusOK || t.AccessToken == "":
		return t, c.HTTPError("X", resp.StatusCode, "")
	}
	return t, nil
}

func withTokens(creds c.Creds, t tokenResp) c.Creds {
	out := c.Creds{}
	for k, v := range creds {
		out[k] = v
	}
	out["access_token"] = t.AccessToken
	if t.RefreshToken != "" {
		out["refresh_token"] = t.RefreshToken
	}
	exp := t.ExpiresIn
	if exp <= 0 {
		exp = 7200
	}
	out["expires_at"] = strconv.FormatInt(time.Now().Add(time.Duration(exp)*time.Second).Unix(), 10)
	return out
}

// RefreshDue reports whether the access token has less than five minutes
// left (or is missing) on a signed-in connection.
func (X) RefreshDue(creds c.Creds) bool {
	if !SignedIn(creds) {
		return false
	}
	exp, err := strconv.ParseInt(creds["expires_at"], 10, 64)
	return err != nil || creds["access_token"] == "" || time.Until(time.Unix(exp, 0)) < 5*time.Minute
}

// RefreshCreds renews the access token. X hands back a new refresh token
// and retires the old one, so the result must be stored.
func (x X) RefreshCreds(ctx context.Context, cl *c.Client, creds c.Creds) (c.Creds, error) {
	t, err := x.token(ctx, cl, creds, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {creds["refresh_token"]}, "client_id": {creds["client_id"]}})
	if err != nil {
		return nil, err
	}
	return withTokens(creds, t), nil
}

// ---- Calls ----

func (X) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	if creds["access_token"] == "" {
		return c.Errf(c.ErrAuth, "sign in to the brand's X account first")
	}
	u := cl.Base(ID, apiBase) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds["access_token"])
	status, err := c.DoJSON(ctx, cl, "X", req, out)
	if status == http.StatusPaymentRequired {
		return c.Errf(c.ErrQuota, "X says the developer account has no credits left; add credits in the X developer console")
	}
	return err
}

type user struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Username        string `json:"username"`
	ProfileImageURL string `json:"profile_image_url"`
	PublicMetrics   struct {
		Followers *int64 `json:"followers_count"`
		Following *int64 `json:"following_count"`
		Tweets    *int64 `json:"tweet_count"`
	} `json:"public_metrics"`
}

func (x X) me(ctx context.Context, cl *c.Client, creds c.Creds) (user, error) {
	var r struct {
		Data user `json:"data"`
	}
	err := x.get(ctx, cl, creds, "/2/users/me", url.Values{"user.fields": {"public_metrics,profile_image_url"}}, &r)
	if err == nil && r.Data.ID == "" {
		err = c.Errf(c.ErrUnavailable, "X answered without the account")
	}
	return r.Data, err
}

// SocialAccounts is the signed-in account, the only one an X sign-in
// reaches.
func (x X) SocialAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.SocialAccount, error) {
	if !SignedIn(creds) {
		return nil, nil
	}
	u, err := x.me(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	return []c.SocialAccount{{ExternalID: u.ID, Kind: c.SocialProfile, Handle: u.Username, Name: u.Name,
		URL: "https://x.com/" + u.Username, AvatarURL: u.ProfileImageURL}}, nil
}

// SocialStats reads followers, following and posts from the account.
func (x X) SocialStats(ctx context.Context, cl *c.Client, creds c.Creds, a c.SocialAccount) (c.SocialStats, error) {
	u, err := x.me(ctx, cl, creds)
	if err != nil {
		return c.SocialStats{}, err
	}
	if u.ID != a.ExternalID {
		return c.SocialStats{}, c.Errf(c.ErrAuth, "the X connection is now signed in to @%s, not this account; sign in again", u.Username)
	}
	m := u.PublicMetrics
	return c.SocialStats{Followers: m.Followers, Following: m.Following, Posts: m.Tweets}, nil
}

// SocialPosts reads the account's own recent posts with their metrics.
// Replies and reposts are left out. Private metrics (link clicks) are
// only given for posts under 30 days old, which is all this asks for.
func (x X) SocialPosts(ctx context.Context, cl *c.Client, creds c.Creds, a c.SocialAccount, since time.Time, limit int) ([]c.SocialOwnPost, error) {
	if limit < 5 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}
	if min := time.Now().Add(-29 * 24 * time.Hour); since.Before(min) {
		since = min
	}
	q := url.Values{
		"max_results": {strconv.Itoa(limit)}, "exclude": {"retweets,replies"},
		"start_time":   {since.UTC().Format(time.RFC3339)},
		"tweet.fields": {"created_at,public_metrics,non_public_metrics"},
	}
	var r struct {
		Data []struct {
			ID            string    `json:"id"`
			Text          string    `json:"text"`
			CreatedAt     time.Time `json:"created_at"`
			PublicMetrics struct {
				Retweets    *int64 `json:"retweet_count"`
				Replies     *int64 `json:"reply_count"`
				Likes       *int64 `json:"like_count"`
				Quotes      *int64 `json:"quote_count"`
				Bookmarks   *int64 `json:"bookmark_count"`
				Impressions *int64 `json:"impression_count"`
			} `json:"public_metrics"`
			NonPublicMetrics struct {
				Impressions *int64 `json:"impression_count"`
				LinkClicks  *int64 `json:"url_link_clicks"`
			} `json:"non_public_metrics"`
		} `json:"data"`
	}
	if err := x.get(ctx, cl, creds, "/2/users/"+url.PathEscape(a.ExternalID)+"/tweets", q, &r); err != nil {
		return nil, err
	}
	out := make([]c.SocialOwnPost, 0, len(r.Data))
	for _, t := range r.Data {
		pm, np := t.PublicMetrics, t.NonPublicMetrics
		imp := pm.Impressions
		if np.Impressions != nil {
			imp = np.Impressions
		}
		out = append(out, c.SocialOwnPost{ExternalID: t.ID, URL: "https://x.com/" + a.Handle + "/status/" + t.ID, Body: t.Text, PostedAt: t.CreatedAt,
			Metrics: c.SocialPostMetrics{Impressions: imp, Likes: pm.Likes, Replies: pm.Replies, Reposts: pm.Retweets, Quotes: pm.Quotes,
				Bookmarks: pm.Bookmarks, Clicks: np.LinkClicks}})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func decode(resp *http.Response, v any) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
