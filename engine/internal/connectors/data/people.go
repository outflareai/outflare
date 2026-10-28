package data

import (
	"context"
	"encoding/json"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Visitor identification tools that name the people on a workspace's
// website. Each one runs on the workspace's own account and pushes the
// people it finds to a webhook Outflare gives the workspace; Outflare never
// asks them who anyone is. They identify people in the United States only,
// and the workspace turns this on itself after reading the notice on the
// Visitors page.

// maxPeoplePerHook is the most people one webhook call may carry.
const maxPeoplePerHook = 100

func revealPersonDefinition(id, name, site, docs, summary, description, pricing string) c.Definition {
	return c.Definition{
		ID: id, Name: name, Category: c.CategorySignals, Summary: summary, Description: description,
		Website: site, DocsURL: docs, Auth: c.AuthAPIKey, Capabilities: []string{c.CapRevealPerson}, Pricing: pricing,
	}
}

// RB2B names US visitors from the workspace's own RB2B account, through
// RB2B's webhook integration: https://support.rb2b.com
type RB2B struct{}

func (RB2B) Definition() c.Definition {
	return revealPersonDefinition("rb2b", "RB2B", "https://www.rb2b.com", "https://support.rb2b.com",
		"People visiting your website, by name, from your own RB2B account.",
		"Connect it, then paste the webhook address from Visitors → Settings into RB2B's webhook integration. Each person RB2B names becomes a person in your CRM marked for review and tagged as from RB2B. Nobody is emailed until you approve them.",
		"On your RB2B plan")
}

func (RB2B) Test(context.Context, *c.Client, c.Creds) (c.Account, error) {
	return c.Account{Name: "RB2B"}, nil
}

func (RB2B) ParsePeople(body []byte) ([]c.RevealedPerson, error) { return parsePeople(body) }

// Vector names US visitors from the workspace's own Vector account, through
// its contact.visited webhook: https://learn.vector.co
type Vector struct{}

func (Vector) Definition() c.Definition {
	return revealPersonDefinition("vector", "Vector", "https://www.vector.co", "https://learn.vector.co",
		"Contacts who visit your website, from your own Vector account.",
		"Connect it, then add the webhook address from Visitors → Settings in Vector for the contact.visited event. Each person Vector names becomes a person in your CRM marked for review and tagged as from Vector. Nobody is emailed until you approve them.",
		"On your Vector plan")
}

func (Vector) Test(context.Context, *c.Client, c.Creds) (c.Account, error) {
	return c.Account{Name: "Vector"}, nil
}

func (Vector) ParsePeople(body []byte) ([]c.RevealedPerson, error) { return parsePeople(body) }

// Warmly names US visitors from the workspace's own Warmly account, through
// its website visit webhook: https://help.warmly.ai
type Warmly struct{}

func (Warmly) Definition() c.Definition {
	return revealPersonDefinition("warmly", "Warmly", "https://www.warmly.ai", "https://help.warmly.ai",
		"Identified contacts on your website, from your own Warmly account.",
		"Connect it, then add the webhook address from Visitors → Settings in Warmly's webhook settings. Each person Warmly names becomes a person in your CRM marked for review and tagged as from Warmly. Nobody is emailed until you approve them.",
		"On your Warmly plan")
}

func (Warmly) Test(context.Context, *c.Client, c.Creds) (c.Account, error) {
	return c.Account{Name: "Warmly"}, nil
}

func (Warmly) ParsePeople(body []byte) ([]c.RevealedPerson, error) { return parsePeople(body) }

// parsePeople reads the people in a webhook body. The tools send one person
// as an object, several as an array, or either under a key such as data;
// their field names differ in case and spelling ("Business Email",
// business_email, workEmail), so fields are matched by their letters alone.
// People with neither an email address nor a LinkedIn profile are left out.
func parsePeople(body []byte) ([]c.RevealedPerson, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, c.Errf(c.ErrInvalid, "the body is not JSON")
	}
	var objs []map[string]any
	collectPeople(v, &objs, 0)
	if len(objs) > maxPeoplePerHook {
		return nil, c.Errf(c.ErrInvalid, "at most %d people per call", maxPeoplePerHook)
	}
	out := []c.RevealedPerson{}
	for _, o := range objs {
		f := map[string]any{}
		flatten(o, "", f)
		p := c.RevealedPerson{
			Email:       email(pick(f, "businessemail", "workemail", "email", "emailaddress", "contactemail", "personemail")),
			FirstName:   pick(f, "firstname", "givenname"),
			LastName:    pick(f, "lastname", "familyname", "surname"),
			Name:        pick(f, "fullname", "name", "contactname", "personname"),
			Title:       pick(f, "title", "jobtitle", "position"),
			LinkedInURL: linkedIn(pick(f, "linkedinurl", "linkedin", "linkedinprofile", "linkedinprofileurl", "contactlinkedinurl")),
			Page:        pick(f, "capturedurl", "pageurl", "visitedurl", "webpage", "lastpageurl", "url", "page"),
			SeenAt:      when(pick(f, "seenat", "visitedat", "lastseenat", "occurredat", "timestamp", "eventtime", "createdat")),
		}
		p.Company = c.Company{
			Name:        pick(f, "companyname", "company", "organizationname"),
			Domain:      domainOf(pick(f, "companydomain", "companywebsite", "website", "domain")),
			Industry:    pick(f, "companyindustry", "industry"),
			LinkedInURL: pick(f, "companylinkedinurl"),
		}
		p.Company.Website = pick(f, "companywebsite", "website")
		if n, err := strconv.Atoi(pick(f, "companyemployeecount", "employeecount", "employees")); err == nil {
			p.Company.Employees = n
		}
		var place []string
		for _, k := range []string{"city", "state", "country"} {
			if s := pick(f, k); s != "" {
				place = append(place, s)
			}
		}
		p.Company.Location = strings.Join(place, ", ")
		if p.Name == "" {
			p.Name = strings.TrimSpace(p.FirstName + " " + p.LastName)
		}
		if p.Email == "" && p.LinkedInURL == "" {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// collectPeople finds the person objects in a decoded body.
func collectPeople(v any, out *[]map[string]any, depth int) {
	if depth > 3 {
		return
	}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			collectPeople(x, out, depth+1)
		}
	case map[string]any:
		for _, k := range []string{"data", "contacts", "people", "records", "results", "payload"} {
			for key, x := range t {
				if norm(key) != k {
					continue
				}
				switch x.(type) {
				case []any, map[string]any:
					collectPeople(x, out, depth+1)
					return
				}
			}
		}
		*out = append(*out, t)
	}
}

// flatten copies an object's values into f by normalised key. A person's
// own nested objects (contact, session) add their fields as they are; a
// company's add them after "company".
func flatten(o map[string]any, prefix string, f map[string]any) {
	for k, v := range o {
		nk := norm(k)
		if m, ok := v.(map[string]any); ok {
			switch nk {
			case "contact", "person", "lead", "profile", "session", "visit", "attributes", "properties", "fields":
				flatten(m, prefix, f)
			case "company", "organization", "organisation", "account", "org":
				flatten(m, "company", f)
			default:
				flatten(m, prefix+nk, f)
			}
			continue
		}
		key := nk
		if prefix != "" && !strings.HasPrefix(nk, prefix) {
			key = prefix + nk
		}
		if _, seen := f[key]; !seen {
			f[key] = v
		}
	}
}

// norm keeps a key's letters and digits, lowercased.
func norm(k string) string {
	var b strings.Builder
	for _, r := range k {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// pick returns the first non-empty value among keys, as text.
func pick(f map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := f[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s[:min(len(s), 500)]
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

func email(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if a, err := mail.ParseAddress(s); err != nil || a.Address != s {
		return ""
	}
	return s
}

func linkedIn(s string) string {
	if !strings.Contains(strings.ToLower(s), "linkedin.com/") {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return s
}

func when(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		if n > 1e12 {
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	return time.Time{}
}
