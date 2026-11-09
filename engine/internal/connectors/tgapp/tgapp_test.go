package tgapp

import (
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// fakeHash is a made-up api_hash: 32 hex characters, like Telegram's.
var fakeHash = strings.Repeat("ab", 16)

func TestParse(t *testing.T) {
	good := c.Creds{"api_id": " 1234567 ", "api_hash": strings.ToUpper(fakeHash)}
	app, err := Parse(good)
	if err != nil || app.ID != 1234567 || app.Hash != fakeHash || app.Proxy != "" {
		t.Fatalf("good: %+v %v", app, err)
	}
	for name, creds := range map[string]c.Creds{
		"no id":      {"api_hash": fakeHash},
		"bad id":     {"api_id": "abc", "api_hash": fakeHash},
		"short hash": {"api_id": "1", "api_hash": "abc"},
		"bad proxy":  {"api_id": "1", "api_hash": fakeHash, "proxy": "ftp://proxy.example"},
		"no host":    {"api_id": "1", "api_hash": fakeHash, "proxy": "socks5://"},
	} {
		if _, err := Parse(creds); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	app, err = Parse(c.Creds{"api_id": "1", "api_hash": fakeHash, "proxy": "socks5://user:secret@proxy.example:1080"})
	if err != nil || app.Proxy == "" {
		t.Fatalf("proxy: %+v %v", app, err)
	}
	acc, err := Connector{}.Test(t.Context(), nil, c.Creds{"api_id": "1", "api_hash": fakeHash, "proxy": "socks5://proxy.example:1080"})
	if err != nil || acc.Details["proxy"] != "on" || acc.Details["api_id"] != "1" {
		t.Fatalf("test: %+v %v", acc, err)
	}
}
