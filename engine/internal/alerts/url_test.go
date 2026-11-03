package alerts

import "testing"

func TestURL(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"inbox/r1", "https://app.example/acme/inbox/r1"},
		{"alerts", "https://app.example/acme/alerts"},
		{"/billing", "https://app.example/billing"},
		{"//elsewhere.example/x", "https://app.example/acme/elsewhere.example/x"}, // never another host
	} {
		if got := URL("https://app.example", "acme", c.path); got != c.want {
			t.Errorf("%q: %s, want %s", c.path, got, c.want)
		}
	}
}
