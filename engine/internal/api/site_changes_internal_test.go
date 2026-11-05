package api

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// The visual editor's richer changes, run by the website script on a tiny
// page: rich text is built as elements (never parsed as HTML), sections
// move, a button is added once however often changes apply, styles and
// CSS never load anything, a version can send its visitors to another page
// of the site's own (keeping the address's parameters) but never off it,
// and a change for phones applies only on a narrow screen.
func TestSiteRicherVisualChanges(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	run := func(opts map[string]any) string {
		t.Helper()
		b, _ := json.Marshal(opts)
		out, err := exec.Command(node, "testdata/run_changes.mjs", "site.js", string(b)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	page := []map[string]any{
		{"tag": "h1", "sel": []string{"h1"}, "text": "Old"},
		{"tag": "section", "sel": []string{"#a"}, "text": "A"},
		{"tag": "section", "sel": []string{"#b"}, "text": "B"},
	}
	got := run(map[string]any{"page": page, "payload": map[string]any{
		"css": "h1 { color: #0f766e }",
		"changes": []map[string]any{
			{"selector": "h1", "rich": "**Ship** *faster* <img src=x onerror=alert(1)> [start](/signup) [bad](javascript:alert(1))"},
			{"selector": "#b", "move": "before", "target": "#a"},
			{"selector": "#a", "insert": map[string]any{"where": "after", "kind": "button", "text": "Start free", "href": "/signup"}},
			{"selector": "h1", "style": map[string]any{"font-size": "40px", "background-color": "url(https://evil.example/x)"}},
		},
	}})
	for _, want := range []string{
		`{"tag":"strong","text":"Ship"}`, `{"tag":"em","text":"faster"}`, `" <img src=x onerror=alert(1)> "`,
		`{"tag":"a","attrs":{"href":"/signup"},"text":"start"}`, `"bad"`,
		`{"tag":"section","sel":"#b","text":"B"},{"tag":"section","sel":"#a","text":"A"},{"tag":"a","attrs":{"href":"/signup","role":"button","data-of-insert":"hero:2"},"text":"Start free"}`,
		`"style":{"font-size":"40px"}`, `{"id":"of-css-hero","css":"h1 { color: #0f766e }"}`, `"Experiment Viewed"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, `"href":"javascript`) || strings.Contains(got, "evil.example") || strings.Count(got, "Start free") != 1 {
		t.Fatalf("unsafe or repeated: %s", got)
	}

	// CSS that loads something is left out.
	if got := run(map[string]any{"page": page, "payload": map[string]any{"css": "body { background: url(https://evil.example/x) }"}}); strings.Contains(got, "of-css") {
		t.Fatalf("unsafe CSS added: %s", got)
	}

	// Split URL: to a page of the site, keeping the address's parameters.
	got = run(map[string]any{"href": "https://northpeak.example/pricing?utm_source=ads", "page": page, "payload": map[string]any{"redirect": "/pricing-b"}})
	if !strings.Contains(got, `"redirect":"https://northpeak.example/pricing-b?utm_source=ads"`) || !strings.Contains(got, "Experiment Viewed") {
		t.Fatalf("redirect: %s", got)
	}
	// Another of the site's domains is fine; anyone else's is not.
	if got := run(map[string]any{"page": page, "domains": "northpeak.example,northpeak-docs.example", "payload": map[string]any{"redirect": "https://www.northpeak-docs.example/start"}}); !strings.Contains(got, `"redirect":"https://www.northpeak-docs.example/start"`) {
		t.Fatalf("own domain: %s", got)
	}
	if got := run(map[string]any{"page": page, "payload": map[string]any{"redirect": "https://evil.example/login"}}); !strings.Contains(got, `"redirect":null`) {
		t.Fatalf("redirected off the site: %s", got)
	}
	// Already on the version's page: no loop.
	if got := run(map[string]any{"href": "https://northpeak.example/pricing-b", "page": page, "payload": map[string]any{"redirect": "/pricing-b"}}); !strings.Contains(got, `"redirect":null`) {
		t.Fatalf("redirect loop: %s", got)
	}

	// A change whose element is gone is reported once, with its selector.
	got = run(map[string]any{"page": page, "payload": map[string]any{"changes": []map[string]any{
		{"selector": "h1", "text": "New"}, {"selector": ".gone", "hide": true}, {"selector": "#a", "move": "after", "target": "#nope"},
	}}})
	if strings.Count(got, "Experiment Change Missed") != 1 || !strings.Contains(got, `"missed":2,"found":1,"selectors":[".gone","#nope"]`) {
		t.Fatalf("missing changes: %s", got)
	}
	if got := run(map[string]any{"page": page, "payload": map[string]any{"changes": []map[string]any{{"selector": "h1", "text": "New"}}}}); strings.Contains(got, "Missed") {
		t.Fatalf("reported with nothing missing: %s", got)
	}

	// Phones only.
	phone := map[string]any{"changes": []map[string]any{{"selector": "h1", "text": "Short", "device": "mobile"}}}
	if got := run(map[string]any{"page": page, "payload": phone, "width": 390}); !strings.Contains(got, `"text":"Short"`) {
		t.Fatalf("phone change not applied on a phone: %s", got)
	}
	if got := run(map[string]any{"page": page, "payload": phone}); strings.Contains(got, `"text":"Short"`) {
		t.Fatalf("phone change applied on a desktop: %s", got)
	}
}
