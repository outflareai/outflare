package adconv

import (
	"context"
	"fmt"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/conversions"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// adjustAttempts is how often a refused adjustment is tried: a platform
	// may not know a conversion for hours after it took it.
	adjustAttempts = 8
	// adjustBackoff is how long the next try waits, times the tries so far.
	adjustBackoff = time.Hour
)

// MatchAdjustments queues, for each refund (a payment.refunded event) of a
// payment whose conversion was sent, a change to that conversion: its value
// restated to what is left of the payment after every refund so far, in
// proportion to the value sent, or retracted when nothing is left. A
// conversion sent without a value can only be retracted. The conversion is
// the payment's payment.succeeded, or its order event (the same order_id,
// or outflare_payment naming it). ws "" matches every workspace.
func MatchAdjustments(ctx context.Context, q conversions.Querier, ws string) (int64, error) {
	tag, err := q.Exec(ctx, `
		INSERT INTO ad_conversion_adjustments (workspace_id, brand_id, send_id, refund_event_id, kind, value_micros, currency, occurred_at)
		SELECT x.workspace_id, p.brand_id, x.id, f.id,
			CASE WHEN p.amount_minor <= rf.refunded THEN 'retraction' ELSE 'restatement' END,
			CASE WHEN p.amount_minor > rf.refunded AND x.value_micros IS NOT NULL
				THEN round(x.value_micros::numeric * (p.amount_minor - rf.refunded) / p.amount_minor)::bigint END,
			x.currency, f.occurred_at
		FROM customer_events f
		JOIN transactions p ON p.brand_id = f.brand_id AND p.kind = 'payment' AND p.source_id = f.properties->>'payment_id'
		CROSS JOIN LATERAL (SELECT COALESCE(sum(CASE WHEN (r.properties->>'amount_minor') ~ '^[0-9]+$' THEN (r.properties->>'amount_minor')::bigint END), 0) AS refunded
			FROM customer_events r WHERE r.workspace_id = f.workspace_id AND r.brand_id = f.brand_id AND r.name = 'payment.refunded'
			  AND r.properties->>'payment_id' = p.source_id AND (r.occurred_at, r.id) <= (f.occurred_at, f.id)) rf
		JOIN customer_events o ON o.workspace_id = f.workspace_id AND o.brand_id = p.brand_id
			AND (o.properties->>'payment_id' = p.source_id OR o.properties->>'order_id' = p.source_id
				OR o.properties->>'outflare_payment' IN ('payment:' || p.brand_id::text || ':' || p.source_id, p.source || ':charge:' || p.source_id))
		JOIN ad_conversion_sends x ON x.event_row_id = o.id AND x.workspace_id = f.workspace_id AND x.status = 'sent'
		WHERE f.name = 'payment.refunded' AND f.received_at > now() - interval '30 days'
		  AND ($1 = '' OR f.workspace_id::text = $1)
		  AND (x.value_micros IS NOT NULL OR p.amount_minor <= rf.refunded)
		ON CONFLICT (send_id, refund_event_id) DO NOTHING`, ws)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

type adjustment struct {
	id, eventID, kind, currency string
	value                       *int64
	at, convAt                  time.Time
	attempts                    int
	// sendID is the conversion sent; transport the path it took, which
	// decides how a restatement goes back (adjust_feed.go).
	sendID, transport string
}

// AdjustRule sends a rule's adjustments that are due. A platform that
// can't change a conversion after taking it skips them, saying so.
func (s *Sender) AdjustRule(ctx context.Context, ws, ruleID string) (Result, error) {
	var res Result
	rows, err := s.Pool.Query(ctx, `SELECT a.id::text, x.event_id, a.kind, a.currency, a.value_micros, a.occurred_at, x.occurred_at, a.attempts, x.id::text, x.transport
		FROM ad_conversion_adjustments a JOIN ad_conversion_sends x ON x.id = a.send_id
		WHERE a.workspace_id = $1 AND x.rule_id = $2 AND a.status = 'queued' AND a.next_attempt_at <= now()
		ORDER BY a.occurred_at, a.created_at LIMIT 500`, ws, ruleID)
	if err != nil {
		return res, dbError{err}
	}
	var list []adjustment
	for rows.Next() {
		var a adjustment
		if err := rows.Scan(&a.id, &a.eventID, &a.kind, &a.currency, &a.value, &a.at, &a.convAt, &a.attempts, &a.sendID, &a.transport); err != nil {
			rows.Close()
			return res, dbError{err}
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, dbError{err}
	}
	if len(list) == 0 {
		return res, nil
	}
	r, err := s.loadRule(ctx, ws, ruleID)
	if err != nil {
		return res, err
	}
	res.Connector = r.connector
	o, err := s.Store.Open(ctx, s.Pool, ws, r.integrationID)
	if err != nil {
		return res, err
	}
	// Restatements of conversions that went through the data feed go
	// back the same way; the rest, and every retraction, as adjustments.
	feeder, hasFeed := o.Connector.(connectors.FeedSender)
	var restate, rest []adjustment
	for _, a := range list {
		if viaFeed(a, hasFeed) {
			restate = append(restate, a)
		} else {
			rest = append(rest, a)
		}
	}
	if len(restate) > 0 {
		if err := s.restateViaFeed(ctx, ws, o, feeder, r, restate, &res); err != nil {
			return res, err
		}
	}
	list = rest
	if len(list) == 0 {
		return res, nil
	}
	adj, ok := o.Connector.(connectors.ConversionAdjuster)
	if !ok {
		reason := o.Connector.Definition().Name + " can't change a conversion after it was sent, so refunds aren't sent to it"
		for _, a := range list {
			if err := s.markAdjustment(ctx, a.id, "skipped", reason); err != nil {
				return res, dbError{err}
			}
			res.Skipped++
		}
		return res, nil
	}
	adjs := make([]connectors.AdConversionAdjustment, len(list))
	for i, a := range list {
		at := a.at
		if !at.After(a.convAt) {
			at = a.convAt.Add(time.Second)
		}
		adjs[i] = connectors.AdConversionAdjustment{EventID: a.eventID, Retract: a.kind == "retraction", Currency: a.currency, Time: at, Index: i}
		if a.value != nil {
			adjs[i].ValueMicros = *a.value
		}
	}
	target := connectors.ConversionTarget{ID: r.targetID, Name: r.targetName}
	results, err := adj.AdjustConversions(ctx, o.Client, o.Creds, r.acct, target, adjs)
	if err != nil {
		for _, a := range list {
			if failed, e := s.retryAdjustment(ctx, a, err.Error()); e != nil {
				return res, dbError{e}
			} else if failed {
				res.Failed++
			}
		}
		return res, err
	}
	answered := map[int]bool{}
	for _, cr := range results {
		if cr.Index < 0 || cr.Index >= len(list) {
			continue
		}
		answered[cr.Index] = true
		a := list[cr.Index]
		if cr.OK {
			if err := s.markAdjustment(ctx, a.id, "sent", ""); err != nil {
				return res, dbError{err}
			}
			res.Sent++
			continue
		}
		failed, err := s.retryAdjustment(ctx, a, cr.Reason)
		if err != nil {
			return res, dbError{err}
		}
		if failed {
			res.Failed++
		}
	}
	for i, a := range list {
		if !answered[i] {
			if failed, err := s.retryAdjustment(ctx, a, "the platform gave no answer for it"); err != nil {
				return res, dbError{err}
			} else if failed {
				res.Failed++
			}
		}
	}
	return res, nil
}

// retryAdjustment tries a refused adjustment again later, or fails it after
// adjustAttempts. failed says it gave up.
func (s *Sender) retryAdjustment(ctx context.Context, a adjustment, reason string) (bool, error) {
	if a.attempts+1 >= adjustAttempts {
		return true, s.markAdjustment(ctx, a.id, "failed", reason)
	}
	_, err := s.Pool.Exec(ctx, `UPDATE ad_conversion_adjustments SET attempts = attempts + 1, reason = $2,
		next_attempt_at = now() + $3::interval, updated_at = now() WHERE id = $1`,
		a.id, trim(reason, 500), fmt.Sprintf("%d seconds", int(adjustBackoff.Seconds())*(a.attempts+1)))
	return false, err
}

func (s *Sender) markAdjustment(ctx context.Context, id, status, reason string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE ad_conversion_adjustments SET status = $2, reason = $3, updated_at = now(),
		sent_at = CASE WHEN $2 = 'sent' THEN now() END WHERE id = $1`, id, status, trim(reason, 500))
	return err
}

// adjustJob sends a rule's due adjustments as part of its send job, and
// records what it did.
func (s *Sender) adjustJob(ctx context.Context, ws, ruleID string) {
	res, err := s.AdjustRule(ctx, ws, ruleID)
	if res.Connector != "" {
		metrics.AdConversionAdjustments(res.Connector, res.Sent, res.Skipped, res.Failed)
	}
	ev := track.Event{Name: "ad_conversions.adjusted", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"rule_id": ruleID, "connector": res.Connector, "sent": res.Sent, "skipped": res.Skipped, "failed": res.Failed}}
	if err != nil {
		code := "internal"
		if _, isDB := err.(dbError); !isDB {
			code = "connector_" + connectors.KindOf(err)
		}
		track.Fail(ctx, s.Log, ev, code, err)
		return
	}
	if res.Sent+res.Skipped+res.Failed > 0 {
		track.Emit(ctx, ev)
	}
}
