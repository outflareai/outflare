// Package journeys runs if-this-then-that flows for people on customer
// records: an event on someone's timeline, or a manual add, starts a run
// that sends emails, waits, branches on conditions and exits. The API
// handlers in engine/internal/api own creating and changing journeys;
// this package checks their steps and moves runs along.
package journeys

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Step kinds.
const (
	KindEmail = "email"

	// What quiet hours do to an email step.
	QuietHold  = "hold"
	QuietSkip  = "skip"
	KindWait   = "wait"
	KindBranch = "branch"
	KindExit   = "exit"
	// KindWaitUntil waits for one of its paths (an event, or conditions
	// coming true), or its timeout, whichever comes first.
	KindWaitUntil = "wait_until"
	// KindSplit sends people down one of its paths: the first whose
	// conditions hold (by value), or one picked at random by weight.
	KindSplit = "split"
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
	// email: send even over the workspace's frequency cap, for email a
	// person must get (a sign-in link, a trial ending).
	IgnoreCap bool `json:"ignore_cap,omitempty"`
	// email: what quiet hours do to it: hold (the default) sends it when
	// they end; skip doesn't send it, and the run goes on.
	QuietHours string `json:"quiet_hours,omitempty"`
	// wait: how long, in seconds.
	WaitSeconds int64 `json:"wait_seconds,omitempty"`
	// branch: the conditions, and whether all or any must hold.
	Conditions []Condition `json:"conditions,omitempty"`
	Match      string      `json:"match,omitempty"`
	Then       string      `json:"then,omitempty"`
	Else       string      `json:"else,omitempty"`
	Next       string      `json:"next,omitempty"`

	// Action steps (actions.go). Which settings a kind takes is in its
	// entry in Actions.
	Field           string `json:"field,omitempty"`
	Change          string `json:"change,omitempty"`
	Value           any    `json:"value,omitempty"`
	OverwriteManual bool   `json:"overwrite_manual,omitempty"`
	Tag             string `json:"tag,omitempty"`
	ListID          string `json:"list_id,omitempty"`
	JourneyID       string `json:"journey_id,omitempty"`
	SequenceID      string `json:"sequence_id,omitempty"`
	Scope           string `json:"scope,omitempty"`
	AllowElsewhere  bool   `json:"allow_elsewhere,omitempty"`
	Target          string `json:"target,omitempty"`
	Topic           string `json:"topic,omitempty"`
	Title           string `json:"title,omitempty"`
	Text            string `json:"text,omitempty"`
	To              string `json:"to,omitempty"`
	// webhook: the call, with {{person.x}}, {{event.x}} and {{vars.x}}
	// filled in the URL, header values and body; how it proves who may
	// call (the key itself is set apart, never in the steps); and where
	// its answer is kept for later steps (vars.<save_as>).
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
	Auth    *Auth             `json:"auth,omitempty"`
	SaveAs  string            `json:"save_as,omitempty"`
	// wait_until and split: the ways on, in order. wait_until also has
	// timeout_seconds and where to go then (timeout_next; empty ends the
	// run). split picks by value (the first path whose conditions hold,
	// else else) or at random (by weight, the same for a person, or for
	// everyone at their company with split_key company).
	Paths          []Path `json:"paths,omitempty"`
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty"`
	TimeoutNext    string `json:"timeout_next,omitempty"`
	SplitBy        string `json:"split_by,omitempty"`
	SplitKey       string `json:"split_key,omitempty"`
	// slack: the channel, and which Slack connection (the workspace's
	// first when empty). The message is text, with title in bold above it.
	Channel      string `json:"channel,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	// ai: what to ask the workspace's AI model, with {{…}} filled in; the
	// only answers allowed (choices, to classify), or at most how many
	// words to write. The answer is kept in vars.<save_as>.
	Prompt   string   `json:"prompt,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	MaxWords int      `json:"max_words,omitempty"`
	// push_to_sequencer: the campaign's own variables, by their name
	// there, with {{person.x}}, {{event.x}} and {{vars.x}} filled in.
	Variables map[string]string `json:"variables,omitempty"`
	// push_to_crm: the CRM's own properties, by their internal names
	// there, filled in the same way.
	Properties map[string]string `json:"properties,omitempty"`
	// sync_audience: one of the workspace's ad accounts, the platform's id
	// for a customer list audience on it, and change (add or remove).
	AdAccountID string `json:"ad_account_id,omitempty"`
	AudienceID  string `json:"audience_id,omitempty"`
	// What a failed action does: stop (the default: the run fails),
	// continue (go on to next) or a step id to go to.
	// set_consent: granted or withdrawn, for topic (empty: all marketing
	// email).
	Consent string `json:"consent,omitempty"`
	// assign_owner: one member (owner_id), or members to spread people
	// between evenly; replace_owner changes an owner already set.
	OwnerID      string   `json:"owner_id,omitempty"`
	Members      []string `json:"members,omitempty"`
	ReplaceOwner bool     `json:"replace_owner,omitempty"`
	// move_deal: the deal to move or open.
	Deal    *DealMove `json:"deal,omitempty"`
	OnError string    `json:"on_error,omitempty"`
}

// Path is one way on from a wait_until or split step. Its id names it in
// the step's results.
//
//   - wait_until: event (they do this event; conditions then test its
//     properties and their fields), or conditions alone (they come true).
//   - split by value: conditions, all or any of them (match).
//   - split at random: weight, a share of people; holdout marks the
//     people kept out on purpose, whose runs end there as exited.
type Path struct {
	ID         string      `json:"id"`
	Name       string      `json:"name,omitempty"`
	Event      string      `json:"event,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Match      string      `json:"match,omitempty"`
	Weight     int         `json:"weight,omitempty"`
	Holdout    bool        `json:"holdout,omitempty"`
	Next       string      `json:"next,omitempty"`
}

// Auth is how a webhook step's call proves it may call: type none (the
// default), header (name: the header, such as Authorization), basic
// (username; the key is the password) or query (name: the URL
// parameter, such as api_key). The key is set on its own (PUT
// .../step-secrets/{step}), sealed, and never shown again.
type Auth struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
}

// Condition is one test on a person or what started their run.
//
//   - attribute: a field on their customer record (plan, country).
//   - property: a property of the event that started the run.
//   - event: whether they did this event since the run started.
//   - email with step: whether the email from that step was delivered,
//     opened or clicked.
//   - var: something an earlier step saved in the run, such as
//     billing.status for a webhook's answer saved as billing.
//
// Op is eq, neq, gt, lt, contains, exists or not_exists. For event and
// email it is exists (did) or not_exists (didn't).
type Condition struct {
	Attribute string `json:"attribute,omitempty"`
	Property  string `json:"property,omitempty"`
	Event     string `json:"event,omitempty"`
	Email     string `json:"email,omitempty"`
	Var       string `json:"var,omitempty"`
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
			if st.QuietHours != "" && st.QuietHours != QuietHold && st.QuietHours != QuietSkip {
				return "step " + st.ID + ": quiet_hours is hold or skip"
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
		case KindWaitUntil, KindSplit:
			if msg := checkPaths(*st); msg != "" {
				return "step " + st.ID + ": " + msg
			}
		default:
			a := action(st.Kind)
			if a == nil {
				return "step " + st.ID + ": kind is one of " + strings.Join(KindNames(), ", ")
			}
			if msg := a.check(*st); msg != "" {
				return "step " + st.ID + ": " + msg
			}
		}
		if st.OnError != "" && (action(st.Kind) == nil) {
			return "step " + st.ID + ": on_error is for action steps"
		}
	}
	for _, st := range steps {
		for _, to := range st.Links() {
			if byID[to] == nil {
				return "step " + st.ID + " goes to " + to + ", which is not a step"
			}
		}
		conds := slices.Clone(st.Conditions)
		for _, p := range st.Paths {
			conds = append(conds, p.Conditions...)
		}
		for _, c := range conds {
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
		ok := true
		for _, to := range st.Links() {
			ok = ok && visit(to)
		}
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
	for _, v := range []string{c.Attribute, c.Property, c.Event, c.Email, c.Var} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return "each condition tests one of attribute, property, event, email or var"
	}
	if !ops[c.Op] {
		return "op is eq, neq, gt, lt, contains, exists or not_exists"
	}
	switch {
	case c.Attribute != "" && !fieldRE.MatchString(c.Attribute), c.Property != "" && !fieldRE.MatchString(c.Property):
		return "attribute and property are field names such as plan or address.country"
	case c.Var != "" && !fieldRE.MatchString(c.Var):
		return "var is a saved value's path, such as billing.status"
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

// CheckCondition reports what is wrong with one condition, or "".
func CheckCondition(c Condition) string { return checkCondition(c) }

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
		if c.Event != "" || c.Email != "" || c.Var != "" {
			return "trigger conditions test attribute or property; use a branch step for event, email and var conditions"
		}
		if msg := checkCondition(c); msg != "" {
			return msg
		}
	}
	return ""
}

// errorStep is the step on_error goes to, or "" for stop and continue.
func errorStep(onError string) string {
	if onError == "stop" || onError == "continue" {
		return ""
	}
	return onError
}

// Links is the steps a step goes on to: next, then and else, its paths
// and timeout, and where a failed action goes (on_error).
func (st Step) Links() []string {
	out := []string{}
	to := []string{st.Next, st.Then, st.Else, st.TimeoutNext, errorStep(st.OnError)}
	for _, p := range st.Paths {
		to = append(to, p.Next)
	}
	for _, to := range to {
		if to != "" {
			out = append(out, to)
		}
	}
	return out
}

// Unreachable is the ids of the steps no run can get to from the first
// step, in order. A journey can be saved with them while it is being
// built, but not published.
func Unreachable(steps []Step) []string {
	if len(steps) == 0 {
		return nil
	}
	byID := map[string]Step{}
	for _, st := range steps {
		byID[st.ID] = st
	}
	seen := map[string]bool{}
	todo := []string{steps[0].ID}
	for len(todo) > 0 {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		todo = append(todo, byID[id].Links()...)
	}
	var out []string
	for _, st := range steps {
		if !seen[st.ID] {
			out = append(out, st.ID)
		}
	}
	return out
}

// MaxPaths is the most paths a wait_until or split step can have.
const MaxPaths = 10

func checkPaths(st Step) string {
	if len(st.Paths) == 0 || len(st.Paths) > MaxPaths {
		return fmt.Sprintf("a %s step needs 1 to %d paths", st.Kind, MaxPaths)
	}
	if st.Next != "" || st.Then != "" {
		return "it goes on through its paths, not next or then"
	}
	ids := map[string]bool{}
	weights := 0
	for i, p := range st.Paths {
		at := fmt.Sprintf("path %d: ", i+1)
		if !stepIDRE.MatchString(p.ID) || ids[p.ID] {
			return at + "id must be unique: 1 to 40 lowercase letters, digits, - or _"
		}
		ids[p.ID] = true
		if len(p.Name) > 100 {
			return at + "name must be at most 100 characters"
		}
		if p.Match != "" && p.Match != "all" && p.Match != "any" {
			return at + "match is all or any"
		}
		if len(p.Conditions) > 20 {
			return at + "at most 20 conditions"
		}
		for _, c := range p.Conditions {
			if msg := checkCondition(c); msg != "" {
				return at + msg
			}
		}
		random := st.Kind == KindSplit && st.SplitBy == "random"
		switch {
		case p.Event != "" && (st.Kind != KindWaitUntil || !ValidEventName(p.Event)):
			return at + "event is for wait_until, and is an event name such as order_completed"
		case st.Kind == KindWaitUntil && p.Event == "" && len(p.Conditions) == 0:
			return at + "wait for an event or for conditions"
		case st.Kind == KindSplit && !random && len(p.Conditions) == 0:
			return at + "a split by value tests conditions"
		case random && len(p.Conditions) > 0:
			return at + "a random split doesn't test conditions"
		case random && (p.Weight < 0 || p.Weight > 100):
			return at + "weight is 0 to 100"
		case !random && (p.Weight != 0 || p.Holdout):
			return at + "weight and holdout are for a random split"
		case p.Holdout && p.Next != "":
			return at + "a holdout path ends there, so it has no next"
		}
		weights += p.Weight
	}
	switch st.Kind {
	case KindWaitUntil:
		if st.TimeoutSeconds < 60 || st.TimeoutSeconds > 90*24*3600 {
			return "timeout_seconds must be from 60 (a minute) to 7776000 (90 days)"
		}
		if st.SplitBy != "" || st.SplitKey != "" || st.Else != "" {
			return "split_by, split_key and else are for split steps"
		}
	case KindSplit:
		switch st.SplitBy {
		case "", "value":
			if st.SplitKey != "" {
				return "split_key is for a random split"
			}
		case "random":
			if weights == 0 {
				return "a random split needs a weight above 0"
			}
			if st.Else != "" {
				return "a random split always picks a path, so it has no else"
			}
		default:
			return "split_by is value or random"
		}
		if st.SplitKey != "" && st.SplitKey != "person" && st.SplitKey != "company" {
			return "split_key is person or company"
		}
		if st.TimeoutSeconds != 0 || st.TimeoutNext != "" {
			return "timeout_seconds and timeout_next are for wait_until"
		}
	}
	return ""
}
