package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/fxrates"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Transactions (plans/customer-data-platform, the owner's decision of
// 2026-11-02; docs/transactions.md): one row per payment, refund,
// chargeback or credit, per brand, from billing connectors and the API.
// The same source id twice is the same transaction, so a retry or a sync
// that reads it again never adds it twice. From now on only: nothing is
// rebuilt from past events.

func (s *Server) routesTransactions(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/transactions", s.onBrand(read, s.listTransactions))
	mux.HandleFunc("POST /v1/brands/{brand}/transactions", s.onBrand(manage, s.createTransaction))
	mux.HandleFunc("GET /v1/brands/{brand}/transactions/{transaction}", s.onBrand(read, s.getTransaction))
}

var (
	transactionKinds    = []string{"payment", "refund", "chargeback", "credit"}
	transactionStatuses = []string{"succeeded", "pending", "failed", "reversed"}
)

type transactionView struct {
	ID               string     `json:"id"`
	BrandID          string     `json:"brand_id"`
	RecordID         *string    `json:"record_id"`
	Kind             string     `json:"kind"`
	Status           string     `json:"status"`
	AmountMinor      int64      `json:"amount_minor"`
	Currency         string     `json:"currency"`
	BrandAmountMinor *int64     `json:"brand_amount_minor"`
	BrandCurrency    string     `json:"brand_currency"`
	ProductID        *string    `json:"product_id"`
	Source           string     `json:"source"`
	SourceID         string     `json:"source_id"`
	RefundOf         *string    `json:"refund_of"`
	Description      string     `json:"description"`
	OccurredAt       time.Time  `json:"occurred_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        *time.Time `json:"updated_at"`
}

const transactionColumns = `t.id::text, t.brand_id::text, t.record_id::text, t.kind, t.status, t.amount_minor, t.currency, t.brand_amount_minor,
	t.brand_currency, t.product_id::text, t.source, t.source_id, t.refund_of, t.description, t.occurred_at, t.created_at, t.updated_at`

func scanTransaction(row pgx.Row) (transactionView, error) {
	var v transactionView
	err := row.Scan(&v.ID, &v.BrandID, &v.RecordID, &v.Kind, &v.Status, &v.AmountMinor, &v.Currency, &v.BrandAmountMinor,
		&v.BrandCurrency, &v.ProductID, &v.Source, &v.SourceID, &v.RefundOf, &v.Description, &v.OccurredAt, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// newTransaction is a transaction to save: from the API or a billing sync.
type newTransaction struct {
	RecordID    string
	Kind        string
	Status      string
	AmountMinor int64
	Currency    string
	ProductID   string
	Source      string
	SourceID    string
	RefundOf    string
	Description string
	OccurredAt  time.Time
}

// brandAmount converts minor units of currency to the brand's currency at
// day's rate (through US dollars), or nil when a rate isn't known yet.
func brandAmount(ctx context.Context, q fxrates.Querier, currency string, minor int64, brandCurrency string, day time.Time) (*int64, error) {
	if currency == brandCurrency {
		return &minor, nil
	}
	d := day.UTC().Format("2006-01-02")
	cents, err := fxrates.ToUSD(ctx, q, currency, minor, d)
	if err != nil || cents == nil {
		return nil, err
	}
	if brandCurrency == "USD" {
		return cents, nil
	}
	var out *int64
	err = q.QueryRow(ctx, `
		SELECT round($2::numeric / 100 / usd_per_unit * $3)::bigint FROM exchange_rates
		WHERE currency = $1 AND day <= $4::date AND day > $4::date - $5::int ORDER BY day DESC LIMIT 1`,
		brandCurrency, *cents, pow10(fxrates.MinorDigits(brandCurrency)), d, int(fxrates.MaxAge.Hours()/24)).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return out, err
}

func pow10(n int) int64 {
	p := int64(1)
	for range n {
		p *= 10
	}
	return p
}

// errTransactionDeleted is a transaction sent again while the one with its
// source id waits in Recently deleted.
var errTransactionDeleted = errors.New("transaction deleted")

// saveTransaction adds a transaction to brand, or finds the one with the
// same source and source id (created false). A known one only changes its
// status, which is recorded as transaction.updated. Each new transaction
// is recorded as transaction.created, which is its webhook.
func saveTransaction(ctx context.Context, tx pgx.Tx, p *principal, ws, brand string, in newTransaction) (transactionView, bool, error) {
	var brandCurrency string
	if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&brandCurrency); err != nil {
		return transactionView{}, false, err
	}
	// One deleted into Recently deleted stays deleted until it is restored
	// or purged: sending it again doesn't add it back.
	var deleted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deleted_rows WHERE brand_id = $1 AND source_table = 'transactions'
		AND data->>'source' = $2 AND data->>'source_id' = $3)`, brand, in.Source, in.SourceID).Scan(&deleted); err != nil {
		return transactionView{}, false, err
	}
	if deleted {
		return transactionView{}, false, errTransactionDeleted
	}
	converted, err := brandAmount(ctx, tx, in.Currency, in.AmountMinor, brandCurrency, in.OccurredAt)
	if err != nil {
		return transactionView{}, false, err
	}
	var id, oldStatus string
	var created bool
	err = tx.QueryRow(ctx, `
		WITH old AS (SELECT id, status FROM transactions WHERE brand_id = $2 AND source = $9 AND source_id = $10)
		INSERT INTO transactions (workspace_id, brand_id, record_id, kind, status, amount_minor, currency, brand_amount_minor, brand_currency,
			product_id, source, source_id, refund_of, description, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $15, $11, $9, $10, $12, $13, $14)
		ON CONFLICT (brand_id, source, source_id) DO UPDATE SET status = EXCLUDED.status,
			updated_at = CASE WHEN transactions.status <> EXCLUDED.status THEN now() ELSE transactions.updated_at END
		RETURNING id::text, COALESCE((SELECT status FROM old), ''), NOT EXISTS (SELECT 1 FROM old)`,
		ws, brand, nullIfEmpty(in.RecordID), in.Kind, in.Status, in.AmountMinor, in.Currency, converted,
		in.Source, in.SourceID, nullIfEmpty(in.ProductID), nullIfEmpty(in.RefundOf), in.Description, in.OccurredAt, brandCurrency).
		Scan(&id, &oldStatus, &created)
	if err != nil {
		return transactionView{}, false, err
	}
	data := map[string]any{"kind": in.Kind, "status": in.Status, "amount_minor": in.AmountMinor, "currency": in.Currency,
		"brand_amount_minor": converted, "brand_currency": brandCurrency, "source": in.Source, "source_id": in.SourceID,
		"record_id": nullIfEmpty(in.RecordID), "product_id": nullIfEmpty(in.ProductID), "refund_of": nullIfEmpty(in.RefundOf), "brand_id": brand}
	switch {
	case created:
		if err := record(ctx, tx, p, ws, "transaction.created", "transaction", id, data); err != nil {
			return transactionView{}, false, err
		}
	case oldStatus != in.Status:
		data["from_status"] = oldStatus
		if err := record(ctx, tx, p, ws, "transaction.updated", "transaction", id, data); err != nil {
			return transactionView{}, false, err
		}
	}
	v, err := scanTransaction(tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM transactions t WHERE t.id = $1`, id))
	return v, created, err
}

// addTransaction saves a transaction from the API (or a partner
// conversion reported to it) and counts it as a payment once. The same
// payment already in from an order event or a billing connector is that
// one (merged): it isn't added or counted again. The caller holds
// lockTransaction for n.
func addTransaction(ctx context.Context, tx pgx.Tx, p *principal, ws, brand string, n newTransaction) (v transactionView, created, merged bool, err error) {
	// A refund may name its payment by any id the sender has for it
	// (findPayment); it is kept as that payment's source_id, so it comes
	// off the right payment.
	if n.RefundOf != "" {
		paid, found, err := findPayment(ctx, tx, ws, brand, n.RefundOf)
		if err != nil {
			return v, false, false, err
		}
		if found {
			n.RefundOf = paid.sourceID
		}
	}
	// The same refund already in from an Order Refunded event is that one.
	if n.Kind == "refund" || n.Kind == "chargeback" {
		known, err := sameRefund(ctx, tx, brand, n.Source, n.SourceID, n.RefundOf, n.AmountMinor, n.Currency, n.OccurredAt)
		if err != nil {
			return v, false, false, err
		}
		if known != "" {
			v, err = scanTransaction(tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM transactions t WHERE t.id = $1`, known))
			return v, false, true, err
		}
	}
	if n.Kind == "payment" {
		known, found, err := samePayment(ctx, tx, brand, n.Source, n.SourceID, n.RecordID, n.AmountMinor, n.Currency, n.OccurredAt)
		if err != nil {
			return v, false, false, err
		}
		if found {
			if err := paymentMerged(ctx, tx, p, ws, brand, n.Source, n.SourceID, n.AmountMinor, n.Currency, n.OccurredAt, known); err != nil {
				return v, false, false, err
			}
			if n.RecordID != "" && known.recordID == nil {
				if _, err := tx.Exec(ctx, `UPDATE transactions SET record_id = $2, updated_at = now() WHERE id = $1`, known.id, n.RecordID); err != nil {
					return v, false, false, err
				}
			}
			if v, err = scanTransaction(tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM transactions t WHERE t.id = $1`, known.id)); err != nil {
				return v, false, false, err
			}
			if known.source == "api" || known.source == orderSource {
				_, err = countPayment(ctx, tx, ws, v)
			}
			return v, false, true, err
		}
	}
	if v, created, err = saveTransaction(ctx, tx, p, ws, brand, n); err != nil {
		return v, false, false, err
	}
	if !created {
		if err := paymentResent(ctx, tx, p, ws, v, n.OccurredAt); err != nil {
			return v, false, false, err
		}
	}
	_, err = countPayment(ctx, tx, ws, v)
	return v, created, false, err
}

type transactionInput struct {
	RecordID    *string    `json:"record_id"`
	UserID      *string    `json:"user_id"`
	Kind        string     `json:"kind"`
	Status      *string    `json:"status"`
	AmountMinor *int64     `json:"amount_minor"`
	Currency    string     `json:"currency"`
	Product     *string    `json:"product"`
	SourceID    string     `json:"source_id"`
	RefundOf    *string    `json:"refund_of"`
	Description *string    `json:"description"`
	OccurredAt  *time.Time `json:"occurred_at"`
}

// createTransaction adds one transaction from the API. Sending the same
// source_id again answers the one already saved (200), with its status
// updated when it changed.
func (s *Server) createTransaction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in transactionInput
	if !decode(w, r, &in) {
		return
	}
	n := newTransaction{Kind: in.Kind, Status: "succeeded", Currency: strings.ToUpper(strings.TrimSpace(in.Currency)), Source: "api",
		SourceID: strings.TrimSpace(in.SourceID), OccurredAt: time.Now().UTC()}
	var problem string
	switch {
	case !slices.Contains(transactionKinds, n.Kind):
		problem = "kind is payment, refund, chargeback or credit"
	case in.AmountMinor == nil || *in.AmountMinor < 0:
		problem = "amount_minor is the amount in the currency's minor unit (cents), 0 or more; kind says which way the money went"
	case !currencyCode.MatchString(n.Currency):
		problem = "currency is a three-letter ISO 4217 code, such as USD"
	case n.SourceID == "" || len(n.SourceID) > 200:
		problem = "source_id is your id for the transaction, 1 to 200 characters: sending it again never adds it twice"
	case in.Status != nil && !slices.Contains(transactionStatuses, *in.Status):
		problem = "status is succeeded, pending, failed or reversed"
	case in.Description != nil && len(*in.Description) > 500:
		problem = "description is at most 500 characters"
	case in.RefundOf != nil && (len(*in.RefundOf) > 200 || (n.Kind != "refund" && n.Kind != "chargeback")):
		problem = "refund_of names the payment a refund or chargeback gives back: its source_id (an order's order_id), its order event's messageId or Outflare's transaction id"
	}
	if problem != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", problem)
		return
	}
	n.AmountMinor = *in.AmountMinor
	if in.Status != nil {
		n.Status = *in.Status
	}
	if in.Description != nil {
		n.Description = strings.TrimSpace(*in.Description)
	}
	if in.RefundOf != nil {
		n.RefundOf = *in.RefundOf
	}
	if in.OccurredAt != nil {
		n.OccurredAt = *in.OccurredAt
	}
	start := time.Now()
	var v transactionView
	var created, merged bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockTransaction(ctx, tx, a.Brand, n.Kind, n.SourceID); err != nil {
			return err
		}
		if in.RecordID != nil && *in.RecordID != "" {
			var id string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM records WHERE workspace_id = $1 AND id = $2 AND merged_into IS NULL AND deleted_at IS NULL
				AND ($3 = '' OR brand_id = $3::uuid)`, a.WorkspaceID, validOrNil(*in.RecordID), a.Brand).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
				return errInvalid("record_id is a person or company of this brand")
			} else if err != nil {
				return err
			}
			n.RecordID = id
		}
		if in.Product != nil && *in.Product != "" {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM products WHERE brand_id = $1 AND (id::text = $2 OR key = $2) AND archived_at IS NULL`,
				a.Brand, *in.Product).Scan(&n.ProductID); errors.Is(err, pgx.ErrNoRows) {
				return errInvalid("product is the id or key of one of this brand's products (GET /v1/brands/{brand}/products)")
			} else if err != nil {
				return err
			}
		}
		if n.RecordID == "" && in.UserID != nil && strings.TrimSpace(*in.UserID) != "" {
			rec, _, err := ownerOf(ctx, tx, a, a.Brand, "user_id", strings.TrimSpace(*in.UserID))
			if err != nil {
				return err
			}
			if rec != nil {
				n.RecordID = *rec
			}
		}
		var err error
		v, created, merged, err = addTransaction(ctx, tx, &a.principal, a.WorkspaceID, a.Brand, n)
		return err
	})
	outcome := "created"
	switch {
	case merged:
		outcome = "merged"
	case !created:
		outcome = "duplicate"
	}
	if err != nil {
		outcome = "error"
		if errors.As(err, new(errInvalid)) {
			outcome = "invalid"
		}
	}
	if errors.Is(err, errTransactionDeleted) {
		metrics.TransactionSaved("api", "deleted", time.Since(start))
		writeError(w, r, http.StatusConflict, "transaction_deleted",
			"a transaction with this source_id was deleted and is in Recently deleted: restore it there (POST .../recently-deleted/{deletion}/restore) instead of sending it again")
		return
	}
	metrics.TransactionSaved("api", outcome, time.Since(start))
	if s.allocError(w, r, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, v)
}

func (s *Server) getTransaction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := scanTransaction(s.pool.QueryRow(r.Context(), `SELECT `+transactionColumns+` FROM transactions t
		WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.id = $3`, a.WorkspaceID, a.Brand, validOrNil(r.PathValue("transaction"))))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "transaction not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// listTransactions lists a brand's transactions, newest first: all, or a
// person's or company's (?record=), of some kinds (?kind=payment,refund).
func (s *Server) listTransactions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	cursor, ok := pageCursor(w, r, pageBefore)
	if !ok {
		return
	}
	kinds := []string{}
	if k := q.Get("kind"); k != "" {
		for _, x := range strings.Split(k, ",") {
			if !slices.Contains(transactionKinds, x) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is payment, refund, chargeback or credit, or several separated by commas")
				return
			}
			kinds = append(kinds, x)
		}
	}
	record := q.Get("record")
	if record != "" && !validUUID(record) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record is a record id")
		return
	}
	args := []any{a.WorkspaceID, a.Brand, nullIfEmpty(record), kinds, validOrNil(cursor), limit + 1}
	rows, err := s.pool.Query(r.Context(), `SELECT `+transactionColumns+` FROM transactions t
		WHERE t.workspace_id = $1 AND t.brand_id = $2 AND ($3::uuid IS NULL OR t.record_id = $3)
			AND (cardinality($4::text[]) = 0 OR t.kind = ANY ($4))
			AND ($5::uuid IS NULL OR (t.occurred_at, t.id) < (SELECT occurred_at, id FROM transactions WHERE id = $5))
		ORDER BY t.occurred_at DESC, t.id DESC LIMIT $6`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (transactionView, error) { return scanTransaction(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next string
	if len(list) > limit {
		list = list[:limit]
		next = list[limit-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list), "next_cursor": nextCursor(pageBefore, next)})
}

// RunTransactionRates fills in the brand amounts of transactions saved
// before their day's exchange rate was known, every interval.
func (s *Server) RunTransactionRates(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.ConvertTransactions(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "transactions.convert_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ConvertTransactions converts what it can of the transactions with no
// brand amount yet, and says how many it converted.
func (s *Server) ConvertTransactions(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, amount_minor, currency, brand_currency, occurred_at FROM transactions
		WHERE brand_amount_minor IS NULL ORDER BY occurred_at LIMIT 1000`)
	if err != nil {
		return 0, err
	}
	type todo struct {
		id, currency, brandCurrency string
		minor                       int64
		at                          time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (todo, error) {
		var t todo
		return t, r.Scan(&t.id, &t.minor, &t.currency, &t.brandCurrency, &t.at)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range list {
		v, err := brandAmount(ctx, s.pool, t.currency, t.minor, t.brandCurrency, t.at)
		if err != nil {
			return n, fmt.Errorf("transactions: convert %s: %w", t.id, err)
		}
		if v == nil {
			continue
		}
		if _, err := s.pool.Exec(ctx, `UPDATE transactions SET brand_amount_minor = $2 WHERE id = $1 AND brand_amount_minor IS NULL`, t.id, *v); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
