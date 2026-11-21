package api

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/leadfeed"
)

func TestFeedDecide(t *testing.T) {
	two := []string{"ana@acme.example", "ben@birch.example"}
	for _, c := range []struct {
		name                   string
		x                      leadfeed.Extracted
		r                      feedReading
		ok                     bool
		email, domain, outcome string
	}{
		{"one address", leadfeed.Extracted{Emails: []string{"ana@acme.example"}}, feedReading{}, false, "ana@acme.example", "acme.example", "person"},
		{"one address, AI agrees", leadfeed.Extracted{Emails: []string{"ana@acme.example"}}, feedReading{Email: "ana@acme.example"}, true, "ana@acme.example", "acme.example", "person"},
		{"free mail has no company", leadfeed.Extracted{Emails: []string{"ana@gmail.com"}}, feedReading{}, false, "ana@gmail.com", "", "person"},
		{"several, no AI", leadfeed.Extracted{Emails: two}, feedReading{}, false, "", "", "needs_look"},
		{"several, AI picks", leadfeed.Extracted{Emails: two}, feedReading{Email: "ben@birch.example"}, true, "ben@birch.example", "birch.example", "person"},
		{"several, AI unsure", leadfeed.Extracted{Emails: two}, feedReading{}, true, "", "", "needs_look"},
		{"several, AI names another", leadfeed.Extracted{Emails: two}, feedReading{Email: "cy@cedar.example"}, true, "", "", "needs_look"},
		{"none, one link", leadfeed.Extracted{Links: []string{"https://www.birch.example/pricing"}}, feedReading{}, false, "", "birch.example", "company"},
		{"none, two links", leadfeed.Extracted{Links: []string{"https://birch.example", "https://cedar.example"}}, feedReading{}, false, "", "", "needs_look"},
		{"none, AI website", leadfeed.Extracted{Links: []string{"https://birch.example", "https://cedar.example"}}, feedReading{Website: "https://cedar.example"}, true, "", "cedar.example", "company"},
		{"none, free mail website", leadfeed.Extracted{Links: []string{"https://gmail.com"}}, feedReading{}, false, "", "", "needs_look"},
		{"nothing", leadfeed.Extracted{}, feedReading{}, false, "", "", "needs_look"},
		{"spam", leadfeed.Extracted{Emails: []string{"ana@acme.example"}}, feedReading{Spam: true, SpamConfidence: 0.95}, true, "", "", "spam"},
		{"spam at the line", leadfeed.Extracted{Emails: []string{"ana@acme.example"}}, feedReading{Spam: true, SpamConfidence: feedSpamSure}, true, "", "", "spam"},
		{"spam unsure", leadfeed.Extracted{Emails: []string{"ana@acme.example"}}, feedReading{Spam: true, SpamConfidence: 0.6}, true, "ana@acme.example", "acme.example", "person"},
		{"spam without AI is ignored", leadfeed.Extracted{Emails: []string{"ana@acme.example"}}, feedReading{Spam: true, SpamConfidence: 1}, false, "ana@acme.example", "acme.example", "person"},
	} {
		t.Run(c.name, func(t *testing.T) {
			email, domain, outcome := feedDecide(c.x, c.r, c.ok)
			if email != c.email || domain != c.domain || outcome != c.outcome {
				t.Fatalf("got %q %q %q, want %q %q %q", email, domain, outcome, c.email, c.domain, c.outcome)
			}
		})
	}
}
