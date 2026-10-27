package data

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Apollo: https://docs.apollo.io/reference/people-api-search and
// https://docs.apollo.io/reference/bulk-people-enrichment
//
// People search is free and hides emails; enrichment by Apollo id reveals
// the work email and spends one credit per match. The same API key also
// lists the mailboxes connected in Apollo (apollo_mailboxes.go).
type Apollo struct{}

func (Apollo) Definition() c.Definition {
	return c.Definition{
		ID: "apollo", Name: "Apollo", Category: c.CategoryData,
		Summary:     "Search Apollo's people database and reveal work emails with your Apollo credits.",
		Description: "Build lists from Apollo searches by title, seniority, company size, industry and location. Searching is free; revealing an email uses one Apollo credit per person found. Mailboxes also connected in Apollo are flagged on the Mailboxes page, since Apollo may send from them too.",
		Website:     "https://www.apollo.io", DocsURL: "https://docs.apollo.io/reference/people-api-search",
		KeyURL: "https://app.apollo.io/#/settings/integrations/api",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		// Apollo grants the scopes picked when the app was registered
		// (app_scopes); pick people search, person enrichment and email
		// accounts there.
		OAuth: &c.OAuth{
			AuthURL: "https://app.apollo.io/#/oauth/authorize", TokenURL: "https://app.apollo.io/api/v1/oauth/token",
			Scopes: []string{"read_user_profile", "app_scopes"},
		},
		Fields:       apiKeyField("Apollo → Settings → Integrations → API → create a master key (search needs a master key)."),
		Capabilities: []string{c.CapSearchPeople, c.CapEnrichPerson, c.CapFindEmail, c.CapObserveMailboxes},
		Pricing:      "Free search; one credit per revealed email",
	}
}

func (Apollo) base(cl *c.Client) string { return cl.Base("apollo", "https://api.apollo.io") }

func (a Apollo) req(r *http.Request, cr c.Creds) *http.Request {
	if t := cr["access_token"]; t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	} else {
		r.Header.Set("X-Api-Key", cr["api_key"])
	}
	r.Header.Set("Cache-Control", "no-cache")
	return r
}

func (a Apollo) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Healthy    bool `json:"healthy"`
		IsLoggedIn bool `json:"is_logged_in"`
	}
	if _, err := call(ctx, cl, "Apollo", a.req(getReq(a.base(cl)+"/v1/auth/health", nil), cr), &r); err != nil {
		return c.Account{}, err
	}
	if !r.IsLoggedIn {
		return c.Account{}, c.Errf(c.ErrAuth, "Apollo did not accept the API key. Create a new key in Apollo → Settings → Integrations → API.")
	}
	return c.Account{Name: "Apollo"}, nil
}

func (Apollo) SearchFilters() []string {
	return []string{c.FilterTitles, c.FilterSeniorities, c.FilterIndustries, c.FilterEmployeeRanges,
		c.FilterLocations, c.FilterCompanyLocations, c.FilterCompanyDomains, c.FilterKeywords}
}

// apolloSeniority maps shared seniority words onto Apollo's.
var apolloSeniority = map[string]string{
	"owner": "owner", "founder": "founder", "c_suite": "c_suite", "partner": "partner", "vp": "vp",
	"head": "head", "director": "director", "manager": "manager", "senior": "senior", "entry": "entry", "intern": "intern",
}

type apolloOrg struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	PrimaryDomain         string `json:"primary_domain"`
	WebsiteURL            string `json:"website_url"`
	Industry              string `json:"industry"`
	EstimatedNumEmployees num    `json:"estimated_num_employees"`
	LinkedInURL           string `json:"linkedin_url"`
	City                  string `json:"city"`
	Country               string `json:"country"`
}

type apolloPerson struct {
	ID                 string     `json:"id"`
	FirstName          string     `json:"first_name"`
	LastName           string     `json:"last_name"`
	LastNameObfuscated string     `json:"last_name_obfuscated"`
	Title              string     `json:"title"`
	Seniority          string     `json:"seniority"`
	Email              string     `json:"email"`
	EmailStatus        string     `json:"email_status"`
	LinkedInURL        string     `json:"linkedin_url"`
	City               string     `json:"city"`
	State              string     `json:"state"`
	Country            string     `json:"country"`
	HasEmail           bool       `json:"has_email"`
	Organization       *apolloOrg `json:"organization"`
}

func (p apolloPerson) person() c.Person {
	out := c.Person{
		ProviderID: p.ID, FirstName: p.FirstName, LastName: p.LastName, Title: p.Title, Seniority: p.Seniority,
		LinkedInURL: p.LinkedInURL, City: p.City, State: p.State, Country: p.Country, HasEmail: p.HasEmail,
	}
	// Apollo returns placeholder addresses for emails it will not reveal.
	if e := strings.ToLower(strings.TrimSpace(p.Email)); e != "" && !strings.HasPrefix(e, "email_not_unlocked") && strings.Contains(e, "@") {
		out.Email = e
		out.HasEmail = true
		switch p.EmailStatus {
		case "verified":
			out.EmailStatus = c.EmailVerified
		default:
			out.EmailStatus = c.EmailGuessed
		}
	}
	if o := p.Organization; o != nil {
		out.Company = c.Company{
			Name: o.Name, Domain: firstNonEmpty(domainOf(o.PrimaryDomain), domainOf(o.WebsiteURL)), Website: o.WebsiteURL,
			Industry: o.Industry, Employees: int(o.EstimatedNumEmployees), LinkedInURL: o.LinkedInURL,
			Location: strings.Trim(o.City+", "+o.Country, ", "),
		}
	}
	return out
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a Apollo) SearchPeople(ctx context.Context, cl *c.Client, cr c.Creds, f c.PeopleFilter, cursor string, size int) (c.SearchPage, error) {
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return c.SearchPage{}, c.Errf(c.ErrInvalid, "bad page cursor %q", cursor)
		}
		page = n
	}
	size = min(max(size, 1), 100)
	body := map[string]any{"page": page, "per_page": size}
	set := func(k string, v []string) {
		if len(v) > 0 {
			body[k] = v
		}
	}
	set("person_titles", f.Titles)
	var sen []string
	for _, s := range f.Seniorities {
		if v, ok := apolloSeniority[s]; ok {
			sen = append(sen, v)
		}
	}
	set("person_seniorities", sen)
	set("q_organization_keyword_tags", append(append([]string{}, f.Industries...), f.Keywords...))
	var ranges []string
	for _, r := range f.EmployeeRanges {
		if lo, hi, ok := splitRange(r); ok {
			if hi == 0 {
				hi = 1000000
			}
			ranges = append(ranges, strconv.Itoa(lo)+","+strconv.Itoa(hi))
		}
	}
	set("organization_num_employees_ranges", ranges)
	set("person_locations", f.Locations)
	set("organization_locations", f.CompanyLocations)
	set("q_organization_domains_list", f.CompanyDomains)

	var r struct {
		TotalEntries *int           `json:"total_entries"`
		People       []apolloPerson `json:"people"`
		Pagination   *struct {
			TotalEntries *int `json:"total_entries"`
			TotalPages   int  `json:"total_pages"`
		} `json:"pagination"`
	}
	if _, err := call(ctx, cl, "Apollo", a.req(postReq(a.base(cl)+"/api/v1/mixed_people/api_search", body), cr), &r); err != nil {
		return c.SearchPage{}, err
	}
	out := c.SearchPage{Total: r.TotalEntries}
	if out.Total == nil && r.Pagination != nil {
		out.Total = r.Pagination.TotalEntries
	}
	for _, p := range r.People {
		out.People = append(out.People, p.person())
	}
	// Apollo serves at most 500 pages.
	more := len(r.People) == size && page < 500
	if out.Total != nil {
		more = more && page*size < *out.Total
	}
	if more {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

func (a Apollo) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	body := map[string]any{"reveal_personal_emails": false, "reveal_phone_number": false}
	put := func(k, v string) {
		if v != "" {
			body[k] = v
		}
	}
	put("id", q.ProviderID)
	put("email", q.Email)
	put("linkedin_url", q.LinkedInURL)
	put("first_name", q.FirstName)
	put("last_name", q.LastName)
	put("domain", q.Domain)
	put("organization_name", q.Company)
	if len(body) == 2 {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "nothing to look the person up by")
	}
	var r struct {
		Person          *apolloPerson `json:"person"`
		CreditsConsumed *num          `json:"credits_consumed"`
	}
	if _, err := call(ctx, cl, "Apollo", a.req(postReq(a.base(cl)+"/api/v1/people/match", body), cr), &r, http.StatusNotFound); err != nil {
		return c.Enriched{}, err
	}
	if r.Person == nil || r.Person.ID == "" {
		return c.Enriched{}, nil
	}
	p := r.Person.person()
	credits := 0.0
	if r.CreditsConsumed != nil {
		credits = float64(*r.CreditsConsumed)
	} else if p.Email != "" {
		credits = 1
	}
	return c.Enriched{Person: p, Found: true, Credits: credits}, nil
}
