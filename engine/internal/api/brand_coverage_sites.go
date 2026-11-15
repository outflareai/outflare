package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Claiming a site: a teammate says "I'm managing this one" about a site
// the brand wants a link or placement from, so nobody else on the team
// reaches out to it at the same time. The claim is the owner of the site's
// company record (the built-in owner field), so it shows in the CRM, the
// relationship check and every Presence tab, and every claim, hand-over
// and release is in the company's own history. Nothing here contacts
// anyone.

// coverageSite is who manages a site for the brand.
type coverageSite struct {
	Host      string  `json:"host"`
	RecordID  *string `json:"record_id"`
	OwnerID   *string `json:"owner_id"`
	OwnerName *string `json:"owner_name"`
	// OwnerAvatarURL is the manager's profile photo, or null.
	OwnerAvatarURL *string `json:"owner_avatar_url"`
}

// maxSiteLookup caps the sites one lookup reads.
const maxSiteLookup = 200

func (s *Server) routesCoverageSites(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/coverage/sites", s.onBrand(auth.ScopeCoverageRead, s.coverageSites))
	mux.HandleFunc("POST /v1/brands/{brand}/coverage/sites/claim", s.onBrand(auth.ScopeCoverageManage, s.coverageClaimSite))
}

// coverageSiteHost is a site as Presence keys it: lowercase, no scheme,
// path or www. It is "" when it isn't a site.
func coverageSiteHost(raw string) string {
	return customers.CompanyDomain(raw)
}

// coverageSiteOwnerSQL is SQL for the brand's company record of the site in
// hostExpr ($1 workspace, $2 brand), as a records row alias r: its owner is
// r.attributes->>'owner'. Use it in a LEFT JOIN LATERAL.
func coverageSiteRecordSQL(hostExpr string) string {
	return `(SELECT r.id, r.attributes->>'owner' AS owner FROM record_identities i
 JOIN records r ON r.id = i.record_id AND r.record_type = 'account' AND r.merged_into IS NULL AND r.deleted_at IS NULL
 WHERE i.workspace_id = $1 AND i.brand_id = $2 AND i.namespace = 'domain' AND i.value = ` + hostExpr + ` AND i.valid_to IS NULL
 ORDER BY i.verified DESC, i.created_at LIMIT 1)`
}

// loadCoverageSites returns who manages each site, in the order asked.
func loadCoverageSites(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ws, brand string, hosts []string) ([]coverageSite, error) {
	rows, err := q.Query(ctx, `SELECT h.host, c.id::text, u.id::text, u.name, nullif(u.avatar_url, '')
 FROM unnest($3::text[]) WITH ORDINALITY AS h(host, n)
 LEFT JOIN LATERAL `+coverageSiteRecordSQL("h.host")+` c ON true
 LEFT JOIN users u ON u.id::text = c.owner
 ORDER BY h.n`, ws, brand, hosts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (coverageSite, error) {
		var v coverageSite
		return v, r.Scan(&v.Host, &v.RecordID, &v.OwnerID, &v.OwnerName, &v.OwnerAvatarURL)
	})
}

// coverageSites says who manages each site asked for (host, repeated).
func (s *Server) coverageSites(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var hosts []string
	seen := map[string]bool{}
	for _, raw := range r.URL.Query()["host"] {
		for _, part := range strings.Split(raw, ",") {
			h := coverageSiteHost(part)
			if h == "" {
				if strings.TrimSpace(part) != "" {
					writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a site such as example.com")
					return
				}
				continue
			}
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	if len(hosts) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give at least one host")
		return
	}
	if len(hosts) > maxSiteLookup {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "ask for at most 200 sites at once")
		return
	}
	list, err := loadCoverageSites(r.Context(), s.pool, a.WorkspaceID, a.Brand, hosts)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// errSiteClaimed is a site another teammate manages.
func errSiteClaimed(name string) error {
	if name == "" {
		name = "Another teammate"
	}
	return recordConflict{"site_claimed", name + " is managing this site. Check with them first, or send take_over: true to take it over."}
}

// coverageClaimSite claims a site for the caller (or hands it to another
// member, or releases it with owner_id null). A site someone else manages
// changes hands only with take_over: true, which agents never send.
func (s *Server) coverageClaimSite(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Host     string          `json:"host"`
		Owner    json.RawMessage `json:"owner_id"`
		TakeOver bool            `json:"take_over"`
	}
	if !decode(w, r, &in) {
		return
	}
	host := coverageSiteHost(in.Host)
	if host == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a site such as example.com")
		return
	}
	target := a.personID()
	if len(in.Owner) > 0 {
		if string(in.Owner) == "null" {
			target = ""
		} else if err := json.Unmarshal(in.Owner, &target); err != nil || !validUUID(target) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "owner_id is a member's user id, or null to release the site")
			return
		}
	} else if target == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "owner_id is required for a token that isn't a person's")
		return
	}
	if in.TakeOver && agentWrite(r) {
		writeError(w, r, http.StatusForbidden, "forbidden", "agents never take a site over from a teammate: ask a person to")
		return
	}
	var out coverageSite
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if target != "" {
			var member bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2
 AND (role IN ('owner','admin') OR brand_ids IS NULL OR cardinality(brand_ids)=0 OR $3=ANY(brand_ids)))`, a.WorkspaceID, target, a.Brand).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errInvalid("owner_id is not a member who can see this brand")
			}
		}
		brand := a.Brand
		records, err := customers.EnsureCompanies(ctx, tx, a.WorkspaceID, &brand, "presence", []customers.Company{{Domain: host, Name: host}})
		if err != nil {
			return err
		}
		id := records[host]
		if id == "" {
			return errInvalid("this site can't be claimed")
		}
		rec, err := lockRecord(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if rec.Attributes == nil {
			rec.Attributes = map[string]any{}
		}
		if rec.AttributeSources == nil {
			rec.AttributeSources = map[string]string{}
		}
		current, _ := rec.Attributes["owner"].(string)
		if current == target {
			return loadSiteInto(ctx, tx, a, host, &out)
		}
		takeOver := current != "" && current != a.personID()
		if takeOver && !in.TakeOver {
			var name string
			if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT name FROM users WHERE id::text = $1), '')`, current).Scan(&name); err != nil {
				return err
			}
			return errSiteClaimed(name)
		}
		c, err := loadCatalog(ctx, tx, a.WorkspaceID, deref(rec.BrandID), false)
		if err != nil {
			return err
		}
		t := c.recordType(rec.Type)
		if t == nil {
			return errNotFound
		}
		value := json.RawMessage("null")
		if target != "" {
			value, _ = json.Marshal(target)
		}
		changed, err := applyAttributes(ctx, tx, c, t, a, &rec, map[string]json.RawMessage{"owner": value}, "manual")
		if err != nil {
			return err
		}
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
				rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
				return err
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.updated", "record", rec.ID, map[string]any{"type": rec.Type}); err != nil {
				return err
			}
		}
		// The site's live opportunities follow its owner; their state stays
		// as it was, so they stay in the list.
		if _, err := tx.Exec(ctx, `UPDATE placement_opportunities SET owner_id=$4, updated_at=now()
 WHERE workspace_id=$1 AND brand_id=$2 AND host=$3 AND state IN ('open','picked')`, a.WorkspaceID, a.Brand, host, nullIfEmpty(target)); err != nil {
			return err
		}
		action := "coverage_site.claimed"
		switch {
		case target == "":
			action = "coverage_site.released"
		case takeOver:
			action = "coverage_site.taken_over"
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "record", rec.ID,
			map[string]any{"host": host, "owner_id": nullIfEmpty(target), "previous_owner_id": nullIfEmpty(current)},
			map[string]any{"brand_id": a.Brand, "assigned": target != "" && target != a.personID(), "take_over": takeOver}); err != nil {
			return err
		}
		return loadSiteInto(ctx, tx, a, host, &out)
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func loadSiteInto(ctx context.Context, tx pgx.Tx, a *access, host string, out *coverageSite) error {
	list, err := loadCoverageSites(ctx, tx, a.WorkspaceID, a.Brand, []string{host})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return errors.New("coverage: site vanished")
	}
	*out = list[0]
	return nil
}

// siteClaimedByOther returns the name of the teammate who manages the
// company record (or a person's company), when that isn't the caller.
func siteClaimedByOther(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, a *access, recordID string) (string, bool, error) {
	var owner, name string
	err := q.QueryRow(ctx, `SELECT c.attributes->>'owner', coalesce(u.name, '')
 FROM records p
 JOIN records c ON c.workspace_id = p.workspace_id AND c.record_type = 'account' AND c.merged_into IS NULL AND c.deleted_at IS NULL
  AND (c.id = p.id OR (p.record_type = 'person' AND c.id::text IN (SELECT jsonb_array_elements_text(coalesce(p.attributes->'companies', '[]'::jsonb)))))
 LEFT JOIN users u ON u.id::text = c.attributes->>'owner'
 WHERE p.workspace_id = $1 AND p.id = $2 AND coalesce(c.attributes->>'owner', '') NOT IN ('', $3)
 LIMIT 1`, a.WorkspaceID, recordID, a.personID()).Scan(&owner, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}
