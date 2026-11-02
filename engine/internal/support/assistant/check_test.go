package assistant

import (
	"strings"
	"testing"
)

func TestStop(t *testing.T) {
	for _, c := range []struct {
		name    string
		b       Before
		reason  string
		handoff bool
	}{
		{"a plain question", Before{Body: "How do I export a chart?", From: "ana@acme.example"}, "", false},
		{"out of office", Before{Body: "I'm away", AutoReply: true}, "auto_reply", false},
		{"no-reply sender", Before{Body: "Your invoice", From: "no-reply@billing.example"}, "no_reply_sender", false},
		{"a teammate is on it", Before{Body: "Thanks!", TeamReplied: true}, "team_handling", false},
		{"enough AI replies", Before{Body: "Still broken", AIReplies: 3}, "too_many_replies", true},
		{"asks for a person", Before{Body: "Can I talk to a real person please"}, "asked_for_person", true},
		{"a chargeback", Before{Body: "I'll file a chargeback if this isn't fixed"}, "sensitive_topic", true},
		{"data deletion", Before{Body: "Please delete my data under GDPR"}, "sensitive_topic", true},
		{"hacked", Before{Body: "I think my account was hacked"}, "sensitive_topic", true},
	} {
		reason, handoff := Stop(c.b)
		if reason != c.reason || handoff != c.handoff {
			t.Errorf("%s: %q %v, want %q %v", c.name, reason, handoff, c.reason, c.handoff)
		}
	}
}

func TestLint(t *testing.T) {
	s := Style{Channel: Chat, Hosts: []string{"help.northpeak.example", "northpeak.example"},
		Sources: "Email billing@northpeak.example or call +1 415 555 0100. Refunds within 30 days."}
	if issues := Lint("Yes: open the chart, then More, Export CSV.", s); len(issues) != 0 {
		t.Fatalf("clean reply: %v", issues)
	}
	if issues := Lint("Refunds take 30 days. Write to billing@northpeak.example or call +1 415 555 0100. See https://help.northpeak.example/articles/x", s); len(issues) != 0 {
		t.Fatalf("sourced details: %v", issues)
	}
	for _, c := range []struct{ reply, want string }{
		{"Great question! Yes, you can.", `drop "great question"`},
		{"Yes — open Settings.", "no dashes"},
		{"I hope this helps.", `drop "i hope this helps"`},
		{"See https://evil.example/login for that.", "evil.example isn't one of the business's own pages"},
		{"See https://northpeak.example/a and https://northpeak.example/b", "at most one link"},
		{"Email ceo@northpeak.example.", "ceo@northpeak.example isn't in the articles"},
		{"Call +1 212 555 0199.", "isn't in the articles"},
		{"## Refunds\nWithin 30 days.", "no headings"},
		{strings.Repeat("word ", 80), "keep it under 70"},
	} {
		issues := Lint(c.reply, s)
		if !strings.Contains(strings.Join(issues, " | "), c.want) {
			t.Errorf("%q: %v, want %q", c.reply, issues, c.want)
		}
	}
	// Dates and prices aren't phone numbers.
	if issues := Lint("It renews on 2026-11-01 at $1,490.00.", s); len(issues) != 0 {
		t.Errorf("date and price: %v", issues)
	}
	// Email allows more words than chat.
	if issues := Lint(strings.Repeat("word ", 100), Style{Channel: Email}); len(issues) != 0 {
		t.Errorf("email length: %v", issues)
	}
}
