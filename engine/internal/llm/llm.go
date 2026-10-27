// Package llm talks to the AI model a workspace chose, with the
// workspace's own API key: Claude, OpenAI, Gemini, Mistral, xAI, or a router
// (OpenRouter, Vercel AI Gateway, Requesty). The planner
// and the built-in agent use it through one small interface, so they work
// the same whichever provider runs them.
//
// Keys are the workspace's secrets, so this package holds to a few rules:
//
//   - A key only ever goes to its provider's own API. Base URLs are fixed
//     here; only the operator (an environment variable) or a test can point
//     them elsewhere, never a workspace setting.
//   - Error text from a provider is scrubbed of the key and of anything
//     shaped like a key before it reaches a log, an event, the API or the
//     model.
//   - Answers are read with a size limit, and redirects are not followed,
//     so the Authorization header never leaves the provider's host.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Provider is a model vendor Outflare can use. ID is its connector id.
type Provider struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Vendor string `json:"vendor"` // who bills it, as spend entries name it
	// API base URL. Env, when set, overrides it for the whole instance
	// (an operator's gateway); a workspace can never change it.
	BaseURL string `json:"-"`
	Env     string `json:"-"`
	// The model used when the workspace has not picked one. Empty means the
	// workspace must pick, because the provider's line-up changes too often
	// to guess.
	DefaultModel string `json:"default_model"`
	// A cheaper model good enough for routine work (scheduled check-ins),
	// used unless the workspace chooses otherwise. Empty: routine work uses
	// the main model.
	LightModel string `json:"light_model"`
	// The cheapest model fit for short, simple jobs (labelling a reply,
	// classifying a domain). Empty: those jobs use LightModel, else the
	// main model.
	CheapModel string `json:"cheap_model"`
	KeyURL     string `json:"key_url"`
	PricingURL string `json:"pricing_url"`
	// Prefixes the provider's keys start with, to catch a pasted wrong
	// secret. Empty: the provider's keys have no fixed shape.
	KeyPrefixes []string `json:"-"`
	// The provider says what each call cost (Usage.CostUSD), so no price
	// per token has to be entered.
	ReportsCost bool `json:"reports_cost"`
	openAI      bool // speaks the OpenAI chat completions API
}

// The providers, in the order the selector lists them.
var (
	Claude = Provider{ID: "claude", Name: "Claude", Vendor: "anthropic", BaseURL: "https://api.anthropic.com", Env: "ANTHROPIC_BASE_URL",
		DefaultModel: "claude-opus-5-5", LightModel: "claude-sonnet-5-5", CheapModel: "claude-haiku-5-5", KeyURL: "https://console.anthropic.com/settings/keys", PricingURL: "https://www.anthropic.com/pricing#api",
		KeyPrefixes: []string{"sk-ant-"}}
	OpenAI = Provider{ID: "openai", Name: "OpenAI", Vendor: "openai", BaseURL: "https://api.openai.com/v1", Env: "OUTFLARE_OPENAI_BASE_URL",
		KeyURL: "https://platform.openai.com/api-keys", PricingURL: "https://openai.com/api/pricing/", KeyPrefixes: []string{"sk-"}, openAI: true}
	Gemini = Provider{ID: "gemini", Name: "Gemini", Vendor: "google", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Env: "OUTFLARE_GEMINI_BASE_URL",
		KeyURL: "https://aistudio.google.com/app/apikey", PricingURL: "https://ai.google.dev/gemini-api/docs/pricing", KeyPrefixes: []string{"AIza"}, openAI: true}
	Mistral = Provider{ID: "mistral", Name: "Mistral", Vendor: "mistral", BaseURL: "https://api.mistral.ai/v1", Env: "OUTFLARE_MISTRAL_BASE_URL",
		KeyURL: "https://console.mistral.ai/api-keys", PricingURL: "https://mistral.ai/pricing#api-pricing", openAI: true}
	// OpenRouter reaches many vendors' models with one key, and reports
	// each call's cost, so a workspace can sample the models its customers
	// use without a key per vendor.
	OpenRouter = Provider{ID: "openrouter", Name: "OpenRouter", Vendor: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Env: "OUTFLARE_OPENROUTER_BASE_URL",
		KeyURL: "https://openrouter.ai/settings/keys", PricingURL: "https://openrouter.ai/models", KeyPrefixes: []string{"sk-or-"}, ReportsCost: true, openAI: true}

	XAI = Provider{ID: "xai", Name: "xAI", Vendor: "xai", BaseURL: "https://api.x.ai/v1", Env: "OUTFLARE_XAI_BASE_URL",
		KeyURL: "https://console.x.ai/", PricingURL: "https://docs.x.ai/docs/models", KeyPrefixes: []string{"xai-"}, openAI: true}
	// Vercel AI Gateway and Requesty are routers like OpenRouter: one key
	// for many vendors' models, billed by the router.
	VercelAIGateway = Provider{ID: "vercel_ai_gateway", Name: "Vercel AI Gateway", Vendor: "vercel", BaseURL: "https://ai-gateway.vercel.sh/v1", Env: "OUTFLARE_VERCEL_AI_GATEWAY_BASE_URL",
		KeyURL: "https://vercel.com/dashboard", PricingURL: "https://vercel.com/ai-gateway", openAI: true}
	Requesty = Provider{ID: "requesty", Name: "Requesty", Vendor: "requesty", BaseURL: "https://router.requesty.ai/v1", Env: "OUTFLARE_REQUESTY_BASE_URL",
		KeyURL: "https://app.requesty.ai/", PricingURL: "https://www.requesty.ai/models-new", openAI: true}

	Providers = []Provider{Claude, OpenAI, Gemini, Mistral, XAI, OpenRouter, VercelAIGateway, Requesty}
)

// ProviderFor returns the provider whose connector id is id.
func ProviderFor(id string) (Provider, bool) {
	i := slices.IndexFunc(Providers, func(p Provider) bool { return p.ID == id })
	if i < 0 {
		return Provider{}, false
	}
	return Providers[i], true
}

// Vendors lists every provider's vendor name, for summing AI spend.
func Vendors() []string {
	out := make([]string, len(Providers))
	for i, p := range Providers {
		out[i] = p.Vendor
	}
	return out
}

// Base is the API base URL: override (tests), else the operator's
// environment variable, else the provider's own.
func (p Provider) Base(override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	if u := strings.TrimRight(os.Getenv(p.Env), "/"); u != "" {
		return u
	}
	return p.BaseURL
}

// CheckKey catches a secret that is clearly not this provider's key, such
// as an Outflare token pasted into the wrong box.
func (p Provider) CheckKey(key string) error {
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return errors.New("the API key is empty")
	case strings.ContainsAny(key, " \t\r\n"):
		return errors.New("the API key has spaces or line breaks in it; paste only the key")
	case outflareSecret.MatchString(key):
		return errors.New("that is an Outflare token, not a " + p.Name + " key. Never paste Outflare tokens into another service")
	case len(p.KeyPrefixes) > 0 && !slices.ContainsFunc(p.KeyPrefixes, func(pre string) bool { return strings.HasPrefix(key, pre) }):
		return errors.New(p.Name + " keys start with " + strings.Join(p.KeyPrefixes, " or ") + "; check you copied the right key")
	}
	return nil
}

var outflareSecret = regexp.MustCompile(`^of[a-z]_`)

// Message is one turn of a conversation.
type Message struct {
	Role        string // "user" or "assistant"
	Text        string
	ToolCalls   []ToolCall   // assistant
	ToolResults []ToolResult // user
	// The provider's own form of an assistant message, sent back as it
	// came (Claude needs its thinking blocks back unchanged).
	native any
}

// ToolCall is the model asking to run a tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Tool is a function the model may call. Schema is a JSON Schema object.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Request is one model call.
type Request struct {
	// System is the stable instruction. Reference is more stable context
	// (an API guide), sent after it; both are cached where the provider
	// caches.
	System    string
	Reference string
	Messages  []Message
	Tools     []Tool
	// JSONSchema asks for an answer that is JSON matching it.
	JSONSchema map[string]any
	MaxTokens  int
	// WebSearch lets the model search the web before it answers, where the
	// provider offers it (SupportsWebSearch); the pages it used come back
	// as Response.Citations.
	WebSearch bool
}

// Citation is a web page a model's answer is based on.
type Citation struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// SupportsWebSearch reports whether a model can search the web before it
// answers: Claude's web search tool, OpenRouter's web plugin, or one of
// OpenAI's search models.
func SupportsWebSearch(provider, model string) bool {
	switch provider {
	case Claude.ID, OpenRouter.ID:
		return true
	case OpenAI.ID:
		return strings.Contains(model, "search")
	}
	return false
}

// Stop reasons.
const (
	StopEnd       = "end"
	StopToolUse   = "tool_use"
	StopRefusal   = "refusal"
	StopMaxTokens = "max_tokens"
)

// Response is the model's answer.
type Response struct {
	Message Message
	Stop    string
	Usage   Usage
	// The web pages the answer cites, when it searched the web.
	Citations []Citation
}

// Usage is what calls cost, in tokens.
type Usage struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_input_tokens"`
	CacheWrite   int64  `json:"cache_creation_input_tokens"`
	// Web searches the provider ran for the call, billed per search.
	WebSearches int64 `json:"web_searches,omitempty"`
	// What the provider says the call cost, in US dollars, when it says
	// (Provider.ReportsCost); zero otherwise.
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Add adds b to u.
func (u *Usage) Add(b Usage) {
	if b.Model != "" {
		u.Model = b.Model
	}
	u.InputTokens += b.InputTokens
	u.OutputTokens += b.OutputTokens
	u.CacheRead += b.CacheRead
	u.CacheWrite += b.CacheWrite
	u.WebSearches += b.WebSearches
	u.CostUSD += b.CostUSD
}

// Model is a provider's model with a workspace's key.
type Model interface {
	Chat(ctx context.Context, req Request) (Response, error)
	Provider() Provider
	ID() string
}

// Config opens a model.
type Config struct {
	Provider Provider
	Key      string
	Model    string // empty: the provider's default
	BaseURL  string // tests
	HTTP     *http.Client
}

// ErrAuth means the provider rejected the key.
var ErrAuth = errors.New("the provider did not accept the API key")

// ErrNoKey means the workspace hasn't added its own key. There is no
// other key to fall back to: every workspace brings its own (AGENTS.md,
// "Bring your own keys"). It is an ErrAuth, so callers ask for the key.
var ErrNoKey error = noKey{}

type noKey struct{}

func (noKey) Error() string        { return "add your own API key for this provider" }
func (noKey) Is(target error) bool { return target == ErrAuth }

// ErrNoModel means the workspace has to pick a model for the provider.
var ErrNoModel = errors.New("pick a model for this provider")

// New opens the model in c.
func New(c Config) (Model, error) {
	if strings.TrimSpace(c.Key) == "" {
		return nil, ErrNoKey
	}
	if c.Model == "" {
		c.Model = c.Provider.DefaultModel
	}
	if c.Model == "" {
		return nil, ErrNoModel
	}
	if c.Provider.openAI {
		return &openAIModel{c: c, http: httpClient(c.HTTP)}, nil
	}
	return &claudeModel{c: c}, nil
}

// ModelInfo is a model the key can use.
type ModelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListModels asks the provider which chat models the key can use. It also
// proves the key works, at no cost.
func ListModels(ctx context.Context, c Config) ([]ModelInfo, error) {
	if strings.TrimSpace(c.Key) == "" {
		return nil, ErrNoKey
	}
	if c.Provider.openAI {
		return listOpenAIModels(ctx, c, httpClient(c.HTTP))
	}
	return listClaudeModels(ctx, c)
}

// httpClient is h, or a client with a timeout, that never follows a
// redirect (the key must not travel to another host).
func httpClient(h *http.Client) *http.Client {
	if h == nil {
		h = &http.Client{Timeout: 5 * time.Minute}
	}
	c := *h
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// keyShaped matches strings that look like provider or Outflare secrets.
var keyShaped = regexp.MustCompile(`sk-[A-Za-z0-9_\-]{12,}|AIza[0-9A-Za-z_\-]{20,}|of[a-z]_[A-Za-z0-9_\-]{8,}|(?i)bearer\s+[A-Za-z0-9._\-]{12,}`)

// Scrub removes secrets from text: each of keys, and anything shaped like
// a key.
func Scrub(s string, keys ...string) string {
	for _, k := range keys {
		if k = strings.TrimSpace(k); len(k) >= 8 {
			s = strings.ReplaceAll(s, k, "[key removed]")
		}
	}
	return keyShaped.ReplaceAllString(s, "[key removed]")
}

// scrubbed is an error whose text has been scrubbed. It still unwraps to
// the original, so errors.Is keeps working.
type scrubbed struct {
	msg string
	err error
}

func (e scrubbed) Error() string { return e.msg }
func (e scrubbed) Unwrap() error { return e.err }

func scrubErr(err error, key string) error {
	if err == nil {
		return nil
	}
	return scrubbed{msg: Scrub(err.Error(), key), err: err}
}

// Price is what a model costs, in US cents per million tokens.
type Price struct {
	Input  float64 `json:"input_cents_per_million"`
	Output float64 `json:"output_cents_per_million"`
	// Cached input reads; zero means billed as input (the safe guess).
	CacheRead float64 `json:"-"`
}

// Known reports whether the price is set.
func (p Price) Known() bool { return p.Input > 0 && p.Output > 0 }

// Cents prices u, rounded up. Cache writes cost 1.25 times input. A cost
// the provider reported wins over the price, and web searches add
// WebSearchCents each.
func (p Price) Cents(u Usage) int64 {
	if u.CostUSD > 0 {
		return int64(math.Ceil(u.CostUSD * 100))
	}
	read := p.CacheRead
	if read == 0 {
		read = p.Input
	}
	micro := float64(u.InputTokens)*p.Input + float64(u.OutputTokens)*p.Output +
		float64(u.CacheWrite)*p.Input*1.25 + float64(u.CacheRead)*read
	return int64(math.Ceil(micro/1e6)) + u.WebSearches*WebSearchCents
}

// WebSearchCents is what one web search costs on Claude: US$10 per
// thousand.
const WebSearchCents = 1

// claudePrices are Anthropic's list prices. Other providers' prices change
// too often to keep here, so the workspace enters them with its choice.
var claudePrices = map[string]Price{
	"claude-fable-5-1":  {Input: 1000, Output: 5000, CacheRead: 25},
	"claude-opus-5-5":   {Input: 400, Output: 2000, CacheRead: 20},
	"claude-sonnet-5-5": {Input: 200, Output: 1000, CacheRead: 20},
	"claude-haiku-5-5":  {Input: 10, Output: 50, CacheRead: 1},
}

// KnownPrice is the list price of a model, when Outflare knows it. A
// provider that reports each call's cost (OpenRouter) knows every model's
// price: the zero Price, since Cents takes the provider's figure.
func KnownPrice(provider, model string) (Price, bool) {
	if p, ok := ProviderFor(provider); ok && p.ReportsCost {
		return Price{}, true
	}
	if provider != Claude.ID {
		return Price{}, false
	}
	p, ok := claudePrices[model]
	return p, ok
}
