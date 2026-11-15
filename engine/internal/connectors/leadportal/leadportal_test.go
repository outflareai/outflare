package leadportal_test

import (
	"context"
	"encoding/json"
	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/leadportal"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestWorkspaceCredentialAndReadOnlyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer lp_test_not_a_real_key" || r.URL.Path != "/api/v1/reports/sales-reporting" {
			t.Error("changed request or credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"schema_version":1}`))
	}))
	defer srv.Close()
	client := &c.Client{}
	creds := c.Creds{"base_url": srv.URL, "api_key": "lp_test_not_a_real_key"}
	v := leadportal.Connector{}
	if _, e := v.Test(context.Background(), client, creds); e != nil {
		t.Fatal(e)
	}
	if _, e := v.Read(context.Background(), client, c.Creds{"base_url": srv.URL}, url.Values{}); e == nil {
		t.Fatal("missing key accepted")
	}
	for _, base := range []string{"ftp://portal.example", "https://user:password@portal.example", "https://portal.example?key=test"} {
		if _, e := v.Read(context.Background(), client, c.Creds{"base_url": base, "api_key": "lp_test_not_a_real_key"}, nil); e == nil {
			t.Fatal("unsafe base accepted")
		}
	}
}

func TestOriginLookupValidatesIdentityAndRequestedLeads(t *testing.T) {
	const leadID = "00000000-0000-4000-8000-000000000001"
	for _, name := range []string{"valid", "extra_lead", "credential", "bad_kind", "bad_id", "bad_date", "missing_map"} {
		t.Run(name, func(t *testing.T) {
			origin := map[string]any{"id": "worker:10000000-0000-4000-8000-000000000001", "name": "Discovery Example", "kind": "worker", "revoked": true}
			id := leadID
			switch name {
			case "extra_lead":
				id = "00000000-0000-4000-8000-000000000002"
			case "credential":
				origin["key_hash"] = "test-not-a-real-hash"
			case "bad_kind":
				origin["kind"] = "researcher"
			case "bad_id":
				origin["id"] = "worker:invalid"
			case "bad_date":
				origin["submitted_at"] = "yesterday"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "missing_map" {
					json.NewEncoder(w).Encode(map[string]any{})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"origins": map[string]any{id: origin}})
			}))
			defer srv.Close()
			_, err := (leadportal.Connector{}).Read(context.Background(), &c.Client{}, c.Creds{"base_url": srv.URL, "api_key": "lp_test_not_a_real_key"}, url.Values{"lead_ids": {leadID}})
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}
