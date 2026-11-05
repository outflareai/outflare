package outflare

import (
	"context"
	"errors"
	"fmt"

	"github.com/outflareai/outflare/engine/internal/llm"
)

// The operator's own tooling: hooks for what an operator runs to operate
// Outflare itself, never for a workspace feature. Every workspace feature
// uses the workspace's own keys (AI.Chat and the connectors), and nothing
// here may stand in for them (AGENTS.md, "Bring your own keys"). An
// extension that uses these holds the operator's key under an exception
// its own repository records, and books and caps what it spends itself.

// SealSecret encrypts plaintext with the engine's secret key, for an
// extension that stores a secret of its own in its own tables. purpose is
// a fixed label for what the secret is (for example "example-purpose"):
// OpenSecret needs the same one, so a secret sealed for one purpose can't
// be opened as another. It works once the engine has started serving.
func (h *Host) SealSecret(purpose string, plaintext []byte) ([]byte, error) {
	if h.box == nil {
		return nil, errors.New("the engine has not started")
	}
	if purpose == "" {
		return nil, errors.New("a sealed secret needs a purpose")
	}
	return h.box.Seal(plaintext, []byte("extension:"+purpose)), nil
}

// OpenSecret decrypts what SealSecret sealed for purpose. It also opens
// secrets sealed with the previous secret key while one is being rotated.
func (h *Host) OpenSecret(purpose string, sealed []byte) ([]byte, error) {
	if h.box == nil {
		return nil, errors.New("the engine has not started")
	}
	return h.box.Open(sealed, []byte("extension:"+purpose))
}

// OperatorModel is a model opened with a key the operator holds, for the
// operator's own tooling, such as its own evaluation tooling. It is never
// handed to a workspace feature.
type OperatorModel struct {
	m     llm.Model
	price llm.Price
}

// OpenOperatorModel opens provider's model with the operator's key. Only
// models whose price Outflare knows, or providers that report each call's
// cost, are opened, so every call can be booked against the operator's
// budget.
func OpenOperatorModel(provider, key, model string) (*OperatorModel, error) {
	p, ok := llm.ProviderFor(provider)
	if !ok {
		return nil, fmt.Errorf("unknown AI provider %q", provider)
	}
	m, err := llm.New(llm.Config{Provider: p, Key: key, Model: model})
	if err != nil {
		return nil, err
	}
	price, ok := llm.KnownPrice(p.ID, m.ID())
	if !ok {
		return nil, fmt.Errorf("Outflare doesn't know what %s costs, so it can't keep to a budget", m.ID())
	}
	return &OperatorModel{m: m, price: price}, nil
}

// Model is the model's id.
func (o *OperatorModel) Model() string { return o.m.ID() }

// Chat sends req and returns the answer and what it cost in cents. Errors
// never carry the key.
func (o *OperatorModel) Chat(ctx context.Context, req AIRequest) (AIResponse, int64, error) {
	resp, err := o.m.Chat(ctx, req)
	return resp, o.price.Cents(resp.Usage), err
}

// CheckOperatorKey asks the provider whether key works, at no cost.
func CheckOperatorKey(ctx context.Context, provider, key string) error {
	p, ok := llm.ProviderFor(provider)
	if !ok {
		return fmt.Errorf("unknown AI provider %q", provider)
	}
	_, err := llm.ListModels(ctx, llm.Config{Provider: p, Key: key})
	return err
}
