package api_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// download fetches a file as the client: status, headers and body.
func (c *client) download(path string) (int, http.Header, []byte) {
	c.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

// Privacy export (plans/brands/README.md): everything a brand holds about
// one person, as a file built by a job, downloadable for 7 days, in the
// audit log, and never anything of another brand or any secret.
func TestPrivacyExport(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	acme := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	// The same person in two brands is two records, each with its own data.
	person := func(brand, userID, company string) string {
		t.Helper()
		id := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Lima", "brand_id": brand,
			"attributes": map[string]any{"first_name": "Ana", "job_title": "Head of " + company},
			"identities": []map[string]any{{"namespace": "user_id", "value": userID}, {"namespace": "email", "value": "ana@example.com"}}})["id"].(string)
		owner.must(201, "POST", base+"/records/"+id+"/consents", map[string]any{"channel": "email", "status": "granted"})
		owner.must(200, "PATCH", base+"/records/"+id, map[string]any{"attributes": map[string]any{"job_title": "Chief of " + company}})
		ev := owner.must(202, "POST", "/v1/brands/"+brand+"/events", map[string]any{"type": "page", "userId": userID, "name": "Pricing " + company,
			"messageId": "m-" + userID})["events"].([]any)[0].(map[string]any)["id"].(string)
		deadline := time.Now().Add(30 * time.Second)
		for owner.must(200, "GET", "/v1/brands/"+brand+"/events/"+ev, nil)["status"] == "pending" {
			if time.Now().After(deadline) {
				t.Fatal("event still pending")
			}
			time.Sleep(30 * time.Millisecond)
		}
		for _, q := range []string{
			`INSERT INTO deals (workspace_id, record_id, source, state, amount_cents, currency, attribution, name) VALUES ($1, $2, 'manual', 'won', 12000, 'USD', 'manual', $3)`,
			`INSERT INTO emails (workspace_id, record_id, brand_id, purpose, from_address, to_addresses, subject)
				VALUES ($1, $2, (SELECT brand_id FROM records WHERE id = $2), 'transactional', 'hello@example.com', ARRAY['ana@example.com'], $3)`,
		} {
			if _, err := e.pool.Exec(ctx, q, ws, id, "Welcome to "+company); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	ana := person(acme, "u_ana", "Acme")
	anaRP := person(rp, "u_ana_rp", "Repocket")
	// A row that points at her but is another brand's is never hers to see
	// from this brand.
	if _, err := e.pool.Exec(ctx, `INSERT INTO emails (workspace_id, record_id, brand_id, purpose, from_address, to_addresses, subject)
		VALUES ($1, $2, $3, 'transactional', 'hello@example.com', ARRAY['ana@example.com'], 'Repocket only')`, ws, ana, rp); err != nil {
		t.Fatal(err)
	}
	// An email to her with no brand (shared, from before brands owned
	// their data) is still hers.
	if _, err := e.pool.Exec(ctx, `INSERT INTO emails (workspace_id, record_id, brand_id, purpose, from_address, to_addresses, subject)
		VALUES ($1, $2, NULL, 'transactional', 'hello@example.com', ARRAY['ana@example.com'], 'Sent before brands')`, ws, ana); err != nil {
		t.Fatal(err)
	}

	exports := "/v1/brands/" + acme + "/privacy-exports"
	ask := "/v1/brands/" + acme + "/records/" + ana + "/privacy-exports"
	// Another brand's record is not found, from either side.
	owner.must(404, "POST", "/v1/brands/"+acme+"/records/"+anaRP+"/privacy-exports", nil)
	owner.must(404, "POST", "/v1/brands/"+rp+"/records/"+ana+"/privacy-exports", nil)
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket app", "scopes": []string{"customers.manage", "customers.read"}, "brand": "repocket"})
	app := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	app.must(404, "POST", ask, nil)
	app.must(404, "GET", exports, nil)
	reader := agentToken(t, owner, base, "Reader", "customers.read")
	reader.must(403, "POST", ask, nil)

	x := owner.must(202, "POST", ask, nil)
	id := x["id"].(string)
	if x["status"] != "pending" || x["record_id"] != ana || x["download_url"] != nil {
		t.Fatalf("asked: %v", x)
	}
	ready := func(path string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			v := owner.must(200, "GET", path, nil)
			if v["status"] != "pending" {
				return v
			}
			if time.Now().After(deadline) {
				t.Fatalf("export still pending: %v", v)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	x = ready(exports + "/" + id)
	if x["status"] != "ready" || x["download_url"] == nil || x["expires_at"] == nil || x["size_bytes"] == nil {
		t.Fatalf("ready: %v", x)
	}
	created, _ := time.Parse(time.RFC3339Nano, x["finished_at"].(string))
	expires, _ := time.Parse(time.RFC3339Nano, x["expires_at"].(string))
	if d := expires.Sub(created); d < 167*time.Hour || d > 169*time.Hour {
		t.Fatalf("expires after %v", d)
	}
	counts := x["counts"].(map[string]any)
	for _, table := range []string{"customer_events", "consents", "emails", "deals", "record_identities", "attribute_changes", "event_inbox"} {
		if n, _ := counts[table].(float64); n < 1 {
			t.Fatalf("%s: %v rows; counts %v", table, counts[table], counts)
		}
	}
	// Her Acme email and her brandless one; not the Repocket one.
	if counts["emails"] != 2.0 || counts["deals"] != 1.0 {
		t.Fatalf("only this brand's rows: %v", counts)
	}
	link := strings.TrimPrefix(x["download_url"].(string), "http://localhost:8080")
	if !strings.HasSuffix(link, "/privacy-exports/"+id+"/download") {
		t.Fatalf("download_url: %s", link)
	}

	// Reading needs customers.read; the file itself customers.manage.
	reader.must(200, "GET", exports+"/"+id, nil)
	if code, _, _ := reader.download(exports + "/" + id + "/download"); code != 403 {
		t.Fatalf("a reader downloaded it: %d", code)
	}
	if code, _, _ := app.download(exports + "/" + id + "/download"); code != 404 {
		t.Fatalf("another brand's token downloaded it: %d", code)
	}
	code, hdr, body := owner.download(link)
	if code != 200 || hdr.Get("Content-Type") != "application/gzip" || !strings.Contains(hdr.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %v", code, hdr)
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		ExportedAt string                      `json:"exported_at"`
		Brand      map[string]string           `json:"brand"`
		Record     map[string]any              `json:"record"`
		Tables     map[string][]map[string]any `json:"tables"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("not JSON: %v: %.200s", err, raw)
	}
	if file.Brand["id"] != acme || file.Record["id"] != ana || file.ExportedAt == "" ||
		file.Record["attributes"].(map[string]any)["job_title"] != "Chief of Acme" {
		t.Fatalf("head: %v %v", file.Brand, file.Record)
	}
	text := string(raw)
	for _, want := range []string{"u_ana", "Pricing Acme", "Welcome to Acme", "Head of Acme"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the file lacks %q", want)
		}
	}
	// Nothing of the other brand's Ana, nor the row of the other brand.
	for _, never := range []string{"Repocket", "u_ana_rp", anaRP, rp} {
		if strings.Contains(text, never) {
			t.Fatalf("the file has %q, which is another brand's", never)
		}
	}
	for table, rows := range file.Tables {
		for _, row := range rows {
			if b, ok := row["brand_id"]; ok && b != acme && b != nil {
				t.Fatalf("%s row of brand %v", table, b)
			}
			if w, ok := row["workspace_id"]; ok && w != ws {
				t.Fatalf("%s row of workspace %v", table, w)
			}
		}
	}
	// No secret-like column in any row (emails.token_id is one).
	secret := regexp.MustCompile(`(?i)(token|secret|password|hash|sealed|credential)`)
	for table, rows := range file.Tables {
		for _, row := range rows {
			for k := range row {
				if secret.MatchString(k) {
					t.Fatalf("%s.%s is in the file", table, k)
				}
			}
		}
	}
	subjects := []string{}
	for _, row := range file.Tables["emails"] {
		subjects = append(subjects, row["subject"].(string))
	}
	slices.Sort(subjects)
	if !slices.Equal(subjects, []string{"Sent before brands", "Welcome to Acme"}) {
		t.Fatalf("emails: %v", subjects)
	}
	if x := owner.must(200, "GET", exports+"/"+id, nil); x["downloads"] != 1.0 {
		t.Fatalf("downloads: %v", x["downloads"])
	}

	// The list shows the brand's exports, and one record's.
	if l := owner.must(200, "GET", exports+"?record="+ana, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}
	if l := owner.must(200, "GET", "/v1/brands/"+rp+"/privacy-exports", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another brand's list: %v", l)
	}
	owner.must(404, "GET", "/v1/brands/"+rp+"/privacy-exports/"+id, nil)
	owner.must(400, "GET", exports+"?status=lost", nil)

	// Past its 7 days the file is gone: 410 at once, and the sweep deletes it.
	if _, err := e.pool.Exec(ctx, `UPDATE privacy_exports SET expires_at = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := owner.download(link); code != 410 {
		t.Fatalf("expired download: %d", code)
	}
	if n, err := e.srv.ExpirePrivacyExports(ctx); err != nil || n != 1 {
		t.Fatalf("expired %d: %v", n, err)
	}
	var kept bool
	if err := e.pool.QueryRow(ctx, `SELECT content IS NOT NULL FROM privacy_exports WHERE id = $1`, id).Scan(&kept); err != nil || kept {
		t.Fatalf("file kept after expiry: %v %v", kept, err)
	}
	if x := owner.must(200, "GET", exports+"/"+id, nil); x["status"] != "expired" || x["download_url"] != nil {
		t.Fatalf("expired: %v", x)
	}
	if code, _, _ := owner.download(link); code != 410 {
		t.Fatalf("expired download: %d", code)
	}

	// Over the size limit it fails with too_large.
	restore := api.SetPrivacyExportLimit(500)
	big := owner.must(202, "POST", ask, nil)["id"].(string)
	x = ready(exports + "/" + big)
	restore()
	if x["status"] != "failed" || x["error_code"] != "too_large" || x["download_url"] != nil {
		t.Fatalf("too large: %v", x)
	}
	if code, _, _ := owner.download(exports + "/" + big + "/download"); code != 409 {
		t.Fatalf("failed download: %d", code)
	}

	// Erasing her deletes her exports.
	last := owner.must(202, "POST", ask, nil)["id"].(string)
	ready(exports + "/" + last)
	rec := owner.must(200, "GET", base+"/records/"+ana, nil)
	out := owner.must(200, "DELETE", base+"/records/"+ana+"?version="+strconv.Itoa(int(rec["version"].(float64))), nil)
	if out["erased"].(map[string]any)["privacy_exports"] != 3.0 {
		t.Fatalf("erased: %v", out["erased"])
	}
	owner.must(404, "GET", exports+"/"+last, nil)
	owner.must(404, "GET", exports+"/"+id, nil)

	events := got()
	if len(named(events, "privacy_export.requested")) != 3 || len(named(events, "privacy_export.ready")) != 2 ||
		len(named(events, "privacy_export.failed")) != 1 || len(named(events, "privacy_export.downloaded")) != 1 {
		t.Fatalf("events: %d requested, %d ready, %d failed, %d downloaded", len(named(events, "privacy_export.requested")),
			len(named(events, "privacy_export.ready")), len(named(events, "privacy_export.failed")), len(named(events, "privacy_export.downloaded")))
	}
	var audited int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'privacy_export.downloaded'
		AND target_id = $2 AND brand_id = $3`, ws, id, acme).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("download audit: %d %v", audited, err)
	}
	var hooks int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'privacy_export.ready'`, ws).Scan(&hooks); err != nil || hooks != 2 {
		t.Fatalf("ready audit: %d %v", hooks, err)
	}
}
