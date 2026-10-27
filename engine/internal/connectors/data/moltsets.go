package data

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// MoltSets: https://developer.moltsets.com/llms.txt
//
// Every tool is a POST to /api/v1/tools/{tool} with a JSON body, a bearer
// key (ms_...) and a non-empty User-Agent (403 without one). A call that
// completes is a 200 with {"status": "ok" | "not_found", "results",
// "metadata"}; a miss is free. metadata.tokens_used is present only on
// token-bound (free) accounts; paid plans are flat and omit it.
type MoltSets struct{}

func (MoltSets) Definition() c.Definition {
	return c.Definition{
		ID: "moltsets", Name: "MoltSets", Category: c.CategoryData,
		Summary:     "Search B2B people and find valid work emails with your own MoltSets plan.",
		Description: "Build lists from MoltSets people searches by title, seniority, industry, company size, country, company domain and keywords, and find work emails from a LinkedIn URL, an email, or a name with a company domain. Each email carries MoltSets' send-safety grade.",
		Website:     "https://moltsets.com", DocsURL: "https://developer.moltsets.com/api-reference",
		KeyURL: "https://app.moltsets.com", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("MoltSets app → API keys → create a key (it starts with ms_)."),
		Capabilities: []string{c.CapSearchPeople, c.CapEnrichPerson, c.CapFindEmail},
		Pricing:      "Flat monthly plans with fair-use limits; a free tier of 1,000 tokens",
	}
}

func (MoltSets) base(cl *c.Client) string { return cl.Base("moltsets", "https://api.moltsets.com") }

// msEnvelope is the shape every MoltSets tool answers with.
type msEnvelope struct {
	Status   string          `json:"status"`
	Results  json.RawMessage `json:"results"`
	Metadata struct {
		TokensUsed      num  `json:"tokens_used"`
		TokensRemaining *num `json:"tokens_remaining"`
	} `json:"metadata"`
}

func (m MoltSets) tool(ctx context.Context, cl *c.Client, cr c.Creds, name string, body any) (msEnvelope, error) {
	r := postReq(m.base(cl)+"/api/v1/tools/"+name, body)
	r.Header.Set("Authorization", "Bearer "+cr["api_key"])
	r.Header.Set("User-Agent", "Outflare")
	var env msEnvelope
	if _, err := call(ctx, cl, "MoltSets", r, &env); err != nil {
		return msEnvelope{}, err
	}
	return env, nil
}

func (m MoltSets) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	// get_account is free and checks the key.
	env, err := m.tool(ctx, cl, cr, "get_account", map[string]any{})
	if err != nil {
		return c.Account{}, err
	}
	var r struct {
		Name         string `json:"name"`
		Status       string `json:"status"`
		Plan         string `json:"plan"`
		TokenBalance *num   `json:"token_balance"`
	}
	_ = json.Unmarshal(env.Results, &r)
	if r.Status != "" && r.Status != "active" {
		return c.Account{}, c.Errf(c.ErrAuth, "The MoltSets account is %s. Sign in at app.moltsets.com and choose a plan.", r.Status)
	}
	acct := c.Account{Name: firstNonEmpty(r.Name, "MoltSets")}
	if r.TokenBalance != nil {
		acct.Credits = ptr(float64(*r.TokenBalance))
	}
	if r.Plan != "" {
		acct.Details = map[string]string{"plan": r.Plan}
	}
	return acct, nil
}

func (MoltSets) SearchFilters() []string {
	return []string{c.FilterTitles, c.FilterSeniorities, c.FilterIndustries, c.FilterEmployeeRanges,
		c.FilterLocations, c.FilterCompanyDomains, c.FilterKeywords}
}

// msSeniority maps shared seniority words onto MoltSets' levels.
var msSeniority = map[string]string{
	"owner": "Owner", "founder": "Owner", "c_suite": "C Suite", "partner": "Partner", "vp": "VP",
	"head": "Head", "director": "Director", "manager": "Manager", "senior": "Senior", "entry": "Entry", "intern": "Intern",
}

// msSize maps shared ranges onto MoltSets' employee bands.
var msSize = map[string][]string{
	"1-10": {"1-10"}, "11-50": {"11-20", "21-50"}, "51-200": {"51-200"}, "201-500": {"201-500"},
	"501-1000": {"501-1000"}, "1001-5000": {"1001-5000"}, "5001-10000": {"5001+"}, "10001+": {"5001+"},
}

// msIndustries are MoltSets' broad industry buckets. Other industries go
// to its LinkedIn industry filter, which uses LinkedIn's own labels.
var msIndustries = []string{
	"Professional and Business Services", "Information Technology", "Manufacturing", "Retail",
	"Health and Pharmaceuticals", "Creative Arts and Entertainment", "Construction", "Marketing & Advertising",
	"Finance and Banking", "Tourism and Hospitality", "Non-Profit and Social Services", "Food and Beverage",
	"Real Estate", "Education", "Transportation and Logistics", "Media and Publishing", "Energy",
	"Government and Public Administration", "Automotive", "Telecommunications", "Agriculture", "Utilities",
}

// msPerson is one MoltSets business profile.
type msPerson struct {
	ID            string `json:"_id"`
	FullName      string `json:"full_name"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	Title         string `json:"title"`
	Seniority     string `json:"seniority"`
	City          string `json:"city"`
	State         string `json:"state"`
	Country       string `json:"country"`
	LinkedInURL   string `json:"linkedin_url"`
	BusinessEmail string `json:"business_email"`
	RiskScore     string `json:"business_email_risk_score"`
	Company       struct {
		Name        string `json:"name"`
		Size        num    `json:"size"`
		Industry    string `json:"industry"`
		WebsiteURL  string `json:"website_url"`
		Domain      string `json:"domain"`
		LinkedInURL string `json:"linkedin_url"`
	} `json:"company"`
}

func (p msPerson) person() c.Person {
	first, last := p.FirstName, p.LastName
	if first == "" && last == "" {
		first, last, _ = strings.Cut(strings.TrimSpace(p.FullName), " ")
	}
	out := c.Person{
		ProviderID: p.ID, FirstName: first, LastName: strings.TrimSpace(last), Title: p.Title,
		Seniority:   strings.ToLower(strings.ReplaceAll(p.Seniority, " ", "_")),
		LinkedInURL: withScheme(p.LinkedInURL), City: p.City, State: p.State, Country: p.Country,
		Company: c.Company{
			Name: p.Company.Name, Domain: firstNonEmpty(domainOf(p.Company.Domain), domainOf(p.Company.WebsiteURL)),
			Website: p.Company.WebsiteURL, Industry: p.Company.Industry, Employees: int(p.Company.Size),
			LinkedInURL: withScheme(p.Company.LinkedInURL),
		},
	}
	out.Email, out.EmailStatus = msEmail(p.BusinessEmail, p.RiskScore)
	out.HasEmail = out.Email != ""
	return out
}

// msEmail reads an address with its send-safety grade: A (validated) and
// B (known engagement) are verified, C (catch-all) and F (no signal) are
// guesses, and D (spam trap, abuse or complaint) is never used.
func msEmail(email, grade string) (string, string) {
	e := strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(e, "@") {
		return "", c.EmailNone
	}
	switch strings.ToUpper(grade) {
	case "A", "B":
		return e, c.EmailVerified
	case "D":
		return "", c.EmailNone
	}
	return e, c.EmailGuessed
}

// oneOrMany sends a single value as a string and several as a list, the
// way MoltSets filters take them.
func oneOrMany(body map[string]any, key string, vals []string) {
	switch len(vals) {
	case 0:
	case 1:
		body[key] = vals[0]
	default:
		body[key] = vals
	}
}

func (m MoltSets) SearchPeople(ctx context.Context, cl *c.Client, cr c.Creds, f c.PeopleFilter, cursor string, size int) (c.SearchPage, error) {
	size = min(max(size, 1), 25)
	offset, _ := strconv.Atoi(cursor)
	offset = max(offset, 0)
	body := map[string]any{"limit": size, "offset": offset}
	oneOrMany(body, "title", f.Titles)
	var levels, sizes, buckets, labels, domains []string
	for _, s := range f.Seniorities {
		if v, ok := msSeniority[s]; ok {
			levels = append(levels, v)
		}
	}
	seen := map[string]bool{}
	for _, r := range f.EmployeeRanges {
		for _, v := range msSize[r] {
			if !seen[v] {
				seen[v] = true
				sizes = append(sizes, v)
			}
		}
	}
	for _, in := range f.Industries {
		bucket := ""
		for _, b := range msIndustries {
			if strings.EqualFold(strings.TrimSpace(in), b) {
				bucket = b
			}
		}
		if bucket != "" {
			buckets = append(buckets, bucket)
		} else {
			labels = append(labels, in)
		}
	}
	for _, d := range f.CompanyDomains {
		if d = domainOf(d); d != "" {
			domains = append(domains, d)
		}
	}
	oneOrMany(body, "seniority", levels)
	oneOrMany(body, "employee_range", sizes)
	oneOrMany(body, "industry", buckets)
	oneOrMany(body, "linkedin_industry", labels)
	oneOrMany(body, "country", f.Locations)
	oneOrMany(body, "company_domain", domains)
	if len(f.Keywords) > 0 {
		body["query"] = strings.Join(f.Keywords, " ")
	}
	env, err := m.tool(ctx, cl, cr, "search_people", body)
	if err != nil {
		return c.SearchPage{}, err
	}
	var r struct {
		Results []msPerson `json:"results"`
		Total   *int       `json:"total"`
	}
	_ = json.Unmarshal(env.Results, &r)
	out := c.SearchPage{Total: r.Total, Credits: float64(env.Metadata.TokensUsed)}
	if env.Status == "not_found" {
		return out, nil
	}
	for _, p := range r.Results {
		out.People = append(out.People, p.person())
	}
	if len(r.Results) == size && (r.Total == nil || offset+size < *r.Total) {
		out.Next = strconv.Itoa(offset + size)
	}
	return out, nil
}

// msFound is what the email tools return.
type msFound struct {
	Email       string `json:"email"`
	RiskScore   string `json:"risk_score"`
	LinkedInURL string `json:"linkedin_url"`
}

// EnrichPerson finds a work email: from a LinkedIn URL, from a name with a
// company domain, or from an email resolved to LinkedIn first.
func (m MoltSets) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	var out c.Enriched
	li := strings.TrimSpace(q.LinkedInURL)
	first, last, domain, byName := nameDomain(q)
	if li == "" && !byName && strings.Contains(q.Email, "@") {
		env, err := m.tool(ctx, cl, cr, "email_to_linkedin", map[string]string{"email": q.Email})
		if err != nil {
			return c.Enriched{}, err
		}
		out.Credits += float64(env.Metadata.TokensUsed)
		var r msFound
		_ = json.Unmarshal(env.Results, &r)
		if env.Status == "not_found" || r.LinkedInURL == "" {
			return out, nil
		}
		li = r.LinkedInURL
		out.Found, out.Person = true, c.Person{Email: strings.ToLower(q.Email), LinkedInURL: withScheme(li)}
	}
	var env msEnvelope
	var err error
	switch {
	case li != "":
		env, err = m.tool(ctx, cl, cr, "linkedin_to_business_email", map[string]string{"linkedin_url": li})
	case byName:
		env, err = m.tool(ctx, cl, cr, "search_business_email_by_name", map[string]string{"first_name": first, "last_name": last, "company_domain": domain})
	default:
		return c.Enriched{}, c.Errf(c.ErrInvalid, "MoltSets needs a LinkedIn URL, a name with a company domain, or an email")
	}
	if err != nil {
		return c.Enriched{}, err
	}
	out.Credits += float64(env.Metadata.TokensUsed)
	var r msFound
	_ = json.Unmarshal(env.Results, &r)
	email, status := msEmail(r.Email, r.RiskScore)
	if env.Status == "not_found" || email == "" {
		return out, nil
	}
	p := c.Person{FirstName: first, LastName: last, Email: email, EmailStatus: status, HasEmail: true,
		LinkedInURL: withScheme(firstNonEmpty(r.LinkedInURL, li)), Company: c.Company{Domain: domain}}
	return c.Enriched{Person: p.Merge(out.Person), Found: true, Credits: out.Credits}, nil
}
