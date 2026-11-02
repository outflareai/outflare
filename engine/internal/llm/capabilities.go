package llm

// Capabilities says what a provider's chat API does through Outflare's
// adapters, so an agent picks only what will work: it never offers tools
// to a model that can't call them, or sends images to one that can't read
// them. These are the provider's API; a particular model may still refuse
// something, and then the call fails with the provider's own error.
type Capabilities struct {
	Tools      bool `json:"tools"`       // tool calling
	JSONSchema bool `json:"json_schema"` // answers matching a JSON schema
	Images     bool `json:"images"`      // images in messages and tool results
	Streaming  bool `json:"streaming"`   // Request.OnText
	WebSearch  bool `json:"web_search"`  // Request.WebSearch
	// Cache: stable context (the system prompt, a reference) is cached and
	// billed at the cheaper rate, so a long brief costs little to resend.
	Cache bool `json:"cache"`
}

// CapabilitiesOf reports what provider's API does with model.
func CapabilitiesOf(provider, model string) Capabilities {
	c := Capabilities{Tools: true, JSONSchema: true, Images: SupportsImages(provider), Streaming: true,
		WebSearch: SupportsWebSearch(provider, model)}
	switch provider {
	case Claude.ID:
		// The adapter marks the stable prefix for caching.
		c.Cache = true
	case OpenAI.ID, DeepSeek.ID:
		// The provider caches a repeated prefix by itself. A router's
		// caching depends on the model behind it, so it isn't claimed.
		c.Cache = true
	}
	return c
}

// Missing lists what want needs that c lacks, by name, for a message that
// says which capability a model is missing.
func (c Capabilities) Missing(want Capabilities) []string {
	var out []string
	check := func(need, have bool, name string) {
		if need && !have {
			out = append(out, name)
		}
	}
	check(want.Tools, c.Tools, "tools")
	check(want.JSONSchema, c.JSONSchema, "json_schema")
	check(want.Images, c.Images, "images")
	check(want.Streaming, c.Streaming, "streaming")
	check(want.WebSearch, c.WebSearch, "web_search")
	check(want.Cache, c.Cache, "cache")
	return out
}
