// Package conversions is what a workspace counts as a conversion, defined
// once on the customer events stream for every module that needs it:
// attribution credits channels with it, each definition carries the ad
// networks' own names for sending it back, and partner programs can pay
// commissions on it. A
// definition is never tied to one integration: it names a customer event
// (signed_up, payment.succeeded, meeting.held, call.completed), may filter
// it on its properties, and says what it is worth.
//
// Nothing here is particular to a kind of business. Presets (SaaS, booked
// calls, e-commerce, lead generation) are starting points a workspace
// copies and changes, never fixed paths.
package conversions

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Filter ops.
const (
	OpEq        = "eq"
	OpNeq       = "neq"
	OpGt        = "gt"
	OpGte       = "gte"
	OpLt        = "lt"
	OpLte       = "lte"
	OpIn        = "in"
	OpContains  = "contains"
	OpExists    = "exists"
	OpNotExists = "not_exists"
)

// Ops lists every filter op.
var Ops = []string{OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpIn, OpContains, OpExists, OpNotExists}

// Filter is one condition on an event's properties. Property is a key, or
// a dotted path into nested objects (plan.tier).
type Filter struct {
	Property string `json:"property"`
	Op       string `json:"op"`
	Value    any    `json:"value,omitempty"`
}

// Value modes.
const (
	ValueNone     = "none"
	ValueFixed    = "fixed"
	ValueProperty = "property"
)

// Definition is one conversion a workspace counts.
type Definition struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Event string `json:"event"`
	// Filters must all match; none means every such event.
	Filters []Filter `json:"filters"`
	// ValueMode is none, fixed (FixedValueMinor) or property
	// (ValueProperty, in ValueUnit: major 12.50 or minor 1250).
	ValueMode       string `json:"value_mode"`
	FixedValueMinor *int64 `json:"fixed_value_minor"`
	ValueProperty   string `json:"value_property"`
	ValueUnit       string `json:"value_unit"`
	// Currency is fixed, or '' for the event's currency property.
	Currency string `json:"currency"`
	// NetworkEvents maps an ad network (adtrack's ids: meta, tiktok) to
	// its own name for the conversion (Purchase, CompletePayment).
	NetworkEvents map[string]string `json:"network_events"`
	// Attribution says whether attribution credits channels with it.
	Attribution bool       `json:"attribution"`
	ArchivedAt  *time.Time `json:"archived_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

var (
	// KeyRE is what a definition's key looks like.
	KeyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	// EventRE is a customer event's catalog name.
	EventRE    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	propertyRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$-]{0,63}(\.[A-Za-z_$][A-Za-z0-9_$-]{0,63}){0,4}$`)
	networkRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	currencyRE = regexp.MustCompile(`^([A-Z]{3})?$`)
)

// Validate reports what is wrong with a definition, for a person to fix.
// It fills in defaults: value mode none, value unit major.
func (d *Definition) Validate() error {
	d.Name = strings.TrimSpace(d.Name)
	if !KeyRE.MatchString(d.Key) {
		return errors.New("key is a lowercase letter, then up to 39 letters, digits, hyphens or underscores")
	}
	if d.Name == "" || len([]rune(d.Name)) > 80 {
		return errors.New("name is 1 to 80 characters")
	}
	if len(d.Event) > 100 || !EventRE.MatchString(d.Event) {
		return errors.New("event is a customer event name, such as signed_up, payment.succeeded or meeting.held")
	}
	if d.Event == "payment.refunded" {
		return errors.New("payment.refunded is not a conversion: refunds are taken off payment.succeeded's revenue")
	}
	if len(d.Filters) > 10 {
		return errors.New("at most 10 filters")
	}
	if d.Filters == nil {
		d.Filters = []Filter{}
	}
	for i, f := range d.Filters {
		if !propertyRE.MatchString(f.Property) {
			return fmt.Errorf("filters[%d].property is a property key, or a dotted path such as plan.tier", i)
		}
		if !slices.Contains(Ops, f.Op) {
			return fmt.Errorf("filters[%d].op is one of %s", i, strings.Join(Ops, ", "))
		}
		switch f.Op {
		case OpExists, OpNotExists:
		case OpGt, OpGte, OpLt, OpLte:
			if _, ok := number(f.Value); !ok {
				return fmt.Errorf("filters[%d].value must be a number for %s", i, f.Op)
			}
		case OpIn:
			list, ok := f.Value.([]any)
			if !ok || len(list) == 0 || len(list) > 50 {
				return fmt.Errorf("filters[%d].value must be a list of 1 to 50 values for in", i)
			}
		default:
			if f.Value == nil {
				return fmt.Errorf("filters[%d].value is required for %s", i, f.Op)
			}
		}
	}
	if d.ValueMode == "" {
		d.ValueMode = ValueNone
	}
	if d.ValueUnit == "" {
		d.ValueUnit = "major"
	}
	// Fields another value mode uses are cleared, so switching modes in a
	// change needs only the new mode's fields.
	switch d.ValueMode {
	case ValueNone:
		d.FixedValueMinor, d.ValueProperty = nil, ""
	case ValueFixed:
		if d.FixedValueMinor == nil || *d.FixedValueMinor < 0 {
			return errors.New("value_mode fixed needs fixed_value_minor, 0 or more (in cents)")
		}
		d.ValueProperty = ""
	case ValueProperty:
		if !propertyRE.MatchString(d.ValueProperty) {
			return errors.New("value_mode property needs value_property, such as revenue or amount_minor")
		}
		d.FixedValueMinor = nil
	default:
		return errors.New("value_mode is none, fixed or property")
	}
	if d.ValueUnit != "major" && d.ValueUnit != "minor" {
		return errors.New("value_unit is major (12.50) or minor (1250)")
	}
	if !currencyRE.MatchString(d.Currency) {
		return errors.New("currency is a three-letter code such as USD, or empty for the event's own currency")
	}
	if d.ValueMode == ValueFixed && d.Currency == "" {
		return errors.New("a fixed value needs a currency")
	}
	if d.NetworkEvents == nil {
		d.NetworkEvents = map[string]string{}
	}
	if len(d.NetworkEvents) > 30 {
		return errors.New("at most 30 network_events")
	}
	for n, e := range d.NetworkEvents {
		if !networkRE.MatchString(n) {
			return fmt.Errorf("network_events: %q is not an ad network id (such as meta or tiktok)", n)
		}
		if e = strings.TrimSpace(e); e == "" || len(e) > 100 {
			return fmt.Errorf("network_events: %s's event is 1 to 100 characters", n)
		}
		d.NetworkEvents[n] = e
	}
	return nil
}

// lookup reads a property, following a dotted path.
func lookup(props map[string]any, path string) (any, bool) {
	var cur any = props
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// equal compares a property with a filter value: as numbers when both are
// numbers, else as strings, ignoring case.
func equal(a, b any) bool {
	if x, ok := number(a); ok {
		if y, ok := number(b); ok {
			return x == y
		}
	}
	return strings.EqualFold(fmt.Sprint(a), fmt.Sprint(b))
}

// Match reports whether an event's properties pass every filter.
func (d Definition) Match(props map[string]any) bool {
	for _, f := range d.Filters {
		v, ok := lookup(props, f.Property)
		switch f.Op {
		case OpExists:
			if !ok {
				return false
			}
			continue
		case OpNotExists:
			if ok {
				return false
			}
			continue
		}
		if !ok {
			return false
		}
		switch f.Op {
		case OpEq:
			if !equal(v, f.Value) {
				return false
			}
		case OpNeq:
			if equal(v, f.Value) {
				return false
			}
		case OpGt, OpGte, OpLt, OpLte:
			x, ok1 := number(v)
			y, ok2 := number(f.Value)
			if !ok1 || !ok2 {
				return false
			}
			if (f.Op == OpGt && !(x > y)) || (f.Op == OpGte && !(x >= y)) || (f.Op == OpLt && !(x < y)) || (f.Op == OpLte && !(x <= y)) {
				return false
			}
		case OpIn:
			list, _ := f.Value.([]any)
			if !slices.ContainsFunc(list, func(x any) bool { return equal(v, x) }) {
				return false
			}
		case OpContains:
			if !strings.Contains(strings.ToLower(fmt.Sprint(v)), strings.ToLower(fmt.Sprint(f.Value))) {
				return false
			}
		}
	}
	return true
}

// Value is what one matching event is worth, in minor units (cents), and
// its currency: the fixed value, or the property's, with the definition's
// currency or the event's own currency property. Nil when it has none.
func (d Definition) Value(props map[string]any) (*int64, string) {
	cur := d.Currency
	if cur == "" {
		if c, ok := props["currency"].(string); ok && len(strings.TrimSpace(c)) == 3 {
			cur = strings.ToUpper(strings.TrimSpace(c))
		}
	}
	switch d.ValueMode {
	case ValueFixed:
		if d.FixedValueMinor == nil {
			return nil, cur
		}
		v := *d.FixedValueMinor
		return &v, cur
	case ValueProperty:
		raw, ok := lookup(props, d.ValueProperty)
		if !ok {
			return nil, cur
		}
		f, ok := number(raw)
		if !ok {
			return nil, cur
		}
		if d.ValueUnit == "major" {
			f *= 100
		}
		v := int64(math.Round(f))
		return &v, cur
	}
	return nil, cur
}

// For returns the first definition, in the order given, that counts an
// event with this name and these properties, or nil.
func For(defs []Definition, event string, props map[string]any) *Definition {
	for i := range defs {
		if defs[i].ArchivedAt == nil && defs[i].Event == event && defs[i].Match(props) {
			return &defs[i]
		}
	}
	return nil
}
