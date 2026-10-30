package flags

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

func split(weights ...int) Flag {
	f := Flag{Key: "hero", Status: "on", Rollout: 100, Salt: "s1"}
	for i, w := range weights {
		f.Variants = append(f.Variants, Variant{Key: fmt.Sprintf("v%d", i), Weight: w})
	}
	return f
}

func TestSplitFollowsWeightsAndSticks(t *testing.T) {
	f := split(20, 30, 50)
	got := map[string]int{}
	const n = 20000
	for i := range n {
		v := Visitor{Subject: fmt.Sprintf("visitor-%d", i)}
		r := Evaluate(f, v)
		if r.Reason != "match" {
			t.Fatalf("visitor %d: %s", i, r.Reason)
		}
		// The same visitor gets the same variant every time.
		if again := Evaluate(f, v); again.Variant != r.Variant {
			t.Fatalf("visitor %d moved from %s to %s", i, r.Variant, again.Variant)
		}
		got[r.Variant]++
	}
	for k, want := range map[string]float64{"v0": 0.2, "v1": 0.3, "v2": 0.5} {
		if share := float64(got[k]) / n; math.Abs(share-want) > 0.02 {
			t.Errorf("%s got %.3f of visitors, want about %.2f", k, share, want)
		}
	}
	// Another salt splits the same visitors independently.
	g := f
	g.Salt = "s2"
	same := 0
	for i := range 2000 {
		v := Visitor{Subject: fmt.Sprintf("visitor-%d", i)}
		if Evaluate(f, v).Variant == Evaluate(g, v).Variant {
			same++
		}
	}
	// Independent 20/30/50 splits agree for 0.04+0.09+0.25 = 38% of visitors.
	if same < 640 || same > 880 {
		t.Errorf("%d of 2000 visitors got the same variant under another salt; the salts are not independent", same)
	}
}

func TestRolloutGrowsWithoutMovingAnyone(t *testing.T) {
	f := split(100)
	in := map[string]bool{}
	for _, pct := range []int{0, 10, 50, 100} {
		f.Rollout = pct
		n := 0
		for i := range 5000 {
			s := fmt.Sprintf("visitor-%d", i)
			on := Evaluate(f, Visitor{Subject: s}).Variant != ""
			if in[s] && !on {
				t.Fatalf("visitor %s fell out when the rollout grew to %d%%", s, pct)
			}
			if on {
				in[s] = true
				n++
			}
		}
		if share := float64(n) / 5000; math.Abs(share-float64(pct)/100) > 0.03 {
			t.Errorf("rollout %d%% let in %.3f", pct, share)
		}
	}
}

func TestOffAndNoSubject(t *testing.T) {
	f := split(50, 50)
	f.Status = "off"
	if r := Evaluate(f, Visitor{Subject: "a"}); r.Variant != "" || r.Reason != "off" {
		t.Fatalf("an off flag gave %+v", r)
	}
	f.Status = "on"
	if r := Evaluate(f, Visitor{}); r.Variant != "" || r.Reason != "no_subject" {
		t.Fatalf("no subject gave %+v", r)
	}
}

func TestRules(t *testing.T) {
	v := Visitor{Subject: "a", URL: "https://www.Northpeak.example/pricing?plan=Team&utm_source=news", Device: "mobile", Language: "en-GB",
		Referrer: "https://search.example/", Properties: map[string]string{"plan": "pro"}}
	for _, c := range []struct {
		rule Rule
		want bool
	}{
		{Rule{"host", "is", []string{"northpeak.example"}}, true},
		{Rule{"host", "is", []string{"app.northpeak.example"}}, false},
		{Rule{"path", "starts_with", []string{"/blog", "/pricing"}}, true},
		{Rule{"path", "is_not", []string{"/pricing"}}, false},
		{Rule{"query.plan", "is", []string{"team"}}, true},
		{Rule{"query.utm_source", "exists", nil}, true},
		{Rule{"query.gclid", "exists", nil}, false},
		{Rule{"query.gclid", "not_exists", nil}, true},
		{Rule{"query.gclid", "is", []string{""}}, false},
		{Rule{"device", "is", []string{"desktop", "tablet"}}, false},
		{Rule{"language", "starts_with", []string{"en"}}, true},
		{Rule{"referrer", "contains", []string{"search.example"}}, true},
		{Rule{"referrer", "not_contains", []string{"search.example"}}, false},
		{Rule{"signed_in", "is", []string{"true"}}, false},
		{Rule{"property.plan", "is", []string{"PRO"}}, true},
		{Rule{"property.seats", "exists", nil}, false},
		{Rule{"url", "ends_with", []string{"news"}}, true},
	} {
		f := split(100)
		f.Rules = []Rule{c.rule}
		if got := Evaluate(f, v).Variant != ""; got != c.want {
			t.Errorf("%s %s %v: got %v, want %v", c.rule.Attribute, c.rule.Op, c.rule.Values, got, c.want)
		}
	}
	// Every rule must hold.
	f := split(100)
	f.Rules = []Rule{{"device", "is", []string{"mobile"}}, {"path", "is", []string{"/about"}}}
	if r := Evaluate(f, v); r.Reason != "rules" {
		t.Fatalf("one failing rule of two still gave %+v", r)
	}
}

func TestPayloadComesWithItsVariant(t *testing.T) {
	f := split(100)
	f.Variants[0].Payload = json.RawMessage(`{"headline":"Ship faster"}`)
	if r := Evaluate(f, Visitor{Subject: "a"}); string(r.Payload) != `{"headline":"Ship faster"}` {
		t.Fatalf("payload %s", r.Payload)
	}
}

func TestChecks(t *testing.T) {
	for _, vs := range [][]Variant{
		nil,
		{{Key: "Control", Weight: 50}},
		{{Key: "a", Weight: 50}, {Key: "a", Weight: 50}},
		{{Key: "a", Weight: 0}},
		{{Key: "a", Weight: 101}},
		{{Key: "a", Weight: 100, Payload: json.RawMessage(`{nope`)}},
	} {
		if CheckVariants(vs) == "" {
			t.Errorf("variants %+v passed", vs)
		}
	}
	if msg := CheckVariants([]Variant{{Key: "control", Weight: 50}, {Key: "new-headline", Weight: 50, Payload: json.RawMessage(`"Hi"`)}}); msg != "" {
		t.Errorf("good variants refused: %s", msg)
	}
	for _, rs := range [][]Rule{
		{{"country", "is", []string{"au"}}},
		{{"path", "like", []string{"/x"}}},
		{{"path", "is", nil}},
		{{"path", "exists", []string{"/x"}}},
		{{"query.bad name", "exists", nil}},
		{{"device", "is", []string{"watch"}}},
	} {
		if CheckRules(rs) == "" {
			t.Errorf("rules %+v passed", rs)
		}
	}
	if msg := CheckRules([]Rule{{"query.utm_source", "is", []string{"news"}}, {"property.plan", "exists", nil}}); msg != "" {
		t.Errorf("good rules refused: %s", msg)
	}
}

// Flags in one exclusion group never both take the same visitor, and each
// takes about its share of the group.
func TestExclusionGroup(t *testing.T) {
	vs := []Variant{{Key: "control", Weight: 50}, {Key: "new", Weight: 50}}
	a := Flag{Key: "hero", Status: "on", Variants: vs, Rollout: 100, Salt: "s1", Group: "homepage", GroupFrom: 0, GroupTo: 3000}
	b := Flag{Key: "cta", Status: "on", Variants: vs, Rollout: 100, Salt: "s2", Group: "homepage", GroupFrom: 3000, GroupTo: 10000}
	inA, inB := 0, 0
	for i := range 20000 {
		v := Visitor{Subject: fmt.Sprintf("visitor-%d", i)}
		ra, rb := Evaluate(a, v), Evaluate(b, v)
		if ra.Variant != "" && rb.Variant != "" {
			t.Fatalf("%s is in both tests", v.Subject)
		}
		if ra.Variant == "" && ra.Reason != "group" || rb.Variant == "" && rb.Reason != "group" {
			t.Fatalf("left out for %s/%s", ra.Reason, rb.Reason)
		}
		if ra.Variant != "" {
			inA++
		} else {
			inB++
		}
	}
	if inA < 5600 || inA > 6400 || inA+inB != 20000 {
		t.Fatalf("hero took %d of 20000, want about 30%%", inA)
	}

	// Slices are handed out from the first gap wide enough.
	if f, to, ok := Allocate([][2]int{{0, 3000}, {5000, 10000}}, 20, Buckets); !ok || f != 3000 || to != 5000 {
		t.Fatalf("gap: %d %d %v", f, to, ok)
	}
	if _, _, ok := Allocate([][2]int{{0, 3000}, {5000, 10000}}, 21, Buckets); ok {
		t.Fatal("a slice wider than any gap was given")
	}
	if f, to, ok := Allocate(nil, 100, Buckets); !ok || f != 0 || to != 10000 {
		t.Fatalf("empty group: %d %d %v", f, to, ok)
	}
	// A 10% holdout leaves 90% for flags.
	if _, _, ok := Allocate([][2]int{{0, 5000}}, 50, 9000); ok {
		t.Fatal("a slice ran into the holdout")
	}
	if f, to, ok := Allocate([][2]int{{0, 5000}}, 40, 9000); !ok || f != 5000 || to != 9000 {
		t.Fatalf("up to the holdout: %d %d %v", f, to, ok)
	}
}

// A version's visual changes are checked: one thing each, safe addresses,
// a selector that can't break out of a style rule.
func TestCheckChanges(t *testing.T) {
	ok := []string{
		`{"headline": "x"}`,
		`{"changes": [{"selector": "h1.hero", "text": "Ship today"}, {"selector": ".old", "hide": true}, {"selector": "a.cta", "href": "/signup"},
			{"selector": "img.hero", "src": "https://cdn.example.com/a.png", "alt": "A team"}]}`,
		`{"changes": []}`,
	}
	for _, p := range ok {
		if msg := CheckChanges(json.RawMessage(p)); msg != "" {
			t.Errorf("%s: %s", p, msg)
		}
	}
	bad := []string{
		`{"changes": "h1"}`,
		`{"changes": [{"selector": "", "text": "x"}]}`,
		`{"changes": [{"selector": "h1", "text": "x", "hide": true}]}`,
		`{"changes": [{"selector": "h1"}]}`,
		`{"changes": [{"selector": "a", "href": "javascript:alert(1)"}]}`,
		`{"changes": [{"selector": "a", "href": "//evil.example"}]}`,
		`{"changes": [{"selector": "img", "src": "data:text/html,x"}]}`,
		`{"changes": [{"selector": "h1 } body { color: red", "text": "x"}]}`,
	}
	for _, p := range bad {
		if CheckChanges(json.RawMessage(p)) == "" {
			t.Errorf("%s passed", p)
		}
	}
	if msg := CheckVariants([]Variant{{Key: "control", Weight: 50}, {Key: "new", Weight: 50, Payload: json.RawMessage(`{"changes": [{"selector": "a", "href": "javascript:x"}]}`)}}); msg == "" {
		t.Error("CheckVariants let a javascript: link through")
	}
}
