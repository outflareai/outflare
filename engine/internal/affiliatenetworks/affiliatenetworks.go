// Package affiliatenetworks is the catalogue of affiliate-network templates
// (plans/ad-tracker.md, T6a): for each network or network platform a brand
// runs offers from, the parameter in its offer links that carries the
// tracker's click id, and the network's own macro for each value Outflare's
// postback reads (the click id, payout, transaction id and the rest). The
// templates are data in integrations/affiliate-networks/<id>.yaml, so adding a
// network needs no Go.
//
// A brand's postback source names its template, and its postback URL is then
// written with that network's macros, ready to paste into the network.
package affiliatenetworks

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	"github.com/outflareai/outflare/integrations"
)

// Kinds group the catalogue: software many networks run on, or one network.
var Kinds = []string{"platform", "network"}

// Values are the postback parameters a template can give a macro for, in
// the order a postback URL lists them: the click id, payout, transaction
// id, currency, status and conversion type (Outflare's postback reads each
// under these names).
var Values = []string{"cid", "payout", "txid", "currency", "status", "type"}

// Template is one network's template.
type Template struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
	Kind string `yaml:"kind" json:"kind"`
	// ClickIDParam is the parameter in the network's offer links where the
	// tracker's click id goes (a sub id), such as sub1 or aff_sub.
	ClickIDParam string `yaml:"click_id_param" json:"click_id_param"`
	// Macros are the network's macro for each of Values it has, such as
	// {"cid": "{sub1}", "payout": "{payout}"}.
	Macros map[string]string `yaml:"macros" json:"macros"`
	// Note is anything a person setting it up needs to know.
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
	// Sources are where the macros come from (the network's docs).
	Sources []string `yaml:"sources" json:"-"`
}

var (
	idRe    = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	paramRe = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,64}$`)
)

// CheckParam checks a click id parameter's name.
func CheckParam(what, p string) error {
	if !paramRe.MatchString(p) {
		return fmt.Errorf("%s %q is a URL parameter: 1 to 64 letters, digits, _, -, . or []", what, p)
	}
	return nil
}

// CheckMacro checks a network's macro: any text the network uses, without
// spaces, & or ?.
func CheckMacro(what, m string) error {
	switch {
	case m == "" || len(m) > 100:
		return fmt.Errorf("%s is 1 to 100 characters", what)
	case strings.ContainsAny(m, " \t\r\n&?"):
		return fmt.Errorf("%s %q can't have spaces, & or ?", what, m)
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
	case !contains(Kinds, t.Kind):
		return fmt.Errorf("kind is one of %s", strings.Join(Kinds, ", "))
	case t.Macros["cid"] == "":
		return errors.New("macros.cid is required: the network's macro for the click id it passes back")
	case len(t.Sources) == 0:
		return errors.New("sources lists where the macros come from")
	}
	if err := CheckParam("click_id_param", t.ClickIDParam); err != nil {
		return err
	}
	for k, m := range t.Macros {
		if !contains(Values, k) {
			return fmt.Errorf("macros.%s isn't a postback value; use %s", k, strings.Join(Values, ", "))
		}
		if err := CheckMacro("macros."+k, m); err != nil {
			return err
		}
	}
	for _, s := range t.Sources {
		if u, err := url.Parse(s); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("sources: %q is an https address", s)
		}
	}
	return nil
}

// Query is the query string of a postback URL for the network: each value
// it has a macro for, as the network writes it, so the network fills them
// in. Without a template it carries Outflare's own token names.
func (t *Template) Query() string {
	macros := map[string]string{"cid": "{click_id}", "payout": "{payout}", "txid": "{transaction_id}", "currency": "{currency}", "status": "{status}"}
	if t != nil {
		macros = t.Macros
	}
	var parts []string
	for _, k := range Values {
		if m := macros[k]; m != "" {
			parts = append(parts, k+"="+m)
		}
	}
	return strings.Join(parts, "&")
}

// Load reads every template in fsys (affiliate-networks/<id>.yaml), checks
// it and that its file is named after its id, and returns them by name.
func Load(fsys fs.FS) ([]Template, error) {
	files, err := fs.Glob(fsys, "affiliate-networks/*.yaml")
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
