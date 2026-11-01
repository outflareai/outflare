package analytics

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestGA4LandingPageSourcesAndCurrency(t *testing.T) {
	row := func(path, source string, sessions, conversions, revenue string) map[string]any {
		dims := []any{}
		for _, v := range []string{"20261020", "www.example.com", path, source} {
			dims = append(dims, map[string]string{"value": v})
		}
		metrics := []any{}
		for _, v := range []string{sessions, sessions, conversions, revenue} {
			metrics = append(metrics, map[string]string{"value": v})
		}
		return map[string]any{"dimensionValues": dims, "metricValues": metrics}
	}
	cl, calls := fake(t, map[string]any{"POST /v1beta/properties/123:runReport": func(r *http.Request) any {
		if r.Header.Get("Authorization") != "Bearer own_test_token" {
			t.Error("wrong workspace credentials")
		}
		return map[string]any{"rowCount": 3, "metadata": map[string]any{"currencyCode": "EUR"}, "rows": []any{row("/pricing?utm_a=1", "google / organic", "10", "2", "12.50"), row("/pricing?utm_a=2", "google / organic", "5", "1", "2.25"), row("/pricing", "chatgpt.com / referral", "4", "1", "3.00")}}
	}})
	rows, e := (GA4{}).LandingOutcomes(ctx, cl, c.Creds{"access_token": "own_test_token"}, c.PageConversionQuery{Site: "123", From: "2026-10-20", To: "2026-10-20"})
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].Sessions != 19 || rows[0].SearchSessions != 15 || rows[0].Conversions != 4 || len(rows[0].Outcomes) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	x := rows[0].Outcomes[0]
	if x.RevenueMicros != 14750000 || x.Currency != "EUR" || x.Conversions != 3 {
		t.Fatalf("outcome: %+v", x)
	}
	if !strings.Contains((*calls)[0], "sessionSourceMedium") {
		t.Fatal(*calls)
	}
}
func TestSearchDetailsDimensionsAndTotals(t *testing.T) {
	cl, _ := fake(t, map[string]any{"POST /webmasters/v3/sites/sc-domain:example.com/searchAnalytics/query": func(r *http.Request) any {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["dimensions"]; !ok {
			return map[string]any{"rows": []any{map[string]any{"clicks": 100, "impressions": 1000}}}
		}
		return map[string]any{"rows": []any{map[string]any{"keys": []string{"https://example.com/a", "usa", "MOBILE"}, "clicks": 40, "impressions": 500, "position": 5}}}
	}})
	detail, e := (SearchConsole{}).SearchDetails(ctx, cl, c.Creds{"access_token": "test"}, "sc-domain:example.com", "2026-10-20", 50000)
	if e != nil {
		t.Fatal(e)
	}
	if detail.Clicks != 100 || len(detail.Segments) != 1 || detail.Segments[0].Country != "usa" || detail.Segments[0].Device != "MOBILE" {
		t.Fatal(detail)
	}
}
