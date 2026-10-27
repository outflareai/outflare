// Package manifest runs connectors described by a manifest file instead of
// Go code: the integrations/ folder at the root of this repository, where
// anyone can add one by pull request.
//
// A manifest is data, never code. It names one https base URL, the fields a
// person pastes, the request that tests a key, and for each capability the
// request to send and how the answer maps onto Outflare's own types.
// Templates only fill in values ({{api_key}}, {{email}}); there are no
// expressions. Every request goes to the base URL, so credentials never
// reach another host, and through the connector client, so the dial guard
// and timeouts apply as for every other connector.
package manifest

import (
	"bytes"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v4"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Manifest is one connector's manifest.yaml.
type Manifest struct {
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name"`
	Category    string       `yaml:"category"`
	Summary     string       `yaml:"summary"`
	Description string       `yaml:"description"`
	Website     string       `yaml:"website"`
	DocsURL     string       `yaml:"docs_url"`
	KeyURL      string       `yaml:"key_url"`
	Pricing     string       `yaml:"pricing"`
	Publisher   Publisher    `yaml:"publisher"`
	BaseURL     string       `yaml:"base_url"`
	Auth        Auth         `yaml:"auth"`
	Test        Test         `yaml:"test"`
	Caps        Capabilities `yaml:"capabilities"`
}

// Publisher is who looks after the connector. A publisher listed in
// publishers.yaml needs only its id; the name and trust come from there.
type Publisher struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Website string `yaml:"website"`
	// Contact is an address for maintainers, not shown in the catalog.
	Contact string `yaml:"contact"`
}

// Auth is how a person connects. Only api_key for now.
type Auth struct {
	APIKey *APIKey `yaml:"api_key"`
}

// APIKey lists what a person pastes.
type APIKey struct {
	Fields []Field `yaml:"fields"`
}

// Field is one thing a person pastes; its name is a template variable.
type Field struct {
	Name        string `yaml:"name"`
	Label       string `yaml:"label"`
	Secret      bool   `yaml:"secret"`
	Optional    bool   `yaml:"optional"`
	Placeholder string `yaml:"placeholder"`
	Help        string `yaml:"help"`
}

// Request is one call to the service. Path, query, header and body
// values can hold {{variables}}.
type Request struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Query   map[string]string `yaml:"query"`
	Headers map[string]string `yaml:"headers"`
	// Body is sent as JSON when set.
	Body map[string]any `yaml:"body"`
}

// ErrorRule turns a successful HTTP answer that reports a failure into an
// error, such as {"status": "error", "reason": "no credits left"}.
type ErrorRule struct {
	When Cond `yaml:"when"`
	// Kind: auth, quota, unavailable or invalid (the default).
	Kind string `yaml:"kind"`
	// Message is the path of the answer's own words.
	Message string `yaml:"message"`
	// Kinds picks the kind from the message instead: the first rule whose
	// word the message contains wins.
	Kinds []KindRule `yaml:"kinds"`
}

// KindRule maps a word in an error message to a kind.
type KindRule struct {
	Contains string `yaml:"contains"`
	Kind     string `yaml:"kind"`
}

// Cond tests one value in the answer.
type Cond struct {
	Path    string  `yaml:"path"`
	Equals  *string `yaml:"equals"`
	Not     *string `yaml:"not"`
	Present bool    `yaml:"present"`
}

// Test signs in without changing anything or spending credits.
type Test struct {
	Request Request     `yaml:"request"`
	Errors  []ErrorRule `yaml:"errors"`
	Account AccountMap  `yaml:"account"`
}

// AccountMap says where the account's details are in the test answer.
type AccountMap struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
	// Credits is one path, or several whose numbers are added up.
	Credits Paths             `yaml:"credits"`
	Details map[string]string `yaml:"details"`
}

// Capabilities are what the connector can do. Each one implements the
// engine's interface for it, so engine features use it like any other.
type Capabilities struct {
	Verify *Verify `yaml:"verify"`
}

// Verify checks one address: the connectors.Verifier capability.
// Variables: {{email}} and the auth fields.
type Verify struct {
	Request Request      `yaml:"request"`
	Errors  []ErrorRule  `yaml:"errors"`
	Result  VerifyResult `yaml:"result"`
}

// VerifyResult maps the answer onto connectors.Verification. Flags are
// paths to true/false values; status maps the service's words.
type VerifyResult struct {
	Status         StatusMap `yaml:"status"`
	Reason         string    `yaml:"reason"`
	ProviderStatus string    `yaml:"provider_status"`
	CatchAll       string    `yaml:"catch_all"`
	Disposable     string    `yaml:"disposable"`
	Role           string    `yaml:"role"`
	Free           string    `yaml:"free"`
	Suggestion     string    `yaml:"suggestion"`
	// Score is a 0–100 number; anything outside is dropped.
	Score string `yaml:"score"`
}

// StatusMap maps the service's status words onto the four shared ones.
type StatusMap struct {
	From    string                 `yaml:"from"`
	Values  map[string]StatusValue `yaml:"values"`
	Default string                 `yaml:"default"`
}

// StatusValue is a shared status, optionally setting flags too:
// "risky", or {status: risky, catch_all: true}.
type StatusValue struct {
	Status     string `yaml:"status"`
	CatchAll   bool   `yaml:"catch_all"`
	Disposable bool   `yaml:"disposable"`
	Role       bool   `yaml:"role"`
}

func (v *StatusValue) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		v.Status = n.Value
		return nil
	}
	type plain StatusValue
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*v = StatusValue(p)
	return nil
}

// Paths is one path or a list of them.
type Paths []string

func (p *Paths) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*p = Paths{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*p = list
	return nil
}

// PublisherEntry is a line of publishers.yaml.
type PublisherEntry struct {
	Name    string `yaml:"name"`
	Website string `yaml:"website"`
	Trust   string `yaml:"trust"`
}

// Parse reads a manifest, refusing fields it does not know so a typo is an
// error rather than a silently ignored setting.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := decodeStrict(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func decodeStrict(b []byte, v any) error {
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	return d.Decode(v)
}

// ParsePublishers reads publishers.yaml.
func ParsePublishers(b []byte) (map[string]PublisherEntry, error) {
	out := map[string]PublisherEntry{}
	if err := decodeStrict(b, &out); err != nil {
		return nil, fmt.Errorf("publishers.yaml: %w", err)
	}
	for id, p := range out {
		if p.Trust != c.TrustOutflare && p.Trust != c.TrustVerified {
			return nil, fmt.Errorf("publishers.yaml: %s: trust must be outflare or verified", id)
		}
		if p.Name == "" || !strings.HasPrefix(p.Website, "https://") {
			return nil, fmt.Errorf("publishers.yaml: %s needs a name and an https website", id)
		}
	}
	return out, nil
}

// Load reads publishers.yaml and every connectors/<id>/manifest.yaml in
// fsys and returns the connectors, sorted by id.
func Load(fsys fs.FS) ([]*Connector, error) {
	pb, err := fs.ReadFile(fsys, "publishers.yaml")
	if err != nil {
		return nil, err
	}
	pubs, err := ParsePublishers(pb)
	if err != nil {
		return nil, err
	}
	files, err := fs.Glob(fsys, "connectors/*/manifest.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []*Connector
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		m, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if dir := path.Base(path.Dir(f)); m.ID != dir {
			return nil, fmt.Errorf("%s: id %q must match its folder %q", f, m.ID, dir)
		}
		conn, err := New(m, pubs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, conn)
	}
	return out, nil
}

// All loads fsys and returns its connectors, panicking on an invalid
// manifest: the files are embedded and checked by the tests, so this only
// fails on a build that skipped them.
func All(fsys fs.FS) []c.Connector {
	conns, err := Load(fsys)
	if err != nil {
		panic("integrations: " + err.Error())
	}
	out := make([]c.Connector, len(conns))
	for i, x := range conns {
		out[i] = x
	}
	return out
}

var (
	idRe       = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)
	varRe      = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)
	headerRe   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	categories = map[string]bool{c.CategoryVerification: true}
)

// Validate checks a manifest on its own: everything that does not need
// publishers.yaml.
func (m *Manifest) Validate() error {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if !idRe.MatchString(m.ID) {
		bad("id must be 2–40 lowercase letters, digits or _, starting with a letter")
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 60 {
		bad("name is required (at most 60 characters)")
	}
	if !categories[m.Category] {
		bad("category %q is not supported by manifests yet", m.Category)
	}
	if m.Summary == "" || len(m.Summary) > 160 {
		bad("summary is required (one sentence, at most 160 characters)")
	}
	if len(m.Description) > 600 {
		bad("description is at most 600 characters")
	}
	for name, u := range map[string]string{"website": m.Website, "docs_url": m.DocsURL, "key_url": m.KeyURL} {
		if u != "" && !strings.HasPrefix(u, "https://") {
			bad("%s must be an https URL", name)
		}
	}
	if m.Website == "" {
		bad("website is required")
	}
	if !idRe.MatchString(m.Publisher.ID) {
		bad("publisher.id is required: a short lowercase id for who looks after this connector")
	}
	if err := checkBaseURL(m.BaseURL); err != nil {
		bad("base_url: %v", err)
	}
	vars := map[string]bool{}
	if m.Auth.APIKey == nil || len(m.Auth.APIKey.Fields) == 0 {
		bad("auth.api_key.fields: at least one field")
	} else {
		for _, f := range m.Auth.APIKey.Fields {
			if !idRe.MatchString(f.Name) || f.Label == "" {
				bad("auth field %q needs a lowercase name and a label", f.Name)
			}
			if vars[f.Name] {
				bad("auth field %q is listed twice", f.Name)
			}
			vars[f.Name] = true
		}
	}
	checkRequest := func(where string, r Request, extra ...string) {
		allowed := map[string]bool{}
		for k := range vars {
			allowed[k] = true
		}
		for _, k := range extra {
			allowed[k] = true
		}
		if err := r.check(allowed); err != nil {
			bad("%s.request: %v", where, err)
		}
	}
	checkErrors := func(where string, rules []ErrorRule) {
		for i, e := range rules {
			if err := e.check(); err != nil {
				bad("%s.errors[%d]: %v", where, i, err)
			}
		}
	}
	checkRequest("test", m.Test.Request)
	checkErrors("test", m.Test.Errors)
	if m.Test.Request.Method != "GET" {
		bad("test.request: the test must be a GET, so it cannot change anything")
	}
	if m.Caps.Verify == nil {
		bad("capabilities: at least one (verify)")
	}
	if v := m.Caps.Verify; v != nil {
		if m.Category != c.CategoryVerification {
			bad("capabilities.verify needs category email_verification")
		}
		checkRequest("capabilities.verify", v.Request, "email")
		checkErrors("capabilities.verify", v.Errors)
		if err := v.Result.Status.check(); err != nil {
			bad("capabilities.verify.result.status: %v", err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("must be an https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return fmt.Errorf("must be a plain https address, without a port, user, query or fragment")
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || host == "localhost" {
		return fmt.Errorf("must be a public host name")
	}
	return nil
}

var methods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

func (r Request) check(vars map[string]bool) error {
	if !methods[r.Method] {
		return fmt.Errorf("method must be GET, POST, PUT, PATCH or DELETE")
	}
	if !strings.HasPrefix(r.Path, "/") || strings.Contains(r.Path, "//") || strings.Contains(r.Path, "..") || strings.ContainsAny(r.Path, "?#") {
		return fmt.Errorf("path must start with / and stay on the base URL (no //, .., ? or #)")
	}
	if r.Body != nil && r.Method == "GET" {
		return fmt.Errorf("a GET has no body")
	}
	var strs []string
	strs = append(strs, r.Path)
	for _, v := range r.Query {
		strs = append(strs, v)
	}
	for k, v := range r.Headers {
		if !headerRe.MatchString(k) {
			return fmt.Errorf("header name %q", k)
		}
		switch strings.ToLower(k) {
		case "host", "content-length", "transfer-encoding", "connection", "cookie":
			return fmt.Errorf("header %s cannot be set", k)
		}
		strs = append(strs, v)
	}
	walkStrings(r.Body, func(s string) { strs = append(strs, s) })
	for _, s := range strs {
		for _, m := range varRe.FindAllStringSubmatch(s, -1) {
			if !vars[m[1]] {
				return fmt.Errorf("unknown variable {{%s}}", m[1])
			}
		}
		if rest := varRe.ReplaceAllString(s, ""); strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
			return fmt.Errorf("broken template %q", s)
		}
	}
	return nil
}

func walkStrings(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case map[string]any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	case []any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	}
}

var kinds = map[string]bool{c.ErrAuth: true, c.ErrQuota: true, c.ErrUnavailable: true, c.ErrInvalid: true}

func (e ErrorRule) check() error {
	n := 0
	if e.When.Equals != nil {
		n++
	}
	if e.When.Not != nil {
		n++
	}
	if e.When.Present {
		n++
	}
	if e.When.Path == "" || n != 1 {
		return fmt.Errorf("when needs a path and exactly one of equals, not or present")
	}
	if e.Kind != "" && !kinds[e.Kind] {
		return fmt.Errorf("kind must be auth, quota, unavailable or invalid")
	}
	for _, k := range e.Kinds {
		if k.Contains == "" || !kinds[k.Kind] {
			return fmt.Errorf("kinds: each needs contains and a kind")
		}
	}
	return nil
}

var statuses = map[string]bool{c.Deliverable: true, c.Undeliverable: true, c.Risky: true, c.Unknown: true}

func (s StatusMap) check() error {
	if s.From == "" || len(s.Values) == 0 {
		return fmt.Errorf("needs from and values")
	}
	for k, v := range s.Values {
		if !statuses[v.Status] {
			return fmt.Errorf("%s: %q is not deliverable, undeliverable, risky or unknown", k, v.Status)
		}
	}
	if s.Default != "" && !statuses[s.Default] {
		return fmt.Errorf("default %q is not a status", s.Default)
	}
	return nil
}
