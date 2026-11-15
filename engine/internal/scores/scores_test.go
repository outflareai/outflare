package scores

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

func TestScore(t *testing.T) {
	for _, d := range Starters {
		if msg := Check(d); msg != "" {
			t.Fatalf("starter %s: %s", d.Key, msg)
		}
	}
	d := Starters[0]
	// Nothing known: only "no usage" holds.
	if v, band, held := d.Score(map[string]any{}); v != 35 || band != Low || !held[4] || held[0] {
		t.Fatalf("an empty record: %d %s %v", v, band, held)
	}
	// Using it more, and paying.
	attrs := map[string]any{"metrics": map[string]any{"usage_7d": 40.0, "usage_7d_change": 25.0, "payments_90d": 2.0}}
	if v, band, _ := d.Score(attrs); v != 100 || band != High {
		t.Fatalf("a healthy record: %d %s", v, band)
	}
	// Never under 0.
	low := Definition{Key: "x", Label: "X", RecordType: "account", Base: 10, HighFrom: 70, LowBelow: 40,
		Factors: []Factor{{Label: "Always", Match: "all", Points: -50, Conditions: []journeys.Condition{{Attribute: "plan", Op: "not_exists"}}}}}
	if v, band, _ := low.Score(nil); v != 0 || band != Low {
		t.Fatalf("clamped: %d %s", v, band)
	}
	if got := WatchFields(d); len(got) != 1 || got[0] != "metrics" {
		t.Fatalf("watch fields: %v", got)
	}
	for _, bad := range []func(*Definition){
		func(d *Definition) { d.Key = "health_band" },
		func(d *Definition) { d.Factors = nil },
		func(d *Definition) { d.LowBelow = d.HighFrom },
		func(d *Definition) {
			d.Factors = []Factor{{Label: "x", Match: "all", Points: 0, Conditions: d.Factors[0].Conditions}}
		},
		func(d *Definition) {
			d.Factors = []Factor{{Label: "x", Match: "all", Points: 5, Conditions: []journeys.Condition{{Attribute: "scores.health", Op: "gt", Value: 1}}}}
		},
		func(d *Definition) { d.Nature = "guess" },
		func(d *Definition) { d.Parts = []Part{{Key: "usage", Weight: 1}, {Key: "billing", Weight: 1}} },
		func(d *Definition) { d.Factors, d.Parts = nil, []Part{{Key: "usage", Weight: 1}} },
		func(d *Definition) {
			d.Factors = []Factor{{Label: "x", Match: "all", Points: 5, Conditions: []journeys.Condition{{Attribute: "deal.days_open", Op: "gt", Value: 1}}}}
		},
	} {
		c := Starters[0]
		bad(&c)
		if Check(c) == "" {
			t.Fatalf("accepted: %+v", c)
		}
	}
}

func byKey(key string) Definition {
	for _, d := range Starters {
		if d.Key == key {
			return d
		}
	}
	panic(key)
}

func TestHeadlineScores(t *testing.T) {
	usage, billing, sentiment, churn := byKey("usage"), byKey("billing"), byKey("sentiment"), byKey("churn_risk")
	health := Definition{Key: "overall", Label: "Overall", RecordType: "account", HighFrom: 70, LowBelow: 40, Nature: AI, HigherIsBetter: true,
		Parts: []Part{{Key: "usage", Weight: 2}, {Key: "billing", Weight: 1}, {Key: "sentiment", Weight: 1}}}
	if msg := Check(health); msg != "" {
		t.Fatal(msg)
	}
	set := []Definition{usage, billing, sentiment, churn}
	if msg := CheckSet(health, set); msg != "" {
		t.Fatal(msg)
	}
	// Measured can't be made of what the AI read, or of a missing score.
	m := health
	m.Nature = Measured
	if CheckSet(m, set) == "" {
		t.Fatal("a measured score made of sentiment")
	}
	if CheckSet(health, []Definition{usage, billing}) == "" {
		t.Fatal("a part that doesn't exist")
	}
	// A score that reads the headline score is fine; a headline score
	// can't then become a part of anything.
	reads := Definition{Key: "watch", Label: "Watch", RecordType: "account", Base: 10, HighFrom: 70, LowBelow: 40, Nature: Predicted,
		Factors: []Factor{{Label: "Overall is low", Match: "all", Points: 50, Conditions: cond("scores.overall_band", "eq", "low")}}}
	all := append(set, health)
	if msg := CheckSet(reads, all); msg != "" {
		t.Fatal(msg)
	}
	other := Definition{Key: "two", Label: "Two", RecordType: "account", HighFrom: 70, LowBelow: 40, Nature: Predicted,
		Parts: []Part{{Key: "overall", Weight: 1}, {Key: "usage", Weight: 1}}}
	if CheckSet(other, all) == "" {
		t.Fatal("a headline score as a part")
	}
	// Turning usage into a score that reads scores breaks the headline.
	u := usage
	u.Factors = append([]Factor{}, u.Factors...)
	u.Factors = append(u.Factors, Factor{Label: "x", Match: "all", Points: 1, Conditions: cond("scores.billing", "gt", 1)})
	if CheckSet(u, append(all, reads)) == "" {
		t.Fatal("a part that reads scores")
	}

	// No conversations read: sentiment has no value and the headline is
	// made of the other two; usage counts double.
	attrs := map[string]any{"metrics": map[string]any{"usage_7d": 40.0, "usage_7d_change": 25.0, "payments_90d": 2.0}}
	got := All(append(all, reads), attrs)
	if _, ok := got["sentiment"]; ok {
		t.Fatalf("sentiment with nothing read: %+v", got)
	}
	if got["usage"].Value != 100 || got["billing"].Value != 75 || got["overall"].Value != 92 || got["overall"].Band != High {
		t.Fatalf("scores: %+v", got)
	}
	if got["watch"].Value != 10 {
		t.Fatalf("watch: %+v", got["watch"])
	}
	// An unhappy customer pulls the headline down and churn risk up.
	attrs["sentiment"] = map[string]any{"value": 20.0, "label": "negative", "trend": "down"}
	got = All(append(all, reads), attrs)
	if got["sentiment"].Value != 0 || got["overall"].Value != 69 || got["churn_risk"].Value != 35 {
		t.Fatalf("unhappy: %+v", got)
	}
}
