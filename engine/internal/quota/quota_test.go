package quota_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/outflareai/outflare/engine/internal/quota"
)

type checker func(resource string) error

func (c checker) Allow(_ context.Context, _, resource string, _ int64) error { return c(resource) }

func TestCheck(t *testing.T) {
	ctx, log := context.Background(), slog.New(slog.DiscardHandler)
	if err := quota.Check(ctx, nil, log, "ws", quota.Brands, 1); err != nil {
		t.Fatalf("no checker refused: %v", err)
	}
	allow := checker(func(string) error { return nil })
	if err := quota.Check(ctx, allow, log, "ws", quota.Brands, 1); err != nil {
		t.Fatalf("allowed: %v", err)
	}
	refuse := checker(func(r string) error {
		return fmt.Errorf("wrapped: %w", &quota.Error{Resource: r, Message: "No more brands on this plan."})
	})
	err := quota.Check(ctx, refuse, log, "ws", quota.Brands, 1)
	qe, ok := quota.Refusal(err)
	if !ok || qe.Resource != quota.Brands || err.Error() != "No more brands on this plan." {
		t.Fatalf("refused: %v", err)
	}
	broken := checker(func(string) error { return errors.New("down") })
	if err := quota.Check(ctx, broken, log, "ws", quota.Records, 1); err != nil {
		t.Fatalf("a broken checker must allow: %v", err)
	}
}
