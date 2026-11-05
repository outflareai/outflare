package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Partners read the events the workspace already sends (docs/partners.md,
// "From your events"). A brand's programs need no partner API of their own:
//
//   - A sign-up is the product's first identify (or any server event) that
//     gives a person their user id, or a Signed Up event. It credits the
//     partner whose link the person came from, or the one its properties
//     name (ref, promo_code), once per program.
//   - An event named by one of a program's reward rules (trial_started, say)
//     is that program's custom conversion.
//   - Sales and refunds come from transactions (partners_billing.go).
//
// The partner conversions API stays as a shortcut: what it reports is
// stored as the same events and transactions, with the same ids, so a
// customer sent both ways counts once.

const jobPartnerEventConversion = "partners.event_conversion"

// signedUpEvent is a Signed Up event as stored.
const signedUpEvent = "signed_up"

// partnerSignupAge is how long after their account was made a person's
// first event still counts as their sign-up: an existing user whose events
// start arriving now didn't just sign up.
const partnerSignupAge = 24 * time.Hour

type partnerEventConversion struct {
	Brand     string    `json:"brand"`
	Type      string    `json:"type"` // signup or custom
	EventName string    `json:"event_name,omitempty"`
	RecordID  string    `json:"record_id"`
	EventID   string    `json:"event_id"`
	Ref       string    `json:"ref,omitempty"`
	PromoCode string    `json:"promo_code,omitempty"`
	At        time.Time `json:"at"`
}

// partnerFromEvent queues a stored event as a partner conversion when one
// of the brand's active programs takes it. Browser events never get here.
func partnerFromEvent(ctx context.Context, tx pgx.Tx, ws, brand, rec, kind, name string, newUser bool, props, traits map[string]any,
	eventID string, at time.Time) error {
	if brand == "" {
		return nil
	}
	c := partnerEventConversion{Brand: brand, RecordID: rec, EventID: eventID, At: at}
	switch {
	case kind == "track" && name == signedUpEvent, newUser && kind != "alias":
		c.Type = "signup"
	case kind == "track" && !slices.Contains(reservedConversionEvents, name) && conversionEventRE.MatchString(name):
		c.Type, c.EventName = "custom", name
	default:
		return nil
	}
	var on bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_programs p WHERE p.workspace_id = $1 AND p.brand_id = $2 AND p.status = 'active'
		AND ($3 = '' OR EXISTS (SELECT 1 FROM partner_reward_rules r WHERE r.program_id = p.id AND r.trigger = $3)))`,
		ws, brand, c.EventName).Scan(&on); err != nil || !on {
		return err
	}
	if c.Type == "signup" && kind == "identify" && name != signedUpEvent {
		// An account made well before this event is an existing user whose
		// events just started arriving, not a new sign-up.
		if made, ok := traitTime(traits); ok && made.Before(at.Add(-partnerSignupAge)) {
			return nil
		}
	}
	for key, dst := range map[string]*string{"ref": &c.Ref, "promo_code": &c.PromoCode} {
		if v, _ := props[key].(string); partnerCodeRE.MatchString(v) {
			*dst = v
		}
	}
	// One job per event: a later Signed Up that names the partner still
	// counts after an identify that found none. The conversion itself is
	// stored once (one sign-up per customer and program).
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerEventConversion, WorkspaceID: &ws, Payload: c, MaxAttempts: 5,
		IdempotencyKey: "partner-event:" + eventID})
	return err
}

// traitTime is when the account was made, from an identify call's
// created_at (or createdAt) trait.
func traitTime(m map[string]any) (time.Time, bool) {
	for _, k := range []string{"created_at", "createdAt"} {
		if s, _ := m[k].(string); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// partnerEventConversionJob credits one event in every active program of
// its brand that takes it.
func (s *Server) partnerEventConversionJob(ctx context.Context, job jobs.Job) error {
	var c partnerEventConversion
	if err := json.Unmarshal(job.Payload, &c); err != nil || job.WorkspaceID == nil || !validUUID(c.RecordID) || !validUUID(c.Brand) ||
		(c.Type != "signup" && c.Type != "custom") {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	rows, err := s.pool.Query(ctx, `SELECT p.id::text FROM partner_programs p WHERE p.workspace_id = $1 AND p.brand_id = $2 AND p.status = 'active'
		AND ($3 = '' OR EXISTS (SELECT 1 FROM partner_reward_rules r WHERE r.program_id = p.id AND r.trigger = $3)) ORDER BY p.created_at`,
		ws, c.Brand, c.EventName)
	if err != nil {
		return err
	}
	programs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	at := c.At.UTC().Format(time.RFC3339)
	for _, p := range programs {
		in := conversionInput{Type: c.Type, EventName: c.EventName, Program: p, RecordID: c.RecordID, Ref: c.Ref, PromoCode: c.PromoCode, OccurredAt: &at}
		if c.Type == "custom" {
			in.ExternalID = "event:" + c.EventID
		}
		if err := in.check(time.Now()); err != nil {
			s.log.WarnContext(ctx, "partners: event not taken", "workspace_id", ws, "program_id", p, "err", err)
			continue
		}
		_, err := s.recordPartnerConversion(ctx, ws, nil, in, "events")
		if errors.Is(err, errConflict) {
			_, err = s.recordPartnerConversion(ctx, ws, nil, in, "events")
		}
		if err != nil && !errors.As(err, new(errInvalid)) {
			return err
		}
	}
	return nil
}
