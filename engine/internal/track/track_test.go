package track

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHoldSendsOnlyOnSuccess(t *testing.T) {
	got := Capture(t)
	ctx, release := Hold(context.Background())
	Emit(ctx, Event{Name: "mailbox.connected", ActorID: "a1"})
	Emit(ctx, Event{Name: "mailbox.connect_failed", Outcome: Failure, ErrorCode: "invalid_request"})
	if n := len(got()); n != 1 {
		t.Fatalf("a failure inside a hold is sent at once; got %d events before release", n)
	}
	release(true, Event{Surface: "mcp", ActorID: "a1", UserID: "u1", RequestID: "r1"})
	ev := got()
	if len(ev) != 2 || ev[1].Name != "mailbox.connected" {
		t.Fatalf("got %+v", ev)
	}
	if e := ev[1]; e.Surface != "mcp" || e.UserID != "u1" || e.RequestID != "r1" || e.Outcome != Success {
		t.Fatalf("release didn't fill in the request: %+v", e)
	}

	ctx, release = Hold(context.Background())
	Emit(ctx, Event{Name: "sequence.created"})
	release(false, Event{})
	if n := len(got()); n != 2 {
		t.Fatalf("a failed request's actions were sent: %d events", n)
	}
}

func TestNestedHoldDefersToOuter(t *testing.T) {
	got := Capture(t)
	outer, release := Hold(context.Background())
	inner, releaseInner := Hold(outer)
	Emit(inner, Event{Name: "lead.updated"})
	releaseInner(true, Event{})
	if len(got()) != 0 {
		t.Fatal("the inner hold sent events")
	}
	release(true, Event{})
	if len(got()) != 1 {
		t.Fatal("the outer hold didn't send")
	}
}

func TestCaptureFormat(t *testing.T) {
	at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	c := toCapture(Event{Name: "member.removed", UserID: "u1", WorkspaceID: "w1", Surface: "web", Outcome: Success, At: at,
		Props: map[string]any{"target_id": "u2"}}, "1.0.0")
	if c.DistinctID != "u1" || c.Event != "member.removed" || !c.Timestamp.Equal(at) {
		t.Fatalf("got %+v", c)
	}
	if g := c.Properties["$groups"].(map[string]string); g["workspace"] != "w1" {
		t.Fatalf("groups %v", c.Properties["$groups"])
	}
	if _, ok := c.Properties["$process_person_profile"]; ok {
		t.Fatal("a person's event shouldn't turn off person profiles")
	}
	sys := toCapture(Event{Name: "job.dead", WorkspaceID: "w1"}, "1.0.0")
	if sys.DistinctID != "workspace:w1" || sys.Properties["$process_person_profile"] != false {
		t.Fatalf("system event %+v", sys)
	}
}

func TestClientPostsBatches(t *testing.T) {
	var (
		mu   sync.Mutex
		body map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/batch/" {
			t.Errorf("path %s", r.URL.Path)
		}
		mu.Lock()
		defer mu.Unlock()
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
	}))
	defer srv.Close()
	prev := current.Load()
	defer current.Store(prev)

	ctx, cancel := context.WithCancel(context.Background())
	wait := Start(ctx, "phc_test", srv.URL, "1.0.0", nil)
	Emit(context.Background(), Event{Name: "api.called", UserID: "u1"})
	cancel()
	wait()
	mu.Lock()
	defer mu.Unlock()
	if body["api_key"] != "phc_test" {
		t.Fatalf("body %v", body)
	}
	batch := body["batch"].([]any)
	if len(batch) != 1 || batch[0].(map[string]any)["event"] != "api.called" {
		t.Fatalf("batch %v", batch)
	}
}

type sinkFunc func([]Event)

func (f sinkFunc) Send(_ context.Context, e []Event) { f(e) }

func TestSinksGetEventsWithoutPostHog(t *testing.T) {
	prev := current.Load()
	defer current.Store(prev)
	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	wait := Start(ctx, "", "", "1.0.0", nil, sinkFunc(func(e []Event) { got = append(got, e...) }))
	Emit(context.Background(), Event{Name: "api.called", UserID: "u1"})
	Emit(Mute(context.Background()), Event{Name: "customer_events.ingested"})
	cancel()
	wait()
	if len(got) != 1 || got[0].Name != "api.called" || got[0].Outcome != Success {
		t.Fatalf("sink got %+v", got)
	}
}

func TestMuteDropsEvents(t *testing.T) {
	got := Capture(t)
	ctx, release := Hold(Mute(context.Background()))
	Emit(ctx, Event{Name: "lead.updated"})
	release(true, Event{})
	Emit(Mute(context.Background()), Event{Name: "job.failed", Outcome: Error})
	if len(got()) != 0 {
		t.Fatalf("muted events were sent: %+v", got())
	}
}
