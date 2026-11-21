package flags

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// vector is one decision the server libraries must repeat exactly: the
// TypeScript and Python libraries decide flags in their own memory from
// the flags' definitions, and their tests read testdata/vectors.json and
// must give the same variant and reason as Evaluate does here.
type vector struct {
	Name    string `json:"name"`
	Flag    vflag  `json:"flag"`
	Visitor vvis   `json:"visitor"`
	Variant string `json:"variant"`
	Reason  string `json:"reason"`
}

type vflag struct {
	Key       string    `json:"key"`
	Variants  []Variant `json:"variants"`
	Rollout   int       `json:"rollout"`
	Rules     []Rule    `json:"rules"`
	Salt      string    `json:"salt"`
	Group     string    `json:"group"`
	GroupFrom int       `json:"group_from"`
	GroupTo   int       `json:"group_to"`
}

type vvis struct {
	Subject    string            `json:"subject"`
	SignedIn   bool              `json:"signed_in"`
	URL        string            `json:"url"`
	Referrer   string            `json:"referrer"`
	Device     string            `json:"device"`
	Language   string            `json:"language"`
	Properties map[string]string `json:"properties"`
	UserID     string            `json:"user_id"`
	Lists      []string          `json:"lists"`
	Person     map[string]string `json:"person"`
}

func buildVectors() []vector {
	split := []Variant{{Key: "control", Weight: 50}, {Key: "faster", Weight: 30, Payload: json.RawMessage(`{"headline":"Ship faster"}`)}, {Key: "calmer", Weight: 20}}
	on := []Variant{{Key: "on", Weight: 100}}
	var out []vector
	add := func(name string, f vflag, v vvis) {
		if f.Rules == nil {
			f.Rules = []Rule{}
		}
		if v.Properties == nil {
			v.Properties = map[string]string{}
		}
		if v.Lists == nil {
			v.Lists = []string{}
		}
		if v.Person == nil {
			v.Person = map[string]string{}
		}
		res := Evaluate(Flag{Key: f.Key, Status: "on", Variants: f.Variants, Rollout: f.Rollout, Rules: f.Rules, Salt: f.Salt,
			Group: f.Group, GroupFrom: f.GroupFrom, GroupTo: f.GroupTo},
			Visitor{Subject: v.Subject, SignedIn: v.SignedIn, URL: v.URL, Referrer: v.Referrer, Device: v.Device, Language: v.Language, Properties: v.Properties,
				UserID: v.UserID, Lists: v.Lists, Person: v.Person})
		out = append(out, vector{Name: name, Flag: f, Visitor: v, Variant: res.Variant, Reason: res.Reason})
	}
	// Splits, rollouts and exclusion groups over many visitors, so every
	// boundary of the hash is crossed.
	for i := range 60 {
		id := fmt.Sprintf("visitor-%03d", i)
		add("split "+id, vflag{Key: "pricing-headline", Variants: split, Rollout: 100, Salt: "s4lt-a"}, vvis{Subject: id})
		add("rollout "+id, vflag{Key: "new-nav", Variants: on, Rollout: 35, Salt: "s4lt-b"}, vvis{Subject: id})
		add("group "+id, vflag{Key: "hero-test", Variants: split, Rollout: 100, Salt: "s4lt-c", Group: "homepage", GroupFrom: 2500, GroupTo: 7500}, vvis{Subject: id})
	}
	add("uneven weights", vflag{Key: "w", Variants: []Variant{{Key: "a", Weight: 1}, {Key: "b", Weight: 2}}, Rollout: 100, Salt: "s"}, vvis{Subject: "7f0c9a52-9d1e-4c58-8a53-0d4f6f1c2b11"})
	add("no weight", vflag{Key: "w0", Variants: []Variant{{Key: "a", Weight: 0}}, Rollout: 100, Salt: "s"}, vvis{Subject: "v"})
	add("no subject", vflag{Key: "n", Variants: on, Rollout: 100, Salt: "s"}, vvis{})
	add("unicode subject", vflag{Key: "u", Variants: split, Rollout: 100, Salt: "s"}, vvis{Subject: "usuário-ñ-日本"})

	page := vvis{Subject: "visitor-001", URL: "https://www.Northpeak.example/Pricing/Team%20plan?utm_source=News&plan=pro&empty=", Referrer: "https://search.example/results",
		Device: "mobile", Language: "en-GB", Properties: map[string]string{"plan": "Pro", "seats": "12"}}
	rule := func(name string, r Rule, v vvis) {
		add("rule "+name, vflag{Key: "r", Variants: on, Rollout: 100, Rules: []Rule{r}, Salt: "s"}, v)
	}
	rule("host without www", Rule{Attribute: "host", Op: "is", Values: []string{"northpeak.example"}}, page)
	rule("host is_not", Rule{Attribute: "host", Op: "is_not", Values: []string{"northpeak.example"}}, page)
	rule("path keeps escapes and case", Rule{Attribute: "path", Op: "is", Values: []string{"/pricing/team%20plan"}}, page)
	rule("path starts_with", Rule{Attribute: "path", Op: "starts_with", Values: []string{"/docs", "/pricing"}}, page)
	rule("path ends_with miss", Rule{Attribute: "path", Op: "ends_with", Values: []string{"/team"}}, page)
	rule("url contains", Rule{Attribute: "url", Op: "contains", Values: []string{"UTM_SOURCE=news"}}, page)
	rule("url not_contains", Rule{Attribute: "url", Op: "not_contains", Values: []string{"checkout"}}, page)
	rule("query is", Rule{Attribute: "query.utm_source", Op: "is", Values: []string{"news", "ads"}}, page)
	rule("query exists empty", Rule{Attribute: "query.empty", Op: "exists"}, page)
	rule("query not_exists", Rule{Attribute: "query.missing", Op: "not_exists"}, page)
	rule("query missing is", Rule{Attribute: "query.missing", Op: "is", Values: []string{""}}, page)
	rule("query missing is_not", Rule{Attribute: "query.missing", Op: "is_not", Values: []string{"x"}}, page)
	rule("referrer contains", Rule{Attribute: "referrer", Op: "contains", Values: []string{"search.example"}}, page)
	rule("device is", Rule{Attribute: "device", Op: "is", Values: []string{"mobile", "tablet"}}, page)
	rule("device miss", Rule{Attribute: "device", Op: "is", Values: []string{"desktop"}}, page)
	rule("language starts_with", Rule{Attribute: "language", Op: "starts_with", Values: []string{"en"}}, page)
	rule("signed_in false", Rule{Attribute: "signed_in", Op: "is", Values: []string{"true"}}, page)
	signed := page
	signed.SignedIn = true
	rule("signed_in true", Rule{Attribute: "signed_in", Op: "is", Values: []string{"true"}}, signed)
	rule("signed_in exists", Rule{Attribute: "signed_in", Op: "exists"}, vvis{Subject: "v"})
	rule("property is", Rule{Attribute: "property.plan", Op: "is", Values: []string{"pro"}}, page)
	rule("property missing exists", Rule{Attribute: "property.team", Op: "exists"}, page)
	rule("property missing not_contains", Rule{Attribute: "property.team", Op: "not_contains", Values: []string{"x"}}, page)
	rule("property ends_with", Rule{Attribute: "property.seats", Op: "ends_with", Values: []string{"2"}}, page)
	rule("no url host exists", Rule{Attribute: "host", Op: "exists"}, vvis{Subject: "v"})
	rule("no url path not_exists", Rule{Attribute: "path", Op: "not_exists"}, vvis{Subject: "v"})
	rule("empty referrer exists", Rule{Attribute: "referrer", Op: "exists"}, vvis{Subject: "v", URL: "https://northpeak.example/"})
	rule("root path", Rule{Attribute: "path", Op: "is", Values: []string{"/"}}, vvis{Subject: "v", URL: "https://northpeak.example/"})
	rule("no path", Rule{Attribute: "path", Op: "exists"}, vvis{Subject: "v", URL: "https://northpeak.example"})
	rule("host with port", Rule{Attribute: "host", Op: "is", Values: []string{"localhost"}}, vvis{Subject: "v", URL: "http://localhost:3000/a"})
	rule("repeated query takes first", Rule{Attribute: "query.a", Op: "is", Values: []string{"1"}}, vvis{Subject: "v", URL: "https://northpeak.example/?a=1&a=2"})
	rule("query plus is space", Rule{Attribute: "query.q", Op: "is", Values: []string{"a b"}}, vvis{Subject: "v", URL: "https://northpeak.example/?q=a+b"})
	rule("path with a space and letters outside ASCII", Rule{Attribute: "path", Op: "is", Values: []string{"/a%20b/%C3%BC"}}, vvis{Subject: "v", URL: "https://northpeak.example/a b/ü"})
	rule("path re-encoded loses an escaped slash", Rule{Attribute: "path", Op: "is", Values: []string{"/a/b%20c"}}, vvis{Subject: "v", URL: "https://northpeak.example/a%2fb c"})
	rule("path bad escape", Rule{Attribute: "path", Op: "exists"}, vvis{Subject: "v", URL: "https://northpeak.example/a%zz"})
	rule("relative url has a path", Rule{Attribute: "path", Op: "is", Values: []string{"/pricing"}}, vvis{Subject: "v", URL: "/pricing?x=1"})
	rule("relative url has no host", Rule{Attribute: "host", Op: "exists"}, vvis{Subject: "v", URL: "/pricing?x=1"})
	rule("host with sign-in and port", Rule{Attribute: "host", Op: "is", Values: []string{"northpeak.example"}}, vvis{Subject: "v", URL: "https://ana:pw@WWW.northpeak.example:8443/x#frag?not=query"})
	rule("fragment is not query", Rule{Attribute: "query.not", Op: "exists"}, vvis{Subject: "v", URL: "https://northpeak.example/x#frag?not=query"})
	rule("ipv6 host", Rule{Attribute: "host", Op: "is", Values: []string{"::1"}}, vvis{Subject: "v", URL: "http://[::1]:8080/"})
	rule("query bad escape is dropped", Rule{Attribute: "query.a", Op: "exists"}, vvis{Subject: "v", URL: "https://northpeak.example/?a=%zz&b=1"})
	rule("query semicolon pair is dropped", Rule{Attribute: "query.b", Op: "exists"}, vvis{Subject: "v", URL: "https://northpeak.example/?a=1;b=2"})
	rule("query key is decoded", Rule{Attribute: "query.a b", Op: "is", Values: []string{"é"}}, vvis{Subject: "v", URL: "https://northpeak.example/?a%20b=%C3%A9"})
	rule("query without value", Rule{Attribute: "query.flag", Op: "is", Values: []string{""}}, vvis{Subject: "v", URL: "https://northpeak.example/?flag"})
	add("two rules, second fails", vflag{Key: "r2", Variants: on, Rollout: 100, Salt: "s", Rules: []Rule{
		{Attribute: "device", Op: "is", Values: []string{"mobile"}}, {Attribute: "language", Op: "is", Values: []string{"de"}}}}, page)
	// Who the visitor is: your id for them, the People lists they are in
	// and their record's fields.
	const pilots, staff = "0b9f6c1e-5a55-4f0e-9d6f-0d2c7a1b3e44", "6d1a2f3b-7c88-4e21-b5a0-9f3e2d1c0b77"
	known := vvis{Subject: "visitor-001", UserID: "U-42", Lists: []string{strings.ToUpper(pilots)}, Person: map[string]string{"plan": "Pro", "seats": "12", "note": ""}}
	stranger := vvis{Subject: "visitor-002"}
	for _, who := range []struct {
		name string
		v    vvis
	}{{"known", known}, {"stranger", stranger}} {
		rule("list is "+who.name, Rule{Attribute: "list", Op: "is", Values: []string{pilots}}, who.v)
		rule("list is any of "+who.name, Rule{Attribute: "list", Op: "is", Values: []string{staff, pilots}}, who.v)
		rule("list is another "+who.name, Rule{Attribute: "list", Op: "is", Values: []string{staff}}, who.v)
		rule("list is_not "+who.name, Rule{Attribute: "list", Op: "is_not", Values: []string{pilots}}, who.v)
		rule("user_id is "+who.name, Rule{Attribute: "user_id", Op: "is", Values: []string{"u-7", "u-42"}}, who.v)
		rule("user_id is_not "+who.name, Rule{Attribute: "user_id", Op: "is_not", Values: []string{"u-42"}}, who.v)
		rule("user_id exists "+who.name, Rule{Attribute: "user_id", Op: "exists"}, who.v)
		rule("person is "+who.name, Rule{Attribute: "person.plan", Op: "is", Values: []string{"pro"}}, who.v)
		rule("person is_not "+who.name, Rule{Attribute: "person.plan", Op: "is_not", Values: []string{"free"}}, who.v)
		rule("person starts_with "+who.name, Rule{Attribute: "person.seats", Op: "starts_with", Values: []string{"1"}}, who.v)
		rule("person exists "+who.name, Rule{Attribute: "person.plan", Op: "exists"}, who.v)
		rule("person empty exists "+who.name, Rule{Attribute: "person.note", Op: "exists"}, who.v)
		rule("person not_exists "+who.name, Rule{Attribute: "person.team", Op: "not_exists"}, who.v)
	}
	return out
}

const vectorsFile = "testdata/vectors.json"

// TestVectors keeps testdata/vectors.json, which the server libraries'
// tests read, equal to what Evaluate decides. After changing how flags are
// decided, run UPDATE_VECTORS=1 go test ./engine/internal/flags/ and then
// the libraries' tests: they must change in step.
func TestVectors(t *testing.T) {
	want, err := json.MarshalIndent(buildVectors(), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_VECTORS") != "" {
		if err := os.WriteFile(vectorsFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is out of date: run UPDATE_VECTORS=1 go test ./engine/internal/flags/, then the server libraries' tests (sdk/ts, sdk/python)", vectorsFile)
	}
	reasons := map[string]int{}
	for _, v := range buildVectors() {
		reasons[v.Reason]++
	}
	for _, r := range []string{"match", "rules", "group", "rollout", "off", "no_subject"} {
		if reasons[r] == 0 {
			t.Errorf("no vector ends in %q", r)
		}
	}
}
