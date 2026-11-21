package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
)

// Subscriptions from a brand's own billing system (docs/transactions.md,
// "Subscriptions from your own billing"). A billing connection (Stripe)
// writes a customer's subscriptions as it reads them; a business that bills
// with its own system sends each one here instead, and again whenever it
// changes. Either way the state is a row of payment_subscriptions, its
// history is the subscription.* events on the person's timeline, and
// monthly revenue, billing status and plan are worked out from it.

const ownBillingProvider = "api"

var (
	subscriptionStatuses  = []string{"trialing", "active", "past_due", "canceled"}
	subscriptionIntervals = []string{"day", "week", "month", "year"}
)

func (s *Server) routesBillingSubscriptions(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/billing-subscriptions", s.onBrand(auth.ScopeCustomersRead, s.listBillingSubscriptions))
	mux.HandleFunc("POST /v1/brands/{brand}/billing-subscriptions", s.onBrand(auth.ScopeCustomersManage, s.saveBillingSubscription))
}

type billingSubscriptionView struct {
	ID           string     `json:"id"`
	BrandID      string     `json:"brand_id"`
	RecordID     *string    `json:"record_id"`
	Provider     string     `json:"provider"`
	SourceID     string     `json:"source_id"`
	Status       string     `json:"status"`
	Plan         string     `json:"plan"`
	Product      string     `json:"product"`
	PriceID      string     `json:"price_id"`
	Interval     string     `json:"interval"`
	MonthlyMinor int64      `json:"monthly_minor"`
	Currency     string     `json:"currency"`
	StartedAt    time.Time  `json:"started_at"`
	CanceledAt   *time.Time `json:"canceled_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const billingSubscriptionColumns = `p.id::text, p.brand_id::text, p.record_id::text, p.provider, p.provider_id, p.status, p.plan, p.product_id, p.price_id,
	p.billing_interval, p.monthly_minor, p.currency, p.started_at, p.canceled_at, p.created_at, p.updated_at`

func scanBillingSubscription(row pgx.Row) (billingSubscriptionView, error) {
	var v billingSubscriptionView
	err := row.Scan(&v.ID, &v.BrandID, &v.RecordID, &v.Provider, &v.SourceID, &v.Status, &v.Plan, &v.Product, &v.PriceID,
		&v.Interval, &v.MonthlyMinor, &v.Currency, &v.StartedAt, &v.CanceledAt, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

type billingSubscriptionInput struct {
	RecordID    *string    `json:"record_id"`
	UserID      *string    `json:"user_id"`
	SourceID    string     `json:"source_id"`
	Status      *string    `json:"status"`
	Plan        *string    `json:"plan"`
	Product     *string    `json:"product"`
	PriceID     *string    `json:"price_id"`
	Interval    *string    `json:"interval"`
	AmountMinor *int64     `json:"amount_minor"`
	Currency    string     `json:"currency"`
	StartedAt   *time.Time `json:"started_at"`
	CanceledAt  *time.Time `json:"canceled_at"`
}

// monthlyMinor is what amount per interval brings in a month.
func monthlyMinor(amount int64, interval string) int64 {
	switch interval {
	case "day":
		return amount * 365 / 12
	case "week":
		return amount * 52 / 12
	case "year":
		return amount / 12
	}
	return amount
}

// saveBillingSubscription saves one subscription's current state from the
// brand's own billing. The same source_id sent again updates it (200), and
// what changed is written on the person's timeline: subscription.started
// once, subscription.changed when the price or amount moves, and
// subscription.canceled once.
func (s *Server) saveBillingSubscription(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in billingSubscriptionInput
	if !decode(w, r, &in) {
		return
	}
	sourceID := strings.TrimSpace(in.SourceID)
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	status, interval := "active", "month"
	if in.Status != nil {
		status = *in.Status
	}
	if in.Interval != nil {
		interval = *in.Interval
	}
	text := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	plan, priceID, product := text(in.Plan), text(in.PriceID), text(in.Product)
	hasPerson := (in.RecordID != nil && *in.RecordID != "") || text(in.UserID) != ""
	var problem string
	switch {
	case sourceID == "" || len(sourceID) > 200:
		problem = "source_id is your id for the subscription, 1 to 200 characters: sending it again updates the one saved"
	case !hasPerson:
		problem = "record_id or user_id says whose subscription it is: a person or company of this brand, or your own id for them (the userId your events send)"
	case !slices.Contains(subscriptionStatuses, status):
		problem = "status is trialing, active, past_due or canceled"
	case !slices.Contains(subscriptionIntervals, interval):
		problem = "interval is day, week, month or year: how often amount_minor is charged"
	case in.AmountMinor == nil || *in.AmountMinor < 0:
		problem = "amount_minor is what one interval costs in the currency's minor unit (cents), 0 or more"
	case !currencyCode.MatchString(currency):
		problem = "currency is a three-letter ISO 4217 code, such as USD"
	case len(plan) > 200 || len(priceID) > 200:
		problem = "plan and price_id are at most 200 characters"
	case in.StartedAt != nil && in.StartedAt.After(time.Now().Add(24*time.Hour)):
		problem = "started_at is when the subscription began, not a time to come"
	}
	if problem != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", problem)
		return
	}
	monthly := monthlyMinor(*in.AmountMinor, interval)
	started := time.Now().UTC()
	if in.StartedAt != nil {
		started = in.StartedAt.UTC()
	}
	canceledAt := in.CanceledAt
	if status == "canceled" && canceledAt == nil {
		now := time.Now().UTC()
		canceledAt = &now
	}
	if status != "canceled" {
		canceledAt = nil
	}

	var v billingSubscriptionView
	var created bool
	err := s.retryTx(ctx, "save_billing_subscription", func(tx pgx.Tx) error {
		v, created = billingSubscriptionView{}, false
		// Two saves of one subscription take turns.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "billing_subscription:"+a.Brand+":"+sourceID); err != nil {
			return err
		}
		var rec string
		if in.RecordID != nil && *in.RecordID != "" {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM records WHERE workspace_id = $1 AND id = $2 AND merged_into IS NULL AND deleted_at IS NULL
				AND brand_id = $3::uuid`, a.WorkspaceID, validOrNil(*in.RecordID), a.Brand).Scan(&rec); errors.Is(err, pgx.ErrNoRows) {
				return errInvalid("record_id is a person or company of this brand")
			} else if err != nil {
				return err
			}
		} else {
			found, _, err := ownerOf(ctx, tx, a, a.Brand, "user_id", text(in.UserID))
			if err != nil {
				return err
			}
			if found == nil {
				return errInvalid("nobody in this brand has this user_id yet: send an identify for them first (POST /v1/brands/{brand}/events), then the subscription")
			}
			rec = *found
		}
		var productID, productKey string
		if product != "" {
			if err := tx.QueryRow(ctx, `SELECT id::text, key FROM products WHERE brand_id = $1 AND (id::text = $2 OR key = $2) AND archived_at IS NULL`,
				a.Brand, product).Scan(&productID, &productKey); errors.Is(err, pgx.ErrNoRows) {
				return errInvalid("product is the id or key of one of this brand's products (GET /v1/brands/{brand}/products)")
			} else if err != nil {
				return err
			}
		}

		var prevID, prevPrice, prevStatus string
		var prevMonthly int64
		var prevRecord *string
		err := tx.QueryRow(ctx, `SELECT id::text, price_id, monthly_minor, status, record_id::text FROM payment_subscriptions
			WHERE brand_id = $1 AND integration_id IS NULL AND provider_id = $2`, a.Brand, sourceID).Scan(&prevID, &prevPrice, &prevMonthly, &prevStatus, &prevRecord)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		created = prevID == ""
		if created {
			err = tx.QueryRow(ctx, `INSERT INTO payment_subscriptions (workspace_id, brand_id, provider, provider_id, record_id, customer_id, status,
					plan, product_id, price_id, billing_interval, monthly_minor, currency, started_at, canceled_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id::text`,
				a.WorkspaceID, a.Brand, ownBillingProvider, sourceID, rec, text(in.UserID), status, plan, productKey, priceID, interval,
				monthly, currency, started, canceledAt).Scan(&prevID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE payment_subscriptions SET record_id = $2, status = $3, plan = $4, product_id = $5, price_id = $6,
					billing_interval = $7, monthly_minor = $8, currency = $9, canceled_at = $10, updated_at = now() WHERE id = $1`,
				prevID, rec, status, plan, productKey, priceID, interval, monthly, currency, canceledAt)
		}
		if err != nil {
			return err
		}

		props := func() map[string]any {
			return map[string]any{"provider": ownBillingProvider, "subscription_id": sourceID, "plan": plan, "product_id": productKey,
				"price_id": priceID, "interval": interval, "status": status, "mrr": map[string]any{"amount_minor": monthly, "currency": currency}}
		}
		ev := func(name, msg string, at time.Time, p map[string]any) error {
			_, err := customers.Record(ctx, tx, a.WorkspaceID, customers.Event{RecordID: rec, Name: name, Source: ownBillingProvider, OccurredAt: at,
				MessageID: "api:subscription:" + a.Brand + ":" + sourceID + ":" + msg, RequestID: RequestID(ctx), Properties: p, ProductID: productID})
			return err
		}
		if err := ev(evSubscriptionStarted, "started", started, props()); err != nil {
			return err
		}
		if !created && status != "canceled" && (prevPrice != priceID || prevMonthly != monthly) {
			p := props()
			p["previous_price_id"] = prevPrice
			p["previous_mrr"] = map[string]any{"amount_minor": prevMonthly, "currency": currency}
			if err := ev(evSubscriptionChanged, fmt.Sprintf("changed:%s:%d:%d", priceID, monthly, time.Now().Unix()/60), time.Now(), p); err != nil {
				return err
			}
		}
		if status == "canceled" {
			if err := ev(evSubscriptionCanceled, "canceled", *canceledAt, props()); err != nil {
				return err
			}
		}

		c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
		if err != nil {
			return err
		}
		people := []string{rec}
		if prevRecord != nil && *prevRecord != rec {
			people = append(people, *prevRecord)
		}
		for _, id := range people {
			if err := recomputeBilling(ctx, tx, a, c, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		action := "billing_subscription.updated"
		if created {
			action = "billing_subscription.created"
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "billing_subscription", prevID, map[string]any{
			"brand_id": a.Brand, "record_id": rec, "status": status, "previous_status": prevStatus, "interval": interval}); err != nil {
			return err
		}
		v, err = scanBillingSubscription(tx.QueryRow(ctx, `SELECT `+billingSubscriptionColumns+` FROM payment_subscriptions p WHERE p.id = $1`, prevID))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, v)
}

// listBillingSubscriptions lists the brand's subscriptions, from billing
// connections and its own billing alike, latest start first: all, a
// person's or company's (?record=), or those in one status (?status=).
func (s *Server) listBillingSubscriptions(w http.ResponseWriter, r *http.Request) {
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
	rec, status := q.Get("record"), q.Get("status")
	if rec != "" && !validUUID(rec) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record is a record id")
		return
	}
	if status != "" && !slices.Contains(subscriptionStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is trialing, active, past_due or canceled")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+billingSubscriptionColumns+` FROM payment_subscriptions p
		WHERE p.workspace_id = $1 AND p.brand_id = $2 AND ($3::uuid IS NULL OR p.record_id = $3) AND ($4 = '' OR p.status = $4)
			AND ($5::uuid IS NULL OR (p.started_at, p.id) < (SELECT started_at, id FROM payment_subscriptions WHERE id = $5))
		ORDER BY p.started_at DESC, p.id DESC LIMIT $6`, a.WorkspaceID, a.Brand, nullIfEmpty(rec), status, validOrNil(cursor), limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (billingSubscriptionView, error) { return scanBillingSubscription(row) })
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
