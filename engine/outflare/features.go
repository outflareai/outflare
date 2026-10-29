package outflare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/config"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Workspace features: parts of Outflare that stay off until switched on
// for a workspace (package features), such as AI Accounts while its first
// workspace tests it. An extension switches them through the Host; an
// operator can also run outflare features.

// Feature is a switchable part of Outflare: its name and one line.
type Feature = features.Feature

// ErrUnknownFeature is returned for a name that isn't in Features.
var ErrUnknownFeature = features.ErrUnknown

// Features lists every switchable feature.
func Features() []Feature { return features.Catalog }

// WorkspaceFeatures returns the features that are on for a workspace.
func (h *Host) WorkspaceFeatures(ctx context.Context, workspaceID string) ([]string, error) {
	return features.List(ctx, h.Pool, workspaceID)
}

// SetWorkspaceFeature switches feature on or off for everyone in a
// workspace, and records workspace.feature_enabled or
// workspace.feature_disabled in its audit log when it changes. byUserID is
// the person who did it. changed is false when it already was that
// way.
func (h *Host) SetWorkspaceFeature(ctx context.Context, workspaceID, feature string, on bool, byUserID string) (changed bool, err error) {
	err = pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		changed, err = features.Set(ctx, tx, workspaceID, feature, on, byUserID)
		return err
	})
	return changed, err
}

const featuresUsage = `usage:
  outflare features list [--workspace <id or slug>]
  outflare features enable --workspace <id or slug> <feature>
  outflare features disable --workspace <id or slug> <feature>`

// featuresCommand lets an operator switch features for a workspace:
// outflare features enable --workspace acme ai_accounts.
func featuresCommand(ctx context.Context, args []string) int {
	var verb, ws, name string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--workspace" && i+1 < len(args):
			ws = args[i+1]
			i++
		case strings.HasPrefix(a, "--workspace="):
			ws = strings.TrimPrefix(a, "--workspace=")
		case verb == "":
			verb = a
		case name == "":
			name = a
		default:
			fmt.Fprintln(os.Stderr, featuresUsage)
			return 2
		}
	}
	switch {
	case verb == "list" && name == "":
	case (verb == "enable" || verb == "disable") && ws != "" && name != "":
	default:
		fmt.Fprintln(os.Stderr, featuresUsage)
		return 2
	}
	if verb == "list" && ws == "" {
		for _, f := range features.Catalog {
			fmt.Printf("%s\t%s\n", f.Name, f.Description)
		}
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	var id string
	err = pool.QueryRow(ctx, `SELECT id::text FROM workspaces WHERE id::text = $1 OR slug = $1`, ws).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		fmt.Fprintln(os.Stderr, "no workspace has id or slug "+ws)
		return 1
	} else if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if verb == "list" {
		on, err := features.List(ctx, pool, id)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, f := range features.Catalog {
			state := "off"
			if slices.Contains(on, f.Name) {
				state = "on"
			}
			fmt.Printf("%s\t%s\n", f.Name, state)
		}
		return 0
	}
	var changed bool
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		changed, err = features.Set(ctx, tx, id, name, verb == "enable", "")
		return err
	})
	failed := track.Event{Name: "workspace.feature_switch_failed", Surface: "system", WorkspaceID: id, Props: map[string]any{"feature": name}}
	switch {
	case errors.Is(err, features.ErrUnknown):
		failed.Outcome = track.Failure
		track.Fail(ctx, nil, failed, "not_found", err)
		fmt.Fprintln(os.Stderr, "unknown feature "+name+" (outflare features list shows them)")
		return 1
	case err != nil:
		track.Fail(ctx, nil, failed, "internal", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	state := "off"
	if verb == "enable" {
		state = "on"
	}
	if !changed {
		fmt.Printf("%s was already %s for %s\n", name, state, ws)
		return 0
	}
	fmt.Printf("%s is now %s for %s\n", name, state, ws)
	return 0
}
