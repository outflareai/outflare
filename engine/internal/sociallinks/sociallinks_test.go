package sociallinks

import (
	"reflect"
	"testing"
)

func TestFindKeepsProfilesAndListings(t *testing.T) {
	page := `<html><head><link rel="me" href="https://twitter.com/northpeak?ref=site">
		<script type="application/ld+json">{"@type":"Organization","sameAs":["https:\/\/www.linkedin.com\/company\/northpeak\/about\/","https://github.com/northpeak"]}</script></head>
		<body><footer>
		<a href="https://x.com/northpeak">X</a>
		<a href="https://twitter.com/intent/tweet?text=hi">Share</a>
		<a href="https://www.facebook.com/sharer/sharer.php?u=x">Share</a>
		<a href="https://m.youtube.com/@northpeak/videos">YouTube</a>
		<a href="https://www.youtube.com/watch?v=abc">A video</a>
		<a href="https://x.com/northpeak/status/123">A post</a>
		<a href="https://www.instagram.com/northpeak/">Instagram</a>
		<a href="https://discord.gg/abc123">Discord</a>
		<a href="https://apps.apple.com/app/northpeak/id123456">App Store</a>
		<a href="https://play.google.com/store/apps/details?id=com.northpeak.app&hl=en">Google Play</a>
		<a href="https://play.google.com/store">Not a listing</a>
		<a href="/about">About</a>
		<a href="https://example.com/">Elsewhere</a>
		</footer></body></html>`
	got := Find(page, "https://northpeak.example/")
	want := []Link{
		{"x", "https://x.com/northpeak"},
		{"youtube", "https://www.youtube.com/@northpeak"},
		{"instagram", "https://www.instagram.com/northpeak"},
		{"discord", "https://discord.gg/abc123"},
		{"app_store", "https://apps.apple.com/app/northpeak/id123456"},
		{"google_play", "https://play.google.com/store/apps/details?id=com.northpeak.app"},
		{"linkedin", "https://www.linkedin.com/company/northpeak"},
		{"github", "https://github.com/northpeak"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links:\n got %v\nwant %v", got, want)
	}
}

func TestXHandle(t *testing.T) {
	for raw, want := range map[string]string{
		"https://x.com/northpeak":                   "northpeak",
		"https://twitter.com/@Northpeak_HQ":         "Northpeak_HQ",
		"https://x.com/intent/tweet":                "",
		"https://x.com/":                            "",
		"https://example.com/northpeak":             "",
		"https://x.com/this_handle_is_far_too_long": "",
	} {
		if got := XHandle(raw); got != want {
			t.Errorf("XHandle(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestReadXProfile(t *testing.T) {
	page := `<html><head><title>Northpeak (@northpeak) / X</title>
		<link rel="preload" as="image" href="https://pbs.twimg.com/profile_images/1/abc_400x400.jpg"></head><body>
		<a href="/northpeak/following"><div><span class="font-bold">258</span> <span class="text-secondary">Following</span></div></a>
		<a href="/northpeak/verified_followers"><div><span class="font-bold">12.3K</span> <span class="text-secondary">Followers</span></div></a>
		</body></html>`
	p, ok := ReadXProfile(page)
	if !ok || p.Name != "Northpeak" || p.Handle != "northpeak" || p.AvatarURL != "https://pbs.twimg.com/profile_images/1/abc_400x400.jpg" {
		t.Fatalf("profile = %+v, %v", p, ok)
	}
	if *p.Followers != 12300 || *p.Following != 258 {
		t.Fatalf("counts = %d, %d", *p.Followers, *p.Following)
	}
	// A login wall has no counts: not a profile, never zero.
	if _, ok := ReadXProfile(`<html><title>X</title><body>Log in to X</body></html>`); ok {
		t.Fatal("a login wall read as a profile")
	}
}

func TestParseCount(t *testing.T) {
	for s, want := range map[string]int64{"2,315": 2315, "12.3K": 12300, "1.2M": 1200000, "7": 7, "1B": 1000000000} {
		if got, ok := ParseCount(s); !ok || got != want {
			t.Errorf("ParseCount(%q) = %d, %v", s, got, ok)
		}
	}
	if _, ok := ParseCount("lots"); ok {
		t.Error("ParseCount read a word")
	}
}
