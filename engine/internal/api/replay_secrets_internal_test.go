package api

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// The website script hides keys, tokens and passwords in a recording
// before it is sent, and leaves ordinary text alone.
func TestSiteScriptHidesSecrets(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	// Made-up values, put together here so no scanner takes them for real.
	jwt := "ey" + "J" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 12) + "." + strings.Repeat("c", 12)
	short := "abcd1234" + "efgh5678"
	hidden := []string{
		"Your API key: " + short,
		"sk-test-not-a-real-key-0000000000",
		"oft_test_not_a_real_token_000000",
		jwt,
		"6f1c2a9e-4b7d-4c1a-9e2f-0a1b2c3d4e5f",
		"Authorization: Bearer notreal.token-123456",
		"Password = notreal2000",
		"a1b2c3d4e5f6a7b8",
	}
	kept := []string{
		"See /docs/guides/session-recordings-and-heatmaps for more",
		"Simple pricing for teams of 20 or more people, internationally",
		"token expired yesterday",
		"Order #12345 shipped on 2026-11-16 at 13:40",
		"ana@acme.example",
	}
	in := map[string]any{
		"texts":     append(append([]string{}, hidden...), kept...),
		"addresses": []string{"https://acme.example/cb?api_key=notreal123&page=2", "http://user-1:notrealpass99@proxy.acme.example:9000"},
		"event": map[string]any{"type": 2, "data": map[string]any{"node": map[string]any{"childNodes": []any{map[string]any{
			"attributes": map[string]any{"title": "key " + short + "ijkl9012", "class": short + "ijkl9012", "data-copy": short + "ijkl9012"},
			"childNodes": []any{map[string]any{"textContent": "Secret: notreal-value"}},
		}}}}},
	}
	b, _ := json.Marshal(in)
	raw, err := exec.Command(node, "testdata/run_secrets.mjs", "site.js", string(b)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	var out struct {
		Texts     []string `json:"texts"`
		Addresses []string `json:"addresses"`
		Event     json.RawMessage
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	for i, want := range hidden {
		got := out.Texts[i]
		if len(got) != len(want) || !strings.Contains(got, "****") {
			t.Errorf("%q should be hidden, got %q", want, got)
		}
		for _, secret := range []string{short, "not-a-real-key", "not_a_real_token", "aaaaaaaaaaaa", "4b7d", "token-123456", "notreal2000", "a1b2c3d4e5f6a7b8"} {
			if strings.Contains(got, secret) {
				t.Errorf("%q still shows %q", got, secret)
			}
		}
	}
	for i, want := range kept {
		if got := out.Texts[len(hidden)+i]; got != want {
			t.Errorf("%q should be left alone, got %q", want, got)
		}
	}
	if out.Addresses[0] != "https://acme.example/cb?api_key=**********&page=2" || out.Addresses[1] != "http://user-1:*************@proxy.acme.example:9000" {
		t.Errorf("addresses: %v", out.Addresses)
	}
	ev := string(out.Event)
	if strings.Contains(ev, `"title":"key abcd`) || strings.Contains(ev, `"data-copy":"abcd`) || strings.Contains(ev, "notreal-value") || !strings.Contains(ev, `"class":"abcd1234efgh5678ijkl9012"`) {
		t.Errorf("the page's attributes and text: %s", ev)
	}
}

// The server hides the known key formats again before it stores a part.
func TestHideReplaySecrets(t *testing.T) {
	in := `[{"type":3,"data":{"text":"key sk-test-not-a-real-key-0000000000 and oft_test_not_a_real_token_000000"}},{"type":2,"data":{"class":"plain-text-stays"}}]`
	out := string(hideReplaySecrets([]byte(in)))
	if len(out) != len(in) || strings.Contains(out, "not-a-real-key") || strings.Contains(out, "not_a_real_token") || !strings.Contains(out, "plain-text-stays") {
		t.Fatalf("got %s", out)
	}
	var v []any
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v) != 2 {
		t.Fatalf("still JSON: %v", err)
	}
}

// A brand's own key shapes are hidden by the website script and again by
// the server, only where the shape stands on its own.
func TestReplayKeyShapes(t *testing.T) {
	shape := "xxxx-xxxx-xxxx"
	made := "ab" + "12-cd34-ef56"
	raw := `[{"type":3,"data":{"text":"key ` + made + ` and\n` + made + `, not z` + made + ` or ` + made + `9"}},{"type":2,"data":{"href":"/k/` + made + `/x"}}]`
	out := string(hideReplayShapes([]byte(raw), []string{shape}))
	if len(out) != len(raw) || strings.Count(out, made) != 2 || strings.Count(out, "**************") != 3 || !strings.Contains(out, `and\n*`) {
		t.Fatalf("got %s", out)
	}
	var v []any
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v) != 2 {
		t.Fatalf("still JSON: %v", err)
	}
	if _, err := cleanReplayShapes([]string{made}); err == nil {
		t.Fatal("a key is not a shape")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	in, _ := json.Marshal(map[string]any{"shapes": []string{shape}, "texts": []string{"key " + made + " here", "z" + made + " stays"},
		"addresses": []string{"https://acme.example/k/" + made + "/x"}, "event": map[string]any{}})
	res, err := exec.Command(node, "testdata/run_secrets.mjs", "site.js", string(in)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Texts, Addresses []string }
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatal(err)
	}
	if got.Texts[0] != "key ************** here" || got.Texts[1] != "z"+made+" stays" || got.Addresses[0] != "https://acme.example/k/**************/x" {
		t.Fatalf("the script: %s", res)
	}
}
