package connectors

import (
	"context"
	"errors"
)

// The conversion feed (plans/paid-ads/plan.md, "Conversion feed"): the
// contracts an ad platform implements so Outflare can make its own
// conversion actions and customer list on an account, send conversions to
// them through the platform's data feed (Google's Data Manager API) rather
// than its older upload call, keep the list of paying customers filled,
// and set the platform's new customer acquisition goal.

// CapAdsFeed marks a connector that implements ConversionFeedManager,
// FeedSender and AudienceFeeder.
const CapAdsFeed = "ads.feed"

// Customer types a conversion carries, when known.
const (
	CustomerNew       = "new"
	CustomerReturning = "returning"
)

// Transports a conversion send takes.
const (
	TransportAdsAPI      = "ads_api"
	TransportDataManager = "data_manager"
)

// FeedRefused says the platform's data feed refused the destination
// itself, not a row: the account or the sign-in cannot use it yet (not
// allowlisted, the conversion action is not valid for it, terms not
// accepted, the sign-in lacks the feed's scope, the API not enabled).
// The sender then falls back to the older upload call when the platform
// still takes it. It wraps the platform's error.
type FeedRefused struct {
	Reason string
	Err    error
}

func (e *FeedRefused) Error() string { return e.Err.Error() }
func (e *FeedRefused) Unwrap() error { return e.Err }

// IsFeedRefused reports whether err is, or wraps, a FeedRefused, and
// returns it.
func IsFeedRefused(err error) (*FeedRefused, bool) {
	var fr *FeedRefused
	if errors.As(err, &fr) {
		return fr, true
	}
	return nil, false
}

// FeedResult is how a feed took one batch: each conversion's outcome, and
// the id the platform gave the request, which its processing status can be
// asked by later.
type FeedResult struct {
	Results   []ConversionResult
	RequestID string
}

// FeedSender sends conversions through the platform's data feed (Google's
// Data Manager API events:ingest). The feed takes a batch whole or not at
// all, so the sender narrows a refused batch down to the bad rows and
// reports each with the platform's reason; an error means the whole batch
// was not taken (a *FeedRefused when the destination itself cannot be
// used).
type FeedSender interface {
	ConversionSender
	SendConversionsFeed(ctx context.Context, c *Client, creds Creds, acct AdAccount, target ConversionTarget, convs []AdConversion) (FeedResult, error)
}

// AudienceFeeder adds many people to a customer list at once, or takes them
// out, through the platform's data feed (Google's Data Manager API
// audienceMembers:ingest and :remove), for the payers list. It returns the
// request id the platform gave.
type AudienceFeeder interface {
	AudienceSyncer
	IngestAudience(ctx context.Context, c *Client, creds Creds, acct AdAccount, audienceID, change string, members []AudienceMember) (string, error)
}

// ConversionActionSpec is a conversion action Outflare makes on an
// account for the feed.
type ConversionActionSpec struct {
	Name string
	// Category is the platform's: signup, purchase or subscribe_paid.
	Category string
	// Primary says the action counts toward bidding; a secondary one is
	// observed only.
	Primary bool
	// OnePerClick counts one conversion per click (a first payment);
	// otherwise every one counts (sign-ups, repeat payments).
	OnePerClick bool
	// ClickWindowDays is how long after a click a conversion still counts
	// for it (1 to 90).
	ClickWindowDays int
}

// Conversion action categories a feed action takes.
const (
	ConversionSignup        = "signup"
	ConversionPurchase      = "purchase"
	ConversionSubscribePaid = "subscribe_paid"
)

// New customer acquisition modes.
const (
	// NCAOff removes the goal from the account's campaigns.
	NCAOff = "off"
	// NCABidHigher bids more for new customers and still serves to
	// everyone (Google's TARGET_ALL).
	NCABidHigher = "bid_higher"
	// NCANewOnly serves to new customers only (Google's TARGET_SPECIFIC).
	NCANewOnly = "new_only"
)

// NewCustomerGoal is the new customer acquisition goal to set on an
// account: the mode, the value added to a new customer's first conversion
// (in micros of the account's currency; 0 for none), and the campaigns it
// applies to.
type NewCustomerGoal struct {
	Mode                  string
	AdditionalValueMicros int64
	CampaignIDs           []string
}

// ConversionFeedManager makes what the feed needs on an account and sets
// its new customer goal. Each call is idempotent: an action or list with
// the same name is found rather than made again.
type ConversionFeedManager interface {
	Connector
	// CreateConversionAction makes (or finds) an action that takes
	// uploaded clicks, and returns the platform's id for it.
	CreateConversionAction(ctx context.Context, c *Client, creds Creds, acct AdAccount, spec ConversionActionSpec) (string, error)
	// CreateCustomerList makes (or finds) a customer list of paying
	// customers, marked as such for the new customer goal, and returns
	// the platform's id for it.
	CreateCustomerList(ctx context.Context, c *Client, creds Creds, acct AdAccount, name string) (string, error)
	// SetNewCustomerGoal sets, changes or removes the account's new
	// customer acquisition goal on the campaigns named.
	SetNewCustomerGoal(ctx context.Context, c *Client, creds Creds, acct AdAccount, goal NewCustomerGoal) error
}

// ReconnectNeeder is a connector whose sign-in may predate a permission a
// feature needs, so the person has to connect it again. ReconnectNeeded
// returns why, or "" when the credentials are fine.
type ReconnectNeeder interface {
	ReconnectNeeded(creds Creds) string
}

// ReconnectField is the setup_needed entry that tells a person to connect
// again; it takes no value.
func ReconnectField(why string) Field {
	return Field{Name: "reconnect", Label: "Connect again", Optional: true, Help: why}
}
