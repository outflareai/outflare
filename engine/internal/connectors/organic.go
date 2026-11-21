package connectors

import "context"

// OrganicConversionQuery asks for one PostHog project's first-touch
// Organic Search sessions, signups and payments, day by day.
type OrganicConversionQuery struct {
	Site         string
	From         string
	To           string
	TestAccounts bool
}

// OrganicSessionRow is organic sessions that started on one path on one day.
type OrganicSessionRow struct {
	Day      string
	Host     string
	Path     string
	Sessions int
	Source   string // "sessions" or "events_referrer"
}

// OrganicSignupRow is first-touch organic signups on one path on one day.
type OrganicSignupRow struct {
	Day          string
	Host         string
	Path         string
	Signups      int
	SignupPeople int
	Country      string // the person's first country, ISO alpha-2, or ""
}

// OrganicPaymentEvent is one payment event used to build paid revenue
// without counting the same payment twice.
type OrganicPaymentEvent struct {
	Day      string
	Host     string
	Path     string
	PersonID string
	Event    string // payment_succeeded or service_purchased
	Amount   float64
	Currency string
	Trial    bool
	Wallet   bool
	TxID     string
	Country  string // the person's first country, ISO alpha-2, or ""
}

// OrganicConversionResult is the three PostHog reads for one site and window.
// OrganicCountrySessionRow is organic sessions from one country on one
// host on one day, with their time on the site added up.
type OrganicCountrySessionRow struct {
	Day      string
	Host     string
	Country  string // ISO alpha-2, or ""
	Sessions int
	Seconds  float64
}

type OrganicConversionResult struct {
	Sessions             []OrganicSessionRow
	CountrySessions      []OrganicCountrySessionRow // empty when the tool can't say
	Signups              []OrganicSignupRow
	Payments             []OrganicPaymentEvent
	PaymentSucceededFrom string // YYYY-MM-DD of the earliest payment_succeeded, if any
	SessionsSource       string
	SkippedOtherCurrency int
}

// OrganicConversionReader reads first-touch organic conversions from a
// product analytics tool that has raw events (PostHog).
type OrganicConversionReader interface {
	AnalyticsReader
	OrganicConversions(ctx context.Context, c *Client, creds Creds, q OrganicConversionQuery) (OrganicConversionResult, error)
}
