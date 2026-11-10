// Package tokens fills {token} placeholders in URLs: a tracker link's
// destination, a partner's postback and, later, a postback back to a traffic
// source (plans/ad-tracker.md, T3). One function, so every URL Outflare
// builds from a template follows the same rules.
//
// A token is {name}, optionally followed by functions after a pipe:
//
//	{click_id}         the value, escaped for a URL query
//	{field3|raw}       the value as it is (for a path or a value already escaped)
//	{email_hash|hash}  the SHA-256 of the value, in hex
//	{payout|50%}       a number scaled by a percentage (payout shares)
//
// Values are data from clicks, visitors and networks, never markup: unless
// |raw is asked for, every value is escaped before it goes into the URL.
// A token without a value is left empty.
package tokens

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Re matches one token. Names are lowercase snake case; functions follow
// pipes.
var Re = regexp.MustCompile(`\{([a-z][a-z0-9_]*)((?:\|[a-z0-9.%]+)*)\}`)

// percentRe is a percentage function: 50%, 12.5%, 150%.
var percentRe = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,2})?%$`)

// Placeholder is the word a template uses where a person still has to
// fill in a value of their own (a pixel id, an account key), as RedTrack
// does. A template with it isn't ready to save.
const Placeholder = "REPLACE"

// Values are the tokens' values by name.
type Values map[string]string

// Expand fills every token in tmpl.
func Expand(tmpl string, v Values) string {
	return expand(tmpl, v, nil)
}

// ExpandKnown fills only the tokens named in known and leaves any other
// {word} as written: a destination URL people wrote before tokens existed
// may have braces of its own. Braces a URL's path escaped (%7B, %7D) around
// a known token count as braces.
func ExpandKnown(tmpl string, v Values, known []string) string {
	allowed := map[string]bool{}
	for _, k := range known {
		allowed[k] = true
	}
	tmpl = escapedRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		inner := escapedRe.FindStringSubmatch(m)[1]
		if name, _, _ := strings.Cut(inner, "|"); allowed[name] {
			return "{" + inner + "}"
		}
		return m
	})
	return expand(tmpl, v, allowed)
}

// escapedRe is a token whose braces a URL path escaped.
var escapedRe = regexp.MustCompile(`(?i:%7B)([a-z][a-z0-9_]*(?:\|[a-z0-9.]+)*)(?i:%7D)`)

func expand(tmpl string, v Values, only map[string]bool) string {
	return Re.ReplaceAllStringFunc(tmpl, func(m string) string {
		parts := Re.FindStringSubmatch(m)
		if only != nil && !only[parts[1]] {
			return m
		}
		out, escape := v[parts[1]], true
		for _, fn := range functions(parts[2]) {
			switch {
			case fn == "raw":
				escape = false
			case fn == "hash":
				sum := sha256.Sum256([]byte(out))
				out = hex.EncodeToString(sum[:])
			case percentRe.MatchString(fn):
				out = percent(out, fn)
			}
		}
		if escape {
			return url.QueryEscape(out)
		}
		return out
	})
}

// Names are the token names tmpl uses, in order, once each.
func Names(tmpl string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range Re.FindAllStringSubmatch(tmpl, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ErrPlaceholder is a template that still has REPLACE in it.
var ErrPlaceholder = errors.New("fill in every " + Placeholder + " with your own value first")

// Check reports the first problem with tmpl's tokens: a name known
// doesn't accept, a function that doesn't exist, a brace that doesn't
// open or close a token, or a REPLACE left to fill in. known lists the
// names allowed, for the message.
func Check(tmpl string, known []string) error {
	if strings.Contains(tmpl, Placeholder) {
		return ErrPlaceholder
	}
	allowed := map[string]bool{}
	for _, k := range known {
		allowed[k] = true
	}
	for _, m := range Re.FindAllStringSubmatch(tmpl, -1) {
		if !allowed[m[1]] {
			return fmt.Errorf("{%s} isn't a token here; use %s", m[1], list(known))
		}
		for _, fn := range functions(m[2]) {
			if fn != "raw" && fn != "hash" && !percentRe.MatchString(fn) {
				return fmt.Errorf("{%s|%s}: %q isn't a function; use raw, hash or a percentage such as 50%%", m[1], fn, fn)
			}
		}
	}
	if rest := Re.ReplaceAllString(tmpl, ""); strings.ContainsAny(rest, "{}") {
		return errors.New("a { or } isn't part of a token; tokens look like {click_id} or {payout|50%}")
	}
	return nil
}

func functions(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(s, "|"), "|")
}

// percent scales a decimal number: "12.50" at 50% is "6.25". A value that
// isn't a number stays as it is. The result keeps up to six decimal
// places, without trailing zeros.
func percent(v, fn string) string {
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return v
	}
	p, _ := strconv.ParseFloat(strings.TrimSuffix(fn, "%"), 64)
	return strconv.FormatFloat(math.Round(n*p/100*1e6)/1e6, 'f', -1, 64)
}

func list(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "{" + n + "}"
	}
	return strings.Join(out, ", ")
}
