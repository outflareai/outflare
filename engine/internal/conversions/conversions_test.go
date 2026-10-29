package conversions

import (
	"encoding/json"
	"testing"
)

func props(t *testing.T, raw string) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMatch(t *testing.T) {
	call := Definition{Event: "call.completed", Filters: []Filter{{Property: "duration_s", Op: OpGt, Value: 60.0}, {Property: "direction", Op: OpEq, Value: "Inbound"}}}
	for raw, want := range map[string]bool{
		`{"duration_s": 95, "direction": "inbound"}`:   true,
		`{"duration_s": "95", "direction": "INBOUND"}`: true,
		`{"duration_s": 60, "direction": "inbound"}`:   false,
		`{"duration_s": 95, "direction": "outbound"}`:  false,
		`{"direction": "inbound"}`:                     false,
	} {
		if got := call.Match(props(t, raw)); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
	plan := Definition{Filters: []Filter{{Property: "plan.tier", Op: OpIn, Value: []any{"pro", "team"}}, {Property: "test", Op: OpNotExists}}}
	if !plan.Match(props(t, `{"plan": {"tier": "Team"}}`)) || plan.Match(props(t, `{"plan": {"tier": "free"}}`)) || plan.Match(props(t, `{"plan": {"tier": "pro"}, "test": true}`)) {
		t.Error("nested in / not_exists")
	}
	other := Definition{Filters: []Filter{{Property: "utm", Op: OpContains, Value: "Spring"}, {Property: "coupon", Op: OpExists}, {Property: "plan", Op: OpNeq, Value: "free"}}}
	if !other.Match(props(t, `{"utm": "spring-launch", "coupon": "X", "plan": "pro"}`)) || other.Match(props(t, `{"utm": "spring-launch", "coupon": "X", "plan": "free"}`)) {
		t.Error("contains / exists / neq")
	}
	if !(Definition{}).Match(nil) {
		t.Error("no filters matches everything")
	}
}

func TestValue(t *testing.T) {
	fixed := int64(15000)
	for name, c := range map[string]struct {
		d     Definition
		raw   string
		value *int64
		cur   string
	}{
		"none":            {Definition{ValueMode: ValueNone}, `{"currency": "eur"}`, nil, "EUR"},
		"fixed":           {Definition{ValueMode: ValueFixed, FixedValueMinor: &fixed, Currency: "USD"}, `{"currency": "eur"}`, &fixed, "USD"},
		"major":           {Definition{ValueMode: ValueProperty, ValueProperty: "revenue", ValueUnit: "major"}, `{"revenue": 49.99, "currency": "usd"}`, ptr(4999), "USD"},
		"minor, a string": {Definition{ValueMode: ValueProperty, ValueProperty: "amount_minor", ValueUnit: "minor", Currency: "GBP"}, `{"amount_minor": "1250"}`, ptr(1250), "GBP"},
		"nested":          {Definition{ValueMode: ValueProperty, ValueProperty: "order.total", ValueUnit: "major"}, `{"order": {"total": 10}}`, ptr(1000), ""},
		"missing":         {Definition{ValueMode: ValueProperty, ValueProperty: "revenue", ValueUnit: "major"}, `{}`, nil, ""},
	} {
		v, cur := c.d.Value(props(t, c.raw))
		if (v == nil) != (c.value == nil) || (v != nil && *v != *c.value) || cur != c.cur {
			t.Errorf("%s: %v %q", name, v, cur)
		}
	}
}

func ptr(v int64) *int64 { return &v }

func TestValidate(t *testing.T) {
	ok := Definition{Key: "held_call", Name: "Held call", Event: "meeting.held", Filters: []Filter{{Property: "source", Op: OpIn, Value: []any{"calendly"}}},
		NetworkEvents: map[string]string{"meta": " Schedule "}}
	if err := ok.Validate(); err != nil || ok.ValueMode != ValueNone || ok.ValueUnit != "major" || ok.NetworkEvents["meta"] != "Schedule" {
		t.Fatalf("%v %+v", err, ok)
	}
	fixed := int64(100)
	bad := []Definition{
		{Key: "Held", Name: "x", Event: "meeting.held"},
		{Key: "a", Name: "", Event: "meeting.held"},
		{Key: "a", Name: "x", Event: "Meeting Held"},
		{Key: "a", Name: "x", Event: "payment.refunded"},
		{Key: "a", Name: "x", Event: "e", Filters: []Filter{{Property: "a b", Op: OpEq, Value: 1.0}}},
		{Key: "a", Name: "x", Event: "e", Filters: []Filter{{Property: "a", Op: "like", Value: 1.0}}},
		{Key: "a", Name: "x", Event: "e", Filters: []Filter{{Property: "a", Op: OpGt, Value: "many"}}},
		{Key: "a", Name: "x", Event: "e", Filters: []Filter{{Property: "a", Op: OpIn, Value: "pro"}}},
		{Key: "a", Name: "x", Event: "e", Filters: []Filter{{Property: "a", Op: OpEq}}},
		{Key: "a", Name: "x", Event: "e", ValueMode: ValueFixed},
		{Key: "a", Name: "x", Event: "e", ValueMode: ValueFixed, FixedValueMinor: &fixed},
		{Key: "a", Name: "x", Event: "e", ValueMode: ValueProperty},
		{Key: "a", Name: "x", Event: "e", ValueMode: "guess"},
		{Key: "a", Name: "x", Event: "e", ValueMode: ValueProperty, ValueProperty: "v", ValueUnit: "cents"},
		{Key: "a", Name: "x", Event: "e", Currency: "usd"},
		{Key: "a", Name: "x", Event: "e", NetworkEvents: map[string]string{"Meta": "Purchase"}},
		{Key: "a", Name: "x", Event: "e", NetworkEvents: map[string]string{"meta": " "}},
	}
	for i, d := range bad {
		if err := d.Validate(); err == nil {
			t.Errorf("bad %d passed: %+v", i, d)
		}
	}
}

func TestPresetsAreValid(t *testing.T) {
	ids := map[string]bool{}
	for _, p := range Presets {
		if ids[p.ID] {
			t.Errorf("preset %s twice", p.ID)
		}
		ids[p.ID] = true
		keys := map[string]bool{}
		for _, d := range p.Definitions {
			if err := d.Validate(); err != nil {
				t.Errorf("%s/%s: %v", p.ID, d.Key, err)
			}
			if keys[d.Key] {
				t.Errorf("%s: key %s twice", p.ID, d.Key)
			}
			keys[d.Key] = true
		}
	}
	if _, ok := PresetOf("booked_calls"); !ok {
		t.Error("no booked_calls preset")
	}
}

func TestFor(t *testing.T) {
	defs := []Definition{
		{Key: "long_call", Event: "call.completed", Filters: []Filter{{Property: "duration_s", Op: OpGte, Value: 60.0}}},
		{Key: "any_call", Event: "call.completed"},
	}
	if d := For(defs, "call.completed", props(t, `{"duration_s": 90}`)); d == nil || d.Key != "long_call" {
		t.Errorf("long call: %+v", d)
	}
	if d := For(defs, "call.completed", props(t, `{"duration_s": 5}`)); d == nil || d.Key != "any_call" {
		t.Errorf("short call: %+v", d)
	}
	if For(defs, "signed_up", nil) != nil {
		t.Error("other event matched")
	}
}
