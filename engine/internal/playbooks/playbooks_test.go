package playbooks_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/playbooks"
	folder "github.com/outflareai/outflare/playbooks"
)

func fullCatalog() *playbooks.Catalog {
	return playbooks.NewCatalog(api.OpenAPI, catalog.New().All())
}

// TestShippedPlaybooks checks every playbook in playbooks/ against the API
// and the connector catalog: this is the check a contributor's pull request
// must pass.
func TestShippedPlaybooks(t *testing.T) {
	list, err := playbooks.Load(folder.FS, fullCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("no playbooks found")
	}
	seen := map[string]bool{}
	for _, p := range list {
		if seen[p.ID] {
			t.Errorf("%s is listed twice", p.ID)
		}
		seen[p.ID] = true
	}
	all, err := playbooks.All()
	if err != nil || len(all) != len(list) {
		t.Fatalf("All() = %d, %v; want %d", len(all), err, len(list))
	}
}

const good = `id: demo-play
version: 1
title: Demo
summary: A demo playbook.
tier: obvious
goals: [customers]
publisher: community
author: { github: ana-example }
needs:
  connections:
    - { capability: email.send, label: Email, recommended: [resend] }
inputs:
  - { name: brand, label: Brand, example: Northpeak }
steps:
  - { say: Read the brand., tools: [getBrandContext] }
measure:
  - { metric: Replies, how: Count them. }
prompt: Run it for {{brand}}.
`

func TestValidate(t *testing.T) {
	c := fullCatalog()
	cases := []struct{ name, from, to, want string }{
		{"good", "", "", ""},
		{"unknown key", "tier: obvious", "tier: obvious\ncolour: red", "colour"},
		{"bad tier", "tier: obvious", "tier: easy", "tier must be"},
		{"unknown tool", "getBrandContext", "doMagic", "not an operationId"},
		{"unknown capability", "capability: email.send", "capability: teleport", "no connector has capability"},
		{"wrong recommendation", "recommended: [resend]", "recommended: [hubspot]", "does not offer"},
		{"unknown connector", "recommended: [resend]", "recommended: [nosuchtool]", "not in the catalog"},
		{"undeclared input", "{{brand}}", "{{brand}} {{city}}", "{{city}}"},
		{"unused input", "Run it for {{brand}}.", "Run it.", "never used"},
		{"bad id", "id: demo-play", "id: Demo_Play", "id must be"},
		{"no measure", "measure:\n  - { metric: Replies, how: Count them. }\n", "", "measure needs"},
		{"bad publisher", "publisher: community", "publisher: acme", "publisher must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := good
			if tc.from != "" {
				src = strings.Replace(good, tc.from, tc.to, 1)
			}
			p, err := playbooks.Parse([]byte(src))
			if err == nil {
				err = p.Validate(c)
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

func TestFolderMustMatchID(t *testing.T) {
	fsys := fstest.MapFS{"other-name/playbook.yaml": {Data: []byte(good)}}
	if _, err := playbooks.Load(fsys, fullCatalog()); err == nil || !strings.Contains(err.Error(), "folder") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderAndCheck(t *testing.T) {
	p, err := playbooks.Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Render(map[string]string{"brand": "Northpeak"}); got != "Run it for Northpeak." {
		t.Errorf("Render = %q", got)
	}
	if got := p.Render(nil); got != "Run it for {{brand}}." {
		t.Errorf("Render without a value = %q", got)
	}
	r := p.Check(nil, nil)
	if r.Ready || len(r.MissingConnections) != 1 {
		t.Errorf("no connections: %+v", r)
	}
	r = p.Check([]playbooks.ConnectorInfo{{Category: "email_sending", Capabilities: []string{"email.send"}}}, nil)
	if !r.Ready {
		t.Errorf("with an email provider: %+v", r)
	}
}
