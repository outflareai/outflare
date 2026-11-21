package api

import (
	"encoding/json"
	"testing"

	"github.com/outflareai/outflare/engine/internal/insights"
)

func TestWithDashboardFilters(t *testing.T) {
	f := []insights.Filter{{Property: "plan", Op: "eq", Value: "pro"}}
	cases := []struct{ kind, in, want string }{
		{"trend", `{"series":[{"kind":"event","event":"x","filters":[{"property":"a","op":"exists"}]},{"kind":"visit"},{"kind":"page"}]}`,
			`{"series":[{"event":"x","filters":[{"op":"exists","property":"a"},{"op":"eq","property":"plan","value":"pro"}],"kind":"event"},{"kind":"visit"},{"filters":[{"op":"eq","property":"plan","value":"pro"}],"kind":"page"}]}`},
		{"funnel", `{"steps":[{"kind":"ad"},{"kind":"event","event":"x"}]}`, `{"steps":[{"kind":"ad"},{"event":"x","filters":[{"op":"eq","property":"plan","value":"pro"}],"kind":"event"}]}`},
		{"retention", `{"start":{"kind":"event","event":"x"},"return":{"kind":"payment"}}`, `{"return":{"kind":"payment"},"start":{"event":"x","filters":[{"op":"eq","property":"plan","value":"pro"}],"kind":"event"}}`},
		{"lifecycle", `{"step":{"kind":"page"}}`, `{"step":{"filters":[{"op":"eq","property":"plan","value":"pro"}],"kind":"page"}}`},
		{"paths", `{"start":{"kind":"page","value":"/"}}`, `{"start":{"kind":"page","value":"/"}}`},
	}
	for _, c := range cases {
		out, err := withDashboardFilters(c.kind, []byte(c.in), f)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		_ = json.Unmarshal(out, &a)
		_ = json.Unmarshal([]byte(c.want), &b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s: got %s, want %s", c.kind, ja, jb)
		}
	}
	if out, _ := withDashboardFilters("trend", []byte(`{"series":[]}`), nil); string(out) != `{"series":[]}` {
		t.Errorf("no filters changes nothing: %s", out)
	}
}
