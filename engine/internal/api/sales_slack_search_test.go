package api_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// slackMatch is one search.messages match in Slack's shape. flags are the
// channel's own booleans (is_private, is_im, is_mpim, is_group).
func slackMatch(id, name string, flags map[string]any, user, text string, ago time.Duration) map[string]any {
	ch := map[string]any{"id": id, "name": name, "is_channel": true}
	for k, v := range flags {
		ch[k] = v
	}
	ts := feedTS(ago, 1)
	return map[string]any{"channel": ch, "user": user, "text": text, "ts": ts,
		"permalink": "https://acme.slack.example/archives/" + id + "/p" + strings.Replace(ts, ".", "", 1)}
}

// searchFixture connects Slack with a search token, adds lead channel C1
// and makes a conversation with ana@acme.example at Acme (acme.example).
func searchFixture(t *testing.T, withToken bool) (f *feedFixture, lead string) {
	t.Helper()
	f = newFeedFixture(t)
	creds := map[string]any{"api_key": feedBotToken}
	if withToken {
		creds["search_token"] = feedUserToken
	}
	f.integ = f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds", "credentials": creds})["id"].(string)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	lead = *f.itemRow(t, item).LeadID
	f.setCompany(t, lead, "Acme", "https://www.acme.example/pricing")
	return f, lead
}

// setCompany names the conversation's company and its website.
func (f *feedFixture) setCompany(t *testing.T, lead, name, website string) {
	t.Helper()
	tag, err := f.e.pool.Exec(f.ctx, `UPDATE records c SET name=$2, attributes=c.attributes||jsonb_build_object('website',$3::text)
		FROM sales_leads l JOIN records r ON r.id=l.record_id WHERE l.id=$1 AND c.id::text=r.attributes->'companies'->>0`, lead, name, website)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("company: %v %v", tag, err)
	}
}

func (f *feedFixture) search(t *testing.T, c *client, want int, lead string) map[string]any {
	t.Helper()
	return c.must(want, "GET", f.salesRoot()+"/leads/"+lead+"/slack-search", nil)
}

func TestSlackSearchQuery(t *testing.T) {
	f, lead := searchFixture(t, true)
	out := f.search(t, f.owner, 200, lead)
	want := `ana@acme.example OR acme.example OR "Acme"`
	if out["query"] != want || out["available"] != true {
		t.Fatalf("out: %v", out)
	}
	if q := f.slack.searchQuery; len(q) != 1 || q[0] != want {
		t.Fatalf("Slack got %q", q)
	}
}

func TestSlackSearchQueryTerms(t *testing.T) {
	f, lead := searchFixture(t, true)
	// A quote in the name is removed; no website falls back to the email's domain.
	f.setCompany(t, lead, `Acme "The Best" Co`, "")
	if q := f.search(t, f.owner, 200, lead)["query"]; q != `ana@acme.example OR acme.example OR "Acme The Best Co"` {
		t.Fatalf("query: %v", q)
	}
	// A free-mail address gives no domain term.
	f.exec(t, `UPDATE record_identities SET value='ana@gmail.com' WHERE workspace_id=$1 AND namespace='email' AND value='ana@acme.example'`, f.ws)
	f.setCompany(t, lead, "Acme", "")
	if q := f.search(t, f.owner, 200, lead)["query"]; q != `ana@gmail.com OR "Acme"` {
		t.Fatalf("free mail: %v", q)
	}
}

func TestSlackSearchDropsPrivate(t *testing.T) {
	f, lead := searchFixture(t, true)
	var feedID string
	f.e.pool.QueryRow(f.ctx, `SELECT channel_id FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2`, f.ws, f.brand).Scan(&feedID)
	f.slack.matches = []map[string]any{
		slackMatch("C9", "general", map[string]any{"is_private": false}, "U1", "public ana@acme.example", time.Minute),
		slackMatch("D1", "U7", map[string]any{"is_im": true, "is_channel": false}, "U7", "a direct message", 2*time.Minute),
		slackMatch("G1", "mpdm-a--b", map[string]any{"is_mpim": true, "is_private": true, "is_channel": false}, "U2", "a group message", 3*time.Minute),
		slackMatch("C8", "secret", map[string]any{"is_private": true}, "U3", "private channel", 4*time.Minute),
		slackMatch(feedID, "lead-channel", map[string]any{"is_private": true}, "U4", "private lead channel", 5*time.Minute),
		slackMatch("C7", "old-style", map[string]any{"is_group": true}, "U5", "old private channel", 6*time.Minute),
	}
	data := f.search(t, f.owner, 200, lead)["data"].([]any)
	var names []string
	for _, h := range data {
		names = append(names, h.(map[string]any)["channel_name"].(string))
	}
	if !slices.Equal(names, []string{"general", "lead-channel"}) {
		t.Fatalf("channels: %v", names)
	}
	// A removed Sales channel is not one of the brand's: its private hits go.
	f.exec(t, `INSERT INTO sales_feed_channels(workspace_id,brand_id,integration_id,channel_id,channel_name,kind,enabled,removed_at)
		VALUES($1,$2,$3,'C5','old-leads','new_person',false,now())`, f.ws, f.brand, f.integ)
	f.slack.matches = append(f.slack.matches, slackMatch("C5", "old-leads", map[string]any{"is_private": true}, "U6", "removed lead channel", 7*time.Minute))
	names = nil
	for _, h := range f.search(t, f.owner, 200, lead)["data"].([]any) {
		names = append(names, h.(map[string]any)["channel_name"].(string))
	}
	if !slices.Equal(names, []string{"general", "lead-channel"}) {
		t.Fatalf("with a removed channel: %v", names)
	}
	h := data[0].(map[string]any)
	if h["author"] != "U1" || h["text"] != "public ana@acme.example" || !strings.HasPrefix(h["link"].(string), "https://acme.slack.example/archives/C9/") || h["at"] == "" {
		t.Fatalf("hit: %v", h)
	}
}

func TestSlackSearchLimits(t *testing.T) {
	f, lead := searchFixture(t, true)
	for i := range 30 {
		f.slack.matches = append(f.slack.matches, slackMatch("C9", "general", nil, "U1", strings.Repeat("x", 600), time.Duration(i+1)*time.Minute))
	}
	data := f.search(t, f.owner, 200, lead)["data"].([]any)
	if len(data) != 20 {
		t.Fatalf("hits: %d", len(data))
	}
	if n := len([]rune(data[0].(map[string]any)["text"].(string))); n != 500 {
		t.Fatalf("text: %d", n)
	}
}

func TestSlackSearchNoToken(t *testing.T) {
	f, lead := searchFixture(t, false)
	out := f.search(t, f.owner, 200, lead)
	if out["available"] != false || out["query"] != `ana@acme.example OR acme.example OR "Acme"` || len(out["data"].([]any)) != 0 {
		t.Fatalf("out: %v", out)
	}
	if n := f.slack.count("search.messages"); n != 0 {
		t.Fatalf("Slack called %d times", n)
	}
}

func TestSlackSearchNoChannels(t *testing.T) {
	f, lead := searchFixture(t, true)
	f.exec(t, `DELETE FROM sales_feed_channels WHERE workspace_id=$1`, f.ws)
	out := f.search(t, f.owner, 200, lead)
	if out["available"] != false || out["query"] == "" || len(out["data"].([]any)) != 0 || f.slack.count("search.messages") != 0 {
		t.Fatalf("out: %v", out)
	}
}

func TestSlackSearchNotVisible(t *testing.T) {
	f, lead := searchFixture(t, true)
	f.exec(t, `UPDATE sales_leads SET state='active', owner_id=$2 WHERE id=$1`, lead, f.owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"])
	rep, _ := f.salesUser(t, "rep@acme.example", "sdr")
	f.search(t, rep, 404, lead)
	f.search(t, f.owner, 200, lead)
	// A caller with no Sales role gets what the conversation itself gives.
	none, _ := f.salesUser(t, "none@acme.example", "")
	code, _ := none.do("GET", f.salesRoot()+"/leads/"+lead, nil)
	f.search(t, none, code, lead)
	f.search(t, f.owner, 404, "00000000-0000-0000-0000-000000000000")
	if n := f.slack.count("search.messages"); n != 1 {
		t.Fatalf("Slack called %d times", n)
	}
}

func TestSlackSearchSlackDown(t *testing.T) {
	f, lead := searchFixture(t, true)
	f.slack.searchStatus = http.StatusServiceUnavailable
	f.search(t, f.owner, http.StatusBadGateway, lead)
	var failed, searched bool
	for _, e := range f.events() {
		switch e.Name {
		case "sales_feed.slack_search_failed":
			failed = e.Outcome == track.Failure && e.ErrorCode == "connector_unavailable"
		case "sales_feed.slack_searched":
			searched = true
		}
	}
	if !failed || searched {
		t.Fatalf("events: failed=%v searched=%v", failed, searched)
	}
}

func TestSlackSearchRateLimited(t *testing.T) {
	f, lead := searchFixture(t, true)
	f.slack.searchStatus = http.StatusTooManyRequests
	f.search(t, f.owner, http.StatusTooManyRequests, lead)
	if n := f.slack.count("search.messages"); n != 1 {
		t.Fatalf("Slack called %d times", n)
	}
}

func TestSlackSearchEvent(t *testing.T) {
	f, lead := searchFixture(t, true)
	f.slack.matches = []map[string]any{slackMatch("C9", "general", nil, "U1", "hello", time.Minute)}
	f.search(t, f.owner, 200, lead)
	for _, e := range f.events() {
		if e.Name == "sales_feed.slack_searched" {
			if len(e.Props) != 2 || e.Props["hits"] != 1 || e.Props["available"] != true {
				t.Fatalf("props: %v", e.Props)
			}
			return
		}
	}
	t.Fatal("no sales_feed.slack_searched event")
}

func TestSlackSearchStoresNothing(t *testing.T) {
	f, lead := searchFixture(t, true)
	f.slack.matches = []map[string]any{slackMatch("C9", "general", nil, "U1", "hello", time.Minute)}
	tables := func() string {
		rows, err := f.e.pool.Query(f.ctx, `SELECT relname FROM pg_class WHERE relkind='r' AND relnamespace='public'::regnamespace AND (relname LIKE 'sales\_%' OR relname IN ('records','attribute_changes','audit_events','jobs','product_events')) ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			rows.Scan(&n)
			var c int
			if err := f.e.pool.QueryRow(f.ctx, fmt.Sprintf(`SELECT count(*) FROM %q WHERE workspace_id=$1`, n), f.ws).Scan(&c); err == nil {
				out = append(out, fmt.Sprintf("%s=%d", n, c))
			}
		}
		return strings.Join(out, ",")
	}
	before := tables()
	f.search(t, f.owner, 200, lead)
	if after := tables(); after != before {
		t.Fatalf("rows changed:\n%s\n%s", before, after)
	}
}

func TestSlackSearchDropsUnclearChannels(t *testing.T) {
	f, lead := searchFixture(t, true)
	noChannel := slackMatch("C9", "x", nil, "U1", "no channel object", time.Minute)
	delete(noChannel, "channel")
	f.slack.matches = []map[string]any{
		noChannel,
		slackMatch("", "x", nil, "U1", "empty id", 2*time.Minute),
		slackMatch("C6", "partner", map[string]any{"is_ext_shared": true}, "U1", "externally shared", 3*time.Minute),
		slackMatch("C5", "org", map[string]any{"is_org_shared": true}, "U1", "org shared", 4*time.Minute),
		slackMatch("C4", "open", nil, "U1", "kept", 5*time.Minute),
	}
	data := f.search(t, f.owner, 200, lead)["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["text"] != "kept" {
		t.Fatalf("data: %v", data)
	}
}

func TestSlackSearchKeepsOwnSharedChannel(t *testing.T) {
	f, lead := searchFixture(t, true)
	var own string
	f.e.pool.QueryRow(f.ctx, `SELECT channel_id FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2`, f.ws, f.brand).Scan(&own)
	f.slack.matches = []map[string]any{slackMatch(own, "partner-leads", map[string]any{"is_ext_shared": true, "is_shared": true}, "U1", "from a partner", time.Minute)}
	if data := f.search(t, f.owner, 200, lead)["data"].([]any); len(data) != 1 {
		t.Fatalf("data: %v", data)
	}
}

func TestSlackSearchQueryHardening(t *testing.T) {
	f, lead := searchFixture(t, true)
	f.exec(t, `UPDATE record_identities SET value='ana smith in:#general@acme.example' WHERE workspace_id=$1 AND namespace='email' AND value='ana@acme.example'`, f.ws)
	f.setCompany(t, lead, "Acme\n", "https://www.acme.example/")
	if q := f.search(t, f.owner, 200, lead)["query"]; q != `acme.example OR "Acme"` {
		t.Fatalf("bad email: %v", q)
	}
	long := "Acme\n\tCo\\ " + strings.Repeat("x", 300)
	f.setCompany(t, lead, long, "")
	q := f.search(t, f.owner, 200, lead)["query"].(string)
	name := strings.TrimSuffix(strings.TrimPrefix(q[strings.Index(q, `"`):], `"`), `"`)
	if strings.ContainsAny(q, "\n\t\\") || len([]rune(name)) != 100 || !strings.HasPrefix(name, "Acme Co xxx") {
		t.Fatalf("name %q in %q", name, q)
	}
}

// A search token Slack doesn't let search (a bot token) is a refusal that
// says what to change, not a service that is down, and the event carries
// Slack's own code.
func TestSlackSearchWrongTokenKind(t *testing.T) {
	f := newFeedFixture(t)
	f.integ = f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds",
		"credentials": map[string]any{"api_key": feedBotToken, "search_token": feedBotToken}})["id"].(string)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	lead := *f.itemRow(t, item).LeadID
	out := f.search(t, f.owner, http.StatusUnprocessableEntity, lead)
	e, _ := out["error"].(map[string]any)
	if e["code"] != "connector_auth" || !strings.Contains(e["message"].(string), "search:read") {
		t.Fatalf("out: %v", out)
	}
	var failed bool
	for _, ev := range f.events() {
		if ev.Name == "sales_feed.slack_search_failed" {
			failed = ev.Outcome == track.Failure && ev.ErrorCode == "connector_auth" && ev.Props["provider_code"] == "not_allowed_token_type"
		}
	}
	if !failed {
		t.Fatalf("events: %v", f.events())
	}
}
