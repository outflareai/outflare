// Package stories holds what decides whether a digital PR story may go
// out: the check a release must pass before a person can approve it.
// Every number in the release has a fact with a source, every quote is
// one its expert approved, and hype or claims that need a second look are
// flagged. The stories, facts, quotes and experts themselves live in
// package api.
package stories

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// The values a story, a fact and a quote take.
var (
	Kinds       = []string{"data", "reactive", "comment"}
	Statuses    = []string{"idea", "draft", "approved", "archived"}
	SourceKinds = []string{"own_data", "public_data", "news", "brand_fact", "other"}
	QuoteStates = []string{"draft", "approved", "withdrawn"}
)

// Fact is a fact as the check sees it.
type Fact struct {
	Statement  string
	SourceURL  string
	SourceNote string
}

// Quote is a quote as the check sees it.
type Quote struct {
	Body     string
	Approved bool
}

// Problem is one thing the check found; Detail quotes the release.
type Problem struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Result is a check: ok when there are no errors. Warnings are for the
// person approving to read; they don't stop it.
type Result struct {
	OK       bool      `json:"ok"`
	Errors   []Problem `json:"errors"`
	Warnings []Problem `json:"warnings"`
}

// The check's codes.
const (
	CodeNoRelease         = "no_release"
	CodeUnsourcedNumber   = "unsourced_number"
	CodeFactWithoutSource = "fact_without_source"
	CodeUnapprovedQuote   = "unapproved_quote"
	CodeHype              = "hype_phrase"
	CodeSecondLook        = "needs_second_look"
)

// hype are phrases journalists skip a release for.
var hype = []string{"game-changer", "game changer", "revolutionary", "groundbreaking", "ground-breaking", "world-class",
	"cutting-edge", "industry-leading", "best-in-class", "unprecedented", "disruptive", "paradigm shift", "synergy", "next-generation"}

// secondLook are words that make a finance or health claim, which a person
// should check against the rules for those.
var secondLook = []string{"guarantee", "guaranteed", "risk-free", "returns", "investment advice", "financial advice", "cure", "cures",
	"diagnose", "diagnosis", "treatment", "clinically", "medical advice", "prevents disease"}

// numberRE finds figures: 42, 3.5, 1,200, 37%, $49, 2x. A lone digit is
// left alone (one of three, step 2), as are years.
var numberRE = regexp.MustCompile(`[$€£]?\d[\d,]*(?:\.\d+)?\s?(?:%|x\b|k\b|m\b|bn\b|million|billion|percent)?`)

// quoteRE finds quoted passages in a release, straight or curly quotes.
var quoteRE = regexp.MustCompile(`["“]([^"”]{12,})["”]`)

// Check checks a release against its story's facts and quotes.
func Check(release string, facts []Fact, quotes []Quote) Result {
	r := Result{Errors: []Problem{}, Warnings: []Problem{}}
	text := strings.TrimSpace(release)
	if text == "" {
		r.Errors = append(r.Errors, Problem{Code: CodeNoRelease, Detail: "The story has no release yet."})
	}
	for _, f := range facts {
		if strings.TrimSpace(f.SourceURL) == "" && strings.TrimSpace(f.SourceNote) == "" {
			r.Errors = append(r.Errors, Problem{Code: CodeFactWithoutSource, Detail: f.Statement})
		}
	}
	// Quoted passages first, so their numbers are judged with the quote.
	var approved []string
	for _, q := range quotes {
		if q.Approved {
			approved = append(approved, normal(q.Body))
		}
	}
	for _, m := range quoteRE.FindAllStringSubmatch(text, -1) {
		said := normal(m[1])
		if !slices.ContainsFunc(approved, func(a string) bool { return strings.Contains(a, said) }) {
			r.Errors = append(r.Errors, Problem{Code: CodeUnapprovedQuote, Detail: m[1]})
		}
	}
	sourced := map[string]bool{}
	for _, f := range facts {
		for _, n := range Numbers(f.Statement) {
			sourced[n] = true
		}
	}
	seen := map[string]bool{}
	for _, n := range Numbers(text) {
		if !sourced[n] && !seen[n] {
			seen[n] = true
			r.Errors = append(r.Errors, Problem{Code: CodeUnsourcedNumber, Detail: n})
		}
	}
	lower := " " + strings.ToLower(text) + " "
	for _, p := range hype {
		if containsWord(lower, p) {
			r.Warnings = append(r.Warnings, Problem{Code: CodeHype, Detail: p})
		}
	}
	for _, p := range secondLook {
		if containsWord(lower, p) {
			r.Warnings = append(r.Warnings, Problem{Code: CodeSecondLook, Detail: p})
		}
	}
	r.OK = len(r.Errors) == 0
	return r
}

// Numbers are the figures in a text, each as its digits and unit, so "37
// percent", "37%" and "37 %" are the same figure. Years (1900 to 2100) and
// lone digits are left out.
func Numbers(text string) []string {
	var out []string
	for _, m := range numberRE.FindAllString(text, -1) {
		n := strings.ToLower(strings.TrimSpace(m))
		unit := ""
		for _, u := range []string{"%", "percent", "x", "k", "m", "bn", "million", "billion"} {
			if strings.HasSuffix(n, u) {
				unit, n = u, strings.TrimSpace(strings.TrimSuffix(n, u))
				break
			}
		}
		switch unit {
		case "percent":
			unit = "%"
		case "million":
			unit = "m"
		case "billion":
			unit = "bn"
		}
		cur := ""
		if strings.ContainsAny(n[:1], "$€£") {
			cur, n = n[:1], n[1:]
		}
		n = strings.ReplaceAll(n, ",", "")
		n = strings.TrimSuffix(n, ".")
		if unit == "" && cur == "" {
			if len(n) == 1 {
				continue
			}
			if y, err := strconv.Atoi(n); err == nil && len(n) == 4 && y >= 1900 && y <= 2100 {
				continue
			}
		}
		out = append(out, cur+n+unit)
	}
	return out
}

// normal is text as quotes are compared: lowercase letters, digits and
// single spaces.
func normal(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// containsWord reports whether lower (padded with spaces) has phrase as
// whole words.
func containsWord(lower, phrase string) bool {
	i := 0
	for {
		j := strings.Index(lower[i:], phrase)
		if j < 0 {
			return false
		}
		j += i
		before, after := rune(lower[j-1]), rune(' ')
		if j+len(phrase) < len(lower) {
			after = rune(lower[j+len(phrase)])
		}
		if !unicode.IsLetter(before) && !unicode.IsLetter(after) {
			return true
		}
		i = j + 1
	}
}
