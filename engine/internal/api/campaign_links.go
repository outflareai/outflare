package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Campaign links: the workspace's one tagging scheme. A tracking campaign
// fixes the tags every link in it carries (channel, source, medium and the
// utm_campaign slug) plus a short ofc code; a link is one destination in
// it. Modules make campaigns for their own objects through utm.Ensure;
// people and agents make the rest here.

const (
	maxTrackingCampaigns = 5000
	maxTrackingLinks     = 1000
)

func (s *Server) routesCampaignLinks(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/tracking-taxonomy", s.inWorkspace(read, s.getTrackingTaxonomy))
	mux.HandleFunc("GET /v1/workspaces/{ws}/tracking-campaigns", s.inWorkspace(read, s.listTrackingCampaigns))
	mux.HandleFunc("POST /v1/workspaces/{ws}/tracking-campaigns", s.inWorkspace(manage, s.createTrackingCampaign))
	mux.HandleFunc("GET /v1/workspaces/{ws}/tracking-campaigns/{id}", s.inWorkspace(read, s.getTrackingCampaign))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/tracking-campaigns/{id}", s.inWorkspace(manage, s.updateTrackingCampaign))
	mux.HandleFunc("GET /v1/workspaces/{ws}/tracking-campaigns/{id}/links", s.inWorkspace(read, s.listTrackingLinks))
	mux.HandleFunc("POST /v1/workspaces/{ws}/tracking-campaigns/{id}/links", s.inWorkspace(manage, s.createTrackingLink))
}

type trackingCampaignView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Slug       string  `json:"slug"`
	Code       string  `json:"code"`
	Channel    string  `json:"channel"`
	Source     string  `json:"source"`
	Medium     string  `json:"medium"`
	BrandID    *string `json:"brand_id"`
	ObjectKind string  `json:"object_kind"`
	ObjectID   string  `json:"object_id"`
	LandingURL string  `json:"landing_url"`
	// LandingLink is the landing page with the campaign's tags, ready to
	// share; "" without a landing page.
	LandingLink string    `json:"landing_link"`
	Status      string    `json:"status"`
	Links       int       `json:"links"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const trackingCampaignSelect = `
	SELECT c.id::text, c.name, c.slug, c.code, c.channel, c.source, c.medium, c.brand_id::text, c.object_kind, c.object_id,
	       c.landing_url, c.status, (SELECT count(*) FROM tracking_links l WHERE l.campaign_id = c.id)::int, c.created_at, c.updated_at
	FROM tracking_campaigns c
	WHERE c.workspace_id = $1`

func scanTrackingCampaign(row pgx.Row) (trackingCampaignView, error) {
	var v trackingCampaignView
	err := row.Scan(&v.ID, &v.Name, &v.Slug, &v.Code, &v.Channel, &v.Source, &v.Medium, &v.BrandID, &v.ObjectKind, &v.ObjectID,
		&v.LandingURL, &v.Status, &v.Links, &v.CreatedAt, &v.UpdatedAt)
	if err == nil && v.LandingURL != "" {
		v.LandingLink, _ = v.campaign().Link(v.LandingURL, "")
	}
	return v, err
}

func (v trackingCampaignView) campaign() utm.Campaign {
	return utm.Campaign{ID: v.ID, Slug: v.Slug, Code: v.Code, Channel: v.Channel, Source: v.Source, Medium: v.Medium}
}

func (s *Server) getTrackingTaxonomy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"channels": utm.Channels})
}

func (s *Server) listTrackingCampaigns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status, channel, brand := q.Get("status"), q.Get("channel"), q.Get("brand")
	if status != "" && status != "active" && status != "archived" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or archived")
		return
	}
	if _, ok := utm.ChannelByID(channel); channel != "" && !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", utm.ErrChannel.Error())
		return
	}
	brandID := ""
	if brand != "" {
		id, err := s.brandIDFor(r.Context(), a.WorkspaceID, brand)
		if s.allocError(w, r, err) {
			return
		}
		brandID = id
	}
	rows, err := s.pool.Query(r.Context(), trackingCampaignSelect+`
		AND ($2 = '' OR c.status = $2) AND ($3 = '' OR c.channel = $3) AND ($4 = '' OR c.brand_id::text = $4)
		ORDER BY c.created_at DESC LIMIT 1000`, a.WorkspaceID, status, channel, brandID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackingCampaignView, error) { return scanTrackingCampaign(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []trackingCampaignView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findTrackingCampaign finds a campaign by id, slug or ofc code.
func (s *Server) findTrackingCampaign(w http.ResponseWriter, r *http.Request, ws, ref string) (*trackingCampaignView, bool) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	v, err := scanTrackingCampaign(s.pool.QueryRow(r.Context(), trackingCampaignSelect+` AND (c.id::text = $2 OR c.slug = $2 OR c.code = $2)`, ws, ref))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "campaign not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func (s *Server) getTrackingCampaign(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findTrackingCampaign(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type trackingCampaignInput struct {
	Name       *string `json:"name"`
	Slug       *string `json:"slug"`
	Channel    *string `json:"channel"`
	Source     *string `json:"source"`
	Brand      *string `json:"brand"`
	LandingURL *string `json:"landing_url"`
	Status     *string `json:"status"`
}

// cleanLanding checks a landing page and drops tags it already had.
func cleanLanding(p *string) (*string, error) {
	if p == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return &v, nil
	}
	dest, err := utm.Destination(v)
	if err != nil {
		return nil, errInvalid("landing_url: " + err.Error())
	}
	return &dest, nil
}

func validCampaignName(p *string) (string, error) {
	n := strings.TrimSpace(deref(p))
	if n == "" || len(n) > 120 {
		return "", errInvalid("name must be 1 to 120 characters")
	}
	return n, nil
}

// campaignBrand resolves a brand's id or handle; "" is no brand.
func (s *Server) campaignBrand(r *http.Request, ws string, ref *string) (*string, error) {
	if ref == nil || strings.TrimSpace(*ref) == "" {
		return nil, nil
	}
	id, err := s.brandIDFor(r.Context(), ws, *ref)
	if errors.Is(err, errNotFound) {
		return nil, errInvalid("brand not found: " + truncate(*ref, 63))
	}
	return &id, err
}

func (s *Server) createTrackingCampaign(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in trackingCampaignInput
	if !decode(w, r, &in) {
		return
	}
	name, err := validCampaignName(in.Name)
	if s.allocError(w, r, err) {
		return
	}
	ch, ok := utm.ChannelByID(deref(in.Channel))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", utm.ErrChannel.Error())
		return
	}
	source := utm.Source(deref(in.Source))
	if source == "" && len(ch.Sources) == 1 {
		source = ch.Sources[0]
	}
	if err := utm.CheckSource(source); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	slug := utm.Slug(name)
	if in.Slug != nil {
		slug = strings.ToLower(strings.TrimSpace(*in.Slug))
	}
	if err := utm.CheckSlug(slug); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if in.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new campaign is active; archive it with PATCH")
		return
	}
	landing, err := cleanLanding(in.LandingURL)
	if s.allocError(w, r, err) {
		return
	}
	brand, err := s.campaignBrand(r, a.WorkspaceID, in.Brand)
	if s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracking_campaigns WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxTrackingCampaigns {
			return errInvalid("a workspace can have at most 5,000 campaigns; archive old ones and reuse them")
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO tracking_campaigns (workspace_id, brand_id, name, slug, code, channel, source, medium, landing_url, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (workspace_id, slug) DO NOTHING RETURNING id::text`,
			a.WorkspaceID, brand, name, slug, utm.NewCode(), ch.ID, source, ch.Medium, deref(landing), nullIfEmpty(a.ActorID)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_campaign.created", "tracking_campaign", id,
			map[string]any{"channel": ch.ID, "brand_id": brand, "via": "manual"})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "another campaign already uses the slug "+slug+"; pick another slug or reuse that campaign")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findTrackingCampaign(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updateTrackingCampaign renames a campaign, changes its brand or landing
// page, or archives or restores it. Its tags are fixed, because links
// already shared carry them: for other tags, make another campaign.
func (s *Server) updateTrackingCampaign(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findTrackingCampaign(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in trackingCampaignInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Slug != nil && *in.Slug != cur.Slug, in.Channel != nil && *in.Channel != cur.Channel, in.Source != nil && utm.Source(*in.Source) != cur.Source:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a campaign's slug, channel and source can't change, because links already shared carry them; make another campaign instead")
		return
	case in.Status != nil && *in.Status != "active" && *in.Status != "archived":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be active or archived")
		return
	}
	var name *string
	if in.Name != nil {
		n, err := validCampaignName(in.Name)
		if s.allocError(w, r, err) {
			return
		}
		name = &n
	}
	landing, err := cleanLanding(in.LandingURL)
	if s.allocError(w, r, err) {
		return
	}
	brand := cur.BrandID
	if in.Brand != nil {
		if brand, err = s.campaignBrand(r, a.WorkspaceID, in.Brand); s.allocError(w, r, err) {
			return
		}
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE tracking_campaigns SET name = COALESCE($3, name), landing_url = COALESCE($4, landing_url),
				brand_id = $5, status = COALESCE($6, status), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, name, landing, brand, in.Status); err != nil {
			return err
		}
		action := "tracking_campaign.updated"
		if in.Status != nil && *in.Status != cur.Status {
			action = map[string]string{"archived": "tracking_campaign.archived", "active": "tracking_campaign.restored"}[*in.Status]
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "tracking_campaign", cur.ID, map[string]any{"channel": cur.Channel, "brand_id": brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findTrackingCampaign(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type trackingLinkView struct {
	ID         string    `json:"id"`
	CampaignID string    `json:"campaign_id"`
	URL        string    `json:"url"`
	Content    string    `json:"content"`
	Term       string    `json:"term"`
	TaggedURL  string    `json:"tagged_url"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) listTrackingLinks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, ok := s.findTrackingCampaign(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, url, content, term, created_at FROM tracking_links
		WHERE workspace_id = $1 AND campaign_id = $2 ORDER BY created_at DESC, id`, a.WorkspaceID, c.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackingLinkView, error) {
		v := trackingLinkView{CampaignID: c.ID}
		err := row.Scan(&v.ID, &v.URL, &v.Content, &v.Term, &v.CreatedAt)
		v.TaggedURL = tagLink(*c, v)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []trackingLinkView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func tagLink(c trackingCampaignView, l trackingLinkView) string {
	out, _ := utm.Tag(l.URL, utm.Tags{Source: c.Source, Medium: c.Medium, Campaign: c.Slug, Content: l.Content, Term: l.Term, Code: c.Code})
	return out
}

// createTrackingLink tags a destination with the campaign. The same page,
// content and term give back the link already made.
func (s *Server) createTrackingLink(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, ok := s.findTrackingCampaign(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		URL     *string `json:"url"`
		Content string  `json:"content"`
		Term    string  `json:"term"`
	}
	if !decode(w, r, &in) {
		return
	}
	if c.Status != "active" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the campaign is archived; restore it first")
		return
	}
	raw := deref(in.URL)
	if strings.TrimSpace(raw) == "" {
		raw = c.LandingURL
	}
	if raw == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is required, since the campaign has no landing page")
		return
	}
	dest, err := utm.Destination(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	l := trackingLinkView{CampaignID: c.ID, URL: dest, Content: utm.Value(in.Content), Term: utm.Value(in.Term)}
	if utm.CheckContent(l.Content) != nil || utm.CheckContent(l.Term) != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", utm.ErrContent.Error())
		return
	}
	ctx := r.Context()
	created := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id::text, created_at FROM tracking_links WHERE campaign_id = $1 AND url = $2 AND content = $3 AND term = $4`,
			c.ID, l.URL, l.Content, l.Term).Scan(&l.ID, &l.CreatedAt)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracking_links WHERE campaign_id = $1`, c.ID).Scan(&n); err != nil {
			return err
		}
		if n >= maxTrackingLinks {
			return errInvalid("a campaign can have at most 1,000 links; tell them apart with content instead")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tracking_links (workspace_id, campaign_id, url, content, term, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text, created_at`,
			a.WorkspaceID, c.ID, l.URL, l.Content, l.Term, nullIfEmpty(a.ActorID)).Scan(&l.ID, &l.CreatedAt); err != nil {
			return err
		}
		created = true
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_link.created", "tracking_link", l.ID,
			map[string]any{"campaign_id": c.ID, "channel": c.Channel, "has_content": l.Content != ""})
	})
	if s.allocError(w, r, err) {
		return
	}
	l.TaggedURL = tagLink(*c, l)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, l)
}
