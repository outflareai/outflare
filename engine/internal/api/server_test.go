package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/db"
)

func newServer(t *testing.T, webDir string) http.Handler {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return handler(t, api.NewServer(pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{WebDir: webDir}))
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSystemEndpoints(t *testing.T) {
	h := newServer(t, "")
	for _, path := range []string{"/healthz", "/readyz", "/v1/meta", "/.well-known/outflare.json", "/v1/openapi.yaml"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
		}
		if rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s: missing X-Request-Id", path)
		}
	}
}

func TestUnknownAPIPathReturnsJSONError(t *testing.T) {
	rec := get(t, newServer(t, ""), "/v1/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message, RequestID string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("code %q", body.Error.Code)
	}
}

func TestServesWebAppWithClientRouteFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log(1)"), 0o644)
	h := newServer(t, dir)

	if rec := get(t, h, "/mailboxes/123"); rec.Code != http.StatusOK || rec.Body.String() != "<html>app</html>" {
		t.Fatalf("client route: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, h, "/app.js"); rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %q", rec.Body.String())
	}
	if rec := get(t, h, "/missing.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset status %d", rec.Code)
	}
	if rec := get(t, h, "/../../etc/passwd"); rec.Code == http.StatusOK && rec.Body.String() != "<html>app</html>" {
		t.Fatal("path escaped the web dir")
	}
}

func TestWebAppEntriesRevalidateAndAssetsCache(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<script type="module" src="/assets/app-new.js"></script>`), 0o644)
	os.WriteFile(filepath.Join(dir, "portal.html"), []byte("<html>portal</html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "assets", "app-new.js"), []byte("console.log(1)"), 0o644)
	os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg/>"), 0o644)
	h := newServer(t, dir)

	for _, path := range []string{"/", "/acme/crm", "/acme/domains/acme-com.example", "/portal.html"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
			t.Errorf("%s: %d Cache-Control %q ETag %q", path, rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
		}
	}
	if rec := get(t, h, "/assets/app-new.js"); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	for _, path := range []string{"/assets/app-old.js", "/assets/a/b.js"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "" {
			t.Errorf("%s: %d Cache-Control %q", path, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	if rec := get(t, h, "/favicon.svg"); rec.Code != http.StatusOK || strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("favicon: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	// A browser revalidates with the validators it was given; a rollback serves an older file.
	revalidate := func(first http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if v := first.Get("ETag"); v != "" {
			req.Header.Set("If-None-Match", v)
		}
		if v := first.Get("Last-Modified"); v != "" {
			req.Header.Set("If-Modified-Since", v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := get(t, h, "/").Header()
	if rec := revalidate(first); rec.Code != http.StatusNotModified {
		t.Errorf("unchanged entry: %d", rec.Code)
	}
	index := filepath.Join(dir, "index.html")
	os.WriteFile(index, []byte(`<script type="module" src="/assets/app-old.js"></script>`), 0o644)
	older := time.Now().Add(-10 * time.Hour)
	os.Chtimes(index, older, older)
	if rec := revalidate(first); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "app-old.js") {
		t.Errorf("rolled back entry: %d %q", rec.Code, rec.Body.String())
	}
}
