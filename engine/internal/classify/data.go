package classify

import (
	"bufio"
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"
)

// DataVersion names the snapshot of data/ built into this binary. Bump it
// whenever a file in data/ changes.
const DataVersion = "2026-10-26.1"

// Sources, as they appear in evidence.
const (
	SourceOutflare   = "outflare"                 // data/providers.yaml, reviewed by Outflare
	SourceDisposable = "disposable-email-domains" // CC0
	SourceFakeFilter = "fakefilter"               // BSD 3-Clause
	SourceDNS        = "dns"
	SourceSuffix     = "public_suffix"
)

var (
	//go:embed data/providers.yaml
	providersYAML []byte
	//go:embed data/disposable_email_blocklist.conf
	disposableList string
	//go:embed data/fakefilter.txt
	fakeFilterList string
)

// Provider is a mailbox or alias service Outflare has reviewed.
type Provider struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Category    string   `yaml:"category"`
	Subcategory string   `yaml:"subcategory"`
	Domains     []string `yaml:"domains"`
	// Subdomains: addresses live on subdomains too (alias@you.example.com).
	Subdomains bool `yaml:"subdomains"`
}

// listing is one open list naming a domain.
type listing struct {
	Source string
	// Group is the provider the list files the domain under, when it says
	// (fakefilter groups domains by the service that issues them).
	Group string
}

// Intel is the global intelligence: reviewed providers and open lists,
// keyed by canonical domain.
type Intel struct {
	Version   string
	providers map[string]*Provider
	listed    map[string][]listing
	// groupSize counts the domains in each fakefilter group: a service
	// issuing many domains is rotating them.
	groupSize map[string]int
}

// Builtin returns the intelligence embedded in this binary.
var Builtin = sync.OnceValue(func() *Intel {
	in, err := load(providersYAML, disposableList, fakeFilterList)
	if err != nil {
		panic("classify: embedded data: " + err.Error())
	}
	return in
})

func load(providers []byte, disposable, fakefilter string) (*Intel, error) {
	in := &Intel{Version: DataVersion, providers: map[string]*Provider{}, listed: map[string][]listing{}, groupSize: map[string]int{}}
	var ps []*Provider
	if err := yaml.Unmarshal(providers, &ps); err != nil {
		return nil, fmt.Errorf("providers.yaml: %w", err)
	}
	for _, p := range ps {
		if p.ID == "" || p.Name == "" || len(p.Domains) == 0 {
			return nil, fmt.Errorf("providers.yaml: provider %q needs an id, a name and domains", p.ID)
		}
		switch p.Category {
		case FreePersonal, PrivateMailbox, ManagedOther:
		case RelayAlias:
			if p.Subcategory != PermanentAlias && p.Subcategory != AnonymousAlias {
				return nil, fmt.Errorf("providers.yaml: %s: a relay needs subcategory permanent_alias or anonymous_alias", p.ID)
			}
		default:
			return nil, fmt.Errorf("providers.yaml: %s: category %q is not one a provider can have", p.ID, p.Category)
		}
		for _, d := range p.Domains {
			dom, err := ParseDomain(d)
			if err != nil || dom.Name != d {
				return nil, fmt.Errorf("providers.yaml: %s: %q is not a canonical domain", p.ID, d)
			}
			if other, ok := in.providers[d]; ok {
				return nil, fmt.Errorf("providers.yaml: %s is listed under both %s and %s", d, other.ID, p.ID)
			}
			in.providers[d] = p
		}
	}
	in.addList(SourceDisposable, disposable, false)
	in.addList(SourceFakeFilter, fakefilter, true)
	return in, nil
}

// addList reads one domain a line; # starts a comment. With groups, a
// "## name" line starts the group the following domains belong to.
func (in *Intel) addList(source, text string, groups bool) {
	group := ""
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if groups && strings.HasPrefix(line, "## ") {
			group = strings.ToLower(strings.TrimSpace(line[3:]))
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		d, err := ParseDomain(line)
		if err != nil || seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		in.listed[d.Name] = append(in.listed[d.Name], listing{Source: source, Group: group})
		if group != "" {
			in.groupSize[group]++
		}
	}
}

// Count reports how many domains a source lists.
func (in *Intel) Count(source string) int {
	if source == SourceOutflare {
		return len(in.providers)
	}
	n := 0
	for _, ls := range in.listed {
		for _, l := range ls {
			if l.Source == source {
				n++
			}
		}
	}
	return n
}

// match is what the intelligence knows about a domain.
type match struct {
	provider       *Provider
	providerDomain string // the provider domain matched, maybe a parent
	listed         []listing
	listedDomain   string
}

// lookup finds a domain, or the nearest parent, in the providers and the
// lists. Parents are tried only up to the registrable domain, and for
// providers only when the provider issues subdomains.
func (in *Intel) lookup(d Domain) match {
	var m match
	for name := d.Name; ; {
		if m.provider == nil {
			if p := in.providers[name]; p != nil && (name == d.Name || p.Subdomains) {
				m.provider, m.providerDomain = p, name
			}
		}
		if m.listed == nil {
			if ls := in.listed[name]; ls != nil {
				m.listed, m.listedDomain = ls, name
			}
		}
		if name == d.Registrable {
			break
		}
		_, rest, ok := strings.Cut(name, ".")
		if !ok {
			break
		}
		name = rest
	}
	return m
}

// MailboxProvider reports whether domain belongs to a mailbox or alias
// service (Gmail, iCloud, a relay, a disposable domain) rather than to the
// company its people work at, so two people on it never share a company.
func (in *Intel) MailboxProvider(domain string) bool {
	d, err := ParseDomain(domain)
	if err != nil {
		return false
	}
	m := in.lookup(d)
	return m.provider != nil || m.listed != nil
}
