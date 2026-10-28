// Package playbooks reads the playbooks in the repository's playbooks/
// folder: ready-made recipes that say what data and connections they need,
// which of Outflare's own tools each step uses, and the prompt an agent
// runs. A playbook has no code and no tables of its own. Running one
// creates a mission (package api), so the mission's approvals, limits and
// audit apply to everything it does.
package playbooks

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	"github.com/outflareai/outflare/engine/internal/connectors"
	folder "github.com/outflareai/outflare/playbooks"
)

// Tiers, from playbooks that suit almost anyone to niche ones.
const (
	TierObvious = "obvious"
	TierClever  = "clever"
	TierEdge    = "edge"
)

// Tiers lists every tier, in order.
var Tiers = []string{TierObvious, TierClever, TierEdge}

// Goals a playbook can serve.
var Goals = []string{"customers", "revenue", "activation", "retention", "partners", "search", "ai_answers", "tracking", "replies"}

// Publishers: playbooks the Outflare team looks after, and everyone else.
const (
	PublisherOutflare  = "outflare"
	PublisherCommunity = "community"
)

// Data checks a playbook can ask for. Each is a cheap count of rows the
// workspace already has (Readiness in readiness.go).
var DataChecks = map[string]string{
	"records":         "Customer records",
	"customer_events": "Sign-up and product events",
	"inbox":           "Replies in the inbox",
	"site_tracking":   "Visits from Outflare's site tracking",
	"seo_site":        "A site added to SEO",
	"mailboxes":       "Mailboxes for outreach",
	"leads":           "Leads",
}

// Playbook is one playbook.yaml.
type Playbook struct {
	ID          string    `yaml:"id" json:"id"`
	Version     int       `yaml:"version" json:"version"`
	Title       string    `yaml:"title" json:"title"`
	Summary     string    `yaml:"summary" json:"summary"`
	Description string    `yaml:"description" json:"description"`
	Tier        string    `yaml:"tier" json:"tier"`
	Goals       []string  `yaml:"goals" json:"goals"`
	Publisher   string    `yaml:"publisher" json:"publisher"`
	Author      Author    `yaml:"author" json:"author"`
	Needs       Needs     `yaml:"needs" json:"needs"`
	Inputs      []Input   `yaml:"inputs" json:"inputs"`
	Steps       []Step    `yaml:"steps" json:"steps"`
	Guardrails  []string  `yaml:"guardrails" json:"guardrails"`
	Measure     []Measure `yaml:"measure" json:"measure"`
	Prompt      string    `yaml:"prompt" json:"prompt"`
	Example     string    `yaml:"example" json:"example"`
	// Kit names the setup kit (kits/<id>.yaml) the playbook offers to
	// apply before its first run, if any.
	Kit string `yaml:"kit" json:"kit,omitempty"`
}

// Author is who wrote the playbook, by GitHub handle.
type Author struct {
	GitHub string `yaml:"github" json:"github"`
	Name   string `yaml:"name" json:"name,omitempty"`
}

// Needs is what the workspace must have before the playbook runs.
type Needs struct {
	Data        []DataNeed       `yaml:"data" json:"data"`
	Connections []ConnectionNeed `yaml:"connections" json:"connections"`
}

// DataNeed is data the workspace already collects.
type DataNeed struct {
	Check string `yaml:"check" json:"check"`
	Label string `yaml:"label" json:"label"`
}

// ConnectionNeed is a connected account. Any connector with the capability
// (or in the category) counts; Recommended lists the ones the author
// suggests, best first.
type ConnectionNeed struct {
	Capability  string   `yaml:"capability" json:"capability,omitempty"`
	Category    string   `yaml:"category" json:"category,omitempty"`
	Label       string   `yaml:"label" json:"label"`
	Optional    bool     `yaml:"optional" json:"optional"`
	Recommended []string `yaml:"recommended" json:"recommended"`
}

// Input is a value the person fills in before running, used in the prompt
// as {{name}}.
type Input struct {
	Name     string `yaml:"name" json:"name"`
	Label    string `yaml:"label" json:"label"`
	Help     string `yaml:"help" json:"help,omitempty"`
	Example  string `yaml:"example" json:"example"`
	Optional bool   `yaml:"optional" json:"optional"`
}

// Step is one thing the agent does, with the API operations (and so the
// MCP tools) it uses.
type Step struct {
	Say   string   `yaml:"say" json:"say"`
	Tools []string `yaml:"tools" json:"tools"`
}

// Measure is a number that shows the playbook worked.
type Measure struct {
	Metric string `yaml:"metric" json:"metric"`
	How    string `yaml:"how" json:"how"`
}

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	inputPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	githubPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	placeholder   = regexp.MustCompile(`\{\{\s*([^{}\s]*)\s*\}\}`)
)

// Parse reads one playbook.yaml. Unknown keys are errors, so a typo never
// goes unnoticed.
func Parse(b []byte) (Playbook, error) {
	var p Playbook
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&p); err != nil {
		return Playbook{}, err
	}
	p.normal()
	return p, nil
}

func (p *Playbook) normal() {
	p.Summary = strings.TrimSpace(p.Summary)
	p.Description = unwrap(p.Description)
	p.Prompt = unwrap(p.Prompt)
	p.Example = unwrap(p.Example)
	for _, s := range []*[]string{&p.Goals, &p.Guardrails} {
		if *s == nil {
			*s = []string{}
		}
	}
	if p.Needs.Data == nil {
		p.Needs.Data = []DataNeed{}
	}
	if p.Needs.Connections == nil {
		p.Needs.Connections = []ConnectionNeed{}
	}
	for i := range p.Needs.Connections {
		if p.Needs.Connections[i].Recommended == nil {
			p.Needs.Connections[i].Recommended = []string{}
		}
	}
	if p.Inputs == nil {
		p.Inputs = []Input{}
	}
	if p.Measure == nil {
		p.Measure = []Measure{}
	}
	for i := range p.Steps {
		if p.Steps[i].Tools == nil {
			p.Steps[i].Tools = []string{}
		}
	}
}

// unwrap joins the lines of each paragraph, so text wrapped in the YAML
// file reads as running text wherever it is shown. Blank lines still
// separate paragraphs.
func unwrap(s string) string {
	paras := strings.Split(strings.TrimSpace(s), "\n\n")
	for i, p := range paras {
		paras[i] = strings.Join(strings.Fields(p), " ")
	}
	return strings.Join(paras, "\n\n")
}

// Catalog is what a playbook may refer to: the API's operations and the
// connectors the engine ships. The api package builds it from the OpenAPI
// document and the connector registry.
type Catalog struct {
	// Operations are the API's operationIds; each is also an MCP tool.
	Operations map[string]bool
	// Capabilities and Categories of available connectors.
	Capabilities map[string]bool
	Categories   map[string]bool
	// Connectors maps a connector id to its category and capabilities.
	Connectors map[string]ConnectorInfo
}

// ConnectorInfo is what Validate needs to know about one connector.
type ConnectorInfo struct {
	Category     string
	Capabilities []string
}

// Validate checks a playbook against the rules in playbooks/README.md and,
// when c is not nil, that every tool, capability and recommended connector
// exists.
func (p Playbook) Validate(c *Catalog) error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !idPattern.MatchString(p.ID) || len(p.ID) > 60 || p.ID == "kits" {
		bad("id must be lowercase words joined by hyphens, at most 60 characters, and not kits")
	}
	if p.Version < 1 {
		bad("version must be 1 or more; raise it whenever the steps or prompt change")
	}
	if n := len(p.Title); n == 0 || n > 80 {
		bad("title is required, at most 80 characters")
	}
	if n := len(p.Summary); n == 0 || n > 160 {
		bad("summary is required, one sentence of at most 160 characters")
	}
	if len(p.Description) > 2000 {
		bad("description is at most 2,000 characters")
	}
	if !slices.Contains(Tiers, p.Tier) {
		bad("tier must be one of %s", strings.Join(Tiers, ", "))
	}
	if len(p.Goals) == 0 {
		bad("goals needs at least one of %s", strings.Join(Goals, ", "))
	}
	for _, g := range p.Goals {
		if !slices.Contains(Goals, g) {
			bad("goal %q is not one of %s", g, strings.Join(Goals, ", "))
		}
	}
	if p.Publisher != PublisherOutflare && p.Publisher != PublisherCommunity {
		bad("publisher must be outflare or community")
	}
	if !githubPattern.MatchString(p.Author.GitHub) {
		bad("author.github must be the author's GitHub handle")
	}
	for _, d := range p.Needs.Data {
		if _, ok := DataChecks[d.Check]; !ok {
			bad("needs.data check %q is not one of %s", d.Check, strings.Join(dataCheckNames(), ", "))
		}
		if d.Label == "" {
			bad("needs.data %q needs a label", d.Check)
		}
	}
	for _, n := range p.Needs.Connections {
		switch {
		case (n.Capability == "") == (n.Category == ""):
			bad("each needs.connections entry has a capability or a category, not both")
		case c != nil && n.Capability != "" && !c.Capabilities[n.Capability]:
			bad("no connector has capability %q", n.Capability)
		case c != nil && n.Category != "" && !c.Categories[n.Category]:
			bad("no connector is in category %q", n.Category)
		}
		if n.Label == "" {
			bad("needs.connections %s needs a label", n.Capability+n.Category)
		}
		for _, id := range n.Recommended {
			if c == nil {
				continue
			}
			info, ok := c.Connectors[id]
			if !ok {
				bad("recommended connector %q is not in the catalog (GET /v1/connectors); add it under integrations/ first", id)
				continue
			}
			if !n.matches(info) {
				bad("recommended connector %q does not offer %s", id, n.Capability+n.Category)
			}
		}
	}
	inputs := map[string]bool{}
	for _, in := range p.Inputs {
		if !inputPattern.MatchString(in.Name) {
			bad("input name %q must be lowercase letters, digits and _", in.Name)
		}
		if inputs[in.Name] {
			bad("input %q is listed twice", in.Name)
		}
		inputs[in.Name] = true
		if in.Label == "" || in.Example == "" {
			bad("input %q needs a label and an example", in.Name)
		}
	}
	if len(p.Steps) == 0 {
		bad("steps needs at least one step")
	}
	for i, s := range p.Steps {
		if s.Say == "" {
			bad("step %d needs say", i+1)
		}
		for _, t := range s.Tools {
			if c != nil && !c.Operations[t] {
				bad("step %d uses %q, which is not an operationId in api/openapi.yaml", i+1, t)
			}
		}
	}
	if len(p.Measure) == 0 {
		bad("measure needs at least one metric, so people can tell it worked")
	}
	if n := len(p.Prompt); n == 0 || n > 4000 {
		bad("prompt is required, at most 4,000 characters")
	}
	used := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(p.Prompt, -1) {
		used[m[1]] = true
		if !inputs[m[1]] {
			bad("prompt uses {{%s}}, which is not an input", m[1])
		}
	}
	for name := range inputs {
		if !used[name] {
			bad("input %q is never used in the prompt", name)
		}
	}
	return errors.Join(errs...)
}

func (n ConnectionNeed) matches(info ConnectorInfo) bool {
	if n.Category != "" {
		return info.Category == n.Category
	}
	return slices.Contains(info.Capabilities, n.Capability)
}

func dataCheckNames() []string {
	out := make([]string, 0, len(DataChecks))
	for k := range DataChecks {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Render fills the prompt's {{inputs}}. A missing value keeps its
// placeholder, so the agent can ask for it.
func (p Playbook) Render(values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(p.Prompt, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		if v := strings.TrimSpace(values[name]); v != "" {
			return v
		}
		return m
	})
}

// Load reads every playbook in fsys (one folder per playbook, each with a
// playbook.yaml), checks it with Validate and that its folder is its id,
// and returns them sorted by tier then title.
func Load(fsys fs.FS, c *Catalog) ([]Playbook, error) {
	files, err := fs.Glob(fsys, "*/playbook.yaml")
	if err != nil {
		return nil, err
	}
	kits, err := LoadKits(fsys)
	if err != nil {
		return nil, err
	}
	var out []Playbook
	var errs []error
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		p, err := Parse(b)
		if err == nil {
			err = p.Validate(c)
		}
		if dir := path.Dir(f); err == nil && dir != p.ID {
			err = fmt.Errorf("the folder is %q but the id is %q; they must match", dir, p.ID)
		}
		if err == nil && p.Kit != "" && !slices.ContainsFunc(kits, func(k Kit) bool { return k.ID == p.Kit }) {
			err = fmt.Errorf("kit %q is not in playbooks/kits", p.Kit)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("playbooks/%s: %w", f, err))
			continue
		}
		out = append(out, p)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := slices.Index(Tiers, out[i].Tier), slices.Index(Tiers, out[j].Tier)
		if ti != tj {
			return ti < tj
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

var (
	once    sync.Once
	all     []Playbook
	loadErr error
)

// All returns the playbooks shipped in the repository's playbooks/ folder.
// They are checked in full by the tests (playbooks_test.go), so here only
// the format is checked.
func All() ([]Playbook, error) {
	once.Do(func() { all, loadErr = Load(folder.FS, nil) })
	return all, loadErr
}

// Get returns one shipped playbook.
func Get(id string) (Playbook, bool) {
	list, _ := All()
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return Playbook{}, false
}

// NewCatalog builds a Catalog from the OpenAPI document and the connector
// definitions (connectors.Registry.All). Only available connectors count.
func NewCatalog(spec []byte, defs []connectors.Definition) *Catalog {
	c := &Catalog{Operations: map[string]bool{}, Capabilities: map[string]bool{}, Categories: map[string]bool{}, Connectors: map[string]ConnectorInfo{}}
	for _, line := range strings.Split(string(spec), "\n") {
		if op, ok := strings.CutPrefix(strings.TrimSpace(line), "operationId:"); ok {
			c.Operations[strings.TrimSpace(op)] = true
		}
	}
	for _, d := range defs {
		if d.Status != connectors.Available {
			continue
		}
		c.Connectors[d.ID] = ConnectorInfo{Category: d.Category, Capabilities: d.Capabilities}
		c.Categories[d.Category] = true
		for _, cap := range d.Capabilities {
			c.Capabilities[cap] = true
		}
	}
	return c
}
