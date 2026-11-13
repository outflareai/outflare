package linkorders

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DonorMetrics is the intermediary's metrics for a catalog site that are
// shown on the donor card but not filtered on. DR, traffic, country and
// languages keep their own columns. Every field is optional: a nil value
// means the import or the person never set it. Stored as one JSON object
// (coverage_donor_profiles.metrics) holding only the keys that are set.
type DonorMetrics struct {
	DateAdded                *string `json:"date_added"`
	GeoTop1                  *string `json:"geo_top1"`
	GeoTop2                  *string `json:"geo_top2"`
	ArticleType              *string `json:"article_type"`
	MainCategory             *string `json:"main_category"`
	Subcategory              *string `json:"subcategory"`
	OrganicKeywords          *int64  `json:"organic_keywords"`
	TotalKeywords            *int64  `json:"total_keywords"`
	ReferringDomains         *int64  `json:"referring_domains"`
	ReferringDomainsFollowed *int64  `json:"referring_domains_followed"`
	ReferringDomainsNoFollow *int64  `json:"referring_domains_not_followed"`
	ReferringIPs             *int64  `json:"referring_ips"`
	ReferringSubnets         *int64  `json:"referring_subnets"`
	Backlinks                *int64  `json:"backlinks"`
	BacklinksFollowed        *int64  `json:"backlinks_followed"`
	BacklinksNoFollow        *int64  `json:"backlinks_not_followed"`
	BacklinksRedirects       *int64  `json:"backlinks_redirects"`
	BacklinksInternal        *int64  `json:"backlinks_internal"`
	OutgoingDomainsFollowed  *int64  `json:"outgoing_domains_followed"`
	OutgoingDomainsAllTime   *int64  `json:"outgoing_domains_all_time"`
	OutgoingLinksFollowed    *int64  `json:"outgoing_links_followed"`
	OutgoingLinksAllTime     *int64  `json:"outgoing_links_all_time"`
}

// Metric keys, as stored and as the API names them.
const (
	MetricDateAdded                = "date_added"
	MetricGeoTop1                  = "geo_top1"
	MetricGeoTop2                  = "geo_top2"
	MetricArticleType              = "article_type"
	MetricMainCategory             = "main_category"
	MetricSubcategory              = "subcategory"
	MetricOrganicKeywords          = "organic_keywords"
	MetricTotalKeywords            = "total_keywords"
	MetricReferringDomains         = "referring_domains"
	MetricReferringDomainsFollowed = "referring_domains_followed"
	MetricReferringDomainsNoFollow = "referring_domains_not_followed"
	MetricReferringIPs             = "referring_ips"
	MetricReferringSubnets         = "referring_subnets"
	MetricBacklinks                = "backlinks"
	MetricBacklinksFollowed        = "backlinks_followed"
	MetricBacklinksNoFollow        = "backlinks_not_followed"
	MetricBacklinksRedirects       = "backlinks_redirects"
	MetricBacklinksInternal        = "backlinks_internal"
	MetricOutgoingDomainsFollowed  = "outgoing_domains_followed"
	MetricOutgoingDomainsAllTime   = "outgoing_domains_all_time"
	MetricOutgoingLinksFollowed    = "outgoing_links_followed"
	MetricOutgoingLinksAllTime     = "outgoing_links_all_time"
)

// textMetrics are the metrics that hold text; every other metric is a
// whole, non-negative count.
var textMetrics = map[string]bool{
	MetricDateAdded: true, MetricGeoTop1: true, MetricGeoTop2: true,
	MetricArticleType: true, MetricMainCategory: true, MetricSubcategory: true,
}

// MetricKeys lists every metric, in the order the donor card and the CSV
// export show them.
var MetricKeys = []string{
	MetricDateAdded, MetricGeoTop1, MetricGeoTop2, MetricArticleType,
	MetricMainCategory, MetricSubcategory, MetricOrganicKeywords, MetricTotalKeywords,
	MetricReferringDomains, MetricReferringDomainsFollowed, MetricReferringDomainsNoFollow,
	MetricReferringIPs, MetricReferringSubnets,
	MetricBacklinks, MetricBacklinksFollowed, MetricBacklinksNoFollow, MetricBacklinksRedirects, MetricBacklinksInternal,
	MetricOutgoingDomainsFollowed, MetricOutgoingDomainsAllTime, MetricOutgoingLinksFollowed, MetricOutgoingLinksAllTime,
}

// metricLabels are the names the donor card and error messages use.
var metricLabels = map[string]string{
	MetricDateAdded: "Date added", MetricGeoTop1: "Geo top 1", MetricGeoTop2: "Geo top 2",
	MetricArticleType: "Article type", MetricMainCategory: "Main category", MetricSubcategory: "Subcategory",
	MetricOrganicKeywords: "Organic keywords", MetricTotalKeywords: "Total keywords",
	MetricReferringDomains: "Referring domains", MetricReferringDomainsFollowed: "Referring domains, followed",
	MetricReferringDomainsNoFollow: "Referring domains, not followed", MetricReferringIPs: "Referring IPs",
	MetricReferringSubnets: "Referring subnets", MetricBacklinks: "Backlinks", MetricBacklinksFollowed: "Backlinks, followed",
	MetricBacklinksNoFollow: "Backlinks, not followed", MetricBacklinksRedirects: "Backlinks, redirects",
	MetricBacklinksInternal: "Backlinks, internal", MetricOutgoingDomainsFollowed: "Outgoing domains, followed",
	MetricOutgoingDomainsAllTime: "Outgoing domains, all time", MetricOutgoingLinksFollowed: "Outgoing links, followed",
	MetricOutgoingLinksAllTime: "Outgoing links, all time",
}

// IsMetric reports whether k is a metric key.
func IsMetric(k string) bool {
	for _, x := range MetricKeys {
		if x == k {
			return true
		}
	}
	return false
}

// maxMetricText bounds a text metric.
const maxMetricText = 200

// ValidateMetrics checks a metrics patch from the API: known keys only,
// text for text metrics and whole non-negative numbers for counts, nil to
// clear. It returns the patch with numbers as int64.
func ValidateMetrics(raw map[string]json.RawMessage) (map[string]any, error) {
	out := map[string]any{}
	for k, b := range raw {
		if !IsMetric(k) {
			return nil, fmt.Errorf("%w: metrics.%s is not a known metric", ErrInvalid, k)
		}
		if string(b) == "null" {
			out[k] = nil
			continue
		}
		if textMetrics[k] {
			var s string
			if err := json.Unmarshal(b, &s); err != nil {
				return nil, fmt.Errorf("%w: metrics.%s must be a string", ErrInvalid, k)
			}
			s = strings.TrimSpace(s)
			if len(s) > maxMetricText {
				return nil, fmt.Errorf("%w: metrics.%s is longer than %d characters", ErrInvalid, k, maxMetricText)
			}
			if s == "" {
				out[k] = nil
			} else {
				out[k] = s
			}
			continue
		}
		var n int64
		if err := json.Unmarshal(b, &n); err != nil || n < 0 {
			return nil, fmt.Errorf("%w: metrics.%s must be a whole number, 0 or more", ErrInvalid, k)
		}
		out[k] = n
	}
	return out, nil
}

// blankValues are spreadsheet cells that mean "no value".
var blankValues = map[string]bool{"": true, "-": true, "—": true, "–": true, "n/a": true, "na": true, "none": true, "null": true, "nan": true, "?": true}

func isBlank(s string) bool { return blankValues[strings.ToLower(strings.TrimSpace(s))] }

// ParseCount reads a count as brokers write it: "12345", "12,345",
// "12 345", "12.5K", "1.2M", "45.0". ok is false for a blank cell.
func ParseCount(s string) (n int64, ok bool, err error) {
	s = strings.TrimSpace(s)
	if isBlank(s) {
		return 0, false, nil
	}
	s = strings.NewReplacer(",", "", " ", "", "\u00a0", "", "_", "", "'", "").Replace(s)
	mult := 1.0
	switch last := strings.ToLower(s[len(s)-1:]); last {
	case "k":
		mult, s = 1e3, s[:len(s)-1]
	case "m":
		mult, s = 1e6, s[:len(s)-1]
	case "b":
		mult, s = 1e9, s[:len(s)-1]
	}
	s = strings.TrimSuffix(s, "+")
	f, perr := strconv.ParseFloat(s, 64)
	if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f*mult > math.MaxInt64/2 {
		return 0, false, fmt.Errorf("want a whole number, 0 or more")
	}
	return int64(math.Round(f * mult)), true, nil
}

var moneyCurrency = regexp.MustCompile(`(?i)(usd|eur|\$|€)`)

// ParsePrice reads a price in major units as a price list writes it:
// "150", "$1,200.50", "1.200,50 €", "150 EUR". It returns minor units and
// the currency the cell names, if any.
func ParsePrice(s string) (minor int64, currency string, ok bool, err error) {
	s = strings.TrimSpace(s)
	if isBlank(s) {
		return 0, "", false, nil
	}
	if m := moneyCurrency.FindString(s); m != "" {
		switch strings.ToUpper(m) {
		case "USD", "$":
			currency = CurrencyUSD
		case "EUR", "€":
			currency = CurrencyEUR
		}
	}
	s = moneyCurrency.ReplaceAllString(s, "")
	s = strings.NewReplacer(" ", "", "\u00a0", "", "'", "").Replace(s)
	lastComma, lastDot := strings.LastIndex(s, ","), strings.LastIndex(s, ".")
	switch {
	case lastComma >= 0 && lastDot >= 0:
		if lastComma > lastDot { // 1.200,50
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else { // 1,200.50
			s = strings.ReplaceAll(s, ",", "")
		}
	case lastComma >= 0:
		if len(s)-lastComma-1 <= 2 && strings.Count(s, ",") == 1 { // 150,5 or 150,50
			s = strings.Replace(s, ",", ".", 1)
		} else { // 1,200
			s = strings.ReplaceAll(s, ",", "")
		}
	}
	f, perr := strconv.ParseFloat(s, 64)
	if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e12 {
		return 0, "", false, fmt.Errorf("want an amount such as 150 or 1,200.50")
	}
	return int64(math.Round(f * 100)), currency, true, nil
}

var dateLayouts = []string{
	"2006-01-02", time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006/01/02",
	"02.01.2006", "2.1.2006", "01/02/2006", "1/2/2006", "02-01-2006",
	"Jan 2, 2006", "January 2, 2006", "2 Jan 2006", "2 January 2006", "Jan 2006", "January 2006",
	"2006-01", "01/2006", "01.2006",
}

// ParseDate reads a date in the shapes spreadsheets use. Slashed dates are
// read month first (01/02/2006 is 2 January); dotted ones day first.
func ParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

var ageYears = regexp.MustCompile(`(?i)^\s*(\d+(?:[.,]\d+)?)\s*(y|yr|yrs|year|years|г|год|года|лет)?\.?\s*(old)?\s*$`)
var ageMonths = regexp.MustCompile(`(?i)^\s*(\d+)\s*(m|mo|mos|month|months)\.?\s*(old)?\s*$`)

// ParseSiteAge reads how long a site has existed and returns the date it
// has existed since. It takes a year ("2012" is 1 January 2012), a date
// ("2012-05-01"), or an age ("12 years", "12", "3.5 years", "8 months",
// counted back from now). ok is false for a blank cell.
func ParseSiteAge(s string, now time.Time) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if isBlank(s) {
		return time.Time{}, false, nil
	}
	now = now.UTC()
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	if y, err := strconv.Atoi(s); err == nil && y >= 1985 && y <= now.Year() {
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), true, nil
	}
	if t, ok := ParseDate(s); ok {
		if t.Year() < 1985 || t.After(now) {
			return time.Time{}, false, fmt.Errorf("the date is out of range")
		}
		return day(t), true, nil
	}
	if m := ageMonths.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 12*60 {
			return time.Time{}, false, fmt.Errorf("want a year, a date, or an age such as 12 years")
		}
		return day(now.AddDate(0, -n, 0)), true, nil
	}
	if m := ageYears.FindStringSubmatch(s); m != nil {
		f, _ := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
		if f > 60 {
			return time.Time{}, false, fmt.Errorf("want a year, a date, or an age such as 12 years")
		}
		months := int(math.Round(f * 12))
		return day(now.AddDate(0, -months, 0)), true, nil
	}
	return time.Time{}, false, fmt.Errorf("want a year, a date, or an age such as 12 years")
}

// SiteAgeYears is the whole number of years from since to now.
func SiteAgeYears(since, now time.Time) int {
	y := now.Year() - since.Year()
	if now.Month() < since.Month() || (now.Month() == since.Month() && now.Day() < since.Day()) {
		y--
	}
	if y < 0 {
		y = 0
	}
	return y
}
