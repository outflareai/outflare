package api

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Draft checks (plans/paid-ads/plan.md, "Knowledge sheet"): what the
// writer of a new ad is warned about, beside the platform's own rules.
// They never refuse a draft; Google's limits stay the only refusals. A
// claim is checked against the brand's knowledge sheet (a price the sheet
// doesn't have, a superlative, a competitor's name), and a responsive
// search ad against what works (eleven or more distinct headlines across
// keywords, benefits, features, calls to action and authority; sentence
// case; short headlines; pins only with three variants a position; one
// evergreen unpinned ad per ad group).

// adDraftCheck is one warning on a draft.
type adDraftCheck struct {
	Code string `json:"code"`
	// Level is warning (worth fixing before approval) or note (a soft
	// suggestion).
	Level   string `json:"level"`
	Message string `json:"message"`
}

// adSuperlatives are the claims Google and the sheet can't back.
var adSuperlatives = []string{"best", "#1", "number one", "no. 1", "cheapest", "guaranteed", "guarantee", "leading", "fastest", "lowest price"}

var adWordRE = regexp.MustCompile(`[\p{L}\p{N}#'’.-]+`)

// adDraftChecks works out a draft's warnings against the brand's sheet,
// its competitors and the ad group it goes in.
func (s *Server) adDraftChecks(ctx context.Context, ws, brand string, d connectors.AdDraft, p adDraftPlace) ([]adDraftCheck, error) {
	sheet, _, _, _, err := loadBrandKnowledge(ctx, s.pool, ws, brand)
	if err != nil {
		return nil, err
	}
	competitors, err := s.brandCompetitorNames(ctx, ws, brand)
	if err != nil {
		return nil, err
	}
	enabledAds := 0
	if p.group != nil && p.connector == "google_ads" {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ad_entities WHERE workspace_id = $1 AND parent_id = $2 AND level = 'ad' AND status = 'active'`,
			ws, *p.group).Scan(&enabledAds); err != nil {
			return nil, err
		}
	}
	return draftChecks(d, sheet, competitors, p.connector == "google_ads", enabledAds), nil
}

// brandCompetitorNames are the names a brand's tracked competitors go by.
func (s *Server) brandCompetitorNames(ctx context.Context, ws, brand string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, aliases, host FROM seo_competitors WHERE workspace_id = $1 AND brand_id = $2 AND status = 'tracked'`, ws, brand)
	if err != nil {
		return nil, err
	}
	var out []string
	_, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) {
		var name, host string
		var aliases []string
		if err := row.Scan(&name, &aliases, &host); err != nil {
			return struct{}{}, err
		}
		for _, n := range append([]string{name}, aliases...) {
			if n = strings.TrimSpace(n); utf8.RuneCountInString(n) >= 3 {
				out = append(out, n)
			}
		}
		if h := strings.TrimPrefix(strings.ToLower(host), "www."); h != "" {
			out = append(out, h)
		}
		return struct{}{}, nil
	})
	return out, err
}

// draftChecks is the pure part: the warnings for a draft given the sheet,
// the competitors' names, whether it is a Google search ad, and how many
// enabled ads the ad group already has.
func draftChecks(d connectors.AdDraft, sheet BrandKnowledge, competitors []string, search bool, enabledAds int) []adDraftCheck {
	checks := []adDraftCheck{}
	add := func(code, level, msg string) {
		checks = append(checks, adDraftCheck{Code: code, Level: level, Message: msg})
	}
	copyLines := append(append([]string{}, d.Headlines...), d.Descriptions...)
	if d.PrimaryText != "" {
		copyLines = append(copyLines, d.PrimaryText)
	}
	copyText := strings.Join(copyLines, "\n")
	lower := strings.ToLower(copyText)

	// Prices and percentages the sheet doesn't have.
	known := knowledgeNumbers(sheet.Text())
	var unknown []string
	seen := map[string]bool{}
	for _, m := range knowledgePriceRE.FindAllString(copyText, -1) {
		for n := range knowledgeNumbers(m) {
			if !known[n] && !seen[m] {
				seen[m] = true
				unknown = append(unknown, strings.TrimSpace(m))
			}
		}
	}
	if len(unknown) > 0 {
		add("price_not_in_sheet", "warning", "the sheet doesn't have the price or percentage "+quoteList(unknown)+"; a person checks it, or it comes out")
	}
	// Superlatives nobody can back.
	var supers []string
	for _, w := range adSuperlatives {
		if containsWord(lower, w) {
			supers = append(supers, w)
		}
	}
	if len(supers) > 0 {
		add("superlative", "warning", "says "+quoteList(supers)+": a superlative needs proof on the page, and the sheet has none")
	}
	// Another company's name.
	var named []string
	for _, c := range competitors {
		if containsWord(lower, strings.ToLower(c)) {
			named = append(named, c)
		}
	}
	if len(named) > 0 {
		add("competitor_named", "warning", "names a competitor "+quoteList(named)+"; Google refuses trademarks in text and the claim can't come from the sheet")
	}
	if !search {
		return checks
	}

	// A responsive search ad: eleven or more distinct headlines, so
	// Google has enough to combine.
	distinct := map[string]bool{}
	for _, h := range d.Headlines {
		distinct[strings.ToLower(strings.Join(strings.Fields(h), " "))] = true
	}
	if len(distinct) < 11 {
		add("few_headlines", "warning", fmt.Sprintf("%d distinct headlines; an ad does best with 11 or more across keywords, benefits, features, calls to action and authority", len(distinct)))
	}
	// Pins: each pinned position with fewer than three variants starves
	// the ad of combinations; an evergreen ad pins nothing.
	variants := map[int]int{}
	for _, pin := range d.HeadlinePins {
		if pin > 0 {
			variants[pin]++
		}
	}
	thin := 0
	for _, n := range variants {
		if n < 3 {
			thin++
		}
	}
	if len(variants) > 1 && thin > 0 {
		add("pins_thin", "warning", fmt.Sprintf("%d headline positions are pinned and %d of them have fewer than 3 variants; pin nothing for the evergreen ad, or give each pinned position three", len(variants), thin))
	}
	// Title Case Headlines read as ads; sentence case reads as answers.
	var titled []string
	for _, h := range d.Headlines {
		if isTitleCase(h) {
			titled = append(titled, h)
		}
	}
	if len(titled) > 0 {
		add("title_case", "warning", "headlines in title case "+quoteList(titled)+"; write them in sentence case")
	}
	// Short headlines fit more positions and read faster.
	var long []string
	for _, h := range d.Headlines {
		if utf8.RuneCountInString(strings.TrimSpace(h)) > 20 {
			long = append(long, h)
		}
	}
	if len(long) > 0 {
		add("long_headline", "note", "headlines over 20 characters "+quoteList(long)+"; shorter ones fit more positions")
	}
	// One evergreen ad per ad group; a second splits the traffic, a third
	// starves them all.
	if d.Kind == connectors.DraftAd && enabledAds > 1 {
		add("rsa_crowded", "warning", fmt.Sprintf("the ad group already runs %d enabled ads; one evergreen unpinned ad and at most one challenger do best, so pause one first", enabledAds))
	}
	return checks
}

// containsWord reports whether text has phrase as whole words.
func containsWord(text, phrase string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], phrase)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(phrase)
		before := start == 0 || !isWordRune(rune(text[start-1]))
		after := end == len(text) || !isWordRune(rune(text[end]))
		if before && after {
			return true
		}
		i = start + 1
		if i >= len(text) {
			return false
		}
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// isTitleCase reports whether most words after the first start with a
// capital: "Close Deals Faster Today" is, "Close deals faster today" and
// "Acme CRM for small teams" are not.
func isTitleCase(h string) bool {
	words := adWordRE.FindAllString(h, -1)
	if len(words) < 3 {
		return false
	}
	capped, counted := 0, 0
	for _, w := range words[1:] {
		r, _ := utf8.DecodeRuneInString(w)
		if !unicode.IsLetter(r) {
			continue
		}
		// An acronym (CRM, SEO) is a name, not title case.
		if strings.ToUpper(w) == w && utf8.RuneCountInString(w) > 1 {
			continue
		}
		counted++
		if unicode.IsUpper(r) {
			capped++
		}
	}
	return counted >= 2 && capped*2 > counted
}

func quoteList(items []string) string {
	q := make([]string, 0, len(items))
	for i, it := range items {
		if i == 5 {
			q = append(q, fmt.Sprintf("and %d more", len(items)-5))
			break
		}
		q = append(q, `"`+it+`"`)
	}
	return strings.Join(q, ", ")
}
