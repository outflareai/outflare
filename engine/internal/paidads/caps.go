package paidads

// Capabilities is what a network can do, as the rules need to know it:
// which levers exist there, how long it learns after a change, how many
// conversions its bidding needs, and the shape of its reporting. A rule
// consults them before it fires; a finding whose lever a network cannot
// apply is never generated there. Google's values come from the research
// in plans/paid-ads/README.md; every other network starts with
// conservative defaults (every Supports* false except what its connector
// already implements) and is filled in when its adapter is built.
type Capabilities struct {
	Network                     string
	HasKeywords                 bool     // search terms, negatives, match types
	NegativeLevels              []string // campaign, ad_group, shared_list, account
	BidAdjustmentsHonoured      bool     // false on Google under Smart Bidding (device -100% only)
	SupportsScheduleWindows     bool
	SupportsGeoPresenceSetting  bool
	SupportsOfflineConversions  bool
	SupportsCustomerTypeFlag    bool // NEW / RETURNING on uploads
	SupportsConversionValue     bool
	SupportsNewCustomerGoal     bool
	SupportsCustomerListExclude bool
	SupportsExperiments         bool
	SupportsCampaignCreate      bool
	// SupportsTargets: the network takes a target cost per conversion or
	// return on ad spend on a campaign (tCPA, tROAS).
	SupportsTargets bool
	// Manageable: Outflare can pause, resume and set budgets there.
	Manageable                bool
	LearningPeriodDays        int            // Google: 7
	MinConversionsForTarget   int            // Google floor 15; practitioner 30
	MinConversionsForROAS     int            // 15 floor; 150 reliable
	CampaignTypesWithOwnRules []string       // search, performance_max, shopping, demand_gen
	TargetDirectionByType     map[string]int // +1 volume rises when target loosens; PMax -1 on tROAS cut
	ReportFreshness           map[string]string
	ClickIDFields             []string
	ClickLookbackDays         int     // 90
	DailyOverspendMultiple    float64 // 2.0
	MonthlySpendMultiple      float64 // 30.4
}

// manageable are the networks Outflare can pause, resume and set budgets
// on (the same list adsuggest and the campaign calls use).
var manageable = map[string]bool{"google_ads": true, "meta_ads": true, "microsoft_ads": true, "linkedin_ads": true, "tiktok_ads": true, "reddit_ads": true, "x_ads": true}

// google is what the Google Ads adapter can do and how Google behaves,
// from the research tables (plans/paid-ads/README.md and research/).
var google = Capabilities{
	Network:                     "google_ads",
	HasKeywords:                 true,
	NegativeLevels:              []string{"campaign", "ad_group", "shared_list", "account"},
	BidAdjustmentsHonoured:      false,
	SupportsScheduleWindows:     true,
	SupportsGeoPresenceSetting:  true,
	SupportsOfflineConversions:  true,
	SupportsCustomerTypeFlag:    true,
	SupportsConversionValue:     true,
	SupportsNewCustomerGoal:     true,
	SupportsCustomerListExclude: true,
	SupportsExperiments:         true,
	SupportsCampaignCreate:      true,
	SupportsTargets:             true,
	Manageable:                  true,
	LearningPeriodDays:          7,
	MinConversionsForTarget:     15,
	MinConversionsForROAS:       15,
	CampaignTypesWithOwnRules:   []string{"search", "performance_max", "shopping", "demand_gen"},
	// Loosening a target raises volume on Search and Shopping; on PMax a
	// tROAS cut can lower it (Google reallocates toward conversion value).
	TargetDirectionByType: map[string]int{"search": 1, "shopping": 1, "demand_gen": 1, "performance_max": -1},
	// Google reports conversions by click date and restates them for up
	// to 90 days; clicks and spend settle within about 3 hours; click_view
	// rows are kept 90 days; change_event 30 days.
	ReportFreshness:        map[string]string{"spend": "3h", "clicks": "3h", "conversions": "90d", "click_view": "90d", "change_event": "30d"},
	ClickIDFields:          []string{"gclid", "gbraid", "wbraid"},
	ClickLookbackDays:      90,
	DailyOverspendMultiple: 2.0,
	MonthlySpendMultiple:   30.4,
}

// Caps are a connector's capabilities: Google's as researched, every
// other network conservative defaults with only what its connector already
// implements (pausing, resuming and budgets where it is manageable).
func Caps(connector string) Capabilities {
	if connector == google.Network {
		c := google
		return c
	}
	c := Capabilities{Network: connector, Manageable: manageable[connector], LearningPeriodDays: 7, MinConversionsForTarget: 30, MinConversionsForROAS: 150,
		ClickLookbackDays: 90, DailyOverspendMultiple: 2.0, MonthlySpendMultiple: 30.4, ReportFreshness: map[string]string{}, TargetDirectionByType: map[string]int{}}
	switch connector {
	case "meta_ads":
		c.ClickIDFields = []string{"fbclid", "fbc"}
		c.SupportsConversionValue = true
		c.SupportsExperiments = true
		c.SupportsCampaignCreate = false // new ads only; ad sets and campaigns are made in Ads Manager
	case "microsoft_ads":
		c.ClickIDFields = []string{"msclkid"}
		c.HasKeywords = true // read through the keywords reader where the connector has it
		c.SupportsConversionValue = true
	case "linkedin_ads":
		c.ClickIDFields = []string{"li_fat_id"}
	case "tiktok_ads":
		c.ClickIDFields = []string{"ttclid"}
		c.SupportsConversionValue = true
	case "reddit_ads":
		c.ClickIDFields = []string{"rdt_cid"}
		c.SupportsOfflineConversions = true // Conversions API, with value
		c.SupportsConversionValue = true
	case "snapchat_ads":
		c.ClickIDFields = []string{"ScCid"}
	case "pinterest_ads":
		c.ClickIDFields = []string{"epik"}
	case "taboola_ads":
		c.ClickIDFields = []string{"tblci"}
	case "outbrain_ads":
		c.ClickIDFields = []string{"ob_click_id"}
	case "x_ads":
		c.ClickIDFields = []string{"twclid"}
		c.SupportsOfflineConversions = true // Conversions API, with value
		c.SupportsConversionValue = true
	}
	return c
}

// Allows reports whether a network can apply a lever: its required
// capability flag is set.
func (c Capabilities) Allows(kind string) bool {
	l, ok := LeverOf(kind)
	if !ok {
		return false
	}
	switch l.Requires {
	case "", "all":
		return true
	case "manageable":
		return c.Manageable
	case "HasKeywords":
		return c.HasKeywords
	case "SupportsTargets":
		return c.SupportsTargets
	case "SupportsCampaignCreate":
		return c.SupportsCampaignCreate
	case "SupportsExperiments":
		return c.SupportsExperiments
	case "exclusions":
		return c.Manageable && (c.SupportsScheduleWindows || c.SupportsGeoPresenceSetting || c.HasKeywords || c.SupportsCustomerListExclude)
	}
	return false
}
