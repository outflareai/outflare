package adtrack

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/utm"
)

func TestCatalogIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range Networks {
		if seen[n.ID] {
			t.Errorf("network %s listed twice", n.ID)
		}
		seen[n.ID] = true
		if utm.Source(n.ID) != n.ID {
			t.Errorf("network id %s is not a clean utm_source", n.ID)
		}
		if n.Macros.ClickID != "" && n.ClickParam() == "" {
			t.Errorf("%s has a click id macro but no click id", n.ID)
		}
	}
	params := map[string]bool{}
	for _, c := range ClickIDs {
		if params[c.Param] {
			t.Errorf("click id %s listed twice", c.Param)
		}
		params[c.Param] = true
		if _, ok := utm.ChannelByID(c.Channel); !ok {
			t.Errorf("click id %s credits unknown channel %s", c.Param, c.Channel)
		}
		if _, ok := NetworkOf(c.Network); !ok && c.Channel == "paid_ads" {
			t.Errorf("click id %s names unknown network %s", c.Param, c.Network)
		}
	}
	for _, c := range Cookies {
		if params[c.Key] {
			t.Errorf("cookie key %s is also a click id", c.Key)
		}
	}
	// Every ad network is a paid ads source, so links tagged with its id
	// land in paid ads.
	paid, _ := utm.ChannelByID("paid_ads")
	for _, n := range Networks {
		if !slices.Contains(paid.Sources, n.ID) {
			t.Errorf("paid_ads sources miss %s", n.ID)
		}
	}
}

func TestTemplates(t *testing.T) {
	for _, n := range Networks {
		tpl := n.Template()
		if n.Field == "" {
			if tpl != "" {
				t.Errorf("%s links nowhere but has a template", n.ID)
			}
			continue
		}
		q, err := url.ParseQuery(tpl)
		if err != nil {
			t.Fatalf("%s: %v", n.ID, err)
		}
		if q.Get("utm_source") != n.ID || q.Get("utm_medium") != "cpc" {
			t.Errorf("%s: template %q", n.ID, tpl)
		}
		if strings.HasPrefix(tpl, "?") || strings.HasPrefix(tpl, "&") {
			t.Errorf("%s: template starts with a separator", n.ID)
		}
	}
	meta, _ := NetworkOf("meta")
	want := "utm_source=meta&utm_medium=cpc&utm_campaign={{campaign.name}}&utm_content={{ad.name}}&of_campaign={{campaign.id}}&of_adset={{adset.id}}&of_ad={{ad.id}}&of_placement={{placement}}"
	if got := meta.Template(); got != want {
		t.Errorf("meta template\n got %s\nwant %s", got, want)
	}
	taboola, _ := NetworkOf("taboola")
	if !strings.HasSuffix(taboola.Template(), "&tblci={click_id}") {
		t.Errorf("taboola template doesn't ask for its click id: %s", taboola.Template())
	}
}

func TestCleanAd(t *testing.T) {
	got, skipped := CleanAd(map[string]any{
		"campaign": "120200000001", "adset": "{{adset.id}}", "ad": "__CID__", "placement": " Facebook_Mobile_Feed ",
		"other": "dropped", "keyword": 7,
	})
	want := map[string]string{"campaign": "120200000001", "placement": "Facebook_Mobile_Feed"}
	if len(got) != len(want) || got["campaign"] != want["campaign"] || got["placement"] != want["placement"] {
		t.Errorf("CleanAd = %v, want %v", got, want)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if got, _ := CleanAd(map[string]any{"ad": strings.Repeat("9", 300)}); len(got["ad"]) != 200 {
		t.Errorf("long ad id not cut: %d", len(got["ad"]))
	}
}
