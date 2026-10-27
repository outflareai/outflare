package infra

import (
	"fmt"
	"time"
)

// Mailbox lifecycle stages.
const (
	StageWarming = "warming"
	StageReady   = "ready"
	StageResting = "resting"
	StageRetired = "retired"
)

// Placement thresholds.
const (
	// ReadyAt is the warm-up inbox placement a mailbox needs to start, or
	// return to, cold email.
	ReadyAt = 0.85
	// MinPlaced is the least warm-up mail, placed in the last 7 days, that
	// says enough about a mailbox to judge it.
	MinPlaced = 20
	// RestDays is how long an automatic rest lasts; ExtendDays is how much
	// longer it goes on when placement has not recovered.
	RestDays   = 7
	ExtendDays = 3
)

// MailboxFacts is what NextStage needs to know about a mailbox.
type MailboxFacts struct {
	Stage          string
	StageChangedAt time.Time
	StageUntil     *time.Time
	AutoRest       bool
	// WarmupEnabled is whether the connected sequencer that runs the
	// warm-up shows it on.
	WarmupEnabled bool
	// The sequencer's warm-up score, read as Inbox out of Placed: 100
	// placed once there is a score, and none before.
	Placed, Inbox int
	// A blocklist listing or broken DNS on the mailbox's domain.
	DomainCritical bool
	// Rest below this placement (0 to 1). Zero uses 0.75.
	RestBelow float64
	// Retire after resting this long without recovering. Zero never does.
	RetireAfter time.Duration
}

// Change is the outcome of NextStage. Same is true when the stage stays;
// the until may still change, and the reason too unless it is empty.
type Change struct {
	Stage  string
	Reason string
	Until  *time.Time
	Same   bool
}

func (f MailboxFacts) rate() float64 {
	if f.Placed == 0 {
		return 0
	}
	return float64(f.Inbox) / float64(f.Placed)
}

func pct(r float64) string { return fmt.Sprintf("%.0f%%", r*100) }

// NextStage decides where a mailbox goes next, and says why in words a
// person can act on.
func NextStage(f MailboxFacts, now time.Time) Change {
	restBelow := f.RestBelow
	if restBelow == 0 {
		restBelow = 0.75
	}
	stay := func(reason string) Change {
		return Change{Stage: f.Stage, Reason: reason, Until: f.StageUntil, Same: true}
	}
	move := func(stage, reason string, until *time.Time) Change {
		return Change{Stage: stage, Reason: reason, Until: until}
	}
	enough := f.Placed >= MinPlaced
	rate := f.rate()

	switch f.Stage {
	case StageWarming:
		switch {
		case f.StageUntil != nil && now.Before(*f.StageUntil):
			return stay("Warming up until " + f.StageUntil.UTC().Format("Mon 2 Jan") + ".")
		case !f.WarmupEnabled:
			return stay("Warm-up is off. Turn it on in a connected sequencer (EmailBison, Instantly or Smartlead) to finish warming up.")
		case !enough:
			return stay("Waiting for the sequencer's first warm-up score.")
		case rate < ReadyAt:
			return stay("Inbox placement is " + pct(rate) + "; ready at " + pct(ReadyAt) + ". Warm-up continues.")
		case f.DomainCritical:
			return stay("The domain has a critical issue. Fix it on the Domains page to finish warming up.")
		}
		return move(StageReady, "Warmed up: "+pct(rate)+" of warm-up mail reached the inbox.", nil)

	case StageReady:
		if !f.AutoRest {
			return stay("")
		}
		until := now.Add(RestDays * 24 * time.Hour)
		switch {
		case f.DomainCritical:
			return move(StageResting, fmt.Sprintf("The domain has a critical issue (a blocklist or broken DNS). Resting for %d days while warm-up continues.", RestDays), &until)
		case enough && rate < restBelow:
			return move(StageResting, fmt.Sprintf("Inbox placement fell to %s. Resting for %d days while warm-up continues.", pct(rate), RestDays), &until)
		}
		return stay("")

	case StageResting:
		if f.StageUntil != nil && now.Before(*f.StageUntil) {
			return stay("")
		}
		if f.AutoRest && (f.DomainCritical || enough && rate < ReadyAt) {
			if f.RetireAfter > 0 && now.Sub(f.StageChangedAt) >= f.RetireAfter {
				return move(StageRetired, fmt.Sprintf("Rested %d days without recovering.", int(now.Sub(f.StageChangedAt).Hours()/24)), nil)
			}
			until := now.Add(ExtendDays * 24 * time.Hour)
			why := "the domain still has a critical issue"
			if !f.DomainCritical {
				why = "inbox placement is still " + pct(rate)
			}
			return Change{Stage: StageResting, Reason: fmt.Sprintf("Resting %d more days: %s.", ExtendDays, why), Until: &until, Same: true}
		}
		return move(StageReady, "Rest over.", nil)
	}
	return stay("")
}

// CanMove reports whether a person or agent may move a mailbox from one
// stage to another by hand. Retired is final: a retired mailbox comes back
// only by connecting it again.
func CanMove(from, to string) bool {
	switch to {
	case StageReady:
		return from == StageWarming || from == StageResting
	case StageResting:
		return from == StageReady || from == StageWarming
	case StageWarming:
		return from == StageReady || from == StageResting
	case StageRetired:
		return from != StageRetired
	}
	return false
}
