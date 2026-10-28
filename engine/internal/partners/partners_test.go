package partners

import (
	"testing"
	"time"
)

func TestRewards(t *testing.T) {
	gold := "gold"
	base := Rule{ID: "base", Trigger: "sale", Side: "partner", Kind: "percent", PercentBP: ptr(2000), Recurrence: "every"}
	tier := Rule{ID: "tier", Trigger: "sale", Side: "partner", Kind: "percent", PercentBP: ptr(3000), Recurrence: "every", MinSales: 10}
	group := Rule{ID: "group", GroupID: &gold, Trigger: "sale", Side: "partner", Kind: "percent", PercentBP: ptr(2500), Recurrence: "every"}
	lead := Rule{ID: "lead", Trigger: "signup", Side: "partner", Kind: "fixed", AmountCents: ptr(int64(500))}
	credit := Rule{ID: "credit", Trigger: "sale", Side: "referred", Kind: "action", Action: "grant_credit", Recurrence: "first"}
	year := Rule{ID: "year", Trigger: "sale", Side: "partner", Kind: "fixed", AmountCents: ptr(int64(1000)), Recurrence: "months", Months: ptr(12), ProductIDs: []string{"pro"}}
	rules := []Rule{base, tier, group, lead, credit}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	earlier := now.AddDate(0, -2, 0)

	ids := func(es []Earned) []string {
		out := []string{}
		for _, e := range es {
			out = append(out, e.Rule.ID)
		}
		return out
	}
	cases := map[string]struct {
		rules []Rule
		c     Conversion
		want  []string
		cash  int64
	}{
		"first sale stacks cash and the two-sided action": {rules, Conversion{Trigger: "sale", AmountCents: 10000, At: now}, []string{"base", "credit"}, 2000},
		"later sale: no second action":                    {rules, Conversion{Trigger: "sale", AmountCents: 10000, FirstSaleAt: &earlier, At: now}, []string{"base"}, 2000},
		"tier replaces the base rate":                     {rules, Conversion{Trigger: "sale", AmountCents: 10000, PriorSales: 10, FirstSaleAt: &earlier, At: now}, []string{"tier"}, 3000},
		"a group's own rate beats a tier":                 {rules, Conversion{Trigger: "sale", AmountCents: 10000, PriorSales: 10, GroupID: &gold, FirstSaleAt: &earlier, At: now}, []string{"group"}, 2500},
		"lead":                                            {rules, Conversion{Trigger: "signup", At: now}, []string{"lead"}, 500},
		"in the first months, for the product":            {[]Rule{year}, Conversion{Trigger: "sale", ProductID: "pro", FirstSaleAt: &earlier, At: now}, []string{"year"}, 1000},
		"after the months":                                {[]Rule{year}, Conversion{Trigger: "sale", ProductID: "pro", FirstSaleAt: ptr(now.AddDate(-1, 0, 0)), At: now}, []string{}, 0},
		"another product":                                 {[]Rule{year}, Conversion{Trigger: "sale", ProductID: "basic", At: now}, []string{}, 0},
		"track only":                                      {nil, Conversion{Trigger: "sale", AmountCents: 10000, At: now}, []string{}, 0},
	}
	for name, tc := range cases {
		got := Rewards(tc.rules, tc.c)
		var cash int64
		for _, e := range got {
			if e.Kind == "cash" {
				cash += e.AmountCents
			}
		}
		if g := ids(got); len(g) != len(tc.want) || cash != tc.cash {
			t.Errorf("%s: got %v (cash %d), want %v (cash %d)", name, g, cash, tc.want, tc.cash)
			continue
		}
		for i := range tc.want {
			if ids(got)[i] != tc.want[i] {
				t.Errorf("%s: got %v, want %v", name, ids(got), tc.want)
			}
		}
	}
}

func TestPresets(t *testing.T) {
	for _, k := range Kinds {
		p := PresetFor(k)
		if p.CreditWindowDays < 1 || p.CreditModel == "" || p.Rules == nil {
			t.Errorf("%s preset = %+v", k, p)
		}
	}
	if r := PresetFor(KindAffiliate).Rules; len(r) != 1 || *r[0].PercentBP != 2000 {
		t.Errorf("affiliate rules = %+v", r)
	}
	if r := PresetFor(KindReferral).Rules; len(r) != 2 || r[1].Side != "referred" {
		t.Errorf("referral rules = %+v", r)
	}
}

func TestCredit(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	visits := []Visit{
		{At: at.AddDate(0, 0, -40), MembershipID: "old"},
		{At: at.AddDate(0, 0, -20), MembershipID: "first"},
		{At: at.AddDate(0, 0, -2), MembershipID: "last"},
		{At: at.Add(30 * time.Minute), MembershipID: "skew"},
		{At: at.Add(3 * time.Hour), MembershipID: "after"},
	}
	if v, ok := Credit(visits, at, 30, "first"); !ok || v.MembershipID != "first" {
		t.Errorf("first = %v %v", v, ok)
	}
	if v, ok := Credit(visits, at, 30, "last"); !ok || v.MembershipID != "skew" {
		t.Errorf("last = %v %v", v, ok)
	}
	if _, ok := Credit(visits[:1], at, 30, "last"); ok {
		t.Error("a visit outside the window got credit")
	}
}

func TestShare(t *testing.T) {
	if Share(2000, 5000, 10000) != 1000 || Share(2000, 10000, 10000) != 2000 || Share(2000, 0, 0) != 2000 || Percent(999, 2000) != 200 {
		t.Error("share or percent rounding is off")
	}
}
