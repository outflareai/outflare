// Package journeys runs if-this-then-that flows for people on customer
// records: an event on someone's timeline, or a manual add, starts a run
// that sends emails, waits, branches on conditions and exits. The API
// handlers in engine/internal/api own creating and changing journeys;
// this package checks their steps and moves runs along.
package journeys

import (
	"fmt"
	"regexp"
	"strings"
)

// Step kinds.
const (
	KindEmail  = "email"
	KindWait   = "wait"
	KindBranch = "branch"
	KindExit   = "exit"
)

// Step is one step of a journey. Next (or Then and Else for a branch) names
// the step after it; empty ends the run as completed.
type Step struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// A label people see; optional.
	Name string `json:"name,omitempty"`
	// email: a lifecycle template's handle or id.
	Template string `json:"template,omitempty"`
	// wait: how long, in seconds.
	WaitSeconds int64 `json:"wait_seconds,omitempty"`
	// branch: the conditions, and whether all or any must hold.
	Conditions []Condition `json:"conditions,omitempty"`
	Match      string      `json:"match,omitempty"`
	Then       string      `json:"then,omitempty"`
	Else       string      `json:"else,omitempty"`
	Next       string      `json:"next,omitempty"`
}

// Condition is one test on a person or what started their run.
//
//   - attribute: a field on their customer record (plan, country).
//   - property: a property of the event that started the run.
//   - event: whether they did this event since the run started.
//   - email with step: whether the email from that step was delivered,
//     opened or clicked.
//
// Op is eq, neq, gt, lt, contains, exists or not_exists. For event and
// email it is exists (did) or not_exists (didn't).
type Condition struct {
	Attribute string `json:"attribute,omitempty"`
	Property  string `json:"property,omitempty"`
	Event     string `json:"event,omitempty"`
	Email     string `json:"email,omitempty"`
	Step      string `json:"step,omitempty"`
	Op        string `json:"op"`
	Value     any    `json:"value,omitempty"`
}

var (
	stepIDRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	fieldRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}(\.[a-z0-9_]{1,62})*$`)
	eventNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	ops         = map[string]bool{"eq": true, "neq": true, "gt": true, "lt": true, "contains": true, "exists": true, "not_exists": true}
)

// MaxSteps is the most steps a journey can have.
const MaxSteps = 100

// ValidEventName reports whether name is a customer event name, as the
// catalog writes them (order_completed, email.opened).
func ValidEventName(name string) bool { return eventNameRE.MatchString(name) && len(name) <= 100 }

// Check reports what is wrong with a journey's steps, or "": unique ids,
// known kinds, what each kind needs, links to steps that exist, and no
// loops.
func Check(steps []Step) string {
	if len(steps) > MaxSteps {
		return fmt.Sprintf("a journey has at most %d steps", MaxSteps)
	}
	byID := map[string]*Step{}
	for i := range steps {
		st := &steps[i]
		if !stepIDRE.MatchString(st.ID) {
			return fmt.Sprintf("step %d: id must be 1 to 40 lowercase letters, digits, - or _", i+1)
		}
		if byID[st.ID] != nil {
			return "two steps have the id " + st.ID
		}
		byID[st.ID] = st
		if len(st.Name) > 100 {
			return "step " + st.ID + ": name must be at most 100 characters"
		}
		switch st.Kind {
		case KindEmail:
			if strings.TrimSpace(st.Template) == "" {
				return "step " + st.ID + ": an email step needs a template"
			}
		case KindWait:
			if st.WaitSeconds < 60 || st.WaitSeconds > 90*24*3600 {
				return "step " + st.ID + ": wait_seconds must be from 60 (a minute) to 7776000 (90 days)"
			}
		case KindBranch:
			if len(st.Conditions) == 0 || len(st.Conditions) > 20 {
				return "step " + st.ID + ": a branch needs 1 to 20 conditions"
			}
			if st.Match != "" && st.Match != "all" && st.Match != "any" {
				return "step " + st.ID + ": match is all or any"
			}
			for _, c := range st.Conditions {
				if msg := checkCondition(c); msg != "" {
					return "step " + st.ID + ": " + msg
				}
			}
			if st.Next != "" {
				return "step " + st.ID + ": a branch goes on through then and else, not next"
			}
		case KindExit:
			if st.Next != "" {
				return "step " + st.ID + ": an exit step ends the run, so it has no next"
			}
		default:
			return "step " + st.ID + ": kind is email, wait, branch or exit"
		}
	}
	for _, st := range steps {
		for _, to := range []string{st.Next, st.Then, st.Else} {
			if to != "" && byID[to] == nil {
				return "step " + st.ID + " goes to " + to + ", which is not a step"
			}
		}
		for _, c := range st.Conditions {
			if c.Step != "" && (byID[c.Step] == nil || byID[c.Step].Kind != KindEmail) {
				return "step " + st.ID + ": " + c.Step + " is not an email step"
			}
		}
	}
	// No loops: every run moves forward and ends.
	state := map[string]int{}
	var visit func(id string) bool
	visit = func(id string) bool {
		if id == "" {
			return true
		}
		switch state[id] {
		case 1:
			return false
		case 2:
			return true
		}
		state[id] = 1
		st := byID[id]
		ok := visit(st.Next) && visit(st.Then) && visit(st.Else)
		state[id] = 2
		return ok
	}
	for _, st := range steps {
		if !visit(st.ID) {
			return "the steps loop back on themselves at " + st.ID + "; a journey only moves forward"
		}
	}
	return ""
}

func checkCondition(c Condition) string {
	set := 0
	for _, v := range []string{c.Attribute, c.Property, c.Event, c.Email} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return "each condition tests one of attribute, property, event or email"
	}
	if !ops[c.Op] {
		return "op is eq, neq, gt, lt, contains, exists or not_exists"
	}
	switch {
	case c.Attribute != "" && !fieldRE.MatchString(c.Attribute), c.Property != "" && !fieldRE.MatchString(c.Property):
		return "attribute and property are field names such as plan or address.country"
	case c.Event != "" && !ValidEventName(c.Event):
		return "event must be an event name such as order_completed"
	case c.Email != "" && c.Email != "delivered" && c.Email != "opened" && c.Email != "clicked":
		return "email is delivered, opened or clicked"
	case c.Email != "" && c.Step == "":
		return "an email condition names the email step it is about"
	case (c.Event != "" || c.Email != "") && c.Op != "exists" && c.Op != "not_exists":
		return "event and email conditions use exists (did) or not_exists (didn't)"
	case c.Op != "exists" && c.Op != "not_exists" && c.Value == nil:
		return "op " + c.Op + " needs a value"
	}
	return ""
}

// Templates is every template a journey's steps send.
func Templates(steps []Step) []string {
	out := []string{}
	for _, st := range steps {
		if st.Kind == KindEmail {
			out = append(out, st.Template)
		}
	}
	return out
}

// CheckFilter reports what is wrong with a trigger's conditions, or "".
// They test the person and the event that starts the run, so only
// attribute and property conditions fit.
func CheckFilter(filter []Condition) string {
	if len(filter) > 20 {
		return "a trigger has at most 20 conditions"
	}
	for _, c := range filter {
		if c.Event != "" || c.Email != "" {
			return "trigger conditions test attribute or property; use a branch step for event and email conditions"
		}
		if msg := checkCondition(c); msg != "" {
			return msg
		}
	}
	return ""
}
