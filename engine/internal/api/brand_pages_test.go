package api

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestBrandHandlesAreNotPages: the pages a brand handle may not be named
// after are the web app's pages, so /acme/{brand}/{page} never mixes the
// two up.
func TestBrandHandlesAreNotPages(t *testing.T) {
	src, err := os.ReadFile("../../../web/src/lib/paths.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export const appTabs = \[([^\]]*)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("appTabs not found in web/src/lib/paths.ts")
	}
	var tabs []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		tabs = append(tabs, string(q[1]))
	}
	got, want := slices.Sorted(slices.Values(pageNames)), slices.Sorted(slices.Values(tabs))
	if !slices.Equal(got, want) {
		t.Fatalf("pageNames in brands.go must match appTabs in web/src/lib/paths.ts:\n brands.go: %s\n paths.ts:  %s", strings.Join(got, " "), strings.Join(want, " "))
	}
	if handleProblem("seo") == "" || handleFrom("SEO") != "seo-brand" {
		t.Fatalf("seo: %q, %q", handleProblem("seo"), handleFrom("SEO"))
	}
}
