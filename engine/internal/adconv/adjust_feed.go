package adconv

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Refunds for conversions that went through the data feed: Google's Data
// Manager API treats a later event with the same transactionId as an
// adjustment of the conversion, so a partial refund re-sends the event
// with what is left of its value (a restatement). The API has no
// documented way to take a conversion away, so a full refund (a
// retraction) still goes through the Ads API's conversion adjustments,
// found by the order id the conversion was sent with, as for conversions
// sent the older way (adjust.go).

// viaFeed says whether an adjustment is sent as a restated event.
func viaFeed(a adjustment, hasFeed bool) bool {
	return hasFeed && a.kind == "restatement" && a.transport == connectors.TransportDataManager && a.value != nil
}

// restateViaFeed re-sends each adjustment's conversion with its new value,
// built as it was first sent (click ids, hashed identifiers, consent as of
// now) and marks how it went. A conversion that may no longer be sent (the
// person withdrew consent) is skipped with the reason.
func (s *Sender) restateViaFeed(ctx context.Context, ws string, o *connectors.Opened, feeder connectors.FeedSender, r rule, list []adjustment, res *Result) error {
	mode, err := s.consentMode(ctx, ws, r.brand)
	if err != nil {
		return dbError{err}
	}
	var convs []connectors.AdConversion
	byIndex := map[int]adjustment{}
	for i, a := range list {
		q, err := s.sentEvent(ctx, ws, a.sendID)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := s.markAdjustment(ctx, a.id, "skipped", "the conversion's event is gone"); err != nil {
				return dbError{err}
			}
			res.Skipped++
			continue
		}
		if err != nil {
			return dbError{err}
		}
		cv, reason, err := s.build(ctx, ws, r, mode, q)
		if err != nil {
			return dbError{err}
		}
		if reason != "" {
			if err := s.markAdjustment(ctx, a.id, "skipped", reason); err != nil {
				return dbError{err}
			}
			res.Skipped++
			continue
		}
		v := *a.value
		cv.ValueMicros, cv.Currency = &v, a.currency
		cv.Restates, cv.UpdatedAt, cv.Index = true, a.at, i
		byIndex[i] = a
		convs = append(convs, cv)
	}
	if len(convs) == 0 {
		return nil
	}
	out, err := feeder.SendConversionsFeed(ctx, o.Client, o.Creds, r.acct, connectors.ConversionTarget{ID: r.targetID, Name: r.targetName}, convs)
	if err != nil {
		for _, a := range byIndex {
			if failed, e := s.retryAdjustment(ctx, a, err.Error()); e != nil {
				return dbError{e}
			} else if failed {
				res.Failed++
			}
		}
		return err
	}
	for _, cr := range out.Results {
		a, ok := byIndex[cr.Index]
		if !ok {
			continue
		}
		delete(byIndex, cr.Index)
		switch {
		case cr.OK:
			if err := s.markAdjustment(ctx, a.id, "sent", ""); err != nil {
				return dbError{err}
			}
			res.Sent++
		case cr.Skipped:
			if err := s.markAdjustment(ctx, a.id, "skipped", cr.Reason); err != nil {
				return dbError{err}
			}
			res.Skipped++
		default:
			failed, err := s.retryAdjustment(ctx, a, cr.Reason)
			if err != nil {
				return dbError{err}
			}
			if failed {
				res.Failed++
			}
		}
	}
	for _, a := range byIndex {
		if failed, err := s.retryAdjustment(ctx, a, "the platform gave no answer for it"); err != nil {
			return dbError{err}
		} else if failed {
			res.Failed++
		}
	}
	return nil
}

// sentEvent reads a sent conversion's event as the sender saw it.
func (s *Sender) sentEvent(ctx context.Context, ws, sendID string) (queued, error) {
	var q queued
	var props, evCtx []byte
	err := s.Pool.QueryRow(ctx, `SELECT q.id, q.event_id, q.record_id, e.anonymous_id, q.occurred_at, e.properties, e.context, q.attempts
		FROM ad_conversion_sends q JOIN customer_events e ON e.id = q.event_row_id
		WHERE q.workspace_id = $1 AND q.id = $2`, ws, sendID).Scan(&q.id, &q.eventID, &q.recordID, &q.anonID, &q.occurred, &props, &evCtx, &q.attempts)
	if err != nil {
		return q, err
	}
	_ = json.Unmarshal(props, &q.props)
	var c map[string]any
	_ = json.Unmarshal(evCtx, &c)
	q.country = eventCountry(c)
	return q, nil
}
