package ads

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// What Google's Required Minimum Functionality asks of a full-service tool
// (docs/paid-media.md) beyond the campaign calls elsewhere: website
// conversion actions and their tag (C.65), account-level callouts (C.75),
// portfolio Target CPA and Target ROAS strategies and their report (C.96,
// C.97, M.96, M.97, R.130), and removing a campaign, an ad or a keyword
// (M.110, M.130, M.140). Every read is one GAQL query and every change one
// mutate on the workspace's own account. Shapes follow the Google Ads API
// reference: ConversionActionService (type WEBPAGE, tag_snippets read back
// with GAQL), AssetService plus CustomerAssetService (field_type CALLOUT;
// a customer asset's resource name is customerAssets/{asset}~CALLOUT),
// BiddingStrategyService (target_cpa, target_roas) and the remove
// operation of CampaignService, AdGroupAdService and
// AdGroupCriterionService.

var (
	_ c.AdConversionManager = GoogleAds{}
	_ c.AdCalloutManager    = GoogleAds{}
	_ c.AdPortfolioManager  = GoogleAds{}
	_ c.AdRemover           = GoogleAds{}
)

// allConversions reads a row's all conversions and their value, nil when
// Google left them out.
func allConversions(r gaqlRow) (*float64, *int64) {
	m := r.Metrics
	var conv *float64
	var value *int64
	if m.AllConversions != nil {
		conv = ptrF(float64(*m.AllConversions))
	}
	if m.AllConversionsValue != nil {
		value = ptrI(c.Micros(float64(*m.AllConversionsValue)))
	}
	return conv, value
}

// keywordEstimates keeps the bids Google estimated and a quality score
// between 1 and 10.
func keywordEstimates(firstPage, topOfPage, firstPosition *flexInt, quality int) c.AdKeywordEstimates {
	var e c.AdKeywordEstimates
	pos := func(v *flexInt) *int64 {
		if v == nil || *v < 0 {
			return nil
		}
		return ptrI(int64(*v))
	}
	e.FirstPageCPCMicros, e.TopOfPageCPCMicros, e.FirstPositionCPCMicros = pos(firstPage), pos(topOfPage), pos(firstPosition)
	if quality >= 1 && quality <= 10 {
		e.QualityScore = &quality
	}
	return e
}

// searchMatch is a search term's match type in lowercase, empty for
// Google's unknown words.
func searchMatch(m string) string {
	switch m = strings.ToLower(strings.TrimSpace(m)); m {
	case "", "unspecified", "unknown":
		return ""
	}
	return m
}

// ---- conversion actions (C.65) ----

// googleConvCategory is Google's category for each of Outflare's.
var googleConvCategory = map[string]string{
	c.ConvCategoryPurchase: "PURCHASE",
	c.ConvCategorySignup:   "SIGNUP",
	c.ConvCategoryLead:     "SUBMIT_LEAD_FORM",
	c.ConvCategoryPageView: "PAGE_VIEW",
	c.ConvCategoryContact:  "CONTACT",
	c.ConvCategoryOther:    "DEFAULT",
}

// AdConversionActions lists the account's conversion actions that aren't
// removed, with the website code of the website ones.
func (g GoogleAds) AdConversionActions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdConversionAction, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT conversion_action.id, conversion_action.name, conversion_action.type, conversion_action.category, conversion_action.status, "+
			"conversion_action.primary_for_goal, conversion_action.counting_type, conversion_action.click_through_lookback_window_days, "+
			"conversion_action.value_settings.default_value, conversion_action.value_settings.default_currency_code, "+
			"conversion_action.value_settings.always_use_default_value, conversion_action.tag_snippets "+
			"FROM conversion_action WHERE conversion_action.status != 'REMOVED' ORDER BY conversion_action.name")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdConversionAction, 0, len(rows))
	for _, r := range rows {
		ca := r.ConversionAction
		x := c.AdConversionAction{ID: fmt.Sprint(int64(ca.ID)), Name: ca.Name, Type: strings.ToLower(ca.Type), Category: strings.ToLower(ca.Category),
			Status: strings.ToLower(ca.Status), Primary: ca.PrimaryForGoal, ClickWindowDays: int(ca.ClickThroughLookbackWindowDays),
			DefaultCurrency: ca.ValueSettings.DefaultCurrencyCode, AlwaysUseDefault: ca.ValueSettings.AlwaysUseDefaultValue, Snippets: []c.AdTagSnippet{}}
		switch ca.CountingType {
		case "ONE_PER_CLICK":
			x.Counting = "one"
		case "MANY_PER_CLICK":
			x.Counting = "many"
		}
		if v := ca.ValueSettings.DefaultValue; v != nil {
			x.DefaultValueMicros = ptrI(c.Micros(float64(*v)))
		}
		for _, sn := range ca.TagSnippets {
			x.Snippets = append(x.Snippets, c.AdTagSnippet{Type: strings.ToLower(sn.Type), PageFormat: strings.ToLower(sn.PageFormat),
				GlobalSiteTag: sn.GlobalSiteTag, EventSnippet: sn.EventSnippet})
		}
		out = append(out, x)
	}
	return out, nil
}

// CreateWebConversion creates a WEBPAGE conversion action, enabled and
// primary, and returns its id. Its tag is read back with
// AdConversionActions.
func (g GoogleAds) CreateWebConversion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, w c.AdWebConversion) (string, error) {
	if err := c.CheckWebConversion(w); err != nil {
		return "", err
	}
	counting := "ONE_PER_CLICK"
	if w.Counting == "many" {
		counting = "MANY_PER_CLICK"
	}
	create := map[string]any{"name": strings.TrimSpace(w.Name), "type": "WEBPAGE", "category": googleConvCategory[w.Category], "status": "ENABLED",
		"countingType": counting, "clickThroughLookbackWindowDays": strconv.Itoa(w.ClickWindowDays)}
	if w.DefaultValueMicros != nil {
		create["valueSettings"] = map[string]any{"defaultValue": float64(*w.DefaultValueMicros) / 1e6, "defaultCurrencyCode": strings.ToUpper(strings.TrimSpace(w.Currency)),
			"alwaysUseDefaultValue": w.AlwaysUseDefault}
	}
	res, err := g.mutate(ctx, cl, creds, acct, "conversionActions", create)
	if err != nil {
		return "", err
	}
	if res == "" {
		return "", c.Errf(c.ErrUnavailable, "Google Ads made the conversion action but did not say its id")
	}
	return lastPart(res), nil
}

// ---- callouts (C.75) ----

// AdCallouts lists the callouts linked to the account itself.
func (g GoogleAds) AdCallouts(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCallout, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT customer_asset.resource_name, customer_asset.asset, customer_asset.status, asset.id, asset.callout_asset.callout_text "+
			"FROM customer_asset WHERE customer_asset.field_type = 'CALLOUT' AND customer_asset.status != 'REMOVED'")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdCallout, 0, len(rows))
	for _, r := range rows {
		id := fmt.Sprint(int64(r.Asset.ID))
		if r.Asset.ID == 0 {
			id = lastPart(r.CustomerAsset.Asset)
		}
		out = append(out, c.AdCallout{ID: id, Text: r.Asset.CalloutAsset.CalloutText, Status: status(r.CustomerAsset.Status)})
	}
	return out, nil
}

// AddCallout creates the callout asset and links it to the account in one
// batch, so a refused link leaves no stray asset.
func (g GoogleAds) AddCallout(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, text string) (string, error) {
	text, err := c.CheckCallout(text)
	if err != nil {
		return "", err
	}
	asset := g.resource(acct, "assets", "-1")
	names, err := g.batch(ctx, cl, creds, acct, []any{
		map[string]any{"assetOperation": map[string]any{"create": map[string]any{"resourceName": asset, "calloutAsset": map[string]any{"calloutText": text}}}},
		map[string]any{"customerAssetOperation": map[string]any{"create": map[string]any{"asset": asset, "fieldType": "CALLOUT"}}},
	})
	if err != nil {
		return "", err
	}
	if len(names) == 0 || names[0] == "" {
		return "", c.Errf(c.ErrUnavailable, "Google Ads made the callout but did not say its id")
	}
	return lastPart(names[0]), nil
}

// RemoveCallout takes the callout off the account.
func (g GoogleAds) RemoveCallout(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, id string) error {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return c.Errf(c.ErrInvalid, "Google Ads asset ids are numbers")
	}
	list, err := g.AdCallouts(ctx, cl, creds, acct)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.ID == id {
			return g.remove(ctx, cl, creds, acct, "customerAssets", g.resource(acct, "customerAssets", id+"~CALLOUT"))
		}
	}
	return nil
}

// ---- portfolio bidding strategies (C.96, C.97, M.96, M.97, R.130) ----

func portfolioOf(r gaqlRow) c.AdPortfolio {
	b := r.BiddingStrategy
	p := c.AdPortfolio{ID: fmt.Sprint(int64(b.ID)), Name: b.Name, Type: strings.ToLower(b.Type), Status: strings.ToLower(b.Status),
		CampaignCount: int(b.CampaignCount), Currency: b.EffectiveCurrencyCode}
	for _, v := range []flexInt{b.TargetCpa.TargetCpaMicros, b.MaximizeConversions.TargetCpaMicros} {
		if v > 0 {
			p.TargetCPAMicros = ptrI(int64(v))
			break
		}
	}
	for _, v := range []float64{b.TargetRoas.TargetRoas, b.MaximizeConversionValue.TargetRoas} {
		if v > 0 {
			p.TargetROAS = ptrF(v)
			break
		}
	}
	return p
}

const portfolioFields = "bidding_strategy.id, bidding_strategy.name, bidding_strategy.type, bidding_strategy.status, bidding_strategy.campaign_count, " +
	"bidding_strategy.effective_currency_code, bidding_strategy.target_cpa.target_cpa_micros, bidding_strategy.target_roas.target_roas, " +
	"bidding_strategy.maximize_conversions.target_cpa_micros, bidding_strategy.maximize_conversion_value.target_roas"

// AdPortfolios lists the account's portfolio strategies that aren't
// removed.
func (g GoogleAds) AdPortfolios(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdPortfolio, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT "+portfolioFields+" FROM bidding_strategy WHERE bidding_strategy.status != 'REMOVED' ORDER BY bidding_strategy.name")
	if err != nil {
		return nil, err
	}
	out := make([]c.AdPortfolio, 0, len(rows))
	for _, r := range rows {
		out = append(out, portfolioOf(r))
	}
	return out, nil
}

func (g GoogleAds) CreatePortfolio(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, p c.AdPortfolioSpec) (string, error) {
	if err := c.CheckPortfolio(p, true); err != nil {
		return "", err
	}
	create := map[string]any{"name": strings.TrimSpace(p.Name)}
	if p.Type == c.PortfolioTargetCPA {
		create["targetCpa"] = map[string]any{"targetCpaMicros": strconv.FormatInt(*p.TargetCPAMicros, 10)}
	} else {
		create["targetRoas"] = map[string]any{"targetRoas": *p.TargetROAS}
	}
	res, err := g.mutate(ctx, cl, creds, acct, "biddingStrategies", create)
	if err != nil {
		return "", err
	}
	if res == "" {
		return "", c.Errf(c.ErrUnavailable, "Google Ads made the strategy but did not say its id")
	}
	return lastPart(res), nil
}

// portfolio reads one strategy.
func (g GoogleAds) portfolio(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, id string) (c.AdPortfolio, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return c.AdPortfolio{}, c.Errf(c.ErrInvalid, "Google Ads bidding strategy ids are numbers")
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], "SELECT "+portfolioFields+" FROM bidding_strategy WHERE bidding_strategy.id = "+id)
	if err != nil {
		return c.AdPortfolio{}, err
	}
	if len(rows) == 0 {
		return c.AdPortfolio{}, c.Errf(c.ErrInvalid, "Google Ads has no bidding strategy %s in account %s", id, acct.ID)
	}
	return portfolioOf(rows[0]), nil
}

// UpdatePortfolio moves the strategy's target where its type keeps it (a
// Target CPA or Maximise conversions strategy takes a cost per conversion,
// a Target ROAS or Maximise conversion value one a return on spend), and
// renames it.
func (g GoogleAds) UpdatePortfolio(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, id string, p c.AdPortfolioSpec) error {
	if err := c.CheckPortfolio(p, false); err != nil {
		return err
	}
	cur, err := g.portfolio(ctx, cl, creds, acct, id)
	if err != nil {
		return err
	}
	update := map[string]any{"resourceName": g.resource(acct, "biddingStrategies", id)}
	var mask []string
	if name := strings.TrimSpace(p.Name); name != "" {
		update["name"] = name
		mask = append(mask, "name")
	}
	switch {
	case p.TargetCPAMicros != nil:
		field := map[string]string{"target_cpa": "targetCpa", "maximize_conversions": "maximizeConversions"}[cur.Type]
		if field == "" {
			return c.Errf(c.ErrInvalid, "strategy %s bids %s, which takes no target cost per conversion", id, cur.Type)
		}
		update[field] = map[string]any{"targetCpaMicros": strconv.FormatInt(*p.TargetCPAMicros, 10)}
		mask = append(mask, cur.Type+".target_cpa_micros")
	case p.TargetROAS != nil:
		field := map[string]string{"target_roas": "targetRoas", "maximize_conversion_value": "maximizeConversionValue"}[cur.Type]
		if field == "" {
			return c.Errf(c.ErrInvalid, "strategy %s bids %s, which takes no target return on spend", id, cur.Type)
		}
		update[field] = map[string]any{"targetRoas": *p.TargetROAS}
		mask = append(mask, cur.Type+".target_roas")
	}
	return g.update(ctx, cl, creds, acct, "biddingStrategies", update, strings.Join(mask, ","))
}

// AdPortfolioReport reads each strategy's numbers over the range: the
// fields RMF R.130 asks for, Google's own averages.
func (g GoogleAds) AdPortfolioReport(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdPortfolioReport, error) {
	q := fmt.Sprintf("SELECT %s, metrics.clicks, metrics.impressions, metrics.cost_micros, metrics.average_cpc, metrics.conversions, "+
		"metrics.cost_per_conversion, metrics.all_conversions FROM bidding_strategy WHERE segments.date BETWEEN '%s' AND '%s'",
		portfolioFields, mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02"))
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	// Strategies without a click in the range aren't reported: list them
	// at zero, so every one shows.
	all, err := g.AdPortfolios(ctx, cl, creds, acct)
	if err != nil {
		return nil, err
	}
	at := map[string]int{}
	out := make([]c.AdPortfolioReport, 0, len(all))
	for _, p := range all {
		at[p.ID] = len(out)
		out = append(out, c.AdPortfolioReport{AdPortfolio: p})
	}
	for _, r := range rows {
		p := portfolioOf(r)
		i, ok := at[p.ID]
		if !ok {
			// A strategy removed since, with numbers in the range.
			i = len(out)
			at[p.ID] = i
			out = append(out, c.AdPortfolioReport{AdPortfolio: p})
		}
		x := &out[i]
		m := r.Metrics
		x.SpendMicros += int64(m.CostMicros)
		x.Impressions += int64(m.Impressions)
		x.Clicks += int64(m.Clicks)
		x.Conversions += float64(m.Conversions)
		if m.AllConversions != nil {
			x.AllConversions += float64(*m.AllConversions)
		}
		if m.AverageCpc > 0 {
			x.CPCMicros = ptrI(int64(m.AverageCpc))
		}
		if m.CostPerConversion > 0 {
			x.CostPerConvMicros = ptrI(int64(m.CostPerConversion))
		}
	}
	return out, nil
}

// ---- removing (M.110, M.130, M.140) ----

// removable is the service and resource name of what Remove removes, and
// the GAQL that reads its status back.
func (g GoogleAds) removable(acct c.AdAccount, what, adGroupID, id string) (service, resource, query string, err error) {
	ids := []string{id}
	if what != c.RemoveCampaign {
		ids = append(ids, adGroupID)
	}
	for _, v := range ids {
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return "", "", "", c.Errf(c.ErrInvalid, "Google Ads ids are numbers")
		}
	}
	switch what {
	case c.RemoveCampaign:
		return "campaigns", g.resource(acct, "campaigns", id), "SELECT campaign.id, campaign.status FROM campaign WHERE campaign.id = " + id, nil
	case c.RemoveAd:
		return "adGroupAds", g.resource(acct, "adGroupAds", adGroupID+"~"+id),
			"SELECT ad_group_ad.ad.id, ad_group_ad.status FROM ad_group_ad WHERE ad_group.id = " + adGroupID + " AND ad_group_ad.ad.id = " + id, nil
	case c.RemoveKeyword:
		return "adGroupCriteria", g.resource(acct, "adGroupCriteria", adGroupID+"~"+id),
			"SELECT ad_group_criterion.criterion_id, ad_group_criterion.status FROM ad_group_criterion WHERE ad_group.id = " + adGroupID + " AND ad_group_criterion.criterion_id = " + id, nil
	}
	return "", "", "", c.Errf(c.ErrInvalid, "remove a campaign, an ad or a keyword")
}

// Remove removes the campaign, ad or keyword. One Google already shows as
// removed is left alone, so a retry changes nothing twice.
func (g GoogleAds) Remove(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, what, adGroupID, id string) error {
	service, resource, _, err := g.removable(acct, what, adGroupID, id)
	if err != nil {
		return err
	}
	if st, err := g.RemovedStatus(ctx, cl, creds, acct, what, adGroupID, id); err == nil && st == c.AdRemoved {
		return nil
	}
	return g.remove(ctx, cl, creds, acct, service, resource)
}

// RemovedStatus reads the campaign's, ad's or keyword's status back.
func (g GoogleAds) RemovedStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, what, adGroupID, id string) (string, error) {
	_, _, q, err := g.removable(acct, what, adGroupID, id)
	if err != nil {
		return "", err
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		// Google lists removed rows; none at all is a resource it no
		// longer has.
		return c.AdRemoved, nil
	}
	r := rows[0]
	switch what {
	case c.RemoveCampaign:
		return status(r.Campaign.Status), nil
	case c.RemoveAd:
		return status(r.AdGroupAd.Status), nil
	}
	return status(r.AdGroupCriterion.Status), nil
}
