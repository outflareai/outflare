package api_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/domainconnect"
	"github.com/outflareai/outflare/engine/internal/track"
)

// txtRecords is a resolver whose TXT answers a test changes.
type txtRecords struct {
	mu sync.Mutex
	m  map[string][]string
}

func (r *txtRecords) set(name string, v ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] = v
}

func (r *txtRecords) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.m[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// A site's proof approved once at its DNS provider (Domain Connect): the
// lookup says why not until the key is published, then gives a signed
// link where the provider has the template; coming back starts the checks.
func TestDNSApproval(t *testing.T) {
	got := track.Capture(t)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dc/v2/northpeak.example/settings":
			w.Write([]byte(`{"providerId":"cloudflare.com","providerName":"Cloudflare","urlSyncUX":"https://dash.example.net/dc","urlAPI":"https://` + r.Host + `/dc"}`))
		case "/dc/v2/domainTemplates/providers/outflare.ai/services/siteverify", "/dc/v2/domainTemplates/providers/outflare.ai/services/dmarc":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	txt := &txtRecords{m: map[string][]string{}}
	e := freshEnvWith(t, func(o *api.Options) {
		o.DomainConnect = &domainconnect.Client{HTTP: provider.Client(), Resolver: txt}
	})
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerAndMailJobs(t)

	anon := &client{t: t, h: e.h}
	key := anon.must(200, "GET", "/v1/domain-connect", nil)
	if key["ready"] != false || key["provider_id"] != "outflare.ai" {
		t.Fatalf("key: %v", key)
	}
	k, _ := key["key"].(map[string]any)
	if k == nil {
		t.Fatalf("no key: %v", key)
	}
	if k["host"] != "_dck1.outflare.ai" || len(k["txt"].([]any)) < 2 {
		t.Fatalf("key: %v", k)
	}

	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.northpeak.example/"})
	sitePath := base + "/seo-sites/" + site["id"].(string)
	approval := func(path string) map[string]any {
		return owner.must(200, "GET", path+"/dns/approval?return=/acme/audits/"+site["id"].(string), nil)
	}
	if a := approval(sitePath); a["available"] != false || a["reason"] != "not_configured" {
		t.Fatalf("before the key is published: %v", a)
	}
	var parts []string
	for _, p := range k["txt"].([]any) {
		parts = append(parts, p.(string))
	}
	txt.set("_dck1.outflare.ai", parts...)
	txt.set("_domainconnect.northpeak.example", strings.TrimPrefix(provider.URL, "https://")+"/dc")

	// The tracking template points at app.outflare.ai only, not this
	// test server.
	td := owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.northpeak.example"})
	if a := approval(base + "/tracking-domains/" + td["id"].(string)); a["reason"] != "no_template" {
		t.Fatalf("other target: %v", a)
	}

	a := approval(sitePath)
	if a["available"] != true || a["provider"] != "Cloudflare" {
		t.Fatalf("available: %v", a)
	}
	link, _ := url.Parse(a["url"].(string))
	q := link.Query()
	if link.Host != "dash.example.net" || !strings.HasSuffix(link.Path, "/providers/outflare.ai/services/siteverify/apply") ||
		q.Get("domain") != "northpeak.example" || q.Get("host") != "" || q.Get("token") == "" || q.Get("sig") == "" || q.Get("key") != "_dck1" ||
		q.Get("state") != "dc."+site["id"].(string) || !strings.HasSuffix(q.Get("redirect_uri"), "/acme/audits/"+site["id"].(string)) {
		t.Fatalf("link: %s", link)
	}

	// Cancelled at the provider: nothing changes.
	owner.must(202, "POST", sitePath+"/dns/approved", map[string]any{"error": "access_denied"})
	if s := owner.must(200, "GET", sitePath, nil); s["dns_install"] != nil {
		t.Fatalf("cancelled: %v", s["dns_install"])
	}
	if ev := named(got(), "seo_site.dns_approval_failed"); len(ev) != 1 || ev[0].ErrorCode != "dns_approval_access_denied" {
		t.Fatalf("failed event: %+v", ev)
	}

	owner.must(202, "POST", sitePath+"/dns/approved", map[string]any{"provider": "Cloudflare"})
	run := owner.must(200, "GET", sitePath, nil)["dns_install"].(map[string]any)
	if run["provider"] != "Cloudflare" || run["status"] != "done" || run["integration_id"] != nil {
		t.Fatalf("approved: %v", run)
	}
	if len(named(got(), "seo_site.dns_approved")) != 1 {
		t.Fatal("no seo_site.dns_approved event")
	}

	// A cold email domain missing only its DMARC record: the general
	// template adds it.
	e.dns.mx["northpeak.example"] = []*net.MX{{Host: "aspmx.l.google.com.", Pref: 1}}
	e.dns.txt["northpeak.example"] = []string{"v=spf1 include:_spf.google.com ~all"}
	dPath := base + "/sending-domains/northpeak.example"
	owner.must(201, "POST", base+"/sending-domains", map[string]any{"domain": "northpeak.example"})
	waitFor(t, owner, dPath, func(m map[string]any) bool { return m["checked_at"] != nil })
	a = owner.must(200, "GET", dPath+"/dns/approval?return=/acme/domains", nil)
	if a["available"] != true {
		t.Fatalf("sending domain: %v", a)
	}
	if link, _ := url.Parse(a["url"].(string)); !strings.HasSuffix(link.Path, "/services/dmarc/apply") || link.Query().Get("state") != "dc.northpeak.example" {
		t.Fatalf("sending domain link: %s", link)
	}
	owner.must(202, "POST", dPath+"/dns/approved", map[string]any{"provider": "Cloudflare"})
	if run := owner.must(200, "GET", dPath, nil)["dns_install"].(map[string]any); run["provider"] != "Cloudflare" {
		t.Fatalf("sending domain approved: %v", run)
	}

	owner.must(404, "GET", base+"/email-provider-domains/00000000-0000-4000-8000-000000000000/dns/approval", nil)
	owner.must(404, "POST", base+"/tracking-domains/00000000-0000-4000-8000-000000000000/dns/approved", map[string]any{})
}
