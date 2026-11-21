package peoplesteps

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/pipeline"
)

// A feature that can't decide about a person yet (its own check got no
// answer) returns a Later: the item waits at enroll and is asked about
// again, without being added anywhere and without using up an attempt.
// Any other error is retried as before.
func TestEnrollLater(t *testing.T) {
	s := steps{c: Config{
		Campaign: func(_ context.Context, it pipeline.Item) (string, bool, error) {
			switch it.Ref {
			case "later":
				return "", false, fmt.Errorf("guard: %w", &Later{After: 20 * time.Minute, Code: "contact_guard_unavailable", Reason: "the lookup is down"})
			case "broken":
				return "", false, errors.New("database is away")
			}
			return "", false, nil
		},
	}}
	out, err := s.enroll(context.Background(), []pipeline.Item{{ID: "1", Ref: "later"}, {ID: "2", Ref: "broken"}})
	if err != nil || len(out) != 2 {
		t.Fatalf("enroll: %+v, %v", out, err)
	}
	if r := out[0]; r.Outcome != pipeline.Wait || r.Code != "contact_guard_unavailable" || r.RetryAfter != 20*time.Minute || r.Reason != "the lookup is down" {
		t.Errorf("a person to ask about later: %+v", r)
	}
	if r := out[1]; r.Outcome != pipeline.Retry {
		t.Errorf("any other error: %+v", r)
	}
}
