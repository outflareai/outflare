package partners

import (
	"testing"
	"time"
)

func TestFlowConditions(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	facts := Facts{
		"partner.revenue_cents":  float64(150000),
		"partner.country":        "DE",
		"customer.first_sale_at": now.AddDate(0, -2, 0),
		"transaction.recurring":  true,
		"transaction.product_id": "pro",
	}
	cond := func(field, op string, v any) Condition { return Condition{Field: field, Op: op, Value: v} }
	cases := map[string]struct {
		cs   Conditions
		want bool
	}{
		"none":                {Conditions{}, true},
		"number":              {Conditions{Groups: []ConditionGroup{{Conditions: []Condition{cond("partner.revenue_cents", "gte", 100000.0)}}}}, true},
		"text in, any case":   {Conditions{Groups: []ConditionGroup{{Conditions: []Condition{cond("partner.country", "in", []any{"de", "at"})}}}}, true},
		"within 3 months":     {Conditions{Groups: []ConditionGroup{{Conditions: []Condition{cond("customer.first_sale_at", "within", map[string]any{"amount": 3.0, "unit": "months"})}}}}, true},
		"not within 30 days":  {Conditions{Groups: []ConditionGroup{{Conditions: []Condition{cond("customer.first_sale_at", "within", map[string]any{"amount": 30.0, "unit": "days"})}}}}, false},
		"missing fact":        {Conditions{Groups: []ConditionGroup{{Conditions: []Condition{cond("partner.leads", "gt", 0.0)}}}}, false},
		"missing fact is neq": {Conditions{Groups: []ConditionGroup{{Conditions: []Condition{cond("partner.id", "neq", "x")}}}}, true},
		"all fails on one": {Conditions{Groups: []ConditionGroup{{Conditions: []Condition{
			cond("transaction.recurring", "eq", true), cond("transaction.product_id", "eq", "basic")}}}}, false},
		"any group": {Conditions{Match: "any", Groups: []ConditionGroup{
			{Conditions: []Condition{cond("transaction.product_id", "eq", "basic")}},
			{Match: "any", Conditions: []Condition{cond("partner.country", "eq", "FR"), cond("transaction.recurring", "eq", true)}}}}, true},
	}
	for name, tc := range cases {
		if err := tc.cs.Check(); err != nil {
			t.Errorf("%s: check: %v", name, err)
		}
		if got := tc.cs.Holds(facts, now); got != tc.want {
			t.Errorf("%s = %v, want %v", name, got, tc.want)
		}
	}
	for name, bad := range map[string]Condition{
		"unknown field": cond("partner.mood", "eq", "x"),
		"wrong op":      cond("partner.country", "gt", "x"),
		"not a number":  cond("partner.leads", "gt", "x"),
		"bad span":      cond("customer.signed_up_at", "within", map[string]any{"amount": 3.0, "unit": "weeks"}),
	} {
		if err := (Conditions{Groups: []ConditionGroup{{Conditions: []Condition{bad}}}}).Check(); err == nil {
			t.Errorf("%s passed the check", name)
		}
	}
}

func TestFlowRewards(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ana, pro, gold := "ana", "pro", "gold"
	base := Rule{ID: "base", Trigger: "sale", Side: "partner", Kind: "percent", PercentBP: ptr(2000), Recurrence: "every",
		Overrides: []Override{{PartnerID: &ana, PercentBP: ptr(4000)}, {ProductID: &pro, PercentBP: ptr(3000)}}}
	bonus := Rule{ID: "bonus", Trigger: "sale", Side: "partner", Kind: "fixed", AmountCents: ptr(int64(500)), Recurrence: "every", Stacking: StackAdds,
		Conditions: Conditions{Groups: []ConditionGroup{{Conditions: []Condition{{Field: "partner.transactions", Op: "gte", Value: 10.0}}}}}}
	promote := Rule{ID: "promote", Trigger: "sale", Side: "partner", Kind: "group", TargetGroupID: &gold,
		Conditions: Conditions{Groups: []ConditionGroup{{Conditions: []Condition{{Field: "partner.revenue_cents", Op: "gte", Value: 100000.0}}}}}}
	rules := []Rule{base, bonus, promote}
	pays := func(c Conversion) map[string]int64 {
		out := map[string]int64{}
		for _, e := range Rewards(rules, c) {
			out[e.Rule.ID+":"+e.Kind] = e.AmountCents
		}
		return out
	}
	sale := func(partner, product string, facts Facts) Conversion {
		return Conversion{Trigger: "sale", AmountCents: 10000, PartnerID: partner, ProductID: product, Facts: facts, At: now}
	}
	if got := pays(sale("bo", "", Facts{})); len(got) != 1 || got["base:cash"] != 2000 {
		t.Fatalf("plain sale = %v", got)
	}
	if got := pays(sale("bo", "pro", Facts{})); got["base:cash"] != 3000 {
		t.Fatalf("product override = %v", got)
	}
	if got := pays(sale("ana", "pro", Facts{})); got["base:cash"] != 4000 {
		t.Fatalf("partner override beats product = %v", got)
	}
	got := pays(sale("bo", "", Facts{"partner.transactions": 12.0, "partner.revenue_cents": 200000.0}))
	if got["base:cash"] != 2000 || got["bonus:cash"] != 500 || len(got) != 3 {
		t.Fatalf("bonus adds, promotion = %v", got)
	}
	if _, ok := got["promote:group"]; !ok {
		t.Fatalf("no group move: %v", got)
	}
}
