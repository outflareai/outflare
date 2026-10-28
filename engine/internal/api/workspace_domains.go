package api

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/publicsuffix"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// One list of the workspace's own domains. A domain is set up separately
// for each thing it does: a brand's website, SEO crawls, site tracking,
// cold email, Lifecycle email and tracked links. These live in each
// module's table; this joins them, one entry per registrable domain, so
// people and agents see everything a domain is used for and what is left
// to set up.

func (s *Server) routesWorkspaceDomains(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/domains", s.inBrand(auth.ScopeWorkspaceRead, s.listWorkspaceDomains))
	mux.HandleFunc("GET /v1/workspaces/{ws}/connections", s.inBrand(auth.ScopeIntegrationsRead, s.listAllConnections))
}

// The purposes a domain can be set up for, in the order the list shows them.
var domainPurposes = []string{"website", "seo", "site_tracking", "cold_email", "lifecycle_email", "tracking_links"}

type domainUse struct {
	Purpose string `json:"purpose"`
	// The host set up, which can be a subdomain (mail.acme.example).
	Host string `json:"host"`
	// verified, pending, failed, or active where there is nothing to verify.
	Status  string  `json:"status"`
	BrandID *string `json:"brand_id"`
	// The id to open it with in its own module: the brand, SEO site, site
	// key, Lifecycle or tracking domain. Sending domains use the host.
	ID string `json:"id"`
}

type workspaceDomain struct {
	Domain   string      `json:"domain"`
	BrandIDs []string    `json:"brand_ids"`
	Uses     []domainUse `json:"uses"`
	Purposes []string    `json:"purposes"`
	// Purposes that need a step of yours: a use that is pending or failed.
	NeedsAttention []string `json:"needs_attention"`
	// The company record with this domain, when the workspace has one.
	RecordID *string `json:"record_id"`
}

func (s *Server) listWorkspaceDomains(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT 'website', d, b.id::text, 'active', b.id::text FROM brands b, unnest(b.domains) d
			WHERE b.workspace_id = $1 AND b.status = 'active'
		UNION ALL
		SELECT 'seo', host, brand_id::text, CASE WHEN verified_at IS NOT NULL THEN 'verified' ELSE 'pending' END, id::text
			FROM seo_sites WHERE workspace_id = $1
		UNION ALL
		SELECT 'site_tracking', o, brand_id::text, 'active', id::text FROM site_keys, unnest(origins) o
			WHERE workspace_id = $1 AND revoked_at IS NULL
		UNION ALL
		SELECT 'cold_email', domain, brand_id::text,
			CASE WHEN spf = 'pass' AND dkim = 'pass' AND dmarc <> 'missing' THEN 'verified' ELSE 'failed' END, domain
			FROM sending_domains WHERE workspace_id = $1
		UNION ALL
		SELECT 'lifecycle_email', domain, brand_id::text, status, id::text FROM email_domains WHERE workspace_id = $1
		UNION ALL
		SELECT 'tracking_links', domain, NULL, status, id::text FROM tracking_domains WHERE workspace_id = $1`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	uses, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domainUse, error) {
		var u domainUse
		err := row.Scan(&u.Purpose, &u.Host, &u.BrandID, &u.Status, &u.ID)
		return u, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byDomain := map[string]*workspaceDomain{}
	var order []string
	for _, u := range uses {
		// A picked brand, or a brand-limited caller, sees what its brands
		// use and what is shared.
		if !sel.matches(u.BrandID) {
			continue
		}
		host := useHost(u.Host)
		if host == "" {
			continue
		}
		u.Host = host
		root, err := publicsuffix.EffectiveTLDPlusOne(host)
		if err != nil {
			root = host
		}
		d := byDomain[root]
		if d == nil {
			d = &workspaceDomain{Domain: root, BrandIDs: []string{}, Uses: []domainUse{}, Purposes: []string{}, NeedsAttention: []string{}}
			byDomain[root] = d
			order = append(order, root)
		}
		if slices.ContainsFunc(d.Uses, func(x domainUse) bool { return x.Purpose == u.Purpose && x.Host == u.Host && x.ID == u.ID }) {
			continue
		}
		d.Uses = append(d.Uses, u)
		if u.BrandID != nil && !slices.Contains(d.BrandIDs, *u.BrandID) {
			d.BrandIDs = append(d.BrandIDs, *u.BrandID)
		}
		if !slices.Contains(d.Purposes, u.Purpose) {
			d.Purposes = append(d.Purposes, u.Purpose)
		}
		if (u.Status == "pending" || u.Status == "failed") && !slices.Contains(d.NeedsAttention, u.Purpose) {
			d.NeedsAttention = append(d.NeedsAttention, u.Purpose)
		}
	}
	if len(order) > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT DISTINCT ON (i.value) i.value, r.id::text FROM record_identities i
			JOIN records r ON r.id = i.record_id AND r.record_type = 'account' AND r.merged_into IS NULL AND r.archived_at IS NULL
			WHERE i.workspace_id = $1 AND i.namespace = 'domain' AND i.value = ANY($2) AND i.valid_to IS NULL
			ORDER BY i.value, r.created_at`, a.WorkspaceID, order)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for rows.Next() {
			var domain, id string
			if err := rows.Scan(&domain, &id); err != nil {
				rows.Close()
				s.internal(w, r, err)
				return
			}
			byDomain[domain].RecordID = &id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	rank := func(p string) int { return slices.Index(domainPurposes, p) }
	sort.Strings(order)
	out := make([]workspaceDomain, 0, len(order))
	for _, k := range order {
		d := byDomain[k]
		sort.SliceStable(d.Uses, func(i, j int) bool {
			if d.Uses[i].Purpose != d.Uses[j].Purpose {
				return rank(d.Uses[i].Purpose) < rank(d.Uses[j].Purpose)
			}
			return d.Uses[i].Host < d.Uses[j].Host
		})
		sort.Slice(d.Purposes, func(i, j int) bool { return rank(d.Purposes[i]) < rank(d.Purposes[j]) })
		sort.Slice(d.NeedsAttention, func(i, j int) bool { return rank(d.NeedsAttention[i]) < rank(d.NeedsAttention[j]) })
		sort.Strings(d.BrandIDs)
		out = append(out, *d)
	}
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("purpose"))); q != "" {
		out = slices.DeleteFunc(out, func(d workspaceDomain) bool { return !slices.Contains(d.Purposes, q) })
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// A connected account, from the connector framework or a mailbox provider
// sign-in, in one shape.
type connectedAccount struct {
	// integration (the connector framework, GET /integrations/{id}) or
	// mailbox_oauth (a Google or Microsoft sign-in for mailboxes,
	// /oauth-connections).
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Connector string    `json:"connector"`
	Name      string    `json:"name"`
	Category  string    `json:"category"`
	Status    string    `json:"status"`
	BrandID   *string   `json:"brand_id"`
	CreatedAt time.Time `json:"created_at"`
}

// listAllConnections lists every account the workspace has connected:
// the connector framework's integrations and the mailbox sign-ins that sit
// beside it, so a connections page and an agent read one list.
func (s *Server) listAllConnections(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	ctx := r.Context()
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.integrations.List(ctx, s.pool, a.WorkspaceID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = integrationsFor(list, sel)
	out := []connectedAccount{}
	for _, i := range list {
		v := s.view(i)
		out = append(out, connectedAccount{Kind: "integration", ID: v.ID, Connector: v.Connector, Name: v.Name, Category: v.Category,
			Status: v.Status, BrandID: v.BrandID, CreatedAt: v.CreatedAt})
	}
	// Mailbox sign-ins belong to the whole workspace, so a picked brand or
	// a brand-limited caller does not see them.
	if sel.id == "" && sel.in == nil {
		rows, err := s.pool.Query(ctx, `SELECT id::text, provider, name, created_at FROM oauth_connections WHERE workspace_id = $1 ORDER BY created_at`, a.WorkspaceID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		oauth, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (connectedAccount, error) {
			v := connectedAccount{Kind: "mailbox_oauth", Category: "mailboxes", Status: "connected"}
			err := row.Scan(&v.ID, &v.Connector, &v.Name, &v.CreatedAt)
			return v, err
		})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, oauth...)
	}
	if c := r.URL.Query().Get("category"); c != "" {
		out = slices.DeleteFunc(out, func(v connectedAccount) bool { return v.Category != c })
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// useHost is the lowercase host of a domain, origin or URL, subdomain kept,
// or "" when it isn't one.
func useHost(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if host, _, ok := strings.Cut(s, ":"); ok {
		s = host
	}
	s = strings.TrimSuffix(s, ".")
	if customers.CompanyDomain(s) == "" {
		return ""
	}
	return s
}
