package api

import "testing"

func TestIsCompareURL(t *testing.T) {
	yes := []string{
		"https://northpeak.example/compare",
		"https://northpeak.example/compare/oxylabs",
		"https://northpeak.example/zh/compare/oxylabs?utm=1",
		"https://www.northpeak.example/fr/compare/",
	}
	no := []string{
		"https://northpeak.example/pricing",
		"https://northpeak.example/blog/compare-notes",
		"https://northpeak.example/comparison",
		"not-a-url",
		"",
	}
	for _, u := range yes {
		if !isCompareURL(u) {
			t.Fatalf("expected compare: %s", u)
		}
	}
	for _, u := range no {
		if isCompareURL(u) {
			t.Fatalf("did not expect compare: %s", u)
		}
	}
}
