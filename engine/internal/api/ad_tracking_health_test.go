package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/metrics"
)

// metricValue sums a counter's series whose labels include all of want.
func metricValue(t *testing.T, name string, want map[string]string) float64 {
	t.Helper()
	fams, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	total := 0.0
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
	series:
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			for k, v := range want {
				if got[k] != v {
					continue series
				}
			}
			total += m.GetCounter().GetValue()
		}
	}
	return total
}

func TestAdTrackingHealth(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	key := owner.must(201, "POST", "/v1/workspaces/"+ws+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)

	const arrivals, unfilled = "outflare_ad_tracking_arrivals_total", "outflare_ad_tracking_unfilled_macros_total"
	clickBefore := metricValue(t, arrivals, map[string]string{"network": "google", "outcome": "click_id"})
	adBefore := metricValue(t, arrivals, map[string]string{"network": "tiktok", "outcome": "ad_ids"})
	noneBefore := metricValue(t, arrivals, map[string]string{"network": "linkedin", "outcome": "none"})
	macroBefore := metricValue(t, unfilled, map[string]string{"network": "tiktok"})
	allBefore := metricValue(t, arrivals, nil)

	landing := func(id, source, medium string, props map[string]any) map[string]any {
		return map[string]any{"type": "page", "anonymousId": id, "properties": props, "context": map[string]any{
			"page":     map[string]any{"url": "https://northpeak.example/pricing", "path": "/pricing"},
			"campaign": map[string]any{"source": source, "medium": medium, "name": "spring"},
		}}
	}
	code, out := sendSite(t, e.h, "https://northpeak.example", key,
		landing("a-1", "google", "cpc", map[string]any{"click_ids": map[string]any{"gclid": "g-1"}}),
		// The template's ad ids, one of them a macro TikTok left unfilled.
		landing("a-2", "tiktok", "cpc", map[string]any{"ad": map[string]any{"campaign": "123", "ad": "__CID__"}}),
		landing("a-3", "LinkedIn", "paid_social", nil),
		// Not paid: organic social and a track event don't count.
		landing("a-4", "linkedin", "social", nil),
		map[string]any{"type": "track", "event": "Signup Started", "anonymousId": "a-1", "context": map[string]any{"campaign": map[string]any{"source": "google", "medium": "cpc"}}})
	if code != 200 || out["accepted"] != 5.0 {
		t.Fatalf("send: %d %v", code, out)
	}
	for name, got := range map[string]float64{
		"google click_id": metricValue(t, arrivals, map[string]string{"network": "google", "outcome": "click_id"}) - clickBefore,
		"tiktok ad_ids":   metricValue(t, arrivals, map[string]string{"network": "tiktok", "outcome": "ad_ids"}) - adBefore,
		"linkedin none":   metricValue(t, arrivals, map[string]string{"network": "linkedin", "outcome": "none"}) - noneBefore,
		"tiktok unfilled": metricValue(t, unfilled, map[string]string{"network": "tiktok"}) - macroBefore,
		"all arrivals":    metricValue(t, arrivals, nil) - allBefore - 2,
	} {
		if got != 1 {
			t.Errorf("%s: %v", name, got)
		}
	}
}
