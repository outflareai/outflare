// Package retry is when Outflare tries a failed daily read again: review
// pages, social profiles, rank checks. Their day-by-day numbers are a
// reading taken that day and can't be filled in later, so a failed read
// is tried again soon and then every few hours, until one works the same
// day, rather than waiting for tomorrow.
package retry

import (
	"fmt"
	"strings"
	"time"
)

// Steps are the waits after the first, second, third ... failure in a
// row; later failures wait the last step.
var Steps = []time.Duration{15 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour}

// After is how long to wait after failures reads in a row failed.
func After(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	return Steps[min(failures, len(Steps))-1]
}

// SQL is After as a Postgres interval, for failures given as an
// expression (a column, or "failures + 1").
func SQL(failures string) string {
	var b strings.Builder
	b.WriteString("(CASE")
	for i, d := range Steps[:len(Steps)-1] {
		fmt.Fprintf(&b, " WHEN %s <= %d THEN interval '%d seconds'", failures, i+1, int(d.Seconds()))
	}
	fmt.Fprintf(&b, " ELSE interval '%d seconds' END)", int(Steps[len(Steps)-1].Seconds()))
	return b.String()
}
