package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Case is one entry of a connector's tests.yaml: an answer the service
// sends, recorded with fake data, and what Outflare should make of it.
type Case struct {
	Name string `yaml:"name"`
	// Run: test, or a capability (verify).
	Run   string            `yaml:"run"`
	Creds map[string]string `yaml:"creds"`
	Input map[string]string `yaml:"input"`
	// Request is what the service should receive: method and path, and
	// the query, headers and body values listed (others are not checked).
	Request ExpectRequest `yaml:"request"`
	Answer  Answer        `yaml:"answer"`
	// Expect lists result fields to compare; ExpectError an error kind.
	Expect      map[string]any `yaml:"expect"`
	ExpectError string         `yaml:"expect_error"`
}

// ExpectRequest is the request a case expects.
type ExpectRequest struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Query   map[string]string `yaml:"query"`
	Headers map[string]string `yaml:"headers"`
	Body    map[string]any    `yaml:"body"`
}

// Answer is the service's recorded answer.
type Answer struct {
	Status int `yaml:"status"`
	Body   any `yaml:"body"`
}

// ParseCases reads tests.yaml.
func ParseCases(b []byte) ([]Case, error) {
	var out []Case
	if err := decodeStrict(b, &out); err != nil {
		return nil, fmt.Errorf("tests.yaml: %w", err)
	}
	return out, nil
}

// RunCases runs every case against a fake of the service and returns what
// went wrong, one line per problem. Every capability and the test request
// need at least one case.
func RunCases(ctx context.Context, x *Connector, cases []Case) []string {
	var problems []string
	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.Run] = true
		for _, p := range runCase(ctx, x, tc) {
			problems = append(problems, tc.Name+": "+p)
		}
	}
	need := []string{"test"}
	need = append(need, x.def.Capabilities...)
	for _, n := range need {
		if !covered[n] {
			problems = append(problems, fmt.Sprintf("tests.yaml has no case for %s", n))
		}
	}
	return problems
}

func runCase(ctx context.Context, x *Connector, tc Case) []string {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if tc.Name == "" {
		bad("every case needs a name")
	}
	if (tc.Expect == nil) == (tc.ExpectError == "") {
		return append(problems, "needs exactly one of expect or expect_error")
	}
	var mu sync.Mutex
	got := false
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = true
		seen = append(seen, checkRequest(tc.Request, r)...)
		mu.Unlock()
		status := tc.Answer.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(tc.Answer.Body)
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{x.m.ID: srv.URL}}
	creds := c.Creds(tc.Creds)
	var result any
	var err error
	switch tc.Run {
	case "test":
		result, err = x.Test(ctx, cl, creds)
	case c.CapVerify:
		result, err = x.Verify(ctx, cl, creds, tc.Input["email"])
	default:
		return append(problems, fmt.Sprintf("run %q: want test or one of the capabilities", tc.Run))
	}
	srv.Close()
	mu.Lock()
	problems = append(problems, seen...)
	if !got {
		bad("no request was sent")
	}
	mu.Unlock()
	if tc.ExpectError != "" {
		if err == nil {
			bad("want a %s error, got none", tc.ExpectError)
		} else if k := c.KindOf(err); k != tc.ExpectError {
			bad("want a %s error, got %s: %v", tc.ExpectError, k, err)
		}
		return problems
	}
	if err != nil {
		return append(problems, fmt.Sprintf("unexpected error: %v", err))
	}
	have := asMap(result)
	keys := make([]string, 0, len(tc.Expect))
	for k := range tc.Expect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want := normal(tc.Expect[k])
		if !reflect.DeepEqual(have[k], want) {
			bad("%s: want %v, got %v", k, want, have[k])
		}
	}
	return problems
}

func checkRequest(want ExpectRequest, r *http.Request) []string {
	var problems []string
	if want.Method != "" && r.Method != want.Method {
		problems = append(problems, fmt.Sprintf("method: want %s, got %s", want.Method, r.Method))
	}
	if want.Path != "" && r.URL.Path != want.Path {
		problems = append(problems, fmt.Sprintf("path: want %s, got %s", want.Path, r.URL.Path))
	}
	for k, v := range want.Query {
		if got := r.URL.Query().Get(k); got != v {
			problems = append(problems, fmt.Sprintf("query %s: want %q, got %q", k, v, got))
		}
	}
	for k, v := range want.Headers {
		if got := r.Header.Get(k); got != v {
			problems = append(problems, fmt.Sprintf("header %s: want %q, got %q", k, v, got))
		}
	}
	if want.Body != nil {
		b, _ := io.ReadAll(io.LimitReader(r.Body, maxAnswer))
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		for k, v := range want.Body {
			if !reflect.DeepEqual(normal(body[k]), normal(v)) {
				problems = append(problems, fmt.Sprintf("body %s: want %v, got %v", k, v, body[k]))
			}
		}
	}
	return problems
}

// asMap is a result as its JSON fields.
func asMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, e := range m {
		m[k] = normal(e)
	}
	return m
}

// normal makes YAML and JSON values comparable: numbers as float64,
// maps as map[string]any.
func normal(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	if s, ok := out.(string); ok {
		return strings.TrimSpace(s)
	}
	return out
}
