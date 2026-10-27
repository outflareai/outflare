package verifiers

import (
	"context"
	"net"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

type mxAll struct{}

func (mxAll) LookupMX(context.Context, string) ([]*net.MX, error) {
	return []*net.MX{{Host: "mx.example.net.", Pref: 1}}, nil
}
func (mxAll) LookupHost(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil }

func TestBuiltinUsesTheClassifier(t *testing.T) {
	b := Builtin{Resolver: mxAll{}}
	cases := []struct {
		email      string
		status     string
		reason     string
		free, role bool
	}{
		{"jo@gmail.com", c.Unknown, "mailbox_not_checked", true, false},
		// Proton is a private mailbox, not a free one.
		{"jo@proton.me", c.Unknown, "mailbox_not_checked", false, false},
		// An alias service forwards to a real inbox: not disposable.
		{"jo@duck.com", c.Unknown, "mailbox_not_checked", false, false},
		{"jo@mailinator.com", c.Undeliverable, "disposable_domain", false, false},
		{"info@acme-example-com.example", c.Risky, "role_address", false, true},
		{"not an address", c.Undeliverable, "invalid_syntax", false, false},
	}
	for _, tc := range cases {
		v, err := b.Verify(context.Background(), nil, nil, tc.email)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != tc.status || v.Reason != tc.reason || v.Free != tc.free || v.Role != tc.role {
			t.Errorf("%s: %+v", tc.email, v)
		}
	}
	// Built from the list, so no misspelt domain is written out as an address.
	for typo := range typoDomains {
		v, err := b.Verify(context.Background(), nil, nil, "jo@"+typo)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != c.Undeliverable || v.Reason != "domain_typo" {
			t.Errorf("%s: %+v", typo, v)
		}
	}
}
