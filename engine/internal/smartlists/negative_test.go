package smartlists

import "testing"

func TestNegative(t *testing.T) {
	did := Rule{Event: "signed_up", Op: "exists"}
	didnt := Rule{Event: "paid", Op: "not_exists"}
	isnt := Rule{Attribute: "plan", Op: "neq", Value: "pro"}
	group := func(match string, rules ...Rule) Rule { return Rule{Match: match, Rules: rules} }
	for _, c := range []struct {
		name  string
		match string
		rules []Rule
		want  bool
	}{
		{"no rules", "all", nil, false},
		{"all, one positive", "all", []Rule{did, didnt}, false},
		{"all negative", "all", []Rule{didnt, isnt}, true},
		{"any, one negative is enough", "any", []Rule{did, didnt}, true},
		{"any, all positive", "any", []Rule{did, did}, false},
		{"none of positives", "all", []Rule{group("none", did, did)}, true},
		{"none of a negative", "all", []Rule{group("none", didnt)}, false},
		{"none of positive and negative", "all", []Rule{group("none", did, didnt)}, false},
		{"positive and none-of", "all", []Rule{did, group("none", did)}, false},
		{"any group with a negative, all negative beside", "all", []Rule{didnt, group("any", did, isnt)}, true},
		{"all group with a positive", "all", []Rule{didnt, group("all", did, isnt)}, false},
		{"none of an all group with a positive", "all", []Rule{group("none", group("all", did, didnt))}, true},
	} {
		if got := Negative(c.rules, c.match); got != c.want {
			t.Errorf("%s: Negative = %v, want %v", c.name, got, c.want)
		}
	}
}
