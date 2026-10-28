// Package reviews reads a brand's public review pages (Trustpilot, G2,
// Capterra, Google) and works out the numbers Outflare keeps about them:
// the platform's own score and count, each review with its rating, and
// Outflare's own review score and momentum across platforms. Pages come
// from the workspace's own page reader (Geonode); nothing here fetches
// anything. Page text is data: only ratings, dates, names and review text
// are taken from it, and nothing in it is followed.
package reviews

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The platforms Outflare reads, as brand link kinds.
const (
	Trustpilot = "trustpilot"
	G2         = "g2"
	Capterra   = "capterra"
	Google     = "google_reviews"
)

// Platforms are the review platforms Outflare reads, in display order.
var Platforms = []string{Trustpilot, G2, Capterra, Google}

// Names are the platforms' names as people know them.
var Names = map[string]string{Trustpilot: "Trustpilot", G2: "G2", Capterra: "Capterra", Google: "Google"}

// IsPlatform says whether a brand link kind is a review page.
func IsPlatform(kind string) bool {
	for _, p := range Platforms {
		if p == kind {
			return true
		}
	}
	return false
}

// Page is a review page in a brand's links, ready to read: its id on the
// platform (so the same page is one review page however it was pasted)
// and its own address.
type Page struct {
	Platform string
	ID       string
	URL      string
}

var (
	trustpilotDomain = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,250}\.[a-z]{2,}$`)
	g2Slug           = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,150}$`)
	capterraID       = regexp.MustCompile(`^[0-9]{1,12}$`)
	capterraSlug     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,150}$`)
)

// host is a lowercase host without www.
func host(u *url.URL) string {
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// KindOf names the review platform of an address, or "".
func KindOf(u *url.URL) string {
	h := host(u)
	path := strings.ToLower(u.Path)
	switch {
	case h == "trustpilot.com" || strings.HasSuffix(h, ".trustpilot.com"):
		return Trustpilot
	case h == "g2.com":
		return G2
	case isCapterraHost(h):
		return Capterra
	case h == "maps.app.goo.gl" || h == "g.page" || (h == "goo.gl" && strings.HasPrefix(path, "/maps")):
		return Google
	case isGoogleHost(h) && (strings.HasPrefix(path, "/maps") || (h == "search.google.com" && strings.HasPrefix(path, "/local/"))):
		return Google
	}
	return ""
}

// isCapterraHost is capterra.com and its country sites (capterra.co.uk,
// capterra.com.au, capterra.de ...).
func isCapterraHost(h string) bool {
	return h == "capterra.com" || strings.HasPrefix(h, "capterra.")
}

// isGoogleHost is google.com and its country sites (google.co.uk ...),
// and search.google.com.
func isGoogleHost(h string) bool {
	h = strings.TrimPrefix(h, "search.")
	return h == "google.com" || strings.HasPrefix(h, "google.")
}

// ParsePage reads a review page address into the page Outflare keeps:
// https, the platform's own form of the address, no tracking query. It
// reports false for an address that is not a brand's review page (a
// category, a single review, a search).
func ParsePage(raw string) (Page, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Page{}, false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return Page{}, false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	switch KindOf(u) {
	case Trustpilot:
		// trustpilot.com/review/northpeak.example
		if len(parts) < 2 || parts[0] != "review" {
			return Page{}, false
		}
		d := strings.ToLower(parts[1])
		d, _ = url.PathUnescape(d)
		if !trustpilotDomain.MatchString(d) {
			return Page{}, false
		}
		return Page{Platform: Trustpilot, ID: d, URL: "https://www.trustpilot.com/review/" + d}, true
	case G2:
		// g2.com/products/northpeak/reviews
		if len(parts) < 2 || parts[0] != "products" {
			return Page{}, false
		}
		slug := strings.ToLower(parts[1])
		if !g2Slug.MatchString(slug) {
			return Page{}, false
		}
		return Page{Platform: G2, ID: slug, URL: "https://www.g2.com/products/" + slug + "/reviews"}, true
	case Capterra:
		// capterra.com/p/123456/Northpeak/reviews/
		if len(parts) < 3 || parts[0] != "p" || !capterraID.MatchString(parts[1]) || !capterraSlug.MatchString(parts[2]) {
			return Page{}, false
		}
		return Page{Platform: Capterra, ID: parts[1], URL: "https://www.capterra.com/p/" + parts[1] + "/" + parts[2] + "/reviews/"}, true
	case Google:
		return googlePage(u)
	}
	return Page{}, false
}

// googlePage keeps a Google Maps place or a short link as given, without
// tracking parameters. Its id is the place's name and position, the short
// code, or the place id.
func googlePage(u *url.URL) (Page, bool) {
	h := host(u)
	path := strings.Trim(u.EscapedPath(), "/")
	switch {
	case h == "maps.app.goo.gl" || h == "g.page" || h == "goo.gl":
		if path == "" || path == "maps" {
			return Page{}, false
		}
		return Page{Platform: Google, ID: h + "/" + path, URL: "https://" + h + "/" + path}, true
	case h == "search.google.com":
		id := u.Query().Get("placeid")
		if id == "" {
			return Page{}, false
		}
		return Page{Platform: Google, ID: "place:" + id, URL: "https://search.google.com/local/reviews?placeid=" + url.QueryEscape(id)}, true
	}
	// google.com/maps/place/Northpeak/@-33.86,151.2,17z/data=...
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "maps" || parts[1] != "place" || parts[2] == "" {
		if cid := u.Query().Get("cid"); cid != "" && len(parts) >= 1 && parts[0] == "maps" {
			return Page{Platform: Google, ID: "cid:" + cid, URL: "https://www.google.com/maps?cid=" + url.QueryEscape(cid)}, true
		}
		return Page{}, false
	}
	keep := parts[:3]
	if len(parts) > 3 && strings.HasPrefix(parts[3], "@") {
		keep = parts[:4]
	}
	p := strings.Join(keep, "/")
	id := strings.ToLower(parts[2])
	if len(keep) == 4 {
		// The place's position tells two branches with one name apart.
		pos := strings.Split(strings.TrimPrefix(parts[3], "@"), ",")
		if len(pos) >= 2 {
			id += "@" + round3(pos[0]) + "," + round3(pos[1])
		}
	}
	return Page{Platform: Google, ID: id, URL: "https://www.google.com/" + p}, true
}

func round3(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return strconv.FormatFloat(f, 'f', 3, 64)
}

// PageURL is the address of one page of a review page's reviews, newest
// first. Google shows one page.
func PageURL(p Page, n int) string {
	switch p.Platform {
	case Trustpilot:
		if n <= 1 {
			return p.URL + "?sort=recency"
		}
		return p.URL + "?page=" + strconv.Itoa(n) + "&sort=recency"
	case G2:
		if n <= 1 {
			return p.URL + "?order=most_recent"
		}
		return p.URL + "?order=most_recent&page=" + strconv.Itoa(n)
	case Capterra:
		// Capterra sorts in the browser and ignores a sort in the address,
		// so pages are read in its own order (see NewestFirst).
		if n <= 1 {
			return p.URL
		}
		return p.URL + "?page=" + strconv.Itoa(n)
	}
	return p.URL
}

// RenderJS says whether a platform's page must be read in a browser that
// runs its scripts: Google builds the page in the browser, and G2 answers
// anything else with a bot check. Other platforms are read without, and
// again in a browser when a read shows no reviews.
func RenderJS(platform string) bool { return platform == Google || platform == G2 }

// NewestFirst says whether a platform's pages list reviews newest first,
// so a daily read can stop at the first review it already has. Capterra's
// pages list the most helpful first.
func NewestFirst(platform string) bool { return platform != Capterra }

// SearchQueries are the web searches that find a brand's page on each
// platform it can be searched for. Google places are added by pasting
// their Maps link.
func SearchQueries(name, website string) map[string]string {
	name = strings.TrimSpace(name)
	website = strings.ToLower(strings.TrimSpace(website))
	out := map[string]string{}
	if website != "" {
		out[Trustpilot] = `site:trustpilot.com/review "` + website + `"`
	}
	if name != "" {
		out[G2] = `site:g2.com/products "` + name + `" reviews`
		out[Capterra] = `site:capterra.com "` + name + `" reviews`
	}
	return out
}

// Matches says whether a page found by a search is the brand's own: a
// Trustpilot page for its website, or a G2 or Capterra page whose address
// names the brand.
func Matches(p Page, name, website string) bool {
	website = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(website)), "www.")
	switch p.Platform {
	case Trustpilot:
		return website != "" && strings.TrimPrefix(p.ID, "www.") == website
	case G2, Capterra:
		key := slugOf(name)
		if key == "" {
			return false
		}
		u, err := url.Parse(p.URL)
		if err != nil {
			return false
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		slug := ""
		if p.Platform == G2 && len(parts) > 1 {
			slug = parts[1]
		}
		if p.Platform == Capterra && len(parts) > 2 {
			slug = parts[2]
		}
		s := slugOf(slug)
		return s == key || strings.HasPrefix(s, key+"-") || strings.TrimSuffix(s, "-app") == key
	}
	return false
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugOf(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}
