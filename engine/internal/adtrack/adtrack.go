// Package adtrack is the catalog first-party ad tracking reads: for each ad
// network, the click id it adds to landing pages, the macros its ads fill
// in, and the tracking template a workspace pastes into it so every paid
// click arrives with the network's own campaign, ad set and ad ids.
//
// site.js captures what is listed here (its lists are checked against these
// in tests), the site events endpoint keeps only these keys, visits store
// them, and attribution reads which click ids prove a paid click. Adding a
// network means adding it here; its connector, cost pulls and send-back
// live with paid media (engine/internal/connectors/ads, engine/internal/adconv).
package adtrack

import (
	"regexp"
	"strings"
)

// ClickID is a parameter a network adds to the landing page of a click.
type ClickID struct {
	// Param is the parameter as it appears in the URL (case matters).
	Param string
	// Network is the Networks entry, or another source such as an
	// affiliate network.
	Network string
	// Source is the utm_source-style name analytics shows for a visit
	// that has it.
	Source string
	// Paid reports whether the id proves a paid click. fbclid does not:
	// Facebook adds it to every outbound link, paid or not.
	Paid bool
	// Channel is the channel a paid click is credited to.
	Channel string
}

// ClickIDs are the click ids site.js reads from landing pages, in the order
// attribution checks them.
var ClickIDs = []ClickID{
	{"gclid", "google", "google", true, "paid_ads"},
	{"gbraid", "google", "google", true, "paid_ads"},
	{"wbraid", "google", "google", true, "paid_ads"},
	// Display & Video 360 and Campaign Manager 360.
	{"dclid", "google", "google", true, "paid_ads"},
	{"fbclid", "meta", "facebook", false, "paid_ads"},
	{"msclkid", "microsoft", "microsoft", true, "paid_ads"},
	{"li_fat_id", "linkedin", "linkedin", true, "paid_ads"},
	{"rdt_cid", "reddit", "reddit", true, "paid_ads"},
	{"twclid", "x", "x", true, "paid_ads"},
	{"ttclid", "tiktok", "tiktok", true, "paid_ads"},
	{"ScCid", "snapchat", "snapchat", true, "paid_ads"},
	{"epik", "pinterest", "pinterest", true, "paid_ads"},
	{"tblci", "taboola", "taboola", true, "paid_ads"},
	{"ob_click_id", "outbrain", "outbrain", true, "paid_ads"},
	{"qclid", "quora", "quora", true, "paid_ads"},
	// Impact's partner click: an affiliate, not an ad.
	{"irclickid", "impact", "impact", true, "affiliates"},
}

// Params lists every click id parameter.
func Params() []string {
	out := make([]string, len(ClickIDs))
	for i, c := range ClickIDs {
		out[i] = c.Param
	}
	return out
}

// ClickIDOf returns the click id with param.
func ClickIDOf(param string) (ClickID, bool) {
	for _, c := range ClickIDs {
		if c.Param == param {
			return c, true
		}
	}
	return ClickID{}, false
}

// Cookie is a first-party cookie a network's own pixel sets, which its
// conversions API takes to match a conversion to the click. site.js reads
// them only with consent, and sends them with the click ids under Key.
// They identify the browser, not how it arrived, so they never start a
// visit.
type Cookie struct {
	Name, Key, Network string
}

// Cookies are the network cookies site.js reads.
var Cookies = []Cookie{
	{"_fbp", "fbp", "meta"},
	{"_fbc", "fbc", "meta"},
	{"_gcl_aw", "gcl_aw", "google"},
	{"_ttp", "ttp", "tiktok"},
	{"_scid", "scid", "snapchat"},
}

// IsCookie reports whether key is a Cookies key.
func IsCookie(key string) bool {
	for _, c := range Cookies {
		if c.Key == key {
			return true
		}
	}
	return false
}

// AdParams are the parameters the tracking template adds, by the key a
// visit stores them under (its ad object).
var AdParams = []struct{ Param, Key string }{
	{"of_campaign", "campaign"},
	{"of_adset", "adset"},
	{"of_ad", "ad"},
	{"of_placement", "placement"},
}

// unfilled matches a macro the network left as it was: {campaignid},
// {{ad.id}}, __CID__, [section_id], or LinkedIn's CAMPAIGN_ID typed in
// as text instead of chosen as a dynamic value.
var unfilled = regexp.MustCompile(`\{[^}]*\}|^__[A-Z0-9_]+__$|^\[[a-z_]+\]$|^(ACCOUNT|CAMPAIGN_GROUP|CAMPAIGN|CREATIVE)_(ID|NAME)$`)

// CleanAd keeps the known ad keys with filled-in values, each up to 200
// characters, and reports how many were left unfilled by the network.
func CleanAd(in map[string]any) (map[string]string, int) {
	out, skipped := map[string]string{}, 0
	for _, p := range AdParams {
		v, _ := in[p.Key].(string)
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if unfilled.MatchString(v) {
			skipped++
			continue
		}
		if len(v) > 200 {
			v = v[:200]
		}
		out[p.Key] = v
	}
	return out, skipped
}

// Macros are a network's placeholders for its own ids and names, filled in
// on each click. Empty means the network has none for it.
type Macros struct {
	CampaignID   string `json:"campaign_id,omitempty"`
	CampaignName string `json:"campaign_name,omitempty"`
	AdSetID      string `json:"adset_id,omitempty"`
	AdID         string `json:"ad_id,omitempty"`
	AdName       string `json:"ad_name,omitempty"`
	Placement    string `json:"placement,omitempty"`
	Keyword      string `json:"keyword,omitempty"`
	// ClickID is the macro for the network's click id, for networks that
	// add it only when the template asks.
	ClickID string `json:"click_id,omitempty"`
}

// Network is one ad network.
type Network struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Connector is the paid media connector that reads its accounts, empty
	// until there is one.
	Connector string `json:"connector,omitempty"`
	// Field is where the template goes, in the network's own words. Empty
	// when ads on it don't link to a website.
	Field  string `json:"field,omitempty"`
	Macros Macros `json:"macros"`
	// Note is anything a person setting it up needs to know.
	Note string `json:"note,omitempty"`
}

// Networks are the ad networks, the six paid media reads first.
var Networks = []Network{
	{ID: "google", Label: "Google Ads", Connector: "google_ads", Field: "Final URL suffix (account, campaign or ad group)",
		Macros: Macros{CampaignID: "{campaignid}", AdSetID: "{adgroupid}", AdID: "{creative}", Placement: "{network}", Keyword: "{keyword}"},
		Note:   "Keep auto-tagging on: Google adds gclid, gbraid or wbraid itself."},
	{ID: "meta", Label: "Meta Ads", Connector: "meta_ads", Field: "URL parameters (ad)",
		Macros: Macros{CampaignID: "{{campaign.id}}", CampaignName: "{{campaign.name}}", AdSetID: "{{adset.id}}", AdID: "{{ad.id}}", AdName: "{{ad.name}}", Placement: "{{placement}}"},
		Note:   "Meta adds fbclid itself. With consent, site.js also reads the Meta pixel's _fbp and _fbc cookies."},
	// Microsoft's help ("What tracking or URL parameters can I use?") and
	// its URL tracking guide give {CampaignId}, {Campaign}, {AdGroupId} and
	// {AdId}. {Network} is from Adobe's Microsoft docs only; the unfilled
	// macros alert shows it if Microsoft doesn't fill it in.
	{ID: "microsoft", Label: "Microsoft Advertising", Connector: "microsoft_ads", Field: "Final URL suffix (account, campaign or ad group)",
		Macros: Macros{CampaignID: "{CampaignId}", CampaignName: "{Campaign}", AdSetID: "{AdGroupId}", AdID: "{AdId}", Placement: "{Network}", Keyword: "{keyword}"},
		Note:   "Keep auto-tagging of the click id on: Microsoft adds msclkid itself."},
	// LinkedIn's dynamic UTM tracking (Microsoft Learn, LinkedIn Marketing
	// API) sets each parameter on the campaign to a dynamic value named
	// CAMPAIGN_ID, CAMPAIGN_NAME, CREATIVE_ID and so on, without braces.
	{ID: "linkedin", Label: "LinkedIn Ads", Connector: "linkedin_ads", Field: "URL parameters (campaign): add each parameter with the dynamic value named after its =",
		Macros: Macros{CampaignID: "CAMPAIGN_ID", CampaignName: "CAMPAIGN_NAME", AdID: "CREATIVE_ID"},
		Note:   "Choose CAMPAIGN_ID, CAMPAIGN_NAME and CREATIVE_ID as dynamic values rather than typing them. LinkedIn adds li_fat_id when the Insight Tag's enhanced conversion tracking is on."},
	// Reddit's help lists these macros for app install measurement; whether
	// a website ad's URL fills them isn't confirmed in its docs. The
	// unfilled macros alert shows it if not.
	{ID: "reddit", Label: "Reddit Ads", Connector: "reddit_ads", Field: "URL parameters (ad)",
		Macros: Macros{CampaignID: "{{CAMPAIGN_ID}}", CampaignName: "{{CAMPAIGN_NAME}}", AdSetID: "{{ADGROUP_ID}}", AdID: "{{AD_ID}}", AdName: "{{AD_NAME}}"},
		Note:   "Reddit adds rdt_cid itself. It doesn't let you change an ad's URL parameters once the ad is live."},
	{ID: "x", Label: "X Ads", Connector: "x_ads", Field: "Website URL (ad)",
		Note: "X adds twclid itself and has no macros for its ids, so add a campaign name to each ad's link."},
	{ID: "tiktok", Label: "TikTok Ads", Connector: "tiktok_ads", Field: "URL parameters (ad, under tracking)",
		Macros: Macros{CampaignID: "__CAMPAIGN_ID__", CampaignName: "__CAMPAIGN_NAME__", AdSetID: "__AID__", AdID: "__CID__", AdName: "__CID_NAME__", Placement: "__PLACEMENT__"},
		Note:   "TikTok adds ttclid itself."},
	{ID: "snapchat", Label: "Snapchat Ads", Connector: "snapchat_ads", Field: "URL parameters (ad)",
		Macros: Macros{CampaignID: "{{campaign.id}}", CampaignName: "{{campaign.name}}", AdSetID: "{{adSet.id}}", AdID: "{{ad.id}}"},
		Note:   "Snapchat adds ScCid itself."},
	{ID: "pinterest", Label: "Pinterest Ads", Connector: "pinterest_ads", Field: "Destination URL (ad)",
		Macros: Macros{CampaignID: "{campaignid}", AdSetID: "{adgroupid}", AdID: "{adid}", Keyword: "{keyword}"},
		Note:   "Pinterest adds epik itself."},
	// Taboola's "Creating and Adding URL Parameters for Tracking" lists the
	// macros; its S2S guide says it appends tblci={click_id} to every ad
	// URL itself, so the template doesn't repeat it.
	{ID: "taboola", Label: "Taboola", Connector: "taboola_ads", Field: "Tracking code (campaign)",
		Macros: Macros{CampaignID: "{campaign_id}", CampaignName: "{campaign_name}", AdID: "{campaign_item_id}", Placement: "{site_id}"},
		Note:   "Paste it without a leading ? or &: Taboola adds one, and adds tblci itself. Replace the default utm_source=taboola&utm_medium=referral."},
	// Outbrain's "How do I add UTM tracking to my campaign?" lists every
	// macro usable in the tracking code, ob_click_id included.
	{ID: "outbrain", Label: "Outbrain", Connector: "outbrain_ads", Field: "Tracking (campaign)",
		Macros: Macros{CampaignID: "{{campaign_id}}", CampaignName: "{{campaign_name}}", AdID: "{{ad_id}}", AdName: "{{ad_name}}", Placement: "{{section_id}}", ClickID: "{{ob_click_id}}"}},
	// Quora's "UTM Parameters" article lists its six dynamic variables.
	{ID: "quora", Label: "Quora Ads", Field: "Landing page url (ad), after /?",
		Macros: Macros{CampaignID: "{{campaign.id}}", CampaignName: "{{campaign.name}}", AdSetID: "{{adset.id}}", AdID: "{{ad.id}}", AdName: "{{ad.name}}"},
		Note:   "Save the ad before checking the preview: {{ad.id}} and {{ad.name}} fill in only once it exists. Quora's click id (qclid) is kept on its side, so it may not reach your page."},
	{ID: "apple_search_ads", Label: "Apple Ads", Connector: "apple_ads",
		Note: "Apple Ads (formerly Apple Search Ads) run in the App Store and send people to your app, not your website. Installs are measured in the app, through Apple's AdServices framework."},
	{ID: "amazon_ads", Label: "Amazon Ads", Connector: "amazon_ads", Field: "Click-through URL (ad)",
		Note: "Most Amazon ads send people to your Amazon listing. For ads that link to your own site, Amazon has no macros for its ids, so add a campaign name to each ad's link."},
}

// NetworkOf returns the network with id.
func NetworkOf(id string) (Network, bool) {
	for _, n := range Networks {
		if n.ID == id {
			return n, true
		}
	}
	return Network{}, false
}

// ClickParam is the network's click id parameter that its template has to
// ask for, if any.
func (n Network) ClickParam() string {
	if n.Macros.ClickID == "" {
		return ""
	}
	for _, c := range ClickIDs {
		if c.Network == n.ID {
			return c.Param
		}
	}
	return ""
}

// Template is the query string to paste into the network's Field: the
// usual UTM tags, the network's own ids in Outflare's of_ parameters, and
// its click id where it adds one only when asked. Macros stay as they are,
// unescaped, since the network fills them in. Empty for a network whose
// ads don't link to a website.
func (n Network) Template() string {
	if n.Field == "" {
		return ""
	}
	m := n.Macros
	parts := []string{"utm_source=" + n.ID, "utm_medium=cpc"}
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("utm_campaign", m.CampaignName)
	add("utm_term", m.Keyword)
	add("utm_content", m.AdName)
	add("of_campaign", m.CampaignID)
	add("of_adset", m.AdSetID)
	add("of_ad", m.AdID)
	add("of_placement", m.Placement)
	if p := n.ClickParam(); p != "" {
		add(p, m.ClickID)
	}
	return strings.Join(parts, "&")
}

// Arrival outcomes for a paid landing page, from best to worst: a click id
// (the network's own proof of the click), only the template's ad ids, or
// neither, which means the ad's link has no tracking template and the
// visit can be credited only by its UTM tags.
const (
	ArrivalClickID = "click_id"
	ArrivalAdIDs   = "ad_ids"
	ArrivalNone    = "none"
)

// Arrival says how well a paid landing page was tracked, and on which
// network (a Networks id, or "other"). ids are the page's click ids, ad
// how many of the template's ad ids it kept, and source its cleaned
// utm_source.
func Arrival(ids map[string]any, ad int, source string) (network, outcome string) {
	network = "other"
	if _, ok := NetworkOf(source); ok {
		network = source
	}
	for _, c := range ClickIDs {
		if network == "other" && c.Source == source {
			if _, ok := NetworkOf(c.Network); ok {
				network = c.Network
			}
		}
	}
	for _, c := range ClickIDs {
		if v, _ := ids[c.Param].(string); v != "" {
			if _, ok := NetworkOf(c.Network); ok {
				network = c.Network
			}
			return network, ArrivalClickID
		}
	}
	if ad > 0 {
		return network, ArrivalAdIDs
	}
	return network, ArrivalNone
}
