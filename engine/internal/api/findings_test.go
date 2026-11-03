package api_test

import (
	"testing"
	"time"
)

// A finding is what an agent or a person learned about a brand: text,
// evidence, confidence, scope and a review date. It is kept to its brand,
// listed as due once past its review date, carried by the brand's context,
// and retired rather than deleted.
func TestBrandFindings(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	path := "/v1/brands/" + brand + "/findings"

	// Evidence and confidence are required: a finding without them is a guess.
	st, res := owner.do("POST", path, map[string]any{"text": "Comparison pages convert search visitors better than guides", "confidence": "medium"})
	if st != 400 || res["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("no evidence: %d %v", st, res)
	}
	st, _ = owner.do("POST", path, map[string]any{"text": "x", "confidence": "certain", "evidence": []map[string]any{{"kind": "metric", "note": "n"}}})
	if st != 400 {
		t.Fatalf("bad confidence: %d", st)
	}
	f := owner.must(201, "POST", path, map[string]any{
		"text":       "Comparison pages convert search visitors twice as well as guides",
		"confidence": "medium",
		"evidence":   []map[string]any{{"kind": "metric", "ref": "page_conversion_rate", "note": "4.1% on the 3 comparison pages against 1.9% on 12 guides over 30 days"}},
		"scope":      map[string]any{"channel": "search"},
	})
	id := f["id"].(string)
	if f["status"] != "active" || f["due"] != false || f["brand_id"] != brand || f["scope"].(map[string]any)["channel"] != "search" {
		t.Fatalf("finding: %v", f)
	}
	if ra, _ := time.Parse(time.RFC3339, f["review_at"].(string)); time.Until(ra) < 89*24*time.Hour {
		t.Fatalf("default review date should be about 90 days out: %v", f["review_at"])
	}

	// A finding past its review date is due; one scoped to another channel isn't listed for search.
	past := owner.must(201, "POST", path, map[string]any{
		"text": "LinkedIn posts with a number in the first line get twice the reach", "confidence": "low",
		"evidence":  []map[string]any{{"kind": "observation", "note": "6 of the 8 best posts last quarter opened with a number"}},
		"scope":     map[string]any{"channel": "linkedin"},
		"review_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if past["due"] != true {
		t.Fatalf("past review date should be due: %v", past)
	}
	if l := owner.must(200, "GET", path+"?due=true", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != past["id"] {
		t.Fatalf("due: %v", l)
	}
	if l := owner.must(200, "GET", path+"?channel=search", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != id {
		t.Fatalf("by channel: %v", l)
	}
	if l := owner.must(200, "GET", path+"?limit=1", nil); l["next_cursor"] == nil || len(l["data"].([]any)) != 1 {
		t.Fatalf("paging: %v", l)
	}

	// The brand's context carries its active findings, nearest review first.
	ctx := owner.must(200, "GET", base+"/brands/"+brand+"/context", nil)
	if fs := ctx["findings"].([]any); len(fs) != 2 || fs[0].(map[string]any)["id"] != past["id"] {
		t.Fatalf("context findings: %v", ctx["findings"])
	}

	// Kept to its brand: another brand's path doesn't see it.
	owner.must(404, "GET", "/v1/brands/"+other+"/findings/"+id, nil)
	if l := owner.must(200, "GET", "/v1/brands/"+other+"/findings", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("other brand's findings: %v", l)
	}

	// After a review the date moves forward; retiring keeps it with its reason.
	later := time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	u := owner.must(200, "PATCH", path+"/"+past["id"].(string), map[string]any{"review_at": later, "confidence": "medium"})
	if u["due"] != false || u["confidence"] != "medium" {
		t.Fatalf("reviewed: %v", u)
	}
	st, _ = owner.do("PATCH", path+"/"+id, map[string]any{"retired_reason": "x"})
	if st != 400 {
		t.Fatalf("a reason without retiring: %d", st)
	}
	r := owner.must(200, "PATCH", path+"/"+id, map[string]any{"status": "retired", "retired_reason": "The guides were rewritten in June and now convert as well"})
	if r["status"] != "retired" || r["retired_at"] == nil || r["retired_reason"] == "" {
		t.Fatalf("retired: %v", r)
	}
	if l := owner.must(200, "GET", path+"?status=active", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("active after retiring: %v", l)
	}
	if fs := owner.must(200, "GET", base+"/brands/"+brand+"/context", nil)["findings"].([]any); len(fs) != 1 {
		t.Fatalf("context after retiring: %v", fs)
	}
	back := owner.must(200, "PATCH", path+"/"+id, map[string]any{"status": "active"})
	if back["status"] != "active" || back["retired_at"] != nil {
		t.Fatalf("back: %v", back)
	}

	// Every change is in the audit log.
	for _, want := range []string{"finding.recorded", "finding.updated", "finding.retired"} {
		found := false
		for _, x := range owner.must(200, "GET", base+"/audit-events?limit=50", nil)["data"].([]any) {
			if x.(map[string]any)["action"] == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("audit lacks %s", want)
		}
	}
}
