package visitors

import (
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/secrets"
)

func TestToken(t *testing.T) {
	box, err := secrets.NewBox("ZGV2LW9ubHktZGV2LW9ubHktZGV2LW9ubHktZGV2LTA=") // the dev-only key
	if err != nil {
		t.Fatal(err)
	}
	tok := Token(box, 12345)
	if id, ok := ParseToken(box, tok); !ok || id != 12345 {
		t.Fatalf("parse: %d %v", id, ok)
	}
	for _, bad := range []string{"", "abc", "9ix.", tok + "x", "9iy" + tok[3:], "-1." + strings.Split(tok, ".")[1]} {
		if _, ok := ParseToken(box, bad); ok {
			t.Errorf("%q passed", bad)
		}
	}
	if got := WithToken("https://acme.example/pricing?plan=pro#faq", tok); got != "https://acme.example/pricing?ofvisit="+tok+"&plan=pro#faq" {
		t.Errorf("with token: %s", got)
	}
}

func TestKeyPages(t *testing.T) {
	pats := []string{"/pricing*", "/demo", "/"}
	for path, want := range map[string]bool{
		"/pricing": true, "/pricing/teams": true, "/PRICING": true, "/demo": true, "/demo/": true,
		"/demos": false, "/blog/pricing": false, "/": true, "": false, "/about": false,
	} {
		if KeyPage(path, pats) != want {
			t.Errorf("%q: want %v", path, want)
		}
	}
	for in, want := range map[string]string{"/Pricing*": "/pricing*", "https://acme.example/demo": "/demo", "pricing": "", "/a*b": "", "/x?y": ""} {
		got, ok := CleanPattern(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
}

func TestScore(t *testing.T) {
	now := time.Now()
	if Score(Activity{}, now) != 0 {
		t.Error("no views")
	}
	one := Score(Activity{Pageviews: 1, Sessions: 1, LastSeen: now}, now)
	pricing := Score(Activity{Pageviews: 3, KeyPageviews: 2, Sessions: 2, LastSeen: now}, now)
	stale := Score(Activity{Pageviews: 3, KeyPageviews: 2, Sessions: 2, LastSeen: now.AddDate(0, 0, -20)}, now)
	team := Score(Activity{Pageviews: 3, KeyPageviews: 2, Sessions: 2, People: 3, LastSeen: now}, now)
	if !(one < pricing && stale < pricing && pricing < team) || one != 12 {
		t.Errorf("one %d pricing %d stale %d team %d", one, pricing, stale, team)
	}
	if max := Score(Activity{Pageviews: 999, KeyPageviews: 999, Sessions: 999, People: 999, LastSeen: now}, now); max != 100 {
		t.Errorf("max %d", max)
	}
}
