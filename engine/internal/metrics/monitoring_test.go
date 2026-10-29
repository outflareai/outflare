package metrics

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.yaml.in/yaml/v4"
)

// The dashboard and alert rules in deploy/monitoring must keep up with the
// metrics (AGENTS.md, "Monitoring"): a renamed or removed metric would leave
// a panel empty or an alert that never fires, silently.

const monitoringDir = "../../../deploy/monitoring/"

var metricName = regexp.MustCompile(`\boutflare_[a-z0-9_]+`)

// known is every outflare_ series name the engine can export.
func known(t *testing.T) map[string]bool {
	t.Helper()
	ch := make(chan *prometheus.Desc, 256)
	for _, c := range own {
		c.Describe(ch)
	}
	(&Postgres{}).Describe(ch)
	close(ch)
	fq := regexp.MustCompile(`fqName: "([^"]+)"`)
	names := map[string]bool{}
	for d := range ch {
		if m := fq.FindStringSubmatch(d.String()); m != nil {
			names[m[1]] = true
			// Histograms and summaries are queried by their series.
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				names[m[1]+suffix] = true
			}
		}
	}
	// Metrics other packages register on Registry, once they have a value.
	families, err := Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		names[f.GetName()] = true
	}
	return names
}

func TestAlertRulesAreValid(t *testing.T) {
	raw, err := os.ReadFile(monitoringDir + "alerts.yml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Groups []struct {
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Expr        string            `yaml:"expr"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	names := known(t)
	seen := map[string]bool{}
	for _, g := range doc.Groups {
		for _, r := range g.Rules {
			if r.Alert == "" || strings.TrimSpace(r.Expr) == "" {
				t.Errorf("rule %q needs an alert name and an expr", r.Alert)
				continue
			}
			if seen[r.Alert] {
				t.Errorf("alert %s is defined twice", r.Alert)
			}
			seen[r.Alert] = true
			if !slices.Contains([]string{"critical", "warning", "info"}, r.Labels["severity"]) {
				t.Errorf("alert %s: severity %q, want critical, warning or info", r.Alert, r.Labels["severity"])
			}
			if strings.TrimSpace(r.Annotations["summary"]) == "" {
				t.Errorf("alert %s has no summary", r.Alert)
			}
			for _, n := range metricName.FindAllString(r.Expr, -1) {
				if !names[n] {
					t.Errorf("alert %s uses %s, which the engine doesn't export", r.Alert, n)
				}
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no alert rules found")
	}
}

func TestDashboardUsesRealMetrics(t *testing.T) {
	raw, err := os.ReadFile(monitoringDir + "grafana/dashboards/outflare.json")
	if err != nil {
		t.Fatal(err)
	}
	var dash any
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatal(err)
	}
	names := known(t)
	exprs := 0
	var walk func(v any, panel string)
	walk = func(v any, panel string) {
		switch v := v.(type) {
		case map[string]any:
			if title, ok := v["title"].(string); ok {
				panel = title
			}
			for k, c := range v {
				if s, ok := c.(string); ok && k == "expr" {
					exprs++
					for _, n := range metricName.FindAllString(s, -1) {
						if !names[n] {
							t.Errorf("panel %q uses %s, which the engine doesn't export", panel, n)
						}
					}
					continue
				}
				walk(c, panel)
			}
		case []any:
			for _, c := range v {
				walk(c, panel)
			}
		}
	}
	walk(dash, "")
	if exprs == 0 {
		t.Fatal("no queries found in the dashboard")
	}
}

// Pull requests that add dashboard rows in parallel each take "the next
// free" panel id and row, so they can clash after merging: Grafana then
// mixes up panels that share an id and draws overlapping ones on top of
// each other.
func TestDashboardLayoutIsClean(t *testing.T) {
	raw, err := os.ReadFile(monitoringDir + "grafana/dashboards/outflare.json")
	if err != nil {
		t.Fatal(err)
	}
	type panel struct {
		ID      int    `json:"id"`
		Title   string `json:"title"`
		GridPos struct {
			X, Y, W, H int
		} `json:"gridPos"`
		Panels []panel `json:"panels"`
	}
	var dash struct {
		Panels []panel `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatal(err)
	}
	var all []panel
	for _, p := range dash.Panels {
		all = append(all, p)
		// A collapsed row keeps its panels inside it; they share the id
		// space but are laid out only when the row opens.
		all = append(all, p.Panels...)
	}
	if len(all) == 0 {
		t.Fatal("no panels found in the dashboard")
	}
	ids := map[int]string{}
	for _, p := range all {
		if prev, ok := ids[p.ID]; ok {
			t.Errorf("panels %q and %q share id %d; give the later one an id above the current highest", prev, p.Title, p.ID)
		}
		ids[p.ID] = p.Title
	}
	top := dash.Panels
	for i, a := range top {
		g := a.GridPos
		if g.W <= 0 || g.H <= 0 || g.X < 0 || g.Y < 0 || g.X+g.W > 24 {
			t.Errorf("panel %q has gridPos %+v outside the 24-column grid", a.Title, g)
		}
		for _, b := range top[i+1:] {
			h := b.GridPos
			if g.X < h.X+h.W && h.X < g.X+g.W && g.Y < h.Y+h.H && h.Y < g.Y+g.H {
				t.Errorf("panels %q %+v and %q %+v overlap; move the later section down", a.Title, g, b.Title, h)
			}
		}
	}
}
