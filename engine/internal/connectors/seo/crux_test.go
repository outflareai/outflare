package seo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestCrUXHistoryWithOwnKeyAndMissingPeriods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "test_own_key" {
			t.Error("missing own key")
		}
		if r.URL.Path != "/v1/records:queryHistoryRecord" {
			t.Error(r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["url"] != "https://example.com/pricing" || body["formFactor"] != "PHONE" {
			t.Error(body)
		}
		json.NewEncoder(w).Encode(map[string]any{"record": map[string]any{"collectionPeriods": []any{map[string]any{"lastDate": map[string]int{"year": 2026, "month": 10, "day": 1}}, map[string]any{"lastDate": map[string]int{"year": 2026, "month": 10, "day": 8}}}, "metrics": map[string]any{"cumulative_layout_shift": map[string]any{"percentilesTimeseries": map[string]any{"p75s": []any{"0.12", nil}}, "histogramTimeseries": []any{map[string]any{"densities": []any{.8, nil}}}}}}})
	}))
	defer srv.Close()
	rows, e := (CrUX{}).WebVitals(context.Background(), &c.Client{BaseURLs: map[string]string{"google_crux": srv.URL}}, c.Creds{"api_key": "test_own_key"}, "https://example.com/pricing", "PHONE")
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].P75 != .12 || rows[0].GoodShare != .8 || rows[0].Day != "2026-10-01" {
		t.Fatalf("history: %+v", rows)
	}
}
