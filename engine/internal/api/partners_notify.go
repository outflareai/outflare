package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Partner emails: a partner hears about a credited conversion and about
// payouts, from the program portal's sender through the workspace's own
// email provider, when they want to (their portal settings) and the
// portal has a sender. Each is a job queued with the change that caused
// it, so an email that can't be sent never holds up a conversion.

const jobPartnerNotify = "partners.notify"

type partnerNotify struct {
	// Kind is conversion (rewards a conversion earned), payout_due or
	// payout_paid.
	Kind      string `json:"kind"`
	PartnerID string `json:"partner_id"`
	Ref       string `json:"ref"` // the conversion or payout
}

func enqueuePartnerNotify(ctx context.Context, tx pgx.Tx, ws string, n partnerNotify) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerNotify, WorkspaceID: &ws, Payload: n, MaxAttempts: 3,
		IdempotencyKey: "partner-notify:" + n.Kind + ":" + n.Ref})
	return err
}

func (s *Server) partnerNotifyJob(ctx context.Context, job jobs.Job) error {
	var n partnerNotify
	if err := json.Unmarshal(job.Payload, &n); err != nil || job.WorkspaceID == nil || !validUUID(n.PartnerID) || !validUUID(n.Ref) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	v, err := s.loadPartner(ctx, ws, n.PartnerID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT notify FROM partner_memberships WHERE id = $1`, v.ID).Scan(&raw); err != nil {
		return err
	}
	want := map[string]bool{"conversions": true, "payouts": true}
	_ = json.Unmarshal(raw, &want)
	pref := "payouts"
	if n.Kind == "conversion" {
		pref = "conversions"
	}
	if !want[pref] || v.Email == "" || v.Status != "active" {
		return nil
	}
	p, err := s.portalProgramByID(ctx, v.ProgramID)
	if err != nil {
		return err
	}
	if !p.enabled || p.settings.EmailFrom == "" {
		return nil
	}
	subject, body, ok, err := s.partnerNotice(ctx, p, v, n)
	if err != nil || !ok {
		return err
	}
	htmlBody := "<p>" + html.EscapeString(body) + `</p><p><a href="{{portal_url}}">Open your partner portal</a></p>`
	text := body + "\n\n{{portal_url}}"
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return s.portalEmail(ctx, tx, p, v.Email, "partner_"+n.Kind, emails.Draft{Subject: &subject, HTML: &htmlBody, Text: &text,
			Data: map[string]any{"partner_name": v.Name}})
	})
	if errors.Is(err, errPortalEmail) {
		// The program's sender or provider isn't set up: nothing to retry.
		track.Fail(ctx, s.log, track.Event{Name: "partner.notify_failed", WorkspaceID: ws, Surface: "job",
			Props: map[string]any{"kind": n.Kind, "program_id": p.id}}, "email_unavailable", err)
		return nil
	}
	return err
}

// partnerNotice is what an email says; ok is false when there is nothing
// to tell (a conversion that earned the partner nothing).
func (s *Server) partnerNotice(ctx context.Context, p *portalProgram, v partnerView, n partnerNotify) (subject, body string, ok bool, err error) {
	switch n.Kind {
	case "conversion":
		var cents int64
		var actions int
		var hold time.Time
		err = s.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents) FILTER (WHERE kind = 'cash'), 0)::bigint, count(*) FILTER (WHERE kind = 'action')::int,
			COALESCE(max(hold_until), now()) FROM partner_rewards WHERE conversion_id = $1 AND membership_id = $2 AND side = 'partner'`, n.Ref, v.ID).Scan(&cents, &actions, &hold)
		if err != nil || cents == 0 && actions == 0 {
			return "", "", false, err
		}
		what := "a reward"
		if cents > 0 {
			what = formatMoney(cents, p.currency)
		}
		body = "A customer you referred to " + p.title() + " just converted, earning you " + what + "."
		if hold.After(time.Now().Add(time.Hour)) {
			body += " It is held until " + hold.Format("2 January 2006") + " in case of a refund, then approved."
		}
		return "You earned " + what + " from " + p.title(), body, true, nil
	case "payout_due", "payout_paid":
		var cents int64
		var currency, ref string
		err = s.pool.QueryRow(ctx, `SELECT amount_cents, currency, reference FROM partner_payouts WHERE id = $1 AND membership_id = $2`, n.Ref, v.ID).
			Scan(&cents, &currency, &ref)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		if err != nil {
			return "", "", false, err
		}
		amount := formatMoney(cents, currency)
		if n.Kind == "payout_due" {
			return "A payout of " + amount + " is on its way", "Your payout of " + amount + " from " + p.title() + " is being prepared.", true, nil
		}
		body = p.title() + " paid you " + amount + "."
		if ref != "" {
			body += " Reference: " + ref + "."
		}
		return "You were paid " + amount, body, true, nil
	}
	return "", "", false, nil
}

// formatMoney writes cents as an amount with its currency code, such as
// 49.00 USD.
func formatMoney(cents int64, currency string) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, cents/100, cents%100, currency)
}
