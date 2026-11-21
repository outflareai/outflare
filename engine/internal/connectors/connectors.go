// Package connectors connects a workspace to outside services: email
// verification, other sequencers, mailbox suppliers, DNS hosts and CRMs.
//
// A connector is a definition in code (what it is, how it signs in, what it
// can do) plus an implementation. A workspace's connected accounts are
// integrations: rows holding the sealed credentials. Everything else in the
// engine opens an integration through Store.Open and gets a ready client,
// without unsealing credentials itself.
package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Categories group connectors in the catalog.
const (
	CategoryVerification = "email_verification"
	CategorySequencer    = "sequencer"
	CategoryMailbox      = "mailbox_provider"
	CategoryDNS          = "dns"
	CategoryTags         = "tags" // tag managers that put scripts on a website
	CategoryCRM          = "crm"
	CategoryData         = "data"         // lead search and enrichment
	CategoryCalendar     = "calendar"     // meetings booked
	CategoryRegistrar    = "registrar"    // buying domains
	CategoryWarehouse    = "warehouse"    // exporting events and reports
	CategoryNotification = "notification" // Slack, Teams, webhooks
	CategoryModel        = "model"        // AI models that plan and run missions
	CategorySocial       = "social"       // Reddit, X, LinkedIn, Engain: find posts and act on them
	CategoryCMS          = "cms"          // WordPress, Ghost: publish posts and pages on the brand's site
	CategoryFraud        = "fraud"        // IP reputation lookups for tracker clicks, on the workspace's own key
	CategoryMessaging    = "messaging"    // Telegram: the company's own chat accounts, read into the CRM
	// Anything else: a generic HTTP API or an MCP server registered by the
	// workspace, so the long tail needs no code in the engine.
	CategoryCustom = "custom"
)

// Auth methods. A connector can offer more than one; the person picks.
const (
	AuthAPIKey = "api_key" // one or more pasted fields
	AuthOAuth2 = "oauth2"  // consent screen; needs an OAuth app configured on the server
	AuthNone   = "none"    // built in, nothing to connect
)

// Capabilities a connector can have. Other slices add their own.
const (
	CapVerify          = "verify"           // implements Verifier
	CapIPScore         = "ip.score"         // implements IPScorer
	CapImportCampaigns = "import.campaigns" // implements Importer
	CapImportLeads     = "import.leads"
	CapImportMailboxes = "import.mailboxes"
	// CapMailboxSignIn: the connection signs mailboxes in to SMTP and IMAP
	// with OAuth instead of a password (implements MailSignIn).
	CapMailboxSignIn = "mailboxes.sign_in"
	CapImportCRM     = "import.crm"    // implements CRMImporter
	CapSocialSearch  = "social.search" // implements SocialSearcher
	CapSocialReply   = "social.reply"
	// CapRedditCampaigns: posts, comments, replies and upvotes on Reddit
	// through a managed account network (Engain), with status reads.
	CapRedditCampaigns = "reddit.campaigns"
)

// AccountSignIn is a connector that runs on the workspace's own app: the
// connection holds the app's id and secret, and signing in to an account
// through that app (not an OAuth app on the server) adds the account.
type AccountSignIn interface {
	SignInURL(cl *Client, creds Creds, redirectURI, state string) string
	// SignIn trades the consent page's code and returns the connection's
	// new credentials and the account.
	SignIn(ctx context.Context, cl *Client, creds Creds, code, redirectURI string) (Creds, Account, error)
}

// AccountSignInOAuth1 is an account sign-in through the workspace's own
// app over OAuth 1.0a (X Ads): the server asks the provider for a request
// token with its callback, sends the person to the consent page, and on
// the way back trades the request token and the verifier for the
// account's access token.
type AccountSignInOAuth1 interface {
	// SignInRequest returns the consent page and the request token and
	// its secret, which the server keeps until the person comes back.
	SignInRequest(ctx context.Context, cl *Client, creds Creds, callback string) (link, token, secret string, err error)
	// SignInAccess trades them for the connection's new credentials and
	// the account.
	SignInAccess(ctx context.Context, cl *Client, creds Creds, token, secret, verifier string) (Creds, Account, error)
}

// Credentials an OAuth 1.0a sign-in keeps on the connection between the
// request and the person coming back.
const (
	OAuth1RequestToken  = "oauth1_request_token"
	OAuth1RequestSecret = "oauth1_request_secret"
)

// Status of a connector in the catalog.
const (
	Available = "available"
	Planned   = "planned" // listed so people can see what is coming; cannot be connected yet
)

// Field is one thing a person pastes to connect.
type Field struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret"`
	Optional    bool   `json:"optional"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
}

// OAuth describes a connector's consent flow. The client id and secret
// come from the server's environment (CONNECTOR_<ID>_CLIENT_ID and
// CONNECTOR_<ID>_CLIENT_SECRET), never from the catalog.
type OAuth struct {
	AuthURL  string   `json:"-"`
	TokenURL string   `json:"-"`
	Scopes   []string `json:"scopes"`
	// Extra query parameters for the consent screen.
	Params map[string]string `json:"-"`
	// ClientMetadata: the provider reads the client from a metadata
	// document the server hosts (OAuth Client ID Metadata Document), so the
	// client id is that document's URL and nobody registers an app.
	ClientMetadata bool `json:"client_metadata,omitempty"`
	// BasicAuth sends the client id and secret to the token endpoint as
	// HTTP Basic auth instead of form fields (Reddit requires it).
	BasicAuth bool `json:"-"`
	// SecretOnly authenticates the token call with the client secret
	// alone, as the Basic auth user, and sends only the grant: Stripe Apps
	// take the app's own secret key that way and refuse other fields.
	SecretOnly bool `json:"-"`
	// Lifetime is how long an access token lasts when the provider's
	// token answer doesn't say (Stripe Apps: one hour).
	Lifetime time.Duration `json:"-"`
	// KeyExchange: the provider trades the code for an API key that
	// belongs to the person's own account (OpenRouter's PKCE flow). No app
	// is registered, the code comes back to a callback URL that carries
	// the state, and the connection stores the key like a pasted one.
	KeyExchange bool `json:"key_exchange,omitempty"`
	// AppIDAuthCode: the consent screen names the app app_id, the callback
	// carries auth_code, and the code is traded in a JSON body of app_id,
	// secret and auth_code whose answer sits in a {code, message, data}
	// envelope (TikTok's Marketing API). The token has no expiry; the
	// advertiser accounts it was granted come back with it.
	AppIDAuthCode bool `json:"-"`
	// KeepClientID stores the app's client id beside the tokens as
	// client_id, refreshed with them, for APIs that want it on every call
	// next to the workspace's token (Amazon Ads'
	// Amazon-Advertising-API-ClientId header). It is the app's public id,
	// not a secret, and only names the app the workspace signed in through.
	KeepClientID bool `json:"-"`
	// IDToken keeps the OpenID Connect ID token the token endpoint returns
	// beside the tokens as id_token, for connectors whose account is read
	// from it (a mailbox sign-in's token is for the mail servers only).
	IDToken bool `json:"-"`
}

// Definition is a connector's catalog entry.
type Definition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Website     string `json:"website"`
	DocsURL     string `json:"docs_url"`
	KeyURL      string `json:"key_url,omitempty"` // where to find the API key
	// Auth is the preferred way to connect: OAuth when the vendor has
	// it. AuthMethods lists every way, preferred first; empty means
	// only Auth. Fields are what the api_key method asks for, OAuth is
	// the oauth2 method's consent screen. Neither is in the API: the
	// catalog's auth_options lists each method, whether this server can
	// use it and which is recommended, so clients read only that.
	Auth         string   `json:"-"`
	AuthMethods  []string `json:"-"`
	Fields       []Field  `json:"fields"`
	OAuth        *OAuth   `json:"oauth,omitempty"`
	Capabilities []string `json:"capabilities"`
	Status       string   `json:"status"`
	// Pricing note shown in the catalog, e.g. "Pay per credit".
	Pricing string `json:"pricing,omitempty"`
	// Short labels on the catalog card, each with a sentence that says
	// what it means ("Router", "ChatGPT plan sign-in soon").
	Tags []Tag `json:"tags,omitempty"`
	// Other OAuth connectors the same sign-in connects too, when the
	// person grants their scopes (Google Analytics and Search Console).
	AlsoConnects []string `json:"also_connects,omitempty"`
	// Who built it and how far it is trusted. Connectors written in the
	// engine are Outflare's own; manifests in integrations/ name their
	// publisher, and integrations/publishers.yaml says who is verified.
	Publisher *Publisher `json:"publisher,omitempty"`
	Trust     string     `json:"trust"`
	// BrandKind is the kind a new connection gets unless the person picks
	// another (plans/brands/README.md, "Connections and brands"): brand
	// (the whole connection is one brand's), split (its accounts or
	// properties are assigned to brands one by one) or shared (a tool
	// every brand uses). Empty takes the category's default (KindFor).
	BrandKind string `json:"brand_kind"`
}

// Connection kinds: who a connection's data belongs to.
const (
	KindBrand  = "brand"
	KindSplit  = "split"
	KindShared = "shared"
)

// BrandKinds are the kinds a connection can have.
var BrandKinds = []string{KindBrand, KindSplit, KindShared}

// KindFor is a connector's default kind: its own BrandKind, else by
// category. Analytics and ads read many sites or accounts with one
// sign-in, so they are split; models, notifications, data, verification,
// DNS, SEO data tools (DataForSEO, Ahrefs) and the accounts that make
// pictures, video and voice serve every brand, and so do
// mailbox sign-ins, whose mailboxes each carry their own brand (none is
// shared); the rest carry one brand's data.
func KindFor(d Definition) string {
	if d.BrandKind != "" {
		return d.BrandKind
	}
	switch d.Category {
	case CategoryAnalytics, CategoryAds:
		return KindSplit
	case CategoryModel, CategoryNotification, CategoryData, CategorySignals, CategoryVerification, CategoryDNS, CategoryRegistrar, CategorySEO, CategoryMailbox, CategoryFraud, CategoryMedia:
		return KindShared
	}
	return KindBrand
}

// Tag is a label on a connector's catalog card.
type Tag struct {
	Label string `json:"label"`
	Help  string `json:"help"`
}

// Publisher is who built and looks after a connector.
type Publisher struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Website string `json:"website,omitempty"`
}

// Trust levels, shown as a badge wherever a connector appears.
const (
	TrustOutflare  = "outflare"  // built by Outflare
	TrustVerified  = "verified"  // the publisher proved its domain and passed review
	TrustCommunity = "community" // reviewed and merged, publisher not verified
)

// Methods returns every way to connect, recommended first.
func (d Definition) Methods() []string {
	if len(d.AuthMethods) > 0 {
		return d.AuthMethods
	}
	return []string{d.Auth}
}

// Supports reports whether the connector can connect with method.
func (d Definition) Supports(method string) bool {
	for _, m := range d.Methods() {
		if m == method {
			return true
		}
	}
	return false
}

// Both is AuthMethods for a connector that offers OAuth and an API key.
var Both = []string{AuthOAuth2, AuthAPIKey}

// Has reports whether the connector has a capability.
func (d Definition) Has(c string) bool {
	for _, x := range d.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// Token is the bearer credential in creds: the OAuth access token, or the
// pasted API key for connectors whose key is used the same way.
func Token(creds Creds) string {
	if t := creds["access_token"]; t != "" {
		return t
	}
	return creds["api_key"]
}

// Creds are an integration's credentials: the definition's field values,
// or for OAuth connectors access_token, refresh_token and expires_at.
type Creds map[string]string

// String, GoString and LogValue print the field names only, so credentials
// never end up in a log line or an error by accident.
func (c Creds) String() string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "creds[" + strings.Join(keys, " ") + "] (redacted)"
}

func (c Creds) GoString() string     { return c.String() }
func (c Creds) LogValue() slog.Value { return slog.StringValue(c.String()) }

// Account identifies the outside account an integration signed in to.
type Account struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	// Credits or quota left, when the service reports it.
	Credits *float64 `json:"credits,omitempty"`
	// Anything else worth showing, e.g. the plan.
	Details map[string]string `json:"details,omitempty"`
}

// MailSignIn is a connector whose connections sign mailboxes in to SMTP
// and IMAP with XOAUTH2: one person's sign-in (oauth2) or an admin
// connection that signs in every mailbox of an organisation (api_key).
type MailSignIn interface {
	// MailProvider is the mailbox provider its mailboxes use (google or
	// microsoft).
	MailProvider() string
	// MailToken returns an access token for the mailbox email, from an
	// opened connection's credentials.
	MailToken(ctx context.Context, cl *Client, method string, creds Creds, email string) (string, error)
}

// Connector is implemented by every available connector.
type Connector interface {
	Definition() Definition
	// Test signs in with creds and returns the account. It must not change
	// anything on the other side or spend credits.
	Test(ctx context.Context, c *Client, creds Creds) (Account, error)
}

// Client is the HTTP client connectors use. Tests point BaseURLs at fakes.
type Client struct {
	HTTP *http.Client
	// BaseURLs overrides a connector's API base URL by connector id.
	BaseURLs map[string]string
}

// Base returns the connector's base URL, or the override.
func (c *Client) Base(id, def string) string {
	if c != nil && c.BaseURLs != nil {
		if u, ok := c.BaseURLs[id]; ok {
			return strings.TrimRight(u, "/")
		}
	}
	return def
}

// Do runs req with a timeout.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.DoWithin(req, 0)
}

// DoWithin runs req and waits up to wait for the whole answer, for a call
// the service itself takes long to answer (a page read in a browser). Zero
// keeps the client's own timeout, or 45 seconds.
func (c *Client) DoWithin(req *http.Request, wait time.Duration) (*http.Response, error) {
	h := http.DefaultClient
	if c != nil && c.HTTP != nil {
		h = c.HTTP
	}
	if wait > 0 {
		h = &http.Client{Timeout: wait, Transport: waitingTransport(h.Transport, wait), CheckRedirect: h.CheckRedirect, Jar: h.Jar}
	} else if h.Timeout == 0 {
		h = &http.Client{Timeout: 45 * time.Second, Transport: h.Transport}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "outflare")
	}
	resp, err := h.Do(req)
	var ue *url.Error
	if errors.As(err, &ue) {
		// Some services take the API key in the query string, and Go puts
		// the whole URL in the error, which ends up in last_error, the API
		// and the logs. Keep only the address.
		ue.URL = redactURL(ue.URL)
	}
	return resp, err
}

// patientTransports holds, per shared transport, a copy of it with no
// limit on the wait for response headers, so every long call reuses one
// copy and its pooled connections.
var patientTransports sync.Map // *http.Transport -> *http.Transport

// waitingTransport is the transport for a call allowed to wait up to wait.
// A transport's own limit on the wait for response headers (netguard's 30
// seconds) would cut off a call the service itself holds longer (Treg
// answers after up to 60 seconds, a page read or long poll after up to
// 60): the cut-off call reads as a timeout although it was allowed more.
// When the call's wait is longer, it goes through a copy of the transport
// without that limit; the client's Timeout (wait) still bounds the whole
// call. The copy keeps everything else, the dialer that refuses private
// addresses among it.
func waitingTransport(rt http.RoundTripper, wait time.Duration) http.RoundTripper {
	t, ok := rt.(*http.Transport)
	if !ok || t.ResponseHeaderTimeout <= 0 || wait <= t.ResponseHeaderTimeout {
		return rt
	}
	if p, ok := patientTransports.Load(t); ok {
		return p.(*http.Transport)
	}
	p := t.Clone()
	p.ResponseHeaderTimeout = 0
	got, _ := patientTransports.LoadOrStore(t, p)
	return got.(*http.Transport)
}

// redactURL drops a URL's query, fragment and user info.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(url hidden)"
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

// DoJSON runs req and decodes a JSON answer into v (nil skips decoding).
// Failures come back as *Error with a kind; name is the service's name for
// messages. Answers over 8 MB are cut off.
func DoJSON(ctx context.Context, cl *Client, name string, req *http.Request, v any) (int, error) {
	return DoJSONWithin(ctx, cl, name, req, v, 0)
}

// DoJSONWithin is DoJSON, waiting up to wait for the whole answer, for a
// call the service itself takes long to answer (an AI assistant searching
// the web and writing). Zero keeps the client's own timeout. A call that
// ran out of time comes back as an unavailable *Error that IsTimeout
// recognises.
func DoJSONWithin(ctx context.Context, cl *Client, name string, req *http.Request, v any, wait time.Duration) (int, error) {
	return doJSON(ctx, cl, name, req, v, wait, false)
}

// DoJSONRateLimited is DoJSONWithin for a service whose HTTP 429 is a rate
// limit and never the account's quota (HTTPErrorFor).
func DoJSONRateLimited(ctx context.Context, cl *Client, name string, req *http.Request, v any, wait time.Duration) (int, error) {
	return doJSON(ctx, cl, name, req, v, wait, true)
}

func doJSON(ctx context.Context, cl *Client, name string, req *http.Request, v any, wait time.Duration, rateLimits bool) (int, error) {
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.DoWithin(req, wait)
	if err != nil {
		e := Errf(ErrUnavailable, "could not reach %s: %v", name, err)
		e.timeout = timedOut(err)
		return 0, e
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		e := Errf(ErrUnavailable, "reading the %s response: %v", name, err)
		e.timeout = timedOut(err)
		return resp.StatusCode, e
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if rateLimits {
			return resp.StatusCode, HTTPErrorFor(name, resp, string(body))
		}
		return resp.StatusCode, HTTPError(name, resp.StatusCode, string(body))
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return resp.StatusCode, Errf(ErrUnavailable, "%s returned something that is not JSON", name)
		}
	}
	return resp.StatusCode, nil
}

// Error is a failure explained in words a person can act on.
type Error struct {
	// auth: the credentials are wrong or revoked. quota: out of credits or
	// rate limited. unavailable: the service is down or unreachable.
	// invalid: the request was refused.
	Kind    string
	Message string
	// Code is the service's own short code for the failure
	// ("not_allowed_token_type"), when it gave one: an enum of the service's,
	// never text. It lets an event or a log line say why without quoting
	// the service's answer.
	Code string
	// timeout marks a call that got no answer in time, as opposed to a
	// service that is down.
	timeout bool
	// expired marks an OAuth sign-in the provider no longer honours (its
	// refresh token expired or was revoked): only signing in again mends it.
	expired bool
	// busy marks a provider that has no room for the call right now and
	// charged nothing; retryAfter is when it said to ask again. throttled:
	// it has no room because this account is calling it too fast.
	busy       bool
	throttled  bool
	retryAfter time.Duration
}

// SignInExpired says whether err is a provider refusing to renew an OAuth
// sign-in, so the person has to sign in again.
func SignInExpired(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.expired
}

// timedOut says whether a transport error is a call running out of time.
func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// IsTimeout says whether err is a call to a connected service that got no
// answer in time.
func IsTimeout(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.timeout
}

func (e *Error) Error() string { return e.Message }

// ErrorCode is the error's code in the API's catalog (connector_quota,
// connector_unavailable ...). A job that fails with it records that code on
// its job.failed event instead of the generic job_error.
func (e *Error) ErrorCode() string { return "connector_" + e.Kind }

// Error kinds.
const (
	ErrAuth        = "auth"
	ErrQuota       = "quota"
	ErrUnavailable = "unavailable"
	ErrInvalid     = "invalid"
)

// ProviderCode is the service's own code for err ("" when it gave none or
// it isn't a plain code): lowercase letters, digits and underscores, at most
// 60 characters, so it is safe on an event.
func ProviderCode(err error) string {
	var e *Error
	if !errors.As(err, &e) || e.Code == "" || len(e.Code) > 60 {
		return ""
	}
	for _, r := range e.Code {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return ""
		}
	}
	return e.Code
}

// Errf builds an Error.
func Errf(kind, format string, a ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, a...)}
}

// TimedOutf builds an unavailable Error for a call that got no answer in
// time, which IsTimeout recognises: a connector that waits for a service's
// own job (submit, then ask for the result) uses it when the wait runs out.
func TimedOutf(format string, a ...any) *Error {
	e := Errf(ErrUnavailable, format, a...)
	e.timeout = true
	return e
}

// Busyf builds an unavailable Error for a provider that has no room for
// the call right now (its capacity is in use, or it asked for fewer calls)
// and charged nothing, which IsBusy recognises. retryAfter is when it said
// to ask again. It is not the account's quota: nothing needs fixing, the
// same call succeeds a moment later, and it says nothing about what was
// asked, so it is never a miss and never a verdict.
func Busyf(retryAfter time.Duration, format string, a ...any) *Error {
	e := Errf(ErrUnavailable, format, a...)
	e.busy, e.retryAfter = true, max(retryAfter, 0)
	return e
}

// Throttledf builds a busy Error (Busyf) for a provider that turned the
// call down because this account is calling it too fast: a rate limit.
// IsThrottled recognises it, so a caller that paces its calls slows down.
// A provider whose capacity is used up by everyone else is Busyf alone:
// calling it more slowly would not have helped.
func Throttledf(retryAfter time.Duration, format string, a ...any) *Error {
	e := Busyf(retryAfter, format, a...)
	e.throttled = true
	return e
}

// IsThrottled says whether err is a provider asking this account for
// fewer calls (Throttledf).
func IsThrottled(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.throttled
}

// IsBusy says whether err is a provider with no room for the call right
// now (Busyf, Throttledf), and how long it said to wait before asking
// again.
func IsBusy(err error) (time.Duration, bool) {
	var e *Error
	if errors.As(err, &e) && e.busy {
		return e.retryAfter, true
	}
	return 0, false
}

// KindOf returns an error's kind, or unavailable for unexplained errors.
func KindOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ErrUnavailable
}

// HTTPError turns a failed response status into an Error.
func HTTPError(name string, status int, body string) *Error {
	body = strings.TrimSpace(body)
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	switch {
	case status == 401 || status == 403:
		return Errf(ErrAuth, "%s refused the API key (HTTP %d). Check the key, or create a new one.", name, status)
	case status == 402 || status == 429:
		return Errf(ErrQuota, "%s says the account is out of credits or rate limited (HTTP %d).", name, status)
	case status >= 500:
		return Errf(ErrUnavailable, "%s is not responding properly (HTTP %d). Try again in a few minutes.", name, status)
	}
	return Errf(ErrInvalid, "%s refused the request (HTTP %d): %s", name, status, body)
}

// HTTPErrorFor is HTTPError for a service whose HTTP 429 is a rate limit
// and never the account's quota (402 is): it is busy (Throttledf), with
// how long its Retry-After header said to wait. Nothing needs fixing and
// the same call succeeds a little later, so a caller waits it out, slowing
// down, and never stops for a person.
func HTTPErrorFor(name string, resp *http.Response, body string) *Error {
	if resp.StatusCode == http.StatusTooManyRequests {
		return Throttledf(RetryAfter(resp.Header, time.Now()), "%s asked for fewer calls (HTTP %d).", name, resp.StatusCode)
	}
	return HTTPError(name, resp.StatusCode, body)
}

// maxRetryAfter bounds what a provider can ask a caller to wait.
const maxRetryAfter = 15 * time.Minute

// RetryAfter reads a Retry-After header, in seconds or as an HTTP date:
// zero when there is none or it can't be read, and never more than 15
// minutes.
func RetryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), maxRetryAfter)
}

// Registry holds every connector the engine knows.
type Registry struct {
	byID    map[string]Connector
	planned map[string]Definition
}

// NewRegistry builds a registry from available connectors and planned
// definitions.
func NewRegistry(available []Connector, planned []Definition) *Registry {
	r := &Registry{byID: map[string]Connector{}, planned: map[string]Definition{}}
	for _, c := range available {
		r.byID[c.Definition().ID] = c
	}
	for _, d := range planned {
		if _, ok := r.byID[d.ID]; !ok {
			d.Status = Planned
			r.planned[d.ID] = d
		}
	}
	return r
}

// Get returns an available connector.
func (r *Registry) Get(id string) (Connector, bool) {
	c, ok := r.byID[id]
	return c, ok
}

// Definition returns any connector's definition, available or planned.
func (r *Registry) Definition(id string) (Definition, bool) {
	if c, ok := r.byID[id]; ok {
		d := c.Definition()
		d.Status = Available
		return d.normal(), true
	}
	d, ok := r.planned[id]
	return d.normal(), ok
}

// normal fills AuthMethods, so callers always get the full list, and
// marks connectors with no publisher as Outflare's own.
func (d Definition) normal() Definition {
	d.AuthMethods = d.Methods()
	d.BrandKind = KindFor(d)
	if d.Trust == "" {
		d.Trust = TrustOutflare
	}
	return d
}

// All returns every definition, by category then name.
func (r *Registry) All() []Definition {
	out := make([]Definition, 0, len(r.byID)+len(r.planned))
	for _, c := range r.byID {
		d := c.Definition()
		d.Status = Available
		out = append(out, d.normal())
	}
	for _, d := range r.planned {
		out = append(out, d.normal())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status == Available
		}
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// CheckFields validates creds against the definition's fields and trims them.
func CheckFields(d Definition, creds Creds) (Creds, error) {
	out := Creds{}
	for _, f := range d.Fields {
		v := strings.TrimSpace(creds[f.Name])
		if v == "" && !f.Optional {
			return nil, Errf(ErrInvalid, "%s is required", f.Label)
		}
		if len(v) > 4096 {
			return nil, Errf(ErrInvalid, "%s is too long", f.Label)
		}
		if v != "" {
			out[f.Name] = v
		}
	}
	return out, nil
}

// Verification statuses, the same for every provider.
const (
	Deliverable   = "deliverable"   // the mailbox exists and accepts mail
	Undeliverable = "undeliverable" // it does not exist or cannot receive mail: never send
	Risky         = "risky"         // catch-all, disposable, role or full: may bounce
	Unknown       = "unknown"       // the provider could not tell
)

// Verification is one checked address, in provider-neutral terms.
type Verification struct {
	Email      string `json:"email"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	CatchAll   bool   `json:"catch_all"`
	Disposable bool   `json:"disposable"`
	Role       bool   `json:"role"`
	Free       bool   `json:"free"`
	Suggestion string `json:"suggestion,omitempty"` // a likely correction, e.g. gmail.com for gmial.com
	Score      *int   `json:"score,omitempty"`      // 0–100 when the provider gives one
	// NotCatchAll is the provider saying outright that the domain does not
	// accept every address. CatchAll false alone does not say that: most
	// providers only flag a domain that does. It is for a caller deciding
	// whether a second opinion is needed, and is not stored.
	NotCatchAll bool `json:"-"`
	// The provider's own status words, kept for people who know them.
	ProviderStatus string `json:"provider_status"`
	// CostMicros is what the provider said the check cost, in millionths
	// of a dollar, over every call it took: 0 when it said nothing. It is
	// for booking the check and is not stored.
	CostMicros float64 `json:"-"`
}

// IPScore is one address's reputation, in provider-neutral terms: a risk
// score and the kinds of network it comes from.
type IPScore struct {
	// Score is 0 (clean) to 100 (fraud) when the provider gives one.
	Score *int `json:"score,omitempty"`
	// Proxy, VPN and Tor: the address hides another; Hosting: a data
	// centre or cloud network; Crawler: a known bot.
	Proxy   bool `json:"proxy"`
	VPN     bool `json:"vpn"`
	Tor     bool `json:"tor"`
	Hosting bool `json:"hosting"`
	Crawler bool `json:"crawler"`
}

// IPScorer looks up an address's reputation. Each call spends the
// workspace's own credits with the provider.
type IPScorer interface {
	Connector
	ScoreIP(ctx context.Context, c *Client, creds Creds, ip, userAgent string) (IPScore, error)
}

// Verifier checks email addresses. Verify spends the provider's credits.
type Verifier interface {
	Connector
	Verify(ctx context.Context, c *Client, creds Creds, email string) (Verification, error)
}

// FallbackVerifier is a Verifier whose first check can be unavailable (a
// provider behind a gateway that errors or is still working). VerifyWith
// is Verify, except that when that first check gave no answer it asks
// fallback, another verifier of the workspace's own, before going on:
//
//   - fallback says undeliverable: undeliverable.
//   - fallback says deliverable and that the domain does not accept every
//     address (Verification.NotCatchAll): deliverable.
//   - fallback says deliverable or risky on a domain that accepts every
//     address, or that it did not confirm does not: the connector's own
//     second opinion decides, as in Verify.
//   - fallback says risky on a domain it confirmed does not accept every
//     address: risky.
//   - fallback says unknown, or fails: as Verify.
//
// A real answer from the first check (invalid, accept-all, unknown) never
// asks fallback. A nil fallback is Verify.
type FallbackVerifier interface {
	VerifyWith(ctx context.Context, c *Client, creds Creds, email string, fallback func(ctx context.Context, email string) (Verification, error)) (Verification, error)
}

// AccountsVerifier is a FallbackVerifier that can use several of the
// workspace's own connections to it for one check, such as two accounts at
// a gateway. VerifyAcross is VerifyWith with every account's credentials,
// the workspace's chosen verifier first: the accounts take turns, and one
// that is refused, out of balance or has no room for a call hands that
// same call to the next. account is the index of the account whose answer
// decided, or the error came from.
type AccountsVerifier interface {
	VerifyAcross(ctx context.Context, c *Client, accounts []Creds, email string, fallback func(ctx context.Context, email string) (Verification, error)) (v Verification, account int, err error)
}

// SlowVerifier is a Verifier that names how long one address may take
// when that is longer than the verification service's own limit: a
// gateway that holds a call for a minute and is asked again when it has
// no room.
type SlowVerifier interface {
	VerifyWait() time.Duration
}

// Importer reads another sequencer's campaigns, leads and sending
// mailboxes so a workspace can switch over. It only reads. Paging is the
// connector's job: each method returns everything, calling yield per item
// so large accounts stream instead of loading into memory.
type Importer interface {
	Connector
	Campaigns(ctx context.Context, c *Client, creds Creds, yield func(ExternalCampaign) error) error
	CampaignLeads(ctx context.Context, c *Client, creds Creds, campaignID string, yield func(ExternalLead) error) error
	SendingAccounts(ctx context.Context, c *Client, creds Creds, yield func(ExternalMailbox) error) error
}

// CRMImporter reads another CRM so a workspace can move over: its setup
// (objects, fields, lists and how much is in each), its members and its
// records. It only reads.
type CRMImporter interface {
	Connector
	CRMPreview(ctx context.Context, c *Client, creds Creds, limit int) (CRMPreview, error)
	CRMMembers(ctx context.Context, c *Client, creds Creds) ([]CRMMember, error)
	// CRMRecords reads one page of an object's records, oldest first. A
	// page shorter than limit is the last.
	CRMRecords(ctx context.Context, c *Client, creds Creds, object string, offset, limit int) ([]CRMRecord, error)
}

// CRM field kinds: what a CRMField holds, whatever the CRM calls it.
const (
	CRMText        = "text"
	CRMNumber      = "number"
	CRMCheckbox    = "checkbox"
	CRMCurrency    = "currency"
	CRMDate        = "date"
	CRMTimestamp   = "timestamp"
	CRMRating      = "rating"
	CRMSelect      = "select"
	CRMStatus      = "status"
	CRMEmail       = "email"
	CRMPhone       = "phone"
	CRMDomain      = "domain"
	CRMReference   = "reference" // another record
	CRMMemberRef   = "member"    // a member of the CRM's workspace
	CRMLocationVal = "location"
	CRMPersonName  = "name"
	CRMUnsupported = "unsupported" // computed by the CRM, such as last interaction
)

// CRMRecord is one record and its values by field slug. A value's Go type
// follows its field's kind: string for text, select and status (the
// option's title), email, phone (E.164), domain, date (2006-01-02),
// timestamp (RFC 3339) and member (the member's id); float64 for number
// and rating; bool for checkbox; CRMMoney, CRMRef, CRMLocation or CRMName.
type CRMRecord struct {
	ID     string           `json:"id"`
	Values map[string][]any `json:"values"`
}

type CRMMoney struct {
	Amount   float64 `json:"amount"` // in whole units, such as 12.5
	Currency string  `json:"currency"`
}

type CRMRef struct {
	Object string `json:"object"`
	ID     string `json:"id"`
}

type CRMLocation struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"` // ISO 3166 code
}

type CRMName struct {
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
	Full  string `json:"full,omitempty"`
}

// CRMMember is a person who uses the CRM, matched to Outflare members by
// email.
type CRMMember struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// CRMPreview is another CRM's setup with counts. Counts stop at the limit
// the caller passed; Capped says so.
type CRMPreview struct {
	Workspace string      `json:"workspace"`
	Objects   []CRMObject `json:"objects"`
	Lists     []CRMList   `json:"lists"`
	Members   int         `json:"members"`
	Notes     CRMCount    `json:"notes"`
	Tasks     CRMCount    `json:"tasks"`
}

// CRMCount is a count that may have stopped early.
type CRMCount struct {
	Count  int  `json:"count"`
	Capped bool `json:"capped"`
}

// CRMObject is a record type, such as People, Companies or Deals.
type CRMObject struct {
	Slug    string     `json:"slug"`
	Name    string     `json:"name"`
	Plural  string     `json:"plural"`
	Records CRMCount   `json:"records"`
	Fields  []CRMField `json:"fields"`
}

// CRMList is a list or pipeline of records, with fields of its own.
type CRMList struct {
	Slug    string     `json:"slug"`
	Name    string     `json:"name"`
	Object  string     `json:"object"` // the record type its entries point at
	Entries CRMCount   `json:"entries"`
	Fields  []CRMField `json:"fields"`
}

// CRMField is one field and how often it is filled in the records counted.
type CRMField struct {
	Slug     string   `json:"slug"`
	Title    string   `json:"title"`
	Type     string   `json:"type"` // the CRM's own type name
	Kind     string   `json:"kind"` // one of the CRM field kinds
	System   bool     `json:"system"`
	Multiple bool     `json:"multiple"`
	Unique   bool     `json:"unique"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"` // select options or status stages, in order
	Target   []string `json:"target,omitempty"`  // record types a reference points at
	Filled   int      `json:"filled"`
}

// ExternalCampaign is a campaign at another platform, with its steps.
type ExternalCampaign struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // the platform's own word: active, paused, draft, completed
	// Sending window and limits, when the platform exposes them.
	Timezone      string         `json:"timezone,omitempty"`
	DailyLimit    int            `json:"daily_limit,omitempty"`
	Steps         []ExternalStep `json:"steps"`
	MailboxEmails []string       `json:"mailbox_emails"`
	LeadCount     int            `json:"lead_count"`
}

// ExternalStep is one email in a campaign. Variants are A/B versions.
type ExternalStep struct {
	DelayDays int      `json:"delay_days"` // days after the previous step; 0 on the first
	Subject   string   `json:"subject"`
	Body      string   `json:"body"` // plain text or HTML as the platform stores it; the importer converts variables
	Variants  []string `json:"variants,omitempty"`
}

// ExternalLead is a lead in a campaign and where it got to.
type ExternalLead struct {
	Email     string            `json:"email"`
	FirstName string            `json:"first_name,omitempty"`
	LastName  string            `json:"last_name,omitempty"`
	Company   string            `json:"company,omitempty"`
	Title     string            `json:"title,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	// Status: active, completed, replied, bounced, unsubscribed, paused.
	Status   string `json:"status"`
	StepDone int    `json:"step_done"`         // emails already sent to this lead in the campaign
	Mailbox  string `json:"mailbox,omitempty"` // the mailbox that sent to it, so follow-ups stay on it
}

// ExternalMailbox is a sending account at another platform. Credentials are
// never readable through these APIs; mailboxes are matched by email.
type ExternalMailbox struct {
	Email         string `json:"email"`
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider,omitempty"` // google, microsoft, smtp
	DailyLimit    int    `json:"daily_limit,omitempty"`
	WarmupEnabled bool   `json:"warmup_enabled"`
	Status        string `json:"status,omitempty"`
}

// CRMActivityImporter optionally brings record-linked notes and tasks with a CRM import.
// A short page is the last. Each source id is stable across retries and reruns.
type CRMActivityImporter interface {
	CRMActivities(ctx context.Context, c *Client, creds Creds, kind string, offset, limit int) ([]CRMActivityRecord, error)
}
type CRMActivityRecord struct {
	ID         string
	Kind       string
	Title      string
	Body       string
	Records    []CRMRef
	AssignedTo string
	DueAt      *time.Time
	Completed  bool
	CreatedAt  *time.Time
}

// CRMListImporter reads list entries and their per-entry values. An import
// represents each list as an entry object linked to its original parent record.
type CRMListImporter interface {
	CRMListEntries(ctx context.Context, c *Client, creds Creds, list string, offset, limit int) ([]CRMRecord, error)
}
