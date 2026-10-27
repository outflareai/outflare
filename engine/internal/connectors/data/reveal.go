package data

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// RevealCompany looks up the company behind an IP address with People Data
// Labs' IP Enrichment API: https://docs.peopledatalabs.com/docs/ip-enrichment-api
// One credit per match; a miss (404) is free.
func (p PDL) RevealCompany(ctx context.Context, cl *c.Client, cr c.Creds, ip string) (c.Revealed, error) {
	q := url.Values{"ip": {ip}, "return_ip_metadata": {"true"}, "min_confidence": {"moderate"}}
	var out struct {
		Data struct {
			IP struct {
				Metadata struct {
					Mobile  bool `json:"mobile"`
					Hosting bool `json:"hosting"`
					Proxy   bool `json:"proxy"`
					Tor     bool `json:"tor"`
					VPN     bool `json:"vpn"`
					Relay   bool `json:"relay"`
				} `json:"metadata"`
			} `json:"ip"`
			Company *struct {
				Name          string `json:"name"`
				DisplayName   string `json:"display_name"`
				Website       string `json:"website"`
				Industry      string `json:"industry"`
				Size          string `json:"size"`
				EmployeeCount int    `json:"employee_count"`
				LinkedInURL   string `json:"linkedin_url"`
				Location      *struct {
					Name string `json:"name"`
				} `json:"location"`
			} `json:"company"`
		} `json:"data"`
	}
	status, err := call(ctx, cl, "People Data Labs", p.req(getReq(p.base(cl)+"/v5/ip/enrich", q), cr), &out, http.StatusNotFound)
	if err != nil {
		return c.Revealed{}, err
	}
	m := out.Data.IP.Metadata
	r := c.Revealed{Network: c.NetworkUnknown}
	switch {
	case m.Hosting || m.Proxy || m.Tor || m.VPN || m.Relay:
		r.Network = c.NetworkHosting
	case m.Mobile:
		r.Network = c.NetworkMobile
	}
	co := out.Data.Company
	if status == http.StatusNotFound || co == nil || domainOf(co.Website) == "" {
		return r, nil
	}
	if r.Network == c.NetworkUnknown {
		r.Network = c.NetworkBusiness
	}
	name := co.DisplayName
	if name == "" {
		name = co.Name
	}
	r.Found = true
	r.Company = c.Company{Name: name, Domain: domainOf(co.Website), Website: co.Website, Industry: co.Industry,
		Employees: co.EmployeeCount, LinkedInURL: co.LinkedInURL}
	if co.Location != nil {
		r.Company.Location = co.Location.Name
	}
	return r, nil
}

// Snitcher identifies the companies behind website visitors' IP addresses:
// https://docs.snitcher.com/powered-by-snitcher/ip2company/introduction
// Its IP to Company API is sold separately from Snitcher's own plans; the
// workspace gets the key from Snitcher.
type Snitcher struct{}

func (Snitcher) Definition() c.Definition {
	return c.Definition{
		ID: "snitcher", Name: "Snitcher", Category: c.CategoryData,
		Summary:     "Find the companies behind your website's visitors with your own Snitcher IP to Company key.",
		Description: "Outflare sends Snitcher the address of each new visitor who accepted your site's cookies, once a day at most, and adds the company to your CRM. Addresses of homes, phones, data centres and VPNs name nobody. Checking the key looks up one public address.",
		Website:     "https://www.snitcher.com", DocsURL: "https://docs.snitcher.com/powered-by-snitcher/ip2company/introduction",
		KeyURL: "https://www.snitcher.com/ip-to-company-api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("The IP to Company API key Snitcher gave you."),
		Capabilities: []string{c.CapRevealCompany},
		Pricing:      "Per lookup, on your Snitcher plan",
	}
}

func (Snitcher) base(cl *c.Client) string { return cl.Base("snitcher", "https://api.snitcher.com") }

// snitcherTestIP is a public resolver's address: a lookup that names no
// company, to check the key.
const snitcherTestIP = "1.1.1.1"

func (s Snitcher) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	if _, err := s.RevealCompany(ctx, cl, cr, snitcherTestIP); err != nil && err != c.ErrRetryLater {
		return c.Account{}, err
	}
	return c.Account{Name: "Snitcher"}, nil
}

func (s Snitcher) RevealCompany(ctx context.Context, cl *c.Client, cr c.Creds, ip string) (c.Revealed, error) {
	req, _ := http.NewRequest(http.MethodPost, s.base(cl)+"/company/find?"+url.Values{"ip": {ip}}.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+cr["api_key"])
	var out struct {
		Type    string `json:"type"`
		Domain  string `json:"domain"`
		Company *struct {
			Name          string `json:"name"`
			Domain        string `json:"domain"`
			Website       string `json:"website"`
			Industry      string `json:"industry"`
			EmployeeRange string `json:"employee_range"`
			Employees     any    `json:"employees"`
			LinkedInURL   string `json:"linkedin_url"`
			Location      any    `json:"location"`
		} `json:"company"`
	}
	status, err := call(ctx, cl, "Snitcher", req, &out, http.StatusAccepted, http.StatusNotFound)
	if err != nil {
		return c.Revealed{}, err
	}
	if status == http.StatusAccepted {
		return c.Revealed{}, c.ErrRetryLater
	}
	r := c.Revealed{}
	switch strings.ToLower(out.Type) {
	case "business", "company", "organization", "education", "government":
		r.Network = c.NetworkBusiness
	case "isp", "residential":
		r.Network = c.NetworkISP
	case "mobile", "cellular":
		r.Network = c.NetworkMobile
	case "hosting", "datacenter", "data_center", "vpn", "proxy", "tor":
		r.Network = c.NetworkHosting
	}
	if status == http.StatusNotFound || out.Company == nil || r.Network != c.NetworkBusiness {
		return r, nil
	}
	co := out.Company
	domain := domainOf(co.Domain)
	if domain == "" {
		domain = domainOf(co.Website)
	}
	if domain == "" {
		domain = domainOf(out.Domain)
	}
	if domain == "" {
		return r, nil
	}
	r.Found = true
	r.Company = c.Company{Name: co.Name, Domain: domain, Website: co.Website, Industry: co.Industry, LinkedInURL: co.LinkedInURL}
	switch n := co.Employees.(type) {
	case float64:
		r.Company.Employees = int(n)
	case string:
		r.Company.Employees, _ = strconv.Atoi(n)
	}
	if loc, ok := co.Location.(string); ok {
		r.Company.Location = loc
	}
	return r, nil
}
