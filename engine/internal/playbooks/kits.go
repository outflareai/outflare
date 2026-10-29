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

	"github.com/outflareai/outflare/engine/internal/emails"
	folder "github.com/outflareai/outflare/playbooks"
)

// A setup kit is what a playbook needs set up in the workspace before its
// first run, so nobody has to build it by hand: a pipeline in the CRM (a
// record type whose stage field uses an option list), fields on people
// and companies, and email drafts. Kits live in playbooks/kits/<id>.yaml;
// a playbook names one with kit: <id>, and several playbooks can share
// one. Applying a kit only adds: it reuses what already matches, adds
// missing options to its own lists, never changes or removes anything
// else, and stops at a conflict (package api).
type Kit struct {
	ID         string         `yaml:"id,omitempty" json:"id"`
	Version    int            `yaml:"version,omitempty" json:"version"`
	Title      string         `yaml:"title,omitempty" json:"title"`
	Summary    string         `yaml:"summary,omitempty" json:"summary"`
	OptionSets []KitOptionSet `yaml:"option_sets,omitempty" json:"option_sets"`
	Types      []KitType      `yaml:"record_types,omitempty" json:"record_types"`
	// Fields adds fields to record types the workspace already has, by
	// type key: person, account, or a type this kit adds.
	Fields    map[string][]KitField `yaml:"fields,omitempty" json:"fields"`
	Templates []KitTemplate         `yaml:"email_templates,omitempty" json:"email_templates"`
	// Inputs, smart lists, rules and journeys make it a flow kit
	// (flows.go): an automation added as drafts.
	Inputs     []KitInput     `yaml:"inputs,omitempty" json:"inputs"`
	SmartLists []KitSmartList `yaml:"smart_lists,omitempty" json:"smart_lists"`
	Rules      []KitRule      `yaml:"rules,omitempty" json:"rules"`
	Journeys   []KitJourney   `yaml:"journeys,omitempty" json:"journeys"`
}

// KitOptionSet is an option list, such as a pipeline's stages.
type KitOptionSet struct {
	Key     string      `yaml:"key,omitempty" json:"key"`
	Name    string      `yaml:"name,omitempty" json:"name"`
	Options []KitOption `yaml:"options,omitempty" json:"options"`
}

// KitOption is one choice in a list.
type KitOption struct {
	Key   string `yaml:"key,omitempty" json:"key"`
	Label string `yaml:"label,omitempty" json:"label"`
}

// KitType is a record type, such as a pipeline's deals. Stage names the
// status field that holds its stages, so the CRM can show it as a board.
type KitType struct {
	Key         string     `yaml:"key,omitempty" json:"key"`
	Name        string     `yaml:"name,omitempty" json:"name"`
	Plural      string     `yaml:"plural,omitempty" json:"plural"`
	Description string     `yaml:"description,omitempty" json:"description"`
	Stage       string     `yaml:"stage,omitempty" json:"stage,omitempty"`
	Fields      []KitField `yaml:"fields,omitempty" json:"fields"`
}

// KitField is a field on a record type, in the catalog's own terms.
type KitField struct {
	Key         string `yaml:"key,omitempty" json:"key"`
	Label       string `yaml:"label,omitempty" json:"label"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	ValueType   string `yaml:"value_type,omitempty" json:"value_type"`
	OptionSet   string `yaml:"option_set,omitempty" json:"option_set,omitempty"`
	Multiple    bool   `yaml:"multiple,omitempty" json:"multiple"`
	TargetType  string `yaml:"target_type,omitempty" json:"target_type,omitempty"`
	Filterable  bool   `yaml:"filterable,omitempty" json:"filterable"`
}

// KitTemplate is an email draft. It is added unpublished, so a person
// reads and publishes it before anything sends it.
type KitTemplate struct {
	Handle  string `yaml:"handle,omitempty" json:"handle"`
	Name    string `yaml:"name,omitempty" json:"name"`
	Purpose string `yaml:"purpose,omitempty" json:"purpose"`
	Subject string `yaml:"subject,omitempty" json:"subject"`
	Text    string `yaml:"text,omitempty" json:"text"`
}

// KitValueTypes are the catalog's value types (package api checks they
// match its own list) and KitMultiTypes the ones that can hold several.
var (
	KitValueTypes = []string{"text", "number", "currency", "date", "timestamp", "checkbox", "select", "status", "rating", "email", "phone", "url", "location", "record_reference", "actor_reference"}
	KitMultiTypes = []string{"text", "select", "email", "phone", "url", "record_reference", "actor_reference"}
	// KitBuiltinTypes are the record types every workspace has.
	KitBuiltinTypes = []string{"person", "account"}
	// KitReservedFields are keys a field may not take (package api's
	// reservedFieldKeys).
	KitReservedFields = []string{"id", "name", "type", "record_type", "brand_id", "origin", "version", "identities", "attributes", "created_at", "updated_at", "archived", "archived_at"}
)

var (
	kitKey      = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)
	kitFieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	kitOption   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]{0,62}$`)
	kitHandle   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// ParseKit reads one kit file. Unknown keys are errors.
func ParseKit(b []byte) (Kit, error) {
	var k Kit
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&k); err != nil {
		return Kit{}, err
	}
	k.Summary = strings.TrimSpace(k.Summary)
	if k.OptionSets == nil {
		k.OptionSets = []KitOptionSet{}
	}
	if k.Types == nil {
		k.Types = []KitType{}
	}
	for i := range k.Types {
		k.Types[i].Description = unwrap(k.Types[i].Description)
		if k.Types[i].Fields == nil {
			k.Types[i].Fields = []KitField{}
		}
	}
	if k.Fields == nil {
		k.Fields = map[string][]KitField{}
	}
	if k.Templates == nil {
		k.Templates = []KitTemplate{}
	}
	for i := range k.Templates {
		k.Templates[i].Text = strings.TrimSpace(k.Templates[i].Text)
	}
	if k.Inputs == nil {
		k.Inputs = []KitInput{}
	}
	if k.SmartLists == nil {
		k.SmartLists = []KitSmartList{}
	}
	if k.Rules == nil {
		k.Rules = []KitRule{}
	}
	if k.Journeys == nil {
		k.Journeys = []KitJourney{}
	}
	for i := range k.SmartLists {
		k.SmartLists[i].Description = unwrap(k.SmartLists[i].Description)
	}
	for i := range k.Rules {
		k.Rules[i].Description = unwrap(k.Rules[i].Description)
	}
	for i := range k.Journeys {
		k.Journeys[i].Description = unwrap(k.Journeys[i].Description)
	}
	return k, nil
}

// Validate checks a kit against the rules in playbooks/README.md: the same
// rules the catalog and email template endpoints apply, so applying it
// never fails on its own content.
func (k Kit) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !idPattern.MatchString(k.ID) || len(k.ID) > 60 {
		bad("id must be lowercase words joined by hyphens, at most 60 characters")
	}
	if k.Version < 1 {
		bad("version must be 1 or more; raise it whenever the kit changes")
	}
	if n := len(k.Title); n == 0 || n > 80 {
		bad("title is required, at most 80 characters")
	}
	if n := len(k.Summary); n == 0 || n > 200 {
		bad("summary is required, at most 200 characters")
	}
	if len(k.OptionSets)+len(k.Types)+len(k.Fields)+len(k.Templates) == 0 && !k.HasFlows() {
		bad("a kit adds at least one option list, record type, field, email template, smart list, rule or journey")
	}
	sets := map[string]bool{}
	for _, s := range k.OptionSets {
		if !kitKey.MatchString(s.Key) {
			bad("option list key %q must be 2 to 40 lowercase letters, numbers and underscores", s.Key)
		}
		if sets[s.Key] {
			bad("option list %q is listed twice", s.Key)
		}
		sets[s.Key] = true
		if n := len(s.Name); n == 0 || n > 60 {
			bad("option list %q needs a name of at most 60 characters", s.Key)
		}
		if len(s.Options) == 0 || len(s.Options) > 500 {
			bad("option list %q needs 1 to 500 options", s.Key)
		}
		seen := map[string]bool{}
		for _, o := range s.Options {
			if !kitOption.MatchString(o.Key) || seen[o.Key] {
				bad("option list %q: option %q needs a unique key of letters, numbers and underscores", s.Key, o.Key)
			}
			seen[o.Key] = true
			if n := len(o.Label); n == 0 || n > 80 {
				bad("option list %q: option %q needs a label of at most 80 characters", s.Key, o.Key)
			}
		}
	}
	types := map[string]bool{}
	for _, t := range KitBuiltinTypes {
		types[t] = true
	}
	for _, t := range k.Types {
		if !kitKey.MatchString(t.Key) || slices.Contains(KitBuiltinTypes, t.Key) {
			bad("record type key %q must be 2 to 40 lowercase letters, numbers and underscores, and not a built-in type", t.Key)
		}
		if types[t.Key] && !slices.Contains(KitBuiltinTypes, t.Key) {
			bad("record type %q is listed twice", t.Key)
		}
		types[t.Key] = true
		if n, p := len(t.Name), len(t.Plural); n == 0 || n > 60 || p == 0 || p > 60 {
			bad("record type %q needs a name and plural of at most 60 characters", t.Key)
		}
		if len(t.Description) > 1000 {
			bad("record type %q: description is at most 1,000 characters", t.Key)
		}
	}
	checkFields := func(on string, fields []KitField) {
		seen := map[string]bool{}
		for _, f := range fields {
			where := on + "." + f.Key
			switch {
			case !kitFieldKey.MatchString(f.Key) || slices.Contains(KitReservedFields, f.Key):
				bad("field %s: the key must be 2 to 63 lowercase letters, numbers and underscores, and not one the record itself uses", where)
			case seen[f.Key]:
				bad("field %s is listed twice", where)
			case f.Label == "" || len(f.Label) > 80:
				bad("field %s needs a label of at most 80 characters", where)
			case len(f.Description) > 1000:
				bad("field %s: description is at most 1,000 characters", where)
			case !slices.Contains(KitValueTypes, f.ValueType):
				bad("field %s: value_type is one of %s", where, strings.Join(KitValueTypes, ", "))
			case f.Multiple && !slices.Contains(KitMultiTypes, f.ValueType):
				bad("field %s: multiple is for %s fields", where, strings.Join(KitMultiTypes, ", "))
			case (f.ValueType == "select" || f.ValueType == "status") != (f.OptionSet != ""):
				bad("field %s: select and status fields, and only they, name an option_set", where)
			case (f.ValueType == "record_reference") != (f.TargetType != ""):
				bad("field %s: record_reference fields, and only they, name a target_type", where)
			case f.TargetType != "" && !types[f.TargetType]:
				bad("field %s: target_type %q is not person, account or a type this kit adds", where, f.TargetType)
			}
			seen[f.Key] = true
		}
	}
	for _, t := range k.Types {
		checkFields(t.Key, t.Fields)
		if t.Stage != "" {
			i := slices.IndexFunc(t.Fields, func(f KitField) bool { return f.Key == t.Stage })
			if i < 0 || t.Fields[i].ValueType != "status" {
				bad("record type %q: stage must name one of its status fields", t.Key)
			}
		}
	}
	for _, on := range sortedKeys(k.Fields) {
		if !types[on] {
			bad("fields: %q is not person, account or a type this kit adds", on)
		}
		if slices.ContainsFunc(k.Types, func(t KitType) bool { return t.Key == on }) {
			bad("fields: put %q's fields under its record type", on)
		}
		checkFields(on, k.Fields[on])
	}
	handles := map[string]bool{}
	for _, t := range k.Templates {
		if !kitHandle.MatchString(t.Handle) || handles[t.Handle] {
			bad("email template %q needs a unique handle of lowercase letters, digits, dashes or underscores", t.Handle)
		}
		handles[t.Handle] = true
		if n := len(t.Name); n == 0 || n > 200 {
			bad("email template %q needs a name of at most 200 characters", t.Handle)
		}
		if t.Purpose != "lifecycle" && t.Purpose != "transactional" {
			bad("email template %q: purpose is lifecycle or transactional", t.Handle)
		}
		if t.Subject == "" || t.Text == "" || len(t.Subject) > 998 || len(t.Text) > 64<<10 {
			bad("email template %q needs a subject and a text body", t.Handle)
		} else if err := emails.Check(t.Subject, "", t.Text); err != nil {
			bad("email template %q has a mistake in %v", t.Handle, err)
		}
	}
	k.validateFlows(bad)
	return errors.Join(errs...)
}

// OptionSetsUsed lists the option lists the kit's fields use that it does
// not define itself, such as the built-in country.
func (k Kit) OptionSetsUsed() []string {
	own := map[string]bool{}
	for _, s := range k.OptionSets {
		own[s.Key] = true
	}
	var out []string
	add := func(fs []KitField) {
		for _, f := range fs {
			if f.OptionSet != "" && !own[f.OptionSet] && !slices.Contains(out, f.OptionSet) {
				out = append(out, f.OptionSet)
			}
		}
	}
	for _, t := range k.Types {
		add(t.Fields)
	}
	for _, on := range sortedKeys(k.Fields) {
		add(k.Fields[on])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LoadKits reads every kit in fsys (kits/<id>.yaml), checks it and that
// its file is named after its id, and returns them by title.
func LoadKits(fsys fs.FS) ([]Kit, error) {
	files, err := fs.Glob(fsys, "kits/*.yaml")
	if err != nil {
		return nil, err
	}
	var out []Kit
	var errs []error
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		k, err := ParseKit(b)
		if err == nil {
			err = k.Validate()
		}
		if name := strings.TrimSuffix(path.Base(f), ".yaml"); err == nil && name != k.ID {
			err = fmt.Errorf("the file is %q but the id is %q; they must match", name, k.ID)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("playbooks/%s: %w", f, err))
			continue
		}
		out = append(out, k)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

var (
	kitsOnce sync.Once
	allKits  []Kit
	kitsErr  error
)

// Kits returns the kits shipped in the repository's playbooks/kits folder.
func Kits() ([]Kit, error) {
	kitsOnce.Do(func() { allKits, kitsErr = LoadKits(folder.FS) })
	return allKits, kitsErr
}

// GetKit returns one shipped kit.
func GetKit(id string) (Kit, bool) {
	list, _ := Kits()
	for _, k := range list {
		if k.ID == id {
			return k, true
		}
	}
	return Kit{}, false
}
