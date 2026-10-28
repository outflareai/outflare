package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Partners and billing connections, both optional:
//
//   - A program whose sales_source is a payment connector (stripe) also
//     takes that account's synced payments and refunds as partner sales,
//     beside what the workspace's backend reports. The payment sync queues
//     a job per payment; the job credits it like a reported conversion, so
//     a referral already bound, or the customer's own visits, decide the
//     partner.
//   - A promo code can also be made in the workspace's billing account,
//     for a coupon it already has, by a job with the workspace's own key.

const (
	jobPartnerBillingConversion = "partners.billing_conversion"
	jobPartnerPromoCode         = "partners.promo_code"
)

type partnerBillingConversion struct {
	Provider    string    `json:"provider"`
	Type        string    `json:"type"` // sale or refund
	RecordID    string    `json:"record_id"`
	ExternalID  string    `json:"external_id"` // the payment's or refund's id
	RefundOf    string    `json:"refund_of"`   // a refund's payment
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	At          time.Time `json:"at"`
}

// billingSalesOn reports whether any program of the workspace takes sales
// from provider, so the payment sync only queues work that is wanted.
func billingSalesOn(ctx context.Context, tx pgx.Tx, ws, provider string) (bool, error) {
	var on bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_programs WHERE workspace_id = $1 AND sales_source = $2 AND status = 'active')`,
		ws, provider).Scan(&on)
	return on, err
}

func enqueueBillingConversion(ctx context.Context, tx pgx.Tx, ws string, c partnerBillingConversion) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerBillingConversion, WorkspaceID: &ws, Payload: c, MaxAttempts: 5,
		IdempotencyKey: billingConversionKey(c)})
	return err
}

func billingConversionKey(c partnerBillingConversion) string {
	return "partner-billing:" + c.Provider + ":" + c.Type + ":" + c.ExternalID
}

// partnerBillingConversionJob credits one synced payment or refund in
// every program that takes sales from its provider.
func (s *Server) partnerBillingConversionJob(ctx context.Context, job jobs.Job) error {
	var c partnerBillingConversion
	if err := json.Unmarshal(job.Payload, &c); err != nil || job.WorkspaceID == nil || !validUUID(c.RecordID) || c.ExternalID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM partner_programs WHERE workspace_id = $1 AND sales_source = $2 AND status = 'active'`, ws, c.Provider)
	if err != nil {
		return err
	}
	programs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if c.Type == "refund" && c.RefundOf != "" {
		// The sale's job, queued by the same sync, may not have run yet:
		// a refund before its sale would find nothing to take back.
		var waiting bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND idempotency_key = $2 AND status IN ('queued', 'running'))`,
			jobPartnerBillingConversion, billingConversionKey(partnerBillingConversion{Provider: c.Provider, Type: "sale", ExternalID: c.RefundOf})).Scan(&waiting)
		if err != nil {
			return err
		}
		if waiting {
			return errors.New("partners: the refunded payment isn't credited yet")
		}
	}
	at := c.At.UTC().Format(time.RFC3339)
	for _, p := range programs {
		amount := c.AmountMinor
		in := conversionInput{Type: c.Type, Program: p, RecordID: c.RecordID, ExternalID: c.ExternalID, RefundOf: c.RefundOf,
			AmountCents: &amount, Currency: c.Currency, OccurredAt: &at}
		if err := in.check(time.Now()); err != nil {
			// Too old or malformed for a conversion: nothing to retry.
			s.log.WarnContext(ctx, "partners: billing payment not taken", "workspace_id", ws, "program_id", p, "err", err)
			continue
		}
		if _, err := s.recordPartnerConversion(ctx, ws, nil, in, "billing"); errors.Is(err, errConflict) {
			_, err = s.recordPartnerConversion(ctx, ws, nil, in, "billing")
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// ---- Promo codes in the billing account ----

type partnerPromoJob struct {
	ID string `json:"id"`
}

// partnerPromoCodeJob makes a partner's promo code in the workspace's
// billing account and records how it went on the code.
func (s *Server) partnerPromoCodeJob(ctx context.Context, job jobs.Job) error {
	var p partnerPromoJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var code, coupon, membership string
	var integration *string
	err := s.pool.QueryRow(ctx, `SELECT code, coupon, integration_id::text, membership_id::text FROM partner_promo_codes
		WHERE workspace_id = $1 AND id = $2 AND sync_status = 'pending'`, ws, p.ID).Scan(&code, &coupon, &integration, &membership)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(code string, err error) error {
		track.Fail(ctx, s.log, track.Event{Name: "partner.promo_code_failed", WorkspaceID: ws, Surface: "job",
			Props: map[string]any{"partner_id": membership}}, code, err)
		_, uerr := s.pool.Exec(ctx, `UPDATE partner_promo_codes SET sync_status = 'failed', sync_error = $2 WHERE id = $1`, p.ID, cutUTF8(err.Error(), 500))
		return uerr
	}
	if integration == nil {
		return fail("connector_needed", errors.New("the billing connection was removed"))
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, *integration)
	if errors.Is(err, connectors.ErrNotFound) {
		return fail("connector_needed", errors.New("the billing connection was removed"))
	}
	if err != nil {
		return err
	}
	writer, ok := o.Connector.(connectors.PromoCodeWriter)
	if !ok || o.Status != connectors.StatusConnected {
		return fail("connector_needed", errors.New("the billing connection can't make promo codes, or isn't connected"))
	}
	id, err := writer.CreatePromoCode(ctx, s.integrations.Client, o.Creds, code, coupon, map[string]string{"outflare_partner": membership})
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable || connectors.KindOf(err) == connectors.ErrQuota {
			return err // tried again
		}
		return fail("connector_"+connectors.KindOf(err), err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE partner_promo_codes SET sync_status = 'created', external_id = $2, sync_error = '' WHERE id = $1`, p.ID, id); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, nil, ws, "partner.promo_code_created", "partner", membership,
			map[string]any{"promo_code_id": p.ID, "connector": o.Connector.Definition().ID})
	})
}
