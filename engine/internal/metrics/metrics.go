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
		Help: "Jobs this process ran, by kind and outcome (succeeded, retried, dead, interrupted by a shutdown and requeued, released: still running when the shutdown grace ended and handed back).",
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
		Help: "Clicks on campaign redirect links (/l/{code}), by outcome (counted, bot for a link scanner or preview, unknown for a code that isn't a link, error when the link couldn't be looked up, dropped when too many clicks waited to be written; the person is sent on either way). Clicks are written in batches a moment later: see click_writes_total.",
	}, []string{"outcome"})
	pageVitals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ingest", Name: "page_vitals_total",
		Help: "Page speed readings from visitors' browsers, by outcome (stored, rejected for their shape or a site key with no brand, pruned after 90 days).",
	}, []string{"outcome"})
	redirectLandings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "redirect", Name: "landings_total",
		Help: "Page views on a site that carried a redirect click's key (ofclk), by outcome (claimed when the visit took the click, pending when its click wasn't written yet and the landing waits for it, direct when a direct-mode visit recorded its own click, unmatched when the click was already claimed, as on a reload, isn't the site's workspace's, or no click took a waiting landing within an hour).",
	}, []string{"outcome"})
	inboxEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "inbox", Name: "events_total",
		Help: "Browser events workers took from the inbox, by outcome (stored, rejected, failed).",
	}, []string{"outcome"})
	inboxBatch = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "inbox", Name: "batch_seconds",
		Help: "Time to store one inbox row (a browser's batch of events).", Buckets: requestBuckets,
	})

	redirectClickWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "redirect", Name: "click_writes_total",
		Help: "Redirect clicks by what happened when their batch was written: written, refused_quota (the workspace's click quota), link_gone, failed (the batch waits for the next flush) or dropped_full (too many waiting).",
	}, []string{"outcome"})
	trackerFraudFlags = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "tracker", Name: "fraud_flags_total",
		Help: "Tracker clicks and conversions a first-party fraud signal flagged, by subject (click or conversion) and reason: datacenter_network (a hosting network), repeat_click (one address and browser clicking a link more than ten times an hour) or fast_conversion (within 10 seconds of its click). A flagged click still goes on but counts against no cap.",
	}, []string{"subject", "reason"})
	trackerWeightSuggestions = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "tracker", Name: "weight_suggestions_seconds",
		Help:    "Time to work out the suggested weights of a link or flow (its clicks and conversions over up to 90 days, then the chance each lander, offer or page is the best), by outcome: success or error.",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"outcome"})
	trackerSourcePostbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "tracker", Name: "source_postbacks_total",
		Help: "Conversions sent back to traffic sources' postback URLs, by outcome: queued, filtered (the source's rules don't send it), sent, retried, or why one failed: rejected (the network refused it), unreachable, or invalid (the URL).",
	}, []string{"outcome"})
	trackerLanderClicks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "tracker", Name: "lander_clicks_total",
		Help: "Clicks on landers' calls to action (/click) by outcome: routed (to an offer or page of the lander's list), fallback (none open, so the link's own page), or why one went to the domain's fallback page: unknown_click, wrong_domain, or error.",
	}, []string{"outcome"})
	trackerPostbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "tracker", Name: "postbacks_total",
		Help: "Tracker conversion postbacks by outcome: accepted, duplicate (ignored), updated (a repeated transaction id updated or added to), or why one was refused: unknown_source, wrong_domain, ip_refused, invalid, unknown_click, expired_click, rate_limited, or error.",
	}, []string{"outcome"})
	trackerConversionRows = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "tracker", Name: "conversion_rows_total",
		Help: "Tracker conversions from pixels and uploads, by how they came (pixel, api, csv) and outcome: accepted, updated, duplicate, failed (a row the upload refused), or for a pixel why it was refused (unknown_source, wrong_domain, ip_refused, rate_limited, invalid, unknown_click, expired_click, error).",
	}, []string{"via", "outcome"})
	redirectRoutes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "redirect", Name: "routes_total",
		Help: "Redirect clicks on links with destinations, by where they went: path (a destination of the first rule-based path that matched), destination (one of the link's default destinations), fallback (every destination was capped or stopped, so the link's own page) or shared_domain (the click came on the shared domain, where tracker features are off, so the link's own page).",
	}, []string{"outcome"})
	redirectClickBuffer = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "redirect", Name: "click_buffer",
		Help: "Redirect clicks waiting in this process to be written. Normal is a few hundred at most; it grows while the database can't take them.",
	})
	redirectClickFlush = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "redirect", Name: "click_flush_seconds",
		Help:    "Time to write one batch of up to 1,000 redirect clicks.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
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
		Help: "Domain Connect lookups, by outcome: available, or why not (not_configured, no_provider, not_onboarded, no_template, nothing_to_add, lookup_failed, conflict), and by the step a failed one stopped at (discover_dns, settings_http, settings_status, template_http, template_status, conflict_dns, sign; none otherwise).",
	}, []string{"outcome", "step"})
	domainConnectDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "domain_connect", Name: "lookup_seconds",
		Help:    "How long a Domain Connect lookup took, DNS and the provider's API included (cached answers are near zero).",
		Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})
	// Query: read-only SQL run for people and agents.
	// Calls Outflare makes to MCP servers (engine/internal/mcpclient).
	mcpClientCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "mcp_client", Name: "calls_total",
		Help: "Calls Outflare made to MCP servers, by operation (connect, list_tools, call_tool) and outcome (success, error).",
	}, []string{"op", "outcome"})
	mcpClientDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "mcp_client", Name: "call_duration_seconds",
		Help:    "How long calls to MCP servers took, by operation.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"op"})

	// The action layer (ADR 13): what the gate decided for agent tokens'
	// changes, and how decided changes ended.
	actionsDecided = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "actions", Name: "decided_total",
		Help: "Changes agent tokens made through the action layer, by what the gate did (running, held, refused) and why (grant, no_grant, daily_limit, always_person).",
	}, []string{"decision", "rule"})
	agentRunTurns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "agent_runs", Name: "turns_total",
		Help: "Turns Outflare's agent took in agent runs (playbooks on the action layer), by how they ended: done, failed (the model or the API broke) or paused (the run paused with a reason a person acts on).",
	}, []string{"outcome"})
	actionsFinished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "actions", Name: "finished_total",
		Help: "Agent changes that ended, by outcome: executed, failed (it ran and the API answered an error) or rejected (a person said no).",
	}, []string{"outcome"})
	ruleEvaluations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "rules", Name: "evaluations_total",
		Help: "Rules evaluated for a record, by trigger kind and outcome (fired; or why not: no_match, duplicate, cooldown, late, before_live, too_deep, manual; absence clocks started and stopped).",
	}, []string{"trigger", "outcome"})
	// Lifecycle stages per product (engine/internal/lifecycle).
	lifecycleEvaluations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "lifecycle", Name: "evaluations_total",
		Help: "Lifecycle checks of a record at a level (company, person; any before a level is known), by outcome: moved, or why not (no_move, duplicate, late, stale, before_live, not_approved, off).",
	}, []string{"level", "outcome"})
	lifecycleSweepDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "lifecycle", Name: "sweep_seconds",
		Help: "How long one inactivity sweep took.", Buckets: jobBuckets,
	})
	// Live record metrics (engine/internal/recmetrics).
	recordMetricUpdates = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "record_metrics", Name: "updates_total",
		Help: "Events added to a live metric, by outcome: updated, or why not (backfilled: the backfill counted it; too_old: before the window; no_value: the property isn't a number).",
	}, []string{"outcome"})
	recordMetricBackfills = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "record_metrics", Name: "backfill_seconds",
		Help: "How long one metric's quiet backfill from stored events took, by outcome (ok, error).", Buckets: jobBuckets,
	}, []string{"outcome"})
	recordMetricSweeps = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "record_metrics", Name: "sweep_seconds",
		Help: "How long one sweep that moves metric windows on took.", Buckets: jobBuckets,
	})
	// Marketing email guardrails: sends the frequency cap stopped and
	// quiet hours held.
	emailsGuarded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "emails", Name: "guarded_total",
		Help: "Marketing emails a guardrail stopped or held, by what sent them (journey, broadcast) and outcome (frequency_cap: not sent; quiet_hours: held until morning; quiet_hours_skipped: not sent, on a step that skips in quiet hours).",
	}, []string{"trigger", "outcome"})
	// Journey webhook and Slack steps: calls out of a run, with retries.
	journeyCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "journeys", Name: "calls_total",
		Help: "Calls journey steps made, by step kind (webhook, slack, push_to_sequencer, ai) and outcome (done; skipped, nothing to do; retried, for a try that will be tried again; failed).",
	}, []string{"kind", "outcome"})
	crmContactPushes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "crm", Name: "contacts_pushed_total",
		Help: "People journeys created or updated in a CRM, by connector (hubspot) and outcome (created, updated, error).",
	}, []string{"connector", "outcome"})
	browserTasks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "browser", Name: "tasks_total",
		Help: "Runs of browser tasks (bounded work in a browser on sites with no API) by how they ended: done, failed, or waiting_person (MFA, a CAPTCHA, a passkey or a consent screen asked for a person).",
	}, []string{"status"})
	browserSteps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "browser", Name: "steps_total",
		Help: "Tool calls made by browser tasks, by tool (navigate, read_page, click, type, type_content, fill_secret, report_sign_in, need_person, finish).",
	}, []string{"tool"})
	browserSessions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "browser", Name: "sessions_total",
		Help: "Saved sign-in sessions of brand accounts used by browser tasks, by event: restored (a task started signed in), saved (a run that confirmed the account's identity kept its cookies) or cleared (a run found the wrong identity, a locked account or a refused credential, or the session was too old).",
	}, []string{"event"})
	accountTakeovers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "accounts", Name: "takeovers_total",
		Help: "Steps of human takeovers of brands' external accounts (a person asked for at MFA, a CAPTCHA, a passkey or a consent screen): requested, started, handed_back, cancelled.",
	}, []string{"step"})
	accountAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "accounts", Name: "attempts_total",
		Help: "Managed uses of brands' external accounts (sign-ins, verifications, session uses) recorded by tools, by purpose and outcome: authenticated, or the failure kind (credential_failed, session_expired, mfa_required, verification_pending, wrong_identity, locked, rate_limited, unresolved).",
	}, []string{"purpose", "outcome"})
	contentPublishes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "content", Name: "publishes_total",
		Help: "Posts and pages Outflare published to a brand's own CMS, by connector (wordpress, ghost), kind (create, update) and outcome (applied, unverified when the CMS did not show the post as asked when read back, failed when the CMS refused it, error for a database failure).",
	}, []string{"connector", "kind", "outcome"})
	adChanges = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "changes_total",
		Help: "Changes Outflare made to campaigns on ad platforms (pausing, resuming, setting a daily budget, adding the tracking template to URL suffixes), by connector (google_ads, meta_ads, microsoft_ads, linkedin_ads, tiktok_ads), kind (pause, resume, budget, tracking) and outcome (applied, unverified when the platform did not show the change afterwards, failed, error for a database failure).",
	}, []string{"connector", "kind", "outcome"})
	adSuggestRuns = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "ads", Name: "suggest_run_seconds",
		Help:    "Runs of the paid ads suggestions job for a workspace (judging measured changes, then suggesting), by outcome (success, error), and how long each took.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"outcome"})
	adSuggestions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "suggestions_total",
		Help: "Changes Outflare suggested for ad campaigns, by connector and kind (budget_up, budget_down, pause, resume).",
	}, []string{"connector", "kind"})
	adVerdicts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "suggestion_verdicts_total",
		Help: "Approved suggestions judged after their measuring window, by connector, kind and verdict (worked, did_not_work, not_sure).",
	}, []string{"connector", "kind", "verdict"})
	adTestSteps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "test_steps_total",
		Help: "Steps of tests run inside ad networks (Google Ads experiments, Meta split tests), by connector, step (start, check, end) and outcome (success, failed when the network refused, error for a failure Outflare retries).",
	}, []string{"connector", "step", "outcome"})
	adDraftSteps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "draft_steps_total",
		Help: "Steps of new ads approved in Outflare (making them paused on the platform, turning them on), by connector, step (create, go_live) and outcome (success, failed when the platform refused, error for a failure Outflare retries).",
	}, []string{"connector", "step", "outcome"})
	adAudienceSyncs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "audience_syncs_total",
		Help: "People journeys added to or removed from ad platform audiences, by connector (google_ads, meta_ads) and outcome (added, removed, skipped for consent or nothing to match on, error).",
	}, []string{"connector", "outcome"})
	sequencerPushes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "sequencers", Name: "leads_pushed_total",
		Help: "People journeys added to sequencer campaigns, by connector (smartlead, instantly, emailbison) and outcome (added, already_in, refused by the sequencer, error).",
	}, []string{"connector", "outcome"})
	mailSignInsMoved = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "mailboxes", Name: "sign_ins_moved_total",
		Help: "Mailbox sign-ins from before they were connections, moved into Gmail and Outlook connections at start-up, by kind (admin: an admin connection; sign_in: a mailbox's own sign-in) and outcome (moved, linked to a connection that already existed, skipped with nothing to move, failed).",
	}, []string{"kind", "outcome"})
	journeyCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "journeys", Name: "call_duration_seconds",
		Help: "Time one try of a journey step's call took, by step kind.", Buckets: requestBuckets,
	}, []string{"kind"})
	journeyEntriesRefused = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "journeys", Name: "entries_refused_total",
		Help: "People a journey's trigger would have started but its limits turned away, by reason (limit: its entry limit per hour or day; reentry: too soon after their last run).",
	}, []string{"reason"})
	// Exclusions: people a journey, broadcast or sequence left out.
	excluded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "automations", Name: "excluded_total",
		Help: "People an automation's exclusion left out, by where (journey: refused entry; broadcast: skipped; sequence: not enrolled).",
	}, []string{"where"})
	transactionsSaved = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "transactions", Name: "saved_total",
		Help: "Transactions sent to Outflare, by where from (api, connector) and outcome (created; duplicate: the same source id again; deleted: the same source id as one in Recently deleted, not added back; invalid; error).",
	}, []string{"from", "outcome"})
	txRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "db", Name: "tx_retries_total",
		Help: "Database transactions a request ran again because Postgres chose it as a deadlock victim or couldn't serialize it, by operation and outcome (retried: tried again; gave_up: failed on its last try, so the request answered an error).",
	}, []string{"operation", "outcome"})
	transactionSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "transactions", Name: "save_seconds",
		Help: "How long saving one transaction took, its conversion to the brand's currency included.", Buckets: requestBuckets,
	})
	insightRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "insights", Name: "runs_total",
		Help: "Insight queries run, by kind (funnel, funnel_people, trend, retention, shared for a shared dashboard's tile, alert_check for an alert on a number, digest for a dashboard in the digest) and outcome (success; invalid: a spec the caller can fix; timeout: stopped at the time limit; error).",
	}, []string{"kind", "outcome"})
	insightSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "insights", Name: "run_seconds",
		Help: "How long one insight query took, by kind.", Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60},
	}, []string{"kind"})
	handEditsExpired = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "hand_edits", Name: "expired_total",
		Help: "Hand edits whose time to win ran out, by outcome (switched: another source's value took over; kept: no other source had a value, so the next one to write replaces it; error).",
	}, []string{"outcome"})
	queryEndpointCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query_endpoints", Name: "calls_total",
		Help: "Calls to published query endpoints, by outcome (success, failure: refused, error) and error code (empty on success).",
	}, []string{"outcome", "code"})
	queryEndpointSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "query_endpoints", Name: "duration_seconds",
		Help:    "How long a call to a published query endpoint took, from its check to its answer.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	})
	queryEndpointRows = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query_endpoints", Name: "rows_total",
		Help: "Rows published query endpoints returned.",
	})
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
	queryTypeTablesFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query", Name: "type_tables_failed_total",
		Help: "Brand queries that ran without their record type tables, because the database refused to make them.",
	})
	querySavedRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query", Name: "saved_runs_total",
		Help: "Saved queries run (Query Studio, run_saved_query), by outcome (success, invalid, timeout, unavailable, error).",
	}, []string{"outcome"})
	queryExports = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query", Name: "exports_total",
		Help: "Query results downloaded as CSV, by outcome (success, invalid, timeout, unavailable, error).",
	}, []string{"outcome"})
	queryExportRows = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "query", Name: "export_rows_total",
		Help: "Rows written to CSV downloads of query results.",
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
	knowledgeSuggestions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "suggestions_total",
		Help: "Knowledge review queue: suggestions by source (support_reply, website, slack, agent, person, answer) and outcome (created, not_reusable, no_ai, error, approved, rejected).",
	}, []string{"source", "outcome"})
	supportMessages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "support", Name: "messages_total",
		Help: "Support inbox messages, by kind (in: from a customer, out: a reply queued, note) and outcome.",
	}, []string{"kind", "outcome"})
	supportReplies = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "support", Name: "replies_total",
		Help: "Support replies the send job finished, by outcome (sent, failed, retry).",
	}, []string{"outcome"})
	supportReplyDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "support", Name: "reply_seconds",
		Help: "Time to send one support reply through its mailbox.", Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 20, 30, 60},
	})
	supportAssistantRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "support", Name: "assistant_runs_total",
		Help: "Runs of the support assistant, by channel (email, chat) and action (answer, clarify, handoff, skipped, failed).",
	}, []string{"channel", "action"})
	supportAssistantDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "support", Name: "assistant_seconds",
		Help: "Time for the support assistant to decide on one customer message, model calls included.", Buckets: []float64{.1, .5, 1, 2.5, 5, 10, 20, 30, 60, 120},
	})
	knowledgeAnswerSearches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "answer_searches_total",
		Help: "Searches for an answer the knowledge didn't have, by source (website, slack, none) and outcome (found, none, nothing_to_read, error, no_ai, off).",
	}, []string{"source", "outcome"})
	knowledgeAnswerSearchDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "answer_search_seconds",
		Help: "Time to search the website and Slack for one missing answer.", Buckets: []float64{.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 180},
	})
	knowledgeWebsiteImports = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "website_imports_total",
		Help: "Website imports into knowledge that finished, by outcome (done, failed).",
	}, []string{"outcome"})
	knowledgeWebsiteImportDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "website_import_seconds",
		Help: "Time to read a website and keep its pages as knowledge.", Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1200},
	})
	knowledgeWebsitePages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "website_pages_total",
		Help: "Website pages an import handled, by result (added, updated, unchanged, removed).",
	}, []string{"result"})
	knowledgeWebsiteRechecks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "website_rechecks_total",
		Help: "Daily website re-checks the scheduler queued, by outcome (queued, error).",
	}, []string{"outcome"})
	knowledgeReplyLearning = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "reply_learning_total",
		Help: "Runs that learned answers from past support replies, by outcome (done, failed).",
	}, []string{"outcome"})
	knowledgeReplyLearningDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "reply_learning_seconds",
		Help: "Time to read past support replies and file what they teach.", Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1200},
	})
	supportAssistantHeld = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "support", Name: "assistant_held_total",
		Help: "Held Auto email replies when their hold ended or someone acted, by result (sent, stopped, cancelled, refused, auto_off).",
	}, []string{"result"})
	supportChatRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "support", Name: "chat_requests_total",
		Help: "Requests from website chat widgets, by route (loader, frame, session, poll, send, articles, feedback) and outcome (ok, not_found, limited, bad_token, invalid, error).",
	}, []string{"route", "outcome"})
	supportSLABreaches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "support", Name: "sla_breaches_total",
		Help: "Support SLA targets missed, by target (reply: the team's next reply, resolve: resolving the conversation).",
	}, []string{"target"})
	supportOverdue = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "support", Name: "overdue",
		Help: "Conversations not closed that are past an SLA target, by target (reply, resolve), across workspaces.",
	}, []string{"target"})
	supportSLAChecks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "support", Name: "sla_checks_total",
		Help: "Runs of the SLA check that marks missed targets, by outcome (success, error).",
	}, []string{"outcome"})
	supportSLACheckDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "support", Name: "sla_check_seconds",
		Help: "Time one SLA check takes.", Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})
	knowledgeAskDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "ask_seconds",
		Help: "Time to answer a question from knowledge: finding passages and the AI model's answer.", Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 20, 30, 60},
	})
	helpPages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "help_centre", Name: "requests_total",
		Help: "Requests to public help centres, by page (home, collection, article, search, feedback, markdown, sitemap, llms) and outcome (ok, not_found, limited, count_failed, error).",
	}, []string{"page", "outcome"})
	helpRender = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "help_centre", Name: "render_seconds",
		Help: "Time to build a help centre page, its database reads included.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	})
	semanticAvailable = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "semantic_search", Name: "available",
		Help: "1 when the database has pgvector, so meaning-based search can run; 0 when it hasn't.",
	})
	semanticWorkspaces = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "semantic_search", Name: "workspaces",
		Help: "Workspaces with meaning-based search turned on.",
	})

	// The data network: lookups the email finder makes, and what joined
	// workspaces share.
	dataNetworkLookups = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "data_network", Name: "lookups_total",
		Help: "Data network lookups by outcome (answered, unknown, refused_allowance, refused_limit, error).",
	}, []string{"outcome"})
	dataNetworkShared = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "data_network", Name: "shared_total",
		Help: "Addresses shared with the data network, by outcome (delivered, bounced).",
	}, []string{"outcome"})
	dataNetworkShareRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "data_network", Name: "share_runs_total",
		Help: "Sharing jobs run, by outcome (success, failed).",
	}, []string{"outcome"})
	dataNetworkMembers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "data_network", Name: "members",
		Help: "Workspaces that have joined the data network.",
	})
	dataNetworkWithdrawals = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "data_network", Name: "withdrawals_pending",
		Help: "Workspaces that left whose shared data is still to be withdrawn.",
	})
	dataNetworkOverdue = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "data_network", Name: "withdrawal_overdue_seconds",
		Help: "How far past its 30-day deadline the most overdue withdrawal is; 0 when none is late.",
	})
	dataNetworkLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "data_network", Name: "share_lag_seconds",
		Help: "How far behind sharing is for the furthest-behind joined workspace, beyond the 7 days a send waits to settle.",
	})

	// Conversions sent back to ad platforms. connector is a catalog id.
	adConversions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ad_conversions", Name: "total",
		Help: "Conversions sent back to ad platforms, by connector and outcome (sent, skipped, failed).",
	}, []string{"connector", "outcome"})
	adConversionAdjustments = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ad_conversion_adjustments", Name: "total",
		Help: "Changes to conversions already sent, after a refund (restated or retracted), by connector and outcome (sent, skipped, failed).",
	}, []string{"connector", "outcome"})
	adConversionsOldest = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "ad_conversions", Name: "oldest_queued_seconds",
		Help: "How long the oldest conversion waiting to be sent has waited, as of this process's last round.",
	})
	// The conversion feed (engine/internal/adconv, feed.go): conversions
	// sent to Google by transport (data_manager, the Data Manager API;
	// ads_api, the older upload call), the batches each took, and the
	// payers lists' size. transport also takes payers_list for the list's
	// refresh batches.
	adsFeedSends = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads_feed", Name: "sends_total",
		Help: "Conversions sent through the conversion feed, by transport (data_manager, ads_api) and outcome (sent, failed).",
	}, []string{"transport", "outcome"})
	adsFeedBatches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads_feed", Name: "batches_total",
		Help: "Batches the conversion feed sent, by transport (data_manager, ads_api, payers_list) and outcome (sent, failed, refused: the destination cannot be used, so sends fall back to ads_api).",
	}, []string{"transport", "outcome"})
	adsPayersListMembers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "ads", Name: "payers_list_members",
		Help: "People in every account's Outflare paying customers list, added together, as of this process's last round.",
	})

	// The paid ads ledger (engine/internal/paidads): sign-ups, payments and
	// refunds tied to the ad click that brought them.
	paidAdsLedgerEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "paidads", Name: "ledger_events_total",
		Help: "Customer events the paid ads ledger looked at, by kind (signup, payment, refund) and outcome (acquired, recorded, known, no_click, not_ads, no_payment, error).",
	}, []string{"kind", "outcome"})
	paidAdsLedgerLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "paidads", Name: "ledger_lag_seconds",
		Help: "How far behind the paid ads ledger is for the furthest-behind workspace with an ad account: seconds since the last event it read arrived, as of this process's last round. Normal is under ten minutes (events wait five to settle).",
	})
	paidAdsAcquisitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "paidads", Name: "acquisitions_total",
		Help: "People a brand's ads acquired, by connector and kind (signup, first_payment).",
	}, []string{"connector", "kind"})

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
		Help: "Review page reads, by platform and outcome (success, pending, blocked, slow_page, page_failed, page_not_found, not_a_review_page, save_failed, connector_error).",
	}, []string{"platform", "outcome"})
	// Prices: reading the brand's and its competitors' pricing pages
	// through the workspace's own page reader, once a week.
	pricingReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "pricing", Name: "page_reads_total",
		Help: "Pricing page reads, by outcome (success, blocked, slow_page, page_failed, page_not_found, no_prices_found, save_failed, connector_error).",
	}, []string{"outcome"})
	// Scheduled syncs of connections (analytics, Search Console, page
	// conversions, ads, SEO snapshots).
	syncRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "sync", Name: "runs_total",
		Help: "Scheduled sync runs of connections, by module (analytics, search_console, page_conversions, organic_conversions, ads, seo) and outcome (success, internal, connector_auth, connector_quota, connector_unavailable, connector_invalid).",
	}, []string{"module", "outcome"})
	// Ad groups' and ads' days stored by the ad sync (Meta and Google).
	adItemRows = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "item_days_synced_total",
		Help: "Days of numbers stored below campaigns by the ad sync, by level: ad_group (Meta ad sets, Google ad groups), ad, keyword and search_term (Google).",
	}, []string{"level"})
	// Social accounts: reading followers of connected accounts and of the
	// public profiles in brand links.
	socialReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "social", Name: "account_reads_total",
		Help: "Social account reads, by channel and outcome (success, blocked, slow_page, page_failed, page_not_found, invalid_address, no_follower_count, save_failed, connector_error).",
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

	// Reddit campaigns: actions sent to Engain and checks of them.
	redditSends = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "reddit", Name: "sends_total",
		Help: "Reddit campaign actions sent to Engain, by kind and outcome (submitted, failed: refused, unknown: no answer, so it may or may not exist).",
	}, []string{"kind", "outcome"})
	redditChecks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "reddit", Name: "checks_total",
		Help: "Checks of sent Reddit campaign actions, by outcome (ok, engain_error, reddit_error, internal) and whether Reddit was read.",
	}, []string{"outcome", "reddit_read"})

	// Link reads: who links to a page or domain, from the workspace's
	// own SEO connection.
	linkReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "seo", Name: "link_reads_total",
		Help: "Reads of who links to a page or domain, by reason (requested, dead_page, competitor_presence) and outcome (success, budget_reached, connector_auth, invalid_request, provider_error, internal).",
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
		Help: "Objects partner imports read, by kind (groups, links, partners, flows, promo_codes, customers, transactions, commissions) and result (created, updated, unchanged, skipped).",
	}, []string{"kind", "result"})
	partnerRewardsOverdue = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "partners", Name: "rewards_overdue_seconds",
		Help: "How long past its hold the oldest reward that should approve on its own has waited, as of this process's last round.",
	})

	// News watch: saved news searches run through the workspace's own
	// web search connection.
	coverageReviews   = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "coverage", Name: "reviews_total", Help: "Coverage AI reviews by bounded result status."}, []string{"status"})
	mailPrivacyChecks = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "crm_mail", Name: "privacy_checks_total", Help: "Emails the workspace's AI model checked before team email sharing shows them, by outcome: work (shown to colleagues), private (kept to its owner), failed (the model call or its answer failed; the email stays private), no_ai (no AI connection) and budget (the AI limit was reached)."}, []string{"outcome"})
	eventNameChecks   = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "customer_events", Name: "name_checks_total", Help: "New event names checked for typos of names the brand already has, by how (ai: the workspace's own AI model; spelling: compared letter by letter, with no AI connected or when the AI call failed) and outcome (ok: nothing to ask; asked: a person is asked whether it is a typo)."}, []string{"method", "outcome"})
	coverageChecks    = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "coverage", Name: "checks_total", Help: "Coverage page checks by observed condition."}, []string{"health"})
	coverageFound     = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "coverage", Name: "found_total", Help: "Pages Presence discovery added to a brand's review list for the first time, by source (news, backlinks, link_prospects, social, ai_citations, web_search)."}, []string{"source"})
	coverageSearches  = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "coverage", Name: "search_runs_total", Help: "Presence web search runs by outcome (done, connector_needed, budget_reached, connector_invalid, connector_auth, connector_quota, connector_unavailable)."}, []string{"outcome"})
	competitorScans   = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "coverage", Name: "competitor_scans_total", Help: "Presence scans of where a competitor appears, by source (web_search, backlinks, ai_citations) and outcome (done, connector_needed, budget_reached, connector_invalid, connector_auth, connector_quota, connector_unavailable, provider_error, internal)."}, []string{"source", "outcome"})
	competitorFound   = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "coverage", Name: "competitor_found_total", Help: "Pages where a competitor appears that a Presence scan found for the first time, by arrangement (likely_paid, unknown)."}, []string{"arrangement"})
	competitorSuggest = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "seo", Name: "competitor_suggestions_total", Help: "Daily looks for new competitors of an SEO site, by source (a connector id, or rankings) and outcome: ok and error count looks, suggested counts the competitors they suggested."}, []string{"source", "outcome"})
	opportunities     = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "coverage", Name: "opportunities_total", Help: "Presence opportunities, by event: suggested (new) and won (the brand is there now, or the link is back)."}, []string{"event"})
	newsRuns          = prometheus.NewCounterVec(prometheus.CounterOpts{
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
	storyAIRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "news", Name: "story_ai_runs_total",
		Help: "Runs of the workspace's own AI on stories, by kind (ideas, draft, pitch: a pitch opener, reply: reading a journalist's reply) and outcome (success, connector_needed, ai_model_needed, ai_budget_reached, connector_auth, ai_failed, check_failed).",
	}, []string{"kind", "outcome"})

	quoteApprovalEmails = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "stories", Name: "quote_approval_emails_total",
		Help: "Emails asking an expert to approve a quote, as their outreach sequence finished them, by outcome (sent, blocked, failed, bounced).",
	}, []string{"outcome"})
	quoteApprovalPages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "stories", Name: "quote_approval_pages_total",
		Help: "Requests to the public quote approval page, by action (view, decide) and outcome (ok, gone, invalid, limited, error).",
	}, []string{"action", "outcome"})
	quoteApprovalDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "stories", Name: "quote_approval_decisions_total",
		Help: "Experts' decisions on quotes through an approval link, by decision (approved, changes_suggested, declined).",
	}, []string{"decision"})

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
	linkAutoSends = linkCounter("link_pitches_auto_total",
		"Link pitches through sequences that skip approval: sent, or why they wait (unsure, spammy_site, daily_cap, site_in_outreach, opted_out, not_pitchable, sequence_archived).", "outcome")
	linkReplies = linkCounter("link_replies_read_total",
		"Replies to link pitches the workspace's AI read, by what they say (agreed, declined, wants_payment, wants_content, other, no_ai, ai_unavailable, ai_failed).", "kind")
	linkLiveChecks = linkCounter("link_live_checks_total",
		"Checks of agreed and live links, by what they found (live, not_yet, removed, still_live, unreachable).", "outcome")
	webSearches = linkCounter("web_searches_total",
		"Searches on workspaces' own SERP connections (search-results), by outcome (done, budget_reached, connector_invalid, connector_auth, connector_quota, connector_unavailable).", "outcome")
	aiAnswers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai_visibility", Name: "screen_answers_total",
		Help: "Answers read from AI assistants' own screens (ChatGPT, Gemini) through workspaces' own answer readers, by engine and outcome (done, budget_reached, connector_invalid, connector_auth, connector_quota, connector_unavailable).",
	}, []string{"engine", "outcome"})
	aiDemandReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai_visibility", Name: "demand_reads_total",
		Help: "Reads of what people ask AI assistants (real questions, AI search volume) through workspaces' own demand readers, by kind (questions, volume, benchmark) and outcome (done, budget_reached, connector_invalid, connector_auth, connector_quota, connector_unavailable).",
	}, []string{"kind", "outcome"})
	aiStanceReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai_visibility", Name: "stance_reads_total",
		Help: "AI visibility answers read by workspaces' own AI for how they treat the brands they name, by outcome (judged, skipped, ai_failed, refused, ai_model_needed, or the provider's error code).",
	}, []string{"outcome"})
	aiPromptDrafts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ai_visibility", Name: "prompt_drafts_total",
		Help: "Drafts of AI visibility questions by the workspace's own AI, by outcome (success, ai_failed, connector_auth, ai_budget_reached, ai_model_needed, connector_needed).",
	}, []string{"outcome"})

	automationRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "playbooks", Name: "automation_runs_total",
		Help: "Playbook automations firing, by kind (schedule, event) and outcome (started, held, failed, capped).",
	}, []string{"kind", "outcome"})
	automationLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "playbooks", Name: "automation_lag_seconds",
		Help: "How long the most overdue active playbook automation has waited past its run time, at the last pass.",
	})
	inboundReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "inbound_email", Name: "received_total",
		Help: "Messages delivered to POST /v1/inbound/email, by purpose (ai_receipts, support, or none when the address is unknown) and outcome (accepted, duplicate, unknown_address, bad_signature, too_large, error).",
	}, []string{"purpose", "outcome"})
	inboundProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "inbound_email", Name: "processed_total",
		Help: "Inbound messages processed, by purpose and status (processed, ignored, rejected, failed).",
	}, []string{"purpose", "status"})
	inboundBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "inbound_email", Name: "received_bytes_total",
		Help: "Size of the inbound messages accepted.",
	})
	// Product events sent to a brand (POST /v1/brands/{brand}/events): kept
	// in the event inbox, then processed by the events.process job.
	eventInboxReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "received_total",
		Help: "Product events saved to the event inbox, waiting to be processed.",
	})
	eventInboxProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "processed_total",
		Help: "Product events the events.process job finished, by outcome (accepted, duplicate, rejected, held).",
	}, []string{"outcome"})
	eventInboxRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "rejected_total",
		Help: "Product events rejected, by code (missing_id, invalid_timestamp, property_type, internal and the rest of the inbox's codes).",
	}, []string{"code"})
	eventInboxDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "process_seconds",
		Help: "Time to process one product event from the inbox.", Buckets: requestBuckets,
	})
	eventInboxPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "pending",
		Help: "Product events waiting to be processed, as of this process's last round.",
	})
	eventInboxOldest = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "oldest_pending_seconds",
		Help: "How long the oldest product event waiting to be processed has waited, as of this process's last round.",
	})
	eventInboxPurged = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "purged_total",
		Help: "Event inbox rows deleted at the end of their retention (30 days accepted, 90 rejected or held).",
	})
	eventInboxPurgeOverdue = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "event_inbox", Name: "purge_overdue_seconds",
		Help: "How long past its retention the oldest event inbox row still waiting for the sweep is, as of this process's last round.",
	})

	// Privacy exports (POST /v1/brands/{brand}/records/{record}/privacy-exports),
	// built by the privacy_export.build job.
	privacyExportsBuilt = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "privacy_exports", Name: "built_total",
		Help: "Privacy exports the privacy_export.build job finished, by outcome (ready, too_large, record_gone, internal, error).",
	}, []string{"outcome"})
	privacyExportSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "privacy_exports", Name: "build_seconds",
		Help: "Time to build one privacy export.", Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	})
	privacyExportBytes = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "privacy_exports", Name: "size_bytes",
		Help:    "Uncompressed size of the privacy exports built (the limit is 100 MB).",
		Buckets: prometheus.ExponentialBuckets(1024, 4, 10),
	})
	privacyExportsExpired = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "privacy_exports", Name: "expired_total",
		Help: "Privacy exports whose file was deleted 7 days after it was ready.",
	})

	// The 7-day recovery window: deletions of customer data that can be
	// restored, then are purged.
	recoveryDeleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "recovery", Name: "deleted_total",
		Help: "Customer data deleted into the 7-day recovery window, by kind (record, field_value, identity, relationship, event, batch, transaction).",
	}, []string{"kind"})
	recoveryRestored = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "recovery", Name: "restored_total",
		Help: "Deletions restored, by kind and outcome (restored, partly_restored, not_restored).",
	}, []string{"kind", "outcome"})
	recoveryPurged = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "recovery", Name: "purged_total",
		Help: "Deletions purged for good after their 7 days, by kind.",
	}, []string{"kind"})
	recoveryPurgeOverdue = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "recovery", Name: "purge_overdue_seconds",
		Help: "How long past its 7 days the oldest deletion still waiting for the purge is, as of this process's last round.",
	})

	// Undo for bad data: unmerges and batch undos.
	undoUnmerges = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "undo", Name: "unmerges_total",
		Help: "Merges undone (undone), or refused (refused: not reversible, not the latest, already undone), by outcome.",
	}, []string{"outcome"})
	bulkEdits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "bulk_edits", Name: "total",
		Help: "Bulk edits finished, by kind (edit, undo) and outcome (done, failed).",
	}, []string{"kind", "outcome"})
	bulkEditRecords = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "bulk_edits", Name: "records_total",
		Help: "Records bulk edits went through, by kind (edit, undo) and result (changed, unchanged, skipped).",
	}, []string{"kind", "result"})
	bulkEditSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "bulk_edits", Name: "duration_seconds",
		Help:    "How long a bulk edit took from being queued to finishing, by kind (edit, undo).",
		Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600},
	}, []string{"kind"})
	undoBatches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "undo", Name: "batches_total",
		Help: "Import and ingest batches undone, by mode (untouched, everything) and outcome (undone, queued, failed).",
	}, []string{"mode", "outcome"})
	undoRowsReverted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "undo", Name: "rows_reverted_total",
		Help: "Rows moved back, removed or put back by undos, by kind (unmerge, batch).",
	}, []string{"kind"})
	undoBatchSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "undo", Name: "batch_seconds",
		Help:    "How long undoing one batch took.",
		Buckets: []float64{0.05, 0.25, 1, 5, 15, 60, 300},
	})

	inboundPurged = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "inbound_email", Name: "raw_purged_total",
		Help: "Raw inbound messages deleted after 30 days (their details stay).",
	})

	ratesFetched = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "exchange_rates", Name: "fetched_total",
		Help: "Fetches of the European Central Bank's daily exchange rates, by outcome (success, error). Only servers with inbound email fetch them.",
	}, []string{"outcome"})
	ratesAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "exchange_rates", Name: "newest_age_seconds",
		Help: "Age of the newest exchange rate stored, from the start of its day. Set after each fetch.",
	})
	receiptsConverted = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "exchange_rates", Name: "receipts_converted_total",
		Help: "AI receipts charged in another currency given their amount in US dollars once a rate arrived.",
	})

	geoDownloads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "geoip", Name: "downloads_total",
		Help: "Downloads of DB-IP's monthly City Lite and ASN Lite files, by file (City, ASN) and outcome (success, not_published when the month's release isn't out yet, error).",
	}, []string{"file", "outcome"})
	geoAge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "geoip", Name: "file_age_seconds",
		Help: "Age of the DB-IP file in use, from the day it was built, by file (City, ASN). Absent while a server has no such file. Set every 6 hours.",
	}, []string{"file"})
	quotaChecks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "quota", Name: "checks_total",
		Help: "Quota checks an extension answered (package quota), by resource and outcome: allowed, refused, or error (the checker failed and the work was allowed).",
	}, []string{"resource", "outcome"})

	desktopLinks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "desktop", Name: "links_total",
		Help: "Steps of linking computers to Outflare for desktop: asked (the app asked for a link), approved (a person approved its code), linked (the app got its token).",
	}, []string{"step"})
	desktopReports = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "desktop", Name: "reports_total",
		Help: "Reports from Outflare for desktop, by outcome (ok, invalid, error).",
	}, []string{"outcome"})
	desktopReadings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "desktop", Name: "readings_total",
		Help: "Usage windows reported by Outflare for desktop, by provider (claude, chatgpt, cursor, copilot) and source (official, extended).",
	}, []string{"provider", "source"})
	desktopReadFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "desktop", Name: "read_failures_total",
		Help: "Tools Outflare for desktop couldn't read, by provider and the app's error code (not_signed_in, rate_limited, bad_response...).",
	}, []string{"provider", "code"})
	desktopDevices = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "desktop", Name: "devices",
		Help: "Linked computers by state: reporting (a report in the last hour) or silent.",
	}, []string{"state"})
	telegramSteps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "steps_total",
		Help: "Company Telegram account sign-ins and checks, by step (login, password, check, logout) and outcome (connected, needs_password, expired, success, or why it failed: wrong_password, signed_out, bad_app, no_app, unreachable).",
	}, []string{"step", "outcome"})
	telegramSynced = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "synced_messages_total",
		Help: "Messages copied from company Telegram accounts into the CRM, by direction (in, out).",
	}, []string{"direction"})
	telegramSyncRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "sync_runs_total",
		Help: "Syncs of company Telegram accounts, by outcome (live, importing, or why it failed: signed_out, no_app, bad_app, slowed_down, unreachable).",
	}, []string{"outcome"})
	telegramMatches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "matches_total",
		Help: "Synced Telegram chats matched to customer records, by result (confirmed: a record already had its Telegram id; suggested: waits for its holder; no_match).",
	}, []string{"result"})
	telegramSummaries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "summaries_total",
		Help: "AI summaries of synced Telegram chats, by outcome (success, no_ai: the workspace has no AI for it, failed: the AI call failed).",
	}, []string{"outcome"})
	telegramWaiting = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "waiting_chats",
		Help: "Customer chats on company Telegram accounts where the customer wrote last and has waited past the alert time, across workspaces, as of the last count.",
	})
	telegramSyncLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "sync_lag_seconds",
		Help: "How long ago the least recently synced company Telegram account was synced, as of the last count. Normal is a few minutes.",
	})
	telegramAccounts = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "telegram", Name: "accounts",
		Help: "Company Telegram accounts signed in to Outflare, by status (connected, signed_out, failed...), across workspaces, as of the last count.",
	}, []string{"status"})
	desktopPurged = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "desktop", Name: "readings_purged_total",
		Help: "Usage readings deleted after 90 days.",
	})

	connectionsUnassigned = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "connections", Name: "unassigned",
		Help: "Connections (kind connection: a brand connection with no brand) and their accounts or properties (kind account: an analytics site or ad account no brand has) whose data shows in no brand, across every workspace, as of the last count.",
	}, []string{"kind"})

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info",
		Help: "Always 1; the labels are the running engine version and its role (OUTFLARE_ROLE).",
	}, []string{"version", "role"})

	// ---- Paid ads intelligence layer: hourly sync, click harvest, change
	// events and alerts (plans/paid-ads/plan.md; engine/internal/adsync). ----
	adSyncRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "sync_runs_total",
		Help: "Runs of the hourly ad sync per connection, by the window each read (first: 90 days on an account's first sync; hourly: 2 days; daily: 7; weekly: 35; monthly: 95) and outcome (success, internal, connector_auth, connector_quota, connector_unavailable, connector_invalid).",
	}, []string{"window", "outcome"})
	adClicksHarvested = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "clicks_harvested_total",
		Help: "Paid clicks read from a network's click report (Google click_view) into ad_clicks, by connector and outcome: stored (one per click), or failed (one per read a network refused, with the error kind as connector_<kind>).",
	}, []string{"connector", "outcome"})
	adChangeEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "change_events_total",
		Help: "Changes made on ad platforms read into ad_change_events (Google change_event, every 15 minutes), by connector. Normal is a trickle; zero for days on an account people work in means the read is failing (see the job).",
	}, []string{"connector"})
	adChangeEventReads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "change_event_reads_total",
		Help: "Reads of ad platforms' change histories per connection, by connector and outcome (success, internal, connector_auth, connector_quota, connector_unavailable, connector_invalid).",
	}, []string{"connector", "outcome"})
	adAlerts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "alerts_total",
		Help: "Alerts the paid ads alert job raised as new findings, by kind (alert.spend_anomaly, alert.pacing, alert.flatline, alert.zero_conversions, alert.disapproved, alert.auto_apply_on, alert.sends_failing, alert.tracking_broken, overspend_pause). A few a day across all workspaces is normal; dozens an hour is a flood.",
	}, []string{"kind"})
	adAlertRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "alert_runs_total",
		Help: "Runs of the paid ads alert job (once per workspace after each hourly sync), by outcome (success, error).",
	}, []string{"outcome"})

	// ---- Paid ads intelligence layer: findings and verdicts
	// (plans/paid-ads/plan.md; engine/internal/adsuggest). ----
	adFindings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "findings_total",
		Help: "Findings the paid ads rules wrote, by kind (budget_down, pause, negative_add, target_tighten, exclusion_add, ..., alert.target_stale), autonomy (auto, approve, alert, shadow) and outcome (created: a new row for a person; applied: a saving made on its own; recorded: what a rule would have done in shadow mode).",
	}, []string{"kind", "autonomy", "outcome"})
	adFindingVerdicts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ads", Name: "verdicts_total",
		Help: "Verdicts on findings whose change was made, by kind and verdict (worked, did_not_work, not_sure). Mostly worked and not_sure is normal; did_not_work outnumbering worked for a kind means that rule's thresholds want a look.",
	}, []string{"kind", "verdict"})
	adFindingsOpen = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "ads", Name: "findings_open",
		Help: "Findings waiting for a person across every workspace (status open, autonomy approve), as of the last run of the rules. A number that only grows means nobody is deciding them.",
	})
)

// own are the metrics this package keeps itself; Postgres is registered
// separately because it needs the pool.
var own = []prometheus.Collector{
	httpRequests, httpDuration, httpInFlight, httpRateLimited,
	jobsDone, jobDuration, jobWait, jobsActive, jobsReaped, workers,
	siteEvents, pageVitals, inboxEvents, inboxBatch, redirectClicks, redirectLandings, redirectClickWrites, redirectRoutes, trackerPostbacks, trackerLanderClicks, trackerSourcePostbacks, trackerFraudFlags, trackerWeightSuggestions, trackerConversionRows, redirectClickBuffer, redirectClickFlush, paidArrivals, adMacrosUnfilled,
	flagRequests, flagDuration, flagCache, abResults, abResultsDuration,
	consentRequests, consentDuration, consentChoices,
	cookieScans, cookieScanPages, cookieScanFound, cookieScanDuration, cookieReports, cookieNew,
	pipelineItems, pipelineCodes, pipelineBatch, pipelineBatchSize, pipelineWait, pipelineLimitWaits, pipelineLimitReports,
	aiCalls, aiTokens, aiCost, aiBudgetAlerts,
	connectionLimitAlerts, connectionLimitStops,
	hooksReceived, hookEvents, hookLag,
	domainConnectLookups, domainConnectDuration,
	queryEndpointCalls, queryEndpointSeconds, queryEndpointRows, handEditsExpired, transactionsSaved, transactionSeconds, txRetries, insightRuns, insightSeconds,
	queryRuns, queryDuration, queryRows, queryTypeTablesFailed, querySavedRuns, queryExports, queryExportRows,
	mcpClientCalls, mcpClientDuration, ruleEvaluations, recordMetricUpdates, recordMetricBackfills, recordMetricSweeps, lifecycleEvaluations, lifecycleSweepDuration, emailsGuarded, journeyCalls, journeyCallDuration, journeyEntriesRefused, excluded, sequencerPushes, crmContactPushes, adAudienceSyncs, adChanges, adSuggestRuns, adSuggestions, adVerdicts, adTestSteps, adDraftSteps, contentPublishes, accountAttempts, accountTakeovers, browserTasks, browserSteps, browserSessions, mailSignInsMoved,
	actionsDecided, actionsFinished, agentRunTurns,
	semanticRuns, semanticItems, semanticDuration, semanticAvailable, semanticWorkspaces, knowledgeAsks, knowledgeAskDuration,
	dataNetworkLookups, dataNetworkShared, dataNetworkShareRuns, dataNetworkMembers, dataNetworkWithdrawals, dataNetworkOverdue, dataNetworkLag,
	helpPages, helpRender,
	knowledgeSuggestions,
	supportMessages, supportReplies, supportReplyDuration, supportAssistantRuns, supportAssistantDuration, knowledgeAnswerSearches, knowledgeAnswerSearchDuration, knowledgeWebsiteImports, knowledgeWebsiteImportDuration, knowledgeWebsitePages, knowledgeWebsiteRechecks, knowledgeReplyLearning, knowledgeReplyLearningDuration, supportAssistantHeld, supportSLABreaches, supportOverdue, supportChatRequests, supportSLAChecks, supportSLACheckDuration,
	adConversions, adConversionsOldest, adConversionAdjustments,
	paidAdsLedgerEvents, paidAdsLedgerLag, paidAdsAcquisitions,
	adsFeedSends, adsFeedBatches, adsPayersListMembers,
	meetingEvents, meetingEventsPending,
	reviewReads, reviewsFound, pricingReads, socialReads, syncRuns, adItemRows, linkReads, linkPageChecks, archiveLookups, linkPitches, linkAutoSends, linkReplies, linkLiveChecks, webSearches, aiAnswers, aiDemandReads, aiStanceReads, aiPromptDrafts,
	socialPublishes,
	redditSends,
	redditChecks,
	partnerClicks, partnerConversions, partnerRewards, partnerRewardsOverdue, partnerImports, partnerImportObjects, partnerPostbacks,
	mailPrivacyChecks, eventNameChecks,
	coverageChecks, coverageReviews, coverageFound, coverageSearches, competitorScans, competitorFound, competitorSuggest, opportunities, newsRuns, newsFound, newsReads, storyAIRuns,
	quoteApprovalEmails, quoteApprovalPages, quoteApprovalDecisions,
	automationRuns, automationLag,
	connectionsUnassigned,
	inboundReceived, inboundProcessed, inboundBytes, inboundPurged,
	eventInboxReceived, eventInboxProcessed, eventInboxRejected, eventInboxDuration, eventInboxPending, eventInboxOldest, eventInboxPurged, eventInboxPurgeOverdue,
	privacyExportsBuilt, privacyExportSeconds, privacyExportBytes, privacyExportsExpired,
	recoveryDeleted, recoveryRestored, recoveryPurged, recoveryPurgeOverdue,
	undoUnmerges, undoBatches, undoRowsReverted, undoBatchSeconds,
	bulkEdits, bulkEditRecords, bulkEditSeconds,
	ratesFetched, ratesAge, receiptsConverted,
	geoDownloads, geoAge,
	quotaChecks,
	desktopLinks, desktopReports, desktopReadings, desktopReadFailures, desktopDevices, desktopPurged,
	telegramSteps, telegramAccounts, telegramSynced, telegramSyncRuns, telegramSyncLag, telegramMatches, telegramSummaries, telegramWaiting,
	buildInfo,
	adSyncRuns, adClicksHarvested, adChangeEvents, adChangeEventReads, adAlerts, adAlertRuns,
	// Paid ads: the brand knowledge sheet.
	brandKnowledgeReads,
	// Paid ads: findings and verdicts.
	adFindings, adFindingVerdicts, adFindingsOpen,
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

// JobReleased counts a job of this kind that was still running when the
// shutdown grace ended and was handed back to the queue.
func JobReleased(kind string) { jobsDone.WithLabelValues(kind, JobReleasedOutcome).Inc() }

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
	// JobInterrupted is a handler that stopped for a shutdown; its job is
	// requeued without using up an attempt.
	JobInterrupted = "interrupted"
	// JobReleasedOutcome is a job still running when the shutdown grace
	// ended, handed back to the queue.
	JobReleasedOutcome = "released"
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

// RedirectClickWrites counts buffered redirect clicks by what happened when
// their batch was written: written, refused_quota, link_gone, failed (the
// batch goes back to wait) or dropped_full.
func RedirectClickWrites(outcome string, n int) {
	if n > 0 {
		redirectClickWrites.WithLabelValues(outcome).Add(float64(n))
	}
}

// TrackerConversionRow counts a tracker conversion from a pixel or an
// upload.
func TrackerConversionRow(via, outcome string) {
	trackerConversionRows.WithLabelValues(via, outcome).Inc()
}

// TrackerFraudFlag counts a tracker click or conversion a fraud signal
// flagged, by reason.
func TrackerFraudFlag(subject, reason string) {
	trackerFraudFlags.WithLabelValues(subject, reason).Inc()
}

// TrackerSourcePostback counts a conversion sent back to a traffic source
// by outcome.
func TrackerSourcePostback(outcome string) { trackerSourcePostbacks.WithLabelValues(outcome).Inc() }

// TrackerLanderClick counts a click on a lander's call to action by
// outcome.
func TrackerLanderClick(outcome string) { trackerLanderClicks.WithLabelValues(outcome).Inc() }

// TrackerPostback counts a tracker conversion postback by outcome.
func TrackerPostback(outcome string) { trackerPostbacks.WithLabelValues(outcome).Inc() }

// RedirectRoute counts a click on a link with destinations by where it
// went: path, destination, fallback or shared_domain.
func RedirectRoute(outcome string) { redirectRoutes.WithLabelValues(outcome).Inc() }

// RedirectClickBuffer reports how many clicks wait to be written.
func RedirectClickBuffer(n int) { redirectClickBuffer.Set(float64(n)) }

// TrackerWeightSuggestions times working out one link's or flow's
// suggested weights, by outcome (success or error).
func TrackerWeightSuggestions(outcome string, d time.Duration) {
	trackerWeightSuggestions.WithLabelValues(outcome).Observe(d.Seconds())
}

// RedirectClickFlush times writing one batch of clicks.
func RedirectClickFlush(d time.Duration) { redirectClickFlush.Observe(d.Seconds()) }

// RedirectLanding counts one page view carrying a redirect click's key, by
// outcome: claimed, pending (its click not written yet), direct (a
// direct-mode visit recorded its own click) or unmatched.
func RedirectLanding(outcome string) { redirectLandings.WithLabelValues(outcome).Inc() }

// PageVitals counts page speed readings by outcome: stored, rejected or
// pruned.
func PageVitals(outcome string, n int) { pageVitals.WithLabelValues(outcome).Add(float64(n)) }

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

// LifecycleEvaluated counts one lifecycle check of a record at a level
// (company, person, or any), by outcome.
func LifecycleEvaluated(level, outcome string) {
	lifecycleEvaluations.WithLabelValues(level, outcome).Inc()
}

// RecordMetricUpdated counts one event offered to a live metric, by
// outcome.
func RecordMetricUpdated(outcome string) { recordMetricUpdates.WithLabelValues(outcome).Inc() }

// RecordMetricBackfill observes one metric backfill.
func RecordMetricBackfill(d time.Duration, ok bool) {
	outcome := "ok"
	if !ok {
		outcome = "error"
	}
	recordMetricBackfills.WithLabelValues(outcome).Observe(d.Seconds())
}

// RecordMetricSweep observes one metric window sweep.
func RecordMetricSweep(d time.Duration) { recordMetricSweeps.Observe(d.Seconds()) }

// LifecycleSweepDuration observes one inactivity sweep.
func LifecycleSweepDuration(d time.Duration) { lifecycleSweepDuration.Observe(d.Seconds()) }

// MCPClientCalled counts a call Outflare made to an MCP server.
func MCPClientCalled(op, outcome string, d time.Duration) {
	mcpClientCalls.WithLabelValues(op, outcome).Inc()
	mcpClientDuration.WithLabelValues(op).Observe(d.Seconds())
}

// AgentRunTurn counts a turn of Outflare's agent in an agent run, by how
// it ended: done, failed, or paused (the run paused with a reason).
func AgentRunTurn(outcome string) { agentRunTurns.WithLabelValues(outcome).Inc() }

// ActionDecided counts what the action layer's gate did with a change.
func ActionDecided(decision, rule string) { actionsDecided.WithLabelValues(decision, rule).Inc() }

// ActionFinished counts how a decided change ended.
func ActionFinished(outcome string) { actionsFinished.WithLabelValues(outcome).Inc() }

// EmailGuarded records a marketing email a guardrail stopped or held: what
// sent it (journey, broadcast) and the outcome (frequency_cap,
// quiet_hours).
func EmailGuarded(trigger, outcome string) {
	if trigger != "journey" && trigger != "broadcast" {
		trigger = "other"
	}
	emailsGuarded.WithLabelValues(trigger, outcome).Inc()
}

// JourneyEntryRefused records an automatic entry a journey's limits
// turned away: limit or reentry.
func JourneyEntryRefused(reason string) { journeyEntriesRefused.WithLabelValues(reason).Inc() }

// JourneyCalled records one try of a journey step's call: its kind
// (webhook, slack), outcome (done, retried, failed) and time.
func JourneyCalled(kind, outcome string, d time.Duration) {
	journeyCalls.WithLabelValues(kind, outcome).Inc()
	journeyCallDuration.WithLabelValues(kind).Observe(d.Seconds())
}

// CRMContactPushed records one try at creating or updating a person in a
// CRM: the connector, and what happened (created, updated, error).
func CRMContactPushed(connector, outcome string) {
	crmContactPushes.WithLabelValues(connector, outcome).Inc()
}

// AccountAttempt records one managed use of a brand's external account and
// its outcome.
func AccountAttempt(purpose, outcome string) {
	accountAttempts.WithLabelValues(purpose, outcome).Inc()
}

// BrowserTask records how a browser task's run ended: done, failed or
// waiting_person.
func BrowserTask(status string) {
	browserTasks.WithLabelValues(status).Inc()
}

// BrowserSession records a saved session of a brand account being
// restored, saved or cleared by a browser task.
func BrowserSession(event string) {
	browserSessions.WithLabelValues(event).Inc()
}

// BrowserStep records one tool call of a browser task, by tool.
func BrowserStep(tool string) {
	browserSteps.WithLabelValues(tool).Inc()
}

// AccountTakeover records a step of a human takeover of a brand's
// external account: requested, started, handed_back or cancelled.
func AccountTakeover(step string) {
	accountTakeovers.WithLabelValues(step).Inc()
}

// ContentPublished records one post or page published to a CMS and how it
// ended (applied, unverified, failed, error).
func ContentPublished(connector, kind, outcome string) {
	contentPublishes.WithLabelValues(connector, kind, outcome).Inc()
}

// AdSuggestRun records one run of the suggestions job.
func AdSuggestRun(outcome string, took time.Duration) {
	adSuggestRuns.WithLabelValues(outcome).Observe(took.Seconds())
}

// AdSuggested records one new suggestion.
func AdSuggested(connector, kind string) {
	adSuggestions.WithLabelValues(connector, kind).Inc()
}

// AdSuggestionJudged records one verdict on an approved suggestion.
func AdSuggestionJudged(connector, kind, verdict string) {
	adVerdicts.WithLabelValues(connector, kind, verdict).Inc()
}

// AdTestStep records one step of a test inside an ad network.
func AdTestStep(connector, step, outcome string) {
	adTestSteps.WithLabelValues(connector, step, outcome).Inc()
}

// AdDraftStep records one step of a new ad: made paused on the platform
// (create) or turned on (go_live), and how it ended.
func AdDraftStep(connector, step, outcome string) {
	adDraftSteps.WithLabelValues(connector, step, outcome).Inc()
}

// AdChanged records one change to a campaign on an ad platform and how it
// ended (applied, unverified, failed, error).
func AdChanged(connector, kind, outcome string) {
	adChanges.WithLabelValues(connector, kind, outcome).Inc()
}

// AdAudienceSynced records one try at adding a person to an ad platform
// audience or removing them: the connector, and what happened (added,
// removed, skipped, error).
func AdAudienceSynced(connector, outcome string) {
	adAudienceSyncs.WithLabelValues(connector, outcome).Inc()
}

// SequencerPushed records one try at adding a person to a sequencer
// campaign: the connector, and what happened (added, already_in, refused,
// error).
func SequencerPushed(connector, outcome string) {
	sequencerPushes.WithLabelValues(connector, outcome).Inc()
}

// MailSignInMoved records one older mailbox sign-in moved into a
// connection: kind (admin, sign_in) and outcome (moved, linked, skipped,
// failed).
func MailSignInMoved(kind, outcome string) { mailSignInsMoved.WithLabelValues(kind, outcome).Inc() }

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

// TransactionSaved counts one transaction sent to Outflare, by where it
// came from (api, connector) and outcome, with how long saving it took.
func TransactionSaved(from, outcome string, d time.Duration) {
	transactionsSaved.WithLabelValues(from, outcome).Inc()
	transactionSeconds.Observe(d.Seconds())
}

// TxRetry counts one deadlock or serialization failure a request's
// transaction hit: retried when it runs again, gave_up when it was the
// last try.
func TxRetry(operation, outcome string) {
	txRetries.WithLabelValues(operation, outcome).Inc()
}

// InsightRun counts one insight query, by kind (funnel, funnel_people,
// trend, retention, shared, alert_check, digest) and outcome, with how long
// it took.
func InsightRun(kind, outcome string, d time.Duration) {
	insightRuns.WithLabelValues(kind, outcome).Inc()
	insightSeconds.WithLabelValues(kind).Observe(d.Seconds())
}

// HandEditExpired counts a hand edit whose time ran out, by outcome
// (switched, kept or error).
func HandEditExpired(outcome string) { handEditsExpired.WithLabelValues(outcome).Inc() }

// QueryEndpointCalled counts a call to a published query endpoint, by
// outcome and error code, with how long it took and the rows it returned.
func QueryEndpointCalled(outcome, code string, d time.Duration, rows int) {
	queryEndpointCalls.WithLabelValues(outcome, code).Inc()
	queryEndpointSeconds.Observe(d.Seconds())
	queryEndpointRows.Add(float64(rows))
}

// QueryRan records one query: its outcome, time and rows returned.
func QueryRan(outcome string, d time.Duration, rows int) {
	queryRuns.WithLabelValues(outcome).Inc()
	queryDuration.Observe(d.Seconds())
	queryRows.Add(float64(rows))
}

// QueryTypeTablesFailed records a brand query that ran without its record
// type tables.
func QueryTypeTablesFailed() { queryTypeTablesFailed.Inc() }

// QuerySavedRan records one run of a saved query, by outcome.
func QuerySavedRan(outcome string) { querySavedRuns.WithLabelValues(outcome).Inc() }

// QueryExported records one CSV download of a query's results: its
// outcome and the rows written.
func QueryExported(outcome string, rows int) {
	queryExports.WithLabelValues(outcome).Inc()
	queryExportRows.Add(float64(rows))
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

// HelpCentrePage records one request to a public help centre.
func HelpCentrePage(page, outcome string) {
	helpPages.WithLabelValues(page, outcome).Inc()
}

// HelpCentreRender records how long a help centre page took to build.
func HelpCentreRender(d time.Duration) {
	helpRender.Observe(d.Seconds())
}

// KnowledgeSuggestion records what happened to one review queue
// suggestion, by where it came from.
func KnowledgeSuggestion(source, outcome string) {
	knowledgeSuggestions.WithLabelValues(source, outcome).Inc()
}

// SupportMessage records one support inbox message: kind in, out or note,
// and its outcome.
func SupportMessage(kind, outcome string) { supportMessages.WithLabelValues(kind, outcome).Inc() }

// SupportChatRequest records one request from a website chat widget.
func SupportChatRequest(route, outcome string) {
	supportChatRequests.WithLabelValues(route, outcome).Inc()
}

// SupportSLABreached records one SLA target missed: reply or resolve.
func SupportSLABreached(target string) { supportSLABreaches.WithLabelValues(target).Inc() }

// SupportOverdue sets how many open conversations are past a target.
func SupportOverdue(target string, n int) { supportOverdue.WithLabelValues(target).Set(float64(n)) }

// SupportSLACheck records one run of the SLA check.
func SupportSLACheck(ok bool, d time.Duration) {
	outcome := "success"
	if !ok {
		outcome = "error"
	}
	supportSLAChecks.WithLabelValues(outcome).Inc()
	supportSLACheckDuration.Observe(d.Seconds())
}

// SupportAssistant records one run of the support assistant.
func SupportAssistant(channel, action string, d time.Duration) {
	supportAssistantRuns.WithLabelValues(channel, action).Inc()
	supportAssistantDuration.Observe(d.Seconds())
}

// KnowledgeAnswerSearch records one search of a source for a missing
// answer; d is zero when nothing was searched.
func KnowledgeAnswerSearch(source, outcome string, d time.Duration) {
	knowledgeAnswerSearches.WithLabelValues(source, outcome).Inc()
	if d > 0 {
		knowledgeAnswerSearchDuration.Observe(d.Seconds())
	}
}

// KnowledgeWebsiteImport records a website import that finished.
func KnowledgeWebsiteImport(outcome string, d time.Duration) {
	knowledgeWebsiteImports.WithLabelValues(outcome).Inc()
	knowledgeWebsiteImportDuration.Observe(d.Seconds())
}

// KnowledgeWebsiteRecheck records a daily website re-check the scheduler
// queued, or failed to.
func KnowledgeWebsiteRecheck(outcome string) { knowledgeWebsiteRechecks.WithLabelValues(outcome).Inc() }

// KnowledgeReplyLearning records a run that learned from past replies.
func KnowledgeReplyLearning(outcome string, d time.Duration) {
	knowledgeReplyLearning.WithLabelValues(outcome).Inc()
	knowledgeReplyLearningDuration.Observe(d.Seconds())
}

// SupportAssistantHeld records what became of a held Auto email reply.
func SupportAssistantHeld(result string) { supportAssistantHeld.WithLabelValues(result).Inc() }

// KnowledgeWebsitePage records one page a website import handled.
func KnowledgeWebsitePage(result string) { knowledgeWebsitePages.WithLabelValues(result).Inc() }

// SupportSent records one run of the support reply job.
func SupportSent(outcome string, d time.Duration) {
	supportReplies.WithLabelValues(outcome).Inc()
	supportReplyDuration.Observe(d.Seconds())
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

// DataNetworkLookup counts one data network lookup by outcome.
func DataNetworkLookup(outcome string) { dataNetworkLookups.WithLabelValues(outcome).Inc() }

// DataNetworkShared counts addresses shared with the data network.
func DataNetworkShared(delivered, bounced int) {
	dataNetworkShared.WithLabelValues("delivered").Add(float64(delivered))
	dataNetworkShared.WithLabelValues("bounced").Add(float64(bounced))
}

// DataNetworkShareRun counts one sharing job by outcome.
func DataNetworkShareRun(outcome string) { dataNetworkShareRuns.WithLabelValues(outcome).Inc() }

// SetDataNetwork sets the data network's gauges: members, withdrawals
// still to do, the most overdue one and the sharing lag, in seconds.
func SetDataNetwork(members, withdrawals int, overdue, lag float64) {
	dataNetworkMembers.Set(float64(members))
	dataNetworkWithdrawals.Set(float64(withdrawals))
	dataNetworkOverdue.Set(overdue)
	dataNetworkLag.Set(lag)
}

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

// AdItemRows records n days of numbers stored for ad groups or ads.
func AdItemRows(level string, n int) {
	adItemRows.WithLabelValues(level).Add(float64(n))
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

// LinkPitchAutoSent records one pitch through a sequence that skips
// approval: sent, or why it waits (unsure, spammy_site, daily_cap, or why
// it couldn't be approved).
func LinkPitchAutoSent(outcome string) { linkAutoSends.WithLabelValues(outcome).Inc() }

// LinkReplyRead records one reply to a pitch the AI read.
func LinkReplyRead(kind string) { linkReplies.WithLabelValues(kind).Inc() }

// WebSearch records one search on a workspace's SERP connection.
func WebSearch(outcome string) { webSearches.WithLabelValues(outcome).Inc() }

// AIPromptDraft records one draft of AI visibility questions.
func AIPromptDraft(outcome string) { aiPromptDrafts.WithLabelValues(outcome).Inc() }

// AIDemand records one read of AI demand: kind is questions or volume.
func AIDemand(kind, outcome string) {
	if kind != "questions" && kind != "volume" && kind != "benchmark" {
		kind = "other"
	}
	aiDemandReads.WithLabelValues(kind, outcome).Inc()
}

// AIStance records one answer read for its stances; outcome is judged,
// skipped or an error code.
func AIStance(outcome string) { aiStanceReads.WithLabelValues(outcome).Inc() }

// AIAnswer records one answer read from an AI assistant's own screen.
// engine is chatgpt or gemini.
func AIAnswer(engine, outcome string) {
	if engine != "chatgpt" && engine != "gemini" {
		engine = "other"
	}
	aiAnswers.WithLabelValues(engine, outcome).Inc()
}

// LinkLiveCheck records one check of an agreed or live link.
func LinkLiveCheck(outcome string) { linkLiveChecks.WithLabelValues(outcome).Inc() }

// LinkPageCheck records one check of a link prospect's page.
func LinkPageCheck(status string) { linkPageChecks.WithLabelValues(status).Inc() }

// ArchiveLookup records one Internet Archive lookup.
func ArchiveLookup(outcome string) { archiveLookups.WithLabelValues(outcome).Inc() }

// PricingPageRead records one read of a pricing page by its outcome.
func PricingPageRead(outcome string) { pricingReads.WithLabelValues(outcome).Inc() }

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

// StoryAIRun records one run of the AI on stories: ideas or a draft, and
// its outcome.
func StoryAIRun(kind, outcome string) { storyAIRuns.WithLabelValues(kind, outcome).Inc() }

// QuoteApprovalEmail records what became of one quote approval email in
// its outreach sequence: sent, blocked, failed or bounced.
func QuoteApprovalEmail(outcome string) { quoteApprovalEmails.WithLabelValues(outcome).Inc() }

// QuoteApprovalPage records one request to the public quote approval page.
func QuoteApprovalPage(action, outcome string) {
	quoteApprovalPages.WithLabelValues(action, outcome).Inc()
}

// QuoteApprovalDecided records an expert's decision through an approval
// link.
func QuoteApprovalDecided(decision string) { quoteApprovalDecisions.WithLabelValues(decision).Inc() }

// SocialPublished records one try of the publish job.
func SocialPublished(channel, outcome string) {
	socialPublishes.WithLabelValues(channel, outcome).Inc()
}

// RedditSent records one send of a Reddit campaign action to Engain.
func RedditSent(kind, outcome string) {
	redditSends.WithLabelValues(kind, outcome).Inc()
}

// RedditChecked records one check of a sent Reddit campaign action.
func RedditChecked(outcome string, redditRead bool) {
	read := "false"
	if redditRead {
		read = "true"
	}
	redditChecks.WithLabelValues(outcome, read).Inc()
}

// DomainConnectLookup records a Domain Connect lookup: available, or the
// reason it isn't, the step a failed one stopped at ("none" otherwise),
// and how long it took.
func DomainConnectLookup(outcome, step string, d time.Duration) {
	if step == "" {
		step = "none"
	}
	domainConnectLookups.WithLabelValues(outcome, step).Inc()
	domainConnectDuration.Observe(d.Seconds())
}

// AdConversions counts what one send of a conversion rule did.
func AdConversions(connector string, sent, skipped, failed int) {
	adConversions.WithLabelValues(connector, "sent").Add(float64(sent))
	adConversions.WithLabelValues(connector, "skipped").Add(float64(skipped))
	adConversions.WithLabelValues(connector, "failed").Add(float64(failed))
}

// AdConversionAdjustments counts what one round of a rule's adjustments did.
func AdConversionAdjustments(connector string, sent, skipped, failed int) {
	adConversionAdjustments.WithLabelValues(connector, "sent").Add(float64(sent))
	adConversionAdjustments.WithLabelValues(connector, "skipped").Add(float64(skipped))
	adConversionAdjustments.WithLabelValues(connector, "failed").Add(float64(failed))
}

// AdsFeedSends counts the conversions one feed batch sent and the ones
// the platform refused, by the transport it took.
func AdsFeedSends(transport string, sent, failed int) {
	adsFeedSends.WithLabelValues(transport, "sent").Add(float64(sent))
	adsFeedSends.WithLabelValues(transport, "failed").Add(float64(failed))
}

// AdsFeedBatch counts one feed batch: sent, failed, or refused (the
// destination cannot be used, so the account falls back to ads_api).
func AdsFeedBatch(transport, outcome string) {
	adsFeedBatches.WithLabelValues(transport, outcome).Inc()
}

// AdsPayersListMembers records how many people every payers list holds,
// added together.
func AdsPayersListMembers(n int64) {
	adsPayersListMembers.Set(float64(n))
}

// AdConversionsOldest records how long the oldest waiting conversion has
// waited.
func AdConversionsOldest(d time.Duration) { adConversionsOldest.Set(d.Seconds()) }

// PaidAdsLedgerEvent records one customer event the paid ads ledger looked
// at: its kind (signup, payment, refund) and what came of it.
func PaidAdsLedgerEvent(kind, outcome string) {
	paidAdsLedgerEvents.WithLabelValues(kind, outcome).Inc()
}

// PaidAdsLedgerLag records how far behind the ledger is for the
// furthest-behind workspace, as of this round.
func PaidAdsLedgerLag(d time.Duration) { paidAdsLedgerLag.Set(d.Seconds()) }

// PaidAdsAcquisition records one person a brand's ads acquired, by the
// network's connector and whether a sign-up or a first payment made them.
func PaidAdsAcquisition(connector, kind string) {
	paidAdsAcquisitions.WithLabelValues(connector, kind).Inc()
}

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

// AutomationRun counts a playbook automation firing: kind is schedule or
// event; outcome is started, held, failed or capped.
func AutomationRun(kind, outcome string) { automationRuns.WithLabelValues(kind, outcome).Inc() }

// AutomationLag records how late the most overdue automation is.
func AutomationLag(d time.Duration) { automationLag.Set(d.Seconds()) }

// InboundReceived counts one message delivered to the inbound endpoint:
// its purpose ("none" when the address is unknown), outcome and size.
func InboundReceived(purpose, outcome string, bytes int) {
	if purpose == "" {
		purpose = "none"
	}
	inboundReceived.WithLabelValues(purpose, outcome).Inc()
	if outcome == "accepted" {
		inboundBytes.Add(float64(bytes))
	}
}

// InboundProcessed counts one inbound message processed, by its purpose
// and the status it ended in.
func InboundProcessed(purpose, status string) {
	inboundProcessed.WithLabelValues(purpose, status).Inc()
}

// InboundPurged counts raw inbound messages deleted after 30 days.
func InboundPurged(n int64) { inboundPurged.Add(float64(n)) }

// EventInboxReceived counts product events saved to the event inbox.
func EventInboxReceived(n int) { eventInboxReceived.Add(float64(n)) }

// EventInboxProcessed counts a product event the inbox job finished, with
// its outcome (accepted, duplicate, rejected, held), the rejection's code
// and how long it took.
func EventInboxProcessed(outcome, code string, d time.Duration) {
	eventInboxProcessed.WithLabelValues(outcome).Inc()
	if outcome == "rejected" {
		eventInboxRejected.WithLabelValues(code).Inc()
	}
	eventInboxDuration.Observe(d.Seconds())
}

// SetEventInboxPending records how many events wait and how long the
// oldest has waited.
func SetEventInboxPending(n int, oldest time.Duration) {
	eventInboxPending.Set(float64(n))
	eventInboxOldest.Set(max(oldest, 0).Seconds())
}

// EventInboxPurged counts inbox rows deleted at the end of their retention.
func EventInboxPurged(n int) { eventInboxPurged.Add(float64(n)) }

// SetEventInboxPurgeOverdue records how far past its retention the oldest
// row still waiting for the sweep is.
func SetEventInboxPurgeOverdue(d time.Duration) { eventInboxPurgeOverdue.Set(max(d, 0).Seconds()) }

// PrivacyExportBuilt counts a privacy export the job finished, with its
// outcome (ready, or the failure's code), how long it took and, when
// ready, its uncompressed size.
func PrivacyExportBuilt(outcome string, d time.Duration, size int64) {
	privacyExportsBuilt.WithLabelValues(outcome).Inc()
	if d > 0 {
		privacyExportSeconds.Observe(d.Seconds())
	}
	if outcome == "ready" {
		privacyExportBytes.Observe(float64(size))
	}
}

// PrivacyExportsExpired counts privacy exports whose file was deleted.
func PrivacyExportsExpired(n int) { privacyExportsExpired.Add(float64(n)) }

// RecoveryDeleted counts customer data deleted into the recovery window.
func RecoveryDeleted(kind string) { recoveryDeleted.WithLabelValues(kind).Inc() }

// RecoveryRestored counts a deletion restored, with its outcome.
func RecoveryRestored(kind, outcome string) { recoveryRestored.WithLabelValues(kind, outcome).Inc() }

// RecoveryPurged counts a deletion purged after its 7 days.
func RecoveryPurged(kind string) { recoveryPurged.WithLabelValues(kind).Inc() }

// SetConnectionsUnassigned sets how many connections (kind connection) or
// accounts (kind account) have no brand.
func SetConnectionsUnassigned(kind string, n int) {
	connectionsUnassigned.WithLabelValues(kind).Set(float64(n))
}

// SetRecoveryPurgeOverdue records how far past its 7 days the oldest
// deletion waiting for the purge is.
func SetRecoveryPurgeOverdue(d time.Duration) { recoveryPurgeOverdue.Set(max(d, 0).Seconds()) }

// Unmerged counts a merge undone or refused.
func Unmerged(outcome string) { undoUnmerges.WithLabelValues(outcome).Inc() }

// BulkEditFinished counts a bulk edit (kind edit or undo) that finished
// with outcome done or failed, the records it went through by result
// (changed, unchanged, skipped), and how long it took since it was queued.
func BulkEditFinished(kind, outcome string, records map[string]int, d time.Duration) {
	bulkEdits.WithLabelValues(kind, outcome).Inc()
	for _, result := range []string{"changed", "unchanged", "skipped"} {
		if n := records[result]; n > 0 {
			bulkEditRecords.WithLabelValues(kind, result).Add(float64(n))
		}
	}
	if outcome == "done" {
		bulkEditSeconds.WithLabelValues(kind).Observe(d.Seconds())
	}
}

// BatchUndone counts a batch undo by mode and outcome (undone, queued,
// failed), with how long it took when it ran.
func BatchUndone(mode, outcome string, d time.Duration) {
	undoBatches.WithLabelValues(mode, outcome).Inc()
	if outcome == "undone" {
		undoBatchSeconds.Observe(d.Seconds())
	}
}

// UndoRowsReverted counts rows an undo moved back, removed or put back.
func UndoRowsReverted(kind string, n int) {
	if n > 0 {
		undoRowsReverted.WithLabelValues(kind).Add(float64(n))
	}
}

// RatesFetched counts one fetch of exchange rates, success or error, and
// on success sets the newest rate's age.
func RatesFetched(outcome string, newest time.Time) {
	ratesFetched.WithLabelValues(outcome).Inc()
	if !newest.IsZero() {
		ratesAge.Set(time.Since(newest).Seconds())
	}
}

// GeoDownloaded counts one attempt to download a month's DB-IP file.
func GeoDownloaded(file, outcome string) { geoDownloads.WithLabelValues(file, outcome).Inc() }

// GeoFileBuilt reports the age of the DB-IP file in use; a zero built means
// the server has none, and the file's age is dropped.
func GeoFileBuilt(file string, built, now time.Time) {
	if built.IsZero() {
		geoAge.DeleteLabelValues(file)
		return
	}
	geoAge.WithLabelValues(file).Set(now.Sub(built).Seconds())
}

// ReceiptsConverted counts receipts given their US dollar amount late.
func ReceiptsConverted(n int64) { receiptsConverted.Add(float64(n)) }

// QuotaChecked counts one quota check: resource is one of quota.Resources,
// outcome allowed, refused or error.
func QuotaChecked(resource, outcome string) { quotaChecks.WithLabelValues(resource, outcome).Inc() }

// DesktopLinked counts one step of linking a computer.
func DesktopLinked(step string) { desktopLinks.WithLabelValues(step).Inc() }

// DesktopReport counts one report from Outflare for desktop.
func DesktopReport(outcome string) { desktopReports.WithLabelValues(outcome).Inc() }

// DesktopReadings counts usage windows reported for a provider.
func DesktopReadings(provider, source string, n int) {
	desktopReadings.WithLabelValues(provider, source).Add(float64(n))
}

// DesktopReadFailed counts a tool the app couldn't read. code comes from
// the app, so anything outside a bounded pattern is counted as other.
func DesktopReadFailed(provider, code string) {
	if len(code) > 40 {
		code = "other"
	}
	desktopReadFailures.WithLabelValues(provider, code).Inc()
}

// DesktopDevices sets how many linked computers report and are silent.
func DesktopDevices(reporting, silent int64) {
	desktopDevices.WithLabelValues("reporting").Set(float64(reporting))
	desktopDevices.WithLabelValues("silent").Set(float64(silent))
}

// DesktopPurged counts usage readings deleted after 90 days.
func DesktopPurged(n int64) { desktopPurged.Add(float64(n)) }

// CoverageCheck records a bounded page-check outcome.
// MailPrivacyCheck counts one email's privacy check before team sharing:
// work, private, failed, no_ai or budget.
func MailPrivacyCheck(outcome string) { mailPrivacyChecks.WithLabelValues(outcome).Inc() }

// EventNameCheck counts one new event name's check: method ai or
// spelling, outcome ok or asked.
func EventNameCheck(method, outcome string) { eventNameChecks.WithLabelValues(method, outcome).Inc() }

func CoverageCheck(health string) { coverageChecks.WithLabelValues(health).Inc() }

func CoverageReview(status string) { coverageReviews.WithLabelValues(status).Inc() }

// CoverageFound counts pages discovery added to a review list for the
// first time, by source.
func CoverageFound(source string, n int) {
	if n > 0 {
		coverageFound.WithLabelValues(source).Add(float64(n))
	}
}

// CoverageSearch counts a Presence web search run by outcome.
func CoverageSearch(outcome string) { coverageSearches.WithLabelValues(outcome).Inc() }

// CompetitorScan counts one source of a competitor presence scan by outcome.
func CompetitorScan(source, outcome string) { competitorScans.WithLabelValues(source, outcome).Inc() }

// CompetitorSuggest counts one daily look for new competitors and, when it
// worked, the suggestions it made.
func CompetitorSuggest(source, outcome string, suggested int) {
	competitorSuggest.WithLabelValues(source, outcome).Inc()
	if suggested > 0 {
		competitorSuggest.WithLabelValues(source, "suggested").Add(float64(suggested))
	}
}

// CoverageOpportunities counts opportunities suggested and won.
func CoverageOpportunities(suggested, won int) {
	if suggested > 0 {
		opportunities.WithLabelValues("suggested").Add(float64(suggested))
	}
	if won > 0 {
		opportunities.WithLabelValues("won").Add(float64(won))
	}
}

// CompetitorFound counts new pages where a competitor appears.
func CompetitorFound(arrangement string, n int) {
	if n > 0 {
		competitorFound.WithLabelValues(arrangement).Add(float64(n))
	}
}

// TelegramStep counts one step of a company Telegram account's sign-in or
// check, with how it ended.
func TelegramStep(step, outcome string) { telegramSteps.WithLabelValues(step, outcome).Inc() }

// TelegramSynced counts messages copied in (direction in) or out.
func TelegramSynced(direction string, n int) {
	telegramSynced.WithLabelValues(direction).Add(float64(n))
}

// TelegramSyncRun counts one sync of an account, with how it ended.
func TelegramSyncRun(outcome string) { telegramSyncRuns.WithLabelValues(outcome).Inc() }

// TelegramMatch counts one chat matched, with its result.
func TelegramMatch(result string) { telegramMatches.WithLabelValues(result).Inc() }

// TelegramSummary counts one chat summary, with how it ended.
func TelegramSummary(outcome string) { telegramSummaries.WithLabelValues(outcome).Inc() }

// TelegramWaiting sets how many customers are waiting past the alert time.
func TelegramWaiting(n int) { telegramWaiting.Set(float64(n)) }

// TelegramSyncLag sets how far behind the least recently synced account is.
func TelegramSyncLag(d time.Duration) { telegramSyncLag.Set(d.Seconds()) }

var telegramStatuses = []string{"starting", "waiting_scan", "needs_password", "checking_password", "connected", "expired", "disconnecting", "signed_out", "failed"}

// TelegramAccounts sets how many company Telegram accounts are in each
// status.
func TelegramAccounts(counts map[string]int) {
	for _, st := range telegramStatuses {
		telegramAccounts.WithLabelValues(st).Set(float64(counts[st]))
	}
}

// ---- Paid ads intelligence layer (engine/internal/adsync) ----

// AdSyncRun records one run of the hourly ad sync for a connection: the
// window it read (first, hourly, daily, weekly, monthly) and its outcome.
func AdSyncRun(window, outcome string) {
	adSyncRuns.WithLabelValues(window, outcome).Inc()
}

// AdClicksHarvested records n paid clicks stored from a network's click
// report.
func AdClicksHarvested(connector string, n int) {
	if n > 0 {
		adClicksHarvested.WithLabelValues(connector, "stored").Add(float64(n))
	}
}

// AdClickHarvestFailed records one click report read a network refused.
func AdClickHarvestFailed(connector, code string) {
	adClicksHarvested.WithLabelValues(connector, code).Inc()
}

// AdChangeEventsRead records one read of a platform's change history and
// the n changes it stored.
func AdChangeEventsRead(connector, outcome string, n int) {
	adChangeEventReads.WithLabelValues(connector, outcome).Inc()
	if n > 0 {
		adChangeEvents.WithLabelValues(connector).Add(float64(n))
	}
}

// AdAlertRaised records one new alert finding of a kind.
func AdAlertRaised(kind string) {
	adAlerts.WithLabelValues(kind).Inc()
}

// AdAlertRun records one run of the alert job and its outcome.
func AdAlertRun(outcome string) {
	adAlertRuns.WithLabelValues(outcome).Inc()
}

// ---- Brand knowledge (paid ads: the sheet ads are written from) ----

// brandKnowledgeReads counts reads of a brand's website for its knowledge
// sheet (the brand.read_knowledge job: on creation, on request and daily
// once an ad account is connected), by how each ended.
var brandKnowledgeReads = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace, Subsystem: "brand", Name: "knowledge_reads_total",
	Help: "Reads of brands' websites for their knowledge sheet, by outcome: success, failed (the workspace lacks a page reader or an AI model, or the site refused), error (the read broke).",
}, []string{"outcome"})

// BrandKnowledgeRead records one read of a brand's website for its
// knowledge sheet and how it ended (success, failed, error).
func BrandKnowledgeRead(outcome string) {
	brandKnowledgeReads.WithLabelValues(outcome).Inc()
}

// ---- Paid ads: findings and verdicts (engine/internal/adsuggest) ----

// AdFinding records one finding the rules wrote: created for a person,
// applied on its own, or recorded in shadow mode.
func AdFinding(kind, autonomy, outcome string) {
	adFindings.WithLabelValues(kind, autonomy, outcome).Inc()
}

// AdVerdict records one verdict on a finding whose change was made.
func AdVerdict(kind, verdict string) {
	adFindingVerdicts.WithLabelValues(kind, verdict).Inc()
}

// AdFindingsOpen sets how many findings wait for a person.
func AdFindingsOpen(n int) {
	adFindingsOpen.Set(float64(n))
}
