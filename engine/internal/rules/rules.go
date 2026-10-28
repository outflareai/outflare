// Package rules is the rules engine of the customer data platform. A rule
// watches what happens to records and, when its condition becomes true,
// emits a business event of its own (such as customer.reactivated) on the
// record's timeline. Journeys, webhooks and anything else that listens to
// events act on that event: a rule never sends anything itself, so "what
// happened" and "what we did about it" stay separate facts.
//
// A rule fires when its condition becomes true, not while it stays true,
// and at most once per episode: a retry, a replayed job or a duplicate
// event never fires twice (rule_firings is unique per rule, record and
// episode). Events that happened before the rule went live, or arrived
// more than a day late, never fire it, so a backfill or an import sets
// nothing off.
//
// The API handlers in engine/internal/api make and change rules; this
// package checks them, evaluates them in jobs and runs absence timers.
package rules

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Trigger kinds.
const (
	KindEvent    = "event"
	KindChange   = "change"
	KindAbsence  = "absence"
	KindAudience = "audience"
)

// Kinds are the trigger kinds, in the order the docs list them.
var Kinds = []string{KindEvent, KindChange, KindAbsence, KindAudience}

const (
	// MaxDepth is how many rules can set each other off in a row: a rule
	// set off by another rule's event is one deeper.
	MaxDepth = 5
	// Lateness is how late an event may arrive and still fire a rule.
	Lateness = 24 * time.Hour
	// MaxConditions is the most conditions a rule has.
	MaxConditions = 20
	// MinWithin and MaxWithin bound an absence rule's time.
	MinWithin = 60
	MaxWithin = 365 * 24 * 3600
)

// Trigger is what sets a rule off.
//
//   - event: Event happens.
//   - change: the record's fields come to meet the conditions (no
//     settings: the attribute conditions are the trigger).
//   - absence: Event doesn't happen within WithinSeconds of StartEvent.
//     Without StartEvent, each Event starts the clock again, so the rule
//     fires after WithinSeconds with no Event (inactivity).
//   - audience: someone joins (On enter) or leaves (On leave) the smart
//     list ListID.
type Trigger struct {
	Kind          string `json:"kind"`
	Event         string `json:"event,omitempty"`
	StartEvent    string `json:"start_event,omitempty"`
	WithinSeconds int64  `json:"within_seconds,omitempty"`
	ListID        string `json:"list_id,omitempty"`
	On            string `json:"on,omitempty"`
}

// Start is the event that starts an absence rule's clock.
func (t Trigger) Start() string {
	if t.StartEvent != "" {
		return t.StartEvent
	}
	return t.Event
}

// Condition is an attribute or property test, as in journeys.
type Condition = journeys.Condition

// Rule is a rule as the engine reads it.
type Rule struct {
	ID          string
	WorkspaceID string
	BrandID     *string
	Status      string
	RecordType  string
	Trigger     Trigger
	Conditions  []Condition
	Match       string
	Emit        string
	Fire        string // every_time or once
	Cooldown    int64  // seconds
	ManualEdits bool
	Version     int
	ActivatedAt *time.Time
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Check reports what is wrong with a rule, or "".
func Check(r Rule) string {
	t := r.Trigger
	if r.RecordType == "" {
		return "record_type is required"
	}
	if r.Match != "all" && r.Match != "any" {
		return "match is all or any"
	}
	if r.Fire != "every_time" && r.Fire != "once" {
		return "fire is every_time or once"
	}
	if r.Cooldown < 0 || r.Cooldown > 31536000 {
		return "cooldown_seconds is 0 to 31,536,000 (a year)"
	}
	if !journeys.ValidEventName(r.Emit) {
		return "emit_event must be an event name such as customer.reactivated"
	}
	if len(r.Conditions) > MaxConditions {
		return fmt.Sprintf("a rule has at most %d conditions", MaxConditions)
	}
	properties := false
	for _, c := range r.Conditions {
		if c.Event != "" || c.Email != "" {
			return "rule conditions test attribute (a field on the record) or property (a property of the event)"
		}
		if msg := journeys.CheckCondition(c); msg != "" {
			return msg
		}
		properties = properties || c.Property != ""
	}
	if t.Kind != KindChange && r.RecordType != "person" {
		return "event, absence and audience rules are about people (record_type person); change rules can watch any record type"
	}
	switch t.Kind {
	case KindEvent:
		if !journeys.ValidEventName(t.Event) {
			return "an event trigger needs event, an event name such as product_activated"
		}
		if t.StartEvent != "" || t.WithinSeconds != 0 || t.ListID != "" || t.On != "" {
			return "an event trigger takes only event"
		}
	case KindChange:
		if t.Event != "" || t.StartEvent != "" || t.WithinSeconds != 0 || t.ListID != "" || t.On != "" {
			return "a change trigger takes no settings: its attribute conditions say what to watch"
		}
		if properties {
			return "a change rule tests the record's fields (attribute), not event properties"
		}
		if len(r.Conditions) == 0 {
			return "a change rule needs at least one attribute condition, such as lifecycle_stage eq customer"
		}
	case KindAbsence:
		if !journeys.ValidEventName(t.Event) {
			return "an absence trigger needs event: what must happen, such as product_activated"
		}
		if t.StartEvent != "" && !journeys.ValidEventName(t.StartEvent) {
			return "start_event must be an event name such as signed_up"
		}
		if t.WithinSeconds < MinWithin || t.WithinSeconds > MaxWithin {
			return "within_seconds is 60 (a minute) to 31,536,000 (a year)"
		}
		if t.ListID != "" || t.On != "" {
			return "an absence trigger takes event, start_event and within_seconds"
		}
	case KindAudience:
		if !uuidRE.MatchString(t.ListID) {
			return "an audience trigger needs list_id, a smart list's id"
		}
		if t.On != "enter" && t.On != "leave" {
			return "on is enter or leave"
		}
		if t.Event != "" || t.StartEvent != "" || t.WithinSeconds != 0 {
			return "an audience trigger takes list_id and on"
		}
		if properties {
			return "an audience rule tests the person's fields (attribute), not event properties"
		}
	default:
		return "trigger kind is " + strings.Join(Kinds, ", ")
	}
	if slices.Contains(WatchEvents(t), r.Emit) {
		return "a rule can't emit the event it listens to"
	}
	return ""
}

// WatchEvents are the events a rule listens to.
func WatchEvents(t Trigger) []string {
	switch t.Kind {
	case KindEvent:
		return []string{t.Event}
	case KindAbsence:
		if t.StartEvent != "" && t.StartEvent != t.Event {
			return []string{t.StartEvent, t.Event}
		}
		return []string{t.Event}
	}
	return []string{}
}

// WatchFields are the record fields a change rule tests: each attribute
// condition's top-level field.
func WatchFields(t Trigger, conds []Condition) []string {
	out := []string{}
	if t.Kind != KindChange {
		return out
	}
	for _, c := range conds {
		f, _, _ := strings.Cut(c.Attribute, ".")
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// Loops reports whether activating r would let rules set each other off in
// a circle: r's event sets off a rule whose event, in the end, sets off r.
// active are the workspace's other active rules.
func Loops(r Rule, active []Rule) bool {
	listening := map[string][]Rule{}
	for _, o := range active {
		if o.ID == r.ID {
			continue
		}
		for _, e := range WatchEvents(o.Trigger) {
			listening[e] = append(listening[e], o)
		}
	}
	mine := WatchEvents(r.Trigger)
	seen := map[string]bool{}
	next := []string{r.Emit}
	for len(next) > 0 {
		e := next[0]
		next = next[1:]
		if slices.Contains(mine, e) {
			return true
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		for _, o := range listening[e] {
			next = append(next, o.Emit)
		}
	}
	return false
}
