package api

import (
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Community links: "Star us on GitHub" and "Follow us on X", two optional
// one-click items on Home and in a step of Get started. Nobody can check
// that a person starred or followed, so opening a link ticks its item. Each
// person keeps their own ticks, and can hide the items for good. A server's
// operator turns them off with OUTFLARE_COMMUNITY_LINKS=off; "Follow us on
// X" shows only when OUTFLARE_FOLLOW_URL is set.

// communityRepoURL is the engine's public repository, the one to star.
const communityRepoURL = "https://github.com/outflareai/outflare"

func (s *Server) routesCommunity(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/community", s.requireUser(s.getCommunity))
	mux.HandleFunc("PATCH /v1/me/community", s.requireUser(s.updateCommunity))
}

type communityLink struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	// Done: the person opened the link.
	Done bool `json:"done"`
}

// communityLinksFor lists the links a server shows: none when its operator
// turned them off, and X only when there is a profile to open.
func communityLinksFor(on bool, xURL string) []communityLink {
	if !on {
		return []communityLink{}
	}
	links := []communityLink{{ID: "github", Title: "Star us on GitHub", URL: communityRepoURL}}
	if xURL != "" {
		links = append(links, communityLink{ID: "x", Title: "Follow us on X", URL: xURL})
	}
	return links
}

type communityView struct {
	Links []communityLink `json:"links"`
	// The person hid the items.
	Hidden   bool       `json:"hidden"`
	HiddenAt *time.Time `json:"hidden_at"`
	// Whether the app shows them: there are links, the person didn't hide
	// them, and at least one isn't done yet.
	Show bool `json:"show"`
}

func (s *Server) loadCommunity(r *http.Request) (communityView, error) {
	v := communityView{Links: []communityLink{}}
	var clicked []string
	if err := s.pool.QueryRow(r.Context(), `SELECT community_clicked, community_hidden_at FROM users WHERE id = $1`, principalFrom(r.Context()).UserID).
		Scan(&clicked, &v.HiddenAt); err != nil {
		return v, err
	}
	v.Hidden = v.HiddenAt != nil
	left := false
	for _, l := range s.community {
		l.Done = slices.Contains(clicked, l.ID)
		left = left || !l.Done
		v.Links = append(v.Links, l)
	}
	v.Show = !v.Hidden && left
	return v, nil
}

func (s *Server) getCommunity(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadCommunity(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type communityInput struct {
	// The link the person just opened: github or x.
	Clicked *string `json:"clicked"`
	// True hides the items; false shows them again.
	Hidden *bool `json:"hidden"`
}

func (s *Server) updateCommunity(w http.ResponseWriter, r *http.Request) {
	var req communityInput
	if !decode(w, r, &req) {
		return
	}
	if req.Clicked != nil && !slices.ContainsFunc(s.community, func(l communityLink) bool { return l.ID == *req.Clicked }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "no community link "+*req.Clicked+" on this server")
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var clicked []string
		var hiddenAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT community_clicked, community_hidden_at FROM users WHERE id = $1 FOR UPDATE`, p.UserID).Scan(&clicked, &hiddenAt); err != nil {
			return err
		}
		if req.Clicked != nil && !slices.Contains(clicked, *req.Clicked) {
			if _, err := tx.Exec(ctx, `UPDATE users SET community_clicked = array_append(community_clicked, $2) WHERE id = $1`, p.UserID, *req.Clicked); err != nil {
				return err
			}
			clicked = append(clicked, *req.Clicked)
			if err := record(ctx, tx, p, "", "community.link_clicked", "community_link", *req.Clicked, nil); err != nil {
				return err
			}
		}
		if req.Hidden != nil && *req.Hidden == (hiddenAt == nil) {
			if _, err := tx.Exec(ctx, `UPDATE users SET community_hidden_at = CASE WHEN $2 THEN now() END WHERE id = $1`, p.UserID, *req.Hidden); err != nil {
				return err
			}
			action := "community.links_hidden"
			if !*req.Hidden {
				action = "community.links_shown"
			}
			return record(ctx, tx, p, "", action, "user", p.UserID, map[string]any{"done": len(clicked)})
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.loadCommunity(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
