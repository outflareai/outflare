package linkorders

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/fxrates"
)

// InvoiceCurrencies are the currencies a link-order invoice may be in:
// everyday invoice currencies, in the order the forms list them. USD and
// EUR come first because the catalog prices and budgets use them.
var InvoiceCurrencies = []string{
	"USD", "EUR", "GBP", "PLN", "CZK", "UAH", "CHF", "CAD", "AUD", "NZD",
	"JPY", "SEK", "NOK", "DKK", "RON", "HUF", "BGN", "TRY", "INR", "SGD",
	"HKD", "MXN", "BRL", "ZAR",
}

// Where a conversion rate came from. The rate is never typed in: it is
// either 1 (US dollars) or the ECB reference rate Outflare looks up.
const (
	// RateSourceUSD is an invoice already in US dollars: no conversion.
	RateSourceUSD = "usd"
	// RateSourceECB is the European Central Bank reference rate Outflare
	// stores (package fxrates) for the invoice date, or the last working
	// day within fxrates.MaxAge before it.
	RateSourceECB = "ecb"
)

// legacy reports whether the existing quoted_currency / currency columns
// can hold the invoice currency itself.
func legacy(c string) bool { return c == CurrencyUSD || c == CurrencyEUR }

// validInvoiceCurrency reports whether c is one of InvoiceCurrencies.
func validInvoiceCurrency(c string) error {
	for _, v := range InvoiceCurrencies {
		if v == c {
			return nil
		}
	}
	return fmt.Errorf("%w: currency must be one of %s", ErrInvalid, strings.Join(InvoiceCurrencies, ", "))
}

// Invoice is an amount as it is on the invoice and its US-dollar
// equivalent. Recording it never sends money.
type Invoice struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	// InvoiceDate is the date on the invoice (YYYY-MM-DD); the rate is
	// for this day.
	InvoiceDate *string `json:"invoice_date"`
	// USDAmountMinor is the equivalent in US cents. nil only on an order
	// quoted in euros before invoices had a rate of their own.
	USDAmountMinor *int64 `json:"usd_amount_minor"`
	// Rate is US dollars per one unit of Currency.
	Rate *float64 `json:"fx_rate"`
	// RateDate is the day the rate is for: the invoice date, or for the
	// ECB fallback the last working day on or before it.
	RateDate *string `json:"fx_rate_date"`
	// RateSource is usd or ecb.
	RateSource *string `json:"fx_rate_source"`
}

// InvoiceInput is what a quote or Mark Paid may say about the invoice.
// There is no rate: Outflare looks up the ECB reference rate itself.
type InvoiceInput struct {
	// Date is YYYY-MM-DD; today (UTC) when empty.
	Date string
}

// Set reports whether the caller said anything about the invoice.
func (in InvoiceInput) Set() bool { return in.Date != "" }

// minorToUSDCents converts minor units of currency at rate (USD per unit)
// to US cents, rounding half away from zero, without float drift.
func minorToUSDCents(currency string, minor int64, rate float64) int64 {
	r := new(big.Rat).SetFloat64(rate)
	if r == nil {
		return 0
	}
	v := new(big.Rat).Mul(new(big.Rat).SetInt64(minor), r)
	v.Mul(v, big.NewRat(100, 1))
	v.Quo(v, new(big.Rat).SetInt64(pow10(fxrates.MinorDigits(currency))))
	f, _ := v.Float64()
	return int64(math.Round(f))
}

func pow10(n int) int64 {
	p := int64(1)
	for range n {
		p *= 10
	}
	return p
}

// ErrNoRate is returned when there is no ECB reference rate for the
// invoice date or the working days just before it.
var ErrNoRate = errors.New("no ECB reference rate")

// Convert prices amountMinor of currency in US dollars on the invoice
// date. USD needs no rate. Every other currency uses the ECB reference
// rate Outflare stores for that date, or the last working day before it
// within fxrates.MaxAge (weekends and holidays have none). With no such
// rate it returns ErrNoRate (wrapped in ErrInvalid) and nothing is saved.
func Convert(ctx context.Context, q Querier, currency string, amountMinor int64, in InvoiceInput) (*Invoice, error) {
	if amountMinor < 0 {
		return nil, fmt.Errorf("%w: amount_minor must be >= 0", ErrInvalid)
	}
	if err := validInvoiceCurrency(currency); err != nil {
		return nil, err
	}
	day := strings.TrimSpace(in.Date)
	if day == "" {
		day = time.Now().UTC().Format(time.DateOnly)
	}
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return nil, fmt.Errorf("%w: invoice_date must be YYYY-MM-DD", ErrInvalid)
	}
	if t.After(time.Now().UTC().AddDate(0, 0, 1)) {
		return nil, fmt.Errorf("%w: invoice_date cannot be in the future", ErrInvalid)
	}
	inv := &Invoice{Currency: currency, AmountMinor: amountMinor, InvoiceDate: &day}
	set := func(rate float64, rateDay, source string) {
		usd := minorToUSDCents(currency, amountMinor, rate)
		inv.USDAmountMinor, inv.Rate, inv.RateDate, inv.RateSource = &usd, &rate, &rateDay, &source
	}
	switch {
	case currency == CurrencyUSD:
		set(1, day, RateSourceUSD)
	default:
		var rate float64
		var rateDay time.Time
		err := q.QueryRow(ctx, `
			SELECT usd_per_unit::float8, day FROM exchange_rates
			WHERE currency = $1 AND day <= $2::date AND day > $2::date - $3::int
			ORDER BY day DESC LIMIT 1`, currency, day, int(fxrates.MaxAge.Hours()/24)).Scan(&rate, &rateDay)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("%w: %w for %s on %s or in the %d days before it, so the US-dollar equivalent cannot be worked out; try again once the rates are fetched, or check the invoice date",
				ErrInvalid, ErrNoRate, currency, day, int(fxrates.MaxAge.Hours()/24))
		case err != nil:
			return nil, err
		}
		set(rate, rateDay.Format(time.DateOnly), RateSourceECB)
	}
	return inv, nil
}

// legacyAmount is what the existing USD-or-EUR columns hold for inv: the
// amount itself for USD and EUR, otherwise the US-dollar equivalent.
func legacyAmount(inv *Invoice) (int64, string) {
	if legacy(inv.Currency) {
		return inv.AmountMinor, inv.Currency
	}
	return *inv.USDAmountMinor, CurrencyUSD
}

// invoiceColumns is the column/value pairs that store inv under prefix
// ("quoted_" for the quote, "" for the frozen amount).
func invoiceColumns(prefix string, inv *Invoice) [][2]any {
	usdCol := prefix + "usd_minor"
	if prefix == "" {
		usdCol = "usd_amount_minor"
	}
	return [][2]any{
		{prefix + "invoice_currency", inv.Currency},
		{prefix + "invoice_amount_minor", inv.AmountMinor},
		{prefix + "invoice_date", inv.InvoiceDate},
		{usdCol, inv.USDAmountMinor},
		{prefix + "fx_rate", inv.Rate},
		{prefix + "fx_rate_date", inv.RateDate},
		{prefix + "fx_rate_source", inv.RateSource},
	}
}

// invoiceScan holds the nullable invoice columns of one row.
type invoiceScan struct {
	Currency    *string
	AmountMinor *int64
	Date        *time.Time
	USD         *int64
	Rate        *float64
	RateDate    *time.Time
	Source      *string
}

func (s *invoiceScan) dests() []any {
	return []any{&s.Currency, &s.AmountMinor, &s.Date, &s.USD, &s.Rate, &s.RateDate, &s.Source}
}

// view builds the Invoice from the stored columns, or from the existing
// USD-or-EUR columns for a row written before invoices had a currency of
// their own.
func (s *invoiceScan) view(legacyMinor *int64, legacyCurrency *string) *Invoice {
	if s.Currency != nil && s.AmountMinor != nil {
		inv := &Invoice{Currency: *s.Currency, AmountMinor: *s.AmountMinor, USDAmountMinor: s.USD, Rate: s.Rate, RateSource: s.Source}
		if s.Date != nil {
			d := s.Date.Format(time.DateOnly)
			inv.InvoiceDate = &d
		}
		if s.RateDate != nil {
			d := s.RateDate.Format(time.DateOnly)
			inv.RateDate = &d
		}
		return inv
	}
	if legacyMinor == nil || legacyCurrency == nil || *legacyCurrency == "" {
		return nil
	}
	inv := &Invoice{Currency: *legacyCurrency, AmountMinor: *legacyMinor}
	if *legacyCurrency == CurrencyUSD {
		usd, one, src := *legacyMinor, 1.0, RateSourceUSD
		inv.USDAmountMinor, inv.Rate, inv.RateSource = &usd, &one, &src
	}
	return inv
}
