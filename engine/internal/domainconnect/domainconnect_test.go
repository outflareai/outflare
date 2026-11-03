package domainconnect

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite templates/")

// The files in templates/ are what gets submitted to the public template
// repository; they must match the templates the server applies.
func TestTemplateFiles(t *testing.T) {
	seen := map[string]bool{}
	for _, tpl := range Templates {
		path := filepath.Join("templates", tpl.File())
		seen[tpl.File()] = true
		if *update {
			if err := os.WriteFile(path, tpl.JSON(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(tpl.JSON()) {
			t.Errorf("%s is out of date: go test ./engine/internal/domainconnect/ -run TestTemplateFiles -update", path)
		}
	}
	files, _ := os.ReadDir("templates")
	for _, f := range files {
		if !seen[f.Name()] {
			t.Errorf("templates/%s matches no template", f.Name())
		}
	}
}

func TestPlanFor(t *testing.T) {
	ses := []Rec{
		{Type: "CNAME", Name: "aaa._domainkey.mail.example.com", Value: "aaa.dkim.amazonses.com"},
		{Type: "CNAME", Name: "bbb._domainkey.mail.example.com", Value: "bbb.dkim.amazonses.com"},
		{Type: "CNAME", Name: "ccc._domainkey.mail.example.com", Value: "ccc.dkim.amazonses.com"},
		{Type: "MX", Name: "mail.mail.example.com", Value: "feedback-smtp.eu-west-1.amazonses.com", Priority: 10},
		{Type: "TXT", Name: "mail.mail.example.com", Value: "v=spf1 include:amazonses.com ~all"},
		{Type: "TXT", Name: "_dmarc.mail.example.com", Value: "v=DMARC1; p=none;"},
	}
	cases := []struct {
		name, kind, fqdn string
		all, want        []Rec
		service          string
		groups           []string
		vars             map[string]string
	}{
		{"ses all", "email", "mail.example.com", ses, ses, "ses", []string{"dkim", "mailfrom", "dmarc"},
			map[string]string{"dkim1": "aaa", "dkim2": "bbb", "dkim3": "ccc", "dkimzone": "dkim.amazonses.com", "region": "eu-west-1"}},
		{"ses mail from only", "email", "mail.example.com", ses, ses[3:5], "ses", []string{"mailfrom"},
			map[string]string{"region": "eu-west-1"}},
		{"ses opt-in region", "email", "example.com", []Rec{
			{Type: "CNAME", Name: "aaa._domainkey.example.com", Value: "aaa.dkim.af-south-1.amazonses.com"},
			{Type: "CNAME", Name: "bbb._domainkey.example.com", Value: "bbb.dkim.af-south-1.amazonses.com"},
			{Type: "CNAME", Name: "ccc._domainkey.example.com", Value: "ccc.dkim.af-south-1.amazonses.com"},
		}, nil, "ses", []string{"dkim"}, map[string]string{"dkim1": "aaa", "dkim2": "bbb", "dkim3": "ccc", "dkimzone": "dkim.af-south-1.amazonses.com"}},
		{"resend", "email", "example.com", []Rec{
			{Type: "TXT", Name: "resend._domainkey.example.com", Value: "p=MIGfMA0GCSq"},
			{Type: "MX", Name: "send.example.com", Value: "feedback-smtp.us-east-1.amazonses.com", Priority: 10},
			{Type: "TXT", Name: "send.example.com", Value: "v=spf1 include:amazonses.com ~all"},
		}, nil, "resend", []string{"dkim", "mailfrom"}, map[string]string{"dkim": "p=MIGfMA0GCSq", "region": "us-east-1"}},
		{"postmark", "email", "example.com", []Rec{
			{Type: "TXT", Name: "20240101pm._domainkey.example.com", Value: "k=rsa;p=MIGf"},
			{Type: "CNAME", Name: "pm-bounces.example.com", Value: "pm.mtasv.net"},
		}, nil, "postmark", []string{"dkim", "returnpath"}, map[string]string{"selector": "20240101pm", "dkim": "k=rsa;p=MIGf"}},
		{"google workspace", "mailbox", "example.com", []Rec{
			{Type: "MX", Name: "example.com", Value: "smtp.google.com", Priority: 1},
			{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.google.com ~all"},
			{Type: "TXT", Name: "google._domainkey.example.com", Value: "v=DKIM1; k=rsa; p=MIIBIjAN"},
		}, nil, "googleworkspace", []string{"mx", "spf", "dkim"}, map[string]string{"dkimkey": "MIIBIjAN"}},
		{"microsoft 365", "mailbox", "example.com", []Rec{
			{Type: "MX", Name: "example.com", Value: "example-com.mail.protection.outlook.com", Priority: 0},
			{Type: "TXT", Name: "example.com", Value: "v=spf1 include:spf.protection.outlook.com -all"},
			{Type: "CNAME", Name: "selector1._domainkey.example.com", Value: "selector1-example-com._domainkey.northpeak.onmicrosoft.example"},
			{Type: "CNAME", Name: "selector2._domainkey.example.com", Value: "selector2-example-com._domainkey.northpeak.onmicrosoft.example"},
		}, nil, "microsoft365", []string{"mx", "spf", "dkim"}, map[string]string{"mx": "example-com.mail.protection.outlook.com",
			"dkim1": "selector1-example-com._domainkey.northpeak.onmicrosoft.example", "dkim2": "selector2-example-com._domainkey.northpeak.onmicrosoft.example"}},
		{"dmarc", "any", "example.com", []Rec{{Type: "TXT", Name: "_dmarc.example.com", Value: "v=DMARC1; p=none"}}, nil,
			"dmarc", nil, map[string]string{"policy": "p=none"}},
		{"spf among others", "any", "example.com", []Rec{{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.google.com include:zoho.example ~all"}}, nil,
			"spf", nil, map[string]string{"include": "_spf.google.com"}},
		{"service cname", "any", "track.example.com", []Rec{{Type: "CNAME", Name: "track.example.com", Value: "prox.tracking.example.net"}}, nil,
			"servicecname", nil, map[string]string{"target": "prox.tracking.example.net"}},
		{"site", "site", "example.com", []Rec{{Type: "TXT", Name: "example.com", Value: "outflare-site-verification=tok123"}}, nil,
			"siteverify", nil, map[string]string{"token": "tok123"}},
		{"tracking", "tracking", "links.example.com", []Rec{{Type: "CNAME", Name: "links.example.com", Value: "app.outflare.ai"}}, nil,
			"trackingdomain", nil, map[string]string{}},
	}
	for _, c := range cases {
		want := c.want
		if want == nil {
			want = c.all
		}
		p, ok := PlanFor(c.kind, c.fqdn, c.all, want)
		if !ok {
			t.Errorf("%s: no plan", c.name)
			continue
		}
		if p.Template.ServiceID != c.service || !reflect.DeepEqual(p.Groups, c.groups) || !reflect.DeepEqual(p.Vars, c.vars) {
			t.Errorf("%s: got %s %v %v", c.name, p.Template.ServiceID, p.Groups, p.Vars)
		}
	}
	for _, c := range []struct {
		name, kind, fqdn string
		recs             []Rec
	}{
		{"unknown provider", "email", "example.com", []Rec{{Type: "CNAME", Name: "x._domainkey.example.com", Value: "x.dkim.mailgun.org"}}},
		{"tracking elsewhere", "tracking", "links.example.com", []Rec{{Type: "CNAME", Name: "links.example.com", Value: "evil.example.net"}}},
		{"outside the domain", "site", "example.com", []Rec{{Type: "TXT", Name: "example.net", Value: "outflare-site-verification=t"}}},
		{"ses custom mail from", "email", "example.com", []Rec{{Type: "MX", Name: "bounce.example.com", Value: "feedback-smtp.us-east-1.amazonses.com", Priority: 10}}},
		{"MX priority", "email", "example.com", []Rec{{Type: "MX", Name: "send.example.com", Value: "feedback-smtp.us-east-1.amazonses.com", Priority: 20}}},
	} {
		if p, ok := PlanFor(c.kind, c.fqdn, c.recs, c.recs); ok {
			t.Errorf("%s: unexpected plan %+v", c.name, p)
		}
	}
}

type txts map[string][]string

func (m txts) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := m[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// LookupMX answers with the hosts set at "mx:" plus the name.
func (m txts) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	var out []*net.MX
	for _, h := range m["mx:"+name] {
		out = append(out, &net.MX{Host: h + ".", Pref: 10})
	}
	if out == nil {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return out, nil
}

// misbehaving answers like a hosting resolver that fails a missing name
// with SERVFAIL instead of "no such name", for the names in fail.
type misbehaving struct {
	txts
	fail map[string]bool
}

func (m misbehaving) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if m.fail[name] {
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	return m.txts.LookupTXT(ctx, name)
}

// A resolver failing a level below the zone doesn't stop discovery: the
// zone's record still names the provider. With no record anywhere, the
// failure is the answer rather than "no provider".
func TestDiscoverPastFailedLevel(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"providerId":"cloudflare.com","providerName":"Cloudflare","urlSyncUX":"https://dash.example.net/domainconnect","urlAPI":"https://` + r.Host + `/dc"}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	res := misbehaving{
		txts: txts{"_domainconnect.example.com": {strings.TrimPrefix(srv.URL, "https://") + "/dc"}},
		fail: map[string]bool{"_domainconnect.click.example.com": true, "_domainconnect.click.example.org": true},
	}
	c := &Client{HTTP: srv.Client(), Resolver: res}
	z, err := c.Discover(ctx, "click.example.com")
	if err != nil || z.Domain != "example.com" || z.Host != "click" || z.Settings.Name() != "Cloudflare" {
		t.Fatalf("discover: %+v %v", z, err)
	}
	if _, err := c.Discover(ctx, "click.example.org"); ReasonOf(err) != ReasonLookupFailed {
		t.Fatalf("no record anywhere: %v", err)
	}
	if _, err := c.Discover(ctx, "www.example.org"); ReasonOf(err) != ReasonNoProvider {
		t.Fatalf("plain not found: %v", err)
	}
}

// Records Cloudflare would add beside the ones already there, instead of
// replacing them, are left to a DNS host connection.
func TestConflicts(t *testing.T) {
	res := txts{
		"google._domainkey.example.com": {"v=DKIM1; k=rsa; p=OLD"},
		"_dmarc.example.com":            {"v=DMARC1; p=quarantine"},
		"_dmarc.example.org":            {"some-other-verification=1"},
		"mx:example.com":                {"mx.example.net"},
	}
	c := &Client{Resolver: res}
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		rec    Rec
		reason string
	}{
		{"second DKIM key", Rec{Type: "TXT", Name: "google._domainkey.example.com", Value: "v=DKIM1; k=rsa; p=NEW"}, ReasonConflict},
		{"second DMARC policy", Rec{Type: "TXT", Name: "_dmarc.example.com", Value: "v=DMARC1; p=none;"}, ReasonConflict},
		{"MX over the domain's own", Rec{Type: "MX", Name: "example.com", Value: "smtp.google.com", Priority: 1}, ReasonConflict},
		{"new DKIM selector", Rec{Type: "TXT", Name: "resend._domainkey.example.com", Value: "p=KEY"}, ""},
		{"no DMARC policy yet", Rec{Type: "TXT", Name: "_dmarc.example.org", Value: "v=DMARC1; p=none;"}, ""},
		{"no MX yet", Rec{Type: "MX", Name: "example.org", Value: "smtp.google.com", Priority: 1}, ""},
		{"SPF merges", Rec{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.google.com ~all"}, ""},
	} {
		err := c.Conflicts(ctx, []Rec{tc.rec})
		if got := ""; err != nil {
			got = ReasonOf(err)
			if got != tc.reason {
				t.Errorf("%s: %q", tc.name, got)
			}
		} else if tc.reason != "" {
			t.Errorf("%s: no conflict", tc.name)
		}
	}
}

func TestDiscoverAndApply(t *testing.T) {
	var supported bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dc/v2/example.com/settings":
			w.Write([]byte(`{"providerId":"cloudflare.com","providerName":"Cloudflare","urlSyncUX":"https://dash.example.net/domainconnect","urlAPI":"https://` + r.Host + `/dc"}`))
		case "/dc/v2/domainTemplates/providers/outflare.ai/services/siteverify":
			if supported {
				w.WriteHeader(http.StatusOK)
			} else {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	key := NewKey(KeyID, priv)
	res := txts{"_domainconnect.example.com": {strings.TrimPrefix(srv.URL, "https://") + "/dc"}}
	c := &Client{HTTP: srv.Client(), Resolver: res, Key: key}
	ctx := context.Background()

	if c.Ready(ctx) {
		t.Fatal("ready before the key is published")
	}
	z, err := c.Discover(ctx, "www.shop.example.com")
	if err != nil || z.Domain != "example.com" || z.Host != "www.shop" || z.Settings.Name() != "Cloudflare" {
		t.Fatalf("discover: %+v %v", z, err)
	}
	if _, err := c.Discover(ctx, "example.org"); ReasonOf(err) != ReasonNoProvider {
		t.Fatalf("no provider: %v", err)
	}
	if err := c.Supports(ctx, z.Settings, "siteverify"); ReasonOf(err) != ReasonNotOnboarded {
		t.Fatalf("not onboarded: %v", err)
	}
	supported = true
	c.cache = nil
	if err := c.Supports(ctx, z.Settings, "siteverify"); err != nil {
		t.Fatalf("supported: %v", err)
	}

	p, _ := PlanFor("site", "www.shop.example.com", []Rec{{Type: "TXT", Name: "www.shop.example.com", Value: "outflare-site-verification=tok"}}, nil)
	link, err := c.ApplyURL(z, p, "https://app.outflare.ai/acme/audits/1", "dc.1")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	if u.Host != "dash.example.net" || u.Path != "/domainconnect/v2/domainTemplates/providers/outflare.ai/services/siteverify/apply" {
		t.Fatalf("link: %s", link)
	}
	q := u.Query()
	if q.Get("domain") != "example.com" || q.Get("host") != "www.shop" || q.Get("token") != "tok" || q.Get("key") != KeyID || q.Get("state") != "dc.1" {
		t.Fatalf("query: %v", q)
	}
	// A provider checks the signature over everything before sig, with
	// the key put back together from DNS.
	signed := u.RawQuery[:strings.Index(u.RawQuery, "&sig=")]
	der, _ := base64.StdEncoding.DecodeString(PublicKeyFrom(key.TXT()))
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.StdEncoding.DecodeString(q.Get("sig"))
	sum := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}

	// A subdomain CNAME never goes out without host=: Cloudflare would put
	// it on the apex, over the site.
	track := []Rec{{Type: "CNAME", Name: "example.com", Value: "app.outflare.ai"}}
	p, ok := PlanFor("tracking", "example.com", track, track)
	if !ok {
		t.Fatal("no tracking plan")
	}
	if _, err := c.ApplyURL(Zone{Domain: "example.com", Settings: z.Settings}, p, "https://app.outflare.ai/", "dc.2"); ReasonOf(err) != ReasonNoTemplate {
		t.Fatalf("apex CNAME: %v", err)
	}
	if link, err := c.ApplyURL(Zone{Domain: "example.com", Host: "links", Settings: z.Settings}, p, "https://app.outflare.ai/", "dc.2"); err != nil || !strings.Contains(link, "host=links") {
		t.Fatalf("subdomain CNAME: %s %v", link, err)
	}

	res[KeyID+"."+PubKeyDomain] = key.TXT()
	c.cache = nil
	if !c.Ready(ctx) {
		t.Fatal("not ready once the key is published")
	}
}
