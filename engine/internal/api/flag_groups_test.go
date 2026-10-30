package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/flags"
)

// A group's holdout is kept out of every flag in it, and the report
// compares it with everyone else, finding who is in it in SQL with the same
// hash the flags use.
func TestFlagGroupHoldout(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// The SQL hash puts every visitor where the flags do.
	var ids []string
	for i := range 300 {
		ids = append(ids, fmt.Sprintf("visitor-%d-%x", i, i*7919))
	}
	rows, err := e.pool.Query(t.Context(), `SELECT id, `+flags.GroupBucketSQL("'homepage'", "id")+` FROM unnest($1::text[]) id`, ids)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var b int
		if err := rows.Scan(&id, &b); err != nil {
			t.Fatal(err)
		}
		if want := flags.GroupBucket("homepage", id); b != want {
			t.Fatalf("%s: SQL bucket %d, Go %d", id, b, want)
		}
	}
	rows.Close()

	split := []map[string]any{{"key": "control", "weight": 50}, {"key": "new", "weight": 50}}
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "hero", "status": "on", "variants": split, "exclusion_group": "homepage"})
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "cta", "status": "on", "variants": split, "exclusion_group": "homepage", "group_share": 40})
	// A holdout can't reach into cta's slice (50 to 90).
	owner.must(409, "PUT", base+"/flag-groups/homepage", map[string]any{"holdout": 20})
	owner.must(400, "PUT", base+"/flag-groups/homepage", map[string]any{"holdout": 60})
	g := owner.must(200, "PUT", base+"/flag-groups/homepage", map[string]any{"holdout": 10})
	if g["holdout"] != 10.0 || g["free"] != 0.0 || len(g["flags"].([]any)) != 2 {
		t.Fatalf("group: %v", g)
	}
	// No room left for another flag.
	owner.must(409, "POST", base+"/flags", map[string]any{"key": "footer", "variants": split, "exclusion_group": "homepage", "group_share": 5})
	if l := owner.must(200, "GET", base+"/flag-groups", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("groups: %v", l)
	}

	// Holdout visitors get neither flag.
	var held []string
	for i := 0; len(held) < 5; i++ {
		if id := fmt.Sprintf("h-%d", i); flags.GroupBucket("homepage", id) >= 9000 {
			held = append(held, id)
		}
	}
	for _, id := range held {
		f := owner.must(200, "POST", base+"/flags/evaluate", map[string]any{"anonymous_id": id})["flags"].(map[string]any)
		if f["hero"] != false || f["cta"] != false {
			t.Fatalf("%s is in the holdout but got %v", id, f)
		}
	}

	// 2,000 visitors: the holdout signs up less than everyone else.
	var visitors, inHoldout []string
	for i := range 2000 {
		id := fmt.Sprintf("site-%d", i)
		visitors = append(visitors, id)
		if flags.GroupBucket("homepage", id) >= 9000 {
			inHoldout = append(inHoldout, id)
		}
	}
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'page', 'page.viewed', id, '{}'::jsonb, '{"page": {"path": "/"}}'::jsonb, now() - interval '3 days' FROM unnest($2::text[]) id
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', id, '{}'::jsonb, '{}'::jsonb, now() - interval '2 days'
		FROM unnest($2::text[]) WITH ORDINALITY AS v(id, n)
		WHERE (id = ANY($3) AND n % 10 = 0) OR (NOT id = ANY($3) AND n % 10 < 3)`, ws, visitors, inHoldout); err != nil {
		t.Fatal(err)
	}
	owner.must(400, "GET", base+"/flag-groups/homepage/holdout", nil)
	r := owner.must(200, "GET", base+"/flag-groups/homepage/holdout?event=signup_started", nil)
	arms := r["arms"].([]any)
	h, rest := arms[0].(map[string]any), arms[1].(map[string]any)
	if h["variant"] != "holdout" || h["visitors"] != float64(len(inHoldout)) || rest["visitors"] != float64(2000-len(inHoldout)) {
		t.Fatalf("arms: %v", arms)
	}
	c := r["comparison"].(map[string]any)
	if c["variant"] != "everyone_else" || c["lift"].(float64) < 1 || c["chance_to_beat"].(float64) < 0.99 {
		t.Fatalf("comparison: %v", c)
	}
	// No holdout: nothing to compare.
	owner.must(200, "PUT", base+"/flag-groups/homepage", map[string]any{"holdout": 0})
	if e := owner.must(409, "GET", base+"/flag-groups/homepage/holdout?event=signup_started", nil)["error"].(map[string]any)["message"].(string); !strings.Contains(e, "no holdout") {
		t.Fatalf("without a holdout: %s", e)
	}
}
