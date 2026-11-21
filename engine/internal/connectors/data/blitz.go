package data

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Blitz: https://docs.blitz-api.ai
//
// Every call carries the workspace's own key in the x-api-key header. A
// plan is a number of records a month: a call counts one record for each
// result it returns and nothing for a miss, and says so in
// fair_usage.records_used. Errors are {"message": "..."} with the status:
// 401 for a refused key, 402 when the plan's records are used up, 423 for a
// locked account, 429 for more calls a second than the plan allows.
//
// Blitz finds a work email from a person's LinkedIn profile and from
// nothing else, so the email finder asks it first for a person who has a
// profile (ProfileOnly). People at a company come from its LinkedIn page:
// the domain is mapped to the page (CompanyPage), then the page is searched
// by job title (SearchCompanyPage). It also reads a profile's current jobs
// (ReadProfile) and a company page's size (CompanyProfile).
type Blitz struct{}

// blitzWait is how long one Blitz call may take. Blitz answers in well
// under a second; a call that takes this long has failed. blitzRetryWait
// is the pause before the one repeat of a call Blitz turned down for
// coming too fast. Variables so tests don't wait.
var (
	blitzWait      = 30 * time.Second
	blitzRetryWait = 1100 * time.Millisecond
)

// blitzFindsPerMinute is the pace of the smallest paid plan: ten calls a
// second on each endpoint.
const blitzFindsPerMinute = 600

func (Blitz) Definition() c.Definition {
	return c.Definition{
		ID: "blitz", Name: "Blitz", Category: c.CategoryData,
		Summary:     "Find decision makers at a company and their work emails from LinkedIn profiles, with your own Blitz plan.",
		Description: "Finds the people at a company by job title or seniority, from the company's domain, and finds a person's work email from their LinkedIn profile. The email finder asks Blitz first for anyone who has a profile. Only an address at the company asked about is used, so someone who has moved on is not emailed at their new job. It can also find a company's LinkedIn page from its domain, read how many people work there from that page, and read a person's profile, to check where they work now. A lookup that finds nothing is free.",
		Website:     "https://blitz-api.ai", DocsURL: "https://docs.blitz-api.ai",
		KeyURL: "https://app.blitz-api.ai", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Blitz app → API keys → copy your key."),
		Capabilities: []string{c.CapSearchPeople, c.CapEnrichPerson, c.CapFindEmail, c.CapCompanyPage, c.CapCompanyProfile, c.CapReadProfile},
		Pricing:      "Flat monthly plans counted in records: one record per email or person found, nothing for a miss",
	}
}

// ProfileOnly: Blitz finds an email from a LinkedIn profile only.
func (Blitz) ProfileOnly() bool { return true }

// FindsPerMinute is the pace one Blitz connection is asked at.
func (Blitz) FindsPerMinute() float64 { return blitzFindsPerMinute }

func (Blitz) base(cl *c.Client) string { return cl.Base("blitz", "https://api.blitz-api.ai") }

// blitzUsage is what a call says it used.
type blitzUsage struct {
	FairUsage *struct {
		RecordsUsed *float64 `json:"records_used"`
	} `json:"fair_usage"`
}

// records is what the call counted against the plan: what Blitz reports,
// or fallback when it reports nothing.
func (u blitzUsage) records(fallback float64) float64 {
	if u.FairUsage != nil && u.FairUsage.RecordsUsed != nil && *u.FairUsage.RecordsUsed >= 0 {
		return *u.FairUsage.RecordsUsed
	}
	return fallback
}

// do sends the request build returns and decodes a 2xx JSON object into v.
// A call Blitz turns down for coming too fast is repeated once after a
// short pause; nothing else is asked twice.
func (b Blitz) do(ctx context.Context, cl *c.Client, cr c.Creds, build func() *http.Request, v any) error {
	return b.ask(ctx, cl, cr, build, v, false)
}

// errBlitzMissing is Blitz's 404 for a call that asked about one thing it
// has no record of, when the caller takes that for an answer (ask).
var errBlitzMissing = errors.New("blitz has no record of it")

// ask is do; with missing, a 404 is errBlitzMissing instead of a refusal.
func (b Blitz) ask(ctx context.Context, cl *c.Client, cr c.Creds, build func() *http.Request, v any, missing bool) error {
	for attempt := 0; ; attempt++ {
		req := build().WithContext(ctx)
		req.Header.Set("x-api-key", cr["api_key"])
		req.Header.Set("Accept", "application/json")
		resp, err := cl.DoWithin(req, blitzWait)
		if err != nil {
			if tregTimedOut(err) {
				return c.TimedOutf("Blitz did not answer in time.")
			}
			return c.Errf(c.ErrUnavailable, "could not reach Blitz: %v", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			if tregTimedOut(err) {
				return c.TimedOutf("Blitz did not answer in time.")
			}
			return c.Errf(c.ErrUnavailable, "reading the Blitz response: %v", err)
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt == 0:
			select {
			case <-ctx.Done():
				return c.Errf(c.ErrUnavailable, "could not reach Blitz: %v", ctx.Err())
			case <-time.After(blitzRetryWait):
			}
			continue
		case resp.StatusCode == http.StatusNotFound && missing:
			return errBlitzMissing
		case resp.StatusCode == http.StatusLocked:
			return c.Errf(c.ErrQuota, "Blitz has locked the account (HTTP 423). Contact Blitz support to unlock it.")
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			// The answer is data from outside: only its message is read.
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(body, &e)
			return c.HTTPErrorFor("Blitz", resp, e.Message)
		}
		if v != nil {
			if err := json.Unmarshal(body, v); err != nil {
				return c.Errf(c.ErrUnavailable, "Blitz returned something that is not the JSON it documents")
			}
		}
		return nil
	}
}

func (b Blitz) post(ctx context.Context, cl *c.Client, cr c.Creds, path string, body, v any) error {
	return b.do(ctx, cl, cr, func() *http.Request { return postReq(b.base(cl)+path, body) }, v)
}

// Test checks the key and reads what is left of the plan. It uses no
// records.
func (b Blitz) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var info struct {
		Valid     *bool           `json:"valid"`
		ID        json.RawMessage `json:"id"`
		Remaining *float64        `json:"records_remaining"`
		Plans     []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"active_plans"`
	}
	err := b.do(ctx, cl, cr, func() *http.Request { return getReq(b.base(cl)+"/v2/account/key-info", nil) }, &info)
	if err != nil {
		return c.Account{}, err
	}
	if info.Valid != nil && !*info.Valid {
		return c.Account{}, c.Errf(c.ErrAuth, "Blitz says this API key is not valid. Check the key, or create a new one.")
	}
	acct := c.Account{ID: tregText(info.ID), Name: "Blitz"}
	if info.Remaining != nil {
		acct.Credits = info.Remaining
	}
	for _, p := range info.Plans {
		if name := tregWord(p.Name); name != "" {
			acct.Details = map[string]string{"plan": name}
			break
		}
	}
	return acct, nil
}

func (Blitz) SearchFilters() []string {
	return []string{c.FilterCompanyDomains, c.FilterTitles, c.FilterSeniorities}
}

// blitzTier is one step of a title search: Blitz returns the people who
// match the first tier, then the second, and so on until it has enough.
type blitzTier struct {
	Include []string `json:"include_title"`
	Exclude []string `json:"exclude_title,omitempty"`
}

// blitzDecisionMakers are the tiers asked for when a search names no title
// and no seniority: who decides at a company, most senior first.
var blitzDecisionMakers = []blitzTier{
	{Include: []string{"founder", "co-founder", "cofounder", "CEO", "chief executive", "owner", "managing director", "Geschäftsführer", "director general"}},
	{Include: []string{"CTO", "COO", "CMO", "CPO", "president", "general manager", "partner"}},
	{Include: []string{"head", "VP", "vice president", "director"}, Exclude: []string{"assistant", "intern", "junior"}},
}

// blitzSeniority maps the shared seniority words onto job title words.
// Owner and founder share a tier.
var blitzSeniority = []struct {
	words  []string
	titles []string
}{
	{[]string{"owner", "founder"}, []string{"founder", "co-founder", "cofounder", "owner"}},
	{[]string{"c_suite"}, []string{"CEO", "chief", "CTO", "COO", "CMO", "CFO", "CPO", "president", "managing director"}},
	{[]string{"partner"}, []string{"partner"}},
	{[]string{"vp"}, []string{"VP", "vice president"}},
	{[]string{"head"}, []string{"head"}},
	{[]string{"director"}, []string{"director"}},
	{[]string{"manager"}, []string{"manager"}},
	{[]string{"senior"}, []string{"senior", "lead", "principal"}},
	{[]string{"entry"}, []string{"junior", "associate"}},
	{[]string{"intern"}, []string{"intern"}},
}

// blitzCascade turns a filter into title tiers. Titles, when given, are the
// search. Otherwise each seniority asked for is a tier, most senior first.
// With neither, the decision makers are asked for.
func blitzCascade(f c.PeopleFilter) []blitzTier {
	var titles []string
	for _, t := range f.Titles {
		if t = strings.TrimSpace(t); t != "" && len(titles) < 50 {
			titles = append(titles, t)
		}
	}
	if len(titles) > 0 {
		return []blitzTier{{Include: titles}}
	}
	want := map[string]bool{}
	for _, s := range f.Seniorities {
		want[s] = true
	}
	var tiers []blitzTier
	for _, s := range blitzSeniority {
		for _, w := range s.words {
			if want[w] {
				tiers = append(tiers, blitzTier{Include: s.titles})
				break
			}
		}
	}
	if len(tiers) > 0 {
		return tiers
	}
	return blitzDecisionMakers
}

// blitzPerson is a person in a Blitz search. Only these fields are read.
type blitzPerson struct {
	FirstName   json.RawMessage `json:"first_name"`
	LastName    json.RawMessage `json:"last_name"`
	FullName    json.RawMessage `json:"full_name"`
	Headline    json.RawMessage `json:"headline"`
	LinkedInURL json.RawMessage `json:"linkedin_url"`
	Email       json.RawMessage `json:"email"`
	Location    struct {
		City    json.RawMessage `json:"city"`
		State   json.RawMessage `json:"state_code"`
		Country json.RawMessage `json:"country_code"`
	} `json:"location"`
	Experiences []struct {
		Company    json.RawMessage `json:"company_name"`
		CompanyURL json.RawMessage `json:"company_linkedin_url"`
		Domain     json.RawMessage `json:"company_domain"`
		Title      json.RawMessage `json:"job_title"`
		Current    json.RawMessage `json:"job_is_current"`
	} `json:"experiences"`
}

// person maps a search result onto the shared person, at the company that
// was searched (its domain, LinkedIn page and name).
func (p blitzPerson) person(domain, page, company string) c.Person {
	first, last := tregText(p.FirstName), tregText(p.LastName)
	if first == "" && last == "" {
		first, last, _ = strings.Cut(tregText(p.FullName), " ")
		last = strings.TrimSpace(last)
	}
	// The title is the current job at this company, else any current job,
	// else the profile's headline.
	title, anyCurrent := "", ""
	for _, e := range p.Experiences {
		if string(e.Current) != "true" {
			continue
		}
		t := tregText(e.Title)
		if anyCurrent == "" {
			anyCurrent = t
		}
		here := domainOf(tregText(e.Domain)) == domain ||
			(page != "" && strings.EqualFold(strings.TrimRight(tregText(e.CompanyURL), "/"), strings.TrimRight(page, "/")))
		if here && title == "" {
			title = t
		}
	}
	out := c.Person{
		FirstName: first, LastName: last, Title: firstNonEmpty(title, anyCurrent, tregText(p.Headline)),
		LinkedInURL: firstNonEmpty(c.LinkedInProfile(tregText(p.LinkedInURL)), withScheme(tregText(p.LinkedInURL))),
		City:        tregText(p.Location.City), State: tregText(p.Location.State), Country: tregText(p.Location.Country),
		Company: c.Company{Name: company, Domain: domain, LinkedInURL: page},
	}
	// Blitz's search documents no email. One it sends anyway is kept only
	// when it is at the company searched, and is never called verified.
	if e := tregAddress(tregText(p.Email)); e != "" && tregAtDomain(e, domain) {
		out.Email, out.EmailStatus, out.HasEmail = e, c.EmailGuessed, true
	}
	return out
}

// SearchPeople finds people at the companies in f.CompanyDomains, one
// company a page: the domain is mapped to the company's LinkedIn page, and
// the page is searched by job title (blitzCascade). cursor is the place of
// the next company in the list. A company with no page has nobody.
func (b Blitz) SearchPeople(ctx context.Context, cl *c.Client, cr c.Creds, f c.PeopleFilter, cursor string, size int) (c.SearchPage, error) {
	var domains []string
	seen := map[string]bool{}
	for _, d := range f.CompanyDomains {
		if d = domainOf(d); d != "" && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	if len(domains) == 0 {
		return c.SearchPage{}, c.Errf(c.ErrInvalid, "Blitz finds people at a company: add at least one company domain")
	}
	at := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n >= len(domains) {
			return c.SearchPage{}, c.Errf(c.ErrInvalid, "bad page cursor %q", cursor)
		}
		at = n
	}
	domain := domains[at]
	var out c.SearchPage
	if at+1 < len(domains) {
		out.Next = strconv.Itoa(at + 1)
	}

	page, err := b.CompanyPage(ctx, cl, cr, domain)
	if err != nil {
		return c.SearchPage{}, err
	}
	if !page.Found {
		return out, nil
	}
	found, err := b.SearchCompanyPage(ctx, cl, cr, page, f, size)
	if err != nil {
		return c.SearchPage{}, err
	}
	out.People, out.Credits = found.People, page.Credits+found.Credits
	return out, nil
}

// CompanyPage maps a company's domain to its LinkedIn page
// (connectors.CompanyLocator). A domain Blitz knows no page for is Found
// false and costs nothing; a page costs one record.
func (b Blitz) CompanyPage(ctx context.Context, cl *c.Client, cr c.Creds, domain string) (c.CompanyLinkedInPage, error) {
	d := domainOf(domain)
	if d == "" {
		return c.CompanyLinkedInPage{}, c.Errf(c.ErrInvalid, "Blitz needs the company's domain, like acme.example")
	}
	var page struct {
		blitzUsage
		Found json.RawMessage `json:"found"`
		URL   json.RawMessage `json:"company_linkedin_url"`
		Name  json.RawMessage `json:"company_name"`
	}
	if err := b.post(ctx, cl, cr, "/v2/enrichment/domain-to-linkedin", map[string]any{"domain": d}, &page); err != nil {
		return c.CompanyLinkedInPage{}, err
	}
	out := c.CompanyLinkedInPage{Domain: d}
	url := tregText(page.URL)
	if string(page.Found) != "true" || url == "" {
		return out, nil
	}
	out.Found, out.LinkedInURL, out.Name, out.Credits = true, url, tregClip(tregText(page.Name), 200), page.records(1)
	return out, nil
}

// CompanyProfile reads a company's LinkedIn page for its size: the
// employee range the page states and how many people list the company as
// their employer (connectors.CompanyProfiler). A page Blitz does not know is
// Found false and costs nothing; one it knows costs one record. The page is
// data from outside: only the fields named here are read.
func (b Blitz) CompanyProfile(ctx context.Context, cl *c.Client, cr c.Creds, pageURL string) (c.CompanyLinkedInProfile, error) {
	url := c.LinkedInCompanyPage(pageURL)
	if url == "" {
		return c.CompanyLinkedInProfile{}, c.Errf(c.ErrInvalid, "Blitz needs the company's LinkedIn page, like https://www.linkedin.com/company/acme-example")
	}
	var o struct {
		blitzUsage
		Found   json.RawMessage `json:"found"`
		Company *struct {
			Name      json.RawMessage `json:"name"`
			Domain    json.RawMessage `json:"domain"`
			Size      json.RawMessage `json:"size"`
			Employees json.RawMessage `json:"employees_on_linkedin"`
		} `json:"company"`
	}
	out := c.CompanyLinkedInProfile{LinkedInURL: url}
	// Blitz answers a page it has no record of with 404, which is an
	// answer like found false.
	err := b.ask(ctx, cl, cr, func() *http.Request {
		return postReq(b.base(cl)+"/v2/enrichment/company", map[string]any{"company_linkedin_url": url})
	}, &o, true)
	if errors.Is(err, errBlitzMissing) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if string(o.Found) != "true" || o.Company == nil {
		return out, nil
	}
	out.Found, out.Credits = true, o.records(1)
	out.Name, out.Domain, out.Size = tregClip(tregText(o.Company.Name), 200), domainOf(tregText(o.Company.Domain)), tregClip(tregText(o.Company.Size), 40)
	if n, err := strconv.Atoi(strings.TrimSpace(string(o.Company.Employees))); err == nil && n > 0 {
		out.Employees = n
	}
	return out, nil
}

// SearchCompanyPage searches a company's LinkedIn page by job title
// (blitzCascade), without looking the page up again
// (connectors.CompanyPageSearcher). Only the titles and seniorities of f
// are used. One record for each person returned.
func (b Blitz) SearchCompanyPage(ctx context.Context, cl *c.Client, cr c.Creds, page c.CompanyLinkedInPage, f c.PeopleFilter, size int) (c.SearchPage, error) {
	url := strings.TrimSpace(page.LinkedInURL)
	if url == "" {
		return c.SearchPage{}, c.Errf(c.ErrInvalid, "Blitz searches a company's LinkedIn page: look the page up first")
	}
	var res struct {
		blitzUsage
		Results []struct {
			Person json.RawMessage `json:"person"`
		} `json:"results"`
	}
	err := b.post(ctx, cl, cr, "/v2/search/waterfall-icp-keyword", map[string]any{
		"company_linkedin_url": url, "cascade": blitzCascade(f), "max_results": min(max(size, 1), 100)}, &res)
	if err != nil {
		return c.SearchPage{}, err
	}
	out := c.SearchPage{Credits: res.records(float64(len(res.Results)))}
	domain := domainOf(page.Domain)
	for _, r := range res.Results {
		// A person whose fields are not what Blitz documents keeps the
		// fields that are.
		var bp blitzPerson
		_ = json.Unmarshal(r.Person, &bp)
		p := bp.person(domain, withScheme(url), page.Name)
		if p.FirstName == "" && p.LastName == "" {
			continue
		}
		out.People = append(out.People, p)
	}
	return out, nil
}

// ReadProfile reads a person's LinkedIn profile and returns the jobs it
// lists as current (connectors.ProfileReader). A profile Blitz does not
// have is Found false and costs nothing; one it has costs one record,
// reported in Cost.Units. The profile is data from outside: only the
// fields named here are read.
func (b Blitz) ReadProfile(ctx context.Context, cl *c.Client, cr c.Creds, profileURL string) (c.PersonProfile, error) {
	profile := c.LinkedInProfile(profileURL)
	if profile == "" {
		return c.PersonProfile{}, c.Errf(c.ErrInvalid, "Blitz needs a person's LinkedIn profile address, like https://www.linkedin.com/in/someone")
	}
	var o struct {
		blitzUsage
		Found  json.RawMessage `json:"found"`
		Person *struct {
			Headline    json.RawMessage `json:"headline"`
			Experiences []struct {
				Company    json.RawMessage `json:"company_name"`
				CompanyURL json.RawMessage `json:"company_linkedin_url"`
				Domain     json.RawMessage `json:"company_domain"`
				Title      json.RawMessage `json:"job_title"`
				Current    json.RawMessage `json:"job_is_current"`
			} `json:"experiences"`
		} `json:"person"`
	}
	out := c.PersonProfile{Current: []c.ProfileRole{}}
	if err := b.post(ctx, cl, cr, "/v2/enrichment/person", map[string]any{"person_linkedin_url": profile}, &o); err != nil {
		return out, err
	}
	if string(o.Found) != "true" || o.Person == nil {
		return out, nil
	}
	out.Found = true
	out.Cost = c.SEOCost{Units: o.records(1)}
	out.Headline = tregClip(tregText(o.Person.Headline), 300)
	for _, e := range o.Person.Experiences {
		if strings.TrimSpace(string(e.Current)) != "true" {
			continue
		}
		role := c.ProfileRole{Company: tregClip(tregText(e.Company), 200), Title: tregClip(tregText(e.Title), 200)}
		if u := tregText(e.CompanyURL); u != "" {
			role.CompanyURL = tregClip(u, 300)
		} else if d := tregText(e.Domain); d != "" {
			role.CompanyURL = tregClip("https://"+d, 300)
		}
		if len(out.Current) < 20 {
			out.Current = append(out.Current, role)
		}
	}
	return out, nil
}

// blitzStep is Blitz's one step for the email finder: the lookup by
// LinkedIn profile.
const blitzStep = "blitz"

// FindSteps: Blitz has one step, by profile (connectors.StepFinder).
func (Blitz) FindSteps() []c.FindStep { return []c.FindStep{{ID: blitzStep, NeedsProfile: true}} }

// blitzEmails asks Blitz for the addresses it holds for a profile. found
// is Blitz saying it has any; the addresses are the one it puts first,
// then the rest of its list, each as it sent it.
func (b Blitz) blitzEmails(ctx context.Context, cl *c.Client, cr c.Creds, profile string) (found bool, emails []string, u blitzUsage, err error) {
	var o struct {
		blitzUsage
		Found json.RawMessage `json:"found"`
		Email json.RawMessage `json:"email"`
		All   json.RawMessage `json:"all_emails"`
	}
	if err := b.post(ctx, cl, cr, "/v2/enrichment/email", map[string]any{"person_linkedin_url": profile}, &o); err != nil {
		return false, nil, blitzUsage{}, err
	}
	if string(o.Found) != "true" {
		return false, nil, o.blitzUsage, nil
	}
	// The answer is data from outside: a list that is not the one
	// documented has no addresses.
	emails = []string{tregText(o.Email)}
	var all []struct {
		Email json.RawMessage `json:"email"`
	}
	if json.Unmarshal(o.All, &all) == nil {
		for _, a := range all {
			emails = append(emails, tregText(a.Email))
		}
	}
	return true, emails, o.blitzUsage, nil
}

// FindStep asks Blitz for every address it holds for the person's profile
// (connectors.StepFinder): the one it puts first, then the rest, current
// employer or not. The caller judges them. A miss costs nothing.
func (b Blitz) FindStep(ctx context.Context, cl *c.Client, cr c.Creds, step string, q c.PersonQuery) (c.StepAnswer, error) {
	if step != blitzStep {
		return c.StepAnswer{}, c.Errf(c.ErrInvalid, "Blitz has no email finder step %q", step)
	}
	profile := c.LinkedInProfile(q.LinkedInURL)
	if profile == "" {
		return c.StepAnswer{}, c.Errf(c.ErrInvalid, "Blitz needs a person's LinkedIn profile address, like https://www.linkedin.com/in/someone")
	}
	found, emails, u, err := b.blitzEmails(ctx, cl, cr, profile)
	if err != nil || !found {
		return c.StepAnswer{}, err
	}
	return c.StepAnswer{Addresses: tregAddresses(emails), Credits: u.records(1)}, nil
}

// EnrichPerson finds a work email from the person's LinkedIn profile.
// Blitz has no lookup by name, so a query without a profile has no address
// and makes no call. Blitz lists every address it holds for the person,
// current employer or not: only one at the company asked about, or a
// subdomain of it, is the answer, so someone who has moved on has none.
// With no company asked about, the address Blitz puts first is used.
//
// The address is never called verified: Blitz says it found one, with no
// word on when the mailbox was last checked, so the workspace's verifier
// still checks it.
func (b Blitz) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	profile := c.LinkedInProfile(q.LinkedInURL)
	if profile == "" {
		return c.Enriched{}, nil
	}
	found, emails, u, err := b.blitzEmails(ctx, cl, cr, profile)
	if err != nil {
		return c.Enriched{}, err
	}
	if !found {
		return c.Enriched{Credits: u.records(0)}, nil
	}
	out := c.Enriched{Credits: u.records(1)}
	domain := domainOf(q.Domain)
	for _, e := range emails {
		if e = tregAddress(e); e == "" || (domain != "" && !tregAtDomain(e, domain)) {
			continue
		}
		out.Found = true
		out.Person = c.Person{FirstName: q.FirstName, LastName: q.LastName, Email: e, EmailStatus: c.EmailGuessed, HasEmail: true,
			LinkedInURL: profile, Company: c.Company{Domain: domain}}
		return out, nil
	}
	return out, nil
}
