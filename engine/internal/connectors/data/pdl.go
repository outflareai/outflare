package data

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// PDL is People Data Labs: https://docs.peopledatalabs.com/docs/person-search-api
// and https://docs.peopledatalabs.com/docs/person-enrichment-api
//
// Search and enrichment both spend one credit per person returned.
type PDL struct{}

func (PDL) Definition() c.Definition {
	return c.Definition{
		ID: "peopledatalabs", Name: "People Data Labs", Category: c.CategoryData,
		Summary:     "Search and enrich people from People Data Labs with your own credits.",
		Description: "Build lists from People Data Labs searches by title, seniority, industry, company size and location, and fill in missing fields on leads you already have.",
		Website:     "https://www.peopledatalabs.com", DocsURL: "https://docs.peopledatalabs.com/docs/person-search-api",
		KeyURL: "https://dashboard.peopledatalabs.com/api-keys", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("People Data Labs dashboard → API Keys."),
		Capabilities: []string{c.CapSearchPeople, c.CapEnrichPerson},
		Pricing:      "One credit per person returned",
	}
}

func (PDL) base(cl *c.Client) string {
	return cl.Base("peopledatalabs", "https://api.peopledatalabs.com")
}

func (p PDL) req(r *http.Request, cr c.Creds) *http.Request {
	r.Header.Set("X-Api-Key", cr["api_key"])
	return r
}

func (p PDL) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	// The autocomplete endpoint is free and checks the key.
	q := url.Values{"field": {"title"}, "text": {"sales"}, "size": {"1"}}
	if _, err := call(ctx, cl, "People Data Labs", p.req(getReq(p.base(cl)+"/v5/autocomplete", q), cr), nil); err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: "People Data Labs"}, nil
}

func (PDL) SearchFilters() []string {
	return []string{c.FilterTitles, c.FilterSeniorities, c.FilterIndustries, c.FilterEmployeeRanges,
		c.FilterLocations, c.FilterCompanyLocations, c.FilterCompanyDomains, c.FilterKeywords}
}

// pdlLevel maps shared seniority words onto PDL job_title_levels.
var pdlLevel = map[string]string{
	"owner": "owner", "founder": "owner", "c_suite": "cxo", "partner": "partner", "vp": "vp",
	"head": "director", "director": "director", "manager": "manager", "senior": "senior", "entry": "entry", "intern": "training",
}

// pdlSize maps shared ranges onto PDL job_company_size values.
var pdlSize = map[string]string{
	"1-10": "1-10", "11-50": "11-50", "51-200": "51-200", "201-500": "201-500", "501-1000": "501-1000",
	"1001-5000": "1001-5000", "5001-10000": "5001-10000", "10001+": "10001+",
}

type pdlPerson struct {
	ID                 string `json:"id"`
	FirstName          string `json:"first_name"`
	LastName           string `json:"last_name"`
	JobTitle           string `json:"job_title"`
	WorkEmail          any    `json:"work_email"` // a string, or true when the plan hides it
	LinkedInURL        string `json:"linkedin_url"`
	MobilePhone        any    `json:"mobile_phone"`
	LocationLocality   string `json:"location_locality"`
	LocationRegion     string `json:"location_region"`
	LocationCountry    string `json:"location_country"`
	JobCompanyName     string `json:"job_company_name"`
	JobCompanyWebsite  string `json:"job_company_website"`
	JobCompanyIndustry string `json:"job_company_industry"`
	JobCompanySize     string `json:"job_company_size"`
	JobCompanyLinkedIn string `json:"job_company_linkedin_url"`
	JobCompanyCountry  string `json:"job_company_location_country"`
}

func (p pdlPerson) person() c.Person {
	out := c.Person{
		ProviderID: p.ID, FirstName: titleCase(p.FirstName), LastName: titleCase(p.LastName), Title: p.JobTitle,
		LinkedInURL: withScheme(p.LinkedInURL), City: titleCase(p.LocationLocality), State: titleCase(p.LocationRegion),
		Country: titleCase(p.LocationCountry),
		Company: c.Company{
			Name: titleCase(p.JobCompanyName), Domain: domainOf(p.JobCompanyWebsite), Website: withScheme(p.JobCompanyWebsite),
			Industry: p.JobCompanyIndustry, LinkedInURL: withScheme(p.JobCompanyLinkedIn), Location: titleCase(p.JobCompanyCountry),
		},
	}
	if lo, hi, ok := splitRange(p.JobCompanySize); ok {
		out.Company.Employees = (lo + max(hi, lo)) / 2
	}
	switch v := p.WorkEmail.(type) {
	case string:
		if strings.Contains(v, "@") {
			out.Email, out.EmailStatus, out.HasEmail = strings.ToLower(v), c.EmailGuessed, true
		}
	case bool:
		out.HasEmail = v
	}
	if s, ok := p.MobilePhone.(string); ok {
		out.Phone = s
	}
	return out
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if w != "" && strings.ToLower(w) == w {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func withScheme(s string) string {
	if s == "" || strings.Contains(s, "://") {
		return s
	}
	return "https://" + s
}

// pdlQuery builds the Elasticsearch query PDL's search takes.
func pdlQuery(f c.PeopleFilter) map[string]any {
	var must []any
	anyOf := func(field string, vals []string) {
		if len(vals) == 0 {
			return
		}
		var should []any
		for _, v := range vals {
			should = append(should, map[string]any{"match": map[string]any{field: strings.ToLower(v)}})
		}
		must = append(must, map[string]any{"bool": map[string]any{"should": should}})
	}
	terms := func(field string, vals []string) {
		if len(vals) > 0 {
			must = append(must, map[string]any{"terms": map[string]any{field: vals}})
		}
	}
	anyOf("job_title", f.Titles)
	var levels, sizes []string
	for _, s := range f.Seniorities {
		if v, ok := pdlLevel[s]; ok {
			levels = append(levels, v)
		}
	}
	for _, r := range f.EmployeeRanges {
		if v, ok := pdlSize[r]; ok {
			sizes = append(sizes, v)
		}
	}
	terms("job_title_levels", levels)
	terms("job_company_size", sizes)
	anyOf("job_company_industry", f.Industries)
	anyOf("location_name", f.Locations)
	anyOf("job_company_location_name", f.CompanyLocations)
	var sites []string
	for _, d := range f.CompanyDomains {
		if d = domainOf(d); d != "" {
			sites = append(sites, d)
		}
	}
	terms("job_company_website", sites)
	anyOf("job_summary", f.Keywords)
	// Only people PDL holds a work email for are worth a credit.
	must = append(must, map[string]any{"exists": map[string]any{"field": "work_email"}})
	return map[string]any{"bool": map[string]any{"must": must}}
}

func (p PDL) SearchPeople(ctx context.Context, cl *c.Client, cr c.Creds, f c.PeopleFilter, cursor string, size int) (c.SearchPage, error) {
	size = min(max(size, 1), 100)
	body := map[string]any{"query": pdlQuery(f), "size": size, "titlecase": true}
	if cursor != "" {
		body["scroll_token"] = cursor
	}
	var r struct {
		Data        []pdlPerson `json:"data"`
		Total       *int        `json:"total"`
		ScrollToken string      `json:"scroll_token"`
	}
	status, err := call(ctx, cl, "People Data Labs", p.req(postReq(p.base(cl)+"/v5/person/search", body), cr), &r, http.StatusNotFound)
	if err != nil {
		return c.SearchPage{}, err
	}
	if status == http.StatusNotFound {
		return c.SearchPage{Total: ptr(0)}, nil
	}
	out := c.SearchPage{Total: r.Total, Credits: float64(len(r.Data))}
	for _, x := range r.Data {
		out.People = append(out.People, x.person())
	}
	if r.ScrollToken != "" && len(r.Data) == size {
		out.Next = r.ScrollToken
	}
	return out, nil
}

func (p PDL) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	v := url.Values{"titlecase": {"true"}, "min_likelihood": {"6"}}
	put := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	put("pdl_id", q.ProviderID)
	put("email", q.Email)
	put("profile", q.LinkedInURL)
	put("first_name", q.FirstName)
	put("last_name", q.LastName)
	put("company", firstNonEmpty(q.Domain, q.Company))
	if len(v) == 2 {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "nothing to look the person up by")
	}
	var r struct {
		Status     int       `json:"status"`
		Likelihood num       `json:"likelihood"`
		Data       pdlPerson `json:"data"`
	}
	status, err := call(ctx, cl, "People Data Labs", p.req(getReq(p.base(cl)+"/v5/person/enrich", v), cr), &r, http.StatusNotFound)
	if err != nil || status == http.StatusNotFound {
		return c.Enriched{}, err
	}
	return c.Enriched{Person: r.Data.person(), Found: true, Credits: 1}, nil
}
