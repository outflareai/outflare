package api_test

import (
	"context"
	"testing"
	"time"
)

// salesOwnerFixture is a brand with one conversation (state new) for a person,
// two SDRs, two account managers, and the workspace owner as manager.
type salesOwnerFixture struct {
	owner          *client
	sdr1, sdr2     *client
	ae1, ae2       *client
	sdr1ID, sdr2ID string
	ae1ID, ae2ID   string
	person         string
	leadID         string
	path           string
	e              *env
	invite         func(email, role string) (*client, string)
	root           string
}

func newSalesOwnerFixture(t *testing.T) *salesOwnerFixture {
	t.Helper()
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	invite := func(email, role string) (*client, string) {
		t.Helper()
		inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": email, "role": "operator"})
		c := &client{t: t, h: e.h}
		c.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Example Rep", "password": "test-not-real-password"})
		id := c.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
		if role != "" {
			owner.must(200, "PUT", root+"/team/"+id, map[string]any{"role": role})
		}
		return c, id
	}
	f := &salesOwnerFixture{owner: owner, person: person["id"].(string), e: e, invite: invite, root: root}
	f.sdr1, f.sdr1ID = invite("sdr1@owner.example", "sdr")
	f.sdr2, f.sdr2ID = invite("sdr2@owner.example", "sdr")
	f.ae1, f.ae1ID = invite("ae1@owner.example", "ae")
	f.ae2, f.ae2ID = invite("ae2@owner.example", "ae")
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	lead := owner.must(201, "POST", root+"/bridge", map[string]any{"source": "test", "source_id": "owner", "record_id": person["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{{"direction": "incoming", "from": "ana@customer.example", "to": []string{"sender@northpeak.example"}, "cc": []string{}, "subject": "Example conversation", "body": "Hello", "message_id": "<owner@customer.example>", "provider_id": "501", "occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
	f.leadID = lead["id"].(string)
	f.path = root + "/leads/" + f.leadID
	return f
}

func (f *salesOwnerFixture) version(t *testing.T) any {
	t.Helper()
	_, v := f.row(t)
	return v
}

func (f *salesOwnerFixture) act(t *testing.T, c *client, status int, action, assignee string) map[string]any {
	t.Helper()
	return c.must(status, "POST", f.path+"/actions", map[string]any{"action": action, "assignee_id": assignee, "version": f.version(t)})
}

func (f *salesOwnerFixture) crmOwner(t *testing.T) string {
	t.Helper()
	var v string
	if err := f.e.pool.QueryRow(context.Background(), `SELECT COALESCE(attributes->>'owner','') FROM records WHERE id=$1`, f.person).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// makeActive is a fixture standing in for taking the conversation, which
// needs a connected mailbox.
func (f *salesOwnerFixture) makeActive(t *testing.T) {
	t.Helper()
	if _, err := f.e.pool.Exec(context.Background(), `UPDATE sales_leads SET state='active',version=version+1 WHERE id=$1`, f.leadID); err != nil {
		t.Fatal(err)
	}
}

func TestSalesAssignSetsPersonOwner(t *testing.T) {
	f := newSalesOwnerFixture(t)
	lead := f.act(t, f.owner, 200, "assign", f.sdr1ID)
	if lead["owner_id"] != f.sdr1ID || lead["state"] != "new" {
		t.Fatal("assign:", lead)
	}
	if got := f.crmOwner(t); got != f.sdr1ID {
		t.Fatalf("person's owner %q, want assignee", got)
	}
	var actor string
	var n int
	if err := f.e.pool.QueryRow(context.Background(), `SELECT count(*), COALESCE(max(actor_id::text),'') FROM attribute_changes WHERE record_id=$1 AND field_key='owner'`, f.person).Scan(&n, &actor); err != nil {
		t.Fatal(err)
	}
	me := f.owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"]
	var caller string
	if err := f.e.pool.QueryRow(context.Background(), `SELECT actor_id::text FROM users WHERE id=$1`, me).Scan(&caller); err != nil {
		t.Fatal(err)
	}
	if n != 1 || actor != caller {
		t.Fatalf("owner history: %d changes by %q, want 1 by the caller %q", n, actor, caller)
	}
	// Assigning someone else moves the Owner too.
	f.act(t, f.owner, 200, "assign", f.sdr2ID)
	if got := f.crmOwner(t); got != f.sdr2ID {
		t.Fatalf("owner after second assign %q", got)
	}
}

func TestSalesReleaseClearsOwnOwner(t *testing.T) {
	f := newSalesOwnerFixture(t)
	f.act(t, f.owner, 200, "assign", f.sdr1ID)
	lead := f.act(t, f.owner, 200, "release", "")
	if lead["owner_id"] != nil || f.crmOwner(t) != "" {
		t.Fatalf("release must clear both owners: %v, crm %q", lead["owner_id"], f.crmOwner(t))
	}
	var n int
	if err := f.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='owner'`, f.person).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("owner history has %d changes, want set and clear", n)
	}
	// When the person's Owner is someone else, release leaves it alone.
	f.act(t, f.owner, 200, "assign", f.sdr1ID)
	if _, err := f.e.pool.Exec(context.Background(), `UPDATE records SET attributes=jsonb_set(attributes,'{owner}',to_jsonb($2::text)) WHERE id=$1`, f.person, f.ae1ID); err != nil {
		t.Fatal(err)
	}
	f.act(t, f.owner, 200, "release", "")
	if got := f.crmOwner(t); got != f.ae1ID {
		t.Fatalf("release changed another person's Owner: %q", got)
	}
	// An active conversation cannot be released.
	f.act(t, f.owner, 200, "assign", f.sdr1ID)
	f.makeActive(t)
	f.act(t, f.owner, 403, "release", "")
}

func TestSalesReassignActive(t *testing.T) {
	f := newSalesOwnerFixture(t)
	f.act(t, f.owner, 200, "assign", f.sdr1ID)
	f.makeActive(t)
	f.act(t, f.owner, 200, "assign", f.ae1ID)
	// The introducing SDR still sees the conversation but no longer owns it.
	f.act(t, f.sdr1, 403, "assign", f.ae2ID)
	// A teammate with no part in it cannot even see it.
	f.act(t, f.sdr2, 404, "assign", f.ae2ID)
	lead := f.act(t, f.ae1, 200, "assign", f.ae2ID)
	if lead["owner_id"] != f.ae2ID || lead["ae_id"] != f.ae2ID || lead["state"] != "active" {
		t.Fatal("reassign:", lead)
	}
	if got := f.crmOwner(t); got != f.ae2ID {
		t.Fatalf("person's owner %q, want new owner", got)
	}
	// The previous owner is no longer the owner.
	f.act(t, f.ae1, 404, "assign", f.ae1ID)
}

func TestSalesReassignActiveManager(t *testing.T) {
	f := newSalesOwnerFixture(t)
	f.act(t, f.owner, 200, "assign", f.sdr1ID)
	f.makeActive(t)
	lead := f.act(t, f.owner, 200, "assign", f.ae1ID)
	if lead["state"] != "active" || lead["owner_id"] != f.ae1ID || lead["ae_id"] != f.ae1ID || lead["sdr_id"] != f.sdr1ID {
		t.Fatal("manager reassign:", lead)
	}
	if got := f.crmOwner(t); got != f.ae1ID {
		t.Fatalf("person's owner %q", got)
	}
	// A target must still be an SDR or account manager of the brand.
	f.act(t, f.owner, 404, "assign", "00000000-0000-4000-8000-000000000000")
}

// row reads the stored row, for checks that nothing was written.
func (f *salesOwnerFixture) row(t *testing.T) (owner string, version int) {
	t.Helper()
	if err := f.e.pool.QueryRow(context.Background(), `SELECT COALESCE(owner_id::text,''),version FROM sales_leads WHERE id=$1`, f.leadID).Scan(&owner, &version); err != nil {
		t.Fatal(err)
	}
	return
}

func (f *salesOwnerFixture) message(t *testing.T, status string) string {
	t.Helper()
	var id string
	if err := f.e.pool.QueryRow(context.Background(), `INSERT INTO sales_messages(id,workspace_id,brand_id,record_id,lead_id,sender_id,handoff_to,kind,direction,status,content_sealed,identity_hash,occurred_at)
		SELECT gen_random_uuid(),workspace_id,brand_id,record_id,id,owner_id,$2::uuid,'handoff','outgoing',$3,'\x00'::bytea,'test-in-flight',now() FROM sales_leads WHERE id=$1 RETURNING id::text`, f.leadID, f.ae2ID, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSalesReassignRefusedWhileSending(t *testing.T) {
	f := newSalesOwnerFixture(t)
	f.act(t, f.owner, 200, "assign", f.sdr1ID)
	f.makeActive(t)
	msg := f.message(t, "queued")
	_, v0 := f.row(t)
	resp := f.act(t, f.owner, 409, "assign", f.ae1ID)
	if resp["error"].(map[string]any)["code"] != "conflict" {
		t.Fatal("refusal:", resp)
	}
	if o, v := f.row(t); o != f.sdr1ID || v != v0 || f.crmOwner(t) != f.sdr1ID {
		t.Fatalf("a refused reassign changed something: owner %q version %d, person %q", o, v, f.crmOwner(t))
	}
	for _, status := range []string{"sending", "uncertain"} {
		if _, err := f.e.pool.Exec(context.Background(), `UPDATE sales_messages SET status=$2 WHERE id=$1`, msg, status); err != nil {
			t.Fatal(err)
		}
		f.act(t, f.owner, 409, "assign", f.ae1ID)
	}
	if _, err := f.e.pool.Exec(context.Background(), `UPDATE sales_messages SET status='accepted' WHERE id=$1`, msg); err != nil {
		t.Fatal(err)
	}
	lead := f.act(t, f.owner, 200, "assign", f.ae1ID)
	if lead["owner_id"] != f.ae1ID || f.crmOwner(t) != f.ae1ID {
		t.Fatal("reassign after acceptance:", lead)
	}
}

func TestSalesReassignSameOwnerIsNoop(t *testing.T) {
	f := newSalesOwnerFixture(t)
	f.act(t, f.owner, 200, "assign", f.ae1ID)
	f.makeActive(t)
	var events int
	count := func() {
		if err := f.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_events WHERE lead_id=$1`, f.leadID).Scan(&events); err != nil {
			t.Fatal(err)
		}
	}
	count()
	before := events
	_, v0 := f.row(t)
	lead := f.act(t, f.owner, 200, "assign", f.ae1ID)
	count()
	if _, v := f.row(t); v != v0 || events != before || lead["owner_id"] != f.ae1ID {
		t.Fatalf("same-owner assign wrote: version %d->%d, events %d->%d", v0, v, before, events)
	}
}

func TestSalesReassignByDeskManagerAndRoles(t *testing.T) {
	f := newSalesOwnerFixture(t)
	manager, _ := f.invite("desk@owner.example", "manager")
	_, plainID := f.invite("plain@owner.example", "")
	f.act(t, f.owner, 200, "assign", f.sdr1ID)
	f.makeActive(t)
	// A member with no Sales role cannot receive it.
	f.act(t, f.owner, 400, "assign", plainID)
	if o, _ := f.row(t); o != f.sdr1ID {
		t.Fatalf("refused assign changed the owner to %q", o)
	}
	// A manager of the Sales desk (not the workspace owner) can pass it on.
	lead := f.act(t, manager, 200, "assign", f.ae1ID)
	if lead["state"] != "active" || lead["owner_id"] != f.ae1ID || f.crmOwner(t) != f.ae1ID {
		t.Fatal("desk manager reassign:", lead)
	}
	// The SDR who introduced it now finds it under hand-offs.
	rows := f.sdr1.must(200, "GET", f.root+"/leads?queue=handoffs", nil)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != f.leadID {
		t.Fatalf("hand-offs queue: %v", rows)
	}
}
