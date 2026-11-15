package journeys

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every preset of the outflare_action step names a real API call in a
// brand, and a step made from it passes the step's own check once its
// target is picked.
func TestActionPresets(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// operationId -> "METHOD path", read from the document's layout.
	ops := map[string]string{}
	var path, method string
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "  /v1/"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case len(line) > 4 && strings.HasPrefix(line, "    ") && line[4] != ' ' && strings.HasSuffix(line, ":"):
			method = strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(line), ":"))
		default:
			if op, ok := strings.CutPrefix(strings.TrimSpace(line), "operationId:"); ok {
				ops[strings.TrimSpace(op)] = method + " " + path
			}
		}
	}
	param := regexp.MustCompile(`\{[a-z_]+\}`)
	filled := regexp.MustCompile(`\{\{person\.id\}\}|[A-Z]+_ID`)
	const agent = "00000000-0000-4000-8000-000000000001"
	seen := map[string]bool{}
	for _, p := range ActionPresets {
		if seen[p.ID] || p.ID == "" || p.Label == "" || p.Group == "" || p.Description == "" {
			t.Errorf("preset %q needs a unique id, a label, a group and a description", p.ID)
		}
		seen[p.ID] = true
		got, ok := ops[p.OperationID]
		if !ok {
			t.Errorf("preset %s: %q is not an operationId in api/openapi.yaml", p.ID, p.OperationID)
			continue
		}
		want := p.Method + " " + param.ReplaceAllString("/v1/brands/{brand}/", "{}") + filled.ReplaceAllString(p.Path, "{}")
		if param.ReplaceAllString(got, "{}") != want {
			t.Errorf("preset %s calls %s, but %s is %s", p.ID, want, p.OperationID, got)
		}
		st := Step{ID: "a", Kind: "outflare_action", AgentID: agent, Method: p.Method, Path: p.Path}
		if p.Body != nil {
			st.Body = map[string]any(p.Body)
		}
		if p.Pick == nil {
			if msg := checkOutflareAction(st); msg != "" {
				t.Errorf("preset %s: %s", p.ID, msg)
			}
			continue
		}
		if !strings.Contains(p.Path, p.Pick.Token) || p.Pick.Label == "" {
			t.Errorf("preset %s: its path must hold %s, and the pick needs a label", p.ID, p.Pick.Token)
		}
		switch p.Pick.From {
		case "journey", "rule", "smart_list", "ad_campaign":
		default:
			t.Errorf("preset %s: the builder can't list %q", p.ID, p.Pick.From)
		}
		if msg := checkOutflareAction(st); !strings.Contains(msg, p.Pick.Token) {
			t.Errorf("preset %s: a step with %s still in its path must be refused, got %q", p.ID, p.Pick.Token, msg)
		}
		st.Path = strings.ReplaceAll(p.Path, p.Pick.Token, agent)
		if msg := checkOutflareAction(st); msg != "" {
			t.Errorf("preset %s: %s", p.ID, msg)
		}
	}
}
