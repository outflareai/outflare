package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/holdout"
	"github.com/outflareai/outflare/engine/internal/missions"
)

// Broadcasts: one lifecycle email to many people on customer records at
// once, such as a newsletter or a launch. The audience is a topic's
// subscribers, a list of records, or a saved list or segment (audience
// kind list, a smart list). A service broadcast sends a transactional
// template to a list or records whatever people chose about marketing. Sending is a job (emails.StartBroadcast).

type broadcastAudience struct {
	Kind      string   `json:"kind"`
	RecordIDs []string `json:"record_ids"`
	// The saved list (smart list id) of audience kind list.
	Ref *string `json:"ref"`
}

func (a broadcastAudience) of() emails.Audience {
	return emails.Audience{Kind: a.Kind, RecordIDs: a.RecordIDs, Ref: deref(a.Ref)}
}

type broadcastStats struct {
	Emails       int `json:"emails"`
	Sent         int `json:"sent"`
	Delivered    int `json:"delivered"`
	Opened       int `json:"opened"`
	Clicked      int `json:"clicked"`
	Bounced      int `json:"bounced"`
	Complained   int `json:"complained"`
	Unsubscribed int `json:"unsubscribed"`
	NotSent      int `json:"not_sent"`
}

type emailBroadcastView struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Status          string            `json:"status"`
	StatusReason    *string           `json:"status_reason"`
	Template        templateRef       `json:"template"`
	TemplateVersion *int              `json:"template_version"`
	Topic           *topicRef         `json:"topic"`
	From            string            `json:"from"`
	ProviderID      *string           `json:"provider_id"`
	BrandID         *string           `json:"brand_id"`
	Audience        broadcastAudience `json:"audience"`
	// A service message: a transactional template sent to everyone in the
	// audience whatever they chose about marketing email (suppressions
	// still apply), such as a notice about their account.
	Service bool `json:"service"`
	// Who it leaves out: people on these lists or with these tags, or at
	// a company with one. They are skipped with the reason.
	Exclude customers.Exclusion `json:"exclude"`
	// How many people the audience has now, before anyone's choices: only
	// on a single broadcast that has not started.
	AudienceSize *int `json:"audience_size"`
	// How many of them the exclusion leaves out: only with audience_size.
	AudienceExcluded *int `json:"audience_excluded,omitempty"`
	// The audience by contact basis, and what looks wrong about sending
	// to it: only on a single broadcast that has not started.
	AudienceBases map[string]int   `json:"audience_bases,omitempty"`
	Warnings      []contactWarning `json:"warnings,omitempty"`
	Data          map[string]any   `json:"data"`
	Track         struct {
		Opens  *bool `json:"opens"`
		Clicks *bool `json:"clicks"`
	} `json:"track"`
	SendAt     *time.Time `json:"send_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Recipients int        `json:"recipients"`
	Skipped    int        `json:"skipped"`
	// The share of the audience held out on purpose, and how many people
	// were (not counted in skipped).
	HoldoutPercent int            `json:"holdout_percent"`
	HeldOut        int            `json:"held_out"`
	Stats          broadcastStats `json:"stats"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type templateRef struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
}

const emailBroadcastSelect = `
	SELECT b.id, b.name, b.status, b.status_reason, t.id, t.handle, t.name, b.template_version, tp.id, tp.key, tp.name,
	       b.from_address, b.provider_id, b.brand_id, b.audience_kind, b.record_ids::text[], b.audience_ref, b.data, b.exclude, b.service,
	       b.track_opens, b.track_clicks, b.send_at, b.started_at, b.finished_at, b.recipients, b.skipped, b.holdout_percent, b.held_out,
	       COALESCE(st.emails, 0), COALESCE(st.sent, 0), COALESCE(st.delivered, 0), COALESCE(st.opened, 0), COALESCE(st.clicked, 0),
	       COALESCE(st.bounced, 0), COALESCE(st.complained, 0), COALESCE(st.not_sent, 0),
	       (SELECT count(DISTINCT ev.email_id) FROM email_events ev JOIN emails ue ON ue.id = ev.email_id
	          WHERE ue.broadcast_id = b.id AND ev.type = 'unsubscribed')::int,
	       b.created_at, b.updated_at
	FROM email_broadcasts b
	JOIN email_templates t ON t.id = b.template_id
	LEFT JOIN email_topics tp ON tp.id = b.topic_id
	LEFT JOIN LATERAL (
		SELECT count(*)::int AS emails,
		       count(*) FILTER (WHERE e.status IN ('sent', 'delivered', 'delayed', 'bounced', 'complained'))::int AS sent,
		       count(*) FILTER (WHERE e.status IN ('delivered', 'complained'))::int AS delivered,
		       count(*) FILTER (WHERE e.opened_at IS NOT NULL)::int AS opened,
		       count(*) FILTER (WHERE e.clicked_at IS NOT NULL)::int AS clicked,
		       count(*) FILTER (WHERE e.status = 'bounced')::int AS bounced,
		       count(*) FILTER (WHERE e.status = 'complained')::int AS complained,
		       count(*) FILTER (WHERE e.status IN ('suppressed', 'failed', 'cancelled'))::int AS not_sent
		FROM emails e WHERE e.broadcast_id = b.id) st ON true
	WHERE b.workspace_id = $1`

func scanEmailBroadcast(row pgx.Row) (emailBroadcastView, error) {
	var v emailBroadcastView
	var topic nullTopic
	var data, exclude []byte
	err := row.Scan(&v.ID, &v.Name, &v.Status, &v.StatusReason, &v.Template.ID, &v.Template.Handle, &v.Template.Name, &v.TemplateVersion,
		&topic.ID, &topic.Key, &topic.Name, &v.From, &v.ProviderID, &v.BrandID, &v.Audience.Kind, &v.Audience.RecordIDs, &v.Audience.Ref, &data, &exclude, &v.Service,
		&v.Track.Opens, &v.Track.Clicks, &v.SendAt, &v.StartedAt, &v.FinishedAt, &v.Recipients, &v.Skipped, &v.HoldoutPercent, &v.HeldOut,
		&v.Stats.Emails, &v.Stats.Sent, &v.Stats.Delivered, &v.Stats.Opened, &v.Stats.Clicked, &v.Stats.Bounced, &v.Stats.Complained,
		&v.Stats.NotSent, &v.Stats.Unsubscribed, &v.CreatedAt, &v.UpdatedAt)
	v.Topic = topic.ref()
	v.Data = map[string]any{}
	_ = json.Unmarshal(data, &v.Data)
	_ = json.Unmarshal(exclude, &v.Exclude)
	v.Exclude = v.Exclude.Clean()
	if v.Audience.RecordIDs == nil {
		v.Audience.RecordIDs = []string{}
	}
	return v, err
}

var broadcastStatuses = []string{"draft", "scheduled", "sending", "sent", "cancelled", "failed"}

func (s *Server) listEmailBroadcasts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains(broadcastStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of "+strings.Join(broadcastStatuses, ", "))
		return
	}
	sel, ok := s.dataBrand(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status}
	rows, err := s.pool.Query(r.Context(), emailBroadcastSelect+` AND ($2 = '' OR b.status = $2)`+sel.sql("b.brand_id", &args)+` ORDER BY b.created_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailBroadcastView, error) { return scanEmailBroadcast(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []emailBroadcastView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findEmailBroadcast(w http.ResponseWriter, r *http.Request, ws, id string) (*emailBroadcastView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "broadcast not found")
		return nil, false
	}
	v, err := scanEmailBroadcast(s.pool.QueryRow(r.Context(), emailBroadcastSelect+` AND b.id = $2`, ws, id))
	// On a brand route only the route's brand's broadcasts are found.
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !accessFrom(r.Context()).sees(v.BrandID)) {
		writeError(w, r, http.StatusNotFound, "not_found", "broadcast not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if v.Status == "draft" || v.Status == "scheduled" {
		aud := v.Audience.of()
		n, err := emails.AudienceSize(r.Context(), s.pool, ws, v.BrandID, aud)
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		v.AudienceSize = &n
		if !v.Exclude.Empty() {
			x, err := emails.AudienceExcluded(r.Context(), s.pool, ws, v.BrandID, aud, v.Exclude)
			if err != nil {
				s.internal(w, r, err)
				return nil, false
			}
			v.AudienceExcluded = &x
		}
		if v.AudienceBases, err = emails.AudienceBases(r.Context(), s.pool, ws, v.BrandID, aud); err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		// What people chose about marketing doesn't stop a service
		// message, so there's nothing to warn about.
		if !v.Service {
			rule, err := customers.MarketingRule(r.Context(), s.pool, ws)
			if err != nil {
				s.internal(w, r, err)
				return nil, false
			}
			v.Warnings = audienceWarnings(v.AudienceBases, rule)
		}
	}
	return &v, true
}

func (s *Server) getEmailBroadcast(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type broadcastFields struct {
	Name       *string              `json:"name"`
	Template   *string              `json:"template"`
	Topic      optional[string]     `json:"topic"`
	From       *string              `json:"from"`
	ProviderID optional[string]     `json:"provider_id"`
	BrandID    optional[string]     `json:"brand_id"`
	Audience   *broadcastAudience   `json:"audience"`
	Service    *bool                `json:"service"`
	Data       map[string]any       `json:"data"`
	Exclude    *customers.Exclusion `json:"exclude"`
	Track      *struct {
		Opens  optional[bool] `json:"opens"`
		Clicks optional[bool] `json:"clicks"`
	} `json:"track"`
	// The share of the audience to hold out on purpose, 0 to 50 percent.
	HoldoutPercent *int `json:"holdout_percent"`
}

// broadcastRow is what a create or update writes.
type broadcastRow struct {
	name, templateID, from, audience string
	topicID, providerID, brandID     *string
	ref                              *string
	service                          bool
	recordIDs                        []string
	data                             map[string]any
	exclude                          customers.Exclusion
	trackOpens, trackClicks          *bool
	holdout                          int
}

// apply checks f and applies it to b.
func (s *Server) applyBroadcast(r *http.Request, ws string, f broadcastFields, b *broadcastRow) error {
	ctx := r.Context()
	if f.Name != nil {
		if n := strings.TrimSpace(*f.Name); n == "" || len(n) > 200 {
			return errInvalid("name must be 1 to 200 characters")
		}
		b.name = strings.TrimSpace(*f.Name)
	}
	// A broadcast belongs to one brand, and sends that brand's template
	// to that brand's people.
	brand, err := s.optionalBrand(ctx, accessFrom(ctx), f.BrandID, b.brandID)
	if err != nil {
		return err
	}
	b.brandID = brand
	if f.Template == nil && b.templateID != "" {
		var same bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_templates WHERE id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid)`,
			b.templateID, b.brandID).Scan(&same); err != nil {
			return err
		}
		if !same {
			return errInvalid("the broadcast's template belongs to another brand; name one of this brand's with template")
		}
	}
	if f.Template != nil {
		var id, purpose, status string
		var topic *string
		err := s.pool.QueryRow(ctx, `SELECT id, purpose, status, topic_id FROM email_templates WHERE workspace_id = $1 AND (id::text = $2 OR handle = $2)
			AND brand_id IS NOT DISTINCT FROM $3::uuid`, ws, *f.Template, b.brandID).Scan(&id, &purpose, &status, &topic)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return errInvalid("template not found: " + truncate(*f.Template, 63))
		case err != nil:
			return err
		case status != "active":
			return errInvalid("the template is archived; restore it first")
		}
		if b.templateID != id && !f.Topic.Set {
			b.topicID = topic
		}
		b.templateID = id
	}
	if f.Topic.Set {
		id, err := topicID(ctx, s.pool, ws, b.brandID, deref(f.Topic.Value))
		if err != nil {
			return err
		}
		b.topicID = id
	}
	if f.From != nil {
		b.from = strings.TrimSpace(*f.From)
		if len(b.from) > 320 {
			return errInvalid("from must be at most 320 characters")
		}
	}
	if f.ProviderID.Set {
		if f.ProviderID.Value != nil && !validUUID(*f.ProviderID.Value) {
			return errInvalid("provider_id must be an email provider id")
		}
		b.providerID = f.ProviderID.Value
	}
	if f.Audience != nil {
		switch f.Audience.Kind {
		case "topic":
			if len(f.Audience.RecordIDs) > 0 {
				return errInvalid("record_ids only go with audience kind records")
			}
			b.recordIDs, b.ref = []string{}, nil
		case "records":
			if len(f.Audience.RecordIDs) == 0 || len(f.Audience.RecordIDs) > 10000 {
				return errInvalid("audience kind records needs 1 to 10,000 record_ids")
			}
			for _, id := range f.Audience.RecordIDs {
				if !validUUID(id) {
					return errInvalid("record_ids must be record ids")
				}
			}
			b.recordIDs, b.ref = f.Audience.RecordIDs, nil
		case "list":
			if len(f.Audience.RecordIDs) > 0 {
				return errInvalid("record_ids only go with audience kind records")
			}
			if f.Audience.Ref == nil || !validUUID(*f.Audience.Ref) {
				return errInvalid("audience kind list needs ref: a list's id, from GET .../smart-lists or GET .../lead-lists")
			}
			// The list must be this workspace's, and of the broadcast's
			// brand or shared by every brand.
			var ok bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM smart_lists WHERE workspace_id = $1 AND id = $2
				AND (brand_id IS NULL OR $3::uuid IS NULL OR brand_id = $3::uuid))
				OR EXISTS (SELECT 1 FROM lead_lists WHERE workspace_id = $1 AND id = $2
				AND (brand_id IS NULL OR $3::uuid IS NULL OR brand_id = $3::uuid))`, ws, *f.Audience.Ref, b.brandID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("list not found: ref must be a list of this brand, from GET .../smart-lists or GET .../lead-lists")
			}
			b.recordIDs, b.ref = []string{}, f.Audience.Ref
		default:
			return errInvalid("audience kind is topic, records or list")
		}
		b.audience = f.Audience.Kind
	}
	if f.Service != nil {
		b.service = *f.Service
	}
	// A marketing broadcast sends a lifecycle template, a service message
	// a transactional one; a topic's audience is for marketing only.
	if b.templateID != "" {
		var purpose string
		if err := s.pool.QueryRow(ctx, `SELECT purpose FROM email_templates WHERE id = $1`, b.templateID).Scan(&purpose); err != nil {
			return err
		}
		switch {
		case b.service && purpose != "transactional":
			return errInvalid("a service broadcast sends a transactional template; this one is " + purpose + " (or leave service off)")
		case !b.service && purpose != "lifecycle":
			return errInvalid("this template is transactional: set service to true to send it as a service message, or pick a lifecycle template")
		}
	}
	if b.service && b.audience == "topic" {
		return errInvalid("a service broadcast goes to a list or to records, not a topic's subscribers")
	}
	if f.Data != nil {
		if raw, _ := json.Marshal(f.Data); len(raw) > 32<<10 {
			return errInvalid("data must be at most 32 KB of JSON")
		}
		b.data = f.Data
	}
	if f.Exclude != nil {
		x, err := s.checkExclusion(r, ws, *f.Exclude)
		if err != nil {
			return err
		}
		b.exclude = x
	}
	if f.HoldoutPercent != nil {
		if *f.HoldoutPercent < 0 || *f.HoldoutPercent > holdout.Max {
			return errInvalid("holdout_percent must be 0 to 50")
		}
		b.holdout = *f.HoldoutPercent
	}
	if b.service && b.holdout > 0 {
		return errInvalid("a service message holds nobody out: set holdout_percent to 0")
	}
	if f.Track != nil {
		if f.Track.Opens.Set {
			b.trackOpens = f.Track.Opens.Value
		}
		if f.Track.Clicks.Set {
			b.trackClicks = f.Track.Clicks.Value
		}
	}
	return nil
}

func (s *Server) createEmailBroadcast(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f broadcastFields
	if !decode(w, r, &f) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if f.Name == nil || f.Template == nil {
		bad("name and template are required")
		return
	}
	b := broadcastRow{audience: "topic", recordIDs: []string{}, data: map[string]any{}, exclude: customers.Exclusion{}.Clean()}
	err := s.applyBroadcast(r, a.WorkspaceID, f, &b)
	var inv errInvalid
	if errors.As(err, &inv) {
		bad(string(inv))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		data, _ := json.Marshal(b.data)
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO email_broadcasts (workspace_id, name, template_id, topic_id, from_address, provider_id, brand_id,
			audience_kind, record_ids, data, track_opens, track_clicks, created_by, request_id, exclude, mission_id, holdout_percent,
			audience_ref, service)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid[], $10, $11, $12, $13, NULLIF($14, ''), $15, NULLIF($16, '')::uuid, $17, $18, $19) RETURNING id`,
			a.WorkspaceID, b.name, b.templateID, b.topicID, b.from, b.providerID, b.brandID, b.audience, b.recordIDs, data,
			b.trackOpens, b.trackClicks, createdBy, RequestID(ctx), b.exclude, missions.ID(ctx), b.holdout, b.ref, b.service).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_broadcast.created", "email_broadcast", id,
			map[string]any{"audience": b.audience, "has_topic": b.topicID != nil, "holdout_percent": b.holdout, "service": b.service})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updateEmailBroadcast changes a draft; once it is sending only the name
// can change.
func (s *Server) updateEmailBroadcast(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var f broadcastFields
	if !decode(w, r, &f) {
		return
	}
	onlyName := f.Template == nil && !f.Topic.Set && f.From == nil && !f.ProviderID.Set && !f.BrandID.Set && f.Audience == nil && f.Data == nil && f.Track == nil && f.Exclude == nil && f.HoldoutPercent == nil && f.Service == nil
	if cur.Status != "draft" && !onlyName {
		writeError(w, r, http.StatusConflict, "conflict", "the broadcast is "+cur.Status+"; only its name can change")
		return
	}
	b := broadcastRow{name: cur.Name, templateID: cur.Template.ID, from: cur.From, audience: cur.Audience.Kind, providerID: cur.ProviderID,
		brandID: cur.BrandID, recordIDs: cur.Audience.RecordIDs, data: cur.Data, exclude: cur.Exclude, trackOpens: cur.Track.Opens, trackClicks: cur.Track.Clicks,
		holdout: cur.HoldoutPercent, ref: cur.Audience.Ref, service: cur.Service}
	if cur.Topic != nil {
		b.topicID = &cur.Topic.ID
	}
	err := s.applyBroadcast(r, a.WorkspaceID, f, &b)
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		data, _ := json.Marshal(b.data)
		if _, err := tx.Exec(ctx, `UPDATE email_broadcasts SET name = $3, template_id = $4, topic_id = $5, from_address = $6, provider_id = $7,
			brand_id = $8, audience_kind = $9, record_ids = $10::uuid[], data = $11, track_opens = $12, track_clicks = $13, exclude = $14,
			holdout_percent = $15, audience_ref = $16, service = $17, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, b.name, b.templateID, b.topicID, b.from, b.providerID, b.brandID,
			b.audience, b.recordIDs, data, b.trackOpens, b.trackClicks, b.exclude, b.holdout, b.ref, b.service); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_broadcast.updated", "email_broadcast", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// sendEmailBroadcast starts a draft now or at send_at, after checking that
// its email can go: the template is published, the sender's domain is
// verified and the provider sends lifecycle email.
func (s *Server) sendEmailBroadcast(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		SendAt *time.Time `json:"send_at"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if req.SendAt != nil && req.SendAt.After(time.Now().Add(30*24*time.Hour)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send_at must be within 30 days")
		return
	}
	if cur.Status != "draft" {
		writeError(w, r, http.StatusConflict, "conflict", "the broadcast is "+cur.Status+"; only a draft can be sent")
		return
	}
	ctx := r.Context()
	purpose := "lifecycle"
	if cur.Service {
		purpose = "transactional"
	}
	// A dry run with an example person: problems only one person's data
	// could cause are left to the send, which skips that person.
	_, err := s.emails.Compose(ctx, s.pool, emails.Draft{NewEmail: emails.NewEmail{WorkspaceID: a.WorkspaceID, BrandID: cur.BrandID,
		From: cur.From, To: []string{"person@example.com"}, Purpose: purpose, ProviderID: deref(cur.ProviderID)},
		TemplateID: cur.Template.ID, Data: cur.Data, TopicSet: true})
	var ce *emails.ComposeError
	if errors.As(err, &ce) && ce.Code == "template_variables_missing" {
		err = nil
	}
	if !s.composeOK(w, r, err) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		started, err := s.emails.StartBroadcast(ctx, tx, s.emailWho(ctx, a), a.WorkspaceID, cur.ID, req.SendAt)
		if err == nil && !started {
			return errConflict
		}
		return err
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the broadcast is no longer a draft")
		return
	}
	if !s.composeOK(w, r, err) {
		return
	}
	if v, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) cancelEmailBroadcast(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	var stopped bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		stopped, err = s.emails.CancelBroadcast(ctx, tx, s.emailWho(ctx, a), a.WorkspaceID, cur.ID)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !stopped {
		writeError(w, r, http.StatusConflict, "conflict", "the broadcast is "+cur.Status+"; only a scheduled or sending broadcast can be cancelled")
		return
	}
	if v, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type broadcastRecipientView struct {
	RecordID string  `json:"record_id"`
	Name     string  `json:"name"`
	Address  *string `json:"address"`
	EmailID  *string `json:"email_id"`
	Status   *string `json:"status"`
	// Why the email has its status, such as the receiving server's reply
	// to a bounce, and whether the bounce is hard or soft.
	StatusReason *string `json:"status_reason"`
	BounceType   *string `json:"bounce_type"`
	Skipped      *string `json:"skipped"`
	// When each thing first happened to this person's email; null when it
	// has not.
	SentAt         *time.Time `json:"sent_at"`
	DeliveredAt    *time.Time `json:"delivered_at"`
	OpenedAt       *time.Time `json:"opened_at"`
	ClickedAt      *time.Time `json:"clicked_at"`
	BouncedAt      *time.Time `json:"bounced_at"`
	UnsubscribedAt *time.Time `json:"unsubscribed_at"`
	Opens          int        `json:"opens"`
	Clicks         int        `json:"clicks"`
	CreatedAt      time.Time  `json:"created_at"`
}

// broadcastRecipientShow is the groups a broadcast's recipients can be
// listed by. Each matches the number of the same name in the broadcast's
// stats, so a count and its list never disagree.
var broadcastRecipientShow = map[string]string{
	"":             "true",
	"sent":         "e.status IN ('sent', 'delivered', 'delayed', 'bounced', 'complained')",
	"delivered":    "e.status IN ('delivered', 'complained')",
	"opened":       "e.opened_at IS NOT NULL",
	"clicked":      "e.clicked_at IS NOT NULL",
	"bounced":      "e.status = 'bounced'",
	"complained":   "e.status = 'complained'",
	"unsubscribed": "EXISTS (SELECT 1 FROM email_events ev WHERE ev.email_id = e.id AND ev.type = 'unsubscribed')",
	"not_sent":     "e.status IN ('suppressed', 'failed', 'cancelled')",
	"skipped":      "br.skipped IS NOT NULL",
}

// listEmailBroadcastRecipients is who a broadcast went to, or skipped and
// why, by name (by address for someone with no name).
func (s *Server) listEmailBroadcastRecipients(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	q := r.URL.Query()
	after, ok := pageCursor(w, r, pageAfter)
	if !ok {
		return
	}
	if after != "" && !validUUID(after) {
		invalidCursor(w, r)
		return
	}
	skipped := q.Get("skipped")
	if skipped != "" && skipped != "true" && skipped != "false" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "skipped is true or false")
		return
	}
	show, ok := broadcastRecipientShow[q.Get("show")]
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "show is sent, delivered, opened, clicked, bounced, complained, unsubscribed, not_sent or skipped")
		return
	}
	search := strings.TrimSpace(q.Get("q"))
	if len(search) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "q is at most 200 characters")
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT br.record_id, COALESCE(rc.name, ''), br.address, br.email_id, e.status, e.status_reason, e.bounce_type, br.skipped,
		       e.sent_at, e.delivered_at, e.opened_at, e.clicked_at,
		       (SELECT min(ev.occurred_at) FROM email_events ev WHERE ev.email_id = e.id AND ev.type = 'bounced'),
		       (SELECT min(ev.occurred_at) FROM email_events ev WHERE ev.email_id = e.id AND ev.type = 'unsubscribed'),
		       COALESCE(e.opens, 0), COALESCE(e.clicks, 0), br.created_at
		FROM email_broadcast_recipients br
		LEFT JOIN records rc ON rc.id = br.record_id
		LEFT JOIN emails e ON e.id = br.email_id
		WHERE br.workspace_id = $1 AND br.broadcast_id = $2
		  AND ($3 = '' OR (lower(COALESCE(NULLIF(rc.name, ''), br.address, '')), br.record_id) > (
		        SELECT lower(COALESCE(NULLIF(r2.name, ''), b2.address, '')), b2.record_id
		        FROM email_broadcast_recipients b2 LEFT JOIN records r2 ON r2.id = b2.record_id
		        WHERE b2.workspace_id = $1 AND b2.broadcast_id = $2 AND b2.record_id = $3::uuid))
		  AND ($4 = '' OR (br.skipped IS NOT NULL) = ($4 = 'true'))
		  AND ($6 = '' OR rc.name ILIKE '%' || $6 || '%' OR br.address ILIKE '%' || $6 || '%')
		  AND `+show+`
		ORDER BY lower(COALESCE(NULLIF(rc.name, ''), br.address, '')), br.record_id LIMIT $5`, a.WorkspaceID, cur.ID, after, skipped, limit+1, likeEscape(search))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (broadcastRecipientView, error) {
		var v broadcastRecipientView
		err := row.Scan(&v.RecordID, &v.Name, &v.Address, &v.EmailID, &v.Status, &v.StatusReason, &v.BounceType, &v.Skipped,
			&v.SentAt, &v.DeliveredAt, &v.OpenedAt, &v.ClickedAt, &v.BouncedAt, &v.UnsubscribedAt, &v.Opens, &v.Clicks, &v.CreatedAt)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		next = &list[limit-1].RecordID
	}
	if list == nil {
		list = []broadcastRecipientView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageAfter, next)})
}
