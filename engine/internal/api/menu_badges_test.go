package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
)

// Extensions' badges reach the sidebar for the workspace that is open; a
// failing extension or a badge too long for the sidebar is left out.
func TestMenuBadges(t *testing.T) {
	var asked string
	e := freshEnvWith(t, func(o *api.Options) {
		o.MenuBadges = []api.MenuBadges{
			func(_ context.Context, ws string) (map[string]string, error) {
				asked = ws
				return map[string]string{"billing": "$50.00", "long": "far too long to fit a sidebar"}, nil
			},
			func(context.Context, string) (map[string]string, error) { return nil, errors.New("down") },
		}
	})
	owner, ws := setupOwner(t, e.h)
	out := owner.must(200, "GET", "/v1/workspaces/"+ws+"/menu-badges", nil)
	badges := out["badges"].(map[string]any)
	if asked != ws || len(badges) != 1 || badges["billing"] != "$50.00" {
		t.Fatalf("badges for %s: %v", asked, out)
	}
	// Without extensions there are none.
	plain := freshEnv(t)
	other, ws2 := setupOwner(t, plain.h)
	if b := other.must(200, "GET", "/v1/workspaces/"+ws2+"/menu-badges", nil)["badges"].(map[string]any); len(b) != 0 {
		t.Fatalf("badges without an extension: %v", b)
	}
}
