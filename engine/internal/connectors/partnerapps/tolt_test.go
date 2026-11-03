package partnerapps

import (
	"errors"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestDecodeToltList(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		n          int
		more       bool
	}{
		{"flat", `{"data": [{"id": "a"}, {"id": "b"}], "has_more": true}`, 2, true},
		{"nested", `{"success": true, "data": {"data": [{"id": "a"}], "has_more": "false"}, "total_count": 1}`, 1, false},
		{"bare", `[{"id": "a"}, 7]`, 1, false},
		{"named", `{"customers": [{"id": "a"}], "has_more": 1}`, 1, true},
		{"empty", `{"success": true, "data": null, "has_more": false, "next_cursor": ""}`, 0, false},
	} {
		l, err := decodeToltList("customers", 200, "application/json", []byte(tc.body))
		if err != nil || len(l.Data) != tc.n || (l.HasMore != nil && *l.HasMore) != tc.more {
			t.Errorf("%s: %d objects, more %v, err %v", tc.name, len(l.Data), l.HasMore, err)
		}
	}
	_, err := decodeToltList("customers", 200, "text/html; charset=utf-8", []byte("<html>ana@acme.example</html>"))
	var ue *c.UnreadableError
	if !errors.As(err, &ue) || ue.Message != "reading customers from Tolt (HTTP 200, text/html): the answer is not JSON" {
		t.Errorf("html: %v", err)
	}
	_, err = decodeToltList("customers", 200, "application/json", []byte(`{"success": false, "message": "program not found", "code": 4}`))
	if !errors.As(err, &ue) || !strings.Contains(ue.Message, "keys code: number, message: string, success: boolean") ||
		!strings.HasSuffix(ue.Message, "Tolt says program not found") {
		t.Errorf("no list: %v", err)
	}
	if s := toltSnippet([]byte("<p>write to ana@acme.example</p>")); strings.Contains(s, "@") {
		t.Errorf("snippet = %q", s)
	}
}

// TestToltFlowActions: a flow's percentage reward is read however Tolt
// words it, its "created within 1 year" condition maps to the customer's
// sign-up date, and an action it still can't read names its settings.
func TestToltFlowActions(t *testing.T) {
	cond := map[string]any{"operator": "and", "conditions": []any{
		map[string]any{"attribute": "customer_transactions_count", "operator": "equals", "value": "1"},
		map[string]any{"attribute": "created_at", "operator": "within", "value": 1.0, "unit": "year"},
	}}
	for name, action := range map[string]map[string]any{
		"commission_type":  {"type": "commission", "commission_type": "percentage", "value": 10.0},
		"type is percent":  {"type": "percentage", "value": "10"},
		"percentage field": {"type": "generate_commission", "percentage": 10.0},
		"nested":           {"type": "commission", "commission": map[string]any{"type": "percent", "value": 10.0}},
		"calculation":      {"calculation": "percentage_of_transaction", "rate": 10.0},
	} {
		f := toltFlow(map[string]any{"id": "flw_1", "name": "10% First Time", "trigger": "new_payment", "action": action, "conditions": cond,
			"approval": map[string]any{"type": "manual"}})
		if len(f.Unsupported) != 0 || f.Kind != "percent" || f.PercentBP == nil || *f.PercentBP != 1000 {
			t.Errorf("%s: kind %q bp %v unsupported %v (%s)", name, f.Kind, f.PercentBP, f.Unsupported, f.Detail)
			continue
		}
		if f.Approval != "manual" || len(f.Conditions.Groups) != 1 || len(f.Conditions.Groups[0].Conditions) != 2 {
			t.Errorf("%s: approval %q conditions %+v", name, f.Approval, f.Conditions)
		}
		if w := f.Conditions.Groups[0].Conditions[1]; w.Field != "customer.signed_up_at" || w.Value.(map[string]any)["unit"] != "years" {
			t.Errorf("%s: window = %+v", name, w)
		}
	}

	if v := toltFlow(map[string]any{"id": "flw_1", "action": map[string]any{"type": "percentage", "value": 10.0}, "conditions": cond}); v.Conditions.Groups[0].Conditions[0].Value != 0.0 {
		t.Errorf("Tolt's first payment = %+v", v.Conditions.Groups[0].Conditions[0])
	}

	f := toltFlow(map[string]any{"id": "flw_2", "trigger": "new_payment", "action": map[string]any{"type": "bonus_tier", "tiers": []any{1.0, 2.0}, "label": "Tier bonus for Ana"}})
	if len(f.Unsupported) != 1 || f.Unsupported[0] != "action" {
		t.Fatalf("unsupported = %v", f.Unsupported)
	}
	if want := `its action: label: string, tiers: array, type: "bonus_tier"`; f.Detail != want {
		t.Errorf("detail = %q, want %q", f.Detail, want)
	}
}

// TestToltFlowV2: flows as Tolt's v2 API sends them (docs.tolt.com, "Get a
// flow"): the event, action_type, {type, value} action, nested condition
// groups with children, and options.auto_approval.
func TestToltFlowV2(t *testing.T) {
	flow := func(name, op, count string, extra map[string]any) map[string]any {
		o := map[string]any{"id": "flw_" + name, "name": name, "event": "transaction_created", "action_type": "generate_commission",
			"action": map[string]any{"type": "percentage", "value": 10.0},
			"conditions": map[string]any{"kind": "group", "operator": "AND", "children": []any{
				map[string]any{"kind": "condition", "type": "customer_transaction_count", "operator": op, "value": count},
				map[string]any{"kind": "condition", "type": "customer_created_date", "operator": "within", "value": "1", "unit": "years"},
			}},
			"program_id": "prg_1", "group_id": "grp_default", "campaign_id": nil, "active": true,
			"partner_overrides": []any{}, "link_overrides": []any{},
			"options": map[string]any{"auto_approval": map[string]any{"type": "manual", "delay_days": 0.0}},
		}
		for k, v := range extra {
			o[k] = v
		}
		return o
	}

	first := toltFlow(flow("first", "equals", "1", nil))
	if len(first.Unsupported) != 0 || first.Trigger != "sale" || first.Kind != "percent" || *first.PercentBP != 1000 ||
		first.Approval != "manual" || first.GroupID != "grp_default" || first.Paused {
		t.Fatalf("first = %+v", first)
	}
	if g := first.Conditions; g.Match != "all" || len(g.Groups) != 1 || g.Groups[0].Match != "all" || len(g.Groups[0].Conditions) != 2 {
		t.Fatalf("conditions = %+v", g)
	}
	// "Equals 1" in Tolt is the first payment: none before it here.
	if c := first.Conditions.Groups[0].Conditions[0]; c.Field != "customer.transactions" || c.Op != "eq" || c.Value != 0.0 {
		t.Errorf("count = %+v", c)
	}
	if c := first.Conditions.Groups[0].Conditions[1]; c.Field != "customer.signed_up_at" || c.Op != "within" ||
		c.Value.(map[string]any)["amount"] != 1.0 || c.Value.(map[string]any)["unit"] != "years" {
		t.Errorf("window = %+v", c)
	}
	if err := first.Conditions.Check(); err != nil {
		t.Error(err)
	}

	after := toltFlow(flow("after", "greater_than", "1", map[string]any{"action": map[string]any{"type": "percentage", "value": 2.0}}))
	if c := after.Conditions.Groups[0].Conditions[0]; len(after.Unsupported) != 0 || *after.PercentBP != 200 || c.Op != "gt" || c.Value != 0.0 {
		t.Errorf("after = %+v %+v", after, c)
	}

	// Signups, a delay, a fixed amount, a group move, OR of nested groups.
	signup := toltFlow(map[string]any{"id": "flw_s", "event": "lead_created", "action_type": "generate_commission",
		"action": map[string]any{"type": "fixed", "value": 20.0}, "conditions": nil,
		"options": map[string]any{"auto_approval": map[string]any{"type": "delay", "delay_days": 30.0}}})
	if len(signup.Unsupported) != 0 || signup.Trigger != "signup" || signup.Kind != "fixed" || signup.Approval != "after_days" || *signup.ApprovalDays != 30 {
		t.Errorf("signup = %+v", signup)
	}
	move := toltFlow(map[string]any{"id": "flw_m", "event": "transaction_created", "action_type": "move_to_group",
		"action": map[string]any{"group_id": "grp_vip"}})
	if len(move.Unsupported) != 0 || move.Kind != "group" || move.TargetGroupID != "grp_vip" {
		t.Errorf("move = %+v", move)
	}
	nested := toltFlow(map[string]any{"id": "flw_n", "event": "transaction_created", "action": map[string]any{"type": "percentage", "value": 30.0},
		"conditions": map[string]any{"kind": "group", "operator": "OR", "children": []any{
			map[string]any{"kind": "group", "operator": "AND", "children": []any{
				map[string]any{"kind": "condition", "type": "customer_conversion_date", "operator": "within", "value": 350.0},
			}},
			map[string]any{"kind": "condition", "type": "partner_id", "operator": "equals", "value": "part_1"},
		}}})
	if g := nested.Conditions; len(nested.Unsupported) != 0 || g.Match != "any" || len(g.Groups) != 2 ||
		g.Groups[0].Conditions[0].Field != "customer.first_sale_at" || g.Groups[0].Conditions[0].Value.(map[string]any)["unit"] != "days" {
		t.Errorf("nested = %+v %v", g, nested.Unsupported)
	}

	if p := toltFlow(flow("paused", "equals", "1", map[string]any{"active": false})); !p.Paused {
		t.Error("a paused flow came over as running")
	}
	if l := toltFlow(flow("link", "equals", "1", map[string]any{"link_overrides": []any{map[string]any{"link_id": "lnk_1", "value": 5.0}}})); len(l.Unsupported) == 0 {
		t.Error("a link override was dropped silently")
	}
	if u := toltFlow(flow("odd", "equals", "1", map[string]any{"event": "click_created"})); len(u.Unsupported) != 1 || u.Unsupported[0] != "trigger" ||
		u.Detail != `its event: event: "click_created", trigger: null` {
		t.Errorf("odd event = %v %q", u.Unsupported, u.Detail)
	}
}
