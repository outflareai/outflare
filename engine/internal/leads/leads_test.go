package leads_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/leads"
)

func TestParseCSVApolloExport(t *testing.T) {
	in := "\ufeffFirst Name,Last Name,Title,Company Name,Email,Person Linkedin Url,Website,# Employees,Industry\n" +
		"Ana,Lee,CEO,Acme,ana@acme-io.example,https://linkedin.com/in/ana,acme-io.example,50,SaaS\n" +
		"Bo,,CTO,Beta,not-an-email,,,,\n" +
		",,,,,,,,\n" +
		"Cy,Ng,,Gamma,cy@gamma-co.example,,,,\n"
	got, bad, err := leads.ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(bad) != 1 || bad[0].Line != 3 {
		t.Fatalf("got=%+v bad=%+v", got, bad)
	}
	a := got[0]
	if a.FirstName != "Ana" || a.Company != "Acme" || a.Title != "CEO" || a.LinkedInURL != "https://linkedin.com/in/ana" || a.Website != "acme-io.example" {
		t.Fatalf("ana: %+v", a)
	}
	if a.Custom["employees"] != "50" || a.Custom["industry"] != "SaaS" || len(a.Custom) != 2 {
		t.Fatalf("custom: %v", a.Custom)
	}
	if got[1].Line != 5 || got[1].Custom != nil {
		t.Fatalf("cy: %+v", got[1])
	}
}

func TestParseCSVFullName(t *testing.T) {
	got, _, err := leads.ParseCSV(strings.NewReader("Full Name,Work Email\nMary Ann Smith,mary@x-com.example\n"))
	if err != nil || len(got) != 1 || got[0].FirstName != "Mary Ann" || got[0].LastName != "Smith" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, _, err := leads.ParseCSV(strings.NewReader("name,company\nA,B\n")); err == nil {
		t.Fatal("missing email column accepted")
	}
	if _, bad, _ := leads.ParseCSV(strings.NewReader("email\nuser@localhost\n")); len(bad) != 1 {
		t.Fatal("address without a dotted domain accepted")
	}
}

func TestDedupeMerges(t *testing.T) {
	in := []leads.Lead{
		{Email: "Ana@acme-io.example", FirstName: "Ana", Custom: map[string]string{"a": "1"}},
		{Email: "bo@acme-io.example"},
		{Email: "ana@ACME-io.example", Company: "Acme", Custom: map[string]string{"b": "2"}},
	}
	out, dups := leads.Dedupe(in)
	if dups != 1 || len(out) != 2 {
		t.Fatalf("dups=%d out=%+v", dups, out)
	}
	if out[0].FirstName != "Ana" || out[0].Company != "Acme" || out[0].Custom["a"] != "1" || out[0].Custom["b"] != "2" {
		t.Fatalf("merged: %+v", out[0])
	}
}

func TestParseSuppression(t *testing.T) {
	cases := map[string][2]string{
		"Bob@Acme-com.example":               {"email", "bob@acme-com.example"},
		"acme-com.example":                   {"domain", "acme-com.example"},
		"@Acme-com.example":                  {"domain", "acme-com.example"},
		"https://www.acme-com.example/about": {"domain", "www.acme-com.example"},
		" competitor.io ":                    {"domain", "competitor.io"},
	}
	for in, want := range cases {
		k, v, err := leads.ParseSuppression(in)
		if err != nil || k != want[0] || v != want[1] {
			t.Errorf("%q: %s %s %v", in, k, v, err)
		}
	}
	for _, in := range []string{"", "nodot", "a@", "bad domain.com", "-x.com"} {
		if _, _, err := leads.ParseSuppression(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestNormaliseKey(t *testing.T) {
	for in, want := range map[string]string{"Company Size (est.)": "company_size_est", "# Employees": "employees", "  ": "", "ICP-Tier": "icp_tier"} {
		if got := leads.NormaliseKey(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}
