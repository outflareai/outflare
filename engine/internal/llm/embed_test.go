package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbed(t *testing.T) {
	var asked map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer sk-or-test-not-a-real-key" {
			w.WriteHeader(401)
			return
		}
		json.NewDecoder(r.Body).Decode(&asked)
		long := make([]float64, 2048)
		long[0], long[1500] = 3, 4 // the part past 1024 is cut
		// Answers may come in any order.
		json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 1, "embedding": long},
			map[string]any{"index": 0, "embedding": append([]float64{3, 4}, make([]float64, EmbedDims-2)...)},
		}, "usage": map[string]any{"prompt_tokens": 12, "cost": 0.0000024}})
	}))
	defer srv.Close()
	c := Config{Provider: OpenRouter, Key: "sk-or-test-not-a-real-key", BaseURL: srv.URL}
	vecs, u, err := Embed(context.Background(), c, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if asked["model"] != "openai/text-embedding-3-small" || asked["dimensions"] != float64(EmbedDims) {
		t.Fatalf("asked %v", asked)
	}
	if len(vecs) != 2 || len(vecs[0]) != EmbedDims || math.Abs(float64(vecs[0][0])-0.6) > 1e-6 || math.Abs(float64(vecs[0][1])-0.8) > 1e-6 || vecs[1][0] != 1 {
		t.Fatalf("vectors not cut and scaled: %v %v", vecs[0][:2], vecs[1][:2])
	}
	if u.InputTokens != 12 || u.CostUSD == 0 || u.Model != "openai/text-embedding-3-small" {
		t.Fatalf("usage %+v", u)
	}
	// Claude has no embeddings, and there is never a key but the workspace's.
	if _, _, err := Embed(context.Background(), Config{Provider: Claude, Key: "sk-ant-x"}, []string{"x"}); !errors.Is(err, ErrNoEmbeddings) {
		t.Fatalf("claude: %v", err)
	}
	if _, _, err := Embed(context.Background(), Config{Provider: OpenAI}, []string{"x"}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	if _, err := fit(make([]float64, 512)); err == nil {
		t.Fatal("a vector shorter than Outflare stores was taken")
	}
}
