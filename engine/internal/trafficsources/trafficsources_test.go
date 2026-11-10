package trafficsources

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/outflareai/outflare/engine/internal/adtrack"
)

// Every template in integrations/traffic-sources loads and is valid.
func TestCatalogue(t *testing.T) {
	list, err := All()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tpl := range list {
		have[tpl.ID] = true
	}
	// The networks plans/ad-tracker.md (T3) names beside the ones Outflare
	// syncs.
	for _, id := range []string{"mgid", "revcontent", "newsbreak", "propellerads", "zeropark", "exoclick", "adsterra", "trafficjunky",
		"richads", "pushground", "mediago", "smartnews", "applovin"} {
		if !have[id] {
			t.Errorf("no template for %s", id)
		}
	}
	for _, tpl := range list {
		if tpl.Connector != "" {
			if n, ok := adtrack.NetworkForConnector(tpl.Connector); !ok || n.ID != tpl.ID {
				t.Errorf("%s: connector %q isn't the network's", tpl.ID, tpl.Connector)
			}
		}
	}
}

// The networks the site script has tracking templates for use the same
// macros here, so the two never disagree.
func TestMatchesAdtrack(t *testing.T) {
	for _, n := range adtrack.Networks {
		hasClickID := false
		for _, c := range adtrack.ClickIDs {
			hasClickID = hasClickID || c.Network == n.ID
		}
		if n.Template() == "" || !hasClickID {
			continue // ads that don't link to a website, or no click id to track by
		}
		tpl, ok := Get(n.ID)
		if !ok {
			t.Errorf("no template for %s", n.ID)
			continue
		}
		byParam := map[string]string{}
		for _, p := range tpl.Parameters {
			byParam[p.Param] = p.Token
		}
		for _, part := range strings.Split(n.Template(), "&") {
			k, v, _ := strings.Cut(part, "=")
			if k == "utm_source" || k == "utm_medium" {
				continue
			}
			if k == tpl.ClickID.Param {
				if v != tpl.ClickID.Token {
					t.Errorf("%s: click id token %q, adtrack has %q", n.ID, tpl.ClickID.Token, v)
				}
				continue
			}
			if byParam[k] != v {
				t.Errorf("%s: %s is %q, adtrack has %q", n.ID, k, byParam[k], v)
			}
		}
		if c, ok := adtrack.ClickIDOf(tpl.ClickID.Param); !ok || c.Network != n.ID {
			t.Errorf("%s: click id parameter %q isn't the network's", n.ID, tpl.ClickID.Param)
		}
	}
}

func TestValidate(t *testing.T) {
	good := `id: acme_native
name: Acme Native
category: native
click_id: {param: clickid, token: "{clickid}"}
cost: {param: cost, token: "{cpc}"}
parameters:
  - {name: Zone, param: zone, token: "{zoneid}", field: 1, aliases: [zone_id]}
postback: {url: "https://acme.example/pb?c={source_click_id}&p={payout|80%}&key=REPLACE"}
sources: [https://acme.example/docs]
`
	list, err := Load(fstest.MapFS{"traffic-sources/acme_native.yaml": {Data: []byte(good)}})
	if err != nil || len(list) != 1 || list[0].Postback.Method != "GET" || list[0].Currency != "USD" || !list[0].HasPlaceholder() {
		t.Fatalf("good: %v %+v", err, list)
	}
	for name, bad := range map[string]string{
		"file name":      strings.Replace(good, "id: acme_native", "id: acme_other", 1),
		"category":       strings.Replace(good, "native\n", "rocket\n", 1),
		"field twice":    strings.Replace(good, "parameters:\n", "parameters:\n  - {name: Site, param: site, token: \"{site}\", field: 1}\n", 1),
		"param twice":    strings.Replace(good, "aliases: [zone_id]", "aliases: [cost]", 1),
		"field range":    strings.Replace(good, "field: 1", "field: 21", 1),
		"postback token": strings.Replace(good, "{payout|80%}", "{revenue}", 1),
		"unknown key":    good + "colour: red\n",
		"no sources":     strings.Replace(good, "sources: [https://acme.example/docs]\n", "", 1),
		"token replace":  strings.Replace(good, `token: "{zoneid}"`, `token: "REPLACE"`, 1),
	} {
		if _, err := Load(fstest.MapFS{"traffic-sources/acme_native.yaml": {Data: []byte(bad)}}); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
