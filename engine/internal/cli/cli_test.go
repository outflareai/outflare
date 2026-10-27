package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	spec "github.com/outflareai/outflare/api"
)

const ws = "11111111-1111-4111-8111-111111111111"

type seen struct {
	method, path, query, body string
	header                    http.Header
}

// fakeAPI answers with handle and records each request.
func fakeAPI(t *testing.T, handle func(r *http.Request, n int) (int, string)) (*httptest.Server, *[]seen) {
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		n := len(got)
		mu.Unlock()
		status, body := handle(r, n)
		w.Header().Set("X-Request-Id", "req_9")
		w.WriteHeader(status)
		io.WriteString(w, body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func run(t *testing.T, srv *httptest.Server, dir string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Spec: spec.OpenAPI, Version: "test", ConfigDir: dir, RetryBase: time.Millisecond,
		Getenv: func(k string) string {
			switch k {
			case "OUTFLARE_URL":
				if srv != nil {
					return srv.URL
				}
			case "OUTFLARE_TOKEN":
				return "oft_test"
			case "OUTFLARE_WORKSPACE":
				return ws
			}
			return ""
		},
	}
	code := Run(context.Background(), env, args)
	return code, out.String(), errb.String()
}

func TestEveryOperationIsACommand(t *testing.T) {
	doc, err := parseDocument(spec.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, op := range doc.ops {
		if ids[op.Command] {
			t.Errorf("two operations are both %q", op.Command)
		}
		ids[op.Command] = true
		for _, name := range []string{"login", "logout", "whoami", "api", "listen", "help", "serve", "migrate", "version"} {
			if op.Command == name && op.ID != "login" && op.ID != "logout" && op.ID != "whoami" {
				t.Errorf("%s (%s) is shadowed by the built-in command", op.Command, op.ID)
			}
		}
		for _, p := range op.Params {
			if op.Access == nil {
				continue // public pages (unsubscribe links) are not for the client
			}
			for _, g := range globalFlags {
				if kebab(p.Name) == g {
					t.Errorf("%s: parameter %s clashes with the global --%s", op.Command, p.Name, g)
				}
			}
		}
	}
	if len(doc.ops) < 250 {
		t.Fatalf("only %d operations", len(doc.ops))
	}
	if got := kebab("listAPITokens"); got != "list-api-tokens" {
		t.Errorf("kebab: %s", got)
	}
}

func TestFlagsBecomeParametersAndBody(t *testing.T) {
	srv, got := fakeAPI(t, func(*http.Request, int) (int, string) { return 201, `{"id":"l1"}` })
	code, out, errs := run(t, srv, t.TempDir(), "", "upsert-lead", "--email", "ana@acme-com.example", "--first-name=Ana", "--custom", `{"tier":"a"}`)
	if code != 0 || !strings.Contains(out, `"l1"`) {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
	r := (*got)[0]
	if r.method != "POST" || r.path != "/v1/workspaces/"+ws+"/leads" || r.header.Get("Authorization") != "Bearer oft_test" {
		t.Fatalf("request %+v", r)
	}
	var body map[string]any
	json.Unmarshal([]byte(r.body), &body) //nolint:errcheck
	if body["email"] != "ana@acme-com.example" || body["first_name"] != "Ana" || body["custom"].(map[string]any)["tier"] != "a" {
		t.Fatalf("body %s", r.body)
	}
	if len(r.header.Get("Idempotency-Key")) != 36 {
		t.Fatalf("a write gets an idempotency key: %q", r.header.Get("Idempotency-Key"))
	}

	// Query parameters, and a typo gets a suggestion.
	run(t, srv, t.TempDir(), "", "list-leads", "--q", "ana", "--limit", "5")
	if q := (*got)[1].query; !strings.Contains(q, "q=ana") || !strings.Contains(q, "limit=5") {
		t.Fatalf("query %q", q)
	}
	if code, _, errs := run(t, srv, t.TempDir(), "", "list-lead"); code != 2 || !strings.Contains(errs, "list-leads") {
		t.Fatalf("typo: %d %q", code, errs)
	}
	if code, _, errs := run(t, srv, t.TempDir(), "", "upsert-lead", "--emial", "x"); code != 2 || !strings.Contains(errs, "--emial") {
		t.Fatalf("unknown flag: %d %q", code, errs)
	}
	if code, _, errs := run(t, srv, t.TempDir(), "", "create-webhook"); code != 2 || !strings.Contains(errs, "--url") {
		t.Fatalf("missing required: %d %q", code, errs)
	}
}

func TestRetriesWritesWithTheSameKey(t *testing.T) {
	srv, got := fakeAPI(t, func(_ *http.Request, n int) (int, string) {
		if n < 3 {
			return 503, `{"error":{"code":"unavailable","message":"busy"}}`
		}
		return 201, `{"id":"l1"}`
	})
	code, _, errs := run(t, srv, t.TempDir(), "", "upsert-lead", "--email", "ana@acme-com.example")
	if code != 0 || len(*got) != 3 {
		t.Fatalf("code %d, %d tries, %s", code, len(*got), errs)
	}
	if k := (*got)[0].header.Get("Idempotency-Key"); k == "" || (*got)[2].header.Get("Idempotency-Key") != k {
		t.Fatal("retries must carry the same Idempotency-Key")
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times = append(times, time.Now())
		if len(times) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(`{"data":[]}`)) //nolint:errcheck
	}))
	defer srv.Close()
	if code, _, errs := run(t, srv, t.TempDir(), "", "list-leads"); code != 0 || len(times) != 2 {
		t.Fatalf("code %d, %d tries, %s", code, len(times), errs)
	}
	if gap := times[1].Sub(times[0]); gap < 900*time.Millisecond {
		t.Fatalf("retried after %v, not the 1s Retry-After", gap)
	}
}

func TestErrorsAndHeldWrites(t *testing.T) {
	srv, _ := fakeAPI(t, func(r *http.Request, _ int) (int, string) {
		if r.Method == "DELETE" {
			return 404, `{"error":{"code":"not_found","message":"lead not found","request_id":"req_9"}}`
		}
		return 202, `{"mission_action":{"id":"a1"},"dry_run":false,"approval_required":true,"message":"held"}`
	})
	code, _, errs := run(t, srv, t.TempDir(), "", "delete-lead", "--id", "33333333-3333-4333-8333-333333333333")
	if code != 1 || !strings.Contains(errs, "not_found: lead not found (request req_9)") {
		t.Fatalf("error: %d %q", code, errs)
	}
	code, _, errs = run(t, srv, t.TempDir(), "", "upsert-lead", "--email", "a@b-co.example", "--mission", "m1")
	if code != 0 || !strings.Contains(errs, "Held by the mission") {
		t.Fatalf("held: %d %q", code, errs)
	}
}

func TestAllFollowsPages(t *testing.T) {
	srv, got := fakeAPI(t, func(r *http.Request, _ int) (int, string) {
		if r.URL.Query().Get("before") == "" {
			return 200, `{"data":[{"id":3},{"id":2}],"next_before":2}`
		}
		return 200, `{"data":[{"id":1}],"next_before":null}`
	})
	code, out, _ := run(t, srv, t.TempDir(), "", "list-audit-events", "--all", "--limit", "2")
	if code != 0 || strings.TrimSpace(out) != `{"data":[{"id":3},{"id":2},{"id":1}]}` || !strings.Contains((*got)[1].query, "before=2") {
		t.Fatalf("all: %d %s %v", code, out, *got)
	}
}

func TestCurlAndLogin(t *testing.T) {
	_, out, _ := run(t, &httptest.Server{URL: "https://gtm.example"}, t.TempDir(), "", "list-leads", "--q", "ana", "--curl")
	if !strings.Contains(out, "curl -X GET 'https://gtm.example/v1/workspaces/"+ws+"/leads?q=ana'") || !strings.Contains(out, "$OUTFLARE_TOKEN") {
		t.Fatalf("curl %s", out)
	}

	srv, _ := fakeAPI(t, func(r *http.Request, _ int) (int, string) {
		if r.Header.Get("Authorization") != "Bearer oft_new" {
			return 401, `{"error":{"code":"unauthenticated","message":"bad token"}}`
		}
		return 200, `{"actor":{},"token":{"name":"laptop","kind":"workspace","workspace_id":"` + ws + `","scopes":["leads.read"]},"user":null}`
	})
	dir := t.TempDir()
	var errb bytes.Buffer
	env := Env{Stdin: strings.NewReader("oft_new\n"), Stdout: io.Discard, Stderr: &errb, Spec: spec.OpenAPI, ConfigDir: dir, Getenv: func(string) string { return "" }}
	if code := Run(context.Background(), env, []string{"login", "--url", srv.URL + "/"}); code != 0 {
		t.Fatalf("login: %s", errb.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	st, _ := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil || !strings.Contains(string(b), ws) || st.Mode().Perm() != 0o600 {
		t.Fatalf("saved %s %v", b, st.Mode())
	}
	env.Stdin = strings.NewReader("oft_wrong\n")
	if code := Run(context.Background(), env, []string{"logout"}); code != 0 {
		t.Fatal("logout")
	}
	if code := Run(context.Background(), env, []string{"login", "--url", srv.URL}); code != 1 {
		t.Fatal("a refused token must not log in")
	}
}
