package ads

import (
	"context"
	"fmt"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Microsoft Advertising is search like Google Ads, so it reads keywords and
// the searches they matched, changes them, pauses ad groups, and changes a
// conversion after a refund. The routes are the Campaign Management REST
// API's: a list of one kind of thing is added with POST, changed with PUT
// and taken away with DELETE on the same path.

var (
	_ c.AdKeywordsReader   = MicrosoftAds{}
	_ c.AdKeywordManager   = MicrosoftAds{}
	_ c.AdKeywordEditor    = MicrosoftAds{}
	_ c.AdGroupManager     = MicrosoftAds{}
	_ c.ConversionAdjuster = MicrosoftAds{}
)

func (m MicrosoftAds) campaignBase(cl *c.Client) string {
	return cl.Base("microsoft_ads.campaign", msCampaignBase)
}

// msMatch is Microsoft's word for a match type; "" when it isn't one.
func msMatch(matchType string) string {
	switch strings.ToLower(strings.TrimSpace(matchType)) {
	case "exact":
		return "Exact"
	case "phrase":
		return "Phrase"
	case "broad":
		return "Broad"
	}
	return ""
}

// msPartial turns the first partial error of a change into an error.
type msPartial struct {
	PartialErrors []struct {
		Message string `json:"Message"`
	} `json:"PartialErrors"`
	NestedPartialErrors []struct {
		Message     string `json:"Message"`
		BatchErrors []struct {
			Message string `json:"Message"`
		} `json:"BatchErrors"`
	} `json:"NestedPartialErrors"`
}

func (p msPartial) err() error {
	for _, e := range p.PartialErrors {
		if e.Message != "" {
			return c.Errf(c.ErrInvalid, "Microsoft Advertising refused the change: %s", e.Message)
		}
	}
	for _, n := range p.NestedPartialErrors {
		for _, e := range n.BatchErrors {
			if e.Message != "" {
				return c.Errf(c.ErrInvalid, "Microsoft Advertising refused the change: %s", e.Message)
			}
		}
		if n.Message != "" {
			return c.Errf(c.ErrInvalid, "Microsoft Advertising refused the change: %s", n.Message)
		}
	}
	return nil
}

// ---- keywords and searches ----

// AdKeywordDays reads the keyword performance report by day.
func (m MicrosoftAds) AdKeywordDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdKeywordDay, error) {
	raw, err := m.report(ctx, cl, creds, acct, map[string]any{
		"Type": "KeywordPerformanceReportRequest", "Aggregation": "Daily",
		"Columns": []string{"TimePeriod", "AdGroupId", "KeywordId", "Keyword", "BidMatchType", "KeywordStatus", "Spend", "Impressions", "Clicks", "Conversions", "Revenue"},
		"Time":    map[string]any{"CustomDateRangeStart": msDate(from), "CustomDateRangeEnd": msDate(to)},
	})
	if err != nil || raw == nil {
		return nil, err
	}
	rows, err := msRows(raw)
	if err != nil {
		return nil, err
	}
	// The report has a row per match type a keyword was delivered as, and
	// may split a day further; a keyword's day is their sum.
	at := map[string]int{}
	var out []c.AdKeywordDay
	for _, r := range rows {
		if _, err := time.Parse(time.DateOnly, r["TimePeriod"]); err != nil || r["KeywordId"] == "" || r["AdGroupId"] == "" {
			continue // a footer or a blank line
		}
		d := c.AdKeywordDay{ID: r["KeywordId"], AdGroupID: r["AdGroupId"], Text: r["Keyword"], MatchType: strings.ToLower(r["BidMatchType"]),
			Status: status(r["KeywordStatus"]), Date: r["TimePeriod"],
			SpendMicros: c.Micros(msNum(r["Spend"])), Impressions: int64(msNum(r["Impressions"])), Clicks: int64(msNum(r["Clicks"])),
			Conversions: ptrF(msNum(r["Conversions"])), ConversionValueMicros: ptrI(c.Micros(msNum(r["Revenue"])))}
		k := d.AdGroupID + "|" + d.ID + "|" + d.Date
		if i, ok := at[k]; ok {
			o := &out[i]
			o.SpendMicros += d.SpendMicros
			o.Impressions += d.Impressions
			o.Clicks += d.Clicks
			*o.Conversions += *d.Conversions
			*o.ConversionValueMicros += *d.ConversionValueMicros
			continue
		}
		at[k] = len(out)
		out = append(out, d)
	}
	return out, nil
}

// AdSearchTermDays reads the search query performance report by day.
func (m MicrosoftAds) AdSearchTermDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdSearchTermDay, error) {
	raw, err := m.report(ctx, cl, creds, acct, map[string]any{
		"Type": "SearchQueryPerformanceReportRequest", "Aggregation": "Daily",
		"Columns": []string{"TimePeriod", "AdGroupId", "SearchQuery", "Keyword", "BidMatchType", "DeliveredMatchType", "Spend", "Impressions", "Clicks", "Conversions", "Revenue"},
		"Time":    map[string]any{"CustomDateRangeStart": msDate(from), "CustomDateRangeEnd": msDate(to)},
	})
	if err != nil || raw == nil {
		return nil, err
	}
	rows, err := msRows(raw)
	if err != nil {
		return nil, err
	}
	at := map[string]int{}
	var out []c.AdSearchTermDay
	for _, r := range rows {
		if _, err := time.Parse(time.DateOnly, r["TimePeriod"]); err != nil || r["SearchQuery"] == "" || r["AdGroupId"] == "" {
			continue
		}
		d := c.AdSearchTermDay{Term: r["SearchQuery"], AdGroupID: r["AdGroupId"], KeywordText: r["Keyword"],
			KeywordMatchType: strings.ToLower(r["BidMatchType"]), MatchType: strings.ToLower(r["DeliveredMatchType"]), Date: r["TimePeriod"],
			SpendMicros: c.Micros(msNum(r["Spend"])), Impressions: int64(msNum(r["Impressions"])), Clicks: int64(msNum(r["Clicks"])),
			Conversions: ptrF(msNum(r["Conversions"])), ConversionValueMicros: ptrI(c.Micros(msNum(r["Revenue"])))}
		k := d.AdGroupID + "|" + d.Term + "|" + d.KeywordText + "|" + d.KeywordMatchType + "|" + d.MatchType + "|" + d.Date
		if i, ok := at[k]; ok {
			o := &out[i]
			o.SpendMicros += d.SpendMicros
			o.Impressions += d.Impressions
			o.Clicks += d.Clicks
			*o.Conversions += *d.Conversions
			*o.ConversionValueMicros += *d.ConversionValueMicros
			continue
		}
		at[k] = len(out)
		out = append(out, d)
	}
	return out, nil
}

// ---- changing keywords ----

// AddKeyword adds an active keyword to an ad group.
func (m MicrosoftAds) AddKeyword(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, text, matchType string) error {
	group, mt := atoi64(adGroupID), msMatch(matchType)
	if group <= 0 || mt == "" || strings.TrimSpace(text) == "" {
		return c.Errf(c.ErrInvalid, "a keyword needs a numeric ad group, its text and a match type of exact, phrase or broad")
	}
	var resp msPartial
	body := map[string]any{"AdGroupId": group, "Keywords": []any{map[string]any{"Text": strings.TrimSpace(text), "MatchType": mt, "Status": "Active"}}}
	if err := m.scoped(cl, creds, acct).post(ctx, m.campaignBase(cl)+"/Keywords", body, &resp); err != nil {
		return err
	}
	return resp.err()
}

// negativeEntity names where a negative keyword sits: an ad group in its
// campaign, or a campaign in the account.
func negativeEntity(acct c.AdAccount, campaignID, adGroupID string) (id int64, kind string, parent int64, err error) {
	camp := atoi64(campaignID)
	if adGroupID != "" {
		if id = atoi64(adGroupID); id <= 0 || camp <= 0 {
			return 0, "", 0, c.Errf(c.ErrInvalid, "Microsoft Advertising needs the ad group's and its campaign's numeric ids")
		}
		return id, "AdGroup", camp, nil
	}
	if camp <= 0 {
		return 0, "", 0, c.Errf(c.ErrInvalid, "Microsoft Advertising campaign ids are numbers")
	}
	return camp, "Campaign", atoi64(acct.ID), nil
}

// AddNegativeKeyword stops ads showing for a search in a campaign or one
// ad group. Microsoft has no broad negatives: broad is added as phrase.
func (m MicrosoftAds) AddNegativeKeyword(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, adGroupID, text, matchType string) error {
	id, kind, _, err := negativeEntity(acct, campaignID, adGroupID)
	if err != nil {
		return err
	}
	mt := msNegativeMatch(matchType)
	if mt == "" || strings.TrimSpace(text) == "" {
		return c.Errf(c.ErrInvalid, "a negative keyword needs its text and a match type of exact or phrase")
	}
	var resp msPartial
	body := map[string]any{"EntityNegativeKeywords": []any{map[string]any{"EntityId": id, "EntityType": kind,
		"NegativeKeywords": []any{map[string]any{"Text": strings.TrimSpace(text), "MatchType": mt}}}}}
	if err := m.scoped(cl, creds, acct).post(ctx, m.campaignBase(cl)+"/EntityNegativeKeywords", body, &resp); err != nil {
		return err
	}
	return resp.err()
}

func msNegativeMatch(matchType string) string {
	switch mt := msMatch(matchType); mt {
	case "Broad":
		return "Phrase"
	default:
		return mt
	}
}

// RemoveNegativeKeyword takes a negative keyword out again. One that
// isn't there succeeds.
func (m MicrosoftAds) RemoveNegativeKeyword(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, campaignID, adGroupID, text, matchType string) error {
	id, kind, parent, err := negativeEntity(acct, campaignID, adGroupID)
	if err != nil {
		return err
	}
	mt := msNegativeMatch(matchType)
	var list struct {
		EntityNegativeKeywords []struct {
			NegativeKeywords []struct {
				ID        flexInt `json:"Id"`
				MatchType string  `json:"MatchType"`
				Text      string  `json:"Text"`
			} `json:"NegativeKeywords"`
		} `json:"EntityNegativeKeywords"`
	}
	if err := m.scoped(cl, creds, acct).post(ctx, m.campaignBase(cl)+"/NegativeKeywords/QueryByEntityIds",
		map[string]any{"EntityIds": []int64{id}, "EntityType": kind, "ParentEntityId": parent}, &list); err != nil {
		return err
	}
	var ids []any
	for _, e := range list.EntityNegativeKeywords {
		for _, k := range e.NegativeKeywords {
			if k.ID != 0 && strings.EqualFold(strings.TrimSpace(k.Text), strings.TrimSpace(text)) && (mt == "" || strings.EqualFold(k.MatchType, mt)) {
				ids = append(ids, map[string]any{"Id": int64(k.ID), "Text": k.Text, "MatchType": k.MatchType})
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var resp msPartial
	body := map[string]any{"EntityNegativeKeywords": []any{map[string]any{"EntityId": id, "EntityType": kind, "NegativeKeywords": ids}}}
	if err := m.scoped(cl, creds, acct).do(ctx, "DELETE", m.campaignBase(cl)+"/EntityNegativeKeywords", body, &resp); err != nil {
		return err
	}
	return resp.err()
}

// SetKeywordStatus pauses or resumes one keyword.
func (m MicrosoftAds) SetKeywordStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID, keywordID string, active bool) error {
	group, id := atoi64(adGroupID), atoi64(keywordID)
	if group <= 0 || id <= 0 {
		return c.Errf(c.ErrInvalid, "Microsoft Advertising ad group and keyword ids are numbers")
	}
	var resp msPartial
	body := map[string]any{"AdGroupId": group, "Keywords": []any{map[string]any{"Id": id, "Status": msStatus(active)}}}
	if err := m.scoped(cl, creds, acct).put(ctx, m.campaignBase(cl)+"/Keywords", body, &resp); err != nil {
		return err
	}
	return resp.err()
}

func msStatus(active bool) string {
	if active {
		return "Active"
	}
	return "Paused"
}

// SplitKeyword isn't offered: Microsoft copies ads one type at a time, so
// a keyword gets its own ad group in Microsoft Advertising itself.
func (MicrosoftAds) SplitKeyword(context.Context, *c.Client, c.Creds, c.AdAccount, string, string, string, string) (string, error) {
	return "", c.Errf(c.ErrInvalid, "Outflare can't give a keyword its own ad group on Microsoft Advertising yet; do it in Microsoft Advertising")
}

// Recommendations is empty: Outflare reads none of Microsoft's own.
func (MicrosoftAds) Recommendations(context.Context, *c.Client, c.Creds, c.AdAccount) ([]c.AdRecommendation, error) {
	return []c.AdRecommendation{}, nil
}

// ---- ad groups ----

// SetAdGroupStatus pauses or resumes one ad group. A change names the ad
// group's campaign, which is looked up campaign by campaign.
func (m MicrosoftAds) SetAdGroupStatus(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, adGroupID string, active bool) error {
	id := atoi64(adGroupID)
	if id <= 0 {
		return c.Errf(c.ErrInvalid, "Microsoft Advertising ad group ids are numbers")
	}
	groups, err := m.AdItems(ctx, cl, creds, acct, c.AdLevelGroup)
	if err != nil {
		return err
	}
	var campaign int64
	for _, g := range groups {
		if g.ID == fmt.Sprint(id) {
			campaign = atoi64(g.ParentID)
			break
		}
	}
	if campaign <= 0 {
		return c.Errf(c.ErrInvalid, "Microsoft Advertising has no such ad group in this account")
	}
	var resp msPartial
	body := map[string]any{"CampaignId": campaign, "AdGroups": []any{map[string]any{"Id": id, "Status": msStatus(active)}}}
	if err := m.scoped(cl, creds, acct).put(ctx, m.campaignBase(cl)+"/AdGroups", body, &resp); err != nil {
		return err
	}
	return resp.err()
}

// ---- refunds ----

// AdjustConversions restates or retracts offline conversions. Microsoft
// finds a conversion by its goal, its time and the click id (or hashed
// email or phone) it was sent with, not by an id of the conversion.
func (m MicrosoftAds) AdjustConversions(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, adjs []c.AdConversionAdjustment) ([]c.ConversionResult, error) {
	results := make([]c.ConversionResult, len(adjs))
	var rows []map[string]any
	var sentIdx []int
	for i, a := range adjs {
		results[i] = c.ConversionResult{Index: a.Index}
		if a.ConversionTime.IsZero() {
			results[i].Skipped, results[i].Reason = true, "the conversion's own time is not known"
			continue
		}
		row := map[string]any{"ConversionName": target.ID, "ConversionTime": a.ConversionTime.UTC().Format("2006-01-02T15:04:05Z"),
			"AdjustmentTime": a.Time.UTC().Format("2006-01-02T15:04:05Z"), "AdjustmentType": "Retract"}
		switch {
		case a.ClickIDs["msclkid"] != "":
			row["MicrosoftClickId"] = a.ClickIDs["msclkid"]
		case len(a.Emails) > 0:
			row["HashedEmailAddress"] = a.Emails[0]
		case len(a.Phones) > 0:
			row["HashedPhoneNumber"] = a.Phones[0]
		default:
			results[i].Skipped, results[i].Reason = true, "no Microsoft click id, email or phone to find the conversion by"
			continue
		}
		if !a.Retract {
			row["AdjustmentType"] = "Restate"
			row["AdjustmentValue"] = float64(a.ValueMicros) / 1e6
			row["AdjustmentCurrencyCode"] = a.Currency
		}
		rows = append(rows, row)
		sentIdx = append(sentIdx, i)
	}
	if len(rows) == 0 {
		return results, nil
	}
	var resp struct {
		PartialErrors []struct {
			Index   *int   `json:"Index"`
			Message string `json:"Message"`
		} `json:"PartialErrors"`
	}
	if err := m.scoped(cl, creds, acct).post(ctx, m.campaignBase(cl)+"/OfflineConversionAdjustments/Apply",
		map[string]any{"OfflineConversionAdjustments": rows}, &resp); err != nil {
		return nil, err
	}
	failed := map[int]string{}
	for _, e := range resp.PartialErrors {
		if e.Index != nil {
			failed[*e.Index] = e.Message
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
