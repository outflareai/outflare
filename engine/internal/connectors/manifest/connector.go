package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// maxAnswer caps how much of an answer is read.
const maxAnswer = 1 << 20

// Connector runs a manifest. It implements connectors.Connector and the
// capability interfaces its manifest lists.
type Connector struct {
	m   *Manifest
	def c.Definition
}

// New checks m against the publishers and builds its connector. The
// manifest never decides its own trust: a publisher id listed in
// publishers.yaml takes that entry's name and trust, anything else is a
// Community connector and may not borrow a listed publisher's name.
func New(m *Manifest, pubs map[string]PublisherEntry) (*Connector, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	pub := &c.Publisher{ID: m.Publisher.ID, Name: m.Publisher.Name, Website: m.Publisher.Website}
	trust := c.TrustCommunity
	if e, ok := pubs[m.Publisher.ID]; ok {
		pub.Name, pub.Website, trust = e.Name, e.Website, e.Trust
	} else {
		if pub.Name == "" || !strings.HasPrefix(pub.Website, "https://") {
			return nil, fmt.Errorf("publisher %q is not in publishers.yaml, so it needs a name and an https website", m.Publisher.ID)
		}
		for _, e := range pubs {
			if strings.EqualFold(strings.TrimSpace(pub.Name), e.Name) {
				return nil, fmt.Errorf("publisher name %q belongs to a listed publisher", pub.Name)
			}
		}
	}
	d := c.Definition{
		ID: m.ID, Name: m.Name, Category: m.Category, Summary: m.Summary, Description: m.Description,
		Website: m.Website, DocsURL: m.DocsURL, KeyURL: m.KeyURL, Pricing: m.Pricing,
		Auth: c.AuthAPIKey, Publisher: pub, Trust: trust, Capabilities: []string{},
	}
	for _, f := range m.Auth.APIKey.Fields {
		d.Fields = append(d.Fields, c.Field{Name: f.Name, Label: f.Label, Secret: f.Secret, Optional: f.Optional, Placeholder: f.Placeholder, Help: f.Help})
	}
	if m.Caps.Verify != nil {
		d.Capabilities = append(d.Capabilities, c.CapVerify)
	}
	return &Connector{m: m, def: d}, nil
}

// Manifest returns the manifest the connector runs.
func (x *Connector) Manifest() *Manifest { return x.m }

func (x *Connector) Definition() c.Definition { return x.def }

func (x *Connector) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	ans, err := x.call(ctx, cl, x.m.Test.Request, x.m.Test.Errors, vars(creds, nil))
	if err != nil {
		return c.Account{}, err
	}
	a := x.m.Test.Account
	acc := c.Account{ID: str(ans, a.ID), Name: str(ans, a.Name), Email: str(ans, a.Email)}
	if len(a.Credits) > 0 {
		var sum float64
		found := false
		for _, p := range a.Credits {
			if n, ok := num(lookup(ans, p)); ok {
				sum += n
				found = true
			}
		}
		if found {
			acc.Credits = &sum
		}
	}
	for k, p := range a.Details {
		if v := str(ans, p); v != "" {
			if acc.Details == nil {
				acc.Details = map[string]string{}
			}
			acc.Details[k] = v
		}
	}
	return acc, nil
}

func (x *Connector) Verify(ctx context.Context, cl *c.Client, creds c.Creds, email string) (c.Verification, error) {
	v := x.m.Caps.Verify
	if v == nil {
		return c.Verification{}, c.Errf(c.ErrInvalid, "%s cannot verify addresses", x.m.Name)
	}
	ans, err := x.call(ctx, cl, v.Request, v.Errors, vars(creds, map[string]string{"email": email}))
	if err != nil {
		return c.Verification{}, err
	}
	r := v.Result
	out := c.Verification{
		Email: email, Reason: str(ans, r.Reason), ProviderStatus: str(ans, r.ProviderStatus), Suggestion: str(ans, r.Suggestion),
		CatchAll: truthy(lookup(ans, r.CatchAll)), Disposable: truthy(lookup(ans, r.Disposable)),
		Role: truthy(lookup(ans, r.Role)), Free: truthy(lookup(ans, r.Free)),
	}
	word := str(ans, r.Status.From)
	if sv, ok := r.Status.Values[word]; ok {
		out.Status = sv.Status
		out.CatchAll = out.CatchAll || sv.CatchAll
		out.Disposable = out.Disposable || sv.Disposable
		out.Role = out.Role || sv.Role
	} else if r.Status.Default != "" {
		out.Status = r.Status.Default
	} else {
		out.Status = c.Unknown
	}
	if n, ok := num(lookup(ans, r.Score)); ok && r.Score != "" {
		if s := int(n + 0.5); s >= 0 && s <= 100 {
			out.Score = &s
		}
	}
	if out.Reason == "" {
		out.Reason = word
	}
	return out, nil
}

// vars are the template variables: the credentials and the inputs.
func vars(creds c.Creds, inputs map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range creds {
		out[k] = v
	}
	for k, v := range inputs {
		out[k] = v
	}
	return out
}

func fill(s string, vars map[string]string, escape func(string) string) string {
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		v := vars[varRe.FindStringSubmatch(m)[1]]
		if escape != nil {
			return escape(v)
		}
		return v
	})
}

func fillAny(v any, vars map[string]string) any {
	switch x := v.(type) {
	case string:
		return fill(x, vars, nil)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = fillAny(e, vars)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fillAny(e, vars)
		}
		return out
	}
	return v
}

// build turns a request template into an HTTP request on the base URL.
func (x *Connector) build(cl *c.Client, r Request, vars map[string]string) (*http.Request, error) {
	base := cl.Base(x.m.ID, x.m.BaseURL)
	u := base + fill(r.Path, vars, url.PathEscape)
	if len(r.Query) > 0 {
		q := url.Values{}
		for k, v := range r.Query {
			q.Set(k, fill(v, vars, nil))
		}
		u += "?" + q.Encode()
	}
	var body io.Reader
	if r.Body != nil {
		b, err := json.Marshal(fillAny(r.Body, vars))
		if err != nil {
			return nil, c.Errf(c.ErrInvalid, "%s: could not build the request", x.m.Name)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(r.Method, u, body)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "%s: could not build the request", x.m.Name)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.Headers {
		v = fill(v, vars, nil)
		if strings.ContainsAny(v, "\r\n") {
			return nil, c.Errf(c.ErrInvalid, "%s: a value has a line break in it", x.m.Name)
		}
		req.Header.Set(k, v)
	}
	return req, nil
}

// call sends a request and decodes the JSON answer, applying the error
// rules to a successful one.
func (x *Connector) call(ctx context.Context, cl *c.Client, r Request, rules []ErrorRule, vars map[string]string) (any, error) {
	req, err := x.build(cl, r, vars)
	if err != nil {
		return nil, err
	}
	name := x.m.Name
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "could not reach %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "reading the %s response: %v", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.HTTPError(name, resp.StatusCode, string(body))
	}
	var ans any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&ans); err != nil {
		return nil, c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", name)
	}
	for _, rule := range rules {
		if rule.When.match(ans) {
			return nil, rule.err(name, ans)
		}
	}
	return ans, nil
}

func (cd Cond) match(ans any) bool {
	v := lookup(ans, cd.Path)
	switch {
	case cd.Present:
		return v != nil && fmt.Sprint(v) != ""
	case cd.Equals != nil:
		return v != nil && text(v) == *cd.Equals
	case cd.Not != nil:
		return v == nil || text(v) != *cd.Not
	}
	return false
}

func (e ErrorRule) err(name string, ans any) error {
	msg := str(ans, e.Message)
	if msg == "" {
		msg = "the request was refused"
	}
	kind := e.Kind
	if kind == "" {
		kind = c.ErrInvalid
	}
	low := strings.ToLower(msg)
	for _, k := range e.Kinds {
		if strings.Contains(low, strings.ToLower(k.Contains)) {
			kind = k.Kind
			break
		}
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if kind == c.ErrAuth {
		return c.Errf(kind, "%s refused the API key: %s", name, msg)
	}
	return c.Errf(kind, "%s: %s", name, msg)
}

// lookup follows a dotted path ("domain.acceptAll", "results.0.email")
// through a decoded JSON answer. An empty path or a missing value is nil.
func lookup(v any, p string) any {
	if p == "" {
		return nil
	}
	for _, part := range strings.Split(p, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

// text is a scalar as words; objects and lists are empty.
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

func str(ans any, p string) string { return text(lookup(ans, p)) }

// truthy reads flags services send as true, "true", 1, "1" or "yes".
func truthy(v any) bool {
	switch strings.ToLower(text(v)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// num reads a number, also when sent as a string ("12").
func num(v any) (float64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(text(v)), 64)
	return n, err == nil
}
