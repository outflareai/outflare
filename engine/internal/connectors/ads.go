package connectors

import (
	"context"
	"math"
	"strings"
)

// Paid media: connectors that read the workspace's own ad accounts (Google
// Ads, Meta, Microsoft Advertising, Reddit, X and LinkedIn): which accounts
// the sign-in can see, their campaigns, and what each campaign spent and
// got per day. Every platform answers in the same shape, with money in
// micros of the account's currency. They only read.

// CategoryAds groups ad platforms.
const CategoryAds = "ads"

// CapAdsRead marks a connector that implements AdsReader.
const CapAdsRead = "ads.read"

// AdAccount is one ad account the credentials can read.
type AdAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
	// TimeZone is the account's reporting time zone (an IANA name when the
	// platform gives one); days are in it.
	TimeZone string `json:"time_zone,omitempty"`
	// Meta is what the connector needs to reach the account again, such as
	// the Google Ads manager account it is reached through.
	Meta map[string]string `json:"meta,omitempty"`
}

// Ad entity statuses, the same for every platform.
const (
	AdActive  = "active"
	AdPaused  = "paused"
	AdRemoved = "removed"
	AdOther   = "other"
)

// AdCampaign is one campaign in an account.
type AdCampaign struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// ProviderStatus is the platform's own word.
	ProviderStatus    string `json:"provider_status"`
	Objective         string `json:"objective,omitempty"`
	DailyBudgetMicros *int64 `json:"daily_budget_micros,omitempty"`
}

// AdDay is one campaign's numbers for one day. Conversions and their value
// are nil when the platform did not report them, and are always what the
// platform claims.
type AdDay struct {
	CampaignID string `json:"campaign_id"`
	// CampaignName is set when the report carries it, so a campaign
	// deleted since still gets a name.
	CampaignName          string   `json:"campaign_name,omitempty"`
	Date                  string   `json:"date"` // YYYY-MM-DD in the account's time zone
	SpendMicros           int64    `json:"spend_micros"`
	Impressions           int64    `json:"impressions"`
	Clicks                int64    `json:"clicks"`
	Conversions           *float64 `json:"conversions,omitempty"`
	ConversionValueMicros *int64   `json:"conversion_value_micros,omitempty"`
}

// AdsReader reads an ad platform. It must not change anything there.
type AdsReader interface {
	Connector
	// AdAccounts lists the ad accounts the credentials can read.
	AdAccounts(ctx context.Context, c *Client, creds Creds) ([]AdAccount, error)
	// AdCampaigns lists an account's campaigns, removed ones included when
	// the platform still returns them.
	AdCampaigns(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]AdCampaign, error)
	// AdDays returns each campaign's numbers for every day from from to to
	// (inclusive dates). Days without spend or impressions may be left out.
	AdDays(ctx context.Context, c *Client, creds Creds, acct AdAccount, from, to string) ([]AdDay, error)
}

// SetupNeeder is a connector that needs more than its sign-in before it
// can be used, such as a Google Ads developer token. SetupFields lists the
// fields it needs; the person fills them in after signing
// in, and they are sealed with the rest of the credentials.
type SetupNeeder interface {
	SetupFields() []Field
}

// MissingSetup returns the setup fields creds lacks.
func MissingSetup(c Connector, creds Creds) []Field {
	n, ok := c.(SetupNeeder)
	if !ok {
		return nil
	}
	var out []Field
	for _, f := range n.SetupFields() {
		if !f.Optional && strings.TrimSpace(creds[f.Name]) == "" {
			out = append(out, f)
		}
	}
	return out
}

// Micros turns an amount in whole currency units (12.34) into micros.
func Micros(v float64) int64 { return int64(math.Round(v * 1e6)) }

// MicrosOf parses an amount a platform sent as a decimal string in whole
// units; empty or unreadable is 0.
func MicrosOf(s string) int64 {
	if p := ParseNum(strings.TrimSpace(s)); p != nil {
		return Micros(*p)
	}
	return 0
}
