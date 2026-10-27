package allocation

import (
	"testing"
	"time"
)

func TestNotReadyLifecycleStages(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	until := now.AddDate(0, 0, 3)
	for _, c := range []struct {
		r    Readiness
		want string
	}{
		{Readiness{Status: "active", Stage: "ready"}, ""},
		{Readiness{Status: "active", Stage: "resting", StageUntil: &until}, "mailbox is resting until Thu 29 Oct"},
		{Readiness{Status: "active", Stage: "warming", StageUntil: &until}, "mailbox is warming until Thu 29 Oct"},
		{Readiness{Status: "active", Stage: "resting"}, "mailbox is resting"},
		{Readiness{Status: "paused", Stage: "retired"}, "mailbox is retired"},
		{Readiness{Status: "paused", Stage: "ready"}, "mailbox is paused"},
	} {
		if got := NotReady(c.r, now); got != c.want {
			t.Errorf("NotReady(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestNotReadyTreatsPausedServerLikePausedDomain(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		r    Readiness
		want string
	}{
		{Readiness{Status: "active", SPF: "pass"}, ""},
		{Readiness{Status: "active", SPF: "pass", DomainPaused: true}, "domain is paused"},
		{Readiness{Status: "active", SPF: "pass", ServerPaused: true}, "sending server is paused"},
		{Readiness{Status: "paused", SPF: "pass", ServerPaused: true}, "mailbox is paused"},
	} {
		if got := NotReady(c.r, now); got != c.want {
			t.Errorf("NotReady(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}
