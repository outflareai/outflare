package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/flags"
)

// The outline numbers what a person reads and clicks, with selectors that
// find exactly that part.
func TestPageOutline(t *testing.T) {
	page := `<html><head><title>x</title><script>var a = "<h1>no</h1>";</script></head><body>
<header id="top"><a href="/">Home</a><nav><ul><li><a href="/pricing">Pricing</a></li><li>Plain item</li></ul></nav></header>
<main><section><h1>Free Proxy List</h1><p>Updated <b>every</b> 10 minutes.</p><p></p><a class="btn" href="/download">Download</a></section>
<div id="dup"><p>One</p></div><div id="dup"><p>Two</p></div><img src="/x.png" alt="A chart"><p class="jsx-3942758193 lead">Lead</p><style>p{}</style></main></body></html>`
	got := pageOutline([]byte(page))
	var lines []string
	for _, el := range got {
		lines = append(lines, el.ID+" "+el.Tag+" "+el.Selector+" | "+el.Text)
	}
	want := []string{
		"e1 header #top | (header)",
		"e2 a #top > a | Home",
		"e3 a #top > nav > ul > li:nth-of-type(1) > a | Pricing",
		"e4 li #top > nav > ul > li:nth-of-type(2) | Plain item",
		"e5 section body > main > section | (section: Free Proxy List)",
		"e6 h1 body > main > section > h1 | Free Proxy List",
		"e7 p body > main > section > p:nth-of-type(1) | Updated every 10 minutes.",
		"e8 a body > main > section > a.btn | Download",
		"e9 p body > main > div:nth-of-type(1) > p | One",
		"e10 p body > main > div:nth-of-type(2) > p | Two",
		"e11 img body > main > img | A chart",
		"e12 p body > main > p.lead | Lead",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("outline:\n%s", strings.Join(lines, "\n"))
	}
	if got[7].Href != "/download" {
		t.Fatalf("href: %v", got[7])
	}
}

// What a version added to its page is a part the AI can change too, found
// by the script's mark on it; a change to it passes the checks.
func TestInsertedParts(t *testing.T) {
	payload := json.RawMessage(`{"changes":[{"selector":"h1","text":"Hi"},{"selector":"#hero","insert":{"kind":"button","where":"after","text":"Start free","href":"/signup"}},{"selector":"#hero","insert":{"kind":"image","where":"before","src":"https://northpeak.example/a.png","alt":"A chart"}}]}`)
	variants := []flags.Variant{{Key: "control"}, {Key: "b", Payload: payload}}
	got := insertedParts("home-hero", "b", variants, map[string]string{"#hero": "e3"})
	if len(got) != 2 {
		t.Fatalf("parts: %+v", got)
	}
	if got[0].ID != "n1" || got[0].Tag != "a" || got[0].Selector != "[data-of-insert='home-hero:1']" || got[0].Text != "Start free (added after e3)" || got[0].Href != "/signup" {
		t.Fatalf("button: %+v", got[0])
	}
	if got[1].ID != "n2" || got[1].Tag != "img" || got[1].Selector != "[data-of-insert='home-hero:2']" || got[1].Text != "A chart (added before e3)" {
		t.Fatalf("image: %+v", got[1])
	}
	if insertedParts("home-hero", "control", variants, nil) != nil {
		t.Fatal("control has no added parts")
	}
	parts := map[string]pageElement{got[0].ID: got[0]}
	ch, ok := toChange(abAIPageChange{Element: "n1", Action: "style", StyleProperty: "background-color", StyleValue: "#0f766e"}, parts)
	if !ok || ch.Selector != got[0].Selector || ch.Style["background-color"] != "#0f766e" {
		t.Fatalf("style on an added part: %+v %v", ch, ok)
	}
	body, _ := json.Marshal(map[string]any{"changes": []flags.Change{ch}})
	if msg := flags.CheckChanges(body); msg != "" {
		t.Fatalf("checked: %s", msg)
	}
}
