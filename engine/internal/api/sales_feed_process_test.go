package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// feedItemSeq keeps the Slack timestamps of seeded posts apart.
var feedItemSeq struct {
	sync.Mutex
	n int
}

// feedChannelOf adds a feed channel of kind (Slack id slackID) to the
// fixture's brand and returns its id.
func (f *feedFixture) feedChannelOf(t *testing.T, kind, slackID string) string {
	t.Helper()
	var id string
	if err := f.e.pool.QueryRow(f.ctx, `INSERT INTO sales_feed_channels(workspace_id,brand_id,integration_id,channel_id,channel_name,kind)
		VALUES($1,$2,$3,$4,$4,$5) RETURNING id::text`, f.ws, f.brand, f.integ, slackID, kind).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedIn stores a pending post with text in channel.
func (f *feedFixture) seedIn(t *testing.T, channel, text string) string {
	t.Helper()
	feedItemSeq.Lock()
	feedItemSeq.n++
	n := feedItemSeq.n
	feedItemSeq.Unlock()
	id, err := f.e.srv.SeedSalesFeedItem(f.ctx, f.ws, f.brand, channel, feedTS(time.Hour, n), text)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedFeedItem adds a channel of kind and stores a pending post with text in it.
func seedFeedItem(t *testing.T, f *feedFixture, kind, text string) (item, channel string) {
	t.Helper()
	feedItemSeq.Lock()
	feedItemSeq.n++
	slackID := fmt.Sprintf("C%d", 100+feedItemSeq.n)
	feedItemSeq.Unlock()
	channel = f.feedChannelOf(t, kind, slackID)
	return f.seedIn(t, channel, text), channel
}

func (f *feedFixture) runItem(t *testing.T, item string) {
	t.Helper()
	if err := f.e.srv.RunSalesFeedItem(f.ctx, f.ws, f.brand, item, 1); err != nil {
		t.Fatalf("item job: %v", err)
	}
}

type feedItemRow struct {
	Status   string
	RecordID *string
	LeadID   *string
	Code     string
	Deleted  bool
}

func (f *feedFixture) itemRow(t *testing.T, item string) feedItemRow {
	t.Helper()
	var r feedItemRow
	if err := f.e.pool.QueryRow(f.ctx, `SELECT status,record_id::text,lead_id::text,error_code,deleted_in_slack FROM sales_feed_items WHERE workspace_id=$1 AND id=$2`, f.ws, item).
		Scan(&r.Status, &r.RecordID, &r.LeadID, &r.Code, &r.Deleted); err != nil {
		t.Fatal(err)
	}
	return r
}

// person is the brand's person with email, or "".
func (f *feedFixture) person(t *testing.T, email string) string {
	t.Helper()
	var id *string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT outflare_brand_person_with_email($1::uuid,$2)::text`, f.brand, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == nil {
		return ""
	}
	return *id
}

// company is the brand's company with domain, or "".
func (f *feedFixture) company(t *testing.T, domain string) string {
	t.Helper()
	var id string
	err := f.e.pool.QueryRow(f.ctx, `SELECT i.record_id::text FROM record_identities i JOIN records r ON r.id=i.record_id AND r.merged_into IS NULL
		WHERE i.workspace_id=$1 AND i.brand_id=$2 AND i.namespace='domain' AND i.value=$3 AND i.valid_to IS NULL`, f.ws, f.brand, domain).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

func (f *feedFixture) attr(t *testing.T, rec, key string) any {
	t.Helper()
	var raw []byte
	if err := f.e.pool.QueryRow(f.ctx, `SELECT attributes FROM records WHERE id=$1`, rec).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m[key]
}

func (f *feedFixture) hasEvent(name string) bool {
	return slices.ContainsFunc(f.events(), func(e track.Event) bool { return e.Name == name })
}

// me is the workspace owner's user id and email.
func (f *feedFixture) me(t *testing.T) (string, string) {
	t.Helper()
	u := f.owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)
	return u["id"].(string), u["email"].(string)
}

// teammate adds a second member to the workspace and returns their id.
func (f *feedFixture) teammate(t *testing.T, email string) string {
	t.Helper()
	acc := &api.Accounts{Pool: f.e.pool, PublicURL: "https://app.example.com", Mailer: f.e.outbox, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	id, _, err := acc.CreateAccount(f.ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: email, Name: "Bea", Password: "correct horse battery", WorkspaceName: "Bea's"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.pool.Exec(f.ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, f.ws, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// feedAIAnswer connects a fake OpenAI to the fixture's workspace that answers
// every read with *answer, and returns the connection's id.
func (f *feedFixture) feedAIAnswer(t *testing.T, answer *string) string {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
			return
		}
		mu.Lock()
		a := *answer
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"model": "gpt-test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant",
			"content": a}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 400, "completion_tokens": 80}})
	}))
	t.Cleanup(srv.Close)
	f.e.integrations.Client.BaseURLs["openai"] = srv.URL
	conn := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	f.owner.must(200, "PUT", "/v1/workspaces/"+f.ws+"/ai-settings", map[string]any{"integration_id": conn["id"], "model": "gpt-test",
		"input_cents_per_million": 10, "output_cents_per_million": 50, "monthly_limit_cents": 5000})
	return conn["id"].(string)
}

func spamAnswer(conf float64) string {
	return fmt.Sprintf(`{"email":"ana@acme.example","name":"Ana Reyes","company":"Acme","website":"","message":"","summary":"A pitch.","spam":true,"spam_confidence":%v}`, conf)
}

func TestFeedItemNewPerson(t *testing.T) {
	f := newFeedFixture(t)
	item, ch := seedFeedItem(t, f, "new_person", "Work email: ana@acme.example\nCompany Website: https://acme.example")
	f.runItem(t, item)

	person, company := f.person(t, "ana@acme.example"), f.company(t, "acme.example")
	if person == "" || company == "" {
		t.Fatalf("person %q company %q", person, company)
	}
	if f.attr(t, company, "website") != "https://acme.example" {
		t.Fatalf("website: %v", f.attr(t, company, "website"))
	}
	if cs, _ := f.attr(t, person, "companies").([]any); len(cs) != 1 || cs[0] != company {
		t.Fatalf("companies: %v", f.attr(t, person, "companies"))
	}
	if f.count(t, `SELECT count(*) FROM record_relationships WHERE workspace_id=$1 AND from_id=$2 AND to_id=$3 AND kind='companies' AND valid_to IS NULL`, f.ws, person, company) != 1 {
		t.Fatal("no link to the company")
	}
	if f.count(t, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='companies'`, person) != 1 {
		t.Fatal("the link is not in the person's history")
	}
	var lead string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT id::text FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND record_id=$3 AND state='new' AND source='slack'
		AND integration_id IS NULL AND feed_kind='new_person' AND feed_channel_id=$4`, f.ws, f.brand, person, ch).Scan(&lead); err != nil {
		t.Fatalf("lead: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws); n != 1 {
		t.Fatalf("leads: %d", n)
	}
	row := f.itemRow(t, item)
	if row.Status != "read" || row.RecordID == nil || *row.RecordID != person || row.LeadID == nil || *row.LeadID != lead {
		t.Fatalf("item: %+v", row)
	}
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if err != nil || len(c.Fields) != 2 || !slices.Equal(c.Candidates, []string{"ana@acme.example"}) {
		t.Fatalf("content: %+v %v", c, err)
	}
	if !f.hasEvent("sales.lead_arrived") || !f.hasEvent("sales_feed.item_read") {
		t.Fatal("events missing")
	}
	for _, e := range f.events() {
		if e.Name == "sales_feed.item_read" && (e.ActorType != "system" || e.Props["made"] != "conversation") {
			t.Fatalf("event: %+v", e)
		}
	}
}

func TestFeedItemFreeMailHasNoCompany(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@gmail.com")
	f.runItem(t, item)
	person := f.person(t, "ana@gmail.com")
	if person == "" || f.company(t, "gmail.com") != "" || f.attr(t, person, "companies") != nil {
		t.Fatalf("person %q, company %q", person, f.company(t, "gmail.com"))
	}
	if f.itemRow(t, item).Status != "read" {
		t.Fatal("not read")
	}
}

func TestFeedItemJoinsOpenConversation(t *testing.T) {
	f := newFeedFixture(t)
	first, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	second, _ := seedFeedItem(t, f, "handover", "Hand over: ana@acme.example is ready")
	f.runItem(t, first)
	f.runItem(t, second)
	if n := f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws); n != 1 {
		t.Fatalf("leads: %d", n)
	}
	a, b := f.itemRow(t, first), f.itemRow(t, second)
	if a.LeadID == nil || b.LeadID == nil || *a.LeadID != *b.LeadID {
		t.Fatalf("items: %+v %+v", a, b)
	}
	if f.count(t, `SELECT version FROM sales_leads WHERE id=$1`, *a.LeadID) != 2 {
		t.Fatal("version not bumped")
	}
}

func TestFeedItemActiveConversationStaysActive(t *testing.T) {
	f := newFeedFixture(t)
	first, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, first)
	lead := *f.itemRow(t, first).LeadID
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_leads SET state='active' WHERE id=$1`, lead); err != nil {
		t.Fatal(err)
	}
	second, _ := seedFeedItem(t, f, "new_person", "Again: ana@acme.example")
	f.runItem(t, second)
	if r := f.itemRow(t, second); r.LeadID == nil || *r.LeadID != lead {
		t.Fatalf("item: %+v", r)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1 AND state='active'`, f.ws) != 1 ||
		f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1 AND state='new'`, f.ws) != 0 {
		t.Fatal("state changed or a new conversation made")
	}
}

func TestFeedItemArchivedMakesNew(t *testing.T) {
	f := newFeedFixture(t)
	first, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, first)
	old := *f.itemRow(t, first).LeadID
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_leads SET state='archived' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	second, _ := seedFeedItem(t, f, "new_person", "Again: ana@acme.example")
	f.runItem(t, second)
	r := f.itemRow(t, second)
	if r.LeadID == nil || *r.LeadID == old || f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1 AND state='new'`, f.ws) != 1 {
		t.Fatalf("item: %+v", r)
	}
}

func TestFeedItemsTogether(t *testing.T) {
	f := newFeedFixture(t)
	a, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	b, _ := seedFeedItem(t, f, "handover", "Also: ana@acme.example")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.e.srv.RunSalesFeedItem(f.ctx, f.ws, f.brand, id, 1)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	if n := f.count(t, `SELECT count(*) FROM record_identities WHERE workspace_id=$1 AND namespace='email' AND value='ana@acme.example'`, f.ws); n != 1 {
		t.Fatalf("people: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM records WHERE workspace_id=$1 AND record_type='account'`, f.ws); n != 1 {
		t.Fatalf("companies: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws); n != 1 {
		t.Fatalf("leads: %d", n)
	}
}

func TestFeedItemMergedRecord(t *testing.T) {
	f := newFeedFixture(t)
	base := "/v1/brands/" + f.brand
	gone := f.owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	kept := f.owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Reyes",
		"identities": []map[string]any{{"namespace": "email", "value": "ana.reyes@acme.example"}}})["id"].(string)
	f.owner.must(200, "POST", base+"/records/"+kept+"/merge", map[string]any{"from": gone})
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	r := f.itemRow(t, item)
	if r.RecordID == nil || *r.RecordID != kept {
		t.Fatalf("item on %v, want %s", r.RecordID, kept)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1 AND record_id=$2`, f.ws, kept) != 1 {
		t.Fatal("conversation not on the surviving record")
	}
}

func TestFeedItemOwnerFromPost(t *testing.T) {
	f := newFeedFixture(t)
	me, myEmail := f.me(t)
	f.owner.must(200, "PUT", "/v1/brands/"+f.brand+"/sales/team/"+me, map[string]any{"role": "sdr"})
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example\nContact owner: "+myEmail)
	f.runItem(t, item)
	person := f.person(t, "ana@acme.example")
	if f.attr(t, person, "owner") != me {
		t.Fatalf("owner: %v", f.attr(t, person, "owner"))
	}
	if f.count(t, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='owner'`, person) != 1 {
		t.Fatal("owner not in the person's history")
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE record_id=$1 AND owner_id=$2 AND sdr_id=$2 AND ae_id IS NULL`, person, me) != 1 {
		t.Fatal("conversation owner not set")
	}
}

func TestFeedItemOwnerNotOnSalesDesk(t *testing.T) {
	f := newFeedFixture(t)
	me, myEmail := f.me(t)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example\nContact owner: "+myEmail)
	f.runItem(t, item)
	person := f.person(t, "ana@acme.example")
	if f.attr(t, person, "owner") != me {
		t.Fatalf("owner: %v", f.attr(t, person, "owner"))
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE record_id=$1 AND owner_id IS NULL AND sdr_id IS NULL AND ae_id IS NULL`, person) != 1 {
		t.Fatal("conversation owned by someone not on the sales desk")
	}
}

func TestFeedItemKeepsExistingOwner(t *testing.T) {
	f := newFeedFixture(t)
	_, myEmail := f.me(t)
	bea := f.teammate(t, "bea@team.example")
	f.owner.must(201, "POST", "/v1/brands/"+f.brand+"/records", map[string]any{"type": "person", "name": "Ana",
		"attributes": map[string]any{"owner": bea}, "identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example\nContact owner: "+myEmail)
	f.runItem(t, item)
	if got := f.attr(t, f.person(t, "ana@acme.example"), "owner"); got != bea {
		t.Fatalf("owner: %v", got)
	}
}

func TestFeedItemRoutedWhenNoOwner(t *testing.T) {
	f := newFeedFixture(t)
	bea := f.teammate(t, "bea@team.example")
	f.owner.must(200, "PUT", "/v1/brands/"+f.brand+"/routing", map[string]any{"rules": []any{
		map[string]any{"name": "Everyone", "record_type": "person", "members": []string{bea}}}})
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	person := f.person(t, "ana@acme.example")
	if got := f.attr(t, person, "owner"); got != bea {
		t.Fatalf("owner: %v", got)
	}
	if f.count(t, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='owner' AND source='routing'`, person) != 1 {
		t.Fatal("routing not in the person's history")
	}
}

func TestFeedItemSignal(t *testing.T) {
	f := newFeedFixture(t)
	item, ch := seedFeedItem(t, f, "signal", "Usage alert: ana@acme.example's team used 95% of the plan")
	f.runItem(t, item)
	company := f.company(t, "acme.example")
	var title, source string
	var data []byte
	if err := f.e.pool.QueryRow(f.ctx, `SELECT title,source,data FROM signals WHERE workspace_id=$1 AND brand_id=$2 AND record_id=$3 AND type='upsell'`, f.ws, f.brand, company).
		Scan(&title, &source, &data); err != nil {
		t.Fatalf("signal: %v", err)
	}
	var d map[string]any
	_ = json.Unmarshal(data, &d)
	if source != "feed" || !strings.HasPrefix(title, "Usage alert") || d["feed_channel_id"] != ch || d["item_id"] != item || d["link"] == "" {
		t.Fatalf("signal: %q %q %v", title, source, d)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("a signal post made a conversation")
	}
	if r := f.itemRow(t, item); r.Status != "read" || r.RecordID == nil || r.LeadID != nil {
		t.Fatalf("item: %+v", r)
	}
}

func TestFeedItemSignalDuplicateIsFine(t *testing.T) {
	f := newFeedFixture(t)
	first, ch := seedFeedItem(t, f, "signal", "Usage alert: ana@acme.example at 95%")
	second := f.seedIn(t, ch, "Usage alert: ana@acme.example at 99%")
	f.runItem(t, first)
	f.runItem(t, second)
	if n := f.count(t, `SELECT count(*) FROM signals WHERE workspace_id=$1`, f.ws); n != 1 {
		t.Fatalf("signals: %d", n)
	}
	if f.itemRow(t, second).Status != "read" {
		t.Fatal("second not read")
	}
}

func TestFeedItemContextOnly(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "context", "Note: ana@acme.example asked about invoices")
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "read" || r.RecordID == nil || r.LeadID != nil {
		t.Fatalf("item: %+v", r)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws)+f.count(t, `SELECT count(*) FROM signals WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("context made a conversation or a signal")
	}
}

func TestFeedItemSeveralAddressesNoAI(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "new_person", "From ana@acme.example, cc ben@birch.example")
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "needs_look" || r.RecordID != nil || r.LeadID != nil {
		t.Fatalf("item: %+v", r)
	}
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if err != nil || !slices.Equal(c.Candidates, []string{"ana@acme.example", "ben@birch.example"}) {
		t.Fatalf("candidates: %v %v", c.Candidates, err)
	}
	if f.person(t, "ana@acme.example") != "" {
		t.Fatal("a person was made")
	}
	if !f.hasEvent("sales_feed.item_needs_look") {
		t.Fatal("no event")
	}
}

func TestFeedItemCompanyOnly(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "new_person", "Someone at https://birch.example asked for pricing")
	f.runItem(t, item)
	company := f.company(t, "birch.example")
	r := f.itemRow(t, item)
	if company == "" || r.Status != "needs_look" || r.RecordID == nil || *r.RecordID != company || r.LeadID != nil {
		t.Fatalf("company %q item %+v", company, r)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("conversation made")
	}
}

func TestFeedItemSpam(t *testing.T) {
	f := newFeedFixture(t)
	answer := spamAnswer(0.95)
	f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Buy followers now! ana@acme.example")
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "spam" || r.RecordID != nil {
		t.Fatalf("item: %+v", r)
	}
	if f.person(t, "ana@acme.example") != "" || f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("spam made records")
	}
	if !f.hasEvent("sales_feed.item_spam") {
		t.Fatal("no event")
	}
}

func TestFeedItemSpamUnsure(t *testing.T) {
	f := newFeedFixture(t)
	answer := spamAnswer(0.6)
	f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Hi, ana@acme.example here")
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "read" || r.LeadID == nil {
		t.Fatalf("item: %+v", r)
	}
	var name string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT name FROM records WHERE id=$1`, f.person(t, "ana@acme.example")).Scan(&name); err != nil || name != "Ana Reyes" {
		t.Fatalf("name from the reading: %q %v", name, err)
	}
}

// An AI that fails marks the item failed with a code and the job retries;
// the last attempt carries on with rules only.
func TestFeedItemAIFailure(t *testing.T) {
	f := newFeedFixture(t)
	answer := "not json"
	f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	// The model's answer fails to parse with "invalid character ...": that
	// text, which can quote the post, goes nowhere.
	const leak = "invalid character"
	err := f.e.srv.RunSalesFeedItem(f.ctx, f.ws, f.brand, item, 1)
	if err == nil {
		t.Fatal("no error to retry")
	}
	if strings.Contains(err.Error(), leak) {
		t.Fatalf("the job's error quotes the AI's: %v", err)
	}
	if r := f.itemRow(t, item); r.Status != "failed" || r.Code != "ai_failed" {
		t.Fatalf("item: %+v", r)
	}
	var row string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT (to_jsonb(i)-'content_sealed')::text FROM sales_feed_items i WHERE id=$1`, item).Scan(&row); err != nil || strings.Contains(row, leak) {
		t.Fatalf("item row: %s %v", row, err)
	}
	reason := ""
	for _, e := range f.events() {
		if b, _ := json.Marshal(e.Props); strings.Contains(string(b), leak) {
			t.Fatalf("event %s carries the error: %s", e.Name, b)
		}
		if e.Name == "sales_feed.item_failed" {
			reason, _ = e.Props["reason"].(string)
		}
	}
	// The event says why, as a code.
	if reason != "bad_answer" {
		t.Fatalf("reason: %q, want bad_answer", reason)
	}
	if err := f.e.srv.RunSalesFeedItem(f.ctx, f.ws, f.brand, item, 5); err != nil {
		t.Fatal(err)
	}
	if r := f.itemRow(t, item); r.Status != "read" || r.Code != "" || r.LeadID == nil {
		t.Fatalf("item: %+v", r)
	}
}

// A model that wraps its object in a code fence is still read.
func TestFeedItemFencedAnswer(t *testing.T) {
	f := newFeedFixture(t)
	answer := "Here it is:\n```json\n" + spamAnswer(0.6) + "\n```"
	f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Hi, ana@acme.example here")
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "read" || r.Code != "" || r.LeadID == nil {
		t.Fatalf("item: %+v", r)
	}
	if f.hasEvent("sales_feed.item_failed") {
		t.Fatal("a fenced answer counted as a failure")
	}
}

func TestFeedItemEditKeepsPerson(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	before := f.itemRow(t, item)
	if err := f.e.srv.EditSalesFeedItem(f.ctx, f.ws, item, "Email: ben@birch.example"); err != nil {
		t.Fatal(err)
	}
	f.runItem(t, item)
	after := f.itemRow(t, item)
	if *after.RecordID != *before.RecordID || *after.LeadID != *before.LeadID || after.Status != "read" {
		t.Fatalf("before %+v after %+v", before, after)
	}
	if f.person(t, "ben@birch.example") != "" || f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws) != 1 {
		t.Fatal("the edit made a person or conversation")
	}
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if err != nil || c.Text != "Email: ben@birch.example" || !slices.Equal(c.Candidates, []string{"ben@birch.example"}) {
		t.Fatalf("content: %+v %v", c, err)
	}
}

// Slack shows a deleted parent with replies as this text: the item is
// marked deleted and nothing else changes.
func TestFeedItemDeletedParent(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	before := f.itemRow(t, item)
	if err := f.e.srv.EditSalesFeedItem(f.ctx, f.ws, item, "This message was deleted."); err != nil {
		t.Fatal(err)
	}
	f.runItem(t, item)
	after := f.itemRow(t, item)
	if !after.Deleted || after.Status != before.Status || *after.RecordID != *before.RecordID || *after.LeadID != *before.LeadID {
		t.Fatalf("before %+v after %+v", before, after)
	}
	c, _ := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if !slices.Equal(c.Candidates, []string{"ana@acme.example"}) {
		t.Fatalf("content changed: %v", c.Candidates)
	}
}

func TestFeedItemNothingInPlainColumns(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "new_person", "Work email: ana@acme.example\nCompany Website: https://acme.example")
	f.runItem(t, item)
	var row string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT (to_jsonb(i)-'content_sealed')::text FROM sales_feed_items i WHERE id=$1`, item).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row, "ana@acme.example") {
		t.Fatalf("item row: %s", row)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND data::text LIKE '%ana@acme.example%'`, f.ws); n != 0 {
		t.Fatalf("audit rows with the address: %d", n)
	}
	for _, e := range f.events() {
		if b, _ := json.Marshal(e.Props); strings.Contains(string(b), "ana@acme.example") {
			t.Fatalf("event %s: %s", e.Name, b)
		}
	}
}

// A person in Recently deleted can't be written to: the post waits for a
// teammate to look, and the job ends.
func TestFeedItemDeletedPerson(t *testing.T) {
	f := newFeedFixture(t)
	gone := f.owner.must(201, "POST", "/v1/brands/"+f.brand+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE records SET deleted_at=now() WHERE id=$1`, gone); err != nil {
		t.Fatal(err)
	}
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "needs_look" || r.RecordID != nil || r.LeadID != nil {
		t.Fatalf("item: %+v", r)
	}
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if err != nil || !slices.Equal(c.Candidates, []string{"ana@acme.example"}) {
		t.Fatalf("candidates: %v %v", c.Candidates, err)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("conversation made")
	}
}

// A post a person resolved is only read again after an edit, whatever its status.
func TestFeedItemResolvedIsRefreshOnly(t *testing.T) {
	f := newFeedFixture(t)
	answer := spamAnswer(0.95)
	f.feedAIAnswer(t, &answer)
	me, _ := f.me(t)
	item, _ := seedFeedItem(t, f, "new_person", "Buy followers now! ana@acme.example")
	f.runItem(t, item)
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_feed_items SET resolved_by=$2 WHERE id=$1`, item, me); err != nil {
		t.Fatal(err)
	}
	if err := f.e.srv.EditSalesFeedItem(f.ctx, f.ws, item, "Buy followers today! ana@acme.example"); err != nil {
		t.Fatal(err)
	}
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "spam" || r.RecordID != nil {
		t.Fatalf("item: %+v", r)
	}
	if f.person(t, "ana@acme.example") != "" {
		t.Fatal("a resolved post made a person")
	}
	c, _ := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if c.Text != "Buy followers today! ana@acme.example" {
		t.Fatalf("content: %q", c.Text)
	}
}

// A member who can't work in the brand is never made an Owner by a post.
func TestFeedItemDisabledMemberNotOwner(t *testing.T) {
	f := newFeedFixture(t)
	bea := f.teammate(t, "bea@team.example")
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE users SET disabled_at=now() WHERE id=$1`, bea); err != nil {
		t.Fatal(err)
	}
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example\nContact owner: bea@team.example")
	f.runItem(t, item)
	if n := f.count(t, `SELECT count(*) FROM records WHERE workspace_id=$1 AND attributes->>'owner'=$2`, f.ws, bea); n != 0 {
		t.Fatalf("records owned by the disabled member: %d", n)
	}
	// A disabled member is still a teammate: never a candidate, never a record.
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if err != nil || !slices.Equal(c.Candidates, []string{"ana@acme.example"}) {
		t.Fatalf("candidates: %v %v", c.Candidates, err)
	}
	person := f.person(t, "ana@acme.example")
	if r := f.itemRow(t, item); person == "" || r.Status != "read" || r.RecordID == nil || *r.RecordID != person {
		t.Fatalf("person %q item %+v", person, r)
	}
	if f.count(t, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='owner' AND source='manual'`, person) != 0 {
		t.Fatal("the post made someone the Owner")
	}
	if f.person(t, "bea@team.example") != "" || f.count(t, `SELECT count(*) FROM record_identities WHERE workspace_id=$1 AND value='bea@team.example'`, f.ws) != 0 {
		t.Fatal("the teammate became a record")
	}
}

// A signal channel whose type the brand doesn't have reads the post and
// says why nothing was raised.
func TestFeedItemSignalTypeMissing(t *testing.T) {
	f := newFeedFixture(t)
	item, ch := seedFeedItem(t, f, "signal", "Usage alert: ana@acme.example at 95%")
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_feed_channels SET signal_type='no_such_type' WHERE id=$1`, ch); err != nil {
		t.Fatal(err)
	}
	f.runItem(t, item)
	if r := f.itemRow(t, item); r.Status != "read" {
		t.Fatalf("item: %+v", r)
	}
	if f.count(t, `SELECT count(*) FROM signals WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("signal raised")
	}
	found := false
	for _, e := range f.events() {
		if e.Name == "sales_feed.signal_failed" && e.ErrorCode == "signal_type_missing" && e.Props["feed_channel_id"] == ch {
			found = true
		}
	}
	if !found {
		t.Fatal("no sales_feed.signal_failed event")
	}
}

// A post read again while the AI is unavailable keeps its last reading.
func TestFeedItemRefreshKeepsReading(t *testing.T) {
	f := newFeedFixture(t)
	answer := `{"email":"ana@acme.example","name":"Ana Reyes","company":"Acme","website":"","message":"","summary":"Ana wants a demo.","spam":false,"spam_confidence":0.1}`
	conn := f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	f.owner.must(204, "DELETE", "/v1/workspaces/"+f.ws+"/integrations/"+conn, nil)
	if err := f.e.srv.EditSalesFeedItem(f.ctx, f.ws, item, "Email: ana@acme.example, wants a demo"); err != nil {
		t.Fatal(err)
	}
	f.runItem(t, item)
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, item)
	if err != nil || c.Reading == nil || c.Reading.Name != "Ana Reyes" || c.Text != "Email: ana@acme.example, wants a demo" {
		t.Fatalf("content: %+v %v", c, err)
	}
}

// A person with no Owner whose open conversation was assigned before they
// came through Slack takes that conversation's owner as their Owner, not the
// one the post names or routing picks.
func TestFeedItemOwnerFromOpenConversation(t *testing.T) {
	f := newFeedFixture(t)
	_, myEmail := f.me(t)
	bea := f.teammate(t, "bea@team.example")
	f.owner.must(200, "PUT", "/v1/brands/"+f.brand+"/sales/team/"+bea, map[string]any{"role": "sdr"})
	cy := f.teammate(t, "cy@team.example")
	f.owner.must(200, "PUT", "/v1/brands/"+f.brand+"/routing", map[string]any{"rules": []any{
		map[string]any{"name": "Everyone", "record_type": "person", "members": []string{cy}}}})
	person := f.owner.must(201, "POST", "/v1/brands/"+f.brand+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	f.exec(t, `INSERT INTO sales_leads(id,workspace_id,brand_id,record_id,source,source_id,source_sealed,state,owner_id,sdr_id) VALUES(gen_random_uuid(),$1,$2,$3,'manual','m1','\x00','active',$4,$4)`,
		f.ws, f.brand, person, bea)
	item, _ := seedFeedItem(t, f, "handover", "Hand over: ana@acme.example\nContact owner: "+myEmail)
	f.runItem(t, item)
	if got := f.attr(t, person, "owner"); got != bea {
		t.Fatalf("owner: %v, want the conversation's %s", got, bea)
	}
	if f.count(t, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='owner'`, person) != 1 {
		t.Fatal("the Owner is not in the person's history")
	}
	if r := f.itemRow(t, item); r.LeadID == nil || f.count(t, `SELECT count(*) FROM sales_leads WHERE id=$1 AND owner_id=$2`, *r.LeadID, bea) != 1 {
		t.Fatalf("not on the open conversation: %+v", r)
	}
}

// A post read again after an edit is billed again.
func TestFeedItemReReadBooksSpend(t *testing.T) {
	f := newFeedFixture(t)
	answer := `{"email":"ana@acme.example","name":"Ana Reyes","company":"Acme","website":"","message":"","summary":"Ana wants a demo.","spam":false,"spam_confidence":0.1}`
	f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	if err := f.e.srv.EditSalesFeedItem(f.ctx, f.ws, item, "Email: ana@acme.example, wants a demo"); err != nil {
		t.Fatal(err)
	}
	f.runItem(t, item)
	if n := f.count(t, `SELECT count(*) FROM ai_calls WHERE workspace_id=$1 AND task='lead_channels' AND ref LIKE 'lead_channel:'||$2||'%'`, f.ws, item); n != 2 {
		t.Fatalf("reads booked: %d", n)
	}
}

// Erasing a person erases the signals their posts raised, also on their
// company, which stays.
func TestFeedSignalErasedWithPerson(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "signal", "Usage alert: ana@acme.example's team used 95% of the plan")
	f.runItem(t, item)
	person, company := f.person(t, "ana@acme.example"), f.company(t, "acme.example")
	if f.count(t, `SELECT count(*) FROM signals WHERE workspace_id=$1 AND record_id=$2 AND data->>'item_id'=$3`, f.ws, company, item) != 1 {
		t.Fatal("no signal on the company")
	}
	f.owner.must(200, "DELETE", "/v1/workspaces/"+f.ws+"/records/"+person, nil)
	if n := f.count(t, `SELECT count(*) FROM signals WHERE workspace_id=$1 AND data->>'item_id'=$2`, f.ws, item); n != 0 {
		t.Fatalf("signals left from the erased person's post: %d", n)
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1`, item) != 0 {
		t.Fatal("the post stayed")
	}
	f.owner.must(200, "GET", "/v1/workspaces/"+f.ws+"/records/"+company, nil)
}
