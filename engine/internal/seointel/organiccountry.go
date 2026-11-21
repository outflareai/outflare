package seointel

import (
	"math"
	"strings"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// OrganicCountryRow is first-touch organic traffic and money from one
// country on one host on one day: sessions and their time on the site,
// signups, and payments counted once the same way as by landing page.
type OrganicCountryRow struct {
	Day        string
	Host       string
	Country    string // ISO alpha-2, or "" when the tool did not know
	Sessions   int
	Seconds    float64
	Signups    int
	Payments   int
	RevenueUSD float64
}

// RollupOrganicCountries adds the PostHog reads up by day, host and
// country. A payment counts once across payment_succeeded and
// service_purchased, trials and wallet top-ups are left out, and only US
// dollars count, as in RollupOrganicConversions.
func RollupOrganicCountries(in connectors.OrganicConversionResult, defaultHost string) []OrganicCountryRow {
	by := map[[3]string]*OrganicCountryRow{}
	add := func(day, host, path, country string) *OrganicCountryRow {
		host = OrganicHost(host, path)
		if host == "" {
			host = strings.ToLower(strings.TrimSpace(defaultHost))
		}
		k := [3]string{day, host, country}
		if by[k] == nil {
			by[k] = &OrganicCountryRow{Day: day, Host: host, Country: country}
		}
		return by[k]
	}
	for _, s := range in.CountrySessions {
		if s.Day == "" || s.Sessions == 0 {
			continue
		}
		r := add(s.Day, s.Host, "/", s.Country)
		r.Sessions += s.Sessions
		r.Seconds += s.Seconds
	}
	for _, s := range in.Signups {
		if s.Day != "" {
			add(s.Day, s.Host, s.Path, s.Country).Signups += s.Signups
		}
	}
	succeeded := map[payKey]bool{}
	for _, e := range in.Payments {
		if e.Event == "payment_succeeded" {
			if skip, _ := skipCurrency(e.Currency); !skip {
				succeeded[payKey{e.PersonID, e.Day, cents(e.Amount)}] = true
			}
		}
	}
	for _, e := range in.Payments {
		if e.Day == "" || e.Amount < 0 {
			continue
		}
		if skip, _ := skipCurrency(e.Currency); skip {
			continue
		}
		if e.Event == "service_purchased" && (e.Trial || e.Wallet || succeeded[payKey{e.PersonID, e.Day, cents(e.Amount)}]) {
			continue
		}
		if e.Event != "payment_succeeded" && e.Event != "service_purchased" {
			continue
		}
		r := add(e.Day, e.Host, e.Path, e.Country)
		r.Payments++
		r.RevenueUSD += math.Round(e.Amount*100) / 100
	}
	out := make([]OrganicCountryRow, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	return out
}
