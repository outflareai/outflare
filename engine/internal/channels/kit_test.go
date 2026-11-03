package channels

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// The channel test kit: every channel's manifest is held to the plan's
// bar (section 3a). Each action is a real API write the action layer can
// hold (a 202 in the document, a scope); each read is a real operation;
// each playbook ships; each capability has at least one connector; each
// data check exists; the dashboard row, the metrics, the alerts and the
// tests it names exist. A channel that fails here is not one the agent
// may use.

const repo = "../../.."

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type operation struct {
	method, path, scope string
	held                bool
}

// operations reads api/openapi.yaml line by line (the document is too
// large and anchored for a full parse here): each operation's method,
// path, scope and whether it answers 202 (held for a person).
func operations(t *testing.T) map[string]operation {
	t.Helper()
	ops := map[string]operation{}
	var path, method, id string
	var op operation
	flush := func() {
		if id != "" {
			op.method, op.path = method, path
			ops[id] = op
		}
		id, op = "", operation{}
	}
	pathRe := regexp.MustCompile(`^  (/v1/\S+):\s*$`)
	methodRe := regexp.MustCompile(`^    (get|post|patch|put|delete):\s*$`)
	idRe := regexp.MustCompile(`^      operationId: (\S+)`)
	scopeRe := regexp.MustCompile(`^      x-outflare-access: .*scope: ([a-z_.]+)`)
	for _, line := range strings.Split(read(t, "api/openapi.yaml"), "\n") {
		switch {
		case pathRe.MatchString(line):
			flush()
			path = pathRe.FindStringSubmatch(line)[1]
		case methodRe.MatchString(line):
			flush()
			method = strings.ToUpper(methodRe.FindStringSubmatch(line)[1])
		case idRe.MatchString(line):
			id = idRe.FindStringSubmatch(line)[1]
		case scopeRe.MatchString(line):
			op.scope = scopeRe.FindStringSubmatch(line)[1]
		case strings.HasPrefix(line, `        "202":`):
			op.held = true
		}
	}
	flush()
	return ops
}

func TestEveryChannelPassesItsKit(t *testing.T) {
	ops := operations(t)
	all, err := playbooks.All()
	if err != nil {
		t.Fatal(err)
	}
	shipped := map[string]bool{}
	for _, p := range all {
		shipped[p.ID] = true
	}
	registry := catalog.New()
	capabilities := map[string]int{}
	connectorIDs := map[string]bool{}
	for _, d := range registry.All() {
		connectorIDs[d.ID] = true
		for _, c := range d.Capabilities {
			capabilities[c]++
		}
	}
	var dash struct {
		Panels []struct {
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(read(t, "deploy/monitoring/grafana/dashboards/outflare.json")), &dash); err != nil {
		t.Fatal(err)
	}
	rows := map[string]bool{}
	for _, p := range dash.Panels {
		if p.Type == "row" {
			rows[p.Title] = true
		}
	}
	dashboard := read(t, "deploy/monitoring/grafana/dashboards/outflare.json")
	alerts := read(t, "deploy/monitoring/alerts.yml")
	metricsSrc := read(t, "engine/internal/metrics/metrics.go")
	exported := map[string]bool{}
	for _, m := range regexp.MustCompile(`Subsystem: "([a-z_]+)", Name: "([a-z_]+)"`).FindAllStringSubmatch(metricsSrc, -1) {
		exported["outflare_"+m[1]+"_"+m[2]] = true
	}
	tests := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(repo, "engine/internal/api/*_test.go"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`).FindAllStringSubmatch(string(b), -1) {
			tests[m[1]] = true
		}
	}
	kinds := regexp.MustCompile(`^[a-z_]+\.[a-z_]+$`)
	seen := map[string]bool{}
	for _, c := range All() {
		bad := func(format string, args ...any) {
			t.Helper()
			t.Errorf("channel %s: "+format, append([]any{c.ID}, args...)...)
		}
		if seen[c.ID] {
			bad("listed twice")
		}
		seen[c.ID] = true
		if c.Name == "" || c.Summary == "" || len(c.Goals) == 0 {
			bad("needs a name, a summary and goals")
		}
		if len(c.Actions) == 0 || len(c.Reads) == 0 || len(c.Measures) == 0 {
			bad("needs actions, reads and measures")
		}
		for _, a := range c.Actions {
			op, ok := ops[a.OperationID]
			switch {
			case !ok:
				bad("action %s names %s, which api/openapi.yaml doesn't have", a.Kind, a.OperationID)
			case op.method == "GET":
				bad("action %s names a read, %s", a.Kind, a.OperationID)
			case op.scope == "":
				bad("action %s: %s has no x-outflare-access scope", a.Kind, a.OperationID)
			case !op.held && a.Category != CategoryAlwaysPerson:
				bad("action %s: %s has no 202 answer, so the action layer can't hold it", a.Kind, a.OperationID)
			}
			if !kinds.MatchString(a.Kind) {
				bad("action kind %q is object.verb", a.Kind)
			}
			if !slices.Contains([]string{CategoryRoutine, CategorySpend, CategoryAlwaysPerson}, a.Category) {
				bad("action %s: category %q", a.Kind, a.Category)
			}
			if a.Category == CategoryAlwaysPerson && a.DefaultDailyLimit != 0 {
				bad("action %s always needs a person, so it has no daily limit", a.Kind)
			}
		}
		for _, r := range c.Reads {
			if op, ok := ops[r]; !ok {
				bad("read %s isn't in api/openapi.yaml", r)
			} else if op.method != "GET" && op.method != "POST" {
				bad("read %s is a %s", r, op.method)
			}
		}
		for _, m := range c.Measures {
			if m.Metric == "" || m.Source == "" {
				bad("a measure needs a metric and a source")
			}
			if _, ok := ops[m.OperationID]; !ok {
				bad("measure %s names %s, which api/openapi.yaml doesn't have", m.Metric, m.OperationID)
			}
		}
		for _, p := range c.Playbooks {
			if !shipped[p] {
				bad("playbook %s doesn't ship", p)
			}
		}
		for _, n := range c.Needs.Connections {
			switch {
			case n.Capability == "" && n.Category == "":
				bad("a connection need names a capability or a category")
			case n.Capability != "" && capabilities[n.Capability] == 0:
				bad("no connector has the capability %s", n.Capability)
			}
			for _, r := range n.Recommended {
				if !connectorIDs[r] {
					bad("recommends %s, which isn't a connector", r)
				}
			}
		}
		for _, d := range c.Needs.Data {
			if _, ok := playbooks.DataChecks[d.Check]; !ok {
				if _, ok := DataChecks[d.Check]; !ok {
					bad("data check %s doesn't exist", d.Check)
				}
			}
		}
		if c.Docs == "" {
			bad("names no docs page")
		} else if _, err := os.Stat(filepath.Join(repo, c.Docs)); err != nil {
			bad("docs page %s is missing", c.Docs)
		}
		if !rows[c.Dashboard] {
			bad("dashboard row %q isn't in outflare.json", c.Dashboard)
		}
		if len(c.Metrics) == 0 {
			bad("names no metrics")
		}
		for _, m := range c.Metrics {
			if !exported[m] {
				bad("metric %s isn't exported by engine/internal/metrics", m)
			}
			if !strings.Contains(dashboard, m) {
				bad("metric %s is on no dashboard panel", m)
			}
		}
		for _, a := range c.Alerts {
			if !strings.Contains(alerts, "alert: "+a+"\n") {
				bad("alert %s isn't in alerts.yml", a)
			}
		}
		if len(c.Tests) == 0 {
			bad("names no tests")
		}
		for _, name := range c.Tests {
			if !tests[name] {
				bad("test %s isn't in engine/internal/api", name)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no channels")
	}
}

// Readiness states follow the needs: a missing required connection is
// connect first, missing data is needs data, and optional connections
// never hold a channel back.
func TestChannelReadiness(t *testing.T) {
	c, ok := Get("paid_ads")
	if !ok {
		t.Fatal("no paid_ads channel")
	}
	r := c.Check(nil, map[string]bool{})
	if r.State != ConnectFirst || len(r.MissingConnections) != 1 {
		t.Fatalf("nothing connected: %+v", r)
	}
	connected := []playbooks.ConnectorInfo{{Category: "ads", Capabilities: []string{"ads.read", "ads.manage"}}}
	if r = c.Check(connected, map[string]bool{}); r.State != NeedsData || len(r.MissingData) != 1 {
		t.Fatalf("connected, no accounts: %+v", r)
	}
	if r = c.Check(connected, map[string]bool{"ad_accounts": true}); r.State != Ready || !r.Ready {
		t.Fatalf("ready: %+v", r)
	}
	community, _ := Get("community")
	if r = community.Check([]playbooks.ConnectorInfo{{Category: "social", Capabilities: []string{"social.search", "social.reply"}}}, nil); r.State != Ready || len(r.MissingOptional) != 1 {
		t.Fatalf("an optional connection never holds a channel back: %+v", r)
	}
}
