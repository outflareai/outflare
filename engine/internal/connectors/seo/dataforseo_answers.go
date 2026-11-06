package seo

import (
	"context"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// DataForSEO's LLM Scraper asks ChatGPT and Gemini on their own screens,
// signed out, from a country, and returns what a person there sees: the
// answer, the brands it named, the pages it cited and every page its
// searches found, and the searches themselves. Its LLM Responses API asks
// the models' APIs instead, which is what the workspace's own AI
// connections already do, so only the scraper is used here.

// answerEngines maps an assistant onto DataForSEO's name for it.
var answerEngines = []struct {
	c.AnswerEngine
	se string
}{
	{c.AnswerEngine{ID: "chatgpt", Name: "ChatGPT"}, "chat_gpt"},
	{c.AnswerEngine{ID: "gemini", Name: "Gemini"}, "gemini"},
}

// AnswerEngines: ChatGPT and Gemini.
func (DataForSEO) AnswerEngines() []c.AnswerEngine {
	out := make([]c.AnswerEngine, 0, len(answerEngines))
	for _, e := range answerEngines {
		out = append(out, e.AnswerEngine)
	}
	return out
}

// EstimateAnswer follows DataForSEO's price list for the live LLM Scraper:
// about $0.004 an answer. The recorded cost is what DataForSEO reports.
func (DataForSEO) EstimateAnswer(string) c.SEOCost {
	return c.SEOCost{USD: 0.004, Estimated: true}
}

// maxAnswerPrompt is the longest question sent; AI visibility prompts are
// at most 500 characters.
const maxAnswerPrompt = 500

// Answer asks one assistant one question with the live LLM Scraper.
func (d DataForSEO) Answer(ctx context.Context, cl *c.Client, creds c.Creds, engine string, q c.AnswerQuery) (c.AnswerResult, error) {
	out := c.AnswerResult{Engine: engine, Brands: []string{}, Cited: []c.AnswerPage{}, Read: []c.AnswerPage{}, Searches: []string{}}
	se := ""
	for _, e := range answerEngines {
		if e.ID == engine {
			se = e.se
		}
	}
	if se == "" {
		return out, c.Errf(c.ErrInvalid, "DataForSEO asks chatgpt or gemini, not %q", engine)
	}
	country := strings.ToUpper(q.Country)
	if country == "" {
		country = "US"
	}
	loc, ok := locations[country]
	if !ok {
		keys := make([]string, 0, len(locations))
		for k := range locations {
			keys = append(keys, k)
		}
		return out, c.Errf(c.ErrInvalid, "DataForSEO here covers %s; ask for one of those countries", strings.Join(sorted(keys), ", "))
	}
	lang := strings.ToLower(q.Language)
	if lang == "" {
		lang = loc.lang
	}
	prompt := strings.TrimSpace(q.Prompt)
	if r := []rune(prompt); len(r) > maxAnswerPrompt {
		prompt = string(r[:maxAnswerPrompt])
	}
	task := map[string]any{"keyword": prompt, "location_code": loc.code, "language_code": lang, "force_web_search": q.WebSearch}
	var resp dfsResponse[map[string]any]
	if err := d.call(ctx, cl, creds, "/v3/ai_optimization/"+se+"/llm_scraper/live/advanced", task, &resp, &out.Cost); err != nil {
		return out, err
	}
	result, err := resp.first("DataForSEO")
	if err != nil {
		return out, err
	}
	if result == nil {
		return out, c.Errf(c.ErrUnavailable, "DataForSEO returned no answer from %s", engine)
	}
	out.Model = str(result["model"])
	out.Text = str(result["markdown"])
	out.CheckURL = httpURL(str(result["check_url"]))
	seen := map[string]bool{}
	for _, b := range maps(result["brand_entities"]) {
		name := strings.TrimSpace(str(b["title"]))
		if name != "" && !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			out.Brands = append(out.Brands, name)
		}
	}
	out.Cited = answerPages(result["sources"])
	out.Read = answerPages(result["search_results"])
	for _, s := range anyStrings(result["fan_out_queries"]) {
		if s = strings.TrimSpace(s); s != "" {
			out.Searches = append(out.Searches, s)
		}
	}
	return out, nil
}

// answerPages reads a list of pages once each, in order. The domain comes
// from the URL, because DataForSEO's own domain field sometimes carries
// Markdown.
func answerPages(v any) []c.AnswerPage {
	out := []c.AnswerPage{}
	seen := map[string]bool{}
	for _, p := range maps(v) {
		u := httpURL(str(p["url"]))
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		parsed, _ := url.Parse(u)
		out = append(out, c.AnswerPage{URL: u, Domain: strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www."),
			Title: strings.TrimSpace(str(p["title"])), Position: len(out) + 1})
	}
	return out
}

// httpURL is s when it is a web address, or "".
func httpURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return ""
	}
	return u.String()
}

func maps(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
