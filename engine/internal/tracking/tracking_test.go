package tracking

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestValidDomain(t *testing.T) {
	for in, want := range map[string]string{"Links.Acme-com.example": "links.acme-com.example", "https://go.acme-io.example/x": "go.acme-io.example", "t.acme.co.uk.": "t.acme.co.uk"} {
		if got, ok := ValidDomain(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "acme", "a b.com", "-x.acme-com.example"} {
		if _, ok := ValidDomain(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestLinksAndBots(t *testing.T) {
	got := links.FindAllString("See https://acme-com.example/pricing. Also (https://acme-com.example/a?b=1), http://x-io.example!", -1)
	want := []string{"https://acme-com.example/pricing", "https://acme-com.example/a?b=1", "http://x-io.example"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %q", i, got[i])
		}
	}
	now := time.Now()
	if !LikelyBot("Mozilla/5.0 Safari", now.Add(-10*time.Second), now) || !LikelyBot("", now.Add(-time.Hour), now) || !LikelyBot("python-requests/2", now.Add(-time.Hour), now) {
		t.Error("bots not caught")
	}
	if LikelyBot("Mozilla/5.0 (Macintosh) Safari/605", now.Add(-time.Hour), now) {
		t.Error("person flagged")
	}
}

type cnames map[string]string

func (c cnames) LookupCNAME(_ context.Context, host string) (string, error) {
	if v, ok := c[host]; ok {
		return v, nil
	}
	return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestVerify(t *testing.T) {
	r := cnames{"links.acme.example": "domains.outflare.test.", "proxied.acme.example": "proxied.acme.example."}
	ctx := context.Background()
	if err := Verify(ctx, r, "links.acme.example", "domains.outflare.test"); err != nil {
		t.Fatal(err)
	}
	// Behind Cloudflare's proxy the CNAME is hidden: say how to fix it.
	if err := Verify(ctx, r, "proxied.acme.example", "domains.outflare.test"); err == nil || !strings.Contains(err.Error(), "DNS only") {
		t.Fatalf("proxied: %v", err)
	}
	if err := Verify(ctx, r, "missing.acme.example", "domains.outflare.test"); err == nil || !strings.Contains(err.Error(), "no DNS record") {
		t.Fatalf("missing: %v", err)
	}
}
