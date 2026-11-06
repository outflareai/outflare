package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Help centre articles in a support reply: the articles a customer can be
// sent a link to, found from what they wrote or from a search, each with
// its address on the help centre of the conversation's brand.

type supportArticle struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
	// Its page on the help centre, or null when no help centre shows it.
	URL *string `json:"url"`
}

// liveHelpCentre is the live help centre customers of a brand read: the
// brand's own, else the workspace's help centre for every brand. Nil when
// there is none.
func (s *Server) liveHelpCentre(ctx context.Context, ws string, brand *string) (*helpCentreView, error) {
	v, err := s.scanHelpCentre(s.pool.QueryRow(ctx, helpCentreSelect+`
		WHERE h.workspace_id = $1 AND h.enabled AND h.brand_id IS NOT DISTINCT FROM $2::uuid
		ORDER BY h.created_at LIMIT 1`, ws, brand))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

// helpArticleURL is an article's page on a help centre.
func helpArticleURL(centre *helpCentreView, id, title string) *string {
	if centre == nil {
		return nil
	}
	u := centre.URL + "articles/" + helpRef(id, title)
	return &u
}

// linkHelpArticles sets the help centre page of every cited article
// customers can read there.
func (s *Server) linkHelpArticles(ctx context.Context, ws string, brand *string, list []knowledgePassage) error {
	centre, err := s.liveHelpCentre(ctx, ws, brand)
	if err != nil || centre == nil {
		return err
	}
	for i := range list {
		if list[i].Visibility == "public" {
			list[i].URL = helpArticleURL(centre, list[i].ItemID, list[i].Title)
		}
	}
	return nil
}

func (s *Server) listSupportArticles(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), knowledgeAskTimeout)
	defer cancel()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	suggested := q == ""
	if suggested {
		// What the customer last wrote, as Suggest reply reads it.
		var body string
		err = s.pool.QueryRow(ctx, `SELECT body FROM support_messages WHERE conversation_id = $1 AND kind = 'in' AND NOT auto_reply
			ORDER BY created_at DESC LIMIT 1`, c.ID).Scan(&body)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
		q = strings.TrimSpace(c.Subject + "\n\n" + body)
	}
	if utf8.RuneCountInString(q) > 1000 {
		q = string([]rune(q)[:1000])
	}
	brand := c.BrandID
	if brand == nil && len(a.Brands) == 1 {
		brand = &a.Brands[0]
	}
	centre, err := s.liveHelpCentre(ctx, a.WorkspaceID, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []supportArticle{}
	if q != "" {
		passages, _, err := s.retrieveKnowledge(ctx, a, knowledgeQuery{q: q, audience: audienceCustomers, brand: brand, limit: 24})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		seen := map[string]bool{}
		for _, p := range passages {
			// Only what the help centre shows can be linked to.
			if p.Visibility != "public" || seen[p.ItemID] || len(list) == 6 {
				continue
			}
			seen[p.ItemID] = true
			list = append(list, supportArticle{ID: p.ItemID, Kind: p.Kind, Title: p.Title, Excerpt: helpExcerpt(p.Text),
				URL: helpArticleURL(centre, p.ItemID, p.Title)})
		}
	}
	var hc any
	if centre != nil {
		hc = map[string]any{"id": centre.ID, "name": centre.Name, "url": centre.URL}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "suggested": suggested, "help_centre": hc})
}
