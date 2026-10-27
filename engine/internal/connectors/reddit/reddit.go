// Package reddit connects a workspace to Reddit with its own Reddit app,
// so searching and posting run on the workspace's own API access and
// rate limits, never Outflare's.
//
// Connecting takes two steps. The person pastes the client id and secret
// of a web app they made at reddit.com/prefs/apps: that is enough to
// search, with an app-only token. Signing in to the brand's own Reddit
// account through that app then lets approved replies be posted as it.
package reddit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id.
const ID = "reddit"

const (
	apiBase = "https://oauth.reddit.com"
	wwwBase = "https://www.reddit.com"
	// scopes the brand's account grants: who it is, reading, and
	// commenting.
	scopes = "identity read submit"
)

// Reddit is the connector.
type Reddit struct{}

// All returns the community connectors.
func All() []c.Connector { return []c.Connector{Reddit{}} }

func (Reddit) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "Reddit", Category: c.CategorySocial,
		Summary: "Find threads where your brand can help, and post approved replies from your brand's own account.",
		Description: "Uses your own Reddit app, so searches and posts run on your Reddit API access. " +
			"Make a web app at reddit.com/prefs/apps with this server's redirect URI, paste its client id and secret, " +
			"then sign in with the brand's Reddit account to post replies a person approved. Every reply says who wrote it.",
		Website: "https://www.reddit.com", DocsURL: "https://www.reddit.com/dev/api/", KeyURL: "https://www.reddit.com/prefs/apps",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "client_id", Label: "Client id", Help: "Under the app's name at reddit.com/prefs/apps."},
			{Name: "client_secret", Label: "Secret", Secret: true},
		},
		Capabilities: []string{c.CapSocialSearch, c.CapSocialReply},
		Status:       c.Available,
		Pricing:      "Reddit's Data API terms apply to your app",
	}
}

// Test gets an app-only token, which proves the client id and secret, and
// reads the signed-in account when there is one.
func (r Reddit) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["refresh_token"] != "" {
		tok, err := r.userToken(ctx, cl, creds)
		if err != nil {
			return c.Account{}, err
		}
		me, err := r.me(ctx, cl, tok)
		if err != nil {
			return c.Account{}, err
		}
		return c.Account{ID: me, Name: "u/" + me, Details: map[string]string{"posting": "on"}}, nil
	}
	if _, err := r.appToken(ctx, cl, creds); err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: "Reddit app " + truncate(creds["client_id"], 8), Details: map[string]string{"posting": "sign in to post"}}, nil
}

// CanPost reports whether the connection is signed in to an account.
func CanPost(creds c.Creds) bool { return creds["refresh_token"] != "" }

// ---- Signing in to the brand's account ----

// SignInURL is Reddit's consent page for the workspace's own app.
func (Reddit) SignInURL(cl *c.Client, creds c.Creds, redirectURI, state string) string {
	q := url.Values{
		"client_id": {creds["client_id"]}, "response_type": {"code"}, "state": {state},
		"redirect_uri": {redirectURI}, "duration": {"permanent"}, "scope": {scopes},
	}
	return cl.Base(ID+".www", wwwBase) + "/api/v1/authorize?" + q.Encode()
}

// SignIn trades the consent page's code for the account's tokens and
// returns the connection's new credentials.
func (r Reddit) SignIn(ctx context.Context, cl *c.Client, creds c.Creds, code, redirectURI string) (c.Creds, c.Account, error) {
	t, err := r.token(ctx, cl, creds, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}})
	if t.Error == "invalid_grant" {
		return nil, c.Account{}, c.Errf(c.ErrAuth, "Reddit did not accept the sign-in; check the app's redirect URI and try again")
	}
	if err != nil {
		return nil, c.Account{}, err
	}
	if t.RefreshToken == "" {
		return nil, c.Account{}, c.Errf(c.ErrAuth, "Reddit gave no lasting sign-in; try again")
	}
	me, err := r.me(ctx, cl, t.AccessToken)
	if err != nil {
		return nil, c.Account{}, err
	}
	out := c.Creds{"client_id": creds["client_id"], "client_secret": creds["client_secret"], "refresh_token": t.RefreshToken, "username": me}
	return out, c.Account{ID: me, Name: "u/" + me, Details: map[string]string{"posting": "on"}}, nil
}

// ---- Tokens ----

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

func (Reddit) token(ctx context.Context, cl *c.Client, creds c.Creds, form url.Values) (tokenResp, error) {
	var t tokenResp
	if creds["client_id"] == "" || creds["client_secret"] == "" {
		return t, c.Errf(c.ErrAuth, "the Reddit connection needs its app's client id and secret")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(ID+".www", wwwBase)+"/api/v1/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.SetBasicAuth(creds["client_id"], creds["client_secret"])
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setAgent(req, creds)
	if _, err := c.DoJSON(ctx, cl, "Reddit", req, &t); err != nil {
		return t, err
	}
	switch {
	case t.Error == "invalid_grant":
		return t, c.Errf(c.ErrAuth, "Reddit no longer accepts the brand's sign-in; sign in again")
	case t.Error != "":
		return t, c.Errf(c.ErrAuth, "Reddit refused the app: %s", t.Error)
	case t.AccessToken == "":
		return t, c.Errf(c.ErrUnavailable, "Reddit answered without a token")
	}
	return t, nil
}

func (r Reddit) appToken(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	t, err := r.token(ctx, cl, creds, url.Values{"grant_type": {"client_credentials"}})
	return t.AccessToken, err
}

func (r Reddit) userToken(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	t, err := r.token(ctx, cl, creds, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {creds["refresh_token"]}})
	return t.AccessToken, err
}

// readToken is the signed-in account's token when there is one, so reads
// see what the account sees, or else an app-only token.
func (r Reddit) readToken(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	if CanPost(creds) {
		return r.userToken(ctx, cl, creds)
	}
	return r.appToken(ctx, cl, creds)
}

// setAgent sets the descriptive User-Agent Reddit asks every app for.
func setAgent(req *http.Request, creds c.Creds) {
	ua := "web:outflare:1 (connection " + truncate(creds["client_id"], 8)
	if u := creds["username"]; u != "" {
		ua += " by /u/" + u
	}
	req.Header.Set("User-Agent", ua+")")
}

func (r Reddit) call(ctx context.Context, cl *c.Client, creds c.Creds, tok, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, cl.Base(ID, apiBase)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	setAgent(req, creds)
	_, err = c.DoJSON(ctx, cl, "Reddit", req, out)
	return err
}

func (r Reddit) me(ctx context.Context, cl *c.Client, tok string) (string, error) {
	var me struct {
		Name string `json:"name"`
	}
	if err := r.call(ctx, cl, nil, tok, http.MethodGet, "/api/v1/me?raw_json=1", nil, &me); err != nil {
		return "", err
	}
	if me.Name == "" {
		return "", c.Errf(c.ErrAuth, "Reddit did not say which account signed in")
	}
	return me.Name, nil
}

// ---- Searching ----

type listing struct {
	Data struct {
		Children []struct {
			Kind string `json:"kind"`
			Data struct {
				Name        string  `json:"name"`
				Subreddit   string  `json:"subreddit"`
				Title       string  `json:"title"`
				Selftext    string  `json:"selftext"`
				Body        string  `json:"body"`
				Author      string  `json:"author"`
				Permalink   string  `json:"permalink"`
				Score       int     `json:"score"`
				NumComments int     `json:"num_comments"`
				CreatedUTC  float64 `json:"created_utc"`
				Archived    bool    `json:"archived"`
				Locked      bool    `json:"locked"`
				Over18      bool    `json:"over_18"`
				Removed     *string `json:"removed_by_category"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

// SearchPosts finds posts from the last week that match query, newest
// first, in subreddit community when one is given or else all of Reddit.
// Adult and removed posts are left out.
func (r Reddit) SearchPosts(ctx context.Context, cl *c.Client, creds c.Creds, community, query string, limit int) ([]c.SocialPost, error) {
	tok, err := r.readToken(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	q := url.Values{"q": {query}, "sort": {"new"}, "t": {"week"}, "limit": {strconv.Itoa(limit)}, "type": {"link"}, "raw_json": {"1"}}
	path := "/search"
	if community != "" {
		path = "/r/" + url.PathEscape(community) + "/search"
		q.Set("restrict_sr", "1")
	}
	var l listing
	if err := r.call(ctx, cl, creds, tok, http.MethodGet, path+"?"+q.Encode(), nil, &l); err != nil {
		return nil, err
	}
	var out []c.SocialPost
	for _, ch := range l.Data.Children {
		d := ch.Data
		if ch.Kind != "t3" || d.Over18 || d.Removed != nil || d.Name == "" {
			continue
		}
		out = append(out, c.SocialPost{ExternalID: d.Name, Community: d.Subreddit, Title: d.Title, Body: truncate(d.Selftext, 10000), Author: d.Author,
			URL: "https://www.reddit.com" + d.Permalink, Score: d.Score, Comments: d.NumComments,
			PostedAt: time.Unix(int64(d.CreatedUTC), 0).UTC(), Closed: d.Archived || d.Locked})
	}
	return out, nil
}

// ---- Replying ----

// CanAct reports whether the connection is signed in to an account.
func (Reddit) CanAct(creds c.Creds) bool { return CanPost(creds) }

// ActingAs is the signed-in account's username.
func (Reddit) ActingAs(creds c.Creds) string { return creds["username"] }

// ReplyTo posts text as a comment on the post externalID, as the
// signed-in account.
func (r Reddit) ReplyTo(ctx context.Context, cl *c.Client, creds c.Creds, externalID, text string) (c.SocialResult, error) {
	if !CanPost(creds) {
		return c.SocialResult{}, c.Errf(c.ErrAuth, "sign in to the brand's Reddit account to post")
	}
	tok, err := r.userToken(ctx, cl, creds)
	if err != nil {
		return c.SocialResult{}, err
	}
	var resp struct {
		JSON struct {
			Errors [][]any `json:"errors"`
			Data   struct {
				Things []struct {
					Data struct {
						Name      string `json:"name"`
						Permalink string `json:"permalink"`
					} `json:"data"`
				} `json:"things"`
			} `json:"data"`
		} `json:"json"`
	}
	form := url.Values{"api_type": {"json"}, "thing_id": {externalID}, "text": {text}}
	if err := r.call(ctx, cl, creds, tok, http.MethodPost, "/api/comment", form, &resp); err != nil {
		return c.SocialResult{}, err
	}
	if len(resp.JSON.Errors) > 0 {
		return c.SocialResult{}, c.Errf(c.ErrInvalid, "Reddit refused the comment: %s", redditErrors(resp.JSON.Errors))
	}
	if len(resp.JSON.Data.Things) == 0 || resp.JSON.Data.Things[0].Data.Name == "" {
		return c.SocialResult{}, c.Errf(c.ErrUnavailable, "Reddit did not return the comment")
	}
	t := resp.JSON.Data.Things[0].Data
	link := t.Permalink
	if strings.HasPrefix(link, "/") {
		link = "https://www.reddit.com" + link
	}
	return c.SocialResult{ExternalID: t.Name, URL: link}, nil
}

// Results reads posted comments' scores, and whether they were taken
// down, by fullname (up to 100).
func (r Reddit) Results(ctx context.Context, cl *c.Client, creds c.Creds, ids []string) (map[string]c.SocialResult, error) {
	if len(ids) == 0 {
		return map[string]c.SocialResult{}, nil
	}
	if len(ids) > 100 {
		return nil, errors.New("at most 100 comments at a time")
	}
	tok, err := r.readToken(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	var l listing
	if err := r.call(ctx, cl, creds, tok, http.MethodGet, "/api/info?raw_json=1&id="+url.QueryEscape(strings.Join(ids, ",")), nil, &l); err != nil {
		return nil, err
	}
	out := map[string]c.SocialResult{}
	for _, ch := range l.Data.Children {
		d := ch.Data
		out[d.Name] = c.SocialResult{ExternalID: d.Name, URL: "https://www.reddit.com" + d.Permalink, Score: d.Score,
			Removed: d.Removed != nil || d.Body == "[removed]" || d.Author == "[deleted]"}
	}
	return out, nil
}

func redditErrors(errs [][]any) string {
	var parts []string
	for _, e := range errs {
		if len(e) > 1 {
			parts = append(parts, fmt.Sprint(e[1]))
		} else if len(e) > 0 {
			parts = append(parts, fmt.Sprint(e[0]))
		}
	}
	return truncate(strings.Join(parts, "; "), 300)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
