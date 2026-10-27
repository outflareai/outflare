package manifest

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/integrations"
)

// TestIntegrationsFolder is what CI runs on a pull request that adds or
// changes a manifest: it loads, it has a logo and its recorded tests pass.
func TestIntegrationsFolder(t *testing.T) {
	conns, err := Load(integrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) == 0 {
		t.Fatal("no manifests found")
	}
	dirs, _ := fs.Glob(integrations.FS, "connectors/*")
	if len(dirs) != len(conns) {
		t.Errorf("%d folders but %d manifests: every folder needs a manifest.yaml", len(dirs), len(conns))
	}
	for _, x := range conns {
		id := x.Definition().ID
		t.Run(id, func(t *testing.T) {
			dir := path.Join("connectors", id)
			if _, err := fs.Stat(integrations.FS, dir+"/logo.svg"); err != nil {
				t.Error("needs a logo.svg")
			}
			entries, _ := fs.ReadDir(integrations.FS, dir)
			for _, e := range entries {
				switch e.Name() {
				case "manifest.yaml", "tests.yaml", "logo.svg", "logo.dark.svg", "README.md":
				default:
					t.Errorf("unexpected file %s", e.Name())
				}
			}
			b, err := fs.ReadFile(integrations.FS, dir+"/tests.yaml")
			if err != nil {
				t.Fatal("needs a tests.yaml")
			}
			cases, err := ParseCases(b)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range RunCases(context.Background(), x, cases) {
				t.Error(p)
			}
		})
	}
}

func TestCatalogEntries(t *testing.T) {
	conns, err := Load(integrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range conns {
		d := x.Definition()
		if d.ID == "bouncer" {
			if d.Trust != c.TrustOutflare || d.Publisher == nil || d.Publisher.Name != "Outflare" {
				t.Errorf("bouncer is Outflare's own: %+v %+v", d.Trust, d.Publisher)
			}
			if !d.Has(c.CapVerify) || d.Auth != c.AuthAPIKey || len(d.Fields) != 1 || !d.Fields[0].Secret {
				t.Errorf("definition: %+v", d)
			}
			if _, ok := any(x).(c.Verifier); !ok {
				t.Error("a verify manifest is a connectors.Verifier")
			}
		}
	}
}

const good = `
id: acmeverify
name: AcmeVerify
category: email_verification
summary: Checks addresses.
website: https://acme.example
publisher: { id: acme, name: Acme, website: https://acme.example }
base_url: https://api.acme.example
auth:
  api_key:
    fields: [{ name: api_key, label: API key, secret: true }]
test:
  request: { method: GET, path: /v1/account, headers: { Authorization: "Bearer {{api_key}}" } }
  account: { name: workspace, credits: credits }
capabilities:
  verify:
    request: { method: GET, path: "/v1/verify/{{email}}", headers: { Authorization: "Bearer {{api_key}}" } }
    result:
      status: { from: result, values: { valid: deliverable, invalid: undeliverable }, default: unknown }
`

var pubs = map[string]PublisherEntry{"outflare": {Name: "Outflare", Website: "https://outflare.ai", Trust: c.TrustOutflare}}

func build(t *testing.T, src string) (*Connector, error) {
	t.Helper()
	m, err := Parse([]byte(src))
	if err != nil {
		return nil, err
	}
	return New(m, pubs)
}

func TestCommunityPublisher(t *testing.T) {
	x, err := build(t, good)
	if err != nil {
		t.Fatal(err)
	}
	d := x.Definition()
	if d.Trust != c.TrustCommunity || d.Publisher.Name != "Acme" {
		t.Errorf("an unlisted publisher is Community: %+v %+v", d.Trust, d.Publisher)
	}
}

func TestRefused(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"plain http":           {"base_url: https://api.acme.example", "base_url: http://api.acme.example", "base_url"},
		"an address":           {"base_url: https://api.acme.example", "base_url: https://10.0.0.1", "public host"},
		"localhost":            {"base_url: https://api.acme.example", "base_url: https://localhost", "public host"},
		"a port":               {"base_url: https://api.acme.example", "base_url: https://api.acme.example:8443", "plain https"},
		"another host":         {"path: /v1/account", "path: //evil.example/v1", "stay on the base URL"},
		"dot dot":              {"path: /v1/account", "path: /v1/../../x", "stay on the base URL"},
		"unknown variable":     {"Bearer {{api_key}}\" } }\n  account", "Bearer {{password}}\" } }\n  account", "unknown variable {{password}}"},
		"unknown field":        {"category: email_verification", "category: email_verification\nscript: rm -rf", "field script not found"},
		"a test that writes":   {"request: { method: GET, path: /v1/account", "request: { method: POST, path: /v1/account", "must be a GET"},
		"a host header":        {"headers: { Authorization: \"Bearer {{api_key}}\" } }\n  account", "headers: { Host: evil.example } }\n  account", "cannot be set"},
		"borrowed name":        {"name: Acme, website", "name: Outflare, website", "belongs to a listed publisher"},
		"no capability":        {"capabilities:\n  verify:", "capabilities:\n  other:", "not found"},
		"bad status":           {"invalid: undeliverable", "invalid: bounced", "not deliverable"},
		"unsupported category": {"category: email_verification", "category: crm", "not supported"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(good, tc.from, tc.to, 1)
			if src == good {
				t.Fatalf("replacement %q did not apply", tc.from)
			}
			_, err := build(t, src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error with %q, got %v", tc.want, err)
			}
		})
	}
}

func TestOutflareNeedsTheList(t *testing.T) {
	// Claiming publisher id outflare gets Outflare's name and trust only
	// because publishers.yaml lists it; with an empty list it is refused.
	src := strings.Replace(good, "publisher: { id: acme, name: Acme, website: https://acme.example }", "publisher: { id: outflare }", 1)
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(m, map[string]PublisherEntry{}); err == nil {
		t.Error("an unlisted publisher must name itself")
	}
}

func TestValuesAreEscaped(t *testing.T) {
	x, err := build(t, good)
	if err != nil {
		t.Fatal(err)
	}
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.EscapedPath(), r.Header.Get("Authorization")
		w.Write([]byte(`{"result":"valid"}`))
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"acmeverify": srv.URL}}
	v, err := x.Verify(context.Background(), cl, c.Creds{"api_key": "k"}, "a/../b@acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/verify/a%2F..%2Fb@acme.example" || gotAuth != "Bearer k" || v.Status != c.Deliverable {
		t.Errorf("path %q auth %q status %q", gotPath, gotAuth, v.Status)
	}
	if _, err := x.Verify(context.Background(), cl, c.Creds{"api_key": "k\r\nX-Evil: 1"}, "a@acme.example"); c.KindOf(err) != c.ErrInvalid {
		t.Errorf("a line break in a header value is refused: %v", err)
	}
}

func TestLookup(t *testing.T) {
	ans := map[string]any{"a": map[string]any{"b": []any{"x", map[string]any{"c": true}}}}
	if lookup(ans, "a.b.1.c") != true || lookup(ans, "a.b.5") != nil || lookup(ans, "a.z.c") != nil || lookup(ans, "") != nil {
		t.Error("lookup")
	}
	for v, want := range map[any]bool{"yes": true, "1": true, true: true, "no": false, false: false, nil: false} {
		if truthy(v) != want {
			t.Errorf("truthy(%v)", v)
		}
	}
}
