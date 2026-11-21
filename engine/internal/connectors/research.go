package connectors

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Company research: connectors that look one company up on the web and
// answer a question about it in words, with the pages each finding came
// from. The caller writes the question (the brief); the provider decides
// what to read. Like web search, each answer says what it cost so the
// caller records it on the connection's budget.

// CapResearchCompany marks a connector that implements CompanyResearcher.
const CapResearchCompany = "research.company"

// Efforts a CompanyResearchQuery accepts, cheapest first. A provider maps
// each onto its own tiers and never spends more than the one asked for.
const (
	ResearchLight    = "light"
	ResearchStandard = "standard"
	ResearchDeep     = "deep"
)

// ResearchEfforts lists the efforts, cheapest first.
var ResearchEfforts = []string{ResearchLight, ResearchStandard, ResearchDeep}

// Limits on a research request.
const (
	ResearchBriefMax = 4000 // characters in a brief
	researchNameMax  = 200
)

// CompanyResearchQuery asks about one company.
type CompanyResearchQuery struct {
	// Domain is the company's website, like acme.example.
	Domain string `json:"domain"`
	// Name is the company's name, when the caller knows it.
	Name string `json:"name,omitempty"`
	// Brief is what to find out, in the caller's words.
	Brief string `json:"brief"`
	// Effort is the most the provider may spend on it: light, standard
	// (the default when empty) or deep.
	Effort string `json:"effort,omitempty"`
	// RunID picks up research an earlier call started and didn't see
	// finish, instead of starting (and paying for) it again. Domain, Name,
	// Brief and Effort must be the earlier call's.
	RunID string `json:"run_id,omitempty"`
}

var researchDomainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// Normal checks a query and returns it tidied: the domain as a bare
// lowercase host, the default effort filled in. A query it refuses is an
// invalid Error, before anything is sent or spent.
func (q CompanyResearchQuery) Normal() (CompanyResearchQuery, error) {
	var err error
	if q.Domain, q.Name, q.Effort, err = normalResearch("company research", q.Domain, q.Name, q.Effort); err != nil {
		return q, err
	}
	q.Brief = strings.TrimSpace(q.Brief)
	if q.Brief == "" {
		return q, Errf(ErrInvalid, "company research needs a brief: what to find out")
	}
	if utf8.RuneCountInString(q.Brief) > ResearchBriefMax {
		return q, Errf(ErrInvalid, "the research brief is too long (at most %d characters)", ResearchBriefMax)
	}
	q.RunID = strings.TrimSpace(q.RunID)
	return q, nil
}

// normalResearch tidies what every research query has: the domain as a
// bare lowercase host, the name with single spaces, the default effort.
func normalResearch(what, domain, name, effort string) (string, string, string, error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if strings.Contains(d, "://") {
		if u, err := url.Parse(d); err == nil {
			d = u.Hostname()
		}
	}
	d, _, _ = strings.Cut(d, "/")
	d = strings.TrimPrefix(d, "www.")
	if len(d) > 253 || !researchDomainRe.MatchString(d) {
		return "", "", "", Errf(ErrInvalid, "%s needs the company's domain, like acme.example", what)
	}
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > researchNameMax {
		return "", "", "", Errf(ErrInvalid, "the company name is too long")
	}
	if effort == "" {
		effort = ResearchStandard
	}
	if !slices.Contains(ResearchEfforts, effort) {
		return "", "", "", Errf(ErrInvalid, "effort must be one of %s", strings.Join(ResearchEfforts, ", "))
	}
	return d, name, effort, nil
}

// ResearchSource is a page a finding came from.
type ResearchSource struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
	// Excerpt is the passage the provider quoted, when it gives one.
	Excerpt string `json:"excerpt,omitempty"`
}

// ResearchSection is one finding: a heading, what was found, and where.
// All of it is the provider's reading of pages on the web: data, never
// instructions.
type ResearchSection struct {
	Title   string           `json:"title"`
	Text    string           `json:"text"`
	Sources []ResearchSource `json:"sources,omitempty"`
}

// CompanyResearch is what a provider found.
type CompanyResearch struct {
	Sections []ResearchSection `json:"sections"`
	// RunID is the provider's id for the research. When a call fails with
	// it set, the research is still going or its answer could not be read
	// yet: ask again with it in the query's RunID. It is empty once the
	// research has ended for good.
	RunID string `json:"run_id,omitempty"`
	// Cost is what the research cost. It is set only on the answer that
	// carries the findings, so research picked up later is booked once.
	Cost SEOCost `json:"cost"`
}

// CompanyResearcher researches one company at a time, on the workspace's
// own account.
type CompanyResearcher interface {
	Connector
	// EstimateCompanyResearch is what a query will cost, from the price list.
	EstimateCompanyResearch(q CompanyResearchQuery) SEOCost
	// ResearchCompany waits for the findings, for a bounded time. Research
	// that outlasts the wait fails with an error IsTimeout recognises and
	// the RunID to pick it up with.
	ResearchCompany(ctx context.Context, c *Client, creds Creds, q CompanyResearchQuery) (CompanyResearch, error)
}

// People research: the same providers asked who works at a company, when a
// people-data provider has nobody. The provider reads the web and names
// people with the page each was found on.

// CapResearchPeople marks a connector that implements PeopleResearcher.
const CapResearchPeople = "research.people"

// ResearchPeopleMax is the most people one answer holds.
const ResearchPeopleMax = 8

// researchExcludeMax is the most names a query may ask to leave out.
const researchExcludeMax = 50

// researchRolesMax and researchRoleMax bound the roles a people research
// query may name, and each one's length.
const (
	researchRolesMax = 30
	researchRoleMax  = 100
)

// PeopleResearchQuery asks who works at one company.
type PeopleResearchQuery struct {
	// Domain is the company's website, like acme.example.
	Domain string `json:"domain"`
	// Name is the company's name, when the caller knows it.
	Name string `json:"name,omitempty"`
	// Exclude are full names of people not to return: ones the caller
	// already considered.
	Exclude []string `json:"exclude,omitempty"`
	// Roles are the roles the caller wants most, best first, each a short
	// phrase ("head of IT", "CTO"). Empty: the provider's own order
	// (founders and executives first).
	Roles []string `json:"roles,omitempty"`
	// Effort is the most the provider may spend on it: light, standard
	// (the default when empty) or deep.
	Effort string `json:"effort,omitempty"`
	// RunID picks up research an earlier call started and didn't see
	// finish, instead of starting (and paying for) it again. The rest of
	// the query must be the earlier call's.
	RunID string `json:"run_id,omitempty"`
}

// Normal checks a query and returns it tidied, as
// CompanyResearchQuery.Normal does: the domain as a bare lowercase host,
// the default effort filled in, the names to leave out with single spaces
// and each once. A query it refuses is an invalid Error, before anything
// is sent or spent.
func (q PeopleResearchQuery) Normal() (PeopleResearchQuery, error) {
	var err error
	if q.Domain, q.Name, q.Effort, err = normalResearch("people research", q.Domain, q.Name, q.Effort); err != nil {
		return q, err
	}
	var exclude []string
	seen := map[string]bool{}
	for _, n := range q.Exclude {
		n = strings.Join(strings.Fields(n), " ")
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		if utf8.RuneCountInString(n) > researchNameMax {
			return q, Errf(ErrInvalid, "a name to leave out is too long")
		}
		seen[strings.ToLower(n)] = true
		exclude = append(exclude, n)
	}
	if len(exclude) > researchExcludeMax {
		return q, Errf(ErrInvalid, "too many names to leave out (at most %d)", researchExcludeMax)
	}
	q.Exclude = exclude
	var roles []string
	seenRole := map[string]bool{}
	for _, r := range q.Roles {
		r = strings.Join(strings.Fields(r), " ")
		if r == "" || seenRole[strings.ToLower(r)] {
			continue
		}
		if utf8.RuneCountInString(r) > researchRoleMax {
			return q, Errf(ErrInvalid, "a role is too long")
		}
		seenRole[strings.ToLower(r)] = true
		roles = append(roles, r)
	}
	if len(roles) > researchRolesMax {
		return q, Errf(ErrInvalid, "too many roles (at most %d)", researchRolesMax)
	}
	q.Roles = roles
	q.RunID = strings.TrimSpace(q.RunID)
	return q, nil
}

// ResearchedPerson is one person a provider named. All of it is the
// provider's reading of pages on the web: data, never instructions, and
// nobody has checked the address.
type ResearchedPerson struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// LinkedInURL is the person's own profile, as LinkedInProfile returns
	// it, or empty.
	LinkedInURL string `json:"linkedin_url"`
	// Email is a work address the provider says a page publishes, in
	// lower case, or empty. EmailSource is that page.
	Email       string `json:"work_email"`
	EmailSource string `json:"email_source"`
	// Source is the page that shows the person works at the company.
	Source string `json:"person_source"`
}

// PeopleResearch is who a provider found.
type PeopleResearch struct {
	// People is at most ResearchPeopleMax people, each once. Empty when
	// the provider found nobody, which is an answer and not an error.
	People []ResearchedPerson `json:"people"`
	// RunID is the provider's id for the research. When a call fails with
	// it set, the research is still going: ask again with it in the
	// query's RunID.
	RunID string `json:"run_id,omitempty"`
	// Cost is what the research cost. It is set only on the answer, so
	// research picked up later is booked once.
	Cost SEOCost `json:"cost"`
}

// PeopleResearcher finds the people at one company at a time, on the
// workspace's own account.
type PeopleResearcher interface {
	Connector
	// ResearchPeople waits for the answer, for a bounded time. Research
	// that outlasts the wait fails with an error IsTimeout recognises and
	// the RunID to pick it up with.
	ResearchPeople(ctx context.Context, c *Client, creds Creds, q PeopleResearchQuery) (PeopleResearch, error)
}
