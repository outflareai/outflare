package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Brands own their data, step 6 (plans/brands/README.md): website and
// analytics. Site keys with their consent banner and cookies, flags and
// A/B tests, SEO experiments, tracking domains and campaigns, visitors,
// attribution and conversion definitions, and the analytics and Search
// Console numbers each belong to one brand. They are served under
// /v1/brands/{brand}/ by the same handlers as the workspace routes, which
// stay for older callers until the contract step: there a list is the
// workspace's first brand's unless ?brand= picks one (dataBrand), and a
// new row goes to the brand_id it names or the first brand
// (activationBrand). Synced analytics take their brand from the site's
// assignment (connection_brands.go): an unassigned site shows in no brand.

// twin is one of the two bases a brand route is served under.
type twin struct {
	s     *Server
	path  string
	brand bool
}

// twins are the bases of a route under prefix: the brand's and the
// workspace's.
func (s *Server) twins(prefix string) []twin {
	return []twin{{s, "/v1/brands/{brand}/" + prefix, true}, {s, "/v1/workspaces/{ws}/" + prefix, false}}
}

// twinRoute registers method rest on both bases: the brand's with
// onBrand, the workspace's with ws.
func (s *Server) twinRoute(mux *http.ServeMux, method, rest string, ws func(string, http.HandlerFunc) http.HandlerFunc, scope string, h http.HandlerFunc) {
	for _, t := range s.twins(rest) {
		mux.HandleFunc(method+" "+t.path, t.on(ws)(scope, h))
	}
}

// on is the wrapper for this base: onBrand on the brand's, else ws, the
// wrapper the workspace route had.
func (t twin) on(ws func(string, http.HandlerFunc) http.HandlerFunc) func(string, http.HandlerFunc) http.HandlerFunc {
	if t.brand {
		return t.s.onBrand
	}
	return ws
}

// owned runs next only when the row of table whose id is the path value
// param belongs to a brand the caller sees: on a brand route, the route's
// brand. Any other row is 404, as if it didn't exist, so ids can't be
// probed across brands. A malformed or unknown id goes on to next, which
// answers it as before.
func (s *Server) owned(table, param string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		id := r.PathValue(param)
		if a.Brand != "" || a.Brands != nil {
			var brand *string
			found := false
			if validUUID(id) {
				err := s.pool.QueryRow(r.Context(), `SELECT brand_id::text FROM `+table+` WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id).Scan(&brand)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					s.internal(w, r, err)
					return
				}
				found = err == nil
			}
			if found && !a.sees(brand) {
				writeError(w, r, http.StatusNotFound, "not_found", "not found")
				return
			}
		}
		next(w, r)
	}
}
