package mentions

import (
	"net/url"
	"strconv"
	"strings"
)

// conversational are page kinds where people talk rather than publish: a
// mention there was said by someone who can be reached.
var conversational = map[string]string{
	"podcast": "podcast", "video": "youtube", "youtube": "youtube",
	"interview": "web", "community": "web", "forum": "web", "influencer": "web",
}

// SourceFor is where a page is, as a mention source, from its address and
// the page kind something else gave it, or "" when the page is not a
// conversation (an article, a directory, a store listing): mentions are
// people talking, not links on websites.
func SourceFor(rawURL, kind string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	path := strings.ToLower(u.Path)
	switch {
	case host == "youtube.com" || host == "youtu.be" || host == "music.youtube.com":
		return "youtube"
	case host == "podcasts.apple.com" || host == "podcasts.google.com" || host == "overcast.fm" || host == "pca.st" ||
		(host == "open.spotify.com" && (strings.HasPrefix(path, "/episode/") || strings.HasPrefix(path, "/show/"))):
		return "podcast"
	case host == "x.com" || host == "twitter.com":
		return "x"
	case host == "reddit.com" || host == "old.reddit.com":
		return "reddit"
	case host == "news.ycombinator.com":
		return "hacker_news"
	case host == "bsky.app":
		return "bluesky"
	}
	return conversational[kind]
}

// AtSeconds reads where a video link starts (?t=90, ?t=1m30s, #t=1h2m3s),
// or nil.
func AtSeconds(rawURL string) *int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	t := u.Query().Get("t")
	if t == "" {
		t = u.Query().Get("start")
	}
	if t == "" && strings.HasPrefix(u.Fragment, "t=") {
		t = strings.TrimPrefix(u.Fragment, "t=")
	}
	if t == "" {
		return nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(t, "s")); err == nil && n >= 0 {
		return &n
	}
	total, num := 0, ""
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'h' || r == 'm' || r == 's':
			n, err := strconv.Atoi(num)
			if err != nil {
				return nil
			}
			total += n * map[rune]int{'h': 3600, 'm': 60, 's': 1}[r]
			num = ""
		default:
			return nil
		}
	}
	if num != "" {
		return nil
	}
	return &total
}
