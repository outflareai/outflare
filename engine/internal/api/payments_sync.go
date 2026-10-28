package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Payments: each payment connection (Stripe) is read every few minutes and
// written into customer records, so revenue is stored once and every
// module reads the same numbers.
//
//   - A customer is a person with a stripe.customer identity. When the
//     customer carries the workspace's own user id (metadata user_id), it
//     joins the person who already has that id. Its email is added
//     unverified: a billing address never claims or merges a record.
//   - Payments, refunds and subscription starts, changes and cancellations
//     are events on the person's timeline (source stripe), each stored
//     once however often it is read.
//   - Plan (billing_plan), monthly revenue, lifetime revenue, customer since and billing
//     status are computed fields, worked out again whenever the person's
//     payments or subscriptions change.
//
// The first sync lists everything (customers, then subscriptions, payments
// and refunds), after taking the provider's change cursor, so nothing that
// changes meanwhile is missed. Later syncs read only the changes. Each run
// stops after paymentsStep and carries on next time.

const jobPaymentsSync = "payments.sync"

// How often a connection syncs, how long one run may take, and how many
// pages of changes one run reads. Tests make them small.
var (
	paymentsEvery       = 5 * time.Minute
	paymentsStep        = 90 * time.Second
	paymentsChangePages = 50
)

// paymentEventNames are the timeline events payments write.
const (
	evPaymentSucceeded      = "payment.succeeded"
	evPaymentRefunded       = "payment.refunded"
	evSubscriptionStarted   = "subscription.started"
	evSubscriptionChanged   = "subscription.changed"
	evSubscriptionCanceled  = "subscription.canceled"
	paymentsFirstPartySrc   = "first_party"
	paymentsAuditAction     = "payments.synced"
	paymentsFailedEventName = "payments.sync_failed"
)

type paymentTotals struct {
	Customers      int `json:"customers"`
	Subscriptions  int `json:"subscriptions"`
	Payments       int `json:"payments"`
	Refunds        int `json:"refunds"`
	RecordsCreated int `json:"records_created"`
	// Ids from the billing account that another person already holds.
	IDConflicts int `json:"id_conflicts"`
}

func (t *paymentTotals) add(o paymentTotals) {
	t.Customers += o.Customers
	t.Subscriptions += o.Subscriptions
	t.Payments += o.Payments
	t.Refunds += o.Refunds
	t.RecordsCreated += o.RecordsCreated
	t.IDConflicts += o.IDConflicts
}

func (t paymentTotals) any() bool { return t != paymentTotals{} }

// paymentConnectors are the connector ids that read payments.
func (s *Server) paymentConnectors() []string {
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if d.Status == connectors.Available && slices.Contains(d.Capabilities, connectors.CapPayments) {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// RunPayments queues payment syncs until ctx ends.
func (s *Server) RunPayments(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.schedulePayments(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("payments: schedule syncs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// schedulePayments queues a sync for every connected payment connection
// that has not run for paymentsEvery.
func (s *Server) schedulePayments(ctx context.Context) error {
	ids := s.paymentConnectors()
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT i.id::text, i.workspace_id::text FROM integrations i LEFT JOIN payment_syncs p ON p.integration_id = i.id
		WHERE i.connector = ANY($1) AND i.status = 'connected'
		  AND COALESCE(p.last_run_at, '-infinity') < now() - make_interval(secs => $2)`, ids, paymentsEvery.Seconds())
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ws)
	})
	if err != nil {
		return err
	}
	bucket := time.Now().UnixMilli() / paymentsEvery.Milliseconds()
	for _, d := range list {
		if err := s.queuePaymentSync(ctx, s.pool, d.ws, d.id, fmt.Sprintf("%s:%d", d.id, bucket)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) queuePaymentSync(ctx context.Context, q jobs.Querier, ws, id, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: jobPaymentsSync, WorkspaceID: &ws,
		Payload: map[string]string{"integration_id": id}, MaxAttempts: 3, IdempotencyKey: key})
	return err
}

func (s *Server) paymentsSyncJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		IntegrationID string `json:"integration_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.IntegrationID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return s.syncPayments(ctx, *job.WorkspaceID, p.IntegrationID)
}

// paymentState is where a connection's sync got to.
type paymentState struct {
	Phase          string
	BackfillKind   *string
	BackfillCursor *string
	ChangesCursor  *string
	Totals         paymentTotals
}

// syncPayments runs one sync of a payment connection. A failure from the
// provider is kept on the connection and tried again on the next run; a
// refused key marks the connection broken, like a failed test does.
func (s *Server) syncPayments(ctx context.Context, ws, id string) error {
	o, err := s.integrations.Open(ctx, s.pool, ws, id)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	reader, ok := o.Connector.(connectors.PaymentReader)
	if !ok || o.Status != connectors.StatusConnected {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO payment_syncs (integration_id, workspace_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, ws); err != nil {
		return err
	}
	var st paymentState
	if err := s.pool.QueryRow(ctx, `UPDATE payment_syncs SET last_run_at = now(), updated_at = now() WHERE integration_id = $1 AND workspace_id = $2
		RETURNING phase, backfill_kind, backfill_cursor, changes_cursor, totals`, id, ws).
		Scan(&st.Phase, &st.BackfillKind, &st.BackfillCursor, &st.ChangesCursor, &st.Totals); err != nil {
		return err
	}
	run := &paymentRun{s: s, ws: ws, o: o, reader: reader, provider: o.Connector.Definition().ID, st: st,
		a: &access{principal: principal{ActorType: "system"}, WorkspaceID: ws}}
	runErr := run.run(ctx)
	if runErr != nil && !isProviderError(runErr) {
		return runErr
	}
	if n := run.ran; n.any() {
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			return record(ctx, tx, &run.a.principal, ws, paymentsAuditAction, "integration", id, map[string]any{
				"connector": run.provider, "phase": run.st.Phase, "customers": n.Customers, "subscriptions": n.Subscriptions,
				"payments": n.Payments, "refunds": n.Refunds, "records_created": n.RecordsCreated})
		}); err != nil {
			return err
		}
	}
	var msg *string
	if runErr != nil {
		m := runErr.Error()
		msg = &m
		code := "connector_" + connectors.KindOf(runErr)
		if connectors.KindOf(runErr) == connectors.ErrAuth {
			if err := s.integrations.Checked(ctx, s.pool, ws, id, nil, runErr); err != nil {
				return err
			}
		}
		track.Fail(ctx, s.log, track.Event{Name: paymentsFailedEventName, WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"connector": run.provider, "kind": connectors.KindOf(runErr), "phase": run.st.Phase}}, code, runErr)
	}
	_, err = s.pool.Exec(ctx, `UPDATE payment_syncs SET last_error = $3,
		last_success_at = CASE WHEN $3::text IS NULL THEN now() ELSE last_success_at END, updated_at = now()
		WHERE integration_id = $1 AND workspace_id = $2`, id, ws, msg)
	return err
}

// isProviderError tells the provider's failures (a refused key, an outage)
// from Outflare's own.
func isProviderError(err error) bool {
	var ce *connectors.Error
	return errors.As(err, &ce)
}

type paymentRun struct {
	s        *Server
	ws       string
	o        *connectors.Opened
	reader   connectors.PaymentReader
	provider string
	a        *access
	st       paymentState
	// What this run wrote, for its one audit event.
	ran paymentTotals
}

func (r *paymentRun) run(ctx context.Context) error {
	deadline := time.Now().Add(paymentsStep)
	if r.st.Phase == "backfill" {
		if r.st.ChangesCursor == nil {
			page, err := r.reader.PaymentChanges(ctx, r.o.Client, r.o.Creds, "")
			if err != nil {
				return err
			}
			r.st.ChangesCursor = &page.Cursor
			if err := r.save(ctx, r.s.pool); err != nil {
				return err
			}
		}
		for time.Now().Before(deadline) {
			kind := connectors.PaymentKinds[0]
			if r.st.BackfillKind != nil {
				kind = *r.st.BackfillKind
			}
			page, err := r.reader.PaymentList(ctx, r.o.Client, r.o.Creds, kind, deref(r.st.BackfillCursor))
			if err != nil {
				return err
			}
			if page.More {
				r.st.BackfillKind, r.st.BackfillCursor = &kind, &page.Cursor
			} else if i := slices.Index(connectors.PaymentKinds, kind); i+1 < len(connectors.PaymentKinds) {
				next := connectors.PaymentKinds[i+1]
				r.st.BackfillKind, r.st.BackfillCursor = &next, nil
			} else {
				r.st.Phase, r.st.BackfillKind, r.st.BackfillCursor = "changes", nil, nil
			}
			if err := r.apply(ctx, page); err != nil {
				return err
			}
			if r.st.Phase == "changes" {
				break
			}
		}
	}
	for i := 0; r.st.Phase == "changes" && i < paymentsChangePages && time.Now().Before(deadline); i++ {
		page, err := r.reader.PaymentChanges(ctx, r.o.Client, r.o.Creds, deref(r.st.ChangesCursor))
		if errors.Is(err, connectors.ErrPaymentCursor) {
			// The provider forgot where we were: read everything again.
			// Events and records are matched by id, so nothing doubles.
			r.st = paymentState{Phase: "backfill", Totals: r.st.Totals}
			return r.save(ctx, r.s.pool)
		}
		if err != nil {
			return err
		}
		r.st.ChangesCursor = &page.Cursor
		if err := r.apply(ctx, page); err != nil {
			return err
		}
		if !page.More {
			break
		}
	}
	return nil
}

func (r *paymentRun) save(ctx context.Context, q customers.Querier) error {
	_, err := q.Exec(ctx, `UPDATE payment_syncs SET phase = $3, backfill_kind = $4, backfill_cursor = $5, changes_cursor = $6, totals = $7,
		backfilled_at = CASE WHEN $3 = 'changes' THEN COALESCE(backfilled_at, now()) ELSE NULL END, updated_at = now()
		WHERE integration_id = $1 AND workspace_id = $2`,
		r.o.ID, r.ws, r.st.Phase, r.st.BackfillKind, r.st.BackfillCursor, r.st.ChangesCursor, r.st.Totals)
	return err
}

// apply writes a page into customer records and saves where the sync got
// to, in one transaction.
func (r *paymentRun) apply(ctx context.Context, page connectors.PaymentPage) error {
	if err := r.fetchMissing(ctx, &page); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		var n paymentTotals
		touched := map[string]bool{}
		partnerSales, err := billingSalesOn(ctx, tx, r.ws, r.provider)
		if err != nil {
			return err
		}
		for _, cust := range page.Customers {
			if cust.Deleted {
				continue
			}
			rec, created, err := r.customer(ctx, tx, &n, cust)
			if err != nil {
				return err
			}
			n.Customers++
			if created {
				n.RecordsCreated++
			}
			touched[rec] = true
		}
		for _, sub := range page.Subscriptions {
			rec, err := r.recordFor(ctx, tx, sub.CustomerID)
			if err != nil {
				return err
			}
			if rec == "" {
				continue
			}
			if err := r.subscription(ctx, tx, rec, sub); err != nil {
				return err
			}
			n.Subscriptions++
			touched[rec] = true
		}
		for _, ch := range page.Charges {
			rec, err := r.recordFor(ctx, tx, ch.CustomerID)
			if err != nil {
				return err
			}
			if rec == "" {
				continue // a payment with no customer, such as a one-off guest checkout
			}
			added, err := customers.Record(ctx, tx, r.ws, customers.Event{RecordID: rec, Name: evPaymentSucceeded, Source: r.provider,
				OccurredAt: ch.Created, MessageID: r.provider + ":charge:" + ch.ID, RequestID: RequestID(ctx),
				Properties: map[string]any{"amount_minor": ch.AmountMinor, "currency": ch.Currency, "provider": r.provider,
					"payment_id": ch.ID, "invoice_id": ch.InvoiceID, "customer_id": ch.CustomerID}})
			if err != nil {
				return err
			}
			if added {
				n.Payments++
				touched[rec] = true
				if partnerSales {
					if err := enqueueBillingConversion(ctx, tx, r.ws, partnerBillingConversion{Provider: r.provider, Type: "sale", RecordID: rec,
						ExternalID: ch.ID, AmountMinor: ch.AmountMinor, Currency: ch.Currency, At: ch.Created}); err != nil {
						return err
					}
				}
			}
		}
		for _, rf := range page.Refunds {
			rec, err := r.recordFor(ctx, tx, rf.CustomerID)
			if err != nil {
				return err
			}
			if rec == "" {
				continue
			}
			added, err := customers.Record(ctx, tx, r.ws, customers.Event{RecordID: rec, Name: evPaymentRefunded, Source: r.provider,
				OccurredAt: rf.Created, MessageID: r.provider + ":refund:" + rf.ID, RequestID: RequestID(ctx),
				Properties: map[string]any{"amount_minor": rf.AmountMinor, "currency": rf.Currency, "provider": r.provider,
					"refund_id": rf.ID, "payment_id": rf.ChargeID, "customer_id": rf.CustomerID}})
			if err != nil {
				return err
			}
			if added {
				n.Refunds++
				touched[rec] = true
				if partnerSales {
					if err := enqueueBillingConversion(ctx, tx, r.ws, partnerBillingConversion{Provider: r.provider, Type: "refund", RecordID: rec,
						ExternalID: rf.ID, RefundOf: rf.ChargeID, AmountMinor: rf.AmountMinor, Currency: rf.Currency, At: rf.Created}); err != nil {
						return err
					}
				}
			}
		}
		if len(touched) > 0 {
			c, err := loadCatalog(ctx, tx, r.ws, false)
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(touched))
			for id := range touched {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				if err := r.recompute(ctx, tx, c, id); err != nil {
					return err
				}
			}
		}
		r.st.Totals.add(n)
		r.ran.add(n)
		return r.save(ctx, tx)
	})
}

func (r *paymentRun) namespace() string { return r.provider + ".customer" }

// fetchMissing reads the customers a page's subscriptions, payments and
// refunds belong to that are not stored yet, before the transaction opens,
// so no provider call runs inside it.
func (r *paymentRun) fetchMissing(ctx context.Context, page *connectors.PaymentPage) error {
	have := map[string]bool{}
	for _, c := range page.Customers {
		have[c.ID] = true
	}
	var want []string
	add := func(id string) {
		if id != "" && !have[id] {
			have[id] = true
			want = append(want, id)
		}
	}
	for _, s := range page.Subscriptions {
		add(s.CustomerID)
	}
	for _, c := range page.Charges {
		add(c.CustomerID)
	}
	for _, rf := range page.Refunds {
		add(rf.CustomerID)
	}
	if len(want) == 0 {
		return nil
	}
	rows, err := r.s.pool.Query(ctx, `SELECT value FROM record_identities WHERE workspace_id = $1 AND namespace = $2 AND value = ANY($3)
		AND valid_to IS NULL AND verified`, r.ws, r.namespace(), want)
	if err != nil {
		return err
	}
	known, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range want {
		if slices.Contains(known, id) {
			continue
		}
		c, err := r.reader.PaymentCustomer(ctx, r.o.Client, r.o.Creds, id)
		if err != nil {
			return err
		}
		if !c.Deleted {
			page.Customers = append(page.Customers, c)
		}
	}
	return nil
}

// recordFor is the person a provider customer is stored as, or "".
func (r *paymentRun) recordFor(ctx context.Context, tx pgx.Tx, customerID string) (string, error) {
	if customerID == "" {
		return "", nil
	}
	id, _, err := customers.OwnerOf(ctx, tx, r.ws, r.namespace(), customerID)
	return id, err
}

// customer stores a provider customer as a person and reports whether a
// record was created for it.
func (r *paymentRun) customer(ctx context.Context, tx pgx.Tx, n *paymentTotals, c connectors.PaymentCustomer) (string, bool, error) {
	ns := r.namespace()
	rec, ok, err := customers.OwnerOf(ctx, tx, r.ws, ns, c.ID)
	if err != nil {
		return "", false, err
	}
	created := false
	if !ok && c.UserID != "" {
		// The workspace's own product already knows them by this id.
		if rec, ok, err = customers.OwnerOf(ctx, tx, r.ws, "user_id", c.UserID); err != nil {
			return "", false, err
		}
		if ok {
			if err := r.identity(ctx, tx, n, rec, ns, c.ID, true); err != nil {
				return "", false, err
			}
		}
	}
	if !ok {
		if rec, err = customers.EnsurePerson(ctx, tx, r.ws, ns, c.ID, true, RequestID(ctx)); err != nil {
			return "", false, err
		}
		created = true
	}
	if c.UserID != "" {
		if err := r.identity(ctx, tx, n, rec, "user_id", c.UserID, true); err != nil {
			return "", false, err
		}
	}
	if e, err := normalizeEmail(c.Email); err == nil && e != "" {
		if err := r.identity(ctx, tx, n, rec, "email", e, false); err != nil {
			return "", false, err
		}
	}
	if c.Name != "" {
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND name = ''`,
			rec, r.ws, cutUTF8(c.Name, 500)); err != nil {
			return "", false, err
		}
	}
	return rec, created, nil
}

// identity adds an identity to a record unless it has it. A verified one
// that another record already holds is added unverified instead, so a
// billing account never takes over someone else's id; the two are then
// listed as possible duplicates and counted, since one product account on
// two people is a mismatch someone should look at. Nothing is merged here.
func (r *paymentRun) identity(ctx context.Context, tx pgx.Tx, n *paymentTotals, rec, namespace, value string, verified bool) error {
	owner := ""
	if verified {
		var conflict recordConflict
		if err := claimFree(ctx, tx, r.a, rec, namespace, value); errors.As(err, &conflict) {
			verified = false
			o, ok, err := customers.OwnerOf(ctx, tx, r.ws, namespace, value)
			if err != nil {
				return err
			}
			if ok && o != rec {
				owner = o
			}
		} else if err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 THEN now() END, $6)
		ON CONFLICT (record_id, namespace, value) WHERE valid_to IS NULL DO NOTHING`, r.ws, rec, namespace, value, verified, r.provider)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if owner != "" {
		n.IDConflicts++
		r.s.log.WarnContext(ctx, "payments: a customer's id belongs to another person; added unverified and listed as a possible duplicate",
			"workspace_id", r.ws, "record_id", rec, "other_record_id", owner, "namespace", namespace, "provider", r.provider)
		_, err = flagPair(ctx, tx, r.ws, rec, owner, namespace, true)
		return err
	}
	_, err = flagDuplicates(ctx, tx, r.ws, rec, namespace, value)
	return err
}

// subscription stores a subscription's current state and records its start,
// plan changes and cancellation on the person's timeline.
func (r *paymentRun) subscription(ctx context.Context, tx pgx.Tx, rec string, s connectors.PaymentSubscription) error {
	var prevPrice *string
	var prevMonthly *int64
	err := tx.QueryRow(ctx, `SELECT price_id, monthly_minor FROM payment_subscriptions WHERE integration_id = $1 AND provider_id = $2`,
		r.o.ID, s.ID).Scan(&prevPrice, &prevMonthly)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payment_subscriptions (workspace_id, integration_id, provider, provider_id, record_id, customer_id, status,
			plan, product_id, price_id, billing_interval, monthly_minor, currency, started_at, canceled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (integration_id, provider_id) DO UPDATE SET record_id = EXCLUDED.record_id, status = EXCLUDED.status, plan = EXCLUDED.plan,
			product_id = EXCLUDED.product_id, price_id = EXCLUDED.price_id, billing_interval = EXCLUDED.billing_interval,
			monthly_minor = EXCLUDED.monthly_minor, currency = EXCLUDED.currency, canceled_at = EXCLUDED.canceled_at, updated_at = now()`,
		r.ws, r.o.ID, r.provider, s.ID, rec, s.CustomerID, s.Status, cutUTF8(s.Plan, 200), s.ProductID, s.PriceID, s.Interval,
		s.MonthlyMinor, s.Currency, s.Created, s.CanceledAt); err != nil {
		return err
	}
	props := func() map[string]any {
		return map[string]any{"provider": r.provider, "subscription_id": s.ID, "customer_id": s.CustomerID, "plan": s.Plan,
			"product_id": s.ProductID, "price_id": s.PriceID, "interval": s.Interval, "status": s.Status,
			"mrr": map[string]any{"amount_minor": s.MonthlyMinor, "currency": s.Currency}}
	}
	ev := func(name, msg string, at time.Time, p map[string]any) error {
		_, err := customers.Record(ctx, tx, r.ws, customers.Event{RecordID: rec, Name: name, Source: r.provider, OccurredAt: at,
			MessageID: r.provider + ":subscription:" + s.ID + ":" + msg, RequestID: RequestID(ctx), Properties: p})
		return err
	}
	if err := ev(evSubscriptionStarted, "started", s.Created, props()); err != nil {
		return err
	}
	if prevPrice != nil && s.Status != "canceled" && (*prevPrice != s.PriceID || *prevMonthly != s.MonthlyMinor) {
		p := props()
		p["previous_price_id"] = *prevPrice
		p["previous_mrr"] = map[string]any{"amount_minor": *prevMonthly, "currency": s.Currency}
		if err := ev(evSubscriptionChanged, fmt.Sprintf("changed:%s:%d", s.PriceID, s.MonthlyMinor), time.Now(), p); err != nil {
			return err
		}
	}
	if s.Status == "canceled" {
		at := time.Now()
		if s.CanceledAt != nil {
			at = *s.CanceledAt
		}
		if err := ev(evSubscriptionCanceled, "canceled", at, props()); err != nil {
			return err
		}
	}
	return nil
}

// billingRank orders statuses for a person with several subscriptions:
// the best one is theirs.
var billingRank = []string{"active", "past_due", "trialing", "canceled"}

func (r *paymentRun) recompute(ctx context.Context, tx pgx.Tx, c *catalog, id string) error {
	return recomputeBilling(ctx, tx, r.a, c, r.ws, id)
}

// recomputeBilling works out a record's billing fields from its payments
// and subscriptions on every payment connection. A workspace's own field
// with the same key as one of them is left alone.
func recomputeBilling(ctx context.Context, tx pgx.Tx, a *access, c *catalog, ws, id string) error {
	rec, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM records r WHERE r.id = $1 AND r.workspace_id = $2 FOR UPDATE`, id, ws))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || rec.MergedInto != nil {
		return err
	}
	t := c.recordType(rec.Type)
	if t == nil {
		return nil
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	money := func(amount int64, currency string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"amount_minor": amount, "currency": currency})
		return b
	}
	str := func(s string) json.RawMessage {
		b, _ := json.Marshal(s)
		return b
	}
	null := json.RawMessage("null")
	vals := map[string]json.RawMessage{"lifetime_revenue": null, "customer_since": null, "mrr": null, "billing_status": null, "billing_plan": null}

	// Lifetime revenue in the currency they paid most in.
	var currency string
	var net int64
	err = tx.QueryRow(ctx, `SELECT properties->>'currency', sum(CASE WHEN name = $3 THEN (properties->>'amount_minor')::bigint
			ELSE -(properties->>'amount_minor')::bigint END) AS net
		FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name IN ($3, $4) AND properties ? 'amount_minor' AND properties ? 'provider' AND source <> 'browser'
		GROUP BY 1 ORDER BY 2 DESC LIMIT 1`, ws, id, evPaymentSucceeded, evPaymentRefunded).Scan(&currency, &net)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if currency != "" {
		vals["lifetime_revenue"] = money(max(net, 0), currency)
	}
	var since *time.Time
	if err := tx.QueryRow(ctx, `SELECT min(occurred_at) FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = $3 AND properties ? 'provider' AND source <> 'browser'`,
		ws, id, evPaymentSucceeded).Scan(&since); err != nil {
		return err
	}
	if since != nil {
		vals["customer_since"] = str(since.UTC().Format(time.RFC3339Nano))
	}

	rows, err := tx.Query(ctx, `SELECT status, plan, monthly_minor, currency FROM payment_subscriptions WHERE workspace_id = $1 AND record_id = $2
		ORDER BY started_at DESC`, ws, id)
	if err != nil {
		return err
	}
	type sub struct {
		status, plan, currency string
		monthly                int64
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sub, error) {
		var s sub
		return s, row.Scan(&s.status, &s.plan, &s.monthly, &s.currency)
	})
	if err != nil {
		return err
	}
	if len(subs) > 0 {
		mrr := map[string]int64{}
		best, bestRank, top := "", len(billingRank), int64(-1)
		plan := subs[0].plan // the latest, when none is live
		for _, s := range subs {
			if s.status == "active" || s.status == "past_due" {
				mrr[s.currency] += s.monthly
			}
			if i := slices.Index(billingRank, s.status); i >= 0 && i < bestRank {
				best, bestRank = s.status, i
			}
			if (s.status == "active" || s.status == "past_due" || s.status == "trialing") && s.monthly > top {
				top, plan = s.monthly, s.plan
			}
		}
		cur, amount := "", int64(-1)
		for k, v := range mrr {
			if v > amount || (v == amount && k < cur) {
				cur, amount = k, v
			}
		}
		if cur != "" {
			vals["mrr"] = money(amount, cur)
		} else if subs[0].currency != "" {
			vals["mrr"] = money(0, subs[0].currency)
		}
		if best != "" {
			vals["billing_status"] = str(best)
		}
		if plan != "" {
			vals["billing_plan"] = str(plan)
		}
	}
	for k := range vals {
		if f := t.field(k); f == nil || !f.Builtin || f.WritePolicy != "computed" {
			delete(vals, k)
		}
	}
	if len(vals) == 0 {
		return nil
	}
	changed, err := applyComputed(ctx, tx, c, t, a, &rec, vals, paymentsFirstPartySrc)
	if err != nil || !changed {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
		id, rec.Attributes, rec.AttributeSources)
	return err
}

// cutUTF8 cuts s to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
