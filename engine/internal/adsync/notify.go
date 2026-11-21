package adsync

// Alerts people hear about (plans/ad-tracker.md, B8): raised through the
// alerts package, so each is kept in the app, sent as the alert.raised
// webhook and goes by email, Slack and Telegram as the brand chose. The
// ad_suggestions alerts beside them (alerts.go) are the findings on the
// Paid ads page; these two are the ones that need someone now.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

const (
	// AlertSyncFailed: an ad connection's sync keeps failing, so the
	// numbers on Paid ads are getting old. Once per run of failures.
	AlertSyncFailed = "ads.sync_failed"
	// AlertSendsRefused: an ad platform is refusing the conversions sent
	// to it. Once each time the sends_failing finding opens.
	AlertSendsRefused = "ads.sends_refused"

	// syncFailedAfter is how many syncs in a row fail before people hear.
	// A refused sign-in tells at once: it won't fix itself.
	syncFailedAfter = 3
)

func init() {
	alerts.Register(alerts.Kind{ID: AlertSyncFailed, Title: "Ad sync failing",
		Description: "A connected ad platform refused the sign-in, or its sync failed three times in a row, so the numbers in Paid ads are getting old. Once each time it starts failing. It goes to the owners and admins.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describeSyncFailed})
	alerts.Register(alerts.Kind{ID: AlertSendsRefused, Title: "Ad platform refusing conversions",
		Description: "An ad platform refused a tenth or more of the conversions sent to it in 24 hours, so its bidding is learning from less. Once each time it starts. It goes to the owners and admins.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describeSendsRefused})
}

// syncFailedDue says whether a failed sync is one to tell people about:
// the platform refused the sign-in, or this is the third failure in a row
// (or a later one, which the alert's dedupe key folds into the same
// alert). A failure of Outflare's own (code internal) never is.
func syncFailedDue(code string, failures int) bool {
	switch code {
	case "connector_auth":
		return true
	case "connector_quota", "connector_unavailable", "connector_invalid":
		return failures >= syncFailedAfter
	}
	return false
}

// raiseSyncFailed raises AlertSyncFailed for a connection, once per run of
// failures: the dedupe key is the connection and when it last synced.
func raiseSyncFailed(ctx context.Context, tx pgx.Tx, ws, integrationID, code string, failures int, lastOK *time.Time) error {
	var connector string
	var brand *string
	if err := tx.QueryRow(ctx, `SELECT i.connector, `+connectionBrand+`::text FROM integrations i WHERE i.id = $1 AND i.workspace_id = $2`,
		integrationID, ws).Scan(&connector, &brand); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the connection was removed while it synced
		}
		return err
	}
	since := "never"
	if lastOK != nil {
		since = lastOK.UTC().Format(time.RFC3339)
	}
	_, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: brand, Kind: AlertSyncFailed, DedupeKey: integrationID + ":" + since,
		TargetType: "integration", TargetID: integrationID,
		Data: map[string]any{"connector": connector, "error_code": code, "failures": failures}})
	return err
}

func describeSyncFailed(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var name, connector string
	if err := q.QueryRow(ctx, `SELECT name, connector FROM integrations WHERE id = $1 AND workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&name, &connector); err != nil {
		return alerts.View{}, err
	}
	if name == "" {
		name = "An ad connection"
	}
	text := "Its last syncs failed, so the numbers in Paid ads are getting old. Open the connection to see why."
	switch a.Data["error_code"] {
	case "connector_auth":
		text = "The platform refused the sign-in, so the numbers in Paid ads are getting old. Connect it again."
	case "connector_quota":
		text = "The platform says the account is out of quota or rate limited, so the numbers in Paid ads are getting old."
	}
	return alerts.View{Title: name + " stopped syncing", Text: text, Path: "connectors/" + connector}, nil
}

// raiseSendsRefused raises AlertSendsRefused for an ad account when its
// sends_failing finding opens.
func raiseSendsRefused(ctx context.Context, tx pgx.Tx, ws, suggestionID string, f finding) error {
	data := map[string]any{"connector": f.c.connector, "suggestion_id": suggestionID}
	for _, k := range []string{"failed", "sent"} {
		if v, ok := f.evidence[k]; ok {
			data[k] = v
		}
	}
	_, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: f.c.brand, Kind: AlertSendsRefused, DedupeKey: suggestionID,
		TargetType: "ad_account", TargetID: f.account, Data: data})
	return err
}

func describeSendsRefused(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var name string
	if err := q.QueryRow(ctx, `SELECT name FROM ad_accounts WHERE id = $1 AND workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&name); err != nil {
		return alerts.View{}, err
	}
	if name == "" {
		name = "An ad account"
	}
	text := "It refused conversions sent to it in the last 24 hours. See why under Paid ads."
	failed, ok1 := a.Data["failed"].(float64)
	sent, ok2 := a.Data["sent"].(float64)
	if ok1 && ok2 && sent > 0 {
		text = fmt.Sprintf("It refused %.0f of the %.0f conversions sent to it in 24 hours. See why under Paid ads.", failed, sent)
	}
	return alerts.View{Title: name + " is refusing conversions", Text: text, Path: "ads"}, nil
}
