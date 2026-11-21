package api

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Alerts on errors (docs/error-tracking.md): the first error of an issue
// nobody has seen before raises an alert, once per issue, and so does the
// first error of an issue someone resolved, once per time it comes back.
// An ignored issue raises nothing. It is decided when the error is
// stored, in its transaction, so nothing polls.

// AlertErrorIssue is the alert kind.
const AlertErrorIssue = "error.new_issue"

// errorAlertsAnHour bounds a broken release: past this many alerts for a
// brand in an hour, further new issues wait for their next error.
const errorAlertsAnHour = 10

func init() {
	alerts.Register(alerts.Kind{ID: AlertErrorIssue, Title: "A new error",
		Description: "Your site or server sent an error of a kind not seen before, or one you resolved came back. Once per issue, and at most ten an hour for a brand. Emails are off until you switch them on.",
		Scope:       auth.ScopeAnalyticsRead, DefaultEmail: alerts.ToOff, Describe: describeErrorIssue})
}

func describeErrorIssue(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	fp, _ := a.Data["fingerprint"].(string)
	v := alerts.View{Title: "A new error", Path: "errors"}
	if errorFingerprintRe.MatchString(fp) {
		v.Path = "errors/" + fp
	}
	var typ, message string
	err := q.QueryRow(ctx, `SELECT left(coalesce(properties->>'type', ''), 100), left(coalesce(properties->>'message', ''), 200)
		FROM customer_events WHERE id = $1 AND workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&typ, &message)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return alerts.View{}, err
	}
	if typ == "" {
		typ = "Error"
	}
	if a.Data["reason"] == "regressed" {
		v.Title = "An error you resolved is back: " + typ
	} else {
		v.Title = "A new error: " + typ
	}
	v.Text = message
	if v.Text == "" {
		v.Text = "Open the issue to see where it happens."
	}
	return v, nil
}

// errorFingerprintOf is errorFingerprint in Go: the issue an error event
// belongs to.
func errorFingerprintOf(props map[string]any) string {
	if fp, _ := props["fingerprint"].(string); errorFingerprintRe.MatchString(fp) {
		return fp
	}
	sum := md5.Sum([]byte(propText(props["type"]) + ":" + propText(props["message"])))
	return hex.EncodeToString(sum[:])[:16]
}

// propText is a JSON value as properties->>'key' reads it.
func propText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	return string(raw(v))
}

// errorStored raises the alert for an error event just stored, when its
// issue is new or was resolved before this error.
func errorStored(ctx context.Context, tx pgx.Tx, ws, brand, eventID string, props, ctxObj map[string]any, at time.Time) error {
	// Noise and errors a rule filters never alert.
	if n, _ := props["noise"].(string); n != "" {
		return nil
	}
	if hit, err := errorRuleHit(ctx, tx, brand, props, ctxObj); err != nil || hit {
		return err
	}
	fp, _ := props["group"].(string)
	if fp == "" {
		fp = errorFingerprintOf(props)
	}
	var into string
	if err := tx.QueryRow(ctx, `SELECT into_fingerprint FROM error_issue_merges WHERE brand_id = $1 AND fingerprint = $2`, brand, fp).Scan(&into); err == nil {
		fp = into
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	key, reason := brand+":"+fp, "new"
	var status string
	var statusAt time.Time
	err := tx.QueryRow(ctx, `SELECT status, status_at FROM error_issues WHERE workspace_id = $1 AND brand_id = $2 AND fingerprint = $3`, ws, brand, fp).Scan(&status, &statusAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return err
	case status == "ignored":
		return nil
	case status == "resolved" && at.After(statusAt):
		key, reason = fmt.Sprintf("%s:%d", key, statusAt.Unix()), "regressed"
	}
	var raised bool
	var recent int
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alerts WHERE workspace_id = $1 AND kind = $2 AND dedupe_key = $3),
			(SELECT count(*) FROM alerts WHERE workspace_id = $1 AND kind = $2 AND brand_id = $4 AND created_at > now() - interval '1 hour')`,
		ws, AlertErrorIssue, key, brand).Scan(&raised, &recent); err != nil {
		return err
	}
	if raised || recent >= errorAlertsAnHour {
		return nil
	}
	_, err = alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: &brand, Kind: AlertErrorIssue, DedupeKey: key,
		TargetType: "customer_event", TargetID: eventID, Data: map[string]any{"fingerprint": fp, "reason": reason}, RequestID: RequestID(ctx)})
	return err
}
