package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// fakeCerts stands in for a certificate provider such as Cloudflare.
type fakeCerts struct {
	mu      sync.Mutex
	state   api.CertState
	asked   []string
	removed []string
}

func (f *fakeCerts) Ensure(_ context.Context, domain string) (api.CertState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, domain)
	return f.state, nil
}

func (f *fakeCerts) Remove(_ context.Context, domain string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, domain)
	return nil
}

func (f *fakeCerts) set(s api.CertState) {
	f.mu.Lock()
	f.state = s
	f.mu.Unlock()
}

func (f *fakeCerts) got() (asked, removed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked), slices.Clone(f.removed)
}

func TestTrackingDomainHTTPS(t *testing.T) {
	certs := &fakeCerts{state: api.CertState{Status: "pending"}}
	e := freshEnvWith(t, func(o *api.Options) {
		o.TrackingCerts, o.TrackingTarget, o.PublicURL = certs, "domains.outflare.test", "https://app.outflare.test"
	})
	events := track.Capture(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	links := func() string {
		u, err := tracking.BaseURL(ctx, e.pool, ws, "https://app.outflare.test")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	// Customers point their CNAME at the provider's target, and nothing is
	// asked of the provider until it does.
	td := owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.acme.example"})
	id := td["id"].(string)
	if td["target"] != "domains.outflare.test" || td["https_status"] != nil {
		t.Fatalf("added: %v", td)
	}
	e.runServerJobs(t)
	e.dns.setCNAME("links.acme.example", "domains.outflare.test")
	if td = owner.must(200, "POST", base+"/tracking-domains/"+id+"/check", nil); td["status"] != "verified" || td["https_status"] != "pending" {
		t.Fatalf("verified: %v", td)
	}
	// Verified but without a certificate, links keep the server's address.
	// The first look finds it pending and queues the next one. Wait for that
	// job to be committed, not just for the provider to be asked: the job
	// asks first and queues the next look after, in its own transaction.
	waitUntil(t, func() bool {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'tracking.domain_https' AND status = 'queued' AND workspace_id = $1 AND (payload ->> 'n')::int = 1`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	})
	if a, _ := certs.got(); len(a) != 1 || a[0] != "links.acme.example" {
		t.Fatalf("asked the provider for: %v", a)
	}
	if got := links(); got != "https://app.outflare.test" {
		t.Fatalf("links before the certificate: %s", got)
	}

	// Issued: the next look marks it active and links move to the domain.
	certs.set(api.CertState{Status: "active"})
	if tag, err := e.pool.Exec(ctx, `UPDATE jobs SET run_at = now() WHERE kind = 'tracking.domain_https' AND status = 'queued' AND workspace_id = $1`, ws); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("bring the next look forward: %v (%d rows)", err, tag.RowsAffected())
	}
	waitUntil(t, func() bool {
		d := owner.must(200, "GET", base+"/tracking-domains", nil)["data"].([]any)[0].(map[string]any)
		return d["https_status"] == "active"
	})
	if got := links(); got != "https://links.acme.example" {
		t.Fatalf("links after the certificate: %s", got)
	}
	if !slices.ContainsFunc(events(), func(ev track.Event) bool { return ev.Name == "tracking_domain.https_ready" && ev.WorkspaceID == ws }) {
		t.Fatal("no tracking_domain.https_ready event")
	}

	// Removing the domain drops its certificate.
	owner.must(204, "DELETE", base+"/tracking-domains/"+id, nil)
	waitUntil(t, func() bool { _, r := certs.got(); return slices.Contains(r, "links.acme.example") })
}

func TestTrackingDomainHTTPSFails(t *testing.T) {
	certs := &fakeCerts{state: api.CertState{Status: "failed", Error: "CAA records block the certificate"}}
	e := freshEnvWith(t, func(o *api.Options) { o.TrackingCerts = certs })
	events := track.Capture(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerJobs(t)
	e.dns.setCNAME("links.acme.example", "localhost")
	td := owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.acme.example"})
	waitUntil(t, func() bool {
		d := owner.must(200, "GET", base+"/tracking-domains", nil)["data"].([]any)[0].(map[string]any)
		return d["https_status"] == "failed" && strings.Contains(d["https_error"].(string), "CAA")
	})
	if !slices.ContainsFunc(events(), func(ev track.Event) bool { return ev.Name == "tracking_domain.https_failed" && ev.ErrorCode != "" }) {
		t.Fatal("no tracking_domain.https_failed event")
	}
	// Checking again asks again.
	certs.set(api.CertState{Status: "active"})
	owner.must(200, "POST", base+"/tracking-domains/"+td["id"].(string)+"/check", nil)
	waitUntil(t, func() bool {
		d := owner.must(200, "GET", base+"/tracking-domains", nil)["data"].([]any)[0].(map[string]any)
		return d["https_status"] == "active"
	})
}

func TestTrackingDomainsBehindAnEdge(t *testing.T) {
	const secret = "edge-test-not-a-real-secret"
	e := freshEnvWith(t, func(o *api.Options) { o.EdgeSecret = secret })
	owner, ws := setupOwner(t, e.h)
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/tracking-domains", map[string]string{"domain": "links.acme.example"})

	// The edge reaches the server on the server's own address.
	get := func(path, sent, host, ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "app-origin.example"
		if sent != "" {
			req.Header.Set("X-Outflare-Edge-Secret", sent)
			req.Header.Set("X-Outflare-Edge-Host", host)
			req.Header.Set("X-Outflare-Edge-IP", ip)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	// With the secret, the request is the tracking domain's: links only.
	if rec := get("/v1/meta", secret, "links.acme.example", "203.0.113.7"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "only serves email links") {
		t.Fatalf("app through the edge: %d %s", rec.Code, rec.Body)
	}
	if rec := get("/c/00000000-0000-0000-0000-000000000000", secret, "Links.Acme.Example", "2001:db8::7"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "only serves email links") {
		t.Fatalf("link through the edge: %d %s", rec.Code, rec.Body)
	}
	// It serves no domain that isn't a tracking domain.
	if rec := get("/c/00000000-0000-0000-0000-000000000000", secret, "app.acme.example", "203.0.113.7"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "only serves email links") {
		t.Fatalf("other domain through the edge: %d %s", rec.Code, rec.Body)
	}
	if rec := get("/c/x", secret, "links.acme.example", "not an address"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad address from the edge: %d", rec.Code)
	}
	// A wrong secret is ignored: the request is an ordinary one.
	if rec := get("/v1/meta", "edge-wrong-not-a-real-secret", "links.acme.example", "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("wrong secret: %d %s", rec.Code, rec.Body)
	}
}

func TestEdgeHeadersIgnoredWithoutASecret(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/tracking-domains", map[string]string{"domain": "links.acme.example"})
	req := httptest.NewRequest("GET", "/v1/meta", nil)
	req.Header.Set("X-Outflare-Edge-Secret", "")
	req.Header.Set("X-Outflare-Edge-Host", "links.acme.example")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no edge configured: %d", rec.Code)
	}
}
