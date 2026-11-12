package ads

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// dmFake is a Data Manager API that refuses the events whose transaction
// ids are in bad (naming the row when name is true, saying nothing about
// which row otherwise) and records every request body.
type dmFake struct {
	sync.Mutex
	bad      map[string]string
	name     bool
	refuse   string // an ErrorInfo reason that refuses the destination
	status   int
	requests []map[string]any
}

func (f *dmFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.Lock()
	f.requests = append(f.requests, body)
	f.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"code":401,"status":"UNAUTHENTICATED","message":"bad token"}}`))
		return
	}
	if r.URL.Path == "/v1/requestStatus:retrieve" {
		json.NewEncoder(w).Encode(map[string]any{"requestStatusPerDestination": []any{map[string]any{"requestStatus": "PARTIAL_SUCCESS",
			"eventsIngestionStatus": map[string]any{"recordCount": "3"},
			"errorInfo":             map[string]any{"errorCounts": []any{map[string]any{"reason": "EVENT_TIME_INVALID", "recordCount": "1", "recordIndices": []int{2}}}}}}})
		return
	}
	if f.refuse != "" {
		st := f.status
		if st == 0 {
			st = 400
		}
		w.WriteHeader(st)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": st, "status": "INVALID_ARGUMENT", "message": "refused",
			"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": f.refuse, "domain": "datamanager.googleapis.com"}}}})
		return
	}
	events, _ := body["events"].([]any)
	var violations []any
	for i, e := range events {
		id, _ := e.(map[string]any)["transactionId"].(string)
		if msg, ok := f.bad[id]; ok {
			violations = append(violations, map[string]any{"field": "events.events[" + itoa(i) + "].user_data.user_identifiers[0]", "description": msg, "reason": "INVALID_HEX_ENCODING"})
		}
	}
	if len(violations) > 0 {
		details := []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "INVALID_ARGUMENT", "domain": "datamanager.googleapis.com"}}
		if f.name {
			details = append(details, map[string]any{"@type": "type.googleapis.com/google.rpc.BadRequest", "fieldViolations": violations})
		}
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 400, "status": "INVALID_ARGUMENT", "message": "Request contains an invalid argument.", "details": details}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"requestId": "req-" + itoa(len(f.requests))})
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + json.Number(string(rune('0'+i%10))).String())
}

func dmClient(t *testing.T, f *dmFake) *c.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{"google_ads.datamanager": srv.URL}}
}

func dmConv(i int, id string, clicks map[string]string) c.AdConversion {
	v := int64(12_500_000)
	return c.AdConversion{EventID: id, Index: i, Time: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), ClickIDs: clicks,
		Emails: []string{c.HashID("ana@acme.example")}, GoogleEmails: []string{c.HashGoogleEmail("ana@acme.example")},
		Phones: []string{c.HashID("+15550100")}, ValueMicros: &v, Currency: "EUR", ConsentGranted: true, CustomerType: c.CustomerNew}
}

func TestDataManagerEvents(t *testing.T) {
	f := &dmFake{}
	cl := dmClient(t, f)
	g, creds := GoogleAds{}, c.Creds{"access_token": "tok"}
	acct := c.AdAccount{ID: "1234567890", Meta: map[string]string{"login_customer_id": "9876543210"}}
	none := c.AdConversion{EventID: "msg-3", Index: 7, Time: time.Now()}
	res, err := g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, []c.AdConversion{
		dmConv(0, "msg-1", map[string]string{"gclid": "g1"}), dmConv(1, "msg-2", map[string]string{"gcl_aw": "GCL.1700000000.Cj0-test"}), none})
	if err != nil || len(res.Results) != 3 || res.RequestID != "req-1" {
		t.Fatalf("send %+v %v", res, err)
	}
	if !res.Results[0].OK || !res.Results[1].OK || !res.Results[2].Skipped || res.Results[2].Index != 7 {
		t.Fatalf("results %+v", res.Results)
	}
	if strings.Join(res.Results[0].MatchKeys, ",") != "gclid,email,phone" {
		t.Fatalf("match keys %v", res.Results[0].MatchKeys)
	}
	raw, _ := json.Marshal(f.requests[0])
	body := string(raw)
	for _, want := range []string{`"operatingAccount":{"accountId":"1234567890","accountType":"GOOGLE_ADS"}`, `"loginAccount":{"accountId":"9876543210","accountType":"GOOGLE_ADS"}`,
		`"productDestinationId":"42"`, `"encoding":"HEX"`, `"transactionId":"msg-1"`, `"eventTimestamp":"2026-10-01T09:30:00Z"`, `"adIdentifiers":{"gclid":"g1"}`,
		`"adIdentifiers":{"gclid":"Cj0-test"}`, `"conversionValue":12.5`, `"currency":"EUR"`, `"consent":{"adUserData":"CONSENT_GRANTED"}`,
		`"userProperties":{"customerType":"NEW"}`, `"emailAddress":"` + c.HashGoogleEmail("ana@acme.example") + `"`, `"phoneNumber":"` + c.HashID("+15550100") + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, "@acme") || strings.Contains(body, "5550100") || strings.Contains(body, "adPersonalization") || strings.Contains(body, "developer") {
		t.Fatalf("something left in the clear, or too much: %s", body)
	}

	// One bad row named by Google fails alone; the rest go again.
	f.Lock()
	f.bad, f.name, f.requests = map[string]string{"msg-2": "The HEX encoded value is malformed."}, true, nil
	f.Unlock()
	var convs []c.AdConversion
	for i, id := range []string{"msg-1", "msg-2", "msg-4", "msg-5"} {
		convs = append(convs, dmConv(i, id, map[string]string{"gclid": "g"}))
	}
	res, err = g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, convs)
	if err != nil || len(f.requests) != 2 {
		t.Fatalf("named row: %v, %d requests", err, len(f.requests))
	}
	if !res.Results[0].OK || res.Results[1].OK || !strings.Contains(res.Results[1].Reason, "INVALID_HEX_ENCODING") || !res.Results[2].OK || !res.Results[3].OK {
		t.Fatalf("named row results %+v", res.Results)
	}

	// Google says a row is bad without saying which: the batch is halved
	// until the bad one stands alone.
	f.Lock()
	f.name, f.requests = false, nil
	f.Unlock()
	res, err = g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, convs)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Results[0].OK || res.Results[1].OK || !strings.Contains(res.Results[1].Reason, "refused the request") || !res.Results[2].OK || !res.Results[3].OK {
		t.Fatalf("bisect results %+v", res.Results)
	}
	// [all] refused; [1,2] refused; [1] ok; [2] refused; [4,5] ok.
	if len(f.requests) != 5 {
		t.Fatalf("bisect took %d requests", len(f.requests))
	}

	// A destination Google will not take at all is a FeedRefused, so the
	// sender can fall back to the Ads API.
	f.Lock()
	f.bad, f.refuse, f.requests = nil, "NOT_ALLOWLISTED", nil
	f.Unlock()
	_, err = g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, convs[:1])
	fr, ok := c.IsFeedRefused(err)
	if !ok || fr.Reason != "NOT_ALLOWLISTED" || c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("refused: %v", err)
	}
	f.Lock()
	f.refuse, f.status = "PERMISSION_DENIED", 403
	f.Unlock()
	_, err = g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, convs[:1])
	if fr, ok := c.IsFeedRefused(err); !ok || fr.Reason != "PERMISSION_DENIED" || c.KindOf(err) != c.ErrAuth {
		t.Fatalf("403: %v", err)
	}
	f.Lock()
	f.refuse, f.status = "RESOURCE_EXHAUSTED", 429
	f.Unlock()
	_, err = g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, convs[:1])
	if _, ok := c.IsFeedRefused(err); ok || c.KindOf(err) != c.ErrQuota {
		t.Fatalf("429: %v", err)
	}
	if _, err := g.SendConversionsFeed(ctx, cl, c.Creds{"access_token": "bad"}, acct, c.ConversionTarget{ID: "42"}, convs[:1]); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("401: %v", err)
	}

	// A restatement re-sends the event with its new value and when it
	// changed.
	f.Lock()
	f.refuse, f.status, f.requests = "", 0, nil
	f.Unlock()
	re := dmConv(0, "msg-1", map[string]string{"gclid": "g1"})
	re.Restates, re.UpdatedAt = true, time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	if _, err := g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, []c.AdConversion{re}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(f.requests[0]); !strings.Contains(string(raw), `"lastUpdatedTimestamp":"2026-10-03T08:00:00Z"`) {
		t.Fatalf("restatement body %s", raw)
	}

	st, err := g.RequestStatus(ctx, cl, creds, "req-1")
	if err != nil || st.Status != "PARTIAL_SUCCESS" || st.Records != 3 || len(st.Errors) != 1 || st.Errors[0].Reason != "EVENT_TIME_INVALID" || st.Errors[0].Indices[0] != 2 {
		t.Fatalf("status %+v %v", st, err)
	}
}

func TestDataManagerBatches(t *testing.T) {
	f := &dmFake{}
	cl := dmClient(t, f)
	g, creds, acct := GoogleAds{}, c.Creds{"access_token": "tok"}, c.AdAccount{ID: "1234567890"}
	convs := make([]c.AdConversion, dmEventsMax+1)
	for i := range convs {
		convs[i] = dmConv(i, "msg-"+itoa(i/10)+itoa(i), map[string]string{"gclid": "g"})
	}
	res, err := g.SendConversionsFeed(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, convs)
	if err != nil || len(f.requests) != 2 {
		t.Fatalf("%v, %d requests", err, len(f.requests))
	}
	if n := len(f.requests[0]["events"].([]any)); n != dmEventsMax {
		t.Fatalf("first batch %d", n)
	}
	for _, r := range res.Results {
		if !r.OK {
			t.Fatalf("result %+v", r)
		}
	}
	// The account alone: no login account is named.
	if _, has := f.requests[0]["destinations"].([]any)[0].(map[string]any)["loginAccount"]; has {
		t.Fatal("a login account was named for an account signed in to directly")
	}
}

func TestDataManagerAudiences(t *testing.T) {
	f := &dmFake{}
	cl := dmClient(t, f)
	g, creds, acct := GoogleAds{}, c.Creds{"access_token": "tok"}, c.AdAccount{ID: "1234567890"}
	em := c.HashGoogleEmail("ana@acme.example")
	m := c.AudienceMember{Emails: []string{c.HashID("ana@acme.example")}, GoogleEmails: []string{em}, Phones: []string{c.HashID("+15550100")}, ConsentGranted: true}
	id, err := g.IngestAudience(ctx, cl, creds, acct, "501", c.AudienceAdd, []c.AudienceMember{m, {}})
	if err != nil || id != "req-1" || len(f.requests) != 1 {
		t.Fatalf("add: %s %v", id, err)
	}
	raw, _ := json.Marshal(f.requests[0])
	for _, want := range []string{`"productDestinationId":"501"`, `"customerMatchTermsOfServiceStatus":"ACCEPTED"`, `"consent":{"adPersonalization":"CONSENT_GRANTED","adUserData":"CONSENT_GRANTED"}`,
		`"emailAddress":"` + em + `"`, `"encoding":"HEX"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("add body lacks %s: %s", want, raw)
		}
	}
	if n := len(f.requests[0]["audienceMembers"].([]any)); n != 1 {
		t.Fatalf("a member without identifiers was sent: %d", n)
	}
	// Removing needs no consent and says nothing about it, through the
	// journeys' one-person path too.
	m.ConsentGranted = false
	if err := g.SyncAudience(ctx, cl, creds, acct, "501", c.AudienceRemove, m); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(f.requests[1])
	if strings.Contains(string(raw), "consent") || strings.Contains(string(raw), "termsOfService") {
		t.Fatalf("remove body %s", raw)
	}
	// Nothing to send: nothing called.
	if id, err := g.IngestAudience(ctx, cl, creds, acct, "501", c.AudienceAdd, nil); err != nil || id != "" || len(f.requests) != 2 {
		t.Fatalf("empty: %s %v %d", id, err, len(f.requests))
	}
	// Big lists go in requests of ten thousand.
	many := make([]c.AudienceMember, dmMembersMax+5)
	for i := range many {
		many[i] = m
	}
	if _, err := g.IngestAudience(ctx, cl, creds, acct, "501", c.AudienceAdd, many); err != nil || len(f.requests) != 4 {
		t.Fatalf("batches: %v %d", err, len(f.requests))
	}
}

func TestGoogleAdsReconnect(t *testing.T) {
	g := GoogleAds{}
	if why := g.ReconnectNeeded(c.Creds{"access_token": "t"}); why == "" {
		t.Fatal("a sign-in of unknown scope should reconnect")
	}
	if why := g.ReconnectNeeded(c.Creds{"scope": scopeAdWords + " openid"}); why == "" {
		t.Fatal("a sign-in without the Data Manager scope should reconnect")
	}
	if why := g.ReconnectNeeded(c.Creds{"scope": scopeAdWords + " " + scopeDataManager}); why != "" {
		t.Fatal(why)
	}
	d := g.Definition()
	if !d.Has(c.CapAdsFeed) || !strings.Contains(strings.Join(d.OAuth.Scopes, " "), scopeDataManager) {
		t.Fatalf("definition: %v", d.OAuth.Scopes)
	}
	if _, ok := any(g).(c.SetupNeeder); ok {
		t.Fatal("Google Ads should ask for no developer token")
	}
	var errNil error
	if _, ok := c.IsFeedRefused(errNil); ok {
		t.Fatal("nil is not a refusal")
	}
	if _, ok := c.IsFeedRefused(errors.New("x")); ok {
		t.Fatal("a plain error is not a refusal")
	}
}
