package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"strings"
	"time"
)

// Paid media: connectors that read the workspace's own ad accounts (Google
// Ads, Meta, Microsoft Advertising, Reddit, X and LinkedIn): which accounts
// the sign-in can see, their campaigns, and what each campaign spent and
// got per day. Every platform answers in the same shape, with money in
// micros of the account's currency. Reading never changes anything; the
// ones that also implement AdsManager can pause and resume a campaign
// and set its daily budget, and nothing else.

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
	// BiddingStrategy is how the platform bids, lowercase in its own words
	// (Google: target_cpa, maximize_conversions, manual_cpc). Empty when the
	// platform doesn't say; then the stored one is kept.
	BiddingStrategy string `json:"bidding_strategy,omitempty"`
	// The cost per conversion or return on spend the platform bids to,
	// when its strategy has one.
	TargetCPAMicros *int64   `json:"target_cpa_micros,omitempty"`
	TargetROAS      *float64 `json:"target_roas,omitempty"`
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
	AdExtra
}

// AdExtra is what some platforms report beside spend, impressions and
// clicks; nil or empty where the platform didn't say.
type AdExtra struct {
	// Reach is how many people saw it that day (Meta). Days don't add up
	// to people: someone reached on two days counts twice.
	Reach            *int64 `json:"reach,omitempty"`
	LinkClicks       *int64 `json:"link_clicks,omitempty"`
	LandingPageViews *int64 `json:"landing_page_views,omitempty"`
	VideoViews       *int64 `json:"video_views,omitempty"`
	// Actions are the day's conversions by the platform's own action or
	// conversion name.
	Actions []AdAction `json:"actions,omitempty"`
	// AllConversions are every conversion the platform counted, its
	// secondary actions included (Google's all_conversions), and their
	// value.
	AllConversions           *float64 `json:"all_conversions,omitempty"`
	AllConversionValueMicros *int64   `json:"all_conversion_value_micros,omitempty"`
}

// AdAction is one kind of conversion the platform counted.
type AdAction struct {
	Name        string  `json:"name"`
	Conversions float64 `json:"conversions"`
	ValueMicros *int64  `json:"value_micros,omitempty"`
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

// Levels below a campaign: an ad group (Meta's ad set, Google's ad group)
// and an ad.
const (
	AdLevelGroup = "ad_group"
	AdLevelAd    = "ad"
)

// AdItem is an ad group or an ad. ParentID is the platform's id of the
// campaign an ad group is in, or of the ad group an ad is in.
type AdItem struct {
	ID                string `json:"id"`
	ParentID          string `json:"parent_id"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	ProviderStatus    string `json:"provider_status"`
	DailyBudgetMicros *int64 `json:"daily_budget_micros,omitempty"`
	// Details is what the platform says about an ad, by the AdDetail
	// keys; empty for ad groups.
	Details map[string]string `json:"details,omitempty"`
}

// Keys of AdItem.Details.
const (
	AdDetailType      = "type"
	AdDetailHeadline  = "headline"
	AdDetailText      = "text"
	AdDetailURL       = "url"
	AdDetailURLTags   = "url_tags"
	AdDetailThumbnail = "thumbnail_url"
	// AdDetailTemplated is "true" when the ad's links carry Outflare's
	// tracking template (adtrack.HasTemplate), "false" when they don't,
	// and missing when the platform didn't say.
	AdDetailTemplated = "templated"
	// AdDetailTrackingTemplate is a Google ad's tracking template, its own
	// or the one it inherits.
	AdDetailTrackingTemplate = "tracking_template"
)

// AdItemDay is one ad group's or ad's numbers for one day, like AdDay.
type AdItemDay struct {
	ID                    string   `json:"id"`
	ParentID              string   `json:"parent_id"`
	Name                  string   `json:"name,omitempty"`
	Date                  string   `json:"date"`
	SpendMicros           int64    `json:"spend_micros"`
	Impressions           int64    `json:"impressions"`
	Clicks                int64    `json:"clicks"`
	Conversions           *float64 `json:"conversions,omitempty"`
	ConversionValueMicros *int64   `json:"conversion_value_micros,omitempty"`
	AdExtra
}

// AdKeywordDay is one search keyword's numbers for one day (Google).
type AdKeywordDay struct {
	ID        string `json:"id"`
	AdGroupID string `json:"ad_group_id"`
	Text      string `json:"text"`
	// MatchType is exact, phrase or broad.
	MatchType             string   `json:"match_type"`
	Status                string   `json:"status"`
	Date                  string   `json:"date"`
	SpendMicros           int64    `json:"spend_micros"`
	Impressions           int64    `json:"impressions"`
	Clicks                int64    `json:"clicks"`
	Conversions           *float64 `json:"conversions,omitempty"`
	ConversionValueMicros *int64   `json:"conversion_value_micros,omitempty"`
	AdKeywordEstimates
}

// AdKeywordEstimates are what the platform estimates a keyword must bid
// to show on the first page of results, at the top of the page and in the
// first position, in micros, and its quality score (1 to 10); nil where
// it said nothing (Google's position_estimates and quality_info).
type AdKeywordEstimates struct {
	FirstPageCPCMicros     *int64 `json:"first_page_cpc_micros,omitempty"`
	TopOfPageCPCMicros     *int64 `json:"top_of_page_cpc_micros,omitempty"`
	FirstPositionCPCMicros *int64 `json:"first_position_cpc_micros,omitempty"`
	QualityScore           *int   `json:"quality_score,omitempty"`
}

// AdSearchTermDay is what people searched that showed an ad, one day
// (Google), with the keyword it matched.
type AdSearchTermDay struct {
	Term             string `json:"term"`
	AdGroupID        string `json:"ad_group_id"`
	KeywordText      string `json:"keyword_text"`
	KeywordMatchType string `json:"keyword_match_type"`
	// MatchType is how the search matched the keyword, lowercase in the
	// platform's words (Google: exact, near_exact, phrase, near_phrase,
	// broad); empty when it didn't say.
	MatchType             string   `json:"match_type,omitempty"`
	Date                  string   `json:"date"`
	SpendMicros           int64    `json:"spend_micros"`
	Impressions           int64    `json:"impressions"`
	Clicks                int64    `json:"clicks"`
	Conversions           *float64 `json:"conversions,omitempty"`
	ConversionValueMicros *int64   `json:"conversion_value_micros,omitempty"`
}

// AdKeywordsReader is a search ad platform that reads keywords and the
// searches they matched.
type AdKeywordsReader interface {
	AdsReader
	AdKeywordDays(ctx context.Context, c *Client, creds Creds, acct AdAccount, from, to string) ([]AdKeywordDay, error)
	AdSearchTermDays(ctx context.Context, c *Client, creds Creds, acct AdAccount, from, to string) ([]AdSearchTermDay, error)
}

// AdItemsReader is an ad platform that also reads ad groups and ads.
type AdItemsReader interface {
	AdsReader
	// AdItems lists an account's ad groups or ads (level AdLevelGroup or
	// AdLevelAd).
	AdItems(ctx context.Context, c *Client, creds Creds, acct AdAccount, level string) ([]AdItem, error)
	// AdItemDays returns each ad group's or ad's numbers per day, like
	// AdDays.
	AdItemDays(ctx context.Context, c *Client, creds Creds, acct AdAccount, level, from, to string) ([]AdItemDay, error)
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

// CapAdsManage marks a connector that implements AdsManager.
const CapAdsManage = "ads.manage"

// AdsManager changes a campaign on an ad platform: its status (active or
// paused) and its daily budget. Those, and the tracking template on URL
// suffixes (AdTracker), are the only changes Outflare makes to ads; every
// one goes through the action layer for an agent, and a campaign change is
// read back from the platform afterwards (AdCampaign) to confirm it took.
type AdsManager interface {
	Connector
	// AdCampaign reads one campaign as AdCampaigns lists it. ErrNotFound
	// when the account has no such campaign.
	AdCampaign(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string) (AdCampaign, error)
	// SetCampaignStatus pauses a campaign (active false) or resumes it.
	SetCampaignStatus(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string, active bool) error
	// SetCampaignDailyBudget sets a campaign's daily budget, in micros of
	// the account's currency. Platforms that round to a minor unit round.
	SetCampaignDailyBudget(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string, micros int64) error
}

// CapAdsKeywords marks a connector that implements AdKeywordManager.
const CapAdsKeywords = "ads.keywords"

// AdKeywordManager changes a search campaign's keywords (Google): adds a
// keyword to an ad group, adds a negative keyword to a campaign or ad
// group, and gives a keyword its own ad group with copies of the ads of
// the group it came from. It also reads the platform's own
// recommendations for the account.
type AdKeywordManager interface {
	Connector
	// AddKeyword adds text with matchType (exact, phrase or broad) to an
	// ad group, active.
	AddKeyword(ctx context.Context, c *Client, creds Creds, acct AdAccount, adGroupID, text, matchType string) error
	// AddNegativeKeyword stops ads showing for a search: in one ad group
	// when adGroupID is set, else in the whole campaign.
	AddNegativeKeyword(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID, adGroupID, text, matchType string) error
	// SplitKeyword makes an ad group named name in the campaign with the
	// keyword (exact) and copies of fromAdGroupID's active ads, and adds
	// the keyword as an exact negative to fromAdGroupID, so its searches
	// go to the new group. It returns the new group's id.
	SplitKeyword(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID, fromAdGroupID, name, text string) (string, error)
	// Recommendations lists what the platform itself recommends for the
	// account, not yet applied or dismissed.
	Recommendations(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]AdRecommendation, error)
}

// AdRecommendation is one of the platform's own recommendations.
type AdRecommendation struct {
	// ID is the platform's name for it (Google: its resource name).
	ID string `json:"id"`
	// Type is the platform's word for it, such as KEYWORD or CAMPAIGN_BUDGET.
	Type string `json:"type"`
	// CampaignID is the campaign it is for, when it is for one.
	CampaignID string `json:"campaign_id,omitempty"`
	// What it is expected to change: clicks, conversions and cost per week,
	// before and after, when the platform says.
	Impact map[string]float64 `json:"impact,omitempty"`
}

// CapAdsTracking marks a connector that implements AdTracker.
const CapAdsTracking = "ads.tracking"

// Tracking levels, where a platform keeps an ad's URL suffix. The lowest
// level that has one wins, so an ad uses its own, else its ad group's,
// its campaign's, then its account's.
const (
	TrackingAccount  = "account"
	TrackingCampaign = "campaign"
	TrackingAdGroup  = "ad_group"
	TrackingAd       = "ad"
)

// TrackingChange is one URL suffix (or set of URL parameters) that needs
// Outflare's tracking template: where it is, what it says, and what it
// will say with the template added.
type TrackingChange struct {
	Level  string `json:"level"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
	// Error is why the platform refused this one, after ApplyTracking.
	Error string `json:"error,omitempty"`
}

// AdTracker puts Outflare's tracking template on every ad of an account:
// on the account's own URL suffix, and on each campaign, ad group or ad
// suffix that would override it without the template. It changes those
// fields only, and keeps every parameter already in them.
type AdTracker interface {
	Connector
	// TrackingPlan reads the account's URL suffixes and returns the
	// changes that leave every ad with the template (template is the
	// network's adtrack template). Empty when nothing needs changing.
	TrackingPlan(ctx context.Context, c *Client, creds Creds, acct AdAccount, template string) ([]TrackingChange, error)
	// ApplyTracking makes the changes, and returns them with Error set on
	// the ones the platform refused. An error means none were made.
	ApplyTracking(ctx context.Context, c *Client, creds Creds, acct AdAccount, changes []TrackingChange) ([]TrackingChange, error)
}

// CapAdsConversions marks a connector that implements ConversionSender.
const CapAdsConversions = "ads.conversions"

// ConversionTarget is somewhere on the platform a conversion can be sent:
// a Google Ads conversion action, a Meta pixel, a Microsoft Advertising
// offline conversion goal, a Reddit account's Conversions API.
type ConversionTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// EventTypes are the platform's event types a rule picks from (Meta
	// Purchase, Reddit SignUp); empty when the target already is the event.
	EventTypes []string `json:"event_types"`
}

// AdConversion is one conversion to send. Identifiers are already
// normalised and SHA-256 hashed (hex); nothing in it is in plain text but
// the click ids, which the platform issued.
type AdConversion struct {
	// EventID is what the platform dedupes on, shared with the browser
	// pixel when there is one.
	EventID string
	Time    time.Time
	// EventType is the rule's event type, for platforms that need one.
	EventType   string
	ValueMicros *int64
	Currency    string
	// ClickIDs by the tracker's names: gclid, gbraid, wbraid, fbclid, fbc,
	// fbp, msclkid, rdt_cid, twclid, li_fat_id. ClickTime is when the
	// click's visit started (Meta builds fbc from it).
	ClickIDs  map[string]string
	ClickTime time.Time
	// Emails and Phones are hashed with HashID; Phones from E.164 with
	// the plus, PhoneDigits without it (Meta wants digits only).
	Emails      []string
	Phones      []string
	PhoneDigits []string
	// GoogleEmails are the same addresses hashed with HashGoogleEmail,
	// the way Google's enhanced conversions ask.
	GoogleEmails []string
	ExternalID   string
	// ConsentGranted is true when the person granted ad_user_data; false
	// means it was not asked where the law allows sending without it.
	ConsentGranted bool
	// Index is the conversion's place in the batch, echoed in results.
	Index int
	// CustomerType says whether the person is a new customer (their first
	// payment) or a returning one, when known: CustomerNew,
	// CustomerReturning or "" (feed.go). Google bids on it for its new
	// customer acquisition goal.
	CustomerType string
	// Restates, when set, re-sends a conversion the platform already has
	// (the same EventID) with a new value, as after a partial refund;
	// UpdatedAt is when the value changed.
	Restates  bool
	UpdatedAt time.Time
}

// ConversionResult is how the platform took one conversion.
type ConversionResult struct {
	Index int
	OK    bool
	// Skipped means it was never sent: nothing the platform can match on.
	Skipped bool
	Reason  string
	// MatchKeys names the kinds of identifier that were sent.
	MatchKeys []string
}

// ConversionSender sends conversions to an ad platform. It changes nothing
// but the platform's conversion counts.
type ConversionSender interface {
	Connector
	// ConversionTargets lists where an account's conversions can go.
	ConversionTargets(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]ConversionTarget, error)
	// SendConversions sends one batch to one target and says how each
	// conversion went. An error means the whole batch was not taken.
	SendConversions(ctx context.Context, c *Client, creds Creds, acct AdAccount, target ConversionTarget, convs []AdConversion) ([]ConversionResult, error)
}

// AdConversionAdjustment changes a conversion already sent, after a refund:
// a restatement gives it its new value, a retraction takes it away.
type AdConversionAdjustment struct {
	// EventID is the id the conversion was sent with (Google's order id).
	EventID string
	// Retract takes the whole conversion away; otherwise ValueMicros is its
	// new value, in Currency.
	Retract     bool
	ValueMicros int64
	Currency    string
	// Time is when the adjustment happened (the refund); it is after the
	// conversion.
	Time time.Time
	// Index is the adjustment's place in the batch, echoed in results.
	Index int
}

// ConversionAdjuster is a ConversionSender that can also change the value
// of a conversion it took, so a refund lowers what bidding learns from it.
type ConversionAdjuster interface {
	ConversionSender
	// AdjustConversions sends one batch of adjustments to one target and
	// says how each went (Index, OK, Reason). An error means the whole
	// batch was not taken.
	AdjustConversions(ctx context.Context, c *Client, creds Creds, acct AdAccount, target ConversionTarget, adjs []AdConversionAdjustment) ([]ConversionResult, error)
}

// HashID normalises and hashes an identifier the way every platform asks:
// trimmed, lower case, SHA-256, hex.
func HashID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// HashGoogleEmail hashes an email the way Google Ads asks for enhanced
// conversions and Customer Match: like HashID, and for gmail.com and
// googlemail.com addresses without the dots before the @, which Gmail
// ignores.
func HashGoogleEmail(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if local, domain, ok := strings.Cut(v, "@"); ok && (domain == "gmail.com" || domain == "googlemail.com") {
		v = strings.ReplaceAll(local, ".", "") + "@" + domain
	}
	return HashID(v)
}

// CapAdsAudiences marks a connector that implements AudienceSyncer.
const CapAdsAudiences = "ads.audiences"

// AdAudience is a customer list audience on the platform: a Meta custom
// audience from a customer list, a Google Ads Customer Match list.
type AdAudience struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AudienceMember is the person an audience change is about. Every
// identifier is already normalised and hashed with HashID: Phones from
// E.164 with the plus, PhoneDigits without it (Meta wants digits only).
type AudienceMember struct {
	Emails      []string
	Phones      []string
	PhoneDigits []string
	// GoogleEmails are the addresses hashed with HashGoogleEmail.
	GoogleEmails []string
	// ConsentGranted is true when the person granted ad data and ad
	// personalisation; false means it was not asked where the law allows
	// adding them without it.
	ConsentGranted bool
}

// What an audience change does.
const (
	AudienceAdd    = "add"
	AudienceRemove = "remove"
)

// AudienceSyncer adds people to, and takes them out of, the account's
// customer list audiences, for journeys' sync_audience steps. Adding
// someone already in the list, or removing someone who isn't, succeeds,
// so a retry never changes more than once; the platform matches them on
// its side, so it never says whether they matched.
type AudienceSyncer interface {
	Connector
	// Audiences lists the account's customer list audiences.
	Audiences(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]AdAudience, error)
	// SyncAudience adds the person to the audience (AudienceAdd) or takes
	// them out (AudienceRemove).
	SyncAudience(ctx context.Context, c *Client, creds Creds, acct AdAccount, audienceID, change string, m AudienceMember) error
}

// CapAdsTests marks a connector that implements AdTester.
const CapAdsTests = "ads.tests"

// What a test changes in its trial arm.
const (
	// AdTestBudget gives the trial a different daily budget (Meta: a
	// copy of the campaign in a split test).
	AdTestBudget = "budget"
	// AdTestBidding gives the trial a different target cost per
	// conversion (Google: an experiment whose trial campaign bids to it).
	AdTestBidding = "bidding"
)

// AdTestSpec is a test to start on the platform: the campaign it splits,
// how much of its traffic or audience goes to the trial, the days it runs
// (inclusive, in the account's time zone) and what the trial does
// differently.
type AdTestSpec struct {
	Name         string
	CampaignID   string
	Kind         string
	SplitPercent int
	StartDate    string
	EndDate      string
	// The trial's daily budget (AdTestBudget) or target cost per
	// conversion (AdTestBidding), in micros of the account's currency.
	DailyBudgetMicros *int64
	TargetCPAMicros   *int64
}

// AdTestRun is a test the platform has: its own id for it and the
// campaign that serves the trial, once known.
type AdTestRun struct {
	ID              string `json:"id"`
	TrialCampaignID string `json:"trial_campaign_id,omitempty"`
}

// Test arms.
const (
	ArmControl = "control"
	ArmTrial   = "trial"
)

// AdTestDay is one arm's numbers for one day.
type AdTestDay struct {
	Arm         string  `json:"arm"`
	Date        string  `json:"date"`
	SpendMicros int64   `json:"spend_micros"`
	Impressions int64   `json:"impressions"`
	Clicks      int64   `json:"clicks"`
	Conversions float64 `json:"conversions"`
}

// AdTester runs tests inside the platform: the platform itself splits the
// campaign's traffic or audience between the campaign as it is and a
// trial, so the two arms run on the same days and can be compared fairly
// (Google Ads experiments, Meta split tests). Starting, keeping and
// rolling back each change the account; reading the arms changes nothing.
type AdTester interface {
	Connector
	// TestKinds lists what the platform's tests can change (AdTestBudget,
	// AdTestBidding).
	TestKinds() []string
	// StartTest creates the test and schedules it to run.
	StartTest(ctx context.Context, c *Client, creds Creds, acct AdAccount, spec AdTestSpec) (AdTestRun, error)
	// TestDays returns each arm's numbers per day from from to to.
	TestDays(ctx context.Context, c *Client, creds Creds, acct AdAccount, spec AdTestSpec, run AdTestRun, from, to string) ([]AdTestDay, error)
	// EndTest stops the test. keep applies the trial's change to the
	// campaign; otherwise the campaign stays as it was. Either way the
	// trial stops serving.
	EndTest(ctx context.Context, c *Client, creds Creds, acct AdAccount, spec AdTestSpec, run AdTestRun, keep bool) error
}

// CapAdsTargeting marks a connector that implements AdTargetingManager.
const CapAdsTargeting = "ads.targeting"

// Bidding methods a campaign can be switched to. Each may carry a target:
// a cost per conversion for BidMaxConversions, a return on spend for
// BidMaxValue.
const (
	BidMaxConversions = "maximize_conversions"
	BidMaxValue       = "maximize_conversion_value"
	// BidPortfolio puts the campaign on a portfolio strategy
	// (AdBidding.BiddingStrategyID); switching to one of the others takes
	// it off again.
	BidPortfolio = "portfolio"
)

// AdBidding is how a campaign bids: Strategy in the platform's own words
// when read, one of the Bid constants when set.
type AdBidding struct {
	Strategy        string   `json:"strategy"`
	TargetCPAMicros *int64   `json:"target_cpa_micros,omitempty"`
	TargetROAS      *float64 `json:"target_roas,omitempty"`
	// BiddingStrategyID is the portfolio strategy the campaign bids with
	// (BidPortfolio), the platform's id from AdPortfolios; its target is
	// the strategy's own.
	BiddingStrategyID string `json:"bidding_strategy_id,omitempty"`
}

// AdTargeting is who a campaign reaches and how it bids: the customer list
// audiences it targets, the countries it shows in (ISO 3166 alpha-2), and
// notes on what is there but can't be changed here (a city, an ad set that
// differs from the others).
type AdTargeting struct {
	Bidding   AdBidding    `json:"bidding"`
	Audiences []AdAudience `json:"audiences"`
	Countries []string     `json:"countries"`
	Notes     []string     `json:"notes"`
}

// AdTargetingManager changes how a campaign bids and whom it reaches. Each
// call changes one thing; adding what is already there, or removing what
// isn't, succeeds, so a retry changes nothing twice.
type AdTargetingManager interface {
	Connector
	// BiddingMethods lists the methods SetBidding takes; empty when the
	// platform's bidding can't be changed here.
	BiddingMethods() []string
	CampaignTargeting(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string) (AdTargeting, error)
	SetBidding(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string, b AdBidding) error
	// SetCampaignAudience adds a customer list audience to the campaign's
	// targeting (add) or takes it out.
	SetCampaignAudience(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID, audienceID string, add bool) error
	// SetCampaignCountry adds a country to where the campaign shows, or
	// takes it out. Taking out the last one is refused: the campaign would
	// show everywhere.
	SetCampaignCountry(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID, country string, add bool) error
}

// CountryCode checks an ISO 3166 alpha-2 country code and returns it in
// capitals.
func CountryCode(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return "", false
	}
	return s, true
}

// CapAdsCreate marks a connector that implements AdCreator.
const CapAdsCreate = "ads.create"

// What a draft makes on the platform.
const (
	// DraftAd is a new ad in an existing ad group (an ad set on Meta).
	DraftAd = "ad"
	// DraftAdGroup is a new ad group in an existing campaign, with its
	// keywords and one ad.
	DraftAdGroup = "ad_group"
	// DraftCampaign is a new search campaign with its daily budget,
	// bidding, countries and one ad group.
	DraftCampaign = "campaign"
)

// AdDraft is new ads written for a platform: what to make and where. Kind
// says which of the parents must be set (an ad needs AdGroupID, an ad
// group CampaignID, a campaign neither). Everything is made paused.
type AdDraft struct {
	Kind       string `json:"kind"`
	CampaignID string `json:"-"`
	AdGroupID  string `json:"-"`
	// Name of the new ad group or campaign (and of the ad on Meta).
	Name string `json:"name,omitempty"`
	// The ad: where it links, and its text. A search ad has 3 to 15
	// headlines of at most 30 characters, 2 to 4 descriptions of at most 90
	// and two optional display path parts of at most 15. A Meta ad has one
	// headline, one description, its main text (PrimaryText), an image
	// shown with it, a call to action and the Facebook page it runs as
	// (the account's first when empty).
	FinalURL  string   `json:"final_url"`
	Headlines []string `json:"headlines,omitempty"`
	// HeadlinePins pins a search ad's headlines to a position: one entry
	// per headline, 0 for unpinned, 1 to 3 for the position. Empty pins
	// nothing (the evergreen ad). Each pinned position should have three
	// variants; the draft's checks warn when it has fewer.
	HeadlinePins []int    `json:"headline_pins,omitempty"`
	Descriptions []string `json:"descriptions,omitempty"`
	Path1        string   `json:"path1,omitempty"`
	Path2        string   `json:"path2,omitempty"`
	PrimaryText  string   `json:"primary_text,omitempty"`
	ImageURL     string   `json:"image_url,omitempty"`
	CallToAction string   `json:"call_to_action,omitempty"`
	PageID       string   `json:"page_id,omitempty"`
	// A new ad group's or campaign's keywords, each "text" or with its
	// match type (exact, phrase, broad; phrase when empty).
	Keywords []AdDraftKeyword `json:"keywords,omitempty"`
	// A new campaign's daily budget, how it bids, and its countries.
	DailyBudgetMicros *int64     `json:"daily_budget_micros,omitempty"`
	Bidding           *AdBidding `json:"bidding,omitempty"`
	Countries         []string   `json:"countries,omitempty"`
}

// AdDraftKeyword is one keyword of a new ad group.
type AdDraftKeyword struct {
	Text      string `json:"text"`
	MatchType string `json:"match_type,omitempty"`
}

// AdMade is what the platform made for a draft: the campaign, ad group
// and ad the new ad is in, and which of them the draft made (Made, by
// level: campaign, ad_group, ad). GoLive turns on only those.
type AdMade struct {
	CampaignID string   `json:"campaign_id,omitempty"`
	AdGroupID  string   `json:"ad_group_id,omitempty"`
	AdID       string   `json:"ad_id,omitempty"`
	Made       []string `json:"made,omitempty"`
}

// Has reports whether the draft made something at level.
func (m AdMade) Has(level string) bool { return slices.Contains(m.Made, level) }

// AdCreator makes new ads on the platform, paused, and later turns them on.
// Making is all or nothing where the platform allows it (Google's batch
// mutate); otherwise what was made before a failure is returned with the
// error, so it can be found and removed.
type AdCreator interface {
	Connector
	// DraftKinds lists the kinds of draft the platform makes here.
	DraftKinds() []string
	// CheckDraft says what is wrong with a draft before anything is made
	// (ErrInvalid), without calling the platform.
	CheckDraft(d AdDraft) error
	// CreateAds makes the draft's ads paused.
	CreateAds(ctx context.Context, c *Client, creds Creds, acct AdAccount, d AdDraft) (AdMade, error)
	// GoLive turns on everything a draft made: the ad, and the ad group
	// and campaign when the draft made them.
	GoLive(ctx context.Context, c *Client, creds Creds, acct AdAccount, made AdMade) error
}

// ---- Paid ads intelligence layer: click harvest, change events, auto-apply ----
// (plans/paid-ads/plan.md, sync and alerts). Read by adsync; Google
// implements them (connectors/ads/google_clicks.go, google_changes.go).

// AdDetailPolicy is the platform's policy verdict on an ad, lowercased
// (Google: approved, approved_limited, disapproved, area_of_interest_only,
// under_review). Empty when the platform said nothing.
const AdDetailPolicy = "policy_status"

// AdClick is one paid click the network knows, with the ids it had: what
// a landing page's click id resolves to.
type AdClick struct {
	ClickID string `json:"click_id"`
	// Kind is the click id's parameter name on the landing page (gclid).
	Kind       string `json:"kind"`
	CampaignID string `json:"campaign_id"`
	AdGroupID  string `json:"ad_group_id,omitempty"`
	AdID       string `json:"ad_id,omitempty"`
	Keyword    string `json:"keyword,omitempty"`
	MatchType  string `json:"match_type,omitempty"`
	Device     string `json:"device,omitempty"`
}

// AdClickReader lists the clicks an account got on one day (Google's
// click_view: one day per query, kept 90 days).
type AdClickReader interface {
	Connector
	// AdClicks lists the account's clicks on day (YYYY-MM-DD, the
	// account's time zone).
	AdClicks(ctx context.Context, c *Client, creds Creds, acct AdAccount, day string) ([]AdClick, error)
}

// AdChangeEvent is one change made on the platform, by whoever made it.
type AdChangeEvent struct {
	// ID is the network's name for the event (Google: its resource name).
	ID string    `json:"id"`
	At time.Time `json:"at"`
	// UserHash is the SHA-256 (hex) of the email of the user who made it,
	// or "" when the platform named none. The address itself is never
	// kept.
	UserHash string `json:"user_hash,omitempty"`
	// ClientType is where it was made, in the platform's words (Google:
	// GOOGLE_ADS_WEB_CLIENT, GOOGLE_ADS_API, GOOGLE_ADS_AUTOMATED_RULE,
	// GOOGLE_ADS_RECOMMENDATIONS_SUBSCRIPTION, ...).
	ClientType string `json:"client_type"`
	// ResourceType is what changed (CAMPAIGN, AD_GROUP, CAMPAIGN_BUDGET,
	// ...) and Operation how (CREATE, UPDATE, REMOVE).
	ResourceType  string   `json:"resource_type"`
	Operation     string   `json:"operation"`
	ChangedFields []string `json:"changed_fields,omitempty"`
	// CampaignID is the campaign it touched, when it was under one.
	CampaignID string `json:"campaign_id,omitempty"`
	// Old and New are the resource before and after, as the platform
	// returned them (only the changed fields are set on an update).
	Old map[string]any `json:"old,omitempty"`
	New map[string]any `json:"new,omitempty"`
}

// AdChangeReader lists what changed on an account since a time (Google's
// change_event: the last 30 days, 10,000 rows a query).
type AdChangeReader interface {
	Connector
	AdChangeEvents(ctx context.Context, c *Client, creds Creds, acct AdAccount, since time.Time) ([]AdChangeEvent, error)
}

// AdAutoApplyReader says which of the platform's own recommendation types
// the account lets the platform apply on its own (Google's
// recommendation_subscription).
type AdAutoApplyReader interface {
	Connector
	// AutoApplied lists the recommendation types the account applies on
	// its own, in the platform's words.
	AutoApplied(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]string, error)
}

// HashEmail is the SHA-256 (hex) of an email address, lowercased and
// trimmed: how a user a platform names is kept without the address.
func HashEmail(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	return HashID(v)
}

// ---- Paid ads intelligence layer, verdicts (plans/paid-ads/plan.md):
// the levers the findings queue applies and reverts beyond the campaign
// calls above, and the per-segment numbers its exclusion rules read.
// Each is a small interface a network implements when it can; the rules
// check the network's capabilities (paidads.Caps) before they fire, and
// a change on a network without the interface ends as connector_invalid.
// ----

// AdKeywordEditor undoes a negative keyword and pauses or resumes a
// keyword (Google).
type AdKeywordEditor interface {
	Connector
	// RemoveNegativeKeyword takes a negative keyword out of the campaign
	// (adGroupID empty) or the ad group it was added to. One that isn't
	// there succeeds, so a retry changes nothing twice.
	RemoveNegativeKeyword(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID, adGroupID, text, matchType string) error
	// SetKeywordStatus pauses a keyword (active false) or resumes it, by
	// the platform's ids of its ad group and the keyword (ad_keywords).
	SetKeywordStatus(ctx context.Context, c *Client, creds Creds, acct AdAccount, adGroupID, keywordID string, active bool) error
}

// AdStatusManager pauses or resumes one ad.
type AdStatusManager interface {
	Connector
	// SetAdStatus pauses an ad (active false) or resumes it, by the
	// platform's ids of its ad group and the ad (ad_entities at level ad).
	SetAdStatus(ctx context.Context, c *Client, creds Creds, acct AdAccount, adGroupID, adID string, active bool) error
}

// AdTarget is a campaign's bidding target: a cost per conversion (micros)
// or a return on ad spend (a multiple, 3.5 for 350%), whichever its
// strategy takes. Both nil removes the target and leaves the strategy.
type AdTarget struct {
	TargetCPAMicros *int64   `json:"target_cpa_micros,omitempty"`
	TargetROAS      *float64 `json:"target_roas,omitempty"`
}

// AdTargetManager moves a campaign's target without changing how it
// bids: target CPA and Maximise conversions take a cost per conversion,
// target ROAS and Maximise conversion value a return on spend.
type AdTargetManager interface {
	Connector
	SetCampaignTarget(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string, t AdTarget) error
}

// Exclusion kinds (AdExclusion.Kind).
const (
	// ExcludeDevice: a device the campaign stops showing on (desktop,
	// mobile, tablet). Google: a device criterion with bid modifier 0.
	ExcludeDevice = "device"
	// ExcludeLocation: a place the campaign stops showing in. Google: a
	// negative location criterion on a geo target constant.
	ExcludeLocation = "location"
	// ExcludePlacement: a site, app or YouTube channel or video Display
	// ads stop showing on. Google: a negative placement criterion.
	ExcludePlacement = "placement"
	// ExcludeNetwork: search_partners or display (Display expansion) off.
	// Google: network_settings.
	ExcludeNetwork = "network"
	// ExcludeSchedule: hours of every day the campaign stops showing in,
	// as "HH-HH" in the account's time zone (end exclusive). Google: ad
	// schedule criteria covering the rest of the day.
	ExcludeSchedule = "schedule"
	// ExcludeBrand: searches for a brand the campaign stops showing on
	// (Performance Max). Google: a brand list shared set excluded.
	ExcludeBrand = "brand"
)

// AdExclusion is one thing a campaign stops showing on, as the rules
// propose it and the network applies it. Value is the network's own id
// where it has one (a geo target constant, a placement URL or app id) and
// a word otherwise (mobile, search_partners, 00-06); Detail narrows a
// placement (website, mobile_application, youtube_channel, youtube_video)
// and Label is what a person reads.
type AdExclusion struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Label  string `json:"label,omitempty"`
}

// AdExclusionManager adds an exclusion to a campaign and takes it out
// again. Adding one already there, or removing one that isn't, succeeds.
type AdExclusionManager interface {
	Connector
	// ExclusionKinds lists the Exclude kinds the network takes.
	ExclusionKinds() []string
	AddExclusion(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string, x AdExclusion) error
	RemoveExclusion(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID string, x AdExclusion) error
}

// AdSegment is a campaign's numbers over a range of days for one slice of
// its traffic: a device, a network, a place, a placement or an hour of the
// day. Value and Detail are as AdExclusion takes them, so a segment that
// wastes money becomes the exclusion that stops it.
type AdSegment struct {
	Kind        string  `json:"kind"`
	Value       string  `json:"value"`
	Detail      string  `json:"detail,omitempty"`
	Label       string  `json:"label,omitempty"`
	Impressions int64   `json:"impressions"`
	Clicks      int64   `json:"clicks"`
	SpendMicros int64   `json:"spend_micros"`
	Conversions float64 `json:"conversions"`
}

// AdCampaignSignals is what a campaign's own report says about where it
// is held back and where its money goes: the share of eligible
// impressions lost to its budget and to its rank (0 to 1, averaged over
// the days read; Search only, zero where the network doesn't say), which
// networks it is on, and its segments over the same days.
type AdCampaignSignals struct {
	BudgetLostImpressionShare float64     `json:"budget_lost_impression_share"`
	RankLostImpressionShare   float64     `json:"rank_lost_impression_share"`
	SearchPartners            bool        `json:"search_partners"`
	DisplayExpansion          bool        `json:"display_expansion"`
	Segments                  []AdSegment `json:"segments"`
}

// AdSignalsReader reads AdCampaignSignals for one campaign over from..to
// (dates, inclusive).
type AdSignalsReader interface {
	Connector
	AdCampaignSignals(ctx context.Context, c *Client, creds Creds, acct AdAccount, campaignID, from, to string) (AdCampaignSignals, error)
}

// ---- Required Minimum Functionality (docs/paid-media.md): what Google
// asks a full-service tool to offer beside the changes above. Each is a
// small interface; Google implements them (connectors/ads/google_rmf.go)
// and the API answers connector_invalid for a network without one. ----

// Website conversion categories a person picks from, in Outflare's words.
const (
	ConvCategoryPurchase = "purchase"
	ConvCategorySignup   = "sign_up"
	ConvCategoryLead     = "lead"
	ConvCategoryPageView = "page_view"
	ConvCategoryContact  = "contact"
	ConvCategoryOther    = "other"
)

// ConvCategories lists them, for the API's checks.
var ConvCategories = []string{ConvCategoryPurchase, ConvCategorySignup, ConvCategoryLead, ConvCategoryPageView, ConvCategoryContact, ConvCategoryOther}

// AdTagSnippet is the code a website runs to count a conversion: the
// platform's global site tag (on every page) and the event snippet (on the
// page the conversion happens on), for one kind of page.
type AdTagSnippet struct {
	// Type is how the event fires, lowercase (Google: webpage when the page
	// loads, webpage_onclick on a click).
	Type string `json:"type"`
	// PageFormat is html or amp.
	PageFormat    string `json:"page_format"`
	GlobalSiteTag string `json:"global_site_tag"`
	EventSnippet  string `json:"event_snippet"`
}

// AdConversionAction is one conversion action on the account, of any
// type, as the platform keeps it.
type AdConversionAction struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type, Category and Status are lowercase in the platform's words
	// (Google: webpage, upload_clicks; purchase, signup; enabled, hidden).
	Type     string `json:"type"`
	Category string `json:"category"`
	Status   string `json:"status"`
	// Primary is whether bidding optimises for it (Google's
	// primary_for_goal); the others only report, in all conversions.
	Primary bool `json:"primary"`
	// Counting is one (one per click) or many (every one).
	Counting        string `json:"counting"`
	ClickWindowDays int    `json:"click_window_days"`
	// The value each conversion gets when the website sends none, in
	// micros of DefaultCurrency, and whether it always gets it.
	DefaultValueMicros *int64 `json:"default_value_micros"`
	DefaultCurrency    string `json:"default_currency"`
	AlwaysUseDefault   bool   `json:"always_use_default"`
	// Snippets are the website code, for website conversions.
	Snippets []AdTagSnippet `json:"snippets"`
}

// AdWebConversion is a website conversion action to create.
type AdWebConversion struct {
	Name string `json:"name"`
	// Category is one of ConvCategories.
	Category string `json:"category"`
	// Counting is one or many.
	Counting string `json:"counting"`
	// ClickWindowDays is how long after a click a conversion still counts,
	// 1 to 90.
	ClickWindowDays int `json:"click_window_days"`
	// DefaultValueMicros is the value a conversion gets when the page sends
	// none (nil for no value), in Currency; AlwaysUseDefault ignores what
	// the page sends.
	DefaultValueMicros *int64 `json:"default_value_micros,omitempty"`
	Currency           string `json:"currency,omitempty"`
	AlwaysUseDefault   bool   `json:"always_use_default,omitempty"`
}

// AdConversionManager lists an account's conversion actions with their
// website code, and creates a website conversion action (RMF C.65).
type AdConversionManager interface {
	Connector
	AdConversionActions(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]AdConversionAction, error)
	// CreateWebConversion creates the action, enabled, and returns its id.
	CreateWebConversion(ctx context.Context, c *Client, creds Creds, acct AdAccount, w AdWebConversion) (string, error)
}

// CalloutMax is the most characters a callout has (Google's limit).
const CalloutMax = 25

// AdCallout is a short line of text shown under an ad (Google's callout
// asset), linked to the whole account.
type AdCallout struct {
	// ID is the asset's id on the platform.
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

// AdCalloutManager lists, adds and removes an account's own callouts
// (RMF C.75). Removing takes the callout off the account; the asset stays
// in the platform's library, as in Google Ads itself.
type AdCalloutManager interface {
	Connector
	AdCallouts(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]AdCallout, error)
	// AddCallout creates the callout and links it to the account, and
	// returns its id.
	AddCallout(ctx context.Context, c *Client, creds Creds, acct AdAccount, text string) (string, error)
	// RemoveCallout unlinks it; one already gone succeeds.
	RemoveCallout(ctx context.Context, c *Client, creds Creds, acct AdAccount, id string) error
}

// Portfolio strategy types a person creates here.
const (
	PortfolioTargetCPA  = "target_cpa"
	PortfolioTargetROAS = "target_roas"
)

// AdPortfolio is a portfolio bidding strategy: one target several
// campaigns share. Type and Status are lowercase in the platform's words.
type AdPortfolio struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	Status          string   `json:"status"`
	TargetCPAMicros *int64   `json:"target_cpa_micros"`
	TargetROAS      *float64 `json:"target_roas"`
	CampaignCount   int      `json:"campaign_count"`
	Currency        string   `json:"currency"`
}

// AdPortfolioSpec is a strategy to create (Type and its target) or a
// change to one (a new target, a new name, or both).
type AdPortfolioSpec struct {
	Name            string   `json:"name,omitempty"`
	Type            string   `json:"type,omitempty"`
	TargetCPAMicros *int64   `json:"target_cpa_micros,omitempty"`
	TargetROAS      *float64 `json:"target_roas,omitempty"`
}

// AdPortfolioReport is one strategy's numbers over a range (RMF R.130).
type AdPortfolioReport struct {
	AdPortfolio
	SpendMicros       int64   `json:"spend_micros"`
	Impressions       int64   `json:"impressions"`
	Clicks            int64   `json:"clicks"`
	CPCMicros         *int64  `json:"cpc_micros"`
	Conversions       float64 `json:"conversions"`
	AllConversions    float64 `json:"all_conversions"`
	CostPerConvMicros *int64  `json:"cost_per_conversion_micros"`
}

// AdPortfolioManager lists, creates and changes an account's portfolio
// bidding strategies and reports on them (RMF C.96, C.97, M.96, M.97,
// R.130). A campaign is put on one through AdTargetingManager.SetBidding
// with BidPortfolio.
type AdPortfolioManager interface {
	Connector
	AdPortfolios(ctx context.Context, c *Client, creds Creds, acct AdAccount) ([]AdPortfolio, error)
	// CreatePortfolio makes the strategy and returns its id.
	CreatePortfolio(ctx context.Context, c *Client, creds Creds, acct AdAccount, p AdPortfolioSpec) (string, error)
	// UpdatePortfolio moves its target or renames it.
	UpdatePortfolio(ctx context.Context, c *Client, creds Creds, acct AdAccount, id string, p AdPortfolioSpec) error
	// AdPortfolioReport reads every strategy's numbers from from to to.
	AdPortfolioReport(ctx context.Context, c *Client, creds Creds, acct AdAccount, from, to string) ([]AdPortfolioReport, error)
}

// What AdRemover removes, and reads back.
const (
	RemoveCampaign = "campaign"
	RemoveAd       = "ad"
	RemoveKeyword  = "keyword"
)

// AdRemover removes a campaign, an ad or a keyword (RMF M.110, M.130,
// M.140). Removing can't be undone on the platform: only a person asks
// for it, never an agent.
type AdRemover interface {
	Connector
	// Remove removes one: what is RemoveCampaign, RemoveAd or
	// RemoveKeyword, id the platform's id, and adGroupID the ad group an
	// ad or a keyword is in. One already removed succeeds.
	Remove(ctx context.Context, c *Client, creds Creds, acct AdAccount, what, adGroupID, id string) error
	// RemovedStatus reads it back: its status (AdRemoved once removed).
	RemovedStatus(ctx context.Context, c *Client, creds Creds, acct AdAccount, what, adGroupID, id string) (string, error)
}

// CheckWebConversion says what is wrong with a website conversion before
// anything is sent (ErrInvalid).
func CheckWebConversion(w AdWebConversion) error {
	name := strings.TrimSpace(w.Name)
	switch {
	case name == "" || len(name) > 100:
		return Errf(ErrInvalid, "a conversion action's name is 1 to 100 characters")
	case !slices.Contains(ConvCategories, w.Category):
		return Errf(ErrInvalid, "category is purchase, sign_up, lead, page_view, contact or other")
	case w.Counting != "one" && w.Counting != "many":
		return Errf(ErrInvalid, "counting is one (one conversion per click) or many (every one)")
	case w.ClickWindowDays < 1 || w.ClickWindowDays > 90:
		return Errf(ErrInvalid, "the click window is 1 to 90 days")
	case w.DefaultValueMicros != nil && *w.DefaultValueMicros < 0:
		return Errf(ErrInvalid, "the default value can't be below nothing")
	case w.DefaultValueMicros != nil && len(strings.TrimSpace(w.Currency)) != 3:
		return Errf(ErrInvalid, "a default value needs its currency, a three-letter code")
	case w.AlwaysUseDefault && w.DefaultValueMicros == nil:
		return Errf(ErrInvalid, "always using the default value needs a default value")
	}
	return nil
}

// CheckCallout trims a callout and checks Google's limit.
func CheckCallout(text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if n := len([]rune(text)); n == 0 || n > CalloutMax {
		return "", Errf(ErrInvalid, "a callout is 1 to %d characters", CalloutMax)
	}
	return text, nil
}

// CheckPortfolio says what is wrong with a strategy to create, or with a
// change to one (create false), before anything is sent.
func CheckPortfolio(p AdPortfolioSpec, create bool) error {
	name := strings.TrimSpace(p.Name)
	if len(name) > 255 || (create && name == "") {
		return Errf(ErrInvalid, "a strategy's name is 1 to 255 characters")
	}
	if p.TargetCPAMicros != nil && p.TargetROAS != nil {
		return Errf(ErrInvalid, "a strategy has a target cost per conversion or a target return on spend, not both")
	}
	if p.TargetCPAMicros != nil && *p.TargetCPAMicros <= 0 || p.TargetROAS != nil && *p.TargetROAS <= 0 {
		return Errf(ErrInvalid, "the target must be more than nothing")
	}
	if !create {
		if name == "" && p.TargetCPAMicros == nil && p.TargetROAS == nil {
			return Errf(ErrInvalid, "say what changes: a new target or a new name")
		}
		return nil
	}
	switch p.Type {
	case PortfolioTargetCPA:
		if p.TargetCPAMicros == nil {
			return Errf(ErrInvalid, "a target_cpa strategy needs its target cost per conversion")
		}
	case PortfolioTargetROAS:
		if p.TargetROAS == nil {
			return Errf(ErrInvalid, "a target_roas strategy needs its target return on spend")
		}
	default:
		return Errf(ErrInvalid, "a strategy here is target_cpa or target_roas")
	}
	return nil
}
