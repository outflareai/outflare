package brands

import (
	"regexp"
	"slices"
	"testing"
)

func TestCatalogIsWellFormed(t *testing.T) {
	c := All()
	slug := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	packs := []string{"saas_sales_led", "saas_self_serve", "subscription", "usage", "mobile", "ecommerce", "marketplace", "lead_gen", "bookings", "content", "donations"}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if !slug.MatchString(m.ID) || seen[m.ID] || m.Label == "" {
			t.Errorf("model %+v: bad or repeated id, or no label", m)
		}
		seen[m.ID] = true
		if !slices.ContainsFunc(c.Groups, func(g Group) bool { return g.ID == m.Group }) {
			t.Errorf("model %s: unknown group %q", m.ID, m.Group)
		}
		if len(m.Packs) == 0 {
			t.Errorf("model %s: no packs", m.ID)
		}
		for _, p := range m.Packs {
			if !slices.Contains(packs, p) {
				t.Errorf("model %s: unknown pack %q", m.ID, p)
			}
		}
	}
	for _, g := range c.Groups {
		if !slices.ContainsFunc(c.Models, func(m Model) bool { return m.Group == g.ID }) {
			t.Errorf("group %s has no models", g.ID)
		}
	}
	if _, ok := ModelByID("b2b_saas_self_serve"); !ok || !ValidRevenue("subscription") || ValidRevenue("barter") {
		t.Error("lookups")
	}
}
