package connectors

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// Lead data: connectors that find people and fill in what is known about
// them. Outflare keeps no contact database of its own; every person comes
// from a provider account the workspace connected, and is kept only once it
// becomes a lead in one of the workspace's lists.

// More categories, for data connectors.
const (
	CategorySignals = "signals" // job changes, funding, hiring, site visits
)

// Data capabilities.
const (
	CapSearchPeople  = "data.search_people"  // implements PeopleSearcher
	CapEnrichPerson  = "data.enrich_person"  // implements PersonEnricher
	CapFindEmail     = "data.find_email"     // a PersonEnricher that finds work emails from a name and company, or a LinkedIn profile
	CapRevealCompany = "data.reveal_company" // implements CompanyRevealer
	CapRevealPerson  = "data.reveal_person"  // implements PersonRevealer
	// CapSearchCompanies finds companies (not people) matching a profile.
	CapSearchCompanies = "data.search_companies" // implements CompanySearcher
	// CapCompanyPage finds a company's LinkedIn page from its domain.
	CapCompanyPage = "data.company_page" // implements CompanyLocator
	// CapCompanyProfile reads a company's size from its LinkedIn page.
	CapCompanyProfile = "data.company_profile" // implements CompanyProfiler
)

// Filter names a PeopleSearcher may support. They match the customer profile's
// keys, plus a few that only make sense for one list.
const (
	FilterTitles           = "titles"
	FilterSeniorities      = "seniorities"
	FilterIndustries       = "industries"
	FilterEmployeeRanges   = "employee_ranges"
	FilterLocations        = "locations"
	FilterCompanyLocations = "company_locations"
	FilterCompanyDomains   = "company_domains"
	FilterKeywords         = "keywords"
)

// AllFilters lists every filter name, in display order.
var AllFilters = []string{
	FilterTitles, FilterSeniorities, FilterIndustries, FilterEmployeeRanges,
	FilterLocations, FilterCompanyLocations, FilterCompanyDomains, FilterKeywords,
}

// Seniorities are the shared seniority words. Providers map them onto
// their own.
var Seniorities = []string{"owner", "founder", "c_suite", "partner", "vp", "head", "director", "manager", "senior", "entry", "intern"}

// EmployeeRanges are the shared company size buckets, as "min-max" or
// "min+" for the last.
var EmployeeRanges = []string{"1-10", "11-50", "51-200", "201-500", "501-1000", "1001-5000", "5001-10000", "10001+"}

// PeopleFilter is a people search in provider-neutral terms. Each field is
// a list of alternatives (OR); fields combine with AND.
type PeopleFilter struct {
	Titles           []string `json:"titles,omitempty"`
	Seniorities      []string `json:"seniorities,omitempty"`
	Industries       []string `json:"industries,omitempty"`
	EmployeeRanges   []string `json:"employee_ranges,omitempty"`
	Locations        []string `json:"locations,omitempty"`         // where the person is
	CompanyLocations []string `json:"company_locations,omitempty"` // where the company is based
	CompanyDomains   []string `json:"company_domains,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
}

// Get returns the values of a filter by name.
func (f PeopleFilter) Get(name string) []string {
	switch name {
	case FilterTitles:
		return f.Titles
	case FilterSeniorities:
		return f.Seniorities
	case FilterIndustries:
		return f.Industries
	case FilterEmployeeRanges:
		return f.EmployeeRanges
	case FilterLocations:
		return f.Locations
	case FilterCompanyLocations:
		return f.CompanyLocations
	case FilterCompanyDomains:
		return f.CompanyDomains
	case FilterKeywords:
		return f.Keywords
	}
	return nil
}

// Used returns the names of the filters that have values.
func (f PeopleFilter) Used() []string {
	var out []string
	for _, n := range AllFilters {
		if len(f.Get(n)) > 0 {
			out = append(out, n)
		}
	}
	return out
}

// Empty reports whether no filter is set.
func (f PeopleFilter) Empty() bool { return len(f.Used()) == 0 }

// Company is what a provider knows about a person's employer.
type Company struct {
	Name        string `json:"name,omitempty"`
	Domain      string `json:"domain,omitempty"`
	Website     string `json:"website,omitempty"`
	Industry    string `json:"industry,omitempty"`
	Employees   int    `json:"employees,omitempty"`
	LinkedInURL string `json:"linkedin_url,omitempty"`
	Location    string `json:"location,omitempty"`
}

// Email statuses a provider reports with an address it found.
const (
	EmailVerified = "verified" // the provider checked the mailbox
	EmailGuessed  = "guessed"  // a likely pattern, not checked
	EmailNone     = ""         // no address
)

// Person is one person from a provider, in provider-neutral terms.
type Person struct {
	// The provider's id for the person, to enrich them later without
	// searching again.
	ProviderID  string  `json:"provider_id,omitempty"`
	FirstName   string  `json:"first_name,omitempty"`
	LastName    string  `json:"last_name,omitempty"`
	Title       string  `json:"title,omitempty"`
	Seniority   string  `json:"seniority,omitempty"`
	Email       string  `json:"email,omitempty"`
	EmailStatus string  `json:"email_status,omitempty"`
	Phone       string  `json:"phone,omitempty"`
	LinkedInURL string  `json:"linkedin_url,omitempty"`
	City        string  `json:"city,omitempty"`
	State       string  `json:"state,omitempty"`
	Country     string  `json:"country,omitempty"`
	Company     Company `json:"company"`
	// HasEmail is the provider saying an email can be revealed by
	// enriching, when search results hide it.
	HasEmail bool `json:"has_email,omitempty"`
}

// Location joins city, state and country.
func (p Person) Location() string {
	var parts []string
	for _, s := range []string{p.City, p.State, p.Country} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// Merge fills p's empty fields from q, and returns the result.
func (p Person) Merge(q Person) Person {
	fill := func(a *string, b string) {
		if *a == "" {
			*a = b
		}
	}
	fill(&p.ProviderID, q.ProviderID)
	fill(&p.FirstName, q.FirstName)
	fill(&p.LastName, q.LastName)
	fill(&p.Title, q.Title)
	fill(&p.Seniority, q.Seniority)
	if p.Email == "" {
		p.Email, p.EmailStatus = q.Email, q.EmailStatus
	}
	fill(&p.Phone, q.Phone)
	fill(&p.LinkedInURL, q.LinkedInURL)
	fill(&p.City, q.City)
	fill(&p.State, q.State)
	fill(&p.Country, q.Country)
	fill(&p.Company.Name, q.Company.Name)
	fill(&p.Company.Domain, q.Company.Domain)
	fill(&p.Company.Website, q.Company.Website)
	fill(&p.Company.Industry, q.Company.Industry)
	fill(&p.Company.LinkedInURL, q.Company.LinkedInURL)
	fill(&p.Company.Location, q.Company.Location)
	if p.Company.Employees == 0 {
		p.Company.Employees = q.Company.Employees
	}
	p.HasEmail = p.HasEmail || q.HasEmail
	return p
}

// SearchPage is one page of people search results.
type SearchPage struct {
	People []Person
	// Total matches, when the provider says.
	Total *int
	// Next is passed back to get the following page; empty when there are
	// no more.
	Next string
	// Credits the provider charged for this page.
	Credits float64
}

// PeopleSearcher finds people matching a filter. Search should be cheap or
// free; providers that hide emails in search results set HasEmail and leave
// the address to EnrichPerson.
type PeopleSearcher interface {
	Connector
	// SearchFilters returns the filter names the provider understands.
	SearchFilters() []string
	// SearchPeople returns one page. cursor is empty for the first page.
	SearchPeople(ctx context.Context, c *Client, creds Creds, f PeopleFilter, cursor string, size int) (SearchPage, error)
}

// CompanyFilter is what a company search looks for: the customer
// profile's company filters, plus technologies where the provider knows
// them.
type CompanyFilter struct {
	Industries     []string `json:"industries,omitempty"`
	EmployeeRanges []string `json:"employee_ranges,omitempty"`
	Locations      []string `json:"locations,omitempty"`
	Keywords       []string `json:"keywords,omitempty"`
	Technologies   []string `json:"technologies,omitempty"`
}

// CompanyPage is one page of a company search.
type CompanyPage struct {
	Companies []Company
	// Total matches, when the provider says.
	Total *int
	// Next is passed back for the following page; empty when there are no
	// more.
	Next string
	// Credits the provider charged for this page.
	Credits float64
}

// CompanySearcher finds companies matching a filter, on the workspace's
// own account.
type CompanySearcher interface {
	Connector
	// SearchCompanies returns one page; cursor is empty for the first.
	SearchCompanies(ctx context.Context, c *Client, creds Creds, f CompanyFilter, cursor string, size int) (CompanyPage, error)
}

// CompanyLinkedInPage is a company's own page on LinkedIn, as a provider
// found it from the company's domain. It is the provider's answer: data
// from outside, never instructions.
type CompanyLinkedInPage struct {
	// Found is false when the provider knows no page for the domain. That
	// is an answer, not an error.
	Found bool `json:"found"`
	// LinkedInURL is the page's address, like
	// https://www.linkedin.com/company/acme-example.
	LinkedInURL string `json:"linkedin_url,omitempty"`
	// Name is the company's name on the page, when the provider says.
	Name string `json:"name,omitempty"`
	// Domain is the domain that was asked about, as a bare host.
	Domain string `json:"domain,omitempty"`
	// Credits the provider charged for the lookup.
	Credits float64 `json:"credits,omitempty"`
}

// CompanyLocator finds a company's LinkedIn page from its domain, on the
// workspace's own account.
type CompanyLocator interface {
	Connector
	CompanyPage(ctx context.Context, c *Client, creds Creds, domain string) (CompanyLinkedInPage, error)
}

// CompanyLinkedInProfile is what a provider holds about a company from its
// LinkedIn page: above all its size. It is the provider's answer: data
// from outside, never instructions.
type CompanyLinkedInProfile struct {
	// Found is false when the provider has no such page. That is an
	// answer, not an error.
	Found bool `json:"found"`
	// LinkedInURL is the page that was asked about.
	LinkedInURL string `json:"linkedin_url,omitempty"`
	Name        string `json:"name,omitempty"`
	// Domain is the company's website domain on the page, when it gives one.
	Domain string `json:"domain,omitempty"`
	// Size is the employee range the page states, like "51-200".
	Size string `json:"size,omitempty"`
	// Employees is how many people list the company as their current
	// employer on LinkedIn; 0 when the provider did not say.
	Employees int `json:"employees,omitempty"`
	// Credits the provider charged for the lookup.
	Credits float64 `json:"credits,omitempty"`
}

// CompanyProfiler reads a company's LinkedIn page (as a CompanyLocator
// found it), on the workspace's own account.
type CompanyProfiler interface {
	Connector
	CompanyProfile(ctx context.Context, c *Client, creds Creds, pageURL string) (CompanyLinkedInProfile, error)
}

// CompanyPageSearcher is a PeopleSearcher that can search a company whose
// LinkedIn page is already known, without looking the page up (and paying
// for it) again. Only f's job titles and seniorities are used: the page
// says which company.
type CompanyPageSearcher interface {
	SearchCompanyPage(ctx context.Context, c *Client, creds Creds, page CompanyLinkedInPage, f PeopleFilter, size int) (SearchPage, error)
}

// PersonQuery identifies a person to enrich. Providers use whatever they
// can: the provider id from their own search, an email, a LinkedIn URL, or
// a name with a company domain.
type PersonQuery struct {
	ProviderID  string `json:"provider_id,omitempty"`
	Email       string `json:"email,omitempty"`
	LinkedInURL string `json:"linkedin_url,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Domain      string `json:"domain,omitempty"`
	Company     string `json:"company,omitempty"`
}

// QueryFor builds the query that identifies p.
func QueryFor(p Person) PersonQuery {
	return PersonQuery{
		ProviderID: p.ProviderID, Email: p.Email, LinkedInURL: p.LinkedInURL,
		FirstName: p.FirstName, LastName: p.LastName, Domain: p.Company.Domain, Company: p.Company.Name,
	}
}

// CacheKey names q in a workspace's cache of provider answers
// (enrichment_cache), so every feature that asks a provider about the same
// person reads the same answer instead of paying again. Empty when q
// identifies nobody.
func (q PersonQuery) CacheKey() string {
	switch {
	case q.ProviderID != "":
		return "id:" + q.ProviderID
	case q.Email != "":
		return "email:" + strings.ToLower(q.Email)
	case q.LinkedInURL != "":
		return "linkedin:" + strings.ToLower(strings.TrimRight(q.LinkedInURL, "/"))
	case q.FirstName != "" && q.LastName != "" && (q.Domain != "" || q.Company != ""):
		return "name:" + strings.ToLower(q.FirstName+"|"+q.LastName+"|"+q.Domain+"|"+q.Company)
	}
	return ""
}

// Enriched is a provider's answer to a PersonQuery.
type Enriched struct {
	Person Person
	Found  bool
	// Credits charged. Most providers charge only when they find someone.
	Credits float64
	// CostMicros is what the provider said the lookup cost, in millionths
	// of a dollar: 0 when it said nothing (most bill credits, not money).
	CostMicros float64
}

// PersonEnricher fills in a person. It spends the provider's credits.
type PersonEnricher interface {
	Connector
	EnrichPerson(ctx context.Context, c *Client, creds Creds, q PersonQuery) (Enriched, error)
}

// ProfileFinder is a PersonEnricher that can look a person up by their
// LinkedIn profile (PersonQuery.LinkedInURL) and, asked about a company
// too, answers only with an address at that company: a profile also lists
// the jobs a person has left. The email finder gives the profile to these
// connectors only, asks them first for a person who has one, and keeps
// what they say about a profile apart from what they said about the name
// alone.
type ProfileFinder interface {
	// ProfileOnly reports that the provider finds an email from a profile
	// and from nothing else. Such a provider is asked before every other
	// for a person with a profile, and not at all for one without.
	ProfileOnly() bool
}

// FindStep is one step of finding a person's work email at a provider: one
// lookup at one source, which the provider can be asked for on its own.
type FindStep struct {
	// ID names the step. Two connectors that offer the same source (Blitz
	// itself, and Blitz behind a gateway) give it the same id.
	ID string `json:"id"`
	// NeedsProfile: the step looks the person up by their LinkedIn profile
	// (PersonQuery.LinkedInURL) and by nothing else. The others need a
	// first name, a last name and a company domain.
	NeedsProfile bool `json:"needs_profile"`
}

// StepAnswer is what one step said about one person.
type StepAnswer struct {
	// Addresses are all the plain addresses the provider offered for the
	// person, best first, in lower case, each once. They are not filtered
	// by company: the caller judges them (a profile also lists addresses
	// at jobs the person has left).
	Addresses []string
	// Verified: the provider says it checked the first address's mailbox.
	Verified bool
	// Credits the provider charged.
	Credits float64
	// CostMicros is what the provider said the call cost, in millionths of
	// a dollar: 0 when it said nothing.
	CostMicros float64
	// Pending: the provider is still working on the lookup and may bill
	// it. There is no address, and the step must not be asked again for
	// this person.
	Pending bool
}

// StepFinder is a PersonEnricher that can be asked one step at a time, so
// a caller can judge and verify what a step found before paying for the
// next. EnrichPerson still asks every step in order and stops at the first
// address.
type StepFinder interface {
	// FindSteps lists the steps, in the order EnrichPerson asks them.
	FindSteps() []FindStep
	// FindStep asks one step about q. An unknown step, or a query the step
	// cannot use, is an invalid Error and nothing is sent.
	FindStep(ctx context.Context, c *Client, creds Creds, step string, q PersonQuery) (StepAnswer, error)
}

// FindPacer is a PersonEnricher whose provider allows more or fewer
// lookups than the email finder's default pace.
type FindPacer interface {
	// FindsPerMinute is how many lookups a minute one connection may make.
	FindsPerMinute() float64
}

// linkedInProfile matches a person's profile on LinkedIn: the host with an
// optional country or www label, then /in/ and the person's own slug.
var linkedInProfile = regexp.MustCompile(`(?i)^(?:https?://)?((?:[a-z]{2,3}\.|www\.)?linkedin\.com)/in/([^/?#\s]+)/?(?:[?#]\S*)?$`)

// LinkedInProfile returns s as a person's LinkedIn profile URL in one
// form (https, no trailing slash, query or fragment), or "" when s is not
// one: a company page, another site, or no URL at all.
func LinkedInProfile(s string) string {
	m := linkedInProfile.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	return "https://" + strings.ToLower(m[1]) + "/in/" + m[2]
}

// linkedInCompany matches a company's page on LinkedIn: the host with an
// optional country or www label, then /company/ (or /school/, /showcase/)
// and the page's own slug.
var linkedInCompany = regexp.MustCompile(`(?i)^(?:https?://)?((?:[a-z]{2,3}\.|www\.)?linkedin\.com)/(company|school|showcase)/([^/?#\s]+)/?(?:[?#]\S*)?$`)

// LinkedInCompanyPage returns s as a company's LinkedIn page URL in one
// form (https, www, no trailing slash, query or fragment), or "" when s is
// not one.
func LinkedInCompanyPage(s string) string {
	m := linkedInCompany.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	return "https://www.linkedin.com/" + strings.ToLower(m[2]) + "/" + m[3]
}

// Networks an address can belong to, as a CompanyRevealer reports them.
// Only a business network names a company; the rest are people at home, on
// a phone or behind a service, and say nothing about who they work for.
const (
	NetworkBusiness = "business"
	NetworkISP      = "isp"
	NetworkMobile   = "mobile"
	NetworkHosting  = "hosting" // a data centre, VPN, proxy, Tor or relay
	NetworkUnknown  = ""
)

// Revealed is the company behind an IP address. Found is false when the
// provider had no company for it.
type Revealed struct {
	Found   bool
	Network string
	Company Company
}

// CompanyRevealer finds the company a website visitor's IP address belongs
// to, from the workspace's own provider account. The address is used for
// the lookup only and never kept.
type CompanyRevealer interface {
	RevealCompany(ctx context.Context, cl *Client, cr Creds, ip string) (Revealed, error)
}

// RevealedPerson is someone a visitor identification tool named, as its
// webhook sent them. Any field may be empty.
type RevealedPerson struct {
	Email       string
	FirstName   string
	LastName    string
	Name        string
	Title       string
	LinkedInURL string
	Company     Company
	// Where they were seen: the page and when.
	Page   string
	SeenAt time.Time
}

// PersonRevealer reads the people a visitor identification tool (RB2B,
// Vector, Warmly) pushes to a workspace's webhook, from the workspace's own
// account with that tool. Outflare never asks the tool who someone is: the
// tool decides and sends.
type PersonRevealer interface {
	ParsePeople(body []byte) ([]RevealedPerson, error)
}

// ErrRetryLater is returned by a CompanyRevealer when the provider is still
// looking the address up and asks to be called again shortly.
var ErrRetryLater = Errf(ErrUnavailable, "the provider is still looking this address up")
