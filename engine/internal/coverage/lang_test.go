package coverage

import "testing"

func TestEnglishURL(t *testing.T) {
	for _, c := range []struct{ in, want, lang string }{
		{"https://reviews.example/fr/isp-proxies", "https://reviews.example/isp-proxies", "fr"},
		{"https://reviews.example/isp-proxies", "https://reviews.example/isp-proxies", ""},
		{"https://reviews.example/en/isp-proxies", "https://reviews.example/isp-proxies", ""},
		{"https://reviews.example/pt-br/isp-proxies", "https://reviews.example/isp-proxies", "pt"},
		{"https://reviews.example/zh-hans/a", "https://reviews.example/a", "zh"},
		{"https://reviews.example/de", "https://reviews.example/", "de"},
		{"https://de.reviews.example/isp-proxies", "https://reviews.example/isp-proxies", "de"},
		{"https://reviews.example/isp-proxies?lang=es&x=1", "https://reviews.example/isp-proxies?x=1", "es"},
		// Paths that only look like a code are left alone.
		{"https://reviews.example/id/123", "https://reviews.example/id/123", ""},
		{"https://reviews.example/go/proxies", "https://reviews.example/go/proxies", ""},
		{"https://de.example/isp-proxies", "https://de.example/isp-proxies", ""},
		{"https://reviews.example/id-id/a", "https://reviews.example/a", "id"},
	} {
		got, lang := EnglishURL(c.in)
		if got != c.want || lang != c.lang {
			t.Errorf("EnglishURL(%q) = %q, %q; want %q, %q", c.in, got, lang, c.want, c.lang)
		}
	}
}
