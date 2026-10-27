package api

import (
	"context"
	"log/slog"
)

// MenuLink is an extra item an extension adds to one person's sidebar, such
// as the hosted Cloud's staff console for staff. It arrives in /v1/me as
// menu_links, so only the people it is for ever see it; the page it opens
// still checks who is calling.
type MenuLink struct {
	// ID is a stable name for the link, such as "admin".
	ID string `json:"id"`
	// Label is what the sidebar shows, such as "Admin".
	Label string `json:"label"`
	// Href is the page it opens: a path on this server, such as "/admin".
	Href string `json:"href"`
	// Icon names one of the web app's icons, such as "shield".
	Icon string `json:"icon"`
}

// MenuLinks returns the links one signed-in person gets, or none.
type MenuLinks func(ctx context.Context, userID string) ([]MenuLink, error)

// menuLinksFor collects every extension's links for a person. A failing
// extension is logged and left out: the sidebar is never worth failing
// /v1/me over.
func (s *Server) menuLinksFor(ctx context.Context, userID string) []MenuLink {
	out := []MenuLink{}
	for _, fn := range s.menuLinks {
		links, err := fn(ctx, userID)
		if err != nil {
			s.log.Error("extension menu links", slog.String("user_id", userID), slog.Any("err", err))
			continue
		}
		out = append(out, links...)
	}
	return out
}
