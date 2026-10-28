// Package planner turns a goal typed in plain words ("book 10 meetings with
// fintech CFOs in Australia by 30 November") into a draft mission: a
// measurable goal, a plan of steps, limits, and the connectors the plan
// needs. It asks a model for the draft and checks the answer against what
// the workspace actually has, so a person reviews something that can run.
//
// The planner never acts. The draft becomes a mission in status draft, and
// everything after that (dry run, approval, running) goes through missions.
package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/llm"
)

// Metrics a mission's goal can be measured in.
var Metrics = []string{"meetings", "replies", "sends", "leads", "revenue_attributed", "revenue_deals"}

// Snapshot is what the planner knows about the workspace. Callers fill it
// from the database; nothing secret goes in it.
type Snapshot struct {
	Today     time.Time  `json:"today"`
	Workspace string     `json:"workspace"`
	Project   *Project   `json:"project,omitempty"`
	Mailboxes Mailboxes  `json:"mailboxes"`
	Leads     Leads      `json:"leads"`
	Sequences []Sequence `json:"sequences"`
	// Connectors already connected, by catalog id.
	Connected []string `json:"connected"`
	// Every connector the engine knows, connected or not.
	Catalog []CatalogItem `json:"catalog"`
}

// Project is the project the mission runs in, with its brief.
type Project struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Brief json.RawMessage `json:"brief,omitempty"`
}

// Mailboxes summarises sending capacity.
type Mailboxes struct {
	Total int `json:"total"`
	// Mailboxes that can send today (active, not paused, warmed).
	Ready int `json:"ready"`
	// Emails a day the ready mailboxes may send together.
	DailyCapacity int `json:"daily_capacity"`
}

// Leads summarises the lead base.
type Leads struct {
	Total      int `json:"total"`
	Suppressed int `json:"suppressed"`
}

// Sequence is an existing campaign the plan may reuse.
type Sequence struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// CatalogItem is one connector the plan may ask for.
type CatalogItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Status   string `json:"status"` // available or planned
	Summary  string `json:"summary"`
}

// Draft is the planner's proposal.
type Draft struct {
	Name       string `json:"name"`
	Goal       string `json:"goal"`
	GoalMetric string `json:"goal_metric"`
	GoalTarget int    `json:"goal_target"`
	// Date the mission should end by, YYYY-MM-DD, or "".
	EndsOn        string          `json:"ends_on"`
	Steps         []Step          `json:"steps"`
	Instructions  string          `json:"instructions"`
	MaxSends      int             `json:"max_sends"`
	MaxSpendCents int64           `json:"max_spend_cents"`
	Connectors    []ConnectorNeed `json:"connectors"`
	Questions     []string        `json:"questions"`
	Assumptions   []string        `json:"assumptions"`
}

// Step is one step of the plan.
type Step struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// ConnectorNeed is a connector the plan needs and does not have.
type ConnectorNeed struct {
	ID  string `json:"id"`
	Why string `json:"why"`
}

// Model answers a planning request with JSON matching schema.
type Model interface {
	Complete(ctx context.Context, system, prompt string, schema map[string]any) (json.RawMessage, Usage, error)
}

// Usage is what one model call cost.
type Usage struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	// The provider's own count, for pricing.
	LLM llm.Usage `json:"-"`
}

// ErrBadDraft is returned when the model's answer can't be used.
var ErrBadDraft = errors.New("the planner returned a draft that could not be used")

// Plan asks the model for a draft and checks it against the snapshot.
func Plan(ctx context.Context, m Model, goal string, snap Snapshot) (Draft, Usage, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return Draft{}, Usage{}, errors.New("goal is required")
	}
	snapJSON, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return Draft{}, Usage{}, err
	}
	prompt := "<workspace>\n" + string(snapJSON) + "\n</workspace>\n\n<goal>\n" + goal + "\n</goal>"
	raw, usage, err := m.Complete(ctx, systemPrompt, prompt, Schema())
	if err != nil {
		return Draft{}, usage, err
	}
	var d Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return Draft{}, usage, fmt.Errorf("%w: %v", ErrBadDraft, err)
	}
	if err := d.check(snap); err != nil {
		return Draft{}, usage, fmt.Errorf("%w: %v", ErrBadDraft, err)
	}
	return d, usage, nil
}

// check fixes what can be fixed and rejects what can't.
func (d *Draft) check(snap Snapshot) error {
	d.Name = clip(strings.TrimSpace(d.Name), 200)
	d.Goal = clip(strings.TrimSpace(d.Goal), 2000)
	if d.Name == "" || d.Goal == "" {
		return errors.New("name and goal are required")
	}
	known := false
	for _, m := range Metrics {
		known = known || d.GoalMetric == m
	}
	if !known || d.GoalTarget < 1 || d.GoalTarget > 1000000 {
		d.GoalMetric, d.GoalTarget = "", 0
	}
	if d.EndsOn != "" {
		t, err := time.Parse("2006-01-02", d.EndsOn)
		if err != nil || !t.After(snap.Today) {
			d.EndsOn = ""
		}
	}
	if len(d.Steps) == 0 {
		return errors.New("the plan has no steps")
	}
	if d.MaxSends < 0 || d.MaxSends > 10000000 {
		d.MaxSends = 0
	}
	if d.MaxSpendCents < 0 || d.MaxSpendCents > 100000000000 {
		d.MaxSpendCents = 0
	}
	// Keep only connectors that exist and are not connected yet.
	catalog := map[string]bool{}
	for _, c := range snap.Catalog {
		catalog[c.ID] = true
	}
	connected := map[string]bool{}
	for _, c := range snap.Connected {
		connected[c] = true
	}
	seen := map[string]bool{}
	needs := d.Connectors[:0]
	for _, c := range d.Connectors {
		if catalog[c.ID] && !connected[c.ID] && !seen[c.ID] {
			seen[c.ID] = true
			needs = append(needs, c)
		}
	}
	d.Connectors = needs
	return nil
}

// Markdown renders the plan as the mission's plan text.
func (d Draft) Markdown() string {
	var b strings.Builder
	for i, s := range d.Steps {
		title := strings.TrimSpace(s.Title)
		if !strings.ContainsAny(title[max(len(title)-1, 0):], ".:?!") {
			title += "."
		}
		fmt.Fprintf(&b, "%d. **%s**", i+1, title)
		if s.Detail = strings.TrimSpace(s.Detail); s.Detail != "" {
			b.WriteString(" " + s.Detail)
		}
		b.WriteString("\n")
	}
	if len(d.Assumptions) > 0 {
		b.WriteString("\nAssumptions:\n")
		for _, a := range d.Assumptions {
			b.WriteString("- " + strings.TrimSpace(a) + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Schema is the JSON schema the model's answer must match.
func Schema() map[string]any {
	str := map[string]any{"type": "string"}
	strs := map[string]any{"type": "array", "items": str}
	obj := func(props map[string]any) map[string]any {
		req := make([]string, 0, len(props))
		for k := range props {
			req = append(req, k)
		}
		return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
	}
	return obj(map[string]any{
		"name":            str,
		"goal":            str,
		"goal_metric":     map[string]any{"type": "string", "enum": []any{"meetings", "replies", "sends", "leads", "revenue_attributed", "revenue_deals"}},
		"goal_target":     map[string]any{"type": "integer"},
		"ends_on":         str,
		"steps":           map[string]any{"type": "array", "items": obj(map[string]any{"title": str, "detail": str})},
		"instructions":    str,
		"max_sends":       map[string]any{"type": "integer"},
		"max_spend_cents": map[string]any{"type": "integer"},
		"connectors":      map[string]any{"type": "array", "items": obj(map[string]any{"id": str, "why": str})},
		"questions":       strs,
		"assumptions":     strs,
	})
}

const systemPrompt = `You plan outbound go-to-market missions for Outflare, a control plane that runs cold email: mailboxes, leads, sequences, an inbox, and connectors to outside tools.

A person has typed a goal. Turn it into a mission draft that a person will review and approve before anything runs. The workspace JSON tells you what exists today: the project brief, sending mailboxes and their daily capacity, leads, campaigns, which connectors are connected, and the catalog of connectors that could be connected.

How to draft:
- name: short, like "Fintech CFOs, Australia".
- goal: one sentence restating what success is, in the person's terms.
- goal_metric and goal_target: the number the goal is measured in. Meetings booked is the usual one for "book meetings"; use replies, sends or leads only when the goal is about those. For a revenue goal in US dollars use revenue_attributed (payments credited to the mission's sequences) or revenue_deals (won deals tagged with the mission), with goal_target in whole dollars.
- ends_on: the deadline as YYYY-MM-DD if the goal gives or implies one, otherwise "".
- steps: 4 to 8 concrete steps in order, each one an action an agent can take through the Outflare API (find and import leads, verify emails, check suppression, write and create the sequence, assign mailboxes, start in a dry run, watch replies, hand interested replies to a person). Say which connector a step uses.
- instructions: what the agent must keep to while running: who to target and exclude, tone, claims it must not make, when to stop and ask.
- max_sends: an email cap that fits the goal and the mailboxes' daily capacity over the mission's length. Cold email books roughly one meeting per 200 to 400 emails; size the cap from that, not more than the mailboxes can send.
- max_spend_cents: a cap for paid tools (lead data, verification) in US cents, or 0 when the plan needs nothing paid.
- connectors: only catalog ids the plan needs and that are not in "connected", with why in one line. Prefer connectors whose status is available. Leave it empty when nothing is missing.
- questions: anything a person must answer before the plan can run well (at most 3). Don't ask what the brief already answers.
- assumptions: what you assumed that the person should check.

Never plan anything that breaks anti-spam law or the suppression list, buys personal data without a lawful basis, or sends from mailboxes that are not ready. If the workspace has no ready mailboxes, the first step is to set them up.`
