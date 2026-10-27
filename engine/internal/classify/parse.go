package classify

import (
	"errors"
	"net/mail"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Errors from parsing. Their text starts with the field name, so the API
// can pass it on as a field error.
var (
	ErrInvalidEmail  = errors.New("email must be a valid email address, such as name@example.com")
	ErrInvalidDomain = errors.New("domain must be a valid domain name, such as example.com")
	ErrPublicSuffix  = errors.New("domain must be a domain someone can register, not a public suffix such as co.uk")
)

// lookup canonicalises domains the same way everywhere: lookups, list
// entries and caches all use its output as the key.
var lookup = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.VerifyDNSLength(true), idna.StrictDomainName(true))

// Address is a parsed email address. Local is kept as given: the
// classifier never rewrites dots or +tags into a different identity.
type Address struct {
	Local  string
	Domain Domain
}

// Domain is a canonical domain: lowercase ASCII (punycode for
// internationalised names), no trailing dot.
type Domain struct {
	Name        string // mail.example.co.uk
	Registrable string // example.co.uk
	Suffix      string // co.uk
	// ICANN is true when Suffix is an ICANN suffix rather than a private
	// one (github.io) or the catch-all for unknown TLDs.
	ICANN bool
}

// IDN reports whether any label is internationalised.
func (d Domain) IDN() bool {
	for _, l := range strings.Split(d.Name, ".") {
		if strings.HasPrefix(l, "xn--") {
			return true
		}
	}
	return false
}

// ParseDomain canonicalises a domain name.
func ParseDomain(s string) (Domain, error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	if s == "" || strings.ContainsAny(s, "@/:[] ") {
		return Domain{}, ErrInvalidDomain
	}
	name, err := lookup.ToASCII(s)
	if err != nil || !strings.Contains(name, ".") || len(name) > 253 {
		return Domain{}, ErrInvalidDomain
	}
	name = strings.ToLower(name)
	reg, err := publicsuffix.EffectiveTLDPlusOne(name)
	if err != nil {
		return Domain{}, ErrPublicSuffix
	}
	suffix, icann := publicsuffix.PublicSuffix(name)
	return Domain{Name: name, Registrable: reg, Suffix: suffix, ICANN: icann}, nil
}

// ParseEmail splits and checks an address. The domain is canonicalised;
// the local part is checked but kept as given.
func ParseEmail(s string) (Address, error) {
	s = strings.TrimSpace(s)
	at := strings.LastIndexByte(s, '@')
	if at < 1 || at == len(s)-1 || len(s) > 320 {
		return Address{}, ErrInvalidEmail
	}
	local := s[:at]
	if len(local) > 64 {
		return Address{}, ErrInvalidEmail
	}
	d, err := ParseDomain(s[at+1:])
	if err != nil {
		return Address{}, ErrInvalidEmail
	}
	// net/mail knows RFC 5322's local-part rules, including quoted ones.
	a, err := mail.ParseAddress(local + "@" + d.Name)
	if err != nil || a.Name != "" {
		return Address{}, ErrInvalidEmail
	}
	return Address{Local: local, Domain: d}, nil
}

// RoleLocal reports whether a local part is a role or shared inbox, such
// as info@ or support@, ignoring a +tag.
func RoleLocal(local string) bool {
	local = strings.ToLower(strings.SplitN(local, "+", 2)[0])
	return roleLocals[local]
}

var roleLocals = set("admin", "administrator", "abuse", "accounts", "accounting", "billing", "careers", "contact",
	"customerservice", "enquiries", "enquiry", "finance", "hello", "help", "hostmaster", "hr", "info", "inquiries",
	"jobs", "legal", "mail", "marketing", "media", "no-reply", "noreply", "office", "orders", "postmaster", "press",
	"privacy", "recruiting", "reception", "sales", "security", "service", "support", "team", "webmaster")

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
