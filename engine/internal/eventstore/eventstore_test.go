package eventstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/blobs"
	"github.com/outflareai/outflare/engine/internal/db"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	return pool
}

func workspace(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ws string
	if err := pool.QueryRow(context.Background(), `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func addEvent(t *testing.T, pool *pgxpool.Pool, ws, name string, at time.Time) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, occurred_at)
		VALUES ($1, 'track', $2, 'anon-1', '{"plan": "pro"}', $3) RETURNING id::text`, ws, name, at).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// fakeClickHouse keeps what is inserted into events, and answers queries
// with nothing.
type fakeClickHouse struct {
	mu   sync.Mutex
	rows []map[string]any
	sql  []string
}

func (f *fakeClickHouse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query().Get("query")
	if strings.HasPrefix(q, "INSERT INTO events") {
		sc := bufio.NewScanner(r.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var row map[string]any
			json.Unmarshal(sc.Bytes(), &row) //nolint:errcheck
			f.rows = append(f.rows, row)
		}
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.sql = append(f.sql, q+string(b))
	io.WriteString(w, `{"data": []}`) //nolint:errcheck
}

func (f *fakeClickHouse) of(ws string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, r := range f.rows {
		if r["workspace_id"] == ws {
			out = append(out, r)
		}
	}
	return out
}

func TestMirror(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	fake := &fakeClickHouse{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ch, err := Open(srv.URL + "/outflare")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fake.sql, "\n"), "CREATE TABLE IF NOT EXISTS events") {
		t.Fatalf("schema: %v", fake.sql)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM event_mirror`) }) //nolint:errcheck
	ws := workspace(t, pool)
	at := time.Now().UTC().Add(-time.Hour)
	first := addEvent(t, pool, ws, "signup_started", at)
	addEvent(t, pool, ws, "order_completed", at.Add(time.Minute))

	m := &Mirror{Pool: pool, CH: ch, Lag: time.Nanosecond, Batch: 100000}
	drain := func() {
		t.Helper()
		for {
			more, err := m.Step(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				return
			}
		}
	}
	drain()
	rows := fake.of(ws)
	if len(rows) != 2 || rows[0]["name"] != "signup_started" || rows[0]["properties"] != `{"plan": "pro"}` || rows[0]["deleted"] != 0.0 {
		t.Fatalf("copied %v", rows)
	}
	// Nothing new: nothing copied again.
	drain()
	if n := len(fake.of(ws)); n != 2 {
		t.Fatalf("copied twice: %d", n)
	}

	// A change (a record linked by a merge) is copied again, newer.
	var rec string
	if err := pool.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ada') RETURNING id::text`, ws).Scan(&rec); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE customer_events SET record_id = $2 WHERE id = $1`, first, rec); err != nil {
		t.Fatal(err)
	}
	drain()
	rows = fake.of(ws)
	if len(rows) != 3 || rows[2]["id"] != first || rows[2]["record_id"] != rec || rows[2]["updated_at"].(string) <= rows[0]["updated_at"].(string) {
		t.Fatalf("after the change %v", rows)
	}

	// A deletion is a tombstone.
	if _, err := pool.Exec(ctx, `DELETE FROM customer_events WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	drain()
	rows = fake.of(ws)
	if last := rows[len(rows)-1]; last["id"] != first || last["deleted"] != 1.0 || last["name"] != "signup_started" {
		t.Fatalf("tombstone %v", last)
	}
	var waiting int
	pool.QueryRow(ctx, `SELECT count(*) FROM customer_event_deletions WHERE from_workspace_id = $1`, ws).Scan(&waiting) //nolint:errcheck
	if waiting != 0 {
		t.Fatalf("%d deletions left", waiting)
	}
	if s, ok, err := MirrorStatus(ctx, pool); err != nil || !ok || s.RowsCopied < 3 {
		t.Fatalf("status %+v %v %v", s, ok, err)
	}
}

func TestArchiver(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// The mirror is running (a row exists), so a plain deletion would leave
	// a tombstone; archiving must not.
	if _, err := pool.Exec(ctx, `INSERT INTO event_mirror (store, cursor_at) VALUES ('clickhouse', now() + interval '1 day')
		ON CONFLICT (store) DO UPDATE SET cursor_at = EXCLUDED.cursor_at`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM event_mirror`) }) //nolint:errcheck
	var (
		mu    sync.Mutex
		files = map[string][]byte{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			files[r.URL.Path] = b
		case "DELETE":
			delete(files, r.URL.Path)
		}
	}))
	defer srv.Close()
	bucket, err := blobs.New(blobs.Config{Endpoint: srv.URL, Bucket: "outflare", AccessKey: "test_not_a_real_key", SecretKey: "test-not-a-real-secret"})
	if err != nil {
		t.Fatal(err)
	}
	ws := workspace(t, pool)
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	old := addEvent(t, pool, ws, "signup_started", now.AddDate(0, -3, -5))
	addEvent(t, pool, ws, "order_completed", now.AddDate(0, -3, -4))
	addEvent(t, pool, ws, "order_completed", now.AddDate(0, -2, -3))
	keep := addEvent(t, pool, ws, "order_completed", now.AddDate(0, 0, -10))

	a := &Archiver{Pool: pool, Blobs: bucket, Days: 30, Chunk: 1}
	if err := a.Step(ctx, now); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, _ := pool.Query(ctx, `SELECT id::text FROM customer_events WHERE workspace_id = $1`, ws)
	for rows.Next() {
		var id string
		rows.Scan(&id) //nolint:errcheck
		left = append(left, id)
	}
	if len(left) != 1 || left[0] != keep {
		t.Fatalf("left %v", left)
	}
	var archived, events, tombstones int
	pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(events), 0) FROM event_archives WHERE from_workspace_id = $1`, ws).Scan(&archived, &events) //nolint:errcheck
	pool.QueryRow(ctx, `SELECT count(*) FROM customer_event_deletions WHERE from_workspace_id = $1`, ws).Scan(&tombstones)                        //nolint:errcheck
	if archived != 3 || events != 3 || tombstones != 0 {
		t.Fatalf("archives %d events %d tombstones %d", archived, events, tombstones)
	}
	mu.Lock()
	var found bool
	for path, b := range files {
		if !strings.Contains(path, "/outflare/events/"+ws+"/") {
			t.Fatalf("file at %s", path)
		}
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		line, _ := io.ReadAll(zr)
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if e["id"] == old {
			found = e["name"] == "signup_started" && e["properties"].(map[string]any)["plan"] == "pro"
		}
	}
	mu.Unlock()
	if !found {
		t.Fatal("the oldest event isn't in a file")
	}

	// A deleted workspace's files go too.
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if err := a.Step(ctx, now); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(files)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("%d files left after the workspace went", n)
	}
	pool.Exec(ctx, `DELETE FROM customer_event_deletions WHERE from_workspace_id = $1`, ws) //nolint:errcheck
}

// With CLICKHOUSE_TEST_URL, the schema and a copy run against a real
// ClickHouse.
func TestClickHouse(t *testing.T) {
	url := os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	pool := testPool(t)
	ctx := context.Background()
	ch, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := ch.Migrate(ctx); err != nil || n != 0 {
		t.Fatalf("second migrate %d %v", n, err)
	}
	ws := workspace(t, pool)
	id := addEvent(t, pool, ws, "signup_started", time.Now().Add(-time.Hour))
	addEvent(t, pool, ws, "order_completed", time.Now().Add(-time.Hour))
	m := &Mirror{Pool: pool, CH: ch, Lag: time.Nanosecond}
	for more := true; more; {
		if more, err = m.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM customer_events WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := ch.Query(ctx, `SELECT name, JSONExtractString(properties, 'plan') AS plan FROM events FINAL
		WHERE workspace_id = {ws:UUID} AND deleted = 0`, map[string]string{"ws": ws})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["name"] != "order_completed" || rows[0]["plan"] != "pro" {
		t.Fatalf("clickhouse has %v", rows)
	}
}
