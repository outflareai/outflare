package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

var salesReportingReads = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: namespace, Subsystem: "sales_reporting", Name: "read_seconds",
	Help:    "Lead Portal reporting reads by bounded read type and outcome, including schema validation. Origin lookups have an eight-second total deadline.",
	Buckets: []float64{.1, .5, 1, 2, 5, 8, 15, 30, 60},
}, []string{"read", "outcome"})

func SalesReportingRead(read string, success bool, d time.Duration) {
	switch read {
	case "summary", "leads", "origins", "check":
	default:
		return
	}
	outcome := "error"
	if success {
		outcome = "success"
	}
	salesReportingReads.WithLabelValues(read, outcome).Observe(d.Seconds())
}
