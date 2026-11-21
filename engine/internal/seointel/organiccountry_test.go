package seointel

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

func TestRollupOrganicCountries(t *testing.T) {
	in := connectors.OrganicConversionResult{
		CountrySessions: []connectors.OrganicCountrySessionRow{
			{Day: "2026-11-01", Host: "northpeak.example", Country: "US", Sessions: 3, Seconds: 90},
			{Day: "2026-11-01", Host: "northpeak.example", Country: "US", Sessions: 1, Seconds: 10},
		},
		Signups: []connectors.OrganicSignupRow{{Day: "2026-11-01", Host: "northpeak.example", Path: "/a", Signups: 2, Country: "US"}},
		Payments: []connectors.OrganicPaymentEvent{
			{Day: "2026-11-01", Host: "northpeak.example", Path: "/a", PersonID: "p1", Event: "payment_succeeded", Amount: 20, Currency: "usd", Country: "US"},
			// The same payment again: counted once.
			{Day: "2026-11-01", Host: "northpeak.example", Path: "/a", PersonID: "p1", Event: "service_purchased", Amount: 20, Currency: "usd", Country: "US"},
			{Day: "2026-11-01", Host: "northpeak.example", Path: "/b", PersonID: "p2", Event: "service_purchased", Amount: 15, Currency: "usd", Country: "IN"},
			{Day: "2026-11-01", Host: "northpeak.example", Path: "/b", PersonID: "p3", Event: "service_purchased", Amount: 15, Currency: "usd", Country: "IN", Trial: true},
			{Day: "2026-11-01", Host: "northpeak.example", Path: "/b", PersonID: "p4", Event: "payment_succeeded", Amount: 15, Currency: "eur", Country: "IN"},
		},
	}
	by := map[string]OrganicCountryRow{}
	for _, r := range RollupOrganicCountries(in, "northpeak.example") {
		by[r.Country] = r
	}
	if us := by["US"]; us.Sessions != 4 || us.Seconds != 100 || us.Signups != 2 || us.Payments != 1 || us.RevenueUSD != 20 {
		t.Fatalf("US: %+v", us)
	}
	if india := by["IN"]; india.Payments != 1 || india.RevenueUSD != 15 || india.Sessions != 0 {
		t.Fatalf("IN: %+v", india)
	}
	if len(by) != 2 {
		t.Fatalf("rows: %v", by)
	}
}
