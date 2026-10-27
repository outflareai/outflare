package domainconnect

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// Template is a Domain Connect template (the spec's template definition).
// Templates are defined here and written to templates/ (TestTemplateFiles),
// which is what gets submitted to the public template repository.
type Template struct {
	ProviderID          string   `json:"providerId"`
	ProviderName        string   `json:"providerName"`
	ServiceID           string   `json:"serviceId"`
	ServiceName         string   `json:"serviceName"`
	Version             int      `json:"version"`
	LogoURL             string   `json:"logoUrl"`
	Description         string   `json:"description"`
	VariableDescription string   `json:"variableDescription,omitempty"`
	SyncBlock           bool     `json:"syncBlock"`
	SyncPubKeyDomain    string   `json:"syncPubKeyDomain"`
	SyncRedirectDomain  string   `json:"syncRedirectDomain"`
	HostRequired        bool     `json:"hostRequired,omitempty"`
	Records             []Record `json:"records"`

	// Kind is what the template is for: site, tracking or email.
	Kind string `json:"-"`
}

// Record is one record of a template. Host, PointsTo, Data and SPFRules
// may hold %variables%.
type Record struct {
	Type                      string `json:"type"`
	GroupID                   string `json:"groupId,omitempty"`
	Host                      string `json:"host"`
	PointsTo                  string `json:"pointsTo,omitempty"`
	Data                      string `json:"data,omitempty"`
	Priority                  int    `json:"priority,omitempty"`
	SPFRules                  string `json:"spfRules,omitempty"`
	TTL                       int    `json:"ttl,omitempty"`
	TxtConflictMatchingMode   string `json:"txtConflictMatchingMode,omitempty"`
	TxtConflictMatchingPrefix string `json:"txtConflictMatchingPrefix,omitempty"`
	// Essential OnApply lets the person change or remove the record later
	// without the DNS provider treating the template as broken.
	Essential string `json:"essential,omitempty"`
}

const (
	logo = "https://outflare.ai/brand/png/outflare-mark-black-256.png"
	ttl  = 3600
)

func template(kind, id, name, desc, vars string, recs ...Record) Template {
	return Template{ProviderID: ProviderID, ProviderName: "Outflare", ServiceID: id, ServiceName: name, Version: 1, LogoURL: logo,
		Description: desc, VariableDescription: vars, SyncPubKeyDomain: PubKeyDomain, SyncRedirectDomain: "app.outflare.ai", Records: recs, Kind: kind}
}

var dmarc = Record{Type: "TXT", GroupID: "dmarc", Host: "_dmarc", Data: "v=DMARC1; p=none;", TTL: ttl,
	TxtConflictMatchingMode: "Prefix", TxtConflictMatchingPrefix: "v=DMARC1", Essential: "OnApply"}

func hostRequired(t Template) Template { t.HostRequired = true; return t }

// Templates are every template Outflare uses, one per kind of change.
// Outflare's own (site, tracking) fix everything but a token. The sending
// domain ones fix each provider's hosts and targets and leave only its
// keys and region to the request. The mailbox ones set up a cold email
// domain's mail host, and the general ones (kind "any") add a single
// record another service asks for. Every request is signed, so only
// Outflare can fill the variables in.
var Templates = []Template{
	// Outflare's own records.
	template("site", "siteverify", "Site ownership",
		"Proves the site is yours so Outflare can audit it: a TXT record with the site's token.",
		"token: the site's verification token",
		Record{Type: "TXT", Host: "@", Data: "outflare-site-verification=%token%", TTL: ttl,
			TxtConflictMatchingMode: "Prefix", TxtConflictMatchingPrefix: "outflare-site-verification=", Essential: "OnApply"}),
	hostRequired(template("tracking", "trackingdomain", "Tracking domain",
		"Points a subdomain at Outflare so the links and opens in your emails, and your website tracking, use your own domain.", "",
		Record{Type: "CNAME", Host: "@", PointsTo: "app.outflare.ai", TTL: ttl})),

	// Sending domains on the workspace's own email provider account.
	template("email", "ses", "Sending domain (Amazon SES)",
		"Lets Amazon SES send email for the domain from your own SES account: DKIM, a MAIL FROM subdomain (mail) with its SPF, and a DMARC policy when there is none.",
		"dkim1-3: the SES DKIM tokens; dkimzone: SES's DKIM zone for the region (dkim.amazonses.com); region: the SES region (us-east-1)",
		Record{Type: "CNAME", GroupID: "dkim", Host: "%dkim1%._domainkey", PointsTo: "%dkim1%.%dkimzone%", TTL: ttl},
		Record{Type: "CNAME", GroupID: "dkim", Host: "%dkim2%._domainkey", PointsTo: "%dkim2%.%dkimzone%", TTL: ttl},
		Record{Type: "CNAME", GroupID: "dkim", Host: "%dkim3%._domainkey", PointsTo: "%dkim3%.%dkimzone%", TTL: ttl},
		Record{Type: "MX", GroupID: "mailfrom", Host: "mail", PointsTo: "feedback-smtp.%region%.amazonses.com", Priority: 10, TTL: ttl},
		Record{Type: "SPFM", GroupID: "mailfrom", Host: "mail", SPFRules: "include:amazonses.com"},
		dmarc),
	template("email", "resend", "Sending domain (Resend)",
		"Lets Resend send email for the domain from your own Resend account: DKIM, a sending subdomain (send) with its SPF, and a DMARC policy when there is none.",
		"dkim: the DKIM public key (p=...); region: Resend's sending region (us-east-1)",
		Record{Type: "TXT", GroupID: "dkim", Host: "resend._domainkey", Data: "%dkim%", TTL: ttl, TxtConflictMatchingMode: "All"},
		Record{Type: "MX", GroupID: "mailfrom", Host: "send", PointsTo: "feedback-smtp.%region%.amazonses.com", Priority: 10, TTL: ttl},
		Record{Type: "SPFM", GroupID: "mailfrom", Host: "send", SPFRules: "include:amazonses.com"},
		dmarc),
	template("email", "postmark", "Sending domain (Postmark)",
		"Lets Postmark send email for the domain from your own Postmark account: DKIM, a return-path subdomain (pm-bounces), and a DMARC policy when there is none.",
		"selector: the DKIM selector (20240101pm); dkim: the DKIM key (k=rsa;p=...)",
		Record{Type: "TXT", GroupID: "dkim", Host: "%selector%._domainkey", Data: "%dkim%", TTL: ttl, TxtConflictMatchingMode: "All"},
		Record{Type: "CNAME", GroupID: "returnpath", Host: "pm-bounces", PointsTo: "pm.mtasv.net", TTL: ttl},
		dmarc),

	// Cold email domains on the workspace's own mail host.
	template("mailbox", "googleworkspace", "Email domain (Google Workspace)",
		"Sets up a domain's mail on your own Google Workspace: its MX record, Google in its SPF record, its DKIM key, and a DMARC policy when there is none.",
		"dkimkey: the public key Google Admin shows for google._domainkey (the part after p=)",
		Record{Type: "MX", GroupID: "mx", Host: "@", PointsTo: "smtp.google.com", Priority: 1, TTL: ttl},
		Record{Type: "SPFM", GroupID: "spf", Host: "@", SPFRules: "include:_spf.google.com"},
		Record{Type: "TXT", GroupID: "dkim", Host: "google._domainkey", Data: "v=DKIM1; k=rsa; p=%dkimkey%", TTL: ttl, TxtConflictMatchingMode: "All"},
		dmarc),
	template("mailbox", "microsoft365", "Email domain (Microsoft 365)",
		"Sets up a domain's mail on your own Microsoft 365: its MX and Autodiscover records, Microsoft in its SPF record, its two DKIM records, and a DMARC policy when there is none.",
		"mx: the MX host Microsoft 365 shows (example-com.mail.protection.outlook.com); dkim1, dkim2: the targets Microsoft shows for selector1 and selector2",
		Record{Type: "MX", GroupID: "mx", Host: "@", PointsTo: "%mx%", Priority: 0, TTL: ttl},
		Record{Type: "CNAME", GroupID: "mx", Host: "autodiscover", PointsTo: "autodiscover.outlook.com", TTL: ttl},
		Record{Type: "SPFM", GroupID: "spf", Host: "@", SPFRules: "include:spf.protection.outlook.com"},
		Record{Type: "CNAME", GroupID: "dkim", Host: "selector1._domainkey", PointsTo: "%dkim1%", TTL: ttl},
		Record{Type: "CNAME", GroupID: "dkim", Host: "selector2._domainkey", PointsTo: "%dkim2%", TTL: ttl},
		dmarc),

	// One record another service asks for.
	template("any", "dmarc", "DMARC policy",
		"Adds or updates the domain's DMARC policy, which Google, Yahoo and Microsoft require from bulk senders.",
		"policy: the policy after v=DMARC1; (p=none, or p=quarantine with reporting tags)",
		Record{Type: "TXT", Host: "_dmarc", Data: "v=DMARC1; %policy%", TTL: ttl,
			TxtConflictMatchingMode: "Prefix", TxtConflictMatchingPrefix: "v=DMARC1", Essential: "OnApply"}),
	template("any", "spf", "SPF include",
		"Adds a mail service you use to the domain's SPF record, keeping everything already in it.",
		"include: the SPF domain the mail service gives you (the part after include:)",
		Record{Type: "SPFM", Host: "@", SPFRules: "include:%include%"}),
	template("any", "dkim", "DKIM key",
		"Publishes the DKIM key a mail service gives you, so mail it sends for the domain is signed.",
		"selector: the DKIM selector; key: the whole DKIM record value, which differs between mail services (v=DKIM1; k=rsa; p=...)",
		Record{Type: "TXT", Host: "%selector%._domainkey", Data: "%key%", TTL: ttl, TxtConflictMatchingMode: "All"}),
	template("any", "dkimcname", "DKIM key (CNAME)",
		"Points a DKIM selector at the key a mail service hosts, so mail it sends for the domain is signed.",
		"selector: the DKIM selector; target: the host the mail service gives for it",
		Record{Type: "CNAME", Host: "%selector%._domainkey", PointsTo: "%target%", TTL: ttl}),
	hostRequired(template("any", "servicecname", "Subdomain for a service",
		"Points a subdomain at a service you use, such as a sequencer's custom tracking domain.",
		"target: the host the service gives you",
		Record{Type: "CNAME", Host: "@", PointsTo: "%target%", TTL: ttl})),
}

// MarshalJSON writes an MX record's priority even when it is 0.
func (r Record) MarshalJSON() ([]byte, error) {
	type plain Record
	if r.Type != "MX" {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Priority int `json:"priority"`
	}{plain(r), r.Priority})
}

// File is the template's file name in the public repository.
func (t Template) File() string { return t.ProviderID + "." + t.ServiceID + ".json" }

// JSON is the template as submitted.
func (t Template) JSON() []byte {
	b, _ := json.MarshalIndent(t, "", "  ")
	return append(b, '\n')
}

// Rec is a record Outflare wants in DNS: an absolute name, no trailing dot.
type Rec struct {
	Type     string
	Name     string
	Value    string
	Priority int
}

// Plan is how to apply records with a template: the groups to apply and
// the values of their variables.
type Plan struct {
	Template Template
	Groups   []string
	Vars     map[string]string
}

// PlanFor finds the template of kind that covers want, the records still
// missing at fqdn, with variables filled from all (want and the records
// already in place, which a group may share). ok is false when none fits.
func PlanFor(kind, fqdn string, all, want []Rec) (Plan, bool) {
	for _, t := range Templates {
		if t.Kind != kind {
			continue
		}
		if p, ok := t.plan(fqdn, all, want); ok {
			return p, true
		}
	}
	return Plan{}, false
}

func (t Template) plan(fqdn string, all, want []Rec) (Plan, bool) {
	vars := map[string]string{}
	used := make([]bool, len(t.Records))
	// which template record each of all matched
	at := make([]int, len(all))
	for i, r := range all {
		at[i] = -1
		for j, tr := range t.Records {
			if used[j] {
				continue
			}
			if got, ok := match(tr, fqdn, r, vars); ok {
				for k, v := range got {
					vars[k] = v
				}
				used[j], at[i] = true, j
				break
			}
		}
	}
	var groups []string
	for _, w := range want {
		i := slices.IndexFunc(all, func(r Rec) bool { return r == w })
		if i < 0 || at[i] < 0 {
			return Plan{}, false
		}
		if g := t.Records[at[i]].GroupID; g != "" && !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	// Every variable the applied records use needs a value.
	need := map[string]bool{}
	for _, tr := range t.Records {
		if len(groups) == 0 || slices.Contains(groups, tr.GroupID) {
			for _, v := range varsIn(tr.Host + " " + tr.PointsTo + " " + tr.Data + " " + tr.SPFRules) {
				need[v] = true
			}
		}
	}
	out := map[string]string{}
	for v := range need {
		if vars[v] == "" {
			return Plan{}, false
		}
		out[v] = vars[v]
	}
	if !slices.ContainsFunc(t.Records, func(r Record) bool { return r.GroupID != "" }) {
		groups = nil
	}
	return Plan{Template: t, Groups: groups, Vars: out}, true
}

var varRe = regexp.MustCompile(`%([a-z0-9]+)%`)

func varsIn(s string) []string {
	var out []string
	for _, m := range varRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// match checks a record against a template record, given the variables
// bound so far, and returns the variables it binds.
func match(tr Record, fqdn string, r Rec, bound map[string]string) (map[string]string, bool) {
	typ := strings.ToUpper(r.Type)
	got := map[string]string{}
	host, ok := relative(r.Name, fqdn)
	if !ok {
		return nil, false
	}
	if !matchPattern(tr.Host, host, label, bound, got) {
		return nil, false
	}
	switch tr.Type {
	case "CNAME":
		return got, typ == "CNAME" && matchPattern(tr.PointsTo, strings.TrimSuffix(r.Value, "."), name, bound, got)
	case "MX":
		return got, typ == "MX" && r.Priority == tr.Priority && matchPattern(tr.PointsTo, strings.TrimSuffix(r.Value, "."), name, bound, got)
	case "TXT":
		return got, typ == "TXT" && !strings.HasPrefix(r.Value, "v=spf1") && matchPattern(tr.Data, r.Value, text, bound, got)
	case "SPFM":
		if typ != "TXT" {
			return nil, false
		}
		f := strings.Fields(r.Value)
		if len(f) < 3 || f[0] != "v=spf1" || !strings.HasSuffix(f[len(f)-1], "all") {
			return nil, false
		}
		// The include may sit among others: SPFM merges into the record.
		for _, m := range f[1 : len(f)-1] {
			g := map[string]string{}
			if matchPattern(tr.SPFRules, m, name, bound, g) {
				for k, v := range g {
					got[k] = v
				}
				return got, true
			}
		}
		return nil, false
	}
	return nil, false
}

// relative gives name relative to fqdn, @ for fqdn itself.
func relative(name, fqdn string) (string, bool) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	fqdn = strings.ToLower(fqdn)
	if name == fqdn {
		return "@", true
	}
	rel, ok := strings.CutSuffix(name, "."+fqdn)
	return rel, ok && rel != ""
}

// What a field holds, which decides what its variables may match.
type field int

const (
	label field = iota // a host: a variable is one DNS label
	name               // a target: a variable is a DNS name, dots allowed
	text               // TXT data: anything
)

// matchPattern matches s against a pattern with %variables%. A variable
// already bound must have the same value, so it is matched as it is.
// Names are compared without case.
func matchPattern(pat, s string, f field, bound, got map[string]string) bool {
	if f != text {
		pat, s = strings.ToLower(pat), strings.ToLower(s)
	}
	var re strings.Builder
	re.WriteString("^")
	var names []string
	last := 0
	for _, m := range varRe.FindAllStringSubmatchIndex(pat, -1) {
		re.WriteString(regexp.QuoteMeta(pat[last:m[0]]))
		n := pat[m[2]:m[3]]
		v, ok := got[n]
		if !ok {
			v, ok = bound[n]
		}
		switch {
		case ok:
			re.WriteString(regexp.QuoteMeta(v))
		case f == label:
			re.WriteString(`([a-z0-9_-]+)`)
			names = append(names, n)
		case f == name:
			re.WriteString(`([a-z0-9_-]+(?:\.[a-z0-9_-]+)*)`)
			names = append(names, n)
		default:
			re.WriteString(`(.+)`)
			names = append(names, n)
		}
		last = m[1]
	}
	re.WriteString(regexp.QuoteMeta(pat[last:]) + "$")
	sub := regexp.MustCompile(re.String()).FindStringSubmatch(s)
	if sub == nil {
		return false
	}
	for i, n := range names {
		if b, ok := got[n]; ok && b != sub[i+1] {
			return false
		}
		got[n] = sub[i+1]
	}
	return true
}
