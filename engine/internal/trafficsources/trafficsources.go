// Package trafficsources is the catalogue of traffic-source templates
// (plans/ad-tracker.md, T3): for each network a brand buys clicks from,
// the URL parameters that carry its click id, its cost and the values worth
// reporting on, with the network's own token for each, and where it takes
// a conversion postback. The templates are data in
// integrations/traffic-sources/<id>.yaml, so adding a network needs no Go.
//
// A brand's traffic source starts as a copy of a template (or a custom one)
// and can be changed; the same Parameters rules apply to both.
package trafficsources

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	"github.com/outflareai/outflare/engine/internal/tokens"
	"github.com/outflareai/outflare/integrations"
)

// Fields is how many custom fields a click has.
const Fields = 20

// Categories group the catalogue, in the order the gallery shows them.
var Categories = []string{"search_social", "native", "push_pop", "display", "mobile", "adult"}

// Template is one network's template.
type Template struct {
	ID       string `yaml:"id" json:"id"`
	Name     string `yaml:"name" json:"name"`
	Category string `yaml:"category" json:"category"`
	// Connector is the Outflare connector that syncs the network's spend,
	// if there is one.
	Connector string `yaml:"connector,omitempty" json:"connector,omitempty"`
	Currency  string `yaml:"currency,omitempty" json:"currency"`
	// ClickID is the parameter for the network's click id. A network that
	// adds its click id itself (gclid, fbclid) has a Param and no Token.
	ClickID    Param       `yaml:"click_id" json:"click_id"`
	Cost       Param       `yaml:"cost,omitempty" json:"cost"`
	Parameters []Parameter `yaml:"parameters" json:"parameters"`
	Postback   *Postback   `yaml:"postback,omitempty" json:"postback"`
	// Note is anything a person setting it up needs to know.
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
	// Sources are where the tokens come from (the network's docs).
	Sources []string `yaml:"sources" json:"-"`
}

// Param is a URL parameter and the network's token for its value.
type Param struct {
	Param string `yaml:"param" json:"param"`
	Token string `yaml:"token,omitempty" json:"token"`
}

// Parameter is a value a click carries into one custom field.
type Parameter struct {
	// Name is what reports call the field.
	Name  string `yaml:"name" json:"name"`
	Param string `yaml:"param" json:"param"`
	// Token is what the network fills in (its macro), shown in the campaign
	// URL. Empty for a parameter the network adds itself.
	Token string `yaml:"token,omitempty" json:"token"`
	// Field is the custom field, 1 to 20.
	Field int `yaml:"field" json:"field"`
	// Aliases are other parameters that fill the same field when Param is
	// missing, such as utm_source.
	Aliases []string `yaml:"aliases,omitempty" json:"aliases"`
}

// Postback is where the network takes a conversion: a URL with Outflare's
// tokens (PostbackTokens) and REPLACE where the brand's own value goes.
type Postback struct {
	URL    string `yaml:"url" json:"url"`
	Method string `yaml:"method,omitempty" json:"method"`
}

// FieldToken is the token for custom field n: field1 to field20.
func FieldToken(n int) string { return "field" + strconv.Itoa(n) }

func fieldTokens() []string {
	out := make([]string, Fields)
	for i := range out {
		out[i] = FieldToken(i + 1)
	}
	return out
}

// DestinationTokens are the tokens a tracker link's destination can carry,
// filled in for each click.
var DestinationTokens = append([]string{"click_id", "source_click_id", "cost", "campaign", "campaign_code", "source", "content",
	"country", "region", "city", "isp", "asn", "device", "os", "browser", "language"}, fieldTokens()...)

// PostbackTokens are the tokens a postback to a traffic source can carry,
// filled in for each conversion it sends back.
// random is a new random number for each call, for networks that ask for
// a cache-buster.
var PostbackTokens = append([]string{"click_id", "source_click_id", "payout", "currency", "transaction_id", "conversion_type",
	"status", "campaign", "campaign_code", "country", "random"}, fieldTokens()...)

var (
	idRe    = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	paramRe = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,64}$`)
	currRe  = regexp.MustCompile(`^[A-Z]{3}$`)
)

// CheckParam checks a URL parameter's name.
func CheckParam(what, p string) error {
	if !paramRe.MatchString(p) {
		return fmt.Errorf("%s %q is a URL parameter: 1 to 64 letters, digits, _, -, . or []", what, p)
	}
	return nil
}

// CheckToken checks a network's token: what the network fills in, so any
// text it uses, without spaces, and never REPLACE.
func CheckToken(what, t string) error {
	switch {
	case len(t) > 100:
		return fmt.Errorf("%s is at most 100 characters", what)
	case strings.ContainsAny(t, " \t\r\n&?"):
		return fmt.Errorf("%s %q can't have spaces, & or ?", what, t)
	case strings.Contains(t, tokens.Placeholder):
		return fmt.Errorf("%s: replace %s with the network's token", what, tokens.Placeholder)
	}
	return nil
}

// CheckParameters checks a source's parameters: at most 20, each with a
// name, a parameter and a field of its own, and no parameter used twice
// (with the click id and cost parameters, passed as taken).
func CheckParameters(ps []Parameter, taken ...string) error {
	if len(ps) > Fields {
		return fmt.Errorf("a traffic source has at most %d parameters, one per custom field", Fields)
	}
	used := map[string]bool{}
	for _, t := range taken {
		if t != "" {
			used[strings.ToLower(t)] = true
		}
	}
	fields := map[int]bool{}
	names := map[string]bool{}
	for i, p := range ps {
		what := fmt.Sprintf("parameters[%d]", i)
		name := strings.TrimSpace(p.Name)
		switch {
		case name == "" || len(name) > 60:
			return fmt.Errorf("%s.name is 1 to 60 characters", what)
		case names[strings.ToLower(name)]:
			return fmt.Errorf("%s.name %q is used twice", what, name)
		case p.Field < 1 || p.Field > Fields:
			return fmt.Errorf("%s.field is 1 to %d", what, Fields)
		case fields[p.Field]:
			return fmt.Errorf("%s.field %d is used twice; give each parameter its own field", what, p.Field)
		case len(p.Aliases) > 5:
			return fmt.Errorf("%s.aliases has at most 5", what)
		}
		names[strings.ToLower(name)], fields[p.Field] = true, true
		if err := CheckToken(what+".token", p.Token); err != nil {
			return err
		}
		for _, q := range append([]string{p.Param}, p.Aliases...) {
			if err := CheckParam(what+".param", q); err != nil {
				return err
			}
			if used[strings.ToLower(q)] {
				return fmt.Errorf("%s: the parameter %q is used twice", what, q)
			}
			used[strings.ToLower(q)] = true
		}
	}
	return nil
}

// Validate checks a template.
func (t Template) Validate() error {
	switch {
	case !idRe.MatchString(t.ID):
		return errors.New("id is 1 to 40 lowercase letters, digits or _")
	case t.Name == "":
		return errors.New("name is required")
	case !contains(Categories, t.Category):
		return fmt.Errorf("category is one of %s", strings.Join(Categories, ", "))
	case t.Currency != "" && !currRe.MatchString(t.Currency):
		return errors.New("currency is a three-letter code such as USD")
	case t.ClickID.Param == "":
		return errors.New("click_id.param is required: the parameter that carries the network's click id")
	case len(t.Sources) == 0:
		return errors.New("sources lists where the tokens come from")
	}
	for what, p := range map[string]Param{"click_id": t.ClickID, "cost": t.Cost} {
		if p.Param == "" && p.Token == "" {
			continue
		}
		if err := CheckParam(what+".param", p.Param); err != nil {
			return err
		}
		if err := CheckToken(what+".token", p.Token); err != nil {
			return err
		}
	}
	if err := CheckParameters(t.Parameters, t.ClickID.Param, t.Cost.Param); err != nil {
		return err
	}
	if t.Postback != nil {
		if t.Postback.Method != "" && t.Postback.Method != "GET" && t.Postback.Method != "POST" {
			return errors.New("postback.method is GET or POST")
		}
		// A template may leave REPLACE for the brand's own value.
		u := strings.ReplaceAll(t.Postback.URL, tokens.Placeholder, "x")
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return errors.New("postback.url is a full address")
		}
		if err := tokens.Check(u, PostbackTokens); err != nil {
			return fmt.Errorf("postback.url: %w", err)
		}
	}
	return nil
}

// HasPlaceholder reports whether a template leaves a value for the brand
// to fill in.
func (t Template) HasPlaceholder() bool {
	return t.Postback != nil && strings.Contains(t.Postback.URL, tokens.Placeholder)
}

// Load reads every template in fsys (traffic-sources/<id>.yaml), checks it
// and that its file is named after its id, and returns them by name.
func Load(fsys fs.FS) ([]Template, error) {
	files, err := fs.Glob(fsys, "traffic-sources/*.yaml")
	if err != nil {
		return nil, err
	}
	var out []Template
	var errs []error
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		var t Template
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		err = dec.Decode(&t)
		if err == nil {
			err = t.Validate()
		}
		if name := strings.TrimSuffix(path.Base(f), ".yaml"); err == nil && name != t.ID {
			err = fmt.Errorf("the file is %q but the id is %q; they must match", name, t.ID)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("integrations/%s: %w", f, err))
			continue
		}
		if t.Currency == "" {
			t.Currency = "USD"
		}
		if t.Postback != nil && t.Postback.Method == "" {
			t.Postback.Method = "GET"
		}
		if t.Parameters == nil {
			t.Parameters = []Parameter{}
		}
		for i := range t.Parameters {
			if t.Parameters[i].Aliases == nil {
				t.Parameters[i].Aliases = []string{}
			}
		}
		out = append(out, t)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

var (
	once    sync.Once
	all     []Template
	loadErr error
)

// All is the catalogue the engine ships with.
func All() ([]Template, error) {
	once.Do(func() { all, loadErr = Load(integrations.FS) })
	return all, loadErr
}

// Get is one template of the catalogue.
func Get(id string) (Template, bool) {
	list, _ := All()
	for _, t := range list {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
