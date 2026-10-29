package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/outflareai/outflare/engine/internal/brands"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/reviews"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
)

// Brands: the businesses one company markets, each with its own website,
// such as Geonode, Repocket and each of its games. Connectors, members,
// tokens, leads and suppressions stay with the workspace; a brand owns its
// context, memories, projects and sending setup. Records without a brand are
// shared by every brand, and a record in a project has the project's brand.

func (s *Server) routesBrands(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands", s.inBrand(read, s.listBrands))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands", s.inWorkspace(manage, s.createBrand))
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands/{brand}", s.inBrand(read, s.getBrand))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/brands/{brand}", s.inBrand(manage, s.updateBrand))
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands/{brand}/context", s.inBrand(read, s.getBrandContext))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/brands/{brand}/context", s.inBrand(auth.ScopeContextManage, s.updateBrandContext))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/add", s.inWorkspace(auth.ScopeProjectsManage, s.addToBrand))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/remove", s.inWorkspace(auth.ScopeProjectsManage, s.removeFromBrand))
	mux.HandleFunc("GET /v1/business-models", s.listBusinessModels)
	mux.HandleFunc("GET /v1/workspaces/{ws}/brand-rules", s.inBrand(read, s.getBrandRules))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/brand-rules", s.inWorkspace(auth.ScopeWorkspaceManage, s.updateBrandRules))
}

var (
	brandStatuses  = []string{"active", "archived"}
	brandPlatforms = []string{"web", "ios", "android", "tv", "desktop", "console", "extension", "api"}
	handleShape    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)
	brandLinkKinds = []string{"app_store", "google_play", "tv_app", "chrome_web_store", "microsoft_store", "steam", "docs", "help", "blog", "status",
		"x", "linkedin", "youtube", "instagram", "tiktok", "facebook", "discord", "reddit", "github",
		"bluesky", "threads", "mastodon", "dev_to", "docker_hub",
		reviews.Trustpilot, reviews.G2, reviews.Capterra, reviews.Google, "other"}
)

// brandLink is one place the brand lives besides its website: an app
// store listing, a social profile, docs.
type brandLink struct {
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Label string `json:"label"`
}

// validLinks checks and tidies a brand's links: absolute http(s) URLs, a
// known kind (guessed from the address when left out), at most 50.
func validLinks(in []brandLink) ([]brandLink, error) {
	if len(in) > 50 {
		return nil, errInvalid("a brand may have at most 50 links")
	}
	out := []brandLink{}
	for _, l := range in {
		l.URL, l.Label, l.Kind = strings.TrimSpace(l.URL), strings.TrimSpace(l.Label), strings.ToLower(strings.TrimSpace(l.Kind))
		if l.URL == "" {
			continue
		}
		if !strings.Contains(l.URL, "://") {
			l.URL = "https://" + l.URL
		}
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !strings.Contains(u.Hostname(), ".") || len(l.URL) > 2000 {
			return nil, errInvalid("links need a web address, like https://apps.apple.com/app/id123: " + l.URL + " is not")
		}
		if len(l.Label) > 60 {
			return nil, errInvalid("a link's label is at most 60 characters")
		}
		if l.Kind == "" {
			l.Kind = sociallinks.KindOf(u)
			if l.Kind == "" {
				l.Kind = "other"
			}
		}
		if !slices.Contains(brandLinkKinds, l.Kind) {
			return nil, errInvalid("a link's kind is one of " + strings.Join(brandLinkKinds, ", "))
		}
		out = append(out, l)
	}
	return out, nil
}

type brandCounts struct {
	Projects        int `json:"projects"`
	Domains         int `json:"domains"`
	Mailboxes       int `json:"mailboxes"`
	LeadLists       int `json:"lead_lists"`
	Sequences       int `json:"sequences"`
	ActiveSequences int `json:"active_sequences"`
}

type brandRecord struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Handle    string      `json:"handle"`
	Website   string      `json:"website"`
	Domains   []string    `json:"domains"`
	Platforms []string    `json:"platforms"`
	Links     []brandLink `json:"links"`
	Status    string      `json:"status"`
	// What the brand does and how its customers pay, from GET
	// /v1/business-models; null and [] until picked in brand setup.
	BusinessModel *string  `json:"business_model"`
	RevenueModels []string `json:"revenue_models"`
	// The brand's own logo and colours where they differ from the
	// company's; "" for the company's.
	LogoURL string     `json:"logo_url"`
	Colors  lookColors `json:"colors"`
	// The brand context's version; 1 until it is first saved.
	ContextVersion int         `json:"context_version"`
	Counts         brandCounts `json:"counts"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// loadBrands returns the workspace's brands, or one when id is set. status
// filters; empty means active, "all" means every status.
func (s *Server) loadBrands(ctx context.Context, ws string, id *string, status string) ([]brandRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.id, b.name, b.handle, b.website, b.domains, b.platforms, b.links, b.status, NULLIF(b.business_model, ''), b.revenue_models,
		       b.logo_url, b.colors,
		       COALESCE((SELECT version FROM brand_context WHERE brand_id = b.id), 1),
		       (SELECT count(*) FROM projects WHERE brand_id = b.id AND status <> 'archived')::int,
		       (SELECT count(*) FROM sending_domains WHERE brand_id = b.id)::int,
		       (SELECT count(*) FROM mailboxes WHERE brand_id = b.id)::int,
		       (SELECT count(*) FROM lead_lists WHERE brand_id = b.id)::int,
		       (SELECT count(*) FROM sequences WHERE brand_id = b.id AND status <> 'archived')::int,
		       (SELECT count(*) FROM sequences WHERE brand_id = b.id AND status = 'active')::int,
		       b.created_at, b.updated_at
		FROM brands b
		WHERE b.workspace_id = $1 AND ($2::uuid IS NULL OR b.id = $2)
		  AND ($3 = 'all' OR ($3 = '' AND b.status = 'active') OR b.status = $3)
		ORDER BY (b.status = 'archived'), lower(b.name)`, ws, id, status)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (brandRecord, error) {
		var v brandRecord
		err := row.Scan(&v.ID, &v.Name, &v.Handle, &v.Website, &v.Domains, &v.Platforms, &v.Links, &v.Status, &v.BusinessModel, &v.RevenueModels, &v.LogoURL, &v.Colors, &v.ContextVersion,
			&v.Counts.Projects, &v.Counts.Domains, &v.Counts.Mailboxes, &v.Counts.LeadLists, &v.Counts.Sequences,
			&v.Counts.ActiveSequences, &v.CreatedAt, &v.UpdatedAt)
		return v, err
	})
}

// brandIDFor turns a brand's id or handle into its id, or errNotFound.
func (s *Server) brandIDFor(ctx context.Context, ws, ref string) (string, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM brands WHERE workspace_id = $1 AND (handle = $2 OR id::text = $2)`, ws, ref).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return id, err
}

// brandFor loads the {brand} path value's brand, or writes a 404.
func (s *Server) brandFor(w http.ResponseWriter, r *http.Request, a *access) (*brandRecord, bool) {
	id, err := s.brandIDFor(r.Context(), a.WorkspaceID, r.PathValue("brand"))
	if err == nil && !a.sees(nil, &id) {
		err = errNotFound
	}
	if err == nil {
		var list []brandRecord
		list, err = s.loadBrands(r.Context(), a.WorkspaceID, &id, "all")
		if err == nil && len(list) == 1 {
			noteBrand(r.Context(), id)
			return &list[0], true
		}
	}
	if err != nil && !errors.Is(err, errNotFound) {
		s.internal(w, r, err)
		return nil, false
	}
	writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
	return nil, false
}

// brandSel is a list's brand filter: ?brand= and, for a brand-limited
// caller, the brands it may see.
type brandSel struct {
	// id is one brand's id, "none" for records shared by every brand, or ""
	// for no ?brand= filter.
	id string
	// in is set for a brand-limited caller: only these brands' records and
	// the shared ones.
	in []string
}

// all reports whether the filter lets every record through.
func (b brandSel) all() bool { return b.id == "" && b.in == nil }

// brandFilter reads ?brand= for list endpoints: a brand's id or handle,
// "none" for records shared by every brand, or "all". A brand-limited
// caller is kept to its brands, and gets a 404 for any other.
func (s *Server) brandFilter(w http.ResponseWriter, r *http.Request) (brandSel, bool) {
	a := accessFrom(r.Context())
	sel := brandSel{in: a.Brands}
	b := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("brand")))
	if b == "" || b == "none" || b == "all" {
		sel.id = strings.TrimSuffix(b, "all")
		return sel, true
	}
	id, err := s.brandIDFor(r.Context(), a.WorkspaceID, b)
	if err == nil && !a.sees(nil, &id) {
		err = errNotFound
	}
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found: brand is a brand's id or handle, or none for shared records")
		return sel, false
	}
	if err != nil {
		s.internal(w, r, err)
		return sel, false
	}
	sel.id = id
	noteBrand(r.Context(), id)
	return sel, true
}

// matches reports whether a record with brand id passes the filter.
func (b brandSel) matches(id *string) bool {
	if b.in != nil && id != nil && !slices.Contains(b.in, *id) {
		return false
	}
	switch b.id {
	case "":
		return true
	case "none":
		return id == nil
	}
	return id != nil && *id == b.id
}

// sql is the filter's condition on column col, adding its arguments to
// args.
func (b brandSel) sql(col string, args *[]any) string {
	out := ""
	switch b.id {
	case "":
	case "none":
		out = ` AND ` + col + ` IS NULL`
	default:
		*args = append(*args, b.id)
		out = fmt.Sprintf(` AND %s = $%d`, col, len(*args))
	}
	if b.in != nil {
		*args = append(*args, b.in)
		out += fmt.Sprintf(` AND (%s IS NULL OR %s = ANY($%d::uuid[]))`, col, col, len(*args))
	}
	return out
}

func (s *Server) listBrands(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "all" && !slices.Contains(brandStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, archived or all")
		return
	}
	list, err := s.loadBrands(r.Context(), a.WorkspaceID, nil, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = slices.DeleteFunc(list, func(b brandRecord) bool { return !a.sees(nil, &b.ID) })
	if list == nil {
		list = []brandRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getBrand(w http.ResponseWriter, r *http.Request) {
	if b, ok := s.brandFor(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, b)
	}
}

type brandInput struct {
	Name      *string      `json:"name"`
	Handle    *string      `json:"handle"`
	Website   *string      `json:"website"`
	Domains   *[]string    `json:"domains"`
	Platforms *[]string    `json:"platforms"`
	Links     *[]brandLink `json:"links"`
	Status    *string      `json:"status"`
	// "" clears it.
	BusinessModel *string   `json:"business_model"`
	RevenueModels *[]string `json:"revenue_models"`
	// The brand's own logo and colours; "" for the company's.
	LogoURL *string     `json:"logo_url"`
	Colors  *lookColors `json:"colors"`
}

// hostOf turns "https://www.Repocket.com/pricing" into "repocket.com".
func hostOf(site string) string {
	site = strings.ToLower(strings.TrimSpace(site))
	site = strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://")
	if i := strings.IndexAny(site, "/?#"); i >= 0 {
		site = site[:i]
	}
	if i := strings.LastIndex(site, ":"); i >= 0 {
		site = site[:i]
	}
	return strings.TrimSuffix(strings.TrimPrefix(site, "www."), ".")
}

var domainShape = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func handleProblem(h string) string {
	switch {
	case len(h) < 2 || len(h) > 40:
		return "handle must be 2 to 40 characters"
	case !handleShape.MatchString(h) || validUUID(h):
		return "handle must use only lowercase letters, numbers and dashes, and start and end with a letter or number"
	case h == "none" || h == "all":
		return "handle must not be none or all"
	case slices.Contains(pageNames, h):
		return "handle must not be the name of a page in the app, like " + h + ": it comes before the page in URLs (/acme/repocket/seo)"
	}
	return ""
}

// pageNames are the web app's pages (appTabs in web/src/lib/paths.ts). A
// brand's handle is the part of the app's URLs after the workspace, before
// the page, so it can't be one of them. TestBrandHandlesAreNotPages keeps
// the two lists the same.
var pageNames = []string{"overview", "get-started", "setup", "ai", "ai-usage", "tools", "reports", "analytics", "tracking", "links", "seo", "seo-overview", "audits", "fixes", "experiments", "keywords", "ai-visibility", "backlinks", "competitors", "reddit", "social", "ads", "partners", "visitors", "inbox", "missions", "playbooks", "outcomes", "projects", "sequences", "link-building", "news", "infrastructure", "mailboxes", "domains", "warmup", "servers", "allocation", "lifecycle", "emails", "broadcasts", "journeys", "rules", "templates", "topics", "providers", "people", "leads", "lists", "suppression", "company", "brands", "ideal-customers", "workspace", "members", "alerts", "digests", "senders", "connectors", "webhooks", "tokens", "logs", "audit", "instance", "account", "email-finder", "lead-finder", "reviews", "flags", "channels", "ab-tests"}

// handleFrom makes a handle from a brand's name: "Repocket Games" becomes
// "repocket-games".
func handleFrom(name string) string {
	h := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(h) > 40 {
		h = strings.Trim(h[:40], "-")
	}
	if slices.Contains(pageNames, h) {
		h += "-brand"
	}
	if handleProblem(h) != "" {
		h = "brand"
	}
	return h
}

func (in *brandInput) validate() error {
	if in.LogoURL != nil {
		logo, err := cleanLogoURL(*in.LogoURL)
		if err != nil {
			return err
		}
		in.LogoURL = &logo
	}
	if in.Colors != nil {
		if err := in.Colors.clean(); err != nil {
			return err
		}
	}
	if in.BusinessModel != nil {
		*in.BusinessModel = strings.TrimSpace(*in.BusinessModel)
		if _, ok := brands.ModelByID(*in.BusinessModel); *in.BusinessModel != "" && !ok {
			return errInvalid("business_model must be an id from GET /v1/business-models")
		}
	}
	if in.RevenueModels != nil {
		out := []string{}
		for _, r := range *in.RevenueModels {
			if !brands.ValidRevenue(r) {
				return errInvalid("revenue_models must be ids from GET /v1/business-models: " + r + " is not")
			}
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
		*in.RevenueModels = out
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 100 {
			return errInvalid("name must be 1 to 100 characters")
		}
	}
	if in.Handle != nil {
		*in.Handle = strings.ToLower(strings.TrimSpace(*in.Handle))
		if p := handleProblem(*in.Handle); p != "" {
			return errInvalid(p)
		}
	}
	if in.Website != nil {
		*in.Website = hostOf(*in.Website)
		if *in.Website != "" && !domainShape.MatchString(*in.Website) {
			return errInvalid("website must be a domain like repocket.com")
		}
	}
	if in.Domains != nil {
		var out []string
		for _, d := range *in.Domains {
			d = hostOf(d)
			if d == "" {
				continue
			}
			if !domainShape.MatchString(d) {
				return errInvalid("domains must be domains like repocket.com: " + d + " is not")
			}
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
		if len(out) > 100 {
			return errInvalid("a brand may have at most 100 domains")
		}
		*in.Domains = out
	}
	if in.Platforms != nil {
		var out []string
		for _, p := range *in.Platforms {
			p = strings.ToLower(strings.TrimSpace(p))
			if !slices.Contains(brandPlatforms, p) {
				return errInvalid("platforms are " + strings.Join(brandPlatforms, ", "))
			}
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
		*in.Platforms = out
	}
	if in.Links != nil {
		links, err := validLinks(*in.Links)
		if err != nil {
			return err
		}
		*in.Links = links
	}
	if in.Status != nil && !slices.Contains(brandStatuses, *in.Status) {
		return errInvalid("status is active or archived")
	}
	return nil
}

// withWebsite puts the website first in domains when it is not there.
func withWebsite(website string, domains []string) []string {
	out := []string{}
	if website != "" {
		out = append(out, website)
	}
	for _, d := range domains {
		if d != website {
			out = append(out, d)
		}
	}
	return out
}

// brandUnique turns a unique violation into the field a person can fix.
func brandUnique(err error) error {
	if !isUnique(err) {
		return err
	}
	if strings.Contains(err.Error(), "brands_handle_idx") {
		return errInvalid("another brand already has that handle")
	}
	return errInvalid("a brand with that name already exists")
}

func (s *Server) createBrand(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req brandInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if req.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is only for updates")
		return
	}
	if err := req.validate(); s.allocError(w, r, err) {
		return
	}
	handle := handleFrom(*req.Name)
	if req.Handle != nil {
		handle = *req.Handle
	}
	website, domains, platforms, links := "", []string{}, []string{}, []brandLink{}
	if req.Links != nil {
		links = *req.Links
	}
	if req.Website != nil {
		website = *req.Website
	}
	if req.Domains != nil {
		domains = *req.Domains
	}
	if req.Platforms != nil {
		platforms = *req.Platforms
	}
	domains = withWebsite(website, domains)
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A handle made from the name gets a number when it is taken.
		if req.Handle == nil {
			base := handle
			for i := 2; ; i++ {
				var taken bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE workspace_id = $1 AND handle = $2)`, a.WorkspaceID, handle).Scan(&taken); err != nil {
					return err
				}
				if !taken {
					break
				}
				handle = fmt.Sprintf("%s-%d", strings.TrimSuffix(base[:min(len(base), 36)], "-"), i)
			}
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO brands (workspace_id, name, handle, website, domains, platforms, links, created_by, business_model, revenue_models, logo_url, colors)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9, ''), COALESCE($10, '{}'::text[]), COALESCE($11, ''), COALESCE($12, '{}'::jsonb)) RETURNING id`,
			a.WorkspaceID, *req.Name, handle, website, domains, platforms, links, a.ActorID, req.BusinessModel, req.RevenueModels, req.LogoURL, req.Colors).Scan(&id)
		if err != nil {
			return brandUnique(err)
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "brand.created", "brand", id, map[string]any{
			"handle": handle, "domains": len(domains), "platforms": platforms, "links": len(links),
		}); err != nil {
			return err
		}
		// With a page reader connected, the brand's website is read for its
		// social links straight away, as suggestions.
		if website != "" {
			if _, err := s.enqueueLinkDiscovery(ctx, tx, a.WorkspaceID, id, "created"); err != nil {
				return err
			}
		}
		if len(links) > 0 {
			return s.enqueuePublicSocialSync(ctx, tx, a.WorkspaceID, "brand:"+id)
		}
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadBrands(ctx, a.WorkspaceID, &id, "all")
	if err != nil || len(list) != 1 {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) updateBrand(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req brandInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); s.allocError(w, r, err) {
		return
	}
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	website := b.Website
	if req.Website != nil {
		website = *req.Website
	}
	var domains []string
	if req.Domains != nil || req.Website != nil {
		domains = b.Domains
		if req.Domains != nil {
			domains = *req.Domains
		}
		domains = withWebsite(website, domains)
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE brands SET name = COALESCE($3, name), handle = COALESCE($4, handle), website = COALESCE($5, website),
				domains = COALESCE($6, domains), platforms = COALESCE($7, platforms), status = COALESCE($8, status),
				links = COALESCE($9, links), business_model = COALESCE($10, business_model), revenue_models = COALESCE($11, revenue_models),
				logo_url = COALESCE($12, logo_url), colors = COALESCE($13, colors),
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			b.ID, a.WorkspaceID, req.Name, req.Handle, req.Website, domains, req.Platforms, req.Status, req.Links, req.BusinessModel, req.RevenueModels,
			req.LogoURL, req.Colors)
		if err != nil {
			return brandUnique(err)
		}
		if req.Website != nil && *req.Website != "" && *req.Website != b.Website {
			if _, err := s.enqueueLinkDiscovery(ctx, tx, a.WorkspaceID, b.ID, "website:"+*req.Website); err != nil {
				return err
			}
		}
		if req.Links != nil {
			if err := s.enqueuePublicSocialSync(ctx, tx, a.WorkspaceID, "links:"+newID()); err != nil {
				return err
			}
		}
		// Archiving is its own event, so it can be counted.
		if req.Status != nil && *req.Status != b.Status {
			action := "brand.archived"
			if *req.Status == "active" {
				action = "brand.restored"
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "brand", b.ID, map[string]any{}); err != nil {
				return err
			}
		}
		// A new business model is its own event: report packs follow it.
		old := ""
		if b.BusinessModel != nil {
			old = *b.BusinessModel
		}
		if req.BusinessModel != nil && *req.BusinessModel != old {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "brand.business_model_changed", "brand", b.ID,
				map[string]any{"from": old, "to": *req.BusinessModel}); err != nil {
				return err
			}
		}
		var fields []string
		for name, set := range map[string]bool{"name": req.Name != nil, "handle": req.Handle != nil, "website": req.Website != nil,
			"domains": req.Domains != nil, "platforms": req.Platforms != nil, "links": req.Links != nil,
			"business_model": req.BusinessModel != nil, "revenue_models": req.RevenueModels != nil,
			"logo_url": req.LogoURL != nil, "colors": req.Colors != nil} {
			if set {
				fields = append(fields, name)
			}
		}
		if len(fields) == 0 {
			return nil
		}
		slices.Sort(fields)
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.updated", "brand", b.ID, map[string]any{"fields": fields})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadBrands(ctx, a.WorkspaceID, &b.ID, "all")
	if err != nil || len(list) != 1 {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// ---- Brand context ----

// brandContextFields are the brand context's text fields, in the order they
// are shown. Rules add to the company's; the rest win over the company's
// where set.
var brandContextFields = []struct {
	key, label string
	max        int
}{
	{"about", "What the brand is", 4000},
	{"audience", "Who it is for", 4000},
	{"goals", "Goals", 4000},
	{"voice", "Voice", 4000},
	{"rules", "Rules", 4000},
	{"notes", "Notes", 100000},
}

type brandContextView struct {
	BrandID   string      `json:"brand_id"`
	Name      string      `json:"name"`
	Handle    string      `json:"handle"`
	Website   string      `json:"website"`
	Platforms []string    `json:"platforms"`
	Links     []brandLink `json:"links"`
	About     string      `json:"about"`
	Audience  string      `json:"audience"`
	Goals     string      `json:"goals"`
	Voice     string      `json:"voice"`
	Rules     string      `json:"rules"`
	Notes     string      `json:"notes"`
	Version   int         `json:"version"`
	UpdatedAt *time.Time  `json:"updated_at"`
	UpdatedBy *string     `json:"updated_by"`
}

func (v *brandContextView) field(key string) *string {
	switch key {
	case "about":
		return &v.About
	case "audience":
		return &v.Audience
	case "goals":
		return &v.Goals
	case "voice":
		return &v.Voice
	case "rules":
		return &v.Rules
	case "notes":
		return &v.Notes
	}
	return nil
}

// loadBrandContext reads one brand's context. A brand that never saved one
// has an empty context at version 1.
func (s *Server) loadBrandContext(ctx context.Context, ws, id string) (brandContextView, error) {
	v := brandContextView{Version: 1}
	err := s.pool.QueryRow(ctx, `
		SELECT b.id, b.name, b.handle, b.website, b.platforms, b.links,
		       COALESCE(c.about, ''), COALESCE(c.audience, ''), COALESCE(c.goals, ''), COALESCE(c.voice, ''),
		       COALESCE(c.rules, ''), COALESCE(c.notes, ''), COALESCE(c.version, 1), c.updated_at, ac.display_name
		FROM brands b
		LEFT JOIN brand_context c ON c.brand_id = b.id
		LEFT JOIN actors ac ON ac.id = c.updated_by
		WHERE b.id = $1 AND b.workspace_id = $2`, id, ws,
	).Scan(&v.BrandID, &v.Name, &v.Handle, &v.Website, &v.Platforms, &v.Links, &v.About, &v.Audience, &v.Goals, &v.Voice,
		&v.Rules, &v.Notes, &v.Version, &v.UpdatedAt, &v.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) getBrandContext(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	v, err := s.loadBrandContext(r.Context(), a.WorkspaceID, b.ID)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type brandContextInput struct {
	About    *string `json:"about"`
	Audience *string `json:"audience"`
	Goals    *string `json:"goals"`
	Voice    *string `json:"voice"`
	Rules    *string `json:"rules"`
	Notes    *string `json:"notes"`
	// The version the edit was made against. When set and the context has
	// changed since, the update fails with 409 instead of overwriting.
	Version *int `json:"version"`
}

func (in *brandContextInput) field(key string) *string {
	return map[string]*string{
		"about": in.About, "audience": in.Audience, "goals": in.Goals,
		"voice": in.Voice, "rules": in.Rules, "notes": in.Notes,
	}[key]
}

func (s *Server) updateBrandContext(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req brandContextInput
	if !decode(w, r, &req) {
		return
	}
	for _, f := range brandContextFields {
		if p := req.field(f.key); p != nil {
			*p = strings.TrimSpace(*p)
			if len(*p) > f.max {
				writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%s must be at most %d characters", f.key, f.max))
				return
			}
		}
	}
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO brand_context (brand_id, workspace_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, b.ID, a.WorkspaceID); err != nil {
			return err
		}
		var cur brandContextView
		err := tx.QueryRow(ctx, `
			SELECT about, audience, goals, voice, rules, notes, version
			FROM brand_context WHERE brand_id = $1 FOR UPDATE`, b.ID,
		).Scan(&cur.About, &cur.Audience, &cur.Goals, &cur.Voice, &cur.Rules, &cur.Notes, &cur.Version)
		if err != nil {
			return err
		}
		var changed []string
		for _, f := range brandContextFields {
			if p := req.field(f.key); p != nil && *p != *cur.field(f.key) {
				changed = append(changed, f.key)
			}
		}
		if len(changed) == 0 {
			return nil
		}
		if req.Version != nil && *req.Version != cur.Version {
			return errStaleBrief
		}
		_, err = tx.Exec(ctx, `
			UPDATE brand_context SET
				about = COALESCE($2, about), audience = COALESCE($3, audience), goals = COALESCE($4, goals),
				voice = COALESCE($5, voice), rules = COALESCE($6, rules), notes = COALESCE($7, notes),
				version = version + 1, updated_at = now(), updated_by = $8
			WHERE brand_id = $1`,
			b.ID, req.About, req.Audience, req.Goals, req.Voice, req.Rules, req.Notes, a.ActorID)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.context_updated", "brand", b.ID,
			map[string]any{"fields": changed, "version": cur.Version + 1})
	})
	if errors.Is(err, errStaleBrief) {
		writeError(w, r, http.StatusConflict, "context_changed", "the brand's context changed since you loaded it; reload it and apply your edit again")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadBrandContext(ctx, a.WorkspaceID, b.ID)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- Moving records between brands ----

// brandMembers names records to move into or out of a brand.
type brandMembers struct {
	ProjectIDs  []string `json:"project_ids"`
	Domains     []string `json:"domains"`
	MailboxIDs  []string `json:"mailbox_ids"`
	LeadListIDs []string `json:"lead_list_ids"`
	SequenceIDs []string `json:"sequence_ids"`
	MissionIDs  []string `json:"mission_ids"`
	// Moving a domain moves its mailboxes too, unless this is false.
	WithMailboxes *bool `json:"with_mailboxes"`
}

type brandMoved struct {
	Projects  int `json:"projects"`
	Domains   int `json:"domains"`
	Mailboxes int `json:"mailboxes"`
	LeadLists int `json:"lead_lists"`
	Sequences int `json:"sequences"`
	Missions  int `json:"missions"`
	// Named records left as they were because they are in a project: they
	// take the project's brand, so move the project instead.
	InProject int `json:"in_project"`
}

func (m *brandMembers) check(a *access) error {
	n := len(m.ProjectIDs) + len(m.Domains) + len(m.MailboxIDs) + len(m.LeadListIDs) + len(m.SequenceIDs) + len(m.MissionIDs)
	if n == 0 {
		return errInvalid("name at least one of project_ids, domains, mailbox_ids, lead_list_ids, sequence_ids or mission_ids")
	}
	if n > 10000 {
		return errInvalid("move at most 10000 records at a time")
	}
	for i, d := range m.Domains {
		m.Domains[i] = strings.ToLower(strings.TrimSpace(d))
	}
	for _, ids := range [][]string{m.ProjectIDs, m.MailboxIDs, m.LeadListIDs, m.SequenceIDs, m.MissionIDs} {
		if !allUUIDs(ids) {
			return errInvalid("ids must be UUIDs")
		}
	}
	need := func(n int, scope string) error {
		if n > 0 && !a.has(scope) {
			return errDenied("moving these also needs scope " + scope)
		}
		return nil
	}
	return errors.Join(
		need(len(m.Domains)+len(m.MailboxIDs), auth.ScopeMailboxesManage),
		need(len(m.LeadListIDs), auth.ScopeLeadsManage),
		need(len(m.SequenceIDs), auth.ScopeSequencesManage),
		need(len(m.MissionIDs), auth.ScopeMissionsRun),
	)
}

func (s *Server) addToBrand(w http.ResponseWriter, r *http.Request) {
	s.moveBrandMembers(w, r, true)
}

func (s *Server) removeFromBrand(w http.ResponseWriter, r *http.Request) {
	s.moveBrandMembers(w, r, false)
}

// moveBrandMembers sets brand_id on the named records: to the brand when
// adding (taking them from any other brand), to null when removing (only
// records in this brand). A project brings everything in it along. Unknown
// names are ignored and not counted.
func (s *Server) moveBrandMembers(w http.ResponseWriter, r *http.Request, add bool) {
	a := accessFrom(r.Context())
	var req brandMembers
	if !decode(w, r, &req) {
		return
	}
	if err := req.check(a); s.allocError(w, r, err) {
		return
	}
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	withMailboxes := req.WithMailboxes == nil || *req.WithMailboxes
	ctx := r.Context()
	var moved brandMoved
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// $3 is the new brand; $4 the brand records must be in now (null:
		// any). Adding moves from anywhere, removing only from here.
		var to, from *string
		if add {
			to = &b.ID
		} else {
			from = &b.ID
		}
		const notSame = `brand_id IS DISTINCT FROM $3 AND ($4::uuid IS NULL OR brand_id = $4)`
		run := func(n *int, table, match string, ids any) error {
			tag, err := tx.Exec(ctx, `UPDATE `+table+` SET brand_id = $3 WHERE workspace_id = $1 AND `+match+` AND `+notSame+` AND project_id IS NULL`,
				a.WorkspaceID, ids, to, from)
			if err != nil {
				return err
			}
			*n += int(tag.RowsAffected())
			var held int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE workspace_id = $1 AND `+match+` AND project_id IS NOT NULL`,
				a.WorkspaceID, ids).Scan(&held); err != nil {
				return err
			}
			moved.InProject += held
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE projects SET brand_id = $3, updated_at = now()
			WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND `+notSame, a.WorkspaceID, req.ProjectIDs, to, from)
		if err != nil {
			return err
		}
		moved.Projects = int(tag.RowsAffected())
		steps := []struct {
			n            *int
			table, match string
			ids          any
		}{
			{&moved.Domains, "sending_domains", "domain = ANY($2::text[])", req.Domains},
			{&moved.Mailboxes, "mailboxes", "id = ANY($2::uuid[])", req.MailboxIDs},
			{&moved.LeadLists, "lead_lists", "id = ANY($2::uuid[])", req.LeadListIDs},
			{&moved.Sequences, "sequences", "id = ANY($2::uuid[])", req.SequenceIDs},
			{&moved.Missions, "missions", "id = ANY($2::uuid[])", req.MissionIDs},
		}
		if withMailboxes && len(req.Domains) > 0 {
			steps = append(steps, struct {
				n            *int
				table, match string
				ids          any
			}{&moved.Mailboxes, "mailboxes", "domain = ANY($2::text[])", req.Domains})
		}
		for _, st := range steps {
			if err := run(st.n, st.table, st.match, st.ids); err != nil {
				return err
			}
		}
		if moved.Projects+moved.Domains+moved.Mailboxes+moved.LeadLists+moved.Sequences+moved.Missions == 0 {
			return nil
		}
		// Mailboxes and sequences changing brand changes which mailbox may
		// serve which sequence.
		if moved.Projects > 0 || moved.Mailboxes > 0 || moved.Sequences > 0 {
			if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
				return err
			}
		}
		action := "brand.records_added"
		if !add {
			action = "brand.records_removed"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "brand", b.ID, map[string]any{
			"project_ids": req.ProjectIDs, "domains": len(req.Domains), "mailbox_ids": req.MailboxIDs,
			"lead_list_ids": req.LeadListIDs, "sequence_ids": req.SequenceIDs, "mission_ids": req.MissionIDs, "moved": moved,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved})
}

// brandRef resolves a brand id or handle given in a request body, for
// records that take a brand. Nil stays nil (shared).
func (s *Server) brandRef(ctx context.Context, ws string, ref *string) (*string, error) {
	if ref == nil {
		return nil, nil
	}
	id, err := s.brandIDFor(ctx, ws, *ref)
	if errors.Is(err, errNotFound) {
		return nil, errInvalid("brand not found")
	}
	return &id, err
}

// errSharedRecord refuses a brand-limited caller a change to a record every
// brand shares.
var errSharedRecord = errDenied("this is shared by every brand; only someone with access to every brand can change it")

// brandWrite checks a brand-limited caller may put a record in project and
// brand, and returns the brand to use: the project's when there is one, and
// the caller's only brand when none is given. Others pass brand through.
func brandWrite(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, a *access, project, brand *string) (*string, error) {
	if a.Brands == nil {
		return brand, nil
	}
	if project != nil {
		var own *string
		err := tx.QueryRow(ctx, `SELECT brand_id::text FROM projects WHERE id = $1 AND workspace_id = $2`, *project, a.WorkspaceID).Scan(&own)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.writes(nil, own)) {
			return nil, errInvalid("project not found")
		}
		return own, err
	}
	if brand == nil {
		if len(a.Brands) == 1 {
			return &a.Brands[0], nil
		}
		return nil, errInvalid("brand_id is required: you can only add records to your own brands")
	}
	if !a.writes(nil, brand) {
		return nil, errInvalid("brand not found")
	}
	return brand, nil
}

// listBusinessModels is the list a brand's business model and revenue
// models are picked from. It is the same everywhere, so it needs no sign-in.
func (s *Server) listBusinessModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, brands.All())
}

// brandHosts lists the websites (hosts) a brand filter lets through, from
// the hosts query returns for the workspace ($1). nil means every host.
func (s *Server) brandHosts(ctx context.Context, ws string, sel brandSel, query string) ([]string, error) {
	if sel.all() {
		return nil, nil
	}
	inBrand, err := s.hostBrandFilter(ctx, ws, sel)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, query, ws)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, h := range all {
		if inBrand(h) {
			out = append(out, h)
		}
	}
	return out, nil
}

// needHostInBrand writes a 404 unless a website (host) belongs to a brand
// filter's brand: by the brand's domains, or because an analytics site the
// brand owns reads it.
func (s *Server) needHostInBrand(w http.ResponseWriter, r *http.Request, ws, host string, sel brandSel) bool {
	if sel.all() {
		return true
	}
	inBrand, err := s.hostBrandFilter(r.Context(), ws, sel)
	if err == nil && inBrand(host) {
		return true
	}
	var mapped bool
	if err == nil {
		args := []any{ws, host}
		err = s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM analytics_syncs a WHERE a.workspace_id = $1 AND a.host = $2`+
			sel.sql(`analytics_site_brand(a.workspace_id, a.integration_id, a.site, a.host)`, &args)+`)`, args...).Scan(&mapped)
	}
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if !mapped {
		writeError(w, r, http.StatusNotFound, "not_found", host+" is not one of this brand's websites")
	}
	return mapped
}

// servesSQL is the filter's condition on a connection, row alias i: the
// connections its brand uses, as integrationsFor keeps them.
func (b brandSel) servesSQL(i string, args *[]any) string {
	out := ""
	switch b.id {
	case "":
	case "none":
		out = fmt.Sprintf(` AND %s.brand_id IS NULL AND cardinality(%s.brand_ids) = 0`, i, i)
	default:
		*args = append(*args, b.id)
		out = fmt.Sprintf(` AND integration_serves(%s, $%d::uuid)`, i, len(*args))
	}
	if b.in != nil {
		*args = append(*args, b.in)
		out += fmt.Sprintf(` AND EXISTS (SELECT 1 FROM unnest($%d::uuid[]) b WHERE integration_serves(%s, b))`, len(*args), i)
	}
	return out
}
