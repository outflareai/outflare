package api_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/features"
)

func TestWorkspaceFeatures(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)

	// Off by default: the field is left out.
	if got := owner.must(200, "GET", "/v1/workspaces/"+ws, nil); got["features"] != nil {
		t.Fatalf("features before switching on: %v", got["features"])
	}

	var id string
	if err := e.pool.QueryRow(t.Context(), `SELECT id::text FROM workspaces WHERE id::text = $1 OR slug = $1`, ws).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, id, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := owner.must(200, "GET", "/v1/workspaces/"+ws, nil)
	if list, _ := got["features"].([]any); len(list) != 1 || list[0] != features.AIAccounts {
		t.Fatalf("features = %v, want [ai_accounts]", got["features"])
	}
}
