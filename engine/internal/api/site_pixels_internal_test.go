package api

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"
)

type pixelRun struct {
	// The calls OpenAI's pixel got, in order, and the scripts added.
	Oaiq [][]any
	// The calls Meta's and TikTok's pixels got, when the script loaded them.
	Metaq   [][]any
	Ttq     [][]any
	Scripts []string
	Stored  map[string]string
}

// runPixels runs site.js in the stand-in browser with an events answer
// that names the ChatGPT Ads pixel.
func runPixels(t *testing.T, opts map[string]any) pixelRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	opts["href"] = "https://northpeak.example/pricing"
	opts["loaded"] = true
	b, _ := json.Marshal(opts)
	out, err := exec.Command(node, "testdata/run_site.mjs", "site.js", string(b)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var got pixelRun
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return got
}

// The script loads the ChatGPT Ads pixel only when the events answer
// names one and the visitor allows marketing, hands it the hashed user
// data the server sent, and leaves a page's own snippet in charge.
func TestSitePixels(t *testing.T) {
	type obj = map[string]any
	const sdk = "https://bzrcdn.openai.com/sdk/oaiq.min.js"
	user := obj{"email_sha256": "aa11", "external_id_sha256": "bb22"}
	view := []any{"measure", "page_viewed", obj{"type": "contents"}}

	t.Run("loads it with the signed-in visitor", func(t *testing.T) {
		got := runPixels(t, obj{"pixels": obj{"chatgpt": obj{"id": "px_test_1", "user": user}}})
		want := [][]any{{"consent", true}, {"init", obj{"pixelId": "px_test_1", "user": user}}, view}
		if !reflect.DeepEqual(got.Oaiq, want) || !reflect.DeepEqual(got.Scripts, []string{sdk}) {
			t.Fatalf("got %+v", got)
		}
		if got.Stored["of_pixels"] == "" {
			t.Fatalf("the pixel isn't kept for the visit: %v", got.Stored)
		}
	})
	t.Run("a visitor nobody signed in", func(t *testing.T) {
		got := runPixels(t, obj{"pixels": obj{"chatgpt": obj{"id": "px_test_1"}}})
		want := [][]any{{"consent", true}, {"init", obj{"pixelId": "px_test_1"}}, view}
		if !reflect.DeepEqual(got.Oaiq, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("off", func(t *testing.T) {
		got := runPixels(t, obj{"capture": obj{"clicks": true}})
		if len(got.Oaiq) != 0 || len(got.Scripts) != 0 {
			t.Fatalf("a pixel loaded with the switch off: %+v", got)
		}
	})
	t.Run("data-pixels off", func(t *testing.T) {
		got := runPixels(t, obj{"pixels": obj{"chatgpt": obj{"id": "px_test_1", "user": user}}, "scriptAttrs": obj{"data-pixels": "off"}})
		if len(got.Oaiq) != 0 || len(got.Scripts) != 0 {
			t.Fatalf("a pixel loaded with data-pixels off: %+v", got)
		}
	})
	t.Run("no marketing consent", func(t *testing.T) {
		got := runPixels(t, obj{"pixels": obj{"chatgpt": obj{"id": "px_test_1", "user": user}}, "consent": "required"})
		if len(got.Oaiq) != 0 || len(got.Scripts) != 0 {
			t.Fatalf("a pixel loaded without consent: %+v", got)
		}
	})
	t.Run("consent given later", func(t *testing.T) {
		got := runPixels(t, obj{"pixels": obj{"chatgpt": obj{"id": "px_test_1", "user": user}}, "consent": "required", "calls": [][]any{{"consent", true}}})
		want := [][]any{{"consent", true}, {"init", obj{"pixelId": "px_test_1", "user": user}}, view}
		if !reflect.DeepEqual(got.Oaiq, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("consent withdrawn", func(t *testing.T) {
		got := runPixels(t, obj{"stored": obj{"of_pixels": `{"key":"ofk_test_not_a_real_key","id":"px_test_1","user":null}`},
			"pixels": obj{"chatgpt": obj{"id": "px_test_1"}}, "calls": [][]any{{"consent", false}}})
		want := [][]any{{"consent", true}, {"init", obj{"pixelId": "px_test_1"}}, view, {"consent", false}}
		if !reflect.DeepEqual(got.Oaiq, want) {
			t.Fatalf("got %+v", got)
		}
	})
	// A page with OpenAI's own snippet keeps it: no second copy, no second
	// page view, only the user data.
	t.Run("the page has its own snippet", func(t *testing.T) {
		got := runPixels(t, obj{"oaiq": true, "pixels": obj{"chatgpt": obj{"id": "px_test_1", "user": user}}})
		want := [][]any{{"init", obj{"pixelId": "px_test_1", "user": user}}}
		if !reflect.DeepEqual(got.Oaiq, want) || len(got.Scripts) != 0 {
			t.Fatalf("got %+v", got)
		}
		none := runPixels(t, obj{"oaiq": true, "pixels": obj{"chatgpt": obj{"id": "px_test_1"}}})
		if len(none.Oaiq) != 0 || len(none.Scripts) != 0 {
			t.Fatalf("got %+v", none)
		}
	})
	t.Run("a pixel id that isn't one", func(t *testing.T) {
		got := runPixels(t, obj{"pixels": obj{"chatgpt": obj{"id": "px\"><script>"}}})
		if len(got.Oaiq) != 0 || len(got.Scripts) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

// The script loads Meta's and TikTok's pixels the same way, and sends a
// conversion the page caused to them with the id the server sends it
// with, so the platform counts it once.
func TestSiteMetaAndTikTokPixels(t *testing.T) {
	type obj = map[string]any
	const metaSDK, tiktokSDK = "https://connect.facebook.net/en_US/fbevents.js", "https://analytics.tiktok.com/i18n/pixel/events.js?sdkid=TT_TEST_1&lib=ttq"
	metaUser := obj{"em": "aa11", "external_id": "bb22"}
	meta := obj{"id": "1234567890", "user": metaUser, "events": obj{
		"order_completed": obj{"type": "Purchase", "value_property": "total", "value_unit": "minor", "account_currency": "USD"},
		"signed_up":       obj{"type": "CompleteRegistration"},
		"demo_booked":     obj{"type": "DemoBooked", "value": 50.0, "currency": "EUR"},
	}}
	tiktok := obj{"id": "TT_TEST_1", "user": obj{"email": "aa11", "external_id": "bb22"}, "events": obj{"signed_up": obj{"type": "CompleteRegistration"}}}

	t.Run("loads both and counts the page", func(t *testing.T) {
		got := runPixels(t, obj{"pixels": obj{"meta": meta, "tiktok": tiktok}})
		wantMeta := [][]any{{"init", "1234567890", metaUser}, {"consent", "grant"}, {"trackSingle", "1234567890", "PageView"}}
		wantTT := [][]any{{"identify", obj{"email": "aa11", "external_id": "bb22"}}, {"grantConsent"}, {"page"}}
		if !reflect.DeepEqual(got.Metaq, wantMeta) || !reflect.DeepEqual(got.Ttq, wantTT) {
			t.Fatalf("got %+v", got)
		}
		if !reflect.DeepEqual(got.Scripts, []string{metaSDK, tiktokSDK}) || len(got.Oaiq) != 0 {
			t.Fatalf("scripts %v, oaiq %v", got.Scripts, got.Oaiq)
		}
	})
	// The pixels are kept for the visit, so they are running by the time
	// the page's own code calls track.
	kept := func(px obj) obj {
		b, _ := json.Marshal(obj{"key": "ofk_test_not_a_real_key", "id": "", "user": nil, "meta": px["meta"], "tiktok": px["tiktok"]})
		return obj{"of_pixels": string(b)}
	}
	last := func(q [][]any) []any {
		if len(q) == 0 {
			return nil
		}
		return q[len(q)-1]
	}
	t.Run("a conversion carries the server's id", func(t *testing.T) {
		px := obj{"meta": meta, "tiktok": tiktok}
		got := runPixels(t, obj{"pixels": px, "stored": kept(px), "track": "Signed Up", "trackId": "signup-u1"})
		if want := []any{"trackSingle", "1234567890", "CompleteRegistration", obj{}, obj{"eventID": "signup-u1"}}; !reflect.DeepEqual(last(got.Metaq), want) {
			t.Fatalf("meta: %v", got.Metaq)
		}
		if want := []any{"track", "CompleteRegistration", obj{}, obj{"event_id": "signup-u1"}}; !reflect.DeepEqual(last(got.Ttq), want) {
			t.Fatalf("tiktok: %v", got.Ttq)
		}
	})
	t.Run("a value from the event, in cents", func(t *testing.T) {
		px := obj{"meta": meta}
		got := runPixels(t, obj{"pixels": px, "stored": kept(px), "track": "orderCompleted", "trackProps": obj{"total": 1250, "currency": "gbp"}, "trackId": "order-9"})
		if want := []any{"trackSingle", "1234567890", "Purchase", obj{"value": 12.5, "currency": "GBP"}, obj{"eventID": "order-9"}}; !reflect.DeepEqual(last(got.Metaq), want) {
			t.Fatalf("meta: %v", got.Metaq)
		}
		if len(got.Ttq) != 0 {
			t.Fatalf("tiktok loaded with its switch off: %v", got.Ttq)
		}
	})
	t.Run("an event of your own with a fixed value", func(t *testing.T) {
		px := obj{"meta": meta}
		got := runPixels(t, obj{"pixels": px, "stored": kept(px), "track": "Demo Booked", "trackId": "demo-1"})
		if want := []any{"trackSingleCustom", "1234567890", "DemoBooked", obj{"value": 50.0, "currency": "EUR"}, obj{"eventID": "demo-1"}}; !reflect.DeepEqual(last(got.Metaq), want) {
			t.Fatalf("meta: %v", got.Metaq)
		}
	})
	t.Run("an event that is no conversion", func(t *testing.T) {
		px := obj{"meta": meta, "tiktok": tiktok}
		got := runPixels(t, obj{"pixels": px, "stored": kept(px), "track": "Plan Viewed"})
		if len(got.Metaq) != 3 || len(got.Ttq) != 3 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("no marketing consent", func(t *testing.T) {
		px := obj{"meta": meta, "tiktok": tiktok}
		got := runPixels(t, obj{"pixels": px, "stored": kept(px), "consent": "required", "track": "Signed Up"})
		if len(got.Metaq) != 0 || len(got.Ttq) != 0 || len(got.Scripts) != 0 {
			t.Fatalf("a pixel loaded without consent: %+v", got)
		}
	})
	t.Run("consent withdrawn", func(t *testing.T) {
		px := obj{"meta": meta, "tiktok": tiktok}
		got := runPixels(t, obj{"pixels": px, "stored": kept(px), "calls": [][]any{{"consent", false}}, "track": "Signed Up"})
		if !reflect.DeepEqual(last(got.Metaq), []any{"consent", "revoke"}) || !reflect.DeepEqual(last(got.Ttq), []any{"revokeConsent"}) {
			t.Fatalf("got %+v", got)
		}
	})
	// A page with the platform's own snippet keeps it, untouched.
	t.Run("the page has its own snippets", func(t *testing.T) {
		px := obj{"meta": meta, "tiktok": tiktok}
		got := runPixels(t, obj{"fbq": true, "ttq": true, "pixels": px, "stored": kept(px), "track": "Signed Up"})
		if len(got.Metaq) != 0 || len(got.Ttq) != 0 || len(got.Scripts) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("data-pixels off", func(t *testing.T) {
		px := obj{"meta": meta, "tiktok": tiktok}
		got := runPixels(t, obj{"pixels": px, "stored": kept(px), "scriptAttrs": obj{"data-pixels": "off"}, "track": "Signed Up"})
		if len(got.Metaq) != 0 || len(got.Ttq) != 0 || len(got.Scripts) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}
