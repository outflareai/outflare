package data

import (
	"context"
	"io"
	"net/http"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestMoltSetsSendsKeyAndUserAgent(t *testing.T) {
	f, cl := newFake(t)
	f.routes["/api/v1/tools/get_account"] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("User-Agent") == "" {
			t.Errorf("headers: %s %v", r.Method, r.Header)
		}
		io.WriteString(w, `{"status":"ok","results":{"id":"acc_1","name":"Northpeak","status":"active","plan":"starter","token_balance":900},"metadata":{}}`)
	}
	acct, err := MoltSets{}.Test(context.Background(), cl, key)
	if err != nil || acct.Name != "Northpeak" || acct.Credits == nil || *acct.Credits != 900 || acct.Details["plan"] != "starter" {
		t.Fatalf("account: %+v %v", acct, err)
	}
	f.routes["/api/v1/tools/get_account"] = reply(`{"status":"ok","results":{"status":"suspended"}}`)
	if _, err := (MoltSets{}).Test(context.Background(), cl, key); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("suspended: %v", err)
	}
	f.routes["/api/v1/tools/get_account"] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"code":"unauthorized","message":"Invalid API key."}}`)
	}
	if _, err := (MoltSets{}).Test(context.Background(), cl, key); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("auth error: %v", err)
	}
}

func TestMoltSetsSearchMapsFiltersAndPages(t *testing.T) {
	f, cl := newFake(t)
	f.routes["/api/v1/tools/search_people"] = reply(`{"status":"ok","results":{"total":3,"results":[
		{"_id":"ana-lee-example","full_name":"Ana Lee","first_name":"Ana","last_name":"Lee","title":"Head of Sales","seniority":"Head","country":"Australia",
		 "linkedin_url":"https://linkedin.com/in/ana-lee-example","business_email":"ANA@acme.example","business_email_risk_score":"A",
		 "company":{"name":"Acme","size":"40","industry":"Information Technology","website_url":"https://www.acme.example/","domain":"acme.example","linkedin_url":"linkedin.com/company/acme-example"}},
		{"_id":"ben-ito-example","full_name":"Ben Ito","title":"VP Sales","seniority":"C Suite","business_email":"ben@globex.example","business_email_risk_score":"D"}]},
		"metadata":{"tokens_used":2}}`)
	filter := c.PeopleFilter{Titles: []string{"head of sales", "vp sales"}, Seniorities: []string{"c_suite", "bogus"},
		EmployeeRanges: []string{"11-50", "5001-10000", "10001+"}, Industries: []string{"information technology", "Software Development"},
		Locations: []string{"Australia"}, CompanyDomains: []string{"https://www.acme.example/"}, Keywords: []string{"saas"}}
	page, err := MoltSets{}.SearchPeople(context.Background(), cl, key, filter, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	body := f.bodies["/api/v1/tools/search_people"]
	if body["limit"] != 2.0 || body["offset"] != 0.0 || body["country"] != "Australia" || body["company_domain"] != "acme.example" ||
		body["seniority"] != "C Suite" || body["industry"] != "Information Technology" || body["linkedin_industry"] != "Software Development" || body["query"] != "saas" {
		t.Fatalf("body: %v", body)
	}
	if s := body["employee_range"].([]any); len(s) != 3 || s[0] != "11-20" || s[1] != "21-50" || s[2] != "5001+" {
		t.Fatalf("sizes: %v", s)
	}
	if ts := body["title"].([]any); len(ts) != 2 {
		t.Fatalf("titles: %v", body["title"])
	}
	p := page.People[0]
	if len(page.People) != 2 || page.Next != "2" || *page.Total != 3 || page.Credits != 2 || p.ProviderID != "ana-lee-example" ||
		p.FirstName != "Ana" || p.LastName != "Lee" || p.Seniority != "head" || p.Email != "ana@acme.example" || p.EmailStatus != c.EmailVerified ||
		p.Company.Domain != "acme.example" || p.Company.Employees != 40 || p.Company.LinkedInURL != "https://linkedin.com/company/acme-example" {
		t.Fatalf("page: %+v", page)
	}
	// A D grade is a spam trap or complaint: the address is dropped.
	if b := page.People[1]; b.HasEmail || b.Email != "" || b.FirstName != "Ben" || b.LastName != "Ito" || b.Seniority != "c_suite" {
		t.Fatalf("second: %+v", b)
	}

	// The last page has no next cursor.
	page, err = MoltSets{}.SearchPeople(context.Background(), cl, key, filter, "2", 2)
	if err != nil || page.Next != "" || f.bodies["/api/v1/tools/search_people"]["offset"] != 2.0 {
		t.Fatalf("last page: %+v %v", page, err)
	}
	f.routes["/api/v1/tools/search_people"] = reply(`{"status":"not_found","results":{"results":[],"total":0},"metadata":{}}`)
	if page, err = (MoltSets{}).SearchPeople(context.Background(), cl, key, filter, "", 2); err != nil || len(page.People) != 0 || *page.Total != 0 {
		t.Fatalf("no match: %+v %v", page, err)
	}
}

func TestMoltSetsEnrich(t *testing.T) {
	f, cl := newFake(t)
	f.routes["/api/v1/tools/linkedin_to_business_email"] = reply(`{"status":"ok","results":{"email":"ana@acme.example","risk_score":"C"},"metadata":{"tokens_used":1,"tokens_remaining":899}}`)
	got, err := MoltSets{}.EnrichPerson(context.Background(), cl, key, c.PersonQuery{LinkedInURL: "https://linkedin.com/in/ana-lee-example"})
	if err != nil || !got.Found || got.Credits != 1 || got.Person.Email != "ana@acme.example" || got.Person.EmailStatus != c.EmailGuessed {
		t.Fatalf("enriched: %+v %v", got, err)
	}
	if f.bodies["/api/v1/tools/linkedin_to_business_email"]["linkedin_url"] != "https://linkedin.com/in/ana-lee-example" {
		t.Fatalf("body: %v", f.bodies["/api/v1/tools/linkedin_to_business_email"])
	}

	// A name with a company domain uses the one-call finder.
	f.routes["/api/v1/tools/search_business_email_by_name"] = reply(`{"status":"ok","results":{"email":"ana@acme.example","risk_score":"A","linkedin_url":"https://linkedin.com/in/ana-lee-example"},"metadata":{}}`)
	got, err = MoltSets{}.EnrichPerson(context.Background(), cl, key, c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "https://www.acme.example/"})
	if err != nil || !got.Found || got.Credits != 0 || got.Person.EmailStatus != c.EmailVerified || got.Person.LinkedInURL == "" || got.Person.Company.Domain != "acme.example" {
		t.Fatalf("by name: %+v %v", got, err)
	}
	if b := f.bodies["/api/v1/tools/search_business_email_by_name"]; b["company_domain"] != "acme.example" || b["first_name"] != "Ana" {
		t.Fatalf("by name body: %v", b)
	}
	f.routes["/api/v1/tools/search_business_email_by_name"] = reply(`{"status":"not_found","results":{"email":null,"risk_score":null,"linkedin_url":null},"metadata":{}}`)
	if got, err = (MoltSets{}).EnrichPerson(context.Background(), cl, key, c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme.example"}); err != nil || got.Found {
		t.Fatalf("no match: %+v %v", got, err)
	}

	// An email alone is looked up on LinkedIn first.
	f.routes["/api/v1/tools/email_to_linkedin"] = reply(`{"status":"ok","results":{"linkedin_url":"https://linkedin.com/in/ana-lee-example"},"metadata":{"tokens_used":1}}`)
	f.routes["/api/v1/tools/linkedin_to_business_email"] = reply(`{"status":"not_found","results":{"email":null,"risk_score":null},"metadata":{"tokens_used":0}}`)
	got, err = MoltSets{}.EnrichPerson(context.Background(), cl, key, c.PersonQuery{Email: "ana@gmail.com"})
	if err != nil || !got.Found || got.Credits != 1 || got.Person.LinkedInURL != "https://linkedin.com/in/ana-lee-example" || got.Person.Email != "ana@gmail.com" {
		t.Fatalf("email only: %+v %v", got, err)
	}

	if _, err := (MoltSets{}).EnrichPerson(context.Background(), cl, key, c.PersonQuery{FirstName: "Ana"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("needs a profile: %v", err)
	}
}
