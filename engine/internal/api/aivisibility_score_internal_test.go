package api

import "testing"

func TestDemandScore(t *testing.T) {
	if s := demandScore(nil); s.Score != nil {
		t.Fatalf("no answers: %+v", s)
	}
	// Two questions: one asked 900 times a month and recommending the
	// brand in both answers, one asked 100 times and leaving it out.
	s := demandScore([]scoreQuestion{
		{weight: 900, answers: 2, points: 2, square: 2},
		{weight: 100, answers: 2},
	})
	if *s.Score != 90 || *s.Low >= 90 || *s.High > 100 || *s.Low < 0 {
		t.Fatalf("score: %v %v %v", *s.Score, *s.Low, *s.High)
	}
	// More answers narrow the range.
	wide := demandScore([]scoreQuestion{{weight: 1, answers: 2, points: 1.2, square: 0.72}})
	narrow := demandScore([]scoreQuestion{{weight: 1, answers: 50, points: 30, square: 18}})
	if *wide.Score != 60 || *narrow.Score != 60 || *narrow.High-*narrow.Low >= *wide.High-*wide.Low {
		t.Fatalf("ranges: %+v %+v", wide, narrow)
	}
	d := 400
	zero := 0
	w := weights(map[string]*int{"a": &d, "b": nil, "c": &zero})
	if w["a"] != 400 || w["b"] != 400 || w["c"] != 400 {
		t.Fatalf("weights: %v", w)
	}
	if w := weights(map[string]*int{"a": nil, "b": nil}); w["a"] != 1 || w["b"] != 1 {
		t.Fatalf("no demand: %v", w)
	}
}
