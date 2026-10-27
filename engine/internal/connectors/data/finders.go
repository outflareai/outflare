package data

import (
	"context"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Email finders: given a name and a company, return a work email. They are
// the steps of an enrichment waterfall after a search provider that hides
// emails, or for leads imported without one.

func nameDomain(q c.PersonQuery) (first, last, domain string, ok bool) {
	return q.FirstName, q.LastName, domainOf(q.Domain), q.FirstName != "" && q.LastName != "" && domainOf(q.Domain) != ""
}

// ---- Prospeo ----

// Prospeo: https://prospeo.io/api-docs/enrich-person
type Prospeo struct{}

func (Prospeo) Definition() c.Definition {
	return c.Definition{
		ID: "prospeo", Name: "Prospeo", Category: c.CategoryData,
		Summary:     "Find verified work emails from a name and company, or a LinkedIn URL.",
		Description: "Fills in work emails for people found elsewhere. Only verified addresses are returned, and you are charged only when one is found.",
		Website:     "https://prospeo.io", DocsURL: "https://prospeo.io/api-docs/enrich-person",
		KeyURL: "https://app.prospeo.io/api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Prospeo → API → copy your API key."),
		Capabilities: []string{c.CapEnrichPerson, c.CapFindEmail},
		Pricing:      "One credit per email found",
	}
}

func (Prospeo) base(cl *c.Client) string { return cl.Base("prospeo", "https://api.prospeo.io") }

func (p Prospeo) req(r *http.Request, cr c.Creds) *http.Request {
	r.Header.Set("X-KEY", cr["api_key"])
	return r
}

func (p Prospeo) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Error    bool `json:"error"`
		Response struct {
			RemainingCredits *float64 `json:"remaining_credits"`
			CurrentPlan      string   `json:"current_plan"`
		} `json:"response"`
	}
	if _, err := call(ctx, cl, "Prospeo", p.req(postReq(p.base(cl)+"/account-information", map[string]any{}), cr), &r); err != nil {
		return c.Account{}, err
	}
	acct := c.Account{Name: "Prospeo", Credits: r.Response.RemainingCredits}
	if r.Response.CurrentPlan != "" {
		acct.Details = map[string]string{"plan": r.Response.CurrentPlan}
	}
	return acct, nil
}

func (p Prospeo) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	d := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			d[k] = v
		}
	}
	put("first_name", q.FirstName)
	put("last_name", q.LastName)
	put("company_website", domainOf(q.Domain))
	put("company_name", q.Company)
	put("linkedin_url", q.LinkedInURL)
	put("email", q.Email)
	if d["linkedin_url"] == "" && d["email"] == "" && (d["first_name"] == "" || (d["company_website"] == "" && d["company_name"] == "")) {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "Prospeo needs a LinkedIn URL, an email, or a name with a company")
	}
	var r struct {
		Error          bool   `json:"error"`
		ErrorCode      string `json:"error_code"`
		FreeEnrichment bool   `json:"free_enrichment"`
		Person         struct {
			FirstName       string `json:"first_name"`
			LastName        string `json:"last_name"`
			CurrentJobTitle string `json:"current_job_title"`
			LinkedInURL     string `json:"linkedin_url"`
			Email           struct {
				Status   string `json:"status"`
				Revealed bool   `json:"revealed"`
				Email    string `json:"email"`
			} `json:"email"`
			Location struct {
				Country string `json:"country"`
				State   string `json:"state"`
				City    string `json:"city"`
			} `json:"location"`
		} `json:"person"`
		Company struct {
			Name          string `json:"name"`
			Website       string `json:"website"`
			Domain        string `json:"domain"`
			Industry      string `json:"industry"`
			EmployeeCount num    `json:"employee_count"`
			LinkedInURL   string `json:"linkedin_url"`
		} `json:"company"`
	}
	body := map[string]any{"data": d, "only_verified_email": true}
	if _, err := call(ctx, cl, "Prospeo", p.req(postReq(p.base(cl)+"/enrich-person", body), cr), &r, http.StatusBadRequest, http.StatusNotFound); err != nil {
		return c.Enriched{}, err
	}
	if r.Error {
		switch r.ErrorCode {
		case "NO_MATCH", "":
			return c.Enriched{}, nil
		case "INSUFFICIENT_CREDITS":
			return c.Enriched{}, c.Errf(c.ErrQuota, "Prospeo is out of credits.")
		case "INVALID_API_KEY":
			return c.Enriched{}, c.Errf(c.ErrAuth, "Prospeo refused the API key.")
		case "RATE_LIMITED":
			return c.Enriched{}, c.Errf(c.ErrQuota, "Prospeo is rate limiting requests. Try again in a minute.")
		}
		return c.Enriched{}, c.Errf(c.ErrInvalid, "Prospeo refused the request: %s", r.ErrorCode)
	}
	pr := r.Person
	out := c.Person{
		FirstName: pr.FirstName, LastName: pr.LastName, Title: pr.CurrentJobTitle, LinkedInURL: pr.LinkedInURL,
		City: pr.Location.City, State: pr.Location.State, Country: pr.Location.Country,
		Company: c.Company{
			Name: r.Company.Name, Domain: firstNonEmpty(domainOf(r.Company.Domain), domainOf(r.Company.Website)), Website: r.Company.Website,
			Industry: r.Company.Industry, Employees: int(r.Company.EmployeeCount), LinkedInURL: r.Company.LinkedInURL,
		},
	}
	if e := strings.ToLower(pr.Email.Email); strings.Contains(e, "@") {
		out.Email, out.HasEmail = e, true
		out.EmailStatus = c.EmailGuessed
		if strings.EqualFold(pr.Email.Status, "VERIFIED") {
			out.EmailStatus = c.EmailVerified
		}
	}
	credits := 1.0
	if r.FreeEnrichment || out.Email == "" {
		credits = 0
	}
	return c.Enriched{Person: out, Found: true, Credits: credits}, nil
}

// ---- LeadMagic ----

// LeadMagic: https://leadmagic.io/docs/v1/reference/email-finder
type LeadMagic struct{}

func (LeadMagic) Definition() c.Definition {
	return c.Definition{
		ID: "leadmagic", Name: "LeadMagic", Category: c.CategoryData,
		Summary:     "Find work emails from a name and company domain.",
		Description: "Fills in work emails for people found elsewhere. Charges only when it finds a valid address.",
		Website:     "https://leadmagic.io", DocsURL: "https://leadmagic.io/docs/v1/reference/email-finder",
		KeyURL: "https://app.leadmagic.io/api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("LeadMagic → API → copy your API key."),
		Capabilities: []string{c.CapEnrichPerson, c.CapFindEmail},
		Pricing:      "One credit per email found",
	}
}

func (LeadMagic) base(cl *c.Client) string { return cl.Base("leadmagic", "https://api.leadmagic.io") }

func (l LeadMagic) req(r *http.Request, cr c.Creds) *http.Request {
	r.Header.Set("X-API-Key", cr["api_key"])
	return r
}

func (l LeadMagic) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Credits *float64 `json:"credits"`
	}
	if _, err := call(ctx, cl, "LeadMagic", l.req(postReq(l.base(cl)+"/credits", map[string]any{}), cr), &r); err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: "LeadMagic", Credits: r.Credits}, nil
}

func (l LeadMagic) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	first, last, domain, ok := nameDomain(q)
	if !ok {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "LeadMagic needs a first name, last name and company domain")
	}
	var r struct {
		Email           string `json:"email"`
		Status          string `json:"status"`
		CreditsConsumed *num   `json:"credits_consumed"`
		CompanyName     string `json:"company_name"`
	}
	body := map[string]string{"first_name": first, "last_name": last, "domain": domain}
	if _, err := call(ctx, cl, "LeadMagic", l.req(postReq(l.base(cl)+"/email-finder", body), cr), &r, http.StatusNotFound); err != nil {
		return c.Enriched{}, err
	}
	e := strings.ToLower(strings.TrimSpace(r.Email))
	if !strings.Contains(e, "@") {
		return c.Enriched{}, nil
	}
	status := c.EmailGuessed
	if r.Status == "valid" {
		status = c.EmailVerified
	}
	credits := 1.0
	if r.CreditsConsumed != nil {
		credits = float64(*r.CreditsConsumed)
	}
	p := c.Person{FirstName: first, LastName: last, Email: e, EmailStatus: status, HasEmail: true,
		Company: c.Company{Domain: domain, Name: r.CompanyName}}
	return c.Enriched{Person: p, Found: true, Credits: credits}, nil
}

// ---- Findymail ----

// Findymail: https://app.findymail.com/docs
type Findymail struct{}

func (Findymail) Definition() c.Definition {
	return c.Definition{
		ID: "findymail", Name: "Findymail", Category: c.CategoryData,
		Summary:     "Find verified work emails from a name and company domain.",
		Description: "Fills in verified work emails for people found elsewhere. Charges only when it finds one.",
		Website:     "https://www.findymail.com", DocsURL: "https://app.findymail.com/docs",
		KeyURL: "https://app.findymail.com/api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Findymail → API → copy your API token."),
		Capabilities: []string{c.CapEnrichPerson, c.CapFindEmail},
		Pricing:      "One credit per email found",
	}
}

func (Findymail) base(cl *c.Client) string { return cl.Base("findymail", "https://app.findymail.com") }

func (f Findymail) req(r *http.Request, cr c.Creds) *http.Request {
	r.Header.Set("Authorization", "Bearer "+cr["api_key"])
	return r
}

func (f Findymail) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Credits *float64 `json:"credits"`
	}
	if _, err := call(ctx, cl, "Findymail", f.req(getReq(f.base(cl)+"/api/credits", nil), cr), &r); err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: "Findymail", Credits: r.Credits}, nil
}

func (f Findymail) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	first, last, domain, ok := nameDomain(q)
	if !ok {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "Findymail needs a first name, last name and company domain")
	}
	var r struct {
		Contact *struct {
			Name   string `json:"name"`
			Domain string `json:"domain"`
			Email  string `json:"email"`
		} `json:"contact"`
	}
	body := map[string]string{"name": first + " " + last, "domain": domain}
	if _, err := call(ctx, cl, "Findymail", f.req(postReq(f.base(cl)+"/api/search/name", body), cr), &r, http.StatusNotFound); err != nil {
		return c.Enriched{}, err
	}
	if r.Contact == nil || !strings.Contains(r.Contact.Email, "@") {
		return c.Enriched{}, nil
	}
	p := c.Person{FirstName: first, LastName: last, Email: strings.ToLower(r.Contact.Email), EmailStatus: c.EmailVerified,
		HasEmail: true, Company: c.Company{Domain: domain}}
	return c.Enriched{Person: p, Found: true, Credits: 1}, nil
}
