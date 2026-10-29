package playbooks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/rules"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// A kit can also bring a whole automation: smart lists, rules and journeys
// (flow kits). Applying the kit adds each one as a draft: a smart list
// fills but does nothing on its own, a rule and a journey stay in draft
// until a person turns them on. Their settings are written as the API
// takes them, and may hold placeholders:
//
//   - {{name}} is the value of one of the kit's inputs, such as the event
//     that means activated or the sequence to use;
//   - {{smart_list.key}} and {{journey.key}} are the ids of the kit's own
//     smart lists and journeys, so its parts can point at each other.
//
// Placeholders go only in text settings. Other dotted ones, such as
// {{person.first_name}} in an alert's text, are the step's own variables
// and stay as written, as do the kit's email drafts.

// KitInput is a value the person gives when applying the kit. A playbook
// that uses the kit takes an input of the same name.
type KitInput struct {
	Name     string `yaml:"name,omitempty" json:"name"`
	Label    string `yaml:"label,omitempty" json:"label"`
	Help     string `yaml:"help,omitempty" json:"help,omitempty"`
	Kind     string `yaml:"kind,omitempty" json:"kind"`
	Example  string `yaml:"example,omitempty" json:"example"`
	Default  string `yaml:"default,omitempty" json:"default,omitempty"`
	Optional bool   `yaml:"optional,omitempty" json:"optional"`
}

// Kinds of kit input: an event name, a sequence (its id), or plain text.
const (
	InputEvent    = "event"
	InputSequence = "sequence"
	InputText     = "text"
)

// KitSmartList is a smart list (kind smart, with rules) or a static list.
type KitSmartList struct {
	Key         string `yaml:"key,omitempty" json:"key"`
	Name        string `yaml:"name,omitempty" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Kind        string `yaml:"kind,omitempty" json:"kind"`
	Match       string `yaml:"match,omitempty" json:"match,omitempty"`
	Rules       []any  `yaml:"rules,omitempty" json:"rules"`
}

// KitRule is a rule, in the API's terms (POST .../rules).
type KitRule struct {
	Key             string         `yaml:"key,omitempty" json:"key"`
	Name            string         `yaml:"name,omitempty" json:"name"`
	Description     string         `yaml:"description,omitempty" json:"description,omitempty"`
	RecordType      string         `yaml:"record_type,omitempty" json:"record_type,omitempty"`
	Trigger         map[string]any `yaml:"trigger,omitempty" json:"trigger"`
	Conditions      []any          `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	Match           string         `yaml:"match,omitempty" json:"match,omitempty"`
	EmitEvent       string         `yaml:"emit_event,omitempty" json:"emit_event"`
	Fire            string         `yaml:"fire,omitempty" json:"fire,omitempty"`
	CooldownSeconds int64          `yaml:"cooldown_seconds,omitempty" json:"cooldown_seconds,omitempty"`
}

// KitJourney is a journey, in the API's terms (POST .../journeys).
type KitJourney struct {
	Key            string         `yaml:"key,omitempty" json:"key"`
	Name           string         `yaml:"name,omitempty" json:"name"`
	Description    string         `yaml:"description,omitempty" json:"description,omitempty"`
	Trigger        map[string]any `yaml:"trigger,omitempty" json:"trigger"`
	Entry          string         `yaml:"entry,omitempty" json:"entry,omitempty"`
	GoalEvent      string         `yaml:"goal_event,omitempty" json:"goal_event,omitempty"`
	ExitEvents     []string       `yaml:"exit_events,omitempty" json:"exit_events,omitempty"`
	ExitConditions []any          `yaml:"exit_conditions,omitempty" json:"exit_conditions,omitempty"`
	Steps          []any          `yaml:"steps,omitempty" json:"steps"`
}

// FlowList is a kit smart list with its placeholders filled in.
type FlowList struct {
	Key, Name, Description, Kind, Match string
	Rules                               []smartlists.Rule
}

// FlowRule is a kit rule with its placeholders filled in.
type FlowRule struct {
	Key, Name, Description string
	Rule                   rules.Rule
}

// FlowJourney is a kit journey with its placeholders filled in.
type FlowJourney struct {
	Key, Name, Description, Entry string
	TriggerKind                   string
	TriggerEvent, GoalEvent       *string
	TriggerFilter                 []journeys.Condition
	ExitEvents                    []string
	ExitConditions                []journeys.Condition
	Steps                         []journeys.Step
}

// Flows are a kit's smart lists, rules and journeys, ready to add.
type Flows struct {
	Lists    []FlowList
	Rules    []FlowRule
	Journeys []FlowJourney
}

// HasFlows reports whether the kit brings smart lists, rules or journeys.
func (k Kit) HasFlows() bool {
	return len(k.SmartLists)+len(k.Rules)+len(k.Journeys) > 0
}

var (
	slotRE       = regexp.MustCompile(`\{\{\s*([a-z_]+(?:\.[a-z0-9_]+)?)\s*\}\}`)
	textInputMax = 200
)

// fill replaces the placeholders in every string of v, a value decoded
// from YAML, and reports the first one it doesn't know.
func fill(v any, values map[string]string) (any, error) {
	switch x := v.(type) {
	case string:
		var bad string
		out := slotRE.ReplaceAllStringFunc(x, func(m string) string {
			name := slotRE.FindStringSubmatch(m)[1]
			val, ok := values[name]
			if !ok && templateVar(name) {
				return m
			}
			if !ok && bad == "" {
				bad = name
			}
			return val
		})
		if bad != "" {
			return nil, fmt.Errorf("{{%s}} is not one of the kit's inputs, smart lists or journeys", bad)
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			f, err := fill(e, values)
			if err != nil {
				return nil, err
			}
			out[i] = f
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			f, err := fill(e, values)
			if err != nil {
				return nil, err
			}
			out[k] = f
		}
		return out, nil
	}
	return v, nil
}

// templateVar reports whether a placeholder is a variable the step fills
// in when it runs, such as {{person.first_name}} in an alert's text,
// rather than one of the kit's own: those stay as written.
func templateVar(name string) bool {
	prefix, _, dotted := strings.Cut(name, ".")
	return dotted && prefix != "smart_list" && prefix != "journey"
}

// decodeFilled fills v's placeholders and decodes it into out as JSON, the
// way the API reads it.
func decodeFilled(v any, values map[string]string, out any) error {
	f, err := fill(v, values)
	if err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func fillString(s string, values map[string]string) (string, error) {
	f, err := fill(s, values)
	if err != nil {
		return "", err
	}
	return f.(string), nil
}

// Resolve fills in the kit's flows: values holds an input's value by name,
// and ids the id of each of the kit's smart lists and journeys by key. It
// checks each part as the API would, so applying never fails on the kit's
// own content.
func (k Kit) Resolve(values map[string]string, ids map[string]string) (Flows, error) {
	all := map[string]string{}
	for _, in := range k.Inputs {
		all[in.Name] = values[in.Name]
	}
	for _, l := range k.SmartLists {
		all["smart_list."+l.Key] = ids["smart_list."+l.Key]
	}
	for _, j := range k.Journeys {
		all["journey."+j.Key] = ids["journey."+j.Key]
	}
	var out Flows
	for _, l := range k.SmartLists {
		fl := FlowList{Key: l.Key, Name: l.Name, Description: l.Description, Kind: l.Kind, Match: l.Match, Rules: []smartlists.Rule{}}
		if fl.Kind == "" {
			fl.Kind = smartlists.KindSmart
		}
		if fl.Match == "" {
			fl.Match = "all"
		}
		if l.Rules != nil {
			if err := decodeFilled(l.Rules, all, &fl.Rules); err != nil {
				return out, fmt.Errorf("smart list %s: %w", l.Key, err)
			}
		}
		switch fl.Kind {
		case smartlists.KindSmart:
			if len(fl.Rules) == 0 {
				return out, fmt.Errorf("smart list %s needs rules (or kind static)", l.Key)
			}
			if msg := smartlists.Check(fl.Rules, fl.Match); msg != "" {
				return out, fmt.Errorf("smart list %s: %s", l.Key, msg)
			}
			if len(smartlists.InLists(fl.Rules))+len(smartlists.NewsSearches(fl.Rules)) > 0 {
				return out, fmt.Errorf("smart list %s: in_list and news_search name a workspace's own lists, which a kit can't know", l.Key)
			}
		case smartlists.KindStatic:
			if len(fl.Rules) > 0 {
				return out, fmt.Errorf("smart list %s: a static list has no rules", l.Key)
			}
		default:
			return out, fmt.Errorf("smart list %s: kind is smart or static", l.Key)
		}
		out.Lists = append(out.Lists, fl)
	}
	for _, r := range k.Rules {
		fr := FlowRule{Key: r.Key, Name: r.Name, Description: r.Description}
		rr := rules.Rule{RecordType: r.RecordType, Match: r.Match, Fire: r.Fire, Cooldown: r.CooldownSeconds, Conditions: []rules.Condition{}}
		if rr.RecordType == "" {
			rr.RecordType = "person"
		}
		if rr.Match == "" {
			rr.Match = "all"
		}
		if rr.Fire == "" {
			rr.Fire = "every_time"
		}
		if err := decodeFilled(r.Trigger, all, &rr.Trigger); err != nil {
			return out, fmt.Errorf("rule %s: trigger: %w", r.Key, err)
		}
		if r.Conditions != nil {
			if err := decodeFilled(r.Conditions, all, &rr.Conditions); err != nil {
				return out, fmt.Errorf("rule %s: conditions: %w", r.Key, err)
			}
		}
		var err error
		if rr.Emit, err = fillString(r.EmitEvent, all); err != nil {
			return out, fmt.Errorf("rule %s: emit_event: %w", r.Key, err)
		}
		if msg := rules.Check(rr); msg != "" {
			return out, fmt.Errorf("rule %s: %s", r.Key, msg)
		}
		if rr.Trigger.Kind == rules.KindAudience && !slices.ContainsFunc(out.Lists, func(l FlowList) bool {
			return l.Kind == smartlists.KindSmart && ids["smart_list."+l.Key] == rr.Trigger.ListID
		}) {
			return out, fmt.Errorf("rule %s: an audience trigger's list_id is one of the kit's smart lists, as {{smart_list.key}}", r.Key)
		}
		fr.Rule = rr
		out.Rules = append(out.Rules, fr)
	}
	for _, j := range k.Journeys {
		fj := FlowJourney{Key: j.Key, Name: j.Name, Description: j.Description, Entry: j.Entry,
			TriggerFilter: []journeys.Condition{}, ExitEvents: []string{}, ExitConditions: []journeys.Condition{}, Steps: []journeys.Step{}}
		if fj.Entry == "" {
			fj.Entry = "once"
		}
		var trigger struct {
			Kind   string               `json:"kind"`
			Event  string               `json:"event"`
			Filter []journeys.Condition `json:"filter"`
		}
		if err := decodeFilled(j.Trigger, all, &trigger); err != nil {
			return out, fmt.Errorf("journey %s: trigger: %w", j.Key, err)
		}
		fj.TriggerKind = trigger.Kind
		switch trigger.Kind {
		case "event":
			if !journeys.ValidEventName(trigger.Event) {
				return out, fmt.Errorf("journey %s: an event trigger needs event, an event name", j.Key)
			}
			fj.TriggerEvent = &trigger.Event
		case "manual":
			if trigger.Event != "" || len(trigger.Filter) > 0 {
				return out, fmt.Errorf("journey %s: a manual trigger has no event or filter", j.Key)
			}
		default:
			return out, fmt.Errorf("journey %s: trigger kind is event or manual", j.Key)
		}
		if trigger.Filter != nil {
			fj.TriggerFilter = trigger.Filter
		}
		if msg := journeys.CheckFilter(fj.TriggerFilter); msg != "" {
			return out, fmt.Errorf("journey %s: trigger: %s", j.Key, msg)
		}
		if fj.Entry != "once" && fj.Entry != "again" {
			return out, fmt.Errorf("journey %s: entry is once or again", j.Key)
		}
		if j.GoalEvent != "" {
			g, err := fillString(j.GoalEvent, all)
			if err != nil {
				return out, fmt.Errorf("journey %s: goal_event: %w", j.Key, err)
			}
			if !journeys.ValidEventName(g) {
				return out, fmt.Errorf("journey %s: goal_event must be an event name", j.Key)
			}
			fj.GoalEvent = &g
		}
		if len(j.ExitEvents) > 10 {
			return out, fmt.Errorf("journey %s: at most 10 exit events", j.Key)
		}
		for _, e := range j.ExitEvents {
			f, err := fillString(e, all)
			if err != nil || !journeys.ValidEventName(f) {
				return out, fmt.Errorf("journey %s: exit_events are event names", j.Key)
			}
			fj.ExitEvents = append(fj.ExitEvents, f)
		}
		if j.ExitConditions != nil {
			if err := decodeFilled(j.ExitConditions, all, &fj.ExitConditions); err != nil {
				return out, fmt.Errorf("journey %s: exit_conditions: %w", j.Key, err)
			}
			if msg := journeys.CheckFilter(fj.ExitConditions); msg != "" {
				return out, fmt.Errorf("journey %s: exit_conditions: %s", j.Key, msg)
			}
		}
		if err := decodeFilled(j.Steps, all, &fj.Steps); err != nil {
			return out, fmt.Errorf("journey %s: steps: %w", j.Key, err)
		}
		if msg := journeys.Check(fj.Steps); msg != "" {
			return out, fmt.Errorf("journey %s: %s", j.Key, msg)
		}
		if bad := journeys.Unreachable(fj.Steps); len(bad) > 0 {
			return out, fmt.Errorf("journey %s: nothing leads to step %s", j.Key, bad[0])
		}
		for _, st := range fj.Steps {
			if st.Kind == journeys.KindEmail && !slices.ContainsFunc(k.Templates, func(t KitTemplate) bool { return t.Handle == st.Template && t.Purpose == "lifecycle" }) {
				return out, fmt.Errorf("journey %s: step %s sends %q, which is not one of the kit's lifecycle email drafts", j.Key, st.ID, st.Template)
			}
			if st.Kind == "transactional_email" && !slices.ContainsFunc(k.Templates, func(t KitTemplate) bool { return t.Handle == st.Template && t.Purpose == "transactional" }) {
				return out, fmt.Errorf("journey %s: step %s sends %q, which is not one of the kit's transactional email drafts", j.Key, st.ID, st.Template)
			}
			if st.Kind == "assign_owner" {
				return out, fmt.Errorf("journey %s: step %s assigns workspace members, whose ids a kit can't know", j.Key, st.ID)
			}
			if (st.Kind == "add_to_list" || st.Kind == "remove_from_list") && !slices.ContainsFunc(out.Lists, func(l FlowList) bool {
				return l.Kind == smartlists.KindStatic && ids["smart_list."+l.Key] == st.ListID
			}) {
				return out, fmt.Errorf("journey %s: step %s: list_id is one of the kit's static lists, as {{smart_list.key}}", j.Key, st.ID)
			}
			if (st.Kind == "enter_journey" || (st.Kind == "exit_journey" && st.Scope != "others")) && !slices.ContainsFunc(k.Journeys, func(o KitJourney) bool {
				return ids["journey."+o.Key] == st.JourneyID
			}) {
				return out, fmt.Errorf("journey %s: step %s: journey_id is one of the kit's journeys, as {{journey.key}}", j.Key, st.ID)
			}
		}
		out.Journeys = append(out.Journeys, fj)
	}
	return out, nil
}

// CheckInputs checks the values given for the kit's inputs, filling in
// defaults, and reports the first problem. Whether a sequence is the
// workspace's is for the caller to check.
func (k Kit) CheckInputs(values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for name := range values {
		if !slices.ContainsFunc(k.Inputs, func(in KitInput) bool { return in.Name == name }) {
			return nil, fmt.Errorf("the kit %s has no input %s", k.ID, name)
		}
	}
	for _, in := range k.Inputs {
		v := strings.TrimSpace(values[in.Name])
		if v == "" {
			v = in.Default
		}
		if v == "" {
			if !in.Optional {
				return nil, fmt.Errorf("%s is required (%s)", in.Name, in.Label)
			}
			out[in.Name] = ""
			continue
		}
		switch in.Kind {
		case InputEvent:
			if !journeys.ValidEventName(v) {
				return nil, fmt.Errorf("%s must be an event name such as project_created", in.Name)
			}
		case InputSequence:
			if !uuidRE.MatchString(v) {
				return nil, fmt.Errorf("%s must be a sequence's id", in.Name)
			}
		default:
			if len(v) > textInputMax {
				return nil, fmt.Errorf("%s is at most %d characters", in.Name, textInputMax)
			}
		}
		out[in.Name] = v
	}
	return out, nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// exampleValues are the inputs' examples (sequences get a made-up id), and
// exampleIDs made-up ids for the kit's parts: what Validate resolves the
// kit with.
func (k Kit) exampleValues() (map[string]string, map[string]string) {
	values := map[string]string{}
	for _, in := range k.Inputs {
		v := in.Example
		if in.Kind == InputSequence {
			v = "00000000-0000-4000-8000-000000000001"
		}
		values[in.Name] = v
	}
	ids := map[string]string{}
	n := 0x10
	for _, l := range k.SmartLists {
		ids["smart_list."+l.Key] = fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
		n++
	}
	for _, j := range k.Journeys {
		ids["journey."+j.Key] = fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
		n++
	}
	return values, ids
}

// validateFlows checks the kit's inputs and flows.
func (k Kit) validateFlows(bad func(string, ...any)) {
	names := map[string]bool{}
	for _, in := range k.Inputs {
		switch {
		case !kitKey.MatchString(in.Name) || names[in.Name]:
			bad("input %q needs a unique name of lowercase letters, numbers and underscores", in.Name)
		case in.Label == "" || len(in.Label) > 80:
			bad("input %q needs a label of at most 80 characters", in.Name)
		case in.Kind != InputEvent && in.Kind != InputSequence && in.Kind != InputText:
			bad("input %q: kind is event, sequence or text", in.Name)
		case in.Example == "" && in.Kind != InputSequence:
			bad("input %q needs an example", in.Name)
		case len(in.Help) > 300:
			bad("input %q: help is at most 300 characters", in.Name)
		}
		names[in.Name] = true
	}
	keys := map[string]bool{}
	part := func(kind, key, name string) {
		switch {
		case !kitKey.MatchString(key) || keys[kind+key]:
			bad("%s %q needs a unique key of lowercase letters, numbers and underscores", kind, key)
		case name == "" || len(name) > 200:
			bad("%s %q needs a name of at most 200 characters", kind, key)
		}
		keys[kind+key] = true
	}
	for _, l := range k.SmartLists {
		part("smart list", l.Key, l.Name)
	}
	for _, r := range k.Rules {
		part("rule", r.Key, r.Name)
	}
	for _, j := range k.Journeys {
		part("journey", j.Key, j.Name)
	}
	if !k.HasFlows() {
		return
	}
	values, ids := k.exampleValues()
	if _, err := k.Resolve(values, ids); err != nil {
		bad("%v", err)
	}
}
