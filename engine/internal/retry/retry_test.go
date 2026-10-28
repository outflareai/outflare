package retry

import (
	"strings"
	"testing"
	"time"
)

func TestAfter(t *testing.T) {
	want := []time.Duration{15 * time.Minute, 15 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 4 * time.Hour, 4 * time.Hour}
	for n, w := range want {
		if got := After(n); got != w {
			t.Errorf("After(%d) = %s, want %s", n, got, w)
		}
	}
	// Every retry of a day's read happens the same day: the waits up to
	// the last step add up to well under a day.
	var sum time.Duration
	for _, d := range Steps {
		sum += d
	}
	if sum > 8*time.Hour {
		t.Errorf("the steps add up to %s", sum)
	}
}

func TestSQL(t *testing.T) {
	got := SQL("failures + 1")
	for _, part := range []string{"WHEN failures + 1 <= 1 THEN interval '900 seconds'", "WHEN failures + 1 <= 3 THEN interval '7200 seconds'", "ELSE interval '14400 seconds' END"} {
		if !strings.Contains(got, part) {
			t.Errorf("SQL() = %s, missing %s", got, part)
		}
	}
}
