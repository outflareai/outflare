package infra

import (
	"strings"
	"testing"
	"time"
)

func at(days int) *time.Time {
	t := monday.AddDate(0, 0, days)
	return &t
}

func TestNextStage(t *testing.T) {
	cases := []struct {
		name   string
		f      MailboxFacts
		stage  string
		same   bool
		reason string
	}{
		{"warming before its date", MailboxFacts{Stage: StageWarming, StageUntil: at(3), WarmupEnabled: true, Placed: 100, Inbox: 100},
			StageWarming, true, "Warming up until Thu 29 Oct"},
		{"warming with warm-up off", MailboxFacts{Stage: StageWarming, StageUntil: at(-1)},
			StageWarming, true, "Warm-up is off"},
		{"warming without a score", MailboxFacts{Stage: StageWarming, WarmupEnabled: true},
			StageWarming, true, "first warm-up score"},
		{"warming below ready", MailboxFacts{Stage: StageWarming, WarmupEnabled: true, Placed: 100, Inbox: 80},
			StageWarming, true, "Inbox placement is 80%; ready at 85%"},
		{"warming, domain broken", MailboxFacts{Stage: StageWarming, WarmupEnabled: true, Placed: 100, Inbox: 95, DomainCritical: true},
			StageWarming, true, "critical issue"},
		{"warmed up", MailboxFacts{Stage: StageWarming, StageUntil: at(-1), WarmupEnabled: true, Placed: 100, Inbox: 90},
			StageReady, false, "Warmed up: 90%"},
		{"ready, no auto rest", MailboxFacts{Stage: StageReady, Placed: 100, Inbox: 10},
			StageReady, true, ""},
		{"ready, placement drops", MailboxFacts{Stage: StageReady, AutoRest: true, Placed: 100, Inbox: 70},
			StageResting, false, "fell to 70%"},
		{"ready, custom threshold", MailboxFacts{Stage: StageReady, AutoRest: true, Placed: 100, Inbox: 70, RestBelow: 0.6},
			StageReady, true, ""},
		{"ready, blocklisted", MailboxFacts{Stage: StageReady, AutoRest: true, DomainCritical: true},
			StageResting, false, "critical issue"},
		{"ready, too little data", MailboxFacts{Stage: StageReady, AutoRest: true, Placed: 10, Inbox: 0},
			StageReady, true, ""},
		{"resting before its end", MailboxFacts{Stage: StageResting, StageUntil: at(2), AutoRest: true, Placed: 100, Inbox: 10},
			StageResting, true, ""},
		{"rest over, recovered", MailboxFacts{Stage: StageResting, StageUntil: at(-1), AutoRest: true, Placed: 100, Inbox: 90},
			StageReady, false, "Rest over"},
		{"rest over, manual rest", MailboxFacts{Stage: StageResting, StageUntil: at(-1), Placed: 100, Inbox: 10},
			StageReady, false, "Rest over"},
		{"rest over, not recovered", MailboxFacts{Stage: StageResting, StageUntil: at(-1), StageChangedAt: monday.AddDate(0, 0, -7), AutoRest: true, Placed: 100, Inbox: 80},
			StageResting, true, "Resting 3 more days: inbox placement is still 80%"},
		{"rested too long", MailboxFacts{Stage: StageResting, StageUntil: at(-1), StageChangedAt: monday.AddDate(0, 0, -15), AutoRest: true, Placed: 100, Inbox: 50, RetireAfter: 14 * 24 * time.Hour},
			StageRetired, false, "Rested 15 days without recovering"},
		{"retired stays", MailboxFacts{Stage: StageRetired, Placed: 100, Inbox: 100},
			StageRetired, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NextStage(c.f, monday)
			if got.Stage != c.stage || got.Same != c.same || !strings.Contains(got.Reason, c.reason) || (c.reason == "" && got.Reason != "") {
				t.Errorf("got %+v, want stage %s same %v reason containing %q", got, c.stage, c.same, c.reason)
			}
			if got.Stage == StageResting && !got.Same && (got.Until == nil || !got.Until.Equal(monday.AddDate(0, 0, RestDays))) {
				t.Errorf("rest until %v, want %d days", got.Until, RestDays)
			}
		})
	}
}

func TestCanMove(t *testing.T) {
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{StageReady, StageResting, true},
		{StageResting, StageReady, true},
		{StageWarming, StageReady, true},
		{StageReady, StageWarming, true},
		{StageReady, StageRetired, true},
		{StageRetired, StageReady, false},
		{StageRetired, StageRetired, false},
		{StageReady, StageReady, false},
	} {
		if got := CanMove(c.from, c.to); got != c.ok {
			t.Errorf("CanMove(%s, %s) = %v", c.from, c.to, got)
		}
	}
}
