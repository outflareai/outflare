package seointel

import (
	"net/url"
	"strings"
)

// organicLocales are the first path segments treated as a language prefix
// when attributing a landing page to a search group. They match the
// prefixes the brand's localized sites use; anything else stays as-is.
var organicLocales = map[string]bool{
	"zh": true, "fr": true, "es": true, "pt": true, "id": true, "tr": true, "ja": true,
}

// NormalizeOrganicPath lowercases a path or URL, keeps only the path, and
// strips a trailing slash except on the site root.
func NormalizeOrganicPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "/"
	}
	if strings.Contains(p, "://") || strings.HasPrefix(p, "//") {
		if u, err := url.Parse(p); err == nil && u.Path != "" {
			p = u.Path
		}
	}
	p = strings.ToLower(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		return "/"
	}
	return p
}

// CanonicalOrganicPath is NormalizeOrganicPath with a known locale prefix
// removed, so /fr/pricing and /pricing join the same search group.
func CanonicalOrganicPath(raw string) string {
	p := NormalizeOrganicPath(raw)
	rest := strings.TrimPrefix(p, "/")
	seg, next, ok := strings.Cut(rest, "/")
	if !organicLocales[seg] {
		return p
	}
	if !ok || next == "" {
		return "/"
	}
	return "/" + next
}

// OrganicHost lowercases a host or, when it is empty, the host of a URL.
func OrganicHost(host, rawURL string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(h, ':'); i > 0 {
		h = h[:i]
	}
	if h != "" {
		return h
	}
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
