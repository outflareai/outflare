// Package pricing reads the plans and prices off one pricing page, and says
// what changed between two reads. It runs on the page as read (HTML), with
// no AI: a page's own structured data (schema.org offers) when it has
// some, otherwise the first price under each heading, or in each row of a
// pricing table. A page with no recognisable price reads as no plans, and
// the caller says so instead of guessing.
package pricing

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// MaxPlans is how many plans one page keeps.
const MaxPlans = 40

// Plan is one plan or price on a page.
type Plan struct {
	// Name is the plan's name as the page shows it.
	Name string `json:"name"`
	// Price is the amount; Currency its ISO code when the page shows one.
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
	// Period is what the price is for: month, year, or a unit the page
	// names (gb, ip, request); empty when the page doesn't say.
	Period string `json:"period"`
	// Text is the price as shown, such as "$49/mo".
	Text string `json:"text"`
}

// Change is one difference between two reads of a page.
type Change struct {
	// Kind is added, removed, price_up, price_down or changed (the same
	// amount for another period or currency).
	Kind   string `json:"kind"`
	Plan   string `json:"plan"`
	Before *Plan  `json:"before"`
	After  *Plan  `json:"after"`
}

var (
	symbols = map[string]string{"$": "USD", "us$": "USD", "€": "EUR", "£": "GBP", "¥": "JPY", "₹": "INR", "a$": "AUD", "c$": "CAD"}
	// A price: a symbol before the amount, or a code or symbol after it.
	priceRe = regexp.MustCompile(`(?i)(us\$|a\$|c\$|\$|€|£|¥|₹)\s?(\d{1,3}(?:[,\s]\d{3})+(?:\.\d{1,2})?|\d+(?:[.,]\d{1,2})?)|(\d{1,3}(?:[,\s]\d{3})+(?:[.,]\d{1,2})?|\d+(?:[.,]\d{1,2})?)\s?(usd|eur|gbp|€|\$)`)
	// The period right after the price ("/mo", "per month", "a year").
	periodRe = regexp.MustCompile(`(?i)^\s*(?:/|per\s+|a\s+|an\s+|each\s+)?\s*(mo|month|monthly|yr|year|yearly|annually|annual|gb|ip|request|req|user|seat|1k requests|1,000 requests)\b`)
	spaces   = regexp.MustCompile(`\s+`)
)

// Extract reads a page's plans: its schema.org offers when it has any,
// otherwise prices found by heading and in table rows. A page with none
// gives nil.
func Extract(page string) []Plan {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil
	}
	if plans := fromStructured(doc); len(plans) > 0 {
		return keep(plans)
	}
	return keep(fromText(doc))
}

// keep drops repeats (a page with a monthly and yearly toggle shows each
// plan twice) and caps the list.
func keep(plans []Plan) []Plan {
	seen := map[string]bool{}
	var out []Plan
	for _, p := range plans {
		p.Name = clip(p.Name, 120)
		if p.Name == "" {
			continue
		}
		k := strings.ToLower(p.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
		if len(out) == MaxPlans {
			break
		}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

// fromStructured reads Offer, AggregateOffer and Product offers out of the
// page's JSON-LD.
func fromStructured(doc *html.Node) []Plan {
	var plans []Plan
	var walk func(v any, name string)
	walk = func(v any, name string) {
		switch x := v.(type) {
		case []any:
			for _, y := range x {
				walk(y, name)
			}
		case map[string]any:
			if n, ok := x["name"].(string); ok && n != "" {
				name = n
			}
			if isType(x["@type"], "Offer", "AggregateOffer") {
				if p, ok := offer(x, name); ok {
					plans = append(plans, p)
				}
			}
			for k, y := range x {
				if k == "@context" {
					continue
				}
				walk(y, name)
			}
		}
	}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Script && strings.Contains(strings.ToLower(attr(n, "type")), "ld+json") && n.FirstChild != nil {
			var v any
			if json.Unmarshal([]byte(n.FirstChild.Data), &v) == nil {
				walk(v, "")
			}
		}
	}
	return plans
}

func isType(v any, names ...string) bool {
	switch t := v.(type) {
	case string:
		for _, n := range names {
			if strings.EqualFold(t, n) || strings.HasSuffix(strings.ToLower(t), "/"+strings.ToLower(n)) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if isType(x, names...) {
				return true
			}
		}
	}
	return false
}

func offer(x map[string]any, name string) (Plan, bool) {
	price, ok := number(x["price"])
	if !ok {
		if price, ok = number(x["lowPrice"]); !ok {
			if spec, isMap := x["priceSpecification"].(map[string]any); isMap {
				price, ok = number(spec["price"])
				if u, _ := spec["unitText"].(string); u != "" {
					x["unitText"] = u
				}
			}
		}
	}
	if !ok {
		return Plan{}, false
	}
	cur, _ := x["priceCurrency"].(string)
	period := ""
	if u, _ := x["unitText"].(string); u != "" {
		period = periodOf(u)
	}
	p := Plan{Name: name, Price: price, Currency: strings.ToUpper(cur), Period: period}
	p.Text = strings.TrimSpace(strconv.FormatFloat(price, 'f', -1, 64) + " " + p.Currency)
	return p, true
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(t), ",", ""), 64)
		return f, err == nil
	}
	return 0, false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// skip is what never holds a plan: scripts, styles and the site's chrome.
func skip(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Svg, atom.Nav, atom.Footer, atom.Head:
		return true
	}
	return false
}

func heading(n *html.Node) bool {
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5:
		return true
	}
	return false
}

// block elements end a run of text; inline ones join it, so a price split
// over spans ("$", "49", "/mo") reads as one.
func block(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Div, atom.P, atom.Li, atom.Ul, atom.Ol, atom.Section, atom.Article, atom.Td, atom.Th, atom.Tr, atom.Table, atom.Br, atom.Header, atom.Main, atom.Aside, atom.Button:
		return true
	}
	return heading(n)
}

// text is a node's visible text, inline elements joined.
func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skip(n) {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && block(n) {
			b.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && block(n) {
			b.WriteString(" ")
		}
	}
	walk(n)
	return strings.TrimSpace(spaces.ReplaceAllString(b.String(), " "))
}

// fromText finds plans in the page's text: each table row whose first cell
// names a plan and another holds a price, then the first price after each
// heading, before the next one.
func fromText(doc *html.Node) []Plan {
	var plans []Plan
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.DataAtom != atom.Tr || hidden(n) {
			continue
		}
		var cells []string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
				cells = append(cells, text(c))
			}
		}
		if len(cells) < 2 {
			continue
		}
		for _, c := range cells[1:] {
			if p, ok := findPrice(c); ok {
				p.Name = cells[0]
				plans = append(plans, p)
				break
			}
		}
	}
	// Headings in page order, each with the text up to the next heading.
	type section struct {
		name string
		body strings.Builder
	}
	var secs []*section
	var cur *section
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (skip(n) || n.DataAtom == atom.Table || hidden(n)) {
			return
		}
		if n.Type == html.ElementNode && heading(n) {
			cur = &section{name: text(n)}
			secs = append(secs, cur)
			return
		}
		if n.Type == html.TextNode && cur != nil {
			cur.body.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && block(n) && cur != nil {
			cur.body.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && block(n) && cur != nil {
			cur.body.WriteString(" ")
		}
	}
	walk(doc)
	for _, s := range secs {
		if p, ok := findPrice(spaces.ReplaceAllString(s.body.String(), " ")); ok {
			p.Name = s.name
			plans = append(plans, p)
		}
	}
	return plans
}

// hidden is an element the page hides from people.
func hidden(n *html.Node) bool {
	if _, ok := attrOK(n, "hidden"); ok {
		return true
	}
	style := strings.ReplaceAll(strings.ToLower(attr(n, "style")), " ", "")
	return strings.Contains(style, "display:none") || attr(n, "aria-hidden") == "true"
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// findPrice is the first price in a run of text, with its period.
func findPrice(s string) (Plan, bool) {
	for _, m := range priceRe.FindAllStringSubmatchIndex(s, -1) {
		var sym, amount string
		if m[2] >= 0 {
			sym, amount = s[m[2]:m[3]], s[m[4]:m[5]]
		} else {
			amount, sym = s[m[6]:m[7]], s[m[8]:m[9]]
		}
		v, ok := parseAmount(amount)
		if !ok {
			continue
		}
		cur := symbols[strings.ToLower(sym)]
		if cur == "" {
			cur = strings.ToUpper(sym)
		}
		p := Plan{Price: v, Currency: cur, Text: strings.TrimSpace(s[m[0]:m[1]])}
		if pm := periodRe.FindStringSubmatchIndex(s[m[1]:]); pm != nil {
			p.Period = periodOf(s[m[1]+pm[2] : m[1]+pm[3]])
			p.Text = strings.TrimSpace(s[m[0] : m[1]+pm[1]])
		}
		return p, true
	}
	return Plan{}, false
}

// parseAmount reads "1,299", "1 299", "49.99" and "49,99".
func parseAmount(a string) (float64, bool) {
	a = strings.ReplaceAll(a, " ", "")
	if i := strings.LastIndexAny(a, ".,"); i >= 0 && len(a)-i-1 <= 2 {
		a = strings.NewReplacer(",", "", ".", "").Replace(a[:i]) + "." + a[i+1:]
	} else {
		a = strings.NewReplacer(",", "", ".", "").Replace(a)
	}
	v, err := strconv.ParseFloat(a, 64)
	return v, err == nil && !math.IsNaN(v)
}

func periodOf(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "mo", "month", "monthly":
		return "month"
	case "yr", "year", "yearly", "annually", "annual":
		return "year"
	case "req", "request", "1k requests", "1,000 requests":
		return "request"
	}
	if strings.Contains(s, "month") {
		return "month"
	}
	if strings.Contains(s, "year") || strings.Contains(s, "annual") {
		return "year"
	}
	return s
}

// Diff says what changed from one read's plans to the next, matching plans
// by name (case aside).
func Diff(before, after []Plan) []Change {
	old := map[string]Plan{}
	for _, p := range before {
		old[strings.ToLower(p.Name)] = p
	}
	out := []Change{}
	seen := map[string]bool{}
	for _, p := range after {
		k := strings.ToLower(p.Name)
		seen[k] = true
		b, ok := old[k]
		a := p
		switch {
		case !ok:
			out = append(out, Change{Kind: "added", Plan: p.Name, After: &a})
		case b.Currency == p.Currency && b.Period == p.Period && p.Price > b.Price:
			out = append(out, Change{Kind: "price_up", Plan: p.Name, Before: &b, After: &a})
		case b.Currency == p.Currency && b.Period == p.Period && p.Price < b.Price:
			out = append(out, Change{Kind: "price_down", Plan: p.Name, Before: &b, After: &a})
		case b.Currency != p.Currency || b.Period != p.Period || b.Price != p.Price:
			out = append(out, Change{Kind: "changed", Plan: p.Name, Before: &b, After: &a})
		}
	}
	for _, p := range before {
		if !seen[strings.ToLower(p.Name)] {
			b := p
			out = append(out, Change{Kind: "removed", Plan: p.Name, Before: &b})
		}
	}
	return out
}

// Same says two reads found the same plans, so the second needs no new
// snapshot.
func Same(a, b []Plan) bool {
	return len(Diff(a, b)) == 0 && len(a) == len(b)
}
