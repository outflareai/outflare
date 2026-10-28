package data

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// fake serves canned responses by path and records request bodies.
type fake struct {
	t      *testing.T
	routes map[string]func(w http.ResponseWriter, body map[string]any, r *http.Request)
	bodies map[string]map[string]any
}

func newFake(t *testing.T) (*fake, *c.Client) {
	f := &fake{t: t, routes: map[string]func(http.ResponseWriter, map[string]any, *http.Request){}, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		f.bodies[r.URL.Path] = body
		h, ok := f.routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		h(w, body, r)
	}))
	t.Cleanup(srv.Close)
	urls := map[string]string{}
	for _, id := range []string{"apollo", "peopledatalabs", "prospeo", "leadmagic", "findymail", "moltsets", "snitcher"} {
		urls[id] = srv.URL
	}
	return f, &c.Client{BaseURLs: urls}
}

func reply(v string) func(http.ResponseWriter, map[string]any, *http.Request) {
	return func(w http.ResponseWriter, _ map[string]any, _ *http.Request) { io.WriteString(w, v) }
}

var key = c.Creds{"api_key": "k"}

func TestApolloSearchMapsFiltersAndPages(t *testing.T) {
	f, cl := newFake(t)
	f.routes["/api/v1/mixed_people/api_search"] = func(w http.ResponseWriter, body map[string]any, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("api key header missing")
		}
		io.WriteString(w, `{"total_entries":3,"people":[
			{"id":"a1","first_name":"Kiki","last_name_obfuscated":"Ma***s","title":"Head of Sales","has_email":true,"organization":{"name":"Atlas"}},
			{"id":"a2","first_name":"Rich","title":"VP Sales","has_email":false,"organization":{"name":"Enviro","primary_domain":"enviro.com.au"}}]}`)
	}
	filter := c.PeopleFilter{Titles: []string{"head of sales"}, Seniorities: []string{"vp", "bogus"}, EmployeeRanges: []string{"11-50", "10001+"},
		Locations: []string{"Australia"}, Industries: []string{"logistics"}}
	page, err := Apollo{}.SearchPeople(context.Background(), cl, key, filter, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	body := f.bodies["/api/v1/mixed_people/api_search"]
	if body["per_page"] != 2.0 || body["page"] != 1.0 {
		t.Fatalf("paging: %v", body)
	}
	if r := body["organization_num_employees_ranges"].([]any); r[0] != "11,50" || r[1] != "10001,1000000" {
		t.Fatalf("ranges: %v", r)
	}
	if s := body["person_seniorities"].([]any); len(s) != 1 || s[0] != "vp" {
		t.Fatalf("seniorities: %v", s)
	}
	if body["person_locations"].([]any)[0] != "Australia" || body["q_organization_keyword_tags"].([]any)[0] != "logistics" {
		t.Fatalf("body: %v", body)
	}
	if len(page.People) != 2 || *page.Total != 3 || page.Next != "2" {
		t.Fatalf("page: %+v", page)
	}
	if p := page.People[0]; p.ProviderID != "a1" || !p.HasEmail || p.Email != "" || p.Company.Name != "Atlas" {
		t.Fatalf("person: %+v", p)
	}
	if page.People[1].Company.Domain != "enviro.com.au" {
		t.Fatalf("domain: %+v", page.People[1])
	}

	// The last page has no next cursor.
	page, _ = Apollo{}.SearchPeople(context.Background(), cl, key, filter, "2", 2)
	if page.Next != "" {
		t.Fatalf("next on last page: %q", page.Next)
	}
}

func TestApolloEnrich(t *testing.T) {
	f, cl := newFake(t)
	f.routes["/api/v1/people/match"] = reply(`{"person":{"id":"a1","first_name":"Kiki","last_name":"Mars","title":"Head of Sales","email":"KIKI@atlas.example","email_status":"verified","seniority":"head","city":"Sydney","country":"Australia","organization":{"name":"Atlas","website_url":"https://www.atlas.example","estimated_num_employees":40}}}`)
	got, err := Apollo{}.EnrichPerson(context.Background(), cl, key, c.PersonQuery{ProviderID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if f.bodies["/api/v1/people/match"]["id"] != "a1" || f.bodies["/api/v1/people/match"]["reveal_personal_emails"] != false {
		t.Fatalf("body: %v", f.bodies["/api/v1/people/match"])
	}
	p := got.Person
	if !got.Found || got.Credits != 1 || p.Email != "kiki@atlas.example" || p.EmailStatus != c.EmailVerified ||
		p.Company.Domain != "atlas.example" || p.Company.Employees != 40 || p.Location() != "Sydney, Australia" {
		t.Fatalf("enriched: %+v", got)
	}

	f.routes["/api/v1/people/match"] = reply(`{"person":null}`)
	if got, err = (Apollo{}).EnrichPerson(context.Background(), cl, key, c.PersonQuery{Email: "x@y.z"}); err != nil || got.Found {
		t.Fatalf("no match: %+v %v", got, err)
	}
	f.routes["/api/v1/people/match"] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) { w.WriteHeader(401) }
	if _, err = (Apollo{}).EnrichPerson(context.Background(), cl, key, c.PersonQuery{Email: "x@y.z"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("auth error: %v", err)
	}
}

func TestPDLSearchAndEnrich(t *testing.T) {
	f, cl := newFake(t)
	f.routes["/v5/person/search"] = reply(`{"status":200,"total":10,"scroll_token":"tok","data":[
		{"id":"p1","first_name":"ana","last_name":"lee","job_title":"cto","work_email":"ana@acme-io.example","job_company_name":"acme","job_company_website":"acme-io.example","job_company_size":"11-50","location_country":"australia"}]}`)
	page, err := PDL{}.SearchPeople(context.Background(), cl, key, c.PeopleFilter{Titles: []string{"CTO"}, Seniorities: []string{"c_suite"}}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(f.bodies["/v5/person/search"]["query"])
	if !json.Valid(q) || !contains(string(q), `"job_title_levels":["cxo"]`) || !contains(string(q), `"work_email"`) {
		t.Fatalf("query: %s", q)
	}
	p := page.People[0]
	if page.Next != "tok" || page.Credits != 1 || p.FirstName != "Ana" || p.Email != "ana@acme-io.example" || p.Company.Domain != "acme-io.example" || p.Company.Employees != 30 {
		t.Fatalf("page: %+v", page)
	}

	f.routes["/v5/person/enrich"] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.URL.Query().Get("email") != "ana@acme-io.example" {
			t.Errorf("query: %v", r.URL.Query())
		}
		w.WriteHeader(404)
		io.WriteString(w, `{"status":404}`)
	}
	if got, err := (PDL{}).EnrichPerson(context.Background(), cl, key, c.PersonQuery{Email: "ana@acme-io.example"}); err != nil || got.Found || got.Credits != 0 {
		t.Fatalf("no match: %+v %v", got, err)
	}
}

func TestEmailFinders(t *testing.T) {
	f, cl := newFake(t)
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "https://www.acme-io.example/"}

	f.routes["/enrich-person"] = reply(`{"error":false,"person":{"first_name":"Ana","last_name":"Lee","email":{"status":"VERIFIED","revealed":true,"email":"ana@acme-io.example"}},"company":{"name":"Acme","domain":"acme-io.example"}}`)
	got, err := Prospeo{}.EnrichPerson(context.Background(), cl, key, q)
	if err != nil || got.Person.Email != "ana@acme-io.example" || got.Person.EmailStatus != c.EmailVerified || got.Credits != 1 {
		t.Fatalf("prospeo: %+v %v", got, err)
	}
	if d := f.bodies["/enrich-person"]["data"].(map[string]any); d["company_website"] != "acme-io.example" {
		t.Fatalf("prospeo body: %v", d)
	}
	f.routes["/enrich-person"] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":true,"error_code":"NO_MATCH"}`)
	}
	if got, err = (Prospeo{}).EnrichPerson(context.Background(), cl, key, q); err != nil || got.Found {
		t.Fatalf("prospeo no match: %+v %v", got, err)
	}
	f.routes["/enrich-person"] = reply(`{"error":true,"error_code":"INSUFFICIENT_CREDITS"}`)
	if _, err = (Prospeo{}).EnrichPerson(context.Background(), cl, key, q); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("prospeo quota: %v", err)
	}

	f.routes["/email-finder"] = reply(`{"email":"ana@acme-io.example","status":"valid","credits_consumed":1}`)
	if got, err = (LeadMagic{}).EnrichPerson(context.Background(), cl, key, q); err != nil || got.Person.Email != "ana@acme-io.example" || got.Person.Company.Domain != "acme-io.example" {
		t.Fatalf("leadmagic: %+v %v", got, err)
	}
	if _, err = (LeadMagic{}).EnrichPerson(context.Background(), cl, key, c.PersonQuery{FirstName: "Ana"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("leadmagic needs a name: %v", err)
	}

	f.routes["/api/search/name"] = reply(`{"contact":{"name":"Ana Lee","domain":"acme-io.example","email":"ana@acme-io.example"}}`)
	if got, err = (Findymail{}).EnrichPerson(context.Background(), cl, key, q); err != nil || got.Person.Email != "ana@acme-io.example" {
		t.Fatalf("findymail: %+v %v", got, err)
	}
	if f.bodies["/api/search/name"]["name"] != "Ana Lee" {
		t.Fatalf("findymail body: %v", f.bodies["/api/search/name"])
	}
}

func TestEveryConnectorIsWellFormed(t *testing.T) {
	for _, conn := range All() {
		d := conn.Definition()
		// Visitor identification tools push to a webhook, so they ask
		// for nothing.
		if pushes := d.Has(c.CapRevealPerson); d.ID == "" || d.Name == "" || d.DocsURL == "" ||
			(!pushes && (d.Category != c.CategoryData || len(d.Fields) == 0)) || (pushes && d.Category != c.CategorySignals) {
			t.Errorf("%s: incomplete definition", d.ID)
		}
		if d.Has(c.CapRevealPerson) {
			if _, ok := conn.(c.PersonRevealer); !ok {
				t.Errorf("%s claims person reveal but does not implement it", d.ID)
			}
		}
		if d.Has(c.CapSearchPeople) {
			if _, ok := conn.(c.PeopleSearcher); !ok {
				t.Errorf("%s claims search but does not implement it", d.ID)
			}
		}
		if d.Has(c.CapRevealCompany) {
			if _, ok := conn.(c.CompanyRevealer); !ok {
				t.Errorf("%s claims company reveal but does not implement it", d.ID)
			}
		}
		if d.Has(c.CapEnrichPerson) || d.Has(c.CapFindEmail) {
			if _, ok := conn.(c.PersonEnricher); !ok {
				t.Errorf("%s claims enrichment but does not implement it", d.ID)
			}
		}
	}
}

func TestRevealCompany(t *testing.T) {
	f, cl := newFake(t)
	key := c.Creds{"api_key": "test-not-a-real-key"}
	ctx := context.Background()

	f.routes["/v5/ip/enrich"] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-not-a-real-key" || r.URL.Query().Get("return_ip_metadata") != "true" {
			t.Errorf("pdl request: %v %v", r.Header, r.URL.Query())
		}
		switch r.URL.Query().Get("ip") {
		case "192.0.2.1":
			io.WriteString(w, `{"status":200,"data":{"ip":{"metadata":{"hosting":false}},"company":{"name":"northpeak","display_name":"Northpeak","website":"www.northpeak.example","industry":"software","employee_count":120}}}`)
		case "192.0.2.2":
			io.WriteString(w, `{"status":200,"data":{"ip":{"metadata":{"vpn":true}},"company":null}}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"status":404,"error":{"type":"not_found"}}`)
		}
	}
	got, err := (PDL{}).RevealCompany(ctx, cl, key, "192.0.2.1")
	if err != nil || !got.Found || got.Network != c.NetworkBusiness || got.Company.Domain != "northpeak.example" || got.Company.Name != "Northpeak" || got.Company.Employees != 120 {
		t.Fatalf("pdl: %+v %v", got, err)
	}
	if got, err = (PDL{}).RevealCompany(ctx, cl, key, "192.0.2.2"); err != nil || got.Found || got.Network != c.NetworkHosting {
		t.Fatalf("pdl vpn: %+v %v", got, err)
	}
	if got, err = (PDL{}).RevealCompany(ctx, cl, key, "192.0.2.3"); err != nil || got.Found {
		t.Fatalf("pdl miss: %+v %v", got, err)
	}

	f.routes["/company/find"] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-not-a-real-key" {
			t.Errorf("snitcher request: %s %v", r.Method, r.Header)
		}
		switch r.URL.Query().Get("ip") {
		case "192.0.2.1":
			io.WriteString(w, `{"type":"business","company":{"name":"Northpeak","domain":"northpeak.example","employees":"120"}}`)
		case "192.0.2.2":
			io.WriteString(w, `{"type":"isp","company":{"name":"Some ISP","domain":"isp.example"}}`)
		case "192.0.2.4":
			w.WriteHeader(202)
			io.WriteString(w, `{}`)
		default:
			w.WriteHeader(401)
		}
	}
	if got, err = (Snitcher{}).RevealCompany(ctx, cl, key, "192.0.2.1"); err != nil || !got.Found || got.Company.Domain != "northpeak.example" || got.Company.Employees != 120 {
		t.Fatalf("snitcher: %+v %v", got, err)
	}
	if got, err = (Snitcher{}).RevealCompany(ctx, cl, key, "192.0.2.2"); err != nil || got.Found || got.Network != c.NetworkISP {
		t.Fatalf("snitcher isp: %+v %v", got, err)
	}
	if _, err = (Snitcher{}).RevealCompany(ctx, cl, key, "192.0.2.4"); err != c.ErrRetryLater {
		t.Fatalf("snitcher queued: %v", err)
	}
	if _, err = (Snitcher{}).RevealCompany(ctx, cl, key, "192.0.2.9"); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("snitcher bad key: %v", err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// A signed-in Apollo connection sends its token as a bearer, a pasted
// key as X-Api-Key.
func TestApolloSignInUsesBearer(t *testing.T) {
	f, cl := newFake(t)
	var auth, apiKey string
	f.routes["/v1/auth/health"] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		auth, apiKey = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		io.WriteString(w, `{"healthy":true,"is_logged_in":true}`)
	}
	if _, err := (Apollo{}).Test(context.Background(), cl, c.Creds{"access_token": "ap_tok"}); err != nil || auth != "Bearer ap_tok" || apiKey != "" {
		t.Fatalf("oauth: %v %q %q", err, auth, apiKey)
	}
	if _, err := (Apollo{}).Test(context.Background(), cl, key); err != nil || auth != "" || apiKey != "k" {
		t.Fatalf("key: %v %q %q", err, auth, apiKey)
	}
}

func TestParsePeople(t *testing.T) {
	// RB2B's field names, one person per call.
	got, err := (RB2B{}).ParsePeople([]byte(`{"LinkedIn URL":"https://www.linkedin.com/in/ana-example","First Name":"Ana","Last Name":"Lee",
		"Title":"Head of Growth","Company Name":"Northpeak","Business Email":"Ana@Northpeak.example","Website":"https://www.northpeak.example",
		"Industry":"Software","Employee Count":"51-200","City":"Austin","State":"TX","Seen At":"2026-10-27T09:00:00Z",
		"Captured URL":"https://acme.example/pricing","Tags":"Hot Pages"}`))
	if err != nil || len(got) != 1 {
		t.Fatalf("rb2b: %+v %v", got, err)
	}
	p := got[0]
	if p.Email != "ana@northpeak.example" || p.Name != "Ana Lee" || p.Title != "Head of Growth" || p.Company.Domain != "northpeak.example" ||
		p.Company.Name != "Northpeak" || p.Company.Location != "Austin, TX" || p.Page != "https://acme.example/pricing" || p.SeenAt.IsZero() {
		t.Fatalf("rb2b person: %+v", p)
	}
	// Vector and Warmly nest the contact and company, sometimes in a list.
	got, err = (Vector{}).ParsePeople([]byte(`{"event":"contact.visited","data":{"contact":{"first_name":"Bo","last_name":"Chan","email":"bo@acme.example",
		"linkedin_url":"linkedin.com/in/bo-example"},"company":{"name":"Acme","domain":"acme.example"},"page_url":"https://acme.example/"}}`))
	if err != nil || len(got) != 1 || got[0].Email != "bo@acme.example" || got[0].Company.Name != "Acme" || got[0].Company.Domain != "acme.example" ||
		got[0].LinkedInURL != "https://linkedin.com/in/bo-example" {
		t.Fatalf("vector: %+v %v", got, err)
	}
	got, err = (Warmly{}).ParsePeople([]byte(`[{"workEmail":"cy@acme.example","fullName":"Cy Diaz"},{"name":"Nobody"}]`))
	if err != nil || len(got) != 1 || got[0].Email != "cy@acme.example" || got[0].Name != "Cy Diaz" {
		t.Fatalf("warmly: %+v %v", got, err)
	}
	if _, err := (Warmly{}).ParsePeople([]byte(`not json`)); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("not json: %v", err)
	}
}
