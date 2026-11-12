package linkorders

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func f(v float64) *float64 { return &v }

func TestNormalizeTargetURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Northpeak.example/Blog/Proxies/": "https://northpeak.example/Blog/Proxies",
		"northpeak.example":                           "https://northpeak.example/",
		"http://northpeak.example/pricing?ref=x#top":  "http://northpeak.example/pricing",
		"https://northpeak.example:8443/a":            "https://northpeak.example:8443/a",
	} {
		got, err := NormalizeTargetURL(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://northpeak.example/x", "https://user:pw@northpeak.example/", "not a url", "https://nodot/"} {
		if _, err := NormalizeTargetURL(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: want ErrInvalid, got %v", bad, err)
		}
	}
}

func TestGuessKind(t *testing.T) {
	for u, want := range map[string]string{
		"https://northpeak.example/blog/what-proxies-do":      TargetArticle,
		"https://northpeak.example/guides/setup":              TargetArticle,
		"https://northpeak.example/how-to-scrape-prices":      TargetArticle,
		"https://northpeak.example/residential-vs-datacenter": TargetArticle,
		"https://northpeak.example/pricing":                   TargetProduct,
		"https://northpeak.example/proxies/residential":       TargetProduct,
		"https://northpeak.example/":                          TargetProduct,
	} {
		if got := GuessKind(u); got != want {
			t.Errorf("%s: got %s, want %s", u, got, want)
		}
	}
}

func TestOnBrandHost(t *testing.T) {
	hosts := []string{"northpeak.example"}
	if !onBrandHost("https://northpeak.example/a", hosts) || !onBrandHost("https://app.northpeak.example/a", hosts) {
		t.Fatal("own host refused")
	}
	if onBrandHost("https://rival.example/a", hosts) || onBrandHost("https://northpeak.example.rival.example/", hosts) {
		t.Fatal("other host accepted")
	}
	if !onBrandHost("https://anything.example/", nil) {
		t.Fatal("no known hosts should accept")
	}
}

func TestNormalizeAnchors(t *testing.T) {
	got, err := NormalizeAnchors(Anchors{Primary: "  Residential   proxies ", Secondary: "Northpeak", SecondaryType: AnchorBrand})
	if err != nil || got.Primary != "Residential proxies" || got.PrimarySource != AnchorSourceManual || got.SecondaryType != AnchorBrand {
		t.Fatalf("got %+v %v", got, err)
	}
	got, err = NormalizeAnchors(Anchors{Primary: "free proxy", PrimarySource: AnchorSourceSERanking, Secondary: "northpeak.example/pricing", SecondaryType: AnchorURL})
	if err != nil || got.PrimarySource != AnchorSourceSERanking {
		t.Fatalf("url anchor: %+v %v", got, err)
	}
	got, err = NormalizeAnchors(Anchors{PrimarySource: AnchorSourceSERanking})
	if err != nil || got.PrimarySource != "" {
		t.Fatalf("empty primary keeps no source: %+v %v", got, err)
	}
	long := strings.Repeat("a", MaxAnchorLen+1)
	for name, in := range map[string]Anchors{
		"secondary without type": {Primary: "a", Secondary: "b"},
		"type without secondary": {Primary: "a", SecondaryType: AnchorKeyword},
		"bad type":               {Primary: "a", Secondary: "b", SecondaryType: "slogan"},
		"bad source":             {Primary: "a", PrimarySource: "ahrefs_paid"},
		"url not a url":          {Primary: "a", Secondary: "two words", SecondaryType: AnchorURL},
		"same as primary":        {Primary: "Free Proxy", Secondary: "free proxy", SecondaryType: AnchorKeyword},
		"secondary only":         {Secondary: "Northpeak", SecondaryType: AnchorBrand},
		"html":                   {Primary: "<a href=x>"},
		"too long":               {Primary: long},
	} {
		if _, err := NormalizeAnchors(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func suggestionFixture() targetInputs {
	unticked := Target{ID: uuid.New(), URL: "https://northpeak.example/legacy", Kind: TargetProduct, Selected: false}
	saved := Target{ID: uuid.New(), URL: "https://northpeak.example/blog/proxy-guide", Kind: TargetArticle, Selected: true}
	return targetInputs{
		BrandName: "Northpeak",
		Hosts:     []string{"northpeak.example"},
		Saved:     []Target{unticked, saved},
		PageTargets: []pageTargetRow{
			{URL: "https://northpeak.example/proxies/residential", Keyword: "residential proxies", Secondary: []string{"buy residential proxies"}},
		},
		OurKeywords: []kwRow{
			{Connector: "seranking", Keyword: "Free Proxy", URL: "https://www.northpeak.example/free-proxy-list/", Position: f(13), Volume: f(40500)},
			{Connector: "seranking", Keyword: "proxy site", URL: "https://northpeak.example/free-proxy-list", Position: f(28), Volume: f(135000)},
			{Connector: "ahrefs", Keyword: "proxy list", URL: "https://northpeak.example/free-proxy-list", Position: f(9), Volume: f(9000)},
			{Connector: "ahrefs", Keyword: "residential proxies", URL: "https://northpeak.example/proxies/residential", Position: f(4), Volume: f(20000)},
			{Connector: "ahrefs", Keyword: "legacy plan", URL: "https://northpeak.example/legacy", Position: f(40), Volume: f(10)},
			{Connector: "seranking", Keyword: "off site", URL: "https://rival.example/x", Position: f(1)},
		},
		Competitors: []string{"rival.example", "other.example"},
		CompKeywords: []compKwRow{
			// gap: rival ranks 3, we rank 13 -> free-proxy-list
			{Competitor: "rival.example", Keyword: "free proxy", URL: "https://rival.example/free-proxies", Position: f(3), Volume: f(40500)},
			// gap: we rank 28
			{Competitor: "other.example", Keyword: "proxy site", URL: "https://other.example/proxy-sites", Position: f(5), Volume: f(135000)},
			// not a gap: we rank 4, they rank 3
			{Competitor: "rival.example", Keyword: "residential proxies", URL: "https://rival.example/residential", Position: f(3), Volume: f(20000)},
			// competitor ranks outside the top 20: ignored
			{Competitor: "rival.example", Keyword: "legacy plan", URL: "https://rival.example/legacy", Position: f(35)},
			// no page of ours: a content gap, ignored
			{Competitor: "rival.example", Keyword: "mobile proxies", URL: "https://rival.example/mobile", Position: f(2)},
		},
		CompLinks: []compLinkRow{
			{Competitor: "rival.example", TargetURL: "https://rival.example/residential", SourceDomain: "a.example"},
			{Competitor: "rival.example", TargetURL: "https://rival.example/residential", SourceDomain: "www.b.example"},
			{Competitor: "rival.example", TargetURL: "https://rival.example/residential", SourceDomain: "b.example"},
			// no keywords stored for this page: matched by its address
			{Competitor: "other.example", TargetURL: "https://other.example/blog/proxy-guide-2026", SourceDomain: "c.example"},
		},
	}
}

func TestBuildSuggestions(t *testing.T) {
	got := buildSuggestions(suggestionFixture(), "", 0)
	byURL := map[string]TargetSuggestion{}
	for _, s := range got.Data {
		byURL[s.URL] = s
	}
	if _, ok := byURL["https://northpeak.example/legacy"]; ok {
		t.Fatal("an unticked saved target must not be suggested")
	}
	for u := range byURL {
		if targetHost(u) != "northpeak.example" {
			t.Fatalf("suggested a page off the brand's site: %s", u)
		}
	}
	free := byURL["https://northpeak.example/free-proxy-list"]
	if len(free.Reasons) != 2 || free.Reasons[0].Kind != ReasonKeywordGap || free.Kind != TargetProduct || !free.KindGuessed {
		t.Fatalf("free-proxy-list: %+v", free)
	}
	if len(free.Competitors) != 2 || free.Competitors[0] != "other.example" {
		t.Fatalf("competitors: %v", free.Competitors)
	}
	if got.Data[0].URL != free.URL {
		t.Fatalf("the page with two high-volume gaps should rank first: %s", got.Data[0].URL)
	}
	res := byURL["https://northpeak.example/proxies/residential"]
	if len(res.Reasons) != 1 || res.Reasons[0].Kind != ReasonCompetitorLinks || res.Reasons[0].ReferringDomains != 2 || res.Reasons[0].Keyword != "residential proxies" {
		t.Fatalf("residential: %+v", res.Reasons)
	}
	guide := byURL["https://northpeak.example/blog/proxy-guide"]
	if guide.TargetID == nil || !guide.Selected || guide.Kind != TargetArticle || guide.KindGuessed || len(guide.Reasons) != 1 || guide.Reasons[0].CompetitorURL != "https://other.example/blog/proxy-guide-2026" {
		t.Fatalf("saved guide matched by slug: %+v", guide)
	}
	if got.Sources.SERankingKeywords != 2 || got.Sources.OtherKeywords != 3 || got.Sources.PageTargets != 1 || got.Sources.CompetitorLinkPages != 2 {
		t.Fatalf("sources: %+v", got.Sources)
	}

	articles := buildSuggestions(suggestionFixture(), TargetArticle, 0)
	if len(articles.Data) != 1 || articles.Data[0].URL != guide.URL {
		t.Fatalf("kind filter: %+v", articles.Data)
	}
	if one := buildSuggestions(suggestionFixture(), "", 1); len(one.Data) != 1 {
		t.Fatalf("limit: %d", len(one.Data))
	}
	empty := buildSuggestions(targetInputs{BrandName: "Northpeak"}, "", 0)
	if empty.Data == nil || len(empty.Data) != 0 || empty.Sources.Competitors == nil {
		t.Fatalf("empty: %+v", empty)
	}
}

func TestAnchorOptions(t *testing.T) {
	got := buildSuggestions(suggestionFixture(), "", 0)
	var free TargetSuggestion
	for _, s := range got.Data {
		if s.URL == "https://northpeak.example/free-proxy-list" {
			free = s
		}
	}
	p := free.Anchors.Primary
	// SE Ranking first, by volume; then the other connection's keyword.
	if len(p) != 3 || p[0].Text != "proxy site" || p[0].Source != AnchorSourceSERanking || p[1].Text != "free proxy" || p[2].Source != AnchorSourceSEOKeywords {
		t.Fatalf("primary: %+v", p)
	}
	sec := free.Anchors.Secondary
	if len(sec.Brand) != 1 || sec.Brand[0].Text != "Northpeak" {
		t.Fatalf("brand: %+v", sec.Brand)
	}
	if len(sec.URL) != 3 || sec.URL[0].Text != free.URL || sec.URL[1].Text != "northpeak.example" || sec.URL[2].Text != "northpeak.example/free-proxy-list" {
		t.Fatalf("url: %+v", sec.URL)
	}
	if len(sec.Keyword) != 2 || sec.Keyword[0].Text != "free proxy" {
		t.Fatalf("keyword: %+v", sec.Keyword)
	}

	res := anchorOptions("https://northpeak.example/proxies/residential", "Northpeak", nil,
		[]kwRow{{Connector: "ahrefs", Keyword: "residential proxies", Volume: f(20000)}},
		pageTargetRow{Keyword: "residential proxies", Secondary: []string{"buy residential proxies"}})
	if len(res.Primary) != 2 || res.Primary[0].Source != AnchorSourcePageTarget || res.Primary[1].Text != "buy residential proxies" {
		t.Fatalf("page target before other keywords, deduped: %+v", res.Primary)
	}
	none := anchorOptions("https://northpeak.example/", "", nil, nil, pageTargetRow{})
	if len(none.Primary) != 0 || len(none.Secondary.Brand) != 0 || len(none.Secondary.URL) != 2 || none.Secondary.Keyword == nil {
		t.Fatalf("no data: %+v", none)
	}
}
