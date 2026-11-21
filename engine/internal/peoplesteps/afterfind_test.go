package peoplesteps

import (
	"context"
	"errors"
	"testing"

	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/pipeline"
)

// A feature's own check sits between find_email and contact_check: an
// item whose address was found goes to the stage AfterFind names, straight
// on when it names none, and is retried when it fails. An item without an
// address never reaches it.
func TestAfterFind(t *testing.T) {
	ctx := context.Background()
	asked := 0
	s := steps{c: Config{
		Found:   func(context.Context, pipeline.Item, string, emailfind.Code, int) error { return nil },
		Stopped: func(context.Context, pipeline.Item, string, []string) error { return nil },
		AfterFind: func(_ context.Context, it pipeline.Item) (string, error) {
			asked++
			switch it.Ref {
			case "checked":
				return "still_there", nil
			case "broken":
				return "", errors.New("could not say")
			}
			return "", nil
		},
	}}
	found := emailfind.ItemResult{Code: emailfind.CodeVerified, Email: "ana@acme.example", Confidence: 95}
	if r := s.found(ctx, pipeline.Item{ID: "1", Ref: "checked"}, found); r.Outcome != pipeline.Advance || r.Next != "still_there" {
		t.Fatalf("an item the feature checks: %+v", r)
	}
	if r := s.found(ctx, pipeline.Item{ID: "2", Ref: "plain"}, found); r.Outcome != pipeline.Advance || r.Next != ContactCheck {
		t.Fatalf("an item the feature doesn't check: %+v", r)
	}
	if r := s.found(ctx, pipeline.Item{ID: "3", Ref: "broken"}, found); r.Outcome != pipeline.Retry {
		t.Fatalf("a check that could not be chosen: %+v", r)
	}
	if r := s.found(ctx, pipeline.Item{ID: "4", Ref: "checked"}, emailfind.ItemResult{Code: emailfind.CodeNotFound}); r.Outcome != pipeline.Skip || asked != 3 {
		t.Fatalf("an item with no address: %+v after %d asks", r, asked)
	}
	s.c.AfterFind = nil
	if r := s.found(ctx, pipeline.Item{ID: "5", Ref: "checked"}, found); r.Outcome != pipeline.Advance || r.Next != ContactCheck {
		t.Fatalf("without AfterFind: %+v", r)
	}
}
