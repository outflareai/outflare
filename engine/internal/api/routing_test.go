package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/routing"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Routing: a brand's ordered rules give companies with no owner to the
// member of the matching rule's pool who owns the fewest, in the
// background, on request, and never over an owner already set.
func TestRouting(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com", Mailer: e.outbox, Log: log}
	bea, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "bea@example.com", Name: "Bea", Password: "correct horse battery", WorkspaceName: "Bea's"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, bea); err != nil {
		t.Fatal(err)
	}
	captured := track.Capture(t)
	has := func(name string) bool {
		for _, ev := range captured() {
			if ev.Name == name {
				return true
			}
		}
		return false
	}

	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Segment", "value_type": "text"})
	add := func(name, segment string) string {
		t.Helper()
		attrs := map[string]any{}
		if segment != "" {
			attrs["segment"] = segment
		}
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": name, "attributes": attrs})["id"].(string)
	}
	ownerOf := func(rec string) any {
		t.Helper()
		return owner.must(200, "GET", base+"/records/"+rec, nil)["attributes"].(map[string]any)["owner"]
	}
	big := add("Acme", "enterprise")
	small := add("Northpeak", "")

	// No rules yet.
	got := owner.must(200, "GET", base+"/routing", nil)
	if len(got["data"].([]any)) != 0 || got["unowned"].(map[string]any)["account"] != float64(2) {
		t.Fatalf("an empty list: %v", got)
	}
	if res := owner.must(200, "GET", base+"/records/"+big+"/routing", nil); res["outcome"] != "no_rule" {
		t.Fatalf("a preview with no rules: %v", res)
	}

	enterprise := map[string]any{"name": "Enterprise", "record_type": "account", "members": []string{bea},
		"conditions": []map[string]any{{"attribute": "segment", "op": "eq", "value": "enterprise"}}}
	rest := map[string]any{"name": "Everyone else", "record_type": "account", "members": []string{me, bea}}
	for i, bad := range []map[string]any{
		{"name": "x", "record_type": "account", "members": []string{}},
		{"name": "x", "record_type": "deal", "members": []string{me}},
		{"name": "x", "record_type": "account", "members": []string{"00000000-0000-0000-0000-000000000000"}},
		{"name": "x", "record_type": "account", "members": []string{me}, "conditions": []map[string]any{{"attribute": "owner", "op": "exists"}}},
		{"name": "x", "record_type": "account", "members": []string{me}, "conditions": []map[string]any{{"property": "plan", "op": "exists"}}},
		{"id": "00000000-0000-0000-0000-000000000000", "name": "x", "record_type": "account", "members": []string{me}},
	} {
		if code, body := owner.do("PUT", base+"/routing", map[string]any{"rules": []any{bad}}); code != 400 {
			t.Fatalf("bad rule %d: %d %v", i, code, body)
		}
	}
	owner.must(400, "PUT", base+"/routing", map[string]any{})
	list := owner.must(200, "PUT", base+"/routing", map[string]any{"rules": []any{enterprise, rest}})["data"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["name"] != "Enterprise" || list[0].(map[string]any)["status"] != "active" {
		t.Fatalf("the rules: %v", list)
	}
	entID := list[0].(map[string]any)["id"].(string)
	if !has("routing.updated") {
		t.Fatal("no routing.updated event")
	}

	// A preview changes nothing.
	if res := owner.must(200, "GET", base+"/records/"+big+"/routing", nil); res["outcome"] != "assigned" || res["rule_name"] != "Enterprise" || res["owner_id"] != bea {
		t.Fatalf("a preview: %v", res)
	}
	if ownerOf(big) != nil {
		t.Fatal("a preview set the owner")
	}

	// The round routes both: the enterprise one to Bea, the other to
	// whoever owns fewest, which is now the owner.
	svc := &routing.Service{Pool: e.pool, Log: log}
	if n, err := svc.Round(ctx); err != nil || n != 2 {
		t.Fatalf("a round: %d %v", n, err)
	}
	if ownerOf(big) != bea || ownerOf(small) != me {
		t.Fatalf("owners: %v %v", ownerOf(big), ownerOf(small))
	}
	var history int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM attribute_changes WHERE record_id = $1 AND field_key = 'owner' AND source = 'routing' AND new_value = to_jsonb($2::text)`,
		big, bea).Scan(&history); err != nil || history != 1 {
		t.Fatalf("the record's history: %d %v", history, err)
	}
	// Nothing left to do, and a second round changes nothing.
	if n, err := svc.Round(ctx); err != nil || n != 0 {
		t.Fatalf("a second round: %d %v", n, err)
	}

	// A company that arrives later is routed by the next round; one with
	// an owner keeps them.
	later := add("Lumen", "enterprise")
	owned := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Harbor", "attributes": map[string]any{"segment": "enterprise", "owner": me}})["id"].(string)
	if n, err := svc.Round(ctx); err != nil || n != 1 {
		t.Fatalf("a later round: %d %v", n, err)
	}
	if ownerOf(later) != bea || ownerOf(owned) != me {
		t.Fatalf("owners later: %v %v", ownerOf(later), ownerOf(owned))
	}

	// On request: kept without replace, moved with it.
	if res := owner.must(200, "POST", base+"/records/"+owned+"/routing", nil); res["outcome"] != "kept" {
		t.Fatalf("routing an owned record: %v", res)
	}
	if res := owner.must(200, "POST", base+"/records/"+owned+"/routing", map[string]any{"replace": true}); res["outcome"] != "assigned" || res["owner_id"] != bea {
		t.Fatalf("routing with replace: %v", res)
	}
	if !has("record.routed") {
		t.Fatal("no record.routed event")
	}
	owner.must(404, "POST", base+"/records/00000000-0000-0000-0000-000000000000/routing", nil)

	// The rule kept its count through an edit; a paused rule is skipped,
	// and changing the rules looks at unowned records again.
	orphan := add("Pinecrest", "enterprise")
	enterprise["id"], enterprise["status"] = entID, "paused"
	list = owner.must(200, "PUT", base+"/routing", map[string]any{"rules": []any{enterprise}})["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["assigned"] != float64(3) || list[0].(map[string]any)["status"] != "paused" {
		t.Fatalf("after an edit: %v", list)
	}
	if n, err := svc.Round(ctx); err != nil || n != 0 || ownerOf(orphan) != nil {
		t.Fatalf("a round with the rule paused: %d %v", n, err)
	}
	enterprise["status"] = "active"
	owner.must(200, "PUT", base+"/routing", map[string]any{"rules": []any{enterprise}})
	if n, err := svc.Round(ctx); err != nil || n != 1 || ownerOf(orphan) != bea {
		t.Fatalf("a round after turning it on: %d %v", n, err)
	}

	// A territory and a team: a rule that takes only companies in Germany
	// or France, and gives them to whoever is in the team at the time.
	team := owner.must(201, "POST", "/v1/workspaces/"+ws+"/teams", map[string]any{"name": "Europe", "member_ids": []string{me}})["id"].(string)
	europe := map[string]any{"name": "Europe", "record_type": "account", "team_id": team, "countries": []string{"de", "FR"}}
	owner.must(400, "PUT", base+"/routing", map[string]any{"rules": []any{map[string]any{"name": "Both", "record_type": "account", "team_id": team, "members": []string{me}}}})
	owner.must(400, "PUT", base+"/routing", map[string]any{"rules": []any{map[string]any{"name": "Bad", "record_type": "account", "team_id": team, "countries": []string{"Germany"}}}})
	owner.must(400, "PUT", base+"/routing", map[string]any{"rules": []any{map[string]any{"name": "Bad", "record_type": "account", "team_id": "00000000-0000-0000-0000-000000000000"}}})
	list = owner.must(200, "PUT", base+"/routing", map[string]any{"rules": []any{europe}})["data"].([]any)
	if got := list[0].(map[string]any); got["team_id"] != team || len(got["members"].([]any)) != 0 || fmt.Sprint(got["countries"]) != "[DE FR]" {
		t.Fatalf("a team rule: %v", got)
	}
	place := func(name, country string) string {
		t.Helper()
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": name, "attributes": map[string]any{"country": country}})["id"].(string)
	}
	berlin, austin, nowhere := place("Berlin Werke", "DE"), place("Austin Works", "US"), add("Nowhere", "")
	if n, err := svc.Round(ctx); err != nil || n != 1 || ownerOf(berlin) != me || ownerOf(austin) != nil || ownerOf(nowhere) != nil {
		t.Fatalf("a round with a territory: %d %v", n, err)
	}
	// Bea joins the team: she owns fewer, so the next one is hers.
	owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/teams/"+team, map[string]any{"member_ids": []string{me, bea}})
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = attributes - 'owner' WHERE brand_id = $1 AND id::text <> $2`, brand, berlin); err != nil {
		t.Fatal(err)
	}
	paris := place("Paris Atelier", "FR")
	if res := owner.must(200, "POST", base+"/records/"+paris+"/routing", nil); res["outcome"] != "assigned" || res["owner_id"] != bea {
		t.Fatalf("routing to a team: %v", res)
	}
	// An empty team gives nobody; a deleted team leaves the rule without a pool.
	owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/teams/"+team, map[string]any{"member_ids": []string{}})
	if res := owner.must(200, "GET", base+"/records/"+place("Lyon Tissus", "FR")+"/routing", nil); res["outcome"] != "no_member" {
		t.Fatalf("routing to an empty team: %v", res)
	}

	// Another brand has no rules of this one.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly", "handle": "summitly"})["id"].(string)
	if got := owner.must(200, "GET", "/v1/brands/"+other+"/routing", nil); len(got["data"].([]any)) != 0 {
		t.Fatalf("another brand's rules: %v", got)
	}
}

// Hours and capacity: routing skips someone who is away or at their
// capacity, and prefers someone at work over someone who is off.
func TestRoutingHoursAndCapacity(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com", Mailer: e.outbox, Log: log}
	bea, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "bea@example.com", Name: "Bea", Password: "correct horse battery", WorkspaceName: "Bea's"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, bea); err != nil {
		t.Fatal(err)
	}
	captured := track.Capture(t)
	owner.must(200, "PUT", base+"/routing", map[string]any{"rules": []any{
		map[string]any{"name": "Everyone", "record_type": "account", "members": []string{me, bea}},
	}})
	n := 0
	route := func() map[string]any {
		t.Helper()
		n++
		rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": fmt.Sprintf("Company %d", n)})["id"].(string)
		return owner.must(200, "POST", base+"/records/"+rec+"/routing", nil)
	}
	people := func(list ...map[string]any) map[string]any {
		t.Helper()
		if list == nil {
			list = []map[string]any{}
		}
		return owner.must(200, "PUT", base+"/routing/people", map[string]any{"people": list})
	}

	// Away: the first in the pool is skipped.
	out := people(map[string]any{"user_id": me, "paused": true})
	who := out["people"].([]any)
	if len(who) != 1 || who[0].(map[string]any)["paused"] != true || who[0].(map[string]any)["at_work"] != true {
		t.Fatalf("people: %v", who)
	}
	if got := route(); got["owner_id"] != bea {
		t.Fatalf("away: %v", got)
	}

	// Capacity: Bea owns one and takes no more, so the next goes to me
	// though I would otherwise be level; at capacity both, nobody gets it.
	people(map[string]any{"user_id": bea, "capacity": 1})
	if got := route(); got["owner_id"] != me {
		t.Fatalf("capacity: %v", got)
	}
	out = people(map[string]any{"user_id": bea, "capacity": 1}, map[string]any{"user_id": me, "capacity": 1})
	for _, p := range out["people"].([]any) {
		if p.(map[string]any)["owned"] != float64(1) {
			t.Fatalf("owned: %v", p)
		}
	}
	if got := route(); got["outcome"] != "no_member" {
		t.Fatalf("both full: %v", got)
	}

	// Hours: I own fewer after Bea gets two more, but I am off (my hours
	// are a minute that isn't now), so the next still goes to Bea; with
	// both off it goes to whoever owns the fewest, so nothing waits.
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = attributes || jsonb_build_object('owner', $2::text) WHERE brand_id = $1 AND record_type = 'account' AND COALESCE(attributes->>'owner', '') = ''`, brand, bea); err != nil {
		t.Fatal(err)
	}
	var off string
	if err := e.pool.QueryRow(ctx, `SELECT to_char(now() AT TIME ZONE 'UTC' + interval '6 hours', 'HH24:MI')`).Scan(&off); err != nil {
		t.Fatal(err)
	}
	var offEnd string
	if err := e.pool.QueryRow(ctx, `SELECT to_char(now() AT TIME ZONE 'UTC' + interval '7 hours', 'HH24:MI')`).Scan(&offEnd); err != nil {
		t.Fatal(err)
	}
	out = people(map[string]any{"user_id": me, "from": off, "to": offEnd, "timezone": "UTC"})
	if p := out["people"].([]any)[0].(map[string]any); p["at_work"] != false || p["from"] != off {
		t.Fatalf("off: %v", p)
	}
	if got := route(); got["owner_id"] != bea {
		t.Fatalf("hours: %v", got)
	}
	people(map[string]any{"user_id": me, "from": off, "to": offEnd}, map[string]any{"user_id": bea, "from": off, "to": offEnd})
	if got := route(); got["owner_id"] != me {
		t.Fatalf("both off: %v", got)
	}

	// Bad input is refused, and an empty list clears everything.
	for _, bad := range []map[string]any{
		{"user_id": me, "from": "09:00"},
		{"user_id": me, "from": "09:00", "to": "09:00"},
		{"user_id": me, "days": []int{8}},
		{"user_id": me, "timezone": "Mars/Olympus"},
		{"user_id": "00000000-0000-4000-8000-000000000000"},
	} {
		owner.must(400, "PUT", base+"/routing/people", map[string]any{"people": []any{bad}})
	}
	if out := people(); len(out["people"].([]any)) != 0 {
		t.Fatalf("cleared: %v", out)
	}
	found := false
	for _, ev := range captured() {
		found = found || ev.Name == "routing.people_updated"
	}
	if !found {
		t.Fatal("no routing.people_updated event")
	}
}
