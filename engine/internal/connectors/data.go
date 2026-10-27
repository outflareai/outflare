package connectors

import (
	"context"
	"strings"
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
	CapSearchPeople = "data.search_people" // implements PeopleSearcher
	CapEnrichPerson = "data.enrich_person" // implements PersonEnricher
	CapFindEmail    = "data.find_email"    // a PersonEnricher that finds work emails from a name and company
)

// Filter names a PeopleSearcher may support. They match the project's ICP
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

// Enriched is a provider's answer to a PersonQuery.
type Enriched struct {
	Person Person
	Found  bool
	// Credits charged. Most providers charge only when they find someone.
	Credits float64
}

// PersonEnricher fills in a person. It spends the provider's credits.
type PersonEnricher interface {
	Connector
	EnrichPerson(ctx context.Context, c *Client, creds Creds, q PersonQuery) (Enriched, error)
}
