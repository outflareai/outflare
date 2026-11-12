package ads

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The findings queue's levers on Google Ads (plans/paid-ads/plan.md,
// verdicts): undoing a negative keyword, pausing and resuming a keyword or
// an ad, moving a campaign's target, and the exclusions a campaign stops
// showing on (a device, a place, a Display placement, a network, hours of
// the day, a brand), each with its inverse so a finding can be reverted.
// Plus the read the exclusion rules run on: impression share lost and the
// campaign's numbers by device, network, country, placement and hour.
// Shapes follow the Google Ads API v26 reference
// (plans/paid-ads/research/api-campaign-management.md): a device
// criterion with bid_modifier 0 opts the device out; negative is
// immutable on a criterion, so an exclusion is removed, never flipped;
// network_settings carry the partner and Display opt-ins; ad schedules
// are the windows a campaign shows in, so a window excluded is the rest
// of the day added; brand exclusions are a BRANDS shared set on a
// negative brand_list criterion.

var (
	_ c.AdKeywordEditor    = GoogleAds{}
	_ c.AdStatusManager    = GoogleAds{}
	_ c.AdTargetManager    = GoogleAds{}
	_ c.AdExclusionManager = GoogleAds{}
	_ c.AdSignalsReader    = GoogleAds{}
)

// googleDevices are Google's device types a campaign can opt out of, by
// the exclusion's word.
var googleDevices = map[string]string{"desktop": "DESKTOP", "mobile": "MOBILE", "tablet": "TABLET", "connected_tv": "CONNECTED_TV"}

// googleDays are Google's days of the week, Monday first.
var googleDays = []string{"MONDAY", "TUESDAY", "WEDNESDAY", "THURSDAY", "FRIDAY", "SATURDAY", "SUNDAY"}

// remove removes one resource through its service's mutate.
func (g GoogleAds) remove(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, service, resource string) error {
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/"+service+":mutate",
		map[string]any{"operations": []any{map[string]any{"remove": resource}}}, nil)
}

// update updates one resource's fields through its service's mutate.
func (g GoogleAds) update(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, service string, update map[string]any, mask string) error {
	return g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/"+service+":mutate",
		map[string]any{"operations": []any{map[string]any{"update": update, "updateMask": mask}}}, nil)
}

// campaignCriteria lists a campaign's live criteria of the given types.
func (g GoogleAds) campaignCriteria(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, types ...string) ([]gaqlRow, error) {
	if err := googleCampaignID(campaignID); err != nil {
		return nil, err
	}
	quoted := make([]string, len(types))
	for i, t := range types {
		quoted[i] = "'" + t + "'"
	}
	return g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT campaign_criterion.resource_name, campaign_criterion.type, campaign_criterion.negative, campaign_criterion.bid_modifier, campaign_criterion.status, "+
			"campaign_criterion.keyword.text, campaign_criterion.keyword.match_type, campaign_criterion.device.type, campaign_criterion.location.geo_target_constant, "+
			"campaign_criterion.ad_schedule.day_of_week, campaign_criterion.ad_schedule.start_hour, campaign_criterion.ad_schedule.start_minute, "+
			"campaign_criterion.ad_schedule.end_hour, campaign_criterion.ad_schedule.end_minute, campaign_criterion.placement.url, "+
			"campaign_criterion.mobile_application.app_id, campaign_criterion.youtube_channel.channel_id, campaign_criterion.youtube_video.video_id, "+
			"campaign_criterion.brand_list.shared_set FROM campaign_criterion WHERE campaign.id = "+campaignID+
			" AND campaign_criterion.type IN ("+strings.Join(quoted, ", ")+") AND campaign_criterion.status != 'REMOVED'")
}

// ---- keywords and ads ----

// RemoveNegativeKeyword finds the negative by its text and match type on
// the campaign (adGroupID empty) or the ad group and removes it; one that
// isn't there succeeds.
func (g GoogleAds) RemoveNegativeKeyword(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, adGroupID, text, matchType string) error {
	m, err := googleMatch(matchType)
	if err != nil {
		return err
	}
	if text, err = keywordText(text); err != nil {
		return err
	}
	if adGroupID != "" {
		if _, err := strconv.ParseInt(adGroupID, 10, 64); err != nil {
			return c.Errf(c.ErrInvalid, "Google Ads ad group ids are numbers")
		}
		rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
			"SELECT ad_group_criterion.resource_name, ad_group_criterion.negative, ad_group_criterion.keyword.text, ad_group_criterion.keyword.match_type FROM ad_group_criterion "+
				"WHERE ad_group.id = "+adGroupID+" AND ad_group_criterion.type = 'KEYWORD' AND ad_group_criterion.negative = TRUE AND ad_group_criterion.status != 'REMOVED'")
		if err != nil {
			return err
		}
		for _, r := range rows {
			k := r.AdGroupCriterion
			if k.Negative && strings.EqualFold(k.Keyword.Text, text) && strings.EqualFold(k.Keyword.MatchType, m) && k.ResourceName != "" {
				return g.remove(ctx, cl, creds, acct, "adGroupCriteria", k.ResourceName)
			}
		}
		return nil
	}
	rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "KEYWORD")
	if err != nil {
		return err
	}
	for _, r := range rows {
		k := r.CampaignCriterion
		if k.Negative && strings.EqualFold(k.Keyword.Text, text) && strings.EqualFold(k.Keyword.MatchType, m) {
			return g.removeCriterion(ctx, cl, creds, acct, k.ResourceName)
		}
	}
	return nil
}

// SetKeywordStatus pauses or resumes a keyword (adGroupCriteria:mutate on
// its status alone).
func (g GoogleAds) SetKeywordStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, keywordID string, active bool) error {
	for _, id := range []string{adGroupID, keywordID} {
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return c.Errf(c.ErrInvalid, "Google Ads ad group and keyword ids are numbers")
		}
	}
	return g.update(ctx, cl, creds, acct, "adGroupCriteria", map[string]any{"resourceName": g.resource(acct, "adGroupCriteria", adGroupID+"~"+keywordID), "status": enabledOrPaused(active)}, "status")
}

// SetAdStatus pauses or resumes an ad (adGroupAds:mutate on its status alone).
func (g GoogleAds) SetAdStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, adID string, active bool) error {
	for _, id := range []string{adGroupID, adID} {
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return c.Errf(c.ErrInvalid, "Google Ads ad group and ad ids are numbers")
		}
	}
	return g.update(ctx, cl, creds, acct, "adGroupAds", map[string]any{"resourceName": g.resource(acct, "adGroupAds", adGroupID+"~"+adID), "status": enabledOrPaused(active)}, "status")
}

func enabledOrPaused(active bool) string {
	if active {
		return "ENABLED"
	}
	return "PAUSED"
}

// ---- targets ----

// SetCampaignTarget moves the target of the strategy the campaign already
// bids with: target_cpa or maximize_conversions take a cost per
// conversion, target_roas or maximize_conversion_value a return on spend.
// A Maximise strategy with both nil drops its target; a portfolio
// strategy is refused, as Google refuses it (change it in Google Ads).
func (g GoogleAds) SetCampaignTarget(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, t c.AdTarget) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT campaign.id, campaign.bidding_strategy_type, campaign.bidding_strategy FROM campaign WHERE campaign.id = "+campaignID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return c.Errf(c.ErrInvalid, "Google Ads has no campaign %s in account %s", campaignID, acct.ID)
	}
	if rows[0].Campaign.BiddingStrategy != "" {
		return c.Errf(c.ErrInvalid, "campaign %s bids with a portfolio strategy; change its target in Google Ads", campaignID)
	}
	if t.TargetCPAMicros != nil && *t.TargetCPAMicros <= 0 {
		return c.Errf(c.ErrInvalid, "the target cost per conversion must be more than nothing")
	}
	if t.TargetROAS != nil && *t.TargetROAS <= 0 {
		return c.Errf(c.ErrInvalid, "the target return on spend must be more than nothing")
	}
	update := map[string]any{"resourceName": g.resource(acct, "campaigns", campaignID)}
	var mask string
	strategy := strings.ToUpper(rows[0].Campaign.BiddingStrategyType)
	switch strategy {
	case "TARGET_CPA", "MAXIMIZE_CONVERSIONS":
		if t.TargetROAS != nil {
			return c.Errf(c.ErrInvalid, "campaign %s bids to a cost per conversion, not a return on spend", campaignID)
		}
		if t.TargetCPAMicros == nil && strategy == "TARGET_CPA" {
			return c.Errf(c.ErrInvalid, "a target CPA strategy needs a target; switch the campaign's bidding to drop it")
		}
		cpa := "0"
		if t.TargetCPAMicros != nil {
			cpa = strconv.FormatInt(*t.TargetCPAMicros, 10)
		}
		if strategy == "TARGET_CPA" {
			update["targetCpa"], mask = map[string]any{"targetCpaMicros": cpa}, "target_cpa.target_cpa_micros"
		} else {
			update["maximizeConversions"], mask = map[string]any{"targetCpaMicros": cpa}, "maximize_conversions.target_cpa_micros"
		}
	case "TARGET_ROAS", "MAXIMIZE_CONVERSION_VALUE":
		if t.TargetCPAMicros != nil {
			return c.Errf(c.ErrInvalid, "campaign %s bids to a return on spend, not a cost per conversion", campaignID)
		}
		if t.TargetROAS == nil && strategy == "TARGET_ROAS" {
			return c.Errf(c.ErrInvalid, "a target ROAS strategy needs a target; switch the campaign's bidding to drop it")
		}
		var roas float64
		if t.TargetROAS != nil {
			roas = *t.TargetROAS
		}
		if strategy == "TARGET_ROAS" {
			update["targetRoas"], mask = map[string]any{"targetRoas": roas}, "target_roas.target_roas"
		} else {
			update["maximizeConversionValue"], mask = map[string]any{"targetRoas": roas}, "maximize_conversion_value.target_roas"
		}
	default:
		return c.Errf(c.ErrInvalid, "campaign %s bids with %s, which takes no target here", campaignID, strings.ToLower(strategy))
	}
	return g.update(ctx, cl, creds, acct, "campaigns", update, mask)
}

// ---- exclusions ----

func (GoogleAds) ExclusionKinds() []string {
	return []string{c.ExcludeDevice, c.ExcludeLocation, c.ExcludePlacement, c.ExcludeNetwork, c.ExcludeSchedule, c.ExcludeBrand}
}

// AddExclusion makes the campaign stop showing on x.
func (g GoogleAds) AddExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	campaign := g.resource(acct, "campaigns", campaignID)
	switch x.Kind {
	case c.ExcludeDevice:
		dev, ok := googleDevices[strings.ToLower(x.Value)]
		if !ok {
			return c.Errf(c.ErrInvalid, "a device is desktop, mobile, tablet or connected_tv, not %q", x.Value)
		}
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "DEVICE")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if cc := r.CampaignCriterion; strings.EqualFold(cc.Device.Type, dev) {
				if cc.BidModifier == 0 {
					return nil
				}
				return g.update(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"resourceName": cc.ResourceName, "bidModifier": 0.0}, "bid_modifier")
			}
		}
		_, err = g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": campaign, "device": map[string]any{"type": dev}, "bidModifier": 0.0})
		return err
	case c.ExcludeLocation:
		if !strings.HasPrefix(x.Value, "geoTargetConstants/") {
			return c.Errf(c.ErrInvalid, "a place is a geo target constant (geoTargetConstants/N), not %q", x.Value)
		}
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "LOCATION")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if cc := r.CampaignCriterion; cc.Negative && cc.Location.GeoTargetConstant == x.Value {
				return nil
			}
		}
		_, err = g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": campaign, "negative": true, "location": map[string]any{"geoTargetConstant": x.Value}})
		return err
	case c.ExcludePlacement:
		field, kind, err := googlePlacement(x)
		if err != nil {
			return err
		}
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "PLACEMENT", "MOBILE_APPLICATION", "YOUTUBE_CHANNEL", "YOUTUBE_VIDEO")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if cc := r.CampaignCriterion; cc.Negative && placementValue(r) == x.Value {
				return nil
			}
		}
		_, err = g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": campaign, "negative": true, kind: map[string]any{field: x.Value}})
		return err
	case c.ExcludeNetwork:
		return g.setNetwork(ctx, cl, creds, acct, campaignID, x.Value, false)
	case c.ExcludeSchedule:
		start, end, err := scheduleWindow(x.Value)
		if err != nil {
			return err
		}
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "AD_SCHEDULE")
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			if scheduleMatches(rows, start, end) {
				return nil
			}
			return c.Errf(c.ErrInvalid, "campaign %s already has an ad schedule; change it in Google Ads", campaignID)
		}
		for _, day := range googleDays {
			for _, w := range servingWindows(start, end) {
				if _, err := g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": campaign, "adSchedule": map[string]any{
					"dayOfWeek": day, "startHour": w[0], "startMinute": "ZERO", "endHour": w[1], "endMinute": "ZERO"}}); err != nil {
					return err
				}
			}
		}
		return nil
	case c.ExcludeBrand:
		return g.excludeBrand(ctx, cl, creds, acct, campaignID, x.Value)
	}
	return c.Errf(c.ErrInvalid, "Google Ads takes no exclusion of kind %q", x.Kind)
}

// RemoveExclusion is AddExclusion's inverse.
func (g GoogleAds) RemoveExclusion(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID string, x c.AdExclusion) error {
	if err := googleCampaignID(campaignID); err != nil {
		return err
	}
	switch x.Kind {
	case c.ExcludeDevice:
		dev, ok := googleDevices[strings.ToLower(x.Value)]
		if !ok {
			return c.Errf(c.ErrInvalid, "a device is desktop, mobile, tablet or connected_tv, not %q", x.Value)
		}
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "DEVICE")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if cc := r.CampaignCriterion; strings.EqualFold(cc.Device.Type, dev) && cc.BidModifier == 0 {
				return g.removeCriterion(ctx, cl, creds, acct, cc.ResourceName)
			}
		}
		return nil
	case c.ExcludeLocation:
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "LOCATION")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if cc := r.CampaignCriterion; cc.Negative && cc.Location.GeoTargetConstant == x.Value {
				return g.removeCriterion(ctx, cl, creds, acct, cc.ResourceName)
			}
		}
		return nil
	case c.ExcludePlacement:
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "PLACEMENT", "MOBILE_APPLICATION", "YOUTUBE_CHANNEL", "YOUTUBE_VIDEO")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if cc := r.CampaignCriterion; cc.Negative && placementValue(r) == x.Value {
				return g.removeCriterion(ctx, cl, creds, acct, cc.ResourceName)
			}
		}
		return nil
	case c.ExcludeNetwork:
		return g.setNetwork(ctx, cl, creds, acct, campaignID, x.Value, true)
	case c.ExcludeSchedule:
		start, end, err := scheduleWindow(x.Value)
		if err != nil {
			return err
		}
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "AD_SCHEDULE")
		if err != nil {
			return err
		}
		// Only the windows the exclusion added go; a schedule a person
		// changed since is left alone.
		if !scheduleMatches(rows, start, end) {
			return c.Errf(c.ErrInvalid, "campaign %s's ad schedule was changed since; change it in Google Ads", campaignID)
		}
		for _, r := range rows {
			if err := g.removeCriterion(ctx, cl, creds, acct, r.CampaignCriterion.ResourceName); err != nil {
				return err
			}
		}
		return nil
	case c.ExcludeBrand:
		set, err := g.brandSet(ctx, cl, creds, acct, x.Value)
		if err != nil || set == "" {
			return err
		}
		rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "BRAND_LIST")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if cc := r.CampaignCriterion; cc.Negative && cc.BrandList.SharedSet == set {
				return g.removeCriterion(ctx, cl, creds, acct, cc.ResourceName)
			}
		}
		return nil
	}
	return c.Errf(c.ErrInvalid, "Google Ads takes no exclusion of kind %q", x.Kind)
}

// googlePlacement is the criterion field a placement exclusion sets, by
// its detail: website (placement.url), mobile_application
// (mobile_application.app_id), youtube_channel (channel_id) or
// youtube_video (video_id).
func googlePlacement(x c.AdExclusion) (field, kind string, err error) {
	if x.Value == "" {
		return "", "", c.Errf(c.ErrInvalid, "a placement needs its site, app or channel")
	}
	switch strings.ToLower(x.Detail) {
	case "", "website":
		return "url", "placement", nil
	case "mobile_application":
		return "appId", "mobileApplication", nil
	case "youtube_channel":
		return "channelId", "youtubeChannel", nil
	case "youtube_video":
		return "videoId", "youtubeVideo", nil
	}
	return "", "", c.Errf(c.ErrInvalid, "a placement is a website, mobile_application, youtube_channel or youtube_video, not %q", x.Detail)
}

// placementValue is the placement a criterion names, whichever kind.
func placementValue(r gaqlRow) string {
	cc := r.CampaignCriterion
	return firstText(cc.Placement.URL, cc.MobileApplication.AppID, cc.YoutubeChannel.ChannelID, cc.YoutubeVideo.VideoID)
}

// setNetwork turns the search partners or Display expansion on or off.
func (g GoogleAds) setNetwork(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, network string, on bool) error {
	var field, mask string
	switch strings.ToLower(network) {
	case "search_partners":
		field, mask = "targetSearchNetwork", "network_settings.target_search_network"
	case "display":
		field, mask = "targetContentNetwork", "network_settings.target_content_network"
	default:
		return c.Errf(c.ErrInvalid, "a network is search_partners or display, not %q", network)
	}
	return g.update(ctx, cl, creds, acct, "campaigns", map[string]any{"resourceName": g.resource(acct, "campaigns", campaignID), "networkSettings": map[string]any{field: on}}, mask)
}

// scheduleWindow parses "HH-HH": the hours a campaign stops showing in,
// end exclusive, 0 to 24.
func scheduleWindow(v string) (start, end int, err error) {
	parts := strings.Split(strings.TrimSpace(v), "-")
	if len(parts) != 2 {
		return 0, 0, c.Errf(c.ErrInvalid, "a schedule window is HH-HH, such as 00-06, not %q", v)
	}
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || start < 0 || start > 23 || end < 1 || end > 24 || start == end {
		return 0, 0, c.Errf(c.ErrInvalid, "a schedule window is HH-HH with hours 0 to 24, not %q", v)
	}
	return start, end, nil
}

// servingWindows are the hours a campaign keeps showing in when
// start..end is excluded: the rest of the day, in one or two windows.
func servingWindows(start, end int) [][2]int {
	if start < end {
		var out [][2]int
		if start > 0 {
			out = append(out, [2]int{0, start})
		}
		if end < 24 {
			out = append(out, [2]int{end, 24})
		}
		return out
	}
	// Overnight (22-06): the campaign shows from end to start.
	return [][2]int{{end, start}}
}

// scheduleMatches says whether the campaign's ad schedule is exactly the
// serving windows of this exclusion on every day.
func scheduleMatches(rows []gaqlRow, start, end int) bool {
	want := map[string]bool{}
	for _, day := range googleDays {
		for _, w := range servingWindows(start, end) {
			want[fmt.Sprintf("%s %d-%d", day, w[0], w[1])] = true
		}
	}
	got := map[string]bool{}
	for _, r := range rows {
		s := r.CampaignCriterion.AdSchedule
		if (s.StartMinute != "" && s.StartMinute != "ZERO") || (s.EndMinute != "" && s.EndMinute != "ZERO") {
			return false
		}
		got[fmt.Sprintf("%s %d-%d", strings.ToUpper(s.DayOfWeek), int64(s.StartHour), int64(s.EndHour))] = true
	}
	if len(got) != len(want) {
		return false
	}
	for k := range want {
		if !got[k] {
			return false
		}
	}
	return true
}

// brandSetName is the shared set Outflare keeps a brand's exclusion in.
func brandSetName(brand string) string {
	return "Outflare brand exclusion: " + strings.Join(strings.Fields(brand), " ")
}

// brandSet finds Outflare's BRANDS shared set for a brand, or "" when
// there is none.
func (g GoogleAds) brandSet(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, brand string) (string, error) {
	name := strings.ReplaceAll(brandSetName(brand), "'", "")
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT shared_set.resource_name, shared_set.name, shared_set.type FROM shared_set WHERE shared_set.type = 'BRANDS' AND shared_set.status = 'ENABLED' AND shared_set.name = '"+name+"'")
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.SharedSet.ResourceName != "" {
			return r.SharedSet.ResourceName, nil
		}
	}
	return "", nil
}

// excludeBrand keeps a Performance Max campaign off searches for a brand:
// Google's brand entity for the name (BrandSuggestionService) goes in a
// BRANDS shared set of Outflare's, which the campaign excludes.
func (g GoogleAds) excludeBrand(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, brand string) error {
	brand = strings.Join(strings.Fields(brand), " ")
	if brand == "" {
		return c.Errf(c.ErrInvalid, "a brand exclusion needs the brand's name")
	}
	set, err := g.brandSet(ctx, cl, creds, acct, brand)
	if err != nil {
		return err
	}
	if set == "" {
		var resp struct {
			Brands []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"brands"`
		}
		if err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+":suggestBrands",
			map[string]any{"brandPrefix": brand}, &resp); err != nil {
			return err
		}
		entity := ""
		for _, b := range resp.Brands {
			if strings.EqualFold(b.Name, brand) {
				entity = b.ID
				break
			}
		}
		if entity == "" && len(resp.Brands) > 0 {
			entity = resp.Brands[0].ID
		}
		if entity == "" {
			return c.Errf(c.ErrInvalid, "Google Ads knows no brand named %q to exclude", brand)
		}
		set, err = g.mutate(ctx, cl, creds, acct, "sharedSets", map[string]any{"name": brandSetName(brand), "type": "BRANDS"})
		if err != nil {
			return err
		}
		if set == "" {
			return c.Errf(c.ErrUnavailable, "Google Ads made the brand list but did not say its id")
		}
		if _, err := g.mutate(ctx, cl, creds, acct, "sharedCriteria", map[string]any{"sharedSet": set, "brand": map[string]any{"entityId": entity}}); err != nil {
			return err
		}
	}
	rows, err := g.campaignCriteria(ctx, cl, creds, acct, campaignID, "BRAND_LIST")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if cc := r.CampaignCriterion; cc.Negative && cc.BrandList.SharedSet == set {
			return nil
		}
	}
	_, err = g.mutate(ctx, cl, creds, acct, "campaignCriteria", map[string]any{"campaign": g.resource(acct, "campaigns", campaignID), "negative": true, "brandList": map[string]any{"sharedSet": set}})
	return err
}

// ---- signals ----

// AdCampaignSignals reads the campaign's impression share lost to budget
// and rank, which networks it is on, and its numbers by device, network,
// country, Display placement and hour over from..to. Reports a campaign
// type doesn't have (places and placements on some types) are left out
// rather than failing the read.
func (g GoogleAds) AdCampaignSignals(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, from, to string) (c.AdCampaignSignals, error) {
	var out c.AdCampaignSignals
	if err := googleCampaignID(campaignID); err != nil {
		return out, err
	}
	f, t := mustDate(from).Format("2006-01-02"), mustDate(to).Format("2006-01-02")
	login := acct.Meta["login_customer_id"]
	where := " FROM campaign WHERE campaign.id = " + campaignID + " AND segments.date BETWEEN '" + f + "' AND '" + t + "'"
	rows, err := g.search(ctx, cl, creds, acct.ID, login, "SELECT campaign.network_settings.target_search_network, campaign.network_settings.target_content_network, "+
		"metrics.search_budget_lost_impression_share, metrics.search_rank_lost_impression_share, segments.date"+where)
	if err != nil {
		return out, err
	}
	var budget, rank float64
	n := 0
	for _, r := range rows {
		out.SearchPartners = out.SearchPartners || r.Campaign.NetworkSettings.TargetSearchNetwork
		out.DisplayExpansion = out.DisplayExpansion || r.Campaign.NetworkSettings.TargetContentNetwork
		budget += float64(r.Metrics.SearchBudgetLostImpressionShare)
		rank += float64(r.Metrics.SearchRankLostImpressionShare)
		n++
	}
	if n > 0 {
		out.BudgetLostImpressionShare, out.RankLostImpressionShare = budget/float64(n), rank/float64(n)
	}
	metrics := "metrics.impressions, metrics.clicks, metrics.cost_micros, metrics.conversions"
	segments := map[string]*c.AdSegment{}
	add := func(kind, value, detail, label string, m gaqlRow) {
		if value == "" {
			return
		}
		key := kind + "\x00" + value
		s := segments[key]
		if s == nil {
			s = &c.AdSegment{Kind: kind, Value: value, Detail: detail, Label: label}
			segments[key] = s
		}
		s.Impressions += int64(m.Metrics.Impressions)
		s.Clicks += int64(m.Metrics.Clicks)
		s.SpendMicros += int64(m.Metrics.CostMicros)
		s.Conversions += float64(m.Metrics.Conversions)
	}
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT segments.device, "+metrics+where)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		add(c.ExcludeDevice, strings.ToLower(r.Segments.Device), "", strings.ToLower(r.Segments.Device), r)
	}
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT segments.ad_network_type, "+metrics+where)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		add(c.ExcludeNetwork, googleNetwork(r.Segments.AdNetworkType), "", googleNetwork(r.Segments.AdNetworkType), r)
	}
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT segments.hour, "+metrics+where)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		add("hour", fmt.Sprintf("%02d", int64(r.Segments.Hour)), "", fmt.Sprintf("%02d:00", int64(r.Segments.Hour)), r)
	}
	// Places and placements: not every campaign type reports them.
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT geographic_view.country_criterion_id, "+metrics+" FROM geographic_view WHERE campaign.id = "+campaignID+
		" AND segments.date BETWEEN '"+f+"' AND '"+t+"'")
	if err != nil && c.KindOf(err) != c.ErrInvalid {
		return out, err
	}
	var places []string
	for _, r := range rows {
		id := int64(r.GeographicView.CountryCriterionID)
		if id <= 0 {
			continue
		}
		v := "geoTargetConstants/" + strconv.FormatInt(id, 10)
		if segments[c.ExcludeLocation+"\x00"+v] == nil {
			places = append(places, v)
		}
		add(c.ExcludeLocation, v, "", "", r)
	}
	if len(places) > 0 {
		quoted := make([]string, len(places))
		for i, p := range places {
			quoted[i] = "'" + p + "'"
		}
		geo, err := g.search(ctx, cl, creds, acct.ID, login, "SELECT geo_target_constant.resource_name, geo_target_constant.country_code, geo_target_constant.target_type "+
			"FROM geo_target_constant WHERE geo_target_constant.resource_name IN ("+strings.Join(quoted, ", ")+")")
		if err == nil {
			for _, r := range geo {
				if s := segments[c.ExcludeLocation+"\x00"+r.GeoTargetConstant.ResourceName]; s != nil {
					s.Label = strings.ToUpper(r.GeoTargetConstant.CountryCode)
				}
			}
		}
	}
	rows, err = g.search(ctx, cl, creds, acct.ID, login, "SELECT group_placement_view.placement, group_placement_view.placement_type, group_placement_view.display_name, group_placement_view.target_url, "+
		metrics+" FROM group_placement_view WHERE campaign.id = "+campaignID+" AND segments.date BETWEEN '"+f+"' AND '"+t+"'")
	if err != nil && c.KindOf(err) != c.ErrInvalid {
		return out, err
	}
	for _, r := range rows {
		p := r.GroupPlacementView
		value, detail := p.Placement, strings.ToLower(p.PlacementType)
		switch detail {
		case "website":
			if p.TargetURL != "" {
				value = p.TargetURL
			}
		case "mobile_application":
			value = strings.TrimPrefix(value, "mobileapp::")
		case "youtube_channel", "youtube_video":
		default:
			continue
		}
		label := p.DisplayName
		if label == "" {
			label = value
		}
		add(c.ExcludePlacement, value, detail, label, r)
	}
	for _, s := range segments {
		out.Segments = append(out.Segments, *s)
	}
	sort.Slice(out.Segments, func(i, j int) bool {
		if out.Segments[i].Kind != out.Segments[j].Kind {
			return out.Segments[i].Kind < out.Segments[j].Kind
		}
		return out.Segments[i].Value < out.Segments[j].Value
	})
	return out, nil
}

// googleNetwork is a network segment as the exclusions name it.
func googleNetwork(t string) string {
	switch strings.ToUpper(t) {
	case "SEARCH":
		return "search"
	case "SEARCH_PARTNERS":
		return "search_partners"
	case "CONTENT":
		return "display"
	case "YOUTUBE", "YOUTUBE_SEARCH", "YOUTUBE_WATCH":
		return "youtube"
	case "":
		return ""
	}
	return "other"
}
