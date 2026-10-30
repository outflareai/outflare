package emailfind

import (
	"context"
	"testing"
)

// fakeNetwork answers every lookup with a fixed answer and records what
// was asked.
type fakeNetwork struct {
	a     NetworkAnswer
	ok    bool
	asked [][]string
}

func (f *fakeNetwork) LookupNetwork(_ context.Context, _, _ string, emails []string) (NetworkAnswer, bool) {
	f.asked = append(f.asked, emails)
	return f.a, f.ok
}

func janeCands() []candidate { return candidatesFor("Jane", "Roe") }

func TestApplyNetworkAddressBeatsWeakGuess(t *testing.T) {
	r := ItemResult{Email: "jroe@acme.example", Confidence: 30, Code: CodeRisky, Evidence: EvidenceDefault}
	a := NetworkAnswer{Emails: map[string]NetworkSignal{"jane.roe@acme.example": {Deliverable: true, Confidence: 90}}}
	if got := applyNetwork(&r, janeCands(), "acme.example", a); got != "address" {
		t.Fatalf("signal = %q", got)
	}
	if r.Email != "jane.roe@acme.example" || r.Confidence != 90 || r.Code != CodeFound || r.Evidence != EvidenceNetwork {
		t.Fatalf("result = %+v", r)
	}
}

func TestApplyNetworkShapeForUnknownDomain(t *testing.T) {
	r := ItemResult{Email: "jane.roe@acme.example", Confidence: 34, Code: CodeRisky, Evidence: EvidenceDefault}
	a := NetworkAnswer{Shape: "flast", ShapeConfidence: 80}
	if got := applyNetwork(&r, janeCands(), "acme.example", a); got != "shape" {
		t.Fatalf("signal = %q", got)
	}
	if r.Email != "jroe@acme.example" || r.Confidence != 80 || r.Code != CodeFound {
		t.Fatalf("result = %+v", r)
	}
}

func TestApplyNetworkKeepsStrongerOwnGuess(t *testing.T) {
	r := ItemResult{Email: "jane.roe@acme.example", Confidence: 70, Code: CodeFound, Evidence: EvidencePattern}
	a := NetworkAnswer{Shape: "flast", ShapeConfidence: 40}
	if got := applyNetwork(&r, janeCands(), "acme.example", a); got != "" {
		t.Fatalf("signal = %q", got)
	}
	if r.Email != "jane.roe@acme.example" || r.Evidence != EvidencePattern {
		t.Fatalf("own guess replaced: %+v", r)
	}
}

func TestApplyNetworkCatchAllCapsConfidence(t *testing.T) {
	r := ItemResult{Email: "jane.roe@acme.example", Confidence: 20, Code: CodeRisky, Evidence: EvidenceDefault}
	a := NetworkAnswer{CatchAll: true, Emails: map[string]NetworkSignal{"jane.roe@acme.example": {Deliverable: true, Confidence: 95}}}
	applyNetwork(&r, janeCands(), "acme.example", a)
	if r.Confidence != confThreshold || r.Code != CodeRisky {
		t.Fatalf("catch-all should stay risky at %d: %+v", confThreshold, r)
	}
}

func TestApplyNetworkBouncedGuess(t *testing.T) {
	// The workspace's guess bounced for others; the network's shape gives
	// a better one.
	r := ItemResult{Email: "jane.roe@acme.example", Confidence: 60, Code: CodeFound, Evidence: EvidencePattern}
	a := NetworkAnswer{Shape: "flast", ShapeConfidence: 55,
		Emails: map[string]NetworkSignal{"jane.roe@acme.example": {Deliverable: false, Confidence: 90}}}
	applyNetwork(&r, janeCands(), "acme.example", a)
	if r.Email != "jroe@acme.example" || r.Evidence != EvidenceNetwork {
		t.Fatalf("bounced guess kept: %+v", r)
	}
	// Nothing better known: the guess is undeliverable.
	r = ItemResult{Email: "jane.roe@acme.example", Confidence: 60, Code: CodeFound, Evidence: EvidencePattern}
	a.Shape = ""
	applyNetwork(&r, janeCands(), "acme.example", a)
	if r.Code != CodeUndeliverable || r.Email != "jane.roe@acme.example" {
		t.Fatalf("bounced guess = %+v", r)
	}
}

func TestAssistOnlyAsksAboutThePersonsCandidates(t *testing.T) {
	f := &fakeNetwork{ok: false}
	s := &Service{Network: f}
	items := []FindItem{
		{Ref: "1", FirstName: "Jane", LastName: "Roe", Domain: "www.Acme.example"},
		{Ref: "2", FirstName: "Bob", LastName: "Smith", Domain: "acme.example"},
	}
	res := []ItemResult{
		{Ref: "1", Email: "jane.roe@acme.example", Confidence: 30, Code: CodeRisky, Evidence: EvidenceDefault},
		{Ref: "2", Email: "bob.smith@acme.example", Confidence: 95, Code: CodeVerified, Evidence: EvidenceHistory},
	}
	s.assist(context.Background(), "ws", items, res)
	if len(f.asked) != 1 {
		t.Fatalf("asked %d times; a proven address needs no lookup", len(f.asked))
	}
	for _, e := range f.asked[0] {
		if e[len(e)-len("@acme.example"):] != "@acme.example" {
			t.Fatalf("asked about %q", e)
		}
	}
	if res[0].Evidence != EvidenceDefault {
		t.Fatalf("a refused lookup changed the result: %+v", res[0])
	}
}
