package api

import "testing"

func TestFollowIntent(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.com/outflareai":            "https://x.com/intent/follow?screen_name=outflareai",
		"https://twitter.com/jackfitzgerald/": "https://x.com/intent/follow?screen_name=jackfitzgerald",
		"https://www.x.com/Some_One":          "https://x.com/intent/follow?screen_name=Some_One",
		"https://x.com/outflareai/status/1":   "https://x.com/outflareai/status/1",
		"https://x.example/outflare":          "https://x.example/outflare",
		"https://mastodon.example/@outflare":  "https://mastodon.example/@outflare",
	} {
		if got := followIntent(in); got != want {
			t.Errorf("followIntent(%q) = %q, want %q", in, got, want)
		}
	}
}
