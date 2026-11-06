// Package features switches parts of Outflare on for some brands only,
// such as a feature while its first workspace tests it. A feature in the
// catalog is off everywhere until the operator switches it on for a brand
// (Host.SetBrandFeature) or for every brand of a workspace (outflare
// features enable, or Host.SetWorkspaceFeature). Brands are fully separate
// (plans/brands/README.md): each has its own switches.
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

// SupportAutoBlocked stops the support assistant answering customers on its
// own (Auto) in a workspace, where an extension allows Auto: a switch for
// the operator's staff. Drafts keep working.
const SupportAutoBlocked = "support_auto_blocked"

// Catalog lists every switchable feature. Names are a promise like event
// names: add freely, never rename. Remove one only once its code no longer
// checks it, and leave its rows (they turn nothing on).
var Catalog = []Feature{
	{Name: AIAccounts, Description: "AI Accounts: every AI subscription the workspace's people pay for, with usage, resets, renewals and spend."},
	{Name: SupportAutoBlocked, Description: "Block support Auto: the support assistant stops answering customers on its own in this workspace and only drafts."},
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

// Enabled reports whether feature is on for any brand of the workspace,
// for what is the team's rather than a brand's (AI Accounts, a linked
// computer, the workspace's receipts address). A brand's own parts check
// BrandEnabled.
func Enabled(ctx context.Context, q Querier, workspaceID, feature string) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brand_features WHERE workspace_id = $1 AND feature = $2)`,
		workspaceID, feature).Scan(&on)
	return on, err
}

// BrandEnabled reports whether feature is on for the brand: brands are
// fully separate (plans/brands/README.md), so each has its own switches.
func BrandEnabled(ctx context.Context, q Querier, brandID, feature string) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brand_features WHERE brand_id::text = $1 AND feature = $2)`,
		brandID, feature).Scan(&on)
	return on, err
}

// List returns the catalog features that are on for any brand of the
// workspace, in catalog order. It is never nil.
func List(ctx context.Context, q Querier, workspaceID string) ([]string, error) {
	return list(ctx, q, `SELECT DISTINCT feature FROM brand_features WHERE workspace_id = $1`, workspaceID)
}

// BrandList returns the catalog features that are on for the brand, in
// catalog order. It is never nil.
func BrandList(ctx context.Context, q Querier, brandID string) ([]string, error) {
	return list(ctx, q, `SELECT feature FROM brand_features WHERE brand_id::text = $1`, brandID)
}

func list(ctx context.Context, q Querier, sql, id string) ([]string, error) {
	rows, err := q.Query(ctx, sql, id)
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

// CopyToBrand gives a new brand the switches of its workspace's first
// brand (or, for the workspace's first brand, what was switched on for the
// workspace), so a feature the operator switched on stays on for a brand
// added later.
func CopyToBrand(ctx context.Context, tx pgx.Tx, workspaceID, brandID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO brand_features (brand_id, workspace_id, feature, enabled_by)
		SELECT $2, $1, feature, enabled_by FROM brand_features
		WHERE workspace_id = $1 AND brand_id <> $2 AND brand_id = (SELECT id FROM brands WHERE workspace_id = $1 AND id <> $2
			ORDER BY status = 'active' DESC, created_at, id LIMIT 1)
		UNION ALL
		SELECT $2, $1, feature, enabled_by FROM workspace_features
		WHERE workspace_id = $1 AND NOT EXISTS (SELECT 1 FROM brands WHERE workspace_id = $1 AND id <> $2)
		ON CONFLICT DO NOTHING`, workspaceID, brandID)
	return err
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
	// Every brand of the workspace, and workspace_features: the previous
	// version reads it (migration 0471), and a brand added later starts
	// with it when it is the workspace's first (CopyToBrand).
	var tag, brands pgconn.CommandTag
	if on {
		tag, err = tx.Exec(ctx, `INSERT INTO workspace_features (workspace_id, feature, enabled_by) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, workspaceID, feature, by)
		if err == nil {
			brands, err = tx.Exec(ctx, `INSERT INTO brand_features (brand_id, workspace_id, feature, enabled_by)
				SELECT id, workspace_id, $2, $3 FROM brands WHERE workspace_id = $1 ON CONFLICT DO NOTHING`, workspaceID, feature, by)
		}
	} else {
		tag, err = tx.Exec(ctx, `DELETE FROM workspace_features WHERE workspace_id = $1 AND feature = $2`, workspaceID, feature)
		if err == nil {
			brands, err = tx.Exec(ctx, `DELETE FROM brand_features WHERE workspace_id = $1 AND feature = $2`, workspaceID, feature)
		}
	}
	if err != nil || tag.RowsAffected()+brands.RowsAffected() == 0 {
		return false, err
	}
	action := "workspace.feature_disabled"
	if on {
		action = "workspace.feature_enabled"
	}
	e := audit.Event{WorkspaceID: workspaceID, ActorType: "system", Action: action, TargetType: "workspace", TargetID: workspaceID,
		Data: map[string]any{"feature": feature}, Props: map[string]any{"feature": feature}}
	return true, recordSwitch(ctx, tx, e, byUserID)
}

// SetBrand switches feature on or off for one brand of the workspace in
// tx, and records brand.feature_enabled or brand.feature_disabled when it
// changes. byUserID and changed are as for Set.
func SetBrand(ctx context.Context, tx pgx.Tx, workspaceID, brandID, feature string, on bool, byUserID string) (changed bool, err error) {
	if !Known(feature) {
		return false, ErrUnknown
	}
	var by *string
	if byUserID != "" {
		by = &byUserID
	}
	var tag pgconn.CommandTag
	if on {
		tag, err = tx.Exec(ctx, `INSERT INTO brand_features (brand_id, workspace_id, feature, enabled_by)
			SELECT id, workspace_id, $3, $4 FROM brands WHERE workspace_id = $1 AND id::text = $2 ON CONFLICT DO NOTHING`,
			workspaceID, brandID, feature, by)
	} else {
		tag, err = tx.Exec(ctx, `DELETE FROM brand_features WHERE workspace_id = $1 AND brand_id::text = $2 AND feature = $3`,
			workspaceID, brandID, feature)
	}
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	action := "brand.feature_disabled"
	if on {
		action = "brand.feature_enabled"
	}
	e := audit.Event{WorkspaceID: workspaceID, BrandID: brandID, ActorType: "system", Action: action, TargetType: "brand", TargetID: brandID,
		Data: map[string]any{"feature": feature}, Props: map[string]any{"feature": feature}}
	return true, recordSwitch(ctx, tx, e, byUserID)
}

func recordSwitch(ctx context.Context, tx pgx.Tx, e audit.Event, byUserID string) error {
	if byUserID != "" {
		// The audit log names the person's actor; events name the user.
		if err := tx.QueryRow(ctx, `SELECT actor_id::text FROM users WHERE id::text = $1`, byUserID).Scan(&e.ActorID); err != nil {
			return err
		}
		e.ActorType, e.UserID = "human", byUserID
	}
	return audit.Record(ctx, tx, e)
}
