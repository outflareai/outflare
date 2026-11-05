// Package flags decides which variant of a feature flag a visitor gets.
//
// A flag is on or off. When on, a visitor who passes every one of its
// rules, and falls inside its rollout, gets one of its variants by weight.
// Which one comes from a hash of the flag's salt and the visitor's id, so
// the same visitor gets the same variant on every page, every server and
// every process, with nothing stored per visitor. Changing the weights of a
// running flag moves only the visitors near the boundaries it shifts.
//
// Flags in the same exclusion group split the group's visitors between
// them: each owns a fixed slice of the group's buckets, from a hash of the
// group's name and the visitor's id, and is off for everyone outside its
// slice, so no visitor is in two of the group's tests.
package flags

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Limits on what a flag may hold.
const (
	MaxVariants    = 10
	MaxRules       = 20
	MaxRuleValues  = 50
	MaxPayload     = 16 << 10
	MaxDescription = 1000
	// Buckets a visitor's hash falls into: weights and rollout are in
	// hundredths of a percent of these. An exclusion group's slices are
	// whole percents of them.
	buckets = 10_000
	Buckets = buckets
)

var (
	keyRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	paramRe   = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,64}$`)
	Ops       = []string{"is", "is_not", "contains", "not_contains", "starts_with", "ends_with", "exists", "not_exists"}
	Attrs     = []string{"host", "path", "url", "referrer", "device", "language", "signed_in"}
	Devices   = []string{"desktop", "mobile", "tablet"}
	valueless = []string{"exists", "not_exists"}
)

// Variant is one arm of a flag.
type Variant struct {
	Key    string `json:"key"`
	Weight int    `json:"weight"`
	// Anything the page or server reads with the variant, such as a
	// headline's text; null for none.
	Payload json.RawMessage `json:"payload"`
}

// Rule is one condition on the visitor or the page.
type Rule struct {
	// host, path, url, referrer, device, language, signed_in,
	// query.<name> (a parameter of the page's address) or
	// property.<name> (a property the caller sends).
	Attribute string   `json:"attribute"`
	Op        string   `json:"op"`
	Values    []string `json:"values"`
}

// Flag is what evaluation needs of a flag.
type Flag struct {
	Key      string
	Status   string
	Variants []Variant
	Rollout  int
	Rules    []Rule
	Salt     string
	// Group is the flag's exclusion group ("" for none), and GroupFrom to
	// GroupTo the buckets of the group it owns.
	Group              string
	GroupFrom, GroupTo int
}

// Visitor is who is asking and from where.
type Visitor struct {
	// The id the variant follows: the browser's anonymousId, or a user id
	// when there is none.
	Subject  string
	SignedIn bool
	URL      string
	Referrer string
	Device   string
	Language string
	// Properties the caller sends, for property.<name> rules.
	Properties map[string]string
}

// Result is the variant a visitor got; Variant is "" when the flag is off
// for them.
type Result struct {
	Variant string
	Payload json.RawMessage
	// Why: off (the flag is off), rules (a rule failed), group (in
	// another flag's slice of its exclusion group), rollout (outside it),
	// no_subject (no id to hash), or match.
	Reason string
}

// Evaluate decides a flag for a visitor.
func Evaluate(f Flag, v Visitor) Result {
	if f.Status != "on" || len(f.Variants) == 0 {
		return Result{Reason: "off"}
	}
	if v.Subject == "" {
		return Result{Reason: "no_subject"}
	}
	attrs := attributes(v)
	for _, r := range f.Rules {
		if !r.holds(attrs) {
			return Result{Reason: "rules"}
		}
	}
	if f.Group != "" {
		if g := GroupBucket(f.Group, v.Subject); g < f.GroupFrom || g >= f.GroupTo {
			return Result{Reason: "group"}
		}
	}
	if bucket(f.Salt+":rollout:"+v.Subject) >= f.Rollout*100 {
		return Result{Reason: "rollout"}
	}
	b, total := bucket(f.Salt+":"+v.Subject), 0
	for _, x := range f.Variants {
		total += x.Weight
	}
	if total == 0 {
		return Result{Reason: "off"}
	}
	// Weights need not add to 100: each variant's share is its weight over
	// the total, laid out in order across the buckets.
	at := 0
	for i, x := range f.Variants {
		at += x.Weight
		if b*total < at*buckets || i == len(f.Variants)-1 {
			return Result{Variant: x.Key, Payload: x.Payload, Reason: "match"}
		}
	}
	return Result{Reason: "off"}
}

// GroupBucket is where a visitor falls in exclusion group g: the same for
// every flag in it, so the flags' slices never overlap for one visitor.
func GroupBucket(g, subject string) int { return bucket("group:" + g + ":" + subject) }

// Allocate finds the first free slice of share percent below limit (the
// group's buckets less its holdout) in a group whose flags hold taken
// (bucket ranges [from, to)), or ok false when no gap is that wide.
func Allocate(taken [][2]int, share, limit int) (from, to int, ok bool) {
	size := share * 100
	if share < 1 || share > 100 {
		return 0, 0, false
	}
	slices.SortFunc(taken, func(a, b [2]int) int { return a[0] - b[0] })
	at := 0
	for _, t := range taken {
		if t[0]-at >= size {
			return at, at + size, true
		}
		at = max(at, t[1])
	}
	if limit-at >= size {
		return at, at + size, true
	}
	return 0, 0, false
}

// GroupBucketSQL is GroupBucket in Postgres, for group (an SQL text
// expression) and subject: the first 8 bytes of the same sha256 as an
// unsigned number, mod Buckets. bigint is signed, so a negative value
// gets 2^64 mod 10,000 (1616) added back.
func GroupBucketSQL(group, subject string) string {
	x := `('x' || substr(encode(sha256(convert_to('group:' || ` + group + ` || ':' || ` + subject + `, 'UTF8')), 'hex'), 1, 16))::bit(64)::bigint`
	return `(CASE WHEN ` + x + ` >= 0 THEN ` + x + ` % 10000 ELSE ((` + x + ` % 10000) + 10000 + 1616) % 10000 END)`
}

// bucket maps s to 0..buckets-1, the same everywhere.
func bucket(s string) int {
	h := sha256.Sum256([]byte(s))
	return int(binary.BigEndian.Uint64(h[:8]) % buckets)
}

type attrs struct {
	plain map[string]string
	query url.Values
	props map[string]string
	has   map[string]bool
}

func attributes(v Visitor) attrs {
	a := attrs{plain: map[string]string{}, query: url.Values{}, props: v.Properties, has: map[string]bool{}}
	set := func(k, val string) {
		a.plain[k] = val
		a.has[k] = val != ""
	}
	if u, err := url.Parse(v.URL); err == nil && v.URL != "" {
		set("host", strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."))
		set("path", u.EscapedPath())
		a.query = u.Query()
	}
	set("url", v.URL)
	set("referrer", v.Referrer)
	set("device", strings.ToLower(v.Device))
	set("language", strings.ToLower(v.Language))
	set("signed_in", fmt.Sprint(v.SignedIn))
	a.has["signed_in"] = true
	return a
}

func (r Rule) holds(a attrs) bool {
	val, present := "", false
	switch {
	case strings.HasPrefix(r.Attribute, "query."):
		name := strings.TrimPrefix(r.Attribute, "query.")
		val, present = a.query.Get(name), a.query.Has(name)
	case strings.HasPrefix(r.Attribute, "property."):
		val, present = a.props[strings.TrimPrefix(r.Attribute, "property.")]
	default:
		val, present = a.plain[r.Attribute], a.has[r.Attribute]
	}
	switch r.Op {
	case "exists":
		return present
	case "not_exists":
		return !present
	}
	val = strings.ToLower(val)
	anyOf := func(f func(v, x string) bool) bool {
		return slices.ContainsFunc(r.Values, func(x string) bool { return f(val, strings.ToLower(x)) })
	}
	switch r.Op {
	case "is":
		return present && anyOf(func(v, x string) bool { return v == x })
	case "is_not":
		return !anyOf(func(v, x string) bool { return v == x })
	case "contains":
		return present && anyOf(strings.Contains)
	case "not_contains":
		return !anyOf(strings.Contains)
	case "starts_with":
		return present && anyOf(strings.HasPrefix)
	case "ends_with":
		return present && anyOf(strings.HasSuffix)
	}
	return false
}

// ValidKey reports whether k can name a flag: lowercase letters, digits,
// - and _, up to 64.
func ValidKey(k string) bool { return keyRe.MatchString(k) }

// CheckVariants says what is wrong with a flag's variants, or "".
func CheckVariants(vs []Variant) string {
	if len(vs) == 0 || len(vs) > MaxVariants {
		return fmt.Sprintf("give 1 to %d variants", MaxVariants)
	}
	seen, total := map[string]bool{}, 0
	for _, v := range vs {
		if !ValidKey(v.Key) {
			return "each variant needs a key of lowercase letters, digits, - and _, such as control or new-headline"
		}
		if seen[v.Key] {
			return "variant " + v.Key + " is listed twice"
		}
		seen[v.Key] = true
		if v.Weight < 0 || v.Weight > 100 {
			return "a variant's weight is 0 to 100"
		}
		total += v.Weight
		if len(v.Payload) > MaxPayload {
			return fmt.Sprintf("a variant's payload is at most %d KB of JSON", MaxPayload>>10)
		}
		if len(v.Payload) > 0 && !json.Valid(v.Payload) {
			return "a variant's payload must be JSON"
		}
		if msg := CheckChanges(v.Payload); msg != "" {
			return "variant " + v.Key + ": " + msg
		}
	}
	if total == 0 {
		return "give at least one variant a weight above 0"
	}
	return ""
}

// Change is one edit the website script makes to the page for a version,
// from the visual editor or written by hand in its payload's "changes".
// The elements matching Selector get exactly one of: Text as their text;
// Rich, a little formatting built as elements (never parsed as HTML);
// hidden; Href, Src or Alt; Style, a few safe CSS properties; moved
// before, after or into the start or end of Target; or Insert, a new text,
// button, link or image next to the first of them. Device limits it to
// phones (mobile, up to 767px wide) or larger screens (desktop).
type Change struct {
	Selector string            `json:"selector"`
	Text     *string           `json:"text,omitempty"`
	Rich     *string           `json:"rich,omitempty"`
	Hide     bool              `json:"hide,omitempty"`
	Href     *string           `json:"href,omitempty"`
	Src      *string           `json:"src,omitempty"`
	Alt      *string           `json:"alt,omitempty"`
	Style    map[string]string `json:"style,omitempty"`
	Move     string            `json:"move,omitempty"`
	Target   string            `json:"target,omitempty"`
	Insert   *Insert           `json:"insert,omitempty"`
	Device   string            `json:"device,omitempty"`
}

// Insert is a new element a change adds: Where is before or after the
// selected element; Kind is text, button, link or image.
type Insert struct {
	Where string `json:"where"`
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	Href  string `json:"href,omitempty"`
	Src   string `json:"src,omitempty"`
	Alt   string `json:"alt,omitempty"`
}

// MaxChanges is how many edits one version may make.
const MaxChanges = 50

// StyleProps are the CSS properties a change may set: looks and layout,
// nothing that loads anything.
var StyleProps = []string{
	"color", "background-color", "font-size", "font-weight", "font-style", "font-family", "text-align", "text-decoration", "text-transform",
	"line-height", "letter-spacing", "padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left", "border", "border-color", "border-width", "border-style",
	"border-radius", "width", "max-width", "min-width", "height", "max-height", "min-height", "display", "opacity", "gap", "order",
	"justify-content", "align-items", "flex-direction", "box-shadow", "visibility",
}

var styleValue = regexp.MustCompile(`^[#(),.%\-+\w\s/"']{1,100}$`)

// SafeStyleValue is a CSS value with nothing that loads, runs or breaks
// out of its declaration.
func SafeStyleValue(v string) bool {
	l := strings.ToLower(v)
	return styleValue.MatchString(v) && !strings.Contains(l, "url") && !strings.Contains(l, "expression") && !strings.Contains(l, "image")
}

// MaxCSS is how long a version's own CSS may be.
const MaxCSS = 8000

// CheckCSS says what is wrong with a version's own CSS ("css" in its
// payload), or "". It may style the page but never load anything (no
// url(), @import or fonts from elsewhere) or end its <style>.
func CheckCSS(css string) string {
	if len(css) > MaxCSS {
		return fmt.Sprintf("a version's CSS is at most %d characters", MaxCSS)
	}
	l := strings.ToLower(css)
	for _, bad := range []string{"<", "\\", "url(", "@import", "@font-face", "@charset", "@namespace", "expression(", "javascript:", "behavior", "-moz-binding", "image-set(", "image("} {
		if strings.Contains(l, bad) {
			return "a version's CSS can style the page but can't load anything or use " + strings.TrimSuffix(bad, "(")
		}
	}
	return ""
}

// Changes reads a payload's "changes", or nil when it has none.
func Changes(payload json.RawMessage) ([]Change, bool) {
	var p struct {
		Changes *json.RawMessage `json:"changes"`
	}
	if len(payload) == 0 || payload[0] != '{' || json.Unmarshal(payload, &p) != nil || p.Changes == nil {
		return nil, false
	}
	var cs []Change
	if json.Unmarshal(*p.Changes, &cs) != nil {
		return nil, true
	}
	return cs, true
}

// Visual says whether a payload changes the page by itself: changes, its
// own CSS or a redirect, so the website script runs it with no code.
func Visual(payload json.RawMessage) bool {
	if cs, _ := Changes(payload); len(cs) > 0 {
		return true
	}
	var p struct {
		CSS      string `json:"css"`
		Redirect string `json:"redirect"`
	}
	if len(payload) == 0 || payload[0] != '{' || json.Unmarshal(payload, &p) != nil {
		return false
	}
	return p.CSS != "" || p.Redirect != ""
}

func badSelector(sel string) bool {
	return sel == "" || len(sel) > 300 || strings.ContainsAny(sel, "<{}@;") || strings.Contains(sel, "/*")
}

// CheckChanges says what is wrong with a payload's "changes", "css" and
// "redirect", or "".
func CheckChanges(payload json.RawMessage) string {
	if len(payload) > 0 && payload[0] == '{' {
		var p struct {
			CSS      *json.RawMessage `json:"css"`
			Redirect *json.RawMessage `json:"redirect"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.CSS != nil {
			var css string
			if json.Unmarshal(*p.CSS, &css) != nil {
				return "css is the version's own CSS, as a string"
			}
			if msg := CheckCSS(css); msg != "" {
				return msg
			}
		}
		if p.Redirect != nil {
			var to string
			if json.Unmarshal(*p.Redirect, &to) != nil || (to != "" && !SafeURL(to)) {
				return "redirect is the page to send the version's visitors to: a path on the site, such as /pricing-b, or an http(s) address on the site's own domains"
			}
		}
	}
	cs, has := Changes(payload)
	if !has {
		return ""
	}
	if cs == nil && string(payload) != "" {
		var raw struct {
			Changes json.RawMessage `json:"changes"`
		}
		_ = json.Unmarshal(payload, &raw)
		if string(raw.Changes) != "null" && string(raw.Changes) != "[]" {
			return "changes is a list of {selector, and text, rich, hide, href, src or alt, style, move or insert}"
		}
	}
	if len(cs) > MaxChanges {
		return fmt.Sprintf("at most %d changes", MaxChanges)
	}
	for _, c := range cs {
		if badSelector(c.Selector) {
			return "each change needs a CSS selector of up to 300 characters, such as h1.hero"
		}
		n := 0
		for _, set := range []bool{c.Text != nil, c.Rich != nil, c.Hide, c.Href != nil, c.Src != nil || c.Alt != nil, c.Style != nil, c.Move != "", c.Insert != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			return "each change does one thing: text, rich, hide, href, src and alt, style, move or insert"
		}
		if c.Text != nil && len(*c.Text) > 2000 || c.Rich != nil && len(*c.Rich) > 2000 {
			return "a change's text is at most 2,000 characters"
		}
		for _, u := range []*string{c.Href, c.Src} {
			if u != nil && !SafeURL(*u) {
				return "a link or image is an http(s) address or a path on the site, such as /pricing"
			}
		}
		if c.Alt != nil && len(*c.Alt) > 500 {
			return "alt text is at most 500 characters"
		}
		if c.Style != nil {
			if len(c.Style) == 0 || len(c.Style) > 20 {
				return "a style change sets 1 to 20 properties"
			}
			for k, v := range c.Style {
				if !slices.Contains(StyleProps, k) {
					return "style can set " + strings.Join(StyleProps, ", ") + "; not " + k
				}
				if !SafeStyleValue(v) {
					return "the style value for " + k + " can't load anything: use colours, sizes and words, such as #0f766e or 18px"
				}
			}
		}
		if (c.Move != "") != (c.Target != "") {
			return "a move needs both move (before, after, start or end) and target, the selector of where it goes"
		}
		if c.Move != "" {
			if !slices.Contains([]string{"before", "after", "start", "end"}, c.Move) {
				return "move is before, after, start or end of the target"
			}
			if badSelector(c.Target) {
				return "a move's target is a CSS selector of up to 300 characters"
			}
		}
		if in := c.Insert; in != nil {
			if in.Where != "before" && in.Where != "after" {
				return "insert.where is before or after the selected element"
			}
			switch in.Kind {
			case "text", "button", "link":
				if in.Text == "" || len(in.Text) > 500 {
					return "an inserted text, button or link needs text of up to 500 characters"
				}
				if in.Kind != "text" && !SafeURL(in.Href) {
					return "an inserted button or link needs href: an http(s) address or a path on the site"
				}
			case "image":
				if !SafeURL(in.Src) || len(in.Alt) > 500 {
					return "an inserted image needs src (an http(s) address or a path on the site) and alt text of up to 500 characters"
				}
			default:
				return "insert.kind is text, button, link or image"
			}
		}
		if c.Device != "" && c.Device != "mobile" && c.Device != "desktop" {
			return "device is mobile or desktop, or left out for both"
		}
	}
	return ""
}

// SafeURL is an address the website script may put in a link or image:
// http(s), or a path on the same site (not //host).
func SafeURL(u string) bool {
	if len(u) > 2000 || strings.ContainsAny(u, " \t\n\r") {
		return false
	}
	if strings.HasPrefix(u, "/") {
		return !strings.HasPrefix(u, "//")
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}

// CheckRules says what is wrong with a flag's rules, or "".
func CheckRules(rs []Rule) string {
	if len(rs) > MaxRules {
		return fmt.Sprintf("at most %d rules", MaxRules)
	}
	for _, r := range rs {
		name, scoped := "", false
		for _, p := range []string{"query.", "property."} {
			if n, ok := strings.CutPrefix(r.Attribute, p); ok {
				name, scoped = n, true
			}
		}
		if scoped && !paramRe.MatchString(name) {
			return "query.<name> and property.<name> need a name of letters, digits, _ . - [ ], such as query.utm_source"
		}
		if !scoped && !slices.Contains(Attrs, r.Attribute) {
			return "a rule's attribute is one of " + strings.Join(Attrs, ", ") + ", query.<name> or property.<name>"
		}
		if !slices.Contains(Ops, r.Op) {
			return "a rule's op is one of " + strings.Join(Ops, ", ")
		}
		if slices.Contains(valueless, r.Op) != (len(r.Values) == 0) {
			if len(r.Values) == 0 {
				return "rule " + r.Attribute + " " + r.Op + " needs values"
			}
			return "exists and not_exists take no values"
		}
		if len(r.Values) > MaxRuleValues {
			return fmt.Sprintf("a rule takes at most %d values", MaxRuleValues)
		}
		for _, v := range r.Values {
			if len(v) > 500 {
				return "a rule's value is at most 500 characters"
			}
		}
		if r.Attribute == "device" {
			for _, v := range r.Values {
				if !slices.Contains(Devices, strings.ToLower(v)) {
					return "device is desktop, mobile or tablet"
				}
			}
		}
	}
	return ""
}
