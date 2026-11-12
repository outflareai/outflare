package fraud

import (
	"strings"
	"testing"
	"time"
)

// The embedded list loads, has the main providers and none of the relay
// networks people browse through.
func TestHostingList(t *testing.T) {
	if err := HostingErr(); err != nil {
		t.Fatal(err)
	}
	for _, asn := range []uint32{16509, 14061, 24940, 16276} {
		if _, ok := Hosting(asn); !ok {
			t.Errorf("AS%d isn't on the hosting list", asn)
		}
	}
	for asn := range relays {
		if _, ok := Hosting(asn); ok {
			t.Errorf("AS%d, a relay network, is on the hosting list", asn)
		}
	}
	if _, ok := Hosting(0); ok {
		t.Error("an unknown network counts as hosting")
	}
}

func TestLoadHostingRefusesBadLines(t *testing.T) {
	good := "# comment\n\n64500\tAcme Cloud\thttps://bgp.example/AS64500\n"
	if m, err := LoadHosting(good); err != nil || m[64500].Name != "Acme Cloud" {
		t.Fatalf("good list: %v, %v", m, err)
	}
	for name, c := range map[string]struct{ text, want string }{
		"fields": {"64500 Acme Cloud https://bgp.example\n", "tab separated"},
		"asn":    {"AS64500\tAcme\thttps://bgp.example\n", "isn't an ASN"},
		"relay":  {"13335\tCloudflare\thttps://bgp.example\n", "privacy relays"},
		"twice":  {good + "64500\tAcme again\thttps://bgp.example\n", "listed twice"},
		"source": {"64500\tAcme\thttp://bgp.example\n", "https source"},
	} {
		if _, err := LoadHosting(c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestRepeatClicks(t *testing.T) {
	var r RepeatCounter
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		if n := r.Count("link-1", "hash-a", "ua-1", now.Add(time.Duration(i)*time.Minute)); n != i {
			t.Fatalf("click %d counted %d", i, n)
		}
	}
	if n := r.Count("link-2", "hash-a", "ua-1", now); n != 1 {
		t.Errorf("another link: %d", n)
	}
	if n := r.Count("link-1", "hash-a", "ua-2", now); n != 1 {
		t.Errorf("another browser on the same address: %d", n)
	}
	if n := r.Count("link-1", "", "ua-1", now); n != 0 {
		t.Errorf("no address: %d", n)
	}
	// After the window the count starts again.
	if n := r.Count("link-1", "hash-a", "ua-1", now.Add(2*time.Hour)); n != 1 {
		t.Errorf("after the window: %d", n)
	}
	if got := ClickReasons(0, RepeatClickLimit); len(got) != 0 {
		t.Errorf("at the limit: %v", got)
	}
	if got := ClickReasons(16509, RepeatClickLimit+1); len(got) != 2 || got[0] != DatacenterNetwork || got[1] != RepeatClick {
		t.Errorf("hosting and repeated: %v", got)
	}
}

func TestConversionReasons(t *testing.T) {
	clicked := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if got := ConversionReasons(nil, clicked, clicked.Add(3*time.Second)); len(got) != 1 || got[0] != FastConversion {
		t.Errorf("fast: %v", got)
	}
	if got := ConversionReasons([]string{RepeatClick}, clicked, clicked.Add(time.Hour)); len(got) != 1 || got[0] != RepeatClick {
		t.Errorf("slow, from a repeat click: %v", got)
	}
	if got := ConversionReasons(nil, clicked, clicked.Add(-time.Minute)); len(got) != 0 {
		t.Errorf("converted before the click (an upload's own time): %v", got)
	}
}
