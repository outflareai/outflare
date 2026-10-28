package playbooks_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/outflareai/outflare/engine/internal/playbooks"
	folder "github.com/outflareai/outflare/playbooks"
)

func TestShippedKits(t *testing.T) {
	list, err := playbooks.LoadKits(folder.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("no kits found")
	}
	all, err := playbooks.Kits()
	if err != nil || len(all) != len(list) {
		t.Fatalf("Kits() = %d, %v; want %d", len(all), err, len(list))
	}
	if _, ok := playbooks.GetKit("creators-partnerships"); !ok {
		t.Fatal("creators-partnerships is missing")
	}
}

const goodKit = `id: demo-kit
version: 1
title: Demo
summary: A demo kit.
option_sets:
  - key: demo_stage
    name: Demo stage
    options:
      - { key: open, label: Open }
      - { key: won, label: Won }
record_types:
  - key: demo_deal
    name: Demo deal
    plural: Demo deals
    stage: stage
    fields:
      - { key: stage, label: Stage, value_type: status, option_set: demo_stage }
      - { key: buyer, label: Buyer, value_type: record_reference, target_type: person }
fields:
  person:
    - { key: shoe_size, label: Shoe size, value_type: number }
email_templates:
  - handle: demo-hello
    name: Hello
    purpose: lifecycle
    subject: Hi {{first_name|there}}
    text: Hello from {{brand}}.
`

func TestKitValidate(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"good", "", "", ""},
		{"unknown key", "version: 1", "version: 1\ncolour: red", "colour"},
		{"bad value type", "value_type: number", "value_type: money", "value_type is one of"},
		{"select without a list", "value_type: status, option_set: demo_stage", "value_type: status", "name an option_set"},
		{"stage not a status", "stage: stage", "stage: buyer", "stage must name"},
		{"unknown target", "target_type: person", "target_type: invoice", "target_type"},
		{"built-in type", "key: demo_deal", "key: person", "not a built-in"},
		{"reserved field", "key: shoe_size", "key: name", "the record itself"},
		{"multiple number", "value_type: number }", "value_type: number, multiple: true }", "multiple is for"},
		{"fields on unknown type", "  person:\n", "  invoice:\n", "is not person"},
		{"broken template", "Hello from {{brand}}.", "Hello {{#if brand}}", "mistake"},
		{"bad purpose", "purpose: lifecycle", "purpose: outreach", "purpose is"},
		{"no subject", "subject: Hi {{first_name|there}}", "subject: ''", "needs a subject"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := goodKit
			if tc.from != "" {
				src = strings.Replace(goodKit, tc.from, tc.to, 1)
			}
			k, err := playbooks.ParseKit([]byte(src))
			if err == nil {
				err = k.Validate()
			}
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestKitFileMustMatchID(t *testing.T) {
	fsys := fstest.MapFS{"kits/other.yaml": {Data: []byte(goodKit)}}
	if _, err := playbooks.LoadKits(fsys); err == nil || !strings.Contains(err.Error(), "file") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlaybookKitMustExist(t *testing.T) {
	src := good + "kit: no-such-kit\n"
	fsys := fstest.MapFS{"demo-play/playbook.yaml": {Data: []byte(src)}, "kits/demo-kit.yaml": {Data: []byte(goodKit)}}
	if _, err := playbooks.Load(fsys, fullCatalog()); err == nil || !strings.Contains(err.Error(), "no-such-kit") {
		t.Fatalf("err = %v", err)
	}
	fsys["demo-play/playbook.yaml"] = &fstest.MapFile{Data: []byte(good + "kit: demo-kit\n")}
	if list, err := playbooks.Load(fsys, fullCatalog()); err != nil || list[0].Kit != "demo-kit" {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
}
