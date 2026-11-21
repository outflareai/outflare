package api

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type capturedEvent struct {
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
}

// runCapture runs site.js in the stand-in browser and gives the track
// events it sent, without the properties every conversion carries.
func runCapture(t *testing.T, opts map[string]any) []capturedEvent {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	opts["href"] = "https://northpeak.example/pricing?plan=pro"
	opts["loaded"] = true
	b, _ := json.Marshal(opts)
	out, err := exec.Command(node, "testdata/run_site.mjs", "site.js", string(b)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var got struct{ Tracked []capturedEvent }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, e := range got.Tracked {
		delete(e.Properties, "click_ids")
	}
	return got.Tracked
}

// Autocapture sends a click on a link or button and a form submit, only
// when the site key's switch is on, and never anything a visitor typed.
func TestSiteAutocapture(t *testing.T) {
	type el = map[string]any
	button := el{"tag": "span", "text": "Start free", "parent": el{"tag": "button", "text": "Start free", "attrs": el{"id": "cta", "class": "btn btn-primary"}}}
	link := el{"tag": "a", "text": "Read the docs", "attrs": el{"href": "/docs/start?token=abc#top"}}
	// A label that looks like a person's address or a key is left out.
	email := el{"tag": "button", "text": "ana@acme.example"}
	key := el{"tag": "button", "text": "oft_test_not_a_real_token_0123456789"}
	plain := el{"tag": "div", "text": "Just text"}
	ignored := el{"tag": "button", "text": "Delete", "parent": el{"tag": "div", "attrs": el{"data-outflare-ignore": ""}}}
	masked := el{"tag": "button", "text": "Balance 12", "parent": el{"tag": "div", "attrs": el{"data-of-mask": ""}}}
	named := el{"tag": "div", "text": "Pro", "attrs": el{"data-outflare-event": "Plan Chosen"}}
	typed := el{"tag": "input", "value": "what someone typed", "attrs": el{"type": "text"}}
	submit := el{"tag": "input", "value": "Send", "attrs": el{"type": "submit"}}
	form := el{"tag": "form", "fields": 3, "attrs": el{"id": "signup", "name": "signup", "action": "/signup?ref=x", "method": "POST"}}
	clicks := []el{button, link, email, key, plain, ignored, masked, named, typed, submit}

	t.Run("on", func(t *testing.T) {
		got := runCapture(t, map[string]any{"capture": el{"clicks": true, "forms": true}, "clicks": clicks, "submits": []el{form, plain}})
		want := []capturedEvent{
			{"Element Clicked", el{"tag": "button", "path": "/pricing", "text": "Start free", "id": "cta", "classes": "btn btn-primary"}},
			{"Element Clicked", el{"tag": "a", "path": "/pricing", "text": "Read the docs", "href": "https://northpeak.example/docs/start"}},
			{"Element Clicked", el{"tag": "button", "path": "/pricing"}},
			{"Element Clicked", el{"tag": "button", "path": "/pricing"}},
			{"Element Clicked", el{"tag": "button", "path": "/pricing"}},
			{"Plan Chosen", el{"tag": "div", "path": "/pricing", "text": "Pro"}},
			{"Element Clicked", el{"tag": "input", "path": "/pricing", "text": "Send"}},
			{"Form Submitted", el{"path": "/pricing", "method": "post", "id": "signup", "name": "signup", "action": "https://northpeak.example/signup", "fields": float64(3)}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
	})
	// Off (the default): only an element the site named itself is sent.
	t.Run("off", func(t *testing.T) {
		got := runCapture(t, map[string]any{"clicks": clicks, "submits": []el{form}})
		want := []capturedEvent{{"Plan Chosen", el{"tag": "div", "path": "/pricing", "text": "Pro"}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
	})
	t.Run("only forms", func(t *testing.T) {
		got := runCapture(t, map[string]any{"capture": el{"forms": true}, "clicks": []el{button}, "submits": []el{form}})
		if len(got) != 1 || got[0].Event != "Form Submitted" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("stopped on the tag", func(t *testing.T) {
		got := runCapture(t, map[string]any{"capture": el{"clicks": true, "forms": true}, "scriptAttrs": el{"data-autocapture": "off"}, "clicks": clicks, "submits": []el{form}})
		if len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	// A page that clicks in a loop sends at most 100 captured events.
	t.Run("capped", func(t *testing.T) {
		many := make([]el, 150)
		for i := range many {
			many[i] = link
		}
		if got := runCapture(t, map[string]any{"capture": el{"clicks": true}, "clicks": many}); len(got) != 100 {
			t.Fatalf("%d events", len(got))
		}
	})
}

// Errors are sent as Error Caught only with the switch on (or when the
// page hands one over itself), grouped by a fingerprint that ignores
// numbers, and never carry an address or a key.
func TestSiteErrorCapture(t *testing.T) {
	type el = map[string]any
	stack := "TypeError: plan 42 not found\n    at choose (https://northpeak.example/assets/app.js?v=3:10:5)\n    at https://northpeak.example/assets/app.js:99:1"
	first := el{"error": el{"name": "TypeError", "message": "plan 42 not found", "stack": stack}, "filename": "https://northpeak.example/assets/app.js?v=3", "lineno": 10, "colno": 5}
	// The same bug with another number, from a new build's line.
	again := el{"error": el{"name": "TypeError", "message": "plan 7 not found", "stack": "TypeError: plan 7 not found\n    at choose (https://northpeak.example/assets/app.js:12:9)"}}
	private := el{"error": el{"name": "Error", "message": "no account for ana@acme.example with oft_test_not_a_real_token_0123456789abcdef"}}
	hidden := el{"message": "Script error."}

	t.Run("on", func(t *testing.T) {
		got := runCapture(t, map[string]any{"capture": el{"errors": true}, "earlyErrors": []el{first}, "errors": []el{again, private, hidden}, "rejections": []any{"timed out"}})
		if len(got) != 4 {
			t.Fatalf("got %+v", got)
		}
		for _, e := range got {
			if e.Event != "Error Caught" || e.Properties["handled"] != false || e.Properties["path"] != "/pricing" {
				t.Fatalf("event: %+v", e)
			}
		}
		a, b := got[0].Properties, got[1].Properties
		if a["type"] != "TypeError" || a["message"] != "plan 42 not found" || a["source"] != "https://northpeak.example/assets/app.js" || a["line"] != float64(10) {
			t.Fatalf("first: %+v", a)
		}
		if st, _ := a["stack"].(string); st == "" || strings.Contains(st, "?v=3") {
			t.Fatalf("stack: %q", st)
		}
		if fp, _ := a["fingerprint"].(string); len(fp) != 16 || fp != b["fingerprint"] {
			t.Fatalf("fingerprints %v and %v", a["fingerprint"], b["fingerprint"])
		}
		if m := got[2].Properties["message"]; m != "no account for [email] with [key]" {
			t.Fatalf("message: %v", m)
		}
		if p := got[3].Properties; p["message"] != "timed out" || p["rejection"] != true || p["fingerprint"] == a["fingerprint"] {
			t.Fatalf("rejection: %+v", p)
		}
	})
	// Off: nothing uncaught is sent, but the page can hand one over.
	t.Run("off", func(t *testing.T) {
		got := runCapture(t, map[string]any{"earlyErrors": []el{first}, "errors": []el{again}, "calls": [][]any{{"captureException", el{"name": "PaymentError", "message": "card declined"}, el{"step": "checkout"}}}})
		if len(got) != 1 || got[0].Properties["type"] != "PaymentError" || got[0].Properties["handled"] != true || got[0].Properties["step"] != "checkout" {
			t.Fatalf("got %+v", got)
		}
	})
	// One issue is sent at most 5 times a page, and a page sends at most 20.
	t.Run("capped", func(t *testing.T) {
		many := make([]el, 0, 60)
		for i := 0; i < 10; i++ {
			many = append(many, again)
		}
		if got := runCapture(t, map[string]any{"capture": el{"errors": true}, "errors": many}); len(got) != 5 {
			t.Fatalf("%d of one issue", len(got))
		}
		for i := 0; i < 50; i++ {
			many = append(many, el{"error": el{"name": "Error", "message": "broke in " + string(rune('a'+i%26)) + string(rune('a'+i/26))}})
		}
		if got := runCapture(t, map[string]any{"capture": el{"errors": true}, "errors": many}); len(got) != 20 {
			t.Fatalf("%d on one page", len(got))
		}
	})
}

// The signed-in hint is a cookie that only says "1": the app sets it, every
// page can read it, flags and page views carry it, and sign-out or reset
// removes it.
func TestSiteSignedInHint(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	type result struct {
		State      any
		Cookies    map[string]string
		FlagBodies []map[string]any
		Bodies     []struct {
			Batch []struct {
				Type    string
				Context map[string]any
			}
		}
		Elements map[string][]struct{ Display string }
	}
	run := func(opts map[string]any) result {
		t.Helper()
		opts["href"] = "https://northpeak.example/pricing"
		opts["loaded"] = true
		b, _ := json.Marshal(opts)
		out, err := exec.Command(node, "testdata/run_site.mjs", "site.js", string(b)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var got result
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return got
	}
	show, hide := `[data-outflare-signed-in="show"]`, `[data-outflare-signed-in="hide"]`
	els := map[string]any{show: 1, hide: 1}
	flags := map[string]any{"flags": map[string]any{}, "payloads": map[string]any{}}
	signedIn := func(r result) bool {
		for _, b := range r.Bodies {
			for _, e := range b.Batch {
				if e.Type == "page" {
					return e.Context["signed_in"] == true
				}
			}
		}
		t.Fatalf("no page view sent: %+v", r.Bodies)
		return false
	}

	// A stranger: no cookie, nothing on the page view, the "show" part hidden.
	r := run(map[string]any{"elements": els, "flags": flags, "calls": [][]any{{"isSignedIn"}}})
	if r.State != false || r.Cookies["of_signed_in"] != "" || signedIn(r) || r.Elements[show][0].Display != "none" || r.Elements[hide][0].Display != "" {
		t.Fatalf("stranger: %+v", r)
	}
	if len(r.FlagBodies) == 0 || r.FlagBodies[0]["signedIn"] != false {
		t.Fatalf("stranger's flags request: %+v", r.FlagBodies)
	}
	// The app says so after sign-in: a cookie that holds only "1", and the
	// flags are asked again with it.
	r = run(map[string]any{"elements": els, "flags": flags, "calls": [][]any{{"signedIn", true}}})
	if r.State != true || r.Cookies["of_signed_in"] != "1" || r.Elements[show][0].Display != "" || r.Elements[hide][0].Display != "none" {
		t.Fatalf("after sign-in: %+v", r)
	}
	if last := r.FlagBodies[len(r.FlagBodies)-1]; len(r.FlagBodies) < 2 || last["signedIn"] != true {
		t.Fatalf("flags after sign-in: %+v", r.FlagBodies)
	}
	// Another page of the site finds the cookie: page views and flags carry it.
	r = run(map[string]any{"cookie": "of_signed_in=1", "elements": els, "flags": flags, "calls": [][]any{{"isSignedIn"}}})
	if r.State != true || !signedIn(r) || r.FlagBodies[0]["signedIn"] != true || r.Elements[hide][0].Display != "none" {
		t.Fatalf("with the cookie: %+v", r)
	}
	// Sign-out and reset both remove it.
	for _, call := range [][]any{{"signedIn", false}, {"reset"}} {
		r = run(map[string]any{"cookie": "of_signed_in=1", "elements": els, "flags": flags, "calls": [][]any{call, {"isSignedIn"}}})
		if r.State != false || r.Cookies["of_signed_in"] != "" || r.Elements[show][0].Display != "none" {
			t.Fatalf("after %v: %+v", call, r)
		}
	}
	// Any other value is not a sign-in.
	if r = run(map[string]any{"cookie": "of_signed_in=10", "calls": [][]any{{"isSignedIn"}}}); r.State != false {
		t.Fatalf("a cookie that isn't 1: %+v", r)
	}
}

// outflare.identify sends the user id and its signature with the
// visitor's id, and nothing without a signature.
func TestSiteIdentifyCall(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	run := func(calls [][]any) []map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"href": "https://northpeak.example/app", "loaded": true, "calls": calls})
		out, err := exec.Command(node, "testdata/run_site.mjs", "site.js", string(b)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var got struct {
			Anon       string
			Identifies []map[string]any
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		for _, i := range got.Identifies {
			if i["anonymousId"] != got.Anon {
				t.Fatalf("identify for another visitor: %v, want %s", i, got.Anon)
			}
		}
		return got.Identifies
	}
	got := run([][]any{{"identify", "u-ana", "abc123"}})
	if len(got) != 1 || got[0]["userId"] != "u-ana" || got[0]["signature"] != "abc123" || got[0]["key"] == "" || got[0]["traits"] != nil {
		t.Fatalf("identify: %v", got)
	}
	if got := run([][]any{{"identify", "u-ana"}, {"identify", "", "abc123"}}); len(got) != 0 {
		t.Fatalf("identify without an id or a signature was sent: %v", got)
	}
}
