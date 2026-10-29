package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/playbooks"
	"github.com/outflareai/outflare/engine/internal/rules"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Kit export: a workspace's journeys, with the journeys, lists, rules and
// email drafts they use, written out as a flow kit (playbooks/flows.go),
// so a flow can be set up in another workspace or contributed as a
// playbook by pull request. Ids become the kit's own placeholders,
// sequences become inputs, and so do the events the person picks. It
// changes nothing.

const maxExportParts = 20

type kitExportInput struct {
	Journeys []string `json:"journeys"`
	Rules    []string `json:"rules"`
	Inputs   []string `json:"inputs"`
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
}

// kitExportEvent is an event the exported flows use, and the input it
// became, if the person asked for one.
type kitExportEvent struct {
	Name  string `json:"name"`
	Input string `json:"input,omitempty"`
}

type kitExport struct {
	Kit    playbooks.Kit    `json:"kit"`
	YAML   string           `json:"yaml"`
	Events []kitExportEvent `json:"events"`
	// What changed on the way out, for the person to know before sharing.
	Notes []string `json:"notes"`
}

// exporter gathers what the chosen journeys use, in one read-only
// transaction.
type exporter struct {
	ctx      context.Context
	tx       pgx.Tx
	a        *access
	taken    map[string]map[string]bool // keys in use, by kind
	journeys []*journeyView
	jKeys    map[string]string // journey id → key
	lists    []*smartListView
	lKeys    map[string]string // list id → key
	rules    []*ruleView
	ruleSeen map[string]bool
	tmpls    []playbooks.KitTemplate
	handles  map[string]string // a step's template ref → handle
	seqs     map[string]string // sequence id → input name
	inputs   []playbooks.KitInput
	notes    []string
}

func (s *Server) exportKit(w http.ResponseWriter, r *http.Request) {
	var in kitExportInput
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	if !a.has(auth.ScopeCustomersRead) {
		writeError(w, r, http.StatusForbidden, "forbidden", "exporting a kit reads smart lists and rules too, which needs the customers.read scope")
		return
	}
	switch {
	case len(in.Journeys) == 0 || len(in.Journeys) > maxExportParts:
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("journeys lists 1 to %d journey ids", maxExportParts))
		return
	case len(in.Rules) > maxExportParts:
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("rules lists at most %d rule ids", maxExportParts))
		return
	case len(in.Title) > 80 || len(in.Summary) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title is at most 80 characters and summary at most 200")
		return
	}
	var out *kitExport
	err := pgx.BeginTxFunc(r.Context(), s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		x := &exporter{ctx: r.Context(), tx: tx, a: a, taken: map[string]map[string]bool{}, jKeys: map[string]string{}, lKeys: map[string]string{},
			ruleSeen: map[string]bool{}, handles: map[string]string{}, seqs: map[string]string{}, notes: []string{}}
		var err error
		out, err = x.export(in)
		return err
	})
	if s.kitError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (x *exporter) export(in kitExportInput) (*kitExport, error) {
	for _, id := range in.Journeys {
		if _, err := x.journey(id); err != nil {
			return nil, err
		}
	}
	// Each journey brings the journeys, lists, sequences and templates its
	// steps use; journeys found on the way join the end of the list.
	for i := 0; i < len(x.journeys); i++ {
		if err := x.steps(x.journeys[i]); err != nil {
			return nil, err
		}
	}
	// Rules: the ones asked for, and the ones whose event starts a journey.
	for _, id := range in.Rules {
		if !validUUID(id) {
			return nil, errInvalid("rules: " + id + " is not a rule id")
		}
		v, err := scanRuleView(x.tx.QueryRow(x.ctx, ruleSelect+` AND id = $2 AND status <> 'archived'`, x.a.WorkspaceID, id))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !x.a.sees(v.BrandID)) {
			return nil, errInvalid("rules: " + id + " is not a rule in this workspace")
		}
		if err != nil {
			return nil, err
		}
		if err := x.rule(&v); err != nil {
			return nil, err
		}
	}
	for _, j := range x.journeys {
		if j.Trigger.Kind != "event" || j.Trigger.Event == nil {
			continue
		}
		rows, err := x.tx.Query(x.ctx, ruleSelect+` AND emit_event = $2 AND status <> 'archived' ORDER BY created_at`, x.a.WorkspaceID, *j.Trigger.Event)
		if err != nil {
			return nil, err
		}
		found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ruleView, error) { return scanRuleView(row) })
		if err != nil {
			return nil, err
		}
		for i := range found {
			if x.a.sees(found[i].BrandID) {
				if err := x.rule(&found[i]); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(x.rules) > maxExportParts || len(x.lists) > maxExportParts || len(x.journeys) > maxExportParts {
		return nil, errInvalid(fmt.Sprintf("a kit holds at most %d journeys, %[1]d rules and %[1]d lists; export fewer journeys", maxExportParts))
	}
	k, err := x.kit(in)
	if err != nil {
		return nil, err
	}
	events, err := x.eventInputs(&k, in.Inputs)
	if err != nil {
		return nil, err
	}
	k.Inputs = append(k.Inputs, x.inputs...)
	b, err := k.Marshal()
	if err != nil {
		return nil, err
	}
	// Read back and checked like a shipped kit, so it applies as written.
	back, err := playbooks.ParseKit(b)
	if err == nil {
		err = back.Validate()
	}
	if err != nil {
		return nil, errInvalid("these flows can't be a kit yet: " + err.Error())
	}
	return &kitExport{Kit: back, YAML: string(b), Events: events, Notes: x.notes}, nil
}

// journey loads a journey into the export, once, and returns its key.
func (x *exporter) journey(id string) (string, error) {
	if key, ok := x.jKeys[id]; ok {
		return key, nil
	}
	if !validUUID(id) {
		return "", errInvalid("journeys: " + id + " is not a journey id")
	}
	v, err := scanJourney(x.tx.QueryRow(x.ctx, journeySelect+` AND j.id = $2`, x.a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !x.a.sees(v.BrandID)) {
		return "", errInvalid("journeys: " + id + " is not a journey in this workspace")
	}
	if err != nil {
		return "", err
	}
	if v.DraftUpdatedAt != nil {
		x.note("The journey %s has unpublished changes; the kit has its published version.", v.Name)
	}
	x.jKeys[id] = x.key("journey", v.Name, v.Kit)
	x.journeys = append(x.journeys, &v)
	return x.jKeys[id], nil
}

// key makes a key for a part of this kind: the one a kit gave it, or one
// from its name. Each kind has keys of its own, as in kit files.
func (x *exporter) key(kind, name string, from *kitRef) string {
	if x.taken[kind] == nil {
		x.taken[kind] = map[string]bool{}
	}
	if from != nil && !x.taken[kind][from.Key] {
		x.taken[kind][from.Key] = true
		return from.Key
	}
	return playbooks.KeyFor(name, x.taken[kind])
}

func (x *exporter) note(format string, args ...any) {
	n := fmt.Sprintf(format, args...)
	if !slices.Contains(x.notes, n) {
		x.notes = append(x.notes, n)
	}
}

// steps swaps the ids in a journey's steps for the kit's placeholders,
// bringing in what they point at.
func (x *exporter) steps(j *journeyView) error {
	for i := range j.Steps {
		st := &j.Steps[i]
		where := fmt.Sprintf("the journey %s, step %s", j.Name, st.ID)
		switch {
		case st.Kind == "assign_owner":
			return errInvalid(where + " assigns workspace members as owners, which a kit can't carry: their ids belong to this workspace. Take the step out of a copy of the journey to export it")
		case st.Kind == "move_deal" && st.Deal != nil:
			x.note("The journey %s moves deals in the %s pipeline (%s); the workspace applying the kit needs a pipeline with the same keys.", j.Name, st.Deal.Type, st.Deal.Field)
		case st.Kind == journeys.KindEmail || st.Kind == "transactional_email":
			handle, err := x.template(st.Template, where)
			if err != nil {
				return err
			}
			st.Template = handle
		case st.ListID != "":
			key, err := x.list(st.ListID, smartlists.KindStatic, where)
			if err != nil {
				return err
			}
			st.ListID = "{{smart_list." + key + "}}"
		case st.JourneyID != "" && st.Scope != "others":
			key, err := x.journey(st.JourneyID)
			if err != nil {
				return errInvalid(where + " names a journey that isn't in this workspace")
			}
			st.JourneyID = "{{journey." + key + "}}"
		}
		if st.SequenceID != "" {
			st.SequenceID = "{{" + x.sequence(st.SequenceID, j.Name) + "}}"
		}
	}
	return nil
}

// sequence turns a sequence into an input: a sequence id means nothing in
// another workspace, so the person applying the kit picks one of theirs.
func (x *exporter) sequence(id, journey string) string {
	if name, ok := x.seqs[id]; ok {
		return name
	}
	var seq string
	_ = x.tx.QueryRow(x.ctx, `SELECT name FROM sequences WHERE workspace_id = $1 AND id::text = $2`, x.a.WorkspaceID, id).Scan(&seq)
	name := x.key("input", "sequence", nil)
	label := "The sequence to use"
	if seq != "" {
		label = cut("The sequence to use for "+seq, 80)
	}
	x.inputs = append(x.inputs, playbooks.KitInput{Name: name, Label: label, Kind: playbooks.InputSequence,
		Help: cut("The journey "+journey+" adds people to it, or stops it. Pick one of this workspace's sequences.", 300)})
	x.note("The journey %s uses a sequence; the kit asks for one as the input %s.", journey, name)
	x.seqs[id] = name
	return name
}

// list loads a smart or static list into the export, once.
func (x *exporter) list(id, kind, where string) (string, error) {
	if key, ok := x.lKeys[id]; ok {
		return key, nil
	}
	var v smartListView
	var err error
	if validUUID(id) {
		v, err = scanSmartList(x.tx.QueryRow(x.ctx, smartListSelect+` AND l.id = $2`, x.a.WorkspaceID, id))
	} else {
		err = pgx.ErrNoRows
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows) || (err == nil && !x.a.sees(v.BrandID)):
		return "", errInvalid(where + " names a list that isn't in this workspace")
	case err != nil:
		return "", err
	case v.Kind != kind:
		return "", errInvalid(where + " names " + v.Name + ", which is a " + v.Kind + " list; it needs a " + kind + " one")
	case len(smartlists.InLists(v.Rules))+len(smartlists.NewsSearches(v.Rules)) > 0:
		return "", errInvalid("the smart list " + v.Name + " is built on other lists or news searches of this workspace, which a kit can't carry")
	}
	x.lKeys[id] = x.key("smart_list", v.Name, v.Kit)
	x.lists = append(x.lists, &v)
	return x.lKeys[id], nil
}

// rule adds a rule to the export, once, with the list its audience
// trigger watches.
func (x *exporter) rule(v *ruleView) error {
	if x.ruleSeen[v.ID] {
		return nil
	}
	x.ruleSeen[v.ID] = true
	if v.Trigger.Kind == rules.KindAudience {
		key, err := x.list(v.Trigger.ListID, smartlists.KindSmart, "the rule "+v.Name)
		if err != nil {
			return err
		}
		v.Trigger.ListID = "{{smart_list." + key + "}}"
	}
	x.rules = append(x.rules, v)
	return nil
}

// template adds an email to the export, once, as a kit's email draft: its
// published version if it has one, otherwise the draft.
func (x *exporter) template(ref, where string) (string, error) {
	if handle, ok := x.handles[ref]; ok {
		return handle, nil
	}
	var t playbooks.KitTemplate
	var html string
	var published bool
	err := x.tx.QueryRow(x.ctx, `SELECT t.handle, t.name, t.purpose, COALESCE(v.subject, t.subject), COALESCE(v.html, t.html), COALESCE(v.text_body, t.text_body),
		v.version IS NOT NULL
		FROM email_templates t LEFT JOIN email_template_versions v ON v.template_id = t.id AND v.version = t.published_version
		WHERE t.workspace_id = $1 AND (t.handle = $2 OR t.id::text = $2)`, x.a.WorkspaceID, ref).
		Scan(&t.Handle, &t.Name, &t.Purpose, &t.Subject, &html, &t.Text, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errInvalid(where + " sends a template that isn't in this workspace")
	}
	if err != nil {
		return "", err
	}
	t.Text = strings.TrimSpace(t.Text)
	if t.Text == "" && html != "" {
		t.Text = strings.TrimSpace(emails.PlainText(html))
		x.note("The email %s has no text version; the kit carries its HTML as plain text, so check its layout after applying.", t.Name)
	} else if html != "" {
		x.note("Kits carry plain-text emails: the HTML version of %s is left out.", t.Name)
	}
	if !published {
		x.note("The email %s isn't published; the kit carries its draft.", t.Name)
	}
	x.handles[ref] = t.Handle
	if !slices.ContainsFunc(x.tmpls, func(o playbooks.KitTemplate) bool { return o.Handle == t.Handle }) {
		x.tmpls = append(x.tmpls, t)
	}
	return t.Handle, nil
}

// kit writes what was gathered as a kit.
func (x *exporter) kit(in kitExportInput) (playbooks.Kit, error) {
	first := x.journeys[0]
	k := playbooks.Kit{ID: in.ID, Version: 1, Title: strings.TrimSpace(in.Title), Summary: strings.TrimSpace(in.Summary), Templates: x.tmpls}
	if k.Title == "" {
		k.Title = cut(first.Name, 80)
	}
	if k.ID == "" {
		k.ID = playbooks.IDFor(k.Title)
	}
	if k.Summary == "" {
		k.Summary, _, _ = strings.Cut(strings.TrimSpace(first.Description), "\n")
	}
	if k.Summary == "" {
		k.Summary = "The journey " + first.Name + ", exported from Outflare."
	}
	k.Summary = cut(k.Summary, 200)
	for _, l := range x.lists {
		kl := playbooks.KitSmartList{Key: x.lKeys[l.ID], Name: l.Name, Description: l.Description, Kind: l.Kind}
		if l.Kind == smartlists.KindSmart {
			kl.Rules = generic(l.Rules).([]any)
			if l.Match != "all" {
				kl.Match = l.Match
			}
		}
		k.SmartLists = append(k.SmartLists, kl)
	}
	for _, r := range x.rules {
		kr := playbooks.KitRule{Key: x.key("rule", r.Name, r.Kit), Name: r.Name, Description: r.Description, Trigger: generic(r.Trigger).(map[string]any),
			EmitEvent: r.EmitEvent, CooldownSeconds: r.CooldownSeconds}
		if len(r.Conditions) > 0 {
			kr.Conditions = generic(r.Conditions).([]any)
		}
		kr.RecordType = unlessDefault(r.RecordType, "person")
		kr.Match = unlessDefault(r.Match, "all")
		kr.Fire = unlessDefault(r.Fire, "every_time")
		k.Rules = append(k.Rules, kr)
	}
	for _, j := range x.journeys {
		trigger := map[string]any{"kind": j.Trigger.Kind}
		if j.Trigger.Event != nil {
			trigger["event"] = *j.Trigger.Event
		}
		if len(j.Trigger.Filter) > 0 {
			trigger["filter"] = generic(j.Trigger.Filter)
		}
		kj := playbooks.KitJourney{Key: x.jKeys[j.ID], Name: j.Name, Description: j.Description, Trigger: trigger,
			Entry: unlessDefault(j.Entry, "once"), ExitEvents: j.ExitEvents, Steps: generic(j.Steps).([]any)}
		if j.GoalEvent != nil {
			kj.GoalEvent = *j.GoalEvent
		}
		if len(j.ExitConditions) > 0 {
			kj.ExitConditions = generic(j.ExitConditions).([]any)
		}
		k.Journeys = append(k.Journeys, kj)
	}
	return k, nil
}

// eventFields are the settings that name an event.
var eventFields = []string{"event", "start_event", "goal_event", "emit_event", "exit_events"}

// eventInputs lists the events the kit uses and turns the ones asked for
// into inputs, defaulting to the event itself, so the kit applies as it is
// and the person applying it can name their own.
func (x *exporter) eventInputs(k *playbooks.Kit, wanted []string) ([]kitExportEvent, error) {
	var names []string
	seen := func(e string) {
		if e != "" && !slices.Contains(names, e) {
			names = append(names, e)
		}
	}
	each := func(swap func(string) string) {
		for i := range k.SmartLists {
			k.SmartLists[i].Rules = walkEvents(k.SmartLists[i].Rules, false, swap).([]any)
		}
		for i := range k.Rules {
			r := &k.Rules[i]
			r.Trigger = walkEvents(r.Trigger, false, swap).(map[string]any)
			r.Conditions, _ = walkEvents(r.Conditions, false, swap).([]any)
			r.EmitEvent = swap(r.EmitEvent)
		}
		for i := range k.Journeys {
			j := &k.Journeys[i]
			j.Trigger = walkEvents(j.Trigger, false, swap).(map[string]any)
			j.Steps = walkEvents(j.Steps, false, swap).([]any)
			j.ExitConditions, _ = walkEvents(j.ExitConditions, false, swap).([]any)
			j.GoalEvent = swap(j.GoalEvent)
			for e := range j.ExitEvents {
				j.ExitEvents[e] = swap(j.ExitEvents[e])
			}
		}
	}
	each(func(e string) string { seen(e); return e })
	inputs := map[string]string{}
	for _, e := range wanted {
		if !slices.Contains(names, e) {
			return nil, errInvalid("inputs: " + e + " is not an event these flows use")
		}
		if _, ok := inputs[e]; ok {
			continue
		}
		name := x.key("input", e, nil)
		inputs[e] = name
		k.Inputs = append(k.Inputs, playbooks.KitInput{Name: name, Label: cut("The event for "+e, 80), Kind: playbooks.InputEvent, Example: e, Default: e,
			Help: cut("The flows were made with the event "+e+". Name the event your workspace sends for the same thing.", 300)})
	}
	each(func(e string) string {
		if name, ok := inputs[e]; ok {
			return "{{" + name + "}}"
		}
		return e
	})
	out := make([]kitExportEvent, 0, len(names))
	for _, e := range names {
		out = append(out, kitExportEvent{Name: e, Input: inputs[e]})
	}
	return out, nil
}

// walkEvents passes every event name in v, a value decoded from JSON,
// through swap. inEvent says v sits under one of eventFields.
func walkEvents(v any, inEvent bool, swap func(string) string) any {
	switch t := v.(type) {
	case string:
		if inEvent {
			return swap(t)
		}
	case []any:
		for i := range t {
			t[i] = walkEvents(t[i], inEvent, swap)
		}
	case map[string]any:
		for key, e := range t {
			t[key] = walkEvents(e, slices.Contains(eventFields, key), swap)
		}
	}
	return v
}

// generic turns a typed setting into the plain values a kit holds.
func generic(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		return []any{}
	}
	return out
}

func unlessDefault(v, def string) string {
	if v == def {
		return ""
	}
	return v
}

// cut shortens s to at most n bytes, on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n])
}
