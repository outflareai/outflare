package verifiers

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// EnrichPerson finds a work email with Hunter's email finder:
// https://hunter.io/api-documentation/v2#email-finder
// It spends one search credit when an address is found.
func (h Hunter) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(q.Domain)), "www.")
	if q.FirstName == "" || q.LastName == "" || (domain == "" && q.Company == "") {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "Hunter needs a first name, last name and company")
	}
	v := url.Values{"first_name": {q.FirstName}, "last_name": {q.LastName}, "api_key": {cr["api_key"]}}
	if domain != "" {
		v.Set("domain", domain)
	} else {
		v.Set("company", q.Company)
	}
	var r struct {
		Data struct {
			FirstName    string  `json:"first_name"`
			LastName     string  `json:"last_name"`
			Email        string  `json:"email"`
			Score        float64 `json:"score"`
			Domain       string  `json:"domain"`
			Position     string  `json:"position"`
			LinkedInURL  string  `json:"linkedin_url"`
			Company      string  `json:"company"`
			Verification struct {
				Status string `json:"status"`
			} `json:"verification"`
		} `json:"data"`
	}
	status, err := getJSON(ctx, cl, "Hunter", get(h.base(cl)+"/v2/email-finder", v), &r)
	if status == http.StatusNotFound {
		return c.Enriched{}, nil
	}
	if err != nil {
		return c.Enriched{}, err
	}
	d := r.Data
	if !strings.Contains(d.Email, "@") {
		return c.Enriched{}, nil
	}
	es := c.EmailGuessed
	if d.Verification.Status == "valid" {
		es = c.EmailVerified
	}
	p := c.Person{FirstName: d.FirstName, LastName: d.LastName, Title: d.Position, LinkedInURL: d.LinkedInURL,
		Email: strings.ToLower(d.Email), EmailStatus: es, HasEmail: true,
		Company: c.Company{Name: d.Company, Domain: d.Domain}}
	return c.Enriched{Person: p, Found: true, Credits: 1}, nil
}
