package connectors

import "context"

// AI answers: connectors that ask an AI assistant a question on its own
// screen (ChatGPT, Gemini), signed out, from a country, and return what a
// person there would see: the answer, the brands it named, the pages it
// cited and read, and the searches it ran. That differs from asking the
// model's API, which cites other pages for most questions. They charge per
// answer, so each answer says what it cost and the caller records it on
// the connection's budget.

// CapAIAnswers marks a connector that implements AnswerReader.
const CapAIAnswers = "ai.answers"

// AnswerEngine is an assistant an answer reader can ask.
type AnswerEngine struct {
	// ID is chatgpt or gemini; Name how people know it.
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AnswerQuery is one question.
type AnswerQuery struct {
	Prompt string `json:"prompt"`
	// Country is a two-letter code, Language a two-letter code ("" for the
	// country's own).
	Country  string `json:"country"`
	Language string `json:"language"`
	// WebSearch makes the assistant search the web before it answers,
	// rather than leaving that to it.
	WebSearch bool `json:"web_search"`
}

// AnswerPage is one page an answer cited or the assistant read.
type AnswerPage struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
	Title  string `json:"title"`
	// Position is 1 for the first.
	Position int `json:"position"`
}

// AnswerResult is what the assistant showed.
type AnswerResult struct {
	Engine string `json:"engine"`
	// Model is the model the screen says it answered with.
	Model string `json:"model"`
	// Text is the answer as Markdown.
	Text string `json:"text"`
	// Brands are the brands the reader recognised in the answer, in order.
	Brands []string `json:"brands"`
	// Cited are the pages the answer links as its sources, in order; Read
	// every page the assistant's searches found, cited or not.
	Cited []AnswerPage `json:"cited"`
	Read  []AnswerPage `json:"read"`
	// Searches are the web searches the assistant ran.
	Searches []string `json:"searches"`
	// CheckURL asks the assistant the same question, for a person to look.
	CheckURL string  `json:"check_url"`
	Cost     SEOCost `json:"cost"`
}

// AnswerReader asks AI assistants questions on their own screens.
type AnswerReader interface {
	Connector
	// AnswerEngines are the assistants it can ask.
	AnswerEngines() []AnswerEngine
	// EstimateAnswer is what one answer costs, from the price list.
	EstimateAnswer(engine string) SEOCost
	Answer(ctx context.Context, c *Client, creds Creds, engine string, q AnswerQuery) (AnswerResult, error)
}

// AI demand: what people ask AI assistants and how often. A demand reader
// keeps a store of real questions put to assistants (DataForSEO's LLM
// Mentions: ChatGPT in the US in English, and Google's AI Overviews) and
// how many times a month people ask about a phrase in them (its AI keyword
// data), so AI visibility can ask what people really ask and weight each
// question by how often it is asked.

// CapAIDemand marks a connector that implements DemandReader.
const CapAIDemand = "ai.demand"

// AIQuestionQuery asks for questions whose answers mention any of the
// domains or keywords.
type AIQuestionQuery struct {
	Domains  []string `json:"domains"`
	Keywords []string `json:"keywords"`
	// Country and Language are two-letter codes ("" for the US and the
	// country's own language).
	Country  string `json:"country"`
	Language string `json:"language"`
	Limit    int    `json:"limit"`
}

// AIQuestion is one question people asked an assistant.
type AIQuestion struct {
	Question string `json:"question"`
	// Platform is chatgpt or ai_overview.
	Platform string `json:"platform"`
	// Volume is about how many times a month people ask it.
	Volume int `json:"volume"`
	// Brands are the brands its answer named, in order.
	Brands []string `json:"brands"`
}

// AIQuestions are the questions found and what finding them cost.
type AIQuestions struct {
	Questions []AIQuestion `json:"questions"`
	Cost      SEOCost      `json:"cost"`
}

// AIVolumes are monthly AI searches by lowercase phrase, for the phrases
// the reader knows, and what reading them cost.
type AIVolumes struct {
	Volume map[string]int `json:"volume"`
	Cost   SEOCost        `json:"cost"`
}

// AIBenchmarkTarget is one brand to count: its domains and names.
type AIBenchmarkTarget struct {
	Key      string   `json:"key"`
	Domains  []string `json:"domains"`
	Keywords []string `json:"keywords"`
}

// AIBenchmarkCount is how many stored AI answers mention a target, and how
// many times a month people ask those questions, in all and by platform
// (chatgpt, ai_overview).
type AIBenchmarkCount struct {
	Mentions       int            `json:"mentions"`
	AISearchVolume int64          `json:"ai_search_volume"`
	Platforms      map[string]int `json:"platforms"`
}

// AIBenchmark is each target's count by key, and what reading it cost.
type AIBenchmark struct {
	Counts map[string]AIBenchmarkCount `json:"counts"`
	Cost   SEOCost                     `json:"cost"`
}

// DemandReader reads what people ask AI assistants.
type DemandReader interface {
	Connector
	// EstimateAIQuestions and EstimateAIVolume are what one call costs,
	// from the price list.
	EstimateAIQuestions() SEOCost
	EstimateAIVolume(phrases int) SEOCost
	AIQuestions(ctx context.Context, c *Client, creds Creds, q AIQuestionQuery) (AIQuestions, error)
	AIVolume(ctx context.Context, c *Client, creds Creds, phrases []string, country, language string) (AIVolumes, error)
	// AIBenchmark counts the stored answers that mention each target:
	// a brand against its competitors beyond the questions it asks itself.
	EstimateAIBenchmark(targets int) SEOCost
	AIBenchmark(ctx context.Context, c *Client, creds Creds, targets []AIBenchmarkTarget, country, language string) (AIBenchmark, error)
}
