package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Pipeline stages (engine/internal/pipeline): what this process worked on.
// The backlog of every stage, across processes, is read from the database
// in Postgres. Labels are pipeline and stage names, outcomes and error
// codes: a fixed set per stage.
var (
	pipelineItems = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "pipeline", Name: "items_total",
		Help: "Pipeline items this process worked on, by pipeline, stage and outcome (advanced, done, skipped, held, retried, waited, paused, failed).",
	}, []string{"pipeline", "stage", "outcome"})
	pipelineCodes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "pipeline", Name: "codes_total",
		Help: "Why pipeline items were skipped, held, retried, made to wait, paused or failed, by pipeline, stage and error code.",
	}, []string{"pipeline", "stage", "code"})
	pipelineBatch = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "pipeline", Name: "batch_seconds",
		Help: "Time a stage took on one batch, by pipeline and stage.", Buckets: jobBuckets,
	}, []string{"pipeline", "stage"})
	pipelineBatchSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "pipeline", Name: "batch_items",
		Help:    "Items in one batch, by pipeline and stage. Small batches with a big backlog mean the per-workspace cap is binding.",
		Buckets: []float64{1, 5, 10, 25, 50, 100},
	}, []string{"pipeline", "stage"})
	pipelineWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "pipeline", Name: "wait_seconds",
		Help: "Time from an item being due to a worker claiming it, by pipeline and stage.", Buckets: jobBuckets,
	}, []string{"pipeline", "stage"})
	pipelineLimitWaits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "pipeline", Name: "limit_waits_total",
		Help: "Calls to an outside account that had to wait, by kind of account and why (rate, breaker, probe).",
	}, []string{"account", "reason"})
	pipelineLimitReports = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "pipeline", Name: "limit_reports_total",
		Help: "Outcomes of calls to outside accounts, by kind of account and outcome (success, rate_limited, failure).",
	}, []string{"account", "outcome"})
)

// PipelineClaimed records how long a claimed item waited after it was due.
func PipelineClaimed(pipeline, stage string, waited time.Duration) {
	pipelineWait.WithLabelValues(pipeline, stage).Observe(max(waited, 0).Seconds())
}

// PipelineBatch records one batch of a stage.
func PipelineBatch(pipeline, stage string, items int, took time.Duration) {
	pipelineBatch.WithLabelValues(pipeline, stage).Observe(took.Seconds())
	pipelineBatchSize.WithLabelValues(pipeline, stage).Observe(float64(items))
}

// PipelineOutcome counts n items with an outcome.
func PipelineOutcome(pipeline, stage, outcome string, n int) {
	pipelineItems.WithLabelValues(pipeline, stage, outcome).Add(float64(n))
}

// PipelineCode counts n items with an error code.
func PipelineCode(pipeline, stage, code string, n int) {
	pipelineCodes.WithLabelValues(pipeline, stage, code).Add(float64(n))
}

// PipelineLimitWait counts a call that waited on an account's limit.
// account is its kind ("integration", "ai"), never its id.
func PipelineLimitWait(account, reason string) {
	pipelineLimitWaits.WithLabelValues(account, reason).Inc()
}

// PipelineLimitReport counts a call's outcome against an account.
func PipelineLimitReport(account, outcome string) {
	pipelineLimitReports.WithLabelValues(account, outcome).Inc()
}
