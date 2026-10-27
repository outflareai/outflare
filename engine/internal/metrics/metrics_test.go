package metrics_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

func scrape(t *testing.T, h http.Handler, auth string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestRequestsAndJobsAreCounted(t *testing.T) {
	metrics.SetVersion("test", "all")
	done := metrics.RequestStarted()
	done("GET /v1/whoami", 404, 30*time.Millisecond)
	metrics.RequestStarted()("", 200, time.Millisecond)
	metrics.JobStarted("test.kind", 2*time.Second)(metrics.JobRetried)
	metrics.AICall("claude", "reply_labelling", 1200, 40, 0, 0, 1)
	metrics.AIBudgetAlert(80, true)

	code, body := scrape(t, metrics.Handler(metrics.Options{}), "")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		`outflare_http_requests_total{code="4xx",route="GET /v1/whoami"} 1`,
		`outflare_http_requests_total{code="2xx",route="unmatched"} 1`,
		`outflare_http_request_duration_seconds_bucket{route="GET /v1/whoami",le="0.1"} 1`,
		`outflare_http_requests_in_flight 0`,
		`outflare_jobs_processed_total{kind="test.kind",outcome="retried"} 1`,
		`outflare_jobs_wait_seconds_bucket{kind="test.kind",le="5"} 1`,
		`outflare_build_info{role="all",version="test"} 1`,
		`outflare_ai_calls_total{provider="claude",task="reply_labelling"} 1`,
		`outflare_ai_tokens_total{kind="input",provider="claude",task="reply_labelling"} 1200`,
		`outflare_ai_cost_cents_total{provider="claude",task="reply_labelling"} 1`,
		`outflare_ai_budget_alerts_total{level="80",scope="tool"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
}

func TestTokenAndPprof(t *testing.T) {
	h := metrics.Handler(metrics.Options{Token: "of_metrics_test_not_real"})
	if code, _ := scrape(t, h, ""); code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", code)
	}
	if code, _ := scrape(t, h, "Bearer wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d, want 401", code)
	}
	if code, _ := scrape(t, h, "Bearer of_metrics_test_not_real"); code != 200 {
		t.Errorf("right token: status %d, want 200", code)
	}

	for _, o := range []struct {
		pprof bool
		want  int
	}{{false, 404}, {true, 200}} {
		rec := httptest.NewRecorder()
		metrics.Handler(metrics.Options{Pprof: o.pprof}).ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
		if rec.Code != o.want {
			t.Errorf("pprof %v: status %d, want %d", o.pprof, rec.Code, o.want)
		}
	}
}

func TestServeStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- metrics.Serve(ctx, "127.0.0.1:0", metrics.Options{}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not stop")
	}
	if err := metrics.Serve(context.Background(), "not an address", metrics.Options{}); err == nil {
		t.Fatal("bad address: no error")
	}
}

func TestPostgres(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `TRUNCATE jobs`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jobs (kind, run_at) VALUES
		('metrics.test', now() - interval '90 seconds'), ('metrics.test', now()), ('metrics.test', now() + interval '1 hour')`); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(&metrics.Postgres{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			name := f.GetName()
			for _, l := range m.GetLabel() {
				name += "|" + l.GetValue()
			}
			v := m.GetGauge().GetValue() + m.GetCounter().GetValue()
			got[name] = v
		}
	}
	if got["outflare_jobs_queued|metrics.test"] != 2 || got["outflare_jobs_scheduled|metrics.test"] != 1 {
		t.Errorf("queue depth: queued %v, scheduled %v; want 2 and 1", got["outflare_jobs_queued|metrics.test"], got["outflare_jobs_scheduled|metrics.test"])
	}
	if age := got["outflare_jobs_oldest_due_seconds|metrics.test"]; age < 89 || age > 200 {
		t.Errorf("oldest due job waited %vs, want about 90", age)
	}
	if got["outflare_metrics_scrape_errors"] != 0 {
		t.Errorf("%v scrape queries failed", got["outflare_metrics_scrape_errors"])
	}
	for _, name := range []string{"outflare_pg_database_bytes", "outflare_pg_max_connections", "outflare_db_pool_max_conns", "outflare_pg_table_bytes|jobs"} {
		if got[name] <= 0 {
			t.Errorf("%s = %v, want > 0", name, got[name])
		}
	}
}
