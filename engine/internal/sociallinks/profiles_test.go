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
		{"github", "https://github.com/northpeak", "northpeak", "https://api.github.com/users/northpeak"},
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
		{"github", `{"login":"northpeak","name":"Northpeak","followers": 321,"following":0}`, 321},
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
