package outflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

func TestHostWiresRoutesMiddlewareAndCapabilities(t *testing.T) {
	h := &Host{}
	h.Handle("GET /v1/cloud/plan", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("X-Tenant"))) //nolint:errcheck
	}))
	h.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("X-Tenant", "acme")
			next.ServeHTTP(w, r)
		})
	})
	h.AddCapability("billing")

	srv := api.NewServer(nil, slog.New(slog.DiscardHandler), api.Options{
		Routes: h.registerRoutes, Middleware: h.middleware, Capabilities: h.capabilities,
	}).Handler()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/cloud/plan", nil))
	if rec.Code != 200 || rec.Body.String() != "acme" {
		t.Fatalf("extension route: %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/meta", nil))
	var meta struct{ Capabilities []string }
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(meta.Capabilities, "billing") || !slices.Contains(meta.Capabilities, "sequences") {
		t.Fatalf("capabilities: %v", meta.Capabilities)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/cloud/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("unknown /v1 path: %d", rec.Code)
	}
}

func TestClassifyEmail(t *testing.T) {
	c, err := ClassifyEmail(context.Background(), "x@mailinator.com")
	if err != nil || c.Global.Category != Disposable {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := ClassifyEmail(context.Background(), "nope"); err == nil {
		t.Fatal("a malformed address is an error")
	}
}

func TestErrorCode(t *testing.T) {
	for err, want := range map[error]string{
		nil:             "",
		ErrNotConnected: "connector_needed",
		ErrNoAIKey:      "ai_model_needed",
		connectors.Errf(connectors.ErrQuota, "out of tokens"): "connector_quota",
		errors.New("something else"):                          "",
	} {
		if got := ErrorCode(err); got != want {
			t.Errorf("%v = %q, want %q", err, got, want)
		}
	}
}

func TestHandleWorkspaceChecksRoute(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request, c *Caller) {}
	for _, tc := range []struct{ pattern, scope string }{
		{"GET /v1/cloud/thing", "workspace.read"},
		{"GET /v1/workspaces/{ws}", "workspace.read"},
		{"GET /v1/workspaces/{ws}/cloud/thing", "workspace.everything"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s %s was accepted", tc.pattern, tc.scope)
				}
			}()
			(&Host{}).HandleWorkspace(tc.pattern, tc.scope, ok)
		}()
	}
	h := &Host{}
	h.HandleWorkspace("GET /v1/workspaces/{ws}/cloud/thing", "workspace.read", ok)
	if len(h.workspaceRoutes) != 1 {
		t.Fatal("a good route was not added")
	}
}

func TestCredsAreRedacted(t *testing.T) {
	creds := connectors.Creds{"api_key": "sk-test-not-a-real-key"}
	for _, s := range []string{fmt.Sprint(creds), fmt.Sprintf("%v %+v %#v %s", creds, creds, creds, creds), (&Connection{ID: "x", Connector: "geonode"}).String()} {
		if strings.Contains(s, "not-a-real") {
			t.Fatalf("leaked: %s", s)
		}
	}
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "creds", creds)
	if strings.Contains(buf.String(), "not-a-real") || !strings.Contains(buf.String(), "api_key") {
		t.Fatalf("log: %s", buf.String())
	}
}
