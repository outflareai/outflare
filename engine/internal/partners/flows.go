package partners

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Flows are reward rules with Tolt's extras: conditions on the partner,
// the customer and the transaction, their own approval, whether a cash
// flow adds to the others or is exclusive, overrides per partner or
// product, and an action that moves the partner to another group.

// Approval modes of a flow.
const (
	ApprovalProgram   = "program"    // the program's hold and auto-approve
	ApprovalAuto      = "auto"       // approved at once
	ApprovalAfterDays = "after_days" // approved ApprovalDays after the conversion
	ApprovalManual    = "manual"     // a person approves it
)

// Stacking of a cash flow.
const (
	StackExclusive = "exclusive" // only the best exclusive cash flow pays
	StackAdds      = "adds"      // pays alongside it
)

// Conditions are groups of conditions, all or any of which must hold.
// None always matches.
type Conditions struct {
	Match  string           `json:"match,omitempty"`
	Groups []ConditionGroup `json:"groups,omitempty"`
}

// ConditionGroup is conditions, all or any of which must hold.
type ConditionGroup struct {
	Match      string      `json:"match,omitempty"`
	Conditions []Condition `json:"conditions"`
}

// Condition compares one fact of the conversion with a value.
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// Override is another rate inside a flow for one partner or one product.
// A partner's beats a product's.
type Override struct {
	PartnerID   *string `json:"partner_id,omitempty"`
	ProductID   *string `json:"product_id,omitempty"`
	PercentBP   *int    `json:"percent_bp,omitempty"`
	AmountCents *int64  `json:"amount_cents,omitempty"`
}

// FieldKind says what values a condition field takes.
type FieldKind string

const (
	FieldNumber FieldKind = "number"
	FieldText   FieldKind = "text"
	FieldBool   FieldKind = "bool"
	FieldDate   FieldKind = "date"
)

// Fields are the facts conditions can test, by name.
var Fields = map[string]FieldKind{
	"partner.id":                 FieldText,
	"partner.country":            FieldText,
	"partner.transactions":       FieldNumber, // sales credited to them, ever
	"partner.customers":          FieldNumber, // referred customers who bought
	"partner.leads":              FieldNumber, // referred customers, bought or not
	"partner.revenue_cents":      FieldNumber, // their sales less refunds
	"partner.earnings_cents":     FieldNumber, // cash earned, not rejected or reversed
	"customer.transactions":      FieldNumber, // the customer's sales before this one
	"customer.total_paid_cents":  FieldNumber, // what they paid before this one
	"customer.first_sale_at":     FieldDate,
	"customer.signed_up_at":      FieldDate, // when they were referred
	"transaction.amount_cents":   FieldNumber,
	"transaction.product_id":     FieldText,
	"transaction.recurring":      FieldBool,
	"transaction.interval":       FieldText, // day, week, month, year or one_time, as the backend said
	"transaction.payment_source": FieldText, // stripe, paddle, ..., as the backend said
}

// Ops each field kind takes.
var opsFor = map[FieldKind][]string{
	FieldNumber: {"eq", "neq", "gt", "gte", "lt", "lte"},
	FieldText:   {"eq", "neq", "in", "not_in"},
	FieldBool:   {"eq"},
	FieldDate:   {"within", "not_within"},
}

// Facts are the values of Fields for one conversion. A missing fact (no
// first sale yet, no country) fails every test but neq, not_in and
// not_within.
type Facts map[string]any

// Within is a date condition's value: a span back from the conversion.
type Within struct {
	Amount int    `json:"amount"`
	Unit   string `json:"unit"` // days, months or years
}

// Empty reports whether the conditions always match.
func (cs Conditions) Empty() bool {
	return !slices.ContainsFunc(cs.Groups, func(g ConditionGroup) bool { return len(g.Conditions) > 0 })
}

// Check validates conditions, with errors a person can act on.
func (cs Conditions) Check() error {
	if cs.Match != "" && cs.Match != "all" && cs.Match != "any" {
		return fmt.Errorf("conditions.match is all or any")
	}
	if len(cs.Groups) > 10 {
		return fmt.Errorf("at most 10 condition groups")
	}
	for i, g := range cs.Groups {
		if g.Match != "" && g.Match != "all" && g.Match != "any" {
			return fmt.Errorf("conditions.groups[%d].match is all or any", i)
		}
		if len(g.Conditions) > 20 {
			return fmt.Errorf("at most 20 conditions in a group")
		}
		for j, c := range g.Conditions {
			if err := c.check(); err != nil {
				return fmt.Errorf("conditions.groups[%d].conditions[%d]: %w", i, j, err)
			}
		}
	}
	return nil
}

func (c Condition) check() error {
	kind, ok := Fields[c.Field]
	if !ok {
		names := make([]string, 0, len(Fields))
		for k := range Fields {
			names = append(names, k)
		}
		slices.Sort(names)
		return fmt.Errorf("field is one of %s", strings.Join(names, ", "))
	}
	if !slices.Contains(opsFor[kind], c.Op) {
		return fmt.Errorf("%s takes the op %s", c.Field, strings.Join(opsFor[kind], ", "))
	}
	switch kind {
	case FieldNumber:
		if _, ok := number(c.Value); !ok {
			return fmt.Errorf("%s compares with a number", c.Field)
		}
	case FieldBool:
		if _, ok := c.Value.(bool); !ok {
			return fmt.Errorf("%s compares with true or false", c.Field)
		}
	case FieldText:
		if c.Op == "in" || c.Op == "not_in" {
			if _, ok := texts(c.Value); !ok {
				return fmt.Errorf("%s %s takes a list of text", c.Field, c.Op)
			}
		} else if _, ok := c.Value.(string); !ok {
			return fmt.Errorf("%s compares with text", c.Field)
		}
	case FieldDate:
		w, ok := within(c.Value)
		if !ok || w.Amount < 1 || w.Amount > 3650 || !slices.Contains([]string{"days", "months", "years"}, w.Unit) {
			return fmt.Errorf(`%s takes {"amount": 1 to 3650, "unit": "days", "months" or "years"}`, c.Field)
		}
	}
	return nil
}

// Holds reports whether the facts meet the conditions at time at.
func (cs Conditions) Holds(f Facts, at time.Time) bool {
	if cs.Empty() {
		return true
	}
	any := cs.Match == "any"
	for _, g := range cs.Groups {
		ok := g.match(f, at)
		if any && ok {
			return true
		}
		if !any && !ok {
			return false
		}
	}
	return !any
}

func (g ConditionGroup) match(f Facts, at time.Time) bool {
	if len(g.Conditions) == 0 {
		return true
	}
	any := g.Match == "any"
	for _, c := range g.Conditions {
		ok := c.match(f, at)
		if any && ok {
			return true
		}
		if !any && !ok {
			return false
		}
	}
	return !any
}

func (c Condition) match(f Facts, at time.Time) bool {
	v, has := f[c.Field]
	if !has || v == nil {
		return c.Op == "neq" || c.Op == "not_in" || c.Op == "not_within"
	}
	switch Fields[c.Field] {
	case FieldNumber:
		a, _ := number(v)
		b, _ := number(c.Value)
		switch c.Op {
		case "eq":
			return a == b
		case "neq":
			return a != b
		case "gt":
			return a > b
		case "gte":
			return a >= b
		case "lt":
			return a < b
		case "lte":
			return a <= b
		}
	case FieldText:
		s, _ := v.(string)
		switch c.Op {
		case "eq", "neq":
			want, _ := c.Value.(string)
			return strings.EqualFold(s, want) == (c.Op == "eq")
		case "in", "not_in":
			list, _ := texts(c.Value)
			in := slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
			return in == (c.Op == "in")
		}
	case FieldBool:
		b, _ := v.(bool)
		want, _ := c.Value.(bool)
		return b == want
	case FieldDate:
		t, ok := v.(time.Time)
		if !ok {
			return c.Op == "not_within"
		}
		w, _ := within(c.Value)
		var since time.Time
		switch w.Unit {
		case "days":
			since = at.AddDate(0, 0, -w.Amount)
		case "months":
			since = at.AddDate(0, -w.Amount, 0)
		default:
			since = at.AddDate(-w.Amount, 0, 0)
		}
		return !t.Before(since) == (c.Op == "within")
	}
	return false
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func texts(v any) ([]string, bool) {
	switch l := v.(type) {
	case []string:
		return l, true
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			s, ok := x.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

func within(v any) (Within, bool) {
	switch w := v.(type) {
	case Within:
		return w, true
	case map[string]any:
		n, ok := number(w["amount"])
		u, _ := w["unit"].(string)
		return Within{Amount: int(n), Unit: u}, ok
	}
	return Within{}, false
}

// rate is the flow's rate for a conversion: a partner's override, else a
// product's, else its own.
func (r Rule) rate(c Conversion) (bp *int, cents *int64) {
	for _, o := range r.Overrides {
		if o.PartnerID != nil && *o.PartnerID == c.PartnerID {
			return o.PercentBP, o.AmountCents
		}
	}
	for _, o := range r.Overrides {
		if o.ProductID != nil && c.ProductID != "" && *o.ProductID == c.ProductID {
			return o.PercentBP, o.AmountCents
		}
	}
	return r.PercentBP, r.AmountCents
}

// NeedsFacts reports whether any rule has conditions, so the caller has
// to gather Facts.
func NeedsFacts(rules []Rule) bool {
	return slices.ContainsFunc(rules, func(r Rule) bool { return !r.Conditions.Empty() })
}
