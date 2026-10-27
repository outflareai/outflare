package crawl

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
	"time"
)

// Robots is a parsed robots.txt (RFC 9309), with the rules for OutflareBot
// already picked: its own group when the file has one, else the "*" group.
type Robots struct {
	rules []robotsRule
	// all is set when every path is allowed or disallowed regardless of
	// rules: a missing file allows all, an unreachable one disallows all.
	all *bool
	// Delay is the group's Crawl-delay, zero when unset.
	Delay time.Duration
	// Sitemaps are the file's Sitemap lines, which apply to every agent.
	Sitemaps []string
}

type robotsRule struct {
	allow   bool
	pattern string
}

// AllowAll is the robots.txt of a site without one (a 4xx answer).
func AllowAll() *Robots { t := true; return &Robots{all: &t} }

// DisallowAll is the robots.txt of a site that could not be read (a 5xx
// answer or no answer): crawlers must assume everything is off limits.
func DisallowAll() *Robots { f := false; return &Robots{all: &f} }

// maxRobotsBytes is how much of a robots.txt is read; RFC 9309 asks
// crawlers to parse at least 500 KiB.
const maxRobotsBytes = 512 << 10

// ParseRobots reads a robots.txt for the agent token (OutflareBot).
func ParseRobots(body []byte, token string) *Robots {
	if len(body) > maxRobotsBytes {
		body = body[:maxRobotsBytes]
	}
	token = strings.ToLower(token)
	type group struct {
		agents []string
		rules  []robotsRule
		delay  time.Duration
	}
	var (
		groups   []*group
		cur      *group
		inAgents bool // the last line was a user-agent line
		out      = &Robots{}
	)
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), maxRobotsBytes)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if !inAgents || cur == nil {
				cur = &group{}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(value))
			inAgents = true
			continue
		case "allow", "disallow":
			// An empty Disallow allows everything, the same as no rule.
			if cur != nil && value != "" {
				cur.rules = append(cur.rules, robotsRule{allow: key == "allow", pattern: value})
			}
		case "crawl-delay":
			if f, err := strconv.ParseFloat(value, 64); err == nil && f > 0 && cur != nil {
				cur.delay = time.Duration(f * float64(time.Second))
			}
		case "sitemap":
			if value != "" && len(out.Sitemaps) < 50 {
				out.Sitemaps = append(out.Sitemaps, value)
			}
		}
		inAgents = false
	}
	// Our own group wins; groups naming the same agent are merged.
	pick := func(match func(string) bool) bool {
		found := false
		for _, g := range groups {
			for _, a := range g.agents {
				if match(a) {
					out.rules = append(out.rules, g.rules...)
					if g.delay > out.Delay {
						out.Delay = g.delay
					}
					found = true
					break
				}
			}
		}
		return found
	}
	if !pick(func(a string) bool { return a == token || strings.HasPrefix(a, token+"/") }) {
		pick(func(a string) bool { return a == "*" })
	}
	return out
}

// Allowed reports whether a path (with its query, percent-encoded as sent)
// may be fetched. The longest matching rule wins; on a tie Allow wins.
func (r *Robots) Allowed(path string) bool {
	if r.all != nil {
		return *r.all
	}
	if path == "" {
		path = "/"
	}
	if path == "/robots.txt" {
		return true
	}
	best, allow := -1, true
	for _, rule := range r.rules {
		if !robotsMatch(rule.pattern, path) {
			continue
		}
		n := len(rule.pattern)
		if n > best || (n == best && rule.allow) {
			best, allow = n, rule.allow
		}
	}
	return allow
}

// robotsMatch matches a rule's pattern against a path: * is any run of
// characters and a final $ anchors the end; otherwise it is a prefix.
func robotsMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	if len(parts) == 1 {
		return !anchored || pos == len(path)
	}
	for i, part := range parts[1:] {
		last := i == len(parts)-2
		if last && anchored {
			return len(path)-pos >= len(part) && strings.HasSuffix(path, part)
		}
		j := strings.Index(path[pos:], part)
		if j < 0 {
			return false
		}
		pos += j + len(part)
	}
	return true
}
