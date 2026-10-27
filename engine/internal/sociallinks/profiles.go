package sociallinks

import (
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// PublicKinds are the link kinds whose public pages show a follower count
// Outflare can read through the workspace's page reader, every day.
var PublicKinds = []string{"x", "linkedin", "youtube", "instagram", "tiktok", "facebook", "reddit", "github", "discord"}

// Public is a profile in a brand's links, ready to read: its id on the
// channel (lowercase, so the same profile is one account), the handle to
// show, the profile's own address and the address whose page has its
// counts.
type Public struct {
	ID      string
	Handle  string
	URL     string
	ReadURL string
	// Page is true for a company page rather than a person's or brand's
	// profile (LinkedIn and Facebook pages).
	Page bool
}

// Profile is what a public profile page shows. A count the page does not
// show is nil, never zero.
type Profile struct {
	Name      string
	AvatarURL string
	Followers *int64
	Following *int64
	Posts     *int64
}

var (
	plainHandle   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	youtubeHandle = regexp.MustCompile(`^@[A-Za-z0-9_.-]{1,100}$`)
)

// PublicProfile is where a brand link's public counts are read, or false
// for a link with none Outflare can read (a LinkedIn person, a Reddit
// user, a single post).
func PublicProfile(kind, raw string) (Public, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || KindOf(u) != kind {
		return Public{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	first, second := parts[0], ""
	if len(parts) > 1 {
		second = parts[1]
	}
	for _, p := range notProfiles[kind] {
		if strings.EqualFold(first, p) {
			return Public{}, false
		}
	}
	id := func(h string) string { return strings.ToLower(h) }
	switch kind {
	case "x":
		h := XHandle(raw)
		if h == "" {
			return Public{}, false
		}
		return Public{ID: id(h), Handle: h, URL: "https://x.com/" + h, ReadURL: "https://x.com/" + h}, true
	case "linkedin":
		// Company and showcase pages show their followers to everyone; a
		// person's profile doesn't.
		if (first != "company" && first != "showcase") || !plainHandle.MatchString(second) {
			return Public{}, false
		}
		addr := "https://www.linkedin.com/" + first + "/" + second
		return Public{ID: id(first + "/" + second), Handle: second, URL: addr, ReadURL: addr + "/", Page: true}, true
	case "youtube":
		var path string
		switch {
		case youtubeHandle.MatchString(first):
			path = first
		case (first == "channel" || first == "c" || first == "user") && plainHandle.MatchString(second):
			path = first + "/" + second
		default:
			return Public{}, false
		}
		addr := "https://www.youtube.com/" + path
		return Public{ID: id(path), Handle: strings.TrimPrefix(path[strings.LastIndex(path, "/")+1:], "@"), URL: addr, ReadURL: addr}, true
	case "instagram", "tiktok", "facebook", "github":
		h := first
		if kind == "tiktok" {
			if !strings.HasPrefix(h, "@") {
				return Public{}, false
			}
			h = strings.TrimPrefix(h, "@")
		}
		if !plainHandle.MatchString(h) {
			return Public{}, false
		}
		switch kind {
		case "instagram":
			return Public{ID: id(h), Handle: h, URL: "https://www.instagram.com/" + h, ReadURL: "https://www.instagram.com/" + h + "/"}, true
		case "tiktok":
			return Public{ID: id(h), Handle: h, URL: "https://www.tiktok.com/@" + h, ReadURL: "https://www.tiktok.com/@" + h}, true
		case "facebook":
			return Public{ID: id(h), Handle: h, URL: "https://www.facebook.com/" + h, ReadURL: "https://www.facebook.com/" + h, Page: true}, true
		default:
			// GitHub's public API answers with the account's counts.
			return Public{ID: id(h), Handle: h, URL: "https://github.com/" + h, ReadURL: "https://api.github.com/users/" + h}, true
		}
	case "reddit":
		// A subreddit shows its members; a person's profile shows no
		// followers.
		if first != "r" || !plainHandle.MatchString(second) {
			return Public{}, false
		}
		return Public{ID: id("r/" + second), Handle: "r/" + second, URL: "https://www.reddit.com/r/" + second, ReadURL: "https://www.reddit.com/r/" + second + "/about.json"}, true
	case "discord":
		code := first
		if strings.ToLower(u.Hostname()) != "discord.gg" {
			if first != "invite" {
				return Public{}, false
			}
			code = second
		}
		if !plainHandle.MatchString(code) {
			return Public{}, false
		}
		return Public{ID: id(code), Handle: code, URL: "https://discord.gg/" + code, ReadURL: "https://discord.com/api/v9/invites/" + code + "?with_counts=true"}, true
	}
	return Public{}, false
}

const count = `([0-9][0-9.,]*\s?[KMBkmb]?)`

var (
	followersText   = regexp.MustCompile(`(?i)` + count + `\s+(?:followers|follower)\b`)
	subscribersText = regexp.MustCompile(`(?i)` + count + `\s+subscribers?\b`)
	instagramMeta   = regexp.MustCompile(`(?i)` + count + `\s+Followers,\s*` + count + `\s+Following,\s*` + count + `\s+Posts`)
	jsonNumber      = func(key string) *regexp.Regexp {
		return regexp.MustCompile(`"` + key + `"\s*:\s*([0-9]{1,15})\b`)
	}
	tiktokFollowers = jsonNumber("followerCount")
	tiktokFollowing = jsonNumber("followingCount")
	tiktokVideos    = jsonNumber("videoCount")
	githubFollowers = jsonNumber("followers")
	githubFollowing = jsonNumber("following")
	redditMembers   = jsonNumber("subscribers")
	discordMembers  = jsonNumber("approximate_member_count")
	jsonName        = regexp.MustCompile(`"name"\s*:\s*"([^"\\]{1,200})"`)
	jsonAvatar      = regexp.MustCompile(`"avatar_url"\s*:\s*"(https://avatars\.githubusercontent\.com/[^"\\]{1,300})"`)
	ogTitle         = regexp.MustCompile(`<meta[^>]{0,40}property="og:title"[^>]{0,40}content="([^"]{1,300})"`)
)

// ReadProfile reads the counts on a profile's page (or, for GitHub, Reddit
// and Discord, their public JSON answer, which the page reader returns as
// the page). It reports false when the page shows no follower count (a
// login wall, an error page), so a missing count is never read as zero.
func ReadProfile(kind, page string) (Profile, bool) {
	var p Profile
	num := func(re *regexp.Regexp) *int64 {
		if m := re.FindStringSubmatch(page); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				return &n
			}
		}
		return nil
	}
	shown := func(re *regexp.Regexp, group int) *int64 {
		if m := re.FindStringSubmatch(page); m != nil {
			if n, ok := ParseCount(m[group]); ok {
				return &n
			}
		}
		return nil
	}
	switch kind {
	case "x":
		x, ok := ReadXProfile(page)
		return Profile{Name: x.Name, AvatarURL: x.AvatarURL, Followers: x.Followers, Following: x.Following}, ok
	case "instagram":
		if m := instagramMeta.FindStringSubmatch(page); m != nil {
			p.Followers, p.Following, p.Posts = parsed(m[1]), parsed(m[2]), parsed(m[3])
		}
	case "tiktok":
		p.Followers, p.Following, p.Posts = num(tiktokFollowers), num(tiktokFollowing), num(tiktokVideos)
		if p.Followers == nil {
			p.Followers = shown(followersText, 1)
		}
	case "youtube":
		p.Followers = shown(subscribersText, 1)
	case "linkedin", "facebook":
		p.Followers = shown(followersText, 1)
	case "github":
		p.Followers, p.Following = num(githubFollowers), num(githubFollowing)
		if m := jsonAvatar.FindStringSubmatch(page); m != nil {
			p.AvatarURL = m[1]
		}
	case "reddit":
		p.Followers = num(redditMembers)
	case "discord":
		p.Followers = num(discordMembers)
	}
	if kind == "github" {
		if m := jsonName.FindStringSubmatch(page); m != nil {
			p.Name = m[1]
		}
	} else if m := ogTitle.FindStringSubmatch(page); m != nil {
		p.Name = html.UnescapeString(strings.TrimSpace(m[1]))
	}
	return p, p.Followers != nil
}

func parsed(s string) *int64 {
	if n, ok := ParseCount(s); ok {
		return &n
	}
	return nil
}
