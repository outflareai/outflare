package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

func TestListenForwardsSignedEvents(t *testing.T) {
	const event = `{"id":"evt_8","type":"lead.replied","created_at":"2026-10-25T10:00:00Z","data":{}}`
	api, got := fakeAPI(t, func(r *http.Request, n int) (int, string) {
		switch r.URL.Query().Get("after") {
		case "latest":
			return 200, `{"data":[],"next_after":"evt_7","has_more":false}`
		case "evt_7":
			return 200, `{"data":[` + event + `],"next_after":"evt_8","has_more":false}`
		}
		return 200, `{"data":[],"next_after":"evt_8","has_more":false}`
	})
	var mu sync.Mutex
	var forwarded []*http.Request
	var bodies []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		forwarded, bodies = append(forwarded, r), append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer recv.Close()

	secret := webhooks.NewSecret()
	var out bytes.Buffer
	errb := &lockedBuffer{}
	env := Env{Stdout: &out, Stderr: errb, Spec: spec.OpenAPI, Version: "test", ConfigDir: t.TempDir(), RetryBase: time.Millisecond,
		Getenv: func(k string) string {
			return map[string]string{"OUTFLARE_URL": api.URL, "OUTFLARE_TOKEN": "oft_test", "OUTFLARE_WORKSPACE": ws}[k]
		}}
	done := make(chan int)
	go func() {
		done <- Run(ctx, env, []string{"listen", "--forward-to", recv.URL + "/hooks", "--types", "lead.*", "--secret", secret, "--interval", "100ms"})
	}()
	// Stop once the receiver's answer has been reported. Match the whole
	// status, not "204" alone: the first line names the receiver's URL and
	// the secret, and a random port such as 42045 contains "204" too.
	go func() {
		for !strings.Contains(errb.String(), "204 No Content") {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listen did not forward the event")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(forwarded) != 1 || bodies[0] != event {
		t.Fatalf("forwarded %v", bodies)
	}
	h := forwarded[0].Header
	var unix int64
	for _, c := range h.Get("webhook-timestamp") {
		unix = unix*10 + int64(c-'0')
	}
	want, _ := webhooks.Sign(secret, "evt_8", time.Unix(unix, 0), []byte(event))
	if h.Get("webhook-id") != "evt_8" || h.Get("webhook-signature") != want {
		t.Fatalf("headers %v, want signature %s", h, want)
	}
	if q := (*got)[1].query; !strings.Contains(q, "types=lead.%2A") || !strings.Contains(q, "after=evt_7") {
		t.Fatalf("second poll %q", q)
	}
	if !strings.Contains(errb.String(), "204 No Content") || !strings.Contains(errb.String(), secret) {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestListenPrintsEvents(t *testing.T) {
	api, _ := fakeAPI(t, func(r *http.Request, n int) (int, string) {
		if r.URL.Query().Get("after") == "latest" {
			return 200, `{"data":[],"next_after":"evt_1","has_more":false}`
		}
		return 200, `{"data":[{"id":"evt_2","type":"lead.created"}],"next_after":"evt_2","has_more":false}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	env := Env{Stdout: out, Stderr: errb, Spec: spec.OpenAPI, Version: "test", ConfigDir: t.TempDir(),
		Getenv: func(k string) string {
			return map[string]string{"OUTFLARE_URL": api.URL, "OUTFLARE_TOKEN": "oft_test", "OUTFLARE_WORKSPACE": ws}[k]
		}}
	go func() {
		for out.String() == "" {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if code := Run(ctx, env, []string{"listen", "--interval", "100ms"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), `{"id":"evt_2","type":"lead.created"}`+"\n") {
		t.Fatalf("out %q, err %q", out.String(), errb.String())
	}
	if code := Run(context.Background(), env, []string{"listen", "--forward-to", "localhost:3000"}); code != 2 {
		t.Fatalf("a URL without a scheme should be refused, got %d", code)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestTokenFindsItsWorkspace(t *testing.T) {
	api, got := fakeAPI(t, func(r *http.Request, n int) (int, string) {
		if r.URL.Path == "/v1/whoami" {
			return 200, `{"token":{"workspace_id":"` + ws + `"}}`
		}
		return 200, `{"data":[]}`
	})
	var out bytes.Buffer
	errb := &lockedBuffer{}
	env := Env{Stdout: &out, Stderr: errb, Spec: spec.OpenAPI, Version: "test", ConfigDir: t.TempDir(),
		Getenv: func(k string) string {
			return map[string]string{"OUTFLARE_URL": api.URL, "OUTFLARE_TOKEN": "oft_test"}[k]
		}}
	if code := Run(context.Background(), env, []string{"list-mailboxes"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(*got) != 2 || (*got)[1].path != "/v1/workspaces/"+ws+"/mailboxes" {
		t.Fatalf("requests %v", *got)
	}
}

func TestLogsPrintsRecentThenFollows(t *testing.T) {
	api, reqs := fakeAPI(t, func(r *http.Request, n int) (int, string) {
		q := r.URL.Query()
		switch {
		case q.Get("after") == "latest":
			return 200, `{"data":[],"next_before":null,"next_after":"log_2","next_cursor":null}`
		case q.Get("after") == "log_2":
			return 200, `{"data":[{"id":"log_3","status":500}],"next_before":null,"next_after":"log_3","next_cursor":null}`
		case q.Get("after") != "":
			return 200, `{"data":[],"next_before":null,"next_after":"log_3","next_cursor":null}`
		}
		return 200, `{"data":[{"id":"log_2"},{"id":"log_1"}],"next_before":null,"next_after":null,"next_cursor":null}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	env := Env{Stdout: out, Stderr: errb, Spec: spec.OpenAPI, Version: "test", ConfigDir: t.TempDir(),
		Getenv: func(k string) string {
			return map[string]string{"OUTFLARE_URL": api.URL, "OUTFLARE_TOKEN": "oft_test", "OUTFLARE_WORKSPACE": ws}[k]
		}}
	go func() {
		for !strings.Contains(out.String(), "log_3") {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if code := Run(ctx, env, []string{"logs", "--follow", "--level", "error", "--interval", "100ms"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := `{"id":"log_1"}` + "\n" + `{"id":"log_2"}` + "\n" + `{"id":"log_3","status":500}` + "\n"
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("out %q, err %q", out.String(), errb.String())
	}
	if got := (*reqs)[0].query; !strings.Contains(got, "level=error") {
		t.Fatalf("level filter not sent: %q", got)
	}
	if code := Run(context.Background(), env, []string{"logs", "--lines", "9000"}); code != 2 {
		t.Fatalf("--lines 9000 should be refused, got %d", code)
	}
}
