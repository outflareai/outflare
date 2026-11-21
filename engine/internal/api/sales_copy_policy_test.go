package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSalesIntroductionCopyPolicy(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)
	user, email := me["id"].(string), me["email"].(string)
	owner.must(200, "PUT", root+"/team/"+user, map[string]any{"role": "sdr"})
	var connection string
	if err := e.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"grant": map[string]string{"email": email, "subject": "test-subject", "refresh_token": "test-not-real-refresh"}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO sales_mail_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, connection, ws, brand, user, e.box.Seal(raw, []byte(connection))); err != nil {
		t.Fatal(err)
	}
	invite := owner.must(201, "POST", base+"/invites", map[string]any{"email": "colleague@example.com", "role": "operator"})
	colleague := &client{t: t, h: e.h}
	colleague.must(200, "POST", "/v1/invites/accept", map[string]any{"token": invite["token"], "name": "Colleague", "password": "test-not-real-password"})
	colleagueID := colleague.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PUT", root+"/team/"+colleagueID, map[string]any{"role": "ae"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO sales_mail_connections(workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,'\x')`, ws, brand, colleagueID); err != nil {
		t.Fatal(err)
	}
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other", "handle": "other"})["id"].(string)
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	var sends atomic.Int32
	var expectedCopies atomic.Value
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/replies/501/conversation-thread":
			_, _ = io.WriteString(w, `{"data":{"current_reply":{"id":501,"sender_email_id":1,"from_email_address":"ana@customer.example","primary_to_email_address":"rep@send-acme.test","raw_message_id":"<root@customer.example>","folder":"Inbox"},"older_messages":[],"newer_messages":[]}}`)
		case "POST /api/replies/501/reply":
			var payload struct {
				CC []struct {
					Email string `json:"email_address"`
				} `json:"cc_emails"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			addresses := []string{}
			for _, cc := range payload.CC {
				addresses = append(addresses, strings.ToLower(cc.Email))
			}
			slices.Sort(addresses)
			if !slices.Equal(addresses, expectedCopies.Load().([]string)) {
				t.Errorf("introduction lost the assigned colleague: %+v", payload.CC)
			}
			sends.Add(1)
			_, _ = io.WriteString(w, `{"data":{"reply":{"id":502,"raw_message_id":"<intro@send-acme.test>","folder":"Sent"}}}`)
		default:
			t.Errorf("unexpected provider call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	e.integrations.Client.BaseURLs["emailbison"] = fake.URL
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"sales.send"}
	e.srv.RegisterJobs(q)

	for _, tc := range []struct {
		name, block, reason, cc string
		also, participant       string
		withdraw, allowed       bool
	}{
		{name: "connected colleague", block: "example.com", reason: "blocklist", allowed: true},
		{name: "unrelated colleague", block: "example.com", reason: "blocklist", cc: "colleague@example.com"},
		{name: "assigned colleague", block: "example.com", reason: "blocklist", cc: "colleague@example.com", participant: "connected", allowed: true},
		{name: "disconnected colleague", block: "example.com", reason: "blocklist", cc: "colleague@example.com", participant: "disconnected"},
		{name: "other brand colleague", block: "example.com", reason: "blocklist", cc: "colleague@example.com", participant: "other_brand"},
		{name: "disabled colleague", block: "example.com", reason: "blocklist", cc: "colleague@example.com", participant: "disabled"},
		{name: "removed role", block: "example.com", reason: "blocklist", cc: "colleague@example.com", participant: "no_role"},
		{name: "blocked prospect", block: "customer.example", reason: "blocklist"},
		// A provider's blocklist often lists staff by address as well as by domain.
		{name: "address blocklist", block: email, reason: "blocklist", allowed: true},
		{name: "address and domain blocklist", block: email, also: "example.com", reason: "blocklist", allowed: true},
		{name: "unrelated colleague address blocklist", block: "colleague@example.com", reason: "blocklist", cc: "colleague@example.com"},
		{name: "manual domain block", block: "example.com", reason: "manual"},
		{name: "complaint", block: email, reason: "complained"},
		{name: "withdrawn consent", block: "example.com", reason: "blocklist", withdraw: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// These are disposable fixtures; production suppressions are never removed.
			if _, err := e.pool.Exec(ctx, `DELETE FROM suppressions WHERE workspace_id=$1`, ws); err != nil {
				t.Fatal(err)
			}
			blocked := []string{tc.block}
			if tc.also != "" {
				blocked = append(blocked, tc.also)
			}
			owner.must(200, "POST", base+"/suppressions", map[string]any{"values": blocked, "reason": tc.reason, "all_brands": true})
			if tc.withdraw {
				rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", email, true, "")
				if err != nil {
					t.Fatal(err)
				}
				if err = customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Status: customers.Withdrawn}); err != nil {
					t.Fatal(err)
				}
			}
			cc := []string{}
			if tc.cc != "" {
				cc = append(cc, tc.cc)
			}
			lead := owner.must(201, "POST", root+"/bridge", map[string]any{"source": "test", "source_id": tc.name, "record_id": person["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{{"direction": "incoming", "from": "ana@customer.example", "to": []string{"rep@send-acme.test"}, "cc": cc, "subject": "Test conversation", "body": "Can we talk?", "message_id": "<root@customer.example>", "provider_id": "501", "occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
			path := root + "/leads/" + lead["id"].(string)
			owner.must(200, "POST", path+"/actions", map[string]any{"action": "take", "version": lead["version"]})
			// Model a previously assigned participant whose access changed after
			// the source conversation was captured. Check their current access.
			if _, err := e.pool.Exec(ctx, `UPDATE sales_mail_connections SET status='connected' WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, ws, brand, colleagueID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.pool.Exec(ctx, `UPDATE memberships SET brand_ids='{}' WHERE workspace_id=$1 AND user_id=$2`, ws, colleagueID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.pool.Exec(ctx, `UPDATE users SET disabled_at=NULL WHERE id=$1`, colleagueID); err != nil {
				t.Fatal(err)
			}
			owner.must(200, "PUT", root+"/team/"+colleagueID, map[string]any{"role": "ae"})
			if tc.participant != "" {
				if _, err := e.pool.Exec(ctx, `UPDATE sales_leads SET ae_id=$4 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, lead["id"], colleagueID); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.participant {
			case "disconnected":
				_, err := e.pool.Exec(ctx, `UPDATE sales_mail_connections SET status='reconnect_required' WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, ws, brand, colleagueID)
				if err != nil {
					t.Fatal(err)
				}
			case "other_brand":
				_, err := e.pool.Exec(ctx, `UPDATE memberships SET brand_ids=ARRAY[$3::uuid] WHERE workspace_id=$1 AND user_id=$2`, ws, colleagueID, otherBrand)
				if err != nil {
					t.Fatal(err)
				}
			case "disabled":
				_, err := e.pool.Exec(ctx, `UPDATE users SET disabled_at=now() WHERE id=$1`, colleagueID)
				if err != nil {
					t.Fatal(err)
				}
			case "no_role":
				owner.must(200, "PUT", root+"/team/"+colleagueID, map[string]any{"role": "none"})
			}
			before := sends.Load()
			copies := append([]string{email}, cc...)
			slices.Sort(copies)
			expectedCopies.Store(copies)
			worker, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); _ = q.Run(worker, 1) }()
			defer func() { cancel(); <-done }()
			want := "failed"
			if tc.allowed {
				want = "accepted"
			}
			deadline := time.Now().Add(15 * time.Second)
			for {
				var status string
				if err := e.pool.QueryRow(ctx, `SELECT status FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND kind='introduction'`, ws, brand, lead["id"]).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if slices.Contains([]string{"accepted", "failed", "uncertain"}, status) {
					if status != want {
						t.Fatalf("send status %s, want %s", status, want)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("send remained %s", status)
				}
				time.Sleep(20 * time.Millisecond)
			}
			expected := int32(0)
			if tc.allowed {
				expected = 1
			}
			if sends.Load()-before != expected {
				t.Fatal("blocked recipient reached the provider, or introduction was not sent")
			}
		})
	}
	if len(named(events(), "sales.send_failed")) != 10 {
		t.Fatal("blocked send events missing")
	}
}
