package sociallinks

import "testing"

func TestPublicProfile(t *testing.T) {
	for _, c := range []struct{ kind, url, id, read string }{
		{"x", "https://twitter.com/Northpeak", "northpeak", "https://x.com/Northpeak"},
		{"linkedin", "https://www.linkedin.com/company/northpeak", "company/northpeak", "https://www.linkedin.com/company/northpeak/"},
		{"linkedin", "https://www.linkedin.com/in/someone", "", ""},
		{"youtube", "https://www.youtube.com/@Northpeak", "@northpeak", "https://www.youtube.com/@Northpeak"},
		{"youtube", "https://www.youtube.com/channel/UC123abc", "channel/uc123abc", "https://www.youtube.com/channel/UC123abc"},
		{"youtube", "https://www.youtube.com/watch", "", ""},
		{"instagram", "https://instagram.com/northpeak", "northpeak", "https://www.instagram.com/northpeak/"},
		{"tiktok", "https://www.tiktok.com/@northpeak", "northpeak", "https://www.tiktok.com/@northpeak"},
		{"tiktok", "https://www.tiktok.com/northpeak", "", ""},
		{"facebook", "https://www.facebook.com/northpeak", "northpeak", "https://www.facebook.com/northpeak"},
		{"reddit", "https://www.reddit.com/r/Northpeak", "r/northpeak", "https://www.reddit.com/r/Northpeak/about.json"},
		{"reddit", "https://www.reddit.com/user/someone", "", ""},
		{"github", "https://github.com/northpeak", "northpeak", "https://api.github.com/users/northpeak/repos?per_page=100&type=owner"},
		{"bluesky", "https://bsky.app/profile/northpeak.example", "northpeak.example", "https://public.api.bsky.app/xrpc/app.bsky.actor.getProfile?actor=northpeak.example"},
		{"threads", "https://www.threads.com/@northpeak", "northpeak", "https://www.threads.com/@northpeak"},
		{"threads", "https://www.threads.net/@northpeak", "northpeak", "https://www.threads.com/@northpeak"},
		{"mastodon", "https://mastodon.social/@Northpeak", "mastodon.social/@northpeak", "https://mastodon.social/api/v1/accounts/lookup?acct=Northpeak"},
		{"mastodon", "https://mastodon.social/explore", "", ""},
		{"dev_to", "https://dev.to/northpeak", "northpeak", "https://dev.to/api/articles?username=northpeak&per_page=1000"},
		{"docker_hub", "https://hub.docker.com/u/northpeak", "northpeak", "https://hub.docker.com/v2/repositories/northpeak/?page_size=100"},
		{"docker_hub", "https://hub.docker.com/r/northpeak/engine", "northpeak", "https://hub.docker.com/v2/repositories/northpeak/?page_size=100"},
		{"discord", "https://discord.com/channels/1234567890", "", ""},
		{"discord", "https://discord.gg/abc123", "abc123", "https://discord.com/api/v9/invites/abc123?with_counts=true"},
		{"discord", "https://discord.com/invite/abc123", "abc123", "https://discord.com/api/v9/invites/abc123?with_counts=true"},
		{"youtube", "https://x.com/northpeak", "", ""},
	} {
		p, ok := PublicProfile(c.kind, c.url)
		if ok != (c.id != "") || p.ID != c.id || p.ReadURL != c.read {
			t.Errorf("PublicProfile(%q, %q) = %+v, %v", c.kind, c.url, p, ok)
		}
	}
}

func TestReadProfile(t *testing.T) {
	for _, c := range []struct {
		kind, page string
		followers  int64
	}{
		{"youtube", `<span>@northpeak</span><span>12.3K subscribers</span><span>48 videos</span>`, 12300},
		{"instagram", `<meta property="og:description" content="1,234 Followers, 56 Following, 78 Posts - See Instagram photos">`, 1234},
		{"tiktok", `{"stats":{"followerCount":9876,"followingCount":12,"videoCount":34}}`, 9876},
		{"facebook", `<div>Northpeak</div><div>4.5k followers</div>`, 4500},
		{"linkedin", `<p>Software Development · 2,001 followers</p>`, 2001},
		{"github", `[{"name":"engine","stargazers_count": 300},{"name":"sdk","stargazers_count":21}]`, 321},
		{"github", `<html><body><pre>[]</pre></body></html>`, 0},
		{"docker_hub", `{"count":2,"results":[{"name":"engine","pull_count":1500,"star_count":3},{"name":"web","pull_count":25}]}`, 1525},
		{"dev_to", `[{"type_of":"article","id":1},{"type_of":"article","id":2}]`, 2},
		{"bluesky", `{"handle":"northpeak.example","displayName":"Northpeak","followersCount":845,"followsCount":12,"postsCount":40}`, 845},
		{"mastodon", `{"username":"northpeak","display_name":"Northpeak","followers_count":96,"following_count":3,"statuses_count":17}`, 96},
		{"threads", `<meta property="og:description" content="1,204 Followers • 12 Threads • Northpeak">`, 1204},
		{"reddit", `<pre>{"data":{"display_name":"Northpeak","subscribers": 5432}}</pre>`, 5432},
		{"discord", `{"code":"abc123","approximate_member_count":777}`, 777},
	} {
		p, ok := ReadProfile(c.kind, c.page)
		if !ok || p.Followers == nil || *p.Followers != c.followers {
			t.Errorf("ReadProfile(%q) = %+v, %v; want %d followers", c.kind, p, ok, c.followers)
		}
	}
	// A login wall has no count: never read as zero.
	for _, k := range PublicKinds {
		if _, ok := ReadProfile(k, `<html><title>Log in</title></html>`); ok {
			t.Errorf("%s: a page with no count read as a profile", k)
		}
	}
}
