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
	inboxEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "inbox", Name: "events_total",
		Help: "Browser events workers took from the inbox, by outcome (stored, rejected, failed).",
	}, []string{"outcome"})
	inboxBatch = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "inbox", Name: "batch_seconds",
		Help: "Time to store one inbox row (a browser's batch of events).", Buckets: requestBuckets,
	})

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
	siteEvents, inboxEvents, inboxBatch,
	pipelineItems, pipelineCodes, pipelineBatch, pipelineBatchSize, pipelineWait, pipelineLimitWaits, pipelineLimitReports,
	aiCalls, aiTokens, aiCost, aiBudgetAlerts,
	hooksReceived, hookEvents, hookLag,
	domainConnectLookups, domainConnectDuration,
	queryRuns, queryDuration, queryRows,
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

// SiteEventsCapped counts browser events turned away by a daily limit.
func SiteEventsCapped(n int) { siteEvents.WithLabelValues("capped").Add(float64(n)) }

// InboxStored records one inbox row a worker finished: how many of its
// events were stored and rejected, or failed when the row failed.
func InboxStored(stored, rejected, failed int, d time.Duration) {
	inboxEvents.WithLabelValues("stored").Add(float64(stored))
	inboxEvents.WithLabelValues("rejected").Add(float64(rejected))
	inboxEvents.WithLabelValues("failed").Add(float64(failed))
	inboxBatch.Observe(d.Seconds())
}

// QueryRan records one query: its outcome, time and rows returned.
func QueryRan(outcome string, d time.Duration, rows int) {
	queryRuns.WithLabelValues(outcome).Inc()
	queryDuration.Observe(d.Seconds())
	queryRows.Add(float64(rows))
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

// DomainConnectLookup records a Domain Connect lookup: available, or the
// reason it isn't, and how long it took.
func DomainConnectLookup(outcome string, d time.Duration) {
	domainConnectLookups.WithLabelValues(outcome).Inc()
	domainConnectDuration.Observe(d.Seconds())
}
