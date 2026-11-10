package crmmail

import (
	"time"

	"slices"
	"strings"
	"testing"
)

func TestPersonalMailPolicy(t *testing.T) {
	raw := "From: Customer <ana@customer.example>\r\nTo: employee@northpeak.example\r\nSubject: Renewal\r\n\r\nA private renewal discussion."
	m, err := Parse(raw, "employee@northpeak.example")
	if err != nil {
		t.Fatal(err)
	}
	brand := DefaultPolicy()
	brand.InternalDomains = []string{"northpeak.example"}
	personal := DefaultPolicy()
	eligible := func(m Message) bool {
		return Eligible(m, brand, personal, []string{"coworker@northpeak.example"}, []string{"ana@customer.example"}, nil)
	}
	if !eligible(m) {
		t.Fatal("customer conversation excluded")
	}
	mixed, err := Parse(strings.Replace(raw, "Subject:", "Cc: coworker@northpeak.example\r\nSubject:", 1), m.OwnerEmail)
	if err != nil {
		t.Fatal(err)
	}
	if eligible(mixed) {
		t.Fatal("internal cc leaked into customer conversation")
	}
	mixed, err = Parse(strings.Replace(raw, "Subject:", "Bcc: finance@sub.northpeak.example\r\nSubject:", 1), m.OwnerEmail)
	if err != nil {
		t.Fatal(err)
	}
	if eligible(mixed) {
		t.Fatal("internal subdomain bcc was allowed")
	}
	blind, err := Parse(strings.Replace(raw, "Subject:", "Bcc: finance@customer.example\r\nSubject:", 1), m.OwnerEmail)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(blind.Participants, "finance@customer.example") || slices.Contains(blind.VisibleParticipants, "finance@customer.example") {
		t.Fatal("blind-copy recipients must be checked but not shared")
	}
	personal.BlockedDomains = []string{"customer.example"}
	if eligible(m) {
		t.Fatal("exclusion did not win")
	}
	if _, err = Parse(strings.Replace(raw, "To: employee@northpeak.example", "To: other@example.com", 1), m.OwnerEmail); err == nil {
		t.Fatal("unrelated mailbox accepted")
	}
	if _, err = Parse(strings.Replace(raw, "Subject:", "Cc: malformed <\r\nSubject:", 1), m.OwnerEmail); err == nil {
		t.Fatal("malformed participant silently discarded")
	}
}
func TestMailVisibility(t *testing.T) {
	s := Private()
	if Visibility("employee", "admin", s) != "private" {
		t.Fatal("implicit elevated access")
	}
	s.Visibility = "metadata"
	s.ViewerIDs = []string{"hr"}
	if Visibility("employee", "hr", s) != "metadata" || Visibility("employee", "manager", s) != "private" {
		t.Fatal("selected audience not enforced")
	}
	s.Visibility = "full"
	s.Audience = "brand"
	if Visibility("employee", "sales", s) != "full" || Visibility("employee", "", s) != "private" {
		t.Fatal("brand audience not enforced")
	}
	if Visibility("employee", "employee", Private()) != "full" {
		t.Fatal("owner denied")
	}
}

func TestCaptureDomainPrivacyFloor(t *testing.T) {
	for _, domain := range []string{"gmail.com", "icloud.com", "com", "*.customer.example"} {
		if CaptureDomainAllowed(domain) {
			t.Error("unsafe company domain accepted", domain)
		}
	}
	if !CaptureDomainAllowed("customer.example") || !captureDomainMatch("mail.customer.example", []string{"customer.example"}) {
		t.Fatal("ordinary company capture stopped working")
	}
	if captureDomainMatch("gmail.com", []string{"gmail.com"}) {
		t.Fatal("public provider grouped unrelated customers")
	}
}

func TestTeamSharingFilters(t *testing.T) {
	if got := ExcludeLabels(DefaultPolicy(), Policy{PrivateLabels: []string{"Board Papers", "HR"}}); got != "-label:personal -label:hr -label:legal -label:finance -label:medical -label:board-papers" {
		t.Fatalf("labels: %q", got)
	}
	if got := ExcludeLabels(Policy{PrivateLabels: []string{}}); got != "" {
		t.Fatalf("no labels: %q", got)
	}
	if !domainMatch("eu.deel.com", PrivateSenderDomains) || domainMatch("customer.example", PrivateSenderDomains) {
		t.Fatal("private sender domains")
	}
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	if (Policy{TeamSharing: true, TeamSharingFrom: &future}).TeamSharingOn(now) || !(Policy{TeamSharing: true, TeamSharingFrom: &past}).TeamSharingOn(now) || (Policy{TeamSharingFrom: &past}).TeamSharingOn(now) {
		t.Fatal("team sharing on")
	}
}

func TestCaptureMatching(t *testing.T) {
	if got := CaptureDomains("bob@mail.acme.example"); !slices.Equal(got, []string{"mail.acme.example", "acme.example"}) {
		t.Errorf("domains %v", got)
	}
	if got := CaptureDomains("someone@gmail.com"); len(got) != 0 {
		t.Errorf("public mailbox matched %v", got)
	}
	m := Message{OwnerEmail: "me@northpeak.example", Participants: []string{"me@northpeak.example", "pat@northpeak.example", "ana@customer.example", "col@team.northpeak.example", "ana@customer.example"}}
	if got := External(m, Policy{InternalDomains: []string{"northpeak.example"}}, []string{"pat@northpeak.example"}); !slices.Equal(got, []string{"ana@customer.example"}) {
		t.Errorf("external %v", got)
	}
}
