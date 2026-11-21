package api

import (
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// A person's menu in the web app: the pages and things they pinned, and
// the things they opened last. Both belong to the person, not to a
// workspace, so the menu is the same wherever they sign in. Each entry is
// a ref: a page's id, or a path of ids to one thing inside a page. Titles
// and names are never stored here; the web app works them out.

func (s *Server) routesMenuItems(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/menu", s.requireUser(s.getMenu))
	mux.HandleFunc("PUT /v1/me/menu/pins", s.requireUser(s.saveMenuPins))
	mux.HandleFunc("POST /v1/me/menu/recents", s.requireUser(s.addMenuRecent))
	mux.HandleFunc("DELETE /v1/me/menu/recents", s.requireUser(s.clearMenuRecents))
}

// A ref is made of ids and URL-safe characters: nothing a person typed.
var menuRef = regexp.MustCompile(`^[A-Za-z0-9/_.:%~|@+,=-]{1,400}$`)

// maxMenuPins and maxMenuRecents keep one person's menu small.
const (
	maxMenuPins    = 100
	maxMenuRecents = 50
)

type menuRecentOut struct {
	Ref      string    `json:"ref"`
	OpenedAt time.Time `json:"opened_at"`
}

type menuOut struct {
	Pins    []string        `json:"pins"`
	Recents []menuRecentOut `json:"recents"`
}

func (s *Server) getMenu(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT kind, ref, at FROM user_menu_items WHERE user_id = $1
		ORDER BY kind, CASE WHEN kind = 'pin' THEN position END, at DESC, ref`, p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := menuOut{Pins: []string{}, Recents: []menuRecentOut{}}
	var kind, ref string
	var at time.Time
	if _, err := pgx.ForEachRow(rows, []any{&kind, &ref, &at}, func() error {
		if kind == "pin" {
			out.Pins = append(out.Pins, ref)
		} else {
			out.Recents = append(out.Recents, menuRecentOut{Ref: ref, OpenedAt: at})
		}
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// saveMenuPins replaces the person's pins with the list sent, in its order.
func (s *Server) saveMenuPins(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var in struct {
		Pins []string `json:"pins"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Pins) > maxMenuPins {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "you can pin at most 100 things: unpin one you no longer use")
		return
	}
	pins, seen := []string{}, map[string]bool{}
	for _, ref := range in.Pins {
		if !menuRef.MatchString(ref) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "pins is a list of refs: a page's id, or a path of ids, up to 400 URL-safe characters")
			return
		}
		if !seen[ref] {
			seen[ref] = true
			pins = append(pins, ref)
		}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `DELETE FROM user_menu_items WHERE user_id = $1 AND kind = 'pin'`, p.UserID); err != nil {
		s.internal(w, r, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO user_menu_items (user_id, kind, ref, position)
		SELECT $1, 'pin', ref, n FROM unnest($2::text[]) WITH ORDINALITY AS t (ref, n)`, p.UserID, pins); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pins": pins})
}

// addMenuRecent notes that the person opened something just now, and keeps
// only their newest recents.
func (s *Server) addMenuRecent(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var in struct {
		Ref string `json:"ref"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !menuRef.MatchString(in.Ref) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "ref is a page's id, or a path of ids, up to 400 URL-safe characters")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `WITH added AS (
			INSERT INTO user_menu_items (user_id, kind, ref) VALUES ($1, 'recent', $2)
			ON CONFLICT (user_id, kind, ref) DO UPDATE SET at = now()
		)
		DELETE FROM user_menu_items WHERE user_id = $1 AND kind = 'recent' AND ref <> $2 AND ref IN (
			SELECT ref FROM user_menu_items WHERE user_id = $1 AND kind = 'recent' AND ref <> $2 ORDER BY at DESC, ref OFFSET $3)`,
		p.UserID, in.Ref, maxMenuRecents-1); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearMenuRecents(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if _, err := s.pool.Exec(r.Context(), `DELETE FROM user_menu_items WHERE user_id = $1 AND kind = 'recent'`, p.UserID); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
