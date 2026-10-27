package inbox

import "testing"

func TestCategorize(t *testing.T) {
	for _, c := range []struct {
		kind, body, want string
		optOut           bool
	}{
		{"reply", "Sounds interesting, what times work next week?", "interested", false},
		{"reply", "Not interested, thanks.\n\nOn Mon, 1 Jan 2026 Ann wrote:\n> Are you interested?", "not_interested", false},
		{"reply", "I'm not the right person, please reach out to Sam Lee.", "referral", false},
		{"reply", "Joe left the company last year.", "wrong_person", false},
		{"reply", "Bad timing, try me next quarter.", "not_now", false},
		{"reply", "Booked a slot on your calendar for Thursday.", "meeting_booked", false},
		{"reply", "How does it compare to Instantly?", "question", false},
		{"reply", "Thanks.", "", false},
		{"reply", "please unsubscribe me", "unsubscribe", true},
		{"auto_reply", "I am out of the office until 3 November.", "out_of_office", false},
		{"auto_reply", "Thanks for your email, we'll respond within 2 days.", "auto_reply", false},
		{"reply", "This is spam. How did you get my email? Stop emailing me.", "complaint", true},
		{"reply", "I've reported you as spam", "complaint", false},
		{"bounce", "Delivery failed", "", false},
	} {
		if got := Categorize(c.kind, c.optOut, c.body); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

func TestExpand(t *testing.T) {
	got, bad := Expand([]string{"hot", "not_now", "interested"})
	if bad != "" || len(got) != 4 || got[0] != "interested" || got[3] != "not_now" {
		t.Fatalf("got %v, bad %q", got, bad)
	}
	if _, bad := Expand([]string{"warm"}); bad != "warm" {
		t.Fatalf("unknown name not reported: %q", bad)
	}
	for _, c := range Categories {
		if GroupOf(c) == "" {
			t.Errorf("%s has no group", c)
		}
	}
}
