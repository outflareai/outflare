package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostHogProxy(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	if newPostHogProxy("", upstream.URL) != nil {
		t.Fatal("no key should mean no proxy")
	}
	p := newPostHogProxy("phc_test", upstream.URL)
	req := httptest.NewRequest("POST", "/ingest/e/?ip=1", nil)
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("Authorization", "Bearer oft_secret")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status %d, upstream reached: %v", rec.Code, got != nil)
	}
	if got.URL.Path != "/e/" || got.URL.RawQuery != "ip=1" {
		t.Errorf("forwarded to %s?%s, want /e/?ip=1", got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Cookie") != "" || got.Header.Get("Authorization") != "" {
		t.Error("session cookie or token reached PostHog")
	}
	if m := p.meta(); m["api_host"] != "/ingest" || m["key"] != "phc_test" {
		t.Errorf("meta = %v", m)
	}
}

func TestPostHogProxyCloudHosts(t *testing.T) {
	p := newPostHogProxy("phc_test", "https://eu.i.posthog.com")
	if p.assets.Host != "eu-assets.i.posthog.com" || p.ui != "https://eu.posthog.com" {
		t.Errorf("assets %s, ui %s", p.assets.Host, p.ui)
	}
}
