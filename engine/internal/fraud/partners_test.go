package fraud

import (
	"testing"
	"time"
)

func TestPartnerFlags(t *testing.T) {
	signals := func(f PartnerFacts) []string {
		var out []string
		for _, x := range PartnerFlags(f) {
			out = append(out, x.Signal)
		}
		return out
	}
	if got := signals(PartnerFacts{}); len(got) != 0 {
		t.Fatalf("clean: %v", got)
	}
	fast, slow := 3*time.Second, time.Minute
	got := signals(PartnerFacts{SharedIdentities: 1, PayoutMatches: true, ClicksFromAddress: 10, ReferralsFromAddress: 3, SinceClick: &fast,
		Sale: true, Sales: 10, Refunds: 3, ClickReasons: []string{Honeypot, FastConversion, "unknown"}})
	want := []string{SharedIdentity, PayoutMatchesCustomer, RepeatClick, ManySignupsOneIP, FastConversion, RefundRate, Honeypot}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// Just under each threshold flags nothing; a refund rate needs 5 sales
	// and only counts on a sale.
	if got := signals(PartnerFacts{ClicksFromAddress: 9, ReferralsFromAddress: 2, SinceClick: &slow, Sale: true, Sales: 4, Refunds: 4}); len(got) != 0 {
		t.Fatalf("under the thresholds: %v", got)
	}
	if got := signals(PartnerFacts{Sales: 10, Refunds: 9}); len(got) != 0 {
		t.Fatalf("a lead counted the refund rate: %v", got)
	}
}
