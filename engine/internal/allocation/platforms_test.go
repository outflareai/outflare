package allocation

import (
	"strings"
	"testing"
)

// onPlatform returns a mailbox found only in a platform account.
func onPlatform(id, integration string) Mailbox {
	m := box(id)
	m.NoLogin = true
	m.Platforms = map[string]bool{integration: true}
	return m
}

func linked(s Sequence, links ...Link) Sequence {
	s.Links = links
	return s
}

func TestPlatformCampaignGetsMailboxesFromItsAccountOnly(t *testing.T) {
	ins := Link{ID: "L1", IntegrationID: "ins", Platform: "Instantly"}
	native := box("a1")                 // Outflare can sign in, but it is not in Instantly
	inIns := onPlatform("b1", "ins")    // only in Instantly
	inOther := onPlatform("c1", "smrt") // only in Smartlead
	p := Make(Input{
		Sequences: []Sequence{linked(seq("A", "", PriorityNormal, 30), ins)},
		Mailboxes: []Mailbox{native, inIns, inOther},
	})
	expect(t, p, "assign b1 >A\n")
	if p.Changes[0].LinkID != "L1" {
		t.Fatalf("link %q", p.Changes[0].LinkID)
	}
	var reasons []string
	for _, b := range p.Status["A"].Blocking {
		reasons = append(reasons, b.Reason)
	}
	if got := strings.Join(reasons, "|"); got != whyNoRoute {
		t.Fatalf("blocking %q", got)
	}
}

func TestOutflareSequenceNeverGetsALoginlessMailbox(t *testing.T) {
	p := Make(Input{
		Sequences: []Sequence{seq("A", "", PriorityNormal, 20)},
		Mailboxes: []Mailbox{onPlatform("b1", "ins"), box("a1")},
	})
	expect(t, p, "assign a1 >A\n")
	if p.Changes[0].LinkID != "" {
		t.Fatalf("link %q", p.Changes[0].LinkID)
	}
}

func TestBusyOutsideIsNeverOffered(t *testing.T) {
	m := onPlatform("b1", "ins")
	m.Outside = "attached to a campaign Outflare does not manage"
	p := Make(Input{
		Sequences: []Sequence{linked(seq("A", "", PriorityNormal, 10), Link{ID: "L1", IntegrationID: "ins"})},
		Mailboxes: []Mailbox{m},
	})
	expect(t, p, "")
	if b := p.Status["A"].Blocking; len(b) != 1 || b[0].Reason != whyOutside {
		t.Fatalf("blocking %+v", b)
	}
}

func TestReadOnlyPlatformIsWatchedNotFilled(t *testing.T) {
	p := Make(Input{
		Sequences: []Sequence{linked(seq("A", "", PriorityNormal, 10), Link{ID: "L1", IntegrationID: "apollo", ReadOnly: true})},
		Mailboxes: []Mailbox{onPlatform("b1", "apollo"), box("a1")},
	})
	expect(t, p, "")
	if !strings.Contains(p.Status["A"].Note, "picks a mailbox for each contact") {
		t.Fatalf("note %q", p.Status["A"].Note)
	}
}

func TestLeavingAPlatformCampaignDrainsForItsLength(t *testing.T) {
	ins := Link{ID: "L1", IntegrationID: "ins"}
	m := assigned(onPlatform("b1", "ins"), "B", 1)
	m.Assignment.LinkID, m.Assignment.DrainDays = "L1", 12
	hi := linked(seq("A", "", PriorityHigh, 10), Link{ID: "L2", IntegrationID: "ins"})
	lo := linked(seq("B", "", PriorityLow, 10), ins)
	p := Make(Input{Sequences: []Sequence{hi, lo}, Mailboxes: []Mailbox{m}})
	expect(t, p, "drain b1 B>A\n")
	c := p.Changes[0]
	if c.LinkID != "L2" || !strings.Contains(c.Reason, "12-day sequence") {
		t.Fatalf("change %+v", c)
	}
}
