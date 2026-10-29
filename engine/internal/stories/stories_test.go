package stories

import (
	"slices"
	"testing"
)

func TestNumbers(t *testing.T) {
	got := Numbers("In 2026, 37% of 1,200 sites (37 percent) blocked bots, up 2x; plans cost $49 and 3 teams took part, 14.5 million requests.")
	want := []string{"37%", "1200", "37%", "2x", "$49", "14.5m"}
	if !slices.Equal(got, want) {
		t.Fatalf("Numbers = %q, want %q", got, want)
	}
}

func TestCheck(t *testing.T) {
	facts := []Fact{
		{Statement: "37% of sites blocked bots in our October sample", SourceNote: "Query: bot blocks by site, October"},
		{Statement: "We measured 1,200 sites", SourceURL: "https://northpeak.example/data"},
	}
	quotes := []Quote{
		{Body: "Blocking every bot also blocks the people your customers send.", Approved: true},
		{Body: "This is a draft I never approved.", Approved: false},
	}
	ok := Check(`Northpeak measured 1200 sites. 37 percent blocked bots. "Blocking every bot also blocks the people your customers send," said its CEO.`, facts, quotes)
	if !ok.OK || len(ok.Errors) != 0 || len(ok.Warnings) != 0 {
		t.Fatalf("clean release: %+v", ok)
	}

	bad := Check(`A revolutionary study of 1,200 sites found 52% blocked bots. "This is a draft I never approved," said the CEO. Returns are guaranteed.`,
		append(facts, Fact{Statement: "An unsourced fact"}), quotes)
	codes := map[string][]string{}
	for _, p := range append(bad.Errors, bad.Warnings...) {
		codes[p.Code] = append(codes[p.Code], p.Detail)
	}
	if bad.OK || !slices.Equal(codes[CodeUnsourcedNumber], []string{"52%"}) || len(codes[CodeUnapprovedQuote]) != 1 ||
		len(codes[CodeFactWithoutSource]) != 1 || !slices.Equal(codes[CodeHype], []string{"revolutionary"}) ||
		!slices.Contains(codes[CodeSecondLook], "guaranteed") || !slices.Contains(codes[CodeSecondLook], "returns") {
		t.Fatalf("bad release: %+v", codes)
	}

	if empty := Check("  ", nil, nil); empty.OK || empty.Errors[0].Code != CodeNoRelease {
		t.Fatalf("empty: %+v", empty)
	}
	// "returns" inside another word is no claim.
	if r := Check("It never returnsx anything.", nil, nil); len(r.Warnings) != 0 {
		t.Fatalf("whole words: %+v", r.Warnings)
	}
}
