package aivis

import (
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/outflareai/outflare/engine/internal/llm"
)

// Entity is a brand or competitor an answer may name.
type Entity struct {
	Name string `json:"name"`
	// Domains are its websites (example.com); a citation of a page on one
	// of them, or a subdomain, is its page.
	Domains []string `json:"domains"`
	// Aliases are other names it goes by in answers.
	Aliases []string `json:"aliases"`
}

// terms are the words that name e: its name, aliases and domains.
func (e Entity) terms() []string {
	out := []string{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if utf8.RuneCountInString(t) >= 2 && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, t) }) {
			out = append(out, t)
		}
	}
	add(e.Name)
	for _, a := range e.Aliases {
		add(a)
	}
	for _, d := range e.Domains {
		add(Host(d))
	}
	return out
}

// Mention is an entity an answer names.
type Mention struct {
	Entity   string `json:"entity"` // brand or competitor
	Name     string `json:"name"`
	Position int    `json:"position"`
	Count    int    `json:"count"`
}

// Citation is a page an answer cites.
type Citation struct {
	URL        string `json:"url"`
	Host       string `json:"host"`
	Title      string `json:"title"`
	Position   int    `json:"position"`
	Owner      string `json:"owner"` // brand, competitor or other
	Competitor string `json:"competitor"`
}

// Mentions finds the brand and competitors in an answer: how often each is
// named and in what order. A name counts only as a whole word, in any
// case, so "Acme" is not found in "Acmeville".
func Mentions(answer string, brand Entity, competitors []Entity) []Mention {
	type found struct {
		Mention
		first int
	}
	var list []found
	look := func(kind string, e Entity) {
		// Terms can overlap ("Acme" in "acme.example" or "Acme Proxies"):
		// each stretch of text counts once.
		type span struct{ at, end int }
		var spans []span
		for _, t := range e.terms() {
			for _, at := range wordMatches(answer, t) {
				spans = append(spans, span{at, at + len(t)})
			}
		}
		sort.Slice(spans, func(i, j int) bool {
			if spans[i].at != spans[j].at {
				return spans[i].at < spans[j].at
			}
			return spans[i].end > spans[j].end
		})
		first, count, upTo := -1, 0, -1
		for _, sp := range spans {
			if sp.at < upTo {
				continue
			}
			if first < 0 {
				first = sp.at
			}
			count++
			upTo = sp.end
		}
		if count > 0 {
			list = append(list, found{Mention{Entity: kind, Name: e.Name, Count: count}, first})
		}
	}
	look("brand", brand)
	for _, c := range competitors {
		if !strings.EqualFold(c.Name, brand.Name) {
			look("competitor", c)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].first < list[j].first })
	out := make([]Mention, len(list))
	for i, f := range list {
		f.Position = i + 1
		out[i] = f.Mention
	}
	return out
}

// wordMatches returns where term starts in s as a whole word, ignoring case.
func wordMatches(s, term string) []int {
	ls, lt := strings.ToLower(s), strings.ToLower(term)
	var out []int
	for from := 0; from < len(ls); {
		i := strings.Index(ls[from:], lt)
		if i < 0 {
			break
		}
		at := from + i
		end := at + len(lt)
		before, _ := utf8.DecodeLastRuneInString(ls[:at])
		after, _ := utf8.DecodeRuneInString(ls[end:])
		if (at == 0 || !wordRune(before)) && (end == len(ls) || !wordRune(after)) {
			out = append(out, at)
		}
		from = end
	}
	return out
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// urlInText finds web addresses written in an answer, including Markdown
// links.
var urlInText = regexp.MustCompile(`https?://[^\s<>()\[\]"'` + "`" + `]+`)

// Citations lists the pages an answer cites: the ones the provider says it
// used, then addresses written in the answer, each once, with whose page
// it is.
func Citations(answer string, cited []llm.Citation, brand Entity, competitors []Entity) []Citation {
	var out []Citation
	seen := map[string]bool{}
	add := func(raw, title string) {
		u, host, ok := cleanURL(raw)
		if !ok || seen[u] {
			return
		}
		seen[u] = true
		c := Citation{URL: u, Host: host, Title: clip(title, 300), Position: len(out) + 1, Owner: "other"}
		switch {
		case ownsHost(brand, host):
			c.Owner = "brand"
		default:
			for _, comp := range competitors {
				if ownsHost(comp, host) {
					c.Owner, c.Competitor = "competitor", comp.Name
					break
				}
			}
		}
		out = append(out, c)
	}
	for _, c := range cited {
		add(c.URL, c.Title)
	}
	for _, m := range urlInText.FindAllString(answer, 50) {
		add(strings.TrimRight(m, ".,;:!?*_"), "")
	}
	return out
}

// cleanURL keeps an http(s) address without its fragment and the tracking
// tags providers add (utm_source=openai).
func cleanURL(raw string) (string, string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 2000 {
		return "", "", false
	}
	u.Fragment = ""
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Host = strings.ToLower(u.Host)
	return u.String(), Host(u.Host), true
}

// Host is a domain or address's host, lowercase and without www.
func Host(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Host
		}
	}
	s = strings.TrimSuffix(strings.SplitN(s, "/", 2)[0], ".")
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[i:], "]") {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}

// ownsHost reports whether host is one of e's domains or under one.
func ownsHost(e Entity, host string) bool {
	for _, d := range e.Domains {
		if d = Host(d); d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
