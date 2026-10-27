package lists

import (
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestScore(t *testing.T) {
	icp := c.PeopleFilter{Titles: []string{"head of sales", "vp sales"}, Seniorities: []string{"vp", "head"}, EmployeeRanges: []string{"11-50"}, Locations: []string{"Australia"}}
	p := c.Person{Title: "Head of Sales APAC", Country: "Australia", Company: c.Company{Employees: 40}}
	if s, why := Score(icp, p); s == nil || *s != 100 || len(why) != 4 {
		t.Fatalf("perfect fit: %v %v", s, why)
	}
	p.Company.Employees = 5000
	p.Country = "Germany"
	if s, _ := Score(icp, p); *s != 65 { // title 35 + seniority 20 of 85
		t.Fatalf("partial fit: %v", *s)
	}
	if s, _ := Score(c.PeopleFilter{}, p); s != nil {
		t.Fatal("an empty profile has no score")
	}
}

func TestInferSeniority(t *testing.T) {
	for title, want := range map[string]string{
		"Co-Founder & CEO": "founder", "Chief Revenue Officer": "c_suite", "VP, Sales": "vp", "Head of Growth": "head",
		"Sales Director": "director", "Account Manager": "manager", "Senior AE": "senior", "Account Executive": "",
	} {
		if got := InferSeniority(title); got != want {
			t.Errorf("%q: got %q, want %q", title, got, want)
		}
	}
}

func TestCleanFilter(t *testing.T) {
	f, err := CleanFilter(c.PeopleFilter{Titles: []string{" CTO ", "cto", ""}, Seniorities: []string{"VP"}, CompanyDomains: []string{"https://www.Acme-io.example/about"}})
	if err != nil || len(f.Titles) != 1 || f.Seniorities[0] != "vp" || f.CompanyDomains[0] != "acme-io.example" {
		t.Fatalf("clean: %+v %v", f, err)
	}
	for _, bad := range []c.PeopleFilter{{Seniorities: []string{"boss"}}, {EmployeeRanges: []string{"50-10"}}, {CompanyDomains: []string{"a@b-io.example"}}} {
		if _, err := CleanFilter(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
