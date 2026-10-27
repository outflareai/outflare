package analyticsync

import (
	"testing"
	"time"
)

func TestSearchDays(t *testing.T) {
	y := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	if d := searchDays(searchState{}, y); len(d) != searchFirstDays || !d[0].Equal(y) || !d[89].Equal(y.AddDate(0, 0, -89)) {
		t.Fatalf("first run: %d days", len(d))
	}
	oldest := y.AddDate(0, 0, -89)
	d := searchDays(searchState{oldest: &oldest}, y)
	if len(d) != searchRecentDays+searchBackDays || !d[searchRecentDays].Equal(oldest.AddDate(0, 0, -1)) {
		t.Fatalf("later run: %v", d)
	}
	// The backfill stops at 16 months, and stops once done.
	near := y.AddDate(0, 0, -(searchKeepDays - 3))
	if d := searchDays(searchState{oldest: &near}, y); len(d) != searchRecentDays+2 {
		t.Fatalf("near the floor: %d", len(d))
	}
	if d := searchDays(searchState{oldest: &oldest, backfilled: true}, y); len(d) != searchRecentDays {
		t.Fatalf("backfilled: %d", len(d))
	}
}

func TestInProperty(t *testing.T) {
	for _, c := range []struct {
		site, url string
		want      bool
	}{
		{"sc-domain:example.com", "https://www.example.com/a", true},
		{"sc-domain:example.com", "https://docs.example.com/a", true},
		{"sc-domain:example.com", "https://example.org/a", false},
		{"sc-domain:example.com", "https://notexample.com/", false},
		{"https://www.example.com/", "https://www.example.com/a", true},
		{"https://www.example.com/", "http://www.example.com/a", false},
		{"https://www.example.com/blog/", "https://www.example.com/pricing", false},
	} {
		if got := inProperty(c.site, c.url); got != c.want {
			t.Errorf("inProperty(%q, %q) = %v", c.site, c.url, got)
		}
	}
}
