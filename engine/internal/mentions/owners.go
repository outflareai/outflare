package mentions

import (
	"net/url"
	"strings"
	"unicode"
)

// Owners: the brand and its competitors talk about themselves on their own
// channels (a competitor's YouTube channel, the brand's own podcast). Those
// are not mentions: the listener looks for other people naming them.

// Owner is the brand or one competitor, with what tells its own channels
// apart: its name, its website and the profiles it is known to have.
type Owner struct {
	// About is "brand" or "competitor".
	About string
	// CompetitorID is the competitor's id; empty for the brand.
	CompetitorID string
	Name         string
	// Host is its website's host.
	Host string
	// Profiles are its known profile addresses (any form ProfileKey reads).
	Profiles []string
}

// ProfileKey is a profile address in one form, for comparing: lowercase,
// without scheme, www. or m., query or trailing slash
// ("youtube.com/@northpeak"). Empty when it isn't an address.
func ProfileKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(strings.TrimPrefix(host, "www."), "m.")
	path := strings.TrimRight(strings.ToLower(u.EscapedPath()), "/")
	// A channel's tabs are the channel: /@northpeak/videos.
	if strings.HasPrefix(path, "/@") {
		if i := strings.IndexByte(path[2:], '/'); i >= 0 {
			path = path[:i+2]
		}
	}
	return host + path
}

// squash keeps only letters and digits, lowercase: "Bright Data" and
// "@brightdata" both become "brightdata".
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// handleOf is the @handle in a profile address, without the @, or "".
func handleOf(raw string) string {
	k := ProfileKey(raw)
	_, path, ok := strings.Cut(k, "/@")
	if !ok {
		return ""
	}
	path, _, _ = strings.Cut(path, "/")
	return path
}

// OwnerOf reports whose own channel a profile is: one of the owners' known
// profiles, or a channel named after an owner (its name, or a handle that
// is its name or its website's first label). Nil when it is someone else's.
func OwnerOf(owners []Owner, name, profileURL string) *Owner {
	key := ProfileKey(profileURL)
	handle := squash(handleOf(profileURL))
	channel := squash(name)
	for i := range owners {
		o := &owners[i]
		if key != "" {
			for _, p := range o.Profiles {
				if ProfileKey(p) == key {
					return o
				}
			}
		}
		names := []string{squash(o.Name)}
		if h := hostOf(o.Host); h != "" {
			label, _, _ := strings.Cut(h, ".")
			names = append(names, squash(h), squash(label))
		}
		for _, n := range names {
			if len(n) < 4 {
				continue
			}
			if n == channel || n == handle {
				return o
			}
		}
	}
	return nil
}
