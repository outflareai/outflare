// Package api is the HTTP API server.
package api

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/go-webauthn/webauthn/webauthn"
	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/cookies"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/datanetwork"
	"github.com/outflareai/outflare/engine/internal/domainconnect"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/imports"
	"github.com/outflareai/outflare/engine/internal/inbound"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/lists"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/platforms"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/sending"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/telemetry"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/vendorsync"
	"github.com/outflareai/outflare/engine/internal/verify"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// Version is set at build time with -ldflags "-X .../api.Version=...".
var Version = "dev"

type Server struct {
	pool          *pgxpool.Pool
	log           *slog.Logger
	webDir        string
	publicURL     string
	secureCookies bool
	limiter       loginLimiter
	emailLimiter  loginLimiter // account emails per address and per network
	mailer        sysmail.Sender
	webauthn      *webauthn.WebAuthn // nil while passkeys are off
	mail          *mailbox.Service
	send          *sending.Engine
	inbox         *inbox.Syncer
	oauth         *oauth.Tokens
	box           *secrets.Box
	query         queryState    // Query's own connections
	semantic      semanticState // whether pgvector is there, and query vectors
	integrations  *connectors.Store
	lists         *lists.Service
	verifier      *verify.Service
	emailFinder   *emailfind.Service
	dataNetwork   *datanetwork.Service
	inboundEmail  *inboundemail.Service
	classifier    *classify.Classifier
	research      *research.Service
	vendors       *vendorsync.Service
	inbound       *inbound.Service
	outcomes      *outcomes.Syncer
	analyticsSync *analyticsync.Syncer
	seoSyncer     *seosync.Syncer
	adSync        *adsync.Syncer
	crawler       *crawl.Service
	imports       *imports.Service
	cname         tracking.CNAMEResolver
	// What tracking domains point at, who gets their certificates, and
	// the secret the edge in front of them signs its headers with.
	trackingTarget  string
	partnersDomain  string
	trackingCerts   TrackingCerts
	edgeSecret      string
	dc              *domainconnect.Client
	dcMu            sync.Mutex
	routes          func(*http.ServeMux)
	workspaceRoutes []WorkspaceRoute
	menuLinks       []MenuLinks
	aiTools         []AITool
	middleware      []func(http.Handler) http.Handler
	capabilities    []string
	posthog         *posthogProxy
	alloc           *allocation.Service
	platforms       *platforms.Service
	telemetry       *telemetry.Reporter
	webhooks        *webhooks.Service
	emails          *emails.Service
	rateLimit       *rateLimiter
	logs            *logWriter
	registerLimit   *rateLimiter
	// Browser events, per visitor address and per site key.
	siteIPLimit, siteKeyLimit *rateLimiter
	// Browser events go through site_event_inbox (RunSiteInbox stores them).
	siteInbox        bool
	inboxWake        chan struct{}
	siteVisitorLimit *rateLimiter
	// Help centre pages per reader's network, and what a reader may record
	// (feedback, searches with no answer) per hour.
	helpIPLimit    *rateLimiter
	helpWriteLimit *rateLimiter
	// portalLimit caps partner portal sign-ups and sign-in emails per address.
	portalLimit *rateLimiter
	// siteFlagLimit caps the website script's flag requests per key.
	siteFlagLimit *rateLimiter
	// siteConsentLimit caps the consent banner's requests per key.
	siteConsentLimit *rateLimiter
	// cookieReports takes what browsers see of a site's cookies.
	cookieReports *cookies.Reporter
	flagCache     flagCache
	// Which hosts are tracking domains, for trackingHostsOnly.
	trackingHosts trackingHostCache
	mcp           *MCPUpstream
	privacyURL    string
	community     []communityLink
	termsURL      string
	handlerOnce   sync.Once
	handler       http.Handler

	signedInDomain string // Options.SignedInCookieDomain
}

// Options configures a Server.
type Options struct {
	WebDir    string // built web app to serve; empty disables
	PublicURL string // external base URL, used in links and to decide Secure cookies
	Mailboxes *mailbox.Service
	Sending   *sending.Engine
	Inbox     *inbox.Syncer
	OAuth     *oauth.Tokens // nil disables OAuth connections
	// Allocation plans which mailboxes serve which sequences; nil builds one.
	Allocation *allocation.Service
	Box        *secrets.Box // seals OAuth states for connectors
	// Connectors and email verification; nil disables them.
	Integrations *connectors.Store
	Verify       *verify.Service
	// EmailFinder proposes and verifies addresses from a workspace's learned
	// patterns; nil turns the /email-finder endpoints off.
	EmailFinder *emailfind.Service
	// DataNetwork is the data network's membership and lookups
	// (internal/datanetwork); it works without a network registered.
	DataNetwork *datanetwork.Service
	// InboundEmail receives workspaces' forwarded mail when an extension
	// turned it on (package inboundemail); nil leaves it off.
	InboundEmail *inboundemail.Service
	// Classifier says what kind of email domain an address is; nil builds
	// one on the built-in data that asks the system resolver.
	Classifier *classify.Classifier
	// Research reads an unknown domain's homepage with the workspace's own
	// connector and AI key; nil leaves unknown domains unresearched.
	Research *research.Service
	// Lists builds lead lists from data providers; nil disables builds.
	Lists *lists.Service
	// Mailbox supplier sync (Cheap Inboxes and the like); nil disables it.
	Vendors *vendorsync.Service
	// Inbound receives connectors' webhooks (live updates); nil turns
	// /v1/hooks and the live update endpoints off.
	Inbound   *inbound.Service
	Imports   *imports.Service   // moving over from other sequencers
	Platforms *platforms.Service // outside sequencers in mailbox allocation; nil disables syncing on demand
	// Verifies tracking domains; defaults to the system resolver.
	CNAME tracking.CNAMEResolver
	// Outcome syncs (calendars, CRMs); nil disables syncing on demand.
	Outcomes *outcomes.Syncer
	// AnalyticsSync stores analytics history; nil turns the sync endpoints off.
	AnalyticsSync *analyticsync.Syncer
	// SEOSync keeps weekly SEO snapshots; nil turns POST .../seo/{id}/sync off.
	SEOSync *seosync.Syncer
	// AdSync reads connected ad accounts; nil turns POST .../ads/sync off.
	AdSync *adsync.Syncer
	// DomainConnect finds domains' DNS providers for the one-off "apply
	// these records?" screen; nil builds one on the public internet and
	// the system resolver, signing with a key the server stores.
	DomainConnect *domainconnect.Client
	// Crawl runs OutflareBot's crawls and ownership checks; nil builds one
	// that checks DNS with the system resolver.
	Crawl *crawl.Service
	// Anonymous usage stats and update checks; nil builds one that sends
	// nothing.
	Telemetry *telemetry.Reporter
	// RateLimit is how many requests an API token may make a minute; 0
	// means no limit.
	RateLimit int
	// Webhooks sends test events and redeliveries; nil builds one that only
	// delivers to public https URLs.
	Webhooks *webhooks.Service
	// Sends account emails (confirmation, password reset); nil only logs
	// them.
	Mailer sysmail.Sender
	// Passkeys turns on passkey sign-in, for PUBLIC_URL's host. Only set it
	// once PUBLIC_URL is the address people will always use: passkeys made
	// for one address don't work on another.
	Passkeys bool
	// ClientIPHeader is the header a trusted proxy in front of the server
	// puts the visitor's address in, such as Fly-Client-IP. Empty uses the
	// connection's address.
	ClientIPHeader string
	// PostHogKey turns on product analytics in the web app, sent through
	// /ingest/ on this server to PostHogHost (https://us.i.posthog.com by
	// default). Empty sends nothing.
	PostHogKey  string
	PostHogHost string
	// MCP is the hosted MCP server to proxy /mcp to; nil serves no /mcp.
	MCP *MCPUpstream
	// Emails sends lifecycle and transactional email through the
	// workspace's email providers; nil builds one when Integrations and
	// Box are set (its jobs run only where the queue registers it).
	Emails *emails.Service
	// PrivacyURL and TermsURL are the operator's policies, linked from the
	// OAuth metadata when set.
	PrivacyURL, TermsURL string
	// CommunityLinks shows the optional "Star us on GitHub" and "Follow us
	// on X" items (GET /v1/me/community); CommunityXURL is the X profile,
	// empty to leave that item out. CommunityBuilderURL is the X profile of
	// the person building Outflare in public ("Follow the builder"), empty
	// to leave it out.
	CommunityLinks      bool
	CommunityXURL       string
	CommunityBuilderURL string
	// SignedInCookieDomain, when set, is a parent domain of PUBLIC_URL's
	// host (outflare.ai for app.outflare.ai) where the server keeps an
	// outflare_signed_in=1 cookie while someone is signed in, so a website
	// on that domain can show a link to the app instead of Sign in. Empty
	// sets no such cookie.
	SignedInCookieDomain string
	// SiteInbox makes POST /v1/site/events only queue browser events in
	// site_event_inbox and answer; RunSiteInbox, on a server that works,
	// stores them. Off stores them during the request.
	SiteInbox bool

	// Extension points for builds that add to the open-source engine, such
	// as the hosted Cloud. See the outflare package.
	Routes func(mux *http.ServeMux) // extra routes, registered before the /v1/ catch-all
	// WorkspaceRoutes are extension routes behind the workspace check.
	WorkspaceRoutes []WorkspaceRoute
	Middleware      []func(http.Handler) http.Handler // wrap every request, outermost first, inside request ids and logging
	Capabilities    []string                          // added to /v1/meta
	// MenuLinks add per-person sidebar items, in /v1/me.
	MenuLinks []MenuLinks
	// AITools name extensions' own AI calls on the usage page.
	AITools []AITool
	// TrackingTarget is the host tracking domains' CNAME records point at;
	// empty for PUBLIC_URL's host.
	TrackingTarget string
	// PartnersDomain serves partner portals at <subdomain>.<it>; empty for
	// /p/<program>/ only.
	PartnersDomain string
	// TrackingCerts gets HTTPS certificates for tracking domains; nil when
	// the operator's own proxy serves them.
	TrackingCerts TrackingCerts
	// EdgeSecret lets an edge in front of tracking domains pass on the
	// domain and visitor address it saw (edge.go); empty turns that off.
	EdgeSecret string
	// QueryConnections is how many queries run at once, each on its own
	// database connection; zero means the default (3), below zero turns
	// Query off.
	QueryConnections int
}

func NewServer(pool *pgxpool.Pool, log *slog.Logger, opts Options) *Server {
	publicURL := strings.TrimRight(opts.PublicURL, "/")
	if publicURL == "" {
		publicURL = "http://localhost:8080"
	}
	dc := opts.DomainConnect
	if dc == nil {
		dc = &domainconnect.Client{HTTP: netguard.Client(10 * time.Second), Resolver: net.DefaultResolver}
	}
	cname := opts.CNAME
	if cname == nil {
		cname = net.DefaultResolver
	}
	alloc := opts.Allocation
	if alloc == nil {
		alloc = &allocation.Service{Pool: pool, Log: log}
	}
	tel := opts.Telemetry
	if tel == nil {
		tel = &telemetry.Reporter{Pool: pool, Log: log, Version: Version}
	}
	clientIPHeader = opts.ClientIPHeader
	crawler := opts.Crawl
	if crawler == nil {
		crawler = &crawl.Service{Pool: pool, Log: log, Resolver: net.DefaultResolver}
	}
	hooks := opts.Webhooks
	if hooks == nil {
		hooks = &webhooks.Service{Pool: pool, Box: opts.Box, Log: log}
	}
	mail := opts.Emails
	if mail == nil && opts.Integrations != nil && opts.Box != nil {
		mail = &emails.Service{Pool: pool, Store: opts.Integrations, Box: opts.Box, PublicURL: publicURL, Log: log}
		mail.Attach(opts.Integrations)
	}
	classifier := opts.Classifier
	if classifier == nil {
		classifier = &classify.Classifier{Resolver: net.DefaultResolver}
	}
	mailer := opts.Mailer
	if mailer == nil {
		mailer = sysmail.Log{Logger: log}
	}
	s := &Server{
		emails:         mail,
		mailer:         mailer,
		webauthn:       newWebAuthn(publicURL, opts.Passkeys && opts.Box != nil),
		cname:          cname,
		trackingTarget: opts.TrackingTarget,
		partnersDomain: opts.PartnersDomain,
		trackingCerts:  opts.TrackingCerts,
		edgeSecret:     opts.EdgeSecret,
		dc:             dc,
		telemetry:      tel,
		webhooks:       hooks,
		rateLimit:      &rateLimiter{name: "api", limit: opts.RateLimit, window: time.Minute},
		// Apps register themselves once per install; this stops a flood.
		registerLimit:    &rateLimiter{name: "register", limit: 30, window: time.Hour},
		siteIPLimit:      &rateLimiter{name: "site_ip", limit: 300, window: time.Minute},
		siteKeyLimit:     &rateLimiter{name: "site_key", limit: 6000, window: time.Minute},
		siteVisitorLimit: &rateLimiter{name: "site_visitor", limit: 600, window: time.Minute},
		helpIPLimit:      &rateLimiter{name: "help_ip", limit: 300, window: time.Minute},
		helpWriteLimit:   &rateLimiter{name: "help_write", limit: 30, window: time.Hour},
		portalLimit:      &rateLimiter{name: "partner_portal", limit: 20, window: time.Minute},
		siteFlagLimit:    &rateLimiter{name: "site_flags", limit: 6000, window: time.Minute},
		siteConsentLimit: &rateLimiter{name: "site_consent", limit: 6000, window: time.Minute},
		cookieReports:    &cookies.Reporter{Pool: pool},
		siteInbox:        opts.SiteInbox,
		inboxWake:        make(chan struct{}, 1),
		mcp:              opts.MCP,
		privacyURL:       opts.PrivacyURL,
		termsURL:         opts.TermsURL,
		community:        communityLinksFor(opts.CommunityLinks, opts.CommunityXURL, opts.CommunityBuilderURL),
		limiter:          loginLimiter{pool: pool, name: "sign_in", log: log},
		emailLimiter:     loginLimiter{pool: pool, name: "account_email", log: log},
		pool:             pool,
		log:              log,
		webDir:           opts.WebDir,
		publicURL:        publicURL,
		secureCookies:    strings.HasPrefix(publicURL, "https://"),
		mail:             opts.Mailboxes,
		send:             opts.Sending,
		inbox:            opts.Inbox,
		oauth:            opts.OAuth,
		box:              opts.Box,
		query:            queryState{size: opts.QueryConnections},
		integrations:     opts.Integrations,
		verifier:         opts.Verify,
		emailFinder:      opts.EmailFinder,
		dataNetwork:      opts.DataNetwork,
		inboundEmail:     cmp.Or(opts.InboundEmail, &inboundemail.Service{}),
		classifier:       classifier,
		research:         opts.Research,
		lists:            opts.Lists,
		imports:          opts.Imports,
		vendors:          opts.Vendors,
		inbound:          opts.Inbound,
		routes:           opts.Routes,
		workspaceRoutes:  opts.WorkspaceRoutes,
		menuLinks:        opts.MenuLinks,
		aiTools:          opts.AITools,
		middleware:       opts.Middleware,
		capabilities:     append([]string{"mailboxes", "domains", "leads", "suppression", "sequences", "inbox", "oauth", "warmup", "infrastructure", "allocation", "platforms", "connectors", "verification", "classification", "mailbox_suppliers", "mailbox_health", "imports", "reports", "outcomes", "tracking", "missions", "lists", "webhooks", "emails"}, opts.Capabilities...),
		alloc:            alloc,
		platforms:        opts.Platforms,
		outcomes:         opts.Outcomes,
		analyticsSync:    opts.AnalyticsSync,
		seoSyncer:        opts.SEOSync,
		adSync:           opts.AdSync,
		crawler:          crawler,
		posthog:          newPostHogProxy(opts.PostHogKey, opts.PostHogHost),

		signedInDomain: opts.SignedInCookieDomain,
	}
	s.logs = &logWriter{s: s}
	s.watchSocialConnections()
	s.watchPageReaders()
	if s.crawler != nil && s.crawler.Owns == nil && s.integrations != nil {
		s.crawler.Owns = s.searchConsoleOwns
	}
	if s.seoSyncer != nil && s.seoSyncer.Store != nil {
		s.seoSyncer.Watch()
	}
	// AI receipts: only members forward them, to one address per
	// workspace, and only where AI Accounts is on.
	s.inboundEmail.Register(inboundemail.PurposeAIReceipts, inboundemail.Purpose{
		Handler: s.handleAIReceipt,
		On: func(ctx context.Context, q inboundemail.Querier, workspaceID string) (bool, error) {
			return features.Enabled(ctx, q, workspaceID, features.AIAccounts)
		},
	})
	s.registerSupportForwarding()
	return s
}

// Handler returns the full HTTP handler with middleware applied. It is
// built once; approved mission actions run through it too.
func (s *Server) Handler() http.Handler {
	s.handlerOnce.Do(func() { s.handler = s.buildHandler() })
	return s.handler
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /v1/meta", s.meta)
	mux.HandleFunc("GET /.well-known/outflare.json", s.meta)
	mux.HandleFunc("GET /v1/openapi.yaml", s.openapi)
	if s.posthog != nil {
		mux.Handle("/ingest/", s.posthog)
	}
	mux.HandleFunc("GET /v1/error-codes", s.listErrorCodes)
	mux.HandleFunc("GET /v1/domain-connect", s.domainConnectKey)
	mux.HandleFunc("GET /v1/whoami", s.whoami)
	s.routesAccess(mux)
	s.routesRoles(mux)
	s.routesLogin(mux)
	s.routesAccountEmail(mux)
	s.routesPasskeys(mux)
	s.routesMailboxes(mux)
	s.routesWebhooks(mux)
	s.routesJobs(mux)
	s.routesLogs(mux)
	s.routesBrands(mux)
	s.routesCatalog(mux)
	s.routesRecords(mux)
	s.routesCustomerEvents(mux)
	s.routesMerges(mux)
	s.routesQuery(mux)
	s.routesSearch(mux)
	s.routesSemanticSearch(mux)
	s.routesAnalyticsBrands(mux)
	s.routesAds(mux)
	s.routesAdTracking(mux)
	s.routesAdConversions(mux)
	s.routesContext(mux)
	s.routesOnboarding(mux)
	s.routesCommunity(mux)
	s.routesSupportAccess(mux)
	s.routesSetup(mux)
	s.routesBrandDNS(mux)
	s.routesStatus(mux)
	s.routesTeams(mux)
	s.routesAllocation(mux)
	s.routesSandbox(mux)
	s.routesPlatforms(mux)
	s.routesLeads(mux)
	s.routesLists(mux)
	s.routesSmartLists(mux)
	s.routesRules(mux)
	s.routesSequences(mux)
	s.routesInbox(mux)
	s.routesInboxTags(mux)
	s.routesReplyRules(mux)
	s.routesAlerts(mux)
	s.routesOAuth(mux)
	s.routesStats(mux)
	s.routesDomains(mux)
	s.routesServers(mux)
	s.routesWarmup(mux)
	s.routesInfra(mux)
	s.routesOutcomes(mux)
	s.routesIntegrations(mux)
	s.routesAISettings(mux)
	s.routesAIUsage(mux)
	s.routesConnectionUsage(mux)
	s.routesAnalytics(mux)
	s.routesAnalyticsHistory(mux)
	s.routesExperiments(mux)
	s.routesAnalyticsAgents(mux)
	s.routesSEO(mux)
	s.routesSEOSites(mux)
	s.routesDeploys(mux)
	s.routesBacklinks(mux)
	s.routesSEOSearch(mux)
	s.routesCompetitors(mux)
	s.routesLinkProspects(mux)
	s.routesNews(mux)
	s.routesStories(mux)
	s.routesLinkReads(mux)
	s.routesLinkPitches(mux)
	s.routesSearchResults(mux)
	s.routesDigests(mux)
	s.routesSEOTargets(mux)
	s.routesSEORanks(mux)
	s.routesSEOConversions(mux)
	s.routesSEORegressions(mux)
	s.routesAIVisibility(mux)
	s.routesSocial(mux)
	s.routesSocialAccounts(mux)
	s.routesSocialPublish(mux)
	s.routesRedditCampaigns(mux)
	s.routesBrandLinks(mux)
	s.routesReviews(mux)
	s.routesAIFacts(mux)
	s.routesFixAutonomy(mux)
	s.routesFixTickets(mux)
	s.routesIndexability(mux)
	s.routesConnectRequests(mux)
	s.routesVendorMailboxes(mux)
	s.routesEmailBisonCompat(mux)
	s.routesImports(mux)
	s.routesCRMImport(mux)
	s.routesPayments(mux)
	s.routesConnectionHooks(mux)
	s.routesSiteKeys(mux)
	s.routesConsentBanner(mux)
	s.routesCookiePolicy(mux)
	s.routesFlags(mux)
	s.routesABTests(mux)
	s.routesABTestAI(mux)
	s.routesVisitors(mux)
	s.routesHealth(mux)
	s.routesReports(mux)
	s.routesTracking(mux)
	s.routesCampaignLinks(mux)
	s.routesPartners(mux)
	s.routesAttribution(mux)
	s.routesAttributionModels(mux)
	s.routesConversionDefinitions(mux)
	s.routesAIReceipts(mux)
	s.routesAIAccounts(mux)
	s.routesCustomerProfiles(mux)
	s.routesKnowledge(mux)
	s.routesKnowledgeRetrieval(mux)
	s.routesSupport(mux)
	s.routesKnowledgeInsights(mux)
	s.routesHelpCentres(mux)
	s.routesHelpCentreSite(mux)
	s.routesInboundEmail(mux)
	s.routesVerification(mux)
	s.routesClassify(mux)
	s.routesWorkspaceDomains(mux)
	s.routesMissions(mux)
	s.routesPlaybooks(mux)
	s.routesPlaybookAutomations(mux)
	s.routesKits(mux)
	s.routesTelemetry(mux)
	s.routesAgentOAuth(mux)
	s.routesMCP(mux)
	if s.emailFinder != nil {
		s.routesEmailFind(mux)
		s.routesDataNetwork(mux)
	}
	if s.emails != nil {
		s.routesEmails(mux)
		s.routesJourneys(mux)
		s.routesJourneySecrets(mux)
	}
	if s.routes != nil {
		s.routes(mux)
	}
	s.routesExtensions(mux)
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such endpoint")
	})
	if s.webDir != "" {
		mux.Handle("/", spaHandler(s.webDir))
	}
	var h http.Handler = mux
	for i := len(s.middleware) - 1; i >= 0; i-- {
		h = s.middleware[i](h)
	}
	if testWrap != nil {
		h = testWrap(h)
	}
	pattern := func(r *http.Request) string {
		_, p := mux.Handler(r)
		return p
	}
	return requestID(s.fromEdge(s.securityHeaders(s.logRequests(pattern, s.recoverer(s.trackingHostsOnly(withCursorParam(withFields(h))))))))
}

// testWrap, set only by this package's tests, wraps every handler the
// server builds. The tests use it to check each response against the
// OpenAPI document.
var testWrap func(http.Handler) http.Handler

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "database unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	m := map[string]any{
		"name":    "Outflare",
		"version": Version,
		"api": map[string]string{
			"rest":    "/v1",
			"openapi": "/v1/openapi.yaml",
		},
		"capabilities":    s.capabilities,
		"oauth_providers": s.oauthProviders(),
		"login_methods":   s.loginMethods(),
		"account_email":   s.mailer.Name(),
	}
	if s.posthog != nil {
		m["product_analytics"] = s.posthog.meta()
	}
	if s.mcp != nil {
		m["mcp_url"] = s.mcpURL()
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(spec.OpenAPI) //nolint:errcheck
}

// spaHandler serves the built web app, falling back to index.html for client-side routes.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			// App routes can end in a dot-name (/{workspace}/domains/acme-com.example),
			// so only short paths that look like files get a 404.
			if strings.Count(r.URL.Path, "/") >= 3 || !strings.Contains(filepath.Base(r.URL.Path), ".") {
				http.ServeFile(w, r, filepath.Join(dir, "index.html"))
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

type ctxKey int

const requestIDKey ctxKey = 0

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 128 {
			b := make([]byte, 12)
			rand.Read(b) //nolint:errcheck
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// RequestID returns the request's id, for logs and audit events.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					// Not a crash: a handler giving up on a response whose
					// client went away, as httputil.ReverseProxy does when
					// an MCP or /ingest stream drops. net/http closes the
					// connection without a word, so let it.
					panic(v)
				}
				s.log.Error("panic in handler", "panic", v, "request_id", RequestID(r.Context()), "stack", string(debug.Stack()))
				e := track.Event{Name: "server.panicked", Surface: "system", Outcome: track.Error, ErrorCode: "internal", RequestID: RequestID(r.Context()),
					Props: map[string]any{"method": r.Method, "where": panicSite()}}
				if i := reqInfoFrom(r.Context()); i != nil {
					e.WorkspaceID = i.workspaceID
				}
				track.Emit(r.Context(), e)
				writeError(w, r, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// panicSite is the code a recovered panic started in, "file.go:line func",
// for grouping crashes by cause in server.panicked. Call it from the deferred
// recover. It carries a code location only, never the panic's value, which
// can quote customer data.
func panicSite() string {
	pc := make([]uintptr, 64)
	frames := runtime.CallersFrames(pc[:runtime.Callers(2, pc)])
	panicking := false
	for {
		f, more := frames.Next()
		switch {
		case f.Function == "runtime.gopanic":
			panicking = true
		case panicking && !strings.HasPrefix(f.Function, "runtime."):
			file := f.File
			if i := strings.LastIndex(file, "/engine/"); i >= 0 {
				file = file[i+1:]
			} else if parts := strings.Split(file, "/"); len(parts) > 3 {
				file = strings.Join(parts[len(parts)-3:], "/")
			}
			fn := f.Function[strings.LastIndex(f.Function, "/")+1:]
			return fmt.Sprintf("%s:%d %s", file, f.Line, fn)
		}
		if !more {
			return ""
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	if m, ok := v.(map[string]any); ok {
		addNextCursor(m)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// writeError writes the standard error body: {"error": {"code", "message", "request_id"}}.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	e := map[string]string{
		"code":       code,
		"message":    message,
		"request_id": RequestID(r.Context()),
	}
	if p := errorParam(status, message); p != "" {
		e["param"] = p
	}
	writeJSON(w, status, map[string]any{"error": e})
}

var (
	paramFirst   = regexp.MustCompile(`^([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)*) (?:must|is|are|needs|cannot|may only|should)\b`)
	paramUnknown = regexp.MustCompile(`unknown field "([^"]+)"`)
	paramType    = regexp.MustCompile(`cannot unmarshal \S+ into Go struct field \S+?\.(\S+) of type`)
)

// errorParam names the request field a 400 is about, when the message
// says ("url must be ...", an unknown JSON field, a wrong type), so a
// client or agent can point at it or fix it without parsing English.
func errorParam(status int, message string) string {
	if status != http.StatusBadRequest {
		return ""
	}
	for _, re := range []*regexp.Regexp{paramUnknown, paramType, paramFirst} {
		if m := re.FindStringSubmatch(message); m != nil {
			return m[1]
		}
	}
	return ""
}
