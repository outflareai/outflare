package api

import (
	"net/url"
	"strings"
	"testing"
)

// A placeholder the network had nothing to put in (status={status}) is a
// parameter left out, never a refusal: the conversion still counts. An
// unfilled click id is refused, in words that say what happened.
func TestPostbackUnfilledPlaceholders(t *testing.T) {
	// A made-up click id in the shape the redirect gives out.
	const click = "aaaaaaaaaaaaaaa1"
	for _, raw := range []string{"{status}", "{{status}}", "${status}", "#STATUS#", "[status]", "%status%", "<status>", "__STATUS__", "{sub id}"} {
		in, err := readConversion(url.Values{"cid": {click}, "payout": {"5"}, "txid": {"T-0"}, "status": {raw}, "type": {raw}, "currency": {raw}}, "EUR", false)
		if err != nil {
			t.Fatalf("status=%s was refused: %v", raw, err)
		}
		if in.status != "approved" || in.hasStatus || in.typ != "conversion" || in.hasType || in.currency != "EUR" || in.payoutMicros != 5_000000 || in.txid != "T-0" {
			t.Fatalf("status=%s wasn't read as left out: %+v", raw, in)
		}
	}
	// A real value that only looks unusual is still read, and still checked.
	if in, err := readConversion(url.Values{"cid": {click}, "status": {"pending"}, "txid": {"{order}-7"}}, "EUR", false); err != nil || in.status != "pending" || in.txid != "{order}-7" {
		t.Fatalf("real values: %+v, %v", in, err)
	}
	if _, err := readConversion(url.Values{"cid": {click}, "status": {"shipped"}}, "EUR", false); err == nil {
		t.Fatal("an unknown status was accepted")
	}
	_, err := readConversion(url.Values{"cid": {"{sub1}"}, "payout": {"5"}}, "EUR", false)
	if err == nil || !strings.Contains(err.Error(), "cid arrived as {sub1}, unfilled") {
		t.Fatalf("an unfilled click id: %v", err)
	}
	// A second name for the click id is still tried after an unfilled one.
	if in, err := readConversion(url.Values{"cid": {"{sub1}"}, "clickid": {click}}, "EUR", false); err != nil || in.clickKey != click {
		t.Fatalf("the click id under its second name: %+v, %v", in, err)
	}
}
