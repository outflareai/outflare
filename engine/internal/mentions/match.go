// Package mentions finds where a brand or its competitors are named in
// text: a post, an article, a video's captions or an episode's transcript.
// It only reads text it is given; finding the text is the sources' job.
package mentions

import (
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Subject is one thing listened for: the brand, or one competitor.
type Subject struct {
	// About is "brand" or "competitor".
	About string
	// CompetitorID is the competitor's id; empty for the brand.
	CompetitorID string
	// Name is how it is shown.
	Name string
	// Terms are what counts as naming it: its name, aliases and site.
	Terms []string
}

// Hit is one subject named in a text.
type Hit struct {
	Subject *Subject
	// Matched is the term found, as listened for.
	Matched string
	// Quote is the words around the first place it was named.
	Quote string
	// Offset is where in the text, in bytes.
	Offset int
}

// minTerm is the shortest term matched: shorter ones ("AI", "Go") name too
// many other things.
const minTerm = 3

// quoteWidth is about how many characters a quote keeps each side of a name.
const quoteWidth = 160

// Terms builds a subject's automatic terms from its name and website host:
// only what can name nothing else. The whole host is always listened for
// ("acme.example"); the name only when it is a brand name (see Branded).
// Descriptions ("Proxy network provider"), plain-language names ("Web
// scraping API") and a host's bare first label ("webscraping") match too
// many posts that aren't about anyone, so they are left out; people add a
// term by hand when they want one.
func Terms(name, host string) []string {
	var out []string
	add := func(t string) { out = AddTerm(out, t) }
	if Branded(name) {
		add(name)
	}
	if h := hostOf(host); h != "" {
		add(h)
	}
	return out
}

// AddTerm adds a term to a list once (any case), when it is long enough to
// match: shorter ones ("AI", "Go") name too many other things.
func AddTerm(list []string, t string) []string {
	t = strings.TrimSpace(t)
	if !ValidTerm(t) {
		return list
	}
	if slices.ContainsFunc(list, func(o string) bool { return strings.EqualFold(o, t) }) {
		return list
	}
	return append(list, t)
}

// ValidTerm reports whether a term is long enough, and short enough, to
// listen for.
func ValidTerm(t string) bool {
	t = strings.TrimSpace(t)
	return utf8.RuneCountInString(t) >= minTerm && len(t) <= 200
}

// Branded reports whether a name is a brand's own name rather than words
// anyone uses: one word that isn't an everyday product word ("Northpeak",
// "Summitly"), or several words that all start with a capital letter and
// aren't all everyday product words ("Bright Peak"). "Web scraping API",
// "Web Scraper API" and "proxy" are not.
func Branded(name string) bool {
	words := strings.Fields(name)
	if len(words) == 0 {
		return false
	}
	plain := 0
	for _, w := range words {
		if len(words) > 1 {
			r, _ := utf8.DecodeRuneInString(w)
			if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
				return false
			}
		}
		if commonWords[strings.ToLower(strings.Trim(w, ".,:;!?()'\""))] {
			plain++
		}
	}
	return plain < len(words)
}

// commonWords are everyday words in product names: a name made only of
// them describes a kind of product, not one company's.
var commonWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and the of for to in on by with your my our
		ai api apis app apps web website site online cloud data search engine
		proxy proxies proxied scraper scrapers scraping scrape crawler crawling crawl
		browser browsers network networks provider providers service services
		platform tool tools software solution solutions server servers hosting
		residential mobile datacenter rotating ip ips vpn email mail marketing
		analytics seo agent agents bot bots automation unblocker extract extractor
		extraction parser parsing serp rank tracker monitor monitoring insights
		lead leads sales crm ads ad social video videos media content news
		pro plus premium free fast smart best top global hub labs lab io inc llc ltd co
		company group studio studios digital tech technologies systems solutions
		applications application`) {
		m[w] = true
	}
	return m
}()

// hostOf is a website's host, lowercase and without www., from a host or URL.
func hostOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Hostname()
	}
	s, _, _ = strings.Cut(s, "/")
	return strings.TrimPrefix(s, "www.")
}

// Find reports each subject named in text, once, at the first place one of
// its terms appears as a whole word (any case). Longer terms are tried
// first, so "Acme Proxy" wins over "Acme" in the same place.
func Find(text string, subjects []Subject) []Hit {
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	var hits []Hit
	for i := range subjects {
		s := &subjects[i]
		terms := slices.Clone(s.Terms)
		slices.SortStableFunc(terms, func(a, b string) int { return len(b) - len(a) })
		best, bestTerm := -1, ""
		for _, t := range terms {
			at := wordIndex(lower, strings.ToLower(t))
			if at >= 0 && (best < 0 || at < best) {
				best, bestTerm = at, t
			}
		}
		if best >= 0 {
			hits = append(hits, Hit{Subject: s, Matched: bestTerm, Offset: best, Quote: Quote(text, best, len(bestTerm))})
		}
	}
	return hits
}

// wordIndex is the first index of term in text where it stands as a whole
// word, or -1. Both are lowercase.
func wordIndex(text, term string) int {
	if term == "" {
		return -1
	}
	from := 0
	for {
		i := strings.Index(text[from:], term)
		if i < 0 {
			return -1
		}
		at := from + i
		end := at + len(term)
		if boundaryBefore(text, at) && boundaryAfter(text, end) {
			return at
		}
		from = at + 1
	}
}

func boundaryBefore(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !isWord(r)
}

func boundaryAfter(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	// "acme's" and "acme." still name acme; "acmes" or "acme.example" do not.
	if r == '.' {
		next, _ := utf8.DecodeRuneInString(s[i+1:])
		return i+1 >= len(s) || !isWord(next)
	}
	return !isWord(r)
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' }

// Quote is the text around [at, at+n), cut at word edges, with "…" where it
// was cut, and its whitespace folded.
func Quote(text string, at, n int) string {
	start, end := at-quoteWidth, at+n+quoteWidth
	cutStart, cutEnd := start > 0, end < len(text)
	if !cutStart {
		start = 0
	} else {
		start = runeStart(text, start)
		if sp := strings.IndexAny(text[start:at], " \n\t"); sp >= 0 {
			start += sp + 1
		}
	}
	if !cutEnd {
		end = len(text)
	} else {
		end = runeStart(text, end)
		if sp := strings.LastIndexAny(text[at+n:end], " \n\t"); sp >= 0 {
			end = at + n + sp
		}
	}
	q := strings.Join(strings.Fields(text[start:end]), " ")
	if cutStart {
		q = "…" + q
	}
	if cutEnd {
		q += "…"
	}
	return q
}

func runeStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
