package api

import "testing"

func TestSummarizeUsesTheSpec(t *testing.T) {
	for _, c := range []struct{ method, path, want string }{
		{"POST", "/v1/workspaces/0b8f7c0e-0000-4000-8000-000000000001/memories", "Remember something"},
		{"POST", "/v1/workspaces/0b8f7c0e-0000-4000-8000-000000000001/link-reads", "Read who links to a page or domain"},
		{"POST", "/v1/workspaces/0b8f7c0e-0000-4000-8000-000000000001/link-reads?fresh=true", "Read who links to a page or domain"},
		{"POST", "/v1/workspaces/w/no-such-things", "Create no such thing"},
		{"POST", "/v1/workspaces/w/no-such-memories", "Create no such memory"},
	} {
		if got := summarize(c.method, c.path); got != c.want {
			t.Errorf("summarize(%s %s) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}
