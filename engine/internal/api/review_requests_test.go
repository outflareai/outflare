package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/reviewrequests"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestReviewRequests(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	guardrailsOff(t, owner, ws)
	fake := &fakeResend{verified: true}
	server := httptest.NewServer(fake)
	defer server.Close()
	e.integrations.Client.BaseURLs["resend"] = server.URL
	mail := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Resolver: e.dns}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	domain := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})["id"].(string)
	e.dns.setTXT("resend._domainkey.example.com", []string{"p=KEY"})
	if err := mail.RunDomainCheck(ctx, domain); err != nil {
		t.Fatal(err)
	}
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})["id"].(string)
	route := "/v1/brands/" + brand + "/review-requests"
	defaults := owner.must(200, "GET", route+"/settings", nil)
	if len(defaults["platforms"].([]any)) != 21 || defaults["settings"].(map[string]any)["interval_days"] != 90.0 {
		t.Fatalf("defaults=%v", defaults)
	}
	settings := reviewrequests.Defaults()
	settings.From = "reviews@example.com"
	settings.Destinations = []reviewrequests.Destination{{Platform: "google_reviews", URL: "https://search.google.com/local/writereview?placeid=example"}, {Platform: "trustpilot", URL: "https://www.trustpilot.com/review/northpeak.example"}}
	owner.must(200, "PUT", route+"/settings", settings)
	person := func(address, b string) string {
		t.Helper()
		id, err := customers.EnsurePerson(ctx, e.pool, ws, "email", address, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.pool.Exec(ctx, `UPDATE records SET brand_id=$2,origin='first_party',attributes='{"plan":"pro","first_name":"Ana"}' WHERE id=$1`, id, b); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ana := person("ana@example.org", brand)
	bo := person("bo@example.org", brand)
	foreign := person("cy@example.org", other)
	cfg := reviewrequests.Config{Name: "Post purchase", Mode: "manual", Subject: "How was {{brand.name}}?", Body: "Hi {{person.first_name|there}}, please share an honest review.", Platforms: []string{"google_reviews"}, Audience: reviewrequests.Audience{Kind: "records", RecordIDs: []string{ana, bo, foreign}}}
	c := owner.must(201, "POST", route+"/campaigns", cfg)
	cid := c["id"].(string)
	cr := route + "/campaigns/" + cid
	owner.must(404, "PUT", "/v1/brands/"+other+"/review-requests/campaigns/"+cid, cfg)
	bad := cfg
	bad.Platforms = []string{"yelp"}
	owner.must(400, "POST", route+"/campaigns", bad)
	preview := owner.must(200, "POST", cr+"/preview", nil)
	if preview["total"] != 2.0 || preview["eligible"] != 2.0 {
		t.Fatalf("preview=%v", preview)
	}
	owner.must(200, "POST", cr+"/test", map[string]any{"to": "test@example.com"})
	when := time.Now().Add(time.Hour)
	owner.must(200, "POST", cr+"/action", map[string]any{"action": "launch", "send_at": when})
	owner.must(409, "POST", cr+"/action", map[string]any{"action": "launch"})
	owner.must(409, "PUT", cr, cfg)
	requests := owner.must(200, "GET", route+"?campaign="+cid, nil)["data"].([]any)
	if len(requests) != 2 {
		t.Fatalf("requests=%v", requests)
	}
	svc := &reviewrequests.Service{Pool: e.pool, Mail: mail}
	queue := func(id string) (string, string) {
		t.Helper()
		var state, detail string
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			state, detail, err = svc.QueueOne(ctx, tx, ws, brand, id)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return state, detail
	}
	rid := requests[0].(map[string]any)["id"].(string)
	state, email := queue(rid)
	if state != "done" {
		t.Fatalf("queued=%s %s", state, email)
	}
	if state, _ = queue(rid); state != "skipped" {
		t.Fatal("retry duplicated a request")
	}
	owner.must(200, "POST", cr+"/action", map[string]any{"action": "pause"})
	var emailState string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM emails WHERE id=$1`, email).Scan(&emailState); err != nil || emailState != "cancelled" {
		t.Fatalf("pause=%s %v", emailState, err)
	}
	owner.must(200, "POST", cr+"/action", map[string]any{"action": "resume"})
	state, email = queue(rid)
	if state != "done" {
		t.Fatalf("resume=%s %s", state, email)
	}
	// Provider calls stay fake. Existing policy and consent still run at send time.
	if err := mail.Send(ctx, email, true); err != nil {
		t.Fatal(err)
	}
	var link string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM tracked_links WHERE email_id=$1 AND url=$2`, email, settings.Destinations[0].URL).Scan(&link); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO link_clicks(workspace_id,link_id,likely_bot) VALUES($1,$2,true),($1,$2,false)`, ws, link); err != nil {
		t.Fatal(err)
	}
	report := owner.must(200, "GET", route+"?campaign="+cid, nil)["report"].(map[string]any)
	if report["sent"] != 1.0 || report["clicked"] != 1.0 || len(report["platforms"].([]any)) != 1 {
		t.Fatalf("report=%v", report)
	}
	owner.must(400, "GET", route+"?cursor=bad", nil)
	// Once-ever applies across workflows. Multiple workers cannot both reserve
	// the remaining customer's invitation, even across distinct source events.
	settings.Frequency = "once"
	owner.must(200, "PUT", route+"/settings", settings)
	workflow := cfg
	workflow.Name = "After onboarding"
	workflow.Mode = "workflow"
	workflow.Platforms = []string{"google_reviews", "trustpilot"}
	wc := owner.must(201, "POST", route+"/campaigns", workflow)["id"].(string)
	owner.must(200, "POST", route+"/campaigns/"+wc+"/action", map[string]any{"action": "launch"})
	var remaining string
	for _, v := range requests {
		v := v.(map[string]any)
		if v["id"] != rid {
			remaining = v["record_id"].(string)
		}
	}
	var wg sync.WaitGroup
	out := make(chan string, 2)
	for _, source := range []string{"run:a", "run:b"} {
		wg.Add(1)
		go func(source string) {
			defer wg.Done()
			var state string
			err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				var err error
				state, _, err = svc.FromWorkflow(ctx, tx, ws, brand, wc, remaining, source)
				return err
			})
			if err != nil {
				out <- err.Error()
			} else {
				out <- state
			}
		}(source)
	}
	wg.Wait()
	close(out)
	done, skipped := 0, 0
	for state := range out {
		if state == "done" {
			done++
		} else if state == "skipped" {
			skipped++
		} else {
			t.Fatalf("concurrent request=%s", state)
		}
	}
	if done != 1 || skipped != 1 {
		t.Fatalf("done=%d skipped=%d", done, skipped)
	}
	// Per-platform once-ever allows the other destination only.
	settings.FrequencyScope = "platform"
	owner.must(200, "PUT", route+"/settings", settings)
	var firstRec string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM review_requests WHERE id=$1`, rid).Scan(&firstRec); err != nil {
		t.Fatal(err)
	}
	var newEmail string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		state, detail, err := svc.FromWorkflow(ctx, tx, ws, brand, wc, firstRec, "run:c")
		if err == nil && state != "done" {
			t.Errorf("per platform=%s %s", state, detail)
		}
		newEmail = detail
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := e.pool.QueryRow(ctx, `SELECT text_body FROM emails WHERE id=$1`, newEmail).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Leave a review on Google") || !strings.Contains(text, "Leave a review on Trustpilot") {
		t.Fatalf("wrong remaining destination: %s", text)
	}
	// Workflow brand mismatch cannot create a request.
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		state, _, err := svc.FromWorkflow(ctx, tx, ws, other, wc, foreign, "run:foreign")
		if state != "failed" {
			t.Errorf("foreign=%s", state)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", cr+"/action", map[string]any{"action": "duplicate"})
	owner.must(200, "POST", cr+"/action", map[string]any{"action": "archive"})
	// Resolve lists without refreshing memberships, and exercise the real
	// dispatch handler. Email providers are not registered on this queue.
	settings.Frequency = "unlimited"
	owner.must(200, "PUT", route+"/settings", settings)
	list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Product users", "brand_id": brand, "rules": []map[string]any{{"attribute": "plan", "op": "eq", "value": "pro"}}})["id"].(string)
	listed := cfg
	listed.Name = "Product users feedback"
	listed.Audience = reviewrequests.Audience{Kind: "list", ListID: list}
	lc := owner.must(201, "POST", route+"/campaigns", listed)["id"].(string)
	pv := owner.must(200, "POST", route+"/campaigns/"+lc+"/preview", nil)
	if pv["total"] != 2.0 {
		t.Fatalf("list preview=%v", pv)
	}
	owner.must(200, "POST", route+"/campaigns/"+lc+"/action", map[string]any{"action": "launch"})
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{reviewrequests.JobDispatch}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(stopped) }()
	for n := 0; ; n++ {
		var queued int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM review_requests WHERE campaign_id=$1 AND status='queued'`, lc).Scan(&queued); err != nil {
			cancel()
			<-stopped
			t.Fatal(err)
		}
		if queued == 2 {
			break
		}
		if n > 200 {
			cancel()
			<-stopped
			t.Fatalf("dispatcher queued %d requests", queued)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-stopped
	// Consent withdrawn after queueing must still stop an invitation.
	var queuedEmail, queuedRecord string
	if err := e.pool.QueryRow(ctx, `SELECT email_id::text,record_id::text FROM review_requests WHERE campaign_id=$1 LIMIT 1`, lc).Scan(&queuedEmail, &queuedRecord); err != nil {
		t.Fatal(err)
	}
	if err := customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: queuedRecord, Channel: "email", Status: customers.Withdrawn}); err != nil {
		t.Fatal(err)
	}
	if err := mail.Send(ctx, queuedEmail, true); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT status FROM emails WHERE id=$1`, queuedEmail).Scan(&emailState); err != nil || emailState != "suppressed" {
		t.Fatalf("withdrawn queued send=%s %v", emailState, err)
	}
	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	if !seen["review_request.queued"] || !seen["review_campaign.launched"] {
		t.Fatal("missing review events")
	}
}
