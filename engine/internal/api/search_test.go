package api_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSearch(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	ada := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada Lovelace", "brand_id": rp,
		"identities": []map[string]any{{"namespace": "email", "value": "ada@northpeak.example"}}})["id"].(string)
	owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak Analytics"})
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)
	owner.must(201, "POST", "/v1/workspaces/"+other+"/records", map[string]any{"type": "person", "name": "Ada Elsewhere"})

	// A reply linked to Ada, straight into the table as the inbox sync writes it.
	db, err := pgx.Connect(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var mb string
	if err := db.QueryRow(context.Background(), `INSERT INTO mailboxes (workspace_id, email, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port, imap_username, credentials, status)
		VALUES ($1, 'sam@acme.example', 'google', 'smtp.gmail.com', 587, 'sam@acme.example', 'imap.gmail.com', 993, 'sam@acme.example', '\x00', 'active') RETURNING id::text`, ws).Scan(&mb); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, subject, body, record_id, received_at, kind)
		VALUES ($1, $3, 0, nextval('inbound_external_uid_seq'), 'ada@northpeak.example', 'Re: your note', 'Thanks. Could you send the pricing for the team plan before Friday?', $2, now(), 'reply')`, ws, ada, mb); err != nil {
		t.Fatal(err)
	}

	hits := owner.must(200, "GET", base+"/search?q=ada", nil)["data"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["id"] != ada || hits[0].(map[string]any)["kind"] != "record" {
		t.Fatalf("ada: %v", hits)
	}
	// An email address, part of a domain, a word prefix in a reply's body.
	if hits := owner.must(200, "GET", base+"/search?q=ada@northpeak.example", nil)["data"].([]any); len(hits) != 1 || hits[0].(map[string]any)["id"] != ada {
		t.Fatalf("email: %v", hits)
	}
	if hits := owner.must(200, "GET", base+"/search?q=northpeak&kinds=record", nil)["data"].([]any); len(hits) != 2 {
		t.Fatalf("northpeak: %v", hits)
	}
	hits = owner.must(200, "GET", base+"/search?q=pric+team", nil)["data"].([]any)
	if len(hits) != 1 {
		t.Fatalf("pricing: %v", hits)
	}
	m := hits[0].(map[string]any)
	if m["kind"] != "message" || m["detail"] != "received" || m["record_id"] != ada || m["record_name"] != "Ada Lovelace" || m["snippet"] == "" {
		t.Fatalf("reply: %v", m)
	}
	// Nothing typed is tsquery syntax.
	if hits := owner.must(200, "GET", base+"/search?q=ada%27%29+%7C+%21%3A*", nil)["data"].([]any); len(hits) != 1 {
		t.Fatalf("syntax in q: %v", hits)
	}
	owner.must(400, "GET", base+"/search?q=%21%21", nil)

	// Scopes: a token with customers.read only finds records, and the record
	// names on what it can't see are not shown.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Records", "scopes": []string{"customers.read"}})
	records := &client{t: t, h: h, bearer: tok["token"].(string)}
	if hits := records.must(200, "GET", base+"/search?q=pricing", nil)["data"].([]any); len(hits) != 0 {
		t.Fatalf("reply without inbox.read: %v", hits)
	}
	if code, _ := records.do("GET", base+"/search?q=pricing&kinds=message", nil); code != 403 {
		t.Fatalf("messages only, without their scopes: %d", code)
	}
	// Brands: a token kept to another brand doesn't find Ada.
	tok = owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Geonode agent", "scopes": []string{"customers.read"}, "brand": "geonode"})
	gn := &client{t: t, h: h, bearer: tok["token"].(string)}
	if hits := gn.must(200, "GET", base+"/search?q=ada", nil)["data"].([]any); len(hits) != 0 {
		t.Fatalf("another brand's person: %v", hits)
	}
	// A company made without a brand belongs to the first brand, not Geonode.
	if hits := gn.must(200, "GET", base+"/search?q=northpeak", nil)["data"].([]any); len(hits) != 0 {
		t.Fatalf("the first brand's company: %v", hits)
	}
}
