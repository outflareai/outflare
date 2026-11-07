package crawl

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// What AI answers need from a page, beyond what search needs: whether the
// page lets them quote it, whether its questions have answers right
// under them, how fresh it says it is and who wrote it, and whether its
// structured data says what the page says. Read from the HTML the crawl
// already fetched, so it costs no extra request.

// AISignals is what the parse finds for AI answers. Stored as JSON on the
// page (crawl_pages.ai_signals).
type AISignals struct {
	// NoSnippet: a robots meta tag (robots or googlebot) or X-Robots-Tag
	// says nosnippet, which keeps the page out of Google's AI Overviews
	// and AI Mode, and Apple's Siri answers.
	NoSnippet bool `json:"nosnippet,omitempty"`
	// MaxSnippet is the smallest max-snippet set, in characters; nil when
	// none is (or it is -1, no limit).
	MaxSnippet *int `json:"max_snippet,omitempty"`
	// NoArchive: robots or bingbot says noarchive or nocache, which Bing
	// reads as limits on how Copilot may use the page.
	NoArchive bool `json:"noarchive,omitempty"`
	// DataNoSnippet counts elements marked data-nosnippet.
	DataNoSnippet int `json:"data_nosnippet,omitempty"`
	// Headings counts h2 and h3 headings; QuestionHeadings those phrased
	// as a question; Unanswered the first few of those with no direct
	// answer (a paragraph or list of at least answerMinWords words) right
	// under them.
	Headings         int      `json:"headings,omitempty"`
	QuestionHeadings int      `json:"question_headings,omitempty"`
	Unanswered       []string `json:"unanswered,omitempty"`
	// Article: the page's structured data says it is an article. Its
	// author and dates come from that, the article meta tags or <time>.
	Article   bool   `json:"article,omitempty"`
	Author    string `json:"author,omitempty"`
	Published string `json:"published,omitempty"`
	Modified  string `json:"modified,omitempty"`
	// Markdown is the URL of a Markdown copy of the page
	// (<link rel="alternate" type="text/markdown">).
	Markdown string `json:"markdown,omitempty"`
	// SchemaMismatch lists what the structured data claims that the
	// visible page doesn't show, like "Product price 49.00".
	SchemaMismatch []string `json:"schema_mismatch,omitempty"`
}

const (
	answerMinWords = 8
	maxUnanswered  = 5
	maxMismatch    = 5
)

// questionWords start a heading phrased as a question.
var questionWords = map[string]bool{"how": true, "what": true, "why": true, "when": true, "where": true, "who": true,
	"which": true, "can": true, "does": true, "do": true, "is": true, "are": true, "should": true, "will": true}

// isQuestion reports whether a heading is phrased as a question.
func isQuestion(h string) bool {
	h = strings.TrimSpace(h)
	if strings.HasSuffix(h, "?") {
		return true
	}
	first, _, _ := strings.Cut(strings.ToLower(h), " ")
	return questionWords[strings.TrimFunc(first, func(r rune) bool { return !unicode.IsLetter(r) })] && strings.Contains(h, " ")
}

// snippetDirectives reads a robots meta tag or X-Robots-Tag value for the
// rules that limit quoting: nosnippet, max-snippet, noarchive, nocache.
// An agent prefix ("googlebot: nosnippet") counts for the agents AI
// answers use and for everyone (*).
func (s *AISignals) snippetDirectives(v string) {
	for _, d := range strings.Split(strings.ToLower(v), ",") {
		d = strings.TrimSpace(d)
		if agent, rule, ok := strings.Cut(d, ":"); ok && !strings.HasPrefix(d, "max-") {
			switch strings.TrimSpace(agent) {
			case "*", "googlebot", "bingbot":
				d = strings.TrimSpace(rule)
			default:
				continue
			}
		}
		switch {
		case d == "nosnippet":
			s.NoSnippet = true
		case d == "noarchive" || d == "nocache":
			s.NoArchive = true
		case strings.HasPrefix(d, "max-snippet:"):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(d, "max-snippet:")))
			if err == nil && n >= 0 && (s.MaxSnippet == nil || n < *s.MaxSnippet) {
				s.MaxSnippet = &n
			}
		}
	}
}

// schemaClaim is something the structured data says the page shows.
type schemaClaim struct{ label, value string }

// schemaClaims are the checkable claims in a JSON-LD block: an article's
// headline, a product's name and price, an FAQ's questions.
func schemaClaims(raw string) (claims []schemaClaim, modified string) {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return nil, ""
	}
	str := func(a any) string {
		s, _ := a.(string)
		return strings.TrimSpace(s)
	}
	hasType := func(m map[string]any, want map[string]bool) bool {
		switch ty := m["@type"].(type) {
		case string:
			return want[ty]
		case []any:
			for _, e := range ty {
				if s, ok := e.(string); ok && want[s] {
					return true
				}
			}
		}
		return false
	}
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 6 {
			return
		}
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e, depth+1)
			}
		case map[string]any:
			if modified == "" {
				modified = clip(str(t["dateModified"]), 40)
			}
			switch {
			case hasType(t, articleTypes):
				if h := str(t["headline"]); h != "" {
					claims = append(claims, schemaClaim{"Article headline", h})
				}
			case hasType(t, map[string]bool{"Product": true}):
				if n := str(t["name"]); n != "" {
					claims = append(claims, schemaClaim{"Product name", n})
				}
				offers := t["offers"]
				if l, ok := offers.([]any); ok && len(l) > 0 {
					offers = l[0]
				}
				if o, ok := offers.(map[string]any); ok {
					p := str(o["price"])
					if p == "" {
						if f, ok := o["price"].(float64); ok {
							p = strconv.FormatFloat(f, 'f', -1, 64)
						}
					}
					if p != "" {
						claims = append(claims, schemaClaim{"Product price", p})
					}
				}
			case hasType(t, map[string]bool{"FAQPage": true}):
				if qs, ok := t["mainEntity"].([]any); ok {
					for i, q := range qs {
						if m, ok := q.(map[string]any); ok && i < 3 {
							if n := str(m["name"]); n != "" {
								claims = append(claims, schemaClaim{"FAQ question", n})
							}
						}
					}
				}
				return
			}
			for k, e := range t {
				if k != "@type" && k != "@context" {
					walk(e, depth+1)
				}
			}
		}
	}
	walk(v, 0)
	return claims, modified
}

// textKey is text reduced for a loose "does the page say this" match:
// lowercase letters and digits only.
func textKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shown reports whether the visible text shows a claim. A price matches
// with or without trailing zeros and thousands separators (49, 49.00,
// 1,299).
func shown(text string, c schemaClaim) bool {
	if c.label == "Product price" {
		v := strings.TrimRight(strings.TrimRight(c.value, "0"), ".")
		if !strings.Contains(c.value, ".") {
			v = c.value
		}
		plain := strings.NewReplacer(",", "", " ", "", " ", "").Replace(text)
		return v != "" && strings.Contains(plain, v)
	}
	k := textKey(c.value)
	return k == "" || strings.Contains(text, k)
}

// ParseDate reads a date as pages write it: RFC 3339, a date-time without
// a zone, or a plain date.
func ParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// aiFinish fills in what needs the whole page: the article's author and
// dates, and which structured data claims the page doesn't show.
func (p *Page) aiFinish(claims []schemaClaim, timeAt, visible string) {
	for _, t := range p.SchemaTypes {
		if articleTypes[t] {
			p.AI.Article = true
		}
	}
	p.AI.Author = p.ArticleAuthor
	if p.AI.Author == "" {
		p.AI.Author = clip(squash(p.Meta["author"]), 200)
	}
	p.AI.Published = p.ArticlePublished
	for _, k := range []string{"article:published_time", "datepublished"} {
		if p.AI.Published == "" {
			p.AI.Published = clip(strings.TrimSpace(p.Meta[k]), 40)
		}
	}
	if p.AI.Published == "" {
		p.AI.Published = timeAt
	}
	for _, k := range []string{"article:modified_time", "og:updated_time", "datemodified"} {
		if p.AI.Modified == "" {
			p.AI.Modified = clip(strings.TrimSpace(p.Meta[k]), 40)
		}
	}
	key := textKey(visible)
	seen := map[string]bool{}
	for _, c := range claims {
		ck := c.label + " " + c.value
		if seen[ck] || len(p.AI.SchemaMismatch) >= maxMismatch {
			continue
		}
		seen[ck] = true
		match := key
		if c.label == "Product price" {
			match = visible
		}
		if !shown(match, c) {
			p.AI.SchemaMismatch = append(p.AI.SchemaMismatch, clip(ck, 200))
		}
	}
}

// aiRules adds the AI answer rules a page breaks.
func aiRules(p *AuditPage, add func(p *AuditPage, rule, detail string)) {
	a := &p.AI
	if a.NoSnippet {
		add(p, "ai_snippet_blocked", "")
	} else if a.MaxSnippet != nil && *a.MaxSnippet < 50 {
		add(p, "ai_snippet_short", "max-snippet:"+strconv.Itoa(*a.MaxSnippet))
	}
	if a.NoArchive {
		add(p, "ai_copilot_limited", "")
	}
	if len(a.Unanswered) > 0 {
		add(p, "ai_question_unanswered", strings.Join(a.Unanswered, " | "))
	}
	if len(a.SchemaMismatch) > 0 {
		add(p, "schema_mismatch", strings.Join(a.SchemaMismatch, "; "))
	}
	if !a.Article {
		return
	}
	if a.Author == "" {
		add(p, "ai_no_author", "")
	}
	var newest time.Time
	for _, d := range []string{a.Published, a.Modified} {
		if t, ok := ParseDate(d); ok && t.After(newest) {
			newest = t
		}
	}
	switch {
	case newest.IsZero():
		add(p, "ai_no_date", "")
	case auditNow().Sub(newest) > staleAfter:
		add(p, "ai_stale", "last dated "+newest.Format(time.DateOnly))
	}
}
