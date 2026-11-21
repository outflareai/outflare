package outflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct {
	path, auth string
	body       struct {
		Source string           `json:"source"`
		Batch  []map[string]any `json:"batch"`
	}
}

func server(t *testing.T, answers ...int) (*httptest.Server, func() []call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{path: r.URL.Path, auth: r.Header.Get("Authorization")}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &c.body)
		mu.Lock()
		calls = append(calls, c)
		status := http.StatusAccepted
		if len(answers) > 0 {
			status, answers = answers[0], answers[1:]
		}
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []call {
		mu.Lock()
		defer mu.Unlock()
		return append([]call(nil), calls...)
	}
}

func tracker(t *testing.T, cfg Config) *Tracker {
	t.Helper()
	cfg.Token, cfg.Brand = "oft_test_not_a_real_token", "northpeak"
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = time.Hour
	}
	cfg.backoff = func(int, time.Duration) time.Duration { return 0 }
	tr, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Close)
	return tr
}

func TestQueuedBatchedAndSent(t *testing.T) {
	srv, calls := server(t)
	tr := tracker(t, Config{BaseURL: srv.URL + "/"})
	id, err := tr.Track(Track{UserID: "u1", Event: "Order Completed", Properties: map[string]any{"total": 42}})
	if err != nil {
		t.Fatal(err)
	}
	tr.Identify(Identify{UserID: "u1", Traits: map[string]any{"plan": "pro"}})
	tr.Group(Group{UserID: "u1", GroupID: "org_7"})
	tr.Page(Page{AnonymousID: "a1", Name: "Pricing"})
	tr.Screen(Page{AnonymousID: "a1", Name: "Home"})
	tr.Alias(Alias{UserID: "u1", PreviousID: "a1"})
	if len(calls()) != 0 {
		t.Fatal("sent before a flush")
	}
	tr.Flush(context.Background())
	got := calls()
	if len(got) != 1 || got[0].path != "/v1/brands/northpeak/events" || got[0].auth != "Bearer oft_test_not_a_real_token" || got[0].body.Source != "go" {
		t.Fatalf("call: %+v", got)
	}
	var kinds []string
	for _, m := range got[0].body.Batch {
		kinds = append(kinds, m["type"].(string))
	}
	if strings.Join(kinds, ",") != "track,identify,group,page,screen,alias" {
		t.Fatalf("kinds: %v", kinds)
	}
	first := got[0].body.Batch[0]
	if first["messageId"] != id || first["event"] != "Order Completed" || first["timestamp"] == "" ||
		first["context"].(map[string]any)["library"].(map[string]any)["name"] != "outflare-go" {
		t.Fatalf("first: %v", first)
	}
	if _, has := got[0].body.Batch[3]["userId"]; has {
		t.Fatal("an anonymous page carries no userId")
	}
}

func TestFlushAtAndClose(t *testing.T) {
	srv, calls := server(t)
	tr := tracker(t, Config{BaseURL: srv.URL, FlushAt: 2})
	tr.Track(Track{UserID: "u1", Event: "A"})
	tr.Track(Track{UserID: "u1", Event: "B"})
	deadline := time.Now().Add(2 * time.Second)
	for len(calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls(); len(got) != 1 || len(got[0].body.Batch) != 2 {
		t.Fatalf("flush at two: %+v", got)
	}
	tr.Track(Track{UserID: "u1", Event: "C"})
	tr.Close()
	if len(calls()) != 2 {
		t.Fatal("Close sends what waits")
	}
	if _, err := tr.Track(Track{UserID: "u1", Event: "D"}); err == nil {
		t.Fatal("a closed tracker takes no more")
	}
}

func TestRetriesAndRefusals(t *testing.T) {
	srv, calls := server(t, 503, 429, 202, 401)
	var lost []string
	tr := tracker(t, Config{BaseURL: srv.URL, OnError: func(err error, n int) { lost = append(lost, err.Error()) }})
	tr.Track(Track{UserID: "u1", Event: "A"})
	tr.Flush(context.Background())
	if len(calls()) != 3 || len(lost) != 0 {
		t.Fatalf("a busy server is retried: %d calls, lost %v", len(calls()), lost)
	}
	tr.Track(Track{UserID: "u1", Event: "B"})
	tr.Flush(context.Background())
	if len(calls()) != 4 || len(lost) != 1 || !strings.Contains(lost[0], "refused the batch (401)") {
		t.Fatalf("a refusal is not retried: %d calls, lost %v", len(calls()), lost)
	}
}

func TestWhatIsMissing(t *testing.T) {
	if _, err := New(Config{Brand: "b"}); err == nil {
		t.Fatal("no token")
	}
	srv, _ := server(t)
	tr := tracker(t, Config{BaseURL: srv.URL})
	if _, err := tr.Track(Track{Event: "A"}); err == nil {
		t.Fatal("no one")
	}
	if _, err := tr.Track(Track{UserID: "u1"}); err == nil {
		t.Fatal("no event")
	}
	if _, err := tr.Identify(Identify{AnonymousID: "a1"}); err == nil {
		t.Fatal("no user")
	}
}
