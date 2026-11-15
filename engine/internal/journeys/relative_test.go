package journeys

import (
	"testing"
	"time"
)

// Conditions can compare a date field with a day relative to today.
func TestRelativeDates(t *testing.T) {
	now := time.Now().UTC()
	day := func(n int) string { return now.AddDate(0, 0, n).Format("2006-01-02") }
	if d, ok := RelativeDate("+60d", now); !ok || d != day(60) {
		t.Fatalf("+60d: %q %v", d, ok)
	}
	for _, v := range []any{"60d", "tomorrow", "+d", 5, nil, "2026-01-01"} {
		if _, ok := RelativeDate(v, now); ok {
			t.Errorf("%v read as a relative date", v)
		}
	}
	attrs := map[string]any{"renews_on": day(30), "last_seen": now.AddDate(0, 0, -20).Format(time.RFC3339), "plan": "pro", "seats": 5.0}
	for _, c := range []struct {
		cond Condition
		want bool
	}{
		{Condition{Attribute: "renews_on", Op: "lt", Value: "+60d"}, true},
		{Condition{Attribute: "renews_on", Op: "lt", Value: "+10d"}, false},
		{Condition{Attribute: "renews_on", Op: "gt", Value: "today"}, true},
		{Condition{Attribute: "renews_on", Op: "eq", Value: "+30d"}, true},
		{Condition{Attribute: "last_seen", Op: "lt", Value: "-14d"}, true},
		{Condition{Attribute: "last_seen", Op: "gt", Value: "-14d"}, false},
		{Condition{Attribute: "last_seen", Op: "eq", Value: "-20d"}, true},
		{Condition{Attribute: "plan", Op: "lt", Value: "+60d"}, false},
		{Condition{Attribute: "seats", Op: "gt", Value: "today"}, false},
		{Condition{Attribute: "missing", Op: "lt", Value: "+60d"}, false},
	} {
		if got := Holds(attrs, nil, []Condition{c.cond}, "all"); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.cond, got, c.want)
		}
	}
}
