// Package quota lets an extension refuse work a workspace isn't allowed
// more of, before the engine does it: adding a brand, a mailbox, a
// tracking domain or a customer record, sending an email, or storing
// website and product events. A server
// without a Checker (every self-hosted one) allows everything.
//
// A refusal is a *Error, which the API answers with 402 quota_exceeded and
// the sending engine answers by waiting and trying again later. Any other
// error from a Checker is logged and allowed: a broken checker must never
// stop a workspace's work.
package quota

import (
	"context"
	"errors"
	"log/slog"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Resources a Checker is asked about.
const (
	Brands          = "brands"
	Mailboxes       = "mailboxes"
	TrackingDomains = "tracking_domains"
	Records         = "records"
	EmailsSent      = "emails_sent"
	TrackedEvents   = "tracked_events"
)

// Resources lists every resource, for metrics and docs.
var Resources = []string{Brands, Mailboxes, TrackingDomains, Records, EmailsSent, TrackedEvents}

// A Checker decides whether a workspace may have n more of resource.
// It is called often (every email sent), so it should answer from memory
// or one quick query.
type Checker interface {
	Allow(ctx context.Context, workspaceID, resource string, n int64) error
}

// Error is a refusal. Message is shown to the person: what stopped and
// what to do about it.
type Error struct {
	Resource string
	Message  string
}

func (e *Error) Error() string { return e.Message }

// Refusal returns the *Error inside err, if there is one.
func Refusal(err error) (*Error, bool) {
	var qe *Error
	ok := errors.As(err, &qe)
	return qe, ok
}

// Check asks c whether workspaceID may have n more of resource. It returns
// nil when c is nil, when the checker allows it, and when the checker
// fails (logged, with a quota.check_failed event); a *Error when it
// refuses.
func Check(ctx context.Context, c Checker, log *slog.Logger, workspaceID, resource string, n int64) error {
	if c == nil {
		return nil
	}
	err := c.Allow(ctx, workspaceID, resource, n)
	if err == nil {
		metrics.QuotaChecked(resource, "allowed")
		return nil
	}
	if qe, ok := Refusal(err); ok {
		metrics.QuotaChecked(resource, "refused")
		return qe
	}
	metrics.QuotaChecked(resource, "error")
	track.Fail(ctx, log, track.Event{Name: "quota.check_failed", Surface: "system", WorkspaceID: workspaceID,
		Props: map[string]any{"resource": resource}}, "internal", err)
	return nil
}
