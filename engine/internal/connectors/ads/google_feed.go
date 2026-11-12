package ads

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The conversion feed on Google Ads (plans/paid-ads/plan.md): Outflare
// makes its three conversion actions (UPLOAD_CLICKS, fed through the Data
// Manager API in datamanager.go), a Customer Match list of paying
// customers marked PURCHASERS, and sets the account's new customer
// acquisition goal: one Goal of type NEW_CUSTOMER_ACQUISITION, and a
// CampaignGoalConfig per campaign that bids higher for new customers
// (TARGET_ALL) or serves new customers only (TARGET_SPECIFIC). Everything
// here is configuration through the Google Ads API with the workspace's
// own sign-in; the Data Manager API carries the data.

var (
	_ c.ConversionFeedManager = GoogleAds{}
	_ c.FeedSender            = GoogleAds{}
	_ c.AudienceFeeder        = GoogleAds{}
	_ c.ReconnectNeeder       = GoogleAds{}
)

// gaqlString quotes a value for a GAQL string literal.
func gaqlString(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// mutateOp posts one mutate operation of any kind to resource (conversionActions,
// userLists, goals ...) and returns the resource name it answers.
func (g GoogleAds) mutateOp(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, resource string, op map[string]any) (string, error) {
	var resp struct {
		Results []struct {
			ResourceName string `json:"resourceName"`
		} `json:"results"`
	}
	err := g.call(cl, creds, acct.Meta["login_customer_id"]).post(ctx, g.base(cl)+"/customers/"+esc(acct.ID)+"/"+resource+":mutate",
		map[string]any{"operations": []any{op}}, &resp)
	if err != nil {
		return "", err
	}
	if len(resp.Results) == 0 {
		return "", nil
	}
	return resp.Results[0].ResourceName, nil
}

var googleCategories = map[string]string{
	c.ConversionSignup:        "SIGNUP",
	c.ConversionPurchase:      "PURCHASE",
	c.ConversionSubscribePaid: "SUBSCRIBE_PAID",
}

// CreateConversionAction makes an UPLOAD_CLICKS conversion action, or
// finds the one with that name, and returns its id.
func (g GoogleAds) CreateConversionAction(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, spec c.ConversionActionSpec) (string, error) {
	category, ok := googleCategories[spec.Category]
	if !ok {
		return "", c.Errf(c.ErrInvalid, "a feed conversion action is a signup, purchase or subscribe_paid")
	}
	if spec.ClickWindowDays < 1 || spec.ClickWindowDays > 90 {
		return "", c.Errf(c.ErrInvalid, "Google Ads counts a conversion 1 to 90 days after the click")
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT conversion_action.id, conversion_action.name, conversion_action.type, conversion_action.status FROM conversion_action WHERE conversion_action.name = "+gaqlString(spec.Name))
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.ConversionAction.Name == spec.Name && r.ConversionAction.Status != "REMOVED" {
			if r.ConversionAction.Type != "" && r.ConversionAction.Type != "UPLOAD_CLICKS" {
				return "", c.Errf(c.ErrInvalid, "Google Ads already has a conversion action named %q that does not take uploaded clicks; rename or remove it", spec.Name)
			}
			return fmt.Sprint(int64(r.ConversionAction.ID)), nil
		}
	}
	counting := "MANY_PER_CLICK"
	if spec.OnePerClick {
		counting = "ONE_PER_CLICK"
	}
	name, err := g.mutateOp(ctx, cl, creds, acct, "conversionActions", map[string]any{"create": map[string]any{
		"name": spec.Name, "type": "UPLOAD_CLICKS", "category": category, "status": "ENABLED",
		"primaryForGoal": spec.Primary, "countingType": counting,
		"clickThroughLookbackWindowDays": strconv.Itoa(spec.ClickWindowDays),
	}})
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(name, "customers/"+acct.ID+"/conversionActions/") {
		return "", c.Errf(c.ErrUnavailable, "Google Ads made no conversion action")
	}
	return lastPart(name), nil
}

// CreateCustomerList makes a Customer Match list of paying customers
// (contact info, the longest membership Google allows) marked as
// PURCHASERS, which the new customer goal reads as existing customers, or
// finds the list with that name, and returns its id.
func (g GoogleAds) CreateCustomerList(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, name string) (string, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT user_list.id, user_list.name FROM user_list WHERE user_list.name = "+gaqlString(name))
	if err != nil {
		return "", err
	}
	id := ""
	for _, r := range rows {
		if r.UserList.Name == name {
			id = fmt.Sprint(int64(r.UserList.ID))
		}
	}
	if id == "" {
		rn, err := g.mutateOp(ctx, cl, creds, acct, "userLists", map[string]any{"create": map[string]any{
			"name": name, "description": "People who paid, kept current by Outflare.",
			"membershipStatus": "OPEN", "membershipLifeSpan": "540",
			"crmBasedUserList": map[string]any{"uploadKeyType": "CONTACT_INFO", "dataSourceType": "FIRST_PARTY"},
		}})
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(rn, "customers/"+acct.ID+"/userLists/") {
			return "", c.Errf(c.ErrUnavailable, "Google Ads made no customer list")
		}
		id = lastPart(rn)
	}
	list := "customers/" + acct.ID + "/userLists/" + id
	types, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT user_list_customer_type.resource_name, user_list_customer_type.customer_type_category FROM user_list_customer_type WHERE user_list_customer_type.user_list = "+gaqlString(list))
	if err != nil {
		return "", err
	}
	for _, r := range types {
		if r.UserListCustomerType.CustomerTypeCategory == "PURCHASERS" {
			return id, nil
		}
	}
	if _, err := g.mutateOp(ctx, cl, creds, acct, "userListCustomerTypes", map[string]any{"create": map[string]any{
		"userList": list, "customerTypeCategory": "PURCHASERS"}}); err != nil {
		return "", err
	}
	return id, nil
}

var googleNCATargets = map[string]string{c.NCABidHigher: "TARGET_ALL", c.NCANewOnly: "TARGET_SPECIFIC"}

// SetNewCustomerGoal sets the account's NEW_CUSTOMER_ACQUISITION goal and
// each campaign's part in it, or takes both away (NCAOff). The additional
// value is in the account's currency (Google takes units, not micros).
func (g GoogleAds) SetNewCustomerGoal(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, goal c.NewCustomerGoal) error {
	target, on := googleNCATargets[goal.Mode]
	if !on && goal.Mode != c.NCAOff {
		return c.Errf(c.ErrInvalid, "nca is %s, %s or %s", c.NCAOff, c.NCABidHigher, c.NCANewOnly)
	}
	for _, id := range goal.CampaignIDs {
		if err := googleCampaignID(id); err != nil {
			return err
		}
	}
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT goal.resource_name, goal.goal_type FROM goal WHERE goal.goal_type = 'NEW_CUSTOMER_ACQUISITION'")
	if err != nil {
		return err
	}
	goalName := ""
	for _, r := range rows {
		if r.Goal.ResourceName != "" {
			goalName = r.Goal.ResourceName
		}
	}
	configs := map[string]string{} // campaign resource name -> config resource name
	if goalName != "" {
		cfgs, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
			"SELECT campaign_goal_config.resource_name, campaign_goal_config.campaign FROM campaign_goal_config WHERE campaign_goal_config.goal = "+gaqlString(goalName))
		if err != nil {
			return err
		}
		for _, r := range cfgs {
			if r.CampaignGoalConfig.ResourceName != "" {
				configs[r.CampaignGoalConfig.Campaign] = r.CampaignGoalConfig.ResourceName
			}
		}
	}
	if !on {
		for _, rn := range configs {
			if _, err := g.mutateOp(ctx, cl, creds, acct, "campaignGoalConfigs", map[string]any{"remove": rn}); err != nil {
				return err
			}
		}
		if goalName != "" {
			if _, err := g.mutateOp(ctx, cl, creds, acct, "goals", map[string]any{"remove": goalName}); err != nil {
				return err
			}
		}
		return nil
	}
	settings := map[string]any{}
	if goal.AdditionalValueMicros > 0 {
		settings["valueSettings"] = map[string]any{"additionalValue": float64(goal.AdditionalValueMicros) / 1e6}
	}
	if goalName == "" {
		rn, err := g.mutateOp(ctx, cl, creds, acct, "goals", map[string]any{"create": map[string]any{"newCustomerAcquisitionGoalSettings": settings}})
		if err != nil {
			return err
		}
		if !strings.HasPrefix(rn, "customers/"+acct.ID+"/goals/") {
			return c.Errf(c.ErrUnavailable, "Google Ads made no new customer acquisition goal")
		}
		goalName = rn
	} else {
		value := map[string]any{"additionalValue": float64(goal.AdditionalValueMicros) / 1e6}
		if _, err := g.mutateOp(ctx, cl, creds, acct, "goals", map[string]any{
			"update":     map[string]any{"resourceName": goalName, "newCustomerAcquisitionGoalSettings": map[string]any{"valueSettings": value}},
			"updateMask": "new_customer_acquisition_goal_settings.value_settings.additional_value",
		}); err != nil {
			return err
		}
	}
	wanted := map[string]bool{}
	for _, id := range goal.CampaignIDs {
		campaign := "customers/" + acct.ID + "/campaigns/" + id
		wanted[campaign] = true
		nca := map[string]any{"targetOption": target}
		if rn, ok := configs[campaign]; ok {
			if _, err := g.mutateOp(ctx, cl, creds, acct, "campaignGoalConfigs", map[string]any{
				"update":     map[string]any{"resourceName": rn, "campaignNewCustomerAcquisitionSettings": nca},
				"updateMask": "campaign_new_customer_acquisition_settings.target_option",
			}); err != nil {
				return err
			}
			continue
		}
		if _, err := g.mutateOp(ctx, cl, creds, acct, "campaignGoalConfigs", map[string]any{"create": map[string]any{
			"campaign": campaign, "goal": goalName, "campaignNewCustomerAcquisitionSettings": nca}}); err != nil {
			return err
		}
	}
	for campaign, rn := range configs {
		if !wanted[campaign] {
			if _, err := g.mutateOp(ctx, cl, creds, acct, "campaignGoalConfigs", map[string]any{"remove": rn}); err != nil {
				return err
			}
		}
	}
	return nil
}
