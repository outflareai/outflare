package ads

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The feed's configuration on Google Ads: three conversion actions and a
// payers list made once (found again by name), and the new customer
// acquisition goal set, changed and removed.
func TestGoogleAdsFeedSetup(t *testing.T) {
	var searches []string
	cl, reqs, _ := fake(t, map[string]any{
		"POST /v23/customers/111/googleAds:search": func(r *http.Request) any {
			var body struct{ Query string }
			json.NewDecoder(r.Body).Decode(&body)
			q := body.Query
			searches = append(searches, q)
			switch {
			case strings.Contains(q, "FROM conversion_action") && strings.Contains(q, "'Outflare sign-up'"):
				return map[string]any{"results": []any{map[string]any{"conversionAction": map[string]any{"id": "42", "name": "Outflare sign-up", "type": "UPLOAD_CLICKS", "status": "ENABLED"}}}}
			case strings.Contains(q, "FROM conversion_action") && strings.Contains(q, "'Taken'"):
				return map[string]any{"results": []any{map[string]any{"conversionAction": map[string]any{"id": "43", "name": "Taken", "type": "WEBPAGE", "status": "ENABLED"}}}}
			case strings.Contains(q, "FROM conversion_action"):
				return map[string]any{"results": []any{}}
			case strings.Contains(q, "FROM user_list_customer_type"):
				return map[string]any{"results": []any{}}
			case strings.Contains(q, "FROM user_list"):
				return map[string]any{"results": []any{}}
			case strings.Contains(q, "FROM goal"):
				return map[string]any{"results": []any{}}
			case strings.Contains(q, "FROM campaign_goal_config"):
				return map[string]any{"results": []any{}}
			}
			return map[string]any{"results": []any{}}
		},
		"POST /v23/customers/111/conversionActions:mutate":     map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/conversionActions/77"}}},
		"POST /v23/customers/111/userLists:mutate":             map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/userLists/501"}}},
		"POST /v23/customers/111/userListCustomerTypes:mutate": map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/userListCustomerTypes/501~PURCHASERS"}}},
		"POST /v23/customers/111/goals:mutate":                 map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/goals/9"}}},
		"POST /v23/customers/111/campaignGoalConfigs:mutate":   map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/campaignGoalConfigs/7~9"}}},
	})
	g, creds, acct := GoogleAds{}, c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111"}

	// An action that exists is found, never made twice.
	id, err := g.CreateConversionAction(ctx, cl, creds, acct, c.ConversionActionSpec{Name: "Outflare sign-up", Category: c.ConversionSignup, ClickWindowDays: 30})
	if err != nil || id != "42" || len(*reqs) != 1 {
		t.Fatalf("existing: %s %v %d", id, err, len(*reqs))
	}
	if _, err := g.CreateConversionAction(ctx, cl, creds, acct, c.ConversionActionSpec{Name: "Taken", Category: c.ConversionPurchase, ClickWindowDays: 90}); err == nil || !strings.Contains(err.Error(), "does not take uploaded clicks") {
		t.Fatalf("taken name: %v", err)
	}
	id, err = g.CreateConversionAction(ctx, cl, creds, acct, c.ConversionActionSpec{Name: "Outflare first payment", Category: c.ConversionSubscribePaid, Primary: true, OnePerClick: true, ClickWindowDays: 90})
	if err != nil || id != "77" {
		t.Fatalf("made: %s %v", id, err)
	}
	made := (*reqs)[len(*reqs)-1].body
	for _, want := range []string{`"type":"UPLOAD_CLICKS"`, `"category":"SUBSCRIBE_PAID"`, `"status":"ENABLED"`, `"primaryForGoal":true`, `"countingType":"ONE_PER_CLICK"`, `"clickThroughLookbackWindowDays":"90"`, `"name":"Outflare first payment"`} {
		if !strings.Contains(made, want) {
			t.Errorf("create lacks %s: %s", want, made)
		}
	}
	if _, err := g.CreateConversionAction(ctx, cl, creds, acct, c.ConversionActionSpec{Name: "x", Category: "lead", ClickWindowDays: 30}); err == nil {
		t.Fatal("an unknown category should be refused")
	}
	if _, err := g.CreateConversionAction(ctx, cl, creds, acct, c.ConversionActionSpec{Name: "x", Category: c.ConversionSignup, ClickWindowDays: 120}); err == nil {
		t.Fatal("a window over 90 days should be refused")
	}

	// The payers list, marked as purchasers.
	list, err := g.CreateCustomerList(ctx, cl, creds, acct, "Outflare paying customers")
	if err != nil || list != "501" {
		t.Fatalf("list: %s %v", list, err)
	}
	all := *reqs
	listBody, typeBody := all[len(all)-3].body, all[len(all)-1].body
	for _, want := range []string{`"crmBasedUserList":{"dataSourceType":"FIRST_PARTY","uploadKeyType":"CONTACT_INFO"}`, `"membershipLifeSpan":"540"`, `"membershipStatus":"OPEN"`, `"name":"Outflare paying customers"`} {
		if !strings.Contains(listBody, want) {
			t.Errorf("list create lacks %s: %s", want, listBody)
		}
	}
	if !strings.Contains(typeBody, `"customerTypeCategory":"PURCHASERS"`) || !strings.Contains(typeBody, `"userList":"customers/111/userLists/501"`) {
		t.Fatalf("customer type %s", typeBody)
	}

	// The goal: made with a value, then one config per campaign.
	before := len(*reqs)
	err = g.SetNewCustomerGoal(ctx, cl, creds, acct, c.NewCustomerGoal{Mode: c.NCABidHigher, AdditionalValueMicros: 25_000_000, CampaignIDs: []string{"7", "8"}})
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, r := range (*reqs)[before:] {
		bodies = append(bodies, r.path+" "+r.body)
	}
	joined := strings.Join(bodies, "\n")
	for _, want := range []string{`goals:mutate {"operations":[{"create":{"newCustomerAcquisitionGoalSettings":{"valueSettings":{"additionalValue":25}}}}]}`,
		`"campaign":"customers/111/campaigns/7"`, `"campaign":"customers/111/campaigns/8"`, `"goal":"customers/111/goals/9"`, `"campaignNewCustomerAcquisitionSettings":{"targetOption":"TARGET_ALL"}`} {
		if !strings.Contains(joined, want) {
			t.Errorf("goal calls lack %s:\n%s", want, joined)
		}
	}
	if err := g.SetNewCustomerGoal(ctx, cl, creds, acct, c.NewCustomerGoal{Mode: "sometimes"}); err == nil {
		t.Fatal("an unknown mode should be refused")
	}
	if err := g.SetNewCustomerGoal(ctx, cl, creds, acct, c.NewCustomerGoal{Mode: c.NCANewOnly, CampaignIDs: []string{"x"}}); err == nil {
		t.Fatal("a campaign id that is not a number should be refused")
	}
}

func TestGoogleAdsFeedGoalChanges(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /v23/customers/111/googleAds:search": func(r *http.Request) any {
			var body struct{ Query string }
			json.NewDecoder(r.Body).Decode(&body)
			switch {
			case strings.Contains(body.Query, "FROM goal"):
				return map[string]any{"results": []any{map[string]any{"goal": map[string]any{"resourceName": "customers/111/goals/9", "goalType": "NEW_CUSTOMER_ACQUISITION"}}}}
			case strings.Contains(body.Query, "FROM campaign_goal_config"):
				return map[string]any{"results": []any{
					map[string]any{"campaignGoalConfig": map[string]any{"resourceName": "customers/111/campaignGoalConfigs/7~9", "campaign": "customers/111/campaigns/7"}},
					map[string]any{"campaignGoalConfig": map[string]any{"resourceName": "customers/111/campaignGoalConfigs/6~9", "campaign": "customers/111/campaigns/6"}}}}
			}
			return map[string]any{"results": []any{}}
		},
		"POST /v23/customers/111/goals:mutate":               map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/goals/9"}}},
		"POST /v23/customers/111/campaignGoalConfigs:mutate": map[string]any{"results": []any{map[string]any{"resourceName": "x"}}},
	})
	g, creds, acct := GoogleAds{}, c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111"}
	// New only on campaigns 7 and 8: 7's config is updated, 8's made, 6's
	// removed, and the goal's value updated.
	if err := g.SetNewCustomerGoal(ctx, cl, creds, acct, c.NewCustomerGoal{Mode: c.NCANewOnly, CampaignIDs: []string{"7", "8"}}); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, r := range *reqs {
		bodies = append(bodies, r.path+" "+r.body)
	}
	joined := strings.Join(bodies, "\n")
	for _, want := range []string{`"updateMask":"new_customer_acquisition_goal_settings.value_settings.additional_value"`,
		`{"update":{"campaignNewCustomerAcquisitionSettings":{"targetOption":"TARGET_SPECIFIC"},"resourceName":"customers/111/campaignGoalConfigs/7~9"},"updateMask":"campaign_new_customer_acquisition_settings.target_option"}`,
		`{"create":{"campaign":"customers/111/campaigns/8","campaignNewCustomerAcquisitionSettings":{"targetOption":"TARGET_SPECIFIC"},"goal":"customers/111/goals/9"}}`,
		`{"remove":"customers/111/campaignGoalConfigs/6~9"}`} {
		if !strings.Contains(joined, want) {
			t.Errorf("calls lack %s:\n%s", want, joined)
		}
	}
	// Off: every config goes, then the goal.
	*reqs = nil
	if err := g.SetNewCustomerGoal(ctx, cl, creds, acct, c.NewCustomerGoal{Mode: c.NCAOff}); err != nil {
		t.Fatal(err)
	}
	bodies = nil
	for _, r := range *reqs {
		bodies = append(bodies, r.path+" "+r.body)
	}
	joined = strings.Join(bodies, "\n")
	if !strings.Contains(joined, `{"remove":"customers/111/campaignGoalConfigs/7~9"}`) || !strings.Contains(joined, `{"remove":"customers/111/campaignGoalConfigs/6~9"}`) ||
		!strings.HasSuffix(strings.TrimSpace(joined), `goals:mutate {"operations":[{"remove":"customers/111/goals/9"}]}`) {
		t.Fatalf("off calls:\n%s", joined)
	}
}
