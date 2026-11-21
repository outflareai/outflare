package verify

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Addresses the verifier could not answer are asked again later, not at
// once: a verifier that answers nobody (down, or too busy) would otherwise
// be asked again without a pause, batch after batch.
func TestVerifyRetriesUnansweredAddressesLater(t *testing.T) {
	down := &other{id: "othera", err: errors.New("verifier is not answering")}
	e := newFallbackEnv(t, down)
	e.choose(e.connect("othera", "test_not_a_real_key_a"))
	ctx := context.Background()
	res, err := e.s.Verify(ctx, e.ws, []string{"ana@acme.example", "ben@acme.example"}, false)
	if err != nil || len(res) != 0 {
		t.Fatalf("unanswered addresses: %+v %v", res, err)
	}
	var jobs, addresses int
	var wait time.Duration
	if err := e.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(jsonb_array_length(payload->'emails')), 0),
		coalesce(min(extract(epoch FROM run_at - now())), 0) * interval '1 second'
		FROM jobs WHERE workspace_id = $1 AND kind = $2 AND status = 'queued'`, e.ws, JobVerify).Scan(&jobs, &addresses, &wait); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || addresses != 2 {
		t.Fatalf("%d retry jobs with %d addresses, want 1 with 2", jobs, addresses)
	}
	if wait < RetryDelay-time.Minute {
		t.Fatalf("the retry runs in %s, want about %s", wait, RetryDelay)
	}
}
