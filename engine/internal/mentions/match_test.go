package mentions

import (
	"strings"
	"testing"
	"time"
)

func TestTerms(t *testing.T) {
	got := Terms("Northpeak", "https://www.northpeak.example/pricing")
	want := []string{"Northpeak", "northpeak.example"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("terms = %q, want %q", got, want)
	}
	// A plain-language name is not listened for, and neither is the
	// host's bare first label: only the whole site.
	if got := Terms("Web scraping API", "webscraping.example"); strings.Join(got, "|") != "webscraping.example" {
		t.Fatalf("plain name: %q", got)
	}
	if got := Terms("", "get.example"); len(got) != 1 || got[0] != "get.example" {
		t.Fatalf("host only: %q", got)
	}
}

func TestBranded(t *testing.T) {
	for name, want := range map[string]bool{
		"Northpeak":               true,
		"Summitly":                true,
		"Bright Peak":             true,
		"Northpeak Web Proxy":     true,
		"northpeak.example":       true,
		"Web scraping API":        false,
		"Web Scraper API":         false,
		"Web search API for AI":   false,
		"Proxy network provider":  false,
		"proxy":                   false,
		"Residential Proxies":     false,
		"Northpeak proxy service": false,
		"":                        false,
	} {
		if got := Branded(name); got != want {
			t.Errorf("Branded(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAddTerm(t *testing.T) {
	list := AddTerm(nil, " Summitly ")
	list = AddTerm(list, "summitly")
	list = AddTerm(list, "NP")
	if strings.Join(list, "|") != "Summitly" {
		t.Fatalf("list = %q", list)
	}
}

func TestFind(t *testing.T) {
	subjects := []Subject{
		{About: "brand", Name: "Acme", Terms: []string{"Acme"}},
		{About: "competitor", CompetitorID: "c1", Name: "Northpeak", Terms: []string{"Northpeak", "northpeak.example"}},
		{About: "competitor", CompetitorID: "c2", Name: "Gone", Terms: []string{"Gone"}},
	}
	text := "I switched from northpeak.example last month. Acmes are not Acme-like, but Acme's dashboard is clearer than Northpeak's."
	hits := Find(text, subjects)
	if len(hits) != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Subject.Name != "Acme" || text[hits[0].Offset:hits[0].Offset+4] != "Acme" || !strings.HasPrefix(text[hits[0].Offset:], "Acme's") {
		t.Fatalf("brand hit at the wrong place: %+v", hits[0])
	}
	if hits[1].Matched != "northpeak.example" || hits[1].Offset != 16 {
		t.Fatalf("competitor hit: %+v", hits[1])
	}
	if Find("nothing here", subjects) != nil {
		t.Fatal("found something in nothing")
	}
	// Any case, whole words only, and a sentence's full stop is an edge.
	if h := Find("we use ACME.", subjects[:1]); len(h) != 1 {
		t.Fatal("missed ACME.")
	}
	if h := Find("acme.example.org", subjects[:1]); len(h) != 0 {
		t.Fatal("matched inside a domain")
	}
}

func TestQuote(t *testing.T) {
	text := strings.Repeat("word ", 100) + "Acme is great " + strings.Repeat("more ", 100)
	at := strings.Index(text, "Acme")
	q := Quote(text, at, 4)
	if !strings.HasPrefix(q, "…word") || !strings.HasSuffix(q, "more…") || !strings.Contains(q, "Acme is great") || len(q) > 2*quoteWidth+20 {
		t.Fatalf("quote = %q", q)
	}
	if q := Quote("short Acme text", 6, 4); q != "short Acme text" {
		t.Fatalf("short quote = %q", q)
	}
}

func TestScore(t *testing.T) {
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	week := now.Add(-48 * time.Hour)
	big, small := int64(250000), int64(40)
	hi := Score("competitor", "youtube", &big, &week, now)
	lo := Score("brand", "news", &small, nil, now)
	if hi <= lo || hi > 100 || lo < 0 {
		t.Fatalf("scores %d, %d", hi, lo)
	}
}

func TestOwnerOf(t *testing.T) {
	owners := []Owner{
		{About: "brand", Name: "Northpeak", Host: "northpeak.example", Profiles: []string{"https://www.youtube.com/@northpeakhq"}},
		{About: "competitor", CompetitorID: "c1", Name: "Bright Peak", Host: "brightpeak.example"},
		{About: "competitor", CompetitorID: "c2", Name: "Summitly", Host: "https://summitly.example/"},
	}
	for _, c := range []struct{ name, url, want string }{
		{"Northpeak HQ", "https://m.youtube.com/@NorthpeakHQ/videos", "brand"},
		{"Bright Peak", "https://www.youtube.com/channel/UCabc", "c1"},
		{"Some channel", "https://www.youtube.com/@summitly", "c2"},
		{"Ana Reviews", "https://www.youtube.com/@anareviews", ""},
		{"", "", ""},
	} {
		o := OwnerOf(owners, c.name, c.url)
		got := ""
		if o != nil {
			got = o.About
			if o.CompetitorID != "" {
				got = o.CompetitorID
			}
		}
		if got != c.want {
			t.Errorf("OwnerOf(%q, %q) = %q, want %q", c.name, c.url, got, c.want)
		}
	}
	if k := ProfileKey("https://www.youtube.com/@Northpeak/videos?x=1"); k != "youtube.com/@northpeak" {
		t.Fatalf("key = %q", k)
	}
}
