package news

import "testing"

func TestArticleURL(t *testing.T) {
	for _, c := range []struct{ in, url, domain string }{
		{"https://News.Example.org/2026/10/proxies?utm_source=x&id=7#top", "https://news.example.org/2026/10/proxies?id=7", "news.example.org"},
		{"http://www.example.com", "http://www.example.com/", "example.com"},
		{"https://example.com/a?fbclid=1", "https://example.com/a", "example.com"},
		{"ftp://example.com/a", "", ""},
		{"https://user@example.com/a", "", ""},
		{"not a url", "", ""},
	} {
		u, d := ArticleURL(c.in)
		if u != c.url || d != c.domain {
			t.Errorf("ArticleURL(%q) = %q, %q; want %q, %q", c.in, u, d, c.url, c.domain)
		}
	}
}

func TestOwnSite(t *testing.T) {
	if !OwnSite("https://www.northpeak.example/", "blog.northpeak.example") || !OwnSite("northpeak.example", "northpeak.example") {
		t.Fatal("own site and its subdomains")
	}
	if OwnSite("https://northpeak.example", "notnorthpeak.example") || OwnSite("", "example.com") {
		t.Fatal("another site")
	}
}

func TestMentions(t *testing.T) {
	for _, c := range []struct {
		query, title, snippet string
		want                  bool
	}{
		{`"Northpeak"`, "Northpeak raises a seed round", "", true},
		{"Northpeak proxies", "Why proxies matter", "Northpeak's CEO says...", true},
		{"Northpeak", "Northpeaks of Scotland", "", false},
		{"Northpeak -jobs", "Jobs at a firm", "Northpeak, the proxy company", true},
		{"Northpeak site:example.org", "Northpeak in the news", "", true},
		{"Northpeak", "Unrelated", "nothing here", false},
		{"OR", "anything", "", false},
	} {
		if got := Mentions(c.query, c.title, c.snippet); got != c.want {
			t.Errorf("Mentions(%q, %q, %q) = %v", c.query, c.title, c.snippet, got)
		}
	}
}
