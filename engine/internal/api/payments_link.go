package api

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/fxrates"
)

// One payment counts once, whichever way it arrives (docs/transactions.md,
// "One payment, counted once"): a billing connector's charge, a transaction
// sent to the API, or an Order Completed event with revenue. Each becomes
// one transaction and one payment.succeeded event, which is what revenue,
// attribution and the conversions sent to ad networks count.

const (
	// paymentWindow is how far apart the same person's payments of the
	// same amount and currency, from two different sources, can be and
	// still be taken as one payment when they share no id.
	paymentWindow = 15 * time.Minute
	// resentAfter is how far apart the same payment id can arrive again
	// from the same source and still be a retry, not another payment
	// sent under an id that was already used.
	resentAfter = 24 * time.Hour
	// paymentLink is the property an order event carries once it is
	// counted as a payment: that payment.succeeded event's message id.
	paymentLink = "outflare_payment"
	// orderSource is the transactions source of payments read from order
	// events.
	orderSource = "events"
)

// orderEvents are the event names (as stored) that are a completed order,
// and refundEvents those that give one back.
var (
	orderEvents  = map[string]bool{"order_completed": true}
	refundEvents = map[string]bool{"order_refunded": true}
)

// refundLink is the property an Order Refunded event carries once it is
// saved as a refund: that refund's transaction id.
const refundLink = "outflare_refund"

// paymentMessageID is the message id of the payment.succeeded (or, for a
// refund, payment.refunded) event of a transaction: a billing
// connector's own, as its sync records it, or one per brand and source id.
func paymentMessageID(brand, source, kind, sourceID string) string {
	refund := kind == "refund" || kind == "chargeback"
	if source != "api" && source != orderSource {
		if refund {
			return source + ":refund:" + sourceID
		}
		return source + ":charge:" + sourceID
	}
	if refund {
		return "refund:" + brand + ":" + sourceID
	}
	return "payment:" + brand + ":" + sourceID
}

// lockTransaction makes the saves of one payment or refund id take turns until tx ends.
func lockTransaction(ctx context.Context, tx pgx.Tx, brand, kind, sourceID string) error {
	if brand == "" || sourceID == "" {
		return nil
	}
	if kind == "chargeback" {
		kind = "refund"
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('transaction:' || $1::uuid::text || ':' || $2 || ':' || $3, 0))`,
		brand, kind, sourceID)
	return err
}

// knownPayment is a brand's payment transaction that another source's
// payment turned out to be.
type knownPayment struct {
	id, source, sourceID, currency string
	recordID                       *string
	amountMinor                    int64
	occurredAt                     time.Time
}

// samePayment finds the brand's payment transaction that is the payment
// described: the one with the same source id from any source, or else, from
// another source, the same person's of the same amount and currency within
// paymentWindow. ok is false when there is none.
func samePayment(ctx context.Context, tx pgx.Tx, brand, source, sourceID, recordID string, amountMinor int64, currency string, at time.Time) (knownPayment, bool, error) {
	var k knownPayment
	err := tx.QueryRow(ctx, `
		SELECT id::text, source, source_id, record_id::text, amount_minor, currency, occurred_at FROM transactions
		WHERE brand_id = $1 AND kind = 'payment' AND (
			($2 <> '' AND source_id = $2 AND source <> $3)
			OR ($4 <> '' AND record_id = NULLIF($4, '')::uuid AND source <> $3 AND amount_minor = $5 AND currency = $6
				AND occurred_at BETWEEN $7::timestamptz - $8::interval AND $7::timestamptz + $8::interval))
		ORDER BY source_id = $2 DESC, abs(extract(epoch FROM occurred_at - $7::timestamptz)), id LIMIT 1`,
		brand, sourceID, source, recordID, amountMinor, currency, at, paymentWindow.String()).Scan(&k.id, &k.source, &k.sourceID, &k.recordID, &k.amountMinor, &k.currency, &k.occurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, false, nil
	}
	return k, err == nil, err
}

// paymentMerged records that a payment sent one way (source, sourceID)
// was kept as one already in (known) as transaction.merged, so Developer
// setup can show the ones joined by a guess instead of a shared id, and
// the ones whose two copies disagree on the amount (docs/developer-setup.md).
// The same id a day or more after the payment it names is recorded as
// transaction.resent instead (paymentResent).
func paymentMerged(ctx context.Context, tx pgx.Tx, p *principal, ws, brand, source, sourceID string, amountMinor int64, currency string, at time.Time, known knownPayment) error {
	matched := "id"
	if sourceID == "" || sourceID != known.sourceID {
		matched = "guess"
	} else if at.Sub(known.occurredAt).Abs() >= resentAfter {
		return record(ctx, tx, p, ws, "transaction.resent", "transaction", known.id, map[string]any{"brand_id": brand, "kind": "payment",
			"source": source, "source_id": sourceID, "first_at": known.occurredAt, "sent_at": at})
	}
	return record(ctx, tx, p, ws, "transaction.merged", "transaction", known.id, map[string]any{"brand_id": brand, "kind": "payment",
		"matched_by": matched, "source": source, "source_id": sourceID, "amount_minor": amountMinor, "currency": currency,
		"kept_source": known.source, "kept_source_id": known.sourceID, "kept_amount_minor": known.amountMinor, "kept_currency": known.currency,
		"amounts_differ": amountMinor != known.amountMinor || currency != known.currency})
}

// paymentResent records a payment id sent again from the same source at
// least resentAfter after the first, as transaction.resent: usually a
// subscription's id sent for each renewal instead of each charge's own,
// which keeps every renewal after the first out (docs/developer-setup.md).
func paymentResent(ctx context.Context, tx pgx.Tx, p *principal, ws string, v transactionView, at time.Time) error {
	if v.Kind != "payment" || at.Sub(v.OccurredAt).Abs() < resentAfter {
		return nil
	}
	return record(ctx, tx, p, ws, "transaction.resent", "transaction", v.ID, map[string]any{"brand_id": v.BrandID, "kind": v.Kind,
		"source": v.Source, "source_id": v.SourceID, "first_at": v.OccurredAt, "sent_at": at})
}

// countPayment records the payment.succeeded or payment.refunded event of a
// transaction saved from the API or an order event, once: a later call
// with the same transaction stores nothing. A transaction that hasn't
// succeeded, has no person or company, or is a credit counts nothing.
func countPayment(ctx context.Context, tx pgx.Tx, ws string, t transactionView) (string, error) {
	if t.RecordID == nil || t.Status != "succeeded" || t.Kind == "credit" {
		return "", nil
	}
	refund := t.Kind == "refund" || t.Kind == "chargeback"
	if refund {
		// A refund counts once its payment is known, so it always comes
		// off a payment (and that conversion); until then it waits in
		// Developer setup's refunds to match.
		var known bool
		if t.RefundOf != nil {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transactions WHERE brand_id = $1 AND kind = 'payment' AND source_id = $2)`,
				t.BrandID, *t.RefundOf).Scan(&known); err != nil {
				return "", err
			}
		}
		if !known {
			return "", nil
		}
	}
	props := map[string]any{"amount_minor": t.AmountMinor, "currency": t.Currency, "transaction_id": t.ID, "source": t.Source}
	name := evPaymentSucceeded
	if t.Kind == "refund" || t.Kind == "chargeback" {
		name = evPaymentRefunded
		props["refund_id"] = t.SourceID
		if t.RefundOf != nil {
			props["payment_id"] = *t.RefundOf
		}
	} else {
		props["payment_id"] = t.SourceID
	}
	mid := paymentMessageID(t.BrandID, t.Source, t.Kind, t.SourceID)
	ev := customers.Event{RecordID: *t.RecordID, Name: name, Source: "transactions", OccurredAt: t.OccurredAt,
		MessageID: mid, RequestID: RequestID(ctx), Properties: props}
	if t.ProductID != nil {
		ev.ProductID = *t.ProductID
	}
	added, err := customers.Record(ctx, tx, ws, ev)
	if err != nil {
		return "", err
	}
	if added {
		if err := partnerSalesFromPayment(ctx, tx, ws, t); err != nil {
			return "", err
		}
	}
	if !refund {
		// Refunds that arrived before this payment count now.
		if err := countWaitingRefunds(ctx, tx, ws, t.BrandID, t.SourceID); err != nil {
			return "", err
		}
	}
	return mid, nil
}

// countWaitingRefunds counts the brand's refunds of the payment with
// sourceID that were waiting for it. Counting is idempotent, so one
// already counted stores nothing.
func countWaitingRefunds(ctx context.Context, tx pgx.Tx, ws, brand, sourceID string) error {
	rows, err := tx.Query(ctx, `SELECT `+transactionColumns+` FROM transactions t
		WHERE t.brand_id = $1 AND t.kind IN ('refund', 'chargeback') AND t.refund_of = $2 AND t.source IN ('api', 'events')
		  AND t.status = 'succeeded' AND t.record_id IS NOT NULL`, brand, sourceID)
	if err != nil {
		return err
	}
	var waiting []transactionView
	for rows.Next() {
		v, err := scanTransaction(rows)
		if err != nil {
			rows.Close()
			return err
		}
		waiting = append(waiting, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v := range waiting {
		if _, err := countPayment(ctx, tx, ws, v); err != nil {
			return err
		}
	}
	return nil
}

// sameRefund finds the brand's refund that is the refund described, sent
// another way: the same refund id from another source, or else, from
// another source, a refund of the same payment of the same amount and
// currency within paymentWindow. It returns the transaction id, "" when none.
func sameRefund(ctx context.Context, tx pgx.Tx, brand, source, sourceID, refundOf string, amountMinor int64, currency string, at time.Time) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM transactions
		WHERE brand_id = $1 AND kind IN ('refund', 'chargeback') AND source <> $3 AND (
			($2 <> '' AND source_id = $2)
			OR ($4 <> '' AND refund_of = $4 AND amount_minor = $5 AND currency = $6
				AND occurred_at BETWEEN $7::timestamptz - $8::interval AND $7::timestamptz + $8::interval))
		ORDER BY source_id = $2 DESC, abs(extract(epoch FROM occurred_at - $7::timestamptz)), id LIMIT 1`,
		brand, sourceID, source, refundOf, amountMinor, currency, at, paymentWindow.String()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// paidRef is the payment a refund gives back.
type paidRef struct {
	id, sourceID, currency string
	amountMinor            int64
}

// findPayment finds the brand's payment a refund names, by any id its
// sender has for it: the payment's source_id (an order's order_id), Outflare's
// transaction id, the order event's messageId (with or without an "order-"
// style prefix, or the website's "browser:"), so a developer never has to
// know which one Outflare keeps. ok is false when none matches.
func findPayment(ctx context.Context, tx pgx.Tx, ws, brand, ref string) (paidRef, bool, error) {
	var p paidRef
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 200 {
		return p, false, nil
	}
	err := tx.QueryRow(ctx, `
		SELECT t.id::text, t.source_id, t.currency, t.amount_minor FROM transactions t
		WHERE t.brand_id = $2 AND t.kind = 'payment' AND (
			t.source_id = $3 OR t.id::text = $3
			OR t.source_id = regexp_replace($3, '^(order|payment|purchase|ord)[-_:]', '', 'i')
			OR EXISTS (SELECT 1 FROM customer_events e WHERE e.workspace_id = $1 AND e.message_id IN ($3, 'browser:' || $3)
				AND e.brand_id = t.brand_id
				AND e.properties->>'outflare_payment' IN ('payment:' || t.brand_id::text || ':' || t.source_id, t.source || ':charge:' || t.source_id)))
		ORDER BY t.source_id = $3 DESC, t.id::text = $3 DESC, t.created_at LIMIT 1`, ws, brand, ref).
		Scan(&p.id, &p.sourceID, &p.currency, &p.amountMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// orderAmount is an order event's money in minor units of its currency:
// Segment's revenue, then total, then value, in major units. ok is false
// when it has none above zero.
func orderAmount(props map[string]any, fallback string) (int64, string, bool) {
	return moneyIn(props, fallback, "revenue", "total", "value")
}

// moneyIn is the first of keys in props above zero, in major units, as
// minor units of the event's currency (else fallback).
func moneyIn(props map[string]any, fallback string, keys ...string) (int64, string, bool) {
	cur := fallback
	if c, ok := props["currency"].(string); ok && currencyCode.MatchString(strings.ToUpper(strings.TrimSpace(c))) {
		cur = strings.ToUpper(strings.TrimSpace(c))
	}
	for _, k := range keys {
		var f float64
		switch v := props[k].(type) {
		case float64:
			f = v
		case string:
			var err error
			if f, err = strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
				continue
			}
		default:
			continue
		}
		if f > 0 && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return int64(math.Round(f * float64(pow10(fxrates.MinorDigits(cur))))), cur, true
		}
	}
	return 0, "", false
}

// orderKey is an order event's own id for the payment: its order_id, then
// transaction_id, then payment_id, else its message id.
func orderKey(props map[string]any, messageID string) string {
	for _, k := range []string{"order_id", "orderId", "transaction_id", "payment_id"} {
		switch v := props[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" && len(s) <= 200 {
				return s
			}
		case float64:
			if v == math.Trunc(v) {
				return strconv.FormatInt(int64(v), 10)
			}
		}
	}
	return messageID
}

// orderPayment counts a completed order (a server event with revenue, from
// a known person) as a payment of brand, unless it is one already: the
// same order id, or the same person, amount and currency within
// paymentWindow, from the API or a billing connector. It returns the
// message id of the payment.succeeded event the order is counted as, or ""
// when the order holds no payment.
func orderPayment(ctx context.Context, tx pgx.Tx, ws, brand, rec string, props map[string]any, messageID string, at time.Time) (string, error) {
	var brandCurrency string
	if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&brandCurrency); err != nil {
		return "", err
	}
	amount, cur, ok := orderAmount(props, brandCurrency)
	if !ok {
		return "", nil
	}
	key := orderKey(props, messageID)
	if err := lockTransaction(ctx, tx, brand, "payment", key); err != nil {
		return "", err
	}
	known, found, err := samePayment(ctx, tx, brand, orderSource, key, rec, amount, cur, at)
	if err != nil {
		return "", err
	}
	if found {
		if err := paymentMerged(ctx, tx, nil, ws, brand, orderSource, key, amount, cur, at, known); err != nil {
			return "", err
		}
		if known.recordID != nil {
			return paymentMessageID(brand, known.source, "payment", known.sourceID), nil
		}
		// One sent to the API without its person learns it from the order,
		// and counts from then on.
		if _, err := tx.Exec(ctx, `UPDATE transactions SET record_id = $2, updated_at = now() WHERE id = $1`, known.id, rec); err != nil {
			return "", err
		}
		t, err := scanTransaction(tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM transactions t WHERE t.id = $1`, known.id))
		if err != nil {
			return "", err
		}
		return countPayment(ctx, tx, ws, t)
	}
	if key == "" {
		if err := tx.QueryRow(ctx, `SELECT 'event:' || gen_random_uuid()::text`).Scan(&key); err != nil {
			return "", err
		}
	}
	t, created, err := saveTransaction(ctx, tx, nil, ws, brand, newTransaction{RecordID: rec, Kind: "payment", Status: "succeeded", AmountMinor: amount,
		Currency: cur, Source: orderSource, SourceID: key, Description: "Order Completed", OccurredAt: at})
	if errors.Is(err, errTransactionDeleted) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !created {
		if err := paymentResent(ctx, tx, nil, ws, t, at); err != nil {
			return "", err
		}
	}
	return countPayment(ctx, tx, ws, t)
}

// orderRefund saves an Order Refunded event (a server event from a known
// person) as a refund of brand: its payment is found by any id the event
// gives (order_id, then transaction_id or payment_id, see findPayment), its
// amount is amount (or revenue, total, value) and is the whole payment when
// none is given, and its own id is refund_id, else its messageId. It counts
// as payment.refunded once its payment is known. It returns the refund's
// transaction id.
func orderRefund(ctx context.Context, tx pgx.Tx, ws, brand, rec string, props map[string]any, messageID string, at time.Time) (string, error) {
	var brandCurrency string
	if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&brandCurrency); err != nil {
		return "", err
	}
	ref := orderKey(props, "")
	paid, found, err := findPayment(ctx, tx, ws, brand, ref)
	if err != nil {
		return "", err
	}
	fallback := brandCurrency
	if found {
		fallback = paid.currency
	}
	amount, cur, ok := moneyIn(props, fallback, "amount", "revenue", "total", "value")
	if !ok {
		amount, cur = 0, fallback
		if found {
			amount = paid.amountMinor
		}
	}
	refundOf := ref
	if found {
		refundOf = paid.sourceID
	}
	key := ""
	if v, ok := props["refund_id"].(string); ok && len(strings.TrimSpace(v)) <= 200 {
		key = strings.TrimSpace(v)
	}
	if key == "" {
		key = messageID
	}
	if err := lockTransaction(ctx, tx, brand, "refund", key); err != nil {
		return "", err
	}
	if key == "" {
		if err := tx.QueryRow(ctx, `SELECT 'event:' || gen_random_uuid()::text`).Scan(&key); err != nil {
			return "", err
		}
	}
	// The same refund already sent as a transaction is that one.
	if known, err := sameRefund(ctx, tx, brand, orderSource, key, refundOf, amount, cur, at); err != nil || known != "" {
		return known, err
	}
	t, _, err := saveTransaction(ctx, tx, nil, ws, brand, newTransaction{RecordID: rec, Kind: "refund", Status: "succeeded", AmountMinor: amount,
		Currency: cur, Source: orderSource, SourceID: key, RefundOf: refundOf, Description: "Order Refunded", OccurredAt: at})
	if errors.Is(err, errTransactionDeleted) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := countPayment(ctx, tx, ws, t); err != nil {
		return "", err
	}
	return t.ID, nil
}
