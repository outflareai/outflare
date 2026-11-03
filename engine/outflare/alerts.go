package outflare

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Alerts tell people when something needs them now (docs/alerts.md): kept
// in the app under Settings → Alerts, emailed to the person it is for or to
// the owners and admins as the workspace chooses, posted to Slack, and sent
// as the alert.raised webhook. An extension adds its own kinds and raises
// them like the engine's modules do.
type (
	// AlertKind is a sort of alert: an id such as "billing.allowance", a
	// title and a description for Settings → Alerts, the scope a caller
	// needs to see them, and Describe, which turns a kept alert into what
	// people see. Describe reads only through the q it is given, never
	// from state captured at Setup.
	AlertKind = alerts.Kind
	// AlertView is an alert as people see it: a title, a line of detail and
	// the page to open: under the workspace (such as "settings"), or a page
	// of the extension's own starting with "/" (such as "/billing").
	AlertView = alerts.View
	// AlertRow is a kept alert, as Describe gets it.
	AlertRow = alerts.Row
	// AlertQuerier is what Describe reads through: a pool or a
	// transaction.
	AlertQuerier = jobs.Querier
	// Alert is what RaiseAlert raises. DedupeKey makes the same thing raise
	// one alert; Data holds ids, enums and counts only.
	Alert = alerts.Alert
)

var alertKindID = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_.]*$`)

// AddAlertKind registers a kind of alert. Call it from Setup. A kind
// already registered under the same id is kept (an engine started twice in
// one process, as tests do); a malformed id or a missing Describe panics at
// start-up.
func (h *Host) AddAlertKind(k AlertKind) {
	if !alertKindID.MatchString(k.ID) || k.Title == "" || k.Describe == nil {
		panic(fmt.Sprintf("outflare: alert kind %q needs an id like \"area.event\", a title and Describe", k.ID))
	}
	if _, ok := alerts.Find(k.ID); ok {
		return
	}
	alerts.Register(k)
}

// RaiseAlert keeps an alert, records alert.raised and queues its emails and
// Slack post, all in tx. It returns the alert's id, or "" when the same
// thing (its DedupeKey) was already raised.
func RaiseAlert(ctx context.Context, tx pgx.Tx, a Alert) (string, error) {
	return alerts.Raise(ctx, tx, a)
}
