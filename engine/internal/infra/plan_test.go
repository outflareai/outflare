package infra

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var monday = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

func codes(ws []Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

func TestPlanTenDomainsThirtyMailboxes(t *testing.T) {
	by := monday.AddDate(0, 0, 19)
	d := Plan(Goal{
		DomainCount: 10, MailboxesPerDomain: 3, Brand: "Acme Corp",
		Personas: []Persona{{"Jane", "Doe"}, {"Sam", "Lee"}},
		ReadyBy:  &by, WarmupDays: 14, DailyLimit: 30, Now: monday,
	})
	if len(d.Domains) != 10 {
		t.Fatalf("domains = %d, want 10", len(d.Domains))
	}
	if d.Domains[0].Name != "getacmecorp.com" || d.Domains[1].Name != "acmecorphq.com" {
		t.Errorf("first suggestions = %s, %s", d.Domains[0].Name, d.Domains[1].Name)
	}
	total := 0
	for _, dom := range d.Domains {
		total += len(dom.Mailboxes)
		if !strings.HasSuffix(dom.Mailboxes[0].Email, "@"+dom.Name) {
			t.Errorf("mailbox %s not on %s", dom.Mailboxes[0].Email, dom.Name)
		}
	}
	if total != 30 {
		t.Errorf("mailboxes = %d, want 30", total)
	}
	got := []string{d.Domains[0].Mailboxes[0].Email, d.Domains[0].Mailboxes[1].Email, d.Domains[0].Mailboxes[2].Email}
	dom := d.Domains[0].Name
	want := []string{"jane@" + dom, "sam@" + dom, "jane.doe@" + dom}
	if !slices.Equal(got, want) {
		t.Errorf("mailboxes = %v, want %v", got, want)
	}
	if d.DailyCapacity != 900 {
		t.Errorf("capacity = %d, want 900", d.DailyCapacity)
	}
	if want := monday.Truncate(24*time.Hour).AddDate(0, 0, 16); !d.Timeline.ReadyOn.Equal(want) {
		t.Errorf("ready on %v, want %v", d.Timeline.ReadyOn, want)
	}
	if len(d.Warnings) != 0 {
		t.Errorf("warnings = %v", codes(d.Warnings))
	}
}

func TestPlanReadyByTooSoon(t *testing.T) {
	// Ready in 12 days: 2 of setup leave 10 of warm-up, enough to shorten.
	by := monday.AddDate(0, 0, 12)
	d := Plan(Goal{DomainCount: 2, MailboxesPerDomain: 2, Brand: "acme", Personas: []Persona{{"Jane", "Doe"}}, ReadyBy: &by, DailyLimit: 30, Now: monday})
	if !slices.Contains(codes(d.Warnings), "ready_by_too_soon") {
		t.Fatalf("warnings = %v", codes(d.Warnings))
	}
	if msg := d.Warnings[0].Message; !strings.Contains(msg, "Shorten warm-up to 10 days") || d.Warnings[0].Blocking {
		t.Errorf("message = %q", msg)
	}
	// In 5 days there is no safe warm-up: move the date.
	by = monday.AddDate(0, 0, 5)
	d = Plan(Goal{DomainCount: 2, MailboxesPerDomain: 2, Brand: "acme", Personas: []Persona{{"Jane", "Doe"}}, ReadyBy: &by, DailyLimit: 30, Now: monday})
	if msg := d.Warnings[0].Message; !strings.Contains(msg, "Move the date to Wed 4 Nov or later") {
		t.Errorf("message = %q", msg)
	}
}

func TestPlanBlockingWarnings(t *testing.T) {
	d := Plan(Goal{DomainCount: 2, MailboxesPerDomain: 2, Brand: "acme", Now: monday})
	if c := codes(d.Warnings); !slices.Contains(c, "no_personas") {
		t.Errorf("warnings = %v", c)
	}
	d = Plan(Goal{DomainCount: 2, MailboxesPerDomain: 2, Brand: "", Personas: []Persona{{"Jane", "Doe"}}, Now: monday})
	if c := codes(d.Warnings); !slices.Contains(c, "not_enough_domains") {
		t.Errorf("warnings = %v", c)
	}
	// A person with only a first name gives fewer addresses.
	d = Plan(Goal{DomainCount: 1, MailboxesPerDomain: 6, Brand: "acme", Personas: []Persona{{"Jane", ""}}, Now: monday})
	if c := codes(d.Warnings); !slices.Contains(c, "too_few_personas") || !slices.Contains(c, "many_per_domain") {
		t.Errorf("warnings = %v", c)
	}
	if n := len(d.Domains[0].Mailboxes); n != 1 {
		t.Errorf("mailboxes = %d, want 1 (jane@ only)", n)
	}
}

func TestPlanChosenAndExcludedDomains(t *testing.T) {
	d := Plan(Goal{DomainCount: 3, MailboxesPerDomain: 1, Brand: "acme", Personas: []Persona{{"Jane", "Doe"}},
		Domains: []string{"Acme-Sales.com"}, Exclude: []string{"getacme.com"}, Now: monday})
	var names []string
	for _, x := range d.Domains {
		names = append(names, x.Name)
	}
	if want := []string{"acme-sales.com", "acmehq.com", "tryacme.com"}; !slices.Equal(names, want) {
		t.Errorf("domains = %v, want %v", names, want)
	}
}

func TestSuggestIsUniqueAndComFirst(t *testing.T) {
	s := Suggest("Acme!")
	seen := map[string]bool{}
	for _, d := range s {
		if seen[d] {
			t.Errorf("duplicate %s", d)
		}
		seen[d] = true
	}
	if len(s) != 80 || !strings.HasSuffix(s[19], ".com") || !strings.HasSuffix(s[20], ".co") {
		t.Errorf("len %d, s[19]=%s s[20]=%s", len(s), s[19], s[20])
	}
}
