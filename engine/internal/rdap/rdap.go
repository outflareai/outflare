// Package rdap reads when a domain was registered from its public
// registration record (RDAP, the successor to WHOIS). Registries publish
// these records openly, with no account or key, so this is not a
// third-party API a workspace pays for.
package rdap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// DefaultBaseURL redirects each domain to its registry's RDAP server.
const DefaultBaseURL = "https://rdap.org"

// ErrNotFound means the registry has no record of the domain, or doesn't
// say when it was registered.
var ErrNotFound = errors.New("rdap: no registration date")

// Client reads registration records.
type Client struct {
	// HTTP makes the requests; nil uses a client with a 15 second timeout.
	HTTP *http.Client
	// BaseURL is the RDAP bootstrap service; "" is DefaultBaseURL.
	BaseURL string
}

// Domain is the registrable domain of host: www.shop.acme.example is
// acme.example.
func Domain(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// Registered is when the registrable domain of host was registered.
func (c *Client) Registered(ctx context.Context, host string) (time.Time, error) {
	d := Domain(host)
	if d == "" {
		return time.Time{}, ErrNotFound
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/domain/"+url.PathEscape(d), nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	res, err := hc.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return time.Time{}, ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("rdap: %s answered %d", d, res.StatusCode)
	}
	var rec struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&rec); err != nil {
		return time.Time{}, fmt.Errorf("rdap: %s: %w", d, err)
	}
	for _, e := range rec.Events {
		if e.Action != "registration" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, e.Date); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, ErrNotFound
}
