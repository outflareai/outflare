package ads

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// GoogleAds reads Google Ads accounts through the Google Ads API with the
// workspace's own Google sign-in. Google retired developer tokens on
// 9 September 2026: API access now belongs to the Google Cloud project of
// the sign-in's OAuth app, and Google ignores a token sent anyway. A token
// a workspace added before then is still sent, and none is asked for.
type GoogleAds struct{}

const (
	googleAdsBase    = "https://googleads.googleapis.com"
	googleAdsVersion = "v23"
	scopeAdWords     = "https://www.googleapis.com/auth/adwords"
)

func (GoogleAds) Definition() c.Definition {
	d := def("google_ads", "Google Ads", "https://ads.google.com",
		"https://developers.google.com/google-ads/api/docs/start", "https://developers.google.com/google-ads/api/docs/get-started/dev-token",
		"Spend, clicks and conversions per campaign from your Google Ads accounts.",
		"Signs in with Google and reads every Google Ads account the sign-in can see, including accounts under a manager account. It changes a campaign only when you, or an agent within the grants you set, pause or resume it or set its daily budget, and adds Outflare's tracking parameters to URL suffixes only when you press Add the template; it sends conversions only to the conversion actions you choose under Paid ads, and adds people only to the Customer Match lists your journeys name.")
	d.Capabilities = append(d.Capabilities, c.CapAdsConversions, c.CapAdsAudiences, c.CapAdsManage, c.CapAdsTracking)
	d.OAuth = &c.OAuth{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scopes:   []string{scopeAdWords, "openid", "email"},
		Params:   map[string]string{"access_type": "offline", "prompt": "consent"},
	}
	return d
}

func (GoogleAds) SetupFields() []c.Field {
	return []c.Field{{Name: "developer_token", Label: "Developer token (no longer needed)", Secret: true, Optional: true,
		Help: "Google stopped requiring developer tokens in September 2026. Leave this empty."}}
}

func (g GoogleAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	acct := c.Account{Details: map[string]string{}}
	ids, err := g.accessible(ctx, cl, creds)
	if err != nil {
		return acct, err
	}
	acct.Details["accounts"] = fmt.Sprint(len(ids))
	return acct, nil
}

func (GoogleAds) base(cl *c.Client) string {
	return cl.Base("google_ads", googleAdsBase) + "/" + googleAdsVersion
}

func (GoogleAds) call(cl *c.Client, creds c.Creds, loginID string) *call {
	return newCall(cl, "Google Ads").bearer(creds["access_token"]).
		set("developer-token", creds["developer_token"]).set("login-customer-id", loginID)
}

func (g GoogleAds) accessible(ctx context.Context, cl *c.Client, creds c.Creds) ([]string, error) {
	var resp struct {
		ResourceNames []string `json:"resourceNames"`
	}
	if err := g.call(cl, creds, "").get(ctx, g.base(cl)+"/customers:listAccessibleCustomers", &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.ResourceNames))
	for _, n := range resp.ResourceNames {
		out = append(out, strings.TrimPrefix(n, "customers/"))
	}
	return out, nil
}

// googleTracking is where a Google Ads resource keeps its tracking: an
// ad, ad group, campaign or account each may have its own, and the most
// specific one that is set applies.
type googleTracking struct {
	FinalURLSuffix      string `json:"finalUrlSuffix"`
	TrackingURLTemplate string `json:"trackingUrlTemplate"`
}

type gaqlRow struct {
	Customer struct {
		ID              flexInt `json:"id"`
		DescriptiveName string  `json:"descriptiveName"`
		CurrencyCode    string  `json:"currencyCode"`
		TimeZone        string  `json:"timeZone"`
		Manager         bool    `json:"manager"`
		googleTracking
	} `json:"customer"`
	CustomerClient struct {
		ID              flexInt `json:"id"`
		DescriptiveName string  `json:"descriptiveName"`
		CurrencyCode    string  `json:"currencyCode"`
		TimeZone        string  `json:"timeZone"`
		Manager         bool    `json:"manager"`
		Status          string  `json:"status"`
	} `json:"customerClient"`
	Campaign struct {
		ID                     flexInt `json:"id"`
		Name                   string  `json:"name"`
		Status                 string  `json:"status"`
		AdvertisingChannelType string  `json:"advertisingChannelType"`
		CampaignBudget         string  `json:"campaignBudget"`
		// How Google bids, and the target it bids to when it has one.
		BiddingStrategyType string `json:"biddingStrategyType"`
		TargetCpa           struct {
			TargetCpaMicros flexInt `json:"targetCpaMicros"`
		} `json:"targetCpa"`
		MaximizeConversions struct {
			TargetCpaMicros flexInt `json:"targetCpaMicros"`
		} `json:"maximizeConversions"`
		TargetRoas struct {
			TargetRoas float64 `json:"targetRoas"`
		} `json:"targetRoas"`
		MaximizeConversionValue struct {
			TargetRoas float64 `json:"targetRoas"`
		} `json:"maximizeConversionValue"`
		googleTracking
	} `json:"campaign"`
	ConversionAction struct {
		ID   flexInt `json:"id"`
		Name string  `json:"name"`
	} `json:"conversionAction"`
	AdGroup struct {
		ID     flexInt `json:"id"`
		Name   string  `json:"name"`
		Status string  `json:"status"`
		googleTracking
	} `json:"adGroup"`
	AdGroupAd struct {
		Status string `json:"status"`
		Ad     struct {
			ID        flexInt  `json:"id"`
			Name      string   `json:"name"`
			Type      string   `json:"type"`
			FinalURLs []string `json:"finalUrls"`
			googleTracking
			ResponsiveSearchAd struct {
				Headlines    []struct{ Text string } `json:"headlines"`
				Descriptions []struct{ Text string } `json:"descriptions"`
			} `json:"responsiveSearchAd"`
			ResponsiveDisplayAd struct {
				Headlines    []struct{ Text string } `json:"headlines"`
				Descriptions []struct{ Text string } `json:"descriptions"`
			} `json:"responsiveDisplayAd"`
		} `json:"ad"`
	} `json:"adGroupAd"`
	UserList struct {
		ID   flexInt `json:"id"`
		Name string  `json:"name"`
	} `json:"userList"`
	CampaignBudget struct {
		AmountMicros flexInt `json:"amountMicros"`
	} `json:"campaignBudget"`
	AdGroupCriterion struct {
		CriterionID flexInt `json:"criterionId"`
		Status      string  `json:"status"`
		Keyword     struct {
			Text      string `json:"text"`
			MatchType string `json:"matchType"`
		} `json:"keyword"`
	} `json:"adGroupCriterion"`
	SearchTermView struct {
		SearchTerm string `json:"searchTerm"`
	} `json:"searchTermView"`
	Segments struct {
		Date                 string `json:"date"`
		ConversionActionName string `json:"conversionActionName"`
		Keyword              struct {
			Info struct {
				Text      string `json:"text"`
				MatchType string `json:"matchType"`
			} `json:"info"`
		} `json:"keyword"`
	} `json:"segments"`
	Metrics struct {
		CostMicros       flexInt `json:"costMicros"`
		Impressions      flexInt `json:"impressions"`
		Clicks           flexInt `json:"clicks"`
		Conversions      flexNum `json:"conversions"`
		ConversionsValue flexNum `json:"conversionsValue"`
	} `json:"metrics"`
}

// search runs a GAQL query against one customer, every page.
func (g GoogleAds) search(ctx context.Context, cl *c.Client, creds c.Creds, customer, loginID, query string) ([]gaqlRow, error) {
	var out []gaqlRow
	token := ""
	for i := 0; i < maxPages; i++ {
		body := map[string]any{"query": query}
		if token != "" {
			body["pageToken"] = token
		}
		var resp struct {
			Results       []gaqlRow `json:"results"`
			NextPageToken string    `json:"nextPageToken"`
		}
		if err := g.call(cl, creds, loginID).post(ctx, g.base(cl)+"/customers/"+esc(customer)+"/googleAds:search", body, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Results...)
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	return out, nil
}

func (g GoogleAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	ids, err := g.accessible(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []c.AdAccount
	for _, id := range ids {
		rows, err := g.search(ctx, cl, creds, id, "", "SELECT customer.id, customer.descriptive_name, customer.currency_code, customer.time_zone, customer.manager FROM customer")
		if err != nil {
			// A suspended or cancelled account refuses queries; the others
			// still sync.
			if c.KindOf(err) == c.ErrInvalid || c.KindOf(err) == c.ErrAuth {
				continue
			}
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		cu := rows[0].Customer
		if !cu.Manager {
			if !seen[id] {
				seen[id] = true
				out = append(out, c.AdAccount{ID: id, Name: cu.DescriptiveName, Currency: cu.CurrencyCode, TimeZone: cu.TimeZone})
			}
			continue
		}
		// A manager account: read the accounts under it through it.
		clients, err := g.search(ctx, cl, creds, id, id, "SELECT customer_client.id, customer_client.descriptive_name, customer_client.currency_code, customer_client.time_zone, customer_client.manager, customer_client.status FROM customer_client WHERE customer_client.manager = FALSE")
		if err != nil {
			return nil, err
		}
		for _, r := range clients {
			cc := r.CustomerClient
			cid := fmt.Sprint(int64(cc.ID))
			if seen[cid] || cc.Status == "CANCELED" || cc.Status == "CLOSED" {
				continue
			}
			seen[cid] = true
			out = append(out, c.AdAccount{ID: cid, Name: cc.DescriptiveName, Currency: cc.CurrencyCode, TimeZone: cc.TimeZone,
				Meta: map[string]string{"login_customer_id": id}})
		}
	}
	return out, nil
}

func (g GoogleAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT campaign.id, campaign.name, campaign.status, campaign.advertising_channel_type, campaign_budget.amount_micros, "+
			"campaign.bidding_strategy_type, campaign.target_cpa.target_cpa_micros, campaign.maximize_conversions.target_cpa_micros, "+
			"campaign.target_roas.target_roas, campaign.maximize_conversion_value.target_roas FROM campaign")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdCampaign, 0, len(rows))
	for _, r := range rows {
		cp := c.AdCampaign{ID: fmt.Sprint(int64(r.Campaign.ID)), Name: r.Campaign.Name, Status: status(r.Campaign.Status),
			ProviderStatus: r.Campaign.Status, Objective: strings.ToLower(r.Campaign.AdvertisingChannelType)}
		if b := int64(r.CampaignBudget.AmountMicros); b > 0 {
			cp.DailyBudgetMicros = ptrI(b)
		}
		googleBidding(&cp, r)
		out = append(out, cp)
	}
	return out, nil
}

// googleBidding reads a campaign's bid strategy and the cost per
// conversion or return it bids to. A portfolio strategy keeps its target on
// the strategy itself, so only its type is known here.
func googleBidding(cp *c.AdCampaign, r gaqlRow) {
	cp.BiddingStrategy = strings.ToLower(r.Campaign.BiddingStrategyType)
	if cp.BiddingStrategy == "" {
		cp.BiddingStrategy = "unknown"
	}
	for _, v := range []flexInt{r.Campaign.TargetCpa.TargetCpaMicros, r.Campaign.MaximizeConversions.TargetCpaMicros} {
		if v > 0 {
			cp.TargetCPAMicros = ptrI(int64(v))
			break
		}
	}
	for _, v := range []float64{r.Campaign.TargetRoas.TargetRoas, r.Campaign.MaximizeConversionValue.TargetRoas} {
		if v > 0 {
			cp.TargetROAS = &v
			break
		}
	}
}

// ---- managing campaigns ----

// googleCampaign reads one campaign with its budget's resource name, which
// a budget change needs.
func (g GoogleAds) googleCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, string, error) {
	if _, err := strconv.ParseInt(campaignID, 10, 64); err != nil {
		return c.AdCampaign{}, "", c.Errf(c.ErrInvalid, "Google Ads campaign ids are numbers")
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT campaign.id, campaign.name, campaign.status, campaign.advertising_channel_type, campaign.campaign_budget, campaign_budget.amount_micros FROM campaign WHERE campaign.id = "+campaignID)
	if err != nil {
		return c.AdCampaign{}, "", err
	}
	for _, r := range rows {
		if fmt.Sprint(int64(r.Campaign.ID)) != campaignID {
			continue
		}
		cp := c.AdCampaign{ID: campaignID, Name: r.Campaign.Name, Status: status(r.Campaign.Status),
			ProviderStatus: r.Campaign.Status, Objective: strings.ToLower(r.Campaign.AdvertisingChannelType)}
		if b := int64(r.CampaignBudget.AmountMicros); b > 0 {
			cp.DailyBudgetMicros = ptrI(b)
		}
		return cp, r.Campaign.CampaignBudget, nil
	}
	return c.AdCampaign{}, "", c.Errf(c.ErrInvalid, "Google Ads has no campaign %s in account %s", campaignID, acct.ID)
}

func (g GoogleAds) AdCampaign(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string) (c.AdCampaign, error) {
	cp, _, err := g.googleCampaign(ctx, cl, creds, acct, campaignID)
	return cp, err
}

// SetCampaignStatus pauses or resumes a campaign (campaigns:mutate with
// an update of its status alone).
func (g GoogleAds) SetCampaignStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, active bool) error {
	if _, err := strconv.ParseInt(campaignID, 10, 64); err != nil {
		return c.Errf(c.ErrInvalid, "Google Ads campaign ids are numbers")
	}
	st := "PAUSED"
	if active {
		st = "ENABLED"
	}
	op := map[string]any{"update": map[string]any{"resourceName": "customers/" + acct.ID + "/campaigns/" + campaignID, "status": st}, "updateMask": "status"}
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/campaigns:mutate",
		map[string]any{"operations": []any{op}}, nil)
}

// SetCampaignDailyBudget sets the campaign's budget amount
// (campaignBudgets:mutate on the budget the campaign uses). A budget
// shared by several campaigns changes for all of them, as it does in
// Google Ads itself.
func (g GoogleAds) SetCampaignDailyBudget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, micros int64) error {
	if micros <= 0 {
		return c.Errf(c.ErrInvalid, "the daily budget must be more than nothing")
	}
	_, budget, err := g.googleCampaign(ctx, cl, creds, acct, campaignID)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(budget, "customers/"+acct.ID+"/campaignBudgets/") {
		return c.Errf(c.ErrInvalid, "Google Ads campaign %s has no budget of its own to change", campaignID)
	}
	op := map[string]any{"update": map[string]any{"resourceName": budget, "amountMicros": strconv.FormatInt(micros, 10)}, "updateMask": "amount_micros"}
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/campaignBudgets:mutate",
		map[string]any{"operations": []any{op}}, nil)
}

func (g GoogleAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	q := fmt.Sprintf("SELECT campaign.id, campaign.name, segments.date, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions, metrics.conversions_value FROM campaign WHERE segments.date BETWEEN '%s' AND '%s'",
		mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdDay, 0, len(rows))
	for _, r := range rows {
		m := r.Metrics
		out = append(out, c.AdDay{CampaignID: fmt.Sprint(int64(r.Campaign.ID)), CampaignName: r.Campaign.Name, Date: r.Segments.Date,
			SpendMicros: int64(m.CostMicros), Impressions: int64(m.Impressions), Clicks: int64(m.Clicks),
			Conversions: ptrF(float64(m.Conversions)), ConversionValueMicros: ptrI(c.Micros(float64(m.ConversionsValue)))})
	}
	acts, err := g.actions(ctx, cl, creds, acct, "campaign", "campaign.id", from, to)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Actions = acts[out[i].CampaignID+"|"+out[i].Date]
	}
	return out, nil
}

// actions reads conversions by conversion action per day for every row of
// resource, keyed by its id (idField) and the day. Google reports them
// apart from cost: the conversion action segment can't sit beside it.
func (g GoogleAds) actions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, resource, idField, from, to string) (map[string][]c.AdAction, error) {
	q := fmt.Sprintf("SELECT %s, segments.date, segments.conversion_action_name, metrics.conversions, metrics.conversions_value FROM %s WHERE segments.date BETWEEN '%s' AND '%s' AND metrics.conversions > 0",
		idField, resource, mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := map[string][]c.AdAction{}
	for _, r := range rows {
		var id int64
		switch idField {
		case "campaign.id":
			id = int64(r.Campaign.ID)
		case "ad_group.id":
			id = int64(r.AdGroup.ID)
		default:
			id = int64(r.AdGroupAd.Ad.ID)
		}
		key := fmt.Sprint(id) + "|" + r.Segments.Date
		out[key] = append(out[key], c.AdAction{Name: firstText(r.Segments.ConversionActionName, "conversion"),
			Conversions: float64(r.Metrics.Conversions), ValueMicros: ptrI(c.Micros(float64(r.Metrics.ConversionsValue)))})
	}
	return out, nil
}

// googleItems are the GAQL resources for each level below a campaign: the
// resource, its id and name fields, and its parent's id field. An ad's id
// is the ad's own ({creative} in a tracking template), not the ad group
// and ad pair.
var googleItems = map[string]struct{ from, id, name, parent string }{
	c.AdLevelGroup: {"ad_group", "ad_group.id", "ad_group.name", "campaign.id"},
	c.AdLevelAd:    {"ad_group_ad", "ad_group_ad.ad.id", "ad_group_ad.ad.name", "ad_group.id"},
}

// AdItems lists ad groups or ads, leaving out removed ones (their
// numbers still sync while a report has them).
func (g GoogleAds) AdItems(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level string) ([]c.AdItem, error) {
	var q string
	switch level {
	case c.AdLevelGroup:
		q = "SELECT ad_group.id, ad_group.name, ad_group.status, campaign.id FROM ad_group WHERE ad_group.status != 'REMOVED'"
	case c.AdLevelAd:
		q = "SELECT ad_group_ad.ad.id, ad_group_ad.ad.name, ad_group_ad.status, ad_group.id, ad_group_ad.ad.type, ad_group_ad.ad.final_urls, " +
			"ad_group_ad.ad.final_url_suffix, ad_group_ad.ad.tracking_url_template, ad_group.final_url_suffix, ad_group.tracking_url_template, " +
			"campaign.final_url_suffix, campaign.tracking_url_template, customer.final_url_suffix, customer.tracking_url_template, " +
			"ad_group_ad.ad.responsive_search_ad.headlines, ad_group_ad.ad.responsive_search_ad.descriptions, " +
			"ad_group_ad.ad.responsive_display_ad.headlines, ad_group_ad.ad.responsive_display_ad.descriptions FROM ad_group_ad WHERE ad_group_ad.status != 'REMOVED'"
	default:
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdItem, 0, len(rows))
	for _, r := range rows {
		if level == c.AdLevelGroup {
			out = append(out, c.AdItem{ID: fmt.Sprint(int64(r.AdGroup.ID)), ParentID: fmt.Sprint(int64(r.Campaign.ID)), Name: r.AdGroup.Name,
				Status: status(r.AdGroup.Status), ProviderStatus: r.AdGroup.Status})
			continue
		}
		ad := r.AdGroupAd.Ad
		text := func(lists ...[]struct{ Text string }) string {
			for _, l := range lists {
				if len(l) > 0 {
					return l[0].Text
				}
			}
			return ""
		}
		link := ""
		if len(ad.FinalURLs) > 0 {
			link = ad.FinalURLs[0]
		}
		headline := text(ad.ResponsiveSearchAd.Headlines, ad.ResponsiveDisplayAd.Headlines)
		suffix := firstText(ad.FinalURLSuffix, r.AdGroup.FinalURLSuffix, r.Campaign.FinalURLSuffix, r.Customer.FinalURLSuffix)
		template := firstText(ad.TrackingURLTemplate, r.AdGroup.TrackingURLTemplate, r.Campaign.TrackingURLTemplate, r.Customer.TrackingURLTemplate)
		it := c.AdItem{ID: fmt.Sprint(int64(ad.ID)), ParentID: fmt.Sprint(int64(r.AdGroup.ID)), Name: firstText(ad.Name, headline),
			Status: status(r.AdGroupAd.Status), ProviderStatus: r.AdGroupAd.Status,
			Details: adDetails(map[string]string{
				c.AdDetailType:             strings.ToLower(ad.Type),
				c.AdDetailHeadline:         headline,
				c.AdDetailText:             text(ad.ResponsiveSearchAd.Descriptions, ad.ResponsiveDisplayAd.Descriptions),
				c.AdDetailURL:              link,
				c.AdDetailURLTags:          suffix,
				c.AdDetailTrackingTemplate: template,
			})}
		// Ads without a link of their own (Performance Max assets, app
		// ads) say nothing either way.
		if link != "" {
			it.Details[c.AdDetailTemplated] = strconv.FormatBool(adtrack.HasTemplate(suffix, template, link))
		}
		out = append(out, it)
	}
	return out, nil
}

// AdItemDays reads each ad group's or ad's numbers per day.
func (g GoogleAds) AdItemDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, level, from, to string) ([]c.AdItemDay, error) {
	it, ok := googleItems[level]
	if !ok {
		return nil, c.Errf(c.ErrInvalid, "level is %s or %s", c.AdLevelGroup, c.AdLevelAd)
	}
	q := fmt.Sprintf("SELECT %s, %s, %s, segments.date, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions, metrics.conversions_value FROM %s WHERE segments.date BETWEEN '%s' AND '%s'",
		it.id, it.name, it.parent, it.from, mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdItemDay, 0, len(rows))
	for _, r := range rows {
		m := r.Metrics
		d := c.AdItemDay{ID: fmt.Sprint(int64(r.AdGroup.ID)), ParentID: fmt.Sprint(int64(r.Campaign.ID)), Name: r.AdGroup.Name}
		if level == c.AdLevelAd {
			d = c.AdItemDay{ID: fmt.Sprint(int64(r.AdGroupAd.Ad.ID)), ParentID: fmt.Sprint(int64(r.AdGroup.ID)), Name: r.AdGroupAd.Ad.Name}
		}
		d.Date, d.SpendMicros, d.Impressions, d.Clicks = r.Segments.Date, int64(m.CostMicros), int64(m.Impressions), int64(m.Clicks)
		d.Conversions, d.ConversionValueMicros = ptrF(float64(m.Conversions)), ptrI(c.Micros(float64(m.ConversionsValue)))
		out = append(out, d)
	}
	acts, err := g.actions(ctx, cl, creds, acct, it.from, it.id, from, to)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Actions = acts[out[i].ID+"|"+out[i].Date]
	}
	return out, nil
}

// AdKeywordDays reads each search keyword's numbers per day. Keywords
// without impressions that day aren't reported.
func (g GoogleAds) AdKeywordDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdKeywordDay, error) {
	q := fmt.Sprintf("SELECT ad_group_criterion.criterion_id, ad_group_criterion.keyword.text, ad_group_criterion.keyword.match_type, ad_group_criterion.status, ad_group.id, "+
		"segments.date, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions, metrics.conversions_value FROM keyword_view WHERE segments.date BETWEEN '%s' AND '%s'",
		mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdKeywordDay, 0, len(rows))
	for _, r := range rows {
		k, m := r.AdGroupCriterion, r.Metrics
		out = append(out, c.AdKeywordDay{ID: fmt.Sprint(int64(k.CriterionID)), AdGroupID: fmt.Sprint(int64(r.AdGroup.ID)), Text: k.Keyword.Text,
			MatchType: strings.ToLower(k.Keyword.MatchType), Status: status(k.Status), Date: r.Segments.Date,
			SpendMicros: int64(m.CostMicros), Impressions: int64(m.Impressions), Clicks: int64(m.Clicks),
			Conversions: ptrF(float64(m.Conversions)), ConversionValueMicros: ptrI(c.Micros(float64(m.ConversionsValue)))})
	}
	return out, nil
}

// AdSearchTermDays reads what people searched that showed an ad, per day,
// with the keyword each matched. Google leaves out searches made by too
// few people.
func (g GoogleAds) AdSearchTermDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdSearchTermDay, error) {
	q := fmt.Sprintf("SELECT search_term_view.search_term, segments.keyword.info.text, segments.keyword.info.match_type, ad_group.id, "+
		"segments.date, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions, metrics.conversions_value FROM search_term_view WHERE segments.date BETWEEN '%s' AND '%s'",
		mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdSearchTermDay, 0, len(rows))
	for _, r := range rows {
		m, kw := r.Metrics, r.Segments.Keyword.Info
		if r.SearchTermView.SearchTerm == "" {
			continue
		}
		out = append(out, c.AdSearchTermDay{Term: r.SearchTermView.SearchTerm, AdGroupID: fmt.Sprint(int64(r.AdGroup.ID)), KeywordText: kw.Text,
			KeywordMatchType: strings.ToLower(kw.MatchType), Date: r.Segments.Date,
			SpendMicros: int64(m.CostMicros), Impressions: int64(m.Impressions), Clicks: int64(m.Clicks),
			Conversions: ptrF(float64(m.Conversions)), ConversionValueMicros: ptrI(c.Micros(float64(m.ConversionsValue)))})
	}
	return out, nil
}

// ---- conversions ----

// ConversionTargets lists the account's enabled conversion actions that
// take uploaded clicks (Google's "Import, from clicks" actions), which is
// what offline and enhanced conversions for leads are sent to.
func (g GoogleAds) ConversionTargets(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.ConversionTarget, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT conversion_action.id, conversion_action.name FROM conversion_action WHERE conversion_action.type = 'UPLOAD_CLICKS' AND conversion_action.status = 'ENABLED'")
	if err != nil {
		return nil, err
	}
	out := make([]c.ConversionTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.ConversionTarget{ID: fmt.Sprint(int64(r.ConversionAction.ID)), Name: r.ConversionAction.Name, EventTypes: []string{}})
	}
	return out, nil
}

// SendConversions uploads click conversions with partial failure on, so
// one bad row doesn't sink the batch. A conversion carries its click id
// when there is one, and the hashed email and phone for enhanced
// conversions; the event id is the order id Google dedupes on.
func (g GoogleAds) SendConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) ([]c.ConversionResult, error) {
	results := make([]c.ConversionResult, len(convs))
	var body []map[string]any
	var sentIdx []int
	for i, cv := range convs {
		results[i] = c.ConversionResult{Index: cv.Index}
		row := map[string]any{
			"conversionAction":   "customers/" + acct.ID + "/conversionActions/" + target.ID,
			"conversionDateTime": cv.Time.UTC().Format("2006-01-02 15:04:05-07:00"),
			"orderId":            cv.EventID,
		}
		var keys []string
		for _, k := range []string{"gclid", "gbraid", "wbraid"} {
			if v := cv.ClickIDs[k]; v != "" {
				row[k] = v
				keys = append(keys, k)
				break
			}
		}
		if len(keys) == 0 {
			// The Google tag's own cookie keeps the click's gclid when the
			// landing URL lost it (a redirect, a later visit).
			if v := gclidFromCookie(cv.ClickIDs["gcl_aw"]); v != "" {
				row["gclid"] = v
				keys = append(keys, "gclid")
			}
		}
		emails := cv.GoogleEmails
		if len(emails) == 0 {
			emails = cv.Emails
		}
		var ids []map[string]string
		for _, e := range emails {
			ids = append(ids, map[string]string{"hashedEmail": e})
		}
		if len(emails) > 0 {
			keys = append(keys, "email")
		}
		for _, p := range cv.Phones {
			ids = append(ids, map[string]string{"hashedPhoneNumber": p})
		}
		if len(cv.Phones) > 0 {
			keys = append(keys, "phone")
		}
		if len(keys) == 0 {
			results[i].Skipped, results[i].Reason = true, "no click id, email or phone to match on"
			continue
		}
		if len(ids) > 0 {
			row["userIdentifiers"] = ids
		}
		if cv.ValueMicros != nil {
			row["conversionValue"] = float64(*cv.ValueMicros) / 1e6
			row["currencyCode"] = cv.Currency
		}
		if cv.ConsentGranted {
			row["consent"] = map[string]string{"adUserData": "GRANTED"}
		}
		results[i].MatchKeys = keys
		body = append(body, row)
		sentIdx = append(sentIdx, i)
	}
	if len(body) == 0 {
		return results, nil
	}
	var resp struct {
		PartialFailureError *struct {
			Message string `json:"message"`
			Details []struct {
				Errors []struct {
					Message  string `json:"message"`
					Location struct {
						FieldPathElements []struct {
							FieldName string `json:"fieldName"`
							Index     *int   `json:"index"`
						} `json:"fieldPathElements"`
					} `json:"location"`
				} `json:"errors"`
			} `json:"details"`
		} `json:"partialFailureError"`
	}
	err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+":uploadClickConversions",
		map[string]any{"conversions": body, "partialFailure": true}, &resp)
	if err != nil {
		return nil, err
	}
	failed := map[int]string{}
	if pf := resp.PartialFailureError; pf != nil {
		for _, d := range pf.Details {
			for _, e := range d.Errors {
				for _, f := range e.Location.FieldPathElements {
					if f.FieldName == "conversions" && f.Index != nil {
						failed[*f.Index] = e.Message
					}
				}
			}
		}
		if len(failed) == 0 && pf.Message != "" {
			// Google said something failed but not which: count it on all.
			for j := range sentIdx {
				failed[j] = pf.Message
			}
		}
	}
	for j, i := range sentIdx {
		if msg, bad := failed[j]; bad {
			results[i].Reason = msg
			continue
		}
		results[i].OK = true
	}
	return results, nil
}

// ---- audiences ----

// Audiences lists the account's open Customer Match lists.
func (g GoogleAds) Audiences(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdAudience, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT user_list.id, user_list.name FROM user_list WHERE user_list.type = 'CRM_BASED' AND user_list.membership_status = 'OPEN'")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdAudience, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.AdAudience{ID: fmt.Sprint(int64(r.UserList.ID)), Name: r.UserList.Name})
	}
	return out, nil
}

// SyncAudience adds the person to a Customer Match list, or removes them,
// through an offline user data job: create it, add the one operation,
// run it. Google processes the job on its own time, and matches the
// hashed identifiers on its side.
func (g GoogleAds) SyncAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, p c.AudienceMember) error {
	emails := p.GoogleEmails
	if len(emails) == 0 {
		emails = p.Emails
	}
	var ids []map[string]string
	for _, e := range emails {
		ids = append(ids, map[string]string{"hashedEmail": e})
	}
	for _, ph := range p.Phones {
		ids = append(ids, map[string]string{"hashedPhoneNumber": ph})
	}
	if len(ids) == 0 {
		return nil
	}
	meta := map[string]any{"userList": "customers/" + acct.ID + "/userLists/" + audienceID}
	if p.ConsentGranted {
		meta["consent"] = map[string]string{"adUserData": "GRANTED", "adPersonalization": "GRANTED"}
	}
	k := g.call(cl, creds, acct.Meta["login_customer_id"])
	var job struct {
		ResourceName string `json:"resourceName"`
	}
	err := k.post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/offlineUserDataJobs:create",
		map[string]any{"job": map[string]any{"type": "CUSTOMER_MATCH_USER_LIST", "customerMatchUserListMetadata": meta}}, &job)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(job.ResourceName, "customers/"+acct.ID+"/offlineUserDataJobs/") {
		return c.Errf(c.ErrUnavailable, "Google Ads made no user data job")
	}
	op := "create"
	if change == c.AudienceRemove {
		op = "remove"
	}
	if err := k.post(ctx, g.base(cl)+"/"+job.ResourceName+":addOperations",
		map[string]any{"operations": []any{map[string]any{op: map[string]any{"userIdentifiers": ids}}}}, nil); err != nil {
		return err
	}
	return k.post(ctx, g.base(cl)+"/"+job.ResourceName+":run", map[string]any{}, nil)
}

// gclidFromCookie reads the gclid from the Google tag's _gcl_aw cookie,
// which holds GCL.<seconds>.<gclid>.
func gclidFromCookie(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 || parts[0] != "GCL" || parts[2] == "" {
		return ""
	}
	return parts[2]
}
