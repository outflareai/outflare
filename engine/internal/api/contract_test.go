package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v4"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/config"
	valerrors "github.com/pb33f/libopenapi-validator/errors"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// The contract check: every response any test in this package gets back is
// validated against api/openapi.yaml, so the document stays an accurate
// description of what the server really sends. A test that passes but gets
// a response the document doesn't describe fails the package.

var contract struct {
	once       sync.Once
	v          validator.Validator
	mu         sync.Mutex
	violations map[string]map[string]bool // "METHOD /path status" -> details
	seen       map[string]bool            // operations exercised
	access     map[string]routeAccess     // who each operation let in
}

func contractValidator() validator.Validator {
	contract.once.Do(func() {
		doc, err := libopenapi.NewDocument(spec.OpenAPI)
		if err != nil {
			panic(err)
		}
		opts := []config.Option{config.WithoutSecurityValidation()}
		if os.Getenv("CONTRACT_STRICT") != "" {
			opts = append(opts, config.WithStrictMode())
		}
		v, errs := validator.NewValidator(doc, opts...)
		if len(errs) > 0 {
			panic(fmt.Sprint(errs))
		}
		contract.v = v
		contract.violations = map[string]map[string]bool{}
		contract.seen = map[string]bool{}
		contract.access = map[string]routeAccess{}
	})
	return contract.v
}

func init() {
	testWrap = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !contractChecked(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			var reqBody []byte
			if r.Body != nil {
				reqBody, _ = io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(reqBody))
			}
			note, ext := &routeAccess{}, new(bool)
			r = r.WithContext(context.WithValue(context.WithValue(r.Context(), ctxRouteAccessKey{}, note), ctxExtensionKey{}, ext))
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, r)
			for k, vs := range rec.Header() {
				w.Header()[k] = vs
			}
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes()) //nolint:errcheck
			if !*ext {
				checkContract(r, reqBody, rec, *note)
			}
		})
	}
}

// contractChecked is false for paths the document deliberately leaves out:
// the EmailBison-compatible API (EmailBison's own shapes), tracked-link
// redirects and the web app.
func contractChecked(path string) bool {
	return strings.HasPrefix(path, "/v1/") || path == "/healthz" || path == "/readyz" ||
		path == "/.well-known/outflare.json"
}

func checkContract(r *http.Request, reqBody []byte, rec *httptest.ResponseRecorder, access routeAccess) {
	v := contractValidator()
	if strings.HasSuffix(specOperation(r.Method, r.URL.Path), "(not in the document)") && rec.Code == http.StatusNotFound {
		return // the catch-all for unknown endpoints
	}
	req := r.Clone(r.Context())
	req.Body = io.NopCloser(bytes.NewReader(reqBody))
	resp := &http.Response{
		StatusCode: rec.Code,
		Header:     rec.Header(),
		Body:       io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Request:    req,
	}
	var errs []*valerrors.ValidationError
	// Newline-delimited JSON is many documents, which the validator would
	// read as one: its schema is only a string, so there is nothing to check.
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/x-ndjson") {
		_, errs = v.ValidateHttpResponse(req, resp)
	}
	var reqErrs []string
	if rec.Code < 300 {
		// The server accepted it, so the document must accept it too.
		req2 := r.Clone(r.Context())
		req2.Body = io.NopCloser(bytes.NewReader(reqBody))
		if ok, es := v.ValidateHttpRequestSync(req2); !ok {
			for _, e := range es {
				reqErrs = append(reqErrs, "request: "+describe(e.Message, e.Reason, e.SchemaValidationErrors))
			}
		}
	}
	contract.mu.Lock()
	defer contract.mu.Unlock()
	op := specOperation(r.Method, r.URL.Path)
	contract.seen[op] = true
	if access != (routeAccess{}) {
		if prev, ok := contract.access[op]; ok && prev != access {
			add := fmt.Sprintf("access: one test saw %+v, another %+v", prev, access)
			contract.violations[op+" access"] = map[string]bool{add: true}
		}
		contract.access[op] = access
	}
	key := fmt.Sprintf("%s %d", op, rec.Code)
	add := func(d string) {
		if contract.violations[key] == nil {
			contract.violations[key] = map[string]bool{}
		}
		contract.violations[key][d] = true
	}
	for _, e := range errs {
		add("response: " + describe(e.Message, e.Reason, e.SchemaValidationErrors))
	}
	for _, d := range reqErrs {
		add(d)
	}
}

var specRoutes struct {
	once   sync.Once
	routes []specRoute
}

type specRoute struct {
	method, template string
	access           *routeAccess // x-outflare-access; nil when the document has none
	re               *regexp.Regexp
	literal          int // literal characters, so /missions/plan beats /missions/{id}
}

// specOperation names the document's operation for a request, as
// "METHOD /template", or the raw path when the document has none.
func specOperation(method, path string) string {
	specRoutes.once.Do(func() {
		var doc struct {
			Paths map[string]map[string]yaml.Node `yaml:"paths"`
		}
		if err := yaml.Unmarshal(spec.OpenAPI, &doc); err != nil {
			panic(err)
		}
		param := regexp.MustCompile(`\{[^}]+\}`)
		for tmpl, ops := range doc.Paths {
			parts := param.Split(tmpl, -1)
			for i := range parts {
				parts[i] = regexp.QuoteMeta(parts[i])
			}
			re := regexp.MustCompile("^" + strings.Join(parts, "[^/]+") + "$")
			lit := len(param.ReplaceAllString(tmpl, ""))
			for m := range ops {
				if !slices.Contains([]string{"get", "post", "put", "patch", "delete"}, m) {
					continue
				}
				var op struct {
					Access *routeAccess `yaml:"x-outflare-access"`
				}
				node := ops[m]
				if err := node.Decode(&op); err != nil {
					panic(err)
				}
				specRoutes.routes = append(specRoutes.routes, specRoute{strings.ToUpper(m), tmpl, op.Access, re, lit})
			}
		}
	})
	best := -1
	name := method + " " + path + " (not in the document)"
	for _, rt := range specRoutes.routes {
		if rt.method == method && rt.re.MatchString(path) && rt.literal > best {
			best, name = rt.literal, method+" "+rt.template
		}
	}
	return name
}

// AllSpecOperations lists every operation in the document.
func allSpecOperations() []string {
	specOperation("GET", "/")
	var out []string
	for _, rt := range specRoutes.routes {
		out = append(out, rt.method+" "+rt.template)
	}
	sort.Strings(out)
	return out
}

func describe(msg, reason string, schemaErrs any) string {
	s := msg
	if reason != "" && reason != msg {
		s += " (" + reason + ")"
	}
	if se := fmt.Sprintf("%v", schemaErrs); se != "[]" && se != "<nil>" {
		s += " " + se
	}
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

// checkAccess compares who each exercised operation let in with the
// document's x-outflare-access.
func checkAccess() {
	specOperation("GET", "/")
	for _, rt := range specRoutes.routes {
		op := rt.method + " " + rt.template
		got, seen := contract.access[op]
		if !seen {
			continue
		}
		want := routeAccess{}
		if rt.access != nil {
			want = *rt.access
		}
		if got != want {
			contract.violations[op+" x-outflare-access"] = map[string]bool{
				fmt.Sprintf("access: the route lets in %+v, the document says %+v", got, want): true}
		}
	}
	if f := os.Getenv("CONTRACT_DUMP_ACCESS"); f != "" {
		b, _ := json.MarshalIndent(contract.access, "", "  ")
		os.WriteFile(f, b, 0o644) //nolint:errcheck
	}
}

func TestMain(m *testing.M) {
	auth.FastHashesForTests()
	code := m.Run()
	contractValidator()
	contract.mu.Lock()
	checkAccess()
	keys := make([]string, 0, len(contract.violations))
	for k := range contract.violations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "\nOpenAPI contract: %d responses differ from api/openapi.yaml\n", len(keys))
		for _, k := range keys {
			fmt.Fprintf(os.Stderr, "  %s\n", k)
			for d := range contract.violations[k] {
				fmt.Fprintf(os.Stderr, "      %s\n", d)
			}
		}
		if code == 0 {
			code = 1
		}
	}
	if f := os.Getenv("CONTRACT_COVERAGE"); f != "" {
		var missing []string
		for _, op := range allSpecOperations() {
			if !contract.seen[op] {
				missing = append(missing, op)
			}
		}
		os.WriteFile(f, []byte(strings.Join(missing, "\n")+"\n"), 0o644) //nolint:errcheck
	}
	contract.mu.Unlock()
	os.Exit(code)
}
