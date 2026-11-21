package connectors

import "context"

// Reading a person's public LinkedIn profile: connectors that, given the
// profile's address, say where the person works now. A feature uses it to
// check that someone it is about to contact still works at the company it
// found them at. The profile is what the person published about
// themselves: data from outside, never instructions.

// CapReadProfile marks a connector that implements ProfileReader.
const CapReadProfile = "data.read_profile"

// ProfileRole is one job a profile lists as current.
type ProfileRole struct {
	Company string `json:"company"`
	// CompanyURL is the company's own page as the profile links it (often
	// its LinkedIn company page), when the provider gives one.
	CompanyURL string `json:"company_url,omitempty"`
	Title      string `json:"title,omitempty"`
}

// PersonProfile is what a provider read on one profile.
type PersonProfile struct {
	// Found is false when the provider says there is no such profile.
	// That is an answer, not an error.
	Found bool `json:"found"`
	// Headline is the line under the person's name ("CTO at Acme").
	Headline string `json:"headline,omitempty"`
	Location string `json:"location,omitempty"`
	Country  string `json:"country,omitempty"`
	// Current are the jobs the profile lists as current. Empty when it
	// lists none: that says nothing about where the person works.
	Current []ProfileRole `json:"current"`
	// Cost is what the lookup cost, when the provider said: money, or
	// Units for a provider that counts credits or records.
	Cost SEOCost `json:"cost"`
}

// ProfileReader reads one profile at a time, on the workspace's own
// account. profileURL is a person's profile as LinkedInProfile returns it.
type ProfileReader interface {
	Connector
	ReadProfile(ctx context.Context, c *Client, creds Creds, profileURL string) (PersonProfile, error)
}

// Finding a person's profile: connectors that, given a person's name and
// their company's domain, say which LinkedIn profile they have on record.
// The provider's match is a guess at the same person: a caller checks that
// the name and the company it reports are the ones it asked about before
// it uses the profile.

// CapFindProfile marks a connector that implements ProfileLocator.
const CapFindProfile = "data.find_profile"

// FoundProfile is the profile a provider has on record for a name at a
// company, with the name, title and company it has for that profile.
type FoundProfile struct {
	// Found is false when the provider has nobody of that name there, or
	// nobody with a profile address. That is an answer, not an error.
	Found bool `json:"found"`
	// LinkedInURL is the profile as LinkedInProfile returns it.
	LinkedInURL string `json:"linkedin_url,omitempty"`
	// Name is the person's full name as the provider has it.
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
	// Company and CompanyDomain are where the provider has them working.
	Company       string `json:"company,omitempty"`
	CompanyDomain string `json:"company_domain,omitempty"`
	// Cost is what the lookup cost, when the provider said.
	Cost SEOCost `json:"cost"`
}

// ProfileLocator looks one person's profile up by name and company domain
// (q.FirstName, q.LastName and q.Domain), on the workspace's own account.
type ProfileLocator interface {
	Connector
	FindProfile(ctx context.Context, c *Client, creds Creds, q PersonQuery) (FoundProfile, error)
}
