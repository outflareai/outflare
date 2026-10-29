// Package metrics is the engine's operational metrics: how busy, slow and
// healthy the server is, for the operator's own dashboards and alerts
// (docs/monitoring.md). It is separate from product events (track), which
// say what people do, and from the anonymous usage report (telemetry).
//
// The counters below are always kept, which costs nothing; they are only
// readable when OUTFLARE_METRICS_ADDR turns on the private listener in
// Serve. Labels are routes, job kinds, outcomes and table names, never
// workspace ids, paths with ids or anything a customer typed, so the series
// stay few and say nothing about anyone.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const namespace = "outflare"

// Registry holds every Outflare metric plus the Go runtime and process
// collectors (CPU, memory, goroutines, garbage collection, open files).
var Registry = prometheus.NewRegistry()

// Buckets in seconds, shared by requests and jobs so dashboards compare
// them directly. Kept short: each bucket is a series per route or kind.
var (
	requestBuckets = []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	jobBuckets     = []float64{0.05, 0.25, 1, 5, 15, 60, 300, 900, 3600}
)

var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "http", Name: "requests_total",
		Help: "HTTP requests served, by route pattern and status class.",
	}, []string{"route", "code"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
		Help: "Time to serve an HTTP request, by route pattern.", Buckets: requestBuckets,
	}, []string{"route"})
	httpRateLimited = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "http", Name: "rate_limited_total",
		Help: "Requests refused with 429 by a rate limit, by limit name.",
	}, []string{"limit"})
	httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "http", Name: "requests_in_flight",
		Help: "HTTP requests being served right now.",
	})

	jobsDone = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "jobs", Name: "processed_total",
		Help: "Jobs this process ran, by kind and outcome (succeeded, retried, dead).",
	}, []string{"kind", "outcome"})
	jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "jobs", Name: "duration_seconds",
		Help: "Time a job's handler ran, by kind.", Buckets: jobBuckets,
	}, []string{"kind"})
	jobWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "jobs", Name: "wait_seconds",
		Help: "Time from a job being due to a worker claiming it, by kind.", Buckets: jobBuckets,
	}, []string{"kind"})
	jobsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "jobs", Name: "active",
		Help: "Jobs this process is running right now.",
	})
	jobsReaped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "jobs", Name: "reaped_total",
		Help: "Running jobs this process put back in the queue because their lease ran out, by kind. Each one may run twice.",
	}, []string{"kind"})
	workers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "jobs", Name: "workers",
		Help: "Job worker goroutines this process runs (WORKERS).",
	})

	siteEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ingest", Name: "events_total",
		Help: "Browser events the collector received, by outcome (accepted, rejected for their shape, capped by the daily limits).",
	}, []string{"outcome"})
	// Tracking health: how well landing pages from paid ads are tracked.
	paidArrivals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ad_tracking", Name: "arrivals_total",
		Help: "Landing pages from paid ads (a paid utm_medium) by network and outcome: click_id (the network's own click id), ad_ids (only the tracking template's ad ids) or none (no tracking template on the ad).",
	}, []string{"network", "outcome"})
	adMacrosUnfilled = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ad_tracking", Name: "unfilled_macros_total",
		Help: "Tracking template values that arrived as the macro itself ({{ad.id}}, __CID__): the network didn't fill them in, so the template or its macro is wrong for that network.",
	}, []string{"network"})
	redirectClicks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "redirect", Name: "clicks_total",
		Help: "Clicks on campaign redirect links (/l/{code}), by outcome (counted, bot for a link scanner or preview, unknown for a code that isn't a link, error when the click couldn't be stored but the person was still sent on).",
	}, []string{"outcome"})
	inboxEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "inbox", Name: "events_total",
		Help: "Browser events workers took from the inbox, by outcome (stored, rejected, failed).",
	}, []string{"outcome"})
	inboxBatch = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "inbox", Name: "batch_seconds",
		Help: "Time to store one inbox row (a browser's batch of events).", Buckets: requestBuckets,
	})

	// Feature flags decided for websites (the tracking script) and servers
	// (the API).
	flagRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "flags", Name: "requests_total",
		Help: "Requests to decide a visitor's feature flags, by surface (site, api) and outcome (ok, refused, error).",
	}, []string{"surface", "outcome"})
	flagDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "flags", Name: "request_seconds",
		Help: "Time to decide a visitor's feature flags, by surface.", Buckets: requestBuckets,
	}, []string{"surface"})
	abResults = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ab", Name: "results_total",
		Help: "A/B test results counted from product events, by outcome (ok, error).",
	}, []string{"outcome"})
	abResultsDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "ab", Name: "results_seconds",
		Help: "Time to count an A/B test's results from product events, every goal included.", Buckets: requestBuckets,
	})
	flagCache = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "flags", Name: "cache_total",
		Help: "Lookups of a workspace's flags, by result (hit: from memory, miss: read from Postgres).",
	}, []string{"result"})

	// The consent banner in the website script: its settings asked for on
	// a visitor's first page, and the choices visitors make.
	consentRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "site_consent", Name: "requests_total",
		Help: "Consent banner requests from websites, by endpoint (config, choice) and outcome (ok, refused, error).",
	}, []string{"endpoint", "outcome"})
	consentDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "site_consent", Name: "request_seconds",
		Help: "Time to answer a consent banner request, by endpoint.", Buckets: requestBuckets,
	}, []string{"endpoint"})
	cookieScans = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "cookie_scan", Name: "scans_total",
		Help: "Scans of websites for cookies and scripts, by outcome (ok, failed: no page could be read).",
	}, []string{"outcome"})
	cookieScanPages = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "cookie_scan", Name: "pages_total",
		Help: "Pages cookie scans read.",
	})
	cookieScanFound = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "cookie_scan", Name: "found_total",
		Help: "Cookies, scripts and embeds cookie scans found (seen again counts again).",
	})
	cookieScanDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "cookie_scan", Name: "seconds",
		Help: "Time one cookie scan takes, pauses between pages included.", Buckets: []float64{1, 5, 15, 30, 60, 120, 300},
	})
	cookieReports = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "cookie_scan", Name: "reports_total",
		Help: "Reports from visitors' browsers of a site's cookies and scripts, by outcome (ok, refused, error).",
	}, []string{"outcome"})
	cookieNew = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "cookie_scan", Name: "new_total",
		Help: "Cookies, storage keys and scripts found on a site for the first time, by whether the catalogue knew them (known, unknown).",
	}, []string{"known"})
	consentChoices = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "site_consent", Name: "choices_total",
		Help: "Consent choices kept, by action (accept_all, reject_all, custom, opt_out, gpc, api) and region (eea, us, other).",
	}, []string{"action", "region"})

	// AI calls made with workspaces' own keys. Labels are the provider and
	// the tool (a fixed task id), never a workspace or a model a person
	// typed.
	aiCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai", Name: "calls_total",
		Help: "AI model calls booked, by provider and tool.",
	}, []string{"provider", "task"})
	aiTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai", Name: "tokens_total",
		Help: "Tokens AI model calls used, by provider, tool and kind (input, output, cache_read, cache_write).",
	}, []string{"provider", "task", "kind"})
	aiCost = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai", Name: "cost_cents_total",
		Help: "What AI model calls cost on workspaces' own provider bills, in US cents, by provider and tool.",
	}, []string{"provider", "task"})
	aiBudgetAlerts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai", Name: "budget_alerts_total",
		Help: "Workspaces warned about their AI spend, by level (80: most of a limit, 100: reached it) and scope (workspace or tool).",
	}, []string{"level", "scope"})

	// Usage limits on paid connections. connector is a catalog id.
	connectionLimitAlerts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "connection_limits", Name: "alerts_total",
		Help: "Workspaces warned that a paid connection used most of a usage limit (level 80) or reached it (level 100), by period (day or month).",
	}, []string{"level", "period"})
	connectionLimitStops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "connection_limits", Name: "stops_total",
		Help: "Paid connector calls Outflare did not make because the connection reached a usage limit, by connector and period.",
	}, []string{"connector", "period"})

	// Incoming webhooks from connectors. connector is a catalog id (or
	// "unknown" for an address that matches no connection).
	hooksReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "hooks", Name: "deliveries_total",
		Help: "Webhook deliveries connectors sent to /v1/hooks, by connector and outcome (accepted, duplicate, bad_signature, not_found, invalid, error).",
	}, []string{"connector", "outcome"})
	hookEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "hooks", Name: "events_total",
		Help: "Webhook events workers finished, by connector and result (handled, ignored, failed, retry).",
	}, []string{"connector", "result"})
	hookLag = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "hooks", Name: "handle_lag_seconds",
		Help: "Time from a webhook event arriving to a worker finishing it, by connector.", Buckets: []float64{.1, .5, 1, 5, 15, 60, 300, 900, 3600},
	}, []string{"connector"})
	// Domain Connect: looking up a domain's DNS provider for the one-off
	// "apply these records?" screen.
	domainConnectLookups = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "domain_connect", Name: "lookups_total",
		Help: "Domain Connect lookups, by outcome: available, or why not (not_configured, no_provider, not_onboarded, no_template, nothing_to_add, lookup_failed).",
	}, []string{"outcome"})
	domainConnectDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "domain_connect", Name: "lookup_seconds",
		Help:    "How long a Domain Connect lookup took, DNS and the provider's API included (cached answers are near zero).",
		Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})
	// Query: read-only SQL run for people and agents.
	ruleEvaluations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "rules", Name: "evaluations_total",
		Help: "Rules evaluated for a record, by trigger kind and outcome (fired; or why not: no_match, duplicate, cooldown, late, before_live, too_deep, manual; absence clocks started and stopped).",
	}, []string{"trigger", "outcome"})
	// Journey webhook and Slack steps: calls out of a run, with retries.
	journeyCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "journeys", Name: "calls_total",
		Help: "Calls journey steps made, by step kind (webhook, slack) and outcome (done; retried, for a try that will be tried again; failed).",
	}, []string{"kind", "outcome"})
	journeyCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "journeys", Name: "call_duration_seconds",
		Help: "Time one try of a journey step's call took, by step kind.", Buckets: requestBuckets,
	}, []string{"kind"})
	// Exclusions: people a journey, broadcast or sequence left out.
	excluded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "automations", Name: "excluded_total",
		Help: "People an automation's exclusion left out, by where (journey: refused entry; broadcast: skipped; sequence: not enrolled).",
	}, []string{"where"})
	queryRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query", Name: "runs_total",
		Help: "Queries run, by outcome (success, invalid, timeout, unavailable, error).",
	}, []string{"outcome"})
	queryDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "query", Name: "duration_seconds",
		Help: "Time a query took, from waiting for a slot to its last row.", Buckets: requestBuckets,
	})
	queryRows = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query", Name: "rows_total",
		Help: "Rows queries returned.",
	})
	// Meaning-based search: vectors made with workspaces' own AI keys.
	semanticRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "semantic_search", Name: "embed_runs_total",
		Help: "Embedding jobs run, by outcome (success, failed).",
	}, []string{"outcome"})
	semanticItems = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "semantic_search", Name: "embedded_total",
		Help: "Records, replies, meetings and deals turned into vectors.",
	})
	semanticDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "semantic_search", Name: "embed_run_seconds",
		Help: "Time one embedding job took, the provider's API included.", Buckets: []float64{.1, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	})
	knowledgeAsks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "asks_total",
		Help: "Questions asked of the knowledge base, by outcome (answered, unanswered, no_ai, error).",
	}, []string{"outcome"})
	knowledgeAskDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "ask_seconds",
		Help: "Time to answer a question from knowledge: finding passages and the AI model's answer.", Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 20, 30, 60},
	})
	semanticAvailable = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "semantic_search", Name: "available",
		Help: "1 when the database has pgvector, so meaning-based search can run; 0 when it hasn't.",
	})
	semanticWorkspaces = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "semantic_search", Name: "workspaces",
		Help: "Workspaces with meaning-based search turned on.",
	})

	// Conversions sent back to ad platforms. connector is a catalog id.
	adConversions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ad_conversions", Name: "total",
		Help: "Conversions sent back to ad platforms, by connector and outcome (sent, skipped, failed).",
	}, []string{"connector", "outcome"})
	adConversionsOldest = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "ad_conversions", Name: "oldest_queued_seconds",
		Help: "How long the oldest conversion waiting to be sent has waited, as of this process's last round.",
	})

	// Meetings on people's timelines (outcomes.MeetingEvents).
	meetingEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "meeting_events", Name: "total",
		Help: "Meeting events recorded on people's timelines, by status (booked, held, no_show, canceled).",
	}, []string{"status"})
	meetingEventsPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "meeting_events", Name: "pending",
		Help: "Meetings tied to a person whose latest status isn't recorded as an event yet, as of this process's last round.",
	})

	// Reviews: reading brands' review pages through the workspace's own
	// page reader.
	reviewReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "reviews", Name: "page_reads_total",
		Help: "Review page reads, by platform and outcome (success, pending, blocked, slow_page, page_not_found, not_a_review_page, save_failed, connector_error).",
	}, []string{"platform", "outcome"})
	// Scheduled syncs of connections (analytics, Search Console, page
	// conversions, ads, SEO snapshots).
	syncRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "sync", Name: "runs_total",
		Help: "Scheduled sync runs of connections, by module (analytics, search_console, page_conversions, ads, seo) and outcome (success, internal, connector_auth, connector_quota, connector_unavailable, connector_invalid).",
	}, []string{"module", "outcome"})
	// Social accounts: reading followers of connected accounts and of the
	// public profiles in brand links.
	socialReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "social", Name: "account_reads_total",
		Help: "Social account reads, by channel and outcome (success, blocked, slow_page, page_not_found, invalid_address, no_follower_count, save_failed, connector_error).",
	}, []string{"channel", "outcome"})
	reviewsFound = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "reviews", Name: "new_total",
		Help: "Reviews kept for the first time, by platform (a page's first read included).",
	}, []string{"platform"})

	// Social publishing: the brand's own posts on X and LinkedIn.
	socialPublishes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "social", Name: "publishes_total",
		Help: "Posts the publish job tried, by channel and outcome (published, failed: the channel refused or the outcome is unknown, retried: a rate limit before anything was posted).",
	}, []string{"channel", "outcome"})

	// Link reads: who links to a page or domain, from the workspace's
	// own SEO connection.
	linkReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "seo", Name: "link_reads_total",
		Help: "Reads of who links to a page or domain, by reason (requested, dead_page) and outcome (success, budget_reached, connector_auth, invalid_request, provider_error, internal).",
	}, []string{"reason", "outcome"})

	// Partner programs: short link clicks, conversions reported by the
	// workspace's backend or a postback, and the reward ledger.
	partnerClicks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "partners", Name: "clicks_total",
		Help: "Partner short link clicks, by outcome (tracked, inactive, not_found, error).",
	}, []string{"outcome"})
	partnerConversions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "partners", Name: "conversions_total",
		Help: "Partner conversions reported, by type (signup, sale, refund, custom), source (api, postback) and outcome (credited, not_credited, duplicate, rejected, error).",
	}, []string{"type", "source", "outcome"})
	partnerRewards = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "partners", Name: "rewards_total",
		Help: "Reward ledger changes, by kind (cash, fee, action, adjustment) and event (created, approved, rejected, reversed, fulfilled, paid).",
	}, []string{"kind", "event"})
	partnerPostbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "partners", Name: "postbacks_total",
		Help: "Postbacks to partners' own trackers, by outcome (sent, retried, rejected: the tracker refused it, unreachable: failed after every retry, invalid).",
	}, []string{"outcome"})
	partnerImports = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "partners", Name: "imports_total",
		Help: "Partner program imports from another platform that finished, by connector and outcome (done, failed).",
	}, []string{"connector", "outcome"})
	partnerImportObjects = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "partners", Name: "import_objects_total",
		Help: "Objects partner imports read, by kind (links, partners, promo_codes, customers, transactions, commissions) and result (created, updated, unchanged, skipped).",
	}, []string{"kind", "result"})
	partnerRewardsOverdue = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "partners", Name: "rewards_overdue_seconds",
		Help: "How long past its hold the oldest reward that should approve on its own has waited, as of this process's last round.",
	})

	// News watch: saved news searches run through the workspace's own
	// web search connection.
	newsRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "news", Name: "search_runs_total",
		Help: "News search runs, by purpose (topic, coverage) and outcome (success, connector_needed, usage_limit_reached, connector_error, internal).",
	}, []string{"purpose", "outcome"})
	newsFound = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "news", Name: "found_total",
		Help: "What news search runs kept for the first time, by kind (article: a new article in the workspace, coverage: a new coverage item for a brand).",
	}, []string{"kind"})
	newsReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "news", Name: "article_reads_total",
		Help: "Reads of news articles' pages for their byline, by status (up, gone, blocked, opted_out, unreachable, failed) and journalist (true when the read found a new journalist).",
	}, []string{"status", "journalist"})

	linkPageChecks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "seo", Name: "link_page_checks_total",
		Help: "OutflareBot's checks of link prospects' pages, by status (up, gone, redirected, blocked, opted_out, unreachable, failed).",
	}, []string{"status"})
	archiveLookups = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "seo", Name: "archive_lookups_total",
		Help: "Internet Archive lookups of broken links, by outcome (found, none, error).",
	}, []string{"outcome"})

	linkPitches = linkCounter("link_pitches_written_total",
		"Openers the workspace's AI wrote for link pitches, by outcome (written, no_ai, ai_unavailable, ai_failed).", "outcome")
	linkReplies = linkCounter("link_replies_read_total",
		"Replies to link pitches the workspace's AI read, by what they say (agreed, declined, wants_payment, wants_content, other, no_ai, ai_unavailable, ai_failed).", "kind")
	linkLiveChecks = linkCounter("link_live_checks_total",
		"Checks of agreed and live links, by what they found (live, not_yet, removed, still_live, unreachable).", "outcome")
	webSearches = linkCounter("web_searches_total",
		"Searches on workspaces' own SERP connections (search-results), by outcome (done, budget_reached, connector_invalid, connector_auth, connector_quota, connector_unavailable).", "outcome")

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info",
		Help: "Always 1; the labels are the running engine version and its role (OUTFLARE_ROLE).",
	}, []string{"version", "role"})
)

// own are the metrics this package keeps itself; Postgres is registered
// separately because it needs the pool.
var own = []prometheus.Collector{
	httpRequests, httpDuration, httpInFlight, httpRateLimited,
	jobsDone, jobDuration, jobWait, jobsActive, jobsReaped, workers,
	siteEvents, inboxEvents, inboxBatch, redirectClicks, paidArrivals, adMacrosUnfilled,
	flagRequests, flagDuration, flagCache, abResults, abResultsDuration,
	consentRequests, consentDuration, consentChoices,
	cookieScans, cookieScanPages, cookieScanFound, cookieScanDuration, cookieReports, cookieNew,
	pipelineItems, pipelineCodes, pipelineBatch, pipelineBatchSize, pipelineWait, pipelineLimitWaits, pipelineLimitReports,
	aiCalls, aiTokens, aiCost, aiBudgetAlerts,
	connectionLimitAlerts, connectionLimitStops,
	hooksReceived, hookEvents, hookLag,
	domainConnectLookups, domainConnectDuration,
	queryRuns, queryDuration, queryRows,
	ruleEvaluations, journeyCalls, journeyCallDuration, excluded,
	semanticRuns, semanticItems, semanticDuration, semanticAvailable, semanticWorkspaces, knowledgeAsks, knowledgeAskDuration,
	adConversions, adConversionsOldest,
	meetingEvents, meetingEventsPending,
	reviewReads, reviewsFound, socialReads, syncRuns, linkReads, linkPageChecks, archiveLookups, linkPitches, linkReplies, linkLiveChecks, webSearches,
	socialPublishes,
	partnerClicks, partnerConversions, partnerRewards, partnerRewardsOverdue, partnerImports, partnerImportObjects, partnerPostbacks,
	newsRuns, newsFound, newsReads,
	buildInfo,
}

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	Registry.MustRegister(own...)
}

// SetVersion records the running version and role in outflare_build_info.
func SetVersion(v, role string) { buildInfo.WithLabelValues(v, role).Set(1) }

// JobReaped counts a running job of this kind that was requeued because its
// lease expired.
func JobReaped(kind string) { jobsReaped.WithLabelValues(kind).Inc() }

// SetWorkers records how many job workers this process runs.
func SetWorkers(n int) { workers.Set(float64(n)) }

// RequestStarted counts a request in flight; call the returned func when
// it is done with its route pattern ("GET /v1/workspaces/{ws}/leads", or
// empty when no route matched) and status.
func RequestStarted() func(route string, status int, d time.Duration) {
	httpInFlight.Inc()
	return func(route string, status int, d time.Duration) {
		httpInFlight.Dec()
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(route, statusClass(status)).Inc()
		httpDuration.WithLabelValues(route).Observe(d.Seconds())
	}
}

// RateLimited counts a request a rate limit refused, by the limit's name.
func RateLimited(limit string) { httpRateLimited.WithLabelValues(limit).Inc() }

func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "other"
	}
	return strconv.Itoa(status/100) + "xx"
}

// Job outcomes.
const (
	JobSucceeded = "succeeded"
	JobRetried   = "retried"
	JobDead      = "dead"
)

// JobStarted counts a job as running and records how long it waited after
// it was due; call the returned func with its outcome when it finishes.
func JobStarted(kind string, waited time.Duration) func(outcome string) {
	start := time.Now()
	jobsActive.Inc()
	if waited < 0 {
		waited = 0
	}
	jobWait.WithLabelValues(kind).Observe(waited.Seconds())
	return func(outcome string) {
		jobsActive.Dec()
		jobDuration.WithLabelValues(kind).Observe(time.Since(start).Seconds())
		jobsDone.WithLabelValues(kind, outcome).Inc()
	}
}

// SiteEventsReceived counts browser events the collector accepted and
// rejected.
func SiteEventsReceived(accepted, rejected int) {
	siteEvents.WithLabelValues("accepted").Add(float64(accepted))
	siteEvents.WithLabelValues("rejected").Add(float64(rejected))
}

// PaidArrival counts one landing page from a paid ad by network and how
// it was tracked (click_id, ad_ids or none).
func PaidArrival(network, outcome string) { paidArrivals.WithLabelValues(network, outcome).Inc() }

// AdMacrosUnfilled counts template values a network left as the macro.
func AdMacrosUnfilled(network string, n int) {
	adMacrosUnfilled.WithLabelValues(network).Add(float64(n))
}

// RedirectClick counts one click on a campaign redirect link by outcome:
// counted, bot, unknown or error.
func RedirectClick(outcome string) { redirectClicks.WithLabelValues(outcome).Inc() }

// SiteEventsCapped counts browser events turned away by a daily limit.
func SiteEventsCapped(n int) { siteEvents.WithLabelValues("capped").Add(float64(n)) }

// SiteConsentRequest records one consent banner request: its endpoint
// (config, choice), outcome (ok, refused, error) and time.
func SiteConsentRequest(endpoint, outcome string, d time.Duration) {
	consentRequests.WithLabelValues(endpoint, outcome).Inc()
	consentDuration.WithLabelValues(endpoint).Observe(d.Seconds())
}

// CookieScanned records one cookie scan: its outcome, the pages it read,
// what it found and how long it took.
func CookieScanned(outcome string, pages, found int, d time.Duration) {
	cookieScans.WithLabelValues(outcome).Inc()
	cookieScanPages.Add(float64(pages))
	cookieScanFound.Add(float64(found))
	cookieScanDuration.Observe(d.Seconds())
}

// CookieReport counts one browser's report of a site's cookies.
func CookieReport(outcome string) { cookieReports.WithLabelValues(outcome).Inc() }

// CookiesNew counts what was found on a site for the first time.
func CookiesNew(known, unknown int) {
	cookieNew.WithLabelValues("known").Add(float64(known))
	cookieNew.WithLabelValues("unknown").Add(float64(unknown))
}

// SiteConsentChoice counts one visitor's consent choice kept.
func SiteConsentChoice(action, region string) { consentChoices.WithLabelValues(action, region).Inc() }

// InboxStored records one inbox row a worker finished: how many of its
// events were stored and rejected, or failed when the row failed.
func InboxStored(stored, rejected, failed int, d time.Duration) {
	inboxEvents.WithLabelValues("stored").Add(float64(stored))
	inboxEvents.WithLabelValues("rejected").Add(float64(rejected))
	inboxEvents.WithLabelValues("failed").Add(float64(failed))
	inboxBatch.Observe(d.Seconds())
}

// FlagsDecided records one request to decide a visitor's flags: its
// surface (site, api), outcome (ok, refused, error) and time.
func FlagsDecided(surface, outcome string, d time.Duration) {
	flagRequests.WithLabelValues(surface, outcome).Inc()
	flagDuration.WithLabelValues(surface).Observe(d.Seconds())
}

// FlagCache counts a lookup of a workspace's flags: hit when they were in
// memory.
func FlagCache(hit bool) {
	if hit {
		flagCache.WithLabelValues("hit").Inc()
	} else {
		flagCache.WithLabelValues("miss").Inc()
	}
}

// RuleEvaluated counts one rule evaluated for one record, by trigger kind
// and outcome.
func RuleEvaluated(trigger, outcome string) { ruleEvaluations.WithLabelValues(trigger, outcome).Inc() }

// JourneyCalled records one try of a journey step's call: its kind
// (webhook, slack), outcome (done, retried, failed) and time.
func JourneyCalled(kind, outcome string, d time.Duration) {
	journeyCalls.WithLabelValues(kind, outcome).Inc()
	journeyCallDuration.WithLabelValues(kind).Observe(d.Seconds())
}

// Excluded records people an exclusion left out of a journey, a
// broadcast or a sequence.
func Excluded(where string) { excluded.WithLabelValues(where).Inc() }

// ExcludedN records n people an exclusion left out at once.
func ExcludedN(where string, n int) {
	if n > 0 {
		excluded.WithLabelValues(where).Add(float64(n))
	}
}

// ABResults records counting one A/B test's results: its outcome (ok,
// error) and time.
func ABResults(outcome string, d time.Duration) {
	abResults.WithLabelValues(outcome).Inc()
	abResultsDuration.Observe(d.Seconds())
}

// QueryRan records one query: its outcome, time and rows returned.
func QueryRan(outcome string, d time.Duration, rows int) {
	queryRuns.WithLabelValues(outcome).Inc()
	queryDuration.Observe(d.Seconds())
	queryRows.Add(float64(rows))
}

// SemanticEmbedded records one embedding job: its outcome, how many items
// it embedded and how long it took.
func SemanticEmbedded(outcome string, items int, d time.Duration) {
	semanticRuns.WithLabelValues(outcome).Inc()
	semanticItems.Add(float64(items))
	semanticDuration.Observe(d.Seconds())
}

// KnowledgeAsked records one question asked of the knowledge base: its
// outcome and how long the answer took.
func KnowledgeAsked(outcome string, d time.Duration) {
	knowledgeAsks.WithLabelValues(outcome).Inc()
	knowledgeAskDuration.Observe(d.Seconds())
}

// SetSemanticAvailable says whether the database has pgvector.
func SetSemanticAvailable(ok bool) {
	if ok {
		semanticAvailable.Set(1)
	} else {
		semanticAvailable.Set(0)
	}
}

// SetSemanticWorkspaces is how many workspaces have meaning-based search on.
func SetSemanticWorkspaces(n int) { semanticWorkspaces.Set(float64(n)) }

// AICall records one AI model call made with a workspace's own key: its
// provider (connector id), tool, tokens and cost in cents.
func AICall(provider, task string, input, output, cacheRead, cacheWrite, cents int64) {
	aiCalls.WithLabelValues(provider, task).Inc()
	for kind, n := range map[string]int64{"input": input, "output": output, "cache_read": cacheRead, "cache_write": cacheWrite} {
		if n > 0 {
			aiTokens.WithLabelValues(provider, task, kind).Add(float64(n))
		}
	}
	if cents > 0 {
		aiCost.WithLabelValues(provider, task).Add(float64(cents))
	}
}

// AIBudgetAlert counts a spend warning sent to a workspace: level 80 or
// 100, for the workspace's own limit (tool false) or a tool's.
func AIBudgetAlert(level int, tool bool) {
	scope := "workspace"
	if tool {
		scope = "tool"
	}
	aiBudgetAlerts.WithLabelValues(strconv.Itoa(level), scope).Inc()
}

// ConnectionLimitAlert counts a usage limit warning sent to a workspace:
// level 80 or 100, for a daily or monthly limit.
func ConnectionLimitAlert(level int, period string) {
	connectionLimitAlerts.WithLabelValues(strconv.Itoa(level), period).Inc()
}

// ConnectionLimitStop counts a paid call refused at a usage limit.
func ConnectionLimitStop(connector, period string) {
	connectionLimitStops.WithLabelValues(connector, period).Inc()
}

// HookDelivery counts one webhook delivery by its outcome.
func HookDelivery(connector, outcome string) { hooksReceived.WithLabelValues(connector, outcome).Inc() }

// HookEvent counts a webhook event a worker finished, and how long after
// it arrived.
func HookEvent(connector, result string, lag time.Duration) {
	hookEvents.WithLabelValues(connector, result).Inc()
	if result != "retry" {
		hookLag.WithLabelValues(connector).Observe(lag.Seconds())
	}
}

// SyncRun records one run of a connection's scheduled sync and its
// outcome: success or the error code.
func SyncRun(module, outcome string) {
	syncRuns.WithLabelValues(module, outcome).Inc()
}

// SocialAccountRead records one read of a social account and its outcome.
func SocialAccountRead(channel, outcome string) {
	socialReads.WithLabelValues(channel, outcome).Inc()
}

// LinkRead records one read of who links to a page or domain.
func LinkRead(reason, outcome string) { linkReads.WithLabelValues(reason, outcome).Inc() }

func linkCounter(name, help, label string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "seo", Name: name, Help: help}, []string{label})
}

// LinkPitchWritten records one opener the AI wrote, or why it didn't.
func LinkPitchWritten(outcome string) { linkPitches.WithLabelValues(outcome).Inc() }

// LinkReplyRead records one reply to a pitch the AI read.
func LinkReplyRead(kind string) { linkReplies.WithLabelValues(kind).Inc() }

// WebSearch records one search on a workspace's SERP connection.
func WebSearch(outcome string) { webSearches.WithLabelValues(outcome).Inc() }

// LinkLiveCheck records one check of an agreed or live link.
func LinkLiveCheck(outcome string) { linkLiveChecks.WithLabelValues(outcome).Inc() }

// LinkPageCheck records one check of a link prospect's page.
func LinkPageCheck(status string) { linkPageChecks.WithLabelValues(status).Inc() }

// ArchiveLookup records one Internet Archive lookup.
func ArchiveLookup(outcome string) { archiveLookups.WithLabelValues(outcome).Inc() }

// ReviewPageRead records one read of a review page: its outcome and how
// many reviews it kept for the first time.
func ReviewPageRead(platform, outcome string, added int) {
	reviewReads.WithLabelValues(platform, outcome).Inc()
	if added > 0 {
		reviewsFound.WithLabelValues(platform).Add(float64(added))
	}
}

// NewsSearchRun records one run of a news search: its outcome, and the
// articles and coverage items it kept for the first time.
func NewsSearchRun(purpose, outcome string, articles, coverage int) {
	newsRuns.WithLabelValues(purpose, outcome).Inc()
	if articles > 0 {
		newsFound.WithLabelValues("article").Add(float64(articles))
	}
	if coverage > 0 {
		newsFound.WithLabelValues("coverage").Add(float64(coverage))
	}
}

// NewsArticleRead records one read of an article's page.
func NewsArticleRead(status string, journalist bool) {
	newsReads.WithLabelValues(status, strconv.FormatBool(journalist)).Inc()
}

// SocialPublished records one try of the publish job.
func SocialPublished(channel, outcome string) {
	socialPublishes.WithLabelValues(channel, outcome).Inc()
}

// DomainConnectLookup records a Domain Connect lookup: available, or the
// reason it isn't, and how long it took.
func DomainConnectLookup(outcome string, d time.Duration) {
	domainConnectLookups.WithLabelValues(outcome).Inc()
	domainConnectDuration.Observe(d.Seconds())
}

// AdConversions counts what one send of a conversion rule did.
func AdConversions(connector string, sent, skipped, failed int) {
	adConversions.WithLabelValues(connector, "sent").Add(float64(sent))
	adConversions.WithLabelValues(connector, "skipped").Add(float64(skipped))
	adConversions.WithLabelValues(connector, "failed").Add(float64(failed))
}

// AdConversionsOldest records how long the oldest waiting conversion has
// waited.
func AdConversionsOldest(d time.Duration) { adConversionsOldest.Set(d.Seconds()) }

// MeetingEventsRecorded counts meeting events recorded with one status.
func MeetingEventsRecorded(status string, n int) {
	meetingEvents.WithLabelValues(status).Add(float64(n))
}

// SetMeetingEventsPending sets how many meetings wait for their event.
func SetMeetingEventsPending(n int) { meetingEventsPending.Set(float64(n)) }

// PartnerClick counts a partner short link click by outcome.
func PartnerClick(outcome string) { partnerClicks.WithLabelValues(outcome).Inc() }

// PartnerConversion counts a reported partner conversion.
func PartnerConversion(typ, source, outcome string) {
	partnerConversions.WithLabelValues(typ, source, outcome).Inc()
}

// PartnerRewards counts n reward ledger changes.
func PartnerRewards(kind, event string, n int) {
	if n > 0 {
		partnerRewards.WithLabelValues(kind, event).Add(float64(n))
	}
}

// PartnerPostback counts a postback to a partner's tracker by outcome.
func PartnerPostback(outcome string) { partnerPostbacks.WithLabelValues(outcome).Inc() }

// PartnerImport counts a finished partner import.
func PartnerImport(connector, outcome string) {
	partnerImports.WithLabelValues(connector, outcome).Inc()
}

// PartnerImportObjects counts n objects an import read.
func PartnerImportObjects(kind, result string, n int) {
	if n > 0 {
		partnerImportObjects.WithLabelValues(kind, result).Add(float64(n))
	}
}

// PartnerRewardsOverdue records how long the oldest reward due to approve
// on its own has waited past its hold.
func PartnerRewardsOverdue(d time.Duration) { partnerRewardsOverdue.Set(d.Seconds()) }
