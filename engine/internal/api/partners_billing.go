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
//     beside what the workspace's backend reports. A brand's program whose
//     sales_source is "transactions" takes the brand's payments and
//     refunds as they are counted (docs/transactions.md): transactions
//     sent to the API and Order Completed events, once each. The payment sync queues
//     a job per payment; the job credits it like a reported conversion, so
//     a referral already bound, or the customer's own visits, decide the
//     partner.
//   - A promo code can also be made in the workspace's billing account,
//     for a coupon it already has, by a job with the workspace's own key.

const (
	jobPartnerBillingConversion = "partners.billing_conversion"
	jobPartnerPromoCode         = "partners.promo_code"
)

// salesFromTransactions is the sales_source of a program that takes its
// brand's counted payments, from the API or order events, as sales.
const salesFromTransactions = "transactions"

type partnerBillingConversion struct {
	Provider string `json:"provider"`
	// Brand limits a counted payment to its brand's programs.
	Brand       string    `json:"brand,omitempty"`
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
	if c.Brand != "" {
		return "partner-billing:" + c.Provider + ":" + c.Brand + ":" + c.Type + ":" + c.ExternalID
	}
	return "partner-billing:" + c.Provider + ":" + c.Type + ":" + c.ExternalID
}

// partnerSalesFromPayment queues a counted payment or refund as a partner
// sale for the brand's programs that take their sales from transactions.
// The payment.succeeded event is already recorded, so the conversion adds
// none (source billing): it counts once.
func partnerSalesFromPayment(ctx context.Context, tx pgx.Tx, ws string, t transactionView) error {
	if t.BrandID == "" || t.RecordID == nil || t.SourceID == "" {
		return nil
	}
	var on bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_programs WHERE workspace_id = $1 AND brand_id = $2 AND sales_source = $3 AND status = 'active')`,
		ws, t.BrandID, salesFromTransactions).Scan(&on); err != nil || !on {
		return err
	}
	c := partnerBillingConversion{Provider: salesFromTransactions, Brand: t.BrandID, Type: "sale", RecordID: *t.RecordID, ExternalID: t.SourceID,
		AmountMinor: t.AmountMinor, Currency: t.Currency, At: t.OccurredAt}
	if t.Kind == "refund" || t.Kind == "chargeback" {
		c.Type = "refund"
		if t.RefundOf != nil {
			c.RefundOf = *t.RefundOf
		}
	}
	return enqueueBillingConversion(ctx, tx, ws, c)
}

// referredPaymentLead is how long before a referral was made a payment of
// the customer's can be and still be credited to it: the payment can reach
// Outflare before the sign-up that links the referral.
const referredPaymentLead = time.Hour

// requeueReferredPayments queues the customer's payments again once a
// referral binds them, when the program takes its sales from transactions:
// a payment counted just before (it arrived before the sign-up) found no
// partner then. Each payment is still credited once (its external id).
func requeueReferredPayments(ctx context.Context, tx pgx.Tx, ws, program, rec, referral string, at time.Time) error {
	rows, err := tx.Query(ctx, `SELECT t.brand_id::text, t.source_id, t.amount_minor, t.currency, t.occurred_at FROM transactions t
		JOIN partner_programs p ON p.id = $2 AND p.brand_id = t.brand_id AND p.sales_source = $5 AND p.status = 'active'
		WHERE t.workspace_id = $1 AND t.record_id = $3 AND t.kind = 'payment' AND t.status = 'succeeded' AND t.source IN ('api', 'events')
			AND t.occurred_at >= $4::timestamptz - $6::interval
		ORDER BY t.occurred_at LIMIT 100`, ws, program, rec, at, salesFromTransactions, referredPaymentLead.String())
	if err != nil {
		return err
	}
	type payment struct {
		brand, id, currency string
		amount              int64
		at                  time.Time
	}
	paid, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (payment, error) {
		var p payment
		err := r.Scan(&p.brand, &p.id, &p.amount, &p.currency, &p.at)
		return p, err
	})
	if err != nil {
		return err
	}
	for _, p := range paid {
		c := partnerBillingConversion{Provider: salesFromTransactions, Brand: p.brand, Type: "sale", RecordID: rec, ExternalID: p.id,
			AmountMinor: p.amount, Currency: p.currency, At: p.at}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerBillingConversion, WorkspaceID: &ws, Payload: c, MaxAttempts: 5,
			IdempotencyKey: billingConversionKey(c) + ":referral:" + referral}); err != nil {
			return err
		}
	}
	return nil
}

// partnerSaleDeleted takes back a partner sale whose transaction was
// deleted: it is queued as a refund of the whole payment, as a refund sent
// to the API would be.
func partnerSaleDeleted(ctx context.Context, tx pgx.Tx, ws string, t transactionView) error {
	if t.Kind != "payment" || t.Status != "succeeded" || (t.Source != "api" && t.Source != orderSource) {
		return nil
	}
	refund := t
	refund.Kind, refund.ID = "refund", ""
	refund.RefundOf, refund.SourceID, refund.OccurredAt = &t.SourceID, "deleted:"+t.SourceID, time.Now().UTC()
	return partnerSalesFromPayment(ctx, tx, ws, refund)
}

// partnerBillingConversionJob credits one synced payment or refund in
// every program that takes sales from its provider.
func (s *Server) partnerBillingConversionJob(ctx context.Context, job jobs.Job) error {
	var c partnerBillingConversion
	if err := json.Unmarshal(job.Payload, &c); err != nil || job.WorkspaceID == nil || !validUUID(c.RecordID) || c.ExternalID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM partner_programs WHERE workspace_id = $1 AND sales_source = $2 AND status = 'active'
		AND ($3 = '' OR brand_id = NULLIF($3, '')::uuid)`, ws, c.Provider, c.Brand)
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
			jobPartnerBillingConversion, billingConversionKey(partnerBillingConversion{Provider: c.Provider, Brand: c.Brand, Type: "sale", ExternalID: c.RefundOf})).Scan(&waiting)
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
	Force bool   `json:"force,omitempty"`
	ID    string `json:"id"`
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
	var external *string
	var raw []byte
	var rules connectors.PromoCodeRules
	var integration *string
	err := s.pool.QueryRow(ctx, `SELECT code, coupon, integration_id::text, membership_id::text,external_id,rules FROM partner_promo_codes
		WHERE workspace_id = $1 AND id = $2 AND (sync_status = 'pending' OR $3)`, ws, p.ID, p.Force).Scan(&code, &coupon, &integration, &membership, &external, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(code string, err error) error {
		track.Fail(ctx, s.log, track.Event{Name: "partner.promo_code_failed", WorkspaceID: ws, Surface: "job",
			Props: map[string]any{"partner_id": membership}}, code, err)
		_, uerr := s.pool.Exec(ctx, `UPDATE partner_promo_codes SET sync_status = 'failed', sync_error = $2 WHERE id = $1 AND workspace_id=$3 AND rules=$4::jsonb`, p.ID, cutUTF8(err.Error(), 500), ws, raw)
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

	_ = json.Unmarshal(raw, &rules)
	if rules.StartsAt != nil && rules.StartsAt.After(time.Now()) {
		return nil
	} // creation is queued at its start time
	var id string
	if _, ok := o.Connector.(connectors.ManagedPromoCodeWriter); !ok && string(raw) != "{}" {
		return fail("connector_needed", errors.New("the billing connector does not support these coupon restrictions"))
	}
	if rich, ok := o.Connector.(connectors.ManagedPromoCodeWriter); ok && string(raw) != "{}" {
		if external != nil && *external != "" {
			id = *external
			active := !rules.Disabled && (rules.EndsAt == nil || rules.EndsAt.After(time.Now()))
			err = rich.SetPromoCodeActive(ctx, s.integrations.Client, o.Creds, id, active)
		} else {
			id, err = rich.CreateManagedPromoCode(ctx, s.integrations.Client, o.Creds, code, coupon, rules, map[string]string{"outflare_partner": membership, "outflare_promo_code": p.ID})
			if err == nil {
				err = rich.SetPromoCodeActive(ctx, s.integrations.Client, o.Creds, id, !rules.Disabled && (rules.EndsAt == nil || rules.EndsAt.After(time.Now())))
			}
		}
	} else {
		id, err = writer.CreatePromoCode(ctx, s.integrations.Client, o.Creds, code, coupon, map[string]string{"outflare_partner": membership, "outflare_promo_code": p.ID})
	}

	if err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable || connectors.KindOf(err) == connectors.ErrQuota {
			return err // tried again
		}
		return fail("connector_"+connectors.KindOf(err), err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {

		var current bool
		if err := tx.QueryRow(ctx, `UPDATE partner_promo_codes SET sync_status=CASE WHEN rules=$3::jsonb THEN 'created' ELSE 'pending' END, external_id=$2,sync_error='' WHERE workspace_id=$4 AND id=$1 RETURNING rules=$3::jsonb`, p.ID, id, raw, ws).Scan(&current); err != nil {
			return err
		}
		if !current {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerPromoCode, WorkspaceID: &ws, Payload: partnerPromoJob{ID: p.ID, Force: true}, MaxAttempts: 5}); err != nil {
				return err
			}
		}

		return partnerRecord(ctx, tx, nil, ws, "partner.promo_code_created", "partner", membership,
			map[string]any{"promo_code_id": p.ID, "connector": o.Connector.Definition().ID})
	})
}
