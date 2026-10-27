package emailfind

import (
	"slices"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"José-María": "josmara", "  John  ": "john", "O'Brien": "obrien",
		"Zoë": "zo", "": "", "123": "",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalPart(t *testing.T) {
	cases := map[string]string{
		"john.doe@acme.example": "john.doe", "John.Doe@ACME.example": "john.doe",
		"jane+tag@x.test": "jane", "noat": "", "@x.test": "",
	}
	for in, want := range cases {
		if got := localPart(in); got != want {
			t.Errorf("localPart(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCandidatesFor(t *testing.T) {
	cands := candidatesFor("John", "Doe")
	byShape := map[string]string{}
	for _, c := range cands {
		byShape[c.shape] = c.local
	}
	for shape, want := range map[string]string{
		"first.last": "john.doe", "flast": "jdoe", "firstlast": "johndoe",
		"first": "john", "first_last": "john_doe", "last.first": "doe.john",
	} {
		if byShape[shape] != want {
			t.Errorf("shape %q = %q, want %q", shape, byShape[shape], want)
		}
	}
	// Local parts are de-duplicated: no two candidates share one.
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.local] {
			t.Errorf("duplicate local part %q", c.local)
		}
		seen[c.local] = true
	}
}

func TestCandidatesForMissingName(t *testing.T) {
	// With only a first name, shapes that need a last name are dropped.
	cands := candidatesFor("John", "")
	for _, c := range cands {
		if c.local == "" {
			t.Fatal("empty candidate local part")
		}
	}
	if len(cands) == 0 {
		t.Fatal("expected at least the first-name-only shape")
	}
}

func TestInferShapes(t *testing.T) {
	got := inferShapes("John", "Doe", "john.doe")
	if !slices.Contains(got, "first.last") {
		t.Errorf("inferShapes john.doe = %v, want first.last", got)
	}
	if got := inferShapes("John", "Doe", "jdoe"); !slices.Contains(got, "flast") {
		t.Errorf("inferShapes jdoe = %v, want flast", got)
	}
	// An address that fits no known shape for the name infers nothing.
	if got := inferShapes("John", "Doe", "xyzzy"); len(got) != 0 {
		t.Errorf("inferShapes xyzzy = %v, want none", got)
	}
}

func TestProfileRecomputePicksDominantShape(t *testing.T) {
	p := Profile{Shapes: map[string]tally{
		"first.last": {Hits: 10, Misses: 0},
		"flast":      {Hits: 1, Misses: 0},
	}}
	p.recompute()
	if p.TopShape != "first.last" {
		t.Errorf("TopShape = %q, want first.last", p.TopShape)
	}
	if p.SampleSize != 11 {
		t.Errorf("SampleSize = %d, want 11", p.SampleSize)
	}
	if p.Confidence < confThreshold {
		t.Errorf("confidence = %d, want >= %d for a dominant shape", p.Confidence, confThreshold)
	}
}

func TestProfileRecomputeThinOrMixedIsLowConfidence(t *testing.T) {
	// One observation each for two shapes: no clear winner.
	p := Profile{Shapes: map[string]tally{
		"first.last": {Hits: 1},
		"flast":      {Hits: 1},
	}}
	p.recompute()
	if p.Confidence >= confThreshold {
		t.Errorf("confidence = %d, want < %d for a tied domain", p.Confidence, confThreshold)
	}
}

func TestProfileRecomputeCatchAllCapsConfidence(t *testing.T) {
	strong := map[string]tally{"first.last": {Hits: 30}}
	a := Profile{Shapes: strong}
	a.recompute()
	b := Profile{Shapes: strong, CatchAll: true}
	b.recompute()
	if b.Confidence >= a.Confidence {
		t.Errorf("catch-all confidence %d should be below non-catch-all %d", b.Confidence, a.Confidence)
	}
}

func TestProfileRecomputeMissesLowerShape(t *testing.T) {
	p := Profile{Shapes: map[string]tally{"first.last": {Hits: 3, Misses: 7}}}
	p.recompute()
	// Net hits are negative, so there is no winning shape.
	if p.TopShape != "" {
		t.Errorf("TopShape = %q, want empty when misses dominate", p.TopShape)
	}
}
