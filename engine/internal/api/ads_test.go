package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// fakeMetaAds answers the Marketing API for the token "mt": one account
// with one campaign that spends 25.40 USD on every day asked for.
type fakeMetaAds struct{}

func (fakeMetaAds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer mt" {
		w.WriteHeader(401)
		return
	}
	var out any
	switch r.URL.Path {
	case "/v24.0/me":
		out = map[string]any{"id": "1", "name": "Northpeak ads"}
	case "/v24.0/me/adaccounts":
		out = map[string]any{"data": []any{map[string]any{"account_id": "55", "name": "Northpeak", "currency": "USD", "timezone_name": "UTC"}}}
	case "/v24.0/act_55/campaigns":
		out = map[string]any{"data": []any{
			map[string]any{"id": "9", "name": "Prospecting", "effective_status": "ACTIVE", "objective": "OUTCOME_LEADS", "daily_budget": "3000"},
			map[string]any{"id": "10", "name": "Old test", "effective_status": "PAUSED", "daily_budget": "500"},
		}}
	case "/v24.0/act_55":
		out = map[string]any{"id": "act_55", "business": map[string]any{"id": "600"}}
	case "/v24.0/9/copies":
		metaState.Lock()
		metaState.posts = append(metaState.posts, "COPY 9")
		metaState.Unlock()
		out = map[string]any{"copied_campaign_id": "90"}
	case "/v24.0/600/ad_studies", "/v24.0/700":
		b, _ := io.ReadAll(r.Body)
		metaState.Lock()
		metaState.posts = append(metaState.posts, "STUDY "+strings.TrimPrefix(r.URL.Path, "/v24.0/")+" "+string(b))
		metaState.Unlock()
		out = map[string]any{"id": "700", "success": true}
	case "/v24.0/9", "/v24.0/10", "/v24.0/90":
		// Managing: a POST changes the campaign's state, which a GET then
		// shows (campaign 9 starts active at $30 a day, 10 paused).
		id := strings.TrimPrefix(r.URL.Path, "/v24.0/")
		metaState.Lock()
		defer metaState.Unlock()
		if metaState.campaigns == nil {
			metaState.campaigns = map[string]map[string]any{
				"9":  {"id": "9", "name": "Prospecting", "effective_status": "ACTIVE", "objective": "OUTCOME_LEADS", "daily_budget": "3000", "account_id": "55"},
				"10": {"id": "10", "name": "Old test", "effective_status": "PAUSED", "objective": "OUTCOME_LEADS", "daily_budget": "500", "account_id": "55"},
				"90": {"id": "90", "name": "Prospecting (test)", "effective_status": "PAUSED", "objective": "OUTCOME_LEADS", "daily_budget": "3000", "account_id": "55"},
			}
		}
		cp := metaState.campaigns[id]
		if r.Method == "POST" {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if metaState.refuse {
				w.WriteHeader(400)
				out = map[string]any{"error": map[string]any{"message": "Invalid parameter", "code": 100}}
				break
			}
			if st := body["status"]; st != "" {
				cp["effective_status"] = st
			}
			if b := body["daily_budget"]; b != "" {
				cp["daily_budget"] = b
			}
			metaState.posts = append(metaState.posts, r.Method+" "+id+" "+body["status"]+body["daily_budget"])
			out = map[string]any{"success": true}
			break
		}
		out = cp
	case "/v24.0/9/adsets":
		metaState.Lock()
		if metaState.targeting == nil {
			metaState.targeting = map[string]any{"geo_locations": map[string]any{"countries": []any{"AU"}, "location_types": []any{"home"}}, "age_min": 25.0}
		}
		out = map[string]any{"data": []any{map[string]any{"id": "31", "effective_status": "ACTIVE", "targeting": metaState.targeting}}}
		metaState.Unlock()
	case "/v24.0/31":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		metaState.Lock()
		var t map[string]any
		json.Unmarshal([]byte(body["targeting"]), &t)
		metaState.targeting = t
		metaState.posts = append(metaState.posts, "TARGETING 31 "+body["targeting"])
		metaState.Unlock()
		out = map[string]any{"success": true}
	case "/v24.0/act_55/adsets":
		out = map[string]any{"data": []any{map[string]any{"id": "31", "name": "Lookalike", "effective_status": "ACTIVE", "campaign_id": "9"}}}
	case "/v24.0/act_55/adcreatives", "/v24.0/42":
		b, _ := io.ReadAll(r.Body)
		metaState.Lock()
		metaState.posts = append(metaState.posts, "POST "+strings.TrimPrefix(r.URL.Path, "/v24.0/")+" "+string(b))
		metaState.Unlock()
		out = map[string]any{"id": "551", "success": true}
	case "/v24.0/act_55/promote_pages":
		out = map[string]any{"data": []any{map[string]any{"id": "880"}}}
	case "/v24.0/act_55/ads":
		if r.Method == "POST" {
			b, _ := io.ReadAll(r.Body)
			metaState.Lock()
			metaState.posts = append(metaState.posts, "POST act_55/ads "+string(b))
			metaState.Unlock()
			out = map[string]any{"id": "42"}
			break
		}
		out = map[string]any{"data": []any{map[string]any{"id": "41", "name": "Demo video", "effective_status": "ACTIVE", "adset_id": "31",
			"creative": map[string]any{"object_story_spec": map[string]any{"link_data": map[string]any{"link": "https://acme.example/demo", "name": "Book a demo"}}}}}}
	case "/v24.0/act_55/adspixels":
		out = map[string]any{"data": []any{map[string]any{"id": "777", "name": "Northpeak pixel"}}}
	case "/v24.0/777/events":
		var body struct {
			Data []map[string]any `json:"data"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		metaSent.Lock()
		metaSent.events = append(metaSent.events, body.Data...)
		metaSent.Unlock()
		out = map[string]any{"events_received": len(body.Data)}
	case "/v24.0/act_55/customaudiences":
		out = map[string]any{"data": []any{map[string]any{"id": "801", "name": "Trial users", "subtype": "CUSTOM"},
			map[string]any{"id": "803", "name": "Site visitors", "subtype": "WEBSITE"}}}
	case "/v24.0/801/users":
		var body struct {
			Payload struct {
				Schema []string   `json:"schema"`
				Data   [][]string `json:"data"`
			} `json:"payload"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		metaAudience.Lock()
		for _, row := range body.Payload.Data {
			metaAudience.calls = append(metaAudience.calls, r.Method+" "+body.Payload.Schema[0]+" "+row[0])
		}
		metaAudience.Unlock()
		out = map[string]any{"audience_id": "801", "num_received": len(body.Payload.Data)}
	case "/v24.0/802/users":
		w.WriteHeader(400)
		out = map[string]any{"error": map[string]any{"message": "Custom audience terms not accepted", "code": 2650}}
	case "/v24.0/act_55/insights":
		var tr struct{ Since, Until string }
		json.Unmarshal([]byte(r.URL.Query().Get("time_range")), &tr)
		rows := []any{}
		from, _ := time.Parse(time.DateOnly, tr.Since)
		to, _ := time.Parse(time.DateOnly, tr.Until)
		// The one ad set, and its one ad, are all of the campaign.
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			row := map[string]any{"campaign_id": "9", "campaign_name": "Prospecting", "date_start": d.Format(time.DateOnly),
				"spend": "25.40", "impressions": "1000", "clicks": "20",
				"reach": "800", "actions": []any{map[string]any{"action_type": "lead", "value": "2"}, map[string]any{"action_type": "offsite_conversion.fb_pixel_lead", "value": "2"},
					map[string]any{"action_type": "offsite_conversion.custom.555", "value": "1"}, map[string]any{"action_type": "link_click", "value": "15"},
					map[string]any{"action_type": "landing_page_view", "value": "12"}, map[string]any{"action_type": "video_view", "value": "40"}},
				"action_values": []any{map[string]any{"action_type": "lead", "value": "60"}}}
			switch r.URL.Query().Get("level") {
			case "adset":
				row["adset_id"], row["adset_name"] = "31", "Lookalike"
			case "ad":
				row["ad_id"], row["ad_name"], row["adset_id"] = "41", "Demo video", "31"
			}
			rows = append(rows, row)
		}
		out = map[string]any{"data": rows}
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
}

// metaState is what fakeMetaAds' campaigns look like after changes, and
// the changes it took; refuse makes it refuse the next ones.
var metaState struct {
	sync.Mutex
	campaigns map[string]map[string]any
	posts     []string
	refuse    bool
	// targeting is ad set 31's, as changes leave it.
	targeting map[string]any
}

// metaAudience collects what fakeMetaAds took into audience 801:
// "METHOD SCHEMA hash".
var metaAudience struct {
	sync.Mutex
	calls []string
}

// metaSent collects the events fakeMetaAds took through the Conversions API.
var metaSent struct {
	sync.Mutex
	events []map[string]any
}

// googleSuffix is fakeGoogleAds' URL suffixes: the account's and campaign
// 7's (which has one of its own until a test changes it), and the mutate
// requests it took. refuse makes it refuse campaign updates.
var googleSuffix = struct {
	sync.Mutex
	account, campaign string
	mutates           []string
	// uploads are the conversions and adjustments sent, path and body.
	uploads []string
	refuse  bool
}{campaign: "gad_campaignid={campaignid}"}

// fakeGoogleAds refuses a developer token other than "dev-ok". Google
// retired the tokens, so a call without one goes through.
type fakeGoogleAds struct{}

func (fakeGoogleAds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if tok := r.Header.Get("developer-token"); tok != "" && tok != "dev-ok" {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":{"message":"DEVELOPER_TOKEN_INVALID"}}`))
		return
	}
	switch r.URL.Path {
	case "/v23/customers/123/adGroupCriteria:mutate", "/v23/customers/123/campaignCriteria:mutate", "/v23/customers/123/adGroups:mutate", "/v23/customers/123/adGroupAds:mutate",
		"/v23/customers/123/experiments:mutate", "/v23/customers/123/experimentArms:mutate",
		"/v23/customers/123/experiments/9:scheduleExperiment", "/v23/customers/123/experiments/9:endExperiment", "/v23/customers/123/experiments/9:promoteExperiment":
		b, _ := io.ReadAll(r.Body)
		googleSuffix.Lock()
		googleSuffix.mutates = append(googleSuffix.mutates, r.URL.Path+" "+string(b))
		googleSuffix.Unlock()
		name := "customers/123/x/1"
		switch {
		case strings.HasSuffix(r.URL.Path, "/adGroups:mutate"):
			name = "customers/123/adGroups/72"
		case strings.HasSuffix(r.URL.Path, "/experiments:mutate"):
			name = "customers/123/experiments/9"
		case strings.HasSuffix(r.URL.Path, "/experimentArms:mutate"):
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"resourceName": "customers/123/experimentArms/9~1"},
				map[string]any{"resourceName": "customers/123/experimentArms/9~2", "experimentArm": map[string]any{"inDesignCampaigns": []string{"customers/123/campaigns/7001"}}}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"resourceName": name}}})
	case "/v23/customers/123/googleAds:mutate":
		// New ads: one batch of operations, answered in order with the
		// resource names made (or updated).
		b, _ := io.ReadAll(r.Body)
		googleSuffix.Lock()
		googleSuffix.mutates = append(googleSuffix.mutates, r.URL.Path+" "+string(b))
		googleSuffix.Unlock()
		var body struct {
			Ops []map[string]map[string]any `json:"mutateOperations"`
		}
		json.Unmarshal(b, &body)
		made := map[string]string{"campaignBudgets/-1": "campaignBudgets/501", "campaigns/-2": "campaigns/7002", "adGroups/-3": "adGroups/73"}
		var res []any
		for _, op := range body.Ops {
			for k, v := range op {
				result := strings.TrimSuffix(k, "Operation") + "Result"
				if u, ok := v["update"].(map[string]any); ok {
					res = append(res, map[string]any{result: map[string]any{"resourceName": u["resourceName"]}})
					continue
				}
				c := v["create"].(map[string]any)
				name, _ := c["resourceName"].(string)
				name = strings.TrimPrefix(name, "customers/123/")
				if m, ok := made[name]; ok {
					name = m
				}
				switch k {
				case "campaignCriterionOperation":
					name = "campaignCriteria/7002~2036"
				case "adGroupCriterionOperation":
					name = "adGroupCriteria/73~1"
				case "adGroupAdOperation":
					g := strings.TrimPrefix(c["adGroup"].(string), "customers/123/adGroups/")
					if g == "-3" {
						g = "73"
					}
					name = "adGroupAds/" + g + "~91"
				}
				res = append(res, map[string]any{result: map[string]any{"resourceName": "customers/123/" + name}})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"mutateOperationResponses": res})
	case "/v23/customers:listAccessibleCustomers":
		json.NewEncoder(w).Encode(map[string]any{"resourceNames": []string{"customers/123"}})
	case "/v23/customers/123:mutate", "/v23/customers/123/campaigns:mutate":
		var body struct {
			Operation  map[string]any   `json:"operation"`
			Operations []map[string]any `json:"operations"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		googleSuffix.Lock()
		defer googleSuffix.Unlock()
		if body.Operation != nil {
			body.Operations = append(body.Operations, body.Operation)
		}
		for _, op := range body.Operations {
			u, _ := op["update"].(map[string]any)
			b, _ := json.Marshal(op)
			googleSuffix.mutates = append(googleSuffix.mutates, r.URL.Path+" "+string(b))
			switch {
			case u["resourceName"] == "customers/123":
				googleSuffix.account = u["finalUrlSuffix"].(string)
			case googleSuffix.refuse:
				w.WriteHeader(400)
				w.Write([]byte(`{"error":{"message":"FIELD_NOT_MUTABLE"}}`))
				return
			case u["resourceName"] == "customers/123/campaigns/7" && op["updateMask"] == "final_url_suffix":
				googleSuffix.campaign = u["finalUrlSuffix"].(string)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	case "/v23/customers/123/googleAds:search":
		b, _ := io.ReadAll(r.Body)
		q := string(b)
		googleSuffix.Lock()
		accountSuffix, campaignSuffix := googleSuffix.account, googleSuffix.campaign
		googleSuffix.Unlock()
		if strings.Contains(q, "FROM customer\"") {
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"customer": map[string]any{"id": "123", "descriptiveName": "Acme search", "currencyCode": "EUR", "timeZone": "Pacific/Kiritimati",
				"finalUrlSuffix": accountSuffix}}}})
			return
		}
		// One campaign, ad group, ad and keyword, with numbers on the last
		// day asked for.
		day := ""
		if m := regexp.MustCompile(`AND '([0-9-]{10})'`).FindStringSubmatch(q); m != nil {
			day = m[1]
		}
		metrics := map[string]any{"costMicros": "9000000", "impressions": "300", "clicks": "12", "conversions": 2.0, "conversionsValue": 80.0}
		row := func(r map[string]any) map[string]any {
			r["segments"], r["metrics"] = map[string]any{"date": day}, metrics
			return r
		}
		camp := map[string]any{"id": "7", "name": "Brand search", "status": "ENABLED", "advertisingChannelType": "SEARCH"}
		group := map[string]any{"id": "71", "name": "Exact brand", "status": "ENABLED"}
		var out []any
		switch {
		case strings.Contains(q, "FROM experiment_arm"):
			out = []any{map[string]any{"experimentArm": map[string]any{"control": true, "campaigns": []string{"customers/123/campaigns/7"}}},
				map[string]any{"experimentArm": map[string]any{"control": false, "campaigns": []string{"customers/123/campaigns/7001"}}}}
		case strings.Contains(q, "campaign.id IN ("):
			// The experiment's arms: the trial converts more for the same money.
			out = []any{map[string]any{"campaign": map[string]any{"id": "7"}, "segments": map[string]any{"date": day},
				"metrics": map[string]any{"costMicros": "500000000", "impressions": "9000", "clicks": "400", "conversions": 50.0}},
				map[string]any{"campaign": map[string]any{"id": "7001"}, "segments": map[string]any{"date": day},
					"metrics": map[string]any{"costMicros": "500000000", "impressions": "9100", "clicks": "410", "conversions": 80.0}}}
		case strings.Contains(q, "FROM campaign_criterion"):
			out = []any{map[string]any{"campaignCriterion": map[string]any{"resourceName": "customers/123/campaignCriteria/7~501", "type": "USER_LIST",
				"userList": map[string]any{"userList": "customers/123/userLists/501"}}},
				map[string]any{"campaignCriterion": map[string]any{"resourceName": "customers/123/campaignCriteria/7~2036", "type": "LOCATION",
					"location": map[string]any{"geoTargetConstant": "geoTargetConstants/2036"}}}}
		case strings.Contains(q, "FROM geo_target_constant") && strings.Contains(q, "country_code = 'NZ'"):
			out = []any{map[string]any{"geoTargetConstant": map[string]any{"resourceName": "geoTargetConstants/2554", "countryCode": "NZ", "targetType": "Country"}}}
		case strings.Contains(q, "FROM geo_target_constant"):
			out = []any{map[string]any{"geoTargetConstant": map[string]any{"resourceName": "geoTargetConstants/2036", "countryCode": "AU", "targetType": "Country"}}}
		case strings.Contains(q, "FROM user_list"):
			out = []any{map[string]any{"userList": map[string]any{"id": "501", "name": "Trial users"}}}
		case strings.Contains(q, "FROM recommendation"):
			out = []any{map[string]any{"recommendation": map[string]any{"resourceName": "customers/123/recommendations/1", "type": "KEYWORD", "campaign": "customers/123/campaigns/7",
				"impact": map[string]any{"baseMetrics": map[string]any{"clicks": 10.0}, "potentialMetrics": map[string]any{"clicks": 14.0}}}}}
		case strings.Contains(q, "responsive_search_ad.path1"):
			hl := []any{map[string]any{"text": "Acme CRM"}, map[string]any{"text": "Simple sales"}, map[string]any{"text": "Try it free"}}
			ds := []any{map[string]any{"text": "A CRM for small teams."}, map[string]any{"text": "Set up in minutes."}}
			out = []any{map[string]any{"adGroupAd": map[string]any{"ad": map[string]any{"finalUrls": []string{"https://acme.example/"},
				"responsiveSearchAd": map[string]any{"headlines": hl, "descriptions": ds, "path1": "crm"}}}}}
		case strings.Contains(q, "conversion_action_name"):
			id := map[string]any{"campaign": map[string]any{"id": "7"}, "adGroup": map[string]any{"id": "71"}, "adGroupAd": map[string]any{"ad": map[string]any{"id": "81"}}}
			id["segments"] = map[string]any{"date": day, "conversionActionName": "Demo booked"}
			id["metrics"] = map[string]any{"conversions": 2.0, "conversionsValue": 80.0}
			out = []any{id}
		case strings.Contains(q, "FROM conversion_action"):
			out = []any{map[string]any{"conversionAction": map[string]any{"id": "42", "name": "Purchase"}}}
		case strings.Contains(q, "FROM keyword_view"):
			out = []any{row(map[string]any{"adGroup": map[string]any{"id": "71"}, "adGroupCriterion": map[string]any{"criterionId": "91", "status": "ENABLED",
				"keyword": map[string]any{"text": "acme crm", "matchType": "EXACT"}}})}
		case strings.Contains(q, "FROM search_term_view"):
			r1 := row(map[string]any{"adGroup": map[string]any{"id": "71"}, "searchTermView": map[string]any{"searchTerm": "acme crm pricing"}})
			r1["segments"].(map[string]any)["keyword"] = map[string]any{"info": map[string]any{"text": "acme crm", "matchType": "EXACT"}}
			out = []any{r1}
		case strings.Contains(q, "FROM ad_group_ad WHERE segments"):
			out = []any{row(map[string]any{"adGroup": map[string]any{"id": "71"}, "adGroupAd": map[string]any{"ad": map[string]any{"id": "81"}}})}
		case strings.Contains(q, "FROM ad_group_ad"):
			out = []any{map[string]any{"adGroup": map[string]any{"id": "71", "finalUrlSuffix": "utm_source=google&of_campaign={campaignid}&of_ad={creative}"}, "adGroupAd": map[string]any{"status": "ENABLED",
				"ad": map[string]any{"id": "81", "type": "RESPONSIVE_SEARCH_AD", "finalUrls": []string{"https://acme.example/pricing"},
					"responsiveSearchAd": map[string]any{"headlines": []any{map[string]any{"text": "Acme CRM"}}}}}}}
		case strings.Contains(q, "FROM ad_group WHERE segments"):
			out = []any{row(map[string]any{"adGroup": group, "campaign": map[string]any{"id": "7"}})}
		case strings.Contains(q, "FROM ad_group"):
			out = []any{map[string]any{"adGroup": group, "campaign": map[string]any{"id": "7"}}}
		case strings.Contains(q, "FROM campaign WHERE segments"):
			out = []any{row(map[string]any{"campaign": camp})}
		case strings.Contains(q, "FROM campaign"):
			c := maps.Clone(camp)
			if strings.Contains(q, "bidding_strategy_type") {
				c["biddingStrategyType"] = "MAXIMIZE_CONVERSIONS"
				c["maximizeConversions"] = map[string]any{"targetCpaMicros": "4000000"}
			}
			if strings.Contains(q, "campaign.final_url_suffix") {
				c["finalUrlSuffix"] = campaignSuffix
			}
			out = []any{map[string]any{"campaign": c}}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	case "/v23/customers/123:uploadClickConversions", "/v23/customers/123:uploadConversionAdjustments":
		b, _ := io.ReadAll(r.Body)
		googleSuffix.Lock()
		googleSuffix.uploads = append(googleSuffix.uploads, r.URL.Path+" "+string(b))
		googleSuffix.Unlock()
		w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func TestAdsSyncAndReports(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Nothing connected is not an error, and every ad platform is listed.
	if got := owner.must(200, "GET", base+"/ads/connections", nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	cat := owner.must(200, "GET", base+"/connectors?category=ads", nil)["data"].([]any)
	if len(cat) != 13 {
		t.Fatalf("ads catalog: %d", len(cat))
	}

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	id := i["id"].(string)
	conns := owner.must(200, "GET", base+"/ads/connections", nil)["data"].([]any)
	if len(conns) != 1 || len(conns[0].(map[string]any)["setup_needed"].([]any)) != 0 {
		t.Fatalf("meta needs no setup: %v", conns)
	}

	res, err := e.ads.SyncOnce(ctx, ws, id)
	if err != nil {
		t.Fatal(err)
	}
	// The first run reads 90 days for the one campaign that spent.
	if res.Accounts != 1 || res.Rows != 90 || res.Campaigns != 2 || res.AdGroups != 1 || res.Ads != 1 {
		t.Fatalf("first sync: %+v", res)
	}
	accts := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)
	acct := accts[0].(map[string]any)
	if len(accts) != 1 || acct["external_id"] != "55" || acct["sync_on"] != true || acct["first_day"] == nil || acct["brand_id"] == nil {
		t.Fatalf("accounts: %v", accts)
	}

	// Every campaign-day with spend is in the spend ledger, once.
	var n, cents int64
	var channel string
	if err := e.pool.QueryRow(ctx, `SELECT count(*), sum(amount_cents), min(channel) FROM spend_entries WHERE workspace_id = $1 AND category = 'ads'`, ws).Scan(&n, &cents, &channel); err != nil {
		t.Fatal(err)
	}
	if n != 90 || cents != 90*2540 || channel != "paid_ads" {
		t.Fatalf("ledger: %d rows, %d cents, channel %q", n, cents, channel)
	}
	if res, err := e.ads.SyncOnce(ctx, ws, id); err != nil || res.Rows != 7 {
		t.Fatalf("second sync should re-read 7 days: %+v %v", res, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND category = 'ads'`, ws).Scan(&n); err != nil || n != 90 {
		t.Fatalf("re-reading must not duplicate the ledger: %d %v", n, err)
	}

	today := time.Now().UTC()
	from := today.AddDate(0, 0, -9).Format(time.DateOnly)
	to := today.Format(time.DateOnly)
	camps := owner.must(200, "GET", base+"/ads/campaigns?from="+from+"&to="+to, nil)["data"].([]any)
	if len(camps) != 1 {
		t.Fatalf("campaigns with spend or active: %v", camps)
	}
	c := camps[0].(map[string]any)
	tot := c["totals"].(map[string]any)
	if c["name"] != "Prospecting" || c["status"] != "active" || c["daily_budget_micros"] != 30_000_000.0 || tot["spend_micros"] != 254_000_000.0 ||
		tot["clicks"] != 200.0 || tot["conversions"] != 20.0 || tot["cpc_micros"] != 1_270_000.0 || tot["cost_per_conversion_micros"] != 12_700_000.0 {
		t.Fatalf("campaign: %v", c)
	}
	if roas := tot["platform_roas"].(float64); roas < 2.36 || roas > 2.37 {
		t.Fatalf("platform roas: %v", roas)
	}
	// Meta's delivery numbers; a lead it reports twice (its own and the
	// pixel's) counts once, and a custom conversion isn't in the total.
	if tot["link_clicks"] != 150.0 || tot["landing_page_views"] != 120.0 || tot["video_views"] != 400.0 || tot["daily_reach"] != 8000.0 || tot["frequency"] != 1.25 {
		t.Fatalf("delivery: %v", tot)
	}
	acts := map[string]float64{}
	for _, x := range owner.must(200, "GET", base+"/ads/conversion-actions?from="+from+"&to="+to, nil)["data"].([]any) {
		acts[x.(map[string]any)["action"].(string)] = x.(map[string]any)["conversions"].(float64)
	}
	if len(acts) != 2 || acts["lead"] != 20 || acts["custom.555"] != 10 {
		t.Fatalf("conversions by action: %v", acts)
	}
	// The ad links without the tracking template, so tracking health names
	// it.
	h := owner.must(200, "GET", base+"/ads/tracking-health", nil)
	if bad := h["ads_without_template"].([]any); len(bad) != 1 || bad[0].(map[string]any)["external_id"] != "41" || bad[0].(map[string]any)["campaign_name"] != "Prospecting" {
		t.Fatalf("ads without the template: %v", h["ads_without_template"])
	}
	sum := owner.must(200, "GET", base+"/ads/summary?from="+from+"&to="+to, nil)
	rows := sum["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["key"] != "meta_ads" || rows[0].(map[string]any)["currency"] != "USD" {
		t.Fatalf("summary: %v", sum)
	}
	days := owner.must(200, "GET", base+"/ads/summary?group=day&from="+from+"&to="+to, nil)["data"].([]any)
	if len(days) != 10 {
		t.Fatalf("by day: %d", len(days))
	}
	// Ad sets and ads repeat their campaign's numbers, so totals count
	// campaigns only.
	if tot := sum["totals"].([]any)[0].(map[string]any)["totals"].(map[string]any); tot["spend_micros"] != 254_000_000.0 || tot["clicks"] != 200.0 {
		t.Fatalf("totals count each day once: %v", tot)
	}

	// Below the campaign: its ad set, and the ad in it.
	campID := c["id"].(string)
	sets := owner.must(200, "GET", base+"/ads/items?level=ad_group&from="+from+"&to="+to, nil)["data"].([]any)
	if len(sets) != 1 {
		t.Fatalf("ad sets: %v", sets)
	}
	set := sets[0].(map[string]any)
	if set["external_id"] != "31" || set["name"] != "Lookalike" || set["parent_id"] != campID || set["campaign_id"] != campID || set["campaign_name"] != "Prospecting" ||
		set["totals"].(map[string]any)["spend_micros"] != 254_000_000.0 {
		t.Fatalf("ad set: %v", set)
	}
	ads := owner.must(200, "GET", base+"/ads/items?campaign_id="+campID+"&from="+from+"&to="+to, nil)["data"].([]any)
	if len(ads) != 1 {
		t.Fatalf("ads: %v", ads)
	}
	ad := ads[0].(map[string]any)
	if ad["level"] != "ad" || ad["external_id"] != "41" || ad["parent_id"] != set["id"] || ad["parent_name"] != "Lookalike" || ad["campaign_id"] != campID ||
		ad["details"].(map[string]any)["url"] != "https://acme.example/demo" || ad["details"].(map[string]any)["headline"] != "Book a demo" ||
		ad["totals"].(map[string]any)["conversions"] != 20.0 {
		t.Fatalf("ad: %v", ad)
	}
	if byParent := owner.must(200, "GET", base+"/ads/items?parent_id="+set["id"].(string), nil)["data"].([]any); len(byParent) != 1 {
		t.Fatalf("ads in the ad set: %v", byParent)
	}
	if other := owner.must(200, "GET", base+"/ads/items?campaign_id=00000000-0000-0000-0000-000000000000", nil)["data"].([]any); len(other) != 0 {
		t.Fatalf("ads in another campaign: %v", other)
	}
	// One ad's trend by day.
	if trend := owner.must(200, "GET", base+"/ads/summary?group=day&entity_id="+ad["id"].(string)+"&from="+from+"&to="+to, nil)["data"].([]any); len(trend) != 10 ||
		trend[0].(map[string]any)["totals"].(map[string]any)["spend_micros"] != 25_400_000.0 {
		t.Fatalf("ad by day: %v", trend)
	}
	owner.must(400, "GET", base+"/ads/items?level=campaign", nil)
	owner.must(400, "GET", base+"/ads/items?campaign_id=nope", nil)
	owner.must(400, "GET", base+"/ads/summary?entity_id=nope", nil)
	owner.must(400, "GET", base+"/ads/summary?group=weekday", nil)
	owner.must(400, "GET", base+"/ads/campaigns?from=yesterday", nil)
	owner.must(400, "GET", base+"/ads/campaigns?status=live", nil)

	// An account belongs to a brand; its spend in the ledger follows.
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	acctID := acct["id"].(string)
	got := owner.must(200, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"brand": "geonode"})
	if got["brand_id"] != geo["id"] {
		t.Fatalf("brand: %v", got)
	}
	var branded int64
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND brand_id = $2`, ws, geo["id"]).Scan(&branded); err != nil || branded != 90 {
		t.Fatalf("ledger rows with the brand: %d %v", branded, err)
	}
	if b := owner.must(200, "GET", base+"/ads/summary?brand=geonode&group=brand&from="+from+"&to="+to, nil)["data"].([]any); b[0].(map[string]any)["key"] != geo["id"] {
		t.Fatalf("by brand: %v", b)
	}
	if moved := owner.must(200, "GET", base+"/ads/campaigns?brand=geonode", nil)["data"].([]any); len(moved) == 0 {
		t.Fatalf("the account's campaigns follow it: %v", moved)
	}
	owner.must(400, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"brand": "nope"})
	owner.must(404, "PATCH", base+"/ads/accounts/00000000-0000-0000-0000-000000000000", map[string]any{"sync_on": false})
	if got := owner.must(200, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"sync_on": false, "brand": "none"}); got["sync_on"] != false || got["brand_id"] != nil {
		t.Fatalf("switch off: %v", got)
	}
	if res, err := e.ads.SyncOnce(ctx, ws, id); err != nil || res.Accounts != 0 {
		t.Fatalf("an account switched off is not read: %+v %v", res, err)
	}
	owner.must(202, "POST", base+"/ads/sync", nil)

	// Read-only tokens can read but not change accounts.
	reader := agentToken(t, owner, base, "ads reader", "analytics.read")
	reader.must(200, "GET", base+"/ads/summary", nil)
	reader.must(403, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"sync_on": true})
	agentToken(t, owner, base, "no analytics", "workspace.read").must(403, "GET", base+"/ads/campaigns", nil)
}

// Google Ads signs in with Google and needs nothing more: Google retired
// developer tokens. One a workspace adds anyway is still checked and sealed.
func TestAdsDeveloperTokenSetup(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var id string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		id = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	conns := owner.must(200, "GET", base+"/ads/connections", nil)["data"].([]any)
	need := conns[0].(map[string]any)["setup_needed"].([]any)
	if len(need) != 0 {
		t.Fatalf("setup needed: %v", conns)
	}
	if res, err := e.ads.SyncOnce(ctx, ws, id); err != nil || res.Accounts != 1 {
		t.Fatalf("sync without a developer token: %+v %v", res, err)
	}

	owner.must(422, "PUT", base+"/ads/connections/"+id+"/setup", map[string]any{"values": map[string]string{"developer_token": "wrong"}})
	got := owner.must(200, "PUT", base+"/ads/connections/"+id+"/setup", map[string]any{"values": map[string]string{"developer_token": "dev-ok"}})
	if len(got["setup_needed"].([]any)) != 0 || got["status"] != "connected" {
		t.Fatalf("after setup: %v", got)
	}
	// The token is sealed with the credentials, never shown back.
	_, listed := owner.do("GET", base+"/ads/connections", nil)
	if raw, _ := json.Marshal(listed); strings.Contains(string(raw), "dev-ok") {
		t.Fatal("the developer token leaked into a response")
	}
	res, err := e.ads.SyncOnce(ctx, ws, id)
	if err != nil || res.Accounts != 1 {
		t.Fatalf("sync after setup: %+v %v", res, err)
	}
	if a := owner.must(200, "GET", base+"/ads/accounts?connector=google_ads", nil)["data"].([]any); a[0].(map[string]any)["currency"] != "EUR" {
		t.Fatalf("google account: %v", a)
	}
	// Below the campaign: the ad carries the template through its ad
	// group's final URL suffix; the keyword and the search it matched;
	// conversions by Google's conversion action.
	g := owner.must(200, "GET", base+"/ads/items?connector=google_ads", nil)["data"].([]any)
	if len(g) != 1 || g[0].(map[string]any)["details"].(map[string]any)["templated"] != "true" || g[0].(map[string]any)["details"].(map[string]any)["url_tags"] == "" {
		t.Fatalf("google ads: %v", g)
	}
	// The fake's numbers land on the account's own day (UTC+14), which is
	// already tomorrow in UTC for most of the day: the default range still
	// covers it.
	window := "connector=google_ads"
	kw := owner.must(200, "GET", base+"/ads/keywords?"+window, nil)["data"].([]any)
	if len(kw) != 1 || kw[0].(map[string]any)["text"] != "acme crm" || kw[0].(map[string]any)["match_type"] != "exact" || kw[0].(map[string]any)["campaign_name"] != "Brand search" ||
		kw[0].(map[string]any)["totals"].(map[string]any)["spend_micros"] != 9_000_000.0 {
		t.Fatalf("keywords: %v", kw)
	}
	st := owner.must(200, "GET", base+"/ads/search-terms?"+window+"&ad_group_id="+kw[0].(map[string]any)["ad_group_id"].(string), nil)["data"].([]any)
	if len(st) != 1 || st[0].(map[string]any)["term"] != "acme crm pricing" || st[0].(map[string]any)["keyword_text"] != "acme crm" ||
		st[0].(map[string]any)["totals"].(map[string]any)["clicks"] != 12.0 {
		t.Fatalf("search terms: %v", st)
	}
	acts := owner.must(200, "GET", base+"/ads/conversion-actions?"+window+"&connector=google_ads", nil)["data"].([]any)
	if len(acts) != 1 || acts[0].(map[string]any)["action"] != "Demo booked" || acts[0].(map[string]any)["conversions"] != 2.0 || acts[0].(map[string]any)["value_micros"] != 80_000_000.0 {
		t.Fatalf("google actions: %v", acts)
	}
	// Re-reading the window replaces its searches rather than adding them.
	if _, err := e.ads.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if st := owner.must(200, "GET", base+"/ads/search-terms?"+window, nil)["data"].([]any); len(st) != 1 || st[0].(map[string]any)["totals"].(map[string]any)["clicks"] != 12.0 {
		t.Fatalf("search terms after a second sync: %v", st)
	}
	owner.must(400, "GET", base+"/ads/keywords?campaign_id=nope", nil)
	owner.must(400, "GET", base+"/ads/search-terms?limit=0", nil)
	owner.must(400, "GET", base+"/ads/conversion-actions?entity_id=nope", nil)

	// Meta needs nothing more.
	meta := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	owner.must(400, "PUT", base+"/ads/connections/"+meta["id"].(string)+"/setup", map[string]any{"values": map[string]string{"developer_token": "x"}})
	owner.must(404, "PUT", base+"/ads/connections/00000000-0000-0000-0000-000000000000/setup", map[string]any{"values": map[string]string{"developer_token": "x"}})
}

// An ad account takes the brand of the connection that sees it, so a
// brand's own Google Ads sign-in shows its accounts, campaigns and spend
// under that brand straight away. A brand someone set by hand stays.
func TestAdAccountTakesItsConnectionsBrand(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "https://www.geonode.com/"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "https://repocket.com/"})["id"].(string)
	var id string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		if err != nil {
			return err
		}
		id = i.ID
		_, err = tx.Exec(ctx, `UPDATE integrations SET brand_id = $1 WHERE id = $2`, geo, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := e.ads.SyncOnce(ctx, ws, id); err != nil || res.Accounts != 1 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	a := owner.must(200, "GET", base+"/ads/accounts?brand="+geo, nil)["data"].([]any)
	if len(a) != 1 || a[0].(map[string]any)["brand_id"] != geo {
		t.Fatalf("the brand's accounts: %v", a)
	}
	if c := owner.must(200, "GET", base+"/ads/campaigns?brand="+geo, nil)["data"].([]any); len(c) == 0 {
		t.Fatal("the brand's campaigns are empty")
	}
	if a := owner.must(200, "GET", base+"/ads/accounts?brand="+other, nil)["data"].([]any); len(a) != 0 {
		t.Fatalf("another brand sees the account: %v", a)
	}

	// An account saved before this, with no brand, takes it on the next
	// sync; one moved to another brand by hand keeps that.
	acc := a[0].(map[string]any)["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE ad_accounts SET brand_id = NULL WHERE id = $1`, acc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if a := owner.must(200, "GET", base+"/ads/accounts?brand="+geo, nil)["data"].([]any); len(a) != 1 {
		t.Fatalf("backfilled brand: %v", a)
	}
	owner.must(200, "PATCH", base+"/ads/accounts/"+acc, map[string]any{"brand": other})
	if _, err := e.ads.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if a := owner.must(200, "GET", base+"/ads/accounts?brand="+other, nil)["data"].([]any); len(a) != 1 {
		t.Fatalf("the brand set by hand was replaced: %v", a)
	}
}

// What a campaign tells the page and agents beyond its numbers: its type,
// how Google bids and to what, the brand's own target cost per conversion
// with over or under, and whether spend is scaling up against the days
// before.
func TestAdCampaignTargetsAndTrend(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var id string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		id = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	list := owner.must(200, "GET", base+"/ads/campaigns?connector=google_ads", nil)
	c := list["data"].([]any)[0].(map[string]any)
	// 9 EUR for 2 conversions: 4.50 each.
	if c["objective"] != "search" || c["bidding_strategy"] != "maximize_conversions" || c["platform_target_cpa_micros"] != 4_000_000.0 ||
		c["target_status"] != "no_target" || c["trend"] != "new" || c["totals"].(map[string]any)["cost_per_conversion_micros"] != 4_500_000.0 {
		t.Fatalf("campaign: %v", c)
	}
	cid := c["id"].(string)
	acc := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)

	owner.must(400, "PUT", base+"/ads/campaigns/"+cid+"/target", map[string]any{"cost_per_conversion_micros": 0})
	owner.must(404, "PUT", base+"/ads/campaigns/"+acc+"/target", map[string]any{"cost_per_conversion_micros": 1})
	owner.must(200, "PUT", base+"/ads/accounts/"+acc+"/target", map[string]any{"cost_per_conversion_micros": 6_000_000})
	get := func() map[string]any {
		return owner.must(200, "GET", base+"/ads/campaigns?connector=google_ads", nil)["data"].([]any)[0].(map[string]any)
	}
	if c := get(); c["target_cost_per_conversion_micros"] != 6_000_000.0 || c["target_from"] != "account" || c["target_status"] != "under" {
		t.Fatalf("account target: %v", c)
	}
	owner.must(200, "PUT", base+"/ads/campaigns/"+cid+"/target", map[string]any{"cost_per_conversion_micros": 3_000_000})
	if c := get(); c["target_cost_per_conversion_micros"] != 3_000_000.0 || c["target_from"] != "campaign" || c["target_status"] != "over" {
		t.Fatalf("campaign target: %v", c)
	}
	owner.must(200, "PUT", base+"/ads/campaigns/"+cid+"/target", map[string]any{"cost_per_conversion_micros": 4_400_000})
	if c := get(); c["target_status"] != "on_target" {
		t.Fatalf("within a tenth: %v", c)
	}
	owner.must(200, "PUT", base+"/ads/campaigns/"+cid+"/target", map[string]any{"cost_per_conversion_micros": nil})
	if c := get(); c["target_from"] != "account" {
		t.Fatalf("cleared campaign target: %v", c)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action IN ('ad_campaign.target_set', 'ad_account.target_set')`, ws).Scan(&n); err != nil || n != 4 {
		t.Fatalf("audit: %d %v", n, err)
	}

	// 4 EUR the day before the range, 9 in it: scaling up by 125%.
	from, _ := time.Parse(time.DateOnly, list["from"].(string))
	if _, err := e.pool.Exec(ctx, `INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks, conversions)
		VALUES ($1, $2, $3, 'EUR', 4000000, 5, 1)`, cid, from.AddDate(0, 0, -1), ws); err != nil {
		t.Fatal(err)
	}
	c = get()
	if c["trend"] != "scaling_up" || c["spend_change"] != 1.25 || c["previous_totals"].(map[string]any)["spend_micros"] != 4_000_000.0 {
		t.Fatalf("trend: %v", c)
	}
}

func TestAddAdTracking(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	googleSuffix.Lock()
	googleSuffix.account, googleSuffix.campaign, googleSuffix.mutates, googleSuffix.refuse = "", "gad_campaignid={campaignid}", nil, false
	googleSuffix.Unlock()
	var id string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		id = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	acc := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(404, "GET", base+"/ads/accounts/"+"00000000-0000-4000-8000-000000000001"+"/tracking", nil)

	// The account has no suffix, and the campaign's own one replaces it
	// without the template: both need it, and the campaign keeps its own
	// parameter. The ad group already has the template.
	plan := owner.must(200, "GET", base+"/ads/accounts/"+acc+"/tracking", nil)
	tpl := plan["template"].(string)
	changes := plan["changes"].([]any)
	if plan["connector"] != "google_ads" || !strings.Contains(tpl, "of_ad={creative}") || len(changes) != 2 {
		t.Fatalf("plan: %v", plan)
	}
	a, c := changes[0].(map[string]any), changes[1].(map[string]any)
	if a["level"] != "account" || a["before"] != "" || a["after"] != tpl || a["name"] != "Acme search" ||
		c["level"] != "campaign" || c["id"] != "7" || c["after"] != adtrack.AddTemplate("gad_campaignid={campaignid}", tpl) ||
		!strings.HasPrefix(c["after"].(string), "gad_campaignid={campaignid}&utm_source=google") {
		t.Fatalf("changes: %v", changes)
	}

	// Reading the plan changes nothing; adding it is queued.
	googleSuffix.Lock()
	if len(googleSuffix.mutates) != 0 {
		t.Fatalf("a plan changed something: %v", googleSuffix.mutates)
	}
	googleSuffix.Unlock()
	if r := owner.must(200, "GET", base+"/ads/accounts/"+acc+"/tracking/run", nil); r["run"] != nil {
		t.Fatalf("run before any: %v", r)
	}
	owner.must(202, "POST", base+"/ads/accounts/"+acc+"/tracking", nil)
	if r := owner.must(200, "GET", base+"/ads/accounts/"+acc+"/tracking/run", nil)["run"].(map[string]any); r["status"] != "queued" {
		t.Fatalf("queued run: %v", r)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = 'ads.tracking'`, ws).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued: %d %v", queued, err)
	}
	if err := e.ads.AddTracking(ctx, ws, acc, "", false); err != nil {
		t.Fatal(err)
	}
	googleSuffix.Lock()
	got, mut := googleSuffix.account+" | "+googleSuffix.campaign, len(googleSuffix.mutates)
	googleSuffix.Unlock()
	if got != tpl+" | "+c["after"].(string) || mut != 2 {
		t.Fatalf("suffixes: %s (%d mutates)", got, mut)
	}
	// The job as the queue ran it: finished, with what it changed.
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET status = 'succeeded' WHERE workspace_id = $1 AND kind = 'ads.tracking'`, ws); err != nil {
		t.Fatal(err)
	}
	if r := owner.must(200, "GET", base+"/ads/accounts/"+acc+"/tracking/run", nil)["run"].(map[string]any); r["status"] != "done" || r["changed"] != 2.0 || r["failed"] != 0.0 {
		t.Fatalf("done run: %v", r)
	}
	if p := owner.must(200, "GET", base+"/ads/accounts/"+acc+"/tracking", nil)["changes"].([]any); len(p) != 0 {
		t.Fatalf("still to change: %v", p)
	}
	var data []byte
	if err := e.pool.QueryRow(ctx, `SELECT data FROM audit_events WHERE workspace_id = $1 AND action = 'ad_account.tracking_added'`, ws).Scan(&data); err != nil ||
		!strings.Contains(string(data), `"changed": 2`) && !strings.Contains(string(data), `"changed":2`) {
		t.Fatalf("audit: %s %v", data, err)
	}
	var requested, syncs int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'ad_account.tracking_requested'`, ws).Scan(&requested)
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = 'ads.sync'`, ws).Scan(&syncs)
	if requested != 1 || syncs != 1 {
		t.Fatalf("requested %d, syncs queued %d", requested, syncs)
	}

	// A campaign Google refuses is reported; the rest still change.
	googleSuffix.Lock()
	googleSuffix.account, googleSuffix.campaign, googleSuffix.refuse = "", "ref=ppc", true
	googleSuffix.Unlock()
	owner.must(202, "POST", base+"/ads/accounts/"+acc+"/tracking", nil)
	if err := e.ads.AddTracking(ctx, ws, acc, "", false); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT data FROM audit_events WHERE workspace_id = $1 AND action = 'ad_account.tracking_added' ORDER BY id DESC LIMIT 1`, ws).Scan(&data); err != nil ||
		!strings.Contains(string(data), "FIELD_NOT_MUTABLE") {
		t.Fatalf("refused: %s %v", data, err)
	}
	googleSuffix.Lock()
	acctSuffix := googleSuffix.account
	googleSuffix.refuse = false
	googleSuffix.Unlock()
	if acctSuffix != tpl {
		t.Fatalf("account after a refused campaign: %q", acctSuffix)
	}
	// The second request, refused in part, shows the refusal.
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET status = 'succeeded' WHERE workspace_id = $1 AND kind = 'ads.tracking'`, ws); err != nil {
		t.Fatal(err)
	}
	if r := owner.must(200, "GET", base+"/ads/accounts/"+acc+"/tracking/run", nil)["run"].(map[string]any); r["status"] != "done" || r["failed"] != 1.0 ||
		!strings.Contains(r["errors"].([]any)[0].(map[string]any)["error"].(string), "FIELD_NOT_MUTABLE") {
		t.Fatalf("refused run: %v", r)
	}
	// A run that can't reach the account fails, with why.
	if err := e.ads.AddTracking(ctx, ws, "00000000-0000-4000-8000-000000000002", "", false); err == nil || !errors.Is(err, jobs.ErrPermanent) {
		t.Fatalf("missing account: %v", err)
	}

	// Other platforms say the template goes in by hand.
	m := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, m["id"].(string)); err != nil {
		t.Fatal(err)
	}
	var metaAcc string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_accounts WHERE workspace_id = $1 AND connector = 'meta_ads'`, ws).Scan(&metaAcc); err != nil {
		t.Fatal(err)
	}
	if res := owner.must(422, "GET", base+"/ads/accounts/"+metaAcc+"/tracking", nil); res["error"].(map[string]any)["code"] != "connector_invalid" {
		t.Fatalf("meta: %v", res)
	}
}
