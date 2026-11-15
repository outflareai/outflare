package api_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestScriptPolicyOnTheWebApp(t *testing.T) {
	dir := t.TempDir()
	page := `<!doctype html><html><head><script>try { x = 1 } catch {}</script></head><body><script type="module" src="/assets/app.js"></script></body></html>`
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	e := freshEnvWith(t, func(o *api.Options) { o.WebDir = dir })
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/acme/overview", nil))
	got := rec.Header().Get("Content-Security-Policy-Report-Only")
	// sha256 of the inline script above, as browsers compute it.
	for _, want := range []string{"script-src 'self' 'sha256-", "object-src 'none'", "base-uri 'self'", "report-uri /v1/csp-reports"} {
		if !strings.Contains(got, want) {
			t.Errorf("policy %q lacks %q", got, want)
		}
	}
	if n := strings.Count(got, "'sha256-"); n != 1 {
		t.Errorf("%d hashes, want 1: the script with a src has none", n)
	}
	// Report-only: the enforced header stays as it was.
	if enforced := rec.Header().Get("Content-Security-Policy"); enforced != "frame-ancestors 'none'" {
		t.Errorf("enforced policy changed: %q", enforced)
	}
	// API answers carry no script policy.
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/meta", nil))
	if got := rec.Header().Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Errorf("/v1/meta has a script policy: %q", got)
	}
}

func TestScriptPolicyReports(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	post := func(ctype, body string) int {
		req := httptest.NewRequest("POST", "/v1/csp-reports", strings.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("application/csp-report", `{"csp-report":{"effective-directive":"script-src-elem","blocked-uri":"https://cdn.evil.example/x.js?token=secret","document-uri":"https://app.example.com/acme/people"}}`); code != 204 {
		t.Fatalf("report-uri shape: %d", code)
	}
	if code := post("application/reports+json", `[{"type":"csp-violation","body":{"effectiveDirective":"script-src","blockedURL":"inline"}},{"type":"deprecation","body":{}},{"type":"csp-violation","body":{"effectiveDirective":"script-src-elem","blockedURL":"chrome-extension://abc/inject.js"}}]`); code != 204 {
		t.Fatalf("Reporting API shape: %d", code)
	}
	if code := post("application/json", `{"hello":"world"}`); code != 400 {
		t.Errorf("not a report: %d, want 400", code)
	}
	var got []string
	for _, ev := range events() {
		if ev.Name != "ui.script_policy_violated" {
			continue
		}
		if ev.Outcome != track.Failure || ev.ErrorCode != "script_policy" {
			t.Errorf("event outcome %q code %q", ev.Outcome, ev.ErrorCode)
		}
		for k, v := range ev.Props {
			if s, ok := v.(string); ok && (strings.Contains(s, "evil") || strings.Contains(s, "secret")) {
				t.Errorf("event property %s carries the script's address: %q", k, s)
			}
		}
		got = append(got, ev.Props["directive"].(string)+" "+ev.Props["source"].(string))
	}
	// The extension's script is counted in metrics only.
	if want := "script-src-elem external,script-src inline"; strings.Join(got, ",") != want {
		t.Errorf("events %q, want %q", strings.Join(got, ","), want)
	}
}
