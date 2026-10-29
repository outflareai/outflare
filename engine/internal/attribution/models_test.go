package attribution

import (
	"math"
	"testing"
	"time"
)

func TestSpecWeights(t *testing.T) {
	day := 24 * time.Hour
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	touches := []Point{
		{At: at.Add(-40 * day), Channel: "seo"},
		{At: at.Add(-10 * day), Channel: "paid_ads"},
		{At: at.Add(-3 * day), Channel: "lifecycle_email"},
		{At: at.Add(-1 * day), Channel: "direct", Direct: true},
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	f := func(x float64) *float64 { return &x }

	// The workspace's 30 days leave out the seo visit.
	if w := (Spec{Base: FirstTouch, LookbackDays: 30}).Weights(touches, at); w[0] != 0 || w[1] != 1 {
		t.Errorf("30 day first touch: %v", w)
	}
	// Email left out: last non-direct is paid ads.
	if w := (Spec{Base: LastNonDirect, ExcludeChannels: []string{"lifecycle_email"}}).Weights(touches, at); w[1] != 1 {
		t.Errorf("email excluded: %v", w)
	}
	// Paid ads only count for 7 days: last non-direct over 30 days is email,
	// and first touch is email too.
	paid7 := Spec{Base: FirstTouch, LookbackDays: 30, ChannelLookbackDays: map[string]int{"paid_ads": 7}}
	if w := paid7.Weights(touches, at); w[2] != 1 {
		t.Errorf("paid ads 7 days: %v", w)
	}
	// A seo window longer than the model's brings the seo visit back.
	if w := (Spec{Base: FirstTouch, LookbackDays: 30, ChannelLookbackDays: map[string]int{"seo": 60}}).Weights(touches, at); w[0] != 1 {
		t.Errorf("seo 60 days: %v", w)
	}
	// Position 30/50 over three non-direct touches: the middle gets 20%.
	pos := Spec{Base: Position, FirstWeight: f(0.3), LastWeight: f(0.5)}
	if w := pos.Weights(touches, at); !near(w[0], 0.3) || !near(w[1], 0.2) || !near(w[2], 0.5) || w[3] != 0 {
		t.Errorf("position 30/50: %v", w)
	}
	// With two touches they share in the same proportion.
	if w := pos.Weights(touches[1:3], at); !near(w[0], 0.375) || !near(w[1], 0.625) {
		t.Errorf("position 30/50 over two: %v", w)
	}
	// A one-day half-life all but ignores the 10-day-old click.
	if w := (Spec{Base: TimeDecay, HalfLifeDays: 1}).Weights(touches[1:3], at); w[0] > 0.01 {
		t.Errorf("one day half-life: %v", w)
	}
	// Everything left out: the conversion goes to direct.
	if w := (Spec{Base: Linear, LookbackDays: 1}).Weights(touches[:3], at); w != nil {
		t.Errorf("nothing in the window: %v", w)
	}
}

func TestSpecValidate(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	ok := []Spec{
		{Base: LastNonDirect},
		{Base: Position, FirstWeight: f(0.5), LastWeight: f(0.5)},
		{Base: TimeDecay, HalfLifeDays: 3, LookbackDays: 60, ChannelLookbackDays: map[string]int{"paid_ads": 7}, ExcludeChannels: []string{"direct"}},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []Spec{
		{Base: "w_shaped"},
		{Base: Linear, LookbackDays: 400},
		{Base: Linear, ChannelLookbackDays: map[string]int{"tv": 7}},
		{Base: Linear, ChannelLookbackDays: map[string]int{"seo": 0}},
		{Base: Linear, ExcludeChannels: []string{"tv"}},
		{Base: Linear, FirstWeight: f(0.5)},
		{Base: Position, FirstWeight: f(0.7), LastWeight: f(0.5)},
		{Base: Position, FirstWeight: f(0), LastWeight: f(0)},
		{Base: Linear, HalfLifeDays: 3},
		{Base: TimeDecay, HalfLifeDays: 0.1},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v passed", s)
		}
	}
}
