package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Help centres: the knowledge marked for the help centre (public and
// published), as a public site customers read and search themselves. A
// workspace has one per brand, or one for everything; each is off until
// turned on and served at /h/<slug>/ on this server or on a domain of its
// own (help_centre_site.go). Everything about its look and layout is a
// setting with a good default, so a new help centre looks finished before
// anyone changes a thing; the brand's (or the company's) logo and colour
// are its starting point.

func (s *Server) routesHelpCentres(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/help-centres", s.inBrand(read, s.listHelpCentres))
	mux.HandleFunc("POST /v1/workspaces/{ws}/help-centres", s.inBrand(manage, s.createHelpCentre))
	mux.HandleFunc("GET /v1/workspaces/{ws}/help-centres/{id}", s.inBrand(read, s.getHelpCentre))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/help-centres/{id}", s.inBrand(manage, s.updateHelpCentre))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/help-centres/{id}", s.inBrand(manage, s.deleteHelpCentre))
	mux.HandleFunc("POST /v1/workspaces/{ws}/help-centres/{id}/preview", s.inBrand(read, s.previewHelpCentre))
	mux.HandleFunc("GET /v1/workspaces/{ws}/help-centres/{id}/stats", s.inBrand(read, s.helpCentreStats))
}

type helpLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// helpSettings are a help centre's look, layout and options.
type helpSettings struct {
	// The preset it started from (classic, minimal or bold); only a label.
	Template string `json:"template"`

	// General.
	// The site's title, in the browser tab and search results; the help
	// centre's name when empty.
	Title string `json:"title"`
	// The language its pages are in (a code such as en or fr-ca).
	Language string `json:"language"`
	// Whether search engines may list it (and its sitemap is served).
	Indexing bool `json:"indexing"`
	// The picture shown when a page is shared, and the browser tab icon.
	SocialImageURL string `json:"social_image_url"`
	FaviconURL     string `json:"favicon_url"`

	// Shared elements.
	Color      string `json:"color"`
	Background string `json:"background"`
	Font       string `json:"font"`
	Radius     string `json:"radius"`
	// Show who keeps each article right (its owner) on articles.
	ShowAuthors bool `json:"show_authors"`

	// Header.
	LogoURL     string     `json:"logo_url"`
	ShowTitle   bool       `json:"show_title"`
	HeaderColor string     `json:"header_color"`
	HeaderLinks []helpLink `json:"header_links"`
	// "Go to <website>" at the right of the header.
	WebsiteURL string `json:"website_url"`

	// Hero block.
	HeroTitle         string `json:"hero_title"`
	HeroSubtitle      string `json:"hero_subtitle"`
	HeroImageURL      string `json:"hero_image_url"`
	SearchPlaceholder string `json:"search_placeholder"`

	// Layout and sections of the home page.
	CollectionsLayout string `json:"collections_layout"`
	Columns           int    `json:"columns"`
	ShowPopular       bool   `json:"show_popular"`
	ShowFAQ           bool   `json:"show_faq"`
	ContactTitle      string `json:"contact_title"`
	ContactText       string `json:"contact_text"`
	ContactLabel      string `json:"contact_label"`
	// mailto: or https; no contact block when empty.
	ContactURL string `json:"contact_url"`

	// Collection pages.
	ShowDescriptions bool `json:"show_descriptions"`
	ShowExcerpts     bool `json:"show_excerpts"`
	ShowCounts       bool `json:"show_counts"`

	// Article pages.
	TableOfContents bool `json:"table_of_contents"`
	Related         bool `json:"related"`
	CopyForLLM      bool `json:"copy_for_llm"`
	Feedback        bool `json:"feedback"`
	ShowUpdated     bool `json:"show_updated"`

	// Footer.
	FooterLayout string            `json:"footer_layout"`
	FooterText   string            `json:"footer_text"`
	FooterColor  string            `json:"footer_color"`
	FooterLinks  []helpLink        `json:"footer_links"`
	Social       map[string]string `json:"social"`
}

var (
	helpTemplates  = []string{"classic", "minimal", "bold"}
	helpFonts      = []string{"system", "inter", "roboto", "open-sans", "lato", "source-sans", "ibm-plex-sans", "dm-sans", "nunito", "merriweather", "lora"}
	helpRadii      = []string{"none", "small", "medium", "large"}
	helpLayouts    = []string{"grid", "list"}
	helpFooters    = []string{"simple", "columns"}
	helpSocials    = []string{"x", "linkedin", "youtube", "facebook", "instagram", "github"}
	helpLanguageRE = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)
)

// helpDefaults are what a help centre looks like before anyone changes it.
func helpDefaults() helpSettings {
	return helpSettings{
		Template: "classic", Language: "en", Indexing: true,
		Color: "#4f46e5", Background: "#ffffff", Font: "system", Radius: "medium",
		ShowTitle: true, HeaderLinks: []helpLink{},
		HeroTitle: "How can we help?", SearchPlaceholder: "Search for articles…",
		CollectionsLayout: "grid", Columns: 3, ShowPopular: true, ShowFAQ: true,
		ContactTitle: "Still need help?", ContactText: "Our team is happy to answer.", ContactLabel: "Contact us",
		ShowDescriptions: true, ShowExcerpts: true, ShowCounts: true,
		TableOfContents: true, Related: true, CopyForLLM: true, Feedback: true, ShowUpdated: true,
		FooterLayout: "simple", FooterLinks: []helpLink{}, Social: map[string]string{},
	}
}

// helpSettingsFrom reads stored settings over the defaults.
func helpSettingsFrom(raw []byte) helpSettings {
	h := helpDefaults()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &h)
	}
	if h.HeaderLinks == nil {
		h.HeaderLinks = []helpLink{}
	}
	if h.FooterLinks == nil {
		h.FooterLinks = []helpLink{}
	}
	if h.Social == nil {
		h.Social = map[string]string{}
	}
	return h
}

// helpURL checks an address shown on the help centre: https (or mailto:
// where mail is allowed), or "" for none.
func helpURL(field, raw string, mail bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	ok := err == nil && len(raw) <= 2000 && ((u.Scheme == "https" && u.Host != "") || (mail && u.Scheme == "mailto" && strings.Contains(u.Opaque, "@")))
	if !ok {
		if mail {
			return "", errInvalid(field + " is an https address or a mailto: address")
		}
		return "", errInvalid(field + " is an https address")
	}
	return u.String(), nil
}

func helpText(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > max {
		return "", errInvalid(field + " is at most " + strconv.Itoa(max) + " characters")
	}
	return v, nil
}

func helpColor(field, v string, empty bool) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if (v == "" && empty) || lookColor.MatchString(v) {
		return v, nil
	}
	return "", errInvalid(field + " is a hex colour such as #4f46e5")
}

func helpLinks(field string, list []helpLink, max int) ([]helpLink, error) {
	if len(list) > max {
		return nil, errInvalid(field + " has at most " + strconv.Itoa(max) + " links")
	}
	out := []helpLink{}
	for _, l := range list {
		label, err := helpText(field+" label", l.Label, 40)
		if err != nil {
			return nil, err
		}
		u, err := helpURL(field+" url", l.URL, true)
		if err != nil {
			return nil, err
		}
		if label == "" || u == "" {
			return nil, errInvalid(field + " need a label and an address")
		}
		out = append(out, helpLink{Label: label, URL: u})
	}
	return out, nil
}

// check validates the settings and tidies them.
func (h *helpSettings) check() error {
	var err error
	texts := []struct {
		field string
		p     *string
		max   int
	}{
		{"title", &h.Title, 100}, {"hero_title", &h.HeroTitle, 120}, {"hero_subtitle", &h.HeroSubtitle, 300},
		{"search_placeholder", &h.SearchPlaceholder, 80}, {"contact_title", &h.ContactTitle, 80}, {"contact_text", &h.ContactText, 300},
		{"contact_label", &h.ContactLabel, 40}, {"footer_text", &h.FooterText, 300},
	}
	for _, t := range texts {
		if *t.p, err = helpText(t.field, *t.p, t.max); err != nil {
			return err
		}
	}
	urls := []struct {
		field string
		p     *string
		mail  bool
	}{
		{"social_image_url", &h.SocialImageURL, false}, {"favicon_url", &h.FaviconURL, false}, {"logo_url", &h.LogoURL, false},
		{"website_url", &h.WebsiteURL, false}, {"hero_image_url", &h.HeroImageURL, false}, {"contact_url", &h.ContactURL, true},
	}
	for _, u := range urls {
		if *u.p, err = helpURL(u.field, *u.p, u.mail); err != nil {
			return err
		}
	}
	colors := []struct {
		field string
		p     *string
		empty bool
	}{{"color", &h.Color, false}, {"background", &h.Background, false}, {"header_color", &h.HeaderColor, true}, {"footer_color", &h.FooterColor, true}}
	for _, c := range colors {
		if *c.p, err = helpColor(c.field, *c.p, c.empty); err != nil {
			return err
		}
	}
	h.Language = strings.ToLower(strings.TrimSpace(h.Language))
	switch {
	case !slices.Contains(helpTemplates, h.Template):
		return errInvalid("template is " + strings.Join(helpTemplates, ", "))
	case !helpLanguageRE.MatchString(h.Language):
		return errInvalid("language is a language code such as en or fr-ca")
	case !slices.Contains(helpFonts, h.Font):
		return errInvalid("font is " + strings.Join(helpFonts, ", "))
	case !slices.Contains(helpRadii, h.Radius):
		return errInvalid("radius is " + strings.Join(helpRadii, ", "))
	case !slices.Contains(helpLayouts, h.CollectionsLayout):
		return errInvalid("collections_layout is grid or list")
	case h.Columns < 1 || h.Columns > 3:
		return errInvalid("columns is 1, 2 or 3")
	case !slices.Contains(helpFooters, h.FooterLayout):
		return errInvalid("footer_layout is simple or columns")
	}
	if h.HeaderLinks, err = helpLinks("header_links", h.HeaderLinks, 4); err != nil {
		return err
	}
	if h.FooterLinks, err = helpLinks("footer_links", h.FooterLinks, 12); err != nil {
		return err
	}
	social := map[string]string{}
	for k, v := range h.Social {
		if !slices.Contains(helpSocials, k) {
			return errInvalid("social networks are " + strings.Join(helpSocials, ", "))
		}
		u, err := helpURL("social."+k, v, false)
		if err != nil {
			return err
		}
		if u != "" {
			social[k] = u
		}
	}
	h.Social = social
	return nil
}

type helpCentreView struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Slug    string  `json:"slug"`
	BrandID *string `json:"brand_id"`
	Enabled bool    `json:"enabled"`
	// Where customers find it: its own domain once that works, else its
	// address on this server.
	URL      string       `json:"url"`
	DomainID *string      `json:"domain_id"`
	Domain   *string      `json:"domain"`
	Settings helpSettings `json:"settings"`
	// Published articles and quick answers marked for the help centre.
	Articles  int       `json:"articles"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const helpCentreSelect = `
	SELECT h.id::text, h.name, h.slug, h.brand_id::text, h.enabled, h.domain_id::text,
	       CASE WHEN d.status = 'verified' AND (d.https_status IS NULL OR d.https_status = 'active') THEN d.domain END, d.domain,
	       h.settings, h.created_at, h.updated_at,
	       (SELECT count(*) FROM knowledge_items i WHERE i.workspace_id = h.workspace_id AND i.deleted_at IS NULL
	          AND i.status = 'published' AND i.visibility = 'public' AND (h.brand_id IS NULL OR i.brand_id IS NULL OR i.brand_id = h.brand_id))::int
	FROM help_centres h LEFT JOIN tracking_domains d ON d.id = h.domain_id`

func (s *Server) scanHelpCentre(row pgx.Row) (helpCentreView, error) {
	var v helpCentreView
	var live *string
	var raw []byte
	err := row.Scan(&v.ID, &v.Name, &v.Slug, &v.BrandID, &v.Enabled, &v.DomainID, &live, &v.Domain, &raw, &v.CreatedAt, &v.UpdatedAt, &v.Articles)
	v.Settings = helpSettingsFrom(raw)
	v.URL = strings.TrimRight(s.publicURL, "/") + "/h/" + v.Slug + "/"
	if live != nil {
		v.URL = "https://" + *live + "/"
	}
	return v, err
}

func (s *Server) listHelpCentres(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), helpCentreSelect+` WHERE h.workspace_id = $1`+brand.sql("h.brand_id", &args)+` ORDER BY h.created_at`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (helpCentreView, error) { return s.scanHelpCentre(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []helpCentreView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// helpCentreFor reads one help centre the caller may see.
func (s *Server) helpCentreFor(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, a *access, id string) (helpCentreView, error) {
	if !validUUID(id) {
		return helpCentreView{}, errNotFound
	}
	v, err := s.scanHelpCentre(q.QueryRow(ctx, helpCentreSelect+` WHERE h.workspace_id = $1 AND h.id = $2`, a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.Brands != nil && v.BrandID != nil && !slices.Contains(a.Brands, *v.BrandID)) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) getHelpCentre(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.helpCentreFor(r.Context(), s.pool, a, r.PathValue("id"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type helpCentreInput struct {
	Name     *string          `json:"name"`
	Slug     *string          `json:"slug"`
	BrandID  optional[string] `json:"brand_id"`
	Enabled  *bool            `json:"enabled"`
	DomainID optional[string] `json:"domain_id"`
	// Merged over the current settings: only the fields given change.
	Settings json.RawMessage `json:"settings"`
}

// helpSlug turns a name into an address: "Northpeak Help" → northpeak-help.
func helpSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 50 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "help"
	}
	return out
}

func (s *Server) createHelpCentre(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in helpCentreInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	brand, err := s.brandRef(ctx, a.WorkspaceID, in.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if brand, err = brandWrite(ctx, tx, a, brand); err != nil {
			return err
		}
		// It starts from the brand's look, or the company's.
		var name, logo string
		var colors lookColors
		var rawColors []byte
		if brand != nil {
			err = tx.QueryRow(ctx, `SELECT b.name, COALESCE(NULLIF(b.logo_url, ''), c.logo_url, ''), b.colors FROM brands b
				LEFT JOIN workspace_context c ON c.workspace_id = b.workspace_id WHERE b.id = $1`, *brand).Scan(&name, &logo, &rawColors)
		} else {
			err = tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(c.company, ''), w.name), COALESCE(c.logo_url, ''), COALESCE(c.colors, '{}')
				FROM workspaces w LEFT JOIN workspace_context c ON c.workspace_id = w.id WHERE w.id = $1`, a.WorkspaceID).Scan(&name, &logo, &rawColors)
		}
		if err != nil {
			return err
		}
		_ = json.Unmarshal(rawColors, &colors)
		set := helpDefaults()
		set.LogoURL = logo
		if lookColor.MatchString(colors.Primary) {
			set.Color = colors.Primary
		}
		if len(in.Settings) > 0 {
			if err := json.Unmarshal(in.Settings, &set); err != nil {
				return errInvalid("settings: " + err.Error())
			}
		}
		if err := set.check(); err != nil {
			return err
		}
		if in.Name != nil {
			name = *in.Name
		} else {
			name += " Help Center"
		}
		if name, err = helpText("name", name, 100); err != nil {
			return err
		}
		if name == "" {
			return errInvalid("name is required")
		}
		slug := helpSlug(name)
		if in.Slug != nil {
			slug = strings.ToLower(strings.TrimSpace(*in.Slug))
			if !subdomainRE.MatchString(slug) {
				return errInvalid("slug is lowercase letters, digits and dashes, such as northpeak-help")
			}
		} else {
			base := slug
			for n := 2; ; n++ {
				var taken bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM help_centres WHERE slug = $1)`, slug).Scan(&taken); err != nil {
					return err
				}
				if !taken {
					break
				}
				slug = base + "-" + strconv.Itoa(n)
			}
		}
		raw, _ := json.Marshal(set)
		err := tx.QueryRow(ctx, `INSERT INTO help_centres (workspace_id, brand_id, name, slug, enabled, settings, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
			a.WorkspaceID, brand, name, slug, in.Enabled != nil && *in.Enabled, raw, nilIfEmpty(a.ActorID)).Scan(&id)
		if isUniqueViolation(err) {
			return errInvalid("this brand already has a help centre, or the address is taken")
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "help_centre.created", "help_centre", id, map[string]any{"name": name, "slug": slug, "brand_id": brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.helpCentreFor(ctx, s.pool, a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateHelpCentre(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in helpCentreInput
	if !decode(w, r, &in) {
		return
	}
	if in.BrandID.Set {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a help centre's brand can't change; make another for the other brand")
		return
	}
	ctx := r.Context()
	var v helpCentreView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.helpCentreFor(ctx, tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errDenied("you can only change your own brands' help centres")
		}
		changed := map[string]any{}
		name, slug, enabled, domain, set := cur.Name, cur.Slug, cur.Enabled, cur.DomainID, cur.Settings
		if in.Name != nil {
			if name, err = helpText("name", *in.Name, 100); err != nil {
				return err
			}
			if name == "" {
				return errInvalid("name can't be empty")
			}
			changed["name"] = name
		}
		if in.Slug != nil {
			slug = strings.ToLower(strings.TrimSpace(*in.Slug))
			if !subdomainRE.MatchString(slug) {
				return errInvalid("slug is lowercase letters, digits and dashes, such as northpeak-help")
			}
			changed["slug"] = slug
		}
		if in.Enabled != nil {
			enabled = *in.Enabled
			changed["enabled"] = enabled
		}
		if in.DomainID.Set {
			domain = in.DomainID.Value
			if domain != nil {
				var used bool
				err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_programs WHERE portal_domain_id = d.id)
					OR EXISTS (SELECT 1 FROM help_centres WHERE domain_id = d.id AND id <> $3)
					FROM tracking_domains d WHERE d.workspace_id = $1 AND d.id::text = $2`, a.WorkspaceID, *domain, cur.ID).Scan(&used)
				if errors.Is(err, pgx.ErrNoRows) {
					return errInvalid("domain_id: no such domain; add it under tracking domains first")
				}
				if err != nil {
					return err
				}
				if used {
					return errInvalid("that domain already serves a partner portal or another help centre")
				}
			}
			changed["domain_id"] = domain
		}
		if len(in.Settings) > 0 && string(in.Settings) != "null" {
			if err := json.Unmarshal(in.Settings, &set); err != nil {
				return errInvalid("settings: " + err.Error())
			}
			if err := set.check(); err != nil {
				return err
			}
			var keys map[string]json.RawMessage
			_ = json.Unmarshal(in.Settings, &keys)
			fields := make([]string, 0, len(keys))
			for k := range keys {
				fields = append(fields, k)
			}
			slices.Sort(fields)
			changed["settings"] = fields
		}
		raw, _ := json.Marshal(set)
		_, err = tx.Exec(ctx, `UPDATE help_centres SET name = $3, slug = $4, enabled = $5, domain_id = $6, settings = $7, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, name, slug, enabled, domain, raw)
		if isUniqueViolation(err) {
			return errInvalid("that address or domain is already taken")
		}
		if err != nil {
			return err
		}
		action := "help_centre.updated"
		switch {
		case in.Enabled != nil && enabled && !cur.Enabled:
			action = "help_centre.published"
		case in.Enabled != nil && !enabled && cur.Enabled:
			action = "help_centre.unpublished"
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "help_centre", cur.ID, changed); err != nil {
			return err
		}
		if in.DomainID.Set || in.Slug != nil || in.Enabled != nil {
			s.forgetHosts()
		}
		v, err = s.helpCentreFor(ctx, tx, a, cur.ID)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteHelpCentre(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.helpCentreFor(ctx, tx, a, r.PathValue("id"))
		if err != nil {
			return err
		}
		if !a.writes(cur.BrandID) {
			return errDenied("you can only delete your own brands' help centres")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM help_centres WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		s.forgetHosts()
		return record(ctx, tx, &a.principal, a.WorkspaceID, "help_centre.deleted", "help_centre", cur.ID, map[string]any{"name": cur.Name, "slug": cur.Slug})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewHelpCentre renders a page of the help centre as it would look
// with the given (unsaved) settings, on or off, for the editor's live
// preview. Links in it go nowhere and it runs no script.
func (s *Server) previewHelpCentre(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Settings json.RawMessage `json:"settings"`
		Name     *string         `json:"name"`
		// home, collection, article or search.
		Page string `json:"page"`
		// The folder or article to show; the first one when empty.
		Ref string `json:"ref"`
		Q   string `json:"q"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	v, err := s.helpCentreFor(ctx, s.pool, a, r.PathValue("id"))
	if s.allocError(w, r, err) {
		return
	}
	if len(in.Settings) > 0 && string(in.Settings) != "null" {
		if err := json.Unmarshal(in.Settings, &v.Settings); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "settings: "+err.Error())
			return
		}
		if err := v.Settings.check(); s.allocError(w, r, err) {
			return
		}
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		v.Name = strings.TrimSpace(*in.Name)
	}
	if in.Page == "" {
		in.Page = "home"
	}
	if !slices.Contains([]string{"home", "collection", "article", "search"}, in.Page) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "page is home, collection, article or search")
		return
	}
	site := s.helpSiteFor(a.WorkspaceID, v, "#", true)
	body, _, err := s.renderHelpPage(ctx, site, in.Page, in.Ref, in.Q, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src https: data:; style-src 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com")
	_, _ = w.Write(body)
}

type helpStatsDay struct {
	Day      string `json:"day"`
	Views    int    `json:"views"`
	Searches int    `json:"searches"`
}

type helpStatsArticle struct {
	ItemID    string `json:"item_id"`
	Title     string `json:"title"`
	Views     int    `json:"views"`
	Helpful   int    `json:"helpful"`
	Unhelpful int    `json:"unhelpful"`
}

// helpCentreStats are a help centre's numbers over the last days (30 by
// default): page views and searches by day, the searches that found
// nothing, and its most read articles with their feedback.
func (s *Server) helpCentreStats(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	v, err := s.helpCentreFor(ctx, s.pool, a, r.PathValue("id"))
	if s.allocError(w, r, err) {
		return
	}
	out := struct {
		Days          int                `json:"days"`
		Views         int                `json:"views"`
		Searches      int                `json:"searches"`
		EmptySearches int                `json:"empty_searches"`
		Helpful       int                `json:"helpful"`
		Unhelpful     int                `json:"unhelpful"`
		ByDay         []helpStatsDay     `json:"by_day"`
		Articles      []helpStatsArticle `json:"articles"`
	}{Days: days, ByDay: []helpStatsDay{}}
	rows, err := s.pool.Query(ctx, `
		SELECT g.day::date::text, COALESCE(d.views, 0), COALESCE(d.searches, 0), COALESCE(d.empty_searches, 0)
		FROM generate_series(current_date - ($3::int - 1), current_date, interval '1 day') g(day)
		LEFT JOIN help_centre_days d ON d.centre_id = $2 AND d.workspace_id = $1 AND d.day = g.day::date
		ORDER BY g.day`, a.WorkspaceID, v.ID, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for rows.Next() {
		var d helpStatsDay
		var empty int
		if err := rows.Scan(&d.Day, &d.Views, &d.Searches, &empty); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		out.Views += d.Views
		out.Searches += d.Searches
		out.EmptySearches += empty
		out.ByDay = append(out.ByDay, d)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err = s.pool.Query(ctx, `
		SELECT i.id::text, i.title, sum(d.views)::int, sum(d.helpful)::int, sum(d.unhelpful)::int
		FROM help_article_days d JOIN knowledge_items i ON i.id = d.item_id
		WHERE d.workspace_id = $1 AND d.day > current_date - $2::int AND i.deleted_at IS NULL
		  AND ($3::uuid IS NULL OR i.brand_id IS NULL OR i.brand_id = $3::uuid)
		GROUP BY i.id, i.title ORDER BY sum(d.views) DESC, i.title LIMIT 20`, a.WorkspaceID, days, v.BrandID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.Articles, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (helpStatsArticle, error) {
		var x helpStatsArticle
		err := row.Scan(&x.ItemID, &x.Title, &x.Views, &x.Helpful, &x.Unhelpful)
		return x, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out.Articles == nil {
		out.Articles = []helpStatsArticle{}
	}
	for _, x := range out.Articles {
		out.Helpful += x.Helpful
		out.Unhelpful += x.Unhelpful
	}
	writeJSON(w, http.StatusOK, out)
}
