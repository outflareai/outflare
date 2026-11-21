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
	// Store opens the workspace's own chat connections (Slack, a Telegram
	// bot).
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
	if d.channel == "slack" || d.channel == "telegram" {
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
			EXISTS (SELECT 1 FROM brand_alert_mutes x WHERE x.workspace_id = $2 AND x.brand_id = $4 AND x.user_id = u.id AND x.kind = $3),
			(SELECT count(*) FROM alert_deliveries x WHERE x.user_id = u.id AND x.status = 'sent' AND x.sent_at > now() - interval '1 day')
		FROM users u JOIN memberships m ON m.user_id = u.id AND m.workspace_id = $2
		WHERE u.id = $1 AND u.email_verified_at IS NOT NULL AND u.disabled_at IS NULL`, *d.userID, d.ws, r.Kind, r.BrandID).
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
	// The same kind for the same person in the same brand, still waiting:
	// one email lists them all. The others are marked grouped, and put
	// back if the email fails, so the retry takes them again.
	others, ok, err := s.group(ctx, d, r)
	if err != nil || !ok {
		return err
	}
	views := []View{Describe(ctx, s.Pool, r)}
	for _, id := range others {
		o, err := Load(ctx, s.Pool, d.ws, id)
		if err != nil {
			s.ungroup(ctx, d.ws, others)
			return err
		}
		views = append(views, Describe(ctx, s.Pool, o))
	}
	ev.Props["grouped"] = len(others)
	settings := URL(s.PublicURL, d.slug, "alerts")
	var m sysmail.Message
	if len(views) == 1 {
		m = Message(email, d.workspace, URL(s.PublicURL, d.slug, views[0].Path), settings, views[0])
	} else {
		k, _ := Find(r.Kind)
		m = GroupMessage(email, d.workspace, k.Title, URL(s.PublicURL, d.slug, groupPath(views)), settings, views)
	}
	if err := s.Send(ctx, m); err != nil {
		s.ungroup(ctx, d.ws, others)
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

// groupLimit is the most alerts one email lists.
const groupLimit = 50

// group takes the other alerts of r's kind waiting to be emailed to the
// same person in the same brand, raised in the last hour, and marks them
// grouped into this email. One person's burst is taken by one job at a
// time (an advisory lock), and ok is false when another job already took
// this delivery into its email.
func (s *Service) group(ctx context.Context, d delivery, r Row) (others []string, ok bool, err error) {
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('alert_email:' || $1 || ':' || $2 || ':' || $3, 0))`,
			*d.userID, r.Kind, deref(r.BrandID)); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM alert_deliveries WHERE id = $1`, d.id).Scan(&status); err != nil {
			return err
		}
		if status != "queued" {
			return nil
		}
		ok = true
		rows, err := tx.Query(ctx, `UPDATE alert_deliveries x SET status = 'skipped', reason = 'grouped'
			FROM (SELECT o.id FROM alert_deliveries o JOIN alerts a ON a.id = o.alert_id AND a.workspace_id = o.workspace_id
				WHERE o.workspace_id = $1 AND o.user_id = $2 AND o.channel = 'email' AND o.status = 'queued' AND o.id <> $3
				  AND a.kind = $4 AND a.brand_id IS NOT DISTINCT FROM $5::uuid AND a.created_at > now() - interval '1 hour'
				ORDER BY a.created_at, o.id LIMIT $6) g
			WHERE x.id = g.id RETURNING x.alert_id::text`, d.ws, *d.userID, d.id, r.Kind, r.BrandID, groupLimit-1)
		if err != nil {
			return err
		}
		others, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return others, ok, err
}

// ungroup puts grouped deliveries back in the queue after the email that
// listed them failed; the retry takes them again.
func (s *Service) ungroup(ctx context.Context, ws string, alertIDs []string) {
	if len(alertIDs) == 0 {
		return
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE alert_deliveries SET status = 'queued', reason = NULL
		WHERE workspace_id = $1 AND alert_id::text = ANY($2) AND channel = 'email' AND status = 'skipped' AND reason = 'grouped'`, ws, alertIDs); err != nil {
		s.Log.Error("alert emails could not be put back after a failed send", "err", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// groupPath is where a grouped email's button opens: the page they share,
// else the alerts list.
func groupPath(views []View) string {
	for _, v := range views[1:] {
		if v.Path != views[0].Path {
			return "alerts"
		}
	}
	return views[0].Path
}

// post sends the alert to the workspace's own Slack or Telegram bot (a
// chat tool with the notify capability), through its own connection.
func (s *Service) post(ctx context.Context, job jobs.Job, d delivery, r Row) error {
	ev := track.Event{Name: "alert.posted", WorkspaceID: d.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": r.Kind, "alert_id": r.ID, "channel": d.channel}}
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
	notice := connectors.Notice{Title: v.Title, Text: v.Text, URL: URL(s.PublicURL, d.slug, v.Path)}
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
		To:        email,
		Subject:   v.Title,
		Preview:   workspace + ": " + v.Title,
		Heading:   v.Title,
		Body:      body,
		Button:    "Open in Outflare",
		URL:       url,
		Note:      "You get this because of the alert settings in " + workspace + ". Switch this kind of email off, or mute it for yourself only, at any time.",
		LinkLabel: "Manage notifications",
		LinkURL:   settingsURL,
	}.Message()
}

// GroupMessage is one email for several alerts of one kind: how many, and
// each one's title.
func GroupMessage(email, workspace, kindTitle, url, settingsURL string, views []View) sysmail.Message {
	heading := fmt.Sprintf("%s: %d alerts", kindTitle, len(views))
	body := make([]string, 0, len(views)+1)
	for _, v := range views {
		body = append(body, v.Title+".")
	}
	if len(views) > 0 && views[0].Text != "" {
		body = append(body, views[0].Text)
	}
	return sysmail.Email{
		To:        email,
		Subject:   heading,
		Preview:   workspace + ": " + heading,
		Heading:   heading,
		Body:      body,
		Button:    "Open in Outflare",
		URL:       url,
		Note:      "These came together, so they are in one email. You get this because of the alert settings in " + workspace + ". Switch this kind of email off, or mute it for yourself only, at any time.",
		LinkLabel: "Manage notifications",
		LinkURL:   settingsURL,
	}.Message()
}
