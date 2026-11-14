package api

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/crmmail"
)

func TestSalesCaptureAllowsIntroducedColleagueButNotOtherStaff(t *testing.T) {
	c := mailContext{brand: crmmail.Policy{InternalDomains: []string{"acme.example"}}, members: []string{"rep@acme.example", "ae@acme.example", "hr@acme.example"}, emails: []string{"lead@customer.example"}}
	m := crmmail.Message{OwnerEmail: "rep@acme.example", Participants: []string{"lead@customer.example", "rep@acme.example", "ae@acme.example"}}
	known := []salesMessage{{From: "lead@customer.example", To: []string{"rep@acme.example"}, CC: []string{"ae@acme.example"}, MessageID: "<root@customer.example>"}}
	if !salesMailEligible(c, m, known) {
		t.Fatal("known colleague on CC blocks Sales capture")
	}
	m.Participants = append(m.Participants, "hr@acme.example")
	if salesMailEligible(c, m, known) {
		t.Fatal("unrelated internal mail captured")
	}
	m.Participants = m.Participants[:3]
	c.personal.BlockedAddresses = []string{"ae@acme.example"}
	if salesMailEligible(c, m, known) {
		t.Fatal("explicit exclusion bypassed by handoff")
	}
	if !salesThreadMatches(salesMessage{References: "<root@customer.example> <later@customer.example>"}, known) {
		t.Fatal("original thread lost")
	}
	if salesThreadMatches(salesMessage{Body: "same body", Subject: "same subject", MessageID: "<other@customer.example>"}, known) {
		t.Fatal("unrelated thread matched without identity")
	}
}
func TestSalesTemplateResolutionAndThreadEnvelope(t *testing.T) {
	got := resolveSalesTemplate("Hi {first_name} at {company}, from {my_name}", salesLead{Name: "Ana Example", Company: "Example"}, salesMember{Name: "Bo"})
	if got != "Hi Ana at Example, from Bo" {
		t.Fatal(got)
	}
	m := salesMessage{To: []string{"lead@customer.example"}, CC: []string{"ae@acme.example", "rep@acme.example", "ae@acme.example"}}
	salesRecipients(&m, "rep@acme.example", "ae@acme.example")
	if len(m.CC) != 1 || m.CC[0] != "ae@acme.example" {
		t.Fatal(m.CC)
	}
	m.MessageID = "<send@outflare.invalid>"
	m.InReplyTo = "<root@customer.example>"
	m.References = m.InReplyTo
	m.Subject = "Re: Conversation"
	m.Body = "Hello"
	raw := salesRFC(m)
	for _, header := range []string{"Cc: ae@acme.example", "In-Reply-To: <root@customer.example>", "Message-ID: <send@outflare.invalid>"} {
		if !strings.Contains(raw, header) {
			t.Fatal("missing thread header", header)
		}
	}
}
