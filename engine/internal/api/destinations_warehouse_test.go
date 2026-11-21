package api_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/destinations"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// signedBy reports whether a JWT was signed by key, and its claims.
func signedBy(t *testing.T, jwt string, key *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("the request is not signed with the workspace's key: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims
}

func TestWarehouseDestinations(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand + "/destinations"

	// A key made for this test only.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	account, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "events@acme-analytics.example", "private_key": keyPEM})

	// Stand-ins for Google's sign-in, BigQuery and Snowflake.
	var mu sync.Mutex
	var tableMade, tableThere bool
	var bqRows []map[string]any
	var statements []map[string]any
	badRow := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			claims := signedBy(t, r.PostForm.Get("assertion"), &key.PublicKey)
			if claims["iss"] != "events@acme-analytics.example" || claims["aud"] != "https://oauth2.googleapis.com/token" {
				t.Errorf("Google sign-in claims: %v", claims)
			}
			w.Write([]byte(`{"access_token": "ya29.test-not-a-real-token", "expires_in": 3600}`))
		case strings.HasPrefix(r.URL.Path, "/bq/"):
			if r.Header.Get("Authorization") != "Bearer ya29.test-not-a-real-token" {
				w.WriteHeader(401)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch r.URL.Path {
			case "/bq/projects/acme-analytics/datasets/outflare/tables":
				if tableThere {
					w.WriteHeader(409)
					return
				}
				tableMade, tableThere = true, true
				w.Write([]byte(`{}`))
			case "/bq/projects/acme-analytics/datasets/outflare/tables/events/insertAll":
				if !tableThere {
					w.WriteHeader(404)
					return
				}
				var errs []map[string]any
				for i, row := range body["rows"].([]any) {
					row := row.(map[string]any)
					j := row["json"].(map[string]any)
					if j["name"] == badRow {
						errs = append(errs, map[string]any{"index": i, "errors": []map[string]any{{"reason": "invalid", "location": "properties", "message": "secret words"}}})
						continue
					}
					j["insertId"] = row["insertId"]
					bqRows = append(bqRows, j)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"insertErrors": errs})
			default:
				w.WriteHeader(404)
			}
		case r.URL.Path == "/api/v2/statements":
			claims := signedBy(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &key.PublicKey)
			pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
			print := sha256.Sum256(pub)
			if r.Header.Get("X-Snowflake-Authorization-Token-Type") != "KEYPAIR_JWT" || claims["sub"] != "ACME-PROD1.OUTFLARE" ||
				claims["iss"] != "ACME-PROD1.OUTFLARE.SHA256:"+base64.StdEncoding.EncodeToString(print[:]) {
				t.Errorf("Snowflake sign-in: %v", claims)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			statements = append(statements, body)
			w.Write([]byte(`{"code": "090001", "message": "Statement executed successfully."}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	// What can't be saved.
	bqConfig := map[string]any{"project": "acme-analytics", "dataset": "outflare", "table": "events"}
	sfConfig := map[string]any{"account": "acme-prod1.eu-central-1", "user": "outflare", "database": "analytics", "schema": "public", "table": "events", "warehouse": "compute_wh"}
	owner.must(400, "POST", base, map[string]any{"kind": "bigquery", "name": "BigQuery", "config": bqConfig})
	owner.must(400, "POST", base, map[string]any{"kind": "bigquery", "name": "BigQuery", "config": bqConfig, "secret": "not a key file"})
	owner.must(400, "POST", base, map[string]any{"kind": "bigquery", "name": "BigQuery", "config": map[string]any{"project": "acme-analytics", "dataset": "out flare", "table": "events"}, "secret": string(account)})
	owner.must(400, "POST", base, map[string]any{"kind": "snowflake", "name": "Snowflake", "config": sfConfig, "secret": pemShell("nope")})
	owner.must(400, "POST", base, map[string]any{"kind": "snowflake", "name": "Snowflake", "secret": keyPEM,
		"config": map[string]any{"account": "acme.example/x", "user": "outflare", "database": "analytics", "schema": "public", "table": "events", "warehouse": "compute_wh"}})
	owner.must(400, "POST", base, map[string]any{"kind": "snowflake", "name": "Snowflake", "secret": keyPEM,
		"config": map[string]any{"account": "acme-prod1", "user": "outflare", "database": "analytics", "schema": "public", "table": "events; DROP TABLE x", "warehouse": "compute_wh"}})

	bq := owner.must(201, "POST", base, map[string]any{"kind": "bigquery", "name": "BigQuery", "config": bqConfig, "secret": string(account)})
	sf := owner.must(201, "POST", base, map[string]any{"kind": "snowflake", "name": "Snowflake", "config": sfConfig, "secret": keyPEM, "enabled": false})
	if bq["has_secret"] != true || strings.Contains(mustJSON(bq), "PRIVATE KEY") || strings.Contains(mustJSON(sf), "PRIVATE KEY") {
		t.Fatalf("a key is kept and never returned: %v", bq)
	}
	if test := bq["test"].(map[string]any); test["running"] != false || test["ok"] != nil {
		t.Fatalf("no test yet: %v", test)
	}

	sender := &destinations.Sender{Pool: e.pool, Store: e.integrations, Box: e.box, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hooks: &webhooks.Service{Pool: e.pool, Box: e.box}, GoogleTokenURL: srv.URL + "/token", BigQueryURL: srv.URL + "/bq", SnowflakeURL: srv.URL}

	// A test event: asked for through the API, sent by the job. The
	// first one makes the table. Snowflake's is off and still testable.
	bqID, sfID := bq["id"].(string), sf["id"].(string)
	owner.must(202, "POST", base+"/"+bqID+"/test", nil)
	if one := owner.must(200, "GET", base+"/"+bqID, nil); one["test"].(map[string]any)["running"] != true {
		t.Fatalf("a test asked for is running: %v", one["test"])
	}
	if kind, ok, err := sender.Test(t.Context(), ws, bqID); err != nil || !ok || kind != "bigquery" {
		t.Fatalf("bigquery test: %s %v %v", kind, ok, err)
	}
	if test := owner.must(200, "GET", base+"/"+bqID, nil)["test"].(map[string]any); test["running"] != false || test["ok"] != true || !strings.Contains(test["reason"].(string), "outflare_test") {
		t.Fatalf("the test's result shows on the destination: %v", test)
	}
	if !tableMade || len(bqRows) != 1 || bqRows[0]["name"] != "outflare_test" || bqRows[0]["properties"] != `{"test":true}` {
		t.Fatalf("the test made the table and wrote one row: %v %v", tableMade, bqRows)
	}
	if kind, ok, err := sender.Test(t.Context(), ws, sfID); err != nil || !ok || kind != "snowflake" {
		t.Fatalf("snowflake test: %s %v %v", kind, ok, err)
	}
	if len(statements) != 2 || !strings.HasPrefix(statements[0]["statement"].(string), "CREATE TABLE IF NOT EXISTS EVENTS ") ||
		!strings.HasPrefix(statements[1]["statement"].(string), "MERGE INTO EVENTS ") || statements[1]["database"] != "ANALYTICS" ||
		statements[1]["schema"] != "PUBLIC" || statements[1]["warehouse"] != "COMPUTE_WH" || statements[1]["role"] != nil {
		t.Fatalf("snowflake statements: %v", statements)
	}
	var sfRows []map[string]any
	if err := json.Unmarshal([]byte(statements[1]["bindings"].(map[string]any)["1"].(map[string]any)["value"].(string)), &sfRows); err != nil ||
		len(sfRows) != 1 || sfRows[0]["name"] != "outflare_test" || sfRows[0]["properties"].(map[string]any)["test"] != true {
		t.Fatalf("snowflake rows: %v %v", sfRows, err)
	}
	// A test is not one of the brand's events.
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM customer_events WHERE workspace_id = $1) + (SELECT count(*) FROM event_destination_sends WHERE workspace_id = $1)`, ws).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a test stores no event: %d %v", n, err)
	}

	// Real events go as one batch; a row BigQuery refuses fails alone,
	// and its words are not kept.
	bqRows, badRow = nil, "signed_up"
	if _, err := e.pool.Exec(t.Context(), `UPDATE event_destinations SET matched_until = now() - interval '1 hour' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO customer_events (workspace_id, brand_id, anonymous_id, user_id, kind, name, raw_name, occurred_at, received_at, properties) VALUES
		($1, $2, 'anon-1', 'u1', 'track', 'order_completed', 'Order Completed', now() - interval '2 minutes', now() - interval '1 minute', '{"plan": "pro", "value": 49}'),
		($1, $2, 'anon-2', NULL, 'track', 'signed_up', 'Signed Up', now() - interval '2 minutes', now() - interval '1 minute', '{}')`, ws, brand); err != nil {
		t.Fatal(err)
	}
	if n, err := destinations.Match(t.Context(), e.pool, ""); err != nil || n != 2 {
		t.Fatalf("match (the one that is off takes none): %d %v", n, err)
	}
	res, err := sender.Send(t.Context(), ws, bqID)
	if err != nil || res.Sent != 1 || res.Failed != 1 || res.Unreachable {
		t.Fatalf("bigquery send: %+v %v", res, err)
	}
	if len(bqRows) != 1 || bqRows[0]["name"] != "order_completed" || bqRows[0]["user_id"] != "u1" || bqRows[0]["insertId"] != bqRows[0]["id"] {
		t.Fatalf("bigquery rows: %v", bqRows)
	}
	failed := owner.must(200, "GET", base+"/"+bqID+"/sends?status=failed", nil)["data"].([]any)
	if len(failed) != 1 || failed[0].(map[string]any)["reason"] != "BigQuery refused the row: invalid in properties" {
		t.Fatalf("a refused row says why without the tool's words: %v", failed)
	}

	// BigQuery down: the whole batch waits.
	if _, err := e.pool.Exec(t.Context(), `UPDATE event_destinations SET matched_until = now() - interval '1 hour' WHERE workspace_id = $1;`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO customer_events (workspace_id, brand_id, anonymous_id, kind, name, raw_name, occurred_at, received_at, properties) VALUES
		($1, $2, 'anon-3', 'track', 'plan_changed', 'Plan Changed', now() - interval '2 minutes', now() - interval '30 seconds', '{}'),
		($1, $2, 'anon-4', 'track', 'plan_changed', 'Plan Changed', now() - interval '2 minutes', now() - interval '30 seconds', '{}')`, ws, brand); err != nil {
		t.Fatal(err)
	}
	if _, err := destinations.Match(t.Context(), e.pool, ""); err != nil {
		t.Fatal(err)
	}
	sender.BigQueryURL = "http://127.0.0.1:1"
	res, err = sender.Send(t.Context(), ws, bqID)
	if err != nil || !res.Unreachable || res.Sent != 0 || res.Failed != 0 {
		t.Fatalf("down: %+v %v", res, err)
	}
	if one := owner.must(200, "GET", base+"/"+bqID, nil); one["queued"].(float64) != 2 || one["failing_since"] == nil {
		t.Fatalf("a warehouse that can't be reached keeps its events waiting: %v", one)
	}
	if kind, ok, err := sender.Test(t.Context(), ws, bqID); err != nil || ok || kind != "bigquery" {
		t.Fatalf("a test against a warehouse that is down fails: %v %v", ok, err)
	}
	if test := owner.must(200, "GET", base+"/"+bqID, nil)["test"].(map[string]any); test["ok"] != false || test["reason"] != "BigQuery could not be reached" {
		t.Fatalf("failed test: %v", test)
	}

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
		if strings.HasPrefix(ev.Name, "destination.") && (strings.Contains(mustJSON(ev.Props), "acme") || strings.Contains(mustJSON(ev.Props), "PRIVATE")) {
			t.Fatalf("an event must not carry the destination's account or key: %v", ev.Props)
		}
	}
	if !names["destination.test_requested"] {
		t.Fatalf("missing event destination.test_requested: %v", names)
	}
}

// pemShell wraps text that is not a key in a private key's first and
// last lines, to check that a paste shaped like a key but holding none is
// refused. Built here, so no line of this file looks like a key.
func pemShell(body string) string {
	const dashes = "-----"
	return dashes + "BEGIN PRIVATE KEY" + dashes + "\n" + body + "\n" + dashes + "END PRIVATE KEY" + dashes
}
