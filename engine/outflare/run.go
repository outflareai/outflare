// Package outflare starts the Outflare engine: the HTTP API and the job
// workers. The outflare command is a two-line main around it, and builds that
// add to the open-source engine, such as the hosted Cloud, call Main with
// their Extensions instead of forking it:
//
//	func main() { outflare.Main(billing.Extension{}, signup.Extension{}) }
//
// Commands:
//
//	outflare serve     run migrations, then the API and workers
//	outflare migrate   run migrations and exit
//	outflare version   print the version
package outflare

import (
	_ "time/tzdata"

	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/aiaccounts"
	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/blobs"
	"github.com/outflareai/outflare/engine/internal/config"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/connectors/cheapinboxes"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/cookies"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/datanetwork"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/deploys"
	"github.com/outflareai/outflare/engine/internal/digests"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/eventstore"
	"github.com/outflareai/outflare/engine/internal/experiments"
	"github.com/outflareai/outflare/engine/internal/fixauto"
	"github.com/outflareai/outflare/engine/internal/fxrates"
	"github.com/outflareai/outflare/engine/internal/health"
	"github.com/outflareai/outflare/engine/internal/imports"
	"github.com/outflareai/outflare/engine/internal/inbound"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/indexability"
	"github.com/outflareai/outflare/engine/internal/infra"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/linkbuild"
	"github.com/outflareai/outflare/engine/internal/lists"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/mcphost"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/news"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/ownkeys"
	"github.com/outflareai/outflare/engine/internal/pipeline"
	"github.com/outflareai/outflare/engine/internal/platforms"
	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/rules"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/sending"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/support"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/telemetry"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/vendorsync"
	"github.com/outflareai/outflare/engine/internal/verify"
	"github.com/outflareai/outflare/engine/internal/webhooks"

	"golang.org/x/term"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/cli"
)

// Main runs the command named by os.Args[1] with the given extensions, and
// exits non-zero on failure. serve, migrate and version run the engine;
// anything else is the command line client (package cli).
func Main(exts ...Extension) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// With no command, a configured server serves; anywhere else it is the
	// client, which prints help.
	cmd := "help"
	if os.Getenv("DATABASE_URL") != "" {
		cmd = "serve"
	}
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if cmd == "verify-app" || cmd == "unverify-app" {
		os.Exit(verifyApp(context.Background(), log, cmd == "verify-app", os.Args[2:]))
	}
	if cmd == "features" {
		os.Exit(featuresCommand(context.Background(), os.Args[2:]))
	}
	if cmd != "serve" && cmd != "migrate" && cmd != "version" {
		// Anything else is the command line client.
		os.Exit(cli.Run(context.Background(), cli.Env{
			Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
			Version: api.Version, Spec: spec.OpenAPI, TTY: term.IsTerminal(int(os.Stdout.Fd())),
		}, append([]string{cmd}, os.Args[min(len(os.Args), 2):]...)))
	}
	if err := Run(context.Background(), log, cmd, exts...); err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

// verifyApp lets an operator say they checked an AI app that registered
// itself (its client id is on the app's row under Connect your AI), so
// people can give it more than reading: outflare verify-app <client_id>.
// unverify-app takes it back.
func verifyApp(ctx context.Context, log *slog.Logger, verified bool, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: outflare verify-app <client_id>, or outflare unverify-app <client_id>")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	found, err := (&api.Accounts{Pool: pool, Log: log}).VerifyAgentApp(ctx, args[0], verified)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 1
	case !found:
		fmt.Fprintln(os.Stderr, "no app has client id "+args[0])
		return 1
	}
	return 0
}

// Run is Main without the process around it: it runs cmd until ctx ends or
// the process is interrupted, and returns instead of exiting. Builds on top
// of the engine use it to run the whole engine in their own tests.
func Run(ctx context.Context, log *slog.Logger, cmd string, exts ...Extension) error {
	switch cmd {
	case "serve", "migrate":
	case "version":
		fmt.Println(api.Version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want serve, migrate or version)", cmd)
	}

	// Bring your own keys (AGENTS.md): a vendor key in the server's
	// environment, such as ANTHROPIC_API_KEY, would let an SDK or a child
	// process run a workspace's work on the operator's account. Remove it
	// before anything starts.
	ignoredKeys := ownkeys.Scrub()
	if len(ignoredKeys) > 0 {
		log.Warn("ignoring third-party keys in the environment: every workspace adds its own", "vars", ignoredKeys)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var box *secrets.Box
	if cmd == "serve" {
		if box, err = secrets.NewBox(cfg.SecretKey, cfg.SecretKeyPrevious...); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	log.Info("migrations", "applied", applied)
	if cmd == "migrate" {
		return nil
	}

	if box.Rotating() {
		go resealSecrets(ctx, box, pool, log)
	}
	queue := jobs.New(pool, log)
	tokens := &oauth.Tokens{
		Pool: pool, Box: box, Endpoints: oauth.DefaultEndpoints,
		Google:    oauth.Client{ID: cfg.GoogleClientID, Secret: cfg.GoogleClientSecret},
		Microsoft: oauth.Client{ID: cfg.MicrosoftClientID, Secret: cfg.MicrosoftClientSecret},
	}
	// Connectors and mailbox servers are addresses a workspace types in, so
	// they only reach the public internet unless the operator allows more.
	dialer, connectorHTTP := &mailbox.Dialer{}, &connectors.Client{}
	if !cfg.ConnectorsAllowPrivate {
		dialer.Control = netguard.Control
		connectorHTTP.HTTP = netguard.Client(45 * time.Second)
	}
	mail := &mailbox.Service{Pool: pool, Box: box, Dialer: dialer, Resolver: net.DefaultResolver, Tokens: tokens, Log: log}
	mail.Register(queue)
	send := &sending.Engine{Pool: pool, Mail: mail, Box: box, PublicURL: cfg.PublicURL, Log: log}
	send.Register(queue)
	syncer := &inbox.Syncer{Pool: pool, Mail: mail, Log: log}
	syncer.Register(queue)
	(&support.Sender{Pool: pool, Mail: mail, Log: log}).Register(queue)
	alloc := &allocation.Service{Pool: pool, Log: log}
	alloc.Register(queue)
	infraSvc := &infra.Service{Pool: pool, Mail: mail, Log: log}
	infraSvc.Register(queue)
	registry := catalog.New()
	oauthApps := connectors.OAuthAppsFromEnv(registry)
	connectors.AddMetadataClients(oauthApps, registry, cfg.PublicURL)
	integrations := &connectors.Store{Pool: pool, Box: box, Registry: registry, Client: connectorHTTP,
		OAuthApps: oauthApps}
	send.Platforms = integrations
	verifier := &verify.Service{Pool: pool, Store: integrations, Builtin: verifiers.Builtin{Resolver: net.DefaultResolver}, Log: log}
	verifier.Register(queue)
	lister := &lists.Service{Pool: pool, Store: integrations, Verify: verifier, Log: log}
	lister.Register(queue)
	// Email finding: learns each domain's address shape from the workspace's
	// own leads and their outcomes, and runs the find/verify waterfall.
	emailFinder := &emailfind.Service{Pool: pool, Verify: verifier, Resolver: net.DefaultResolver, Log: log}
	emailFinder.Register(queue)
	// The data network, when an extension registers one (Host.SetDataNetwork).
	dataNet := &datanetwork.Service{Pool: pool, Log: log}
	dataNet.Register(queue)
	emailFinder.Network = dataNet
	vendors := &vendorsync.Service{Pool: pool, Box: box, Store: integrations, Mail: mail, PublicURL: cfg.PublicURL, Log: log}
	// Infrastructure plans buy domains and mailboxes on the customer's own
	// Cheap Inboxes account.
	infra.RegisterSupplier(cheapinboxes.ID, cheapinboxes.Supplier{Open: func(ctx context.Context, ws, id string) (*connectors.Opened, error) {
		return integrations.Open(ctx, pool, ws, id)
	}})
	vendors.Register(queue)
	integrations.OnConnect = append(integrations.OnConnect, vendors.Connected)
	integrations.OnDisconnect = append(integrations.OnDisconnect, vendors.Disconnect)
	// Live updates: connectors that post webhooks to a connection's own
	// address (Geonode tells us when a scrape finishes).
	liveUpdates := &inbound.Service{Pool: pool, Box: box, Store: integrations, PublicURL: cfg.PublicURL, Log: log}
	liveUpdates.Register(queue)
	integrations.OnConnect = append(integrations.OnConnect, liveUpdates.Connected)
	integrations.OnDisconnect = append(integrations.OnDisconnect, liveUpdates.Disconnect)
	plat := &platforms.Service{Pool: pool, Store: integrations, Log: log}
	plat.Register(queue)
	importer := &imports.Service{Pool: pool, Store: integrations, Log: log}
	importer.Register(queue)
	results := &outcomes.Syncer{Pool: pool, Store: integrations, Log: log, PublicURL: cfg.PublicURL}
	results.Register(queue)
	analyticsSync := &analyticsync.Syncer{Pool: pool, Store: integrations, Log: log}
	analyticsSync.Register(queue)
	seoSync := &seosync.Syncer{Pool: pool, Store: integrations, Log: log}
	seoSync.Register(queue)
	rankChecks := &ranks.Service{Pool: pool, Store: integrations, Log: log}
	rankChecks.Register(queue)
	adSync := &adsync.Syncer{Pool: pool, Store: integrations, Log: log}
	adSync.Register(queue)
	adConv := &adconv.Sender{Pool: pool, Store: integrations, Log: log}
	adConv.Register(queue)
	// Research places unknown email domains by reading their homepage with
	// the workspace's own page reader and its own AI key. Its Models comes
	// from the API server below, so the monthly AI limit is checked in one
	// place.
	researcher := &research.Service{Pool: pool, Store: integrations, Log: log}
	researcher.Register(queue)
	labeller := &inbox.Labeller{Pool: pool, Log: log}
	labeller.Register(queue)
	(&inbox.Rules{Pool: pool, Log: log}).Register(queue)
	linker := &customers.Linker{Pool: pool}
	linker.Register(queue)
	(&customers.LeadFields{Pool: pool}).Register(queue)
	hooks := &webhooks.Service{Pool: pool, Box: box, Log: log, AllowPrivate: cfg.WebhooksAllowPrivate}
	hooks.Register(queue)
	crawler := &crawl.Service{Pool: pool, Log: log, Resolver: net.DefaultResolver, AllowPrivate: cfg.CrawlAllowPrivate,
		Fallback: &crawl.ConnectorFallback{Store: integrations, Pool: pool, Ledger: pool}, AfterAudit: seointel.AfterAudit,
		AfterCrawl: indexability.AfterCrawl}
	crawler.Register(queue)
	// Scans of websites for the cookies and scripts their cookie policy lists.
	cookieScans := &cookies.Service{Pool: pool, Log: log, AllowPrivate: cfg.CrawlAllowPrivate}
	cookieScans.Register(queue)
	// Link building: OutflareBot's checks of link prospects' pages.
	linkChecker := &linkbuild.Service{Pool: pool, Log: log, Crawl: crawler, Emails: emailFinder}
	linkChecker.Register(queue)
	// The news watch: OutflareBot reads each new article for its byline.
	(&news.Reader{Pool: pool, Log: log, Crawl: crawler, Emails: emailFinder}).Register(queue)
	// Production deploys of the websites a workspace watches on GitHub or
	// Vercel, which fix re-checks and experiments start from.
	deployWatch := &deploys.Service{Pool: pool, Store: integrations, Log: log}
	deployWatch.Register(queue)
	// SEO experiments start on the deploy of their site and end once
	// Search Console has sent their days.
	experiment := &experiments.Service{Pool: pool, Log: log}
	deployWatch.OnReady = append(deployWatch.OnReady, experiment.OnDeploy)
	// AI visibility asks the workspace's chosen AI models its prompts, with
	// its own keys; its Models comes from the API server below.
	visibility := &aivis.Service{Pool: pool, Log: log}
	visibility.Register(queue)
	// A deploy of a site re-checks its fixes with a fresh crawl.
	fixAuto := &fixauto.Service{Pool: pool, Log: log}
	fixAuto.Register(queue)
	deployWatch.OnReady = append(deployWatch.OnReady, fixAuto.OnDeploy)
	// Lifecycle and transactional email. Sends run on their own queue, so
	// a password reset never waits behind a backlog of other jobs.
	lifecycle := &emails.Service{Pool: pool, Store: integrations, Box: box, PublicURL: cfg.PublicURL, Log: log}
	lifecycle.Attach(integrations)
	lifecycle.Register(queue)
	queue.Skip = emails.SendKinds
	sendQueue := jobs.New(pool, log)
	sendQueue.Only = emails.SendKinds
	lifecycle.Register(sendQueue)
	// Journeys start runs from customer events and move them through their
	// steps, sending with the lifecycle service.
	journeyRuns := &journeys.Service{Pool: pool, Emails: lifecycle, Log: log, Store: integrations, Box: box, AllowPrivate: cfg.WebhooksAllowPrivate}
	journeyRuns.Register(queue)
	// Smart lists keep their members up to date from their rules.
	smart := &smartlists.Service{Pool: pool, Log: log}
	smart.Register(queue)
	// Rules turn what happens to records into business events of their
	// own, which journeys and webhooks act on.
	ruleEngine := &rules.Service{Pool: pool, Log: log}
	ruleEngine.Register(queue)
	// The daily or weekly digest of each workspace, emailed only through
	// the workspace's own email provider.
	digest := &digests.Service{Pool: pool, Emails: lifecycle, PublicURL: cfg.PublicURL, Log: log}
	digest.Register(queue)

	tel := telemetry.FromEnv(pool, log, api.Version)
	mailer, err := sysmail.FromEnv(log)
	if err != nil {
		return err
	}
	// Work that moves through stages one item at a time (docs/pipeline.md).
	stages := pipeline.NewRunner(pool, log)
	inboundMail := &inboundemail.Service{Pool: pool, Log: log}
	queue.Handle(inboundemail.JobProcess, inboundMail.Process)
	host := &Host{inboundEmail: inboundMail, Pool: pool, Log: log, PublicURL: cfg.PublicURL, signedInDomain: cfg.SignedInCookieDomain, queue: queue, box: box, telemetry: tel, mailer: mailer, store: integrations, emailFinder: emailFinder, dataNetwork: dataNet, stages: stages}
	for _, ext := range exts {
		if err := ext.Setup(ctx, host); err != nil {
			return fmt.Errorf("extension %s: %w", ext.Name(), err)
		}
		log.Info("extension loaded", "name", ext.Name())
	}
	// The hosted MCP server, when the build ships one (the Docker image does).
	var mcpHost *mcphost.Host
	var mcpUpstream *api.MCPUpstream
	var mcpMissing error
	if cfg.MCPServer != "" {
		mcpMissing = mcphost.Check(cfg.Node, cfg.MCPServer)
	}
	if cfg.MCPServer != "" && mcpMissing == nil {
		if mcpHost, err = mcphost.New(cfg.Node, cfg.MCPServer, mcphost.EngineURL(cfg.HTTPAddr), log); err != nil {
			return err
		}
		mcpUpstream = &api.MCPUpstream{URL: mcpHost.Upstream, Secret: mcpHost.Secret}
	}
	apiServer := api.NewServer(pool, log, api.Options{
		WebDir: cfg.WebDir, PublicURL: cfg.PublicURL, Mailboxes: mail, Sending: send, Inbox: syncer, OAuth: tokens, Allocation: alloc,
		Box: box, Integrations: integrations, Verify: verifier, EmailFinder: emailFinder, DataNetwork: dataNet, InboundEmail: inboundMail, Imports: importer, Platforms: plat, Lists: lister, Outcomes: results, AnalyticsSync: analyticsSync, SEOSync: seoSync, AdSync: adSync, Crawl: crawler, Vendors: vendors, Inbound: liveUpdates, Telemetry: tel, Mailer: mailer, Webhooks: hooks, Emails: lifecycle, Research: researcher, RateLimit: cfg.RateLimit, Passkeys: cfg.PasskeysOn(), ClientIPHeader: cfg.ClientIPHeader,
		TrackingTarget: cfg.TrackingTarget, PartnersDomain: cfg.PartnersDomain, TrackingCerts: host.trackingCerts, EdgeSecret: cfg.EdgeSecret,
		PostHogKey: cfg.PostHogKey, PostHogHost: cfg.PostHogHost, MCP: mcpUpstream, PrivacyURL: cfg.PrivacyURL, TermsURL: cfg.TermsURL, CommunityLinks: cfg.CommunityLinks, CommunityXURL: cfg.CommunityXURL, CommunityBuilderURL: cfg.CommunityBuilderURL, SignedInCookieDomain: cfg.SignedInCookieDomain,
		SiteInbox: true, QueryConnections: queryConnections(cfg.QueryConnections),
		Routes: host.registerRoutes, Middleware: host.middleware, Capabilities: host.capabilities, WorkspaceRoutes: host.workspaceRoutes, MenuLinks: host.menuLinks, AITools: host.aiTools,
	})
	// Journey steps that change fields write through the API's checks.
	journeyRuns.Fields = apiServer
	journeyRuns.Deals = apiServer
	journeyRuns.AI = apiServer.JourneyAI()
	host.server = apiServer
	researcher.Models = apiServer.AIModels()
	labeller.Models = apiServer.AIModels()
	linkChecker.Models = apiServer.AIModels()
	visibility.Models = apiServer.AIVisibilityModels()
	// The API server's own background work, such as CRM imports into
	// customer records.
	apiServer.RegisterJobs(queue)
	// Certificates for tracking domains verified before the server had a
	// provider (an extension's SetTrackingCerts).
	if err := apiServer.StartTrackingHTTPS(ctx); err != nil {
		track.Fail(ctx, log, track.Event{Name: "tracking_domain.https_failed", ActorType: "system", Surface: "system"}, "internal", err)
	}
	// Product events (every action and error) go to the operator's own
	// PostHog with the web app's, only when OUTFLARE_POSTHOG_KEY is set, and
	// into the operator's own workspace when OUTFLARE_EVENTS_WORKSPACE is.
	var sinks []track.Sink
	if cfg.EventsWorkspace != "" {
		sink, err := apiServer.ProductEventSink(ctx, cfg.EventsWorkspace)
		if err != nil {
			log.Error("product events: not kept in a workspace", "err", err)
		} else {
			sinks = append(sinks, sink)
		}
	}
	flushEvents := track.Start(ctx, cfg.PostHogKey, cfg.PostHogHost, api.Version, log, sinks...)
	// Marks each deploy and restart in PostHog, with the version it runs.
	track.Emit(ctx, track.Event{Name: "server.started", ActorType: "system", Surface: "system",
		Props: map[string]any{"migrations_applied": len(applied), "role": string(cfg.Role)}})
	if len(ignoredKeys) > 0 {
		track.Emit(ctx, track.Event{Name: "server.platform_keys_ignored", ActorType: "system", Surface: "system",
			Outcome: track.Failure, ErrorCode: "forbidden", Props: map[string]any{"vars": ignoredKeys, "count": len(ignoredKeys)}})
	}
	if mcpMissing != nil {
		track.Fail(ctx, log, track.Event{Name: "mcp.server_missing", ActorType: "system", Surface: "system"}, "not_configured", mcpMissing)
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           roleHandler(apiServer.Handler(), cfg.Role),
		ReadHeaderTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup
	errc := make(chan error, 3)
	metrics.SetVersion(api.Version, string(cfg.Role))
	if cfg.Role.Works() {
		metrics.SetWorkers(cfg.Workers + sendWorkers)
	}
	if cfg.MetricsAddr != "" {
		metrics.Registry.MustRegister(&metrics.Postgres{Pool: pool, Log: log})
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("metrics listening", "addr", cfg.MetricsAddr, "token", cfg.MetricsToken != "", "pprof", cfg.MetricsPprof)
			// A metrics problem never stops the server: it is logged and
			// recorded, and the API and workers carry on.
			if err := metrics.Serve(ctx, cfg.MetricsAddr, metrics.Options{Token: cfg.MetricsToken, Pprof: cfg.MetricsPprof}); err != nil {
				track.Fail(ctx, log, track.Event{Name: "metrics.listen_failed", ActorType: "system", Surface: "system"}, "unavailable", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("http listening", "addr", cfg.HTTPAddr, "version", api.Version, "role", cfg.Role)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	// The hosted MCP server answers /mcp, so it runs wherever the API does.
	if mcpHost != nil && (cfg.Role == config.RoleAll || cfg.Role == config.RoleAPI) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mcpHost.Run(ctx)
		}()
	}
	// The job queue and background loops run in the "all" and "worker"
	// roles; an api or ingest process only answers requests.
	if cfg.Role.Works() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("workers starting", "concurrency", cfg.Workers)
			if err := queue.Run(ctx, cfg.Workers); err != nil {
				errc <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sendQueue.Run(ctx, sendWorkers); err != nil {
				errc <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			stages.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			send.Run(ctx, 5*time.Second)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			syncer.Run(ctx, 30*time.Second)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			alloc.Loop(ctx, time.Minute)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			lister.Loop(ctx, 5*time.Minute)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			smart.Run(ctx, time.Minute)
		}()
		// Absence rules' timers.
		wg.Add(1)
		go func() {
			defer wg.Done()
			ruleEngine.Run(ctx, time.Minute)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			plat.Loop(ctx, time.Minute)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunMissions(ctx, 30*time.Second)
		}()
		// Playbooks that start by themselves, on a schedule or on events.
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunAutomations(ctx, time.Minute)
		}()
		// Browser events the collector queued, here or on an ingest server.
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunSiteInbox(ctx, time.Second)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunSocial(ctx, time.Minute)
		}()
		// Running A/B tests' checks: alerts when one is ready, its split
		// breaks or a guardrail is hurt.
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunABTests(ctx, 5*time.Minute)
		}()
		// Each visit's channel, and the credit for each conversion.
		wg.Add(1)
		go func() {
			defer wg.Done()
			(&attribution.Runner{Pool: pool, Log: log}).Run(ctx, time.Minute)
		}()
		// Meetings on people's timelines, as customer events.
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes.RunMeetingEvents(ctx, pool, log, time.Minute)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunSetup(ctx, time.Minute)
		}()
		// Meaning-based search's vectors, kept current.
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunSemanticSearch(ctx, 5*time.Minute)
		}()
		// The data network: sharing joined workspaces' settled sends, and
		// withdrawing what workspaces that left shared.
		wg.Add(1)
		go func() {
			defer wg.Done()
			dataNet.Run(ctx, time.Hour)
		}()
		// Raw inbound messages are deleted after 30 days.
		wg.Add(1)
		go func() {
			defer wg.Done()
			inboundMail.RunPurge(ctx, time.Hour)
		}()
		// Exchange rates for receipts charged in other currencies, fetched
		// only where inbound email is on.
		rates := &fxrates.Service{Pool: pool, Log: log}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rates.Run(ctx, 6*time.Hour, inboundMail.Enabled, func(ctx context.Context) error {
				_, err := aiaccounts.ConvertPending(ctx, pool)
				return err
			})
		}()
		// The AI usage log keeps 13 months of calls.
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunAIUsage(ctx, time.Hour)
		}()
		monitor := &health.Monitor{Pool: pool, Log: log}
		wg.Add(1)
		go func() {
			defer wg.Done()
			monitor.Run(ctx, 5*time.Minute)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			results.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			analyticsSync.Run(ctx)
		}()
		// Optional analytics storage (docs/adr/0010-analytics-storage.md).
		if cfg.ClickHouseURL != "" {
			if ch, err := eventstore.Open(cfg.ClickHouseURL); err != nil {
				log.Error("clickhouse: not used", "err", err)
			} else {
				mirror := &eventstore.Mirror{Pool: pool, CH: ch, Log: log}
				wg.Add(1)
				go func() {
					defer wg.Done()
					mirror.Run(ctx)
				}()
			}
		}
		var bucket *blobs.Store
		if cfg.S3.On() {
			b, err := blobs.New(blobs.Config{Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.Bucket, AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey})
			if err == nil {
				err = b.Check(ctx)
			}
			if err != nil {
				track.Fail(ctx, log, track.Event{Name: "object_storage.failed", ActorType: "system", Surface: "system"}, "unavailable", err)
			} else {
				bucket = b
			}
		}
		if cfg.EventRetentionDays > 0 {
			archiver := &eventstore.Archiver{Pool: pool, Blobs: bucket, Log: log, Days: cfg.EventRetentionDays}
			if cfg.S3.On() && bucket == nil {
				// The bucket is set up but failing: keep events rather than
				// drop what should have been archived.
				log.Error("event retention paused: object storage is set up but not working")
			} else {
				wg.Add(1)
				go func() {
					defer wg.Done()
					archiver.Run(ctx)
				}()
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunPayments(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiServer.RunPartnerRewards(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			experiment.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			seoSync.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			rankChecks.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			linkChecker.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			adSync.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			adConv.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			crawler.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			cookieScans.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			deployWatch.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			digest.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			visibility.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			vendors.Run(ctx, 30*time.Second)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			liveUpdates.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			infraSvc.Run(ctx, time.Minute)
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			tel.Run(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			housekeeping(ctx, log, apiServer, time.Duration(cfg.LogRetentionDays)*24*time.Hour)
		}()

		for _, fn := range host.background {
			wg.Add(1)
			go func() {
				defer wg.Done()
				fn(ctx)
			}()
		}
	}

	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx) //nolint:errcheck
	stop()
	wg.Wait()
	apiServer.FlushLogs(shutdownCtx) //nolint:errcheck
	flushEvents()
	return err
}

// sendWorkers is how many workers run the send queue, beside WORKERS.
const sendWorkers = 4

// housekeeping deletes what the API keeps only for a while, once an hour.
func housekeeping(ctx context.Context, log *slog.Logger, s *api.Server, logRetention time.Duration) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := s.PruneIdempotencyKeys(ctx); err != nil && ctx.Err() == nil {
			log.Error("prune idempotency keys", "err", err)
		} else if n > 0 {
			log.Info("pruned idempotency keys", "deleted", n)
		}
		if n, err := s.PruneWebhookDeliveries(ctx); err != nil && ctx.Err() == nil {
			log.Error("prune webhook deliveries", "err", err)
		} else if n > 0 {
			log.Info("pruned webhook deliveries", "deleted", n)
		}
		if n, err := s.PruneRequestLogs(ctx, logRetention); err != nil && ctx.Err() == nil {
			log.Error("prune request logs", "err", err)
		} else if n > 0 {
			log.Info("pruned request logs", "deleted", n)
		}
		if n, err := s.PruneJobs(ctx); err != nil && ctx.Err() == nil {
			log.Error("prune jobs", "err", err)
		} else if n > 0 {
			log.Info("pruned jobs", "deleted", n)
		}
		if n, err := s.PruneOAuthCodes(ctx); err != nil && ctx.Err() == nil {
			log.Error("prune oauth codes", "err", err)
		} else if n > 0 {
			log.Info("pruned oauth codes and unused apps", "deleted", n)
		}
		if n, err := s.PruneKnowledgeBin(ctx); err != nil && ctx.Err() == nil {
			log.Error("prune knowledge bin", "err", err)
		} else if n > 0 {
			log.Info("deleted knowledge kept in the bin over 30 days", "deleted", n)
		}
		if n, err := s.PruneInboundWebhookIDs(ctx); err != nil && ctx.Err() == nil {
			log.Error("prune inbound webhook ids", "err", err)
		} else if n > 0 {
			log.Info("pruned inbound webhook ids", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// resealSecrets re-encrypts stored secrets with the current key while an
// earlier one is still listed, so the earlier one can then be dropped.
func resealSecrets(ctx context.Context, box *secrets.Box, pool *pgxpool.Pool, log *slog.Logger) {
	res, err := box.ResealStored(ctx, pool)
	ev := track.Event{Name: "secrets.resealed", ActorType: "system", Surface: "system",
		Props: map[string]any{"resealed": res.Resealed, "current": res.Current, "unreadable": res.Unreadable}}
	if err != nil {
		track.Fail(ctx, log, ev, "internal", err)
		return
	}
	if res.Unreadable > 0 {
		ev.Outcome = track.Error
		ev.ErrorCode = "unreadable_secrets"
		log.Error("some stored secrets open with none of the keys; was an older OUTFLARE_SECRET_KEY left out of OUTFLARE_SECRET_KEY_PREVIOUS?", "unreadable", res.Unreadable)
	}
	log.Info("secrets resealed with the current OUTFLARE_SECRET_KEY; once every server runs it, OUTFLARE_SECRET_KEY_PREVIOUS can be removed (unsubscribe links in mail sent before still need it to verify)",
		"resealed", res.Resealed, "current", res.Current, "unreadable", res.Unreadable)
	track.Emit(ctx, ev)
}

// queryConnections maps OUTFLARE_QUERY_CONNECTIONS, where 0 turns Query
// off, onto api.Options, where 0 is the default.
func queryConnections(n int) int {
	if n == 0 {
		return -1
	}
	return n
}
