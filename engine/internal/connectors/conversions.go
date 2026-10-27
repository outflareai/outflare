package connectors

import (
	"context"
	"regexp"
)

// Conversions by landing page: which page each visit started on, and
// whether the visit went on to a conversion event, so the SEO module can
// tell pages that bring buyers from pages that only bring visitors. Only
// product analytics with raw events (PostHog) can answer it.

// PageConversionQuery asks for one site's visits by landing page, day by
// day. Events are the event names that count as a conversion; with none,
// the rows carry visits only.
type PageConversionQuery struct {
	Site   string
	From   string
	To     string
	Events []string
	Limit  int
}

// PageConversionRow is one landing page on one day. A visit counts on the
// day it started. Search visits came from a search engine's results.
type PageConversionRow struct {
	Day               string
	Host              string
	Path              string
	Sessions          int
	Visitors          int
	Conversions       int
	SearchSessions    int
	SearchConversions int
}

// PageConversionReader reads visits and conversions by landing page.
type PageConversionReader interface {
	AnalyticsReader
	PageConversions(ctx context.Context, c *Client, creds Creds, q PageConversionQuery) ([]PageConversionRow, error)
}

// MaxConversionEvents is how many events one site can count as conversions.
const MaxConversionEvents = 20

var eventName = regexp.MustCompile(`^[A-Za-z0-9_$.:/ -]{1,200}$`)

// ValidEventName reports whether name can be a conversion event: letters,
// digits, spaces and _ $ . : / -, up to 200 characters. Names outside it
// are refused rather than escaped, so they are safe to place in a query.
func ValidEventName(name string) bool { return eventName.MatchString(name) }
