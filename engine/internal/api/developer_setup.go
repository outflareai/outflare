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
	}
	out.Checks = append(out.Checks, c)

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
