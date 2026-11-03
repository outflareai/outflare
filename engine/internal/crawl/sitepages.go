package crawl

import (
	"bytes"
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// SitePage is one page read for its words: its address after same-site
// redirects, title and readable text with paragraph breaks.
type SitePage struct {
	URL   string
	Title string
	Text  string
}

var locTag = regexp.MustCompile(`(?i)<loc>\s*([^<\s]+)\s*</loc>`)

// skipPath leaves out addresses that aren't pages to learn from: files,
// feeds, tag and paging lists, carts and accounts.
var skipPath = regexp.MustCompile(`(?i)(\.(pdf|jpe?g|png|gif|svg|webp|zip|xml|json|css|js|mp4|mp3)$)|/(tag|tags|category|author|page|feed|cart|checkout|account|login|signin|sign-in|signup|wp-admin|wp-json)(/|$)`)

// SitePages lists up to max pages of the site at base worth reading: the
// sitemap's (following a sitemap index), else the links from the home page
// and the pages they lead to. Only pages on the same site, never files.
func SitePages(ctx context.Context, f Fetcher, wait func(context.Context, time.Duration) error, base string, max int) ([]string, error) {
	l, err := ListSite(ctx, f, wait, base, max)
	return l.Pages, err
}

// SiteList is what ListSite found.
type SiteList struct {
	Pages []string
	// Complete says the list is every page of the site: it came from
	// sitemaps that were all read, and max didn't cut it short. Only then
	// is a page missing from it gone from the site.
	Complete bool
}

// ListSite lists the site's pages like SitePages. It reads the sitemaps
// robots.txt names (Sitemap: lines) and the usual /sitemap.xml and
// /sitemap_index.xml, following sitemap indexes, and says whether the list
// is complete.
func ListSite(ctx context.Context, f Fetcher, wait func(context.Context, time.Duration) error, base string, max int) (SiteList, error) {
	start, err := url.Parse(base)
	if err != nil || start.Host == "" {
		return SiteList{}, ErrNotPage
	}
	root := start.Scheme + "://" + start.Host
	var out []string
	seen := map[string]bool{}
	full := false
	add := func(raw string) bool {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !sameSite(start, u) || skipPath.MatchString(u.Path) {
			return false
		}
		u.Fragment, u.RawQuery = "", ""
		k := u.String()
		if seen[k] {
			return false
		}
		if len(out) >= max {
			full = true
			return false
		}
		seen[k] = true
		out = append(out, k)
		return true
	}
	// The sitemaps robots.txt names, the usual places, and up to 20 more
	// that an index names.
	var maps []string
	addMap := func(m string) {
		m = strings.TrimSpace(m)
		u, err := url.Parse(m)
		if err != nil || !sameSite(start, u) || slices.Contains(maps, m) {
			return
		}
		maps = append(maps, m)
	}
	if r, err := f.Fetch(ctx, root+"/robots.txt"); err == nil && r.Status == 200 {
		for _, line := range strings.Split(string(r.Body), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "sitemap") {
				addMap(v)
			}
		}
	}
	addMap(root + "/sitemap.xml")
	addMap(root + "/sitemap_index.xml")
	read, found := 0, false
	for i := 0; i < len(maps) && i < 24 && len(out) < max; i++ {
		read++
		r, err := f.Fetch(ctx, maps[i])
		if err != nil || r.Status != 200 {
			continue
		}
		index := bytes.Contains(bytes.ToLower(r.Body), []byte("<sitemapindex"))
		if !index && !bytes.Contains(bytes.ToLower(r.Body), []byte("<urlset")) {
			continue
		}
		found = true
		for _, m := range locTag.FindAllSubmatch(r.Body, 50000) {
			if index {
				addMap(string(m[1]))
			} else {
				add(string(m[1]))
			}
		}
	}
	if len(out) > 0 {
		return SiteList{Pages: out, Complete: found && !full && read == len(maps)}, nil
	}
	// No sitemap: the home page's links, then theirs.
	add(root + "/")
	for i := 0; i < len(out) && i < 25 && len(out) < max; i++ {
		rd, err := ReadPage(ctx, f, wait, out[i])
		if err != nil || rd.Status != 200 {
			continue
		}
		for _, l := range rd.Links {
			add(l.URL)
		}
	}
	return SiteList{Pages: out}, nil
}

// ReadSitePages reads pages of one site politely: robots.txt once, its
// crawl delay before each page, same-site redirects only. A page that
// can't be read is left out; pages keep their order.
func ReadSitePages(ctx context.Context, f Fetcher, wait func(context.Context, time.Duration) error, pages []string, maxText int) ([]SitePage, error) {
	if len(pages) == 0 {
		return nil, nil
	}
	start, err := url.Parse(pages[0])
	if err != nil || start.Host == "" {
		return nil, ErrNotPage
	}
	site, err := readSite(ctx, f, start)
	if err != nil {
		return nil, err
	}
	var out []SitePage
	for _, raw := range pages {
		cur, err := url.Parse(raw)
		if err != nil || !sameSite(start, cur) {
			continue
		}
		for range 4 {
			if !site.allowed(cur) {
				break
			}
			if err := wait(ctx, site.delay); err != nil {
				return out, err
			}
			resp, err := f.Fetch(ctx, cur.String())
			if err != nil {
				break
			}
			if resp.Status >= 300 && resp.Status < 400 {
				next, ok := Normalize(cur, resp.Header.Get("Location"))
				nu, err := url.Parse(next)
				if !ok || err != nil || !sameSite(start, nu) {
					break
				}
				cur = nu
				continue
			}
			if resp.Status == 200 && readable(resp.ContentType()) {
				pg := ParsePage(cur, resp.Body)
				if pg.Noindex {
					break
				}
				title := clip(strings.TrimSpace(pg.Title), 300)
				if title == "" && len(pg.H1) > 0 {
					title = clip(pg.H1[0], 300)
				}
				out = append(out, SitePage{URL: cur.String(), Title: title, Text: PageText(resp.Body, maxText)})
			}
			break
		}
	}
	return out, nil
}

// blocks start a new line in PageText; skipped are left out with all
// they hold, since they are the site's furniture, not what a page says.
var (
	blocks  = map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "section": true, "article": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "dt": true, "dd": true, "blockquote": true, "pre": true, "table": true, "ul": true, "ol": true}
	skipped = map[string]bool{"script": true, "style": true, "noscript": true, "svg": true, "nav": true, "header": true, "footer": true, "form": true, "button": true, "template": true, "iframe": true, "aside": true}
	blanks  = regexp.MustCompile(`\n{3,}`)
	inline  = regexp.MustCompile(`[ \t\r\f\v]+`)
	// listGap joins list items, which blocks would otherwise space apart.
	listGap = regexp.MustCompile(`(- [^\n]*)\n\n(?:- )`)
)

// PageText is a page's readable text: the main content when the page
// marks it (main or article), else the body, without menus, headers,
// footers and scripts, with a blank line between blocks and headings
// marked with #. At most max runes.
func PageText(body []byte, max int) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	var find func(n *html.Node, tag string) *html.Node
	find = func(n *html.Node, tag string) *html.Node {
		if n.Type == html.ElementNode && n.Data == tag {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if f := find(c, tag); f != nil {
				return f
			}
		}
		return nil
	}
	root := find(doc, "main")
	if root == nil {
		root = find(doc, "article")
	}
	if root == nil {
		root = find(doc, "body")
	}
	if root == nil {
		root = doc
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skipped[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		block := n.Type == html.ElementNode && blocks[n.Data]
		if block {
			b.WriteString("\n\n")
			if len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6' {
				b.WriteString(strings.Repeat("#", int(n.Data[1]-'0')) + " ")
			}
			if n.Data == "li" {
				b.WriteString("- ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			b.WriteString("\n\n")
		}
	}
	walk(root)
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(inline.ReplaceAllString(l, " "))
	}
	text := strings.TrimSpace(blanks.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	for {
		joined := listGap.ReplaceAllString(text, "$1\n- ")
		if joined == text {
			break
		}
		text = joined
	}
	return clip(text, max)
}
