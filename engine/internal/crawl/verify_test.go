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
