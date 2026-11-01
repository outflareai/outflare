package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Brands own their data, step 4 (plans/brands/README.md): activation. A
// rule, journey, email template, topic, broadcast, email, alert and webhook
// endpoint each belong to one brand. They are served under
// /v1/brands/{brand}/ by the same handlers as the workspace routes, which
// stay for older callers until the contract step: there a list is the
// workspace's first brand's unless ?brand= picks one (dataBrand), and a new
// row goes to the brand_id it names or the first brand.

// activationBrand is the brand a new activation row is made in, from the
// brand_id (an id or handle) the caller gave, or nil. On a brand route it
// is the route's brand, and naming another is refused; on a workspace
// route it is the brand named, else the workspace's first (or a
// brand-limited caller's own). Nothing is made for every brand.
func (s *Server) activationBrand(ctx context.Context, a *access, ref *string) (string, error) {
	if ref == nil || strings.TrimSpace(*ref) == "" {
		id, err := s.defaultBrand(ctx, a)
		if err != nil {
			return "", err
		}
		if id == "none" {
			return "", errInvalid("the workspace has no brand yet: add one first")
		}
		return id, nil
	}
	id, err := s.brandIDFor(ctx, a.WorkspaceID, *ref)
	if errors.Is(err, errNotFound) || (err == nil && !a.sees(&id)) {
		if a.Brand != "" {
			return "", errInvalid("brand_id can only be this route's brand: it stays in its brand")
		}
		return "", errInvalid("brand_id is not a brand in this workspace")
	}
	if err != nil {
		return "", err
	}
	if !a.writes(&id) {
		return "", errInvalid("brand_id is not one of your brands")
	}
	return id, nil
}

// optionalBrand is activationBrand for a brand_id field that may be left
// out: unset keeps cur (nil for a new row: the default brand), and null
// means the default brand too, since nothing is for every brand.
func (s *Server) optionalBrand(ctx context.Context, a *access, f optional[string], cur *string) (*string, error) {
	if !f.Set && cur != nil {
		if a.Brand != "" && *cur != a.Brand {
			return nil, errInvalid("this belongs to another brand")
		}
		return cur, nil
	}
	id, err := s.activationBrand(ctx, a, f.Value)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// activationError writes err from activationBrand: a 400 for bad input,
// else a 500. It reports whether err was nil.
func (s *Server) activationError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case err == nil:
		return true
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	default:
		s.internal(w, r, err)
	}
	return false
}

// activationBases are the two bases each activation route is served under:
// the brand's (onBrand) and the workspace's, kept for older callers until
// the contract step, with the wrapper it had.
func (s *Server) activationBases(prefix string, workspace func(string, http.HandlerFunc) http.HandlerFunc) []activationBase {
	return []activationBase{
		{"/v1/brands/{brand}/" + prefix, s.onBrand},
		{"/v1/workspaces/{ws}/" + prefix, workspace},
	}
}

type activationBase struct {
	path string
	on   func(string, http.HandlerFunc) http.HandlerFunc
}
