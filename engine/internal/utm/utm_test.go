package utm

import "testing"

func TestSourceMergesSpellings(t *testing.T) {
	for in, want := range map[string]string{
		"Facebook": "facebook", "fb": "facebook", "https://www.facebook.com": "facebook",
		"Twitter": "x", "t.co": "x", "LinkedIn": "linkedin", "Google Ads": "google",
		"My Podcast!": "my-podcast", "  partner_ana ": "partner_ana",
	} {
		if got := Source(in); got != want {
			t.Errorf("Source(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugAndValue(t *testing.T) {
	if got := Slug("Residential proxies, US"); got != "residential-proxies-us" {
		t.Errorf("Slug = %q", got)
	}
	if got := Value("Hero CTA"); got != "hero-cta" {
		t.Errorf("Value = %q", got)
	}
	if got := SuffixSlug("spring-launch", 2); got != "spring-launch-2" {
		t.Errorf("SuffixSlug = %q", got)
	}
	long := ""
	for len(long) < 80 {
		long += "abcdefghij"
	}
	if got := SuffixSlug(long, 12); len(got) != 80 || CheckSlug(got) != nil {
		t.Errorf("long SuffixSlug = %q", got)
	}
}

func TestTagReplacesOldTagsAndKeepsTheRest(t *testing.T) {
	dest, err := Destination("https://example.com/pricing?plan=pro&utm_source=old&ofc=zzzzzzzzzz#faq")
	if err != nil {
		t.Fatal(err)
	}
	if dest != "https://example.com/pricing?plan=pro#faq" {
		t.Fatalf("Destination = %q", dest)
	}
	got, err := Tag(dest, Tags{Source: "linkedin", Medium: "cpc", Campaign: "spring-launch", Content: "carousel-1", Code: "abcde23456"})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/pricing?plan=pro&utm_source=linkedin&utm_medium=cpc&utm_campaign=spring-launch&utm_content=carousel-1&ofc=abcde23456#faq"
	if got != want {
		t.Fatalf("Tag = %q\nwant  %q", got, want)
	}
	for _, bad := range []string{"example.com", "ftp://example.com", "https://user:pw@example.com/", "/pricing"} {
		if _, err := Destination(bad); err == nil {
			t.Errorf("Destination(%q) took it", bad)
		}
	}
}

func TestNewCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c := NewCode()
		if len(c) != 10 || Value(c) != c {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 100 {
		t.Fatal("codes repeat")
	}
}

func TestEveryChannelHasAMediumAndSources(t *testing.T) {
	for _, c := range Channels {
		if c.Medium == "" || len(c.Sources) == 0 || c.Label == "" {
			t.Errorf("channel %s is incomplete", c.ID)
		}
		for _, s := range c.Sources {
			if Source(s) != s {
				t.Errorf("channel %s source %q is not clean", c.ID, s)
			}
		}
	}
}

func TestRefGoesLastAndReplacesTheOldOne(t *testing.T) {
	got, err := Tag("https://example.com/?ref=old&plan=pro", Tags{Source: "partner", Medium: "affiliate", Campaign: "ana", Code: "abcde23456", Ref: "ana"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://example.com/?plan=pro&utm_source=partner&utm_medium=affiliate&utm_campaign=ana&ofc=abcde23456&ref=ana"; got != want {
		t.Fatalf("Tag = %q\nwant  %q", got, want)
	}
	if got, _ := Tag("https://example.com/?ref=keep", Tags{Code: "abcde23456"}); got != "https://example.com/?ref=keep&ofc=abcde23456" {
		t.Fatalf("Tag without Ref = %q", got)
	}
}

func TestOwnLinkTagsOnlyOurSites(t *testing.T) {
	c := Campaign{Slug: "spring-news", Code: "abcde23456", Source: "outflare", Medium: "email"}
	hosts := Hosts("https://www.northpeak.example/", "northpeak.test", "")
	if len(hosts) != 2 || hosts[0] != "northpeak.example" {
		t.Fatalf("Hosts = %v", hosts)
	}
	for in, want := range map[string]string{
		"https://northpeak.example/pricing":          "https://northpeak.example/pricing?utm_source=outflare&utm_medium=email&utm_campaign=spring-news&ofc=abcde23456",
		"https://app.northpeak.test/x?a=1":           "https://app.northpeak.test/x?a=1&utm_source=outflare&utm_medium=email&utm_campaign=spring-news&ofc=abcde23456",
		"https://acme.example/":                      "https://acme.example/",
		"https://northpeak.example/?utm_campaign=me": "https://northpeak.example/?utm_campaign=me",
		"mailto:hello@northpeak.example":             "mailto:hello@northpeak.example",
		"https://evilnorthpeak.example/":             "https://evilnorthpeak.example/",
	} {
		if got := c.OwnLink(in, hosts); got != want {
			t.Errorf("OwnLink(%q) = %q, want %q", in, got, want)
		}
	}
}
