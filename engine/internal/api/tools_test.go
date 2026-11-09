package api_test

import (
	"net"
	"testing"
)

func TestPublicDomainCheck(t *testing.T) {
	e := freshEnv(t)
	anon := &client{t: t, h: e.h}

	// A domain on Google Workspace with SPF and DKIM but no DMARC, listed
	// on one blocklist.
	e.dns.setMX("acme.example", []*net.MX{{Host: "aspmx.l.google.com.", Pref: 1}})
	e.dns.setTXT("acme.example", []string{"v=spf1 include:_spf.google.com ~all"})
	e.dns.setTXT("google._domainkey.acme.example", []string{"v=DKIM1; k=rsa; p=MIIBIjAN"})
	e.dns.setHost("acme.example.dbl.spamhaus.org", []string{"127.0.1.2"})

	got := anon.must(200, "GET", "/v1/tools/domain-check?domain=https://ACME.example/", nil)
	if got["domain"] != "acme.example" || got["health"] != "critical" || got["mx_provider"] != "google" ||
		got["spf"] != "pass" || got["dkim"] != "pass" || got["dkim_selector"] != "google" || got["dmarc"] != "missing" {
		t.Fatalf("check: %v", got)
	}
	codes := map[string]bool{}
	for _, i := range got["issues"].([]any) {
		codes[i.(map[string]any)["code"].(string)] = true
	}
	if !codes["dmarc_missing"] || !codes["blocklisted"] || codes["no_mailboxes"] || len(codes) != 2 {
		t.Fatalf("issues: %v", got["issues"])
	}

	// Answers are cached: a change in DNS shows after the cache expires.
	e.dns.setTXT("_dmarc.acme.example", []string{"v=DMARC1; p=reject"})
	if again := anon.must(200, "GET", "/v1/tools/domain-check?domain=acme.example", nil); again["dmarc"] != "missing" {
		t.Fatalf("not cached: %v", again["dmarc"])
	}

	anon.must(400, "GET", "/v1/tools/domain-check?domain=not%20a%20domain", nil)
	anon.must(400, "GET", "/v1/tools/domain-check", nil)

	// Each network may make 20 checks a minute.
	for range 16 {
		anon.must(200, "GET", "/v1/tools/domain-check?domain=acme.example", nil)
	}
	anon.must(429, "GET", "/v1/tools/domain-check?domain=acme.example", nil)
}
