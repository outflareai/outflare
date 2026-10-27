package cli

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// GeneratePython writes the Python SDK's operations module from the API
// document: one method per operation, the same set the CLI has, so the
// SDK cannot fall behind the server. The engine's tests check that the
// committed file matches; regenerate it with go run ./engine/cmd/sdkgen.
func GeneratePython(spec []byte) ([]byte, error) {
	doc, err := parseDocument(spec)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`# Generated from api/openapi.yaml by go run ./engine/cmd/sdkgen. Do not edit.
"""Every Outflare API operation as a method. Mixed into outflare.Outflare."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, Literal

if TYPE_CHECKING:
    from ._client import _Base as _Mixin
else:
    _Mixin = object


class Operations(_Mixin):`)
	for _, op := range doc.ops {
		// Operations only a signed-in person can use need the browser's
		// session; the SDK signs in with a token.
		if op.Access["signed_in"] == true {
			continue
		}
		b.WriteString("\n")
		writePythonMethod(&b, op)
	}
	return b.Bytes(), nil
}

// Names the methods use for themselves; a body field with one of these
// names is passed in extra_body= instead.
var pyReserved = []string{"self", "extra_body", "content", "content_type", "idempotency_key"}

func writePythonMethod(b *bytes.Buffer, op *operation) {
	type arg = pyArg
	var positional, keyword []arg
	used := map[string]bool{}
	for _, p := range op.Params {
		a := arg{py: pyName(p.Name), api: p.Name, in: p.In, hint: pyHint(p.Schema), required: p.Required}
		used[a.py] = true
		switch {
		case p.In == "path" && (p.Name == "ws" || p.Name == "ag"):
			a.required = false // filled from the client
			keyword = append(keyword, a)
		case p.In == "path":
			positional = append(positional, a)
		default:
			keyword = append(keyword, a)
		}
	}
	var fields []arg
	for _, f := range op.BodyFields {
		a := arg{py: pyName(f.Name), api: f.Name, in: "body", hint: pyHint(f.Schema), required: f.Required}
		if used[a.py] || slices.Contains(pyReserved, a.py) {
			continue
		}
		used[a.py] = true
		fields = append(fields, a)
	}

	name := strings.ReplaceAll(op.Command, "-", "_")
	fmt.Fprintf(b, "    def %s(\n        self,\n", name)
	for _, a := range positional {
		fmt.Fprintf(b, "        %s: str,\n", a.py)
	}
	var kw []string
	for _, a := range append(keyword, fields...) {
		if a.required {
			kw = append(kw, fmt.Sprintf("%s: %s", a.py, a.hint))
		} else {
			kw = append(kw, fmt.Sprintf("%s: %s | None = None", a.py, a.hint))
		}
	}
	switch op.BodyType {
	case "application/json":
		kw = append(kw, "extra_body: dict[str, Any] | None = None")
	case "":
	default:
		kw = append(kw, "content: str | bytes")
	}
	if op.Method != "GET" {
		kw = append(kw, "idempotency_key: str | None = None")
	}
	if len(kw) > 0 {
		b.WriteString("        *,\n")
	}
	for _, k := range kw {
		b.WriteString("        " + k + ",\n")
	}
	b.WriteString("    ) -> Any:\n")
	writeDocstring(b, op)

	var path, query, body []string
	for _, a := range append(positional, keyword...) {
		entry := fmt.Sprintf("%q: %s", a.api, a.py)
		if a.in == "path" {
			path = append(path, entry)
		} else {
			query = append(query, entry)
		}
	}
	for _, a := range fields {
		body = append(body, fmt.Sprintf("%q: %s", a.api, a.py))
	}
	fmt.Fprintf(b, "        return self._call(\n            %q,\n            %q,\n", op.Method, op.Path)
	if len(path) > 0 {
		writePyDict(b, "path", path)
	}
	if len(query) > 0 {
		writePyDict(b, "query", query)
	}
	switch op.BodyType {
	case "application/json":
		writePyDict(b, "body", body)
		b.WriteString("            extra_body=extra_body,\n")
	case "":
	default:
		fmt.Fprintf(b, "            content=content,\n            content_type=%q,\n", op.BodyType)
	}
	if op.Method != "GET" {
		b.WriteString("            idempotency_key=idempotency_key,\n")
	}
	b.WriteString("        )\n")
}

// writePyDict writes name={...}, on one line when it fits in 120 columns
// and one entry per line otherwise, as ruff format would.
func writePyDict(b *bytes.Buffer, name string, entries []string) {
	line := fmt.Sprintf("            %s={%s},", name, strings.Join(entries, ", "))
	if len(line) <= 120 {
		b.WriteString(line + "\n")
		return
	}
	fmt.Fprintf(b, "            %s={\n", name)
	for _, e := range entries {
		b.WriteString("                " + e + ",\n")
	}
	b.WriteString("            },\n")
}

type pyArg struct {
	py, api, in, hint string
	required          bool
}

// writeDocstring gives each method the operation's summary, description,
// the access it needs and its HTTP call, so help() and editors show the
// same text as the API reference.
func writeDocstring(b *bytes.Buffer, op *operation) {
	esc := strings.NewReplacer(`\`, `\\`, `"""`, `\"\"\"`)
	lines := []string{strings.TrimSpace(op.Summary) + "."}
	if strings.HasSuffix(op.Summary, ".") || op.Summary == "" {
		lines[0] = strings.TrimSpace(op.Summary)
	}
	if d := strings.TrimSpace(op.Description); d != "" {
		lines = append(lines, "")
		lines = append(lines, strings.Split(d, "\n")...)
	}
	lines = append(lines, "")
	if access := describeAccess(op.Access); access != "" {
		lines = append(lines, "Needs: "+access+".")
	}
	lines = append(lines, op.Method+" "+op.Path)
	b.WriteString(`        """`)
	for i, l := range lines {
		l = strings.TrimRight(esc.Replace(l), " ")
		if i == 0 {
			b.WriteString(l + "\n")
			continue
		}
		if l == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("        " + l + "\n")
	}
	b.WriteString(`        """` + "\n")
}

// pyName is a parameter or field name as a Python identifier: from and
// is become from_ and is_.
func pyName(s string) string {
	s = strings.NewReplacer("-", "_", ".", "_").Replace(s)
	if slices.Contains(pyKeywords, s) {
		s += "_"
	}
	return s
}

var pyKeywords = []string{"False", "None", "True", "and", "as", "assert", "async", "await", "break", "class", "continue",
	"def", "del", "elif", "else", "except", "finally", "for", "from", "global", "if", "import", "in", "is", "lambda",
	"nonlocal", "not", "or", "pass", "raise", "return", "try", "while", "with", "yield"}

func pyHint(s schema) string {
	if len(s.Enum) > 0 && s.Type != "array" {
		q := make([]string, len(s.Enum))
		for i, e := range s.Enum {
			q[i] = fmt.Sprintf("%q", e)
		}
		// Long enums (every scope) read worse as a hint than str.
		if lit := "Literal[" + strings.Join(q, ", ") + "]"; len(lit) <= 90 {
			return lit
		}
		if s.Type == "string" {
			return "str"
		}
	}
	switch s.Type {
	case "string":
		return "str"
	case "integer":
		return "int"
	case "number":
		return "float"
	case "boolean":
		return "bool"
	case "array":
		return "list[" + pyHint(schema{Type: s.Items, Enum: s.Enum}) + "]"
	case "object":
		return "dict[str, Any]"
	}
	return "Any"
}
