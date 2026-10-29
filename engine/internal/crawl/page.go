package crawl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Page is what a crawl keeps of an HTML page: where it links, whether it
// may be indexed or followed, and the on-page facts an SEO audit checks.
// Text is never stored, only counted and hashed.
type Page struct {
	// Links are the page's <a> and <area> links, absolute http(s) URLs
	// without fragments, in page order, each URL once.
	Links []Link
	// Noindex and Nofollow come from <meta name="robots"> or
	// <meta name="outflarebot">.
	Noindex, Nofollow bool
	// Meta holds the content of every <meta name=...> by lowercase name,
	// the first of each.
	Meta map[string]string

	Title           string
	TitleCount      int
	Description     string
	Canonical       string
	CanonicalCount  int
	H1              []string
	Lang            string
	Hreflang        []Hreflang
	SchemaTypes     []string
	WordCount       int
	ImagesNoAlt     int
	Images          int
	ContentHash     string
	MetaRefreshTo   string
	ViewportMissing bool
	// OGTitle and OGImage are the Open Graph tags link previews use.
	OGTitle, OGImage string
	// ArticleAuthor and ArticlePublished come from the page's JSON-LD
	// article (Article, NewsArticle, BlogPosting...): its first author's
	// name and when it was published, as written.
	ArticleAuthor, ArticlePublished string
	// InsecureResources counts what a https page loads over plain http:
	// scripts, styles, icons, images, media and frames.
	InsecureResources int
}

// Link is one link on a page.
type Link struct {
	URL      string `json:"url"`
	Anchor   string `json:"anchor"`
	Nofollow bool   `json:"nofollow"`
}

// Hreflang is one <link rel="alternate" hreflang> of a page.
type Hreflang struct {
	Lang string `json:"lang"`
	URL  string `json:"url"`
}

// Size limits for what is kept of a page.
const (
	maxTitle  = 500
	maxDesc   = 1000
	maxAnchor = 200
	maxH1     = 10
	maxSchema = 30
	maxHrefl  = 100
)

// ParsePage reads an HTML page fetched from base.
func ParsePage(base *url.URL, body []byte) Page {
	p := Page{Meta: map[string]string{}, ViewportMissing: true}
	type rawLink struct {
		href, rel string
		anchor    strings.Builder
	}
	var (
		links    []*rawLink
		open     *rawLink // the <a> whose text is being read
		inTitle  bool
		inH1     *strings.Builder
		skip     int // depth inside <script>, <style>, <noscript>, <template>
		svg      int // depth inside <svg>
		jsonLD   *strings.Builder
		text     strings.Builder
		canons   []string
		hrefl    [][2]string
		schemaSn = map[string]bool{}
	)
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.TextToken:
			t := z.Text()
			switch {
			case jsonLD != nil:
				jsonLD.Write(t)
			case skip > 0:
			default:
				s := string(t)
				if inTitle && p.TitleCount == 1 {
					p.Title += s
				}
				if inH1 != nil {
					inH1.WriteString(s)
				}
				if open != nil {
					open.anchor.WriteString(s)
				}
				// The words a reader sees: not the title or an icon's.
				if !inTitle && svg == 0 {
					text.WriteString(s)
					text.WriteByte(' ')
				}
			}
			continue
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script":
				if jsonLD != nil {
					for _, t := range schemaTypes(jsonLD.String()) {
						if !schemaSn[t] && len(p.SchemaTypes) < maxSchema {
							schemaSn[t] = true
							p.SchemaTypes = append(p.SchemaTypes, t)
						}
					}
					if p.ArticleAuthor == "" && p.ArticlePublished == "" {
						p.ArticleAuthor, p.ArticlePublished = articleInfo(jsonLD.String())
					}
					jsonLD = nil
				}
				skip = max(skip-1, 0)
			case "style", "noscript", "template":
				skip = max(skip-1, 0)
			case "title":
				inTitle = false
			case "svg":
				svg = max(svg-1, 0)
			case "h1":
				if inH1 != nil && len(p.H1) < maxH1 {
					p.H1 = append(p.H1, clip(squash(inH1.String()), maxTitle))
				}
				inH1 = nil
			case "a", "area":
				open = nil
			}
			continue
		case html.StartTagToken, html.SelfClosingTagToken:
		default:
			continue
		}
		name, hasAttr := z.TagName()
		attrs := map[string]string{}
		for hasAttr {
			k, v, more := z.TagAttr()
			key := strings.ToLower(string(k))
			if _, dup := attrs[key]; !dup {
				attrs[key] = string(v)
			}
			hasAttr = more
		}
		selfClosing := tt == html.SelfClosingTagToken
		if base.Scheme == "https" && skip == 0 {
			ref := ""
			switch string(name) {
			case "img", "script", "iframe", "source", "video", "audio", "embed":
				ref = attrs["src"]
			case "link":
				if rel := strings.ToLower(attrs["rel"]); strings.Contains(rel, "stylesheet") || strings.Contains(rel, "icon") {
					ref = attrs["href"]
				}
			}
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), "http://") {
				p.InsecureResources++
			}
		}
		switch string(name) {
		case "svg":
			if !selfClosing {
				svg++
			}
		case "html":
			if p.Lang == "" {
				p.Lang = clip(strings.TrimSpace(attrs["lang"]), 35)
			}
		case "script":
			if !selfClosing {
				skip++
				if strings.Contains(strings.ToLower(attrs["type"]), "ld+json") {
					jsonLD = &strings.Builder{}
				}
			}
		case "style", "noscript", "template":
			if !selfClosing {
				skip++
			}
		case "title":
			// Only the document's title, not an SVG's.
			if skip == 0 && svg == 0 {
				p.TitleCount++
				inTitle = !selfClosing
			}
		case "h1":
			if !selfClosing {
				inH1 = &strings.Builder{}
			}
		case "img":
			p.Images++
			// alt="" marks a decorative image; no alt at all is the problem.
			if _, ok := attrs["alt"]; !ok {
				p.ImagesNoAlt++
			}
		case "base":
			if h, ok := attrs["href"]; ok {
				if u, err := base.Parse(strings.TrimSpace(h)); err == nil {
					base = u
				}
			}
		case "link":
			rel := strings.ToLower(attrs["rel"])
			for _, r := range strings.Fields(rel) {
				switch r {
				case "canonical":
					canons = append(canons, attrs["href"])
				case "alternate":
					if hl := strings.TrimSpace(attrs["hreflang"]); hl != "" {
						hrefl = append(hrefl, [2]string{hl, attrs["href"]})
					}
				}
			}
		case "meta":
			if strings.EqualFold(attrs["http-equiv"], "refresh") {
				if _, after, ok := strings.Cut(strings.ToLower(attrs["content"]), "url="); ok {
					// Keep the original case of the URL.
					if c := attrs["content"]; len(after) <= len(c) {
						p.MetaRefreshTo = strings.Trim(strings.TrimSpace(c[len(c)-len(after):]), `'"`)
					}
				}
			}
			n := strings.ToLower(strings.TrimSpace(attrs["name"]))
			if n == "" {
				n = strings.ToLower(strings.TrimSpace(attrs["property"]))
			}
			if n == "" {
				continue
			}
			if n == "viewport" {
				p.ViewportMissing = false
			}
			if _, ok := p.Meta[n]; !ok {
				p.Meta[n] = attrs["content"]
			}
			if n == "robots" || n == strings.ToLower(Token) {
				noindex, nofollow := RobotsDirectives(attrs["content"])
				p.Noindex = p.Noindex || noindex
				p.Nofollow = p.Nofollow || nofollow
			}
		case "a", "area":
			if h, ok := attrs["href"]; ok {
				l := &rawLink{href: h, rel: strings.ToLower(attrs["rel"])}
				links = append(links, l)
				if string(name) == "a" && !selfClosing {
					open = l
				} else if alt := attrs["alt"]; alt != "" {
					l.anchor.WriteString(alt)
				}
			}
		}
		// An image inside a link names it by its alt text.
		if string(name) == "img" && open != nil && open.anchor.Len() == 0 {
			open.anchor.WriteString(attrs["alt"])
		}
	}
	p.Title = clip(squash(p.Title), maxTitle)
	p.Description = clip(squash(p.Meta["description"]), maxDesc)
	p.OGTitle = clip(squash(p.Meta["og:title"]), maxTitle)
	p.OGImage = clip(strings.TrimSpace(p.Meta["og:image"]), 2000)
	p.CanonicalCount = len(canons)
	if len(canons) > 0 {
		if u, ok := Normalize(base, canons[0]); ok {
			p.Canonical = u
		} else {
			p.Canonical = clip(strings.TrimSpace(canons[0]), 2000)
		}
	}
	for _, h := range hrefl {
		if len(p.Hreflang) >= maxHrefl {
			break
		}
		u, _ := Normalize(base, h[1])
		p.Hreflang = append(p.Hreflang, Hreflang{Lang: strings.ToLower(clip(h[0], 35)), URL: u})
	}
	// Links resolve against the final <base>, wherever it sits.
	seen := map[string]bool{}
	for _, l := range links {
		u, ok := Normalize(base, l.href)
		if !ok || seen[u] {
			continue
		}
		seen[u] = true
		nf := false
		for _, r := range strings.Fields(l.rel) {
			if r == "nofollow" || r == "ugc" || r == "sponsored" {
				nf = true
			}
		}
		p.Links = append(p.Links, Link{URL: u, Anchor: clip(squash(l.anchor.String()), maxAnchor), Nofollow: nf})
	}
	words := strings.FieldsFunc(text.String(), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
	p.WordCount = len(words)
	if len(words) > 0 {
		sum := sha256.Sum256([]byte(strings.ToLower(strings.Join(words, " "))))
		p.ContentHash = hex.EncodeToString(sum[:16])
	}
	return p
}

// RobotsDirectives reads a robots meta tag or X-Robots-Tag value.
func RobotsDirectives(v string) (noindex, nofollow bool) {
	for _, d := range strings.Split(strings.ToLower(v), ",") {
		d = strings.TrimSpace(d)
		// X-Robots-Tag may name an agent: "otherbot: noindex".
		if agent, rule, ok := strings.Cut(d, ":"); ok {
			if a := strings.TrimSpace(agent); a != strings.ToLower(Token) && a != "*" {
				continue
			}
			d = strings.TrimSpace(rule)
		}
		switch d {
		case "noindex":
			noindex = true
		case "nofollow":
			nofollow = true
		case "none":
			noindex, nofollow = true, true
		}
	}
	return noindex, nofollow
}

// schemaTypes lists the @type values in a JSON-LD block, including those
// in @graph and nested objects.
// articleTypes are the schema.org types of an article.
var articleTypes = map[string]bool{"Article": true, "NewsArticle": true, "BlogPosting": true, "ReportageNewsArticle": true,
	"AnalysisNewsArticle": true, "OpinionNewsArticle": true, "TechArticle": true, "ScholarlyArticle": true, "Report": true}

// articleInfo is the first article's first author's name and its
// datePublished in a JSON-LD block, or "".
func articleInfo(raw string) (author, published string) {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return "", ""
	}
	isArticle := func(m map[string]any) bool {
		switch ty := m["@type"].(type) {
		case string:
			return articleTypes[ty]
		case []any:
			for _, e := range ty {
				if s, ok := e.(string); ok && articleTypes[s] {
					return true
				}
			}
		}
		return false
	}
	name := func(a any) string {
		if l, ok := a.([]any); ok && len(l) > 0 {
			a = l[0]
		}
		switch t := a.(type) {
		case string:
			return t
		case map[string]any:
			if n, ok := t["name"].(string); ok {
				return n
			}
		}
		return ""
	}
	var walk func(any, int) bool
	walk = func(v any, depth int) bool {
		if depth > 6 {
			return false
		}
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				if walk(e, depth+1) {
					return true
				}
			}
		case map[string]any:
			if isArticle(t) {
				author = clip(squash(name(t["author"])), 200)
				published, _ = t["datePublished"].(string)
				published = clip(strings.TrimSpace(published), 40)
				return true
			}
			if g, ok := t["@graph"]; ok {
				return walk(g, depth+1)
			}
		}
		return false
	}
	walk(v, 0)
	return author, published
}

func schemaTypes(raw string) []string {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return nil
	}
	var out []string
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 6 {
			return
		}
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e, depth+1)
			}
		case map[string]any:
			switch ty := t["@type"].(type) {
			case string:
				out = append(out, ty)
			case []any:
				for _, e := range ty {
					if s, ok := e.(string); ok {
						out = append(out, s)
					}
				}
			}
			for k, e := range t {
				if k != "@type" && k != "@context" {
					walk(e, depth+1)
				}
			}
		}
	}
	walk(v, 0)
	return out
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Normalize resolves ref against base and tidies it for the crawl queue:
// http(s) only, lowercase host, no fragment, no default port, "/" for an
// empty path. It reports false for anything that is not a web page link
// (mailto:, javascript:, tel:).
func Normalize(base *url.URL, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return "", false
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	u.Fragment, u.RawFragment = "", ""
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) {
		u.Host = u.Host[:strings.LastIndexByte(u.Host, ':')]
	}
	if u.Path == "" {
		u.Path = "/"
	}
	s := u.String()
	if len(s) > 2000 {
		return "", false
	}
	return s, true
}
