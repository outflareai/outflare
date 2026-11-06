package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/emails"
)

type ctxMovedKey struct{}

// Tests written against the workspace routes that moved under
// /v1/brands/{brand}/ keep running: a call to one is served by its brand
// route. The brand is the one in ?brand=, else the body's brand_id, else
// the workspace's first; a call that finds nothing there (404) is tried on
// each other brand, as the workspace route found a row in any brand.
// Production answers 410 route_moved.
func init() {
	testRetiredServe = func(s *Server, mux http.Handler, w http.ResponseWriter, r *http.Request, rt retiredRoute) bool {
		_, old, _ := strings.Cut(rt.route, " ")
		_, instead, _ := strings.Cut(rt.instead, " ")
		oldRest, ok1 := strings.CutPrefix(old, "/v1/workspaces/{ws}/")
		newRest, ok2 := strings.CutPrefix(instead, "/v1/brands/{brand}/")
		if !ok1 || !ok2 || oldRest != newRest {
			return false
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"), "/", 2)
		if len(parts) != 2 {
			return false
		}
		ctx := r.Context()
		ws, err := s.workspaceIDFor(ctx, parts[0])
		if err != nil || ws == "" {
			return false
		}
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		var brands []string
		add := func(b string) {
			if b != "" && !slices.Contains(brands, b) {
				brands = append(brands, b)
			}
		}
		named := func(ref string) {
			var id string
			_ = s.pool.QueryRow(ctx, `SELECT id::text FROM brands WHERE workspace_id = $1 AND (id::text = $2 OR handle = $2)`, ws, ref).Scan(&id)
			add(id)
		}
		// A brand named in ?brand= is the only one tried, as the
		// workspace route did: one it can't find is not found.
		only := false
		if b := r.URL.Query().Get("brand"); b != "" && b != "all" && b != "none" {
			named(b)
			if len(brands) == 0 {
				writeError(w, r, http.StatusNotFound, "not_found", "not found")
				return true
			}
			only = true
		}
		// The body's brand (brand or brand_id) picks the brand on a
		// workspace route; on the brand route the path says it.
		var fields map[string]json.RawMessage
		bodyBrand := map[string]string{}
		if json.Unmarshal(body, &fields) == nil {
			for _, k := range []string{"brand", "brand_id"} {
				var ref string
				if json.Unmarshal(fields[k], &ref) == nil && ref != "" {
					before := len(brands)
					named(ref)
					if len(brands) > before {
						bodyBrand[k] = brands[len(brands)-1]
					}
				}
			}
		}
		if !only {
			var first string
			_ = s.pool.QueryRow(ctx, `SELECT COALESCE(outflare_first_brand($1)::text, '')`, ws).Scan(&first)
			add(first)
			if rows, err := s.pool.Query(ctx, `SELECT id::text FROM brands WHERE workspace_id = $1 ORDER BY created_at, id`, ws); err == nil {
				for rows.Next() {
					var id string
					if rows.Scan(&id) == nil {
						add(id)
					}
				}
				rows.Close()
			}
		}
		if len(brands) == 0 {
			return false
		}
		var rec *httptest.ResponseRecorder
		var path string
		for _, b := range brands {
			r2 := r.Clone(ctx)
			u := *r.URL
			u.Path = "/v1/brands/" + b + "/" + parts[1]
			q := u.Query()
			q.Del("brand")
			u.RawQuery = q.Encode()
			r2.URL = &u
			r2.RequestURI = u.RequestURI()
			r2.Pattern = ""
			sent := body
			if len(bodyBrand) > 0 {
				trimmed := maps.Clone(fields)
				for k, id := range bodyBrand {
					if id == b {
						delete(trimmed, k)
					}
				}
				sent, _ = json.Marshal(trimmed)
			}
			r2.Body = io.NopCloser(bytes.NewReader(sent))
			r2.ContentLength = int64(len(sent))
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, r2)
			path = u.Path
			if rec.Code != http.StatusNotFound {
				break
			}
		}
		if moved, ok := ctx.Value(ctxMovedKey{}).(*string); ok {
			*moved = path
		}
		for k, vs := range rec.Header() {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes()) //nolint:errcheck
		return true
	}
}

// TestRetiredRoutesAreRegistered: each retired route is a route the server
// serves, so routeMoved and the tests' rewrite find it by its pattern.
func TestRetiredRoutesAreRegistered(t *testing.T) {
	var seen *retiredRoute
	saved := testRetiredServe
	testRetiredServe = func(_ *Server, _ http.Handler, w http.ResponseWriter, r *http.Request, rt retiredRoute) bool {
		seen = &rt
		writeError(w, r, http.StatusGone, "route_moved", "moved")
		return true
	}
	t.Cleanup(func() { testRetiredServe = saved })
	h := NewServer(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Emails: &emails.Service{}}).Handler()
	param := regexp.MustCompile(`\{[^}]+\}`)
	for op, rt := range retiredRoutes {
		method, path, _ := strings.Cut(rt.route, " ")
		seen = nil
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, param.ReplaceAllString(path, "11111111-1111-4111-8111-111111111111"), nil))
		if seen == nil || seen.route != rt.route {
			t.Errorf("%s: %s is not a route the server serves", op, rt.route)
		}
	}
}

// TestRetiredRoutesAnswerMoved: without the tests' rewrite, as in
// production, a retired route answers 410 route_moved naming its brand
// route, and its handler never runs.
func TestRetiredRoutesAnswerMoved(t *testing.T) {
	rt := retiredRoutes["listLeads"]
	ran := false
	mux := http.NewServeMux()
	mux.HandleFunc(rt.route, func(w http.ResponseWriter, r *http.Request) {
		if routeMoved(w, r) {
			return
		}
		ran = true
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workspaces/acme/leads?limit=5", nil))
	if rec.Code != http.StatusGone || ran {
		t.Fatalf("retired route: status %d, handler ran %v", rec.Code, ran)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "route_moved" || !strings.Contains(body.Error.Message, "GET /v1/brands/{brand}/leads") {
		t.Fatalf("error %+v", body.Error)
	}
	if got := operationFor(http.MethodGet, rt.route); got != "listLeads" {
		t.Fatalf("logged as %q", got)
	}
}

// TestRetiredRoutesAreGoneFromTheSpec: none of them is in api/openapi.yaml,
// and each one's replacement is.
func TestRetiredRoutesAreGoneFromTheSpec(t *testing.T) {
	spec, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for op, rt := range retiredRoutes {
		if strings.Contains(string(spec), "operationId: "+op+"\n") {
			t.Errorf("%s is retired but still in api/openapi.yaml", op)
		}
		_, path, _ := strings.Cut(rt.instead, " ")
		if !strings.Contains(string(spec), "\n  "+path+":") {
			t.Errorf("%s: its replacement %s is not in api/openapi.yaml", op, rt.instead)
		}
	}
}
