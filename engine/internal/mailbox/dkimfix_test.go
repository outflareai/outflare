package mailbox_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// The fixes Outflare can add for a cold email domain, from what its
// mailboxes are on while it has no MX record yet.
func TestDomainFixRecords(t *testing.T) {
	issue := func(f mailbox.DomainFacts, code string) *mailbox.Issue {
		for _, i := range mailbox.DomainIssues(f) {
			if i.Code == code {
				return &i
			}
		}
		return nil
	}
	f := mailbox.DomainFacts{Domain: "northpeak.example", Checked: true, MXProvider: "none", MailHost: mailbox.ProviderGoogle, SPF: "missing", DKIM: "missing", DMARC: "missing"}
	if i := issue(f, "mx_missing"); i == nil || i.Record == nil || *i.Record != (mailbox.DNSRecord{Type: "MX", Host: "@", Value: "smtp.google.com", Priority: 1}) {
		t.Fatalf("mx: %+v", i)
	}
	if i := issue(f, "spf_missing"); i == nil || i.Record == nil || i.Record.Value != "v=spf1 include:_spf.google.com ~all" {
		t.Fatalf("spf: %+v", i)
	}
	if i := issue(f, "dmarc_missing"); i == nil || i.Record.Value != "v=DMARC1; p=none;" {
		t.Fatalf("dmarc: %+v", i)
	}
	// Microsoft's MX host isn't known until its admin shows it.
	f.MailHost = mailbox.ProviderMicrosoft
	if i := issue(f, "mx_missing"); i == nil || i.Record != nil {
		t.Fatalf("microsoft mx: %+v", i)
	}
	// An SPF record without the mail host gets it first, keeping the rest.
	f = mailbox.DomainFacts{Domain: "northpeak.example", Checked: true, MXProvider: mailbox.ProviderGoogle, SPF: "pass", SPFRecord: "v=spf1 include:mailgun.org ~all", DKIM: "pass", DMARC: "pass"}
	if i := issue(f, "spf_provider"); i == nil || i.Record == nil || i.Record.Value != "v=spf1 include:_spf.google.com include:mailgun.org ~all" {
		t.Fatalf("spf provider: %+v", i)
	}
}

func TestDKIMRecords(t *testing.T) {
	g, err := mailbox.DKIMRecords(mailbox.ProviderGoogle, []string{` "v=DKIM1; k=rsa;" "p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A" `})
	if err != nil || len(g) != 1 || g[0] != (mailbox.DNSRecord{Type: "TXT", Host: "google._domainkey", Value: "v=DKIM1; k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A"}) {
		t.Fatalf("google: %+v %v", g, err)
	}
	m, err := mailbox.DKIMRecords(mailbox.ProviderMicrosoft, []string{
		"selector1-northpeak-example._domainkey.northpeak.onmicrosoft.com.",
		"Selector2-northpeak-example._domainkey.northpeak.n-v1.dkim.mail.microsoft",
	})
	if err != nil || len(m) != 2 || m[0].Host != "selector1._domainkey" || m[1].Value != "selector2-northpeak-example._domainkey.northpeak.n-v1.dkim.mail.microsoft" {
		t.Fatalf("microsoft: %+v %v", m, err)
	}
	for _, c := range []struct {
		host   string
		values []string
	}{
		{mailbox.ProviderGoogle, []string{"p=MIIB"}},
		{mailbox.ProviderGoogle, []string{"v=DKIM1; k=rsa; p=MIIB; x=evil"}},
		{mailbox.ProviderGoogle, nil},
		{mailbox.ProviderMicrosoft, []string{"selector2-a._domainkey.a.onmicrosoft.com", "selector1-a._domainkey.a.onmicrosoft.com"}},
		{mailbox.ProviderMicrosoft, []string{"selector1-a._domainkey.a.onmicrosoft.com"}},
		{mailbox.ProviderMicrosoft, []string{"selector1-a._domainkey.a.onmicrosoft.com", "attacker.example"}},
		{mailbox.ProviderOther, []string{"v=DKIM1; k=rsa; p=MIIB"}},
	} {
		if _, err := mailbox.DKIMRecords(c.host, c.values); err == nil {
			t.Errorf("%s %q: no error", c.host, c.values)
		}
	}
}
