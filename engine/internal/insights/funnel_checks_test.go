package insights

import (
	"testing"
	"time"
)

func TestStopped(t *testing.T) {
	now := time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC) // a Monday morning
	day := 24 * time.Hour
	long := now.Add(-60 * day)
	cases := []struct {
		name                 string
		first, last          time.Time
		recent, week1, week2 int
		want                 bool
	}{
		{"arrived an hour ago", long, now.Add(-time.Hour), 700, 20, 20, false},
		{"a quiet step: too few to miss", long, now.Add(-3 * day), 14, 3, 3, false},
		{"busy, then nothing for three days", long, now.Add(-3 * day), 700, 150, 150, true},
		{"quiet every weekend, as it is now", long, now.Add(-63 * time.Hour), 700, 0, 0, false},
		{"new step, no weeks to compare, gone quiet", now.Add(-5 * day), now.Add(-2 * day), 300, 0, 0, true},
	}
	for _, c := range cases {
		if got := stopped(c.first, c.last, c.recent, c.week1, c.week2, now); got != c.want {
			t.Errorf("%s: stopped = %v, want %v", c.name, got, c.want)
		}
	}
}
