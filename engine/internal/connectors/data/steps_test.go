package data

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const blitzPersonPath = "/v2/enrichment/person"

// blitzAsked checks one Blitz request: a POST with the workspace's key and
// exactly this body.
func blitzAsked(t *testing.T, want map[string]any, status int, answer string) func(http.ResponseWriter, map[string]any, *http.Request) {
	return func(w http.ResponseWriter, body map[string]any, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || !reflect.DeepEqual(body, want) {
			t.Errorf("%s: %s %q %v, want body %v", r.URL.Path, r.Method, r.Header.Get("Content-Type"), body, want)
		}
		blitzReply(t, status, answer)(w, body, r)
	}
}

func TestBlitzCompanyPage(t *testing.T) {
	ask := map[string]any{"domain": "acme.example"}
	for _, tc := range []struct {
		name   string
		status int
		answer string
		want   c.CompanyLinkedInPage
		kind   string
	}{
		{name: "a page", status: 200,
			answer: `{"found":true,"company_linkedin_url":"https://www.linkedin.com/company/acme-example","company_name":"Acme","fair_usage":{"records_used":1}}`,
			want:   c.CompanyLinkedInPage{Found: true, LinkedInURL: "https://www.linkedin.com/company/acme-example", Name: "Acme", Domain: "acme.example", Credits: 1}},
		{name: "a page with no usage reported costs one record", status: 200,
			answer: `{"found":true,"company_linkedin_url":"https://www.linkedin.com/company/acme-example"}`,
			want:   c.CompanyLinkedInPage{Found: true, LinkedInURL: "https://www.linkedin.com/company/acme-example", Domain: "acme.example", Credits: 1}},
		{name: "no page is free", status: 200, answer: `{"found":false,"fair_usage":{"records_used":0}}`,
			want: c.CompanyLinkedInPage{Domain: "acme.example"}},
		{name: "found without an address is no page", status: 200, answer: `{"found":true,"company_linkedin_url":null}`,
			want: c.CompanyLinkedInPage{Domain: "acme.example"}},
		{name: "found that is not true is no page", status: 200, answer: `{"found":"yes","company_linkedin_url":"https://www.linkedin.com/company/acme-example"}`,
			want: c.CompanyLinkedInPage{Domain: "acme.example"}},
		{name: "a refused key", status: 401, answer: `{"message":"Invalid API key"}`, kind: c.ErrAuth},
		{name: "no records left", status: 402, answer: `{"message":"Insufficient credits"}`, kind: c.ErrQuota},
		{name: "a locked account", status: 423, answer: `{"message":"locked"}`, kind: c.ErrQuota},
		{name: "Blitz down", status: 503, answer: `oops`, kind: c.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cl := newFake(t)
			f.routes[blitzPage] = blitzAsked(t, ask, tc.status, tc.answer)
			got, err := Blitz{}.CompanyPage(context.Background(), cl, key, "https://www.Acme.example/about")
			if tc.kind != "" {
				if err == nil || c.KindOf(err) != tc.kind {
					t.Fatalf("got %+v %v, want a %s error", got, err, tc.kind)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	// Nothing to look up: nothing is sent.
	_, cl := newFake(t)
	if _, err := (Blitz{}).CompanyPage(context.Background(), cl, key, "  "); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no domain: %v", err)
	}
	if _, ok := c.Connector(Blitz{}).(c.CompanyLocator); !ok || !(Blitz{}).Definition().Has(c.CapCompanyPage) {
		t.Fatal("Blitz does not offer the company page lookup")
	}
}

// A search from a page that is already known asks only the search: the
// page is not looked up, or paid for, again.
func TestBlitzSearchCompanyPage(t *testing.T) {
	f, cl := newFake(t)
	page := c.CompanyLinkedInPage{Found: true, LinkedInURL: "https://www.linkedin.com/company/acme-example", Name: "Acme", Domain: "acme.example"}
	f.routes[blitzSearch] = func(w http.ResponseWriter, body map[string]any, r *http.Request) {
		want := map[string]any{"company_linkedin_url": page.LinkedInURL, "max_results": 3.0,
			"cascade": []any{map[string]any{"include_title": []any{"CTO", "Head of Data"}}}}
		if r.Method != http.MethodPost || !reflect.DeepEqual(body, want) {
			t.Errorf("search: %s %v, want %v", r.Method, body, want)
		}
		blitzReply(t, 200, `{"results":[
			{"person":{"first_name":"Ana","last_name":"Lee","linkedin_url":"https://www.linkedin.com/in/ana-lee-example/",
				"experiences":[{"company_name":"Acme","job_title":"CTO","company_domain":"acme.example","job_is_current":true}]}},
			{"person":{"full_name":null}},
			{"person":"not a person"}],
			"fair_usage":{"records_used":3}}`)(w, body, r)
	}
	var _ c.CompanyPageSearcher = Blitz{}
	got, err := Blitz{}.SearchCompanyPage(context.Background(), cl, key, page, c.PeopleFilter{Titles: []string{"CTO", " Head of Data "}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Credits != 3 || got.Next != "" || len(got.People) != 1 {
		t.Fatalf("page: %+v", got)
	}
	ana := got.People[0]
	if ana.FirstName != "Ana" || ana.Title != "CTO" || ana.LinkedInURL != blitzProfile ||
		ana.Company != (c.Company{Name: "Acme", Domain: "acme.example", LinkedInURL: page.LinkedInURL}) {
		t.Fatalf("person: %+v", ana)
	}

	// Blitz's errors are the search's.
	f.routes[blitzSearch] = blitzReply(t, 402, `{"message":"Insufficient credits"}`)
	if _, err := (Blitz{}).SearchCompanyPage(context.Background(), cl, key, page, c.PeopleFilter{}, 3); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("no records left: %v", err)
	}
	// No page: nothing is sent.
	delete(f.routes, blitzSearch)
	if _, err := (Blitz{}).SearchCompanyPage(context.Background(), cl, key, c.CompanyLinkedInPage{Domain: "acme.example"}, c.PeopleFilter{}, 3); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no page: %v", err)
	}
}

func TestBlitzReadsAProfile(t *testing.T) {
	ask := map[string]any{"person_linkedin_url": blitzProfile}
	for _, tc := range []struct {
		name   string
		status int
		answer string
		want   c.PersonProfile
		kind   string
	}{
		{name: "current jobs only", status: 200, answer: `{"found":true,"person":{"headline":"CTO at Acme","experiences":[
				{"company_name":"Acme","company_linkedin_url":"https://www.linkedin.com/company/acme-example","company_domain":"acme.example","job_title":"CTO","job_is_current":true},
				{"company_name":"Globex","company_linkedin_url":"","company_domain":"globex.example","job_title":"Advisor","job_is_current":true},
				{"company_name":"Initech","job_title":"Engineer","job_is_current":false},
				{"company_name":"Hooli","job_title":"Intern","job_is_current":"true"},
				{"company_name":{"odd":1},"job_title":7,"job_is_current":true}]},
				"fair_usage":{"records_used":1}}`,
			want: c.PersonProfile{Found: true, Headline: "CTO at Acme", Cost: c.SEOCost{Units: 1}, Current: []c.ProfileRole{
				{Company: "Acme", CompanyURL: "https://www.linkedin.com/company/acme-example", Title: "CTO"},
				{Company: "Globex", CompanyURL: "https://globex.example", Title: "Advisor"},
				{}}}},
		{name: "a profile with no jobs", status: 200, answer: `{"found":true,"person":{"headline":null}}`,
			want: c.PersonProfile{Found: true, Cost: c.SEOCost{Units: 1}, Current: []c.ProfileRole{}}},
		{name: "no such profile is free", status: 200, answer: `{"found":false}`, want: c.PersonProfile{Current: []c.ProfileRole{}}},
		{name: "found without a person is no profile", status: 200, answer: `{"found":true}`, want: c.PersonProfile{Current: []c.ProfileRole{}}},
		{name: "a refused key", status: 401, answer: `{"message":"Invalid API key"}`, kind: c.ErrAuth},
		{name: "no records left", status: 402, answer: `{"message":"Insufficient credits"}`, kind: c.ErrQuota},
		{name: "Blitz down", status: 500, answer: `oops`, kind: c.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cl := newFake(t)
			f.routes[blitzPersonPath] = blitzAsked(t, ask, tc.status, tc.answer)
			got, err := Blitz{}.ReadProfile(context.Background(), cl, key, "www.linkedin.com/in/ana-lee-example/?utm=x")
			if tc.kind != "" {
				if err == nil || c.KindOf(err) != tc.kind {
					t.Fatalf("got %+v %v, want a %s error", got, err, tc.kind)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	_, cl := newFake(t)
	if _, err := (Blitz{}).ReadProfile(context.Background(), cl, key, "https://www.linkedin.com/company/acme-example"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a company page: %v", err)
	}
	if _, ok := c.Connector(Blitz{}).(c.ProfileReader); !ok || !(Blitz{}).Definition().Has(c.CapReadProfile) {
		t.Fatal("Blitz does not offer reading a profile")
	}
}

func TestBlitzFindStep(t *testing.T) {
	if got := (Blitz{}).FindSteps(); !reflect.DeepEqual(got, []c.FindStep{{ID: "blitz", NeedsProfile: true}}) {
		t.Fatalf("steps: %+v", got)
	}
	ask := map[string]any{"person_linkedin_url": blitzProfile}
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile + "/"}
	for _, tc := range []struct {
		name   string
		status int
		answer string
		want   c.StepAnswer
		kind   string
	}{
		// Every address, the first one first, whatever company it is at.
		{name: "every address", status: 200, answer: `{"found":true,"email":"Ana@Globex.example","all_emails":[
				{"email":"ana@globex.example"},{"email":"ana.lee@acme.example"},{"email":"not an address"},{"email":7},{"email":"ANA.LEE@acme.example"}],
				"fair_usage":{"records_used":1}}`,
			want: c.StepAnswer{Addresses: []string{"ana@globex.example", "ana.lee@acme.example"}, Credits: 1}},
		{name: "a list that is not one", status: 200, answer: `{"found":true,"email":"ana@acme.example","all_emails":"none"}`,
			want: c.StepAnswer{Addresses: []string{"ana@acme.example"}, Credits: 1}},
		{name: "a miss is free", status: 200, answer: `{"found":false,"email":"ana@acme.example","fair_usage":{"records_used":0}}`, want: c.StepAnswer{}},
		{name: "a refused key", status: 401, answer: `{"message":"Invalid API key"}`, kind: c.ErrAuth},
		{name: "no records left", status: 402, answer: `{"message":"Insufficient credits"}`, kind: c.ErrQuota},
		{name: "Blitz down", status: 502, answer: `oops`, kind: c.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cl := newFake(t)
			f.routes[blitzEmail] = blitzAsked(t, ask, tc.status, tc.answer)
			got, err := Blitz{}.FindStep(context.Background(), cl, key, "blitz", q)
			if tc.kind != "" {
				if err == nil || c.KindOf(err) != tc.kind {
					t.Fatalf("got %+v %v, want a %s error", got, err, tc.kind)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	// An unknown step, or no profile: nothing is sent.
	_, cl := newFake(t)
	if _, err := (Blitz{}).FindStep(context.Background(), cl, key, "filtrat", q); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("an unknown step: %v", err)
	}
	if _, err := (Blitz{}).FindStep(context.Background(), cl, key, "blitz", c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no profile: %v", err)
	}
}

func TestTregFindSteps(t *testing.T) {
	want := []c.FindStep{{ID: "blitz", NeedsProfile: true}, {ID: "filtrat"}, {ID: "kitt"}, {ID: "anymailfinder"}}
	if got := (Treg{}).FindSteps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("steps: %+v", got)
	}
	var _ c.StepFinder = Treg{}
}

// One step is one route: the same request the chain sends it, and nothing
// else is asked whatever it says.
func TestTregFindStep(t *testing.T) {
	tregShortWait(t)
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "www.Acme.example", LinkedInURL: blitzProfile}
	for _, tc := range []struct {
		name, step, path, maxCost string
		body                      map[string]any
		reply                     tregHandler
		want                      c.StepAnswer
		kind                      string
		timeout, busy             bool
	}{
		{name: "blitz returns every address", step: "blitz", path: tregBlitzPath, maxCost: "0.01",
			body:  map[string]any{"person_linkedin_url": blitzProfile},
			reply: tregReply(200, "8000", `{"found":true,"email":"ana@globex.example","all_emails":[{"email":"Ana.Lee@acme.example"},{"email":"ana@globex.example"},{"email":"nope"}]}`),
			want:  c.StepAnswer{Addresses: []string{"ana@globex.example", "ana.lee@acme.example"}, Credits: 1}},
		{name: "blitz miss", step: "blitz", path: tregBlitzPath, maxCost: "0.01",
			body: map[string]any{"person_linkedin_url": blitzProfile}, reply: tregMissBlitz, want: c.StepAnswer{}},
		{name: "filtrat hit", step: "filtrat", path: tregFiltratPath, maxCost: "0.01",
			body:  map[string]any{"first_name": "Ana", "last_name": "Lee", "domain": "acme.example", "resolve_catchall": true},
			reply: tregReply(200, "9000", `{"status":"found","found_email":"Ana.Lee@acme.example"}`),
			want:  c.StepAnswer{Addresses: []string{"ana.lee@acme.example"}, Credits: 1}},
		{name: "filtrat miss, not billed", step: "filtrat", path: tregFiltratPath, maxCost: "0.01",
			body:  map[string]any{"first_name": "Ana", "last_name": "Lee", "domain": "acme.example", "resolve_catchall": true},
			reply: tregMissFiltrat, want: c.StepAnswer{}},
		{name: "kitt says it checked the mailbox", step: "kitt", path: tregKittPath, maxCost: "0.01",
			body:  map[string]any{"fullName": "Ana Lee", "domain": "acme.example", "realtime": true},
			reply: tregReply(200, "5000", `{"email":"ana@acme.example","validity":"valid"}`),
			want:  c.StepAnswer{Addresses: []string{"ana@acme.example"}, Verified: true, Credits: 1}},
		{name: "kitt miss that was billed", step: "kitt", path: tregKittPath, maxCost: "0.01",
			body:  map[string]any{"fullName": "Ana Lee", "domain": "acme.example", "realtime": true},
			reply: tregReply(200, "5000", `{"email":"no-results-found"}`), want: c.StepAnswer{Credits: 1}},
		{name: "anymailfinder's checked address", step: "anymailfinder", path: tregAnyPath, maxCost: "0.03",
			body:  map[string]any{"domain": "acme.example", "full_name": "Ana Lee"},
			reply: tregReply(200, "", `{"email":"a.lee@acme.example","email_status":"risky","valid_email":"ana@acme.example"}`),
			want:  c.StepAnswer{Addresses: []string{"ana@acme.example"}, Verified: true, Credits: 1}},
		{name: "still running at Treg is pending and billed", step: "anymailfinder", path: tregAnyPath, maxCost: "0.03",
			body:  map[string]any{"domain": "acme.example", "full_name": "Ana Lee"},
			reply: tregReply(202, "", `{"_treg":{"outcome":"pending"}}`), want: c.StepAnswer{Pending: true, Credits: 1}},
		{name: "a refused token", step: "filtrat", path: tregFiltratPath, maxCost: "0.01",
			body:  map[string]any{"first_name": "Ana", "last_name": "Lee", "domain": "acme.example", "resolve_catchall": true},
			reply: tregReply(401, "", `{"detail":"invalid token"}`), kind: c.ErrAuth},
		{name: "an empty balance", step: "kitt", path: tregKittPath, maxCost: "0.01",
			body:  map[string]any{"fullName": "Ana Lee", "domain": "acme.example", "realtime": true},
			reply: tregReply(402, "", `{"detail":"insufficient balance"}`), kind: c.ErrQuota},
		{name: "a rate limit", step: "kitt", path: tregKittPath, maxCost: "0.01",
			body:  map[string]any{"fullName": "Ana Lee", "domain": "acme.example", "realtime": true},
			reply: tregReply(429, "", `{"detail":{"error":"rate_limited","retry_after_s":30}}`), kind: c.ErrUnavailable, busy: true},
		{name: "the provider down", step: "filtrat", path: tregFiltratPath, maxCost: "0.01",
			body:  map[string]any{"first_name": "Ana", "last_name": "Lee", "domain": "acme.example", "resolve_catchall": true},
			reply: tregReply(502, "", `bad gateway`), kind: c.ErrUnavailable},
		{name: "no answer in time", step: "filtrat", path: tregFiltratPath, maxCost: "0.01",
			body:  map[string]any{"first_name": "Ana", "last_name": "Lee", "domain": "acme.example", "resolve_catchall": true},
			reply: tregSlow(`{"status":"found","found_email":"ana@acme.example"}`), kind: c.ErrUnavailable, timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Only this route is served: any other call fails the test.
			f, cl, order := tregChainFake(t, map[string]tregHandler{tc.path: func(w http.ResponseWriter, body map[string]any, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("X-Treg-Token") != "trg_test_not_a_real_token" ||
					r.Header.Get("X-Treg-Route-Max-Cost") != tc.maxCost || !reflect.DeepEqual(body, tc.body) {
					t.Errorf("request: %s max cost %q body %v, want %v", r.Method, r.Header.Get("X-Treg-Route-Max-Cost"), body, tc.body)
				}
				tc.reply(w, body, r)
			}})
			_ = f
			got, err := Treg{}.FindStep(context.Background(), cl, tregKey, tc.step, q)
			if n := len(order()); n != 1 {
				t.Errorf("%d calls, want one", n)
			}
			if tc.kind != "" {
				if _, busy := c.IsBusy(err); err == nil || c.KindOf(err) != tc.kind || c.IsTimeout(err) != tc.timeout || busy != tc.busy {
					t.Fatalf("got %+v %v, want a %s error (timeout %v, busy %v)", got, err, tc.kind, tc.timeout, tc.busy)
				}
				return
			}
			got.CostMicros = 0 // TestTregReportsWhatItCost
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	// Nothing is sent for a step Treg does not have, or one the query
	// cannot be asked for.
	_, cl := newFake(t)
	for name, tc := range map[string]struct {
		step string
		q    c.PersonQuery
	}{
		"an unknown step":           {"prospeo", q},
		"blitz without a profile":   {"blitz", c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example"}},
		"filtrat without a name":    {"filtrat", c.PersonQuery{Domain: "acme.example", LinkedInURL: blitzProfile}},
		"kitt without a domain":     {"kitt", c.PersonQuery{FirstName: "Ana", LastName: "Lee"}},
		"blitz with a company page": {"blitz", c.PersonQuery{LinkedInURL: "https://www.linkedin.com/company/acme-example"}},
	} {
		if _, err := (Treg{}).FindStep(context.Background(), cl, tregKey, tc.step, tc.q); c.KindOf(err) != c.ErrInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Blitz by profile needs no name.
	_, cl, _ = tregChainFake(t, map[string]tregHandler{tregBlitzPath: tregReply(200, "8000", `{"found":true,"email":"ana@acme.example"}`)})
	if got, err := (Treg{}).FindStep(context.Background(), cl, tregKey, "blitz", c.PersonQuery{LinkedInURL: blitzProfile}); err != nil || len(got.Addresses) != 1 {
		t.Fatalf("blitz by profile alone: %+v %v", got, err)
	}
}

// When Filtrat is unavailable, the workspace's other verifier is asked
// before BounceBan.
func TestTregVerifyWithAFallback(t *testing.T) {
	oldPoll := tregVerifyPoll
	tregVerifyPoll = time.Millisecond
	t.Cleanup(func() { tregVerifyPoll = oldPoll })
	const email = "ana@acme-io.example"
	down := tregReply(502, "", `bad gateway`)
	pending := tregReply(202, "", `{"_treg":{"outcome":"pending"}}`)
	bb := func(result string) tregHandler {
		return tregReply(200, "4000", `{"id":"bb_1","status":"success","result":"`+result+`","score":88}`)
	}
	score := func(n int) *int { return &n }
	says := func(v c.Verification, err error) func(context.Context, string) (c.Verification, error) {
		return func(context.Context, string) (c.Verification, error) { return v, err }
	}
	for _, tc := range []struct {
		name     string
		filtrat  tregHandler
		fallback func(context.Context, string) (c.Verification, error)
		asked    bool        // the fallback was asked
		bb       tregHandler // nil: BounceBan must not be asked
		want     c.Verification
		kind     string
	}{
		{name: "a real answer from Filtrat never asks the fallback",
			filtrat:  tregReply(200, "1250", `{"status":"invalid","domain_intelligence":{"catch_all":false}}`),
			fallback: says(c.Verification{Status: c.Deliverable, NotCatchAll: true}, nil), bb: bb("undeliverable"),
			want: c.Verification{Status: c.Undeliverable, Reason: "Filtrat: invalid, BounceBan: undeliverable", ProviderStatus: "filtrat:invalid/bounceban:undeliverable", Score: score(88)}},
		{name: "Filtrat's pass never asks the fallback",
			filtrat:  tregReply(200, "1250", `{"status":"verified","confidence":97.6}`),
			fallback: says(c.Verification{Status: c.Undeliverable}, nil),
			want:     c.Verification{Status: c.Deliverable, Reason: "Filtrat: verified", ProviderStatus: "filtrat:verified", Score: score(98)}},
		{name: "fallback says undeliverable", filtrat: down, asked: true,
			fallback: says(c.Verification{Status: c.Undeliverable, Reason: "rejected_email", Score: score(3)}, nil),
			want:     c.Verification{Status: c.Undeliverable, Reason: "rejected_email (Filtrat: no answer)", ProviderStatus: "filtrat:no answer/fallback:undeliverable", Score: score(3)}},
		{name: "fallback says deliverable on a domain that is not accept-all", filtrat: pending, asked: true,
			fallback: says(c.Verification{Status: c.Deliverable, Reason: "accepted_email", NotCatchAll: true}, nil),
			want:     c.Verification{Status: c.Deliverable, Reason: "accepted_email (Filtrat: no answer)", NotCatchAll: true, ProviderStatus: "filtrat:no answer/fallback:deliverable"}},
		{name: "fallback says deliverable without saying the domain is not accept-all: BounceBan decides", filtrat: down, asked: true,
			fallback: says(c.Verification{Status: c.Deliverable, Reason: "accepted_email"}, nil), bb: bb("undeliverable"),
			want: c.Verification{Status: c.Undeliverable, Reason: "accepted_email, BounceBan: undeliverable", ProviderStatus: "filtrat:no answer/fallback:deliverable/bounceban:undeliverable", Score: score(88)}},
		{name: "fallback says deliverable on an accept-all domain: BounceBan decides", filtrat: down, asked: true,
			fallback: says(c.Verification{Status: c.Deliverable, CatchAll: true}, nil), bb: bb("deliverable"),
			want: c.Verification{Status: c.Deliverable, Reason: "deliverable, BounceBan: deliverable", ProviderStatus: "filtrat:no answer/fallback:deliverable/bounceban:deliverable", Score: score(88)}},
		{name: "fallback says risky on an accept-all domain: BounceBan decides", filtrat: down, asked: true,
			fallback: says(c.Verification{Status: c.Risky, Reason: "low_deliverability", CatchAll: true}, nil), bb: bb("risky"),
			want: c.Verification{Status: c.Risky, CatchAll: true, Reason: "low_deliverability, BounceBan: risky", ProviderStatus: "filtrat:no answer/fallback:risky/bounceban:risky", Score: score(88)}},
		{name: "fallback says risky on a domain that is not accept-all: risky", filtrat: down, asked: true,
			fallback: says(c.Verification{Status: c.Risky, Reason: "low_quality", NotCatchAll: true}, nil),
			want:     c.Verification{Status: c.Risky, Reason: "low_quality (Filtrat: no answer)", NotCatchAll: true, ProviderStatus: "filtrat:no answer/fallback:risky"}},
		{name: "fallback can't tell: as without one", filtrat: down, asked: true,
			fallback: says(c.Verification{Status: c.Unknown, Reason: "timeout"}, nil), bb: bb("deliverable"),
			want: c.Verification{Status: c.Deliverable, Reason: "Filtrat: no answer, BounceBan: deliverable", ProviderStatus: "filtrat:no answer/bounceban:deliverable", Score: score(88)}},
		{name: "fallback fails: as without one", filtrat: down, asked: true,
			fallback: says(c.Verification{}, errors.New("down too")), bb: bb("risky"),
			want: c.Verification{Status: c.Risky, Reason: "Filtrat: no answer, BounceBan: risky", ProviderStatus: "filtrat:no answer/bounceban:risky", Score: score(88)}},
		{name: "BounceBan down after the fallback is never a verdict", filtrat: down, asked: true,
			fallback: says(c.Verification{Status: c.Deliverable}, nil), bb: tregReply(502, "", `bad gateway`), kind: c.ErrUnavailable},
		{name: "a refused token never asks the fallback", filtrat: tregReply(401, "", `{"detail":"invalid token"}`),
			fallback: says(c.Verification{Status: c.Deliverable, NotCatchAll: true}, nil), kind: c.ErrAuth},
		{name: "an empty balance never asks the fallback", filtrat: tregReply(402, "", `{"detail":"insufficient balance"}`),
			fallback: says(c.Verification{Status: c.Deliverable, NotCatchAll: true}, nil), kind: c.ErrQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cl := newFake(t)
			f.routes[tregVerifyPath] = tc.filtrat
			if tc.bb != nil {
				f.routes[tregBBPath] = tc.bb
			}
			asked := ""
			got, err := Treg{}.VerifyWith(context.Background(), cl, tregKey, email, func(ctx context.Context, e string) (c.Verification, error) {
				asked = e
				return tc.fallback(ctx, e)
			})
			if (asked != "") != tc.asked || (tc.asked && asked != email) {
				t.Errorf("fallback asked about %q, want asked %v", asked, tc.asked)
			}
			if tc.kind != "" {
				if err == nil || c.KindOf(err) != tc.kind {
					t.Fatalf("got %+v %v, want a %s error", got, err, tc.kind)
				}
				return
			}
			tc.want.Email = email
			got.CostMicros = 0 // TestTregReportsWhatItCost
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	var _ c.FallbackVerifier = Treg{}
}
