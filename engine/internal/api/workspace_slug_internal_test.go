package api

import "testing"

func TestSlugFrom(t *testing.T) {
	for name, want := range map[string]string{
		"Acme Outbound": "acme-outbound",
		"  Ünïcode  ":   "n-code",
		"settings":      "settings-workspace",
		"":              "workspace",
		"a-very-long-workspace-name-that-goes-on-and-on": "a-very-long-workspace-name-that-goes-on",
	} {
		got := slugFrom(name)
		if got != want {
			t.Errorf("slugFrom(%q) = %q, want %q", name, got, want)
		}
		if p := slugProblem(got); p != "" {
			t.Errorf("slugFrom(%q) = %q: %s", name, got, p)
		}
	}
}
