// Package cookies keeps each site's cookie policy up to date. It finds
// every cookie, storage key and third-party script on a site, two ways: a
// scan of its pages through the site crawler (engine/internal/crawl), and
// the names the website script sees in visitors' browsers (never values).
// Each is matched to a vendor in the catalogue (catalog.json); ones nobody
// knows wait for a person to say what they are. From what is found, the
// policy is published as versions, and a version that adds a vendor
// needing consent asks visitors again through the consent banner.
package cookies

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"strings"
)

// Categories, as the consent banner asks about them, and unknown for what
// no one has classified yet.
const (
	Necessary   = "necessary"
	Analytics   = "analytics"
	Marketing   = "marketing"
	Preferences = "preferences"
	Unknown     = "unknown"
)

// Categories lists the categories in the order a policy shows them.
var Categories = []string{Necessary, Analytics, Marketing, Preferences}

// Kinds of things a site can hold.
const (
	KindCookie         = "cookie"
	KindLocalStorage   = "local_storage"
	KindSessionStorage = "session_storage"
	KindScript         = "script"
	KindIframe         = "iframe"
	KindPixel          = "pixel"
)

// Vendor is a company's product as the catalogue knows it.
type Vendor struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Company    string `json:"company"`
	Category   string `json:"category"`
	Purpose    string `json:"purpose"`
	PrivacyURL string `json:"privacy_url"`
	// Where its scripts, iframes and pixels come from: a host suffix
	// (connect.facebook.net), optionally with a path prefix
	// (google.com/recaptcha), or a path on any host (/matomo.js).
	Scripts []string `json:"scripts"`
	Cookies []Item   `json:"cookies"`
	Storage []Item   `json:"storage"`
}

// Item is one cookie or storage key a vendor sets. A name ending in or
// holding * matches any text there (_ga_*, ph_*_posthog).
type Item struct {
	Name      string `json:"name"`
	Retention string `json:"retention"`
	// Its own category, where it differs from its vendor's.
	Category string `json:"category"`
	Purpose  string `json:"purpose"`
}

//go:embed catalog.json
var catalogJSON []byte

var catalog = func() []Vendor {
	var c struct {
		Vendors []Vendor `json:"vendors"`
	}
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("cookies: catalog.json: " + err.Error())
	}
	return c.Vendors
}()

// VendorByID finds a vendor in the catalogue.
func VendorByID(id string) (Vendor, bool) {
	for _, v := range catalog {
		if v.ID == id {
			return v, true
		}
	}
	return Vendor{}, false
}

// Catalog is every vendor the catalogue knows.
func Catalog() []Vendor { return catalog }

// Match is what the catalogue says about something found on a site.
type Match struct {
	Vendor    string
	Category  string
	Purpose   string
	Retention string
}

// Known reports whether the catalogue knew it.
func (m Match) Known() bool { return m.Vendor != "" }

// Lookup matches a cookie, storage key, script, iframe or pixel to the
// catalogue. For a script, name is its host and path.
func Lookup(kind, name string) Match {
	switch kind {
	case KindCookie, KindLocalStorage, KindSessionStorage:
		for _, v := range catalog {
			items := v.Cookies
			if kind != KindCookie {
				items = v.Storage
			}
			for _, it := range items {
				if wildcard(it.Name, name) {
					m := Match{Vendor: v.ID, Category: v.Category, Purpose: it.Purpose, Retention: it.Retention}
					if it.Category != "" {
						m.Category = it.Category
					}
					if m.Purpose == "" {
						m.Purpose = v.Purpose
					}
					return m
				}
			}
		}
	default:
		host, path, _ := strings.Cut(strings.ToLower(name), "/")
		path = "/" + path
		for _, v := range catalog {
			for _, s := range v.Scripts {
				if scriptMatch(s, host, path) {
					return Match{Vendor: v.ID, Category: v.Category, Purpose: v.Purpose}
				}
			}
		}
	}
	return Match{Category: Unknown}
}

// scriptMatch matches a catalogue entry to a script's host and path.
func scriptMatch(pattern, host, path string) bool {
	if strings.HasPrefix(pattern, "/") {
		return strings.HasPrefix(path, pattern) || strings.HasSuffix(path, pattern)
	}
	h, p, hasPath := strings.Cut(pattern, "/")
	if host != h && !strings.HasSuffix(host, "."+h) {
		return false
	}
	return !hasPath || strings.HasPrefix(path, "/"+p)
}

// wildcard matches a name against a pattern where * stands for any text.
func wildcard(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	rest := name[len(parts[0]):]
	for i, p := range parts[1:] {
		last := i == len(parts)-2
		if last {
			return strings.HasSuffix(rest, p) && len(rest) >= len(p)
		}
		j := strings.Index(rest, p)
		if j < 0 {
			return false
		}
		rest = rest[j+len(p):]
	}
	return true
}

// ScriptName is how a script, iframe or pixel at raw is named: its host
// and first path segment (www.googletagmanager.com/gtm.js), or "" for an
// address that isn't http(s).
func ScriptName(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	first, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	name := host + "/" + first
	if len(name) > 300 {
		name = name[:300]
	}
	return name
}
