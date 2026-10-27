package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// platformEnv sets every variable an SDK or habit would read a
// server-wide key from, so a test fails if any of it reaches a request.
func platformEnv(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "MISTRAL_API_KEY"} {
		t.Setenv(k, "sk-platform-test-not-a-real-key")
	}
}

// recorder answers a model list and remembers each request's credentials.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (rec *recorder) server(t *testing.T, body string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.seen = append(rec.seen, r.Header.Get("X-Api-Key")+"|"+r.Header.Get("Authorization"))
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Bring your own keys (AGENTS.md): a call to Claude carries the workspace's
// key and nothing from the server's environment.
func TestClaudeUsesOnlyTheWorkspaceKey(t *testing.T) {
	platformEnv(t)
	rec := &recorder{}
	srv := rec.server(t, `{"data":[{"id":"claude-test","display_name":"Claude Test","type":"model","created_at":"2026-01-01T00:00:00Z"}],"has_more":false}`)
	key := "sk-ant-workspace-test-not-a-real-key"
	if _, err := ListModels(context.Background(), Config{Provider: Claude, Key: key, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(rec.seen) != 1 || rec.seen[0] != key+"|" {
		t.Errorf("requests carried %q, want only the workspace key", rec.seen)
	}
}

func TestOpenAICompatibleUsesOnlyTheWorkspaceKey(t *testing.T) {
	platformEnv(t)
	for _, p := range []Provider{OpenAI, Gemini, Mistral} {
		rec := &recorder{}
		srv := rec.server(t, `{"data":[{"id":"model-test"}]}`)
		key := "sk-workspace-test-not-a-real-key"
		if _, err := ListModels(context.Background(), Config{Provider: p, Key: key, BaseURL: srv.URL}); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if len(rec.seen) != 1 || rec.seen[0] != "|Bearer "+key {
			t.Errorf("%s: requests carried %q, want only the workspace key", p.ID, rec.seen)
		}
	}
}

// Without the workspace's own key there is nothing to call with: no
// request is made and the caller is told to add a key.
func TestNoKeyMeansNoCall(t *testing.T) {
	platformEnv(t)
	for _, p := range Providers {
		rec := &recorder{}
		srv := rec.server(t, `{"data":[]}`)
		cfg := Config{Provider: p, Key: "  ", BaseURL: srv.URL, Model: "model-test"}
		if _, err := ListModels(context.Background(), cfg); !errors.Is(err, ErrNoKey) || !errors.Is(err, ErrAuth) {
			t.Errorf("%s: ListModels = %v, want ErrNoKey", p.ID, err)
		}
		if _, err := New(cfg); !errors.Is(err, ErrNoKey) {
			t.Errorf("%s: New = %v, want ErrNoKey", p.ID, err)
		}
		if len(rec.seen) > 0 {
			t.Errorf("%s: made %d requests without a key", p.ID, len(rec.seen))
		}
	}
}
