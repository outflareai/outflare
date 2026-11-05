package coverage

import "testing"

func TestClassify(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=abc":                 "youtube",
		"https://youtu.be/abc":                                "youtube",
		"https://smithery.ai/server/example":                  "mcp_directory",
		"https://glama.ai/mcp/servers/example":                "mcp_directory",
		"https://glama.ai/blog/post":                          "blog",
		"https://apps.apple.com/app/example/id1":              "app_listing",
		"https://slack.com/apps/A1-example":                   "app_listing",
		"https://slack.com/blog/news":                         "blog",
		"https://zapier.com/apps/example/integrations":        "app_listing",
		"https://www.g2.com/products/example/reviews":         "review",
		"https://www.producthunt.com/products/example":        "directory",
		"https://www.reddit.com/r/example/comments/1/thread/": "forum",
		"https://community.example.com/t/thread":              "forum",
		"https://www.instagram.com/p/abc/":                    "influencer",
		"https://www.linkedin.com/posts/ana_example":          "influencer",
		"https://www.linkedin.com/company/example":            "",
		"https://example.com/acme-vs-northpeak":               "comparison",
		"https://example.com/best-crm-tools":                  "roundup",
		"https://example.com/acme-alternatives":               "roundup",
		"https://example.com/blog/our-stack":                  "blog",
		"https://example.com/":                                "",
		"not a url":                                           "",
	} {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q) = %q, want %q", in, got, want)
		}
	}
}
