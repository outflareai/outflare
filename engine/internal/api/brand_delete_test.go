package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// TestDeleteBrand: an archived brand is deleted with everything it owns,
// and nothing of it lands in "no brand" for the other brands to see.
func TestDeleteBrand(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	keep := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Keep", "handle": "keep"})["id"].(string)
	gone := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Gone", "handle": "gone"})["id"].(string)

	// A conversation with a message: the message refers to the
	// conversation, so the delete takes them in turn. Keep's stays.
	var conv, kept string
	for _, b := range []struct {
		id  string
		out *string
	}{{gone, &conv}, {keep, &kept}} {
		if err := e.pool.QueryRow(ctx, `INSERT INTO support_conversations (workspace_id, brand_id) VALUES ($1, $2) RETURNING id`, ws, b.id).Scan(b.out); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO support_messages (workspace_id, brand_id, conversation_id, kind) VALUES ($1, $2, $3, 'in')`, ws, b.id, *b.out); err != nil {
			t.Fatal(err)
		}
	}

	// Never the only brand, and nobody may be kept to it alone.
	var others []string
	for _, b := range owner.must(200, "GET", base+"/brands", nil)["data"].([]any) {
		if h := b.(map[string]any)["handle"].(string); h != "gone" {
			others = append(others, h)
			owner.must(200, "PATCH", base+"/brands/"+h, map[string]any{"status": "archived"})
		}
	}
	owner.must(409, "DELETE", base+"/brands/gone", nil)
	for _, h := range others {
		owner.must(200, "PATCH", base+"/brands/"+h, map[string]any{"status": "active"})
	}
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "op@example.com", "role": "operator", "brand_ids": []string{"gone"}})
	owner.must(409, "DELETE", base+"/brands/gone", nil)
	owner.must(204, "DELETE", base+"/invites/"+inv["invite"].(map[string]any)["id"].(string), nil)

	if out := owner.must(202, "DELETE", base+"/brands/gone", nil); out["queued"] != true {
		t.Fatalf("delete = %v", out)
	}
	e.runCRMImports(t)
	waitBrandGone(t, e, gone)
	owner.must(404, "GET", base+"/brands/gone", nil)

	var convs, msgs, orphans int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM support_conversations WHERE workspace_id = $1),
		(SELECT count(*) FROM support_messages WHERE workspace_id = $1),
		(SELECT count(*) FROM support_conversations WHERE workspace_id = $1 AND brand_id IS NULL)`, ws).Scan(&convs, &msgs, &orphans); err != nil {
		t.Fatal(err)
	}
	if convs != 1 || msgs != 1 || orphans != 0 {
		t.Fatalf("after delete: %d conversations, %d messages, %d without a brand", convs, msgs, orphans)
	}

	seen := false
	for _, ev := range got() {
		seen = seen || ev.Name == "brand.deleted"
	}
	if !seen {
		t.Fatal("no brand.deleted event")
	}
}

func waitBrandGone(t *testing.T, e *env, id string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		var n int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM brands WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			var last *string
			_ = e.pool.QueryRow(context.Background(), `SELECT last_error FROM jobs WHERE kind = 'brands.delete' ORDER BY created_at DESC LIMIT 1`).Scan(&last)
			if last != nil {
				t.Fatalf("the brand was not deleted: %s", *last)
			}
			t.Fatal("the brand was not deleted")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDeleteBrandEverywhere fills every brand table with the brand's rows,
// as TestNoBrandLeaks does, deletes the brand, and checks that none of them
// is left behind.
func TestDeleteBrandEverywhere(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "website": "alpha.example"})
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "website": "bravo.example"})["id"].(string)
	s := &seeder{t: t, pool: e.pool, ws: ws, brand: bravo, rows: map[string]map[string]any{}, failed: map[string]string{}}
	s.load()
	for _, tbl := range s.wanted() {
		s.seed(tbl)
	}

	owner.must(202, "DELETE", base+"/brands/bravo", nil)
	e.runCRMImports(t)
	waitBrandGone(t, e, bravo)

	var left []string
	for tbl, row := range s.rows {
		// The audit and API logs keep what happened, deletion included;
		// they still name the deleted brand, so no other brand sees them.
		if !s.branded[tbl] || row["id"] == nil || tbl == "audit_events" || tbl == "request_logs" {
			continue
		}
		var n int
		if err := e.pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %q WHERE id::text = $1`, tbl), fmt.Sprint(row["id"])).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			left = append(left, tbl)
		}
	}
	if len(left) > 0 {
		t.Fatalf("rows of the deleted brand left in: %s", strings.Join(left, ", "))
	}
}
