package outflare

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/outflareai/outflare/engine/internal/config"
)

// roleHandler limits what a process answers to its role (OUTFLARE_ROLE).
// An ingest process takes website events and serves site.js; a worker
// process answers health checks only, so its platform can watch it. The
// "all" and "api" roles answer everything.
func roleHandler(h http.Handler, role config.Role) http.Handler {
	if role == config.RoleAll || role == config.RoleAPI {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if roleServes(role, r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{ //nolint:errcheck
			"code":       "not_found",
			"message":    "this server is an Outflare " + string(role) + " server; send API requests to the API's address",
			"request_id": r.Header.Get("X-Request-Id"),
		}})
	})
}

func roleServes(role config.Role, path string) bool {
	if path == "/healthz" || path == "/readyz" {
		return true
	}
	return role == config.RoleIngest && (path == "/site.js" || strings.HasPrefix(path, "/v1/site/"))
}
