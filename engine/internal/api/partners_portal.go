package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/partners"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// The partner portal: where partners sign up, sign in with a link emailed
// through the workspace's own email provider, and see their links, codes,
// referred customers (masked), rewards and payouts. Each program has its
// own, off until turned on, served at its subdomain of the operator's
// partners domain (OUTFLARE_PARTNERS_DOMAIN), on a tracking domain tied to
// the program, or at /p/<program id>/ on this server. On a portal's own
// host the host middleware maps / to /p/<program id>/, so these routes
// serve every address.
//
// The portal's JSON API lives beside its page and answers only the
// partner signed in to that program; it is the page's, not part of the
// workspace API.

//go:embed partner_portal.html
var partnerPortalHTML string

//go:embed partner_widget.html
var partnerWidgetHTML string

const (
	partnerSessionCookie   = "outflare_partner"
	partnerSessionLifetime = 30 * 24 * time.Hour
	partnerImpersonateLife = time.Hour
	partnerSignInLinkLife  = 30 * time.Minute
	partnerWidgetLife      = 12 * time.Hour
)

// portalPages are the pages a portal can show.
var portalPages = []string{"dashboard", "links", "assets", "referrals", "payouts", "settings"}

// portalField is one question on the portal's sign-up form.
type portalField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text, textarea, url or select
	Required bool     `json:"required"`
	Options  []string `json:"options"`
}

// portalSettings are a program portal's look, pages, sign-up form and
// sender.
type portalSettings struct {
	Title        string        `json:"title"`
	LogoURL      string        `json:"logo_url"`
	Color        string        `json:"color"`
	CustomCSS    string        `json:"custom_css"`
	Welcome      string        `json:"welcome"`
	Pages        []string      `json:"pages"`
	SignupOpen   bool          `json:"signup_open"`
	SignupFields []portalField `json:"signup_fields"`
	// EmailFrom sends sign-in links and notices, through the workspace's
	// own email provider; it must be on a verified sending domain.
	EmailFrom string `json:"email_from"`
	// SignInTemplate is an email template's id or handle for the sign-in
	// email, with sign_in_url, program_name and partner_name; Outflare's
	// own wording when empty.
	SignInTemplate string `json:"sign_in_template"`
}

// portalFor reads stored settings over the kind's defaults: affiliates get
// every page and open sign-up; referral programs (customers share a link
// or widget) only the dashboard and links; influencers read-only stats.
func portalFor(kind string, raw []byte) portalSettings {
	p := portalSettings{Color: "#111827", Pages: []string{"dashboard", "links", "assets", "referrals", "payouts", "settings"}, SignupOpen: true}
	switch kind {
	case partners.KindReferral:
		p.Pages, p.SignupOpen = []string{"dashboard", "links"}, false
	case partners.KindInfluencer:
		p.Pages, p.SignupOpen = []string{"dashboard", "links", "assets"}, false
	}
	def := p.Pages
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	if len(p.Pages) == 0 {
		p.Pages = def
	}
	if p.SignupFields == nil {
		p.SignupFields = []portalField{}
	}
	return p
}

var (
	colorRE      = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	fieldKeyRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	subdomainRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reservedSubs = []string{"www", "app", "api", "admin", "mail", "partners", "portal", "static", "assets"}
)

func (p *portalSettings) check() error {
	p.Title, p.Welcome = strings.TrimSpace(p.Title), strings.TrimSpace(p.Welcome)
	switch {
	case len(p.Title) > 120:
		return errInvalid("portal.title is at most 120 characters")
	case p.Color != "" && !colorRE.MatchString(p.Color):
		return errInvalid("portal.color is a hex colour such as #0f766e")
	case len(p.CustomCSS) > 20000 || strings.Contains(strings.ToLower(p.CustomCSS), "</style"):
		return errInvalid("portal.custom_css is at most 20,000 characters of CSS")
	case len(p.Welcome) > 4000:
		return errInvalid("portal.welcome is at most 4,000 characters")
	case len(p.SignupFields) > 20:
		return errInvalid("portal.signup_fields has at most 20 questions")
	case len(p.SignInTemplate) > 100:
		return errInvalid("portal.sign_in_template is a template's id or handle")
	}
	if p.LogoURL != "" {
		u, err := utm.Destination(p.LogoURL)
		if err != nil || !strings.HasPrefix(u, "https://") {
			return errInvalid("portal.logo_url is an https address of an image")
		}
		p.LogoURL = u
	}
	for _, pg := range p.Pages {
		if !slices.Contains(portalPages, pg) {
			return errInvalid("portal.pages are " + strings.Join(portalPages, ", "))
		}
	}
	if !slices.Contains(p.Pages, "dashboard") {
		p.Pages = append([]string{"dashboard"}, p.Pages...)
	}
	keys := map[string]bool{}
	for i, f := range p.SignupFields {
		f.Label = strings.TrimSpace(f.Label)
		switch {
		case !fieldKeyRE.MatchString(f.Key) || keys[f.Key]:
			return errInvalid("portal.signup_fields: each key is unique, lowercase letters, digits and underscores, such as website")
		case f.Label == "" || len(f.Label) > 200:
			return errInvalid("portal.signup_fields: each label is 1 to 200 characters")
		case !slices.Contains([]string{"text", "textarea", "url", "select"}, f.Type):
			return errInvalid("portal.signup_fields: type is text, textarea, url or select")
		case f.Type == "select" && (len(f.Options) == 0 || len(f.Options) > 20):
			return errInvalid("portal.signup_fields: a select has 1 to 20 options")
		}
		if f.Options == nil {
			f.Options = []string{}
		}
		keys[f.Key] = true
		p.SignupFields[i] = f
	}
	if p.EmailFrom != "" {
		if a, err := mail.ParseAddress(p.EmailFrom); err != nil || len(p.EmailFrom) > 320 || a.Address == "" {
			return errInvalid("portal.email_from is an address on a verified sending domain, such as Northpeak Partners <partners@northpeak.example>")
		}
	}
	return nil
}

// portalBase is where a program's portal is: its verified own domain, its
// subdomain of the partners domain, or /p/<id>/ on this server.
func (s *Server) portalBase(ctx context.Context, program string, subdomain *string) (string, error) {
	var domain string
	err := s.pool.QueryRow(ctx, `SELECT d.domain FROM partner_programs p JOIN tracking_domains d ON d.id = p.portal_domain_id
		WHERE p.id = $1 AND d.status = 'verified' AND (d.https_status IS NULL OR d.https_status = 'active')`, program).Scan(&domain)
	switch {
	case err == nil:
		return "https://" + domain + "/", nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	case subdomain != nil && s.partnersDomain != "":
		return "https://" + *subdomain + "." + s.partnersDomain + "/", nil
	}
	return strings.TrimRight(s.publicURL, "/") + "/p/" + program + "/", nil
}

// ---- Admin: the portal's settings, assets and impersonation ----

func (s *Server) routesPartnerPortal(mux *http.ServeMux) {
	read, manage := auth.ScopePartnersRead, auth.ScopePartnersManage
	mux.HandleFunc("PUT /v1/workspaces/{ws}/partner-programs/{id}/portal", s.inWorkspace(manage, s.setPartnerPortal))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-programs/{id}/assets", s.inWorkspace(read, s.listPartnerAssets))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-programs/{id}/assets", s.inWorkspace(manage, s.createPartnerAsset))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/partner-programs/{id}/assets/{asset}", s.inWorkspace(manage, s.deletePartnerAsset))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partners/{id}/impersonate", s.inWorkspace(manage, s.impersonatePartner))
	mux.HandleFunc("POST /v1/workspaces/{ws}/partner-programs/{id}/widget-sessions", s.inWorkspace(auth.ScopePartnersConversions, s.createWidgetSession))

	// The portal itself. Public: the session cookie names the partner.
	mux.HandleFunc("GET /p/{program}/{$}", s.portalPage)
	mux.HandleFunc("GET /p/{program}/embed", s.portalEmbed)
	mux.HandleFunc("GET /p/{program}/api/program", s.portalProgramInfo)
	mux.HandleFunc("POST /p/{program}/api/signup", s.portalSignup)
	mux.HandleFunc("POST /p/{program}/api/sign-in", s.portalSignIn)
	mux.HandleFunc("POST /p/{program}/api/session", s.portalSession)
	mux.HandleFunc("POST /p/{program}/api/sign-out", s.portalSignOut)
	mux.HandleFunc("GET /p/{program}/api/me", s.inPortal(s.portalMe))
	mux.HandleFunc("PATCH /p/{program}/api/me", s.inPortal(s.portalUpdateMe))
	mux.HandleFunc("GET /p/{program}/api/referrals", s.inPortal(s.portalReferrals))
	mux.HandleFunc("GET /p/{program}/api/rewards", s.inPortal(s.portalRewards))
	mux.HandleFunc("GET /p/{program}/api/payouts", s.inPortal(s.portalPayouts))
	mux.HandleFunc("GET /p/{program}/api/assets", s.inPortal(s.portalAssets))
}

// setPartnerPortal turns a program's portal on or off and sets its
// subdomain and settings. The settings sent replace the stored ones.
func (s *Server) setPartnerPortal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Enabled   *bool           `json:"enabled"`
		Subdomain *string         `json:"subdomain"`
		Settings  json.RawMessage `json:"settings"`
	}
	if !decode(w, r, &in) {
		return
	}
	settings := cur.Portal
	if len(in.Settings) > 0 && string(in.Settings) != "null" {
		// What is sent replaces the stored settings; what is left out
		// takes the kind's default.
		settings = portalFor(cur.Kind, nil)
		dec := json.NewDecoder(strings.NewReader(string(in.Settings)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&settings); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "settings: "+err.Error())
			return
		}
		if settings.SignupFields == nil {
			settings.SignupFields = []portalField{}
		}
		if s.allocError(w, r, settings.check()) {
			return
		}
	}
	sub := cur.PortalSubdomain
	if in.Subdomain != nil {
		v := strings.ToLower(strings.TrimSpace(*in.Subdomain))
		switch {
		case v == "":
			sub = nil
		case !subdomainRE.MatchString(v) || slices.Contains(reservedSubs, v):
			writeError(w, r, http.StatusBadRequest, "invalid_request", "subdomain is 1 to 63 lowercase letters, digits and hyphens, such as northpeak")
			return
		default:
			sub = &v
		}
	}
	enabled := cur.PortalEnabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	raw, _ := json.Marshal(settings)
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if sub != nil {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_programs WHERE portal_subdomain = $1 AND id <> $2)`, *sub, cur.ID).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return errConflict
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_programs SET portal_enabled = $3, portal_subdomain = $4, portal = $5, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, enabled, sub, raw); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.portal_updated", "partner_program", cur.ID,
			map[string]any{"kind": cur.Kind, "status": map[bool]string{true: "enabled", false: "disabled"}[enabled], "has_subdomain": sub != nil,
				"signup_open": settings.SignupOpen})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "another program already uses that subdomain")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	s.forgetHosts()
	if v, ok := s.findPartnerProgram(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// forgetHosts drops remembered host lookups, so a portal turned on, off or
// moved answers at once on this server (others within a minute).
func (s *Server) forgetHosts() {
	s.trackingHosts.mu.Lock()
	s.trackingHosts.hosts = nil
	s.trackingHosts.mu.Unlock()
}

type partnerAssetView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) programAssets(ctx context.Context, ws, program string) ([]partnerAssetView, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, kind, title, body, url, position, created_at FROM partner_assets
		WHERE workspace_id = $1 AND program_id = $2 ORDER BY position, created_at`, ws, program)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[partnerAssetView])
	if list == nil {
		list = []partnerAssetView{}
	}
	return list, err
}

func (s *Server) listPartnerAssets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	list, err := s.programAssets(r.Context(), a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// createPartnerAsset adds a banner (an image's address), copy or an email
// swipe (text), or a link for partners to use.
func (s *Server) createPartnerAsset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Kind     string `json:"kind"`
		Title    string `json:"title"`
		Body     string `json:"body"`
		URL      string `json:"url"`
		Position int    `json:"position"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	switch {
	case !slices.Contains([]string{"image", "text", "link"}, in.Kind):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is image, text or link")
		return
	case in.Title == "" || len(in.Title) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title is 1 to 200 characters")
		return
	case len(in.Body) > 10000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is at most 10,000 characters")
		return
	case in.Kind == "text" && strings.TrimSpace(in.Body) == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is the copy partners can paste")
		return
	}
	if in.Kind != "text" || in.URL != "" {
		u, err := utm.Destination(in.URL)
		if err != nil || !strings.HasPrefix(u, "https://") {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "url is an https address: the image for a banner, the page for a link")
			return
		}
		in.URL = u
	}
	ctx := r.Context()
	var v partnerAssetView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM partner_assets WHERE program_id = $1`, cur.ID).Scan(&n); err != nil {
			return err
		}
		if n >= 200 {
			return errInvalid("a program can have at most 200 assets")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO partner_assets (workspace_id, program_id, kind, title, body, url, position) VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id::text, kind, title, body, url, position, created_at`, a.WorkspaceID, cur.ID, in.Kind, in.Title, in.Body, in.URL, in.Position).
			Scan(&v.ID, &v.Kind, &v.Title, &v.Body, &v.URL, &v.Position, &v.CreatedAt); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.asset_added", "partner_program", cur.ID,
			map[string]any{"kind": in.Kind, "asset_id": v.ID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deletePartnerAsset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM partner_assets WHERE workspace_id = $1 AND program_id = $2 AND id::text = $3`, a.WorkspaceID, cur.ID, r.PathValue("asset"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.asset_removed", "partner_program", cur.ID,
			map[string]any{"asset_id": r.PathValue("asset")})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// impersonatePartner gives someone on the workspace's team a link that
// opens the portal as the partner sees it, read-only, for an hour. It is
// recorded in the audit log.
func (s *Server) impersonatePartner(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	if a.ActorID == "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person or an API token can open the portal as a partner")
		return
	}
	ctx := r.Context()
	var link string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		token, err := newSignInLink(ctx, tx, a.WorkspaceID, cur.ProgramID, cur.RecordID, &a.ActorID)
		if err != nil {
			return err
		}
		var sub *string
		if err := tx.QueryRow(ctx, `SELECT portal_subdomain FROM partner_programs WHERE id = $1`, cur.ProgramID).Scan(&sub); err != nil {
			return err
		}
		base, err := s.portalBase(ctx, cur.ProgramID, sub)
		if err != nil {
			return err
		}
		link = base + "#signin=" + token
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner.impersonated", "partner", cur.ID,
			map[string]any{"program_id": cur.ProgramID, "record_id": cur.RecordID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": link, "read_only": true, "expires_in_seconds": int(partnerSignInLinkLife.Seconds())})
}

// ---- Signing in ----

// newSignInLink stores a one-time sign-in token and returns it. by is the
// team member opening the portal as the partner, for a read-only session.
func newSignInLink(ctx context.Context, tx pgx.Tx, ws, program, rec string, by *string) (string, error) {
	token, hash := auth.NewToken(auth.PrefixPartnerLink)
	_, err := tx.Exec(ctx, `INSERT INTO partner_sign_in_links (workspace_id, program_id, record_id, token_hash, expires_at, impersonated_by)
		VALUES ($1, $2, $3, $4, $5, $6)`, ws, program, rec, hash, time.Now().Add(partnerSignInLinkLife), by)
	return token, err
}

// portalProgram is a program as its portal sees it.
type portalProgram struct {
	ws, id, name, kind, status, currency, termsURL, landing string
	brandID                                                 *string
	subdomain                                               *string
	enabled, autoAccept                                     bool
	minPayout                                               int64
	holdDays                                                int
	settings                                                portalSettings
}

// loadPortalProgram finds the program in the path; ok is false (after
// answering 404) when there is none or its portal is off and the caller
// isn't on a read-only preview.
func (s *Server) loadPortalProgram(w http.ResponseWriter, r *http.Request, allowOff bool) (*portalProgram, bool) {
	p, err := s.portalProgramByID(r.Context(), r.PathValue("program"))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!p.enabled && !allowOff || p.status == "archived") {
		writeError(w, r, http.StatusNotFound, "not_found", "this partner portal isn't open")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	noteWorkspace(r, p.ws)
	return p, true
}

// portalProgramByID loads a program as its portal sees it.
func (s *Server) portalProgramByID(ctx context.Context, id string) (*portalProgram, error) {
	if !validUUID(id) {
		return nil, pgx.ErrNoRows
	}
	var p portalProgram
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT workspace_id::text, id::text, name, kind, status, currency, terms_url, landing_url, brand_id::text,
		portal_subdomain, portal_enabled, auto_accept, min_payout_cents, hold_days, portal FROM partner_programs WHERE id = $1`, id).
		Scan(&p.ws, &p.id, &p.name, &p.kind, &p.status, &p.currency, &p.termsURL, &p.landing, &p.brandID, &p.subdomain, &p.enabled, &p.autoAccept,
			&p.minPayout, &p.holdDays, &raw)
	if err != nil {
		return nil, err
	}
	p.settings = portalFor(p.kind, raw)
	return &p, nil
}

// title is the portal's name.
func (p *portalProgram) title() string {
	if p.settings.Title != "" {
		return p.settings.Title
	}
	return p.name
}

// portalEmail sends a partner an email from the portal's sender, through
// the workspace's own email provider. Content fills in {{sign_in_url}},
// {{portal_url}}, {{program_name}} and {{partner_name}}. errPortalEmail
// says it can't be sent as the program is set up.
func (s *Server) portalEmail(ctx context.Context, tx pgx.Tx, p *portalProgram, to, ref string, d emails.Draft) error {
	if s.emails == nil || p.settings.EmailFrom == "" {
		return errPortalEmail
	}
	d.NewEmail = emails.NewEmail{WorkspaceID: p.ws, BrandID: p.brandID, From: p.settings.EmailFrom, To: []string{to},
		Purpose: "transactional", Trigger: "system", TriggerRef: ref, Surface: "job",
		Tags: []string{"partner_portal"}, Metadata: map[string]any{"program_id": p.id}}
	base, err := s.portalBase(ctx, p.id, p.subdomain)
	if err != nil {
		return err
	}
	if d.Data == nil {
		d.Data = map[string]any{}
	}
	d.Data["portal_url"], d.Data["program_name"] = base, p.title()
	e, err := s.emails.Compose(ctx, tx, d)
	if err != nil {
		var ce *emails.ComposeError
		if errors.As(err, &ce) {
			s.log.WarnContext(ctx, "partners: portal email refused", "workspace_id", p.ws, "program_id", p.id, "ref", ref, "code", ce.Code)
			return errPortalEmail
		}
		return err
	}
	_, err = s.emails.Queue(ctx, tx, emails.System, e)
	return err
}

// portalCookiePath keeps a portal's session to its own pages.
func (s *Server) portalCookiePath(r *http.Request, program string) string {
	if onPortalHost(r) {
		return "/"
	}
	return "/p/" + program + "/"
}

func (s *Server) portalPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("program")
	if !validUUID(id) {
		http.NotFound(w, r)
		return
	}
	base := "/p/" + id + "/"
	if onPortalHost(r) {
		base = "/"
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src https: data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	_, _ = w.Write([]byte(strings.ReplaceAll(partnerPortalHTML, "{{BASE}}", html.EscapeString(base))))
}

// portalPublicInfo is what anyone sees of a portal before signing in.
type portalPublicInfo struct {
	Name         string        `json:"name"`
	Kind         string        `json:"kind"`
	Title        string        `json:"title"`
	LogoURL      string        `json:"logo_url"`
	Color        string        `json:"color"`
	CustomCSS    string        `json:"custom_css"`
	Welcome      string        `json:"welcome"`
	Pages        []string      `json:"pages"`
	SignupOpen   bool          `json:"signup_open"`
	SignupFields []portalField `json:"signup_fields"`
	TermsURL     string        `json:"terms_url"`
	Currency     string        `json:"currency"`
}

func (p *portalProgram) public() portalPublicInfo {
	t := p.settings.Title
	if t == "" {
		t = p.name
	}
	return portalPublicInfo{Name: p.name, Kind: p.kind, Title: t, LogoURL: p.settings.LogoURL, Color: p.settings.Color, CustomCSS: p.settings.CustomCSS,
		Welcome: p.settings.Welcome, Pages: p.settings.Pages, SignupOpen: p.settings.SignupOpen && p.status == "active", SignupFields: p.settings.SignupFields,
		TermsURL: p.termsURL, Currency: p.currency}
}

func (s *Server) portalProgramInfo(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPortalProgram(w, r, s.portalPreview(r))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p.public())
}

// portalPreview reports whether the request carries a read-only session
// someone on the team opened, which may see a portal that is still off.
func (s *Server) portalPreview(r *http.Request) bool {
	token := portalToken(r)
	if token == "" {
		return false
	}
	var open bool
	err := s.pool.QueryRow(r.Context(), `SELECT impersonated_by IS NOT NULL OR widget FROM partner_sessions WHERE token_hash = $1 AND program_id::text = $2 AND expires_at > now()`,
		auth.HashToken(token), r.PathValue("program")).Scan(&open)
	return err == nil && open
}

// portalToken is the partner's session: the portal's cookie, or a widget's
// bearer token (an iframe in another site's page gets no cookie).
func portalToken(r *http.Request) string {
	token := ""
	if c, err := r.Cookie(partnerSessionCookie); err == nil {
		token = c.Value
	}
	if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		token = b
	}
	if !strings.HasPrefix(token, auth.PrefixPartnerSession) {
		return ""
	}
	return token
}

// sendSignInLink emails a partner a link that signs them in to the portal,
// through the workspace's own email provider.
func (s *Server) sendSignInLink(ctx context.Context, tx pgx.Tx, p *portalProgram, rec, email, name string) error {
	if s.emails == nil || p.settings.EmailFrom == "" {
		return errPortalEmail
	}
	token, err := newSignInLink(ctx, tx, p.ws, p.id, rec, nil)
	if err != nil {
		return err
	}
	base, err := s.portalBase(ctx, p.id, p.subdomain)
	if err != nil {
		return err
	}
	d := emails.Draft{Data: map[string]any{"sign_in_url": base + "#signin=" + token, "partner_name": name}}
	if t := p.settings.SignInTemplate; t != "" {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM email_templates WHERE workspace_id = $1 AND (id::text = $2 OR handle = $2)`, p.ws, t).Scan(&id); err != nil {
			return errPortalEmail
		}
		d.TemplateID = id
	} else {
		title := p.title()
		subject := "Sign in to " + title
		body := "<p>Here is your link to sign in to " + html.EscapeString(title) + ". It works once, for 30 minutes.</p>" +
			`<p><a href="{{sign_in_url}}">Sign in</a></p><p>If you didn't ask for it, ignore this email.</p>`
		text := "Here is your link to sign in to " + title + ". It works once, for 30 minutes.\n\n{{sign_in_url}}\n\nIf you didn't ask for it, ignore this email."
		d.Subject, d.HTML, d.Text = &subject, &body, &text
	}
	return s.portalEmail(ctx, tx, p, email, "partner_sign_in", d)
}

var errPortalEmail = errors.New("portal email")

func (s *Server) portalEmailError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, errPortalEmail) {
		writeError(w, r, http.StatusConflict, "portal_email_unavailable",
			"this portal can't send sign-in emails yet: the program's owner needs to set a sender on a verified sending domain")
		return true
	}
	return s.allocError(w, r, err)
}

// portalSignIn emails a sign-in link to a partner of the program. It
// answers the same whether or not the address belongs to one, so it can't
// be used to find out who is a partner.
func (s *Server) portalSignIn(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPortalProgram(w, r, false)
	if !ok || !s.portalLimit.allow(w, r, "ip:"+clientIP(r)) {
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	email, err := normalizeEmail(in.Email)
	if err != nil || email == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email is not a valid address")
		return
	}
	if p.settings.EmailFrom == "" {
		s.portalEmailError(w, r, errPortalEmail)
		return
	}
	if !s.portalLimit.allow(w, r, "email:"+email) {
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var rec, name string
		err := tx.QueryRow(ctx, `SELECT m.record_id::text, COALESCE(r.name, '') FROM partner_memberships m
			JOIN record_identities i ON i.record_id = m.record_id AND i.namespace = 'email' AND i.value = $2 AND i.valid_to IS NULL
			JOIN records r ON r.id = m.record_id
			WHERE m.program_id = $1 AND m.status <> 'rejected' ORDER BY i.verified DESC LIMIT 1`, p.id, email).Scan(&rec, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.sendSignInLink(ctx, tx, p, rec, email, name)
	})
	if s.portalEmailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}

// portalSignup is a partner applying to the program: they become a
// customer record and a partner, waiting for approval unless the program
// accepts everyone, and are emailed a sign-in link.
func (s *Server) portalSignup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPortalProgram(w, r, false)
	if !ok || !s.portalLimit.allow(w, r, "ip:"+clientIP(r)) {
		return
	}
	var in struct {
		Email       string         `json:"email"`
		Name        string         `json:"name"`
		Answers     map[string]any `json:"answers"`
		AcceptTerms bool           `json:"accept_terms"`
	}
	if !decode(w, r, &in) {
		return
	}
	pub := p.public()
	email, err := normalizeEmail(in.Email)
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case !pub.SignupOpen:
		writeError(w, r, http.StatusForbidden, "forbidden", "this program isn't taking sign-ups; ask the company for an invitation")
		return
	case err != nil || email == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email is not a valid address")
		return
	case in.Name == "" || len(in.Name) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is 1 to 200 characters")
		return
	case p.termsURL != "" && !in.AcceptTerms:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "accept the program's terms to join")
		return
	case p.settings.EmailFrom == "":
		s.portalEmailError(w, r, errPortalEmail)
		return
	}
	answers := map[string]any{}
	for _, f := range p.settings.SignupFields {
		v, _ := in.Answers[f.Key].(string)
		v = strings.TrimSpace(v)
		switch {
		case v == "" && f.Required:
			writeError(w, r, http.StatusBadRequest, "invalid_request", f.Label+" is required")
			return
		case len(v) > 2000:
			writeError(w, r, http.StatusBadRequest, "invalid_request", f.Label+" is at most 2,000 characters")
			return
		case v != "" && f.Type == "select" && !slices.Contains(f.Options, v):
			writeError(w, r, http.StatusBadRequest, "invalid_request", f.Label+" must be one of the options")
			return
		case v != "" && f.Type == "url":
			if _, err := utm.Destination(v); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f.Label+" must be a full address, such as https://example.com")
				return
			}
		}
		if v != "" {
			answers[f.Key] = v
		}
	}
	if !s.portalLimit.allow(w, r, "email:"+email) {
		return
	}
	status := "pending"
	if p.autoAccept {
		status = "active"
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rec, err := customers.EnsurePerson(ctx, tx, p.ws, "email", email, false, RequestID(ctx))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND name = ''`, rec, p.ws, cutUTF8(in.Name, 200)); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_memberships WHERE program_id = $1 AND record_id = $2)`, p.id, rec).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			prog := &partnerProgramView{ID: p.id, Kind: p.kind, BrandID: p.brandID}
			now := time.Now()
			var terms *time.Time
			if in.AcceptTerms {
				terms = &now
			}
			if _, err := s.joinPartner(ctx, tx, p.ws, nil, prog, partnerJoin{rec: rec, status: status, source: "signup",
				application: answers, termsAt: terms}); err != nil {
				return err
			}
		}
		// Already a partner: the same answer, and a sign-in link.
		return s.sendSignInLink(ctx, tx, p, rec, email, in.Name)
	})
	if s.portalEmailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"sent": true})
}

// portalSession turns a sign-in link's token into a session cookie.
func (s *Server) portalSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("program")
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validUUID(id) || !strings.HasPrefix(in.Token, auth.PrefixPartnerLink) {
		writeError(w, r, http.StatusNotFound, "invalid_link", "this sign-in link is invalid, used or expired; ask for a new one")
		return
	}
	ctx := r.Context()
	var token, ws string
	var by *string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var rec string
		var enabled bool
		err := tx.QueryRow(ctx, `UPDATE partner_sign_in_links l SET used_at = now() FROM partner_programs p
			WHERE l.token_hash = $1 AND l.program_id = $2 AND p.id = l.program_id AND l.used_at IS NULL AND l.expires_at > now()
			RETURNING l.workspace_id::text, l.record_id::text, l.impersonated_by::text, p.portal_enabled`, auth.HashToken(in.Token), id).Scan(&ws, &rec, &by, &enabled)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !enabled && by == nil {
			return errNotFound
		}
		if err != nil {
			return err
		}
		life := partnerSessionLifetime
		if by != nil {
			life = partnerImpersonateLife
		}
		var hash []byte
		token, hash = auth.NewToken(auth.PrefixPartnerSession)
		if _, err := tx.Exec(ctx, `INSERT INTO partner_sessions (workspace_id, program_id, record_id, token_hash, impersonated_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, ws, id, rec, hash, by, time.Now().Add(life)); err != nil {
			return err
		}
		if by != nil {
			return nil // the impersonation itself was recorded when the link was made
		}
		// The link reached their inbox: the address is theirs.
		if _, err := tx.Exec(ctx, `UPDATE record_identities i SET verified = true, verified_at = now() FROM partner_memberships m
			WHERE m.program_id = $2 AND m.record_id = $1 AND i.record_id = $1 AND i.namespace = 'email' AND i.valid_to IS NULL AND NOT i.verified
			AND NOT EXISTS (SELECT 1 FROM record_identities o WHERE o.workspace_id = i.workspace_id AND o.namespace = 'email' AND o.value = i.value
				AND o.verified AND o.valid_to IS NULL AND o.record_id <> i.record_id)`, rec, id); err != nil {
			return err
		}
		var member string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM partner_memberships WHERE program_id = $1 AND record_id = $2`, id, rec).Scan(&member); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, nil, ws, "partner.signed_in", "partner", member, map[string]any{"program_id": id})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "invalid_link", "this sign-in link is invalid, used or expired; ask for a new one")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	noteWorkspace(r, ws)
	http.SetCookie(w, &http.Cookie{Name: partnerSessionCookie, Value: token, Path: s.portalCookiePath(r, id), HttpOnly: true,
		Secure: s.secureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(partnerSessionLifetime.Seconds())})
	writeJSON(w, http.StatusOK, map[string]any{"signed_in": true, "read_only": by != nil})
}

func (s *Server) portalSignOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(partnerSessionCookie); err == nil {
		if _, err := s.pool.Exec(r.Context(), `DELETE FROM partner_sessions WHERE token_hash = $1`, auth.HashToken(c.Value)); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: partnerSessionCookie, Value: "", Path: s.portalCookiePath(r, r.PathValue("program")), MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"signed_in": false})
}

// ---- The signed-in partner's own data ----

type portalCaller struct {
	program  *portalProgram
	partner  partnerView
	readOnly bool
}

// inPortal runs next for the partner signed in to the program in the path.
func (s *Server) inPortal(next func(http.ResponseWriter, *http.Request, *portalCaller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.loadPortalProgram(w, r, s.portalPreview(r))
		if !ok {
			return
		}
		token := portalToken(r)
		if token == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "sign in to the partner portal")
			return
		}
		var member string
		var by *string
		err := s.pool.QueryRow(r.Context(), `UPDATE partner_sessions s SET last_seen_at = now() FROM partner_memberships m
			WHERE s.token_hash = $1 AND s.program_id = $2 AND s.expires_at > now() AND m.program_id = s.program_id AND m.record_id = s.record_id
			RETURNING m.id::text, s.impersonated_by::text`, auth.HashToken(token), p.id).Scan(&member, &by)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "your session ended; sign in again")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		v, err := s.loadPartner(r.Context(), p.ws, member)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		next(w, r, &portalCaller{program: p, partner: v, readOnly: by != nil})
	}
}

// portalMeView is the partner's own view of their membership.
type portalMeView struct {
	Name           string           `json:"name"`
	Email          string           `json:"email"`
	Code           string           `json:"code"`
	Status         string           `json:"status"`
	Link           string           `json:"link"`
	CampaignLink   string           `json:"campaign_link"`
	PromoCodes     []string         `json:"promo_codes"`
	PayoutMethod   string           `json:"payout_method"`
	Notify         map[string]bool  `json:"notify"`
	Currency       string           `json:"currency"`
	Stats          partnerStats     `json:"stats"`
	MinPayoutCents int64            `json:"min_payout_cents"`
	HoldDays       int              `json:"hold_days"`
	ReadOnly       bool             `json:"read_only"`
	Program        portalPublicInfo `json:"program"`
}

func (s *Server) portalMe(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	v := c.partner
	notify := map[string]bool{"conversions": true, "payouts": true}
	var raw []byte
	if err := s.pool.QueryRow(r.Context(), `SELECT notify FROM partner_memberships WHERE id = $1`, v.ID).Scan(&raw); err != nil {
		s.internal(w, r, err)
		return
	}
	_ = json.Unmarshal(raw, &notify)
	writeJSON(w, http.StatusOK, portalMeView{Name: v.Name, Email: v.Email, Code: v.Code, Status: v.Status, Link: v.Link, CampaignLink: v.CampaignLink,
		PromoCodes: promoCodeNames(v.PromoCodes), PayoutMethod: v.PayoutMethod, Notify: notify, Currency: v.Currency, Stats: v.Stats,
		MinPayoutCents: c.program.minPayout, HoldDays: c.program.holdDays, ReadOnly: c.readOnly, Program: c.program.public()})
}

func promoCodeNames(codes []partnerPromoCode) []string {
	out := []string{}
	for _, c := range codes {
		if c.Status != "pending" && c.Status != "failed" {
			out = append(out, c.Code)
		}
	}
	return out
}

// portalUpdateMe saves the partner's payout method and which emails they
// want. A read-only preview can't.
func (s *Server) portalUpdateMe(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	if c.readOnly {
		writeError(w, r, http.StatusForbidden, "forbidden", "you are seeing the portal as this partner, read-only")
		return
	}
	var in struct {
		PayoutMethod *string         `json:"payout_method"`
		Notify       map[string]bool `json:"notify"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.PayoutMethod != nil && len(*in.PayoutMethod) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "payout_method is at most 500 characters; never put bank details here")
		return
	}
	for k := range in.Notify {
		if k != "conversions" && k != "payouts" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "notify has conversions and payouts")
			return
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if in.PayoutMethod != nil {
			if _, err := tx.Exec(ctx, `UPDATE partner_memberships SET payout_method = $2, updated_at = now() WHERE id = $1`,
				c.partner.ID, s.sealPayout(c.partner.ID, *in.PayoutMethod)); err != nil {
				return err
			}
		}
		if in.Notify != nil {
			raw, _ := json.Marshal(in.Notify)
			if _, err := tx.Exec(ctx, `UPDATE partner_memberships SET notify = notify || $2::jsonb, updated_at = now() WHERE id = $1`, c.partner.ID, raw); err != nil {
				return err
			}
		}
		return partnerRecord(ctx, tx, nil, c.program.ws, "partner.portal_updated", "partner", c.partner.ID,
			map[string]any{"program_id": c.program.id, "payout_method": in.PayoutMethod != nil, "notify": in.Notify != nil})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.loadPartner(ctx, c.program.ws, c.partner.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	c.partner = v
	s.portalMe(w, r, c)
}

// portalReferralView is a referred customer as their partner sees them:
// never their name or address.
type portalReferralView struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Via         string     `json:"via"`
	SubID       string     `json:"sub_id"`
	Status      string     `json:"status"`
	FirstSaleAt *time.Time `json:"first_sale_at"`
	SalesCents  int64      `json:"sales_cents"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Server) portalReferrals(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	rows, err := s.pool.Query(r.Context(), `SELECT f.id::text, f.via, f.sub_id, f.status, f.first_sale_at,
		COALESCE((SELECT sum(CASE WHEN v.type = 'sale' THEN v.amount_cents WHEN v.type = 'refund' THEN -v.amount_cents ELSE 0 END)
			FROM partner_conversions v WHERE v.referral_id = f.id), 0)::bigint, f.created_at
		FROM partner_referrals f WHERE f.membership_id = $1 ORDER BY f.created_at DESC LIMIT 500`, c.partner.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portalReferralView, error) {
		var v portalReferralView
		err := row.Scan(&v.ID, &v.Via, &v.SubID, &v.Status, &v.FirstSaleAt, &v.SalesCents, &v.CreatedAt)
		v.Label = "Customer " + strings.ToUpper(v.ID[:6])
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []portalReferralView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// portalRewardView is a ledger entry as its partner sees it.
type portalRewardView struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Action      string     `json:"action"`
	AmountCents int64      `json:"amount_cents"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	HoldUntil   time.Time  `json:"hold_until"`
	Note        string     `json:"note"`
	ApprovedAt  *time.Time `json:"approved_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Server) portalRewards(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, kind, action, amount_cents, currency, status, hold_until, note, approved_at, created_at
		FROM partner_rewards WHERE membership_id = $1 AND side = 'partner' ORDER BY created_at DESC LIMIT 500`, c.partner.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[portalRewardView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []portalRewardView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type portalPayoutView struct {
	ID          string     `json:"id"`
	AmountCents int64      `json:"amount_cents"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	Reference   string     `json:"reference"`
	PaidAt      *time.Time `json:"paid_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Server) portalPayouts(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, amount_cents, currency, status, reference, paid_at, created_at
		FROM partner_payouts WHERE membership_id = $1 AND status <> 'canceled' ORDER BY created_at DESC LIMIT 200`, c.partner.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[portalPayoutView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []portalPayoutView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) portalAssets(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	list, err := s.programAssets(r.Context(), c.program.ws, c.program.id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// ---- The referral widget ----

// createWidgetSession is the workspace's backend asking for a referral
// widget for its signed-in customer: the customer becomes a partner in the
// program if they aren't one, and gets a session the widget uses for a
// few hours. The widget shows their link and what they've earned, inside
// the workspace's own app.
func (s *Server) createWidgetSession(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	prog, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		CustomerID string `json:"customer_id"`
		Email      string `json:"email"`
		RecordID   string `json:"record_id"`
		Name       string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if prog.Status != "active" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the program isn't active")
		return
	}
	ci := conversionInput{Type: "signup", CustomerID: strings.TrimSpace(in.CustomerID), Email: in.Email, RecordID: in.RecordID}
	if err := ci.check(time.Now()); err != nil {
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	var token, member string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		run := &conversionRun{s: s, tx: tx, p: &a.principal, ws: a.WorkspaceID, in: ci}
		rec, err := run.customer(ctx)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(in.Name)
		if name != "" {
			if _, err := tx.Exec(ctx, `UPDATE records SET name = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND name = ''`,
				rec, a.WorkspaceID, cutUTF8(name, 200)); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `SELECT id::text FROM partner_memberships WHERE program_id = $1 AND record_id = $2`, prog.ID, rec).Scan(&member)
		if errors.Is(err, pgx.ErrNoRows) {
			status := "pending"
			if prog.AutoAccept {
				status = "active"
			}
			member, err = s.joinPartner(ctx, tx, a.WorkspaceID, &a.principal, prog, partnerJoin{rec: rec, status: status, source: "api"})
		}
		if err != nil {
			return err
		}
		var hash []byte
		token, hash = auth.NewToken(auth.PrefixPartnerSession)
		_, err = tx.Exec(ctx, `INSERT INTO partner_sessions (workspace_id, program_id, record_id, token_hash, widget, expires_at) VALUES ($1, $2, $3, $4, true, $5)`,
			a.WorkspaceID, prog.ID, rec, hash, time.Now().Add(partnerWidgetLife))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadPartner(ctx, a.WorkspaceID, member)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	base, err := s.portalBase(ctx, prog.ID, prog.PortalSubdomain)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "embed_url": base + "embed#token=" + token,
		"expires_in_seconds": int(partnerWidgetLife.Seconds()), "partner_id": v.ID, "status": v.Status, "code": v.Code, "link": v.Link})
}

// portalEmbed is the referral widget, for an iframe in the workspace's own
// app; the token after # names the partner.
func (s *Server) portalEmbed(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("program")
	if !validUUID(id) {
		http.NotFound(w, r)
		return
	}
	base := "/p/" + id + "/"
	if onPortalHost(r) {
		base = "/"
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	// Embedded in the workspace's own pages, over https: the only page
	// here another site may frame.
	h.Del("X-Frame-Options")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src https: data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors https:; base-uri 'none'; form-action 'none'")
	_, _ = w.Write([]byte(strings.ReplaceAll(partnerWidgetHTML, "{{BASE}}", html.EscapeString(base))))
}
