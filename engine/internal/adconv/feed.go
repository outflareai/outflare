package adconv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The conversion feed (plans/paid-ads/plan.md, "Conversion feed"): three
// conversion actions Outflare makes on an ad account, each fed by a rule
// with a feed_role, sent through the platform's data feed (Google's Data
// Manager API) with the person's customer type, and a Customer Match list
// of paying customers (payers.go) the platform's new customer goal reads
// as existing customers. What was made on the account lives in
// ad_accounts.feed (Feed).

// Feed roles of a conversion rule.
const (
	RoleCustom        = "custom"
	RoleSignup        = "signup"
	RoleFirstPayment  = "first_payment"
	RoleRepeatPayment = "repeat_payment"
)

// Customer types a rule sends: a fixed one, or auto, worked out per event
// from the person's earlier payments.
const CustomerAuto = "auto"

// ActionNames are the conversion actions Outflare makes, by role.
var ActionNames = map[string]string{
	RoleSignup:        "Outflare sign-up",
	RoleFirstPayment:  "Outflare first payment",
	RoleRepeatPayment: "Outflare repeat payment",
}

// PayersListName is the Customer Match list of paying customers.
const PayersListName = "Outflare paying customers"

// Feed is what ad_accounts.feed holds: the transport sends take, what
// Outflare made on the account, and the new customer goal.
type Feed struct {
	// Transport is data_manager (the default when empty) or ads_api, set
	// when the data feed refused the destination (TransportReason says
	// why) so sends fall back to the platform's older upload call.
	Transport       string     `json:"transport,omitempty"`
	TransportReason string     `json:"transport_reason,omitempty"`
	TransportSetAt  *time.Time `json:"transport_set_at,omitempty"`
	// Actions are the conversion actions by role (signup, first_payment,
	// repeat_payment).
	Actions map[string]FeedAction `json:"actions,omitempty"`
	// PayersList is the Customer Match list of paying customers.
	PayersList *PayersList `json:"payers_list,omitempty"`
	// NCA is the new customer acquisition goal as set through Outflare.
	NCA *NCASetting `json:"nca,omitempty"`
	// CreatedAt is when the feed was set up.
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// FeedAction is one conversion action on the platform.
type FeedAction struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// PayersList is the Customer Match list and its last refresh.
type PayersList struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Members     int        `json:"members"`
	Removed     int        `json:"removed"`
	RefreshedAt *time.Time `json:"refreshed_at"`
	RequestID   string     `json:"request_id,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// NCASetting is the new customer acquisition goal: off, bid_higher or
// new_only, with the value added to a new customer's first conversion.
type NCASetting struct {
	Mode                  string    `json:"mode"`
	AdditionalValueMicros int64     `json:"additional_value_micros"`
	SetAt                 time.Time `json:"set_at"`
}

// Ready says the feed's conversion actions were made.
func (f Feed) Ready() bool { return len(f.Actions) > 0 }

// LoadFeed reads an account's feed.
func LoadFeed(ctx context.Context, q jobs.Querier, ws, accountID string) (Feed, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT feed FROM ad_accounts WHERE workspace_id = $1 AND id = $2`, ws, accountID).Scan(&raw)
	if err != nil {
		return Feed{}, err
	}
	var f Feed
	_ = json.Unmarshal(raw, &f)
	return f, nil
}

// SaveFeed writes an account's feed whole.
func SaveFeed(ctx context.Context, q jobs.Querier, ws, accountID string, f Feed) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE ad_accounts SET feed = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, accountID, raw)
	return err
}

// FeedRules are the rules the feed makes, by role: the event each sends,
// the customer type and the value it carries.
type FeedRule struct {
	Role, Event, CustomerType, ValueProperty, ValueUnit string
}

// Rules are the three feed rules; the sign-up rule may name the brand's
// sign-up conversion definition instead of the bare event (the API decides
// that).
var Rules = []FeedRule{
	{Role: RoleSignup, Event: "signed_up", ValueUnit: "major"},
	{Role: RoleFirstPayment, Event: "payment.succeeded", CustomerType: connectors.CustomerNew, ValueProperty: "amount_minor", ValueUnit: "minor"},
	{Role: RoleRepeatPayment, Event: "payment.succeeded", CustomerType: connectors.CustomerReturning, ValueProperty: "amount_minor", ValueUnit: "minor"},
}

// firstPaymentSQL is the condition that event e (a payment.succeeded with
// a record) is the person's first payment in its brand: no earlier
// payment.succeeded of theirs there. When the paid ads ledger exists
// (ad_revenue, the core workstream's), its bucket decides for the events
// it has recorded, and this computes the rest.
func firstPaymentSQL(ledger bool) string {
	computed := `NOT EXISTS (SELECT 1 FROM customer_events p WHERE p.workspace_id = e.workspace_id AND p.record_id = e.record_id
		AND p.kind = 'track' AND p.name = 'payment.succeeded' AND p.brand_id IS NOT DISTINCT FROM e.brand_id
		AND (p.occurred_at, p.id) < (e.occurred_at, e.id))`
	if !ledger {
		return computed
	}
	return `COALESCE((SELECT v.bucket = 'new' FROM ad_revenue v WHERE v.event_id = e.id), ` + computed + `)`
}

// roleFilterSQL keeps, for a first_payment rule, only first payments, and
// for a repeat_payment rule only later ones; other roles take every event
// of their name. A payment with no record cannot be told apart and goes to
// neither.
func roleFilterSQL(ledger bool) string {
	return `(r.feed_role NOT IN ('first_payment', 'repeat_payment')
		OR (e.record_id IS NOT NULL AND ((r.feed_role = 'first_payment') = ` + firstPaymentSQL(ledger) + `)))`
}

// hasLedger says whether the paid ads ledger's ad_revenue table exists.
func hasLedger(ctx context.Context, q jobs.Querier) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT to_regclass('public.ad_revenue') IS NOT NULL`).Scan(&ok)
	return ok, err
}

// customerType is the type a rule sends for one event: its fixed one, or
// for auto whether this is the person's first payment.
func (s *Sender) customerType(ctx context.Context, ws string, r rule, q queued) (string, error) {
	switch r.customerType {
	case connectors.CustomerNew, connectors.CustomerReturning:
		return r.customerType, nil
	case CustomerAuto:
	default:
		return "", nil
	}
	if q.recordID == nil {
		return "", nil
	}
	ledger, err := hasLedger(ctx, s.Pool)
	if err != nil {
		return "", err
	}
	var first bool
	err = s.Pool.QueryRow(ctx, `SELECT `+firstPaymentSQL(ledger)+` FROM customer_events e JOIN ad_conversion_sends x ON x.event_row_id = e.id WHERE x.id = $1`, q.id).Scan(&first)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if first {
		return connectors.CustomerNew, nil
	}
	return connectors.CustomerReturning, nil
}

// deliver sends one batch by the transport the account is on: the
// platform's data feed when it has one and the account was not turned
// away from it, else its upload call. A feed that refuses the destination
// itself (not a row) puts the account on the upload call from then on,
// and the batch goes that way at once when the platform still takes it.
// It returns the results, the transport taken and the feed's request id.
func (s *Sender) deliver(ctx context.Context, ws string, o *connectors.Opened, r *rule, target connectors.ConversionTarget, convs []connectors.AdConversion) ([]connectors.ConversionResult, string, string, error) {
	feeder, hasFeed := o.Connector.(connectors.FeedSender)
	if hasFeed && r.feed.Transport != connectors.TransportAdsAPI {
		res, err := feeder.SendConversionsFeed(ctx, o.Client, o.Creds, r.acct, target, convs)
		if err == nil {
			return res.Results, connectors.TransportDataManager, res.RequestID, nil
		}
		fr, refused := connectors.IsFeedRefused(err)
		if !refused {
			return nil, connectors.TransportDataManager, "", err
		}
		metrics.AdsFeedBatch(connectors.TransportDataManager, "refused")
		if err := s.setTransport(ctx, ws, r, connectors.TransportAdsAPI, fr.Reason); err != nil {
			return nil, connectors.TransportDataManager, "", dbError{err}
		}
		track.Emit(ctx, track.Event{Name: "ad_feed.transport_changed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Failure, ErrorCode: "connector_" + connectors.KindOf(err),
			Props: map[string]any{"ad_account_id": r.accountID, "connector": r.connector, "transport": connectors.TransportAdsAPI, "reason": fr.Reason}})
	}
	sender, ok := o.Connector.(connectors.ConversionSender)
	if !ok {
		return nil, connectors.TransportAdsAPI, "", connectors.Errf(connectors.ErrInvalid, "%s cannot take conversions yet", o.Connector.Definition().Name)
	}
	res, err := sender.SendConversions(ctx, o.Client, o.Creds, r.acct, target, convs)
	return res, connectors.TransportAdsAPI, "", err
}

// setTransport remembers which path an account's sends take, and why.
func (s *Sender) setTransport(ctx context.Context, ws string, r *rule, transport, reason string) error {
	now := s.now()
	r.feed.Transport, r.feed.TransportReason, r.feed.TransportSetAt = transport, trim(reason, 200), &now
	return SaveFeed(ctx, s.Pool, ws, r.accountID, r.feed)
}

// feedMetrics counts one batch's sends for a connector with a data feed,
// by the transport they took.
func feedMetrics(transport string, results []connectors.ConversionResult, batchErr error) {
	if batchErr != nil {
		metrics.AdsFeedBatch(transport, "failed")
		return
	}
	metrics.AdsFeedBatch(transport, "sent")
	sent, failed := 0, 0
	for _, r := range results {
		switch {
		case r.OK:
			sent++
		case !r.Skipped:
			failed++
		}
	}
	metrics.AdsFeedSends(transport, sent, failed)
}

// sendRow is one send with what reached the platform.
type sendRow struct {
	id, eventID, transport, requestID string
}

// feedRole reads a rule's role and customer type for the API.
func feedRole(role, customerType string) (string, string) {
	if role == "" {
		role = RoleCustom
	}
	return role, customerType
}

var _ = fmt.Sprint
