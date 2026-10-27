package emails

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
)

// Templates use a small, safe syntax, the same in the subject, the HTML
// and the plain text:
//
//	{{first_name}}               a variable (HTML-escaped in the HTML part)
//	{{first_name|there}}         with a fallback when it is empty
//	{{user.plan}}                a field of an object variable
//	{{{receipt_html}}}           not escaped, for HTML you built yourself
//	{{#if coupon}}…{{else}}…{{/if}}
//
// Nothing else runs: no loops, no functions, no file access.

// Variable describes one variable a template uses.
type Variable struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Default  string `json:"default"`
	Example  string `json:"example"`
}

// Rendered is a template filled in.
type Rendered struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text"`
	// Missing lists required variables that had no value, sorted.
	Missing []string `json:"missing"`
	// Unknown lists variables the content uses that the template does not
	// declare, so editors can add them.
	Unknown []string `json:"unknown"`
}

// Groups: 1 raw name; 2 #if or #unless and 3 its name; 4 else, /if or
// /unless; 5 a variable and 6 its fallback.
var tag = regexp.MustCompile(`\{\{\{\s*([A-Za-z_][\w.]*)\s*\}\}\}|\{\{\s*(?:(#if|#unless)\s+([A-Za-z_][\w.]*)|(else|/if|/unless)|([A-Za-z_][\w.]*)\s*(?:\|([^}]*))?)\s*\}\}`)

type node struct {
	text     string
	name     string
	fallback *string
	raw      bool
	cond     string // "if" or "unless"
	then     []node
	els      []node
}

// parse turns content into nodes, or explains the first mistake.
func parse(src string) ([]node, error) {
	type frame struct {
		n      *node
		inElse bool
	}
	root := &node{}
	stack := []frame{{n: root}}
	add := func(x node) {
		f := &stack[len(stack)-1]
		if f.inElse {
			f.n.els = append(f.n.els, x)
		} else {
			f.n.then = append(f.n.then, x)
		}
	}
	pos := 0
	for _, m := range tag.FindAllStringSubmatchIndex(src, -1) {
		if m[0] > pos {
			add(node{text: src[pos:m[0]]})
		}
		pos = m[1]
		get := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return src[m[2*i]:m[2*i+1]]
		}
		raw, open, condName, closer, name := get(1), get(2), get(3), get(4), get(5)
		switch {
		case raw != "":
			add(node{name: raw, raw: true})
		case open != "":
			stack = append(stack, frame{n: &node{name: condName, cond: open[1:]}})
		case closer == "else":
			if len(stack) == 1 {
				return nil, fmt.Errorf("{{else}} is only allowed inside {{#if}} or {{#unless}}")
			}
			stack[len(stack)-1].inElse = true
		case closer != "":
			if len(stack) == 1 || stack[len(stack)-1].n.cond != closer[1:] {
				return nil, fmt.Errorf("{{%s}} does not close an open {{#%s}}", closer, closer[1:])
			}
			done := *stack[len(stack)-1].n
			stack = stack[:len(stack)-1]
			add(done)
		case name != "":
			x := node{name: name}
			if m[12] >= 0 {
				fb := strings.TrimSpace(get(6))
				x.fallback = &fb
			}
			add(x)
		}
	}
	if pos < len(src) {
		add(node{text: src[pos:]})
	}
	if len(stack) > 1 {
		return nil, fmt.Errorf("{{#%s %s}} is never closed with {{/%s}}", stack[len(stack)-1].n.cond, stack[len(stack)-1].n.name, stack[len(stack)-1].n.cond)
	}
	return root.then, nil
}

// lookup finds a dotted name in vars.
func lookup(vars map[string]any, name string) (string, bool) {
	var cur any = vars
	for _, part := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[part]; !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case nil:
		return "", false
	case string:
		return v, v != ""
	case bool:
		if !v {
			return "", false
		}
		return "true", true
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v)), true
		}
		return fmt.Sprintf("%g", v), true
	case []any:
		return fmt.Sprint(len(v)), len(v) > 0
	case map[string]any:
		return "", len(v) > 0
	default:
		s := fmt.Sprint(v)
		return s, s != ""
	}
}

type renderer struct {
	vars     map[string]any
	defaults map[string]string
	used     map[string]bool
	escape   bool
}

func (r *renderer) value(name string) (string, bool) {
	r.used[name] = true
	if v, ok := lookup(r.vars, name); ok {
		return v, true
	}
	if d, ok := r.defaults[name]; ok && d != "" {
		return d, true
	}
	return "", false
}

func (r *renderer) render(nodes []node, b *strings.Builder) {
	for _, n := range nodes {
		switch {
		case n.cond != "":
			_, ok := r.value(n.name)
			if n.cond == "unless" {
				ok = !ok
			}
			if ok {
				r.render(n.then, b)
			} else {
				r.render(n.els, b)
			}
		case n.name != "":
			v, ok := r.value(n.name)
			if !ok && n.fallback != nil {
				v = *n.fallback
			}
			if r.escape && !n.raw {
				v = html.EscapeString(v)
			}
			b.WriteString(v)
		default:
			b.WriteString(n.text)
		}
	}
}

// Check parses every part and returns the first mistake, naming the part.
func Check(subject, htmlBody, text string) error {
	for _, p := range []struct{ name, src string }{{"subject", subject}, {"html", htmlBody}, {"text", text}} {
		if _, err := parse(p.src); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	return nil
}

// Render fills a template's parts with vars. Declared defaults fill
// variables that are missing; required ones still missing are listed.
func Render(subject, htmlBody, text string, defs []Variable, vars map[string]any) (Rendered, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	defaults := map[string]string{}
	declared := map[string]bool{}
	for _, d := range defs {
		defaults[d.Name], declared[d.Name] = d.Default, true
	}
	used := map[string]bool{}
	out := Rendered{Missing: []string{}, Unknown: []string{}}
	parts := []struct {
		src    string
		dst    *string
		escape bool
	}{{subject, &out.Subject, false}, {htmlBody, &out.HTML, true}, {text, &out.Text, false}}
	for i, p := range parts {
		nodes, err := parse(p.src)
		if err != nil {
			return out, fmt.Errorf("%s: %w", []string{"subject", "html", "text"}[i], err)
		}
		var b strings.Builder
		(&renderer{vars: vars, defaults: defaults, used: used, escape: p.escape}).render(nodes, &b)
		*p.dst = b.String()
	}
	out.Subject = strings.TrimSpace(strings.ReplaceAll(out.Subject, "\n", " "))
	for _, d := range defs {
		if !d.Required {
			continue
		}
		if _, ok := lookup(vars, d.Name); !ok && d.Default == "" {
			out.Missing = append(out.Missing, d.Name)
		}
	}
	for name := range used {
		root, _, _ := strings.Cut(name, ".")
		if !declared[name] && !declared[root] && !builtins[root] {
			out.Unknown = append(out.Unknown, name)
		}
	}
	sort.Strings(out.Missing)
	sort.Strings(out.Unknown)
	return out, nil
}

// Variables Outflare fills itself.
var builtins = map[string]bool{"unsubscribe_url": true, "email_id": true}

// Used returns every variable name the parts use, sorted, so a new
// template's variables can be listed for it.
func Used(subject, htmlBody, text string) []string {
	seen := map[string]bool{}
	var walk func([]node)
	walk = func(ns []node) {
		for _, n := range ns {
			if n.name != "" && !builtins[strings.Split(n.name, ".")[0]] {
				seen[n.name] = true
			}
			walk(n.then)
			walk(n.els)
		}
	}
	for _, src := range []string{subject, htmlBody, text} {
		if ns, err := parse(src); err == nil {
			walk(ns)
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PlainText makes a plain-text part from HTML, for templates that only
// have HTML: tags dropped, links kept as "text (url)", entities decoded.
func PlainText(h string) string {
	h = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`).ReplaceAllString(h, "")
	h = regexp.MustCompile(`(?is)<a\s[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`).ReplaceAllString(h, "$2 ($1)")
	h = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(h, "\n")
	h = regexp.MustCompile(`(?i)</(p|div|h[1-6]|li|tr|table)>`).ReplaceAllString(h, "\n\n")
	h = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(h, "")
	h = html.UnescapeString(h)
	lines := strings.Split(h, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	h = strings.Join(lines, "\n")
	h = regexp.MustCompile(`\n{3,}`).ReplaceAllString(h, "\n\n")
	return strings.TrimSpace(h)
}
