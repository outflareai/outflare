package rules

import (
	"slices"
	"testing"
)

func TestCheck(t *testing.T) {
	ok := Rule{RecordType: "person", Match: "all", Fire: "every_time", Emit: "customer.stalled",
		Trigger: Trigger{Kind: KindAbsence, StartEvent: "signed_up", Event: "product_activated", WithinSeconds: 3600}}
	if msg := Check(ok); msg != "" {
		t.Fatalf("a good rule: %s", msg)
	}
	for name, change := range map[string]func(*Rule){
		"bad emit":            func(r *Rule) { r.Emit = "Stalled" },
		"emits what it hears": func(r *Rule) { r.Emit = "signed_up" },
		"too short":           func(r *Rule) { r.Trigger.WithinSeconds = 5 },
		"list on absence":     func(r *Rule) { r.Trigger.ListID = "x" },
		"deal absence":        func(r *Rule) { r.RecordType = "deal" },
		"email condition":     func(r *Rule) { r.Conditions = []Condition{{Email: "opened", Step: "a", Op: "exists"}} },
		"change with event":   func(r *Rule) { r.Trigger = Trigger{Kind: KindChange, Event: "x"} },
		"empty change":        func(r *Rule) { r.Trigger = Trigger{Kind: KindChange} },
		"audience bad list":   func(r *Rule) { r.Trigger = Trigger{Kind: KindAudience, ListID: "x", On: "enter"} },
		"bad fire":            func(r *Rule) { r.Fire = "sometimes" },
		"unknown kind":        func(r *Rule) { r.Trigger.Kind = "teleport" },
	} {
		r := ok
		change(&r)
		if Check(r) == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	deal := Rule{RecordType: "deal", Match: "all", Fire: "once", Emit: "deal.closed_won", Trigger: Trigger{Kind: KindChange},
		Conditions: []Condition{{Attribute: "stage", Op: "eq", Value: "won"}, {Attribute: "value.amount_minor", Op: "gt", Value: 100}}}
	if msg := Check(deal); msg != "" {
		t.Fatalf("a change rule on deals: %s", msg)
	}
	if got := WatchFields(deal.Trigger, deal.Conditions); !slices.Equal(got, []string{"stage", "value"}) {
		t.Errorf("watch fields = %v", got)
	}
	if got := WatchEvents(ok.Trigger); !slices.Equal(got, []string{"signed_up", "product_activated"}) {
		t.Errorf("watch events = %v", got)
	}
}

func TestLoops(t *testing.T) {
	rule := func(id, hears, emits string) Rule {
		return Rule{ID: id, Emit: emits, Trigger: Trigger{Kind: KindEvent, Event: hears}}
	}
	active := []Rule{rule("a", "x", "y"), rule("b", "y", "z")}
	if !Loops(rule("c", "z", "x"), active) {
		t.Error("x→y→z→x is a loop")
	}
	if Loops(rule("c", "z", "w"), active) {
		t.Error("z→w is no loop")
	}
	// A rule being re-activated doesn't count itself twice.
	if Loops(rule("a", "x", "y"), active) {
		t.Error("a with itself in the list is no loop")
	}
}
