package api

import (
	"strings"
	"testing"
)

func TestChunkKnowledge(t *testing.T) {
	if c := chunkKnowledge("snippet", "Free trial?", "Yes, 7 days."); len(c) != 1 || c[0].text != "Q: Free trial?\nA: Yes, 7 days." {
		t.Fatalf("snippet = %v", c)
	}
	body := "Intro line.\n\n## Refunds\n\nWithin 30 days.\n\n```\n## not a heading\n```\n\n### How to cancel\n\n1. Settings\n2. Billing"
	c := chunkKnowledge("article", "Refunds", body)
	if len(c) != 3 || c[0].heading != "" || c[1].heading != "Refunds" || !strings.Contains(c[1].text, "## not a heading") || c[2].heading != "How to cancel" {
		t.Fatalf("article = %#v", c)
	}
	long := strings.Repeat("A sentence about refunds that goes on. ", 100)
	c = chunkKnowledge("article", "Long", "## Big\n\n"+long+"\n\n"+long)
	if len(c) < 3 {
		t.Fatalf("long section split into %d", len(c))
	}
	for _, x := range c {
		if len([]rune(x.text)) > knowledgeChunkRunes+50 || x.heading != "Big" {
			t.Fatalf("passage of %d runes under %q", len([]rune(x.text)), x.heading)
		}
	}
	if c := chunkKnowledge("article", "Just a title", ""); len(c) != 1 || c[0].text != "Just a title" {
		t.Fatalf("empty = %v", c)
	}
}

func TestKnowledgeWords(t *testing.T) {
	if got := knowledgeWords("Do you refund annual plans? 'or' DROP"); got != "'refund':* | 'annual':* | 'plans':* | 'drop':*" {
		t.Fatalf("words = %q", got)
	}
	if got := knowledgeWords("what is it?"); got != "" {
		t.Fatalf("stop words = %q", got)
	}
}

func TestKnowledgeExcerpt(t *testing.T) {
	for body, want := range map[string]string{
		"## Plans\n\n| Plan | Price |\n| --- | --- |\n| Pro | $149 |\n\n## Trial\n\nEvery plan starts with a **14-day** trial.": "Every plan starts with a 14-day trial.",
		"## Standard\n\n- Annual billing: 2 months free.\n- [ ] Ask a manager\n1. Step one":                                     "Annual billing: 2 months free. Ask a manager Step one",
		"## Only a heading": "Only a heading",
	} {
		if got := knowledgeExcerpt(body); got != want {
			t.Errorf("excerpt(%q) = %q, want %q", body, got, want)
		}
	}
}
