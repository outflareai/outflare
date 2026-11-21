package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/mail"
	"net/url"
	"strings"
	"unicode/utf8"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// People research: the same Task API asked who works at a company. The
// run, the wait and picking a run up again are ResearchCompany's (run);
// only the task and the answer differ.

// peopleSchema is the answer Parallel is asked for: a list of people.
var peopleSchema = map[string]any{
	"type": "json",
	"json_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"people": map[string]any{
				"type":        "array",
				"description": "Up to 8 people who currently work at the company, decision makers first.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":          map[string]any{"type": "string", "description": "Full name of the person."},
						"title":         map[string]any{"type": "string", "description": "Their current job title at the company."},
						"linkedin_url":  map[string]any{"type": "string", "description": "Their personal LinkedIn profile URL (linkedin.com/in/...), or empty string."},
						"work_email":    map[string]any{"type": "string", "description": "Their own work email on the company's domain, only if published in a source. Never guessed. Empty string if none."},
						"email_source":  map[string]any{"type": "string", "description": "URL of the page where the work email is published, or empty string."},
						"person_source": map[string]any{"type": "string", "description": "URL of the page showing they currently work at the company."},
					},
					"required":             []string{"name", "title", "linkedin_url", "work_email", "email_source", "person_source"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"people"},
		"additionalProperties": false,
	},
}

// peopleTask is the task Parallel is given for q.
func peopleTask(q c.PeopleResearchQuery) string {
	company := q.Name
	if company == "" {
		company = q.Domain
	}
	lines := []string{
		"Find up to 8 named people who currently work at " + company + " (website domain " + q.Domain + ").",
		"Prefer founders, the CEO, CTO and other executives, then heads and directors of data, engineering or product.",
		"For each person give their full name, current title, personal LinkedIn profile URL (linkedin.com/in/...) and the URL of a page showing they work there now.",
		"Give a work email only when it is published in a source, belongs to that person, and ends in @" + q.Domain + ". Never guess or construct an address. Never give shared inboxes such as info@, sales@ or support@.",
		"Use an empty string for anything you cannot find. Do not include people who have left the company.",
	}
	if len(q.Roles) > 0 {
		lines = append(lines, "Look first for people in these roles, most wanted first: "+strings.Join(q.Roles, "; ")+".")
	}
	if len(q.Exclude) > 0 {
		lines = append(lines, "Do not return these people, they were already considered: "+strings.Join(q.Exclude, "; ")+".")
	}
	return strings.Join(lines, "\n")
}

// ResearchPeople asks Parallel who works at a company
// (connectors.PeopleResearcher): it starts a run (or picks up q.RunID) and
// waits for its result as ResearchCompany does, on the same processors and
// at the same prices. A run that outlasts the wait comes back as a timeout
// with its id.
//
// A finished run with nobody in it, or with an answer that is not the list
// asked for, is an empty answer and not an error: the run is done and paid
// for, and asking again gives the same.
func (p Connector) ResearchPeople(ctx context.Context, cl *c.Client, creds c.Creds, q c.PeopleResearchQuery) (c.PeopleResearch, error) {
	q, err := q.Normal()
	if err != nil {
		return c.PeopleResearch{}, err
	}
	t := tiers[q.Effort]
	res, runID, err := p.run(ctx, cl, creds, t, q.RunID, q.Domain, peopleTask(q), peopleSchema)
	if err != nil {
		return c.PeopleResearch{RunID: runID}, err
	}
	return c.PeopleResearch{People: res.people(), RunID: runID, Cost: c.SEOCost{USD: t.usd, Estimated: true}}, nil
}

// people reads the people out of the answer: the JSON asked for with
// peopleSchema, sometimes sent as a string, sometimes as the bare list. It
// is text from the web: only the fields named are read, each kept only
// when it is what it should be, and anything else is dropped.
func (r result) people() []c.ResearchedPerson {
	out := []c.ResearchedPerson{}
	raw := bytes.TrimSpace(r.Output.Content)
	if len(raw) > 0 && raw[0] == '"' {
		var plain string
		if json.Unmarshal(raw, &plain) != nil {
			return out
		}
		raw = []byte(strings.TrimSpace(plain))
	}
	type person struct {
		Name     json.RawMessage `json:"name"`
		Title    json.RawMessage `json:"title"`
		LinkedIn json.RawMessage `json:"linkedin_url"`
		Email    json.RawMessage `json:"work_email"`
		EmailSrc json.RawMessage `json:"email_source"`
		Source   json.RawMessage `json:"person_source"`
	}
	var list []json.RawMessage
	var wrapped struct {
		People []json.RawMessage `json:"people"`
	}
	if json.Unmarshal(raw, &wrapped) == nil {
		list = wrapped.People
	} else if json.Unmarshal(raw, &list) != nil {
		return out
	}
	text := func(v json.RawMessage) string {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return ""
		}
		return strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	}
	seen := map[string]bool{}
	for _, item := range list {
		if len(out) == c.ResearchPeopleMax {
			break
		}
		var p person
		if json.Unmarshal(item, &p) != nil {
			continue
		}
		name := text(p.Name)
		if utf8.RuneCountInString(name) > 200 {
			name = strings.TrimSpace(string([]rune(name)[:200]))
		}
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, c.ResearchedPerson{
			Name: name, Title: clip(text(p.Title), maxTitle),
			LinkedInURL: c.LinkedInProfile(text(p.LinkedIn)),
			Email:       plainAddress(text(p.Email)),
			EmailSource: webPage(text(p.EmailSrc)), Source: webPage(text(p.Source)),
		})
	}
	return out
}

// plainAddress is s in lower case when it is one plain email address,
// else "".
func plainAddress(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 320 {
		return ""
	}
	if a, err := mail.ParseAddress(s); err != nil || strings.ToLower(a.Address) != s {
		return ""
	}
	return s
}

// webPage is s when it is an http(s) address, else "".
func webPage(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > 2000 {
		return ""
	}
	return u.String()
}
