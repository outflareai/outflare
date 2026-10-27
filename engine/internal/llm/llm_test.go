package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	key := "mistral-plain-key-1234567890"
	in := "bad key " + key + " and sk-proj-abcdefghijklmnop0123 and AIzaSyA1234567890abcdefghijklmn and oft_secretsecret and Bearer abc.def.ghi.jkl.mno"
	out := Scrub(in, key)
	for _, leak := range []string{key, "sk-proj-abc", "AIzaSy", "oft_secret", "abc.def.ghi"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q left in %q", leak, out)
		}
	}
}

func TestCheckKey(t *testing.T) {
	cases := []struct {
		p   Provider
		key string
		ok  bool
	}{
		{Claude, "sk-ant-api03-abc", true},
		{Claude, "sk-proj-abc", false},
		{OpenAI, "sk-proj-abc", true},
		{OpenAI, "oft_abcdefghijkl", false},
		{Gemini, "AIzaSyabc", true},
		{Gemini, "sk-abc", false},
		{Mistral, "anything-goes-here", true},
		{Mistral, "two words", false},
		{Mistral, "", false},
	}
	for _, c := range cases {
		if err := c.p.CheckKey(c.key); (err == nil) != c.ok {
			t.Errorf("%s %q: err = %v", c.p.Name, c.key, err)
		}
	}
}

func TestPrice(t *testing.T) {
	p, ok := KnownPrice("claude", "claude-opus-5-5")
	if !ok {
		t.Fatal("Opus 5.5 price unknown")
	}
	// 40,000 in, 2,000 out, 20,000 cached reads: 16M + 4M + 0.4M micro-cents.
	if got := p.Cents(Usage{InputTokens: 40000, OutputTokens: 2000, CacheRead: 20000}); got != 21 {
		t.Errorf("cents = %d", got)
	}
	if _, ok := KnownPrice("openai", "anything"); ok {
		t.Error("OpenAI prices should come from the workspace")
	}
	// Cached reads at an entered price are billed as input: the safe guess.
	if got := (Price{Input: 100, Output: 100}).Cents(Usage{CacheRead: 1_000_000}); got != 100 {
		t.Errorf("cached = %d", got)
	}
}

// A provider's error text is scrubbed, and a redirect is never followed,
// so the key can't travel to another host.
func TestOpenAIErrorsAndRedirects(t *testing.T) {
	key := "sk-proj-very-secret-0123456789"
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("followed a redirect with %q", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			http.Redirect(w, r, other.URL+"/models", http.StatusFound)
		default:
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"bad request for key ` + key + `"}}`))
		}
	}))
	defer srv.Close()
	cfg := Config{Provider: OpenAI, Key: key, BaseURL: srv.URL, Model: "gpt-test"}
	if _, err := ListModels(context.Background(), cfg); err == nil {
		t.Error("a redirect should fail")
	}
	m, _ := New(cfg)
	_, err := m.Chat(context.Background(), Request{System: "x", Messages: []Message{{Role: "user", Text: "hi"}}})
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "400") {
		t.Errorf("err = %v", err)
	}
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv401.Close()
	cfg.BaseURL = srv401.URL
	if _, err := ListModels(context.Background(), cfg); !errors.Is(err, ErrAuth) {
		t.Errorf("401 = %v", err)
	}
}

func TestNewNeedsModel(t *testing.T) {
	if _, err := New(Config{Provider: OpenAI, Key: "sk-x"}); !errors.Is(err, ErrNoModel) {
		t.Errorf("err = %v", err)
	}
	m, err := New(Config{Provider: Claude, Key: "sk-ant-x"})
	if err != nil || m.ID() != "claude-opus-5-5" {
		t.Errorf("claude default = %v %v", m, err)
	}
}
