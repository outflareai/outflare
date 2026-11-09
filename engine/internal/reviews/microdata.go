package reviews

import (
	"slices"
	"strings"

	xhtml "golang.org/x/net/html"
)

// microdata reads schema.org microdata (itemscope, itemtype, itemprop):
// an AggregateRating with its score and count, and Review items with
// their stars, reviewer, date, title and text. Only these properties are
// taken; everything else on the page is ignored.
func microdata(page string) Read {
	var r Read
	doc, err := xhtml.Parse(strings.NewReader(page))
	if err != nil {
		return r
	}
	seen := map[string]bool{}
	var walk func(n *xhtml.Node, depth int)
	walk = func(n *xhtml.Node, depth int) {
		if depth > 200 {
			return
		}
		if n.Type == xhtml.ElementNode && hasAttr(n, "itemscope") {
			switch {
			case itemTypeIs(n, "Review"):
				if x, ok := mdReview(n); ok && len(r.Reviews) < 200 {
					key := x.ExternalID
					if key == "" {
						key = fingerprint(x)
					}
					if !seen[key] {
						seen[key] = true
						r.Reviews = append(r.Reviews, x)
					}
				}
				return
			case itemTypeIs(n, "AggregateRating") && r.Score == nil:
				props := mdProps(n)
				best := num(props["bestRating"])
				if s := num(props["ratingValue"]); s != nil {
					v := toFive(*s, best)
					r.Score = &v
				}
				for _, k := range []string{"reviewCount", "ratingCount"} {
					if c := num(props[k]); c != nil && r.Count == nil {
						n := int64(*c)
						r.Count = &n
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	walk(doc, 0)
	return r
}

// mdReview reads one Review item.
func mdReview(n *xhtml.Node) (Review, bool) {
	var x Review
	props := mdProps(n)
	var s, best *float64
	if rating := mdChild(n, "reviewRating"); rating != nil {
		rp := mdProps(rating)
		s, best = num(rp["ratingValue"]), num(rp["bestRating"])
	}
	if s == nil || *s < 1 {
		return x, false
	}
	x.RatingNative, x.RatingScale = *s, 5
	if best != nil && *best > 0 {
		x.RatingScale = *best
	}
	x.Rating = toFive(*s, best)
	x.Title = text(firstOf(props, "name", "headline"))
	x.Body = text(firstOf(props, "reviewBody", "description"))
	// SourceForge marks up only the stars, reviewer and date; its title
	// and text are in the review's own elements.
	if x.Title == "" {
		if t := byClass(n, "review-title"); t != nil {
			x.Title = nodeText(t)
		}
	}
	if x.Body == "" {
		if b := byClass(n, "ext-review-content"); b != nil {
			x.Body = bodyText(b)
		}
	}
	if a := mdChild(n, "author"); a != nil {
		x.Author = text(mdProps(a)["name"])
		if x.Author == "" {
			x.Author = text(nodeText(a))
		}
	}
	x.PostedAt = date(text(props["datePublished"]))
	if id := attr(n, "id"); id != "" {
		x.ExternalID = "md:" + id
	} else if u := text(props["url"]); strings.HasPrefix(u, "http") {
		x.ExternalID, x.URL = u, u
	}
	return x, true
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if t := text(m[k]); t != "" {
			return t
		}
	}
	return ""
}

// mdProps is an item's own properties as text, without those of items
// nested in it.
func mdProps(item *xhtml.Node) map[string]any {
	out := map[string]any{}
	var walk func(n *xhtml.Node, depth int)
	walk = func(n *xhtml.Node, depth int) {
		if depth > 100 {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != xhtml.ElementNode {
				continue
			}
			if p := attr(c, "itemprop"); p != "" {
				for _, name := range strings.Fields(p) {
					if _, ok := out[name]; !ok && !hasAttr(c, "itemscope") {
						out[name] = mdValue(c)
					}
				}
			}
			if !hasAttr(c, "itemscope") {
				walk(c, depth+1)
			}
		}
	}
	walk(item, 0)
	return out
}

// mdChild is the item nested in an item under a property, such as a
// review's reviewRating or author.
func mdChild(item *xhtml.Node, prop string) *xhtml.Node {
	var found *xhtml.Node
	var walk func(n *xhtml.Node, depth int)
	walk = func(n *xhtml.Node, depth int) {
		if found != nil || depth > 100 {
			return
		}
		for c := n.FirstChild; c != nil && found == nil; c = c.NextSibling {
			if c.Type != xhtml.ElementNode {
				continue
			}
			if hasAttr(c, "itemscope") {
				for _, name := range strings.Fields(attr(c, "itemprop")) {
					if name == prop {
						found = c
						return
					}
				}
				continue
			}
			walk(c, depth+1)
		}
	}
	walk(item, 0)
	return found
}

// mdValue is a property's value: its content, datetime or value
// attribute when it has one, else its text.
func mdValue(n *xhtml.Node) string {
	for _, k := range []string{"content", "datetime", "value"} {
		if v, ok := attrOK(n, k); ok {
			return v
		}
	}
	if n.Data == "a" || n.Data == "link" {
		if v, ok := attrOK(n, "href"); ok {
			return v
		}
	}
	return nodeText(n)
}

func nodeText(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == xhtml.ElementNode && (n.Data == "br" || n.Data == "p") {
			b.WriteString("\n")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// byClass is the first element inside n with a class.
func byClass(n *xhtml.Node, class string) *xhtml.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && slices.Contains(strings.Fields(attr(c, "class")), class) {
			return c
		}
		if f := byClass(c, class); f != nil {
			return f
		}
	}
	return nil
}

// bodyText is a review's text, a paragraph a line, without its links
// ("Read More").
func bodyText(n *xhtml.Node) string {
	var parts []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != xhtml.ElementNode || c.Data == "a" || c.Data == "script" || c.Data == "style" {
			continue
		}
		if t := strings.Join(strings.Fields(nodeText(c)), " "); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func itemTypeIs(n *xhtml.Node, want string) bool {
	for _, t := range strings.Fields(attr(n, "itemtype")) {
		if strings.TrimSuffix(t[strings.LastIndex(t, "/")+1:], "/") == want {
			return true
		}
	}
	return false
}

func attr(n *xhtml.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

func attrOK(n *xhtml.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func hasAttr(n *xhtml.Node, key string) bool {
	_, ok := attrOK(n, key)
	return ok
}
