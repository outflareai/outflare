package linkorders

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage"
)

// BudgetTag marks a coverage_budgets row as the cap for this contour.
const BudgetTag = "link_orders"

// CreateOrderInput is a new request against a catalog row.
type CreateOrderInput struct {
	DonorProfileID  uuid.UUID
	TargetURL       string
	SlotType        string
	RelRequested    string
	GuaranteeDays   *int
	Notes           *string
	OwnerUserID     *uuid.UUID
	ReplacesOrderID *uuid.UUID
}

// CreateOrder inserts a requested order. The guarantee window defaults to
// the catalog row, then to DefaultGuaranteeDays. When ReplacesOrderID is
// set the lost-order rules apply.
func CreateOrder(ctx context.Context, tx pgx.Tx, workspaceID, brandID, actorID uuid.UUID, in CreateOrderInput) (*Order, error) {
	if in.TargetURL == "" {
		return nil, fmt.Errorf("%w: target_url is required", ErrInvalid)
	}
	donor, err := GetDonor(ctx, tx, workspaceID, brandID, in.DonorProfileID)
	if err != nil {
		if err == ErrNotFound {
			return nil, fmt.Errorf("%w: donor_profile_id is not in this brand's catalog", ErrInvalid)
		}
		return nil, err
	}
	if donor.ArchivedAt != nil {
		return nil, fmt.Errorf("%w: that donor is archived", ErrInvalid)
	}
	guarantee := in.GuaranteeDays
	if guarantee == nil {
		guarantee = donor.GuaranteeDays
	}
	if guarantee == nil {
		d := DefaultGuaranteeDays
		guarantee = &d
	}
	if in.ReplacesOrderID != nil {
		if err := assertCanReplace(ctx, tx, workspaceID, brandID, *in.ReplacesOrderID); err != nil {
			return nil, err
		}
	}
	notes := ""
	if in.Notes != nil {
		notes = *in.Notes
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO coverage_link_orders (
			workspace_id, brand_id, publisher_id, donor_profile_id, status,
			target_url, slot_type, rel_requested, guarantee_days,
			replaces_order_id, notes, owner_user_id, created_by
		)
		SELECT $1, $2, p.publisher_id, p.id, $3,
			$4, $5, $6, $7, $8, $9, $10, $11
		FROM coverage_donor_profiles p
		WHERE p.id = $12 AND p.workspace_id = $1 AND p.brand_id = $2
		RETURNING id
	`, workspaceID, brandID, StatusRequested,
		in.TargetURL, in.SlotType, in.RelRequested, guarantee,
		in.ReplacesOrderID, notes, in.OwnerUserID, nilIfNil(actorID),
		in.DonorProfileID,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	if err := AppendEvent(ctx, tx, workspaceID, brandID, id, actorID, "created", nil, StatusRequested, map[string]any{}); err != nil {
		return nil, err
	}
	return GetOrder(ctx, tx, workspaceID, brandID, id)
}

// GetOrder returns one order, or ErrNotFound.
func GetOrder(ctx context.Context, q Querier, workspaceID, brandID, id uuid.UUID) (*Order, error) {
	list, err := listOrdersQuery(ctx, q, workspaceID, brandID, OrderListFilter{ID: &id}, 1, nil)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

type OrderListFilter struct {
	ID             *uuid.UUID
	Status         string
	DonorProfileID *uuid.UUID
	Q              string
}

// ListOrders returns orders, newest first, limited to limit+1 so the
// caller can tell if there is another page.
func ListOrders(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, f OrderListFilter, limit int, before *Cursor) ([]Order, error) {
	return listOrdersQuery(ctx, q, workspaceID, brandID, f, limit, before)
}

func listOrdersQuery(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, f OrderListFilter, limit int, before *Cursor) ([]Order, error) {
	where := []string{"o.workspace_id = $1", "o.brand_id = $2"}
	args := []any{workspaceID, brandID}
	if f.ID != nil {
		args = append(args, *f.ID)
		where = append(where, fmt.Sprintf("o.id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, fmt.Sprintf("o.status = $%d", len(args)))
	}
	if f.DonorProfileID != nil {
		args = append(args, *f.DonorProfileID)
		where = append(where, fmt.Sprintf("o.donor_profile_id = $%d", len(args)))
	}
	if f.Q != "" {
		like := "%" + f.Q + "%"
		args = append(args, like, like, like)
		n := len(args)
		where = append(where, fmt.Sprintf("(pub.domain ILIKE $%d OR o.target_url ILIKE $%d OR COALESCE(o.published_url, '') ILIKE $%d)", n-2, n-1, n))
	}
	if before != nil {
		args = append(args, before.At, before.ID)
		where = append(where, fmt.Sprintf("(o.created_at, o.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit)
	sql := `
		SELECT o.id, o.publisher_id, o.donor_profile_id, pub.domain, o.status, o.target_url,
			o.slot_type, o.rel_requested, o.quoted_amount_minor, o.quoted_currency, o.donor_amount_minor,
			o.currency, o.guarantee_days, o.guarantee_until, o.published_url, o.proof_url,
			o.published_at, o.placement_id, o.agreement_id, o.invoice_id, o.payment_id,
			o.credit_payment_id, o.replaces_order_id, o.owner_user_id, o.last_check_at, o.last_check_live,
			o.last_check_target_match, o.last_check_rel, o.last_check_anchor,
			o.approved_by, o.approved_at, o.paid_by, o.paid_at,
			o.lost_at, o.lost_reason, o.notes, o.created_at, o.updated_at
		FROM coverage_link_orders o
		JOIN coverage_donor_profiles p ON p.id = o.donor_profile_id
		JOIN coverage_publishers pub ON pub.id = p.publisher_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(
			&o.ID, &o.PublisherID, &o.DonorProfileID, &o.Domain, &o.Status, &o.TargetURL,
			&o.SlotType, &o.RelRequested, &o.QuotedAmountMinor, &o.QuotedCurrency, &o.DonorAmountMinor,
			&o.Currency, &o.GuaranteeDays, &o.GuaranteeUntil, &o.PublishedURL, &o.ProofURL,
			&o.PublishedAt, &o.PlacementID, &o.AgreementID, &o.InvoiceID, &o.PaymentID,
			&o.CreditPaymentID, &o.ReplacesOrderID, &o.OwnerUserID, &o.LastCheckAt, &o.LastCheckLive,
			&o.LastCheckTargetMatch, &o.LastCheckRel, &o.LastCheckAnchor,
			&o.ApprovedBy, &o.ApprovedAt, &o.PaidBy, &o.PaidAt,
			&o.LostAt, &o.LostReason, &o.Notes, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PatchOrderInput is the editable fields while the amount is not frozen.
type PatchOrderInput struct {
	TargetURL      **string
	SlotType       **string
	RelRequested   **string
	GuaranteeDays  **int
	Notes          **string
	OwnerUserID    **uuid.UUID
	DonorProfileID *uuid.UUID
}

// PatchOrder updates fields that are still mutable. After approval the
// donor amount cannot change; the donor and target may still be corrected
// only before publish.
func PatchOrder(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id uuid.UUID, in PatchOrderInput) (*Order, error) {
	o, err := GetOrder(ctx, tx, workspaceID, brandID, id)
	if err != nil {
		return nil, err
	}
	switch o.Status {
	case StatusPaid, StatusVerified, StatusLost:
		return nil, fmt.Errorf("%w: this order can no longer be edited", ErrConflict)
	}
	sets := []string{"updated_at = now()"}
	args := []any{id}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.DonorProfileID != nil {
		if o.Status == StatusPublished {
			return nil, fmt.Errorf("%w: the donor cannot change after publication", ErrConflict)
		}
		d, err := GetDonor(ctx, tx, workspaceID, brandID, *in.DonorProfileID)
		if err != nil {
			if err == ErrNotFound {
				return nil, fmt.Errorf("%w: donor_profile_id is not in this brand's catalog", ErrInvalid)
			}
			return nil, err
		}
		if d.ArchivedAt != nil {
			return nil, fmt.Errorf("%w: that donor is archived", ErrInvalid)
		}
		add("donor_profile_id", *in.DonorProfileID)
		add("publisher_id", d.PublisherID)
	}
	if in.TargetURL != nil {
		if *in.TargetURL == nil || **in.TargetURL == "" {
			return nil, fmt.Errorf("%w: target_url is required", ErrInvalid)
		}
		add("target_url", **in.TargetURL)
	}
	if in.SlotType != nil {
		v := ""
		if *in.SlotType != nil {
			v = **in.SlotType
		}
		add("slot_type", v)
	}
	if in.RelRequested != nil {
		v := ""
		if *in.RelRequested != nil {
			v = **in.RelRequested
		}
		add("rel_requested", v)
	}
	if in.GuaranteeDays != nil {
		add("guarantee_days", *in.GuaranteeDays)
	}
	if in.Notes != nil {
		v := ""
		if *in.Notes != nil {
			v = **in.Notes
		}
		add("notes", v)
	}
	if in.OwnerUserID != nil {
		add("owner_user_id", *in.OwnerUserID)
	}
	if len(sets) == 1 {
		return o, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE coverage_link_orders SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
		return nil, err
	}
	return GetOrder(ctx, tx, workspaceID, brandID, id)
}

// Quote sets or updates the quoted donor price and moves the order to
// quoted. Allowed from requested, quoted, or negotiating.
func Quote(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID, amountMinor int64, currency string) (*Order, error) {
	if amountMinor < 0 {
		return nil, fmt.Errorf("%w: amount_minor must be >= 0", ErrInvalid)
	}
	if err := validCurrency(currency); err != nil {
		return nil, err
	}
	return transition(ctx, tx, workspaceID, brandID, id, actorID, "quoted",
		[]string{StatusRequested, StatusQuoted, StatusNegotiating}, StatusQuoted,
		func(o *Order, sets *[]string, args *[]any) error {
			if o.Status == StatusApproved || o.Status == StatusPublished || o.Status == StatusPaid || o.Status == StatusVerified {
				return ErrAmountFrozen
			}
			addSet(sets, args, "quoted_amount_minor", amountMinor)
			addSet(sets, args, "quoted_currency", currency)
			return nil
		}, map[string]any{"amount_minor": amountMinor, "currency": currency})
}

// Negotiate moves a quoted order to negotiating.
func Negotiate(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID, note *string) (*Order, error) {
	return transition(ctx, tx, workspaceID, brandID, id, actorID, "negotiating",
		[]string{StatusQuoted}, StatusNegotiating,
		func(o *Order, sets *[]string, args *[]any) error {
			if note != nil {
				addSet(sets, args, "notes", *note)
			}
			return nil
		}, map[string]any{})
}

// Approve freezes the quoted donor amount and moves the order to approved.
// The caller must already have confirmed the person is the brand approver.
// A budget for this currency, when present, is a hard cap.
func Approve(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID) (*Order, error) {
	return transition(ctx, tx, workspaceID, brandID, id, actorID, "approved",
		[]string{StatusQuoted, StatusNegotiating}, StatusApproved,
		func(o *Order, sets *[]string, args *[]any) error {
			if o.QuotedAmountMinor == nil || o.QuotedCurrency == nil {
				return fmt.Errorf("%w: quote the order before approving it", ErrConflict)
			}
			ok, err := CanApproveAmount(ctx, tx, workspaceID, brandID, *o.QuotedCurrency, *o.QuotedAmountMinor)
			if err != nil {
				return err
			}
			if !ok {
				return ErrOverBudget
			}
			addSet(sets, args, "donor_amount_minor", *o.QuotedAmountMinor)
			addSet(sets, args, "currency", *o.QuotedCurrency)
			addSet(sets, args, "approved_by", actorID)
			addSet(sets, args, "approved_at", time.Now().UTC())
			return nil
		}, map[string]any{})
}

// Publish records the live URL, creates or reuses a paid coverage
// placement, and moves the order to published. The same URL on the same
// inventory does not create a second placement.
func Publish(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID, publishedURL, proofURL string) (*Order, error) {
	if publishedURL == "" {
		return nil, fmt.Errorf("%w: published_url is required", ErrInvalid)
	}
	return transition(ctx, tx, workspaceID, brandID, id, actorID, "published",
		[]string{StatusApproved}, StatusPublished,
		func(o *Order, sets *[]string, args *[]any) error {
			donor, err := GetDonor(ctx, tx, workspaceID, brandID, o.DonorProfileID)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			placementID, _, err := coverage.Record(ctx, tx, workspaceID.String(), brandID.String(), coverage.Placement{
				URL:          publishedURL,
				Kind:         "blog",
				Arrangement:  "paid",
				Title:        donor.Domain,
				TargetURL:    o.TargetURL,
				ExpectedLink: true,
				Source:       "link_orders",
				SourceID:     o.ID.String(),
			})
			if err != nil {
				return err
			}
			pid, err := uuid.Parse(placementID)
			if err != nil {
				return err
			}
			addSet(sets, args, "published_url", publishedURL)
			if proofURL != "" {
				addSet(sets, args, "proof_url", proofURL)
			}
			addSet(sets, args, "published_at", now)
			addSet(sets, args, "placement_id", pid)
			if o.GuaranteeDays != nil && *o.GuaranteeDays > 0 {
				until := now.AddDate(0, 0, *o.GuaranteeDays)
				addSet(sets, args, "guarantee_until", until)
			}
			return nil
		}, map[string]any{})
}

// Pay records a coverage invoice and payment for the frozen donor amount
// and moves the order to paid. The link must already be published and the
// last live-link check must have passed. Recording a payment never sends
// money. The caller must already have confirmed the person is the brand
// approver and holds coverage.finance.manage.
func Pay(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID) (*Order, error) {
	o, err := GetOrder(ctx, tx, workspaceID, brandID, id)
	if err != nil {
		return nil, err
	}
	if o.Status != StatusPublished && o.Status != StatusVerified {
		if o.Status == StatusRequested || o.Status == StatusQuoted || o.Status == StatusNegotiating || o.Status == StatusApproved {
			return nil, ErrNotPublished
		}
		return nil, fmt.Errorf("%w: cannot pay from status %s", ErrConflict, o.Status)
	}
	if o.LastCheckLive == nil || !*o.LastCheckLive || o.LastCheckTargetMatch == nil || !*o.LastCheckTargetMatch {
		return nil, ErrNotLive
	}
	if o.DonorAmountMinor == nil || o.Currency == nil {
		return nil, fmt.Errorf("%w: the donor amount is not frozen", ErrConflict)
	}
	if o.PaymentID != nil {
		return nil, fmt.Errorf("%w: this order already has a payment", ErrConflict)
	}

	agreementID := o.AgreementID
	if agreementID == nil {
		aid, err := ensureAgreement(ctx, tx, workspaceID, brandID, o)
		if err != nil {
			return nil, err
		}
		agreementID = &aid
	}
	invoiceID, err := insertInvoice(ctx, tx, workspaceID, brandID, *agreementID, o)
	if err != nil {
		return nil, err
	}
	paymentID, err := insertPayment(ctx, tx, workspaceID, brandID, invoiceID, *o.Currency, *o.DonorAmountMinor, "payment", "LO-"+shortID(o.ID))
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE coverage_link_orders
		SET status = $2, agreement_id = $3, invoice_id = $4, payment_id = $5,
		    paid_by = $6, paid_at = $7, updated_at = now()
		WHERE id = $1
	`, id, StatusPaid, *agreementID, invoiceID, paymentID, actorID, now); err != nil {
		return nil, err
	}
	if err := AppendEvent(ctx, tx, workspaceID, brandID, id, actorID, "paid", &o.Status, StatusPaid, map[string]any{
		"amount_minor": *o.DonorAmountMinor, "currency": *o.Currency,
	}); err != nil {
		return nil, err
	}
	return GetOrder(ctx, tx, workspaceID, brandID, id)
}

// Verify marks a published or paid order verified once the last live-link
// check has passed.
func Verify(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID) (*Order, error) {
	return transition(ctx, tx, workspaceID, brandID, id, actorID, "verified",
		[]string{StatusPublished, StatusPaid}, StatusVerified,
		func(o *Order, sets *[]string, args *[]any) error {
			if o.LastCheckLive == nil || !*o.LastCheckLive || o.LastCheckTargetMatch == nil || !*o.LastCheckTargetMatch {
				return fmt.Errorf("%w: verify needs a passing live-link check", ErrConflict)
			}
			return nil
		}, map[string]any{})
}

// MarkLost moves a published, paid, or verified order to lost.
func MarkLost(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID, note *string) (*Order, error) {
	return transition(ctx, tx, workspaceID, brandID, id, actorID, "lost",
		[]string{StatusPublished, StatusPaid, StatusVerified}, StatusLost,
		func(o *Order, sets *[]string, args *[]any) error {
			addSet(sets, args, "lost_at", time.Now().UTC())
			if note != nil {
				addSet(sets, args, "lost_reason", *note)
				addSet(sets, args, "notes", *note)
			}
			return nil
		}, map[string]any{})
}

// Replace opens a new requested order that takes the place of a lost one.
// One open (not lost) replacement is allowed at a time. Cycles are rejected.
func Replace(ctx context.Context, tx pgx.Tx, workspaceID, brandID, lostID, actorID uuid.UUID, donorProfileID uuid.UUID, note *string) (*Order, error) {
	lost, err := GetOrder(ctx, tx, workspaceID, brandID, lostID)
	if err != nil {
		return nil, err
	}
	if err := assertCanReplace(ctx, tx, workspaceID, brandID, lostID); err != nil {
		return nil, err
	}
	if donorProfileID == uuid.Nil {
		return nil, fmt.Errorf("%w: donor_profile_id is required", ErrInvalid)
	}
	in := CreateOrderInput{
		DonorProfileID:  donorProfileID,
		TargetURL:       lost.TargetURL,
		SlotType:        lost.SlotType,
		RelRequested:    lost.RelRequested,
		Notes:           note,
		OwnerUserID:     lost.OwnerUserID,
		ReplacesOrderID: &lostID,
	}
	n, err := CreateOrder(ctx, tx, workspaceID, brandID, actorID, in)
	if err != nil {
		return nil, err
	}
	if err := AppendEvent(ctx, tx, workspaceID, brandID, lostID, actorID, "replacement_opened", &lost.Status, lost.Status, map[string]any{
		"replacement_id": n.ID.String(),
	}); err != nil {
		return nil, err
	}
	return n, nil
}

// Credit records a credit invoice (and a reversal when the order was
// already paid) after a lost order cannot be replaced. The caller must
// already have confirmed the person is the brand approver and holds
// coverage.finance.manage.
func Credit(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID, reason *string) (*Order, error) {
	o, err := GetOrder(ctx, tx, workspaceID, brandID, id)
	if err != nil {
		return nil, err
	}
	if o.Status != StatusLost {
		return nil, fmt.Errorf("%w: credit is only allowed on a lost order", ErrConflict)
	}
	if o.CreditPaymentID != nil {
		return nil, fmt.Errorf("%w: this order already has a credit", ErrConflict)
	}
	if o.DonorAmountMinor == nil || o.Currency == nil {
		return nil, fmt.Errorf("%w: there is no frozen donor amount to credit", ErrConflict)
	}
	open, err := openReplacement(ctx, tx, workspaceID, brandID, id)
	if err != nil {
		return nil, err
	}
	if open != nil {
		return nil, fmt.Errorf("%w: credit is only allowed when a replacement is not possible; an open replacement already exists", ErrConflict)
	}

	agreementID := o.AgreementID
	if agreementID == nil {
		aid, err := ensureAgreement(ctx, tx, workspaceID, brandID, o)
		if err != nil {
			return nil, err
		}
		agreementID = &aid
	}
	creditInv, err := insertCreditInvoice(ctx, tx, workspaceID, brandID, *agreementID, o)
	if err != nil {
		return nil, err
	}
	var creditPay uuid.UUID
	if o.PaymentID != nil {
		creditPay, err = insertPayment(ctx, tx, workspaceID, brandID, creditInv, *o.Currency, *o.DonorAmountMinor, "reversal", "LO-"+shortID(o.ID)+"-CR")
		if err != nil {
			return nil, err
		}
	} else {
		// No cash movement yet: still store a credit invoice and a
		// zero-movement marker by writing a reversal against the credit
		// invoice for the frozen amount so finance can see it.
		creditPay, err = insertPayment(ctx, tx, workspaceID, brandID, creditInv, *o.Currency, *o.DonorAmountMinor, "reversal", "LO-"+shortID(o.ID)+"-CR")
		if err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE coverage_link_orders
		SET agreement_id = $2, credit_payment_id = $3, updated_at = now()
		WHERE id = $1
	`, id, *agreementID, creditPay); err != nil {
		return nil, err
	}
	data := map[string]any{"amount_minor": *o.DonorAmountMinor, "currency": *o.Currency}
	if reason != nil {
		data["reason_set"] = true
	}
	if err := AppendEvent(ctx, tx, workspaceID, brandID, id, actorID, "credited", &o.Status, o.Status, data); err != nil {
		return nil, err
	}
	return GetOrder(ctx, tx, workspaceID, brandID, id)
}

func assertCanReplace(ctx context.Context, q Querier, workspaceID, brandID, lostID uuid.UUID) error {
	lost, err := GetOrder(ctx, q, workspaceID, brandID, lostID)
	if err != nil {
		return err
	}
	if lost.Status != StatusLost {
		return ErrReplaceNotLost
	}
	open, err := openReplacement(ctx, q, workspaceID, brandID, lostID)
	if err != nil {
		return err
	}
	if open != nil {
		return ErrOpenReplacement
	}
	// Walk the replaces chain starting at this order; a cycle would mean
	// this id already appears in its own ancestry (should not happen
	// because new ids are used, but it guards a patched self-link).
	seen := map[uuid.UUID]bool{lostID: true}
	cur := lost.ReplacesOrderID
	for cur != nil {
		if seen[*cur] {
			return ErrReplaceCycle
		}
		seen[*cur] = true
		var next *uuid.UUID
		err := q.QueryRow(ctx, `SELECT replaces_order_id FROM coverage_link_orders WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, *cur, workspaceID, brandID).Scan(&next)
		if err == pgx.ErrNoRows {
			break
		}
		if err != nil {
			return err
		}
		cur = next
	}
	return nil
}

func openReplacement(ctx context.Context, q Querier, workspaceID, brandID, lostID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT id FROM coverage_link_orders
		WHERE workspace_id = $1 AND brand_id = $2
		  AND replaces_order_id = $3 AND status <> $4
		LIMIT 1
	`, workspaceID, brandID, lostID, StatusLost).Scan(&id)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

type mutateFn func(o *Order, sets *[]string, args *[]any) error

func transition(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id, actorID uuid.UUID, action string, from []string, to string, mut mutateFn, data map[string]any) (*Order, error) {
	o, err := GetOrder(ctx, tx, workspaceID, brandID, id)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, s := range from {
		if o.Status == s {
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w: cannot move from %s to %s", ErrConflict, o.Status, to)
	}
	sets := []string{"status = $2", "updated_at = now()"}
	args := []any{id, to}
	if mut != nil {
		if err := mut(o, &sets, &args); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE coverage_link_orders SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
		return nil, err
	}
	if err := AppendEvent(ctx, tx, workspaceID, brandID, id, actorID, action, &o.Status, to, data); err != nil {
		return nil, err
	}
	return GetOrder(ctx, tx, workspaceID, brandID, id)
}

func addSet(sets *[]string, args *[]any, col string, v any) {
	*args = append(*args, v)
	*sets = append(*sets, fmt.Sprintf("%s = $%d", col, len(*args)))
}

// AppendEvent writes one history row. data must be JSON-serializable.
func AppendEvent(ctx context.Context, tx pgx.Tx, workspaceID, brandID, orderID, actorID uuid.UUID, action string, from *string, to string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	payload := map[string]any{"action": action}
	for k, v := range data {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO coverage_link_order_events (
			workspace_id, brand_id, order_id, actor_id, from_status, to_status, payload
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, workspaceID, brandID, orderID, nilIfNil(actorID), from, to, raw)
	return err
}

func nilIfNil(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// ListEvents returns an order's history, oldest first.
func ListEvents(ctx context.Context, q Querier, workspaceID, brandID, orderID uuid.UUID) ([]Event, error) {
	if _, err := GetOrder(ctx, q, workspaceID, brandID, orderID); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT id, order_id, from_status, to_status, actor_id, payload, created_at
		FROM coverage_link_order_events
		WHERE workspace_id = $1 AND order_id = $2
		ORDER BY created_at ASC, id ASC
	`, workspaceID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var raw []byte
		if err := rows.Scan(&e.ID, &e.OrderID, &e.FromStatus, &e.ToStatus, &e.ActorID, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &e.Payload)
		}
		if a, ok := e.Payload["action"].(string); ok {
			e.Action = a
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Summarize returns counts by status and donor-amount totals. Committed is
// approved plus published not yet paid; spent is paid and verified; lost
// is lost. Currencies are never summed together.
func Summarize(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID) (*Summary, error) {
	s := &Summary{
		Counts: map[string]int{
			StatusRequested: 0, StatusQuoted: 0, StatusNegotiating: 0, StatusApproved: 0,
			StatusPublished: 0, StatusPaid: 0, StatusVerified: 0, StatusLost: 0,
		},
		Committed: []CurrencyTotal{},
		Spent:     []CurrencyTotal{},
		Lost:      []CurrencyTotal{},
	}
	rows, err := q.Query(ctx, `
		SELECT status, COUNT(*) FROM coverage_link_orders
		WHERE workspace_id = $1 AND brand_id = $2
		GROUP BY status
	`, workspaceID, brandID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		s.Counts[st] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	load := func(dest *[]CurrencyTotal, statuses []string) error {
		rows, err := q.Query(ctx, `
			SELECT currency, COALESCE(SUM(donor_amount_minor), 0)
			FROM coverage_link_orders
			WHERE workspace_id = $1 AND brand_id = $2
			  AND status = ANY ($3)
			  AND currency IS NOT NULL
			GROUP BY currency
			ORDER BY currency
		`, workspaceID, brandID, statuses)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t CurrencyTotal
			if err := rows.Scan(&t.Currency, &t.AmountMinor); err != nil {
				return err
			}
			*dest = append(*dest, t)
		}
		return rows.Err()
	}
	if err := load(&s.Committed, []string{StatusApproved, StatusPublished}); err != nil {
		return nil, err
	}
	if err := load(&s.Spent, []string{StatusPaid, StatusVerified}); err != nil {
		return nil, err
	}
	if err := load(&s.Lost, []string{StatusLost}); err != nil {
		return nil, err
	}
	return s, nil
}

// CurrentBudgets returns this period's link-order budgets, with committed
// and spent already applied. Missing a budget means there is no cap.
func CurrentBudgets(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID) (*BudgetView, error) {
	rows, err := q.Query(ctx, `
		SELECT id, title, currency, amount_minor, starts_on::text, ends_on::text
		FROM coverage_budgets
		WHERE workspace_id = $1 AND brand_id = $2
		  AND position($3 in tags) > 0
		  AND starts_on <= CURRENT_DATE AND ends_on >= CURRENT_DATE
		ORDER BY starts_on DESC, currency
	`, workspaceID, brandID, BudgetTag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var raw []BudgetCurrency
	seen := map[string]bool{}
	for rows.Next() {
		var b BudgetCurrency
		if err := rows.Scan(&b.BudgetID, &b.Title, &b.Currency, &b.AmountMinor, &b.StartsOn, &b.EndsOn); err != nil {
			return nil, err
		}
		// One cap per currency: the latest-starting current-period row.
		if seen[b.Currency] {
			continue
		}
		seen[b.Currency] = true
		raw = append(raw, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	view := &BudgetView{Budgets: []BudgetCurrency{}}
	for _, b := range raw {
		comm, spent, err := usedInCurrency(ctx, q, workspaceID, brandID, b.Currency)
		if err != nil {
			return nil, err
		}
		b.Committed = comm
		b.Spent = spent
		b.Remaining = b.AmountMinor - comm - spent
		view.Budgets = append(view.Budgets, b)
	}
	return view, nil
}

// CanApproveAmount reports whether adding amountMinor in currency stays at
// or under the current budget. No budget for that currency means allowed.
func CanApproveAmount(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, currency string, amountMinor int64) (bool, error) {
	view, err := CurrentBudgets(ctx, q, workspaceID, brandID)
	if err != nil {
		return false, err
	}
	for _, b := range view.Budgets {
		if b.Currency == currency {
			return b.Remaining >= amountMinor, nil
		}
	}
	return true, nil
}

func usedInCurrency(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, currency string) (committed, spent int64, err error) {
	err = q.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(donor_amount_minor) FILTER (WHERE status IN ('approved', 'published')), 0),
			COALESCE(SUM(donor_amount_minor) FILTER (WHERE status IN ('paid', 'verified')), 0)
		FROM coverage_link_orders
		WHERE workspace_id = $1 AND brand_id = $2 AND currency = $3
	`, workspaceID, brandID, currency).Scan(&committed, &spent)
	return
}

func ensureAgreement(ctx context.Context, tx pgx.Tx, workspaceID, brandID uuid.UUID, o *Order) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO coverage_agreements (
			workspace_id, brand_id, publisher_id, status, title, currency
		)
		SELECT $1, $2, p.publisher_id, 'active', $3, $4
		FROM coverage_donor_profiles p
		WHERE p.id = $5
		RETURNING id
	`, workspaceID, brandID, "Link order "+shortID(o.ID), *o.Currency, o.DonorProfileID).Scan(&id)
	return id, err
}

func insertInvoice(ctx context.Context, tx pgx.Tx, workspaceID, brandID, agreementID uuid.UUID, o *Order) (uuid.UUID, error) {
	ref := "LO-" + shortID(o.ID)
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO coverage_invoices (
			workspace_id, brand_id, agreement_id, title, status, kind,
			currency, amount_minor, issued_on, due_on, reference
		) VALUES (
			$1, $2, $3, $4, 'received', 'invoice',
			$5, $6, CURRENT_DATE, CURRENT_DATE, $7
		)
		RETURNING id
	`, workspaceID, brandID, agreementID, "Link order "+shortID(o.ID), *o.Currency, *o.DonorAmountMinor, ref).Scan(&id)
	return id, err
}

func insertCreditInvoice(ctx context.Context, tx pgx.Tx, workspaceID, brandID, agreementID uuid.UUID, o *Order) (uuid.UUID, error) {
	ref := "LO-" + shortID(o.ID) + "-CR"
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO coverage_invoices (
			workspace_id, brand_id, agreement_id, title, status, kind,
			currency, amount_minor, issued_on, due_on, reference
		) VALUES (
			$1, $2, $3, $4, 'received', 'credit',
			$5, $6, CURRENT_DATE, CURRENT_DATE, $7
		)
		RETURNING id
	`, workspaceID, brandID, agreementID, "Link order credit "+shortID(o.ID), *o.Currency, *o.DonorAmountMinor, ref).Scan(&id)
	return id, err
}

func insertPayment(ctx context.Context, tx pgx.Tx, workspaceID, brandID, invoiceID uuid.UUID, currency string, amountMinor int64, kind, reference string) (uuid.UUID, error) {
	title := "Link order payment"
	if kind == "reversal" {
		title = "Link order credit"
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO coverage_payments (
			workspace_id, brand_id, invoice_id, title, kind, currency, amount_minor, paid_on, reference
		) VALUES ($1, $2, $3, $4, $5, $6, $7, CURRENT_DATE, $8)
		RETURNING id
	`, workspaceID, brandID, invoiceID, title, kind, currency, amountMinor, reference).Scan(&id)
	return id, err
}

func shortID(id uuid.UUID) string {
	s := id.String()
	if len(s) >= 8 {
		return s[:8]
	}
	return s
}
