package classify

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strings"
	"testing"
)

type stubDNS struct {
	mx    map[string][]*net.MX
	hosts map[string][]string
	fail  bool
	calls int
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (s *stubDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	s.calls++
	if s.fail {
		return nil, &net.DNSError{Err: "timeout", Name: name, IsTimeout: true}
	}
	if mx, ok := s.mx[name]; ok {
		return mx, nil
	}
	return nil, notFound(name)
}

func (s *stubDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	if h, ok := s.hosts[name]; ok {
		return h, nil
	}
	return nil, notFound(name)
}

func classify(t *testing.T, c *Classifier, in Input) Result {
	t.Helper()
	r, err := c.Classify(context.Background(), in)
	if err != nil {
		t.Fatalf("%+v: %v", in, err)
	}
	return r
}

func TestKnownProviders(t *testing.T) {
	c := &Classifier{}
	cases := []struct {
		in       string
		category string
		sub      string
		provider string
		action   string
	}{
		{"person@gmail.com", FreePersonal, "", "gmail", Allow},
		{"Person@GMAIL.com", FreePersonal, "", "gmail", Allow},
		{"me@proton.me", PrivateMailbox, "", "proton", Allow},
		{"me@pm.me", PrivateMailbox, "", "proton", Allow},
		{"x1y2@privaterelay.appleid.com", RelayAlias, PermanentAlias, "apple_hide_my_email", Allow},
		{"shop@me.anonaddy.com", RelayAlias, AnonymousAlias, "addy", Allow},
		{"a@mozmail.com", RelayAlias, PermanentAlias, "firefox_relay", Allow},
		{"x@mailinator.com", Disposable, "", "", Block},
		{"x@sub.mailinator.com", Disposable, "", "", Block},
		// Schools and governments are read from the domain's suffix.
		{"mit.edu", Education, "", "", Allow},
		{"ox.ac.uk", Education, "", "", Allow},
		{"ato.gov.au", Government, "", "", Allow},
		{"x@some-company-we-never-saw-com.example", Unknown, "", "", Verify},
	}
	for _, tc := range cases {
		in := Input{Email: tc.in}
		if !strings.Contains(tc.in, "@") {
			in = Input{Domain: tc.in}
		}
		r := classify(t, c, in)
		g := r.Global
		if g.Category != tc.category {
			t.Errorf("%s: category %s, want %s", tc.in, g.Category, tc.category)
			continue
		}
		if tc.sub != "" && (g.Subcategory == nil || *g.Subcategory != tc.sub) {
			t.Errorf("%s: subcategory %v, want %s", tc.in, g.Subcategory, tc.sub)
		}
		if tc.provider != "" && (g.Provider == nil || g.Provider.ID != tc.provider) {
			t.Errorf("%s: provider %+v, want %s", tc.in, g.Provider, tc.provider)
		}
		if r.Effective.Action != tc.action {
			t.Errorf("%s: action %s, want %s", tc.in, r.Effective.Action, tc.action)
		}
		if g.Confidence != nil {
			t.Errorf("%s: confidence must stay null until calibrated", tc.in)
		}
	}
}

func TestRelayIsNotDisposable(t *testing.T) {
	r := classify(t, &Classifier{}, Input{Domain: "simplelogin.com"})
	a := r.Global.Attributes
	if a.IsDisposable != TriFalse || a.IsForwarder != TriTrue || a.IsAliasService != TriTrue {
		t.Fatalf("relay attributes: %+v", a)
	}
	if r.Scope != "domain" || a.IsRoleAddress != nil {
		t.Fatalf("a domain lookup has no address attributes: %+v", r)
	}
}

func TestUnknownIsNotFalse(t *testing.T) {
	r := classify(t, &Classifier{}, Input{Domain: "acme-widgets-example-com.example"})
	a := r.Global.Attributes
	if a.IsDisposable != TriUnknown || a.IsFreeProvider != TriUnknown || a.HasMX != TriUnknown {
		t.Fatalf("absence of evidence must be unknown: %+v", a)
	}
	if !r.Analysis.Provisional || r.Global.ConfidenceStatus != InsufficientEvidence || r.Global.Risk.Status != "not_assessed" || r.Global.Risk.Score != nil {
		t.Fatalf("unknown domain: %+v", r)
	}
	b, _ := json.Marshal(a)
	if !strings.Contains(string(b), `"is_disposable":"unknown"`) {
		t.Fatalf("unknown should marshal as \"unknown\": %s", b)
	}
}

func TestBothListsCorroborate(t *testing.T) {
	in := Builtin()
	// Walk the domains in order so every run checks the same one. Skip
	// reviewed providers, which outrank the lists, and institution
	// suffixes (edu.pl), which are conflicted rather than corroborated.
	var domains []string
	for d, ls := range in.listed {
		sources := map[string]bool{}
		for _, l := range ls {
			sources[l.Source] = true
		}
		if len(sources) > 1 && in.providers[d] == nil {
			domains = append(domains, d)
		}
	}
	sort.Strings(domains)
	for _, d := range domains {
		r := classify(t, &Classifier{}, Input{Domain: d})
		if r.Global.ConfidenceStatus == Conflicted {
			continue
		}
		if r.Global.ConfidenceStatus != Corroborated || *r.Global.Risk.Score != 90 {
			t.Fatalf("%s: %+v", d, r.Global)
		}
		return
	}
	t.Skip("no domain on both lists")
}

func TestRoleAddress(t *testing.T) {
	r := classify(t, &Classifier{}, Input{Email: "Support+billing@acme-widgets-example-com.example"})
	if r.Global.Attributes.IsRoleAddress == nil || *r.Global.Attributes.IsRoleAddress != TriTrue {
		t.Fatal("support+billing@ is a role address")
	}
	if r.Global.Risk.Level != "medium" {
		t.Fatalf("risk %+v", r.Global.Risk)
	}
}

func TestDNS(t *testing.T) {
	dns := &stubDNS{
		mx: map[string][]*net.MX{
			"acme-widgets-example-com.example": {{Host: "aspmx.l.google.com.", Pref: 1}},
			"parked-example.com":               {{Host: ".", Pref: 0}},
		},
		hosts: map[string][]string{"a-record-only-example.com": {"192.0.2.1"}},
	}
	c := &Classifier{Resolver: dns}

	r := classify(t, c, Input{Email: "jo@acme-widgets-example-com.example"})
	a := r.Global.Attributes
	if a.HasMX != TriTrue || a.MXProviderID == nil || *a.MXProviderID != "google" || r.Global.LastCheckedAt == nil {
		t.Fatalf("mx: %+v", a)
	}
	if r.Global.Category != Unknown {
		t.Fatal("Google Workspace mail alone does not make a domain a business")
	}
	classify(t, c, Input{Email: "other@acme-widgets-example-com.example"})
	if dns.calls != 1 {
		t.Fatalf("the second lookup should come from the cache, got %d calls", dns.calls)
	}

	r = classify(t, c, Input{Domain: "parked-example.com"})
	if r.Effective.Action != Block || r.Effective.MatchedPolicyID != "default_no_mail" || r.Global.Attributes.HasMailReceivingPath != TriFalse {
		t.Fatalf("null MX: %+v", r)
	}
	r = classify(t, c, Input{Domain: "a-record-only-example.com"})
	if r.Global.Attributes.HasMX != TriFalse || r.Global.Attributes.HasMailReceivingPath != TriTrue {
		t.Fatalf("A fallback: %+v", r.Global.Attributes)
	}
	r = classify(t, c, Input{Domain: "nothing-here-example.com"})
	if r.Effective.Action != Block || r.Global.Risk.Signals[0] != "no_mail_servers" {
		t.Fatalf("no mail: %+v", r)
	}

	// A DNS failure proves nothing.
	r = classify(t, &Classifier{Resolver: &stubDNS{fail: true}}, Input{Domain: "slow-example.com"})
	if r.Global.Attributes.HasMX != TriUnknown || r.Effective.Action != Verify || r.Global.LastCheckedAt != nil {
		t.Fatalf("dns failure: %+v", r)
	}
	// Reviewed providers skip DNS.
	dns.calls = 0
	classify(t, c, Input{Domain: "gmail.com"})
	if dns.calls != 0 {
		t.Fatal("gmail.com should not need DNS")
	}
}

func TestParse(t *testing.T) {
	good := map[string]string{
		"a@Example.COM":          "example.com",
		" a@example.com. ":       "example.com",
		"a@bücher.example":       "xn--bcher-kva.example",
		`"john doe"@example.com`: "example.com",
	}
	for in, want := range good {
		a, err := ParseEmail(in)
		if err != nil || a.Domain.Name != want {
			t.Errorf("%q: %+v %v, want %s", in, a, err, want)
		}
	}
	if a, _ := ParseEmail("A.B+tag@example.com"); a.Local != "A.B+tag" {
		t.Errorf("the local part is kept as given, got %q", a.Local)
	}
	for _, in := range []string{"", "a", "@example.com", "a@", "a@localhost", "a@exa mple.com", "a@[192.0.2.1]",
		"a@example", "a@under_score.com", strings.Repeat("x", 65) + "@example.com", "a b@example.com"} {
		if _, err := ParseEmail(in); err == nil {
			t.Errorf("%q should not parse", in)
		}
	}
	if _, err := ParseDomain("co.uk"); !errors.Is(err, ErrPublicSuffix) {
		t.Errorf("co.uk: %v", err)
	}
	d, _ := ParseDomain("mail.bücher.example")
	if !d.IDN() || d.Registrable != "xn--bcher-kva.example" {
		t.Errorf("idn: %+v", d)
	}
}

func TestInput(t *testing.T) {
	c := &Classifier{}
	for _, in := range []Input{{}, {Email: "a@example.com", Domain: "example.com"}} {
		if _, err := c.Classify(context.Background(), in); !errors.Is(err, ErrInput) {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

// The open lists are refreshed by downloading them over data/. A refresh
// that lost more than a fifth of a list is more likely a broken source than
// a cleaned one; check it before raising these floors.
func TestListsDidNotShrink(t *testing.T) {
	in := Builtin()
	for source, last := range map[string]int{SourceDisposable: 9_900, SourceFakeFilter: 1_000, SourceOutflare: 110} {
		if n := in.Count(source); n < last*8/10 {
			t.Errorf("%s lists %d domains, more than 20%% below %d", source, n, last)
		}
	}
}
