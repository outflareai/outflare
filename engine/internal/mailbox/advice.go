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
	SPF        string
	SPFRecord  string
	DKIM       string
	DMARC      string
	Blocklists []Blocklist
	Mailboxes  int
	Failing    int // mailboxes whose connection check failed
	Sent7d     int
	Bounces7d  int
	// Warm-up mail placed in the last 7 days, and how much reached the inbox.
	WarmupPlaced, WarmupInbox int
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

	warmupWarn      = 0.9
	warmupCritical  = 0.75
	warmupMinPlaced = 20
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

	if f.WarmupPlaced >= warmupMinPlaced {
		rate := float64(f.WarmupInbox) / float64(f.WarmupPlaced)
		pct := fmt.Sprintf("%.0f%%", rate*100)
		switch {
		case rate < warmupCritical:
			add(Issue{Code: "inbox_placement", Severity: "critical", Title: "Only " + pct + " of warm-up mail reached the inbox",
				Detail: "Most mail from this domain is going to spam. Pause cold sending from it, fix any DNS issues above, and let warm-up run until placement is back above 90%."})
		case rate < warmupWarn:
			add(Issue{Code: "inbox_placement", Severity: "warning", Title: pct + " of warm-up mail reached the inbox",
				Detail: "Some mail from this domain lands in spam. Lower its mailboxes' daily limits and keep warm-up on until placement is above 90%."})
		}
	}

	include := spfIncludes[f.MXProvider]
	switch f.SPF {
	case "missing":
		i := Issue{Code: "spf_missing", Severity: "critical", Title: "No SPF record",
			Detail: "Google, Yahoo and Microsoft reject or junk bulk mail without SPF. Add this TXT record at your DNS host."}
		if include != "" {
			i.Record = &DNSRecord{"TXT", "@", "v=spf1 " + include + " ~all"}
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
			add(Issue{Code: "spf_provider", Severity: "warning", Title: "SPF does not include " + providerName(f.MXProvider),
				Detail: "Mail sent through " + providerName(f.MXProvider) + " fails SPF until the record includes it. Add " + include + " before the all mechanism."})
		}
		if strings.Contains(rec, "+all") || strings.Contains(rec, "?all") {
			add(Issue{Code: "spf_permissive", Severity: "warning", Title: "SPF lets any server send as this domain",
				Detail: "End the record with ~all (or -all) instead of +all or ?all."})
		}
	}

	if f.DKIM == "missing" {
		add(Issue{Code: "dkim_missing", Severity: "critical", Title: "No DKIM key found", Detail: dkimHelp(f.MXProvider)})
	}

	switch f.DMARC {
	case "missing":
		add(Issue{Code: "dmarc_missing", Severity: "critical", Title: "No DMARC record",
			Detail: "Google and Yahoo require DMARC from bulk senders. Start with p=none, then move to p=quarantine once SPF and DKIM pass.",
			Record: &DNSRecord{"TXT", "_dmarc", "v=DMARC1; p=none"}})
	case "none_policy":
		add(Issue{Code: "dmarc_none", Severity: "info", Title: "DMARC policy is p=none",
			Detail: "This meets Google and Yahoo's rules. Once SPF and DKIM pass, p=quarantine tells receivers you stand behind your mail."})
	}

	if f.MXProvider == "none" {
		add(Issue{Code: "mx_missing", Severity: "warning", Title: "No MX record",
			Detail: "Replies to this domain cannot be delivered, and receivers trust domains that can't receive mail less. Add the MX records your inbox provider gives you."})
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
