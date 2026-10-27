// Package dnshosts writes DNS records on the workspace's own DNS host, so
// verifying a site or setting up a domain is one click instead of a copy
// and paste into another dashboard.
package dnshosts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// All returns the DNS hosts that can be connected.
func All() []c.Connector { return []c.Connector{Cloudflare{}} }

// Cloudflare adds records to the zones of the workspace's own Cloudflare
// account, signed in with OAuth or an API token that can edit DNS.
type Cloudflare struct{}

func (Cloudflare) Definition() c.Definition {
	return c.Definition{
		ID: "cloudflare", Name: "Cloudflare", Category: c.CategoryDNS, Website: "https://www.cloudflare.com",
		DocsURL: "https://developers.cloudflare.com/api/resources/dns/subresources/records/",
		KeyURL:  "https://dash.cloudflare.com/profile/api-tokens",
		Summary: "Add the DNS records Outflare needs (site verification, sending and tracking domains) in one click.",
		Description: "Outflare only adds records, each with the comment \"Added by Outflare\": it never changes or deletes one. " +
			"With an API token, use the \"Edit zone DNS\" template, limited to the zones you want Outflare to write to.",
		Auth: c.AuthOAuth2, AuthMethods: c.Both, Status: c.Available, Pricing: "Free",
		Fields: []c.Field{
			{Name: "api_token", Label: "API token", Secret: true, Help: "My Profile > API Tokens > Create token > \"Edit zone DNS\" template (Zone > DNS > Edit and Zone > Zone > Read)."},
		},
		// Scope names follow API token permission names, like Cloudflare
		// Web Analytics; CONNECTOR_CLOUDFLARE_SCOPES replaces them if the
		// OAuth client was set up with others.
		OAuth: &c.OAuth{
			AuthURL: "https://dash.cloudflare.com/oauth2/auth", TokenURL: "https://dash.cloudflare.com/oauth2/token",
			Scopes: []string{"offline_access", "zone.read", "dns.write"},
		},
		Capabilities: []string{c.CapDNSWrite},
	}
}

func token(creds c.Creds) string {
	if t := creds["access_token"]; t != "" {
		return t
	}
	return creds["api_token"]
}

type zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account struct {
		Name string `json:"name"`
	} `json:"account"`
}

func (cf Cloudflare) call(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, cl.Base("cloudflare", cloudflareAPI)+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token(creds))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	_, err = c.DoJSON(ctx, cl, "Cloudflare", req, out)
	return err
}

func (cf Cloudflare) zones(ctx context.Context, cl *c.Client, creds c.Creds, name string) ([]zone, error) {
	q := url.Values{"per_page": {"50"}}
	if name != "" {
		q.Set("name", name)
	}
	var resp struct {
		Result []zone `json:"result"`
	}
	if err := cf.call(ctx, cl, creds, "GET", "/zones?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (cf Cloudflare) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if token(creds) == "" {
		return c.Account{}, c.Errf(c.ErrAuth, "add a Cloudflare API token")
	}
	zs, err := cf.zones(ctx, cl, creds, "")
	if err != nil {
		return c.Account{}, err
	}
	if len(zs) == 0 {
		return c.Account{}, c.Errf(c.ErrAuth, "the token can't see any zone: give it Zone > Zone > Read and Zone > DNS > Edit on your domains")
	}
	names := make([]string, 0, len(zs))
	for _, z := range zs {
		names = append(names, z.Name)
	}
	acct := c.Account{ID: zs[0].ID, Name: zs[0].Account.Name, Details: map[string]string{"zones": strings.Join(names, ", ")}}
	if acct.Name == "" {
		acct.Name = zs[0].Name
	}
	return acct, nil
}

// zoneFor finds the zone that holds host: the longest of host's parent
// names the connection can see.
func (cf Cloudflare) zoneFor(ctx context.Context, cl *c.Client, creds c.Creds, host string) (zone, error) {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(host), "."), ".")
	for i := 0; i+1 < len(labels); i++ {
		name := strings.Join(labels[i:], ".")
		zs, err := cf.zones(ctx, cl, creds, name)
		if err != nil {
			return zone{}, err
		}
		for _, z := range zs {
			if strings.EqualFold(z.Name, name) {
				return z, nil
			}
		}
	}
	return zone{}, c.Errf(c.ErrInvalid, "%s is not on this Cloudflare account, or the connection can't edit it: add the zone to the token, or add the record by hand", host)
}

// unquote drops the quotes Cloudflare may put around a TXT record's value.
func unquote(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"`)
}

// txtPolicy says whether a TXT value is an SPF or DMARC policy, of which
// a name holds only one.
func txtPolicy(v string) string {
	v = strings.ToLower(unquote(v))
	switch {
	case strings.HasPrefix(v, "v=spf1"):
		return "spf"
	case strings.HasPrefix(v, "v=dmarc1"):
		return "dmarc"
	}
	return ""
}

// spfMechanism is the part of an SPF record to merge into another one.
func spfMechanism(v string) string {
	for _, f := range strings.Fields(unquote(v)) {
		if strings.HasPrefix(strings.ToLower(f), "include:") {
			return f
		}
	}
	return unquote(v)
}

func (cf Cloudflare) EnsureRecords(ctx context.Context, cl *c.Client, creds c.Creds, records []c.DNSRecord) (c.DNSResult, error) {
	var res c.DNSResult
	for _, rec := range records {
		z, err := cf.zoneFor(ctx, cl, creds, rec.Name)
		if err != nil {
			return res, err
		}
		res.Zone = z.Name
		q := url.Values{"type": {rec.Type}, "name": {rec.Name}, "per_page": {"100"}}
		var have struct {
			Result []struct {
				Content string `json:"content"`
			} `json:"result"`
		}
		if err := cf.call(ctx, cl, creds, "GET", "/zones/"+url.PathEscape(z.ID)+"/dns_records?"+q.Encode(), nil, &have); err != nil {
			return res, err
		}
		found, policy := false, ""
		for _, h := range have.Result {
			if strings.EqualFold(strings.TrimSuffix(unquote(h.Content), "."), strings.TrimSuffix(unquote(rec.Content), ".")) {
				found = true
			}
			if p := txtPolicy(h.Content); p != "" && p == txtPolicy(rec.Content) {
				policy = p
			}
		}
		if found {
			res.Existing++
			continue
		}
		switch {
		case rec.Type == "CNAME" && len(have.Result) > 0:
			return res, c.Errf(c.ErrInvalid, "%s already has a CNAME record pointing elsewhere; Outflare doesn't change records, so change it by hand", rec.Name)
		case policy == "dmarc":
			// A name holds one DMARC policy, and any will do: keep theirs.
			res.Existing++
			continue
		case policy == "spf":
			// Two SPF records on one name break both.
			return res, c.Errf(c.ErrInvalid, "%s already has an SPF record; Outflare doesn't change records, so add %q to it by hand", rec.Name, spfMechanism(rec.Content))
		}
		content := rec.Content
		if rec.Type == "TXT" {
			content = fmt.Sprintf("%q", unquote(rec.Content))
		}
		body := map[string]any{"type": rec.Type, "name": rec.Name, "content": content, "ttl": 1, "comment": "Added by Outflare"}
		switch rec.Type {
		case "CNAME":
			body["proxied"] = false
		case "MX":
			body["priority"] = rec.Priority
		}
		if err := cf.call(ctx, cl, creds, "POST", "/zones/"+url.PathEscape(z.ID)+"/dns_records", body, nil); err != nil {
			if c.KindOf(err) == c.ErrAuth {
				return res, c.Errf(c.ErrAuth, "Cloudflare refused to add the record on %s: the connection needs Zone > DNS > Edit on that zone", z.Name)
			}
			return res, err
		}
		res.Added++
	}
	return res, nil
}
