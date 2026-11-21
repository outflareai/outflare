package data

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const (
	blitzKeyInfo = "/v2/account/key-info"
	blitzPage    = "/v2/enrichment/domain-to-linkedin"
	blitzSearch  = "/v2/search/waterfall-icp-keyword"
	blitzEmail   = "/v2/enrichment/email"
	blitzProfile = "https://www.linkedin.com/in/ana-lee-example"
)

// blitzReply answers with a status and body, and fails the test when the
// request does not carry the workspace's key.
func blitzReply(t *testing.T, status int, body string) func(http.ResponseWriter, map[string]any, *http.Request) {
	return func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("%s: key header = %q", r.URL.Path, r.Header.Get("x-api-key"))
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func blitzNoPause(t *testing.T) {
	old := blitzRetryWait
	blitzRetryWait = time.Millisecond
	t.Cleanup(func() { blitzRetryWait = old })
}

func TestBlitzTestReadsThePlan(t *testing.T) {
	f, cl := newFake(t)
	f.routes[blitzKeyInfo] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("x-api-key") != "k" {
			t.Errorf("request: %s %v", r.Method, r.Header)
		}
		io.WriteString(w, `{"valid":true,"id":"key_example","records_remaining":950,"next_reset_at":null,
			"active_plans":[{"name":"Start","status":"active"}]}`)
	}
	acct, err := Blitz{}.Test(context.Background(), cl, key)
	if err != nil || acct.ID != "key_example" || acct.Name != "Blitz" || acct.Credits == nil || *acct.Credits != 950 || acct.Details["plan"] != "start" {
		t.Fatalf("account: %+v %v", acct, err)
	}

	f.routes[blitzKeyInfo] = blitzReply(t, 200, `{"valid":false}`)
	if _, err := (Blitz{}).Test(context.Background(), cl, key); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a key Blitz calls invalid: %v", err)
	}
	f.routes[blitzKeyInfo] = blitzReply(t, 401, `{"success":false,"message":"Invalid API key"}`)
	if _, err := (Blitz{}).Test(context.Background(), cl, key); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a refused key: %v", err)
	}
}

func TestBlitzSearchFindsPeopleAtACompany(t *testing.T) {
	f, cl := newFake(t)
	f.routes[blitzPage] = blitzReply(t, 200, `{"found":true,"company_linkedin_url":"https://www.linkedin.com/company/acme-example","company_name":"Acme",
		"fair_usage":{"records_used":1}}`)
	f.routes[blitzSearch] = blitzReply(t, 200, `{"results_length":3,"results":[
		{"icp":1,"ranking":1,"person":{"first_name":"Ana","last_name":"Lee","full_name":"Ana Lee","headline":"Building Acme",
			"linkedin_url":"https://www.linkedin.com/in/ana-lee-example/","location":{"city":"Sydney","state_code":"NSW","country_code":"AU"},
			"experiences":[
				{"company_name":"Globex","job_title":"Advisor","company_domain":"globex.example","job_is_current":true},
				{"company_name":"Acme","job_title":"Chief Executive Officer","company_domain":"acme.example","job_is_current":true},
				{"company_name":"Initech","job_title":"Engineer","company_domain":"initech.example","job_is_current":false}]}},
		{"icp":1,"ranking":2,"person":{"full_name":"Ben van Ito","headline":"Co-founder at Acme","linkedin_url":"linkedin.com/in/ben-ito-example",
			"email":"Ben@Acme.example","experiences":[]}},
		{"icp":1,"ranking":3,"person":{"full_name":null,"linkedin_url":"https://www.linkedin.com/in/nameless-example","email":"cy@elsewhere.example"}}],
		"fair_usage":{"records_used":3}}`)

	filter := c.PeopleFilter{CompanyDomains: []string{"https://www.acme.example/", "acme.example", "globex.example"}}
	page, err := Blitz{}.SearchPeople(context.Background(), cl, key, filter, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.bodies[blitzPage]; got["domain"] != "acme.example" || len(got) != 1 {
		t.Fatalf("company lookup body: %v", got)
	}
	body := f.bodies[blitzSearch]
	if body["company_linkedin_url"] != "https://www.linkedin.com/company/acme-example" || body["max_results"] != 5.0 || len(body) != 3 {
		t.Fatalf("search body: %v", body)
	}
	// No title and no seniority asked for: the decision makers, in tiers.
	tiers := body["cascade"].([]any)
	if len(tiers) != 3 || tiers[0].(map[string]any)["include_title"].([]any)[0] != "founder" ||
		tiers[2].(map[string]any)["exclude_title"].([]any)[0] != "assistant" {
		t.Fatalf("cascade: %v", tiers)
	}
	if _, has := tiers[0].(map[string]any)["exclude_title"]; has {
		t.Fatalf("an empty exclude list was sent: %v", tiers[0])
	}
	// One record for the company page and three for the people; the next
	// page is the next company (the same domain twice is one company).
	if page.Credits != 4 || page.Next != "1" || len(page.People) != 2 {
		t.Fatalf("page: %+v", page)
	}
	ana := page.People[0]
	if ana.FirstName != "Ana" || ana.LastName != "Lee" || ana.Title != "Chief Executive Officer" ||
		ana.LinkedInURL != blitzProfile || ana.Email != "" || ana.HasEmail ||
		ana.City != "Sydney" || ana.State != "NSW" || ana.Country != "AU" ||
		ana.Company.Domain != "acme.example" || ana.Company.Name != "Acme" || ana.Company.LinkedInURL != "https://www.linkedin.com/company/acme-example" {
		t.Fatalf("ana: %+v", ana)
	}
	// A name in one field is split, the headline stands in for a title, and
	// an address at the company is kept but never called verified.
	ben := page.People[1]
	if ben.FirstName != "Ben" || ben.LastName != "van Ito" || ben.Title != "Co-founder at Acme" ||
		ben.LinkedInURL != "https://linkedin.com/in/ben-ito-example" || ben.Email != "ben@acme.example" || ben.EmailStatus != c.EmailGuessed || !ben.HasEmail {
		t.Fatalf("ben: %+v", ben)
	}

	// The last company has no next page. Blitz says nothing of what it
	// used: one record for the page and one for each person.
	f.routes[blitzPage] = blitzReply(t, 200, `{"found":true,"company_linkedin_url":"https://www.linkedin.com/company/globex-example"}`)
	f.routes[blitzSearch] = blitzReply(t, 200, `{"results":[{"person":{"first_name":"Di","last_name":"Evans","linkedin_url":"https://www.linkedin.com/in/di-evans-example"}}]}`)
	page, err = Blitz{}.SearchPeople(context.Background(), cl, key, filter, "1", 500)
	if err != nil || page.Next != "" || page.Credits != 2 || len(page.People) != 1 || page.People[0].Company.Domain != "globex.example" {
		t.Fatalf("second company: %+v %v", page, err)
	}
	if f.bodies[blitzPage]["domain"] != "globex.example" || f.bodies[blitzSearch]["max_results"] != 100.0 {
		t.Fatalf("second company bodies: %v %v", f.bodies[blitzPage], f.bodies[blitzSearch])
	}
}

func TestBlitzSearchTitlesAndSeniorities(t *testing.T) {
	f, cl := newFake(t)
	f.routes[blitzPage] = blitzReply(t, 200, `{"found":true,"company_linkedin_url":"https://www.linkedin.com/company/acme-example"}`)
	f.routes[blitzSearch] = blitzReply(t, 200, `{"results":[]}`)
	include := func(i int) []any {
		return f.bodies[blitzSearch]["cascade"].([]any)[i].(map[string]any)["include_title"].([]any)
	}

	// Titles are the search, whatever the seniority.
	filter := c.PeopleFilter{CompanyDomains: []string{"acme.example"}, Titles: []string{" Head of Sales ", "", "VP Sales"}, Seniorities: []string{"vp"}}
	if _, err := (Blitz{}).SearchPeople(context.Background(), cl, key, filter, "", 3); err != nil {
		t.Fatal(err)
	}
	if n := len(f.bodies[blitzSearch]["cascade"].([]any)); n != 1 || len(include(0)) != 2 || include(0)[0] != "Head of Sales" {
		t.Fatalf("titles: %v", f.bodies[blitzSearch]["cascade"])
	}

	// Seniorities are tiers, most senior first; owner and founder share one.
	filter = c.PeopleFilter{CompanyDomains: []string{"acme.example"}, Seniorities: []string{"director", "founder", "owner", "bogus"}}
	if _, err := (Blitz{}).SearchPeople(context.Background(), cl, key, filter, "", 3); err != nil {
		t.Fatal(err)
	}
	if n := len(f.bodies[blitzSearch]["cascade"].([]any)); n != 2 || include(0)[0] != "founder" || include(1)[0] != "director" {
		t.Fatalf("seniorities: %v", f.bodies[blitzSearch]["cascade"])
	}
}

func TestBlitzSearchWithNothingToFind(t *testing.T) {
	f, cl := newFake(t)
	// No company: nothing is asked (the fake fails on any call).
	if _, err := (Blitz{}).SearchPeople(context.Background(), cl, key, c.PeopleFilter{Titles: []string{"CEO"}}, "", 5); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no company domain: %v", err)
	}
	filter := c.PeopleFilter{CompanyDomains: []string{"acme.example"}}
	for _, cursor := range []string{"x", "-1", "1"} {
		if _, err := (Blitz{}).SearchPeople(context.Background(), cl, key, filter, cursor, 5); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
	}
	// A company with no LinkedIn page has nobody, costs nothing, and the
	// people search is never called.
	f.routes[blitzPage] = blitzReply(t, 200, `{"found":false,"company_linkedin_url":null}`)
	page, err := Blitz{}.SearchPeople(context.Background(), cl, key, filter, "", 5)
	if err != nil || len(page.People) != 0 || page.Credits != 0 || page.Next != "" {
		t.Fatalf("no company page: %+v %v", page, err)
	}
	// A failure of the people search is the failure of the page.
	f.routes[blitzPage] = blitzReply(t, 200, `{"found":true,"company_linkedin_url":"https://www.linkedin.com/company/acme-example"}`)
	f.routes[blitzSearch] = blitzReply(t, 402, `{"success":false,"message":"Fair Use limit reached"}`)
	if _, err := (Blitz{}).SearchPeople(context.Background(), cl, key, filter, "", 5); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("out of records: %v", err)
	}
}

func TestBlitzFindsAnEmailByProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		query   c.PersonQuery
		answer  string
		sent    string // the profile sent; empty when Blitz must not be called
		email   string
		credits float64
	}{
		{name: "the address at the company asked about",
			query:  c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "https://www.acme.example/", LinkedInURL: "http://LinkedIn.com/in/ana-lee-example/?trk=x"},
			answer: `{"found":true,"email":" Ana@Acme.example ","all_emails":[{"email":"ana@acme.example"}],"fair_usage":{"records_used":1}}`,
			sent:   "https://linkedin.com/in/ana-lee-example", email: "ana@acme.example", credits: 1},
		{name: "a later address is the one at the company, a subdomain counts",
			query:  c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile},
			answer: `{"found":true,"email":"ana@globex.example","all_emails":[{"email":"ana@globex.example"},{"email":7},{"email":"ana@mail.acme.example"}]}`,
			sent:   blitzProfile, email: "ana@mail.acme.example", credits: 1},
		{name: "only an address at another company: no address, and the record is counted",
			query:  c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile},
			answer: `{"found":true,"email":"ana@globex.example","all_emails":[{"email":"ana@notacme.example"}],"fair_usage":{"records_used":1}}`,
			sent:   blitzProfile, credits: 1},
		{name: "a miss is free",
			query:  c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile},
			answer: `{"found":false,"email":null,"fair_usage":{"records_used":0}}`,
			sent:   blitzProfile},
		{name: "found without an address is no address",
			query:  c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile},
			answer: `{"found":true,"email":"not an address","all_emails":"none"}`,
			sent:   blitzProfile, credits: 1},
		{name: "with no company asked about, the first address",
			query:  c.PersonQuery{LinkedInURL: blitzProfile},
			answer: `{"found":true,"email":"ana@globex.example","all_emails":[{"email":"ana@acme.example"}]}`,
			sent:   blitzProfile, email: "ana@globex.example", credits: 1},
		{name: "no profile: a miss without a call",
			query: c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example"}},
		{name: "a company page is no profile",
			query: c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: "https://www.linkedin.com/company/acme-example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cl := newFake(t)
			if tc.sent != "" {
				f.routes[blitzEmail] = blitzReply(t, 200, tc.answer)
			}
			got, err := Blitz{}.EnrichPerson(context.Background(), cl, key, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if tc.sent != "" {
				if body := f.bodies[blitzEmail]; body["person_linkedin_url"] != tc.sent || len(body) != 1 {
					t.Fatalf("body: %v", body)
				}
			}
			if got.Credits != tc.credits || got.Found != (tc.email != "") || got.Person.Email != tc.email {
				t.Fatalf("answer: %+v", got)
			}
			if tc.email != "" {
				p := got.Person
				if p.EmailStatus != c.EmailGuessed || !p.HasEmail || p.LinkedInURL != tc.sent || p.FirstName != tc.query.FirstName {
					t.Fatalf("person: %+v", p)
				}
			}
		})
	}
}

func TestBlitzErrors(t *testing.T) {
	blitzNoPause(t)
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		kind   string
		calls  int32
	}{
		{"a refused key", 401, `{"success":false,"message":"Invalid API key"}`, c.ErrAuth, 1},
		{"a key not allowed", 403, `{"message":"Forbidden"}`, c.ErrAuth, 1},
		{"the plan's records are used up", 402, `{"success":false,"message":"Fair Use limit reached"}`, c.ErrQuota, 1},
		{"a locked account", 423, `{"blocked":true,"message":"Your account was locked"}`, c.ErrQuota, 1},
		{"too fast, twice: asked once more, then a rate limit", 429, `{"success":false,"message":"Rate limit exceeded"}`, c.ErrUnavailable, 2},
		{"Blitz is down", 503, `{"success":false,"message":"Service temporarily unavailable"}`, c.ErrUnavailable, 1},
		{"a request Blitz refuses", 422, `{"success":false,"message":"Invalid request body"}`, c.ErrInvalid, 1},
		{"an answer that is not JSON", 200, `<html>hello</html>`, c.ErrUnavailable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cl := newFake(t)
			var calls atomic.Int32
			f.routes[blitzEmail] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}
			_, err := Blitz{}.EnrichPerson(context.Background(), cl, key, q)
			if err == nil || c.KindOf(err) != tc.kind || c.IsTimeout(err) || calls.Load() != tc.calls {
				t.Fatalf("error = %v (kind %s), %d calls", err, c.KindOf(err), calls.Load())
			}
		})
	}
}

func TestBlitzAsksOnceMoreAfterARateLimit(t *testing.T) {
	blitzNoPause(t)
	f, cl := newFake(t)
	var calls atomic.Int32
	f.routes[blitzEmail] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			io.WriteString(w, `{"success":false,"message":"Rate limit exceeded"}`)
			return
		}
		io.WriteString(w, `{"found":true,"email":"ana@acme.example"}`)
	}
	got, err := Blitz{}.EnrichPerson(context.Background(), cl, key,
		c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile})
	if err != nil || got.Person.Email != "ana@acme.example" || calls.Load() != 2 {
		t.Fatalf("answer: %+v %v after %d calls", got, err, calls.Load())
	}
}

func TestBlitzTimesOut(t *testing.T) {
	old := blitzWait
	blitzWait = 30 * time.Millisecond
	t.Cleanup(func() { blitzWait = old })
	f, cl := newFake(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.routes[blitzEmail] = func(http.ResponseWriter, map[string]any, *http.Request) { <-release }
	_, err := Blitz{}.EnrichPerson(context.Background(), cl, key,
		c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: blitzProfile})
	if !c.IsTimeout(err) || c.KindOf(err) != c.ErrUnavailable {
		t.Fatalf("a call that got no answer in time: %v", err)
	}
}

// The email finder reads these to put Blitz first and pace it.
func TestBlitzIsAProfileFinder(t *testing.T) {
	var b c.Connector = Blitz{}
	if pf, ok := b.(c.ProfileFinder); !ok || !pf.ProfileOnly() {
		t.Fatal("Blitz finds emails by LinkedIn profile only")
	}
	if p, ok := b.(c.FindPacer); !ok || p.FindsPerMinute() != 600 {
		t.Fatal("Blitz names its own pace")
	}
	var tr c.Connector = Treg{}
	if pf, ok := tr.(c.ProfileFinder); !ok || pf.ProfileOnly() {
		t.Fatal("Treg can read a profile, and works without one")
	}
	if _, ok := (c.Connector(MoltSets{})).(c.ProfileFinder); ok {
		t.Fatal("MoltSets does not keep to the company asked about, so it is not given a profile")
	}
}
