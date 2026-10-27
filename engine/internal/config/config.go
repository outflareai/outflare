// Package config reads engine settings from the environment.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Config struct {
	DatabaseURL string // DATABASE_URL, required
	HTTPAddr    string // HTTP_ADDR, default ":8080"
	WebDir      string // WEB_DIR, built web app to serve; empty disables
	PublicURL   string // PUBLIC_URL, external base URL, default http://localhost:8080
	Workers     int    // WORKERS, job worker goroutines, default 32
	SecretKey   string // OUTFLARE_SECRET_KEY, base64 32-byte key for credentials at rest, required
	// OUTFLARE_SECRET_KEY_PREVIOUS, comma-separated: keys being rotated out.
	// Stored secrets are re-encrypted with SecretKey at start-up.
	SecretKeyPrevious []string
	// Passkeys is PASSKEYS: on, off, or auto (the default), which turns
	// passkey sign-in on once PUBLIC_URL is an https address. Passkeys are
	// tied to that address, so "on" with a localhost URL is only for trying
	// them out.
	Passkeys string
	// ClientIPHeader is CLIENT_IP_HEADER: the header a proxy in front of the
	// server (Fly.io: Fly-Client-IP, Cloudflare: CF-Connecting-IP) puts the
	// visitor's address in. Leave it empty unless such a proxy is there.
	ClientIPHeader string
	// WebhooksAllowPrivate is WEBHOOKS_ALLOW_PRIVATE=true: lets webhooks
	// go to private, loopback and plain-http addresses, for self-hosted
	// servers whose receivers are on the same network. Off, webhooks only
	// go to public https URLs, so nobody can use them to reach your
	// internal network.
	WebhooksAllowPrivate bool
	// CrawlAllowPrivate is OUTFLARE_CRAWL_ALLOW_PRIVATE=true: lets the SEO
	// crawler reach private addresses and other ports, to audit an
	// intranet or a site in development. Off, it only visits public sites.
	CrawlAllowPrivate bool
	// ConnectorsAllowPrivate is OUTFLARE_CONNECTORS_ALLOW_PRIVATE=true: lets
	// connectors (a self-hosted Matomo, Umami, PostHog, EmailBison...) and
	// mailboxes' SMTP and IMAP servers be on private, loopback and
	// link-local addresses, for self-hosted servers whose tools are on the
	// same network. Off, they only reach the public internet, so nobody can
	// point one at your internal network.
	ConnectorsAllowPrivate bool
	// RateLimit is API_RATE_LIMIT: requests per minute each API token may
	// make, default 600; 0 turns the limit off.
	RateLimit int
	// LogRetentionDays is OUTFLARE_LOG_RETENTION_DAYS: how many days
	// request logs (the Logs page) are kept, default 30.
	LogRetentionDays int

	// Operational metrics for the operator's own dashboards and alerts
	// (docs/monitoring.md), off unless MetricsAddr is set.
	// MetricsAddr is OUTFLARE_METRICS_ADDR: a second, private listener
	// such as :9091 that serves Prometheus metrics at /metrics. Keep it off
	// the public internet. MetricsToken is OUTFLARE_METRICS_TOKEN: when set,
	// scrapers must send it as a bearer token. MetricsPprof is
	// OUTFLARE_METRICS_PPROF=true: also serve Go's profiler there.
	MetricsAddr  string
	MetricsToken string
	MetricsPprof bool

	// QueryConnections is OUTFLARE_QUERY_CONNECTIONS: how many Query runs
	// at once on this server, each on its own database connection
	// (default 3, 0 turns Query off). Count them in the database's
	// connection budget.
	QueryConnections int

	// OAuth apps for the "Connect with Google/Microsoft" consent screen.
	// Optional: admin-level connections (service account, Entra app) and
	// password mailboxes work without them.
	GoogleClientID        string // GOOGLE_OAUTH_CLIENT_ID
	GoogleClientSecret    string // GOOGLE_OAUTH_CLIENT_SECRET
	MicrosoftClientID     string // MICROSOFT_OAUTH_CLIENT_ID
	MicrosoftClientSecret string // MICROSOFT_OAUTH_CLIENT_SECRET

	// Product analytics for the web app, off unless a key is set. This is
	// the operator's own PostHog project, separate from the anonymous
	// telemetry: it records how people use this server's web app.
	PostHogKey  string // OUTFLARE_POSTHOG_KEY, a project key (phc_...)
	PostHogHost string // OUTFLARE_POSTHOG_HOST, default https://us.i.posthog.com
	// EventsWorkspace keeps the same events as product events in a
	// workspace on this server (OUTFLARE_EVENTS_WORKSPACE, its id), so the
	// operator analyses its own users with Outflare itself.
	EventsWorkspace string

	// Optional analytics storage (docs/adr/0010-analytics-storage.md):
	// ClickHouse keeps a copy of product events for analytics, and an
	// S3-compatible bucket (Cloudflare R2, Amazon S3, MinIO) keeps files
	// such as archived events and recordings. Postgres alone works without
	// them.
	ClickHouseURL string // OUTFLARE_CLICKHOUSE_URL
	S3            S3     // OUTFLARE_S3_*
	// EventRetentionDays is OUTFLARE_EVENT_RETENTION_DAYS: how long product
	// events stay in Postgres. Older ones are archived to the bucket (when
	// there is one) and removed. 0, the default, keeps them all.
	EventRetentionDays int

	// Role is OUTFLARE_ROLE: what this process does, so a large install
	// runs the parts as separate services that deploy and scale apart.
	// "all" (the default) does everything; "api" serves the app and API;
	// "ingest" takes website events and serves site.js; "worker" runs the
	// job queue and background loops.
	Role Role

	// The hosted MCP server at /mcp, so AI apps connect with one URL and
	// sign in with OAuth. MCPServer is OUTFLARE_MCP_SERVER, the bundled
	// server (mcp/dist/outflare-mcp.mjs, at /mcp/outflare-mcp.mjs in the
	// image); empty serves no /mcp. Node is OUTFLARE_NODE, default node.
	MCPServer string
	Node      string

	// The operator's privacy policy and terms (OUTFLARE_PRIVACY_URL,
	// OUTFLARE_TERMS_URL), linked from the OAuth metadata AI apps read.
	PrivacyURL string
	TermsURL   string

	// CommunityLinks is OUTFLARE_COMMUNITY_LINKS: off hides the optional
	// "Star us on GitHub" and "Follow us on X" items on Home and Get
	// started. On by default. CommunityXURL is OUTFLARE_FOLLOW_URL: the X
	// profile "Follow us on X" opens, an https address; empty leaves that
	// item out.
	CommunityLinks bool
	CommunityXURL  string

	// SignedInCookieDomain is OUTFLARE_SIGNED_IN_COOKIE_DOMAIN: a parent
	// domain of PUBLIC_URL's host, such as example.com for
	// app.example.com. While someone is signed in the server keeps a
	// cookie there that says only that (outflare_signed_in=1), so your
	// website can show "Dashboard" instead of "Sign in". Empty sets none.
	SignedInCookieDomain string
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		HTTPAddr:          getenv("HTTP_ADDR", ":8080"),
		WebDir:            os.Getenv("WEB_DIR"),
		PublicURL:         getenv("PUBLIC_URL", "http://localhost:8080"),
		Workers:           32,
		QueryConnections:  3,
		RateLimit:         600,
		LogRetentionDays:  30,
		MetricsAddr:       strings.TrimSpace(os.Getenv("OUTFLARE_METRICS_ADDR")),
		MetricsToken:      strings.TrimSpace(os.Getenv("OUTFLARE_METRICS_TOKEN")),
		MetricsPprof:      os.Getenv("OUTFLARE_METRICS_PPROF") == "true",
		SecretKey:         os.Getenv("OUTFLARE_SECRET_KEY"),
		SecretKeyPrevious: strings.Split(os.Getenv("OUTFLARE_SECRET_KEY_PREVIOUS"), ","),
		Passkeys:          getenv("PASSKEYS", "auto"),

		ClientIPHeader:         os.Getenv("CLIENT_IP_HEADER"),
		WebhooksAllowPrivate:   os.Getenv("WEBHOOKS_ALLOW_PRIVATE") == "true",
		CrawlAllowPrivate:      os.Getenv("OUTFLARE_CRAWL_ALLOW_PRIVATE") == "true",
		ConnectorsAllowPrivate: os.Getenv("OUTFLARE_CONNECTORS_ALLOW_PRIVATE") == "true",

		GoogleClientID:        os.Getenv("GOOGLE_OAUTH_CLIENT_ID"),
		GoogleClientSecret:    os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
		MicrosoftClientID:     os.Getenv("MICROSOFT_OAUTH_CLIENT_ID"),
		MicrosoftClientSecret: os.Getenv("MICROSOFT_OAUTH_CLIENT_SECRET"),

		PostHogKey:  strings.TrimSpace(os.Getenv("OUTFLARE_POSTHOG_KEY")),
		PostHogHost: strings.TrimRight(getenv("OUTFLARE_POSTHOG_HOST", "https://us.i.posthog.com"), "/"),

		EventsWorkspace: strings.ToLower(strings.TrimSpace(os.Getenv("OUTFLARE_EVENTS_WORKSPACE"))),
		ClickHouseURL:   strings.TrimSpace(os.Getenv("OUTFLARE_CLICKHOUSE_URL")),
		S3: S3{
			Endpoint:  strings.TrimRight(strings.TrimSpace(os.Getenv("OUTFLARE_S3_ENDPOINT")), "/"),
			Region:    getenv("OUTFLARE_S3_REGION", "auto"),
			Bucket:    strings.TrimSpace(os.Getenv("OUTFLARE_S3_BUCKET")),
			AccessKey: strings.TrimSpace(os.Getenv("OUTFLARE_S3_ACCESS_KEY_ID")),
			SecretKey: strings.TrimSpace(os.Getenv("OUTFLARE_S3_SECRET_ACCESS_KEY")),
		},

		MCPServer: os.Getenv("OUTFLARE_MCP_SERVER"),
		Node:      getenv("OUTFLARE_NODE", "node"),

		PrivacyURL: os.Getenv("OUTFLARE_PRIVACY_URL"),
		TermsURL:   os.Getenv("OUTFLARE_TERMS_URL"),

		CommunityXURL: strings.TrimSpace(os.Getenv("OUTFLARE_FOLLOW_URL")),

		SignedInCookieDomain: strings.ToLower(strings.Trim(strings.TrimSpace(os.Getenv("OUTFLARE_SIGNED_IN_COOKIE_DOMAIN")), ".")),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	switch c.Passkeys {
	case "on", "off", "auto":
	default:
		return c, fmt.Errorf("PASSKEYS must be on, off or auto, got %q", c.Passkeys)
	}
	if c.PostHogKey != "" && !strings.HasPrefix(c.PostHogKey, "phc_") {
		return c, fmt.Errorf("OUTFLARE_POSTHOG_KEY must be a PostHog project key (phc_...), not a personal API key")
	}
	if !strings.HasPrefix(c.PostHogHost, "https://") {
		return c, fmt.Errorf("OUTFLARE_POSTHOG_HOST must be an https address, got %q", c.PostHogHost)
	}
	if c.EventsWorkspace != "" && !uuidRe.MatchString(c.EventsWorkspace) {
		return c, fmt.Errorf("OUTFLARE_EVENTS_WORKSPACE must be a workspace id (a UUID), got %q", c.EventsWorkspace)
	}
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("OUTFLARE_COMMUNITY_LINKS"))); v {
	case "", "on", "true", "1":
		c.CommunityLinks = true
	case "off", "false", "0":
	default:
		return c, fmt.Errorf("OUTFLARE_COMMUNITY_LINKS must be on or off, got %q", v)
	}
	if c.CommunityXURL != "" && !strings.HasPrefix(c.CommunityXURL, "https://") {
		return c, fmt.Errorf("OUTFLARE_FOLLOW_URL must be an https address, got %q", c.CommunityXURL)
	}
	if err := c.S3.check(); err != nil {
		return c, err
	}
	if err := checkSignedInDomain(c.SignedInCookieDomain, c.PublicURL); err != nil {
		return c, err
	}
	if v := os.Getenv("WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, fmt.Errorf("WORKERS must be a non-negative integer, got %q", v)
		}
		c.Workers = n
	}
	if v := os.Getenv("OUTFLARE_QUERY_CONNECTIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 64 {
			return c, fmt.Errorf("OUTFLARE_QUERY_CONNECTIONS must be 0 to 64, got %q", v)
		}
		c.QueryConnections = n
	}
	if v := os.Getenv("API_RATE_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, fmt.Errorf("API_RATE_LIMIT must be a non-negative integer, got %q", v)
		}
		c.RateLimit = n
	}
	if v := os.Getenv("OUTFLARE_EVENT_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || (n > 0 && n < 30) || n > 3650 {
			return c, fmt.Errorf("OUTFLARE_EVENT_RETENTION_DAYS must be 0 (keep everything) or a number of days from 30 to 3650, got %q", v)
		}
		c.EventRetentionDays = n
	}
	if v := os.Getenv("OUTFLARE_LOG_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 3650 {
			return c, fmt.Errorf("OUTFLARE_LOG_RETENTION_DAYS must be a number of days from 1 to 3650, got %q", v)
		}
		c.LogRetentionDays = n
	}
	c.Role = RoleAll
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("OUTFLARE_ROLE"))); v != "" {
		switch Role(v) {
		case RoleAll, RoleAPI, RoleIngest, RoleWorker:
			c.Role = Role(v)
		default:
			return c, fmt.Errorf("OUTFLARE_ROLE must be all, api, ingest or worker, got %q", v)
		}
	}
	if err := checkMetricsAddr(c.MetricsAddr, c.HTTPAddr); err != nil {
		return c, err
	}
	return c, nil
}

// checkMetricsAddr makes sure the metrics listener is a host:port of its
// own: on the API's port it would either fail or, worse, be public.
func checkMetricsAddr(addr, httpAddr string) error {
	if addr == "" {
		return nil
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fmt.Errorf("OUTFLARE_METRICS_ADDR must be an address such as :9091, got %q", addr)
	}
	if _, httpPort, err := net.SplitHostPort(httpAddr); err == nil && httpPort == port {
		return fmt.Errorf("OUTFLARE_METRICS_ADDR must use a different port from HTTP_ADDR (%s), so metrics stay off the public port", httpAddr)
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// PasskeysOn reports whether passkey sign-in is on.
func (c Config) PasskeysOn() bool {
	return c.Passkeys == "on" || (c.Passkeys == "auto" && strings.HasPrefix(c.PublicURL, "https://"))
}

// checkSignedInDomain makes sure the signed-in hint goes only to a real
// parent domain of the server's own host, never to a whole public suffix
// or someone else's domain.
func checkSignedInDomain(domain, publicURL string) error {
	if domain == "" {
		return nil
	}
	u, err := url.Parse(publicURL)
	host := ""
	if err == nil {
		host = strings.ToLower(u.Hostname())
	}
	if !strings.Contains(domain, ".") || strings.ContainsAny(domain, "/:@ ") {
		return fmt.Errorf("OUTFLARE_SIGNED_IN_COOKIE_DOMAIN must be a domain such as example.com, got %q", domain)
	}
	if host != domain && !strings.HasSuffix(host, "."+domain) {
		return fmt.Errorf("OUTFLARE_SIGNED_IN_COOKIE_DOMAIN must be PUBLIC_URL's host or a parent of it; %q is not one of %q", domain, host)
	}
	return nil
}

// S3 is an S3-compatible bucket for files. Endpoint is the store's address,
// such as https://<account>.r2.cloudflarestorage.com for Cloudflare R2.
type S3 struct {
	Endpoint  string // OUTFLARE_S3_ENDPOINT
	Region    string // OUTFLARE_S3_REGION, default auto (right for R2)
	Bucket    string // OUTFLARE_S3_BUCKET
	AccessKey string // OUTFLARE_S3_ACCESS_KEY_ID
	SecretKey string // OUTFLARE_S3_SECRET_ACCESS_KEY
}

// On reports whether a bucket is set up.
func (s S3) On() bool { return s.Bucket != "" }

func (s S3) check() error {
	if s.Bucket == "" && s.Endpoint == "" && s.AccessKey == "" && s.SecretKey == "" {
		return nil
	}
	if s.Bucket == "" || s.Endpoint == "" || s.AccessKey == "" || s.SecretKey == "" {
		return fmt.Errorf("object storage needs all of OUTFLARE_S3_ENDPOINT, OUTFLARE_S3_BUCKET, OUTFLARE_S3_ACCESS_KEY_ID and OUTFLARE_S3_SECRET_ACCESS_KEY")
	}
	if u, err := url.Parse(s.Endpoint); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("OUTFLARE_S3_ENDPOINT must be an http or https address, got %q", s.Endpoint)
	}
	return nil
}

// Role is what one process of the engine does (OUTFLARE_ROLE).
type Role string

const (
	RoleAll    Role = "all"
	RoleAPI    Role = "api"
	RoleIngest Role = "ingest"
	RoleWorker Role = "worker"
)

// Works says whether the process runs the job queue and background loops.
func (r Role) Works() bool { return r == RoleAll || r == RoleWorker }
