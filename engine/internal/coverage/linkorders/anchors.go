package linkorders

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// Where an order's primary anchor came from. se_ranking is a keyword from
// stored SE Ranking data (its keywords snapshot or keywords its projects
// track); page_target is the page's keyword target on the Keywords page;
// seo_keywords is another SEO connection's stored keywords; manual is
// typed by a person.
const (
	AnchorSourceManual      = "manual"
	AnchorSourceSERanking   = "se_ranking"
	AnchorSourcePageTarget  = "page_target"
	AnchorSourceSEOKeywords = "seo_keywords"
)

// Types of the second anchor.
const (
	AnchorBrand   = "brand"
	AnchorURL     = "url"
	AnchorKeyword = "keyword"
)

// MaxAnchorLen is the longest anchor text, in characters.
const MaxAnchorLen = 500

// Anchors are an order's two anchors. Primary is the main keyword;
// Secondary is a second, separate anchor whose type a person picks.
type Anchors struct {
	Primary       string
	PrimarySource string
	Secondary     string
	SecondaryType string
}

func validPrimarySource(s string) bool {
	switch s {
	case "", AnchorSourceManual, AnchorSourceSERanking, AnchorSourcePageTarget, AnchorSourceSEOKeywords:
		return true
	}
	return false
}

func validSecondaryType(s string) bool {
	switch s {
	case "", AnchorBrand, AnchorURL, AnchorKeyword:
		return true
	}
	return false
}

// cleanAnchor trims and collapses whitespace. Control characters and
// angle brackets are refused, since an anchor ends up in a donor's brief
// and page.
func cleanAnchor(field, s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > MaxAnchorLen {
		return "", fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, field, MaxAnchorLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return "", fmt.Errorf("%w: %s cannot contain control characters or < >", ErrInvalid, field)
		}
	}
	return s, nil
}

// looksLikeURL accepts an http(s) address or a bare domain with an
// optional path, which is how URL anchors are usually written.
func looksLikeURL(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	raw := s
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	h := u.Hostname()
	return strings.Contains(h, ".") && !strings.HasPrefix(h, ".") && !strings.HasSuffix(h, ".")
}

// NormalizeAnchors checks an order's anchors and returns them cleaned:
//   - a primary anchor without a source is manual; a source without a
//     primary anchor is cleared;
//   - a second anchor needs its type (brand, url or keyword), and a type
//     without text is refused;
//   - a url anchor must look like an address or a domain;
//   - the two anchors must differ.
func NormalizeAnchors(in Anchors) (Anchors, error) {
	var out Anchors
	var err error
	if out.Primary, err = cleanAnchor("anchor_primary", in.Primary); err != nil {
		return out, err
	}
	if out.Secondary, err = cleanAnchor("anchor_secondary", in.Secondary); err != nil {
		return out, err
	}
	if !validPrimarySource(in.PrimarySource) {
		return out, fmt.Errorf("%w: anchor_primary_source is manual, se_ranking, page_target or seo_keywords", ErrInvalid)
	}
	if !validSecondaryType(in.SecondaryType) {
		return out, fmt.Errorf("%w: anchor_secondary_type is brand, url or keyword", ErrInvalid)
	}
	out.PrimarySource = in.PrimarySource
	switch {
	case out.Primary == "":
		out.PrimarySource = ""
	case out.PrimarySource == "":
		out.PrimarySource = AnchorSourceManual
	}
	out.SecondaryType = in.SecondaryType
	switch {
	case out.Secondary != "" && out.SecondaryType == "":
		return out, fmt.Errorf("%w: anchor_secondary_type is required with anchor_secondary (brand, url or keyword)", ErrInvalid)
	case out.Secondary == "" && out.SecondaryType != "":
		return out, fmt.Errorf("%w: anchor_secondary is required when anchor_secondary_type is set", ErrInvalid)
	}
	if out.SecondaryType == AnchorURL && !looksLikeURL(out.Secondary) {
		return out, fmt.Errorf("%w: a url anchor must be an address or a domain, like example.com/pricing", ErrInvalid)
	}
	if out.Secondary != "" && out.Primary == "" {
		return out, fmt.Errorf("%w: set anchor_primary before a second anchor", ErrInvalid)
	}
	if out.Secondary != "" && strings.EqualFold(out.Secondary, out.Primary) {
		return out, fmt.Errorf("%w: the second anchor must differ from the primary anchor", ErrInvalid)
	}
	return out, nil
}
