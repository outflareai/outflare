package policy

import (
	"testing"
	"time"
)

func TestWindowNext(t *testing.T) {
	syd, _ := time.LoadLocation("Australia/Sydney")
	w := Window{Location: syd, Days: []int{1, 2, 3, 4, 5}, Start: 9 * time.Hour, End: 17 * time.Hour}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, syd)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct{ now, want string }{
		{"2026-10-21 10:00", "2026-10-21 10:00"}, // Wednesday, inside
		{"2026-10-21 08:00", "2026-10-21 09:00"}, // before opening
		{"2026-10-21 17:00", "2026-10-22 09:00"}, // at closing
		{"2026-10-23 18:00", "2026-10-26 09:00"}, // Friday evening to Monday
		{"2026-10-25 12:00", "2026-10-26 09:00"}, // Sunday
		{"2026-10-04 08:00", "2026-10-05 09:00"}, // daylight saving starts on Sunday 4 Oct
	}
	for _, c := range cases {
		if got := w.Next(at(c.now)); !got.Equal(at(c.want)) {
			t.Errorf("%s: got %s, want %s", c.now, got.In(syd), c.want)
		}
	}
	if !(Window{Location: syd}).Next(time.Now()).IsZero() {
		t.Error("window with no days opened")
	}
}

func TestCheckMailbox(t *testing.T) {
	now := time.Now()
	ok := Mailbox{Status: "active", DailyLimit: 30, SentToday: 3, LastSentAt: now.Add(-5 * time.Minute), MinGap: 2 * time.Minute}
	if v := CheckMailbox(ok, now); !v.Allow {
		t.Fatalf("ok: %+v", v)
	}
	full := ok
	full.SentToday = 30
	if v := CheckMailbox(full, now); v.Allow || v.Reason != DailyLimit {
		t.Fatalf("full: %+v", v)
	}
	recent := ok
	recent.LastSentAt = now.Add(-30 * time.Second)
	v := CheckMailbox(recent, now)
	if v.Allow || v.Reason != MailboxGap || v.DeferUntil.Before(now.Add(90*time.Second)) || v.DeferUntil.After(now.Add(150*time.Second)) {
		t.Fatalf("gap: %+v", v)
	}
	// No gap set: a send stamped a moment after now, by a concurrent
	// send, does not hold this one back.
	noGap := ok
	noGap.MinGap, noGap.LastSentAt = 0, now.Add(time.Millisecond)
	if v := CheckMailbox(noGap, now); !v.Allow {
		t.Fatalf("no gap: %+v", v)
	}
	paused := ok
	paused.Status = "paused"
	if v := CheckMailbox(paused, now); v.Allow || v.Reason != MailboxInactive {
		t.Fatalf("paused: %+v", v)
	}
	domainPaused := ok
	domainPaused.DomainPaused = true
	if v := CheckMailbox(domainPaused, now); v.Allow || v.Reason != DomainPaused {
		t.Fatalf("domain paused: %+v", v)
	}
	domainFull := ok
	domainFull.DomainLimit, domainFull.DomainSentToday = 100, 100
	if v := CheckMailbox(domainFull, now); v.Allow || v.Reason != DomainLimit {
		t.Fatalf("domain full: %+v", v)
	}
	domainRoom := ok
	domainRoom.DomainLimit, domainRoom.DomainSentToday = 100, 99
	if v := CheckMailbox(domainRoom, now); !v.Allow {
		t.Fatalf("domain room: %+v", v)
	}
	serverPaused := ok
	serverPaused.ServerPaused = true
	if v := CheckMailbox(serverPaused, now); v.Allow || v.Reason != ServerPaused {
		t.Fatalf("server paused: %+v", v)
	}
	serverFull := ok
	serverFull.ServerLimit, serverFull.ServerSentToday = 500, 500
	if v := CheckMailbox(serverFull, now); v.Allow || v.Reason != ServerLimit {
		t.Fatalf("server full: %+v", v)
	}
	serverRoom := ok
	serverRoom.ServerLimit, serverRoom.ServerSentToday = 500, 12
	if v := CheckMailbox(serverRoom, now); !v.Allow {
		t.Fatalf("server room: %+v", v)
	}
}
