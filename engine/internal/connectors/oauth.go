package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// OAuthApp is the server's registered app at a provider.
type OAuthApp struct {
	ClientID, ClientSecret string
	// Scopes replaces the connector's default scopes when set
	// (CONNECTOR_<ID>_SCOPES, space separated), for apps registered with
	// a different set.
	Scopes []string
	// AuthURL replaces the connector's consent page when set
	// (CONNECTOR_<ID>_AUTH_URL), for providers that give each app its own
	// install link (Stripe Apps).
	AuthURL string
}

// OAuthAppsFromEnv reads CONNECTOR_<ID>_CLIENT_ID and _CLIENT_SECRET for
// every OAuth connector in the registry. Google connectors fall back to the
// server's Google app (GOOGLE_OAUTH_CLIENT_ID and _SECRET, the one used for
// mailboxes and sign-in), so one Google app covers Analytics, Search
// Console and Calendar; Microsoft ones (Outlook Calendar) fall back to
// MICROSOFT_OAUTH_CLIENT_ID and _SECRET the same way.
func OAuthAppsFromEnv(r *Registry) map[string]OAuthApp {
	apps := map[string]OAuthApp{}
	for _, d := range r.All() {
		if !d.Supports(AuthOAuth2) || d.OAuth == nil {
			continue
		}
		prefix := EnvPrefix(d.ID)
		scopes := strings.Fields(os.Getenv(prefix + "SCOPES"))
		if d.OAuth.KeyExchange {
			// Nothing to register: the provider makes a key for whoever signs in.
			apps[d.ID] = OAuthApp{}
		} else if id := os.Getenv(prefix + "CLIENT_ID"); id != "" {
			apps[d.ID] = OAuthApp{ClientID: id, ClientSecret: os.Getenv(prefix + "CLIENT_SECRET"), Scopes: scopes, AuthURL: os.Getenv(prefix + "AUTH_URL")}
		} else if id := os.Getenv("GOOGLE_OAUTH_CLIENT_ID"); id != "" && IsGoogle(d) {
			apps[d.ID] = OAuthApp{ClientID: id, ClientSecret: os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"), Scopes: scopes}
		} else if id := os.Getenv("MICROSOFT_OAUTH_CLIENT_ID"); id != "" && IsMicrosoft(d) {
			apps[d.ID] = OAuthApp{ClientID: id, ClientSecret: os.Getenv("MICROSOFT_OAUTH_CLIENT_SECRET"), Scopes: scopes}
		}
	}
	return apps
}

// EnvPrefix is the start of a connector's OAuth app variables, e.g.
// CONNECTOR_CALENDLY_ for CONNECTOR_CALENDLY_CLIENT_ID.
func EnvPrefix(id string) string {
	return "CONNECTOR_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_"
}

// OAuthHint says what a server needs before a connector's OAuth works.
func OAuthHint(d Definition) string {
	p := EnvPrefix(d.ID)
	switch {
	case d.OAuth != nil && d.OAuth.KeyExchange:
		return "nothing: " + d.Name + " needs no app"
	case d.OAuth != nil && d.OAuth.ClientMetadata:
		return "set PUBLIC_URL to this server's public https address, so " + d.Name + " can read its sign-in details"
	case IsGoogle(d):
		return "set GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET (or " + p + "CLIENT_ID and " + p + "CLIENT_SECRET)"
	case IsMicrosoft(d):
		return "set MICROSOFT_OAUTH_CLIENT_ID and MICROSOFT_OAUTH_CLIENT_SECRET (or " + p + "CLIENT_ID and " + p + "CLIENT_SECRET)"
	}
	return "set " + p + "CLIENT_ID and " + p + "CLIENT_SECRET"
}

// MetadataClientPath is where the server hosts a connector's OAuth client
// metadata document, relative to its public URL.
func MetadataClientPath(id string) string {
	return "/v1/integrations/oauth/clients/" + id + ".json"
}

// AddMetadataClients sets up the connectors whose provider reads a hosted
// client metadata document. The provider has to fetch that document, so
// it only works on a public https address.
func AddMetadataClients(apps map[string]OAuthApp, r *Registry, publicURL string) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "localhost" || strings.HasPrefix(u.Hostname(), "127.") {
		return
	}
	for _, d := range r.All() {
		if _, set := apps[d.ID]; d.Supports(AuthOAuth2) && d.OAuth != nil && d.OAuth.ClientMetadata && !set {
			apps[d.ID] = OAuthApp{ClientID: strings.TrimRight(publicURL, "/") + MetadataClientPath(d.ID)}
		}
	}
}

// IsGoogle reports whether a connector signs in with Google.
func IsGoogle(d Definition) bool {
	return d.OAuth != nil && strings.HasPrefix(d.OAuth.AuthURL, "https://accounts.google.com/")
}

// IsMicrosoft reports whether a connector signs in with Microsoft.
func IsMicrosoft(d Definition) bool {
	return d.OAuth != nil && strings.HasPrefix(d.OAuth.AuthURL, "https://login.microsoftonline.com/")
}

// PKCEChallenge derives the S256 code challenge from a verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthURL is the provider's consent screen for this connector.
func AuthURL(d Definition, app OAuthApp, redirectURI, state, verifier string) string {
	if d.OAuth.KeyExchange {
		// The provider passes no state back, so the callback URL carries it.
		q := url.Values{
			"callback_url":          {redirectURI + "?" + url.Values{"state": {state}}.Encode()},
			"code_challenge":        {PKCEChallenge(verifier)},
			"code_challenge_method": {"S256"},
		}
		return d.OAuth.AuthURL + "?" + q.Encode()
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {app.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {PKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	scopes := d.OAuth.Scopes
	if len(app.Scopes) > 0 {
		scopes = app.Scopes
	}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	for k, v := range d.OAuth.Params {
		q.Set(k, v)
	}
	base := d.OAuth.AuthURL
	if app.AuthURL != "" {
		base = app.AuthURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	// An install link may carry its own client id and redirect: keep them.
	have := u.Query()
	for k, v := range q {
		if !have.Has(k) {
			have[k] = v
		}
	}
	u.RawQuery = have.Encode()
	return u.String()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// Exchange trades a code for tokens, returned as credentials.
func Exchange(ctx context.Context, cl *Client, d Definition, app OAuthApp, code, redirectURI, verifier string) (Creds, error) {
	if d.OAuth.KeyExchange {
		return keyExchange(ctx, cl, d, code, verifier)
	}
	return tokenCall(ctx, cl, d, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}, "code_verifier": {verifier},
	}, "")
}

// Refresh renews expired OAuth credentials. It returns creds unchanged when
// they are still good for at least five minutes.
func Refresh(ctx context.Context, cl *Client, d Definition, app OAuthApp, creds Creds) (Creds, bool, error) {
	if exp, err := strconv.ParseInt(creds["expires_at"], 10, 64); err == nil && time.Until(time.Unix(exp, 0)) > 5*time.Minute {
		return creds, false, nil
	}
	if creds["refresh_token"] == "" {
		return creds, false, nil
	}
	out, err := tokenCall(ctx, cl, d, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {creds["refresh_token"]},
		"client_id": {app.ClientID}, "client_secret": {app.ClientSecret},
	}, creds["refresh_token"])
	if err != nil {
		return nil, false, err
	}
	// Keep what the connector stored beside the tokens, such as a region.
	for k, v := range creds {
		if _, ok := out[k]; !ok && k != "expires_at" {
			out[k] = v
		}
	}
	return out, true, nil
}

func tokenCall(ctx context.Context, cl *Client, d Definition, form url.Values, oldRefresh string) (Creds, error) {
	if form.Get("client_secret") == "" {
		// Public clients (PKCE only) send no secret.
		form.Del("client_secret")
	}
	var user, pass string
	if d.OAuth.SecretOnly {
		user = form.Get("client_secret")
		for _, k := range []string{"client_id", "client_secret", "redirect_uri", "code_verifier"} {
			form.Del(k)
		}
	} else if d.OAuth.BasicAuth {
		user, pass = form.Get("client_id"), form.Get("client_secret")
		form.Del("client_id")
		form.Del("client_secret")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(d.ID+".token", d.OAuth.TokenURL), strings.NewReader(form.Encode()))
	if d.OAuth.BasicAuth || d.OAuth.SecretOnly {
		req.SetBasicAuth(user, pass)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, Errf(ErrUnavailable, "could not reach %s: %v", d.Name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var t tokenResponse
	_ = json.Unmarshal(body, &t)
	if resp.StatusCode != http.StatusOK || t.AccessToken == "" {
		if t.Error == "invalid_grant" {
			return nil, Errf(ErrAuth, "%s no longer accepts this connection; connect it again", d.Name)
		}
		if t.Error != "" {
			return nil, Errf(ErrAuth, "%s refused: %s %s", d.Name, t.Error, t.Description)
		}
		return nil, HTTPError(d.Name, resp.StatusCode, string(body))
	}
	out := Creds{"access_token": t.AccessToken, "refresh_token": t.RefreshToken}
	if out["refresh_token"] == "" {
		out["refresh_token"] = oldRefresh
	}
	life := time.Duration(t.ExpiresIn) * time.Second
	if life <= 0 {
		life = d.OAuth.Lifetime
	}
	if life > 0 {
		out["expires_at"] = strconv.FormatInt(time.Now().Add(life).Unix(), 10)
	}
	return out, nil
}

// keyExchange trades the code for an API key on the person's own account,
// saved as the connection's api_key.
func keyExchange(ctx context.Context, cl *Client, d Definition, code, verifier string) (Creds, error) {
	body, _ := json.Marshal(map[string]string{"code": code, "code_verifier": verifier, "code_challenge_method": "S256"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(d.ID+".token", d.OAuth.TokenURL), strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Key string `json:"key"`
	}
	if _, err := DoJSON(ctx, cl, d.Name, req, &out); err != nil {
		return nil, err
	}
	if out.Key == "" {
		return nil, Errf(ErrAuth, "%s did not hand back a key; connect it again", d.Name)
	}
	return Creds{"api_key": out.Key}, nil
}
