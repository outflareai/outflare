// Package infra plans and runs sending infrastructure through its whole
// life: a plan for domains and mailboxes, approved once, then bought
// through a supplier (or by the person), DNS checked, connected, warmed
// up, and moved between ready, resting and retired.
//
// Drafting a plan and deciding a mailbox's next stage are pure functions
// here, so the rules can be tested and explained without a database.
package infra

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Limits on one plan.
const (
	MaxDomains            = 200
	MaxMailboxesPerDomain = 10
	MinWarmupDays         = 7
	MaxWarmupDays         = 60
	DefaultWarmupDays     = 14

	// setupDays is how long buying and DNS usually take before warm-up
	// can start.
	setupDays = 2
	// manyPerDomain is where mailboxes per domain start to concentrate risk:
	// one bad domain then takes many mailboxes down with it.
	manyPerDomain = 3
)

// Persona is a person the mailboxes send as.
type Persona struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// Goal is what a person or agent asks for.
type Goal struct {
	DomainCount        int
	MailboxesPerDomain int
	Brand              string
	Personas           []Persona
	// Domains the person chose; the rest are suggested from Brand.
	Domains []string
	// Domains to leave out of suggestions: ones the workspace already has,
	// and ones a supplier said are taken. Domains the person chose are kept,
	// so a plan can add mailboxes to domains the workspace already owns.
	Exclude    []string
	ReadyBy    *time.Time
	WarmupDays int
	DailyLimit int
	Now        time.Time
}

// DraftMailbox is one mailbox to create.
type DraftMailbox struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// DraftDomain is one domain to buy, with its mailboxes.
type DraftDomain struct {
	Name      string         `json:"name"`
	Mailboxes []DraftMailbox `json:"mailboxes"`
}

// Timeline is when each step should be done, by UTC date.
type Timeline struct {
	// Buying and DNS, done by this date.
	SetupBy time.Time `json:"setup_by"`
	// The earliest every mailbox can be ready, if warm-up goes well.
	ReadyOn time.Time `json:"ready_on"`
}

// Warning is something the person should know before approving.
// Blocking ones must be fixed first.
type Warning struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

// Draft is a plan ready for a person to review.
type Draft struct {
	Domains  []DraftDomain `json:"domains"`
	Timeline Timeline      `json:"timeline"`
	// Cold emails a day across all of the plan's mailboxes once ready.
	DailyCapacity int       `json:"daily_capacity"`
	Warnings      []Warning `json:"warnings"`
}

// Plan drafts the domains, mailboxes and timeline for a goal.
func Plan(g Goal) Draft {
	if g.WarmupDays == 0 {
		g.WarmupDays = DefaultWarmupDays
	}
	d := Draft{Warnings: []Warning{}, Domains: []DraftDomain{}}
	names := pickDomains(g)
	for _, name := range names {
		d.Domains = append(d.Domains, DraftDomain{Name: name, Mailboxes: mailboxesFor(name, g.Personas, g.MailboxesPerDomain)})
	}
	d.DailyCapacity = len(names) * g.MailboxesPerDomain * g.DailyLimit

	today := g.Now.UTC().Truncate(24 * time.Hour)
	d.Timeline.SetupBy = today.AddDate(0, 0, setupDays)
	d.Timeline.ReadyOn = d.Timeline.SetupBy.AddDate(0, 0, g.WarmupDays)

	warn := func(code string, blocking bool, format string, args ...any) {
		d.Warnings = append(d.Warnings, Warning{Code: code, Message: fmt.Sprintf(format, args...), Blocking: blocking})
	}
	if len(g.Personas) == 0 {
		warn("no_personas", true, "Add at least one person the mailboxes send as. Each mailbox is named after one, e.g. jane@ and jane.doe@.")
	}
	switch {
	case len(names) >= g.DomainCount:
	case strings.TrimSpace(g.Brand) == "":
		warn("not_enough_domains", true, "Add a brand to get domain name suggestions, or list the %d domains you want.", g.DomainCount)
	default:
		warn("not_enough_domains", true, "Only %d domain names could be suggested from %q. Choose the other %d yourself.",
			len(names), g.Brand, g.DomainCount-len(names))
	}
	if g.ReadyBy != nil {
		readyBy := g.ReadyBy.UTC().Truncate(24 * time.Hour)
		if readyBy.Before(d.Timeline.ReadyOn) {
			fit := int(readyBy.Sub(d.Timeline.SetupBy) / (24 * time.Hour))
			msg := fmt.Sprintf("Ready on %s at the earliest, after %s: buying and DNS take about %d days and warm-up %d.",
				d.Timeline.ReadyOn.Format("Mon 2 Jan"), readyBy.Format("Mon 2 Jan"), setupDays, g.WarmupDays)
			if fit >= MinWarmupDays {
				msg += fmt.Sprintf(" Shorten warm-up to %d days to make the date, or move the date.", fit)
			} else {
				msg += fmt.Sprintf(" Move the date to %s or later; %d days is the shortest safe warm-up.",
					d.Timeline.SetupBy.AddDate(0, 0, MinWarmupDays).Format("Mon 2 Jan"), MinWarmupDays)
			}
			warn("ready_by_too_soon", false, "%s", msg)
		}
	}
	if g.WarmupDays < DefaultWarmupDays {
		warn("short_warmup", false, "%d days of warm-up is short. New domains usually need %d to %d before cold email lands in the inbox.",
			g.WarmupDays, DefaultWarmupDays, 21)
	}
	if g.MailboxesPerDomain > manyPerDomain {
		warn("many_per_domain", false, "%d mailboxes on each domain puts a lot on one name: if a domain is blocklisted, all of them stop. Most teams use 2 or 3.",
			g.MailboxesPerDomain)
	}
	if len(g.Personas) > 0 && g.MailboxesPerDomain > len(g.Personas)*len(localPatterns) {
		warn("too_few_personas", true, "%d mailboxes per domain needs more people: each person gives at most %d addresses on a domain.",
			g.MailboxesPerDomain, len(localPatterns))
	}
	return d
}

// pickDomains returns the person's domains, then suggestions, up to the
// count asked for.
func pickDomains(g Goal) []string {
	skip := map[string]bool{}
	for _, e := range g.Exclude {
		skip[strings.ToLower(e)] = true
	}
	var out []string
	add := func(name string, suggested bool) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || suggested && skip[name] || slices.Contains(out, name) || len(out) >= g.DomainCount {
			return
		}
		out = append(out, name)
	}
	for _, d := range g.Domains {
		add(d, false)
	}
	for _, d := range Suggest(g.Brand) {
		add(d, true)
	}
	return out
}

// Prefixes and suffixes that read as the brand's own domain. Cold email
// never goes from the main domain, so its reputation is never at risk.
var (
	domainPrefixes = []string{"get", "try", "with", "meet", "hello", "join", "go", "use", "team", "hi"}
	domainSuffixes = []string{"hq", "team", "mail", "app", "group", "co", "labs", "io", "now", "online"}
	domainTLDs     = []string{"com", "co", "net", "io"}
)

// UseTestTLDs makes Suggest end every name in tld (such as "example"), so
// tests never name a domain someone could register. It returns the undo.
// Tests only.
func UseTestTLDs(tld string) (undo func()) {
	old := domainTLDs
	domainTLDs = []string{tld}
	return func() { domainTLDs = old }
}

// Suggest returns domain names for a brand, best first: .com before other
// endings, prefixes before suffixes.
func Suggest(brand string) []string {
	b := slug(brand)
	if b == "" {
		return nil
	}
	var out []string
	for _, tld := range domainTLDs {
		for i := range max(len(domainPrefixes), len(domainSuffixes)) {
			if i < len(domainPrefixes) {
				out = append(out, domainPrefixes[i]+b+"."+tld)
			}
			if i < len(domainSuffixes) {
				out = append(out, b+domainSuffixes[i]+"."+tld)
			}
		}
	}
	return out
}

// slug keeps the letters and digits of a brand, lowercased.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// localPatterns turn a person into mailbox names, most natural first.
var localPatterns = []func(first, last string) string{
	func(f, l string) string { return f },
	func(f, l string) string { return join(f, ".", l) },
	func(f, l string) string { return join(initial(f), ".", l) },
	func(f, l string) string { return join(f, "", l) },
	func(f, l string) string { return join(f, "", initial(l)) },
}

func initial(s string) string {
	if s == "" {
		return ""
	}
	return s[:1]
}

// join returns a+sep+b, or "" when b is empty, so a person with no last
// name only gets the patterns that need none.
func join(a, sep, b string) string {
	if a == "" || b == "" {
		return ""
	}
	return a + sep + b
}

// mailboxesFor names n mailboxes on a domain, taking the personas in turn.
func mailboxesFor(domain string, personas []Persona, n int) []DraftMailbox {
	out := []DraftMailbox{}
	if len(personas) == 0 {
		return out
	}
	used := map[string]bool{}
	next := make([]int, len(personas)) // the next pattern for each persona
	for stuck := 0; len(out) < n && stuck < len(personas); {
		for i, p := range personas {
			if len(out) >= n {
				break
			}
			f, l := slug(p.FirstName), slug(p.LastName)
			local := ""
			for next[i] < len(localPatterns) && (local == "" || used[local]) {
				local = localPatterns[next[i]](f, l)
				next[i]++
			}
			if local == "" || used[local] {
				stuck++
				continue
			}
			used[local] = true
			out = append(out, DraftMailbox{Email: local + "@" + domain, FirstName: p.FirstName, LastName: p.LastName})
		}
	}
	return out
}
