package seointel

import (
	"math"
	"testing"
	"time"
)

func TestCTRAndOpportunityEstimates(t *testing.T) {
	rows := []InsightRow{{Key: "a", Impressions: 2000, Clicks: 20, Position: 5}, {Key: "b", Impressions: 100, Clicks: 1, Position: 10}, {Key: "unknown", Impressions: 100, Clicks: 0, Position: 0}}
	ops, curve := Opportunities(rows)
	if curve[4].Status != "site_observed" || curve[4].ExpectedCTR != .01 {
		t.Fatalf("observed curve: %+v", curve[4])
	}
	if len(ops) != 2 {
		t.Fatalf("opportunities: %+v", ops)
	}
	for _, x := range ops {
		if x.PotentialClicks < 0 || x.MissedClicks < 0 || math.IsNaN(x.Score) {
			t.Fatal(x)
		}
	}
	_, empty := Opportunities(nil)
	if len(empty) != 20 || empty[0].Status != "reference" {
		t.Fatal(empty)
	}
}
func TestSearchGroupRules(t *testing.T) {
	for _, tc := range []struct {
		g           SearchGroup
		page, query string
		want        bool
	}{
		{SearchGroup{Name: "Blog", Kind: "content", Rules: []GroupRule{{"prefix", "/blog/"}}}, "https://example.com/blog/a", "", true},
		{SearchGroup{Name: "Blog", Kind: "content", Rules: []GroupRule{{"equals", "/blog/"}}}, "https://example.com/blog/a", "", false},
		{SearchGroup{Name: "Proxy", Kind: "topic", Rules: []GroupRule{{"contains", "PROXY"}}}, "https://example.com/", "Cheap proxy", true},
		{SearchGroup{Name: "Plans", Kind: "content", Rules: []GroupRule{{"regex", `/plans/[a-z]+$`}}}, "https://example.com/plans/basic", "", true},
	} {
		if !ValidRules(tc.g) {
			t.Fatal("valid rule rejected")
		}
		if Matches(tc.g, tc.page, tc.query) != tc.want {
			t.Fatal(tc)
		}
	}
	if ValidRules(SearchGroup{Name: "Bad", Kind: "topic", Rules: []GroupRule{{"regex", "["}}}) {
		t.Fatal("invalid regex accepted")
	}
}
func TestForecastAndChangePoints(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	p := []InsightPoint{}
	for i := 0; i < 100; i++ {
		v := 100.0
		if start.AddDate(0, 0, i).Weekday() == time.Sunday {
			v = 50
		}
		p = append(p, InsightPoint{Date: start.AddDate(0, 0, i).Format(time.DateOnly), Value: v})
	}
	if len(Forecast(p[:20], 12, 0, 0, 0)) != 0 {
		t.Fatal("forecast without history")
	}
	a := Forecast(p, 4, 0, .1, 20)
	b := Forecast(p, 4, 1, .1, 20)
	if len(a) != 28 || b[27].Value <= a[27].Value {
		t.Fatal("growth scenario")
	}
	for _, x := range a {
		if x.Lower < 0 || x.Upper < x.Value || math.Abs(float64(x.RevenueMicros)-x.Value*2e6) > 1 {
			t.Fatalf("forecast bounds: %+v", x)
		}
	}
	if len(ChangePoints(p)) != 0 {
		t.Fatal("weekday seasonality reported as break")
	}
	for i := 50; i < len(p); i++ {
		p[i].Value *= 2
	}
	if len(ChangePoints(p)) == 0 {
		t.Fatal("sustained shift not detected")
	}
}
