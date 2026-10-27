package telemetry_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/telemetry"
)

func TestBucket(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 1: "1", 2: "2-5", 10: "6-10", 11: "11-25", 1000: "501-1000", 1001: "1001-5000", 100001: "100000+"} {
		if got := telemetry.Bucket(n); got != want {
			t.Errorf("Bucket(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.9", true}, {"0.10.0", "0.9.0", true}, {"v1.0.0", "v1.0.0", false},
		{"v0.1.0", "v0.2.0", false}, {"v1.2", "v1.1.5", true}, {"v0.2.0", "dev", false}, {"", "v0.1.0", false},
		{"v0.2.0-rc1", "v0.1.0", true},
	}
	for _, c := range cases {
		if got := telemetry.Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("OUTFLARE_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "")
	if r := telemetry.FromEnv(nil, nil, "v1"); r.EnvOff {
		t.Fatal("off by default")
	}
	t.Setenv("OUTFLARE_TELEMETRY", "off")
	if r := telemetry.FromEnv(nil, nil, "v1"); !r.EnvOff {
		t.Fatal("OUTFLARE_TELEMETRY=off")
	}
	t.Setenv("OUTFLARE_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "1")
	if r := telemetry.FromEnv(nil, nil, "v1"); !r.EnvOff {
		t.Fatal("DO_NOT_TRACK=1")
	}
	t.Setenv("TELEMETRY_URL", "https://stats.example.com/v1/ping")
	if r := telemetry.FromEnv(nil, nil, "v1"); r.URL != "https://stats.example.com/v1/ping" {
		t.Fatalf("url %q", r.URL)
	}
}

func TestSendNowStoresUpdateCheck(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE telemetry_state SET enabled = true, last_sent_at = NULL, latest_version = NULL, notices = '[]'`); err != nil {
		t.Fatal(err)
	}

	var got telemetry.Report
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		json.NewDecoder(r.Body).Decode(&got) //nolint:errcheck
		w.Write([]byte(`{"latest_version":"v0.3.0","notices":[
			{"id":"sec-1","severity":"critical","title":"Security fix in v0.3.0","url":"https://example.com/advisory"},
			{"id":"x","severity":"loud","title":"Odd severity","url":"javascript:alert(1)"},
			{"id":"empty","title":""}]}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	r := &telemetry.Reporter{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "v0.2.0", URL: srv.URL}

	if err := r.SendNow(ctx); err != nil {
		t.Fatal(err)
	}
	if hits != 1 || got.Schema != 1 || got.InstanceID == "" || got.Version != "v0.2.0" || got.Counts.Workspaces == "" {
		t.Fatalf("report: %d %+v", hits, got)
	}
	st, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.UpdateAvailable || st.LastSentAt == nil || len(st.Notices) != 2 {
		t.Fatalf("status: %+v", st)
	}
	if n := st.Notices[1]; n.Severity != "info" || n.URL != "" {
		t.Fatalf("untrusted notice kept as is: %+v", n)
	}

	// Off means nothing leaves the server.
	r.EnvOff = true
	if err := r.SendNow(ctx); err != nil || hits != 1 {
		t.Fatalf("sent while off: %v %d", err, hits)
	}
	r.EnvOff = false
	if err := r.SetEnabled(ctx, false, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatal(err)
	}
	if err := r.SendNow(ctx); err != nil || hits != 1 {
		t.Fatalf("sent while turned off: %v %d", err, hits)
	}
	if err := r.SetEnabled(ctx, true, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatal(err)
	}
}
