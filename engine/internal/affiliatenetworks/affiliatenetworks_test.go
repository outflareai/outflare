package affiliatenetworks

import (
	"strings"
	"testing"
	"testing/fstest"
)

// Every template in integrations/affiliate-networks loads and is valid, and
// the main network platforms are there.
func TestCatalogue(t *testing.T) {
	list, err := All()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]Template{}
	for _, tpl := range list {
		have[tpl.ID] = tpl
	}
	for _, id := range []string{"everflow", "tune", "cake", "affise"} {
		if have[id].Kind != "platform" {
			t.Errorf("no platform template for %s", id)
		}
	}
	if len(list) < 15 {
		t.Errorf("the catalogue has %d networks; plans/ad-tracker.md (T6a) wants the common ones", len(list))
	}
}

func TestLoadRefusesBadTemplates(t *testing.T) {
	good := "id: acme\nname: Acme\nkind: network\nclick_id_param: sub1\nmacros: {cid: \"{sub1}\", payout: \"{payout}\"}\nsources: [https://acme.example/docs]\n"
	for name, c := range map[string]struct{ file, body, want string }{
		"good":       {"acme", good, ""},
		"file name":  {"other", good, "must match"},
		"kind":       {"acme", strings.Replace(good, "kind: network", "kind: tracker", 1), "kind is one of"},
		"no cid":     {"acme", strings.Replace(good, `cid: "{sub1}", `, "", 1), "macros.cid is required"},
		"value":      {"acme", strings.Replace(good, "payout:", "revenue:", 1), "isn't a postback value"},
		"space":      {"acme", strings.Replace(good, `"{payout}"`, `"{pay out}"`, 1), "can't have spaces"},
		"param":      {"acme", strings.Replace(good, "click_id_param: sub1", "click_id_param: \"sub 1\"", 1), "is a URL parameter"},
		"no sources": {"acme", strings.Replace(good, "sources: [https://acme.example/docs]\n", "", 1), "sources lists"},
		"http":       {"acme", strings.Replace(good, "https://acme", "http://acme", 1), "is an https address"},
		"unknown":    {"acme", good + "cost: x\n", "not found"},
	} {
		_, err := Load(fstest.MapFS{"affiliate-networks/" + c.file + ".yaml": {Data: []byte(c.body)}})
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

// A postback URL's query lists the values in order, in the network's
// macros, or Outflare's own token names without a template.
func TestQuery(t *testing.T) {
	tpl := Template{Macros: map[string]string{"payout": "{payout}", "cid": "#s2#", "txid": "#tid#"}}
	if got := tpl.Query(); got != "cid=#s2#&payout={payout}&txid=#tid#" {
		t.Errorf("query = %s", got)
	}
	var none *Template
	if got := none.Query(); got != "cid={click_id}&payout={payout}&txid={transaction_id}&currency={currency}&status={status}" {
		t.Errorf("custom query = %s", got)
	}
}
