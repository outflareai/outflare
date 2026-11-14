package googleworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// usersFake answers the token, signing, Directory and Gmail calls a profile
// change makes. All values are fictional. Nothing here calls Google.
type usersFake struct {
	mu        sync.Mutex
	srv       *httptest.Server
	scopes    []string // scope of each assertion signed
	subjects  []string
	calls     []string
	bodies    map[string]map[string]any
	customer  string
	failName  bool
	denyGmail bool
}

func newUsersFake(t *testing.T) *usersFake {
	f := &usersFake{customer: "Ctest123", bodies: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/token":
			form, _ := url.ParseQuery(string(raw))
			if form.Get("grant_type") == "refresh_token" {
				fmt.Fprintf(w, `{"access_token":"test-only-signer-token","expires_in":3600,"scope":%q}`, iamScope)
				return
			}
			sub := f.subjects[len(f.subjects)-1]
			if f.denyGmail && strings.Contains(f.scopes[len(f.scopes)-1], "gmail") {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"unauthorized_client"}`))
				return
			}
			fmt.Fprintf(w, `{"access_token":"test-only-delegated-%s","expires_in":3600}`, sub)
		case strings.HasSuffix(r.URL.Path, ":signJwt"):
			var body struct{ Payload string }
			_ = json.Unmarshal(raw, &body)
			var claims map[string]any
			_ = json.Unmarshal([]byte(body.Payload), &claims)
			f.scopes = append(f.scopes, claims["scope"].(string))
			f.subjects = append(f.subjects, claims["sub"].(string))
			w.Write([]byte(`{"signedJwt":"test-only.signed.1"}`))
		default:
			f.calls = append(f.calls, r.Method+" "+r.URL.Path)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			f.bodies[r.Method+" "+r.URL.Path] = b
			switch {
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/directory/v1/users/"):
				fmt.Fprintf(w, `{"customerId":%q}`, f.customer)
			case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/admin/directory/v1/users/") && f.failName:
				w.WriteHeader(500)
			default:
				w.Write([]byte(`{}`))
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *usersFake) client() *c.Client {
	return &c.Client{BaseURLs: map[string]string{ID: f.srv.URL, ID + ".token": f.srv.URL + "/token", ID + ".iam": f.srv.URL + "/iam", ID + ".gmail": f.srv.URL}}
}

func str(s string) *string { return &s }

func TestUpdateWorkspaceUser(t *testing.T) {
	resetCache(t)
	ctx := context.Background()
	f := newUsersFake(t)
	cr := keylessCreds("test-only-refresh-users")
	upd := c.WorkspaceUserUpdate{Email: "Ana@Northpeak.example", GivenName: str("Ana"), FamilyName: str("Lee"), Photo: []byte("test-only-photo"), PhotoType: "PNG", Signature: str("<b>Ana</b>")}

	// Read-only connections never change anything.
	if _, err := (Connector{}).UpdateWorkspaceUser(ctx, f.client(), cr, upd); codeOf(err) != c.CodeWorkspaceWritesOff || len(f.calls) != 0 {
		t.Fatalf("read-only connection: %v, calls %v", err, f.calls)
	}
	bad := keylessCreds("x")
	bad["manage_users"] = "please"
	if _, err := checkCreds(bad); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("manage_users other than yes: %v", err)
	}

	cr["manage_users"] = "yes"
	res, err := (Connector{}).UpdateWorkspaceUser(ctx, f.client(), cr, upd)
	if err != nil || res.Name != PartUpdated || res.Photo != PartUpdated || res.Signature != PartUpdated {
		t.Fatalf("update %+v: %v", res, err)
	}
	want := []string{
		"GET /admin/directory/v1/users/ana@northpeak.example",
		"PATCH /admin/directory/v1/users/ana@northpeak.example",
		"PUT /admin/directory/v1/users/ana@northpeak.example/photos/thumbnail",
		"PATCH /gmail/v1/users/me/settings/sendAs/ana@northpeak.example",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
	// Names and photos use the administrator with the user scope only; the
	// signature uses the mailbox's own delegated token with Gmail settings only.
	if f.subjects[0] != testAdmin || f.scopes[0] != userScope || f.subjects[1] != "ana@northpeak.example" || f.scopes[1] != signatureScope {
		t.Fatalf("subjects %v scopes %v", f.subjects, f.scopes)
	}
	if n := f.bodies[want[1]]["name"].(map[string]any); n["givenName"] != "Ana" || n["familyName"] != "Lee" {
		t.Fatalf("name body %v", f.bodies[want[1]])
	}
	if f.bodies[want[2]]["mimeType"] != "PNG" || f.bodies[want[3]]["signature"] != "<b>Ana</b>" {
		t.Fatal("photo or signature body")
	}

	// A user of another tenant is never changed.
	f.calls, f.customer = nil, "Cother999"
	if _, err = (Connector{}).UpdateWorkspaceUser(ctx, f.client(), cr, c.WorkspaceUserUpdate{Email: "bo@northpeak.example", GivenName: str("Bo")}); c.KindOf(err) != c.ErrInvalid || len(f.calls) != 1 {
		t.Fatalf("other tenant: %v, calls %v", err, f.calls)
	}

	// One part failing doesn't hide the others.
	f.customer, f.failName, f.denyGmail = "Ctest123", true, true
	res, err = (Connector{}).UpdateWorkspaceUser(ctx, f.client(), cr, c.WorkspaceUserUpdate{Email: "cy@northpeak.example", GivenName: str("Cy"), Photo: []byte("p"), PhotoType: "JPEG", Signature: str("Cy")})
	if err != nil || res.Name != PartFailed || res.Photo != PartUpdated || res.Signature != PartFailed {
		t.Fatalf("partial %+v: %v", res, err)
	}

	for _, u := range []c.WorkspaceUserUpdate{
		{Email: "not-an-address"},
		{Email: "dee@northpeak.example", GivenName: str("")},
		{Email: "dee@northpeak.example", Photo: []byte("p"), PhotoType: "GIF"},
		{Email: "dee@northpeak.example", Signature: str(strings.Repeat("x", maxSignature+1))},
	} {
		if _, err = (Connector{}).UpdateWorkspaceUser(ctx, f.client(), cr, u); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("invalid %+v: %v", u, err)
		}
	}
}
