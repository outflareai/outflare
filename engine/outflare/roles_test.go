package outflare

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/outflareai/outflare/engine/internal/config"
)

func TestRoleHandler(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	cases := []struct {
		role   config.Role
		path   string
		served bool
	}{
		{config.RoleAll, "/v1/workspaces", true},
		{config.RoleAPI, "/v1/site/events", true},
		{config.RoleIngest, "/v1/site/events", true},
		{config.RoleIngest, "/site.js", true},
		{config.RoleIngest, "/readyz", true},
		{config.RoleIngest, "/v1/workspaces", false},
		{config.RoleIngest, "/", false},
		{config.RoleWorker, "/healthz", true},
		{config.RoleWorker, "/v1/site/events", false},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		roleHandler(ok, c.role).ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if served := rec.Code == http.StatusTeapot; served != c.served {
			t.Errorf("%s %s: served %v, want %v", c.role, c.path, served, c.served)
		}
		if !c.served && (rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json") {
			t.Errorf("%s %s: %d %q", c.role, c.path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
