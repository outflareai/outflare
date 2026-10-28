package config

import "testing"

func TestSignedInCookieDomain(t *testing.T) {
	for _, tc := range []struct {
		domain, publicURL string
		ok                bool
	}{
		{"", "https://app.example.com", true},
		{"example.com", "https://app.example.com", true},
		{"app.example.com", "https://app.example.com", true},
		{"com", "https://app.example.com", false},               // a whole public suffix
		{"other-com.example", "https://app.example.com", false}, // not this server's domain
		{"ample.com", "https://app.example.com", false},         // a suffix, not a parent
		{"example.com/x", "https://app.example.com", false},     // not a domain
	} {
		if err := checkSignedInDomain(tc.domain, tc.publicURL); (err == nil) != tc.ok {
			t.Errorf("%q for %q: %v", tc.domain, tc.publicURL, err)
		}
	}
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("PUBLIC_URL", "https://app.example.com")
	t.Setenv("OUTFLARE_SIGNED_IN_COOKIE_DOMAIN", " .Example.com ")
	c, err := Load()
	if err != nil || c.SignedInCookieDomain != "example.com" {
		t.Fatalf("%q %v", c.SignedInCookieDomain, err)
	}
}

func TestMetricsAddr(t *testing.T) {
	for _, tc := range []struct {
		addr, httpAddr string
		ok             bool
	}{
		{"", ":8080", true},
		{":9091", ":8080", true},
		{"[::]:9091", ":8080", true},
		{"127.0.0.1:9091", "0.0.0.0:8080", true},
		{":8080", ":8080", false}, // the public port
		{"[::]:8080", "0.0.0.0:8080", false},
		{"9091", ":8080", false},       // no colon
		{"localhost:", ":8080", false}, // no port
	} {
		if err := checkMetricsAddr(tc.addr, tc.httpAddr); (err == nil) != tc.ok {
			t.Errorf("%q beside %q: %v", tc.addr, tc.httpAddr, err)
		}
	}
}

func TestEventsWorkspace(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("OUTFLARE_EVENTS_WORKSPACE", " 0F8E1D2C-3B4A-4596-8877-665544332211 ")
	c, err := Load()
	if err != nil || c.EventsWorkspace != "0f8e1d2c-3b4a-4596-8877-665544332211" {
		t.Fatalf("%q %v", c.EventsWorkspace, err)
	}
	t.Setenv("OUTFLARE_EVENTS_WORKSPACE", "outflare")
	if _, err := Load(); err == nil {
		t.Fatal("a workspace name was taken as an id")
	}
}

func TestRole(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	c, err := Load()
	if err != nil || c.Role != RoleAll || !c.Role.Works() {
		t.Fatalf("default: %q %v", c.Role, err)
	}
	t.Setenv("OUTFLARE_ROLE", " Ingest ")
	if c, err = Load(); err != nil || c.Role != RoleIngest || c.Role.Works() {
		t.Fatalf("ingest: %q %v", c.Role, err)
	}
	t.Setenv("OUTFLARE_ROLE", "worker")
	if c, err = Load(); err != nil || !c.Role.Works() {
		t.Fatalf("worker: %q %v", c.Role, err)
	}
	t.Setenv("OUTFLARE_ROLE", "web")
	if _, err := Load(); err == nil {
		t.Fatal("an unknown role was taken")
	}
}

func TestFollowURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("OUTFLARE_FOLLOW_URL", "")
	if c, err := Load(); err != nil || c.CommunityXURL != "https://x.com/outflareai" {
		t.Fatalf("default %q %v", c.CommunityXURL, err)
	}
	t.Setenv("OUTFLARE_FOLLOW_URL", "off")
	if c, err := Load(); err != nil || c.CommunityXURL != "" {
		t.Fatalf("off %q %v", c.CommunityXURL, err)
	}
	t.Setenv("OUTFLARE_FOLLOW_URL", "x.com/outflareai")
	if _, err := Load(); err == nil {
		t.Fatal("an address without https was taken")
	}
}
