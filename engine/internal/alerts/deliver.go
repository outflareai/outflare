package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Service emails alerts.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Send is Outflare's account email.
	Send func(ctx context.Context, m sysmail.Message) error
	// PublicURL is where links in the email point.
	PublicURL string
	// Store opens the workspace's own chat connections (Slack).
	Store *connectors.Store
}

// Register handles JobDeliver on q.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobDeliver, s.deliver) }

func (s *Service) deliver(ctx context.Context, job jobs.Job) error {
	var p struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || p.DeliveryID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var d delivery
	err := s.Pool.QueryRow(ctx, `SELECT d.alert_id::text, d.channel, d.user_id::text, d.integration_id::text, COALESCE(d.target, ''), d.status,
			w.name, COALESCE(NULLIF(w.slug, ''), w.id::text)
		FROM alert_deliveries d JOIN workspaces w ON w.id = d.workspace_id WHERE d.id = $1 AND d.workspace_id = $2`, p.DeliveryID, ws).
		Scan(&d.alertID, &d.channel, &d.userID, &d.integrationID, &d.target, &d.status, &d.workspace, &d.slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if d.status != "queued" {
		return nil
	}
	d.id, d.ws = p.DeliveryID, ws
	r, err := Load(ctx, s.Pool, ws, d.alertID)
	if err != nil {
		return err
	}
	if d.channel == "slack" {
		return s.post(ctx, job, d, r)
	}
	return s.email(ctx, job, d, r)
}

type delivery struct {
	id, ws, alertID, channel, target, status, workspace, slug string
	userID, integrationID                                     *string
}

func (s *Service) finish(ctx context.Context, d delivery, status, reason string) error {
	var why *string
	if reason != "" {
		why = &reason
	}
	_, err := s.Pool.Exec(ctx, `UPDATE alert_deliveries SET status = $2, reason = $3,
		sent_at = CASE WHEN $2 = 'sent' THEN now() END WHERE id = $1`, d.id, status, why)
	return err
}

func (s *Service) email(ctx context.Context, job jobs.Job, d delivery, r Row) error {
	if d.userID == nil {
		return s.finish(ctx, d, "skipped", "not_a_member")
	}
	var email string
	var muted bool
	var sentToday int
	err := s.Pool.QueryRow(ctx, `
		SELECT u.email,
			EXISTS (SELECT 1 FROM alert_mutes x WHERE x.workspace_id = $2 AND x.user_id = u.id AND x.kind = $3),
			(SELECT count(*) FROM alert_deliveries x WHERE x.user_id = u.id AND x.status = 'sent' AND x.sent_at > now() - interval '1 day')
		FROM users u JOIN memberships m ON m.user_id = u.id AND m.workspace_id = $2
		WHERE u.id = $1 AND u.email_verified_at IS NOT NULL AND u.disabled_at IS NULL`, *d.userID, d.ws, r.Kind).
		Scan(&email, &muted, &sentToday)
	ev := track.Event{Name: "alert.emailed", WorkspaceID: d.ws, UserID: *d.userID, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": r.Kind, "alert_id": r.ID}}
	skip := func(reason string) error {
		ev.Name = "alert.email_skipped"
		ev.Props["reason"] = reason
		track.Emit(ctx, ev)
		return s.finish(ctx, d, "skipped", reason)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return skip("not_a_member") // left the workspace, or the address is no longer confirmed
	case err != nil:
		return err
	case muted:
		return skip("muted")
	case sentToday >= DailyCap:
		return skip("daily_cap")
	}
	v := Describe(ctx, s.Pool, r)
	base := s.PublicURL + "/" + d.slug + "/"
	if err := s.Send(ctx, Message(email, d.workspace, base+v.Path, base+"alerts", v)); err != nil {
		track.Fail(ctx, s.Log, ev, "email_failed", err)
		if job.Attempt >= job.MaxAttempts {
			_ = s.finish(ctx, d, "failed", "email_failed")
		}
		return err
	}
	if err := s.finish(ctx, d, "sent", ""); err != nil {
		return err
	}
	track.Emit(ctx, ev)
	return nil
}

// post sends the alert to the workspace's own Slack (or another chat tool
// with the notify capability), through its own connection.
func (s *Service) post(ctx context.Context, job jobs.Job, d delivery, r Row) error {
	ev := track.Event{Name: "alert.posted", WorkspaceID: d.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": r.Kind, "alert_id": r.ID, "channel": "slack"}}
	fail := func(code string, err error) error {
		track.Fail(ctx, s.Log, ev, code, err)
		_ = s.finish(ctx, d, "failed", code)
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	if d.integrationID == nil || s.Store == nil {
		return fail("connector_needed", errors.New("the chat connection was removed"))
	}
	o, err := s.Store.Open(ctx, s.Pool, d.ws, *d.integrationID)
	if err != nil {
		if errors.Is(err, connectors.ErrNotFound) {
			return fail("connector_needed", err)
		}
		if connectors.KindOf(err) == connectors.ErrAuth {
			return fail("connector_auth", err)
		}
		return err
	}
	n, ok := o.Connector.(connectors.Notifier)
	if !ok {
		return fail("connector_needed", errors.New("the connection can't post alerts"))
	}
	v := Describe(ctx, s.Pool, r)
	notice := connectors.Notice{Title: v.Title, Text: v.Text, URL: s.PublicURL + "/" + d.slug + "/" + v.Path}
	err = n.Post(ctx, o.Client, o.Creds, d.target, notice)
	_ = s.Store.Checked(ctx, s.Pool, d.ws, o.ID, nil, err)
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		// Wrong credentials or a channel it can't post to won't fix
		// themselves; anything else is tried again.
		if code == "connector_auth" || code == "connector_invalid" {
			return fail(code, err)
		}
		track.Fail(ctx, s.Log, ev, code, err)
		if job.Attempt >= job.MaxAttempts {
			_ = s.finish(ctx, d, "failed", code)
		}
		return err
	}
	if err := s.finish(ctx, d, "sent", ""); err != nil {
		return err
	}
	track.Emit(ctx, ev)
	return nil
}

// Message is the alert email: what happened, one button to it, and how to
// change which alerts come by email.
func Message(email, workspace, url, settingsURL string, v View) sysmail.Message {
	body := []string{v.Title + "."}
	if v.Text != "" {
		body = append(body, v.Text)
	}
	return sysmail.Email{
		To:      email,
		Subject: v.Title,
		Preview: workspace + ": " + v.Title,
		Heading: v.Title,
		Body:    body,
		Button:  "Open in Outflare",
		URL:     url,
		Note:    "You get this because of the alert settings in " + workspace + ". Change which alerts come by email, or mute this kind for yourself, under Alerts: " + settingsURL,
	}.Message()
}
