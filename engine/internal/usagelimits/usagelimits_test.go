package usagelimits

import (
	"strings"
	"testing"
)

func TestAmount(t *testing.T) {
	for _, c := range []struct {
		unit string
		v    float64
		want string
	}{
		{USD, 1.234, "$1.23"},
		{"API unit", 10000, "10,000 API units"},
		{"credit", 1, "1 credit"},
		{"credit", 2.5, "2.5 credits"},
		{"token", 1234567, "1,234,567 tokens"},
	} {
		if got := Amount(c.unit, c.v); got != c.want {
			t.Errorf("Amount(%q, %v) = %q, want %q", c.unit, c.v, got, c.want)
		}
	}
}

func TestOver(t *testing.T) {
	for _, c := range []struct {
		used, cost, limit float64
		want              bool
	}{
		{0, 5, 10, false},
		{6, 5, 10, true},
		{5, 5, 10, false}, // exactly at the limit is allowed
		{9.99, 0, 10, false},
		{10, 0, 10, true}, // unknown cost: stops once used up
		{0, 0, 0, true},   // 0 stops every call
	} {
		if got := over(c.used, c.cost, c.limit); got != c.want {
			t.Errorf("over(%v, %v, %v) = %v", c.used, c.cost, c.limit, got)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	e := &Error{Name: "Ahrefs", Period: Month, Unit: "API unit", Limit: 10000, Used: 9950, Cost: 150}
	msg := e.Error()
	for _, want := range []string{"Ahrefs reached its monthly limit of 10,000 API units", "9,950 API units used", "about 150 API units", "next month"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q lacks %q", msg, want)
		}
	}
	if e.Code() != "usage_limit_reached" {
		t.Error(e.Code())
	}
}

func TestStarting(t *testing.T) {
	if u, m, ok := Starting("ahrefs"); !ok || u != "API unit" || m != 10000 {
		t.Errorf("ahrefs = %q %v %v", u, m, ok)
	}
	if _, _, ok := Starting("apollo"); ok {
		t.Error("apollo has no starting limit")
	}
	if !ValidUnit("API unit") || ValidUnit("") || ValidUnit("1x") {
		t.Error("ValidUnit")
	}
}
