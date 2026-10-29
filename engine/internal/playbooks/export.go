package playbooks

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Marshal writes the kit as a kit file, in the layout of the kits in
// playbooks/kits: each block starts with its id, key or handle, kind and
// name, and empty settings are left out. ParseKit reads it back as the
// same kit.
func (k Kit) Marshal() ([]byte, error) {
	var n yaml.Node
	if err := n.Encode(k); err != nil {
		return nil, err
	}
	firstKeys(&n)
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(&n); err != nil {
		return nil, err
	}
	if err := e.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// leading are the keys a block starts with, in this order.
var leading = []string{"id", "key", "handle", "kind", "name"}

// firstKeys moves the leading keys of every mapping in n to its start,
// keeping the rest in order (struct fields as declared, a map's keys
// sorted), so a step reads id, kind, then its settings.
func firstKeys(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		type pair struct{ k, v *yaml.Node }
		var pairs []pair
		for i := 0; i+1 < len(n.Content); i += 2 {
			pairs = append(pairs, pair{n.Content[i], n.Content[i+1]})
		}
		rank := func(p pair) int {
			if i := slices.Index(leading, p.k.Value); i >= 0 {
				return i
			}
			return len(leading)
		}
		slices.SortStableFunc(pairs, func(a, b pair) int { return rank(a) - rank(b) })
		n.Content = n.Content[:0]
		for _, p := range pairs {
			n.Content = append(n.Content, p.k, p.v)
		}
	}
	for _, c := range n.Content {
		firstKeys(c)
	}
}

// KeyFor makes a key for a kit part from its name ("Activate new trials"
// becomes activate_new_trials), different from the ones in taken, which
// it adds it to.
func KeyFor(name string, taken map[string]bool) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if under && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			under = false
		default:
			under = true
		}
	}
	key := b.String()
	if len(key) < 2 || key[0] < 'a' || key[0] > 'z' {
		key = "part_" + key
	}
	key = strings.TrimRight(key[:min(len(key), 36)], "_")
	out := key
	for n := 2; taken[out]; n++ {
		out = fmt.Sprintf("%s_%d", key, n)
	}
	taken[out] = true
	return out
}

// IDFor makes a kit id from a title ("Activate new trials" becomes
// activate-new-trials).
func IDFor(title string) string {
	id := strings.ReplaceAll(KeyFor(title, map[string]bool{}), "_", "-")
	if !idPattern.MatchString(id) {
		return "exported-flow"
	}
	return id
}
