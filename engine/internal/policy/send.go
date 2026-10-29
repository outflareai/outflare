package policy

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"time"
)

// Window is when a sequence may send: on the given ISO weekdays (1 is
// Monday), between Start and End local time in Location.
type Window struct {
	Location *time.Location
	Days     []int
	Start    time.Duration // since midnight
	End      time.Duration
}

// Next returns t if it is inside the window, else the next time the window
// opens. A window with no days never opens; Next then returns the zero time.
func (w Window) Next(t time.Time) time.Time {
	if len(w.Days) == 0 || w.Start >= w.End {
		return time.Time{}
	}
	local := t.In(w.Location)
	for i := 0; i < 8; i++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+i, 0, 0, 0, 0, w.Location)
		wd := int(day.Weekday())
		if wd == 0 {
			wd = 7
		}
		if !slices.Contains(w.Days, wd) {
			continue
		}
		start, end := addClock(day, w.Start), addClock(day, w.End)
		switch {
		case local.Before(start):
			return start
		case local.Before(end):
			return t
		}
	}
	return time.Time{}
}

// addClock adds a wall-clock offset to midnight, so the result is right on
// days when daylight saving starts or ends.
func addClock(midnight time.Time, d time.Duration) time.Time {
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	return time.Date(midnight.Year(), midnight.Month(), midnight.Day(), h, m, 0, 0, midnight.Location())
}

// Mailbox is what the policy needs to know about a sending mailbox.
type Mailbox struct {
	Status string
	// Lifecycle stage: only ready mailboxes send cold email. Warming,
	// resting and retired ones wait.
	Stage      string
	DailyLimit int
	SentToday  int       // sends in the last 24 hours, including in-flight ones
	LastSentAt time.Time // zero if never
	MinGap     time.Duration
	// The mailbox's domain: paused, and its cap on sends from all of its
	// mailboxes together in 24 hours (0 is no cap).
	DomainPaused    bool
	DomainLimit     int
	DomainSentToday int
	// The mailbox's sending server (shared SMTP host and IPs), the same way.
	// Google and Microsoft mailboxes have none.
	ServerPaused    bool
	ServerLimit     int
	ServerSentToday int
}

// Verdict is the outcome of a check that can wait: Allow, or not before
// DeferUntil, with the reason.
type Verdict struct {
	Allow      bool
	DeferUntil time.Time
	Reason     string
}

// Reasons a send waits.
const (
	OutsideWindow   = "outside_sending_window"
	MailboxInactive = "mailbox_not_active"
	MailboxNotReady = "mailbox_not_ready"
	DailyLimit      = "mailbox_daily_limit"
	MailboxGap      = "mailbox_min_gap"
	NewLeadLimit    = "sequence_daily_new_limit"
	SequenceLimit   = "sequence_daily_send_limit"
	DomainPaused    = "domain_paused"
	DomainLimit     = "domain_daily_limit"
	ServerPaused    = "server_paused"
	ServerLimit     = "server_daily_limit"
	NoMailbox       = "no_mailbox_allocated"
	SenderGone      = "sender_mailbox_gone"
	TokenBudget     = "token_daily_budget"
	ProjectPaused   = "project_paused"
	ProjectLimit    = "project_daily_limit"
	// The sequence was started in an agent mission that is paused, or
	// whose send limit is used up.
	MissionPaused = "mission_paused"
	MissionBudget = "mission_send_limit"
)

// CheckMailbox decides whether a mailbox may send at now.
func CheckMailbox(m Mailbox, now time.Time) Verdict {
	switch {
	case m.Status != "active":
		return Verdict{DeferUntil: now.Add(15 * time.Minute), Reason: MailboxInactive}
	case m.Stage != "" && m.Stage != "ready":
		return Verdict{DeferUntil: now.Add(time.Hour), Reason: MailboxNotReady}
	case m.DomainPaused:
		return Verdict{DeferUntil: now.Add(15 * time.Minute), Reason: DomainPaused}
	case m.ServerPaused:
		return Verdict{DeferUntil: now.Add(15 * time.Minute), Reason: ServerPaused}
	case m.SentToday >= m.DailyLimit:
		return Verdict{DeferUntil: now.Add(time.Hour), Reason: DailyLimit}
	case m.DomainLimit > 0 && m.DomainSentToday >= m.DomainLimit:
		return Verdict{DeferUntil: now.Add(time.Hour), Reason: DomainLimit}
	case m.ServerLimit > 0 && m.ServerSentToday >= m.ServerLimit:
		return Verdict{DeferUntil: now.Add(time.Hour), Reason: ServerLimit}
	case m.MinGap > 0 && !m.LastSentAt.IsZero() && now.Sub(m.LastSentAt) < m.MinGap:
		return Verdict{DeferUntil: m.LastSentAt.Add(m.MinGap).Add(Jitter(m.MinGap)), Reason: MailboxGap}
	}
	return Verdict{Allow: true}
}

// CheckWindow decides whether now is inside the sending window.
func CheckWindow(w Window, now time.Time) (Verdict, error) {
	next := w.Next(now)
	if next.IsZero() {
		return Verdict{}, fmt.Errorf("the sending window has no open hours")
	}
	if next.Equal(now) {
		return Verdict{Allow: true}, nil
	}
	// Spread sends over the first minutes of the window instead of all at
	// the opening second.
	return Verdict{DeferUntil: next.Add(Jitter(20 * time.Minute)), Reason: OutsideWindow}, nil
}

// Jitter returns a random duration up to half of d.
func Jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d / 2)))
}
