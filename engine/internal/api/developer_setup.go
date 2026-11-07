package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Developer setup (docs/developer-setup.md): one brand's live checklist of
// what its developer sends, so they can see at a glance that sign-ups,
// orders and refunds arrive and count, that the website and server copies
// agree, and what to fix when something is off. Refunds whose payment
// isn't known are listed to link by hand.

func (s *Server) routesDeveloperSetup(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/developer-setup", s.onBrand(read, s.getDeveloperSetup))
	mux.HandleFunc("POST /v1/brands/{brand}/transactions/{transaction}/refund-of", s.onBrand(manage, s.linkRefund))
	mux.HandleFunc("POST /v1/brands/{brand}/developer-setup/merge-duplicates", s.onBrand(manage, s.mergeDuplicatePayments))
}

// setupCheck is one line of the checklist.
type setupCheck struct {
	Key string `json:"key"`
	// Status is ok, warning (it works, but something should be fixed) or
	// missing (nothing has arrived yet).
	Status string     `json:"status"`
	Title  string     `json:"title"`
	Detail string     `json:"detail"`
	Fix    string     `json:"fix"`
	LastAt *time.Time `json:"last_at"`
	Count  int        `json:"count"`
}

type developerSetup struct {
	BrandID          string            `json:"brand_id"`
	Checks           []setupCheck      `json:"checks"`
	UnmatchedRefunds []transactionView `json:"unmatched_refunds"`
}

// setupFresh is how recent the last event must be for its check to be ok.
const setupFresh = 7 * 24 * time.Hour

func (s *Server) getDeveloperSetup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	out, err := developerSetupFor(ctx, s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type setupQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func developerSetupFor(ctx context.Context, q setupQuerier, ws, brand string) (developerSetup, error) {
	out := developerSetup{BrandID: brand, Checks: []setupCheck{}, UnmatchedRefunds: []transactionView{}}
	eventsURL := "POST /v1/brands/" + brand + "/events"
	last := func(name string, server bool) (*time.Time, int, error) {
		var at *time.Time
		var n int
		src := `e.source = 'browser'`
		if server {
			src = `e.source <> 'browser' AND COALESCE(e.message_id, '') NOT LIKE 'browser:%'`
		}
		err := q.QueryRow(ctx, `SELECT max(e.received_at), count(*) FILTER (WHERE e.received_at > now() - interval '30 days')::int
			FROM customer_events e WHERE e.workspace_id = $1 AND e.brand_id = $2 AND e.name = $3 AND `+src, ws, brand, name).Scan(&at, &n)
		return at, n, err
	}
	fresh := func(c *setupCheck, at *time.Time, missing string) {
		switch {
		case at == nil:
			c.Status, c.Detail = "missing", missing
		case time.Since(*at) > setupFresh:
			c.Status, c.Detail = "warning", "None in the last 7 days."
		default:
			c.Status = "ok"
		}
		c.LastAt = at
	}

	// Sign-ups from the server.
	at, n, err := last("signed_up", true)
	if err != nil {
		return out, err
	}
	c := setupCheck{Key: "signed_up", Title: "Sign-ups arrive from your server", Count: n,
		Fix: `Send Signed Up from your server when someone signs up: ` + eventsURL + ` with {"type": "track", "event": "Signed Up", "userId": "<your user id>", "messageId": "signup-<your user id>"}.`}
	fresh(&c, at, "No Signed Up event from your server yet.")
	out.Checks = append(out.Checks, c)

	// Orders from the server, and whether they count as payments.
	at, n, err = last("order_completed", true)
	if err != nil {
		return out, err
	}
	c = setupCheck{Key: "order_completed", Title: "Orders arrive and count as payments", Count: n,
		Fix: `Send Order Completed from your server for every payment: ` + eventsURL + ` with {"type": "track", "event": "Order Completed", "userId": "<your user id>", "messageId": "order-<invoice id>", "properties": {"order_id": "<invoice id>", "revenue": 49.00, "currency": "USD"}}, or send the payment as a transaction (POST /v1/brands/` + brand + `/transactions).`}
	fresh(&c, at, "No Order Completed event from your server yet.")
	if at != nil {
		var noMoney, noPerson int
		if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE e.record_id IS NOT NULL AND NOT e.properties ? 'outflare_payment')::int,
				count(*) FILTER (WHERE e.record_id IS NULL)::int
			FROM customer_events e WHERE e.workspace_id = $1 AND e.brand_id = $2 AND e.name = 'order_completed' AND e.source <> 'browser'
			  AND e.received_at > now() - interval '30 days'`, ws, brand).Scan(&noMoney, &noPerson); err != nil {
			return out, err
		}
		switch {
		case noPerson > 0:
			c.Status, c.Detail = "warning", nOf(noPerson, "order", "orders")+" in the last 30 days had no known person (userId), so they aren't payments. Send userId with every order."
		case noMoney > 0:
			c.Status, c.Detail = "warning", nOf(noMoney, "order", "orders")+" in the last 30 days had no revenue above zero, so they aren't payments. Put revenue (in dollars, such as 49.00) and currency in properties."
		}
	}
	out.Checks = append(out.Checks, c)

	// Payments counted, however they arrived.
	var payAt *time.Time
	var payN int
	if err := q.QueryRow(ctx, `SELECT max(occurred_at), count(*) FILTER (WHERE occurred_at > now() - interval '30 days')::int
		FROM transactions WHERE brand_id = $1 AND kind = 'payment' AND status = 'succeeded'`, brand).Scan(&payAt, &payN); err != nil {
		return out, err
	}
	c = setupCheck{Key: "payments", Title: "Payments are counted once", Count: payN, LastAt: payAt,
		Fix: "Send each payment as Order Completed, a transaction, or both, with the same id in order_id and source_id: Outflare keeps it once."}
	fresh(&c, payAt, "No payment yet.")
	if payAt != nil {
		var noPerson int
		if err := q.QueryRow(ctx, `SELECT count(*)::int FROM transactions WHERE brand_id = $1 AND kind = 'payment' AND record_id IS NULL
			AND created_at > now() - interval '30 days'`, brand).Scan(&noPerson); err != nil {
			return out, err
		}
		if noPerson > 0 {
			c.Status, c.Detail = "warning", nOf(noPerson, "payment", "payments")+" in the last 30 days had no person, so they don't count toward revenue or ads. Send user_id with each transaction."
		}
		// Gone quiet: no payment for three times the usual gap between
		// payments (at least two days), in a brand that had five or more.
		if c.Status == "ok" {
			var gap *float64
			if err := q.QueryRow(ctx, `SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY g) FROM (
					SELECT extract(epoch FROM occurred_at - lag(occurred_at) OVER (ORDER BY occurred_at)) AS g
					FROM transactions WHERE brand_id = $1 AND kind = 'payment' AND status = 'succeeded'
					  AND occurred_at > now() - interval '60 days' AND occurred_at <= now()) x
				WHERE g IS NOT NULL HAVING count(*) >= 4`, brand).Scan(&gap); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return out, err
			}
			if gap != nil {
				usual := time.Duration(*gap) * time.Second
				if quiet := max(3*usual, 48*time.Hour); time.Since(*payAt) > quiet {
					c.Status, c.Detail = "warning", "No payment for "+span(time.Since(*payAt))+", when one usually comes every "+span(usual)+". Check that your server still sends them."
				}
			}
		}
	}
	out.Checks = append(out.Checks, c)
	if err := paymentChecks(ctx, q, ws, brand, &out); err != nil {
		return out, err
	}

	// Refunds, and the ones whose payment isn't known.
	rows, err := q.Query(ctx, `SELECT `+transactionColumns+` FROM transactions t
		WHERE t.brand_id = $1 AND t.kind IN ('refund', 'chargeback') AND t.source IN ('api', 'events')
		  AND NOT EXISTS (SELECT 1 FROM transactions p WHERE p.brand_id = t.brand_id AND p.kind = 'payment' AND p.source_id = t.refund_of)
		ORDER BY t.occurred_at DESC, t.id LIMIT 50`, brand)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		v, err := scanTransaction(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.UnmatchedRefunds = append(out.UnmatchedRefunds, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	var refAt *time.Time
	var refN int
	if err := q.QueryRow(ctx, `SELECT max(occurred_at), count(*) FILTER (WHERE occurred_at > now() - interval '30 days')::int
		FROM transactions WHERE brand_id = $1 AND kind IN ('refund', 'chargeback')`, brand).Scan(&refAt, &refN); err != nil {
		return out, err
	}
	c = setupCheck{Key: "refunds", Title: "Refunds find their payment", Count: refN, LastAt: refAt, Status: "ok",
		Fix: `Send a refund as a transaction with refund_of = the payment's order_id (or Order Refunded with order_id): {"kind": "refund", "amount_minor": 4900, "currency": "USD", "source_id": "<refund id>", "refund_of": "<invoice id>", "user_id": "<your user id>"}.`}
	if refAt == nil {
		c.Detail = "No refund yet. That's fine until you have one."
	}
	if k := len(out.UnmatchedRefunds); k > 0 {
		c.Status, c.Detail = "warning", nOf(k, "refund", "refunds")+" name a payment Outflare doesn't have, so they don't count yet. Link each one to its payment below, or check that refund_of is the payment's order_id."
	}
	out.Checks = append(out.Checks, c)

	// The website's copies of sign-ups and orders against the server's.
	var browser, sharedID, byOrder, byGuess, alone int
	if err := q.QueryRow(ctx, `SELECT count(*)::int,
			count(*) FILTER (WHERE m.shared)::int, count(*) FILTER (WHERE NOT m.shared AND m.ordered)::int,
			count(*) FILTER (WHERE NOT m.shared AND NOT m.ordered AND m.guessed)::int,
			count(*) FILTER (WHERE NOT m.shared AND NOT m.ordered AND NOT m.guessed)::int
		FROM customer_events b
		CROSS JOIN LATERAL (SELECT
			EXISTS (SELECT 1 FROM customer_events s WHERE s.workspace_id = b.workspace_id AND s.message_id = substr(b.message_id, 9)) AS shared,
			EXISTS (SELECT 1 FROM customer_events s WHERE s.workspace_id = b.workspace_id AND s.name = b.name AND s.source <> 'browser' AND `+adconv.SameOrder("s", "b")+`) AS ordered,
			EXISTS (SELECT 1 FROM customer_events s WHERE `+adconv.SameEvent("s", "b")+`) AS guessed) m
		WHERE b.workspace_id = $1 AND b.brand_id = $2 AND b.source = 'browser' AND b.name IN ('signed_up', 'order_completed')
		  AND b.received_at > now() - interval '7 days' AND b.received_at < now() - interval '15 minutes'`, ws, brand).
		Scan(&browser, &sharedID, &byOrder, &byGuess, &alone); err != nil {
		return out, err
	}
	c = setupCheck{Key: "website_and_server", Title: "Website and server copies agree", Count: browser, Status: "ok",
		Fix: `Give both copies the same messageId: outflare.track("Signed Up", properties, { messageId: "signup-<your user id>" }) on the website, and "messageId": "signup-<your user id>" from your server. For orders, the same order_id in both is enough.`}
	switch {
	case browser == 0:
		c.Detail = "No sign-ups or orders from the website tag in the last 7 days. That's fine if you only send them from your server."
	case byGuess > 0:
		c.Status = "warning"
		c.Detail = nOf(byGuess, "website event was", "website events were") + " matched to the server's copy only by person and time, because they didn't share a messageId. Each is still counted once, but add the id so nothing is left to chance."
	default:
		c.Detail = nOf(sharedID+byOrder, "website event matches", "website events match") + " the server's copy by id."
		if alone > 0 {
			c.Detail += " " + nOf(alone, "came", "came") + " only from the website, so the website copy is the one counted."
		}
	}
	out.Checks = append(out.Checks, c)

	// Refunds reaching the ad platforms.
	var adjSent, adjFailed int
	var adjReason string
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'sent')::int, count(*) FILTER (WHERE status = 'failed')::int,
			COALESCE((SELECT reason FROM ad_conversion_adjustments WHERE brand_id = $1 AND status = 'failed' ORDER BY updated_at DESC LIMIT 1), '')
		FROM ad_conversion_adjustments WHERE brand_id = $1 AND created_at > now() - interval '30 days'`, brand).Scan(&adjSent, &adjFailed, &adjReason); err != nil {
		return out, err
	}
	if adjSent+adjFailed > 0 {
		c = setupCheck{Key: "refunds_to_ads", Title: "Refunds lower the conversions sent to ad networks", Count: adjSent, Status: "ok",
			Fix: "In Google Ads, check that the conversion action takes adjustments and that the conversion was sent with its order id."}
		if adjFailed > 0 {
			c.Status = "warning"
			c.Detail = nOf(adjFailed, "refund", "refunds") + " couldn't be sent to the ad network. Its last answer: " + adjReason
		}
		out.Checks = append(out.Checks, c)
	}
	return out, nil
}

// paymentChecks adds the checks that payments sent from the brand's
// server are kept once and as sent: orders carry their payment's id, the
// two copies of a payment share an id and an amount, no payment is saved
// twice, each charge has its own id, a retry keeps its id, and amounts are
// in the currency's minor unit (docs/developer-setup.md).
func paymentChecks(ctx context.Context, q setupQuerier, ws, brand string, out *developerSetup) error {
	transactionsURL := "POST /v1/brands/" + brand + "/transactions"

	// Orders without an id can only be matched by a guess.
	var orders, noID int
	if err := q.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE NOT (e.properties ?| array['order_id', 'orderId', 'transaction_id', 'payment_id']))::int
		FROM customer_events e WHERE e.workspace_id = $1 AND e.brand_id = $2 AND e.name = 'order_completed' AND e.source <> 'browser'
		  AND COALESCE(e.message_id, '') NOT LIKE 'browser:%' AND e.received_at > now() - interval '30 days'`, ws, brand).Scan(&orders, &noID); err != nil {
		return err
	}
	c := setupCheck{Key: "order_ids", Title: "Orders carry their payment's id", Count: orders, Status: "ok",
		Fix: `Put your invoice id in order_id on every Order Completed, the same id you send as source_id on its transaction: {"type": "track", "event": "Order Completed", "userId": "<your user id>", "properties": {"order_id": "<invoice id>", "revenue": 49.00, "currency": "USD"}}.`}
	switch {
	case orders == 0:
		c.Detail = "No orders from your server in the last 30 days. That's fine if you send payments only as transactions."
	case noID > 0:
		c.Status, c.Detail = "warning", nOf(noID, "order", "orders")+" in the last 30 days had no order_id, so Outflare can only match them to their transaction by a guess (the same person and amount within 15 minutes)."
	}
	out.Checks = append(out.Checks, c)

	// Payments sent both ways: joined by id or by a guess, and whether the
	// two copies agreed on the amount.
	var merged, guessed, differ int
	if err := q.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE data->>'matched_by' = 'guess')::int,
			count(*) FILTER (WHERE (data->>'amounts_differ')::boolean)::int
		FROM audit_events WHERE workspace_id = $1 AND action = 'transaction.merged' AND data->>'brand_id' = $2
		  AND data->>'kind' = 'payment' AND created_at > now() - interval '30 days'`, ws, brand).Scan(&merged, &guessed, &differ); err != nil {
		return err
	}
	c = setupCheck{Key: "matched_by_id", Title: "Payments sent two ways share an id and an amount", Count: merged, Status: "ok",
		Fix: `Send the same id as order_id on Order Completed and as source_id on the transaction, and the same amount: revenue 49.00 on the event is amount_minor 4900 (cents) on the transaction.`}
	switch {
	case merged == 0:
		c.Detail = "No payment arrived both as an order and as a transaction in the last 30 days."
	case differ > 0:
		c.Status, c.Detail = "warning", nOf(differ, "payment", "payments")+" arrived with a different amount or currency as an order and as a transaction, and the first one kept its amount. Usually amount_minor was sent in dollars (49) instead of cents (4900)."
	case guessed > 0:
		c.Status, c.Detail = "warning", nOf(guessed, "payment was", "payments were")+" matched to their other copy only by a guess (the same person, amount and currency within 15 minutes), because the ids differed. A guess can miss, or join two real payments, so send the same id both ways."
	default:
		c.Detail = nOf(merged, "payment", "payments") + " sent both ways matched by id."
	}
	out.Checks = append(out.Checks, c)

	// Payments saved twice (before both ways took turns on an id).
	var twice int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM (`+duplicateGroups+`) d`, brand).Scan(&twice); err != nil {
		return err
	}
	c = setupCheck{Key: "duplicates", Title: "No payment is saved twice", Count: twice, Status: "ok",
		Fix: "Merge them: each is kept once, with its person, and the extra copy goes to Recently deleted for 7 days."}
	if twice > 0 {
		c.Status, c.Detail = "warning", nOf(twice, "payment or refund was", "payments or refunds were")+" saved twice, once from an order event and once as a transaction with the same id, so they show twice on the Transactions page. Nothing needs to change in what you send: merge them here."
	}
	out.Checks = append(out.Checks, c)

	// The same id sent again a day or more later.
	var resent int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM audit_events WHERE workspace_id = $1 AND action = 'transaction.resent'
		AND data->>'brand_id' = $2 AND created_at > now() - interval '30 days'`, ws, brand).Scan(&resent); err != nil {
		return err
	}
	c = setupCheck{Key: "ids_reused", Title: "Each charge has its own id", Count: resent, Status: "ok",
		Fix: `Send each charge's own id (its invoice id) as source_id and order_id, never a subscription or customer id: every renewal is a new payment with a new id. ` + transactionsURL + ` with {"kind": "payment", "amount_minor": 4900, "currency": "USD", "source_id": "<invoice id>", "user_id": "<your user id>"}.`}
	if resent > 0 {
		c.Status, c.Detail = "warning", nOf(resent, "payment id was", "payment ids were")+" sent again a day or more after the payment they first named, so Outflare kept the first and left the new one out. Usually that's a subscription id sent for every renewal: each renewal after the first is missing from revenue."
	}
	out.Checks = append(out.Checks, c)

	// The same payment under two ids from the same source: a retry that
	// made a new id.
	var retried int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM transactions a
		WHERE a.brand_id = $1 AND a.kind = 'payment' AND a.source IN ('api', 'events') AND a.record_id IS NOT NULL
		  AND a.created_at > now() - interval '30 days'
		  AND EXISTS (SELECT 1 FROM transactions b WHERE b.brand_id = a.brand_id AND b.kind = 'payment' AND b.source = a.source
			AND b.record_id = a.record_id AND b.amount_minor = a.amount_minor AND b.currency = a.currency AND b.source_id <> a.source_id
			AND b.created_at BETWEEN a.created_at - interval '10 minutes' AND a.created_at AND b.id <> a.id
			AND b.occurred_at BETWEEN a.occurred_at - interval '10 minutes' AND a.occurred_at + interval '10 minutes')`, brand).Scan(&retried); err != nil {
		return err
	}
	c = setupCheck{Key: "retries", Title: "A retry keeps its payment's id", Count: retried, Status: "ok",
		Fix: "Make the id when the payment happens (your invoice id) and send that same id on every retry: Outflare keeps one payment per id, so a retry with the same id is never counted twice."}
	if retried > 0 {
		c.Status, c.Detail = "warning", nOf(retried, "payment", "payments")+" in the last 30 days came minutes after another from the same person, of the same amount, under a different id. If those were retries of one payment, each is counted twice: delete the extra ones on the Transactions page and keep the id on retries. If they were real separate payments, nothing needs to change."
	}
	out.Checks = append(out.Checks, c)

	// Amounts far below the brand's usual: dollars sent as cents.
	var small, sample int
	if err := q.QueryRow(ctx, `WITH recent AS (
			SELECT currency, amount_minor FROM transactions WHERE brand_id = $1 AND kind = 'payment' AND status = 'succeeded'
			  AND source IN ('api', 'events') AND amount_minor > 0 AND occurred_at > now() - interval '30 days'),
		usual AS (SELECT currency, percentile_cont(0.5) WITHIN GROUP (ORDER BY amount_minor) AS mid, count(*) AS n FROM recent GROUP BY currency)
		SELECT COALESCE(sum(CASE WHEN u.n >= 5 AND u.mid >= 1000 AND r.amount_minor * 50 <= u.mid THEN 1 ELSE 0 END), 0)::int, count(*)::int
		FROM recent r JOIN usual u USING (currency)`, brand).Scan(&small, &sample); err != nil {
		return err
	}
	c = setupCheck{Key: "amounts", Title: "Amounts are in cents", Count: sample, Status: "ok",
		Fix: `amount_minor is in the currency's smallest unit: $49.00 is 4900, ¥4900 is 4900. On Order Completed, revenue is in dollars (49.00).`}
	if small > 0 {
		c.Status, c.Detail = "warning", nOf(small, "payment", "payments")+" in the last 30 days were 50 or more times smaller than this brand's usual payment, which usually means dollars were sent as amount_minor (49 instead of 4900). Fix the sender, then delete and send those payments again with the right amount."
	}
	out.Checks = append(out.Checks, c)
	return nil
}

// duplicateGroups are a brand's payments and refunds ($1) kept twice: one
// row from an order event and one from the API with the same id, saved at
// the same moment before both ways took turns on an id. One row per id.
const duplicateGroups = `SELECT CASE WHEN t.kind = 'payment' THEN 'payment' ELSE 'refund' END AS class, t.source_id
	FROM transactions t WHERE t.brand_id = $1 AND t.source IN ('api', 'events')
	GROUP BY 1, 2 HAVING count(DISTINCT t.source) > 1`

// span is a duration in words: "3 days" or "5 hours".
func span(d time.Duration) string {
	if d >= 48*time.Hour {
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	}
	if h := int(d / time.Hour); h >= 1 {
		return nOf(h, "hour", "hours")
	}
	return nOf(max(int(d/time.Minute), 1), "minute", "minutes")
}

// nOf is "1 order" or "3 orders".
func nOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// linkRefund names the payment a refund gives back, for a refund that
// named one Outflare doesn't have: by any id the payment has (findPayment).
// The refund then counts, and comes off that payment and its conversion.
func (s *Server) linkRefund(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Payment string `json:"payment"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Payment = strings.TrimSpace(in.Payment)
	if in.Payment == "" || len(in.Payment) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "payment names the payment this refund gives back: its order_id or source_id, its order's messageId, or Outflare's transaction id")
		return
	}
	start := time.Now()
	var v transactionView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var kind string
		var amount int64
		var old *string
		if err := tx.QueryRow(ctx, `SELECT kind, amount_minor, refund_of FROM transactions WHERE id = $1 AND brand_id = $2 FOR UPDATE`,
			validOrNil(r.PathValue("transaction")), a.Brand).Scan(&kind, &amount, &old); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if kind != "refund" && kind != "chargeback" {
			return errInvalid("only a refund or chargeback gives a payment back")
		}
		paid, found, err := findPayment(ctx, tx, a.WorkspaceID, a.Brand, in.Payment)
		if err != nil {
			return err
		}
		if !found {
			return errInvalid("no payment of this brand has that id: use its order_id or source_id, its order's messageId, or Outflare's transaction id")
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions SET refund_of = $2, amount_minor = CASE WHEN amount_minor = 0 THEN $3 ELSE amount_minor END,
			brand_amount_minor = CASE WHEN amount_minor = 0 THEN $3 ELSE brand_amount_minor END, updated_at = now() WHERE id = $1`,
			r.PathValue("transaction"), paid.sourceID, paid.amountMinor); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "transaction.refund_linked", "transaction", r.PathValue("transaction"),
			map[string]any{"brand_id": a.Brand, "payment_id": paid.id, "refund_of": paid.sourceID, "from_refund_of": old}); err != nil {
			return err
		}
		if v, err = scanTransaction(tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM transactions t WHERE t.id = $1`, r.PathValue("transaction"))); err != nil {
			return err
		}
		_, err = countPayment(ctx, tx, a.WorkspaceID, v)
		return err
	})
	outcome := "linked"
	if err != nil {
		outcome = "error"
		if errors.As(err, new(errInvalid)) {
			outcome = "invalid"
		}
	}
	metrics.TransactionSaved("link", outcome, time.Since(start))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// mergeDuplicatePayments keeps each payment or refund the brand has twice
// (an order event's row and the API's with the same id, duplicateGroups)
// once: the copy already counted (else the one with a person, else the
// first) stays, takes the other's person when it has none, and counts; the
// other goes to Recently deleted, restorable for 7 days. Its partner sale,
// if any, belongs to the copy kept, so none is taken back.
func (s *Server) mergeDuplicatePayments(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if !decodeOptional(w, r, &struct{}{}) {
		return
	}
	start := time.Now()
	merged := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, duplicateGroups, a.Brand)
		if err != nil {
			return err
		}
		type group struct{ class, sourceID string }
		groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (group, error) {
			var g group
			return g, row.Scan(&g.class, &g.sourceID)
		})
		if err != nil {
			return err
		}
		billing := map[string]bool{}
		for _, g := range groups {
			n, rec, err := mergeDuplicate(ctx, tx, a, g.class, g.sourceID)
			if err != nil {
				return err
			}
			merged += n
			if rec != "" {
				billing[rec] = true
			}
		}
		for rec := range billing {
			if err := quietBilling(ctx, tx, a.WorkspaceID, rec); err != nil {
				return err
			}
		}
		return nil
	})
	outcome := "merged"
	if err != nil {
		outcome = "error"
	}
	metrics.TransactionSaved("duplicates", outcome, time.Since(start))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"merged": merged})
}

// mergeDuplicate keeps one row of the payment or refund (class) with
// sourceID, as mergeDuplicatePayments says. It returns how many copies it
// removed and the person of the one kept.
func mergeDuplicate(ctx context.Context, tx pgx.Tx, a *access, class, sourceID string) (int, string, error) {
	if err := lockTransaction(ctx, tx, a.Brand, class, sourceID); err != nil {
		return 0, "", err
	}
	kinds := []string{"payment"}
	if class != "payment" {
		kinds = []string{"refund", "chargeback"}
	}
	var counted string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT properties->>'transaction_id' FROM customer_events WHERE workspace_id = $1 AND message_id = $2 LIMIT 1), '')`,
		a.WorkspaceID, paymentMessageID(a.Brand, "api", class, sourceID)).Scan(&counted); err != nil {
		return 0, "", err
	}
	rows, err := tx.Query(ctx, `SELECT `+transactionColumns+` FROM transactions t
		WHERE t.brand_id = $1 AND t.workspace_id = $2 AND t.source IN ('api', 'events') AND t.source_id = $3 AND t.kind = ANY($4)
		ORDER BY t.id::text = $5 DESC, t.record_id IS NOT NULL DESC, t.created_at, t.id FOR UPDATE`, a.Brand, a.WorkspaceID, sourceID, kinds, counted)
	if err != nil {
		return 0, "", err
	}
	copies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (transactionView, error) { return scanTransaction(row) })
	if err != nil || len(copies) < 2 {
		return 0, "", err
	}
	keep := copies[0]
	for _, gone := range copies[1:] {
		if keep.RecordID == nil && gone.RecordID != nil {
			if _, err := tx.Exec(ctx, `UPDATE transactions SET record_id = $2, updated_at = now() WHERE id = $1`, keep.ID, *gone.RecordID); err != nil {
				return 0, "", err
			}
			keep.RecordID = gone.RecordID
		}
		d, err := startDeletion(ctx, tx, a, deletedTransaction, gone.RecordID, gone.ID)
		if err != nil {
			return 0, "", err
		}
		if _, err := d.move(ctx, tx, "transactions", `gone.record_id`, `t.workspace_id = $1 AND t.id = $2`, d.ws, gone.ID); err != nil {
			return 0, "", err
		}
		if err := d.finish(ctx, tx); err != nil {
			return 0, "", err
		}
		metrics.RecoveryDeleted(deletedTransaction)
		data := map[string]any{"kind": deletedTransaction, "transaction_kind": gone.Kind, "source": gone.Source, "merged_into": keep.ID}
		if gone.RecordID != nil {
			data["record_id"] = *gone.RecordID
		}
		if err := recordDeleted(ctx, tx, a, "transaction.deleted", "transaction", gone.ID, d, data); err != nil {
			return 0, "", err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "transaction.merged", "transaction", keep.ID, map[string]any{"brand_id": a.Brand,
			"kind": keep.Kind, "matched_by": "duplicate", "source": gone.Source, "source_id": gone.SourceID, "amount_minor": gone.AmountMinor,
			"currency": gone.Currency, "kept_source": keep.Source, "kept_source_id": keep.SourceID, "kept_amount_minor": keep.AmountMinor,
			"kept_currency": keep.Currency, "amounts_differ": gone.AmountMinor != keep.AmountMinor || gone.Currency != keep.Currency,
			"deletion_id": d.id}); err != nil {
			return 0, "", err
		}
	}
	v, err := scanTransaction(tx.QueryRow(ctx, `SELECT `+transactionColumns+` FROM transactions t WHERE t.id = $1`, keep.ID))
	if err != nil {
		return 0, "", err
	}
	if _, err := countPayment(ctx, tx, a.WorkspaceID, v); err != nil {
		return 0, "", err
	}
	rec := ""
	if v.RecordID != nil {
		rec = *v.RecordID
	}
	return len(copies) - 1, rec, nil
}
