package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"unicode/utf8"
)

// Embeddings turn text into vectors that sit close together when the texts
// mean similar things, which is what meaning-based search compares. They
// use the workspace's own key, like every other call here. Claude has no
// embeddings API, so a workspace needs one of the others for them.

// EmbedDims is the length of every vector Outflare stores. Each provider's
// model is asked for it, or cut to it (the models below are trained so
// that the first dimensions carry the most meaning).
const EmbedDims = 1024

// ErrNoEmbeddings means the provider has no embeddings API.
var ErrNoEmbeddings = errors.New("this provider doesn't offer embeddings")

// embedModels are the embedding model each provider uses, with its list
// price in US cents per million tokens. OpenRouter reports what each call
// cost, so it needs no price.
var embedModels = map[string]struct {
	model string
	cents float64
	batch int  // most texts in one call
	dims  bool // accepts a dimensions parameter
}{
	OpenAI.ID:     {"text-embedding-3-small", 2, 64, true},
	Gemini.ID:     {"gemini-embedding-001", 15, 64, true},
	Mistral.ID:    {"mistral-embed", 10, 8, false},
	OpenRouter.ID: {"openai/text-embedding-3-small", 0, 64, true},
}

// EmbedModel is the model the provider embeds text with, or "" when it
// offers no embeddings.
func (p Provider) EmbedModel() string { return embedModels[p.ID].model }

// EmbedBatch is the most texts one Embed call sends.
func (p Provider) EmbedBatch() int { return max(embedModels[p.ID].batch, 1) }

// EmbedPrice is what the provider's embedding model costs.
func (p Provider) EmbedPrice() Price {
	c := embedModels[p.ID].cents
	return Price{Input: c, Output: c}
}

// EmbedMaxRunes is the most of a text that is embedded; the rest is cut,
// which keeps each text well inside every provider's limit.
const EmbedMaxRunes = 4000

// Embed returns one vector of EmbedDims numbers per text, each of length
// one, and what the call used. c.Model is ignored: each provider has one
// embedding model (EmbedModel).
func Embed(ctx context.Context, c Config, texts []string) ([][]float32, Usage, error) {
	m, ok := embedModels[c.Provider.ID]
	if !ok {
		return nil, Usage{}, ErrNoEmbeddings
	}
	if len(texts) == 0 {
		return nil, Usage{Model: m.model}, nil
	}
	if len(texts) > c.Provider.EmbedBatch() {
		return nil, Usage{}, fmt.Errorf("at most %d texts per call", c.Provider.EmbedBatch())
	}
	if c.Key == "" {
		return nil, Usage{}, ErrNoKey
	}
	in := make([]string, len(texts))
	for i, t := range texts {
		if utf8.RuneCountInString(t) > EmbedMaxRunes {
			t = string([]rune(t)[:EmbedMaxRunes])
		}
		if t == "" {
			t = " "
		}
		in[i] = t
	}
	body := map[string]any{"model": m.model, "input": in, "encoding_format": "float"}
	if m.dims {
		body["dimensions"] = EmbedDims
	}
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int64   `json:"prompt_tokens"`
			Cost         float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := doOpenAI(ctx, c, httpClient(c.HTTP), http.MethodPost, "/embeddings", body, &out); err != nil {
		return nil, Usage{Model: m.model}, err
	}
	u := Usage{Model: m.model, InputTokens: out.Usage.PromptTokens}
	if c.Provider.ReportsCost {
		u.CostUSD = out.Usage.Cost
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || vecs[d.Index] != nil {
			return nil, u, fmt.Errorf("%s returned embeddings Outflare could not read", c.Provider.Name)
		}
		v, err := fit(d.Embedding)
		if err != nil {
			return nil, u, fmt.Errorf("%s: %w", c.Provider.Name, err)
		}
		vecs[d.Index] = v
	}
	for _, v := range vecs {
		if v == nil {
			return nil, u, fmt.Errorf("%s returned fewer embeddings than texts", c.Provider.Name)
		}
	}
	return vecs, u, nil
}

// fit cuts v to EmbedDims and scales it to length one, so cosine distance
// between stored vectors is comparable.
func fit(v []float64) ([]float32, error) {
	if len(v) < EmbedDims {
		return nil, fmt.Errorf("the embedding has %d numbers, fewer than the %d Outflare stores", len(v), EmbedDims)
	}
	v = v[:EmbedDims]
	var sum float64
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errors.New("the embedding has a number that isn't one")
		}
		sum += x * x
	}
	if sum == 0 {
		return nil, errors.New("the embedding is all zeros")
	}
	n := math.Sqrt(sum)
	out := make([]float32, EmbedDims)
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out, nil
}
