package mailbox

import (
	"context"
	"errors"
	"net"
	"slices"
	"sort"
	"strings"
)

// DomainHealth is the sending-authentication state of a domain. Google, Yahoo
// and Microsoft reject bulk mail that fails SPF, DKIM and DMARC, so these are
// checked for every sending domain.
type DomainHealth struct {
	Domain       string      `json:"domain"`
	MXProvider   string      `json:"mx_provider"` // google, microsoft, other, none
	MXHosts      []string    `json:"mx_hosts"`
	SPF          string      `json:"spf"` // pass, missing, invalid (more than one record)
	SPFRecord    string      `json:"spf_record,omitempty"`
	DKIM         string      `json:"dkim"` // pass, missing
	DKIMSelector string      `json:"dkim_selector,omitempty"`
	DMARC        string      `json:"dmarc"` // pass, none_policy, missing
	DMARCRecord  string      `json:"dmarc_record,omitempty"`
	DMARCPolicy  string      `json:"dmarc_policy,omitempty"` // none, quarantine, reject
	Blocklists   []Blocklist `json:"blocklists"`
}

// Blocklist is the domain's standing on one domain blocklist.
type Blocklist struct {
	Name   string `json:"name"`
	Zone   string `json:"zone"`
	Status string `json:"status"` // clear, listed, unknown
	Detail string `json:"detail,omitempty"`
}

// HostResolver looks up A records. net.Resolver implements it; a Resolver
// without it skips the blocklist checks.
type HostResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// blocklists are domain (not IP) blocklists that mailbox providers consult.
// Both refuse some resolvers, notably public ones such as 8.8.8.8, and say
// so with a special answer, which is reported as unknown rather than clear.
var blocklists = []struct {
	name, zone string
	classify   func(ip string) (status, detail string)
}{
	{"Spamhaus DBL", "dbl.spamhaus.org", func(ip string) (string, string) {
		switch {
		case strings.HasPrefix(ip, "127.0.1."):
			return "listed", "listed (" + ip + ")"
		case strings.HasPrefix(ip, "127.255.255."):
			return "unknown", "Spamhaus refused the query; it does not answer public DNS resolvers"
		}
		return "unknown", "unexpected answer " + ip
	}},
	{"SURBL", "multi.surbl.org", func(ip string) (string, string) {
		switch {
		case ip == "127.0.0.1":
			return "unknown", "SURBL refused the query from this resolver"
		case strings.HasPrefix(ip, "127.0.0."):
			return "listed", "listed (" + ip + ")"
		}
		return "unknown", "unexpected answer " + ip
	}},
}

func checkBlocklists(ctx context.Context, res Resolver, domain string) []Blocklist {
	out := make([]Blocklist, 0, len(blocklists))
	hr, ok := res.(HostResolver)
	for _, bl := range blocklists {
		b := Blocklist{Name: bl.name, Zone: bl.zone, Status: "unknown"}
		if !ok {
			out = append(out, b)
			continue
		}
		ips, err := hr.LookupHost(ctx, domain+"."+bl.zone)
		var dnsErr *net.DNSError
		switch {
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			b.Status = "clear"
		case err != nil:
			b.Detail = "lookup failed"
		case len(ips) > 0:
			b.Status, b.Detail = bl.classify(ips[0])
		}
		out = append(out, b)
	}
	return out
}

// dkimSelectors are tried in order. DKIM keys live under a selector the
// domain chooses, so without knowing it we probe the providers' defaults and
// the common choices.
var dkimSelectors = map[string][]string{
	ProviderGoogle:    {"google"},
	ProviderMicrosoft: {"selector1", "selector2"},
	"":                {"default", "dkim", "mail", "k1", "s1", "s2", "selector1", "google"},
}

// CheckDomain looks up MX, SPF, DKIM and DMARC for a domain.
func CheckDomain(ctx context.Context, res Resolver, domain string) DomainHealth {
	h := DomainHealth{Domain: domain, SPF: "missing", DKIM: "missing", DMARC: "missing"}

	mxs, _ := res.LookupMX(ctx, domain)
	// Sort a copy: a resolver may hand the same slice to concurrent checks.
	mxs = slices.Clone(mxs)
	h.MXProvider = ProviderFromMX(mxs)
	if h.MXProvider == "" {
		h.MXProvider = "none"
	}
	sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
	h.MXHosts = []string{}
	for _, mx := range mxs {
		h.MXHosts = append(h.MXHosts, strings.TrimSuffix(mx.Host, "."))
	}

	txts, _ := res.LookupTXT(ctx, domain)
	var spf []string
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=spf1") {
			spf = append(spf, t)
		}
	}
	switch len(spf) {
	case 0:
	case 1:
		h.SPF, h.SPFRecord = "pass", spf[0]
	default:
		h.SPF, h.SPFRecord = "invalid", strings.Join(spf, " | ")
	}

	selectors := append([]string{}, dkimSelectors[h.MXProvider]...)
	selectors = append(selectors, dkimSelectors[""]...)
	seen := map[string]bool{}
	for _, sel := range selectors {
		if seen[sel] {
			continue
		}
		seen[sel] = true
		recs, _ := res.LookupTXT(ctx, sel+"._domainkey."+domain)
		if hasDKIMKey(recs) {
			h.DKIM, h.DKIMSelector = "pass", sel
			break
		}
	}

	dmarc, _ := res.LookupTXT(ctx, "_dmarc."+domain)
	for _, t := range dmarc {
		lt := strings.ToLower(strings.ReplaceAll(t, " ", ""))
		if strings.HasPrefix(lt, "v=dmarc1") {
			h.DMARCRecord = t
			h.DMARCPolicy = dmarcTag(lt, "p")
			if h.DMARCPolicy == "none" {
				h.DMARC = "none_policy"
			} else {
				h.DMARC = "pass"
			}
			break
		}
	}
	h.Blocklists = checkBlocklists(ctx, res, domain)
	return h
}

// SimulatedDomain is a domain in a test workspace: set up as a healthy one
// would be, without asking DNS about a domain that may not exist.
func SimulatedDomain(domain string) DomainHealth {
	return DomainHealth{
		Domain: domain, MXProvider: ProviderSimulated, MXHosts: []string{SimulatedHost},
		SPF: "pass", SPFRecord: "v=spf1 include:" + SimulatedHost + " -all",
		DKIM: "pass", DKIMSelector: "simulated",
		DMARC: "pass", DMARCRecord: "v=DMARC1; p=reject", DMARCPolicy: "reject",
		Blocklists: []Blocklist{},
	}
}

// dmarcTag returns a tag's value from a lowercased DMARC record without spaces.
func dmarcTag(rec, tag string) string {
	for _, part := range strings.Split(rec, ";") {
		if k, v, ok := strings.Cut(part, "="); ok && k == tag {
			return v
		}
	}
	return ""
}

func hasDKIMKey(recs []string) bool {
	joined := strings.ReplaceAll(strings.Join(recs, ""), " ", "")
	i := strings.Index(joined, "p=")
	if i < 0 {
		return false
	}
	rest := joined[i+2:]
	return rest != "" && rest[0] != ';' // "p=" with no key means revoked
}
