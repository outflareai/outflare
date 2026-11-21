package api_test

import (
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The Performance report asks Search Console live, with every filter, and
// sets each row against the period compared with.
func TestSearchPerformance(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/", "brand_id": brand})["id"].(string)
	perf := "/v1/brands/" + brand + "/seo-sites/" + site + "/search/performance"

	// Nothing connected: an empty report that says what to connect.
	if p := owner.must(200, "GET", perf, nil); p["source"] != nil || len(p["rows"].([]any)) != 0 || len(p["notes"].([]any)) != 1 {
		t.Fatalf("no source: %v", p)
	}

	var mu sync.Mutex
	var bodies []string
	f := &fakeSearchConsole{perf: func(dims []string, start, body string) []any {
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if strings.Contains(body, `"hourly_all"`) {
			// Four hours, oldest first; the newest is 10:00 UTC.
			hours := []string{"2026-11-17T07:00:00Z", "2026-11-17T08:00:00Z", "2026-11-17T09:00:00Z", "2026-11-17T10:00:00Z"}
			out := []any{}
			for i, h := range hours {
				if len(dims) == 1 {
					out = append(out, map[string]any{"keys": []string{h}, "clicks": i + 1, "impressions": 10, "ctr": float64(i+1) / 10, "position": 2})
				} else {
					out = append(out, map[string]any{"keys": []string{"cheap proxies", h}, "clicks": i + 1, "impressions": 10, "ctr": float64(i+1) / 10, "position": float64(i + 1)})
				}
			}
			return out
		}
		now := start == "2026-10-01"
		row := func(key string, clicks, impressions int, position float64) any {
			keys := []any{}
			if key != "" {
				keys = append(keys, key)
			}
			return map[string]any{"keys": keys, "clicks": clicks, "impressions": impressions, "ctr": float64(clicks) / float64(impressions), "position": position}
		}
		switch {
		case len(dims) == 0 && now:
			return []any{row("", 100, 1000, 4.2)}
		case len(dims) == 0:
			return []any{row("", 80, 900, 5.1)}
		case dims[0] == "date" && now:
			return []any{row("2026-10-02", 60, 500, 4), row("2026-10-01", 40, 500, 4.4)}
		case dims[0] == "date":
			return []any{row("2026-09-03", 80, 900, 5.1)}
		case now:
			return []any{row("cheap proxies", 70, 600, 3), row("new query", 30, 400, 6)}
		default:
			return []any{row("cheap proxies", 50, 500, 4), row("lost query", 30, 400, 7)}
		}
	}}
	sc := connectSearchConsole(t, e, owner, base, f)
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)

	q := url.Values{"from": {"2026-10-01"}, "to": {"2026-10-28"}, "compare_from": {"2026-09-03"}, "compare_to": {"2026-09-30"},
		"type": {"image"}, "filter": {"query:excludingRegex:northpeak|acme", "country:equals:deu"}}
	p := owner.must(200, "GET", perf+"?"+q.Encode(), nil)
	if src := p["source"].(map[string]any); src["integration_id"] != sc || src["property"] != "sc-domain:example.com" {
		t.Fatalf("source: %v", src)
	}
	if p["totals"].(map[string]any)["clicks"].(float64) != 100 || p["previous_totals"].(map[string]any)["clicks"].(float64) != 80 {
		t.Fatalf("totals: %v %v", p["totals"], p["previous_totals"])
	}
	series := p["series"].([]any)
	if len(series) != 2 || series[0].(map[string]any)["date"] != "2026-10-01" || len(p["previous_series"].([]any)) != 1 {
		t.Fatalf("days, oldest first: %v", series)
	}
	rows := map[string]map[string]any{}
	order := []string{}
	for _, x := range p["rows"].([]any) {
		r := x.(map[string]any)
		rows[r["key"].(string)] = r
		order = append(order, r["key"].(string))
	}
	if !slices.Equal(order, []string{"cheap proxies", "new query", "lost query"}) {
		t.Fatalf("rows, most clicks first, then what was lost: %v", order)
	}
	if prev := rows["cheap proxies"]["previous"].(map[string]any); prev["clicks"].(float64) != 50 {
		t.Fatalf("cheap proxies before: %v", prev)
	}
	if rows["new query"]["previous"] != nil {
		t.Fatalf("a new query has nothing before: %v", rows["new query"])
	}
	if l := rows["lost query"]; l["clicks"].(float64) != 0 || l["previous"].(map[string]any)["clicks"].(float64) != 30 {
		t.Fatalf("a lost query: %v", l)
	}
	// Every call names the search type and carries every filter.
	if len(bodies) != 6 {
		t.Fatalf("totals, days and rows for two periods: %d calls", len(bodies))
	}
	for _, b := range bodies {
		for _, want := range []string{`"type":"image"`, `"expression":"northpeak|acme","operator":"excludingRegex"`, `"dimension":"country","expression":"deu","operator":"equals"`} {
			if !strings.Contains(b, want) {
				t.Fatalf("call lacks %s: %s", want, b)
			}
		}
	}

	// The same report again is kept for a while; fresh asks again.
	calls := f.perfCalls.Load()
	if again := owner.must(200, "GET", perf+"?"+q.Encode(), nil); again["cached"] != true || f.perfCalls.Load() != calls {
		t.Fatalf("kept: %v", again["cached"])
	}
	q.Set("fresh", "true")
	owner.must(200, "GET", perf+"?"+q.Encode(), nil)
	if f.perfCalls.Load() == calls {
		t.Fatal("fresh asks Google again")
	}

	// Days as rows, newest first, without asking twice.
	bodies = nil
	d := owner.must(200, "GET", perf+"?from=2026-10-01&to=2026-10-28&dimension=date", nil)
	if r := d["rows"].([]any); len(r) != 2 || r[0].(map[string]any)["key"] != "2026-10-02" || len(bodies) != 2 {
		t.Fatalf("dates: %v, %d calls", r, len(bodies))
	}

	// The last two hours, against the two before: added up from the hours.
	hr := owner.must(200, "GET", perf+"?hours=2&compare_previous=true", nil)
	if hr["totals"].(map[string]any)["clicks"].(float64) != 7 || hr["previous_totals"].(map[string]any)["clicks"].(float64) != 3 {
		t.Fatalf("hours: %v %v", hr["totals"], hr["previous_totals"])
	}
	if s := hr["series"].([]any); len(s) != 2 || s[1].(map[string]any)["date"] != "2026-11-17T10:00:00Z" {
		t.Fatalf("hour points: %v", s)
	}
	row := hr["rows"].([]any)[0].(map[string]any)
	if row["key"] != "cheap proxies" || row["clicks"].(float64) != 7 || row["position"].(float64) != 3.5 || row["previous"].(map[string]any)["clicks"].(float64) != 3 {
		t.Fatalf("hour rows: %v", row)
	}
	if !strings.Contains(bodies[len(bodies)-1], `"hourly_all"`) {
		t.Fatal("hours ask for hourly data")
	}
	owner.must(400, "GET", perf+"?hours=0", nil)
	owner.must(400, "GET", perf+"?hours=100", nil)

	for _, bad := range []string{"type=maps", "dimension=keyword", "filter=query:like:x", "filter=nope:equals:x", "filter=query",
		"from=2026-10-10&to=2026-10-01", "from=2025-01-01&to=2026-10-01", "compare_from=2026-09-01",
		"type=discover", "type=discover&dimension=page&filter=query:contains:x"} {
		owner.must(400, "GET", perf+"?"+bad, nil)
	}
	owner.must(200, "GET", perf+"?type=discover&dimension=page&from=2026-10-01&to=2026-10-28", nil)
}
