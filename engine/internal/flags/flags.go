// Package flags decides which variant of a feature flag a visitor gets.
//
// A flag is on or off. When on, a visitor who passes every one of its
// rules, and falls inside its rollout, gets one of its variants by weight.
// Which one comes from a hash of the flag's salt and the visitor's id, so
// the same visitor gets the same variant on every page, every server and
// every process, with nothing stored per visitor. Changing the weights of a
// running flag moves only the visitors near the boundaries it shifts.
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
	MaxPayload     = 4 << 10
	MaxDescription = 1000
	// Buckets a visitor's hash falls into: weights and rollout are in
	// hundredths of a percent of these.
	buckets = 10_000
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
	// Why: off (the flag is off), rules (a rule failed), rollout (outside
	// it), no_subject (no id to hash), or match.
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
// from the visual editor or written by hand in its payload's "changes":
// the elements matching Selector get Text as their text, are hidden, or
// get Href, Src or Alt. Text is set as text, never as HTML.
type Change struct {
	Selector string  `json:"selector"`
	Text     *string `json:"text,omitempty"`
	Hide     bool    `json:"hide,omitempty"`
	Href     *string `json:"href,omitempty"`
	Src      *string `json:"src,omitempty"`
	Alt      *string `json:"alt,omitempty"`
}

// MaxChanges is how many edits one version may make.
const MaxChanges = 50

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

// CheckChanges says what is wrong with a payload's "changes", or "".
func CheckChanges(payload json.RawMessage) string {
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
			return "changes is a list of {selector, and text, hide, href, src or alt}"
		}
	}
	if len(cs) > MaxChanges {
		return fmt.Sprintf("at most %d changes", MaxChanges)
	}
	for _, c := range cs {
		if c.Selector == "" || len(c.Selector) > 300 || strings.ContainsAny(c.Selector, "<{}") {
			return "each change needs a CSS selector of up to 300 characters, such as h1.hero"
		}
		n := 0
		for _, set := range []bool{c.Text != nil, c.Hide, c.Href != nil, c.Src != nil || c.Alt != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			return "each change does one thing: text, hide, href, or src and alt"
		}
		if c.Text != nil && len(*c.Text) > 2000 {
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
