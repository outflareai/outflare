package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/utm"
)

func TestCampaignLinks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	tax := owner.must(200, "GET", base+"/tracking-taxonomy", nil)["channels"].([]any)
	if len(tax) != len(utm.Channels) || tax[0].(map[string]any)["medium"] != "cpc" {
		t.Fatalf("taxonomy = %v", tax)
	}

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
		"name": "Spring launch, US", "channel": "paid_ads", "source": "Google Ads", "brand": "northpeak",
		"landing_url": "https://northpeak.example/pricing?utm_source=old",
	})
	if c["slug"] != "spring-launch-us" || c["source"] != "google" || c["medium"] != "cpc" || c["brand_id"] != brand || len(c["code"].(string)) != 10 {
		t.Fatalf("campaign = %v", c)
	}
	code := c["code"].(string)
	if want := "https://northpeak.example/pricing?utm_source=google&utm_medium=cpc&utm_campaign=spring-launch-us&ofc=" + code; c["landing_link"] != want {
		t.Fatalf("landing_link = %v", c["landing_link"])
	}
	// Reddit has one usual source, so it is filled in.
	if r := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "AMA", "channel": "reddit"}); r["source"] != "reddit" || r["medium"] != "social" {
		t.Fatalf("reddit = %v", r)
	}
	owner.must(409, "POST", base+"/tracking-campaigns", map[string]any{"name": "Spring launch US", "channel": "seo", "source": "google"})
	owner.must(400, "POST", base+"/tracking-campaigns", map[string]any{"name": "X", "channel": "tv", "source": "x"})
	owner.must(400, "POST", base+"/tracking-campaigns", map[string]any{"name": "X", "channel": "paid_ads"})
	owner.must(400, "POST", base+"/tracking-campaigns", map[string]any{"name": "X", "channel": "paid_ads", "source": "google", "landing_url": "northpeak.example"})
	owner.must(400, "POST", base+"/tracking-campaigns", map[string]any{"name": "X", "channel": "paid_ads", "source": "google", "brand": "nobody"})

	// Found by id, slug or code; tags never change.
	for _, ref := range []string{c["id"].(string), "spring-launch-us", code} {
		if v := owner.must(200, "GET", base+"/tracking-campaigns/"+ref, nil); v["id"] != c["id"] {
			t.Fatalf("get %s = %v", ref, v)
		}
	}
	owner.must(400, "PATCH", base+"/tracking-campaigns/"+code, map[string]any{"source": "meta"})
	owner.must(400, "PATCH", base+"/tracking-campaigns/"+code, map[string]any{"slug": "other"})
	if v := owner.must(200, "PATCH", base+"/tracking-campaigns/"+code, map[string]any{"name": "Spring launch", "brand": ""}); v["name"] != "Spring launch" || v["brand_id"] != nil || v["slug"] != "spring-launch-us" {
		t.Fatalf("patch = %v", v)
	}

	// Links: the same page, content and term give back the same link.
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+code+"/links", map[string]any{"url": "https://northpeak.example/blog?ref=x#top", "content": "Hero CTA"})
	if l["content"] != "hero-cta" || !strings.Contains(l["tagged_url"].(string), "?ref=x&utm_source=google&utm_medium=cpc&utm_campaign=spring-launch-us&utm_content=hero-cta&ofc="+code+"#top") {
		t.Fatalf("link = %v", l)
	}
	if again := owner.must(200, "POST", base+"/tracking-campaigns/"+code+"/links", map[string]any{"url": "https://northpeak.example/blog?ref=x#top", "content": "hero-cta"}); again["id"] != l["id"] {
		t.Fatalf("again = %v", again)
	}
	// No url: the landing page.
	if lp := owner.must(201, "POST", base+"/tracking-campaigns/"+code+"/links", map[string]any{"content": "sidebar"}); lp["url"] != "https://northpeak.example/pricing" {
		t.Fatalf("landing link = %v", lp)
	}
	owner.must(400, "POST", base+"/tracking-campaigns/"+code+"/links", map[string]any{"url": "javascript:alert(1)"})
	if n := len(owner.must(200, "GET", base+"/tracking-campaigns/"+code+"/links", nil)["data"].([]any)); n != 2 {
		t.Fatalf("links = %d", n)
	}
	if list := owner.must(200, "GET", base+"/tracking-campaigns?channel=paid_ads", nil)["data"].([]any); len(list) != 1 || list[0].(map[string]any)["links"] != float64(2) {
		t.Fatalf("list = %v", list)
	}

	// Archived campaigns take no new links.
	owner.must(200, "PATCH", base+"/tracking-campaigns/"+code, map[string]any{"status": "archived"})
	owner.must(400, "POST", base+"/tracking-campaigns/"+code+"/links", map[string]any{"content": "late"})
	if n := len(owner.must(200, "GET", base+"/tracking-campaigns?status=active", nil)["data"].([]any)); n != 1 {
		t.Fatalf("active = %d", n)
	}

	// Modules get one campaign per object, with a free slug.
	ctx := context.Background()
	spec := utm.Spec{Channel: "lifecycle_email", Source: "outflare", Name: "AMA", ObjectKind: "email_broadcast", ObjectID: "b1"}
	mc, created, err := utm.Ensure(ctx, e.pool, ws, spec)
	if err != nil || !created || mc.Slug != "ama-2" || mc.Medium != "email" {
		t.Fatalf("ensure = %+v %v %v", mc, created, err)
	}
	if again, created, err := utm.Ensure(ctx, e.pool, ws, spec); err != nil || created || again.ID != mc.ID {
		t.Fatalf("ensure again = %+v %v %v", again, created, err)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"tracking_campaign.created", "tracking_campaign.updated", "tracking_campaign.archived", "tracking_link.created"} {
		if !seen[name] {
			t.Fatalf("no %s event", name)
		}
	}
}
