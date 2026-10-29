package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// Items in web app URLs go by a slug made from their name, such as
// /acme/sequences/q3-founders. Slugs are made the first time a link to an
// item is needed and again when its name changes; old ones keep pointing at
// the item, so links never break. The API itself keeps using IDs.

type linkKind struct {
	scope string
	// SQL for the item's name, from a table with id and workspace_id.
	table, name string
	// SQL the slug is made from, when not the name: a connection is
	// /acme/connectors/posthog after its connector, whatever it is called.
	from string
}

var linkKinds = map[string]linkKind{
	"sequence":   {scope: auth.ScopeSequencesRead, table: "sequences", name: "name"},
	"list":       {scope: auth.ScopeLeadsRead, table: "lead_lists", name: "name"},
	"mission":    {scope: auth.ScopeMissionsRead, table: "missions", name: "name"},
	"server":     {scope: auth.ScopeMailboxesRead, table: "sending_servers", name: "COALESCE(NULLIF(name, ''), host)"},
	"plan":       {scope: auth.ScopeInfraRead, table: "infra_plans", name: "name"},
	"connection": {scope: auth.ScopeIntegrationsRead, table: "integrations", name: "name", from: "connector"},
	"token":      {scope: auth.ScopeTokensManage, table: "api_tokens", name: "name"},
	"webhook":    {scope: auth.ScopeWebhooksRead, table: "webhook_endpoints", name: "COALESCE(NULLIF(description, ''), substring(url from '://([^/:?#]+)'), url)"},
}

const linkKindNames = "sequence, list, mission, server, plan, connection, token or webhook"

// itemSlugFrom makes the slug for an item named name: lowercase letters,
// digits and dashes, at most 60 characters, never a UUID or "new" (which
// the web app uses for its own pages).
func itemSlugFrom(kind, name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" || s == "new" || validUUID(s) {
		s = strings.Trim(s+"-"+kind, "-")
	}
	return s
}

type itemLink struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// itemLink answers which item a URL names: ref is an item ID or any slug
// it has had. The answer carries its current slug.
func (s *Server) itemLink(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	// Slugs never have underscores, so /connectors/google_calendar finds
	// google-calendar.
	kind, ref := r.PathValue("kind"), strings.ReplaceAll(strings.ToLower(r.PathValue("ref")), "_", "-")
	k, ok := linkKinds[kind]
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind must be one of "+linkKindNames)
		return
	}
	if !a.has(k.scope) {
		writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, k.scope))
		return
	}
	link, err := s.resolveItem(r.Context(), a.WorkspaceID, kind, k, ref)
	if err == nil && a.Brands != nil {
		err = s.linkBrand(r.Context(), a, k.table, link.ID)
	}
	if errors.Is(err, errForbidden) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", kind+" not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (s *Server) resolveItem(ctx context.Context, ws, kind string, k linkKind, ref string) (*itemLink, error) {
	link := &itemLink{Kind: kind}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id := ref
		if !validUUID(ref) {
			err := tx.QueryRow(ctx, `SELECT item_id FROM item_slugs WHERE workspace_id = $1 AND kind = $2 AND slug = $3`, ws, kind, ref).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
		}
		from := k.from
		if from == "" {
			from = k.name
		}
		// source is what the slug is made from: the name, or k.from.
		var source string
		err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT id, %s, %s FROM %s WHERE id = $1 AND workspace_id = $2`, k.name, from, k.table), id, ws).Scan(&link.ID, &link.Name, &source)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		// One item at a time, so two first visits do not both make a slug.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('item_slug:' || $1::text))`, link.ID); err != nil {
			return err
		}
		var current, madeFrom string
		err = tx.QueryRow(ctx, `SELECT slug, name FROM item_slugs WHERE workspace_id = $1 AND kind = $2 AND item_id = $3 AND current`, ws, kind, link.ID).Scan(&current, &madeFrom)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current != "" && (madeFrom == source || itemSlugFrom(kind, madeFrom) == itemSlugFrom(kind, source)) {
			link.Slug = current
			return nil
		}
		// New, or renamed: retire the old slug and make one from the name.
		if _, err := tx.Exec(ctx, `UPDATE item_slugs SET current = false WHERE workspace_id = $1 AND kind = $2 AND item_id = $3 AND current`, ws, kind, link.ID); err != nil {
			return err
		}
		base := itemSlugFrom(kind, source)
		for n := 1; ; n++ {
			slug := base
			if n > 1 {
				slug = fmt.Sprintf("%s-%d", base, n)
			}
			// A slug this item had before is taken back, and so is one whose
			// item was deleted (a reconnected PostHog is posthog again, not
			// posthog-2); a slug another item still has is not.
			tag, err := tx.Exec(ctx, fmt.Sprintf(`
				INSERT INTO item_slugs (workspace_id, kind, slug, item_id, name) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (workspace_id, kind, slug) DO UPDATE SET current = true, name = EXCLUDED.name, item_id = EXCLUDED.item_id, created_at = now()
				WHERE item_slugs.item_id = EXCLUDED.item_id
				   OR NOT EXISTS (SELECT 1 FROM %s t WHERE t.id = item_slugs.item_id)`, k.table), ws, kind, slug, link.ID, source)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				link.Slug = slug
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return link, nil
}

// linkBrand checks a brand-limited caller may see the linked item: one of
// its brands' or a shared one, for the kinds that have brands.
func (s *Server) linkBrand(ctx context.Context, a *access, table, id string) error {
	switch table {
	case "sequences", "lead_lists", "missions":
	default:
		return errForbidden
	}
	var brand *string
	if err := s.pool.QueryRow(ctx, `SELECT brand_id::text FROM `+table+` WHERE id = $1`, id).Scan(&brand); err != nil {
		return err
	}
	if !a.sees(brand) {
		return errNotFound
	}
	return nil
}
