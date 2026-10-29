// Package features switches parts of Outflare on for some workspaces
// only, such as a feature while its first workspace tests it. A feature in
// the catalog is off everywhere until the operator switches it on for a
// workspace (outflare features enable, or an extension through
// Host.SetWorkspaceFeature); then it is on for everyone in that workspace.
//
// Routes and jobs of a switched feature check Enabled and answer as if
// they didn't exist while it's off. The web app reads the workspace's
// features from GET /v1/workspaces/{ws}.
//
// This is not the website feature flags product (package flags), which
// workspaces use on their own sites.
package features

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/audit"
)

// A Feature is a part of Outflare that is off until switched on for a
// workspace.
type Feature struct {
	// Name is how code, the API and the command line name it.
	Name string `json:"name"`
	// Description is one line for the people who switch it.
	Description string `json:"description"`
}

// AIAccounts is every AI subscription the workspace's people pay for, with
// usage, resets, renewals and spend (plans/ai-accounts).
const AIAccounts = "ai_accounts"

// Catalog lists every switchable feature. Names are a promise like event
// names: add freely, never rename. Remove one only once its code no longer
// checks it, and leave its rows (they turn nothing on).
var Catalog = []Feature{
	{Name: AIAccounts, Description: "AI Accounts: every AI subscription the workspace's people pay for, with usage, resets, renewals and spend."},
}

// ErrUnknown is returned for a name that isn't in the catalog.
var ErrUnknown = errors.New("unknown feature")

// Known reports whether name is in the catalog.
func Known(name string) bool {
	return slices.ContainsFunc(Catalog, func(f Feature) bool { return f.Name == name })
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Enabled reports whether feature is on for the workspace.
func Enabled(ctx context.Context, q Querier, workspaceID, feature string) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspace_features WHERE workspace_id = $1 AND feature = $2)`,
		workspaceID, feature).Scan(&on)
	return on, err
}

// List returns the catalog features that are on for the workspace, in
// catalog order. It is never nil.
func List(ctx context.Context, q Querier, workspaceID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT feature FROM workspace_features WHERE workspace_id = $1`, workspaceID)
	if err != nil {
		return nil, err
	}
	on, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, f := range Catalog {
		if slices.Contains(on, f.Name) {
			out = append(out, f.Name)
		}
	}
	return out, nil
}

// Set switches feature on or off for the workspace in tx, and records
// workspace.feature_enabled or workspace.feature_disabled when it changes.
// byUserID is the person who did it through an extension, or empty for
// the operator's command line. changed is false when it already was that way.
func Set(ctx context.Context, tx pgx.Tx, workspaceID, feature string, on bool, byUserID string) (changed bool, err error) {
	if !Known(feature) {
		return false, ErrUnknown
	}
	var by *string
	if byUserID != "" {
		by = &byUserID
	}
	var tag pgconn.CommandTag
	if on {
		tag, err = tx.Exec(ctx, `INSERT INTO workspace_features (workspace_id, feature, enabled_by) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, workspaceID, feature, by)
	} else {
		tag, err = tx.Exec(ctx, `DELETE FROM workspace_features WHERE workspace_id = $1 AND feature = $2`, workspaceID, feature)
	}
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	action := "workspace.feature_disabled"
	if on {
		action = "workspace.feature_enabled"
	}
	e := audit.Event{WorkspaceID: workspaceID, ActorType: "system", Action: action, TargetType: "workspace", TargetID: workspaceID,
		Data: map[string]any{"feature": feature}, Props: map[string]any{"feature": feature}}
	if byUserID != "" {
		// The audit log names the person's actor; events name the user.
		if err := tx.QueryRow(ctx, `SELECT actor_id::text FROM users WHERE id::text = $1`, byUserID).Scan(&e.ActorID); err != nil {
			return false, err
		}
		e.ActorType, e.UserID = "human", byUserID
	}
	return true, audit.Record(ctx, tx, e)
}
