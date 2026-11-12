package seointel

import (
	"math"
	"strings"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// OrganicDayRow is one landing page on one day after the PostHog reads
// have been merged and payments have been counted once.
type OrganicDayRow struct {
	Day             string
	Host            string
	Path            string
	CanonicalPath   string
	OrganicSessions int
	Signups         int
	SignupPeople    int
	Payments        int
	PaymentPeople   int
	RevenueUSD      float64
}

type payKey struct {
	person string
	day    string
	cents  int64
}

// RollupOrganicConversions merges sessions, first-touch signups and
// payment events onto one row per day, host and path. Paid
// service_purchased events (trials and wallet top-ups dropped) are kept
// only when no payment_succeeded has the same person, calendar day and
// amount to the cent. Amounts not in USD are skipped.
func RollupOrganicConversions(in connectors.OrganicConversionResult, defaultHost string) (rows []OrganicDayRow, skippedOtherCurrency int) {
	type acc struct {
		OrganicSessions int
		Signups         int
		SignupPeople    int
		Payments        int
		people          map[string]bool
		RevenueUSD      float64
	}
	keyOf := func(day, host, path string) string {
		return day + "\x00" + host + "\x00" + path
	}
	by := map[string]*acc{}
	rowMeta := map[string]OrganicDayRow{}
	add := func(day, host, path string) *acc {
		host = OrganicHost(host, path)
		if host == "" {
			host = strings.ToLower(strings.TrimSpace(defaultHost))
		}
		norm := NormalizeOrganicPath(path)
		canon := CanonicalOrganicPath(path)
		k := keyOf(day, host, norm)
		if _, ok := by[k]; !ok {
			by[k] = &acc{people: map[string]bool{}}
			rowMeta[k] = OrganicDayRow{Day: day, Host: host, Path: norm, CanonicalPath: canon}
		}
		return by[k]
	}
	for _, s := range in.Sessions {
		if s.Day == "" || s.Sessions == 0 {
			continue
		}
		add(s.Day, s.Host, s.Path).OrganicSessions += s.Sessions
	}
	for _, s := range in.Signups {
		if s.Day == "" {
			continue
		}
		a := add(s.Day, s.Host, s.Path)
		a.Signups += s.Signups
		a.SignupPeople += s.SignupPeople
	}

	succeeded := map[payKey]bool{}
	for _, e := range in.Payments {
		if e.Event != "payment_succeeded" {
			continue
		}
		if skip, _ := skipCurrency(e.Currency); skip {
			continue
		}
		succeeded[payKey{e.PersonID, e.Day, cents(e.Amount)}] = true
	}
	countPay := func(e connectors.OrganicPaymentEvent) {
		if skip, other := skipCurrency(e.Currency); skip {
			if other {
				skippedOtherCurrency++
			}
			return
		}
		if e.Amount < 0 {
			return
		}
		a := add(e.Day, e.Host, e.Path)
		a.Payments++
		a.RevenueUSD += math.Round(e.Amount*100) / 100
		if e.PersonID != "" {
			a.people[e.PersonID] = true
		}
	}
	for _, e := range in.Payments {
		if e.Day == "" {
			continue
		}
		switch e.Event {
		case "payment_succeeded":
			countPay(e)
		case "service_purchased":
			if e.Trial || e.Wallet {
				continue
			}
			if succeeded[payKey{e.PersonID, e.Day, cents(e.Amount)}] {
				continue
			}
			countPay(e)
		}
	}

	rows = make([]OrganicDayRow, 0, len(by))
	for k, a := range by {
		r := rowMeta[k]
		r.OrganicSessions = a.OrganicSessions
		r.Signups = a.Signups
		r.SignupPeople = a.SignupPeople
		r.Payments = a.Payments
		r.PaymentPeople = len(a.people)
		r.RevenueUSD = a.RevenueUSD
		rows = append(rows, r)
	}
	return rows, skippedOtherCurrency
}

func cents(v float64) int64 { return int64(math.Round(v * 100)) }

func skipCurrency(cur string) (skip, other bool) {
	c := strings.ToLower(strings.TrimSpace(cur))
	if c == "" || c == "usd" || c == "us$" {
		return false, false
	}
	return true, true
}
