// Package commissions works out what each person on a sales team earns
// from a brand's commission plans. A plan is an ordered list of rules;
// each rule says what it pays on, when it applies, who gets it and how
// much. Nothing here pays anyone or reads the database: the API hands in
// the month's events and gets statement lines back, each naming the rule
// that paid it.
package commissions

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

const (
	MaxRules      = 30
	MaxConditions = 5
	MaxTiers      = 10
)

// What a rule pays on.
const (
	OnDealWon     = "deal_won"     // any deal won
	OnNewBusiness = "new_business" // a won deal with someone who didn't pay before
	OnExpansion   = "expansion"    // a won deal with someone who already pays
	OnRenewal     = "renewal"      // a won deal opened for a renewal
	OnPayment     = "payment"      // money collected
)

// Who a rule pays.
const (
	WhoOwner        = "owner"         // the deal's owner; for a payment, the customer's owner
	WhoCompanyOwner = "company_owner" // the owner of the company
	WhoSourcedBy    = "sourced_by"    // whoever the deal says sourced it
	WhoManager      = "manager"       // the manager of the owner
)

// Tier is a rate that applies from a share of quota reached.
type Tier struct {
	// FromAttainment is the percent of quota from which the rate applies.
	FromAttainment int     `json:"from_attainment"`
	Percent        float64 `json:"percent"`
}

type Rule struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	PaysOn     string               `json:"pays_on"`
	Match      string               `json:"match,omitempty"`
	Conditions []journeys.Condition `json:"conditions,omitempty"`
	Who        string               `json:"who"`
	// Source limits the rule to deals and payments that came in by
	// themselves (inbound) or from outreach (outbound). Empty is either.
	Source string `json:"source,omitempty"`
	// Customer limits the rule to someone paying for the first time (new)
	// or coming back after they had stopped (reactivated). Empty is anyone.
	Customer string `json:"customer,omitempty"`
	// DaysFrom and DaysTo limit a payment rule to payments made that many
	// days after the customer started paying (or started again): 0 to 30
	// is their first month. Either end can be left open.
	DaysFrom *int `json:"days_from,omitempty"`
	DaysTo   *int `json:"days_to,omitempty"`
	// Percent of the amount, or FlatMinor for each deal or payment.
	Percent   float64 `json:"percent,omitempty"`
	FlatMinor int64   `json:"flat_minor,omitempty"`
	// Tiers replace Percent once the person has reached a share of quota.
	Tiers []Tier `json:"tiers,omitempty"`
	// CapMinor is the most the rule pays one person in a month.
	CapMinor *int64 `json:"cap_minor,omitempty"`
	// Clawback takes the commission back when a payment is refunded.
	Clawback bool `json:"clawback,omitempty"`
	// Stack pays even when an earlier rule of the plan already paid the
	// person for the same deal or payment.
	Stack bool `json:"stack,omitempty"`
}

// RolePrefix marks a plan position that is a role on the sales desk
// (role:sdr, role:ae, role:manager) and not a job title.
const RolePrefix = "role:"

// Plan is a brand's commission plan: who it covers and its rules, tried
// from the top.
type Plan struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Positions []string `json:"positions"`
	StaffIDs  []string `json:"staff_ids"`
	// LapseDays is how long someone goes without paying before their
	// next payment makes them a reactivated customer.
	LapseDays int    `json:"lapse_days"`
	Rules     []Rule `json:"rules"`
}

// DefaultLapseDays is a plan's LapseDays when it sets none.
const DefaultLapseDays = 90

var ruleIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Check says what is wrong with a plan's rules, or "".
func Check(rules []Rule) string {
	if len(rules) > MaxRules {
		return fmt.Sprintf("a plan has at most %d rules", MaxRules)
	}
	seen := map[string]bool{}
	for i, r := range rules {
		at := fmt.Sprintf("rule %d: ", i+1)
		if !ruleIDRE.MatchString(r.ID) || seen[r.ID] {
			return at + "id is lowercase letters, digits, - and _, and unique in the plan"
		}
		seen[r.ID] = true
		if n := len(strings.TrimSpace(r.Name)); n == 0 || n > 80 {
			return at + "name is 1 to 80 characters"
		}
		switch r.PaysOn {
		case OnDealWon, OnNewBusiness, OnExpansion, OnRenewal, OnPayment:
		default:
			return at + "pays_on is deal_won, new_business, expansion, renewal or payment"
		}
		switch r.Who {
		case WhoOwner, WhoCompanyOwner, WhoManager:
		case WhoSourcedBy:
			if r.PaysOn == OnPayment {
				return at + "a payment has nobody who sourced it: pay the owner, the company's owner or the manager"
			}
		default:
			return at + "who is owner, company_owner, sourced_by or manager"
		}
		if r.Source != "" && r.Source != "inbound" && r.Source != "outbound" {
			return at + "source is inbound or outbound, or left out for either"
		}
		if r.Customer != "" && r.Customer != "new" && r.Customer != "reactivated" {
			return at + "customer is new or reactivated, or left out for anyone"
		}
		if (r.DaysFrom != nil || r.DaysTo != nil) && r.PaysOn != OnPayment {
			return at + "days_from and days_to go with pays_on payment: days since the customer started paying"
		}
		if (r.DaysFrom != nil && (*r.DaysFrom < 0 || *r.DaysFrom > 3650)) || (r.DaysTo != nil && (*r.DaysTo < 0 || *r.DaysTo > 3650)) ||
			(r.DaysFrom != nil && r.DaysTo != nil && *r.DaysFrom > *r.DaysTo) {
			return at + "days_from and days_to are 0 to 3650, with days_from no later than days_to"
		}
		if r.Match != "" && r.Match != "all" && r.Match != "any" {
			return at + "match is all or any"
		}
		if len(r.Conditions) > MaxConditions {
			return at + fmt.Sprintf("at most %d conditions", MaxConditions)
		}
		for _, c := range r.Conditions {
			if c.Attribute == "" {
				return at + "a condition reads a field of the deal or payment, such as amount, product or company.country"
			}
			if msg := journeys.CheckCondition(c); msg != "" {
				return at + msg
			}
		}
		if r.Percent < 0 || r.Percent > 100 || r.FlatMinor < 0 {
			return at + "percent is 0 to 100 and flat_minor is not negative"
		}
		if r.Percent > 0 && r.FlatMinor > 0 {
			return at + "set a percent or a flat amount, not both"
		}
		if r.Percent == 0 && r.FlatMinor == 0 && len(r.Tiers) == 0 {
			return at + "set a percent, a flat amount or tiers"
		}
		if len(r.Tiers) > MaxTiers || (len(r.Tiers) > 0 && r.FlatMinor > 0) {
			return at + fmt.Sprintf("at most %d tiers, and tiers go with a percent, not a flat amount", MaxTiers)
		}
		last := -1
		for _, t := range r.Tiers {
			if t.FromAttainment <= last || t.FromAttainment < 0 || t.FromAttainment > 1000 || t.Percent < 0 || t.Percent > 100 {
				return at + "tiers go up by from_attainment (0 to 1000, percent of quota), each with a percent of 0 to 100"
			}
			last = t.FromAttainment
		}
		if r.CapMinor != nil && *r.CapMinor < 0 {
			return at + "cap_minor is not negative"
		}
		if r.Clawback && r.PaysOn != OnPayment {
			return at + "clawback goes with pays_on payment: a refund takes the commission back"
		}
	}
	return ""
}

// Event is one thing that can earn commission in a month: a deal won, a
// payment, or a refund of one.
type Event struct {
	Kind   string `json:"kind"` // deal, payment or refund
	ID     string `json:"id"`
	Name   string `json:"name"`
	On     string `json:"on"` // the day, YYYY-MM-DD
	Amount int64  `json:"amount_minor"`
	// DealKind is new, expansion or renewal for a deal.
	DealKind string `json:"deal_kind,omitempty"`
	// Attrs is what conditions read: the deal's fields, amount, stage,
	// deal_kind, product, and the company's fields under company.
	Attrs map[string]any `json:"-"`
	// User ids: the owner, the owner of the company, and who sourced it.
	Owner        string `json:"-"`
	CompanyOwner string `json:"-"`
	SourcedBy    string `json:"-"`
	// CoOwner is a deal's second owner and CoShare the percent of the
	// deal that is theirs (the rest is the owner's); empty with one owner.
	CoOwner    string  `json:"-"`
	CoShare    float64 `json:"-"`
	RecordID   string  `json:"record_id,omitempty"`
	RecordName string  `json:"record_name,omitempty"`
	// Source is inbound or outbound.
	Source string `json:"source,omitempty"`
	// Paid is every earlier day the customer paid, oldest first, as days
	// since 1970, and Day is the day of this deal or payment.
	Paid []int `json:"-"`
	Day  int   `json:"-"`
	// Customer and CustomerDays are worked out from Paid under the plan
	// that pays the line: new or reactivated (or existing, for a deal
	// with someone paying now), and for a payment the days since they
	// started paying.
	Customer     string `json:"customer,omitempty"`
	CustomerDays *int   `json:"customer_days,omitempty"`
}

// place works out who the customer is to a plan: new, reactivated or
// existing, and for a payment how many days into paying they are.
func (e Event) place(lapse int) Event {
	if lapse <= 0 {
		lapse = DefaultLapseDays
	}
	if e.Kind == "deal" {
		switch {
		case len(e.Paid) == 0:
			e.Customer = "new"
		case e.Day-e.Paid[len(e.Paid)-1] > lapse:
			e.Customer = "reactivated"
		default:
			e.Customer = "existing"
		}
		return e
	}
	// The run of payments this one belongs to: back from it until a gap
	// longer than the lapse.
	start, at := e.Day, len(e.Paid)
	for at > 0 && start-e.Paid[at-1] <= lapse {
		at--
		start = e.Paid[at]
	}
	days := e.Day - start
	e.CustomerDays = &days
	e.Customer = "new"
	if at > 0 {
		e.Customer = "reactivated"
	}
	return e
}

// fits reports whether the event is one the rule's source, customer and
// days limits let through.
func (r Rule) fits(e Event) bool {
	if r.Source != "" && r.Source != e.Source {
		return false
	}
	if r.Customer != "" && r.Customer != e.Customer {
		return false
	}
	if r.DaysFrom != nil || r.DaysTo != nil {
		if e.CustomerDays == nil || (r.DaysFrom != nil && *e.CustomerDays < *r.DaysFrom) || (r.DaysTo != nil && *e.CustomerDays > *r.DaysTo) {
			return false
		}
	}
	return true
}

// Person is someone in the team directory.
type Person struct {
	StaffID   string
	UserID    string
	ManagerID string
	Position  string
	// Role is their role on the brand's sales desk: sdr, ae or manager.
	Role string
	// Attainment is their percent of quota for the quarter, nil with no quota.
	Attainment *float64
}

// Line is one amount on a statement, with the rule that paid it.
type Line struct {
	Event    Event   `json:"event"`
	PlanID   string  `json:"plan_id"`
	PlanName string  `json:"plan_name"`
	RuleID   string  `json:"rule_id"`
	RuleName string  `json:"rule_name"`
	Who      string  `json:"who"`
	Percent  float64 `json:"percent,omitempty"`
	// TierFrom is the share of quota the rate applies from, when a tier set it.
	TierFrom *int `json:"tier_from,omitempty"`
	// SplitPercent is this person's share of a deal with two owners.
	SplitPercent float64 `json:"split_percent,omitempty"`
	Amount       int64   `json:"amount_minor"`
	// Capped says the rule's monthly cap cut this line short.
	Capped bool `json:"capped,omitempty"`
}

// Covers reports whether the plan is for this person.
func (p Plan) Covers(who Person) bool {
	for _, id := range p.StaffIDs {
		if id == who.StaffID {
			return true
		}
	}
	pos := strings.ToLower(strings.TrimSpace(who.Position))
	for _, v := range p.Positions {
		v = strings.ToLower(strings.TrimSpace(v))
		if role, ok := strings.CutPrefix(v, RolePrefix); ok {
			if role != "" && role == who.Role {
				return true
			}
		} else if pos != "" && v == pos {
			return true
		}
	}
	return false
}

func (r Rule) paysOn(e Event) bool {
	switch e.Kind {
	case "deal":
		return r.PaysOn == OnDealWon || (r.PaysOn == OnNewBusiness && e.DealKind == "new") ||
			(r.PaysOn == OnExpansion && e.DealKind == "expansion") || (r.PaysOn == OnRenewal && e.DealKind == "renewal")
	case "payment":
		return r.PaysOn == OnPayment
	case "refund":
		return r.PaysOn == OnPayment && r.Clawback
	}
	return false
}

// share is how much of the event the rule's "who" makes this person's:
// 1 for all of it, 0 for none, and in between for one of two owners of a
// deal. managerOf gives the staff id of a user's manager.
func (r Rule) share(e Event, who Person, managerOf func(user string) string) float64 {
	co := 0.0
	if e.CoOwner != "" && e.CoOwner != e.Owner {
		co = min(max(e.CoShare, 0), 100) / 100
	}
	split := func(is func(user string) bool) float64 {
		f := 0.0
		if e.Owner != "" && is(e.Owner) {
			f += 1 - co
		}
		if co > 0 && is(e.CoOwner) {
			f += co
		}
		return f
	}
	switch r.Who {
	case WhoOwner:
		if who.UserID == "" {
			return 0
		}
		return split(func(user string) bool { return user == who.UserID })
	case WhoCompanyOwner:
		if who.UserID != "" && e.CompanyOwner == who.UserID {
			return 1
		}
	case WhoSourcedBy:
		if who.UserID != "" && e.SourcedBy == who.UserID {
			return 1
		}
	case WhoManager:
		return split(func(user string) bool { return managerOf(user) == who.StaffID })
	}
	return 0
}

// rate is the rule's percent for a person, and the tier that set it.
func (r Rule) rate(who Person) (float64, *int) {
	pct := r.Percent
	var from *int
	if who.Attainment != nil {
		for _, t := range r.Tiers {
			if float64(t.FromAttainment) <= *who.Attainment {
				f := t.FromAttainment
				pct, from = t.Percent, &f
			}
		}
	}
	return pct, from
}

// Statement is what one person earns from the events under the plans
// that cover them: for each deal or payment, the first rule of each plan
// that applies pays, and so does any later rule marked Stack.
func Statement(who Person, plans []Plan, events []Event, managerOf func(user string) string) []Line {
	lines := []Line{}
	sort.SliceStable(events, func(i, j int) bool { return events[i].On < events[j].On })
	for _, p := range plans {
		if !p.Covers(who) {
			continue
		}
		paid := map[string]int64{} // per rule, towards its cap
		for _, e := range events {
			e = e.place(p.LapseDays)
			if e.Attrs != nil {
				at := make(map[string]any, len(e.Attrs)+3)
				for k, v := range e.Attrs {
					at[k] = v
				}
				at["source"], at["customer"] = e.Source, e.Customer
				if e.CustomerDays != nil {
					at["customer_days"] = float64(*e.CustomerDays)
				}
				e.Attrs = at
			}
			hit := false
			for _, r := range p.Rules {
				if (hit && !r.Stack) || !r.paysOn(e) || !r.fits(e) {
					continue
				}
				part := r.share(e, who, managerOf)
				if part <= 0 {
					continue
				}
				if len(r.Conditions) > 0 && !journeys.Holds(e.Attrs, nil, r.Conditions, r.Match) {
					continue
				}
				hit = true
				l := Line{Event: e, PlanID: p.ID, PlanName: p.Name, RuleID: r.ID, RuleName: r.Name, Who: r.Who}
				if part < 1 {
					l.SplitPercent = math.Round(part*1000) / 10
				}
				if r.FlatMinor > 0 {
					l.Amount = int64(math.Round(float64(r.FlatMinor) * part))
				} else {
					l.Percent, l.TierFrom = r.rate(who)
					l.Amount = int64(math.Round(float64(e.Amount) * l.Percent / 100 * part))
				}
				if e.Kind == "refund" {
					l.Amount = -l.Amount
				} else if r.CapMinor != nil {
					if left := *r.CapMinor - paid[r.ID]; l.Amount > left {
						l.Amount, l.Capped = max(left, 0), true
					}
					paid[r.ID] += l.Amount
				}
				lines = append(lines, l)
			}
		}
	}
	return lines
}
