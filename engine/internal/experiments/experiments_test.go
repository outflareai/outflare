package experiments

import (
	"encoding/json"
	"math"
	"testing"
)

func TestPattern(t *testing.T) {
	re, err := Pattern("/blog/*")
	if err != nil || !re.MatchString("/blog/a/b") || re.MatchString("/about") {
		t.Fatalf("pattern = %v %v", re, err)
	}
	if _, err := Pattern("blog/*"); err == nil {
		t.Fatal("a pattern starts with /")
	}
	if re, err := Pattern(""); re != nil || err != nil {
		t.Fatal("empty is every page")
	}
}

func TestSummariseAndTest(t *testing.T) {
	pages := []string{"a", "b", "c", "d", "e", "f"}
	before, after, gone := map[string]*PageValue{}, map[string]*PageValue{}, map[string]*PageValue{}
	for i, p := range pages {
		before[p] = &PageValue{URL: p, Clicks: float64(28 * (10 + i)), Impressions: 1000}
		before[p].finish("clicks", 28)
		after[p] = &PageValue{URL: p, Clicks: float64(28 * (15 + i)), Impressions: 1000}
		after[p].finish("clicks", 28)
	}
	g, changes := summarise("clicks", pages, before, after, 28, 28)
	if g.Change == nil || *g.Change < 0.3 || len(changes) != 6 {
		t.Fatalf("group = %+v %v", g, changes)
	}
	if p := pValue(changes, nil, false); p == nil || *p > 0.05 {
		t.Fatalf("p = %v", p)
	}
	if p := pValue(changes[:3], nil, false); p != nil {
		t.Fatal("too few pages to test")
	}
	// A group that lost every click has a change but no infinite lift.
	c, _ := summarise("clicks", pages, before, gone, 28, 28)
	if c.Change == nil || *c.Change != -1 {
		t.Fatalf("gone = %+v", c)
	}
	if _, err := json.Marshal(c); err != nil || math.IsInf(*c.Change, 0) {
		t.Fatal(err)
	}
}
