package hubspot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

func TestDealsMapStagesAmountsAndContacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/crm/v3/pipelines/deals":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"stages": []any{map[string]string{"id": "closedwon", "label": "Closed won"}}}}})
		case r.URL.Path == "/crm/v3/objects/deals/search":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "9", "properties": map[string]string{
				"dealname": "Acme", "dealstage": "closedwon", "amount": "1200.50", "hs_is_closed": "true", "hs_is_closed_won": "true",
				"createdate": "2026-10-01T00:00:00Z", "closedate": "2026-10-20T00:00:00Z", "hs_lastmodifieddate": "2026-10-20T01:00:00Z"}}}})
		case strings.HasPrefix(r.URL.Path, "/crm/v4/associations/deals/contacts"):
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"from": map[string]string{"id": "9"}, "to": []any{map[string]any{"toObjectId": 5}}}}})
		case r.URL.Path == "/crm/v3/objects/contacts/batch/read":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "5", "properties": map[string]string{"email": "sam@acme.test"}}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"hubspot": srv.URL}}
	page, err := HubSpot{}.SyncDeals(context.Background(), cl, c.Creds{"api_key": "pat"}, "")
	if err != nil {
		t.Fatal(err)
	}
	d := page.Deals[0]
	if d.State != outcomes.DealWon || d.Stage != "Closed won" || *d.AmountCents != 120050 || d.ContactEmails[0] != "sam@acme.test" || d.ClosedAt == nil {
		t.Fatalf("deal: %+v", d)
	}
	if page.More || !strings.Contains(page.Cursor, `"since":1792458000000`) {
		t.Fatalf("cursor: %s", page.Cursor)
	}
}
