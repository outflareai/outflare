package catalog

import "testing"

// Every connector has its own id: the registry keeps one per id, so two
// sharing one would silently hide the other.
func TestConnectorIDsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, c := range Available() {
		d := c.Definition()
		if other, ok := seen[d.ID]; ok {
			t.Errorf("connector id %q is used by both %q and %q", d.ID, other, d.Name)
		}
		seen[d.ID] = d.Name
	}
}
