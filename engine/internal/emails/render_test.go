package emails

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	defs := []Variable{{Name: "first_name", Default: "there"}, {Name: "amount", Required: true}, {Name: "coupon"}}
	vars := map[string]any{"amount": 49.5, "user": map[string]any{"plan": "Pro"}, "elsewhere": "x"}
	r, err := Render("Receipt for {{ first_name }}", `<p>Hi {{first_name}}, {{amount}} <b>{{user.plan}}</b> {{elsewhere}}</p>{{#if coupon}}C{{else}}none{{/if}}{{#unless coupon}}!{{/unless}}{{{raw}}}`, "Hi {{name|friend}}", defs, vars)
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "Receipt for there" {
		t.Errorf("subject %q", r.Subject)
	}
	if want := `<p>Hi there, 49.5 <b>Pro</b> x</p>none!`; r.HTML != want {
		t.Errorf("html %q, want %q", r.HTML, want)
	}
	if r.Text != "Hi friend" {
		t.Errorf("text %q", r.Text)
	}
	if strings.Join(r.Unknown, ",") != "elsewhere,name,raw,user.plan" {
		t.Errorf("unknown %v", r.Unknown)
	}
	if len(r.Missing) != 0 {
		t.Errorf("missing %v", r.Missing)
	}
	r, _ = Render("", "", "", defs, nil)
	if strings.Join(r.Missing, ",") != "amount" {
		t.Errorf("missing %v", r.Missing)
	}
}

func TestRenderEscapes(t *testing.T) {
	r, _ := Render("{{x}}", "{{x}}", "{{x}}", nil, map[string]any{"x": `<script>"&`})
	if r.HTML != "&lt;script&gt;&#34;&amp;" || r.Text != `<script>"&` || r.Subject != `<script>"&` {
		t.Errorf("%+v", r)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{"{{#if a}}x", "{{/if}}", "{{else}}", "{{#if a}}{{/unless}}"} {
		if err := Check("", src, ""); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
	if err := Check("", "{{#if a}}{{#unless b}}x{{/unless}}{{/if}}", ""); err != nil {
		t.Error(err)
	}
}

func TestPlainText(t *testing.T) {
	got := PlainText(`<html><head><style>p{}</style></head><body><h1>Hi</h1><p>Reset <a href="https://x-io.example/r">here</a>.<br>Thanks &amp; bye</p></body></html>`)
	want := "Hi\n\nReset here (https://x-io.example/r).\nThanks & bye"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
