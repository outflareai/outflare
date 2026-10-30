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

func TestPaths(t *testing.T) {
	wait := Step{ID: "w", Kind: KindWaitUntil, TimeoutSeconds: 3600, TimeoutNext: "late", Paths: []Path{
		{ID: "paid", Event: "order_completed", Next: "thanks"},
		{ID: "pro", Conditions: []Condition{{Attribute: "plan", Op: "eq", Value: "pro"}}, Next: "loop"},
	}}
	steps := []Step{wait, {ID: "thanks", Kind: KindExit}, {ID: "late", Kind: KindExit}, {ID: "loop", Kind: KindWait, WaitSeconds: 60, Next: "w"}}
	if msg := Check(steps); !strings.Contains(msg, "loop back") {
		t.Fatalf("a path back to its own step = %q", msg)
	}
	steps[3].Next = ""
	if msg := Check(steps); msg != "" {
		t.Fatalf("Check = %q", msg)
	}
	if got := Unreachable(steps); len(got) != 0 {
		t.Fatalf("Unreachable = %v (paths and the timeout lead on)", got)
	}
	split := Step{ID: "s", Kind: KindSplit, SplitBy: "random", SplitKey: "company", Paths: []Path{{ID: "a", Weight: 90, Next: "x"}, {ID: "hold", Weight: 10, Holdout: true}}}
	if msg := Check([]Step{split, {ID: "x", Kind: KindExit}}); msg != "" {
		t.Fatalf("Check = %q", msg)
	}
	for want, st := range map[string]Step{
		"1 to 10 paths":       {ID: "s", Kind: KindSplit},
		"event or for":        {ID: "w", Kind: KindWaitUntil, TimeoutSeconds: 60, Paths: []Path{{ID: "a"}}},
		"timeout_seconds":     {ID: "w", Kind: KindWaitUntil, Paths: []Path{{ID: "a", Event: "x"}}},
		"tests conditions":    {ID: "s", Kind: KindSplit, Paths: []Path{{ID: "a"}}},
		"weight above 0":      {ID: "s", Kind: KindSplit, SplitBy: "random", Paths: []Path{{ID: "a"}}},
		"id must be unique":   {ID: "s", Kind: KindSplit, SplitBy: "random", Paths: []Path{{ID: "a", Weight: 1}, {ID: "a", Weight: 1}}},
		"for a random split":  {ID: "s", Kind: KindSplit, Paths: []Path{{ID: "a", Weight: 5, Conditions: []Condition{{Attribute: "plan", Op: "exists"}}}}},
		"event is for":        {ID: "s", Kind: KindSplit, Paths: []Path{{ID: "a", Event: "x", Conditions: []Condition{{Attribute: "plan", Op: "exists"}}}}},
		"not next or then":    {ID: "s", Kind: KindSplit, Next: "x", SplitBy: "random", Paths: []Path{{ID: "a", Weight: 1}}},
		"split_key is person": {ID: "s", Kind: KindSplit, SplitBy: "random", SplitKey: "team", Paths: []Path{{ID: "a", Weight: 1}}},
		"holdout path ends":   {ID: "s", Kind: KindSplit, SplitBy: "random", Paths: []Path{{ID: "a", Weight: 1, Holdout: true, Next: "s"}}},
		"are for split steps": {ID: "w", Kind: KindWaitUntil, TimeoutSeconds: 60, SplitBy: "random", Paths: []Path{{ID: "a", Event: "x"}}},
		"are for wait_until":  {ID: "s", Kind: KindSplit, TimeoutSeconds: 60, SplitBy: "random", Paths: []Path{{ID: "a", Weight: 1}}},
	} {
		if msg := Check([]Step{st}); !strings.Contains(msg, want) {
			t.Errorf("Check(%s) = %q", want, msg)
		}
	}
}
