// Package sociallinks finds where a brand lives besides its website (its
// social profiles and app store listings) in the brand's own homepage, and
// reads the public counts on an X profile page. Pages come from the
// workspace's own page reader (Geonode); nothing here fetches anything.
// Page text is data: only links and numbers are taken from it.
package sociallinks

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/outflareai/outflare/engine/internal/reviews"
)

// Link is one place the brand lives, with the kind brands use.
type Link struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// KindHosts names the kind of a link by its host.
var KindHosts = map[string]string{
	"apps.apple.com": "app_store", "itunes.apple.com": "app_store", "play.google.com": "google_play",
	"chromewebstore.google.com": "chrome_web_store", "chrome.google.com": "chrome_web_store",
	"apps.microsoft.com": "microsoft_store", "store.steampowered.com": "steam", "channelstore.roku.com": "tv_app",
	"x.com": "x", "twitter.com": "x", "linkedin.com": "linkedin", "youtube.com": "youtube", "instagram.com": "instagram",
	"tiktok.com": "tiktok", "facebook.com": "facebook", "discord.gg": "discord", "discord.com": "discord",
	"reddit.com": "reddit", "github.com": "github",
	"bsky.app": "bluesky", "threads.com": "threads", "threads.net": "threads", "dev.to": "dev_to", "hub.docker.com": "docker_hub",
	// Mastodon runs on many servers; these are the large ones.
	"mastodon.social": "mastodon", "mastodon.online": "mastodon", "mas.to": "mastodon", "fosstodon.org": "mastodon",
	"hachyderm.io": "mastodon", "infosec.exchange": "mastodon", "mstdn.social": "mastodon",
}

// KindOf names a link's kind by its host, or "" when it is no known place.
// Review pages (Trustpilot, G2, Capterra, a Google Maps place) are kinds of
// their own.
func KindOf(u *url.URL) string {
	if k := reviews.KindOf(u); k != "" {
		return k
	}
	h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if k, ok := KindHosts[h]; ok {
		return k
	}
	// Country and mobile hosts: uk.linkedin.com, m.youtube.com.
	if i := strings.IndexByte(h, '.'); i > 0 {
		if k, ok := KindHosts[h[i+1:]]; ok && k != "app_store" && k != "google_play" {
			return k
		}
	}
	return ""
}

// notProfiles are first path parts that are a site's own pages, a share
// button or one post rather than a brand's profile.
var notProfiles = map[string][]string{
	"x":          {"intent", "share", "home", "i", "search", "hashtag", "login", "signup", "tos", "privacy", "explore", "settings"},
	"linkedin":   {"sharearticle", "shareArticle", "share", "feed", "login", "signup", "legal", "jobs", "help", "pulse"},
	"facebook":   {"sharer", "sharer.php", "share", "dialog", "plugins", "login", "policies", "help", "events"},
	"youtube":    {"watch", "embed", "shorts", "results", "playlist", "redirect", "t", "about", "howyoutubeworks"},
	"instagram":  {"p", "reel", "reels", "explore", "accounts", "about", "legal"},
	"tiktok":     {"video", "tag", "discover", "legal", "about", "embed"},
	"reddit":     {"submit", "search", "login", "register", "comments", "policies"},
	"github":     {"login", "join", "features", "pricing", "about", "site", "security", "sponsors"},
	"bluesky":    {"search", "settings", "notifications", "feeds", "lists", "messages"},
	"threads":    {"login", "search", "settings", "activity", "post"},
	"mastodon":   {"about", "explore", "public", "auth", "settings", "tags", "privacy-policy"},
	"dev_to":     {"enter", "search", "settings", "dashboard", "t", "about", "privacy", "terms", "faq", "api"},
	"docker_hub": {"search", "signup", "login", "explore", "_", "settings", "billing", "orgs", "repositories"},
}

// Clean tidies a found link into the address a brand keeps: https, no
// query or fragment, no trailing slash. It reports false for a share
// button, a single post, a bare site root or anything that is not a
// profile or listing.
func Clean(raw string, base *url.URL) (Link, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(strings.ToLower(raw), "javascript:") || strings.HasPrefix(strings.ToLower(raw), "mailto:") {
		return Link{}, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Link{}, false
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Link{}, false
	}
	kind := KindOf(u)
	if kind == "" {
		return Link{}, false
	}
	if reviews.IsPlatform(kind) {
		p, ok := reviews.ParsePage(u.String())
		if !ok {
			return Link{}, false
		}
		return Link{Kind: kind, URL: p.URL}, true
	}
	path := strings.Trim(u.EscapedPath(), "/")
	parts := strings.Split(path, "/")
	first := parts[0]
	switch kind {
	case "app_store", "google_play", "chrome_web_store", "microsoft_store", "steam", "tv_app":
		// A listing needs its id: the query on Google Play, a path elsewhere.
		if kind == "google_play" {
			id := u.Query().Get("id")
			if !strings.HasPrefix(path, "store/apps/details") || id == "" {
				return Link{}, false
			}
			return Link{Kind: kind, URL: "https://play.google.com/store/apps/details?id=" + url.QueryEscape(id)}, true
		}
		if len(parts) < 2 {
			return Link{}, false
		}
	case "discord":
		if path == "" {
			return Link{}, false
		}
	default:
		if first == "" {
			return Link{}, false
		}
		for _, p := range notProfiles[kind] {
			if strings.EqualFold(first, p) {
				return Link{}, false
			}
		}
		if kind == "x" && len(parts) > 1 && parts[1] == "status" {
			return Link{}, false
		}
		// A profile is its first one or two parts: linkedin.com/company/acme,
		// youtube.com/@acme, reddit.com/r/acme, github.com/acme.
		keep := 1
		if (kind == "linkedin" || kind == "reddit" || kind == "bluesky" || kind == "docker_hub") && len(parts) > 1 {
			keep = 2
		}
		if kind == "youtube" && (first == "c" || first == "channel" || first == "user") && len(parts) > 1 {
			keep = 2
		}
		if kind == "facebook" && first == "pages" {
			return Link{}, false
		}
		path = strings.Join(parts[:min(keep, len(parts))], "/")
	}
	host := strings.ToLower(u.Hostname())
	if kind == "x" {
		host = "x.com"
	} else if strings.HasPrefix(host, "m.") {
		host = "www." + strings.TrimPrefix(host, "m.")
	}
	return Link{Kind: kind, URL: "https://" + host + "/" + path}, true
}

// Find reads the links in a page and keeps the brand's profiles and
// listings, one per address, in the order they appear.
func Find(page string, pageURL string) []Link {
	base, _ := url.Parse(pageURL)
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []Link{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "a" || n.Data == "link") {
			for _, at := range n.Attr {
				if at.Key != "href" {
					continue
				}
				if l, ok := Clean(at.Val, base); ok && !seen[strings.ToLower(l.URL)] {
					seen[strings.ToLower(l.URL)] = true
					out = append(out, l)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	// JSON-LD sameAs lists name a brand's profiles too.
	for _, m := range sameAs.FindAllStringSubmatch(page, 20) {
		for _, q := range quoted.FindAllStringSubmatch(m[1], 50) {
			if l, ok := Clean(strings.ReplaceAll(q[1], `\/`, "/"), base); ok && !seen[strings.ToLower(l.URL)] {
				seen[strings.ToLower(l.URL)] = true
				out = append(out, l)
			}
		}
	}
	return out
}

var (
	sameAs = regexp.MustCompile(`"sameAs"\s*:\s*\[([^\]]{0,1000})\]`)
	quoted = regexp.MustCompile(`"([^"]{1,500})"`)
)

// XHandle is the handle in an X profile address, or "".
func XHandle(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || KindOf(u) != "x" {
		return ""
	}
	h := strings.Split(strings.Trim(u.Path, "/"), "/")[0]
	h = strings.TrimPrefix(h, "@")
	if !handleShape.MatchString(h) {
		return ""
	}
	for _, p := range notProfiles["x"] {
		if strings.EqualFold(h, p) {
			return ""
		}
	}
	return h
}

var handleShape = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// XProfile is what a public X profile page shows: its name, picture and
// counts. A count the page does not show is nil.
type XProfile struct {
	Name      string
	Handle    string
	AvatarURL string
	Followers *int64
	Following *int64
}

var (
	xTitle  = regexp.MustCompile(`<title>([^<]{0,200}) \(@([A-Za-z0-9_]{1,15})\) / X</title>`)
	xAvatar = regexp.MustCompile(`https://pbs\.twimg\.com/profile_images/[A-Za-z0-9_./-]{1,200}`)
	// The bold number just before the label, as the profile header shows
	// it: <span ...>2,315</span> <span ...>Followers</span>.
	xCount = regexp.MustCompile(`>\s*([0-9][0-9.,]*\s*[KMB]?)\s*</span>\s*(?:<[^>]{0,300}>\s*){0,3}(Followers|Following)\s*<`)
)

// ReadXProfile reads a profile page through the workspace's page reader.
// It reports false when the page is not a profile (a login wall, an
// error page), so a missing count is never read as zero.
func ReadXProfile(page string) (XProfile, bool) {
	var p XProfile
	if m := xTitle.FindStringSubmatch(page); m != nil {
		p.Name, p.Handle = html.UnescapeString(strings.TrimSpace(m[1])), m[2]
	}
	if m := xAvatar.FindString(page); m != "" {
		p.AvatarURL = m
	}
	for _, m := range xCount.FindAllStringSubmatch(page, 10) {
		n, ok := ParseCount(m[1])
		if !ok {
			continue
		}
		switch {
		case m[2] == "Followers" && p.Followers == nil:
			p.Followers = &n
		case m[2] == "Following" && p.Following == nil:
			p.Following = &n
		}
	}
	return p, p.Followers != nil
}

// ParseCount reads a shown count: 2,315, 12.3K, 1.2M, 4.5k.
func ParseCount(s string) (int64, bool) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1e3, strings.TrimSpace(strings.TrimSuffix(s, "K"))
	case strings.HasSuffix(s, "M"):
		mult, s = 1e6, strings.TrimSpace(strings.TrimSuffix(s, "M"))
	case strings.HasSuffix(s, "B"):
		mult, s = 1e9, strings.TrimSpace(strings.TrimSuffix(s, "B"))
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(f*mult + 0.5), true
}
