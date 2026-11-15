package api_test

import (
	"encoding/json"
	"net/http"
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
	case strings.HasPrefix(r.URL.Path, "/api/stats/campaign/") && strings.HasSuffix(r.URL.Path, "/targets"):
		cid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/stats/campaign/"), "/targets")
		el := f.targets[cid]
		if el == nil {
			el = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 0, "limit": 1000, "total": len(el), "elements": el})
	case r.URL.Path == "/api/stats/campaign/all":
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 0, "limit": 1000, "total": 0, "elements": []any{}})
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
