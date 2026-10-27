// Package models connects a workspace's own AI model API key: Claude,
// OpenAI, Gemini, Mistral, xAI, or a router (OpenRouter, Vercel AI Gateway,
// Requesty). Missions use the one the workspace
// picks to turn a goal into a plan and to run the built-in agent; AI
// visibility asks the models the workspace picks its prompts.
package models

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/llm"
)

// Model is the connector for one provider.
type Model struct{ P llm.Provider }

// All is one connector per provider, in the selector's order.
func All() []c.Connector {
	out := make([]c.Connector, len(llm.Providers))
	for i, p := range llm.Providers {
		out[i] = Model{P: p}
	}
	return out
}

// IsModel reports whether connector id is an AI model connector.
func IsModel(id string) bool {
	_, ok := llm.ProviderFor(id)
	return ok
}

// router is the tag on providers that reach many vendors' models.
var router = c.Tag{Label: "Router", Help: "One key reaches many vendors' models (Claude, GPT, Gemini, Llama and more), billed by the router."}

var info = map[string]struct {
	summary, website, docs, keyHint string
	tags                            []c.Tag
}{
	llm.Claude.ID: {summary: "Anthropic's Claude models.", website: "https://www.anthropic.com/api", docs: "https://docs.anthropic.com/en/api/getting-started", keyHint: "Claude Console → Settings → API keys."},
	llm.OpenAI.ID: {summary: "OpenAI's GPT models.", website: "https://openai.com/api/", docs: "https://platform.openai.com/docs/overview", keyHint: "OpenAI Platform → API keys. A project key limited to this use is safest.",
		tags: []c.Tag{{Label: "ChatGPT plan sign-in soon", Help: "OpenAI lets apps use a ChatGPT Plus or Pro plan instead of an API key (Sign in with ChatGPT). Outflare doesn't offer it yet: connect an API key for now."}}},
	llm.Gemini.ID:  {summary: "Google's Gemini models.", website: "https://ai.google.dev/", docs: "https://ai.google.dev/gemini-api/docs", keyHint: "Google AI Studio → Get API key."},
	llm.Mistral.ID: {summary: "Mistral's models, hosted in the EU.", website: "https://mistral.ai/", docs: "https://docs.mistral.ai/", keyHint: "Mistral console → API keys."},
	llm.XAI.ID: {summary: "xAI's Grok models.", website: "https://x.ai/api", docs: "https://docs.x.ai/docs/overview", keyHint: "xAI Console → API Keys.",
		tags: []c.Tag{{Label: "SuperGrok sign-in soon", Help: "xAI lets some apps use a SuperGrok or X Premium+ plan instead of an API key. It isn't open to Outflare yet: connect an API key for now."}}},
	llm.OpenRouter.ID: {summary: "Hundreds of models from many vendors (OpenAI, Google, Meta, xAI, Perplexity...) with one key; OpenRouter reports what each call cost.",
		website: "https://openrouter.ai/", docs: "https://openrouter.ai/docs/quickstart", keyHint: "OpenRouter → Settings → API keys. A key with its own credit limit is safest.", tags: []c.Tag{router}},
	llm.VercelAIGateway.ID: {summary: "Models from many vendors with one Vercel key, billed to your Vercel team.",
		website: "https://vercel.com/ai-gateway", docs: "https://vercel.com/docs/ai-gateway", keyHint: "Vercel dashboard → AI Gateway → API Keys.", tags: []c.Tag{router}},
	llm.Requesty.ID: {summary: "Models from many vendors with one Requesty key, with its own spending limits and fallbacks.",
		website: "https://www.requesty.ai/", docs: "https://docs.requesty.ai/quickstart", keyHint: "Requesty → API Keys.", tags: []c.Tag{router}},
}

func (m Model) Definition() c.Definition {
	i := info[m.P.ID]
	d := c.Definition{
		ID: m.P.ID, Name: m.P.Name, Category: c.CategoryModel,
		Summary: "Plan and run missions from a goal you type, with your own " + m.P.Name + " API key. " + i.summary,
		Description: "Outflare sends the model the goal, the project brief and a summary of the workspace (mailbox capacity, lead counts, campaigns, connectors) to plan, and the mission's progress and API answers while Outflare's agent runs it. " +
			"Your key is encrypted, only ever sent to " + m.P.Name + "'s own API, and never shown again, logged or given to the model. Usage is billed to your " + m.P.Name + " account; set a monthly limit under Settings → AI model.",
		Website: i.website, DocsURL: i.docs, KeyURL: m.P.KeyURL, Auth: c.AuthAPIKey, Tags: i.tags,
		Fields:       []c.Field{{Name: "api_key", Label: "API key", Secret: true, Placeholder: placeholder(m.P), Help: i.keyHint}},
		Capabilities: []string{"plan", "llm.sample"}, Pricing: "Pay per token, billed by " + m.P.Name,
	}
	if m.P.ID == llm.OpenRouter.ID {
		// OpenRouter's sign-in makes a key on the person's own account and
		// hands it back, so nobody registers an app and usage is still theirs.
		d.Auth, d.AuthMethods = c.AuthOAuth2, c.Both
		d.OAuth = &c.OAuth{AuthURL: "https://openrouter.ai/auth", TokenURL: "https://openrouter.ai/api/v1/auth/keys", Scopes: []string{}, KeyExchange: true}
	}
	return d
}

func placeholder(p llm.Provider) string {
	if len(p.KeyPrefixes) > 0 {
		return p.KeyPrefixes[0] + "…"
	}
	return ""
}

// Config is how a connector call reaches the provider: the test override
// from cl, else the provider's own API.
func Config(p llm.Provider, cl *c.Client, key, model string) llm.Config {
	cfg := llm.Config{Provider: p, Key: key, Model: model}
	if cl != nil {
		cfg.HTTP = cl.HTTP
		if u := cl.Base(p.ID, ""); u != "" {
			cfg.BaseURL = u
		}
	}
	return cfg
}

// Test lists the key's models, which proves it works and costs nothing.
func (m Model) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	key := cr["api_key"]
	if err := m.P.CheckKey(key); err != nil {
		return c.Account{}, c.Errf(c.ErrInvalid, "%s", err.Error())
	}
	list, err := llm.ListModels(ctx, Config(m.P, cl, key, ""))
	if err != nil {
		if errors.Is(err, llm.ErrAuth) {
			return c.Account{}, c.Errf(c.ErrAuth, "%s did not accept the API key. Check it, or create a new one at %s.", m.P.Name, m.P.KeyURL)
		}
		return c.Account{}, c.Errf(c.ErrUnavailable, "Could not reach the %s API: %v", m.P.Name, err)
	}
	return c.Account{Name: m.P.Name + " API", Details: map[string]string{"models": strconv.Itoa(len(list))}}, nil
}

// Models lists the chat models an opened connection can use.
func Models(ctx context.Context, o *c.Opened) ([]llm.ModelInfo, error) {
	p, ok := llm.ProviderFor(o.Integration.Connector)
	if !ok {
		return nil, fmt.Errorf("%s is not an AI model connector", o.Integration.Connector)
	}
	return llm.ListModels(ctx, Config(p, o.Client, o.Creds["api_key"], ""))
}
