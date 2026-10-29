// Package partners holds the rules of partner programs (customer
// referrals, affiliates and influencers) that need no database: the
// presets a program starts from, which reward rules a conversion earns,
// and which visit gets the credit. The API handlers in engine/internal/api
// store programs, partners, conversions and the reward ledger.
//
// One platform, three presets: a referral program, an affiliate program
// and an influencer program differ only in their settings, so everything a
// preset sets stays editable.
package partners

import (
	"math"
	"slices"
	"time"
)

// Program kinds, each a preset.
const (
	KindReferral   = "referral"
	KindAffiliate  = "affiliate"
	KindInfluencer = "influencer"
)

// Kinds lists the presets in display order.
var Kinds = []string{KindReferral, KindAffiliate, KindInfluencer}

// Rule is one reward rule of a program.
type Rule struct {
	ID      string  `json:"id"`
	GroupID *string `json:"group_id"`
	// PartnerID makes it one partner's own rule (their membership), which
	// beats their group's and the program's.
	PartnerID *string `json:"partner_id"`
	// Trigger is the conversion that earns it: signup, sale, or a custom
	// conversion's event name (such as trial_started).
	Trigger string `json:"trigger"`
	// Side is partner (the person who referred) or referred (the customer
	// they brought, for two-sided rewards; always an action).
	Side string `json:"side"`
	// Kind is percent (of the conversion's value), fixed (an amount per
	// conversion) or
	// action (something the workspace's own system does, such as granting
	// credit, delivered by the partner_reward.approved webhook).
	Kind        string         `json:"kind"`
	PercentBP   *int           `json:"percent_bp"`
	AmountCents *int64         `json:"amount_cents"`
	Action      string         `json:"action"`
	ActionData  map[string]any `json:"action_data"`
	// Recurrence is for sales: first (only the customer's first sale),
	// every, or months (sales in the first Months months).
	Recurrence string `json:"recurrence"`
	Months     *int   `json:"months"`
	// MinSales makes the rule a tier: it applies once the partner has made
	// this many earlier sales in the program.
	MinSales   int      `json:"min_sales"`
	ProductIDs []string `json:"product_ids"`
}

// Preset is what a new program of a kind starts with.
type Preset struct {
	CreditWindowDays int
	CreditModel      string
	HoldDays         int
	AutoApprove      bool
	AutoAccept       bool
	MinPayoutCents   int64
	Rules            []Rule
}

func ptr[T any](v T) *T { return &v }

// PresetFor returns the preset of a kind.
func PresetFor(kind string) Preset {
	switch kind {
	case KindReferral:
		// Customers share a short link; both sides get something the
		// workspace's own product grants, on the first sale.
		return Preset{CreditWindowDays: 60, CreditModel: "last", HoldDays: 14, AutoApprove: true, AutoAccept: true, Rules: []Rule{
			{Trigger: "sale", Side: "partner", Kind: "action", Action: "referral_reward", ActionData: map[string]any{}, Recurrence: "first", ProductIDs: []string{}},
			{Trigger: "sale", Side: "referred", Kind: "action", Action: "welcome_reward", ActionData: map[string]any{}, Recurrence: "first", ProductIDs: []string{}},
		}}
	case KindInfluencer:
		// Track only: influencers are usually paid a fixed fee, logged by
		// hand as a fee so cost per sale works.
		return Preset{CreditWindowDays: 30, CreditModel: "last", HoldDays: 0, AutoApprove: true, Rules: []Rule{}}
	}
	// Affiliates: a share of every sale, held for refunds, paid above a
	// minimum.
	return Preset{CreditWindowDays: 30, CreditModel: "last", HoldDays: 30, AutoApprove: true, MinPayoutCents: 5000, Rules: []Rule{
		{Trigger: "sale", Side: "partner", Kind: "percent", PercentBP: ptr(2000), Recurrence: "every", ActionData: map[string]any{}, ProductIDs: []string{}},
	}}
}

// Conversion is what a reward rule is checked against.
type Conversion struct {
	Trigger     string // signup, sale or a custom event name
	AmountCents int64
	ProductID   string
	// PartnerID is the partner's membership.
	PartnerID string
	// GroupID is the partner's group, if any.
	GroupID *string
	// PriorSales is how many sales the partner made in the program before
	// this one.
	PriorSales int
	// FirstSaleAt is when the referred customer first bought; nil when this
	// is their first sale.
	FirstSaleAt *time.Time
	At          time.Time
}

// Earned is one reward a conversion earns.
type Earned struct {
	Rule        Rule
	Side        string
	Kind        string // cash or action
	AmountCents int64
}

func (r Rule) matches(c Conversion) bool {
	if r.Trigger != c.Trigger {
		return false
	}
	if r.PartnerID != nil && *r.PartnerID != c.PartnerID {
		return false
	}
	if r.GroupID != nil && (c.GroupID == nil || *c.GroupID != *r.GroupID) {
		return false
	}
	if c.PriorSales < r.MinSales {
		return false
	}
	if len(r.ProductIDs) > 0 && !slices.Contains(r.ProductIDs, c.ProductID) {
		return false
	}
	if r.Trigger == "sale" {
		switch r.Recurrence {
		case "first":
			return c.FirstSaleAt == nil
		case "months":
			if c.FirstSaleAt != nil && r.Months != nil && !c.At.Before(c.FirstSaleAt.AddDate(0, *r.Months, 0)) {
				return false
			}
		}
	}
	return true
}

func (r Rule) cash() bool { return r.Kind == "percent" || r.Kind == "fixed" }

// better reports whether a beats b as the cash rule for a conversion, the
// same whatever order the rules are in: a partner's own rule beats their
// group's, which beats one for everyone; then the higher tier; then the
// narrower sale (a first-sale rule, then a first-months one, then every
// sale); then a rule for named products over one for all; then the one
// paying more for this conversion; then the lower id.
func better(a, b Rule, c Conversion) bool {
	if (a.PartnerID != nil) != (b.PartnerID != nil) {
		return a.PartnerID != nil
	}
	if (a.GroupID != nil) != (b.GroupID != nil) {
		return a.GroupID != nil
	}
	if a.MinSales != b.MinSales {
		return a.MinSales > b.MinSales
	}
	if ra, rb := narrowness[a.Recurrence], narrowness[b.Recurrence]; c.Trigger == "sale" && ra != rb {
		return ra > rb
	}
	if (len(a.ProductIDs) > 0) != (len(b.ProductIDs) > 0) {
		return len(a.ProductIDs) > 0
	}
	if pa, pb := a.pays(c), b.pays(c); pa != pb {
		return pa > pb
	}
	return a.ID < b.ID
}

// narrowness ranks recurrences: the narrower one is the more specific rule.
var narrowness = map[string]int{"every": 0, "months": 1, "first": 2}

// pays is the cash a cash rule gives for a conversion.
func (r Rule) pays(c Conversion) int64 {
	switch {
	case r.Kind == "percent" && r.PercentBP != nil:
		return Percent(c.AmountCents, *r.PercentBP)
	case r.Kind == "fixed" && r.AmountCents != nil:
		return *r.AmountCents
	}
	return 0
}

// Rewards returns what a conversion earns under rules, in rule order.
// Rules stack, except that only the best cash rule counts (see better),
// so tiers, group rates and partners' own rates replace the base rate
// instead of adding to it.
func Rewards(rules []Rule, c Conversion) []Earned {
	best := -1
	for i, r := range rules {
		if r.Side == "partner" && r.cash() && r.matches(c) && (best < 0 || better(r, rules[best], c)) {
			best = i
		}
	}
	out := []Earned{}
	for i, r := range rules {
		if !r.matches(c) {
			continue
		}
		switch {
		case r.cash() && i != best:
		case r.Kind == "percent":
			if amt := Percent(c.AmountCents, *r.PercentBP); amt > 0 {
				out = append(out, Earned{Rule: r, Side: r.Side, Kind: "cash", AmountCents: amt})
			}
		case r.Kind == "fixed":
			out = append(out, Earned{Rule: r, Side: r.Side, Kind: "cash", AmountCents: *r.AmountCents})
		case r.Kind == "action":
			out = append(out, Earned{Rule: r, Side: r.Side, Kind: "action"})
		}
	}
	return out
}

// Percent is bp basis points of cents, rounded half away from zero.
func Percent(cents int64, bp int) int64 {
	return int64(math.Round(float64(cents) * float64(bp) / 10000))
}

// Share is the part of amount that refunded out of total cents takes
// back, rounded, and never more than amount.
func Share(amount, refunded, total int64) int64 {
	if total <= 0 || refunded >= total {
		return amount
	}
	return int64(math.Round(float64(amount) * float64(refunded) / float64(total)))
}

// Visit is a site visit that carried a ref code.
type Visit struct {
	At           time.Time
	MembershipID string
}

// Credit picks the visit that gets the credit for a conversion at at, by a
// program's window and model (first or last), from visits in any order.
// ok is false when none is inside the window. A visit up to an hour after
// at still counts, since the sender's clock and ours can differ.
func Credit(visits []Visit, at time.Time, windowDays int, model string) (Visit, bool) {
	from, to := at.AddDate(0, 0, -windowDays), at.Add(time.Hour)
	var pick Visit
	ok := false
	for _, v := range visits {
		if v.At.Before(from) || v.At.After(to) {
			continue
		}
		if !ok || (model == "first" && v.At.Before(pick.At)) || (model != "first" && v.At.After(pick.At)) {
			pick, ok = v, true
		}
	}
	return pick, ok
}
