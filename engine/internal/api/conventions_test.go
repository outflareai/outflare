package api

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"

	spec "github.com/outflareai/outflare/api"
)

// The API document follows docs/api-conventions.md, so the SDKs, the
// CLI and the MCP tools can rely on it. This test holds every operation
// to the rules a reviewer would otherwise have to remember.

// Lists that take limit but do not page, and why.
var unpagedLists = map[string]string{
	"getAnalyticsHistory":       "a time series bounded by from and to",
	"getSEOHistory":             "a time series bounded by from and to",
	"listAdCampaigns":           "a ranking by spend over from and to, capped at 1000",
	"listEmailBisonCompatCalls": "mirrors the EmailBison API's own shape",
	"getSiteSearch":             "the top rows of a report bounded by from and to, with totals of every row",
	"getSiteCannibalisation":    "the most shown searches, at most 200; the fixes list holds the top 50",
	"getSiteConversions":        "landing pages, at most 1,000, best first; totals cover the rest",
	"getSiteRegressions":        "the latest regressions, at most 500; open ones are also on the fixes list",
	"getSiteIndexing":           "the worst pages first, with a summary that counts every page",
	"listSocialPosts":           "the top posts of a period bounded by days, in the chosen order",
	"listVisitors":              "a ranking by score over days, at most 200, with totals of every visitor",
	"searchWorkspace":           "the best matches, at most 100, ranked: refine the words to find others",
	"findKnowledgePassages":     "the best passages for a question, at most 20, ranked: ask in other words to find others",
}

func TestDocumentFollowsConventions(t *testing.T) {
	var doc struct {
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Parameters map[string]map[string]any `yaml:"parameters"`
			Schemas    map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	ref := func(v any, kind string) map[string]any {
		m, _ := v.(map[string]any)
		if r, ok := m["$ref"].(string); ok {
			name := strings.TrimPrefix(r, "#/components/"+kind+"/")
			if kind == "parameters" {
				return doc.Components.Parameters[name]
			}
			return doc.Components.Schemas[name]
		}
		return m
	}
	snake := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	var problems []string
	add := func(id, msg string) { problems = append(problems, id+": "+msg) }

	for path, item := range doc.Paths {
		shared, _ := item["parameters"].([]any)
		for method, v := range item {
			if !slices.Contains([]string{"get", "post", "put", "patch", "delete"}, method) {
				continue
			}
			op := v.(map[string]any)
			id, _ := op["operationId"].(string)
			if id == "" {
				add(method+" "+path, "no operationId")
				continue
			}
			if s, _ := op["summary"].(string); s == "" {
				add(id, "no summary")
			}
			if tags, _ := op["tags"].([]any); len(tags) == 0 {
				add(id, "no tags")
			}
			responses, _ := op["responses"].(map[string]any)
			if _, ok := responses["default"]; !ok {
				add(id, `no default response (default: { $ref: "#/components/responses/Error" })`)
			}
			own, _ := op["parameters"].([]any)
			names := map[string]bool{}
			for _, p := range append(slices.Clone(shared), own...) {
				p := ref(p, "parameters")
				name, _ := p["name"].(string)
				names[name] = true
				if p["in"] == "query" && !snake.MatchString(name) {
					add(id, "query parameter "+name+" is not snake_case")
				}
			}
			if method != "get" {
				continue
			}
			var props map[string]any
			if ok, _ := responses["200"].(map[string]any); ok != nil {
				content, _ := ok["content"].(map[string]any)
				js, _ := content["application/json"].(map[string]any)
				props, _ = ref(js["schema"], "schemas")["properties"].(map[string]any)
			}
			_, isList := props["data"]
			if isList && names["limit"] && unpagedLists[id] == "" {
				if !names["cursor"] {
					add(id, "a list with limit must page: take #/components/parameters/Cursor")
				}
				if _, ok := props["next_cursor"]; !ok {
					add(id, "a paged list must answer next_cursor")
				}
			}
			if names["cursor"] && !names["fields"] {
				add(id, "a paged list takes #/components/parameters/Fields")
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
