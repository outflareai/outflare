package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v4"
)

// The commands are the API's operations, read from the OpenAPI document
// built into the binary, so the CLI has every endpoint the server has and
// never falls behind it.

type operation struct {
	ID          string // operationId, e.g. listLeads
	Command     string // list-leads
	Method      string
	Path        string
	Summary     string
	Description string
	Tag         string
	Access      map[string]any
	Params      []param
	BodyType    string  // "application/json", "text/csv", or "" for none
	BodyFields  []field // top-level properties of a JSON body
	BodyNeeded  bool
	Idempotent  bool // takes an Idempotency-Key, so retries are safe
}

type param struct {
	Name     string
	In       string // path, query or header
	Required bool
	Schema   schema
	Desc     string
}

type field struct {
	Name     string
	Required bool
	Schema   schema
}

type schema struct {
	Type        string // string, integer, number, boolean, array, object, or "" when unknown
	Items       string // array item type
	Enum        []string
	Description string
}

type document struct {
	ops        []*operation
	byCommand  map[string]*operation
	components map[string]any
}

func parseDocument(raw []byte) (*document, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	d := &document{byCommand: map[string]*operation{}}
	if c, ok := doc["components"].(map[string]any); ok {
		d.components = c
	}
	paths, _ := doc["paths"].(map[string]any)
	for path, v := range paths {
		item, _ := v.(map[string]any)
		shared := d.params(item["parameters"])
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			o, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			op := &operation{Method: strings.ToUpper(method), Path: path}
			op.ID, _ = o["operationId"].(string)
			op.Summary, _ = o["summary"].(string)
			op.Description, _ = o["description"].(string)
			if tags, ok := o["tags"].([]any); ok && len(tags) > 0 {
				op.Tag, _ = tags[0].(string)
			}
			op.Access, _ = o["x-outflare-access"].(map[string]any)
			op.Command = kebab(op.ID)
			for _, p := range append(append([]param{}, shared...), d.params(o["parameters"])...) {
				if p.In == "header" && p.Name == "Idempotency-Key" {
					op.Idempotent = true
					continue
				}
				if p.In == "path" || p.In == "query" {
					op.Params = append(op.Params, p)
				}
			}
			if rb, ok := d.resolve(o["requestBody"]).(map[string]any); ok {
				op.BodyNeeded, _ = rb["required"].(bool)
				content, _ := rb["content"].(map[string]any)
				if js, ok := content["application/json"].(map[string]any); ok {
					op.BodyType = "application/json"
					op.BodyFields = d.fields(js["schema"])
				} else {
					for ct := range content {
						op.BodyType = ct
						break
					}
				}
			}
			if op.ID == "" {
				continue
			}
			d.ops = append(d.ops, op)
			d.byCommand[op.Command] = op
		}
	}
	sort.Slice(d.ops, func(i, j int) bool { return d.ops[i].Command < d.ops[j].Command })
	return d, nil
}

// resolve follows a $ref into components.
func (d *document) resolve(v any) any {
	for range 10 {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return v
		}
		var cur any = map[string]any{"components": d.components}
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cm, _ := cur.(map[string]any)
			cur = cm[part]
		}
		v = cur
	}
	return v
}

func (d *document) params(v any) []param {
	list, _ := v.([]any)
	var out []param
	for _, x := range list {
		m, ok := d.resolve(x).(map[string]any)
		if !ok {
			continue
		}
		p := param{Schema: d.schema(m["schema"])}
		p.Name, _ = m["name"].(string)
		p.In, _ = m["in"].(string)
		p.Required, _ = m["required"].(bool)
		p.Desc, _ = m["description"].(string)
		if p.Desc == "" {
			p.Desc = p.Schema.Description
		}
		out = append(out, p)
	}
	return out
}

func (d *document) schema(v any) schema {
	m, ok := d.resolve(v).(map[string]any)
	if !ok {
		return schema{}
	}
	var s schema
	switch t := m["type"].(type) {
	case string:
		s.Type = t
	case []any:
		for _, x := range t {
			if x, ok := x.(string); ok && x != "null" {
				s.Type = x
			}
		}
	}
	if s.Type == "" {
		if _, ok := m["properties"]; ok {
			s.Type = "object"
		}
	}
	for _, e := range asList(m["enum"]) {
		if e != nil {
			s.Enum = append(s.Enum, fmt.Sprint(e))
		}
	}
	if s.Type == "array" {
		s.Items = d.schema(m["items"]).Type
		if it := d.schema(m["items"]); len(it.Enum) > 0 && len(s.Enum) == 0 {
			s.Enum = it.Enum
		}
	}
	s.Description, _ = m["description"].(string)
	return s
}

// fields lists a JSON body's top-level properties, merging allOf.
func (d *document) fields(v any) []field {
	m, ok := d.resolve(v).(map[string]any)
	if !ok {
		return nil
	}
	var out []field
	for _, part := range asList(m["allOf"]) {
		out = append(out, d.fields(part)...)
	}
	required := map[string]bool{}
	for _, r := range asList(m["required"]) {
		if r, ok := r.(string); ok {
			required[r] = true
		}
	}
	props, _ := m["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, field{Name: n, Required: required[n], Schema: d.schema(props[n])})
	}
	return out
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

var (
	upper   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	acronym = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
)

// kebab turns listLeadLists into list-lead-lists, and lead_id into lead-id.
func kebab(s string) string {
	s = upper.ReplaceAllString(s, "$1-$2")
	s = acronym.ReplaceAllString(s, "$1-$2")
	s = strings.ReplaceAll(s, "_", "-")
	return strings.Map(unicode.ToLower, s)
}
