package commissions

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

func TestStatement(t *testing.T) {
	att := 120.0
	rep := Person{StaffID: "s-rep", UserID: "u-rep", ManagerID: "s-boss", Position: "Account executive", Attainment: &att}
	boss := Person{StaffID: "s-boss", UserID: "u-boss", Position: "Sales manager"}
	sdr := Person{StaffID: "s-sdr", UserID: "u-sdr", ManagerID: "s-boss", Position: "SDR"}
	managerOf := func(u string) string {
		if u == "u-rep" || u == "u-sdr" {
			return "s-boss"
		}
		return ""
	}
	limit := int64(150000)
	plan := Plan{ID: "p", Name: "Sales", Positions: []string{"account executive", "SDR", "sales manager"}, Rules: []Rule{
		{ID: "big", Name: "Big new deals", PaysOn: OnNewBusiness, Who: WhoOwner, Percent: 10, Tiers: []Tier{{100, 15}, {150, 20}},
			Conditions: []journeys.Condition{{Attribute: "amount", Op: "gt", Value: 999999}}},
		{ID: "new", Name: "New business", PaysOn: OnNewBusiness, Who: WhoOwner, Percent: 8, CapMinor: &limit},
		{ID: "renew", Name: "Renewals", PaysOn: OnRenewal, Who: WhoOwner, Percent: 2},
		{ID: "sourced", Name: "Sourced", PaysOn: OnDealWon, Who: WhoSourcedBy, FlatMinor: 25000},
		{ID: "override", Name: "Team override", PaysOn: OnDealWon, Who: WhoManager, Percent: 1},
		{ID: "cash", Name: "Cash collected", PaysOn: OnPayment, Who: WhoCompanyOwner, Percent: 5, Clawback: true},
	}}
	if msg := Check(plan.Rules); msg != "" {
		t.Fatal(msg)
	}
	events := []Event{
		{Kind: "deal", ID: "d1", On: "2026-11-03", Amount: 2000000, DealKind: "new", Owner: "u-rep", SourcedBy: "u-sdr", Attrs: map[string]any{"amount": 2000000.0}},
		{Kind: "deal", ID: "d2", On: "2026-11-05", Amount: 1500000, DealKind: "new", Owner: "u-rep", Attrs: map[string]any{"amount": 1500000.0}},
		{Kind: "deal", ID: "d3", On: "2026-11-06", Amount: 500000, DealKind: "new", Owner: "u-rep", Attrs: map[string]any{"amount": 500000.0}},
		{Kind: "deal", ID: "d4", On: "2026-11-07", Amount: 900000, DealKind: "new", Owner: "u-rep", Attrs: map[string]any{"amount": 900000.0}},
		{Kind: "deal", ID: "d5", On: "2026-11-08", Amount: 400000, DealKind: "renewal", Owner: "u-rep", Attrs: map[string]any{"amount": 400000.0}},
		{Kind: "payment", ID: "t1", On: "2026-11-09", Amount: 100000, CompanyOwner: "u-rep", Owner: "u-rep"},
		{Kind: "refund", ID: "t2", On: "2026-11-10", Amount: 40000, CompanyOwner: "u-rep", Owner: "u-rep"},
	}
	got := map[string]int64{}
	var total int64
	for _, l := range Statement(rep, []Plan{plan}, events, managerOf) {
		got[l.Event.ID+"/"+l.RuleID] += l.Amount
		total += l.Amount
	}
	want := map[string]int64{
		"d1/big": 300000, "d2/big": 225000, // 15%: the tier from 100% of quota, at 120%
		"d3/new": 40000, "d4/new": 72000, // 8%
		"d5/renew": 8000, "t1/cash": 5000, "t2/cash": -2000,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("lines %v, want %v", got, want)
	}
	if total != 648000 {
		t.Errorf("total %d", total)
	}

	// The cap: 8% of 900,000 would be 72,000 but only what is left pays.
	small := int64(50000)
	capped := plan
	capped.Rules = []Rule{{ID: "new", Name: "New", PaysOn: OnNewBusiness, Who: WhoOwner, Percent: 8, CapMinor: &small}}
	lines := Statement(rep, []Plan{capped}, events[2:4], managerOf)
	if len(lines) != 2 || lines[0].Amount != 40000 || lines[1].Amount != 10000 || !lines[1].Capped {
		t.Errorf("capped lines %+v", lines)
	}

	// Whoever sourced a deal gets the flat amount, the manager the override.
	if l := Statement(sdr, []Plan{plan}, events, managerOf); len(l) != 1 || l[0].Amount != 25000 || l[0].RuleID != "sourced" {
		t.Errorf("sdr %+v", l)
	}
	var override int64
	for _, l := range Statement(boss, []Plan{plan}, events, managerOf) {
		if l.RuleID != "override" {
			t.Errorf("manager paid by %s", l.RuleID)
		}
		override += l.Amount
	}
	if override != 53000 { // 1% of the five deals
		t.Errorf("override %d", override)
	}

	// A plan covers nobody it doesn't name.
	if l := Statement(Person{StaffID: "x", UserID: "u-rep", Position: "Designer"}, []Plan{plan}, events, managerOf); len(l) != 0 {
		t.Errorf("uncovered person paid %+v", l)
	}
	// Stack: a second rule pays on top of the first.
	stacked := Plan{ID: "s", StaffIDs: []string{"s-rep"}, Rules: []Rule{
		{ID: "a", Name: "A", PaysOn: OnDealWon, Who: WhoOwner, Percent: 5},
		{ID: "b", Name: "B", PaysOn: OnDealWon, Who: WhoOwner, FlatMinor: 1000, Stack: true},
		{ID: "c", Name: "C", PaysOn: OnDealWon, Who: WhoOwner, Percent: 1},
	}}
	if l := Statement(rep, []Plan{stacked}, events[:1], managerOf); len(l) != 2 || l[0].RuleID != "a" || l[1].RuleID != "b" {
		t.Errorf("stacked %+v", l)
	}
}

func TestCheck(t *testing.T) {
	ok := Rule{ID: "a", Name: "A", PaysOn: OnDealWon, Who: WhoOwner, Percent: 5}
	bad := []struct {
		change func(*Rule)
		want   string
	}{
		{func(r *Rule) { r.ID = "Bad id" }, "id is"},
		{func(r *Rule) { r.Name = "" }, "name is"},
		{func(r *Rule) { r.PaysOn = "sale" }, "pays_on"},
		{func(r *Rule) { r.Who = "me" }, "who is"},
		{func(r *Rule) { r.PaysOn, r.Who = OnPayment, WhoSourcedBy }, "nobody who sourced"},
		{func(r *Rule) { r.Percent = 0 }, "set a percent"},
		{func(r *Rule) { r.FlatMinor = 5 }, "not both"},
		{func(r *Rule) { r.Tiers = []Tier{{100, 10}, {100, 12}} }, "tiers go up"},
		{func(r *Rule) { r.Clawback = true }, "clawback goes"},
		{func(r *Rule) { r.Conditions = []journeys.Condition{{Op: "eq", Value: 1}} }, "a condition reads"},
	}
	if msg := Check([]Rule{ok}); msg != "" {
		t.Fatal(msg)
	}
	for _, b := range bad {
		r := ok
		b.change(&r)
		if msg := Check([]Rule{r}); !strings.Contains(msg, b.want) {
			t.Errorf("want %q, got %q", b.want, msg)
		}
	}
	if msg := Check([]Rule{ok, ok}); !strings.Contains(msg, "unique") {
		t.Errorf("duplicate id: %q", msg)
	}
}

// A payment in the customer's first 30 days pays one rate and later ones
// a lower rate; someone who comes back after a lapse starts again; and
// rules can tell inbound from outbound.
func TestWindowsSourceAndCustomer(t *testing.T) {
	d := func(n int) *int { return &n }
	plan := Plan{ID: "p", Name: "Plan", StaffIDs: []string{"s1"}, LapseDays: 90, Rules: []Rule{
		{ID: "early", Name: "First 30 days", PaysOn: OnPayment, Who: WhoOwner, Percent: 10, DaysTo: d(30)},
		{ID: "later", Name: "Days 31 to 100", PaysOn: OnPayment, Who: WhoOwner, Percent: 2, DaysFrom: d(31), DaysTo: d(100)},
		{ID: "winback", Name: "Won back, outbound", PaysOn: OnDealWon, Who: WhoOwner, Percent: 8, Source: "outbound", Customer: "reactivated"},
		{ID: "inbound", Name: "Inbound deals", PaysOn: OnDealWon, Who: WhoOwner, Percent: 3, Source: "inbound"},
		{ID: "outbound", Name: "Outbound deals", PaysOn: OnDealWon, Who: WhoOwner, Percent: 6, Source: "outbound"},
	}}
	if msg := Check(plan.Rules); msg != "" {
		t.Fatal(msg)
	}
	who := Person{StaffID: "s1", UserID: "u1"}
	pay := func(id string, day int, paid ...int) Event {
		return Event{Kind: "payment", ID: id, On: "2026-11-01", Amount: 10000, Owner: "u1", Day: day, Paid: paid, Attrs: map[string]any{}}
	}
	deal := func(id, source string, day int, paid ...int) Event {
		return Event{Kind: "deal", ID: id, On: "2026-11-02", Amount: 100000, DealKind: "new", Owner: "u1", Source: source, Day: day, Paid: paid, Attrs: map[string]any{}}
	}
	lines := Statement(who, []Plan{plan}, []Event{
		pay("first", 1000),                    // day 0: 10 percent
		pay("month2", 1030, 1000),             // day 30: 10 percent
		pay("month3", 1061, 1000, 1030),       // day 61: 2 percent
		pay("month5", 1121, 1000, 1030, 1061), // day 121: nothing
		pay("back", 1400, 1000, 1030),         // after a lapse: day 0 again, 10 percent
		pay("back2", 1440, 1000, 1030, 1400),  // day 40 of coming back: 2 percent
		deal("in", "inbound", 1000),
		deal("out", "outbound", 1000),
		deal("won-back", "outbound", 1400, 1000),
		deal("upsell", "outbound", 1010, 1000),
	}, func(string) string { return "" })
	got := map[string]string{}
	for _, l := range lines {
		got[l.Event.ID] = l.RuleID
	}
	want := map[string]string{"first": "early", "month2": "early", "month3": "later", "back": "early", "back2": "later",
		"in": "inbound", "out": "outbound", "won-back": "winback", "upsell": "outbound"}
	if len(got) != len(want) {
		t.Fatalf("lines: %v", got)
	}
	for id, rule := range want {
		if got[id] != rule {
			t.Fatalf("%s paid by %q, want %q (%v)", id, got[id], rule, got)
		}
	}
	for _, l := range lines {
		if l.Event.ID == "back" && (l.Event.Customer != "reactivated" || l.Event.CustomerDays == nil || *l.Event.CustomerDays != 0) {
			t.Fatalf("back: %+v", l.Event)
		}
	}
	for _, bad := range []Rule{
		{ID: "a", Name: "A", PaysOn: OnDealWon, Who: WhoOwner, Percent: 1, DaysTo: d(30)},
		{ID: "a", Name: "A", PaysOn: OnPayment, Who: WhoOwner, Percent: 1, DaysFrom: d(40), DaysTo: d(30)},
		{ID: "a", Name: "A", PaysOn: OnPayment, Who: WhoOwner, Percent: 1, Source: "sideways"},
		{ID: "a", Name: "A", PaysOn: OnPayment, Who: WhoOwner, Percent: 1, Customer: "old"},
	} {
		if Check([]Rule{bad}) == "" {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
