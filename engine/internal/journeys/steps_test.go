package journeys

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	ok := []Step{
		{ID: "hi", Kind: KindEmail, Template: "welcome", Next: "wait"},
		{ID: "wait", Kind: KindWait, WaitSeconds: 3600, Next: "check"},
		{ID: "check", Kind: KindBranch, Conditions: []Condition{{Email: "opened", Step: "hi", Op: "exists"}}, Then: "done", Else: "hi2"},
		{ID: "hi2", Kind: KindEmail, Template: "nudge"},
		{ID: "done", Kind: KindExit},
	}
	if msg := Check(ok); msg != "" {
		t.Fatalf("Check = %q", msg)
	}
	for want, steps := range map[string][]Step{
		"loop back":         {{ID: "a", Kind: KindWait, WaitSeconds: 60, Next: "b"}, {ID: "b", Kind: KindWait, WaitSeconds: 60, Next: "a"}},
		"not a step":        {{ID: "a", Kind: KindWait, WaitSeconds: 60, Next: "zz"}},
		"two steps":         {{ID: "a", Kind: KindExit}, {ID: "a", Kind: KindExit}},
		"needs a template":  {{ID: "a", Kind: KindEmail}},
		"wait_seconds":      {{ID: "a", Kind: KindWait, WaitSeconds: 5}},
		"not an email step": {{ID: "a", Kind: KindBranch, Conditions: []Condition{{Email: "opened", Step: "a", Op: "exists"}}}},
		"exists (did)":      {{ID: "a", Kind: KindBranch, Conditions: []Condition{{Event: "paid", Op: "eq", Value: 1}}}},
		"needs a value":     {{ID: "a", Kind: KindBranch, Conditions: []Condition{{Attribute: "plan", Op: "eq"}}}},
		"kind is":           {{ID: "a", Kind: "sms"}},
	} {
		if msg := Check(steps); !strings.Contains(msg, want) {
			t.Errorf("Check(%s) = %q", want, msg)
		}
	}
	if msg := CheckFilter([]Condition{{Event: "paid", Op: "exists"}}); msg == "" {
		t.Error("an event condition in a trigger filter was allowed")
	}
}

func TestUnreachable(t *testing.T) {
	steps := []Step{
		{ID: "hi", Kind: KindEmail, Template: "welcome", Next: "check"},
		{ID: "check", Kind: KindBranch, Then: "done"},
		{ID: "lost", Kind: KindWait, WaitSeconds: 60, Next: "done"},
		{ID: "done", Kind: KindExit},
	}
	if got := Unreachable(steps); len(got) != 1 || got[0] != "lost" {
		t.Fatalf("Unreachable = %v", got)
	}
	if got := Unreachable(nil); got != nil {
		t.Fatalf("Unreachable(nil) = %v", got)
	}
}
