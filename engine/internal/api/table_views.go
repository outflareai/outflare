package api

import (
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// A table view is the columns a person chose to see in one named table of
// the web app, saved as their default. It belongs to the person, not to a
// workspace: the same table shows the same columns wherever they sign in.

func (s *Server) routesTableViews(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/table-views", s.requireUser(s.listTableViews))
	mux.HandleFunc("PUT /v1/me/table-views/{view}", s.requireUser(s.saveTableView))
	mux.HandleFunc("DELETE /v1/me/table-views/{view}", s.requireUser(s.deleteTableView))
}

// A view's name and a column id: short, and nothing a person typed.
var (
	tableViewName = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,79}$`)
	tableColumnID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,80}$`)
)

// maxTableViews and maxTableColumns keep one person's views small.
const (
	maxTableViews   = 200
	maxTableColumns = 200
)

type tableViewOut struct {
	View   string   `json:"view"`
	Hidden []string `json:"hidden"`
}

func (s *Server) listTableViews(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT view, hidden FROM user_table_views WHERE user_id = $1 ORDER BY view`, p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (tableViewOut, error) {
		var v tableViewOut
		err := row.Scan(&v.View, &v.Hidden)
		if v.Hidden == nil {
			v.Hidden = []string{}
		}
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []tableViewOut{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) saveTableView(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	view := r.PathValue("view")
	if !tableViewName.MatchString(view) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a view's name is up to 80 lowercase letters, digits, dots, dashes, colons and underscores")
		return
	}
	var in struct {
		Hidden []string `json:"hidden"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Hidden) > maxTableColumns {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "hidden lists at most 200 columns")
		return
	}
	hidden, seen := []string{}, map[string]bool{}
	for _, h := range in.Hidden {
		if !tableColumnID.MatchString(h) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "hidden is a list of column ids: letters, digits, dots, dashes, colons and underscores")
			return
		}
		if !seen[h] {
			seen[h] = true
			hidden = append(hidden, h)
		}
	}
	// A new view past the limit is refused; saving an existing one always
	// goes through.
	tag, err := s.pool.Exec(r.Context(), `INSERT INTO user_table_views (user_id, view, hidden)
		SELECT $1, $2, $3 WHERE (SELECT count(*) FROM user_table_views WHERE user_id = $1) < $4
			OR EXISTS (SELECT 1 FROM user_table_views WHERE user_id = $1 AND view = $2)
		ON CONFLICT (user_id, view) DO UPDATE SET hidden = EXCLUDED.hidden, updated_at = now()`, p.UserID, view, hidden, maxTableViews)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "you have saved views for 200 tables: go back to the standard columns on one you no longer use")
		return
	}
	writeJSON(w, http.StatusOK, tableViewOut{View: view, Hidden: hidden})
}

func (s *Server) deleteTableView(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if _, err := s.pool.Exec(r.Context(), `DELETE FROM user_table_views WHERE user_id = $1 AND view = $2`, p.UserID, r.PathValue("view")); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
