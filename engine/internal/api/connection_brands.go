package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Connections and brands (plans/brands/README.md, "Connections and brands
// (owner, 2026-11-01)"): no brand, no data. A connection of kind brand
// belongs to one brand; one of kind split has accounts or properties
// (analytics sites, Search Console sites, ad accounts) assigned to brands
// one by one; one of kind shared is a tool every brand uses. Data from a
// connection or an account nobody assigned is stored with no brand and
// shown in none, and the workspace lists what is unassigned here so
// someone can fix it.

func (s *Server) routesConnectionBrands(mux *http.ServeMux) {
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/connections/unassigned", s.inWorkspace(read, s.listUnassignedConnections))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/accounts", s.inWorkspace(read, s.listConnectionAccounts))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/integrations/{id}/accounts/{account}", s.inWorkspace(manage, s.assignConnectionAccount))
}

// connectionAccount is one account or property of a connection and the
// brand its data belongs to.
type connectionAccount struct {
	IntegrationID string `json:"integration_id"`
	// Kind is site (an analytics or Search Console site or property) or
	// ad_account.
	Kind    string  `json:"kind"`
	Account string  `json:"account"`
	Name    string  `json:"name"`
	Host    string  `json:"host"`
	BrandID *string `json:"brand_id"`
	// Source says where the brand comes from: assigned (someone picked
	// it), connection (the connection's own brand), only_brand (the
	// workspace's one brand) or none (unassigned: its data shows nowhere).
	Source string `json:"source"`
	// SuggestedBrandID is a brand whose domains include the site's, for an
	// unassigned site. It is only a suggestion.
	SuggestedBrandID *string `json:"suggested_brand_id"`
}

// connectionAccountsSQL lists a workspace's sites (from every sync that
// reads them, and every assignment) and ad accounts, with their brands.
// $1 is the workspace, $2 one connection or null.
const connectionAccountsSQL = `
	WITH known AS (
		SELECT integration_id, site, max(host) AS host, max(name) AS name FROM (
			SELECT integration_id, site, host, site_name AS name FROM analytics_syncs WHERE workspace_id = $1
			UNION ALL SELECT integration_id, site, host, '' FROM search_syncs WHERE workspace_id = $1
			UNION ALL SELECT integration_id, site, '', '' FROM page_conversion_syncs WHERE workspace_id = $1
			UNION ALL SELECT integration_id, site, '', '' FROM analytics_site_brands WHERE workspace_id = $1
		) x WHERE site <> '' GROUP BY 1, 2
	), sole AS (
		SELECT min(id::text)::uuid AS id FROM brands WHERE workspace_id = $1 AND status = 'active' HAVING count(*) = 1
	)
	SELECT k.integration_id::text, 'site', k.site, k.name, k.host, outflare_site_brand(k.integration_id, k.site)::text,
		CASE WHEN o.brand_id IS NOT NULL THEN 'assigned' WHEN i.brand_id IS NOT NULL THEN 'connection'
			WHEN (SELECT id FROM sole) IS NOT NULL THEN 'only_brand' ELSE 'none' END,
		CASE WHEN o.brand_id IS NULL AND i.brand_id IS NULL THEN (
			SELECT b.id::text FROM brands b, unnest(b.domains) d
			WHERE b.workspace_id = $1 AND b.status = 'active' AND k.host <> '' AND (k.host = d OR k.host LIKE '%.' || d)
			ORDER BY length(d) DESC, b.created_at LIMIT 1) END
	FROM known k
	JOIN integrations i ON i.id = k.integration_id AND i.workspace_id = $1
	LEFT JOIN analytics_site_brands o ON o.integration_id = k.integration_id AND o.site = k.site
	WHERE ($2::uuid IS NULL OR k.integration_id = $2)
	UNION ALL
	SELECT a.integration_id::text, 'ad_account', a.external_id, a.name, '', COALESCE(a.brand_id, i.brand_id, (SELECT id FROM sole))::text,
		CASE WHEN a.brand_id IS NOT NULL THEN 'assigned' WHEN i.brand_id IS NOT NULL THEN 'connection'
			WHEN (SELECT id FROM sole) IS NOT NULL THEN 'only_brand' ELSE 'none' END, NULL
	FROM ad_accounts a
	JOIN integrations i ON i.id = a.integration_id AND i.workspace_id = $1
	WHERE a.workspace_id = $1 AND a.status = 'active' AND ($2::uuid IS NULL OR a.integration_id = $2)
	ORDER BY 1, 2, 5, 3`

func (s *Server) connectionAccounts(ctx context.Context, ws string, integration *string) ([]connectionAccount, error) {
	rows, err := s.pool.Query(ctx, connectionAccountsSQL, ws, integration)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (connectionAccount, error) {
		var c connectionAccount
		err := row.Scan(&c.IntegrationID, &c.Kind, &c.Account, &c.Name, &c.Host, &c.BrandID, &c.Source, &c.SuggestedBrandID)
		return c, err
	})
}

// unassignedConnection is a connection, or one of its accounts, whose data
// belongs to no brand.
type unassignedConnection struct {
	IntegrationID string `json:"integration_id"`
	Connector     string `json:"connector"`
	ConnectorName string `json:"connector_name"`
	Name          string `json:"name"`
	BrandKind     string `json:"brand_kind"`
	// Account is null when the whole connection has no brand (a brand
	// connection nobody assigned), else the account or property that has
	// none.
	Account *connectionAccount `json:"account"`
	// What says whose the connection is: the outside account it signed
	// in to, what the provider reported about it (Tag Manager accounts,
	// containers and the sites they tag, a Cloudflare account's zones),
	// who connected it and when, and the connection it was signed in
	// together with, if any.
	SignedInAs    string            `json:"signed_in_as"`
	Details       map[string]string `json:"details"`
	ConnectedAt   time.Time         `json:"connected_at"`
	ConnectedBy   *string           `json:"connected_by"`
	ConnectedWith *string           `json:"connected_with"`
	// SuggestedBrandID is a brand the whole connection probably belongs
	// to, for a connection with no brand, and SuggestedBecause why: site
	// (a site or DNS zone it reports is on the brand's domains), name (an account
	// or container it reports is named after the brand), same_sign_in
	// (the same outside account is the brand's in another connection) or
	// email_domain (it signed in with an address at the brand's domain).
	// Only a suggestion.
	SuggestedBrandID *string `json:"suggested_brand_id"`
	SuggestedBecause *string `json:"suggested_because"`
}

// brandHint is what a brand is recognised by.
type brandHint struct {
	ID, Name string
	Domains  []string
}

func onDomain(host string, domains []string) bool {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
	if host == "" {
		return false
	}
	for _, d := range domains {
		d = strings.ToLower(d)
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

// namedAfter says whether text has the brand's name as a whole word.
func namedAfter(text, name string) bool {
	text, name = strings.ToLower(text), strings.ToLower(strings.TrimSpace(name))
	if len(name) < 3 {
		return false
	}
	letter := func(r byte) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' }
	for from := 0; ; {
		at := strings.Index(text[from:], name)
		if at < 0 {
			return false
		}
		at += from
		end := at + len(name)
		if (at == 0 || !letter(text[at-1])) && (end == len(text) || !letter(text[end])) {
			return true
		}
		from = at + 1
	}
}

// suggestBrand guesses the brand of a connection with none, from what it
// reports and the workspace's other connections. It suggests only when
// exactly one brand fits the strongest clue.
func suggestBrand(i connectors.Integration, list []connectors.Integration, brands []brandHint) (*string, *string) {
	one := func(fits func(b brandHint) bool) *string {
		var found *string
		for _, b := range brands {
			if fits(b) {
				if found != nil {
					return nil
				}
				id := b.ID
				found = &id
			}
		}
		return found
	}
	because := func(id *string, why string) (*string, *string) {
		if id == nil {
			return nil, nil
		}
		return id, &why
	}
	// Sites a tag manager tags, or a DNS host's zones.
	if sites := i.Account.Details["sites"] + "," + i.Account.Details["zones"]; sites != "," {
		if id := one(func(b brandHint) bool {
			for _, s := range strings.Split(sites, ",") {
				if onDomain(s, b.Domains) {
					return true
				}
			}
			return false
		}); id != nil {
			return because(id, "site")
		}
	}
	names := i.Account.Details["accounts"] + ", " + i.Account.Details["containers"]
	if id := one(func(b brandHint) bool { return namedAfter(names, b.Name) }); id != nil {
		return because(id, "name")
	}
	if signIn(i) {
		owners := map[string]bool{}
		for _, o := range list {
			if o.ID != i.ID && o.BrandID != nil && signIn(o) && o.Account.ID == i.Account.ID {
				owners[*o.BrandID] = true
			}
		}
		if len(owners) == 1 {
			for id := range owners {
				if one(func(b brandHint) bool { return b.ID == id }) != nil {
					return because(&id, "same_sign_in")
				}
			}
		}
	}
	if _, domain, ok := strings.Cut(i.Account.Email, "@"); ok {
		if id := one(func(b brandHint) bool { return onDomain(domain, b.Domains) }); id != nil {
			return because(id, "email_domain")
		}
	}
	return nil, nil
}

// signIn says whether a connection signed in to an outside account (an
// OAuth sign-in), whose id then names that account: an API key's account
// id can be anything, such as a site.
func signIn(i connectors.Integration) bool {
	return i.AuthMethod == connectors.AuthOAuth2 && i.Account.ID != ""
}

// connectedWith is the connection signed in together with i: another one
// to the same outside account, made by the same person (makers) up to
// five minutes before.
func connectedWith(i connectors.Integration, list []connectors.Integration, makers map[string]string) *string {
	if !signIn(i) {
		return nil
	}
	for _, o := range list {
		if o.ID == i.ID || o.Connector == i.Connector || !signIn(o) || o.Account.ID != i.Account.ID || makers[o.ID] != makers[i.ID] {
			continue
		}
		if d := i.CreatedAt.Sub(o.CreatedAt); d >= 0 && d < 5*time.Minute {
			name := o.Name
			return &name
		}
	}
	return nil
}

// unassigned lists what in a workspace has no brand: brand connections
// without one (unless the workspace has a single brand), and the accounts
// and properties of the others nobody assigned. Shared connections are
// every brand's tools, so they never show.
func (s *Server) unassigned(ctx context.Context, ws string) ([]unassignedConnection, error) {
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	only, err := s.onlyBrand(ctx, s.pool, ws)
	if err != nil {
		return nil, err
	}
	accounts, err := s.connectionAccounts(ctx, ws, nil)
	if err != nil {
		return nil, err
	}
	// Who made each connection: the actor (to tell sign-ins made
	// together) and their name.
	rows, err := s.pool.Query(ctx, `SELECT i.id::text, COALESCE(i.created_by::text, ''), a.display_name
		FROM integrations i LEFT JOIN actors a ON a.id = i.created_by WHERE i.workspace_id = $1`, ws)
	if err != nil {
		return nil, err
	}
	makers, makerNames := map[string]string{}, map[string]*string{}
	var id, maker string
	var name *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &maker, &name}, func() error {
		makers[id], makerNames[id] = maker, name
		return nil
	}); err != nil {
		return nil, err
	}
	var brands []brandHint
	out := []unassignedConnection{}
	for _, i := range list {
		v := s.view(i)
		base := unassignedConnection{IntegrationID: i.ID, Connector: i.Connector, ConnectorName: v.ConnectorName, Name: i.Name, BrandKind: i.BrandKind,
			SignedInAs: cmp.Or(i.Account.Email, i.Account.Name), Details: i.Account.Details, ConnectedAt: i.CreatedAt,
			ConnectedBy: makerNames[i.ID], ConnectedWith: connectedWith(i, list, makers)}
		if base.Details == nil {
			base.Details = map[string]string{}
		}
		if i.BrandKind == connectors.KindBrand && i.BrandID == nil && only == nil {
			if brands == nil {
				if brands, err = s.brandHints(ctx, ws); err != nil {
					return nil, err
				}
			}
			base.SuggestedBrandID, base.SuggestedBecause = suggestBrand(i, list, brands)
			out = append(out, base)
			continue
		}
		if i.BrandKind == connectors.KindShared {
			continue
		}
		for _, acct := range accounts {
			if acct.IntegrationID == i.ID && acct.Source == "none" {
				u := base
				u.Account = &acct
				out = append(out, u)
			}
		}
	}
	return out, nil
}

// brandHints lists the workspace's active brands with their domains.
func (s *Server) brandHints(ctx context.Context, ws string) ([]brandHint, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, domains FROM brands WHERE workspace_id = $1 AND status = 'active' ORDER BY created_at`, ws)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (brandHint, error) {
		var b brandHint
		err := row.Scan(&b.ID, &b.Name, &b.Domains)
		return b, err
	})
	if list == nil {
		list = []brandHint{}
	}
	return list, err
}

func (s *Server) listUnassignedConnections(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	list, err := s.unassigned(r.Context(), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	conns, accts := 0, 0
	for _, u := range list {
		if u.Account == nil {
			conns++
		} else {
			accts++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "connections": conns, "accounts": accts})
}

func (s *Server) listConnectionAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return
	}
	list, err := s.connectionAccounts(r.Context(), a.WorkspaceID, &i.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// assignConnectionAccount puts one account or property of a connection in
// a brand (brand_id null takes the assignment away), and moves the data
// already stored for it to that brand.
func (s *Server) assignConnectionAccount(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return
	}
	var req struct {
		BrandID optional[string] `json:"brand_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !req.BrandID.Set {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id is the brand's id or handle, or null to take the assignment away")
		return
	}
	account := strings.TrimSpace(r.PathValue("account"))
	if account == "" || len(account) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account is the site's or ad account's id, from GET .../integrations/{id}/accounts")
		return
	}
	var brand *string
	if req.BrandID.Value != nil {
		id, err := s.brandIDFor(r.Context(), a.WorkspaceID, *req.BrandID.Value)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id is not a brand in this workspace")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	}
	accounts, err := s.connectionAccounts(r.Context(), a.WorkspaceID, &i.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	at := slices.IndexFunc(accounts, func(c connectionAccount) bool { return c.Account == account })
	if at < 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "this connection has no such account or site: GET .../integrations/{id}/accounts lists them")
		return
	}
	kind := accounts[at].Kind
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		ctx := r.Context()
		if kind == "ad_account" {
			if _, err := tx.Exec(ctx, `UPDATE ad_accounts SET brand_id = $4, updated_at = now() WHERE workspace_id = $1 AND integration_id = $2 AND external_id = $3`,
				a.WorkspaceID, i.ID, account, brand); err != nil {
				return err
			}
		} else {
			if brand == nil {
				if _, err := tx.Exec(ctx, `DELETE FROM analytics_site_brands WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`, a.WorkspaceID, i.ID, account); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, `
				INSERT INTO analytics_site_brands (workspace_id, integration_id, site, brand_id, assigned_by) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (integration_id, site) DO UPDATE SET brand_id = EXCLUDED.brand_id, assigned_by = EXCLUDED.assigned_by, updated_at = now()`,
				a.WorkspaceID, i.ID, account, brand, nilIfEmpty(a.ActorID)); err != nil {
				return err
			}
			if err := rebrandSite(ctx, tx, a.WorkspaceID, i.ID, account); err != nil {
				return err
			}
		}
		if brand != nil {
			noteBrand(ctx, *brand)
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "integration.account_assigned", "integration", i.ID,
			map[string]any{"account_kind": kind, "brand_id": brand, "connector": i.Connector})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	accounts, err = s.connectionAccounts(r.Context(), a.WorkspaceID, &i.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, c := range accounts {
		if c.Account == account && c.Kind == kind {
			writeJSON(w, http.StatusOK, c)
			return
		}
	}
	writeJSON(w, http.StatusOK, accounts[at])
}

// siteBrandTables hold a site's synced data, each with brand_id: the
// brand outflare_site_brand gives the site when the row is written.
// Assigning the site moves its rows with it.
var siteBrandTables = []string{"search_segments", "search_ga4_outcomes", "search_completeness",
	"analytics_daily", "analytics_syncs", "search_daily", "search_syncs", "search_sitemaps", "url_inspections", "page_conversions", "page_conversion_syncs", "organic_conversions", "organic_conversion_syncs"}

// rebrandSite sets the brand of everything stored for one site to the
// brand it belongs to now (none when it is unassigned).
func rebrandSite(ctx context.Context, tx pgx.Tx, ws, integration, site string) error {
	for _, table := range siteBrandTables {
		if _, err := tx.Exec(ctx, `UPDATE `+table+` SET brand_id = outflare_site_brand($2, $3)
			WHERE workspace_id = $1 AND integration_id = $2 AND site = $3 AND brand_id IS DISTINCT FROM outflare_site_brand($2, $3)`,
			ws, integration, site); err != nil {
			return err
		}
	}
	return nil
}

// rebrandConnection is rebrandSite for every site of a connection, after
// its own brand changed.
func rebrandConnection(ctx context.Context, tx pgx.Tx, ws, integration string) error {
	for _, table := range siteBrandTables {
		if _, err := tx.Exec(ctx, `UPDATE `+table+` SET brand_id = outflare_site_brand(integration_id, site)
			WHERE workspace_id = $1 AND integration_id = $2 AND brand_id IS DISTINCT FROM outflare_site_brand(integration_id, site)`,
			ws, integration); err != nil {
			return err
		}
	}
	return nil
}

// RunConnectionBrands reports how many connections and accounts have no
// brand, for the dashboard, until ctx ends.
func (s *Server) RunConnectionBrands(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if err := s.CountUnassigned(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "integration.unassigned_count_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// CountUnassigned sets the unassigned connections gauge across every
// workspace.
func (s *Server) CountUnassigned(ctx context.Context) error {
	if s.integrations == nil {
		metrics.SetConnectionsUnassigned("connection", 0)
		metrics.SetConnectionsUnassigned("account", 0)
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT workspace_id::text FROM integrations`)
	if err != nil {
		return err
	}
	wss, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	counts := map[string]int{"connection": 0, "account": 0}
	for _, ws := range wss {
		list, err := s.unassigned(ctx, ws)
		if err != nil {
			return err
		}
		for _, u := range list {
			if u.Account == nil {
				counts["connection"]++
			} else {
				counts["account"]++
			}
		}
	}
	for kind, n := range counts {
		metrics.SetConnectionsUnassigned(kind, n)
	}
	return nil
}
