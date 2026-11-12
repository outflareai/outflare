package crawl

import (
	"bytes"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// A site's logo and main colour, read from its home page so a brand's look
// can start from them instead of from nothing.

// jsonLDLogo finds the logo a page's JSON-LD names for its organisation:
// "logo": "https://..." or "logo": {"url": "https://..."}.
var jsonLDLogo = regexp.MustCompile(`"logo"\s*:\s*(?:"([^"]+)"|\{[^{}]*?"(?:url|contentUrl)"\s*:\s*"([^"]+)")`)

var hexColor = regexp.MustCompile(`^#([0-9a-f]{3}|[0-9a-f]{6})$`)

// PageLook is the logo and colour a page offers.
type PageLook struct {
	// Logos are https addresses that may be the site's logo, best first:
	// the organisation's logo in its JSON-LD, then an SVG icon, the
	// apple-touch-icon, the largest other icon, and /favicon.ico last.
	Logos []string
	// ThemeColor is <meta name="theme-color"> as a lowercase hex colour, or
	// "" when it is missing or not a hex colour.
	ThemeColor string
}

// ReadLook reads a page's logo candidates and theme colour. base is the
// page's own address.
func ReadLook(base *url.URL, body []byte) PageLook {
	var look PageLook
	seen := map[string]bool{}
	add := func(raw string) {
		u, err := base.Parse(strings.TrimSpace(html.UnescapeString(raw)))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || len(u.String()) > 2000 {
			return
		}
		// The look is shown on https pages: an http logo would be blocked.
		u.Scheme, u.Fragment = "https", ""
		if s := u.String(); !seen[s] {
			seen[s] = true
			look.Logos = append(look.Logos, s)
		}
	}
	for _, m := range jsonLDLogo.FindAllSubmatch(body, 3) {
		add(string(m[1]) + string(m[2]))
	}
	var svg, touch, best string
	bestSize := -1
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, more := z.TagName()
		tag := string(name)
		if tag == "body" {
			break
		}
		if tag != "link" && tag != "meta" && tag != "base" {
			continue
		}
		attrs := map[string]string{}
		for more {
			var k, v []byte
			k, v, more = z.TagAttr()
			attrs[strings.ToLower(string(k))] = string(v)
		}
		switch tag {
		case "base":
			if u, err := base.Parse(strings.TrimSpace(attrs["href"])); err == nil && attrs["href"] != "" {
				base = u
			}
		case "meta":
			if strings.EqualFold(strings.TrimSpace(attrs["name"]), "theme-color") && look.ThemeColor == "" {
				if c := strings.ToLower(strings.TrimSpace(attrs["content"])); hexColor.MatchString(c) {
					look.ThemeColor = c
				}
			}
		case "link":
			href := strings.TrimSpace(attrs["href"])
			if href == "" || strings.HasPrefix(href, "data:") {
				continue
			}
			rel := " " + strings.ToLower(attrs["rel"]) + " "
			switch {
			case strings.Contains(rel, " apple-touch-icon"):
				if touch == "" {
					touch = href
				}
			case strings.Contains(rel, " icon ") || strings.Contains(rel, " mask-icon "):
				if strings.Contains(rel, " mask-icon ") {
					// A one-colour Safari pin: a shape, not the logo's look.
					continue
				}
				if svg == "" && (strings.Contains(strings.ToLower(attrs["type"]), "svg") || strings.HasSuffix(strings.ToLower(strings.SplitN(href, "?", 2)[0]), ".svg")) {
					svg = href
					continue
				}
				if s := iconSize(attrs["sizes"]); s > bestSize {
					best, bestSize = href, s
				}
			}
		}
	}
	for _, h := range []string{svg, touch, best} {
		if h != "" {
			add(h)
		}
	}
	add("/favicon.ico")
	return look
}

// iconSize is the largest width in a sizes attribute ("32x32 64x64"), 0
// when it names none ("any" counts as large: it is a scalable icon).
func iconSize(sizes string) int {
	n := 0
	for _, s := range strings.Fields(strings.ToLower(sizes)) {
		if s == "any" {
			return 1 << 20
		}
		w, _, _ := strings.Cut(s, "x")
		if v, err := strconv.Atoi(w); err == nil && v > n {
			n = v
		}
	}
	return n
}
