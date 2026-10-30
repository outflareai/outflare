// Package emailfind proposes a likely work email for a person at a domain and
// learns each domain's address shape from a workspace's own leads and their
// verification outcomes. It spends no third-party credits: the signal is the
// workspace's own first-party data.
//
// The finder generates candidate addresses from a name (first.last, flast,
// ...), scores each against the domain's learned shape profile, and returns
// the best with a confidence and the evidence behind it. A background job
// rebuilds the profile as new outcomes land.
package emailfind

import "strings"

// A shape turns a first and last name into the local part of an address.
// The names are already normalised (lowercase, letters only) when build runs.
type shape struct {
	name  string
	build func(first, last string) string
	// prior is the shape's base likelihood before we have any evidence for a
	// domain, from how common the shape is across corporate mail. Relative,
	// not a probability; scoring normalises.
	prior float64
}

// shapes are the address formats the finder knows, most common first. Builders
// return "" when a required name part is missing, and such candidates are
// dropped.
var shapes = []shape{
	{"first.last", func(f, l string) string { return join(f, ".", l) }, 0.34},
	{"flast", func(f, l string) string { return initial(f) + l }, 0.22},
	{"firstlast", func(f, l string) string { return f + l }, 0.12},
	{"first", func(f, l string) string { return f }, 0.08},
	{"first_last", func(f, l string) string { return join(f, "_", l) }, 0.05},
	{"f.last", func(f, l string) string { return join(initial(f), ".", l) }, 0.04},
	{"firstl", func(f, l string) string { return f + initial(l) }, 0.03},
	{"first.l", func(f, l string) string { return join(f, ".", initial(l)) }, 0.02},
	{"lastfirst", func(f, l string) string { return l + f }, 0.02},
	{"last.first", func(f, l string) string { return join(l, ".", f) }, 0.02},
	{"last", func(f, l string) string { return l }, 0.02},
	{"lastf", func(f, l string) string { return l + initial(f) }, 0.015},
	{"f_last", func(f, l string) string { return join(initial(f), "_", l) }, 0.01},
	{"last_first", func(f, l string) string { return join(l, "_", f) }, 0.01},
}

var shapeByName = func() map[string]shape {
	m := make(map[string]shape, len(shapes))
	for _, s := range shapes {
		m[s.name] = s
	}
	return m
}()

func join(a, sep, b string) string {
	if a == "" || b == "" {
		return ""
	}
	return a + sep + b
}

func initial(s string) string {
	if s == "" {
		return ""
	}
	return s[:1]
}

// normalize lowercases a name and drops everything but ASCII letters, so
// "José-María" becomes "josemara" and comparisons are stable. It is a
// deliberately plain fold: shape matching only needs it to be consistent
// between the names we store and the addresses we see.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' {
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}

// localPart returns the part of an address before the @, lowercased, with any
// plus-tag removed. It returns "" when email has no local part.
func localPart(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 1 {
		return ""
	}
	lp := strings.ToLower(email[:at])
	if plus := strings.IndexByte(lp, '+'); plus >= 0 {
		lp = lp[:plus]
	}
	return lp
}

// candidate is one proposed address for a name.
type candidate struct {
	shape string
	local string
}

// candidatesFor builds every known shape for a name, skipping shapes that need
// a missing name part and de-duplicating identical local parts (keeping the
// more common shape). first and last are raw names; they are normalised here.
func candidatesFor(first, last string) []candidate {
	nf, nl := normalize(first), normalize(last)
	out := make([]candidate, 0, len(shapes))
	seen := map[string]bool{}
	for _, s := range shapes {
		lp := s.build(nf, nl)
		if lp == "" || seen[lp] {
			continue
		}
		seen[lp] = true
		out = append(out, candidate{shape: s.name, local: lp})
	}
	return out
}

// ShapesOf returns the shapes an address fits for a person's name, such as
// ["first.last"] for jane.roe@ and Jane Roe; none when it fits no known
// shape. The data network shares these, never the name.
func ShapesOf(first, last, email string) []string {
	return inferShapes(first, last, localPart(email))
}

// inferShapes returns the names of every shape whose build of (first, last)
// equals localpart. A plain name like "doe" can match more than one shape
// (e.g. "last"); an ambiguous match counts for each, and the dominant-shape
// maths across many addresses sorts out which the domain really uses.
func inferShapes(first, last, lp string) []string {
	nf, nl := normalize(first), normalize(last)
	lp = strings.ToLower(lp)
	if lp == "" {
		return nil
	}
	var out []string
	for _, s := range shapes {
		if built := s.build(nf, nl); built != "" && built == lp {
			out = append(out, s.name)
		}
	}
	return out
}
