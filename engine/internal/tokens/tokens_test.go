package tokens

import (
	"errors"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	v := Values{"click_id": "abc123", "field1": "a b&c=d", "payout": "12.50", "email": "ana@acme.example", "city": "Lyon"}
	for _, tc := range []struct{ tmpl, want string }{
		{"https://t.example/pb?cid={click_id}&p={payout}", "https://t.example/pb?cid=abc123&p=12.50"},
		{"https://t.example/?s={field1}", "https://t.example/?s=a+b%26c%3Dd"},
		{"https://t.example/{field1|raw}", "https://t.example/a b&c=d"},
		{"?p={payout|50%}", "?p=6.25"},
		{"?p={payout|150%}", "?p=18.75"},
		{"?p={payout|12.5%}", "?p=1.5625"},
		{"?h={email|hash}", "?h=bb03ba1becc024164cf08e371385b735f89fb22eb310fef60c67c3997c22dcf6"},
		{"?missing={field2}&c={city}", "?missing=&c=Lyon"},
		{"?not={click_id|50%}", "?not=abc123"},
		{"no tokens", "no tokens"},
	} {
		if got := Expand(tc.tmpl, v); got != tc.want {
			t.Errorf("Expand(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

func TestExpandKnown(t *testing.T) {
	v := Values{"click_id": "abc123", "field1": "x y"}
	known := []string{"click_id", "field1"}
	for _, tc := range []struct{ tmpl, want string }{
		{"https://shop.example/?c={click_id}&keep={their_macro}", "https://shop.example/?c=abc123&keep={their_macro}"},
		{"https://shop.example/go/%7Bfield1%7D/%7bclick_id%7d?x=%7Bother%7D", "https://shop.example/go/x+y/abc123?x=%7Bother%7D"},
		{"https://shop.example/{field1|raw}", "https://shop.example/x y"},
	} {
		if got := ExpandKnown(tc.tmpl, v, known); got != tc.want {
			t.Errorf("ExpandKnown(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	known := []string{"click_id", "payout"}
	for _, tc := range []struct {
		tmpl string
		ok   bool
	}{
		{"https://t.example/pb?cid={click_id}&p={payout|50%}", true},
		{"https://t.example/pb?cid={click_id|raw|hash}", true},
		{"https://t.example/pb?x={sub_id}", false},
		{"https://t.example/pb?x={click_id|upper}", false},
		{"https://t.example/pb?x={click_id", false},
		{"https://t.example/pb?x=click_id}", false},
		{"https://t.example/pb?pixel=REPLACE&cid={click_id}", false},
	} {
		if err := Check(tc.tmpl, known); (err == nil) != tc.ok {
			t.Errorf("Check(%q) = %v", tc.tmpl, err)
		}
	}
	if err := Check("?p=REPLACE", known); !errors.Is(err, ErrPlaceholder) {
		t.Errorf("REPLACE: %v", err)
	}
	if got := Names("{a}{b|raw}{a}"); strings.Join(got, ",") != "a,b" {
		t.Errorf("Names = %v", got)
	}
}
