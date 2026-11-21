package mentions

import "testing"

func TestSourceFor(t *testing.T) {
	for _, c := range []struct{ url, kind, want string }{
		{"https://www.youtube.com/watch?v=abc", "", "youtube"},
		{"https://youtu.be/abc", "", "youtube"},
		{"https://open.spotify.com/episode/1", "", "podcast"},
		{"https://open.spotify.com/track/1", "", ""},
		{"https://x.com/someone/status/1", "", "x"},
		{"https://news.ycombinator.com/item?id=1", "", "hacker_news"},
		{"https://forum.example/t/1", "forum", "web"},
		{"https://shows.example/ep-4", "podcast", "podcast"},
		{"https://blog.example/best-tools", "listicle", ""},
		{"not a url", "forum", ""},
	} {
		if got := SourceFor(c.url, c.kind); got != c.want {
			t.Errorf("SourceFor(%q, %q) = %q, want %q", c.url, c.kind, got, c.want)
		}
	}
}

func TestAtSeconds(t *testing.T) {
	for in, want := range map[string]int{
		"https://youtu.be/abc?t=90":                  90,
		"https://youtu.be/abc?t=90s":                 90,
		"https://www.youtube.com/watch?v=a&t=1m30s":  90,
		"https://www.youtube.com/watch?v=a#t=1h0m2s": 3602,
		"https://www.youtube.com/embed/a?start=12":   12,
	} {
		got := AtSeconds(in)
		if got == nil || *got != want {
			t.Errorf("AtSeconds(%q) = %v, want %d", in, got, want)
		}
	}
	if AtSeconds("https://youtu.be/abc") != nil || AtSeconds("https://youtu.be/abc?t=x9") != nil {
		t.Error("read a start that isn't there")
	}
}
