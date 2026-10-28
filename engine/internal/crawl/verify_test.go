package crawl

import "testing"

func TestSearchConsoleCovers(t *testing.T) {
	for _, c := range []struct {
		property, host string
		want           bool
	}{
		{"sc-domain:example.com", "example.com", true},
		{"sc-domain:example.com", "www.example.com", true},
		{"sc-domain:example.com", "shop.example.com:8443", true},
		{"sc-domain:example.com", "notexample.com", false},
		{"https://www.example.com/", "www.example.com", true},
		{"https://www.example.com/", "example.com", false},
		{"https://example.com/blog/", "example.com", true},
		{"sc-domain:", "example.com", false},
		{"not a url", "example.com", false},
	} {
		if got := SearchConsoleCovers(c.property, c.host); got != c.want {
			t.Errorf("SearchConsoleCovers(%q, %q) = %v", c.property, c.host, got)
		}
	}
}

func TestHasTrackingTag(t *testing.T) {
	page := []byte(`<head><script async src="https://app.example.com/site.js" data-key="ofk_test_key"></script></head>`)
	if !HasTrackingTag(page, []string{"ofk_other", "ofk_test_key"}) {
		t.Fatal("the workspace's tag proves the site")
	}
	if HasTrackingTag(page, []string{"ofk_other"}) || HasTrackingTag(page, nil) || HasTrackingTag(page, []string{""}) {
		t.Fatal("another workspace's tag proves nothing")
	}
	if HasTrackingTag([]byte(`<p>data-key="ofk_test_key"</p>`), []string{"ofk_test_key"}) {
		t.Fatal("the key alone, without the tag, proves nothing")
	}
}

func TestScope(t *testing.T) {
	set := DefaultSettings()
	set.Include, set.Exclude, set.MaxDepth, set.IgnoreParams = []string{"^/blog/"}, []string{"/draft"}, 3, true
	sc, err := set.Scope()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		in    string
		depth int
		start bool
		want  string
	}{
		{"https://example.com/?ref=x", 0, true, "https://example.com/"},
		{"https://example.com/blog/a?utm=1", 1, false, "https://example.com/blog/a"},
		{"https://example.com/blog/draft-1", 1, false, ""},
		{"https://example.com/about", 1, false, ""},
		{"https://example.com/blog/deep", 4, false, ""},
	} {
		got, ok := sc.URL(c.in, c.depth, c.start)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v", c.in, got, ok)
		}
	}
	if CheckPatterns("x", []string{"(["}) == nil || CheckPatterns("x", make([]string, MaxPatterns+1)) == nil {
		t.Fatal("bad patterns accepted")
	}
	if s, err := SettingsFrom([]byte(`{}`)); err != nil || !s.UseSitemaps || s.RenderJS != "auto" {
		t.Fatalf("old run: %v %v", s, err)
	}
}
