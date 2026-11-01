package geoip

import (
	"net/netip"
	"testing"
)

func TestPackAndLookup(t *testing.T) {
	a := netip.MustParseAddr
	b, err := Pack("2026-10", []Range{
		{a("1.0.0.0"), a("1.0.0.255"), "AU"},
		{a("1.0.1.0"), a("1.0.3.255"), "CN"},
		{a("1.0.4.0"), a("1.0.7.255"), "CN"}, // joins the one before
		{a("2.0.0.0"), a("2.0.0.255"), "FR"}, // after a gap
		{a("255.255.255.0"), a("255.255.255.255"), "ZZ"},
		{a("2001:db8::"), a("2001:db8:0:0:ffff::"), "DE"}, // ends inside a /64
		{a("2001:db8:0:0:ffff::1"), a("2001:db8:0:1:ffff:ffff:ffff:ffff"), "NL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tb, err := unpack(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.v4) != 7 { // three gaps, AU, CN, FR, ZZ
		t.Errorf("v4 starts = %v", tb.v4)
	}
	for ip, want := range map[string]string{
		"0.255.255.255": "", "1.0.0.1": "AU", "1.0.2.9": "CN", "1.0.7.255": "CN", "1.0.8.0": "",
		"2.0.0.10": "FR", "2.0.1.0": "", "255.255.255.255": "ZZ", "::ffff:1.0.0.1": "AU",
		"2001:db8::5": "DE", "2001:db8:0:1::": "NL", "2001:db8:0:2::": "", "::1": "",
	} {
		if got := lookup(tb, a(ip)); got != want {
			t.Errorf("%s = %q, want %q", ip, got, want)
		}
	}
}

// TestShipped: the embedded file loads and knows a few well-known
// addresses' countries.
func TestShipped(t *testing.T) {
	if v := Version(); len(v) != 7 {
		t.Fatalf("version = %q", v)
	}
	for ip, want := range map[string]string{
		"8.8.8.8": "US", "2001:4860:1c00::1": "US", "10.0.0.1": "", "127.0.0.1": "",
	} {
		if got := Country(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s = %q, want %q", ip, got, want)
		}
	}
}
