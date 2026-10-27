package sociallinks

import (
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// PublicKinds are the link kinds whose public pages show a follower count
// Outflare can read through the workspace's page reader, every day.
var PublicKinds = []string{"x", "linkedin", "youtube", "instagram", "tiktok", "facebook", "reddit", "github", "discord",
	"bluesky", "threads", "mastodon", "dev_to", "docker_hub"}

// JSONKinds are read from the channel's own public API (JSON) rather than
// a page, so the page reader needn't run scripts for them.
var JSONKinds = []string{"github", "reddit", "discord", "bluesky", "mastodon", "dev_to", "docker_hub"}

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
//
// Followers is the channel's headline count: followers, or subscribers
// (YouTube), members (a subreddit, a Discord server), stars across the
// account's repositories (GitHub), pulls across its images (Docker Hub) or
// published posts (DEV, which shows no followers).
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
			// GitHub's public API lists the account's repositories, with
			// their stars.
			return Public{ID: id(h), Handle: h, URL: "https://github.com/" + h, ReadURL: "https://api.github.com/users/" + h + "/repos?per_page=100&type=owner"}, true
		}
	case "threads":
		h := strings.TrimPrefix(first, "@")
		if !strings.HasPrefix(first, "@") || !plainHandle.MatchString(h) {
			return Public{}, false
		}
		return Public{ID: id(h), Handle: h, URL: "https://www.threads.com/@" + h, ReadURL: "https://www.threads.com/@" + h}, true
	case "bluesky":
		// bsky.app/profile/<handle>
		if first != "profile" || !plainHandle.MatchString(second) {
			return Public{}, false
		}
		return Public{ID: id(second), Handle: second, URL: "https://bsky.app/profile/" + second,
			ReadURL: "https://public.api.bsky.app/xrpc/app.bsky.actor.getProfile?actor=" + second}, true
	case "mastodon":
		host := strings.ToLower(u.Hostname())
		h := strings.TrimPrefix(first, "@")
		if !strings.HasPrefix(first, "@") || !plainHandle.MatchString(h) {
			return Public{}, false
		}
		return Public{ID: id(host + "/@" + h), Handle: h, URL: "https://" + host + "/@" + h,
			ReadURL: "https://" + host + "/api/v1/accounts/lookup?acct=" + h}, true
	case "dev_to":
		if !plainHandle.MatchString(first) {
			return Public{}, false
		}
		return Public{ID: id(first), Handle: first, URL: "https://dev.to/" + first, ReadURL: "https://dev.to/api/articles?username=" + first + "&per_page=1000"}, true
	case "docker_hub":
		// hub.docker.com/u/<namespace> or /r/<namespace>/<image>: the
		// namespace's images, with their pulls.
		if (first != "u" && first != "r") || !plainHandle.MatchString(second) {
			return Public{}, false
		}
		return Public{ID: id(second), Handle: second, URL: "https://hub.docker.com/u/" + second,
			ReadURL: "https://hub.docker.com/v2/repositories/" + second + "/?page_size=100"}, true
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
	githubStars     = jsonNumber("stargazers_count")
	githubAvatar    = regexp.MustCompile(`"avatar_url"\s*:\s*"(https://avatars\.githubusercontent\.com/[^"\\]{1,300})"`)
	dockerPulls     = jsonNumber("pull_count")
	dockerResults   = regexp.MustCompile(`"results"\s*:\s*\[`)
	devArticle      = regexp.MustCompile(`"type_of"\s*:\s*"article"`)
	bskyFollowers   = jsonNumber("followersCount")
	bskyFollows     = jsonNumber("followsCount")
	bskyPosts       = jsonNumber("postsCount")
	bskyName        = regexp.MustCompile(`"displayName"\s*:\s*"([^"\\]{1,200})"`)
	bskyAvatar      = regexp.MustCompile(`"avatar"\s*:\s*"(https://cdn\.bsky\.app/[^"\\]{1,300})"`)
	mastoFollowers  = jsonNumber("followers_count")
	mastoFollowing  = jsonNumber("following_count")
	mastoPosts      = jsonNumber("statuses_count")
	mastoName       = regexp.MustCompile(`"display_name"\s*:\s*"([^"\\]{1,200})"`)
	redditMembers   = jsonNumber("subscribers")
	discordMembers  = jsonNumber("approximate_member_count")
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
		// An answer that is a list of repositories, possibly empty.
		if strings.HasPrefix(stripPre(page), "[") {
			p.Followers = sum(page, githubStars)
		}
		if m := githubAvatar.FindStringSubmatch(page); m != nil {
			p.AvatarURL = m[1]
		}
	case "docker_hub":
		if dockerResults.MatchString(page) {
			p.Followers = sum(page, dockerPulls)
		}
	case "dev_to":
		if strings.HasPrefix(stripPre(page), "[") {
			n := int64(len(devArticle.FindAllStringIndex(page, -1)))
			p.Followers = &n
		}
	case "bluesky":
		p.Followers, p.Following, p.Posts = num(bskyFollowers), num(bskyFollows), num(bskyPosts)
		if m := bskyName.FindStringSubmatch(page); m != nil {
			p.Name = m[1]
		}
		if m := bskyAvatar.FindStringSubmatch(page); m != nil {
			p.AvatarURL = m[1]
		}
	case "mastodon":
		p.Followers, p.Following, p.Posts = num(mastoFollowers), num(mastoFollowing), num(mastoPosts)
		if m := mastoName.FindStringSubmatch(page); m != nil {
			p.Name = m[1]
		}
	case "threads":
		p.Followers = shown(followersText, 1)
	case "reddit":
		p.Followers = num(redditMembers)
	case "discord":
		p.Followers = num(discordMembers)
	}
	if p.Name == "" && !slices.Contains(JSONKinds, kind) {
		if m := ogTitle.FindStringSubmatch(page); m != nil {
			p.Name = html.UnescapeString(strings.TrimSpace(m[1]))
		}
	}
	return p, p.Followers != nil
}

// sum adds up a JSON number across a list answer (stars across
// repositories, pulls across images): zero for an empty list.
func sum(page string, re *regexp.Regexp) *int64 {
	var t int64
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			t += n
		}
	}
	return &t
}

// stripPre is a JSON answer without the <pre> a page reader may wrap it in.
func stripPre(page string) string {
	page = strings.TrimSpace(page)
	if i := strings.Index(page, "<pre>"); i >= 0 && i < 200 {
		page = strings.TrimSpace(page[i+5:])
	}
	return page
}

func parsed(s string) *int64 {
	if n, ok := ParseCount(s); ok {
		return &n
	}
	return nil
}
