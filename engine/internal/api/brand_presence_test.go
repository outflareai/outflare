package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Presence (Coverage in the API) tags how a placement happened and who
// delivered it, links its CRM deal, and keeps the agencies, partners and
// creators involved as CRM records on it, each brand on its own.
func TestPresencePartiesAndDeals(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	otherRoot := "/v1/brands/" + other + "/coverage"

	site := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example channel", "brand_id": brand})
	publisher := owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": site["id"], "domain": "youtube.example"})
	agency := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example agency", "brand_id": brand})
	creator := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "brand_id": brand})

	// New types and arrangements, and who delivered it.
	p := owner.must(201, "POST", root+"/placements", map[string]any{"publisher_id": publisher["id"], "title": "Review video",
		"url": "https://youtube.example/watch/1", "kind": "youtube", "arrangement": "organic", "delivered_by": "agency"})
	id := p["id"].(string)
	if p["kind"] != "youtube" || p["arrangement"] != "organic" || p["delivered_by"] != "agency" || p["deal_id"] != nil {
		t.Fatalf("placement: %v", p)
	}
	for _, kind := range []string{"influencer", "forum", "mcp_directory", "app_listing"} {
		owner.must(201, "POST", root+"/placements", map[string]any{"publisher_id": publisher["id"], "title": "A " + kind, "kind": kind})
	}
	owner.must(400, "POST", root+"/placements", map[string]any{"publisher_id": publisher["id"], "title": "Bad", "delivered_by": "robot"})
	if p := owner.must(200, "GET", root+"/placements?arrangement=organic", nil); p["total"] != float64(1) {
		t.Fatalf("organic filter: %v", p)
	}

	// The agency and the creator are CRM records on the placement.
	party := owner.must(201, "POST", root+"/parties", map[string]any{"placement_id": id, "record_id": agency["id"], "role": "agency"})
	owner.must(201, "POST", root+"/parties", map[string]any{"placement_id": id, "record_id": creator["id"], "role": "creator"})
	owner.must(409, "POST", root+"/parties", map[string]any{"placement_id": id, "record_id": agency["id"], "role": "agency"})
	owner.must(400, "POST", root+"/parties", map[string]any{"placement_id": id, "record_id": agency["id"], "role": "sponsor"})
	// Another brand sees none of it and can't attach to it.
	owner.must(400, "POST", otherRoot+"/parties", map[string]any{"placement_id": id, "record_id": agency["id"], "role": "agency"})
	owner.must(404, "GET", otherRoot+"/parties/"+party["id"].(string), nil)
	owner.must(404, "DELETE", otherRoot+"/parties/"+party["id"].(string), nil)
	if l := owner.must(200, "GET", root+"/parties?placement_id="+id, nil); l["total"] != float64(2) {
		t.Fatalf("parties: %v", l)
	}
	if l := owner.must(200, "GET", otherRoot+"/parties", nil); l["total"] != float64(0) {
		t.Fatalf("other brand's parties: %v", l)
	}
	owner.must(200, "DELETE", root+"/parties/"+party["id"].(string), nil)
	owner.must(404, "DELETE", root+"/parties/"+party["id"].(string), nil)
	if l := owner.must(200, "GET", root+"/parties?placement_id="+id, nil); l["total"] != float64(1) {
		t.Fatalf("parties after removal: %v", l)
	}
	if len(named(events(), "coverage_record.deleted")) != 1 {
		t.Fatal("removing a party records coverage_record.deleted")
	}

	// The placement links the brand's CRM deal, never another brand's.
	deal := owner.must(201, "POST", "/v1/brands/"+brand+"/deals", map[string]any{"name": "Example channel partnership"})
	otherDeal := owner.must(201, "POST", "/v1/brands/"+other+"/deals", map[string]any{"name": "Elsewhere"})
	owner.must(400, "PATCH", root+"/placements/"+id, map[string]any{"version": 1, "deal_id": otherDeal["id"]})
	linked := owner.must(200, "PATCH", root+"/placements/"+id, map[string]any{"version": 1, "deal_id": deal["id"]})
	if linked["deal_id"] != deal["id"] {
		t.Fatalf("deal link: %v", linked)
	}
}
