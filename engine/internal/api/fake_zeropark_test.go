package api_test

import (
	"cmp"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// fakeZeropark answers like Zeropark's Stats API (its /v3/api-docs): the
// token test-good works; /api/account/details is an account in
// Europe/Berlin; a campaign's targets for a CUSTOM day come from
// targets[campaign], else none.
// zeroparkFake is the fake the latest test environment uses.
var zeroparkFake *fakeZeropark

type fakeZeropark struct {
	mu      sync.Mutex
	targets map[string][]map[string]any
	calls   int
	// campaigns are what /api/stats/campaign/all lists; posts are the
	// changes asked for, as "path?query"; state and budget follow them.
	campaigns []map[string]any
	posts     []string
	// bids are the bids it shows, by campaign id, or campaign id + "|" +
	// target; one never set is 0.01.
	bids map[string]float64
	// states are the target states it lists (campaign id + "|" + target),
	// as a pause or resume of the target, or setState, left them.
	states map[string]string
}

func (f *fakeZeropark) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("api-token") != "test-good" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	switch {
	case r.URL.Path == "/api/account/details":
		_ = json.NewEncoder(w).Encode(map[string]any{"clientId": "zp-1001", "accountBalance": 100.0, "timezone": "Europe/Berlin"})
	case strings.HasPrefix(r.URL.Path, "/api/stats/campaign/") && strings.HasSuffix(r.URL.Path, "/bid"):
		_ = json.NewEncoder(w).Encode(map[string]any{"value": f.bid(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/stats/campaign/"), "/bid")), "autoBid": false})
	case strings.HasPrefix(r.URL.Path, "/api/stats/campaign/") && strings.HasSuffix(r.URL.Path, "/targets") && r.URL.Query().Get("targetAddresses") != "":
		cid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/stats/campaign/"), "/targets")
		t := r.URL.Query().Get("targetAddresses")
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 0, "limit": 100, "total": 1, "elements": []any{
			map[string]any{"id": "aaaaaaaa-0000-4000-8000-00000000000f", "target": t, "address": t, "bid": map[string]any{"value": f.bid(cid + "|" + t), "autoBid": false}}}})
	case strings.HasPrefix(r.URL.Path, "/api/stats/campaign/") && strings.HasSuffix(r.URL.Path, "/targets"):
		cid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/stats/campaign/"), "/targets")
		el := []map[string]any{}
		for _, t := range f.targets[cid] {
			// Each target with the state and bid Zeropark holds for it.
			row := map[string]any{}
			for k, v := range t {
				row[k] = v
			}
			key := cid + "|" + row["target"].(string)
			row["state"] = map[string]any{"state": cmp.Or(f.states[key], "ACTIVE"), "actions": []string{"PAUSE"}}
			row["bid"] = map[string]any{"value": f.bid(key), "autoBid": false}
			row["minBid"] = 0.0005
			el = append(el, row)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 0, "limit": 1000, "total": len(el), "elements": el})
	case r.URL.Path == "/api/stats/campaign/all":
		el := []map[string]any{}
		for _, c := range f.campaigns {
			if id := r.URL.Query().Get("campaignNameOrId"); id == "" || c["details"].(map[string]any)["id"] == id {
				el = append(el, c)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 0, "limit": 1000, "total": len(el), "elements": el})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/campaign/"):
		f.posts = append(f.posts, r.URL.Path+"?"+r.URL.RawQuery)
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/campaign/"), "/", 2)
		for _, c := range f.campaigns {
			d := c["details"].(map[string]any)
			if d["id"] != parts[0] || len(parts) < 2 {
				continue
			}
			if v, err := strconv.ParseFloat(r.URL.Query().Get("bid"), 64); err == nil && (parts[1] == "bid" || parts[1] == "target/bid") {
				key := parts[0]
				if parts[1] == "target/bid" {
					key += "|" + r.URL.Query().Get("hash")
				}
				if f.bids == nil {
					f.bids = map[string]float64{}
				}
				f.bids[key] = v
			}
			switch parts[1] {
			case "targets/pause", "targets/resume":
				if f.states == nil {
					f.states = map[string]string{}
				}
				f.states[parts[0]+"|"+r.URL.Query().Get("hashOrAddress")] = map[string]string{"targets/pause": "PAUSED", "targets/resume": "ACTIVE"}[parts[1]]
			case "pause":
				d["state"] = map[string]any{"state": "PAUSED"}
			case "resume":
				d["state"] = map[string]any{"state": "ACTIVE"}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "ACTIVE"})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeZeropark) set(campaign string, targets []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.targets == nil {
		f.targets = map[string][]map[string]any{}
	}
	f.targets[campaign] = targets
}

// campaign adds an active campaign with a daily budget to the list.
func (f *fakeZeropark) campaign(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.campaigns = append(f.campaigns, map[string]any{
		"details": map[string]any{"id": id, "name": name, "state": map[string]any{"state": "ACTIVE"}, "dailyBudget": map[string]any{"type": "LIMITED", "amount": 50.0}},
		"stats":   map[string]any{"spent": 0.0, "redirects": 0, "conversions": 0, "payout": 0.0}})
}

func (f *fakeZeropark) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

// bid is the bid shown for a key (the caller holds the lock).
func (f *fakeZeropark) bid(key string) float64 {
	if v, ok := f.bids[key]; ok {
		return v
	}
	return 0.01
}

func (f *fakeZeropark) bidOf(key string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bid(key)
}

// setState sets the state Zeropark lists for a campaign's target, as a
// change made in its own panel would.
func (f *fakeZeropark) setState(campaign, target, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string]string{}
	}
	f.states[campaign+"|"+target] = state
}

// setBid sets the bid Zeropark shows for a campaign's target.
func (f *fakeZeropark) setBid(campaign, target string, bid float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bids == nil {
		f.bids = map[string]float64{}
	}
	f.bids[campaign+"|"+target] = bid
}
