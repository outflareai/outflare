package reviews

import (
	"os"
	"testing"
	"time"
)

func TestParsePage(t *testing.T) {
	cases := []struct {
		in, platform, id, url string
	}{
		{"https://www.trustpilot.com/review/northpeak.example", Trustpilot, "northpeak.example", "https://www.trustpilot.com/review/northpeak.example"},
		{"uk.trustpilot.com/review/Northpeak.example?utm_source=x", Trustpilot, "northpeak.example", "https://www.trustpilot.com/review/northpeak.example"},
		{"https://www.g2.com/products/northpeak/reviews?page=3", G2, "northpeak", "https://www.g2.com/products/northpeak/reviews"},
		{"g2.com/products/northpeak", G2, "northpeak", "https://www.g2.com/products/northpeak/reviews"},
		{"https://www.capterra.com/p/123456/Northpeak/", Capterra, "123456", "https://www.capterra.com/p/123456/Northpeak/reviews/"},
		{"https://www.capterra.co.uk/p/123456/Northpeak/reviews/", Capterra, "123456", "https://www.capterra.com/p/123456/Northpeak/reviews/"},
		{"https://maps.app.goo.gl/AbCd123", Google, "maps.app.goo.gl/AbCd123", "https://maps.app.goo.gl/AbCd123"},
		{"https://www.google.com/maps/place/Northpeak+Studio/@-33.8688,151.2093,17z/data=!3m1", Google, "northpeak+studio@-33.869,151.209", "https://www.google.com/maps/place/Northpeak+Studio/@-33.8688,151.2093,17z"},
		{"https://search.google.com/local/reviews?placeid=ChIJ_test", Google, "place:ChIJ_test", "https://search.google.com/local/reviews?placeid=ChIJ_test"},
	}
	for _, c := range cases {
		p, ok := ParsePage(c.in)
		if !ok || p.Platform != c.platform || p.ID != c.id || p.URL != c.url {
			t.Errorf("ParsePage(%q) = %+v, %v; want %s %s %s", c.in, p, ok, c.platform, c.id, c.url)
		}
	}
	for _, bad := range []string{"https://www.trustpilot.com/categories/software", "https://www.g2.com/categories/crm", "https://www.capterra.com/crm-software/",
		"https://www.google.com/search?q=northpeak", "https://northpeak.example/reviews", "https://www.google.com/maps"} {
		if p, ok := ParsePage(bad); ok {
			t.Errorf("ParsePage(%q) = %+v, want not a review page", bad, p)
		}
	}
}

func TestPageURL(t *testing.T) {
	p, _ := ParsePage("https://www.trustpilot.com/review/northpeak.example")
	if got := PageURL(p, 2); got != "https://www.trustpilot.com/review/northpeak.example?page=2&sort=recency" {
		t.Fatal(got)
	}
	c, _ := ParsePage("https://www.capterra.com/p/123456/Northpeak/")
	if PageURL(c, 1) != "https://www.capterra.com/p/123456/Northpeak/reviews/" || PageURL(c, 3) != "https://www.capterra.com/p/123456/Northpeak/reviews/?page=3" {
		t.Fatal(PageURL(c, 1), PageURL(c, 3))
	}
}

func TestMatches(t *testing.T) {
	tp, _ := ParsePage("https://www.trustpilot.com/review/northpeak.example")
	if !Matches(tp, "Northpeak", "northpeak.example") || Matches(tp, "Northpeak", "acme.example") {
		t.Fatal("trustpilot match by website")
	}
	g2, _ := ParsePage("https://www.g2.com/products/northpeak-studio/reviews")
	if !Matches(g2, "Northpeak Studio", "") || !Matches(g2, "Northpeak", "") || Matches(g2, "Acme", "") {
		t.Fatal("g2 match by name")
	}
	other, _ := ParsePage("https://www.g2.com/products/northpeakish/reviews")
	if Matches(other, "Northpeak", "") {
		t.Fatal("a longer name is another product")
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseTrustpilot(t *testing.T) {
	r := Parse(Trustpilot, read(t, "trustpilot.html"), time.Now())
	if r.Score == nil || *r.Score != 4.4 || r.Count == nil || *r.Count != 1284 || r.Name != "Northpeak" || !r.More {
		t.Fatalf("headline: %+v", r)
	}
	if len(r.Reviews) != 2 {
		t.Fatalf("reviews: %d", len(r.Reviews))
	}
	a, b := r.Reviews[0], r.Reviews[1]
	if a.Rating != 5 || !a.Verified || a.Reply == "" || a.RepliedAt == nil || a.Country != "AU" || a.URL != "https://www.trustpilot.com/reviews/6650a1b2c3d4e5f600000001" {
		t.Fatalf("first: %+v", a)
	}
	if b.Rating != 2 || b.Reply != "" || b.EditedAt == nil || b.PostedAt.Format("2006-01-02") != "2026-10-24" {
		t.Fatalf("second: %+v", b)
	}
}

func TestParseG2(t *testing.T) {
	r := Parse(G2, read(t, "g2.html"), time.Now())
	if r.Score == nil || *r.Score != 4.6 || r.Count == nil || *r.Count != 212 || !r.More || len(r.Reviews) != 2 {
		t.Fatalf("%+v", r)
	}
	if x := r.Reviews[0]; x.Rating != 4.5 || x.ExternalID == "" || x.URL == "" || x.Body != "What do you like best?\nQuick setup and good support." {
		t.Fatalf("%+v", x)
	}
}

func TestParseCapterra(t *testing.T) {
	r := Parse(Capterra, read(t, "capterra.html"), time.Now())
	if r.Score == nil || *r.Score != 4.5 || r.Count == nil || *r.Count != 98 || r.More || len(r.Reviews) != 1 {
		t.Fatalf("%+v", r)
	}
	if x := r.Reviews[0]; x.Rating != 5 || x.Author != "Lee" || x.ExternalID == "" {
		t.Fatalf("%+v", x)
	}
}

// Capterra's JSON-LD has no dates or ids, so its reviews come from the
// data the page carries for the browser: the reviews page, and the
// product page it sends a product with few reviews to.
func TestParseCapterraPageData(t *testing.T) {
	r := Parse(Capterra, read(t, "capterra_reviews.html"), time.Now())
	if r.Score == nil || *r.Score != 4.5 || r.Count == nil || *r.Count != 98 || r.Name != "Northpeak" || !r.More || len(r.Reviews) != 2 {
		t.Fatalf("%+v", r)
	}
	a, b := r.Reviews[0], r.Reviews[1]
	if a.ExternalID != "Capterra___1001" || a.Rating != 4 || a.Author != "Lee M." || !a.Verified || a.PostedAt.Format("2006-01-02") != "2026-09-05" ||
		a.Body != "We plan every launch in it.\n\nPros: Fast setup\n\nCons: Few reports" || a.Reply != "" {
		t.Fatalf("first: %+v", a)
	}
	if b.Rating != 2 || b.Reply == "" || b.RepliedAt == nil || b.RepliedAt.Format("2006-01-02") != "2026-08-20" {
		t.Fatalf("second: %+v", b)
	}
	p := Parse(Capterra, read(t, "capterra_product.html"), time.Now())
	if p.Count == nil || *p.Count != 1 || p.More || len(p.Reviews) != 1 {
		t.Fatalf("product page: %+v", p)
	}
	if x := p.Reviews[0]; x.ExternalID != "Capterra___2001" || x.Rating != 5 || x.Author != "Ana P." || x.PostedAt.Format("2006-01-02") != "2025-10-28" || x.Reply != "Thank you, Ana!" {
		t.Fatalf("product review: %+v", x)
	}
	// Capterra draws a "Next page" button even on a product's only page:
	// its data, not the button, says whether there is a page 2.
	if one := Parse(Capterra, read(t, "capterra_single_page.html"), time.Now()); one.More || len(one.Reviews) != 1 {
		t.Fatalf("single page: %+v", one)
	}
}

func TestPlatformRules(t *testing.T) {
	if !RenderJS(G2) || !RenderJS(Google) || !RenderJS(Trustpilot) || !RenderJS(Capterra) || RenderJS("other") ||
		NewestFirst(Capterra) || !NewestFirst(Trustpilot) {
		t.Fatal("platform reading rules")
	}
}

func TestParseGoogle(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	r := Parse(Google, read(t, "google.html"), now)
	if r.Score == nil || *r.Score != 4.7 || r.Count == nil || *r.Count != 312 || len(r.Reviews) != 2 {
		t.Fatalf("%+v", r)
	}
	a, b := r.Reviews[0], r.Reviews[1]
	if a.Author != "Mia T" || a.Rating != 5 || a.Body != "Friendly team & quick help." || a.PostedAt.Format("2006-01-02") != "2026-10-13" {
		t.Fatalf("first: %+v", a)
	}
	if b.Rating != 1 || b.Reply != "Sorry Raj, please email us." {
		t.Fatalf("second: %+v", b)
	}
}

func TestNotAReviewPage(t *testing.T) {
	if r := Parse(Trustpilot, "<html><title>Just a moment...</title></html>", time.Now()); r.Found() {
		t.Fatalf("a challenge page read as %+v", r)
	}
}

func TestScore(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	if _, ok := ScoreAt(nil, now, 0); ok {
		t.Fatal("no reviews, no score")
	}
	// Three 5-star reviews don't beat 900 at 4.6.
	few := []Rated{{5, now}, {5, now}, {5, now}}
	var many []Rated
	for i := range 900 {
		s := 5.0
		if i%10 < 4 {
			s = 4
		}
		many = append(many, Rated{s, now.AddDate(0, 0, -i%60)})
	}
	f, _ := ScoreAt(few, now, 0)
	m, _ := ScoreAt(many, now, 0)
	if f.Score >= m.Score || m.Score != 90 {
		t.Fatalf("few %+v many %+v", f, m)
	}
	// A review counts half after the half-life.
	old := []Rated{{1, now.Add(-HalfLife)}}
	o, _ := ScoreAt(old, now, 0)
	want := (PriorStars*PriorWeight + 0.5) / (PriorWeight + 0.5)
	if o.Average != float64(int(want*100+0.5))/100 {
		t.Fatalf("%+v want %.2f", o, want)
	}
}

func TestMomentum(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, -d) }
	var base []Rated
	for i := range 40 {
		base = append(base, Rated{4.5, day(60 + i*3)})
	}
	add := func(list []Rated, n int, stars float64, from, to int) []Rated {
		for i := range n {
			list = append(list, Rated{stars, day(from + i*(to-from)/max(n, 1))})
		}
		return list
	}
	if m := MomentumAt(add(nil, 3, 5, 1, 10), now, 0); m.State != TooFew {
		t.Fatalf("too few: %+v", m)
	}
	steady := add(add(append([]Rated{}, base...), 10, 4.5, 29, 55), 10, 4.5, 1, 27)
	if m := MomentumAt(steady, now, 0); m.State != Steady {
		t.Fatalf("steady: %+v", m)
	}
	growing := add(add(append([]Rated{}, base...), 10, 4.5, 29, 55), 20, 4.6, 1, 27)
	if m := MomentumAt(growing, now, 0); m.State != Growing || m.PaceChange == nil || *m.PaceChange != 1 {
		t.Fatalf("growing: %+v", m)
	}
	slipping := add(add(append([]Rated{}, base...), 10, 4.5, 29, 55), 10, 1, 1, 27)
	if m := MomentumAt(slipping, now, 0); m.State != Slipping || m.ScoreChange > -2 {
		t.Fatalf("slipping: %+v", m)
	}
	stagnating := add(add(append([]Rated{}, base...), 10, 4.5, 29, 55), 4, 4.5, 1, 27)
	if m := MomentumAt(stagnating, now, 0); m.State != Stagnating {
		t.Fatalf("stagnating: %+v", m)
	}
}

func TestSentiment(t *testing.T) {
	for stars, want := range map[float64]string{5: "positive", 4: "positive", 3: "neutral", 2: "negative", 1: "negative", 4.5: "positive"} {
		if got := Sentiment(stars); got != want {
			t.Errorf("Sentiment(%v) = %s, want %s", stars, got, want)
		}
	}
}
