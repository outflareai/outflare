package mailbox

import (
	"fmt"
	"strings"
)

// DomainFacts is what DomainIssues needs to know about a sending domain.
type DomainFacts struct {
	Domain     string
	Checked    bool // false until the first DNS check has run
	Paused     bool
	MXProvider string
	// MailHost is the mail host every mailbox on the domain is on (google
	// or microsoft), or empty when they differ or there are none. It
	// stands in for MXProvider while the domain has no MX record.
	MailHost   string
	SPF        string
	SPFRecord  string
	DKIM       string
	DMARC      string
	Blocklists []Blocklist
	Mailboxes  int
	Failing    int // mailboxes whose connection check failed
	Sent7d     int
	Bounces7d  int
}

// Issue is one thing to fix or know about a domain, with the fix.
type Issue struct {
	Code     string     `json:"code"`
	Severity string     `json:"severity"` // critical, warning, info
	Title    string     `json:"title"`
	Detail   string     `json:"detail"`
	Record   *DNSRecord `json:"record,omitempty"`
}

// DNSRecord is a record to publish at the domain's DNS host.
type DNSRecord struct {
	Type  string `json:"type"`
	Host  string `json:"host"`
	Value string `json:"value"`
	// Priority is an MX record's preference.
	Priority int `json:"priority,omitempty"`
}

// spfIncludes are the SPF includes each mail host's servers send from.
var spfIncludes = map[string]string{
	ProviderGoogle:    "include:_spf.google.com",
	ProviderMicrosoft: "include:spf.protection.outlook.com",
}

// Bounce rates above these hurt a domain's reputation, and above the
// second, mailbox providers start rejecting or junking its mail.
const (
	bounceWarn     = 0.02
	bounceCritical = 0.05
	bounceMinSent  = 20 // fewer sends than this say too little
)

// DomainIssues lists what is wrong with a domain, most severe first, and
// how to fix each.
func DomainIssues(f DomainFacts) []Issue {
	var out []Issue
	add := func(i Issue) { out = append(out, i) }

	if f.Paused {
		add(Issue{Code: "paused", Severity: "info", Title: "Sending is paused",
			Detail: "No sequence or test emails go out from this domain's mailboxes. Replies you write in the inbox still send."})
	}
	if f.Mailboxes == 0 {
		add(Issue{Code: "no_mailboxes", Severity: "info", Title: "No mailboxes on this domain yet",
			Detail: "Connect mailboxes on " + f.Domain + " in Mailboxes to send from it."})
	}
	if f.Failing > 0 {
		add(Issue{Code: "mailboxes_failing", Severity: "warning", Title: Plural(f.Failing, "mailbox can't", "mailboxes can't") + " connect",
			Detail: "They are out of rotation until their sign-in works. See the error on each mailbox below, fix it, then re-check."})
	}
	if !f.Checked {
		return sortIssues(out)
	}

	for _, b := range f.Blocklists {
		if b.Status == "listed" {
			add(Issue{Code: "blocklisted", Severity: "critical", Title: "Listed on " + b.Name,
				Detail: "Mailbox providers junk or reject mail that links to or comes from a listed domain. Pause the domain, find the cause (often a bought list or high bounces), then request removal at " + delistURL(b.Zone) + "."})
		}
	}
	if f.Sent7d >= bounceMinSent {
		rate := float64(f.Bounces7d) / float64(f.Sent7d)
		pct := fmt.Sprintf("%.1f%%", rate*100)
		switch {
		case rate > bounceCritical:
			add(Issue{Code: "bounce_rate", Severity: "critical", Title: "Bounce rate is " + pct + " over 7 days",
				Detail: "Above 5% providers start blocking the domain. Pause it and verify your lead list before sending again."})
		case rate > bounceWarn:
			add(Issue{Code: "bounce_rate", Severity: "warning", Title: "Bounce rate is " + pct + " over 7 days",
				Detail: "Keep it under 2%: verify emails before importing leads, and remove role and catch-all addresses."})
		}
	}

	host := f.MXProvider
	if host == "" || host == "none" {
		host = f.MailHost
	}
	include := spfIncludes[host]
	switch f.SPF {
	case "missing":
		i := Issue{Code: "spf_missing", Severity: "critical", Title: "No SPF record",
			Detail: "Google, Yahoo and Microsoft reject or junk bulk mail without SPF. Add this TXT record at your DNS host."}
		if include != "" {
			i.Record = &DNSRecord{Type: "TXT", Host: "@", Value: "v=spf1 " + include + " ~all"}
		} else {
			i.Detail = "Google, Yahoo and Microsoft reject or junk bulk mail without SPF. Ask your inbox provider for its SPF include and add a TXT record at @ like v=spf1 include:<their SPF domain> ~all."
		}
		add(i)
	case "invalid":
		add(Issue{Code: "spf_multiple", Severity: "critical", Title: "More than one SPF record",
			Detail: "A domain may have only one SPF record; with two, SPF fails everywhere. Merge them into one TXT record that lists every include."})
	case "pass":
		rec := strings.ToLower(f.SPFRecord)
		if include != "" && !strings.Contains(rec, include) {
			add(Issue{Code: "spf_provider", Severity: "warning", Title: "SPF does not include " + providerName(host),
				Detail: "Mail sent through " + providerName(host) + " fails SPF until the record includes it. Change the SPF record to the one below, which adds " + include + " and keeps everything already in it.",
				Record: &DNSRecord{Type: "TXT", Host: "@", Value: withInclude(f.SPFRecord, include)}})
		}
		if strings.Contains(rec, "+all") || strings.Contains(rec, "?all") {
			add(Issue{Code: "spf_permissive", Severity: "warning", Title: "SPF lets any server send as this domain",
				Detail: "End the record with ~all (or -all) instead of +all or ?all."})
		}
	}

	if f.DKIM == "missing" {
		add(Issue{Code: "dkim_missing", Severity: "critical", Title: "No DKIM key found", Detail: dkimHelp(host)})
	}

	switch f.DMARC {
	case "missing":
		add(Issue{Code: "dmarc_missing", Severity: "critical", Title: "No DMARC record",
			Detail: "Google and Yahoo require DMARC from bulk senders. Start with p=none, then move to p=quarantine once SPF and DKIM pass.",
			Record: &DNSRecord{Type: "TXT", Host: "_dmarc", Value: "v=DMARC1; p=none;"}})
	case "none_policy":
		add(Issue{Code: "dmarc_none", Severity: "info", Title: "DMARC policy is p=none",
			Detail: "This meets Google and Yahoo's rules. Once SPF and DKIM pass, p=quarantine tells receivers you stand behind your mail."})
	}

	if f.MXProvider == "none" {
		i := Issue{Code: "mx_missing", Severity: "warning", Title: "No MX record",
			Detail: "Replies to this domain cannot be delivered, and receivers trust domains that can't receive mail less. Add the MX records your inbox provider gives you."}
		switch f.MailHost {
		case ProviderGoogle:
			i.Detail = "Replies to this domain cannot be delivered, and receivers trust domains that can't receive mail less. Its mailboxes are on Google Workspace, so add Google's MX record."
			i.Record = &DNSRecord{Type: "MX", Host: "@", Value: "smtp.google.com", Priority: 1}
		case ProviderMicrosoft:
			i.Detail = "Replies to this domain cannot be delivered, and receivers trust domains that can't receive mail less. Its mailboxes are on Microsoft 365: add the MX record the Microsoft 365 admin center shows under Settings > Domains (it ends in mail.protection.outlook.com)."
		}
		add(i)
	}
	return sortIssues(out)
}

// Health sums up issues: critical, warning, or healthy. A domain not yet
// checked is pending.
func Health(checked bool, issues []Issue) string {
	if !checked {
		return "pending"
	}
	h := "healthy"
	for _, i := range issues {
		switch i.Severity {
		case "critical":
			return "critical"
		case "warning":
			h = "warning"
		}
	}
	return h
}

// withInclude puts include first in an SPF record, keeping the rest.
func withInclude(spf, include string) string {
	f := strings.Fields(spf)
	if len(f) == 0 || !strings.EqualFold(f[0], "v=spf1") {
		return "v=spf1 " + include + " ~all"
	}
	return strings.Join(append([]string{"v=spf1", include}, f[1:]...), " ")
}

func sortIssues(in []Issue) []Issue {
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	out := make([]Issue, 0, len(in))
	for r := 0; r < 3; r++ {
		for _, i := range in {
			if rank[i.Severity] == r {
				out = append(out, i)
			}
		}
	}
	return out
}

func dkimHelp(provider string) string {
	switch provider {
	case ProviderGoogle:
		return "In the Google Admin console open Apps > Google Workspace > Gmail > Authenticate email, generate a key for this domain, publish the TXT record it shows at google._domainkey, then click Start authentication."
	case ProviderMicrosoft:
		return "In the Microsoft Defender portal open Email & collaboration > Policies & rules > Threat policies > Email authentication settings > DKIM, select this domain, publish the two CNAME records (selector1._domainkey and selector2._domainkey) and turn signing on."
	}
	return "Ask your inbox provider for the domain's DKIM record and publish it. Outflare looks for the common selectors (google, selector1, selector2, default, dkim, mail, k1, s1, s2); a key under another selector shows as missing here even when it works."
}

func providerName(p string) string {
	switch p {
	case ProviderGoogle:
		return "Google Workspace"
	case ProviderMicrosoft:
		return "Microsoft 365"
	}
	return p
}

func delistURL(zone string) string {
	if strings.Contains(zone, "spamhaus") {
		return "https://check.spamhaus.org"
	}
	return "https://surbl.org/surbl-analysis"
}

func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
