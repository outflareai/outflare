package seointel

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

func TestRollupOrganicConversionsDedup(t *testing.T) {
	in := connectors.OrganicConversionResult{
		Sessions: []connectors.OrganicSessionRow{
			{Day: "2026-11-10", Host: "www.example.com", Path: "/Pricing/", Sessions: 10},
			{Day: "2026-11-10", Host: "www.example.com", Path: "/fr/pricing", Sessions: 4},
		},
		Signups: []connectors.OrganicSignupRow{
			{Day: "2026-11-10", Host: "www.example.com", Path: "/pricing", Signups: 2, SignupPeople: 2},
		},
		Payments: []connectors.OrganicPaymentEvent{
			{Day: "2026-11-10", Host: "www.example.com", Path: "/pricing", PersonID: "p1", Event: "payment_succeeded", Amount: 49, Currency: "usd"},
			{Day: "2026-11-10", Host: "www.example.com", Path: "/pricing", PersonID: "p1", Event: "service_purchased", Amount: 49, Currency: "USD"},
			{Day: "2026-11-10", Host: "www.example.com", Path: "/pricing", PersonID: "p2", Event: "service_purchased", Amount: 19, Currency: "usd", Trial: true},
			{Day: "2026-11-10", Host: "www.example.com", Path: "/pricing", PersonID: "p3", Event: "service_purchased", Amount: 10, Currency: "usd", Wallet: true},
			{Day: "2026-11-10", Host: "www.example.com", Path: "/pricing", PersonID: "p4", Event: "service_purchased", Amount: 99, Currency: "usd"},
			{Day: "2026-11-10", Host: "www.example.com", Path: "/pricing", PersonID: "p5", Event: "payment_succeeded", Amount: 20, Currency: "eur"},
		},
	}
	rows, skipped := RollupOrganicConversions(in, "www.example.com")
	if skipped != 1 {
		t.Fatalf("skipped other currency: %d", skipped)
	}
	var pricing, fr OrganicDayRow
	for _, r := range rows {
		switch r.Path {
		case "/pricing":
			pricing = r
		case "/fr/pricing":
			fr = r
		}
	}
	if pricing.OrganicSessions != 10 || pricing.Signups != 2 || pricing.Payments != 2 || pricing.PaymentPeople != 2 || pricing.RevenueUSD != 148 {
		t.Fatalf("pricing: %+v", pricing)
	}
	if pricing.CanonicalPath != "/pricing" {
		t.Fatalf("canonical: %s", pricing.CanonicalPath)
	}
	if fr.OrganicSessions != 4 || fr.CanonicalPath != "/pricing" {
		t.Fatalf("localized path: %+v", fr)
	}
}
