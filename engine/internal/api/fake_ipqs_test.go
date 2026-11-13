package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
)

// fakeIPQS answers like IPQualityScore: the key test-good works, an
// address in 198.51.100.0/24 is a data-centre proxy scoring 90, one in
// 192.0.2.0/24 finds the service down, and any other is a clean home
// network.
type fakeIPQS struct{}

func (fakeIPQS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	key := r.Header.Get("IPQS-KEY")
	if strings.HasPrefix(r.URL.Path, "/api/json/account/") {
		key = strings.TrimPrefix(r.URL.Path, "/api/json/account/")
	}
	if key != "test-good" {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "Invalid or unauthorized key"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/json/account/") {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "Success", "credits": 5000, "usage": 0})
		return
	}
	if strings.HasPrefix(r.URL.Query().Get("ip"), "192.0.2.") {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if strings.HasPrefix(r.URL.Query().Get("ip"), "198.51.100.") {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "fraud_score": 90, "proxy": true, "vpn": false, "tor": false, "is_crawler": false, "connection_type": "Data Center"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "fraud_score": 0, "proxy": false, "vpn": false, "tor": false, "is_crawler": false, "connection_type": "Residential"})
}
