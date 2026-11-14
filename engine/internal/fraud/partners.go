package fraud

import (
	"slices"
	"time"
)

// Partner conversion signals (plans/ad-tracker.md, B7d;
// ideas/fraud-signals.md, "Partner fraud"). Each one found on a credited
// partner conversion is a flag in the program's review queue, and the
// conversion's rewards wait while a flag on it is open. Thresholds are
// fixed for every program for now (the owner's decision 14).
const (
	// SharedIdentity is a partner and customer whose records share an
	// email address, phone number or user id: a self-referral under a
	// second record.
	SharedIdentity = "shared_identity"
	// PayoutMatchesCustomer is a partner whose payout details contain the
	// customer's email address.
	PayoutMatchesCustomer = "payout_matches_customer"
	// ManySignupsOneIP is a partner with PartnerSignupsOneIP or more
	// referrals in 30 days through clicks from one address.
	ManySignupsOneIP = "many_signups_one_ip"
	// RefundRate is a partner with PartnerRefundRate or more of their
	// sales refunded in 90 days, from PartnerRefundMinSales sales.
	RefundRate = "refund_rate"
)

// Thresholds for the partner checks.
const (
	// PartnerRepeatClicks is how many clicks from one address on the
	// partner's links within a day of the click that brought the customer
	// is cookie stuffing (repeat_click).
	PartnerRepeatClicks = 10
	// PartnerSignupsOneIP is how many referrals through clicks from one
	// address in 30 days is many_signups_one_ip.
	PartnerSignupsOneIP = 3
	// PartnerRefundMinSales is how many sales in 90 days a partner needs
	// before their refund rate counts.
	PartnerRefundMinSales = 5
	// PartnerRefundRate is the share of refunded sales that flags.
	PartnerRefundRate = 0.3
)

// PartnerSignals lists every signal a partner conversion can be flagged
// with: the partner checks, then the shared reasons of the tracker click
// that brought the customer.
var PartnerSignals = []string{SharedIdentity, PayoutMatchesCustomer, RepeatClick, ManySignupsOneIP, FastConversion, RefundRate,
	DatacenterNetwork, FilterList, Honeypot, IPReputation}

// PartnerFacts is what the checks look at, gathered by the caller from
// what partner programs already store. Counts only: no addresses, names
// or codes.
type PartnerFacts struct {
	// SharedIdentities is how many identities the partner's and the
	// customer's records share.
	SharedIdentities int
	// PayoutMatches says the partner's payout details contain one of the
	// customer's email addresses.
	PayoutMatches bool
	// ClicksFromAddress is how many clicks on the partner's links came
	// from the address of the click that brought the customer, within a
	// day of it; ReferralsFromAddress how many of the partner's referrals
	// in 30 days came through clicks from that address.
	ClicksFromAddress, ReferralsFromAddress int
	// SinceClick is the time from the click that brought the customer to
	// the conversion, when a click did.
	SinceClick *time.Duration
	// Sale says the conversion is a sale; Sales and Refunds are the
	// partner's others in 90 days.
	Sale           bool
	Sales, Refunds int
	// ClickReasons are the shared fraud reasons of the tracker click that
	// brought the customer, when one did.
	ClickReasons []string
}

// Flag is one signal found, with counts and thresholds as detail.
type Flag struct {
	Signal string
	Detail map[string]any
}

// PartnerFlags runs the partner checks.
func PartnerFlags(f PartnerFacts) []Flag {
	var out []Flag
	add := func(signal string, detail map[string]any) {
		if detail == nil {
			detail = map[string]any{}
		}
		out = append(out, Flag{signal, detail})
	}
	if f.SharedIdentities > 0 {
		add(SharedIdentity, map[string]any{"identities": f.SharedIdentities})
	}
	if f.PayoutMatches {
		add(PayoutMatchesCustomer, nil)
	}
	if f.ClicksFromAddress >= PartnerRepeatClicks {
		add(RepeatClick, map[string]any{"clicks": f.ClicksFromAddress, "threshold": PartnerRepeatClicks})
	}
	if f.ReferralsFromAddress >= PartnerSignupsOneIP {
		add(ManySignupsOneIP, map[string]any{"referrals": f.ReferralsFromAddress, "threshold": PartnerSignupsOneIP})
	}
	if f.SinceClick != nil && *f.SinceClick >= 0 && *f.SinceClick < FastConversionWithin {
		add(FastConversion, map[string]any{"seconds": int(f.SinceClick.Seconds()), "threshold": int(FastConversionWithin.Seconds())})
	}
	if f.Sale && f.Sales >= PartnerRefundMinSales && float64(f.Refunds) >= PartnerRefundRate*float64(f.Sales) {
		add(RefundRate, map[string]any{"sales": f.Sales, "refunds": f.Refunds})
	}
	for _, r := range f.ClickReasons {
		if slices.Contains(PartnerSignals, r) && !slices.ContainsFunc(out, func(x Flag) bool { return x.Signal == r }) {
			add(r, map[string]any{"from": "click"})
		}
	}
	return out
}
