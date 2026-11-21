package track

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func TestLogEvents(t *testing.T) {
	events := Capture(t)
	log := slog.New(LogEvents(slog.NewJSONHandler(io.Discard, nil)))
	ctx := context.Background()

	log.Info("started")
	log.Error("sweep stale rows", "err", errors.New("ana@acme.example is locked"), "request_id", "req_1", "workspace_id", "ws_1")
	log.With("part", "sync").Warn("slow provider")
	// An event of its own already: not counted twice.
	log.Error("internal error", "err", errors.New("x"))
	Fail(ctx, log, Event{Name: "mailbox.sync_failed"}, "connector_unavailable", errors.New("x"))
	// Text with a value formatted into it keeps only where it was logged.
	log.Error("could not reach https://acme.example/hook")

	got := events()
	if len(got) != 4 {
		t.Fatalf("events %+v", got)
	}
	e := got[0]
	if e.Name != ErrorLogged || e.Outcome != Error || e.ErrorCode != "logged_error" || e.RequestID != "req_1" || e.WorkspaceID != "ws_1" ||
		e.Props["message"] != "sweep stale rows" || e.Props["where"] != "track.TestLogEvents" || e.Props["count"] != 1 {
		t.Fatalf("event %+v", e)
	}
	for k, v := range e.Props {
		if s, ok := v.(string); ok && s != "sweep stale rows" && s != "track.TestLogEvents" {
			t.Fatalf("property %s carries %q", k, s)
		}
	}
	if w := got[1]; w.Name != WarningLogged || w.Outcome != Failure || w.Props["message"] != "slow provider" {
		t.Fatalf("warning %+v", w)
	}
	if got[2].Name != "mailbox.sync_failed" {
		t.Fatalf("third %+v", got[2])
	}
	if _, ok := got[3].Props["message"]; ok || got[3].Props["where"] != "track.TestLogEvents" {
		t.Fatalf("fourth %+v", got[3])
	}
}

// The same line again and again is one event a minute, which says how many
// lines it stands for.
func TestLogEventsAreThrottled(t *testing.T) {
	events := Capture(t)
	h := LogEvents(slog.NewJSONHandler(io.Discard, nil)).(*logEvents)
	log := slog.New(h)
	for range 50 {
		log.Error("claim jobs")
	}
	if got := events(); len(got) != 1 {
		t.Fatalf("events %d", len(got))
	}
	h.seen.mu.Lock()
	for k := range h.seen.last {
		h.seen.last[k] = h.seen.last[k].Add(-2 * logEvery)
	}
	h.seen.mu.Unlock()
	log.Error("claim jobs")
	got := events()
	if len(got) != 2 || got[1].Props["count"] != 50 {
		t.Fatalf("events %+v", got)
	}
}
