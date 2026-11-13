package linkorders

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// intermediaryHeader is shaped like a broker's full database export: every
// metric column, with slashes, brackets and mixed case.
var intermediaryHeader = []string{
	"Website", "Price", "Date Added", "Geo AhrefsTop 1", "Geo AhrefsTop 2", "Main language",
	"Article type", "Traffic / Organic", "DR", "Main category", "Subcategory",
	"Organic Keywords", "Total Keywords", "Referring domains", "Referring domains / Followed",
	"Referring domains / Not followed", "Referring IPs", "Referring subnets",
	"Backlinks", "Backlinks / Followed", "Backlinks / Not followed", "Backlinks / Redirects",
	"Backlinks / Internal", "Outgoing domains / Followed", "Outgoing domains / All time",
	"Outgoing links / Followed", "Outgoing links / All time",
	"Intermediary", "Theme", "Site age", "Niche", "Sample post",
}

func TestCSVColMapsIntermediaryHeaders(t *testing.T) {
	want := []string{
		"domain", "price", MetricDateAdded, MetricGeoTop1, MetricGeoTop2, "languages",
		MetricArticleType, "traffic", "domain_rating", MetricMainCategory, MetricSubcategory,
		MetricOrganicKeywords, MetricTotalKeywords, MetricReferringDomains, MetricReferringDomainsFollowed,
		MetricReferringDomainsNoFollow, MetricReferringIPs, MetricReferringSubnets,
		MetricBacklinks, MetricBacklinksFollowed, MetricBacklinksNoFollow, MetricBacklinksRedirects,
		MetricBacklinksInternal, MetricOutgoingDomainsFollowed, MetricOutgoingDomainsAllTime,
		MetricOutgoingLinksFollowed, MetricOutgoingLinksAllTime,
		"intermediary", "theme", "site_age", "niches", "",
	}
	for i, h := range intermediaryHeader {
		if got := csvCol(h); got != want[i] {
			t.Errorf("%q: got %q, want %q", h, got, want[i])
		}
	}
}

func TestCSVColNormalizesPunctuationAndAliases(t *testing.T) {
	for h, want := range map[string]string{
		"Traffic / Organic":            "traffic",
		"traffic/organic":              "traffic",
		"TRAFFIC - ORGANIC":            "traffic",
		"Traffic (Organic)":            "traffic",
		"Organic traffic (Ahrefs)":     "traffic",
		"Domain Rating":                "domain_rating",
		"domain_authority":             "domain_rating",
		"Domain Rating (DR)":           "domain_rating",
		"Ref. domains / Dofollow":      MetricReferringDomainsFollowed,
		"Referring Domains (nofollow)": MetricReferringDomainsNoFollow,
		"RD":                           MetricReferringDomains,
		"Referring domains - all":      MetricReferringDomains,
		"Keywords / Organic":           MetricOrganicKeywords,
		"Keywords / Total":             MetricTotalKeywords,
		"Linked domains":               MetricOutgoingDomainsFollowed,
		"Outgoing links (all-time)":    MetricOutgoingLinksAllTime,
		"Geo top 2":                    MetricGeoTop2,
		"geo_ahrefstop_1":              MetricGeoTop1,
		"Sub-category":                 MetricSubcategory,
		"Broker":                       "intermediary",
		"Contact":                      "intermediary",
		"Domain age":                   "site_age",
		"Site age (years)":             "site_age",
		"Website URL":                  "domain",
		"\ufeffdomain":                 "domain",
		"amount_minor":                 "amount_minor",
		"Price, $":                     "price_usd",
		"Price €":                      "price_eur",
		"Currency":                     "currency",
		"Paid traffic cost":            "",
		"Sample post":                  "",
		"Niches":                       "niches",
		"Тематика":                     "theme",
	} {
		if got := csvCol(h); got != want {
			t.Errorf("%q: got %q, want %q", h, got, want)
		}
	}
}

func TestParseCountPriceAndSiteAge(t *testing.T) {
	for in, want := range map[string]int64{"12345": 12345, "12,345": 12345, "12 345": 12345, "12.5K": 12500, "1.2M": 1200000, "45.0": 45, "0": 0} {
		n, ok, err := ParseCount(in)
		if err != nil || !ok || n != want {
			t.Errorf("ParseCount(%q) = %d %v %v, want %d", in, n, ok, err, want)
		}
	}
	if _, ok, err := ParseCount("n/a"); ok || err != nil {
		t.Errorf("n/a should be blank")
	}
	if _, _, err := ParseCount("lots"); err == nil {
		t.Errorf("lots should fail")
	}
	for in, want := range map[string]struct {
		minor int64
		cur   string
	}{"150": {15000, ""}, "$1,200.50": {120050, "USD"}, "1.200,50 €": {120050, "EUR"}, "150 EUR": {15000, "EUR"}, "99,5": {9950, ""}} {
		n, cur, ok, err := ParsePrice(in)
		if err != nil || !ok || n != want.minor || cur != want.cur {
			t.Errorf("ParsePrice(%q) = %d %q %v %v, want %v", in, n, cur, ok, err, want)
		}
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{
		"2012":       "2012-01-01",
		"2012-05-01": "2012-05-01",
		"12 years":   "2014-11-12",
		"12":         "2014-11-12",
		"3.5 years":  "2023-05-12",
		"8 months":   "2026-03-12",
		"10 лет":     "2016-11-12",
	} {
		got, ok, err := ParseSiteAge(in, now)
		if err != nil || !ok || got.Format("2006-01-02") != want {
			t.Errorf("ParseSiteAge(%q) = %s %v %v, want %s", in, got.Format("2006-01-02"), ok, err, want)
		}
	}
	if _, _, err := ParseSiteAge("ancient", now); err == nil {
		t.Errorf("ancient should fail")
	}
	if got := SiteAgeYears(time.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC), now); got != 14 {
		t.Errorf("SiteAgeYears = %d, want 14", got)
	}
}

func TestCSVRowReadsTheFullIntermediaryRow(t *testing.T) {
	idx, ignored, err := csvHeader(intermediaryHeader)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 || ignored[0] != "Sample post" {
		t.Fatalf("ignored: %v", ignored)
	}
	rec := []string{
		"news.example", "$150", "2026-01-15", "US", "GB", "English",
		"Guest post", "12.5K", "54", "Business", "Finance",
		"3,400", "5,100", "1,200", "900", "300", "800", "600",
		"45,000", "30,000", "15,000", "120", "2,000", "250", "1,100", "400", "3,500",
		"Acme Links", "Fintech", "2012", "finance; saas", "https://news.example/sample",
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	domain, in, err := csvRow(idx, rec, now)
	if err != nil {
		t.Fatal(err)
	}
	if domain != "news.example" || **in.DomainRating != 54 || **in.Traffic != 12500 || **in.Country != "US" {
		t.Fatalf("core fields: %+v", in)
	}
	if (*in.Languages)[0] != "English" || len(*in.Niches) != 2 || **in.Intermediary != "Acme Links" || **in.Theme != "Fintech" {
		t.Fatalf("fields: %+v", in)
	}
	if (*in.SiteSince).Format("2006-01-02") != "2012-01-01" {
		t.Fatalf("site since: %v", *in.SiteSince)
	}
	if p := *in.Prices; len(p) != 1 || p[0].Currency != "USD" || p[0].AmountMinor != 15000 {
		t.Fatalf("price $150 should be 15000 cents: %+v", p)
	}
	for k, want := range map[string]any{
		MetricDateAdded: "2026-01-15", MetricGeoTop2: "GB", MetricArticleType: "Guest post",
		MetricMainCategory: "Business", MetricSubcategory: "Finance",
		MetricOrganicKeywords: int64(3400), MetricTotalKeywords: int64(5100),
		MetricReferringDomains: int64(1200), MetricReferringDomainsFollowed: int64(900), MetricReferringDomainsNoFollow: int64(300),
		MetricReferringIPs: int64(800), MetricReferringSubnets: int64(600),
		MetricBacklinks: int64(45000), MetricBacklinksFollowed: int64(30000), MetricBacklinksNoFollow: int64(15000),
		MetricBacklinksRedirects: int64(120), MetricBacklinksInternal: int64(2000),
		MetricOutgoingDomainsFollowed: int64(250), MetricOutgoingDomainsAllTime: int64(1100),
		MetricOutgoingLinksFollowed: int64(400), MetricOutgoingLinksAllTime: int64(3500),
	} {
		if in.Metrics[k] != want {
			t.Errorf("metrics[%s] = %v (%T), want %v", k, in.Metrics[k], in.Metrics[k], want)
		}
	}
	if in.Notes != nil {
		t.Errorf("intermediary and theme must not go to notes: %v", **in.Notes)
	}
}

func TestCSVRowPricesAndErrors(t *testing.T) {
	now := time.Now()
	row := func(header, rec string) (DonorWrite, error) {
		idx, _, err := csvHeader(strings.Split(header, ","))
		if err != nil {
			t.Fatal(err)
		}
		_, in, err := csvRow(idx, strings.Split(rec, ","), now)
		return in, err
	}
	in, err := row("domain,amount_minor,currency", "a.example,15000,USD")
	if err != nil || (*in.Prices)[0].AmountMinor != 15000 {
		t.Fatalf("amount_minor stays minor: %+v %v", in.Prices, err)
	}
	in, err = row("domain,price,currency", "a.example,150,EUR")
	if err != nil || (*in.Prices)[0].AmountMinor != 15000 || (*in.Prices)[0].Currency != "EUR" {
		t.Fatalf("price is major: %+v %v", in.Prices, err)
	}
	in, err = row("domain,price_usd,price_eur", "a.example,100,90")
	if err != nil || len(*in.Prices) != 2 {
		t.Fatalf("two currencies: %+v %v", in.Prices, err)
	}
	if _, err := row("domain,price,currency", "a.example,150,GBP"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("GBP should be refused: %v", err)
	}
	if _, err := row("domain,dr", "a.example,140"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DR over 100 should be refused: %v", err)
	}
	if _, err := row("domain,site age", "a.example,soon"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad site age should be refused: %v", err)
	}
	in, err = row("domain,backlinks", "a.example,-")
	if err != nil || in.Metrics[MetricBacklinks] != nil {
		t.Fatalf("a blank metric clears it: %+v %v", in.Metrics, err)
	}
	if _, ok := in.Metrics[MetricBacklinks]; !ok {
		t.Fatalf("a blank metric column should still be in the patch")
	}
}

func TestParseCatalogCSVKeepsTheFirstColumnForAField(t *testing.T) {
	f, err := ParseCatalogCSV(strings.NewReader("Website,Traffic,Traffic / Organic\na.example,10,20\n\n,,\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.idx["traffic"] != 1 || len(f.ignored) != 1 || f.ignored[0] != "Traffic / Organic" {
		t.Fatalf("idx %v ignored %v", f.idx, f.ignored)
	}
	if _, err := ParseCatalogCSV(strings.NewReader("price,dr\n1,2\n")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no domain column: %v", err)
	}
}

// The Catalog page's export writes these column names; each must read back
// into the same field so an export imports again unchanged.
func TestExportColumnsRoundTrip(t *testing.T) {
	cols := map[string]string{
		"domain": "domain", "intermediary": "intermediary", "theme": "theme", "site_since": "site_age",
		"pbn": "pbn", "country": "country", "languages": "languages", "niches": "niches",
		"casino_allowed": "casino_allowed", "rel_offered": "rel_offered", "slot_types": "slot_types",
		"duration": "duration", "tat_days": "tat_days", "guarantee_days": "guarantee_days",
		"domain_rating": "domain_rating", "traffic": "traffic", "amount_minor": "amount_minor",
		"currency": "currency", "notes": "notes",
	}
	for _, k := range MetricKeys {
		cols[k] = k
	}
	for h, want := range cols {
		if got := csvCol(h); got != want {
			t.Errorf("%q: got %q, want %q", h, got, want)
		}
	}
}
