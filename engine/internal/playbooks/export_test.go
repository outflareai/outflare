package playbooks_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// Every shipped kit written out with Marshal reads back as the same kit,
// so an exported kit is a kit file like the others.
func TestMarshalRoundTrip(t *testing.T) {
	all, err := playbooks.Kits()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range all {
		b, err := k.Marshal()
		if err != nil {
			t.Fatalf("%s: %v", k.ID, err)
		}
		back, err := playbooks.ParseKit(b)
		if err != nil {
			t.Fatalf("%s: %v\n%s", k.ID, err, b)
		}
		if !reflect.DeepEqual(back, k) {
			t.Errorf("%s doesn't read back the same:\n%s", k.ID, b)
		}
		if !strings.HasPrefix(string(b), "id: "+k.ID+"\n") {
			t.Errorf("%s doesn't start with its id:\n%.200s", k.ID, b)
		}
	}
	// A step reads id, kind, then its settings.
	k, _ := playbooks.GetKit("trial-activation")
	b, _ := k.Marshal()
	if !strings.Contains(string(b), "- id: nudge\n        kind: email\n") {
		t.Errorf("steps don't start with id and kind:\n%s", b)
	}
	// Templates' {{person.x}} and alert texts' variables stay as written.
	if !strings.Contains(string(b), "{{") {
		t.Error("no placeholders left")
	}
}

func TestKeyFor(t *testing.T) {
	taken := map[string]bool{}
	for _, c := range []struct{ name, want string }{
		{"Activate new trials", "activate_new_trials"},
		{"Activate new trials!", "activate_new_trials_2"},
		{"  NPS: detractors (score < 7)  ", "nps_detractors_score_7"},
		{"2024 win-back", "part_2024_win_back"},
		{"A", "part_a"},
		{"", "part"},
		{"Ünïcode only ✓", "n_code_only"},
		{strings.Repeat("long ", 20), "long_long_long_long_long_long_long_l"},
	} {
		if got := playbooks.KeyFor(c.name, taken); got != c.want {
			t.Errorf("KeyFor(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	if got := playbooks.IDFor("Activate new trials"); got != "activate-new-trials" {
		t.Errorf("IDFor = %q", got)
	}
}

// A kit journey sends only the kit's own drafts, of the right purpose, and
// never names workspace members.
func TestKitFlowSteps(t *testing.T) {
	k, _ := playbooks.GetKit("trial-activation")
	b, _ := k.Marshal()
	for _, c := range []struct{ from, to, want string }{
		{"kind: email\n        template: activation-tip", "kind: transactional_email\n        template: activation-tip", "transactional email drafts"},
		{"kind: alert\n", "kind: assign_owner\n        owner_id: 00000000-0000-4000-8000-000000000001\n", "workspace members"},
	} {
		src := strings.Replace(string(b), c.from, c.to, 1)
		if src == string(b) {
			t.Fatalf("%q isn't in the kit", c.from)
		}
		bad, err := playbooks.ParseKit([]byte(src))
		if err == nil {
			err = bad.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("error = %v, want it to mention %q", err, c.want)
		}
	}
}
