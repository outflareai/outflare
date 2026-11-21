package linkorders

import (
	"slices"
	"testing"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

func TestFinderPagesAndAddresses(t *testing.T) {
	r := &crawl.Read{
		FinalURL: "https://news.example/",
		Emails:   []string{"datenschutz@news.example", "info@news.example"},
		Text:     "Werbung: werbung [at] news [dot] example. Redaktion: redaktion@news.example, privacy@news.example, us@other.example, write to us at example.com, jane​@gmail.com",
		Links: []crawl.Link{
			{URL: "https://news.example/impressum", Anchor: "Impressum"},
			{URL: "https://news.example/datenschutz", Anchor: "Datenschutz"},
			{URL: "https://www.news.example/kontakt/", Anchor: "Kontakt"},
			{URL: "https://other.example/contact", Anchor: "Contact"},
			{URL: "https://news.example/blog/post", Anchor: "A post"},
			{URL: "https://news.example/oglasevanje", Anchor: "Oglaševanje"},
		},
	}
	pages := contactPages(r, 5)
	var urls []string
	for _, p := range pages {
		urls = append(urls, p.url)
	}
	// The imprint and the advertising page first, then contact; never the
	// privacy page, another site or a post.
	if !slices.Equal(urls, []string{"https://news.example/impressum", "https://news.example/oglasevanje", "https://www.news.example/kontakt/"}) {
		t.Fatalf("pages: %v", urls)
	}
	scores := map[string]int{}
	for _, e := range pageEmails(r) {
		if s, ok := emailScore(e, "news.example"); ok {
			scores[e] = s
		}
	}
	want := map[string]int{"werbung@news.example": 95, "redaktion@news.example": 80, "info@news.example": 50, "jane@gmail.com": 20}
	if len(scores) != len(want) {
		t.Fatalf("addresses: %v", scores)
	}
	for e, s := range want {
		if scores[e] != s {
			t.Fatalf("%s: got %d, want %d (all: %v)", e, scores[e], s, scores)
		}
	}
	for _, never := range []string{"dpo@news.example", "abuse@news.example", "no-reply@news.example", "jobs@news.example", "legal.team@news.example"} {
		if _, ok := emailScore(never, "news.example"); ok {
			t.Errorf("%s must never be pitched", never)
		}
	}
	if _, ok := emailScore("info@mail.news.example", "news.example"); !ok {
		t.Error("a subdomain of the site is the site's own")
	}
}
