package crawl

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
)

// Settings are how a site is crawled, beyond its limits. A run keeps a
// copy of the ones it started with.
type Settings struct {
	// RenderJS is off, auto (only pages that are empty app shells) or
	// always. Scripts run through the workspace's own scraping connection,
	// only on a proved site.
	RenderJS       string   `json:"render_js"`
	UseSitemaps    bool     `json:"use_sitemaps"`
	FollowNofollow bool     `json:"follow_nofollow"`
	IgnoreParams   bool     `json:"ignore_params"`
	MaxDepth       int      `json:"max_depth"`
	Include        []string `json:"include_patterns"`
	Exclude        []string `json:"exclude_patterns"`
}

// DefaultSettings are a new site's: much like other site audits, they
// read what a search engine would and leave out nothing.
func DefaultSettings() Settings {
	return Settings{RenderJS: "auto", UseSitemaps: true, Include: []string{}, Exclude: []string{}}
}

// SettingsFrom reads a run's copy; a run from before settings existed
// crawls with the defaults.
func SettingsFrom(raw []byte) (Settings, error) {
	s := DefaultSettings()
	if len(raw) == 0 {
		return s, nil
	}
	err := json.Unmarshal(raw, &s)
	return s, err
}

// Limits on scope patterns.
const (
	MaxPatterns      = 20
	maxPatternLength = 300
)

// RenderModes are RenderJS's values.
var RenderModes = []string{"off", "auto", "always"}

// CheckPatterns reports why a list of scope patterns cannot be used.
func CheckPatterns(name string, list []string) error {
	if len(list) > MaxPatterns {
		return fmt.Errorf("%s holds at most %d patterns", name, MaxPatterns)
	}
	for _, p := range list {
		if p == "" || len(p) > maxPatternLength {
			return fmt.Errorf("each of %s is 1 to %d characters", name, maxPatternLength)
		}
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("%s: %q is not a regular expression: %v", name, p, err)
		}
	}
	return nil
}

// Scope decides which found URLs a run crawls.
type Scope struct {
	include, exclude []*regexp.Regexp
	maxDepth         int
	ignoreParams     bool
}

// Scope compiles the settings' scope.
func (s Settings) Scope() (*Scope, error) {
	sc := &Scope{maxDepth: s.MaxDepth, ignoreParams: s.IgnoreParams}
	for _, l := range []struct {
		in  []string
		out *[]*regexp.Regexp
	}{{s.Include, &sc.include}, {s.Exclude, &sc.exclude}} {
		for _, p := range l.in {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, err
			}
			*l.out = append(*l.out, re)
		}
	}
	return sc, nil
}

// URL returns the address to crawl for a found URL at a depth, or false
// when the settings leave it out. The start page is always in.
func (sc *Scope) URL(raw string, depth int, start bool) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if sc.ignoreParams && u.RawQuery != "" {
		u.RawQuery, u.ForceQuery = "", false
		raw = u.String()
	}
	if start {
		return raw, true
	}
	if sc.maxDepth > 0 && depth > sc.maxDepth {
		return "", false
	}
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	for _, re := range sc.exclude {
		if re.MatchString(path) {
			return "", false
		}
	}
	if len(sc.include) == 0 {
		return raw, true
	}
	for _, re := range sc.include {
		if re.MatchString(path) {
			return raw, true
		}
	}
	return "", false
}
