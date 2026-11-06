package seo

import (
	"context"
	"math"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// DataForSEO's AI demand data: LLM Mentions, a store of real questions
// people put to ChatGPT (in the US, in English) and the answers Google's AI
// Overviews gave, searchable by the domains and words the answers mention;
// and AI keyword data, how many times a month people ask AI assistants
// about a phrase.

// maxDemandTargets is how many domains and keywords one question search
// sends; maxDemandQuestions how many questions it returns; maxVolumePhrases
// how many phrases one volume call reads.
const (
	maxDemandTargets   = 10
	maxDemandQuestions = 100
	maxVolumePhrases   = 200
)

// EstimateAIQuestions follows DataForSEO's price list for LLM Mentions'
// search: about $0.10 a call with up to 100 questions. The recorded cost is
// what DataForSEO reports.
func (DataForSEO) EstimateAIQuestions() c.SEOCost { return c.SEOCost{USD: 0.1, Estimated: true} }

// EstimateAIVolume follows its price list for AI keyword data: about $0.01
// a call and $0.0001 a phrase.
func (DataForSEO) EstimateAIVolume(phrases int) c.SEOCost {
	return c.SEOCost{USD: 0.01 + 0.0001*float64(phrases), Estimated: true}
}

// demandLocation is DataForSEO's location and language for a country.
func demandLocation(country, language string) (int, string, error) {
	country = strings.ToUpper(country)
	if country == "" {
		country = "US"
	}
	loc, ok := locations[country]
	if !ok {
		keys := make([]string, 0, len(locations))
		for k := range locations {
			keys = append(keys, k)
		}
		return 0, "", c.Errf(c.ErrInvalid, "DataForSEO here covers %s; ask for one of those countries", strings.Join(sorted(keys), ", "))
	}
	lang := strings.ToLower(language)
	if lang == "" {
		lang = loc.lang
	}
	return loc.code, lang, nil
}

// AIQuestions finds the questions, most asked first, whose answers mention
// any of the domains (or their pages) or keywords.
func (d DataForSEO) AIQuestions(ctx context.Context, cl *c.Client, creds c.Creds, q c.AIQuestionQuery) (c.AIQuestions, error) {
	out := c.AIQuestions{Questions: []c.AIQuestion{}}
	code, lang, err := demandLocation(q.Country, q.Language)
	if err != nil {
		return out, err
	}
	targets := []map[string]any{}
	for _, dom := range q.Domains {
		if dom = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(dom)), "www."); dom != "" && len(targets) < maxDemandTargets {
			targets = append(targets, map[string]any{"domain": dom, "include_subdomains": true})
		}
	}
	for _, k := range q.Keywords {
		if k = strings.TrimSpace(k); k != "" && len(targets) < maxDemandTargets {
			targets = append(targets, map[string]any{"keyword": k})
		}
	}
	if len(targets) == 0 {
		return out, c.Errf(c.ErrInvalid, "say which domains or words the answers mention")
	}
	limit := q.Limit
	if limit <= 0 || limit > maxDemandQuestions {
		limit = maxDemandQuestions
	}
	task := map[string]any{"target": targets, "location_code": code, "language_code": lang,
		"order_by": []string{"ai_search_volume,desc"}, "limit": limit}
	// ChatGPT's questions are only kept for the US in English; elsewhere
	// only Google's AI Overviews are.
	if code != locations["US"].code || lang != "en" {
		task["platform"] = "google"
	}
	var resp dfsResponse[map[string]any]
	if err := d.call(ctx, cl, creds, "/v3/ai_optimization/llm_mentions/search_mentions/live", task, &resp, &out.Cost); err != nil {
		return out, err
	}
	result, err := resp.first("DataForSEO")
	if err != nil || result == nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, it := range items(result) {
		text := strings.Join(strings.Fields(str(it["question"])), " ")
		key := strings.ToLower(text)
		if text == "" || seen[key] {
			continue
		}
		seen[key] = true
		platform := "chatgpt"
		if str(it["platform"]) == "google" {
			platform = "ai_overview"
		}
		qn := c.AIQuestion{Question: text, Platform: platform, Volume: count(it["ai_search_volume"]), Brands: []string{}}
		names := map[string]bool{}
		for _, b := range maps(it["brand_entities"]) {
			if n := strings.TrimSpace(str(b["title"])); n != "" && !names[strings.ToLower(n)] {
				names[strings.ToLower(n)] = true
				qn.Brands = append(qn.Brands, n)
			}
		}
		out.Questions = append(out.Questions, qn)
	}
	return out, nil
}

// AIVolume reads how many times a month people ask AI assistants about
// each phrase.
func (d DataForSEO) AIVolume(ctx context.Context, cl *c.Client, creds c.Creds, phrases []string, country, language string) (c.AIVolumes, error) {
	out := c.AIVolumes{Volume: map[string]int{}}
	code, lang, err := demandLocation(country, language)
	if err != nil {
		return out, err
	}
	keywords := []string{}
	seen := map[string]bool{}
	for _, p := range phrases {
		p = strings.ToLower(strings.Join(strings.Fields(p), " "))
		if r := []rune(p); len(r) > 200 {
			p = string(r[:200])
		}
		if p != "" && !seen[p] && len(keywords) < maxVolumePhrases {
			seen[p] = true
			keywords = append(keywords, p)
		}
	}
	if len(keywords) == 0 {
		return out, nil
	}
	var resp dfsResponse[map[string]any]
	if err := d.call(ctx, cl, creds, "/v3/ai_optimization/ai_keyword_data/keywords_search_volume/live",
		map[string]any{"keywords": keywords, "location_code": code, "language_code": lang}, &resp, &out.Cost); err != nil {
		return out, err
	}
	result, err := resp.first("DataForSEO")
	if err != nil || result == nil {
		return out, err
	}
	for _, it := range items(result) {
		k := strings.ToLower(strings.Join(strings.Fields(str(it["keyword"])), " "))
		if v := num(it["ai_search_volume"]); k != "" && v != nil {
			out.Volume[k] = count(*v)
		}
	}
	return out, nil
}

// count is a non-negative whole number from v, or 0.
func count(v any) int {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	default:
		if p := num(v); p != nil {
			f = *p
		}
	}
	if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int(math.Round(f))
}

// maxBenchmarkTargets is how many brands one benchmark counts.
const maxBenchmarkTargets = 10

// EstimateAIBenchmark follows DataForSEO's price list for LLM Mentions'
// multi-target metrics: about $0.10 a call and $0.02 a target.
func (DataForSEO) EstimateAIBenchmark(targets int) c.SEOCost {
	return c.SEOCost{USD: 0.1 + 0.02*float64(targets), Estimated: true}
}

// AIBenchmark counts, for each target, the stored ChatGPT and AI Overview
// answers that mention its domains or names.
func (d DataForSEO) AIBenchmark(ctx context.Context, cl *c.Client, creds c.Creds, targets []c.AIBenchmarkTarget, country, language string) (c.AIBenchmark, error) {
	out := c.AIBenchmark{Counts: map[string]c.AIBenchmarkCount{}}
	code, lang, err := demandLocation(country, language)
	if err != nil {
		return out, err
	}
	list := []map[string]any{}
	for _, t := range targets {
		el := []map[string]any{}
		for _, dom := range t.Domains {
			if dom = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(dom)), "www."); dom != "" && len(el) < maxDemandTargets {
				el = append(el, map[string]any{"domain": dom, "include_subdomains": true})
			}
		}
		for _, k := range t.Keywords {
			if k = strings.TrimSpace(k); k != "" && len(el) < maxDemandTargets {
				el = append(el, map[string]any{"keyword": k})
			}
		}
		if t.Key != "" && len(el) > 0 && len(list) < maxBenchmarkTargets {
			list = append(list, map[string]any{"key": t.Key, "target": el})
		}
	}
	if len(list) == 0 {
		return out, c.Errf(c.ErrInvalid, "say which brands to count")
	}
	task := map[string]any{"targets": list, "location_code": code, "language_code": lang}
	if code != locations["US"].code || lang != "en" {
		task["platform"] = "google"
	}
	var resp dfsResponse[map[string]any]
	if err := d.call(ctx, cl, creds, "/v3/ai_optimization/llm_mentions/multi_target_metrics/live", task, &resp, &out.Cost); err != nil {
		return out, err
	}
	result, err := resp.first("DataForSEO")
	if err != nil || result == nil {
		return out, err
	}
	for _, it := range items(result) {
		key := str(it["key"])
		if key == "" {
			continue
		}
		total, _ := it["total"].(map[string]any)
		n := c.AIBenchmarkCount{Mentions: count(total["mentions"]), AISearchVolume: int64(count(total["ai_search_volume"])), Platforms: map[string]int{}}
		for _, p := range maps(it["platform"]) {
			name := "chatgpt"
			if str(p["key"]) == "google" {
				name = "ai_overview"
			}
			n.Platforms[name] += count(p["mentions"])
		}
		out.Counts[key] = n
	}
	return out, nil
}
