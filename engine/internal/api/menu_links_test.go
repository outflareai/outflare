package api

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

func TestMenuLinksForOnePerson(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler), menuLinks: []MenuLinks{
		func(_ context.Context, userID string) ([]MenuLink, error) {
			if userID != "staff" {
				return nil, nil
			}
			return []MenuLink{{ID: "admin", Label: "Admin", Href: "/admin", Icon: "shield"}}, nil
		},
		func(context.Context, string) ([]MenuLink, error) { return nil, errors.New("down") },
	}}
	if got := s.menuLinksFor(context.Background(), "staff"); len(got) != 1 || got[0].Href != "/admin" {
		t.Fatalf("staff: %v", got)
	}
	// Everyone else gets an empty list (never null), and a failing
	// extension leaves its links out instead of failing /v1/me.
	if got := s.menuLinksFor(context.Background(), "customer"); got == nil || len(got) != 0 {
		t.Fatalf("customer: %#v", got)
	}
}
