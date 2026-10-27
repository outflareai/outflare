package analyticsync

import (
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

func TestHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Example.com/": "example.com", "sc-domain:example.com": "example.com", "example.com": "example.com",
		"http://shop.example.co.uk/path?q=1": "shop.example.co.uk", "prj_123": "", "42": "", "": "", "Site (Acme)": "", "example.com:8080": "example.com",
	} {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
	if got := HostOf(connectors.AnalyticsSite{ID: "9", Name: "Marketing site (Acme)", URL: "https://acme-io.example"}); got != "acme-io.example" {
		t.Errorf("HostOf: %q", got)
	}
}

func TestPlan(t *testing.T) {
	sites := []connectors.AnalyticsSite{
		{ID: "1", Name: "Shop", URL: "https://www.shop.com/"},
		{ID: "2", Name: "Blog"},
		{ID: "3", Name: "Docs", URL: "https://docs.example.com"},
	}
	cfg := Config{Site: "2", Domain: "blog.example.com", Sites: []SiteConfig{{ID: "3", Off: true}, {ID: "gone", Domain: "x.com"}}}
	plan := Plan(cfg, sites)
	want := []struct {
		host string
		sync bool
	}{{"shop.com", true}, {"blog.example.com", true}, {"docs.example.com", false}}
	if len(plan) != len(want) {
		t.Fatalf("plan: %+v", plan)
	}
	for i, w := range want {
		if plan[i].Host != w.host || plan[i].Sync != w.sync {
			t.Fatalf("site %s: %+v, want %+v", plan[i].ID, plan[i], w)
		}
	}

	// Past MaxSites, sites stay off until switched on.
	many := make([]connectors.AnalyticsSite, MaxSites+2)
	for i := range many {
		many[i] = connectors.AnalyticsSite{ID: fmt.Sprint(i)}
	}
	plan = Plan(Config{Sites: []SiteConfig{{ID: fmt.Sprint(MaxSites + 1)}}}, many)
	on := 0
	for _, p := range plan {
		if p.Sync {
			on++
		}
	}
	if on != MaxSites+1 || plan[MaxSites].Sync || !plan[MaxSites+1].Sync {
		t.Fatalf("cap: %d on", on)
	}

	// A tool that lists nothing still syncs the site someone named.
	if plan := Plan(Config{Site: "abc"}, nil); len(plan) != 1 || plan[0].ID != "abc" || !plan[0].Sync {
		t.Fatalf("named: %+v", plan)
	}
}
