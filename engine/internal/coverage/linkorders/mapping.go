package linkorders

import (
	"regexp"
	"strings"
)

var headerNonAlnum = regexp.MustCompile(`[^\p{L}\p{N}$€]+`)

// normHeader folds a header to lowercase words joined by "_": case,
// spaces, slashes, dots, brackets and other punctuation all fold away, so
// "Traffic / Organic", "traffic-organic" and "Traffic (organic)" are all
// traffic_organic.
func normHeader(h string) string {
	s := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
	s = headerNonAlnum.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

// headerAliases maps a normalized header to the field it fills. Fields
// are the donor's own columns (domain, domain_rating, traffic, ...), the
// metric keys (MetricKeys), and price, price_usd and price_eur, which are
// in major units. Headers not listed here go through csvColRule.
var headerAliases = map[string]string{
	// Site.
	"domain": "domain", "domains": "domain", "host": "domain", "website": "domain", "websites": "domain",
	"web_site": "domain", "site": "domain", "sites": "domain", "url": "domain", "site_url": "domain",
	"website_url": "domain", "domain_name": "domain",
	"name": "name", "site_name": "name", "title": "name",

	// Price. price/amount are major units; amount_minor stays minor.
	"amount_minor": "amount_minor", "price_minor": "amount_minor", "price_cents": "amount_minor",
	"price": "price", "prices": "price", "amount": "price", "cost": "price", "rate": "price",
	"guest_post_price": "price", "gp_price": "price", "article_price": "price", "post_price": "price",
	"publication_price": "price", "price_per_post": "price", "donor_price": "price", "selling_price": "price",
	"price_usd": "price_usd", "usd_price": "price_usd", "price_$": "price_usd", "$": "price_usd", "usd": "price_usd",
	"price_eur": "price_eur", "eur_price": "price_eur", "price_€": "price_eur", "€": "price_eur", "eur": "price_eur",
	"currency": "currency", "cur": "currency", "ccy": "currency",

	// Metrics with their own column.
	"dr": "domain_rating", "domain_rating": "domain_rating", "ahrefs_dr": "domain_rating", "dr_ahrefs": "domain_rating",
	"ahrefs_domain_rating": "domain_rating", "domain_authority": "domain_rating",
	"traffic": "traffic", "traffic_organic": "traffic", "organic_traffic": "traffic", "organic": "traffic",
	"ahrefs_traffic": "traffic", "traffic_ahrefs": "traffic", "monthly_traffic": "traffic", "traffic_monthly": "traffic",
	"organic_traffic_ahrefs": "traffic", "ahrefs_organic_traffic": "traffic", "traffic_organic_ahrefs": "traffic",
	"country": "country", "geo": "country", "top_country": "country", "country_code": "country",
	"language": "languages", "languages": "languages", "main_language": "languages", "lang": "languages",
	"site_language": "languages", "primary_language": "languages",
	"niche": "niches", "niches": "niches",

	// Offer terms.
	"pbn": "pbn", "is_pbn": "pbn",
	"casino": "casino_allowed", "casino_allowed": "casino_allowed", "gambling": "casino_allowed",
	"rel": "rel_offered", "rel_offered": "rel_offered", "link_type": "rel_offered", "dofollow": "rel_offered",
	"slot_type": "slot_types", "slot_types": "slot_types", "placement_type": "slot_types",
	"duration": "duration", "link_duration": "duration", "duration_days": "duration_days",
	"tat": "tat_days", "tat_days": "tat_days", "turnaround": "tat_days", "turnaround_days": "tat_days",
	"guarantee": "guarantee_days", "guarantee_days": "guarantee_days",
	"vendor": "vendor_id", "vendor_id": "vendor_id",
	"notes": "notes", "note": "notes", "comments": "notes", "comment": "notes", "remarks": "notes",

	// Intermediary, theme and site age: their own fields, not notes.
	"intermediary": "intermediary", "intermediary_name": "intermediary", "intermediary_contact": "intermediary",
	"broker": "intermediary", "broker_name": "intermediary", "broker_contact": "intermediary",
	"middleman": "intermediary", "reseller": "intermediary", "agency": "intermediary", "seller": "intermediary",
	"contact": "intermediary", "contact_name": "intermediary", "contacts": "intermediary", "supplier": "intermediary",
	"посредник": "intermediary", "контакт": "intermediary",
	"theme": "theme", "themes": "theme", "site_theme": "theme", "website_theme": "theme", "topic": "theme",
	"тематика": "theme", "тема": "theme",
	"site_age": "site_age", "domain_age": "site_age", "age": "site_age", "site_age_years": "site_age",
	"domain_age_years": "site_age", "age_years": "site_age", "site_since": "site_age", "domain_since": "site_age",
	"since": "site_age", "domain_created": "site_age", "domain_registered": "site_age", "registered": "site_age",
	"creation_date": "site_age", "founded": "site_age", "возраст": "site_age", "возраст_сайта": "site_age",

	// Intermediary metrics shown on the donor card.
	"date_added": MetricDateAdded, "added": MetricDateAdded, "added_at": MetricDateAdded, "date": MetricDateAdded,
	"added_on": MetricDateAdded, "date_of_adding": MetricDateAdded,
	"article_type": MetricArticleType, "type_of_article": MetricArticleType, "article_types": MetricArticleType,
	"post_type": MetricArticleType, "content_type": MetricArticleType, "type": MetricArticleType,
	"main_category": MetricMainCategory, "category": MetricMainCategory, "category_main": MetricMainCategory,
	"subcategory": MetricSubcategory, "sub_category": MetricSubcategory, "subcategories": MetricSubcategory,
	"category_sub": MetricSubcategory, "secondary_category": MetricSubcategory,
}

// csvCol returns the field a header column fills, or "" when the import
// does not read it.
func csvCol(header string) string {
	h := normHeader(header)
	if h == "" {
		return ""
	}
	if f, ok := headerAliases[h]; ok {
		return f
	}
	return csvColRule(h)
}

// csvColRule recognises the metric families a broker export names in many
// ways ("Referring domains / Followed", "RD dofollow", "Backlinks (not
// followed)", "Outgoing links - all time", "Geo AhrefsTop 2").
func csvColRule(h string) string {
	words := strings.Split(h, "_")
	has := func(ws ...string) bool {
		for _, w := range ws {
			for _, x := range words {
				if x == w {
					return true
				}
			}
		}
		return false
	}
	notFollowed := has("not", "no", "nofollow", "nofollowed", "nf") || strings.Contains(h, "not_followed") || strings.Contains(h, "no_follow")
	followed := !notFollowed && has("followed", "follow", "dofollow", "df", "do")
	allTime := strings.Contains(h, "all_time") || has("alltime", "historical", "ever")
	switch {
	case has("geo", "country", "countries") && (has("top", "ahrefstop", "top1", "top2") || strings.Contains(h, "top")):
		if has("2", "top2", "second") || strings.HasSuffix(h, "2") {
			return MetricGeoTop2
		}
		return MetricGeoTop1
	case has("referring", "ref", "rd", "refdomains", "referring_domains") && has("ips", "ip"):
		return MetricReferringIPs
	case has("referring", "ref") && has("subnets", "subnet"):
		return MetricReferringSubnets
	case (has("referring", "ref") && has("domains", "domain")) || has("rd", "refdomains", "referringdomains"):
		switch {
		case notFollowed:
			return MetricReferringDomainsNoFollow
		case followed:
			return MetricReferringDomainsFollowed
		}
		return MetricReferringDomains
	case has("backlinks", "backlink", "bl"):
		switch {
		case notFollowed:
			return MetricBacklinksNoFollow
		case followed:
			return MetricBacklinksFollowed
		case has("redirects", "redirect", "redirected"):
			return MetricBacklinksRedirects
		case has("internal"):
			return MetricBacklinksInternal
		}
		return MetricBacklinks
	case has("outgoing", "linked", "external", "outbound") && has("domains", "domain"):
		if allTime {
			return MetricOutgoingDomainsAllTime
		}
		return MetricOutgoingDomainsFollowed
	case has("outgoing", "external", "outbound") && has("links", "link"):
		if allTime {
			return MetricOutgoingLinksAllTime
		}
		return MetricOutgoingLinksFollowed
	case has("keywords", "keyword", "kw", "kws"):
		if has("total", "all") {
			return MetricTotalKeywords
		}
		return MetricOrganicKeywords
	case has("subcategory", "subcategories") || strings.HasPrefix(h, "sub_categor"):
		return MetricSubcategory
	case has("category", "categories"):
		return MetricMainCategory
	case has("article", "post") && has("type", "types"):
		return MetricArticleType
	case has("added") || (has("date") && has("added", "add", "created")):
		return MetricDateAdded
	case has("intermediary", "broker", "reseller"):
		return "intermediary"
	case has("age") || (has("domain", "site") && has("since", "created", "registered")):
		return "site_age"
	case has("traffic") && !has("paid", "value", "cost"):
		return "traffic"
	case has("dr") || strings.Contains(h, "domain_rating"):
		return "domain_rating"
	case has("language", "lang"):
		return "languages"
	case has("niche", "niches"):
		return "niches"
	case has("theme", "themes"):
		return "theme"
	}
	return ""
}
