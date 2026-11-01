package seointel

import "github.com/outflareai/outflare/engine/internal/crawl"

func init() {
	r := crawl.Rule{ID: "search_recommendation", Severity: crawl.SevNotice, Category: "search", Title: "Review a saved search recommendation", Why: "A person saved this finding from a search report and added it to the shared work list.", Fix: "Read the saved evidence and recommendation in Search insights. Review the page, make a small change, and measure it with Rankings or Experiments."}
	Rules = append(Rules, r)
	ruleByID[r.ID] = r
}
