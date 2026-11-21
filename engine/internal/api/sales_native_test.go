package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// nativeBison is a fake EmailBison whose replies folder and threads a test
// sets. Everything else is fakeEmailBison: one campaign (id 10) and the
// sender emails 1 to 3.
type nativeBison struct {
	sync.Mutex
	replies []map[string]any
	threads map[string][]map[string]any
	// broken makes reading a thread fail, as a sequencer that refuses does.
	broken bool
	reads  int
}

func (f *nativeBison) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/replies") {
		fakeEmailBison{}.ServeHTTP(w, r)
		return
	}
	f.Lock()
	defer f.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/replies/"), "/conversation-thread"); ok {
		f.reads++
		thread := f.threads[id]
		if f.broken || len(thread) == 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"message":"no such reply"}`)
			return
		}
		var current map[string]any
		older := []map[string]any{}
		for _, m := range thread {
			if fmt.Sprint(m["id"]) == id {
				current = m
			} else {
				older = append(older, m)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"current_reply": current, "older_messages": older, "newer_messages": []any{}}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": f.replies, "meta": map[string]any{"current_page": 1, "last_page": 1}})
}

// reply adds a reply to the folder, with a thread of the campaign email it
// answers and the reply itself.
func (f *nativeBison) reply(id int, from, text string, more map[string]any) {
	f.Lock()
	defer f.Unlock()
	now := time.Now().UTC()
	m := map[string]any{"id": id, "sender_email_id": 1, "campaign_id": 10, "from_email_address": from, "primary_to_email_address": "ana@send-acme.test",
		"subject": "Re: Quick question", "text_body": text, "raw_message_id": fmt.Sprintf("<reply-%d@customer.example>", id),
		"date_received": now.Add(-time.Duration(1000-id) * time.Second).Format(time.RFC3339), "folder": "Inbox"}
	for k, v := range more {
		m[k] = v
	}
	f.replies = append(f.replies, m)
	sent := map[string]any{"id": id + 1000, "sender_email_id": 1, "campaign_id": 10, "from_email_address": "ana@send-acme.test", "primary_to_email_address": from,
		"subject": "Quick question", "text_body": "Would a call next week suit?", "raw_message_id": fmt.Sprintf("<sent-%s>", from),
		"date_received": now.Add(-24 * time.Hour).Format(time.RFC3339), "folder": "Sent"}
	if f.threads == nil {
		f.threads = map[string][]map[string]any{}
	}
	f.threads[fmt.Sprint(id)] = []map[string]any{sent, m}
}

// nativeSales is a workspace with a connected fake EmailBison, its campaign
// linked to a sequence, and a worker for the conversations of its replies.
type nativeSales struct {
	t           *testing.T
	e           *env
	owner       *client
	ws, base    string
	brand, root string
	integration string
	bison       *nativeBison
}

func newNativeSales(t *testing.T) *nativeSales {
	t.Helper()
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	n := &nativeSales{t: t, e: e, owner: owner, ws: ws, base: "/v1/workspaces/" + ws, bison: &nativeBison{}}
	fake := httptest.NewServer(n.bison)
	t.Cleanup(fake.Close)
	e.integrations.Client.BaseURLs["emailbison"] = fake.URL
	n.integration = owner.must(201, "POST", n.base+"/integrations", map[string]any{"connector": "emailbison",
		"credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-key"}})["id"].(string)
	// The first read finds the campaign and its mailboxes.
	n.sync()
	if err := e.pool.QueryRow(context.Background(), `SELECT s.brand_id::text FROM campaign_links l JOIN sequences s ON s.id = l.sequence_id
		WHERE l.workspace_id = $1 AND l.integration_id = $2 AND l.external_id = '10'`, ws, n.integration).Scan(&n.brand); err != nil {
		t.Fatalf("the campaign's sequence: %v", err)
	}
	n.root = "/v1/brands/" + n.brand + "/sales"
	user := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PUT", n.root+"/team/"+user, map[string]any{"role": "ae"})

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"sales.reply_import"}
	e.srv.RegisterJobs(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = q.Run(ctx, 1) }()
	t.Cleanup(func() { cancel(); <-done })
	return n
}

func (n *nativeSales) sync() {
	n.t.Helper()
	n.owner.must(200, "POST", n.base+"/platforms/"+n.integration+"/sync", nil)
}

// person makes a person with that address; pushed says Outflare added them
// to the campaign, as the enroll step records it.
func (n *nativeSales) person(name, email, brand string, pushed bool) string {
	n.t.Helper()
	in := map[string]any{"type": "person", "name": name, "identities": []map[string]any{{"namespace": "email", "value": email}}}
	if brand != "" {
		in["brand_id"] = brand
	}
	id := n.owner.must(201, "POST", n.base+"/records", in)["id"].(string)
	if pushed {
		n.pushed(n.ws, id)
	}
	return id
}

func (n *nativeSales) pushed(ws, record string) {
	n.t.Helper()
	if err := audit.Record(context.Background(), n.e.pool, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "sequence.lead_pushed",
		TargetType: "sequence", Data: map[string]any{"record_id": record, "integration_id": n.integration, "connector": "emailbison",
			"campaign_id": "10", "external_id": "77", "via": "pipeline"}}); err != nil {
		n.t.Fatal(err)
	}
}

// settle waits until every conversation job has finished or is waiting
// to be tried again.
func (n *nativeSales) settle() {
	n.t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		var busy int
		if err := n.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = 'sales.reply_import'
			AND (status = 'running' OR status = 'queued' AND attempts = 0)`).Scan(&busy); err != nil {
			n.t.Fatal(err)
		}
		if busy == 0 {
			return
		}
		if time.Now().After(deadline) {
			n.t.Fatal("conversation jobs did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (n *nativeSales) count(sql string, args ...any) int {
	n.t.Helper()
	var c int
	if err := n.e.pool.QueryRow(context.Background(), sql, args...).Scan(&c); err != nil {
		n.t.Fatal(err)
	}
	return c
}

func TestSalesConversationsFromRepliesOutflareReads(t *testing.T) {
	events := track.Capture(t)
	n := newNativeSales(t)
	ctx := context.Background()
	other := n.owner.must(201, "POST", n.base+"/brands", map[string]any{"name": "Other Brand", "handle": "other"})["id"].(string)
	ana := n.person("Ana Example", "ana@customer.example", "", true)
	bo := n.person("Bo Example", "bo@customer.example", "", false) // someone else put Bo in the campaign
	n.person("Cy Example", "cy@customer.example", "", true)
	n.person("Di Example", "di@customer.example", "", true)
	n.person("Eve Example", "eve@customer.example", "", true)
	fay := n.person("Fay Example", "fay@customer.example", "other", true) // pushed, but a person of another brand
	// Another workspace knows Bo and says it pushed him on this connection:
	// nothing of that may count here, and nothing may be written there.
	second := n.owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Second"})["id"].(string)
	bo2 := n.owner.must(201, "POST", "/v1/workspaces/"+second+"/records", map[string]any{"type": "person", "name": "Bo Example",
		"identities": []map[string]any{{"namespace": "email", "value": "bo@customer.example"}}})["id"].(string)
	n.pushed(second, bo2)

	n.bison.reply(601, "ana@customer.example", "Sounds good, can we talk Tuesday?", nil)
	n.bison.reply(602, "bo@customer.example", "I'm interested, tell me more.", nil)
	n.bison.reply(603, "stranger@nobody.example", "Sounds great, let's chat.", nil)
	n.bison.reply(604, "cy@customer.example", "Not interested, no thanks.", nil)
	n.bison.reply(605, "di@customer.example", "I am out of the office until Monday.", map[string]any{"automated_reply": true, "subject": "Out of office"})
	n.bison.reply(606, "eve@customer.example", "Please remove me from your list.", nil)
	n.bison.reply(607, "fay@customer.example", "Sounds good, happy to chat.", nil)
	n.sync()
	n.settle()

	labels := map[string]string{}
	for _, m := range n.owner.must(200, "GET", n.base+"/inbox?source=emailbison&kind=all", nil)["data"].([]any) {
		m := m.(map[string]any)
		labels[m["from"].(string)], _ = m["category"].(string)
	}
	want := map[string]string{"ana@customer.example": "interested", "bo@customer.example": "interested", "stranger@nobody.example": "interested",
		"cy@customer.example": "not_interested", "di@customer.example": "out_of_office", "eve@customer.example": "unsubscribe", "fay@customer.example": "interested"}
	for from, label := range want {
		if labels[from] != label {
			t.Fatalf("%s is labelled %q, want %q (all: %v)", from, labels[from], label, labels)
		}
	}
	// Only Ana: hot, a person, and pushed by Outflare in her brand.
	if c := n.count(`SELECT count(*) FROM sales_leads`); c != 1 {
		t.Fatalf("%d conversations, want Ana's only", c)
	}
	if c := n.count(`SELECT count(*) FROM jobs WHERE kind = 'sales.reply_import'`); c != 1 {
		t.Fatalf("%d conversation jobs, want one", c)
	}
	var id, record, brand, source, sourceID, state string
	var owner *string
	if err := n.e.pool.QueryRow(ctx, `SELECT id::text, record_id::text, brand_id::text, source, source_id, state, owner_id::text FROM sales_leads
		WHERE workspace_id = $1`, n.ws).Scan(&id, &record, &brand, &source, &sourceID, &state, &owner); err != nil {
		t.Fatal(err)
	}
	if record != ana || brand != n.brand || source != "outflare" || sourceID != n.integration+":601" || state != "new" || owner != nil {
		t.Fatalf("conversation: record %s brand %s source %s/%s state %s owner %v", record, brand, source, sourceID, state, owner)
	}
	// It points at the person, with the thread the sequencer holds; the
	// person's Owner waits for whoever takes it.
	detail := n.owner.must(200, "GET", n.root+"/leads/"+id, nil)
	messages := detail["messages"].([]any)
	if lead := detail["lead"].(map[string]any); lead["record_id"] != ana || lead["name"] != "Ana Example" || len(messages) != 2 {
		t.Fatalf("conversation: %v", detail)
	}
	first, last := messages[0].(map[string]any), messages[1].(map[string]any)
	if first["direction"] != "outgoing" || last["direction"] != "incoming" || last["body"] != "Sounds good, can we talk Tuesday?" || last["from"] != "ana@customer.example" {
		t.Fatalf("thread: %v", messages)
	}
	if c := n.count(`SELECT count(*) FROM records WHERE id = $1 AND attributes ? 'owner'`, ana); c != 0 {
		t.Fatal("a conversation arriving must not set the person's Owner")
	}
	// The person's own history and the audit log have it, like an import.
	if c := n.count(`SELECT count(*) FROM attribute_changes WHERE workspace_id = $1 AND record_id = $2 AND field_key = 'sales_workflow'`, n.ws, ana); c != 3 {
		t.Fatalf("%d history entries on the person, want the arrival and two messages", c)
	}
	if c := n.count(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'sales.lead_arrived' AND target_id = $2`, n.ws, id); c != 1 {
		t.Fatalf("sales.lead_arrived recorded %d times", c)
	}
	arrived := named(events(), "sales.lead_arrived")
	if len(arrived) != 1 || arrived[0].Surface != "job" || arrived[0].WorkspaceID != n.ws || arrived[0].Outcome != track.Success {
		t.Fatalf("sales.lead_arrived events: %+v", arrived)
	}
	for _, r := range []string{bo, fay} {
		if c := n.count(`SELECT count(*) FROM sales_leads WHERE record_id = $1`, r); c != 0 {
			t.Fatal("a person Outflare did not push in this brand got a conversation")
		}
	}
	if c := n.count(`SELECT count(*) FROM sales_leads WHERE workspace_id = $1 OR brand_id = $2`, second, other); c != 0 {
		t.Fatal("a conversation crossed into another workspace or brand")
	}
	n.owner.must(404, "GET", "/v1/brands/"+other+"/sales/leads/"+id, nil)

	// Running the job again adds nothing.
	if _, err := n.e.pool.Exec(ctx, `UPDATE jobs SET status = 'queued', attempts = 0, run_at = now() WHERE kind = 'sales.reply_import'`); err != nil {
		t.Fatal(err)
	}
	n.settle()
	if c := n.count(`SELECT count(*) FROM sales_leads`); c != 1 {
		t.Fatalf("a retried job made %d conversations", c)
	}
	if c := n.count(`SELECT count(*) FROM sales_messages WHERE lead_id = $1`, id); c != 2 {
		t.Fatalf("a retried job left %d messages", c)
	}
	if c := n.count(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action IN ('sales.lead_arrived', 'sales.message_received')`, n.ws); c != 3 {
		t.Fatalf("a retried job recorded again: %d actions", c)
	}

	// Ana's next hot reply joins her conversation.
	n.bison.reply(608, "ana@customer.example", "Happy to chat, when are you free?", nil)
	n.bison.Lock()
	n.bison.threads["608"] = append(n.bison.threads["601"], n.bison.replies[len(n.bison.replies)-1])
	n.bison.Unlock()
	n.sync()
	n.settle()
	if c := n.count(`SELECT count(*) FROM sales_leads`); c != 1 {
		t.Fatalf("a second reply made %d conversations", c)
	}
	detail = n.owner.must(200, "GET", n.root+"/leads/"+id, nil)
	if got := detail["messages"].([]any); len(got) != 3 || got[2].(map[string]any)["body"] != "Happy to chat, when are you free?" {
		t.Fatalf("second reply: %v", detail["messages"])
	}
	if lead := detail["lead"].(map[string]any); lead["reply_state"] != "reply_received" || lead["state"] != "new" {
		t.Fatalf("second reply: %v", detail)
	}
	if c := n.count(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'sales.lead_arrived'`, n.ws); c != 1 {
		t.Fatal("a second reply arrived as a new conversation")
	}

	// An outside importer sending the same reply afterwards gets that
	// conversation, not a second one.
	bridged := n.owner.must(201, "POST", n.root+"/bridge", map[string]any{"source": "portal", "source_id": "portal-lead-1", "record_id": ana,
		"integration_id": n.integration, "sender_id": "1", "reply_id": "601", "messages": []map[string]any{{"direction": "incoming",
			"from": "ana@customer.example", "to": []string{"ana@send-acme.test"}, "cc": []string{}, "subject": "Re: Quick question",
			"body": "Sounds good, can we talk Tuesday?", "message_id": "<reply-601@customer.example>", "provider_id": "601",
			"occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
	if bridged["id"] != id || bridged["source"] != "outflare" {
		t.Fatalf("the importer's copy of the reply: %v", bridged)
	}
	if c := n.count(`SELECT count(*) FROM sales_leads`); c != 1 {
		t.Fatalf("%d conversations after the importer sent the same reply", c)
	}
	if c := n.count(`SELECT count(*) FROM sales_messages WHERE lead_id = $1`, id); c != 3 {
		t.Fatalf("the importer's copy was kept twice: %d messages", c)
	}
}

func TestSalesConversationImportedThenRead(t *testing.T) {
	events := track.Capture(t)
	n := newNativeSales(t)
	ctx := context.Background()
	ana := n.person("Ana Example", "ana@customer.example", "", true)
	gus := n.person("Gus Example", "gus@customer.example", "", true)
	// An outside importer brought Ana's reply in first. Its row is from
	// before conversations kept their reply's hash.
	bridged := n.owner.must(201, "POST", n.root+"/bridge", map[string]any{"source": "portal", "source_id": "portal-lead-1", "record_id": ana,
		"integration_id": n.integration, "sender_id": "1", "reply_id": "601", "messages": []map[string]any{{"direction": "incoming",
			"from": "ana@customer.example", "to": []string{"ana@send-acme.test"}, "cc": []string{}, "subject": "Re: Quick question",
			"body": "Sounds good, can we talk Tuesday?", "message_id": "<reply-601@customer.example>", "provider_id": "601",
			"occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
	id := bridged["id"].(string)
	if _, err := n.e.pool.Exec(ctx, `UPDATE sales_leads SET reply_hash = NULL WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	// The sequencer refuses the thread: the failure is an event with a
	// code, and the job is tried again.
	n.bison.reply(601, "ana@customer.example", "Sounds good, can we talk Tuesday?", nil)
	// Gus was pushed to campaign 10, but this reply answers another one.
	n.bison.reply(610, "gus@customer.example", "Sounds good, let's talk.", map[string]any{"campaign_id": 99})
	n.bison.Lock()
	n.bison.broken = true
	n.bison.Unlock()
	n.sync()
	n.settle()
	failed := named(events(), "sales.reply_import_failed")
	if len(failed) == 0 || failed[0].ErrorCode != "connector_unavailable" || failed[0].Outcome != track.Error || failed[0].Surface != "job" ||
		failed[0].WorkspaceID != n.ws || failed[0].Props["integration_id"] != n.integration {
		t.Fatalf("sales.reply_import_failed: %+v", failed)
	}
	for _, e := range failed {
		for k, v := range e.Props {
			if s, ok := v.(string); ok && strings.Contains(s, "@") {
				t.Fatalf("the failure event carries an address in %s", k)
			}
		}
	}
	if c := n.count(`SELECT count(*) FROM sales_leads`); c != 1 {
		t.Fatalf("%d conversations while the sequencer refused", c)
	}
	n.bison.Lock()
	n.bison.broken = false
	n.bison.Unlock()
	if _, err := n.e.pool.Exec(ctx, `UPDATE jobs SET status = 'queued', attempts = 0, run_at = now() WHERE kind = 'sales.reply_import'`); err != nil {
		t.Fatal(err)
	}
	n.settle()

	// Ana keeps the importer's conversation, now with the thread; Gus has
	// none.
	if c := n.count(`SELECT count(*) FROM sales_leads`); c != 1 {
		t.Fatalf("%d conversations, want the imported one", c)
	}
	if c := n.count(`SELECT count(*) FROM sales_leads WHERE record_id = $1`, gus); c != 0 {
		t.Fatal("a reply to a campaign Outflare did not push the person to opened a conversation")
	}
	detail := n.owner.must(200, "GET", n.root+"/leads/"+id, nil)
	if detail["lead"].(map[string]any)["source"] != "portal" || len(detail["messages"].([]any)) != 2 {
		t.Fatalf("imported conversation after Outflare read the reply: %v", detail)
	}
	if c := n.count(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'sales.lead_arrived'`, n.ws); c != 1 {
		t.Fatal("the reply arrived twice")
	}
	if c := n.count(`SELECT count(*) FROM jobs WHERE kind = 'sales.reply_import' AND status <> 'succeeded'`); c != 0 {
		t.Fatalf("%d conversation jobs did not succeed", c)
	}
}
