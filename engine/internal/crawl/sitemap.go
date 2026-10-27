package crawl

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// Sitemap limits from sitemaps.org: 50,000 URLs and 50 MB uncompressed.
const (
	maxSitemapURLs  = 50000
	maxSitemapBytes = 50 << 20
)

// ParseSitemap reads a sitemap (a urlset) or a sitemap index, gzipped or
// not, and returns the page URLs and the further sitemaps it lists. A
// plain-text sitemap (one URL a line) is read too.
func ParseSitemap(body []byte) (pages, sitemaps []string, err error) {
	entries, sitemaps, err := ParseSitemapEntries(body)
	for _, e := range entries {
		pages = append(pages, e.URL)
	}
	return pages, sitemaps, err
}

// SitemapPage is one page a sitemap lists, with its lastmod when given.
type SitemapPage struct {
	URL     string
	Lastmod string
}

// ParseSitemapEntries is ParseSitemap with each page's lastmod.
func ParseSitemapEntries(body []byte) (pages []SitemapPage, sitemaps []string, err error) {
	if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		body, err = io.ReadAll(io.LimitReader(zr, maxSitemapBytes))
		if err != nil {
			return nil, nil, err
		}
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] != '<' {
		for _, line := range strings.Split(string(trimmed), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
				pages = append(pages, SitemapPage{URL: line})
				if len(pages) >= maxSitemapURLs {
					break
				}
			}
		}
		return pages, nil, nil
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	var parent, mod string
	inLoc, inMod := false, false
	// urlAt is where the current <url>'s page sits in pages, once read.
	urlAt := -1
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if len(pages)+len(sitemaps) > 0 {
				break // keep what was read before the damage
			}
			return nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "url", "sitemap":
				parent = t.Name.Local
				urlAt, mod = -1, ""
			case "loc":
				inLoc = true
			case "lastmod":
				inMod = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "loc":
				inLoc = false
			case "lastmod":
				inMod = false
			}
		case xml.CharData:
			if inMod && parent == "url" {
				if urlAt >= 0 {
					pages[urlAt].Lastmod = strings.TrimSpace(string(t))
				} else {
					mod = strings.TrimSpace(string(t))
				}
				continue
			}
			if !inLoc {
				continue
			}
			loc := strings.TrimSpace(string(t))
			if loc == "" {
				continue
			}
			if parent == "sitemap" {
				sitemaps = append(sitemaps, loc)
			} else {
				urlAt = len(pages)
				pages = append(pages, SitemapPage{URL: loc, Lastmod: mod})
			}
			if len(pages)+len(sitemaps) >= maxSitemapURLs {
				return pages, sitemaps, nil
			}
		}
	}
	return pages, sitemaps, nil
}
