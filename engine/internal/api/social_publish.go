package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Social publishing: the brand's own original posts on its own X account
// or LinkedIn page or profile. A person or agent writes one, or the
// workspace's own AI drafts it; a person approves it; a job posts it now
// or at its time through the connection that reaches the account. A
// recurring series (a daily changelog summary) is approved once by a
// person, and the posts handed in for it are queued without another
// approval, up to its daily limit. Original posts carry no disclosure
// line: they come from the brand's own account.

func (s *Server) routesSocialPublish(mux *http.ServeMux) {
	read, manage, post := auth.ScopeSocialRead, auth.ScopeSocialManage, auth.ScopeSocialPost
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/publications", s.inBrand(read, s.listSocialPublications))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/publications", s.inBrand(manage, s.createSocialPublication))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/publications/{id}", s.inBrand(read, s.getSocialPublication))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/social/publications/{id}", s.inBrand(manage, s.updateSocialPublication))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/social/publications/{id}", s.inBrand(manage, s.cancelSocialPublication))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/publications/{id}/approve", s.inBrand(post, s.approveSocialPublication))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/publications/{id}/draft", s.inBrand(manage, s.draftSocialPublication))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/series", s.inBrand(read, s.listSocialSeries))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/series", s.inBrand(manage, s.createSocialSeries))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/series/{id}", s.inBrand(read, s.getSocialSeries))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/social/series/{id}", s.inBrand(manage, s.updateSocialSeries))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/series/{id}/approve", s.inBrand(post, s.approveSocialSeries))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/series/{id}/posts", s.inBrand(manage, s.addSocialSeriesPost))
}

// ---- Length ----

// socialMaxLengths are the channels' limits, for when the connector is not
// registered (a server built without it). The connector's own MaxLength
// wins.
var socialMaxLengths = map[string]int{"x": 280, "linkedin": 3000}

// socialMaxLength is the longest text the channel takes.
func (s *Server) socialMaxLength(channel string) int {
	if s.integrations != nil {
		if c, ok := s.integrations.Registry.Get(channel); ok {
			if p, ok := c.(connectors.SocialPublisher); ok {
				return p.MaxLength()
			}
		}
	}
	return socialMaxLengths[channel]
}

var socialURL = regexp.MustCompile(`https?://\S+`)

// socialLength counts text as the channel does: X counts every link as 23
// characters (its t.co links), whatever its length.
func socialLength(channel, text string) int {
	if channel != "x" {
		return utf8.RuneCountInString(text)
	}
	n := 0
	rest := socialURL.ReplaceAllStringFunc(text, func(string) string { n += 23; return "" })
	return n + utf8.RuneCountInString(rest)
}

// publicationText is the post as it goes out: the text, then the tracked
// link on its own line.
func publicationText(body, link string) string {
	if link == "" {
		return body
	}
	if body == "" {
		return link
	}
	return body + "\n\n" + link
}

// checkPublicationLength refuses a post longer than the channel takes.
func (s *Server) checkPublicationLength(channel, body, link string) error {
	max := s.socialMaxLength(channel)
	if n := socialLength(channel, publicationText(body, link)); max > 0 && n > max {
		name := map[string]string{"x": "X", "linkedin": "LinkedIn"}[channel]
		return errInvalid("body: the post is " + strconv.Itoa(n) + " characters with its link; " + name + " takes at most " + strconv.Itoa(max))
	}
	return nil
}

// ---- Views ----

type socialActorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type socialPublicationView struct {
	ID            string  `json:"id"`
	AccountID     string  `json:"account_id"`
	Channel       string  `json:"channel"`
	BrandID       *string `json:"brand_id"`
	IntegrationID *string `json:"integration_id"`
	Body          string  `json:"body"`
	LinkURL       string  `json:"link_url"`
	UTMContent    string  `json:"utm_content"`
	// The post as it goes out, and its length as the channel counts it.
	Text         string          `json:"text"`
	Length       int             `json:"length"`
	MaxLength    int             `json:"max_length"`
	Prompt       string          `json:"prompt"`
	ScheduledFor *time.Time      `json:"scheduled_for"`
	Status       string          `json:"status"`
	StatusReason *string         `json:"status_reason"`
	DraftedVia   string          `json:"drafted_via"`
	SeriesID     *string         `json:"series_id"`
	MissionID    *string         `json:"mission_id"`
	CreatedBy    *string         `json:"created_by"`
	ApprovedBy   *socialActorRef `json:"approved_by"`
	ApprovedAt   *time.Time      `json:"approved_at"`
	ExternalID   *string         `json:"external_id"`
	URL          *string         `json:"url"`
	PostID       *string         `json:"post_id"`
	PublishedAt  *time.Time      `json:"published_at"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

const socialPublicationSelect = `
	SELECT p.id::text, p.account_id::text, p.channel, p.brand_id::text, p.integration_id::text, p.body, p.link_url, p.utm_content, p.prompt,
	       p.scheduled_for, p.status, p.status_reason, p.drafted_via, p.series_id::text, p.mission_id::text, p.created_by::text,
	       ap.id::text, ap.display_name, p.approved_at, p.external_id, p.url, p.post_id::text, p.published_at, p.created_at, p.updated_at
	FROM social_publications p
	LEFT JOIN actors ap ON ap.id = p.approved_by
	WHERE p.workspace_id = $1`

func (s *Server) scanSocialPublication(row pgx.Row) (socialPublicationView, error) {
	var v socialPublicationView
	var apID, apName *string
	err := row.Scan(&v.ID, &v.AccountID, &v.Channel, &v.BrandID, &v.IntegrationID, &v.Body, &v.LinkURL, &v.UTMContent, &v.Prompt,
		&v.ScheduledFor, &v.Status, &v.StatusReason, &v.DraftedVia, &v.SeriesID, &v.MissionID, &v.CreatedBy,
		&apID, &apName, &v.ApprovedAt, &v.ExternalID, &v.URL, &v.PostID, &v.PublishedAt, &v.CreatedAt, &v.UpdatedAt)
	if apID != nil {
		v.ApprovedBy = &socialActorRef{*apID, deref(apName)}
	}
	v.Text = publicationText(v.Body, v.LinkURL)
	v.Length = socialLength(v.Channel, v.Text)
	v.MaxLength = s.socialMaxLength(v.Channel)
	return v, err
}

var socialPublicationStatuses = []string{"drafting", "draft", "approved", "publishing", "published", "failed", "cancelled"}

func (s *Server) listSocialPublications(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ch, ok := socialAccountChannel(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status, acct, series := q.Get("status"), q.Get("account_id"), q.Get("series_id")
	if status != "" && !slices.Contains(socialPublicationStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(socialPublicationStatuses, ", "))
		return
	}
	if (acct != "" && !validUUID(acct)) || (series != "" && !validUUID(series)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id and series_id are ids")
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status, acct, series, before, limit + 1, ch}
	rows, err := s.pool.Query(r.Context(), socialPublicationSelect+`
		AND ($2 = '' OR p.status = $2) AND ($3 = '' OR p.account_id = NULLIF($3, '')::uuid) AND ($4 = '' OR p.series_id = NULLIF($4, '')::uuid)
		AND ($5::timestamptz IS NULL OR p.created_at < $5) AND ($7 = '' OR p.channel = $7)`+sel.sql("p.brand_id", &args)+
		` ORDER BY p.created_at DESC LIMIT $6`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialPublicationView, error) { return s.scanSocialPublication(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		n := list[limit-1].CreatedAt.Format(time.RFC3339Nano)
		next = &n
	}
	if list == nil {
		list = []socialPublicationView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) findSocialPublication(w http.ResponseWriter, r *http.Request, a *access, id string) (*socialPublicationView, bool) {
	if validUUID(id) {
		v, err := s.scanSocialPublication(s.pool.QueryRow(r.Context(), socialPublicationSelect+` AND p.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "publication not found")
	return nil, false
}

func (s *Server) getSocialPublication(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSocialPublication(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// ---- Writing ----

// publishingAccount loads an account the caller may post from: one of its
// own brand's, on a channel that takes posts, still reached by its
// connection.
func (s *Server) publishingAccount(w http.ResponseWriter, r *http.Request, a *access, id string) (*socialAccountView, bool) {
	if id == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is required: GET .../social/accounts lists them")
		return nil, false
	}
	acct, ok := s.findSocialAccount(w, r, a, id)
	if !ok {
		return nil, false
	}
	switch {
	case !a.writes(acct.BrandID):
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, false
	case socialMaxLengths[acct.Channel] == 0:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id: posting works on X and LinkedIn accounts")
		return nil, false
	case acct.Status == "disconnected":
		writeError(w, r, http.StatusConflict, "connector_needed", "the account's connection no longer reaches it; sign in again under Connectors")
		return nil, false
	}
	return acct, true
}

// publicationLink tags landing with the account's campaign (one per
// account, organic social, the channel as source) and the post's content
// tag.
func publicationLink(ctx context.Context, tx pgx.Tx, a *access, ws string, acct *socialAccountView, landing, content string) (string, error) {
	if landing == "" {
		return "", nil
	}
	if _, err := utm.Destination(landing); err != nil {
		return "", errInvalid("link_url is an http or https address")
	}
	name := acct.Name
	if name == "" {
		name = acct.Handle
	}
	c, created, err := utm.Ensure(ctx, tx, ws, utm.Spec{Channel: "organic_social", Source: acct.Channel, Name: name, BrandID: acct.BrandID,
		ObjectKind: "social_account", ObjectID: acct.ID})
	if err != nil {
		return "", err
	}
	if created {
		var p *principal
		if a != nil {
			p = &a.principal
		}
		if err := record(ctx, tx, p, ws, "tracking_campaign.created", "tracking_campaign", c.ID,
			map[string]any{"channel": c.Channel, "source": c.Source, "object_kind": "social_account"}); err != nil {
			return "", err
		}
	}
	link, err := c.Link(landing, content)
	if err != nil {
		return "", errInvalid("link_url is an http or https address")
	}
	return link, nil
}

// checkSchedule refuses a time in the past or more than a year away. A
// time a minute or so ago is taken as now.
func checkSchedule(t *time.Time) error {
	if t == nil {
		return nil
	}
	switch {
	case t.Before(time.Now().Add(-2 * time.Minute)):
		return errInvalid("scheduled_for is in the past; leave it out to post as soon as it is approved")
	case t.After(time.Now().AddDate(1, 0, 0)):
		return errInvalid("scheduled_for is at most a year away")
	}
	return nil
}

type newPublication struct {
	acct         *socialAccountView
	body, link   string
	prompt       string
	scheduledFor *time.Time
	status, via  string
	seriesID     *string
	approvedBy   *string
}

// insertPublication adds a publication in tx and returns its id, with
// its link tagged.
func insertPublication(ctx context.Context, tx pgx.Tx, a *access, ws string, p newPublication) (string, error) {
	content := p.acct.Channel + "-" + strings.ReplaceAll(newID(), "-", "")[:12]
	link, err := publicationLink(ctx, tx, a, ws, p.acct, p.link, content)
	if err != nil {
		return "", err
	}
	var createdBy, mission *string
	if a != nil && a.ActorID != "" {
		createdBy = &a.ActorID
	}
	if m := missions.ID(ctx); m != "" {
		mission = &m
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO social_publications (workspace_id, brand_id, account_id, integration_id, channel, body, link_url, utm_content,
			prompt, scheduled_for, status, drafted_via, series_id, mission_id, created_by, approved_by, approved_at, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, CASE WHEN $16::uuid IS NULL THEN NULL ELSE now() END, NULLIF($17, ''))
		RETURNING id::text`,
		ws, p.acct.BrandID, p.acct.ID, p.acct.IntegrationID, p.acct.Channel, p.body, link, content, p.prompt, p.scheduledFor, p.status, p.via,
		p.seriesID, mission, createdBy, p.approvedBy, RequestID(ctx)).Scan(&id)
	return id, err
}

// enqueuePublish queues the post for its time. The key carries the
// approval's time, so a publication approved again after an edit gets a
// new job, and the job itself only posts an approved publication once.
func enqueuePublish(ctx context.Context, tx pgx.Tx, ws, id string, approvedAt time.Time, at *time.Time) error {
	j := jobs.NewJob{Kind: jobSocialPublish, WorkspaceID: &ws, Payload: map[string]string{"publication_id": id},
		IdempotencyKey: "social.publish:" + id + ":" + strconv.FormatInt(approvedAt.UnixMicro(), 10), MaxAttempts: 4}
	if at != nil && at.After(time.Now()) {
		j.RunAt = *at
	}
	_, err := jobs.Enqueue(ctx, tx, j)
	return err
}

// enqueuePublicationDraft queues the AI draft of a publication.
func enqueuePublicationDraft(ctx context.Context, tx pgx.Tx, ws, id string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSocialPublishDraft, WorkspaceID: &ws, Payload: map[string]string{"publication_id": id},
		IdempotencyKey: "social.publish_draft:" + id + ":" + strconv.FormatInt(time.Now().UnixMicro(), 10), MaxAttempts: 3})
	return err
}

// createSocialPublication adds a post written by a person or agent, or
// asks the workspace's own AI to draft one from a prompt.
func (s *Server) createSocialPublication(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		AccountID    string     `json:"account_id"`
		Body         string     `json:"body"`
		LinkURL      string     `json:"link_url"`
		Prompt       string     `json:"prompt"`
		ScheduledFor *time.Time `json:"scheduled_for"`
	}
	if !decode(w, r, &req) {
		return
	}
	acct, ok := s.publishingAccount(w, r, a, req.AccountID)
	if !ok {
		return
	}
	body, prompt := strings.TrimSpace(req.Body), strings.TrimSpace(req.Prompt)
	ctx := r.Context()
	status, via := "draft", "person"
	switch {
	case body == "" && prompt == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body or prompt is required: write the post, or say what the AI should write about")
		return
	case len(prompt) > 4000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "prompt is at most 4000 characters")
		return
	case body == "":
		ai, err := s.hasAIModel(ctx, a.WorkspaceID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !ai {
			writeError(w, r, http.StatusConflict, "connector_needed", "drafting needs your own AI model: connect one under Settings, AI model, or write the post yourself")
			return
		}
		status, via = "drafting", "ai"
	}
	if !s.socialInvalid(w, r, checkSchedule(req.ScheduledFor)) {
		return
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if id, err = insertPublication(ctx, tx, a, a.WorkspaceID, newPublication{acct: acct, body: body, link: strings.TrimSpace(req.LinkURL),
			prompt: prompt, scheduledFor: req.ScheduledFor, status: status, via: via}); err != nil {
			return err
		}
		if body != "" {
			var link string
			if err := tx.QueryRow(ctx, `SELECT link_url FROM social_publications WHERE id = $1`, id).Scan(&link); err != nil {
				return err
			}
			if err := s.checkPublicationLength(acct.Channel, body, link); err != nil {
				return err
			}
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "social_publication.created", "social_publication", id,
			map[string]any{"channel": acct.Channel, "account_id": acct.ID, "via": via, "scheduled": req.ScheduledFor != nil, "link": req.LinkURL != ""}); err != nil {
			return err
		}
		if status == "drafting" {
			if err := enqueuePublicationDraft(ctx, tx, a.WorkspaceID, id); err != nil {
				return err
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "social_publication.draft_requested", "social_publication", id,
				map[string]any{"channel": acct.Channel, "account_id": acct.ID})
		}
		return nil
	})
	if !s.socialInvalid(w, r, err) {
		return
	}
	if v, ok := s.findSocialPublication(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updateSocialPublication edits a draft, or an approved post that has not
// gone out yet, which then needs approving again.
func (s *Server) updateSocialPublication(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialPublication(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Body         *string             `json:"body"`
		LinkURL      optional[string]    `json:"link_url"`
		ScheduledFor optional[time.Time] `json:"scheduled_for"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if cur.Status != "draft" && cur.Status != "approved" {
		writeError(w, r, http.StatusConflict, "conflict", "the publication is "+cur.Status+"; only a draft or an approved post that has not gone out can change")
		return
	}
	body, schedule := cur.Body, cur.ScheduledFor
	if req.Body != nil {
		body = strings.TrimSpace(*req.Body)
	}
	if req.ScheduledFor.Set {
		schedule = req.ScheduledFor.Value
		if !s.socialInvalid(w, r, checkSchedule(schedule)) {
			return
		}
	}
	ctx := r.Context()
	acct, ok := s.findSocialAccount(w, r, a, cur.AccountID)
	if !ok {
		return
	}
	changed := map[string]any{"channel": cur.Channel, "body": req.Body != nil, "link": req.LinkURL.Set, "schedule": req.ScheduledFor.Set,
		"unapproved": cur.Status == "approved"}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		link := cur.LinkURL
		if req.LinkURL.Set {
			var err error
			if link, err = publicationLink(ctx, tx, a, a.WorkspaceID, acct, strings.TrimSpace(deref(req.LinkURL.Value)), cur.UTMContent); err != nil {
				return err
			}
		}
		if body == "" {
			return errInvalid("body: the post has no text")
		}
		if err := s.checkPublicationLength(cur.Channel, body, link); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE social_publications SET body = $3, link_url = $4, scheduled_for = $5, status = 'draft',
			approved_by = NULL, approved_at = NULL, status_reason = NULL, updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status IN ('draft', 'approved')`, a.WorkspaceID, cur.ID, body, link, schedule)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_publication.updated", "social_publication", cur.ID, changed)
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the publication is going out or no longer a draft")
		return
	}
	if !s.socialInvalid(w, r, err) {
		return
	}
	if v, ok := s.findSocialPublication(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// cancelSocialPublication says no to a post that has not gone out.
func (s *Server) cancelSocialPublication(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialPublication(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE social_publications SET status = 'cancelled', updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status IN ('drafting', 'draft', 'approved')`, a.WorkspaceID, cur.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_publication.cancelled", "social_publication", cur.ID,
			map[string]any{"channel": cur.Channel, "status": cur.Status})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the publication is "+cur.Status+"; only one that has not gone out can be cancelled")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialPublication(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// canPublishFrom reports whether the account's connection is connected
// and its connector posts.
func (s *Server) canPublishFrom(ctx context.Context, ws string, acct *socialAccountView) (bool, error) {
	if acct.IntegrationID == nil || acct.Status == "disconnected" {
		return false, nil
	}
	var connector, status string
	err := s.pool.QueryRow(ctx, `SELECT connector, status FROM integrations WHERE workspace_id = $1 AND id = $2`, ws, *acct.IntegrationID).Scan(&connector, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || status != "connected" || s.integrations == nil {
		return false, err
	}
	c, ok := s.integrations.Registry.Get(connector)
	return ok && c.Definition().Has(connectors.CapSocialPublish), nil
}

// approveSocialPublication is a person saying yes to a post: a job posts
// it at its time, or now. Only a person can approve, never an API token
// or an agent.
func (s *Server) approveSocialPublication(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person can approve a post, in Outflare under Social")
		return
	}
	cur, ok := s.findSocialPublication(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if cur.Status != "draft" {
		writeError(w, r, http.StatusConflict, "conflict", "the publication is "+cur.Status+"; only a draft can be approved")
		return
	}
	if strings.TrimSpace(cur.Body) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body: the post has no text")
		return
	}
	if !s.socialInvalid(w, r, s.checkPublicationLength(cur.Channel, cur.Body, cur.LinkURL)) {
		return
	}
	ctx := r.Context()
	acct, ok := s.findSocialAccount(w, r, a, cur.AccountID)
	if !ok {
		return
	}
	can, err := s.canPublishFrom(ctx, a.WorkspaceID, acct)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !can {
		writeError(w, r, http.StatusConflict, "connector_needed", "sign in again to the account's "+socialChannelName(cur.Channel)+" connection under Connectors, then approve")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var approvedAt time.Time
		err := tx.QueryRow(ctx, `UPDATE social_publications SET status = 'approved', approved_by = $3, approved_at = now(), integration_id = $4,
			status_reason = NULL, updated_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'draft' RETURNING approved_at`,
			a.WorkspaceID, cur.ID, a.ActorID, *acct.IntegrationID).Scan(&approvedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		if err := enqueuePublish(ctx, tx, a.WorkspaceID, cur.ID, approvedAt, cur.ScheduledFor); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_publication.approved", "social_publication", cur.ID,
			map[string]any{"channel": cur.Channel, "account_id": cur.AccountID, "drafted_via": cur.DraftedVia, "scheduled": cur.ScheduledFor != nil})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the publication is no longer a draft")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialPublication(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func socialChannelName(channel string) string {
	if n := map[string]string{"x": "X", "linkedin": "LinkedIn", "reddit": "Reddit"}[channel]; n != "" {
		return n
	}
	return channel
}

// draftSocialPublication has the workspace's own AI (re)write a draft, in
// a job.
func (s *Server) draftSocialPublication(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialPublication(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Prompt *string `json:"prompt"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	prompt := cur.Prompt
	if req.Prompt != nil {
		prompt = strings.TrimSpace(*req.Prompt)
	}
	switch {
	case cur.Status != "draft":
		writeError(w, r, http.StatusConflict, "conflict", "the publication is "+cur.Status+"; only a draft can be drafted again")
		return
	case prompt == "" && cur.Body == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "prompt is required: say what the AI should write about")
		return
	case len(prompt) > 4000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "prompt is at most 4000 characters")
		return
	}
	ctx := r.Context()
	ai, err := s.hasAIModel(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !ai {
		writeError(w, r, http.StatusConflict, "connector_needed", "drafting needs your own AI model: connect one under Settings, AI model, or write the post yourself")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE social_publications SET status = 'drafting', prompt = $3, drafted_via = 'ai', status_reason = NULL, updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'draft'`, a.WorkspaceID, cur.ID, prompt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		if err := enqueuePublicationDraft(ctx, tx, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_publication.draft_requested", "social_publication", cur.ID,
			map[string]any{"channel": cur.Channel, "account_id": cur.AccountID})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the publication is no longer a draft")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialPublication(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusAccepted, v)
	}
}

// ---- Series ----

type socialSeriesView struct {
	ID         string          `json:"id"`
	AccountID  string          `json:"account_id"`
	Channel    string          `json:"channel"`
	BrandID    *string         `json:"brand_id"`
	Name       string          `json:"name"`
	Template   string          `json:"template"`
	DailyLimit int             `json:"daily_limit"`
	Paused     bool            `json:"paused"`
	CreatedBy  *string         `json:"created_by"`
	ApprovedBy *socialActorRef `json:"approved_by"`
	ApprovedAt *time.Time      `json:"approved_at"`
	// Posts handed in over the last 24 hours, against daily_limit.
	PostsToday int        `json:"posts_today"`
	LastPostAt *time.Time `json:"last_post_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

const socialSeriesSelect = `
	SELECT x.id::text, x.account_id::text, a.channel, x.brand_id::text, x.name, x.template, x.daily_limit, x.paused, x.created_by::text,
	       ap.id::text, ap.display_name, x.approved_at,
	       (SELECT count(*) FROM social_publications p WHERE p.series_id = x.id AND p.status <> 'cancelled' AND p.created_at > now() - interval '24 hours')::int,
	       (SELECT max(p.created_at) FROM social_publications p WHERE p.series_id = x.id),
	       x.created_at, x.updated_at
	FROM social_post_series x
	JOIN social_accounts a ON a.id = x.account_id
	LEFT JOIN actors ap ON ap.id = x.approved_by
	WHERE x.workspace_id = $1`

func scanSocialSeries(row pgx.Row) (socialSeriesView, error) {
	var v socialSeriesView
	var apID, apName *string
	err := row.Scan(&v.ID, &v.AccountID, &v.Channel, &v.BrandID, &v.Name, &v.Template, &v.DailyLimit, &v.Paused, &v.CreatedBy,
		&apID, &apName, &v.ApprovedAt, &v.PostsToday, &v.LastPostAt, &v.CreatedAt, &v.UpdatedAt)
	if apID != nil {
		v.ApprovedBy = &socialActorRef{*apID, deref(apName)}
	}
	return v, err
}

func (s *Server) listSocialSeries(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	acct := r.URL.Query().Get("account_id")
	if acct != "" && !validUUID(acct) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is an account id")
		return
	}
	args := []any{a.WorkspaceID, acct}
	rows, err := s.pool.Query(r.Context(), socialSeriesSelect+` AND ($2 = '' OR x.account_id = NULLIF($2, '')::uuid)`+sel.sql("x.brand_id", &args)+
		` ORDER BY x.created_at LIMIT 500`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialSeriesView, error) { return scanSocialSeries(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []socialSeriesView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findSocialSeries(w http.ResponseWriter, r *http.Request, a *access, id string) (*socialSeriesView, bool) {
	if validUUID(id) {
		v, err := scanSocialSeries(s.pool.QueryRow(r.Context(), socialSeriesSelect+` AND x.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "series not found")
	return nil, false
}

func (s *Server) getSocialSeries(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSocialSeries(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func checkSeriesFields(name, template *string, limit *int) error {
	switch {
	case name != nil && (strings.TrimSpace(*name) == "" || len(*name) > 200):
		return errInvalid("name is 1 to 200 characters")
	case template != nil && len(*template) > 4000:
		return errInvalid("template is at most 4000 characters")
	case limit != nil && (*limit < 1 || *limit > 24):
		return errInvalid("daily_limit is 1 to 24")
	}
	return nil
}

// createSocialSeries adds a recurring series, unapproved until a person
// approves it.
func (s *Server) createSocialSeries(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		AccountID  string `json:"account_id"`
		Name       string `json:"name"`
		Template   string `json:"template"`
		DailyLimit *int   `json:"daily_limit"`
	}
	if !decode(w, r, &req) {
		return
	}
	acct, ok := s.publishingAccount(w, r, a, req.AccountID)
	if !ok {
		return
	}
	limit := 1
	if req.DailyLimit != nil {
		limit = *req.DailyLimit
	}
	if !s.socialInvalid(w, r, checkSeriesFields(&req.Name, &req.Template, &limit)) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO social_post_series (workspace_id, brand_id, account_id, name, template, daily_limit, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, a.WorkspaceID, acct.BrandID, acct.ID, strings.TrimSpace(req.Name),
			strings.TrimSpace(req.Template), limit, createdBy).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_series.created", "social_series", id,
			map[string]any{"channel": acct.Channel, "account_id": acct.ID, "daily_limit": limit})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialSeries(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updateSocialSeries renames, pauses or resumes a series, or changes what
// it posts. A new account, template or a higher daily limit takes its
// approval away until a person approves it again.
func (s *Server) updateSocialSeries(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialSeries(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		AccountID  *string `json:"account_id"`
		Name       *string `json:"name"`
		Template   *string `json:"template"`
		DailyLimit *int    `json:"daily_limit"`
		Paused     *bool   `json:"paused"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if !s.socialInvalid(w, r, checkSeriesFields(req.Name, req.Template, req.DailyLimit)) {
		return
	}
	next := *cur
	unapprove := false
	if req.AccountID != nil && *req.AccountID != cur.AccountID {
		acct, ok := s.publishingAccount(w, r, a, *req.AccountID)
		if !ok {
			return
		}
		next.AccountID, next.BrandID, unapprove = acct.ID, acct.BrandID, true
	}
	if req.Name != nil {
		next.Name = strings.TrimSpace(*req.Name)
	}
	if req.Template != nil && strings.TrimSpace(*req.Template) != cur.Template {
		next.Template, unapprove = strings.TrimSpace(*req.Template), true
	}
	if req.DailyLimit != nil {
		unapprove = unapprove || *req.DailyLimit > cur.DailyLimit
		next.DailyLimit = *req.DailyLimit
	}
	if req.Paused != nil {
		next.Paused = *req.Paused
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE social_post_series SET account_id = $3, brand_id = $4, name = $5, template = $6, daily_limit = $7, paused = $8,
			approved_by = CASE WHEN $9 THEN NULL ELSE approved_by END, approved_at = CASE WHEN $9 THEN NULL ELSE approved_at END, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, next.AccountID, next.BrandID, next.Name, next.Template, next.DailyLimit,
			next.Paused, unapprove); err != nil {
			return err
		}
		data := map[string]any{"channel": cur.Channel, "unapproved": unapprove && cur.ApprovedAt != nil, "daily_limit": next.DailyLimit}
		if next.Paused != cur.Paused {
			action := "social_series.resumed"
			if next.Paused {
				action = "social_series.paused"
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "social_series", cur.ID, map[string]any{"channel": cur.Channel}); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_series.updated", "social_series", cur.ID, data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialSeries(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// approveSocialSeries is a person approving every post handed in for the
// series, up to its daily limit. Only a person can approve.
func (s *Server) approveSocialSeries(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person can approve a series, in Outflare under Social")
		return
	}
	cur, ok := s.findSocialSeries(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	acct, ok := s.findSocialAccount(w, r, a, cur.AccountID)
	if !ok {
		return
	}
	can, err := s.canPublishFrom(ctx, a.WorkspaceID, acct)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !can {
		writeError(w, r, http.StatusConflict, "connector_needed", "sign in again to the account's "+socialChannelName(cur.Channel)+" connection under Connectors, then approve")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE social_post_series SET approved_by = $3, approved_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, a.ActorID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_series.approved", "social_series", cur.ID,
			map[string]any{"channel": cur.Channel, "account_id": cur.AccountID, "daily_limit": cur.DailyLimit})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialSeries(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// addSocialSeriesPost queues a post for an approved series with no other
// approval: the caller hands in the text (a daily changelog summary), and
// the series' daily limit caps how many go out.
func (s *Server) addSocialSeriesPost(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialSeries(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Body         string     `json:"body"`
		LinkURL      string     `json:"link_url"`
		ScheduledFor *time.Time `json:"scheduled_for"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	body := strings.TrimSpace(req.Body)
	switch {
	case cur.ApprovedAt == nil || cur.ApprovedBy == nil:
		writeError(w, r, http.StatusConflict, "conflict", "the series is not approved; a person approves it in Outflare under Social")
		return
	case cur.Paused:
		writeError(w, r, http.StatusConflict, "conflict", "the series is paused")
		return
	case body == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is required")
		return
	}
	if !s.socialInvalid(w, r, checkSchedule(req.ScheduledFor)) {
		return
	}
	ctx := r.Context()
	acct, ok := s.publishingAccount(w, r, a, cur.AccountID)
	if !ok {
		return
	}
	can, err := s.canPublishFrom(ctx, a.WorkspaceID, acct)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !can {
		writeError(w, r, http.StatusConflict, "connector_needed", "sign in again to the account's "+socialChannelName(acct.Channel)+" connection under Connectors")
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The daily limit, counted under the series' lock so two posts
		// cannot both take the last place, and the approval read again
		// under it.
		var approver *string
		var limit int
		var paused bool
		if err := tx.QueryRow(ctx, `SELECT approved_by::text, daily_limit, paused FROM social_post_series WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
			cur.ID, a.WorkspaceID).Scan(&approver, &limit, &paused); err != nil {
			return err
		}
		if approver == nil || paused {
			return errConflict
		}
		var today int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM social_publications WHERE series_id = $1 AND status <> 'cancelled' AND created_at > now() - interval '24 hours'`,
			cur.ID).Scan(&today); err != nil {
			return err
		}
		if today >= limit {
			return errLimit
		}
		var err error
		if id, err = insertPublication(ctx, tx, a, a.WorkspaceID, newPublication{acct: acct, body: body, link: strings.TrimSpace(req.LinkURL),
			scheduledFor: req.ScheduledFor, status: "approved", via: "series", seriesID: &cur.ID, approvedBy: approver}); err != nil {
			return err
		}
		var link string
		var approvedAt time.Time
		if err := tx.QueryRow(ctx, `SELECT link_url, approved_at FROM social_publications WHERE id = $1`, id).
			Scan(&link, &approvedAt); err != nil {
			return err
		}
		if err := s.checkPublicationLength(acct.Channel, body, link); err != nil {
			return err
		}
		if err := enqueuePublish(ctx, tx, a.WorkspaceID, id, approvedAt, req.ScheduledFor); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_publication.created", "social_publication", id,
			map[string]any{"channel": acct.Channel, "account_id": acct.ID, "via": "series", "series_id": cur.ID, "scheduled": req.ScheduledFor != nil,
				"link": req.LinkURL != ""})
	})
	switch {
	case errors.Is(err, errLimit):
		writeError(w, r, http.StatusConflict, "conflict", "the series already has its "+strconv.Itoa(cur.DailyLimit)+
			" posts for the last 24 hours (its daily_limit); hand this one in later")
		return
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "the series is paused or no longer approved")
		return
	}
	if !s.socialInvalid(w, r, err) {
		return
	}
	if v, ok := s.findSocialPublication(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

var errLimit = errors.New("limit reached")
