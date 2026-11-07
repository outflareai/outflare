package api

// AI that writes a no-code test's versions as changes to its page: it reads
// the page (as the site serves it, or as the visual editor saw it in the
// browser), numbers its headings, text, links, buttons and images, and asks
// the workspace's own AI model for changes to those numbered parts. The
// engine, not the AI, turns each number back into the part's selector and
// checks every change like one made by hand, so the AI can't aim at
// something that isn't on the page or add anything the visual editor
// couldn't.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/flags"
)

const (
	// maxOutline is how many parts of a page the AI sees.
	maxOutline = 200
	// maxOutlineText is how much of each part's text it sees.
	maxOutlineText = 200
	// maxAIChanges is how many changes one AI version may make.
	maxAIChanges = 20
)

// pageElement is one part of the page the AI may change: id is how the AI
// names it (e1, e2, …), selector how the site script finds it.
type pageElement struct {
	ID       string `json:"id"`
	Tag      string `json:"tag"`
	Selector string `json:"selector"`
	Text     string `json:"text"`
	Href     string `json:"href,omitempty"`
}

// outlineTags are the parts worth changing: text people read and things
// they click.
var outlineTags = map[atom.Atom]bool{
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.P: true, atom.A: true, atom.Button: true, atom.Img: true, atom.Li: true, atom.Blockquote: true,
	atom.Figcaption: true, atom.Label: true, atom.Section: true, atom.Header: true, atom.Footer: true, atom.Form: true,
}

var skipTags = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Head: true, atom.Iframe: true}

var cssIdent = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,80}$`)

// pageOutline numbers the parts of a page's HTML the AI may change, each
// with a selector that finds exactly it: from the nearest ancestor with a
// unique id, then each step's tag and position among its siblings.
func pageOutline(body []byte) []pageElement {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	ids := map[string]int{}
	var count func(n *html.Node)
	count = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id := attr(n, "id"); id != "" {
				ids[id]++
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			count(c)
		}
	}
	count(doc)
	var out []pageElement
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if len(out) >= maxOutline || n.Type == html.ElementNode && skipTags[n.DataAtom] {
			return
		}
		if n.Type == html.ElementNode && outlineTags[n.DataAtom] {
			if el, ok := outlineElement(n, ids); ok {
				el.ID = fmt.Sprintf("e%d", len(out)+1)
				out = append(out, el)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func outlineElement(n *html.Node, ids map[string]int) (pageElement, bool) {
	el := pageElement{Tag: n.Data}
	switch n.DataAtom {
	case atom.Img:
		el.Text = strings.TrimSpace(attr(n, "alt"))
		if el.Text == "" {
			el.Text = "(image)"
		}
	case atom.Section, atom.Header, atom.Footer, atom.Form:
		// Blocks are places to add or move things: named by their first
		// heading, if any.
		el.Text = "(" + n.Data + ")"
		if h := firstHeading(n); h != "" {
			el.Text = "(" + n.Data + ": " + h + ")"
		}
	default:
		el.Text = squashText(n)
		if el.Text == "" {
			return el, false
		}
		// A list item or paragraph that only wraps a link is the link.
		if n.DataAtom == atom.Li && onlyChild(n, atom.A) {
			return el, false
		}
	}
	if n.DataAtom == atom.A {
		el.Href = attr(n, "href")
	}
	el.Text = clip(el.Text, maxOutlineText)
	el.Selector = selectorFor(n, ids)
	if el.Selector == "" || len(el.Selector) > 300 {
		return el, false
	}
	return el, true
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func squashText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		if c.Type == html.ElementNode && skipTags[c.DataAtom] {
			return
		}
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
			b.WriteByte(' ')
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			walk(k)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func firstHeading(n *html.Node) string {
	var found string
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		if found != "" {
			return
		}
		if c.Type == html.ElementNode && (c.DataAtom == atom.H1 || c.DataAtom == atom.H2 || c.DataAtom == atom.H3) {
			found = clip(squashText(c), 80)
			return
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			walk(k)
		}
	}
	walk(n)
	return found
}

func onlyChild(n *html.Node, a atom.Atom) bool {
	var el *html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.ElementNode:
			if el != nil {
				return false
			}
			el = c
		case c.Type == html.TextNode && strings.TrimSpace(c.Data) != "":
			return false
		}
	}
	return el != nil && el.DataAtom == a
}

// selectorFor is a CSS selector that finds n again when the page loads
// in a browser, written the way the visual editor writes them: the
// nearest stable, unique id, else a path of tag, up to two stable classes,
// and the position among same-tag siblings only where it matters. A page
// built in the browser adds and moves wrappers, so a path that counts
// every step from <body> breaks where this one holds.
func selectorFor(n *html.Node, ids map[string]int) string {
	var parts []string
	for c := n; c != nil && c.Type == html.ElementNode && len(parts) < 6; c = c.Parent {
		if id := attr(c, "id"); id != "" && ids[id] == 1 && stableName(id) {
			parts = append(parts, "#"+id)
			break
		}
		if c.DataAtom == atom.Html {
			break
		}
		if c.DataAtom == atom.Body {
			parts = append(parts, "body")
			break
		}
		part := c.Data
		var classes []string
		for _, cl := range strings.Fields(attr(c, "class")) {
			if stableName(cl) && cl != "of-button" {
				classes = append(classes, cl)
				if len(classes) == 2 {
					break
				}
			}
		}
		if len(classes) > 0 {
			part += "." + strings.Join(classes, ".")
		}
		nth, same := 1, 0
		if c.Parent != nil {
			for s := c.Parent.FirstChild; s != nil; s = s.NextSibling {
				if s.Type == html.ElementNode && s.Data == c.Data {
					same++
					if s == c {
						nth = same
					}
				}
			}
		}
		if same > 1 {
			part += fmt.Sprintf(":nth-of-type(%d)", nth)
		}
		parts = append(parts, part)
	}
	slices.Reverse(parts)
	return strings.Join(parts, " > ")
}

var digitRun = regexp.MustCompile(`[0-9]{3,}`)

// stableName is an id or class worth putting in a selector: a plain name
// under 40 characters with no long run of digits (a build hash).
func stableName(s string) bool {
	return len(s) < 40 && cssIdent.MatchString(s) && !digitRun.MatchString(s)
}

// cleanOutline checks an outline sent by the visual editor: every part
// has a selector the site script would take, and ids are renumbered.
func cleanOutline(in []pageElement) []pageElement {
	out := []pageElement{}
	for _, el := range in {
		if len(out) >= maxOutline {
			break
		}
		el.Selector = strings.TrimSpace(el.Selector)
		el.Tag = strings.ToLower(strings.TrimSpace(el.Tag))
		if el.Selector == "" || len(el.Selector) > 300 || strings.ContainsAny(el.Selector, "<{}@;") || strings.Contains(el.Selector, "/*") || !cssIdent.MatchString(el.Tag) {
			continue
		}
		el.Text = clip(strings.Join(strings.Fields(el.Text), " "), maxOutlineText)
		el.Href = clip(strings.TrimSpace(el.Href), 300)
		el.ID = fmt.Sprintf("e%d", len(out)+1)
		out = append(out, el)
	}
	return out
}

// readTestPage fetches the test's page as the site serves it, following a
// few redirects, and numbers its parts. A page built in the browser (with
// little in its HTML) has few parts: the visual editor sends what it sees
// instead.
func (s *Server) readTestPage(ctx context.Context, ws, page string) ([]pageElement, error) {
	if s.crawler == nil {
		return nil, errInvalid("Outflare can't read web pages on this server. Open the version in the visual editor and ask the AI there.")
	}
	var test bool
	if err := s.pool.QueryRow(ctx, `SELECT test FROM workspaces WHERE id = $1`, ws).Scan(&test); err != nil {
		return nil, err
	}
	fetch, _ := s.crawler.ReadFetcher(test)
	cur := strings.TrimSuffix(page, "*")
	for range 4 {
		if err := crawl.CheckURL(cur, s.crawler.AllowPrivate); err != nil {
			return nil, errInvalid("The test's page can't be read: it must be a public website.")
		}
		resp, err := fetch.Fetch(ctx, cur)
		if err != nil {
			return nil, errInvalid("Couldn't open " + cur + ". Check the page loads, or open the version in the visual editor and ask the AI there.")
		}
		if resp.Status >= 300 && resp.Status < 400 {
			base, _ := url.Parse(cur)
			next, ok := crawl.Normalize(base, resp.Header.Get("Location"))
			if !ok {
				break
			}
			cur = next
			continue
		}
		if resp.Status != 200 {
			return nil, errInvalid(fmt.Sprintf("%s answered %d. Check the page loads, or open the version in the visual editor and ask the AI there.", cur, resp.Status))
		}
		outline := pageOutline(resp.Body)
		if len(outline) < 3 {
			return nil, errInvalid("The page has almost nothing in its HTML: it's probably built in the browser. Open the version in the visual editor and ask the AI there, where it sees the page as visitors do.")
		}
		return outline, nil
	}
	return nil, errInvalid("The test's page redirects too many times.")
}

const abPageVersionsSystem = `You change a company's own web page for an A/B test, for a person to review and keep.
You get the page's parts, each numbered (e1, e2, …) with its tag and text, and, when you add to a version, the parts that version already added (n1, n2, …), which you can change the same way. Write the number of versions asked: each one a set of changes to those numbered parts that follows the person's prompt, in the brand's voice, clearly different from the other versions. Each change names one part by its number and does one thing:
- text: replace the part's text (keep **bold**, *italic* and [links](/path) only where they help).
- hide: hide the part.
- link: send the part (a link or button) to another address, a path on the site such as /signup or a full https address.
- style: set one CSS property (style_property, style_value), such as color #0f766e or font-size 20px. Use one change per property.
- insert_text or insert_button: add a new line of text, or a button with text and href, before or after the part (where).
- move: move the part before or after another part (target, where).
Leave fields a change doesn't use as "". Change only what the prompt asks for and what it needs, keep the page's language, and keep text about as long as it was unless asked otherwise. Give each version a short key (lowercase letters, digits, - or _; not one already used), a one-line summary of what it changes, and one sentence on why it might win.
` + abAISafety

func abPageVersionsSchema() map[string]any {
	str := map[string]any{"type": "string"}
	change := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"element", "action", "text", "href", "where", "target", "style_property", "style_value"},
		"properties": map[string]any{
			"element": str, "action": map[string]any{"type": "string", "enum": []string{"text", "hide", "link", "style", "insert_text", "insert_button", "move"}},
			"text": str, "href": str, "where": map[string]any{"type": "string", "enum": []string{"", "before", "after"}}, "target": str,
			"style_property": str, "style_value": str,
		}}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"versions"},
		"properties": map[string]any{"versions": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"key", "summary", "rationale", "changes"},
			"properties": map[string]any{"key": str, "summary": str, "rationale": str,
				"changes": map[string]any{"type": "array", "items": change}}}}}}
}

type abAIPageChange struct {
	Element       string `json:"element"`
	Action        string `json:"action"`
	Text          string `json:"text"`
	Href          string `json:"href"`
	Where         string `json:"where"`
	Target        string `json:"target"`
	StyleProperty string `json:"style_property"`
	StyleValue    string `json:"style_value"`
}

// abAIPageVersion is a version the AI wrote, as changes ready for the
// flag's payload.
type abAIPageVersion struct {
	Key       string         `json:"key"`
	Summary   string         `json:"summary"`
	Rationale string         `json:"rationale"`
	Changes   []flags.Change `json:"changes"`
}

var richMarks = regexp.MustCompile(`\*\*[^*]+\*\*|\*[^*]+\*|\[[^\]]+\]\([^)\s]+\)|\n`)

// toChange turns one AI change into a checked change, or false when it
// names no part, does something not allowed or makes no sense.
func toChange(c abAIPageChange, parts map[string]pageElement) (flags.Change, bool) {
	el, ok := parts[strings.TrimSpace(c.Element)]
	if !ok {
		return flags.Change{}, false
	}
	out := flags.Change{Selector: el.Selector}
	text := strings.TrimSpace(c.Text)
	where := c.Where
	if where != "before" {
		where = "after"
	}
	switch c.Action {
	case "text":
		if text == "" || len(text) > 2000 {
			return out, false
		}
		if richMarks.MatchString(text) {
			out.Rich = &text
		} else {
			out.Text = &text
		}
	case "hide":
		out.Hide = true
	case "link":
		href := strings.TrimSpace(c.Href)
		if !flags.SafeURL(href) {
			return out, false
		}
		out.Href = &href
	case "style":
		prop, val := strings.ToLower(strings.TrimSpace(c.StyleProperty)), strings.TrimSpace(c.StyleValue)
		if !slices.Contains(flags.StyleProps, prop) || !flags.SafeStyleValue(val) {
			return out, false
		}
		out.Style = map[string]string{prop: val}
	case "insert_text":
		if text == "" || len(text) > 1000 {
			return out, false
		}
		out.Insert = &flags.Insert{Where: where, Kind: "text", Text: text}
	case "insert_button":
		href := strings.TrimSpace(c.Href)
		if text == "" || len(text) > 200 || !flags.SafeURL(href) {
			return out, false
		}
		out.Insert = &flags.Insert{Where: where, Kind: "button", Text: text, Href: href}
	case "move":
		t, ok := parts[strings.TrimSpace(c.Target)]
		if !ok || t.Selector == el.Selector {
			return out, false
		}
		out.Move, out.Target = where, t.Selector
	default:
		return out, false
	}
	return out, true
}

// abPageVersionsPrompt asks for versions of a no-code test as changes to
// its page, or for more changes to one version when in.Version is set.
func (s *Server) abPageVersionsPrompt(ctx context.Context, ws string, run abAIRun, in abVersionsInput) (string, func(string) (any, map[string]any, error), error) {
	if run.flagID == nil {
		return "", nil, errInvalid("The flag this run was for is gone.")
	}
	list, err := s.loadFlags(ctx, nil, ws, ` AND id = $2`, *run.flagID)
	if err != nil {
		return "", nil, err
	}
	if len(list) != 1 {
		return "", nil, errInvalid("The flag this run was for is gone.")
	}
	f := list[0]
	outline := cleanOutline(in.Outline)
	if len(outline) == 0 {
		if outline, err = s.readTestPage(ctx, ws, in.Page); err != nil {
			return "", nil, err
		}
	}
	parts := map[string]pageElement{}
	for _, el := range outline {
		parts[el.ID] = el
	}
	var b strings.Builder
	if in.Version != "" {
		fmt.Fprintf(&b, "Write 1 version with the key %q: the changes to make to version %s, on top of the changes it already has.\n\nData:\n\n", in.Version, in.Version)
	} else {
		fmt.Fprintf(&b, "Write %d new versions of the page.\n\nData:\n\n", in.Count)
	}
	brand, err := s.brandSection(ctx, ws, f.BrandID)
	if err != nil {
		return "", nil, err
	}
	b.WriteString(brand)
	var hypothesis string
	if run.testID != nil {
		_ = s.pool.QueryRow(ctx, `SELECT hypothesis FROM ab_tests WHERE workspace_id = $1 AND id = $2`, ws, *run.testID).Scan(&hypothesis)
	}
	fmt.Fprintf(&b, "## The test\nPage: %s\n", clip(in.Page, 500))
	if hypothesis != "" {
		fmt.Fprintf(&b, "Hypothesis: %s\n", clip(hypothesis, 1000))
	}
	b.WriteString("\n## The page's parts (number <tag> text)\n")
	bySelector := map[string]string{}
	for _, el := range outline {
		bySelector[el.Selector] = el.ID
		line := fmt.Sprintf("%s <%s> %s", el.ID, el.Tag, el.Text)
		if el.Href != "" {
			line += " → " + clip(el.Href, 120)
		}
		b.WriteString(line + "\n")
	}
	// What the version added so far (a button, a line of text) is on its
	// page too, so the AI can change it like any other part.
	if in.Version != "" {
		added := insertedParts(f.Key, in.Version, f.Variants, bySelector)
		if len(added) > 0 {
			b.WriteString("\n## Parts this version added (number <tag> text)\n")
			for _, el := range added {
				parts[el.ID] = el
				bySelector[el.Selector] = el.ID
				line := fmt.Sprintf("%s <%s> %s", el.ID, el.Tag, el.Text)
				if el.Href != "" {
					line += " → " + clip(el.Href, 120)
				}
				b.WriteString(line + "\n")
			}
		}
	}
	used := map[string]bool{}
	b.WriteString("\n## Versions so far\n")
	for _, v := range f.Variants {
		used[v.Key] = true
		cs, _ := flags.Changes(v.Payload)
		if len(cs) == 0 {
			fmt.Fprintf(&b, "%s: no changes (today's page)\n", v.Key)
			continue
		}
		fmt.Fprintf(&b, "%s:\n", v.Key)
		for _, c := range cs {
			fmt.Fprintf(&b, "- %s\n", describeChange(c, bySelector))
		}
	}
	if id, ok := bySelector[in.Focus]; ok && in.Focus != "" {
		fmt.Fprintf(&b, "\n## The part the person picked\n%s. Change that part (or add things right next to it, if asked), and leave the rest of the page as it is.\n", id)
	}
	fmt.Fprintf(&b, "\n## What the person asked for\n%s\n", in.Prompt)
	count := in.Count
	if in.Version != "" {
		count = 1
	}
	finish := func(text string) (any, map[string]any, error) {
		var out struct {
			Versions []struct {
				Key       string           `json:"key"`
				Summary   string           `json:"summary"`
				Rationale string           `json:"rationale"`
				Changes   []abAIPageChange `json:"changes"`
			} `json:"versions"`
		}
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			return nil, nil, err
		}
		kept := []abAIPageVersion{}
		dropped := 0
		for i, v := range out.Versions {
			key := strings.ToLower(strings.TrimSpace(v.Key))
			if in.Version != "" {
				key = in.Version
			} else if !flags.ValidKey(key) || used[key] {
				key = fmt.Sprintf("ai-%d", i+1)
				for n := i + 1; used[key]; n++ {
					key = fmt.Sprintf("ai-%d", n+1)
				}
			}
			var cs []flags.Change
			for _, c := range v.Changes {
				if len(cs) == maxAIChanges {
					break
				}
				if ch, ok := toChange(c, parts); ok {
					cs = append(cs, ch)
				} else {
					dropped++
				}
			}
			if len(cs) == 0 {
				continue
			}
			payload, _ := json.Marshal(map[string]any{"changes": cs})
			if flags.CheckChanges(payload) != "" {
				continue
			}
			used[key] = true
			kept = append(kept, abAIPageVersion{Key: key, Summary: clip(strings.TrimSpace(v.Summary), 300), Rationale: clip(strings.TrimSpace(v.Rationale), 500), Changes: cs})
			if len(kept) == count {
				break
			}
		}
		if len(kept) == 0 {
			if dropped > 0 {
				return nil, nil, errAIUnusable("wrote changes that don't fit this page's parts. Say which part to change (the heading, the first button), or use Edit on your page.")
			}
			return nil, nil, errors.New("no usable page versions")
		}
		return map[string]any{"flag_key": f.Key, "mode": "page", "page": in.Page, "version": in.Version, "versions": kept},
			map[string]any{"versions": len(kept), "mode": "page", "dropped_changes": dropped, "outline": len(outline), "from_editor": in.Editor || len(in.Outline) > 0, "focused": in.Focus != ""}, nil
	}
	return b.String(), finish, nil
}

// errAIUnusable says why the model's answer could not be used, for the
// person: the provider's name goes in front.
type errAIUnusable string

func (e errAIUnusable) Error() string { return string(e) }

// insertedParts lists what a version's changes add to its page (buttons,
// links, text, images), as parts the AI can change: each one is found by
// the mark the script puts on it (data-of-insert="<flag>:<change>").
// bySelector maps the page's own parts, so the line says where it sits.
func insertedParts(flagKey, version string, variants []flags.Variant, bySelector map[string]string) []pageElement {
	var out []pageElement
	for _, v := range variants {
		if v.Key != version {
			continue
		}
		cs, _ := flags.Changes(v.Payload)
		for i, c := range cs {
			if c.Insert == nil {
				continue
			}
			tag, text := "p", c.Insert.Text
			switch c.Insert.Kind {
			case "button", "link":
				tag = "a"
			case "image":
				tag, text = "img", c.Insert.Alt
			}
			at := bySelector[c.Selector]
			if at == "" {
				at = clip(c.Selector, 80)
			}
			out = append(out, pageElement{ID: fmt.Sprintf("n%d", len(out)+1), Tag: tag, Selector: fmt.Sprintf("[data-of-insert='%s:%d']", flagKey, i),
				Text: clip(text, 200) + " (added " + c.Insert.Where + " " + at + ")", Href: c.Insert.Href})
		}
	}
	return out
}

// describeChange says what a change does, in a line, naming the part by
// its number when it is in the outline.
func describeChange(c flags.Change, bySelector map[string]string) string {
	name := bySelector[c.Selector]
	if name == "" {
		name = clip(c.Selector, 80)
	}
	switch {
	case c.Text != nil:
		return fmt.Sprintf("%s text: %s", name, clip(*c.Text, 200))
	case c.Rich != nil:
		return fmt.Sprintf("%s text: %s", name, clip(*c.Rich, 200))
	case c.Hide:
		return name + " hidden"
	case c.Href != nil:
		return fmt.Sprintf("%s links to %s", name, clip(*c.Href, 120))
	case len(c.Style) > 0:
		var ps []string
		for k, v := range c.Style {
			ps = append(ps, k+": "+v)
		}
		slices.Sort(ps)
		return fmt.Sprintf("%s style %s", name, strings.Join(ps, "; "))
	case c.Insert != nil:
		return fmt.Sprintf("new %s %s %s: %s", c.Insert.Kind, c.Insert.Where, name, clip(c.Insert.Text, 200))
	case c.Move != "":
		return fmt.Sprintf("%s moved %s %s", name, c.Move, bySelector[c.Target])
	}
	return name + " changed"
}
