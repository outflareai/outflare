package allocation

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// box returns a ready mailbox with a confirmed sender on the given teams.
func box(id string, teams ...string) Mailbox {
	return Mailbox{ID: id, Email: id + "@" + id[:1] + ".example", Domain: id[:1] + ".example", DailyLimit: 10,
		SenderID: "s-" + id, SenderName: "Sender " + id, SenderConfirmed: true, SenderTeams: teams}
}

func assigned(m Mailbox, seq string, inFlight int) Mailbox {
	m.Assignment = &Assignment{SequenceID: seq, State: "active", Source: "automatic", Version: 1, InFlight: inFlight}
	return m
}

func seq(id string, team string, prio, demand int) Sequence {
	return Sequence{ID: id, Name: "Seq " + id, Status: "active", TeamID: team, Priority: prio, Demand: demand, MayTake: true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, len(id), time.UTC)}
}

// summary renders changes as "action mailbox from>to [needs]" lines.
func summary(p Plan) string {
	var b strings.Builder
	for _, c := range p.Changes {
		fmt.Fprintf(&b, "%s %s %s>%s", c.Action, c.MailboxID, c.From, c.To)
		if c.Needs != "" {
			fmt.Fprintf(&b, " [%s]", c.Needs)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func expect(t *testing.T, p Plan, want string) {
	t.Helper()
	if got := summary(p); got != want {
		t.Fatalf("plan:\n%s\nwant:\n%s", got, want)
	}
	for _, c := range p.Changes {
		if c.Reason == "" {
			t.Errorf("change %s %s has no reason", c.Action, c.MailboxID)
		}
	}
}

func TestFillsFromOwnTeamThenSharedAndProposesBorrowing(t *testing.T) {
	shared := box("c1")
	shared.SenderShared, shared.SenderTeams = true, nil
	in := Input{HasTeams: true,
		Sequences: []Sequence{seq("A", "fin", PriorityNormal, 40)},
		Mailboxes: []Mailbox{box("a1", "fin"), box("a2", "fin"), shared, box("d1", "saas")},
	}
	p := Make(in)
	expect(t, p, "assign a1 >A\nassign a2 >A\nassign c1 >A\nassign d1 >A [team_approval]\n")
	if p.Changes[3].ApproverTeam != "saas" {
		t.Fatalf("approver %q", p.Changes[3].ApproverTeam)
	}
	st := p.Status["A"]
	if st.Active != 30 || st.Incoming != 10 || st.Needed != 40 {
		t.Fatalf("status %+v", st)
	}
}

func TestPreferredSenderComesBeforeTeam(t *testing.T) {
	s := seq("A", "fin", PriorityNormal, 10)
	s.Preferred = map[string]bool{"s-a2": true}
	p := Make(Input{HasTeams: true, Sequences: []Sequence{s}, Mailboxes: []Mailbox{box("a1", "fin"), box("a2", "fin")}})
	expect(t, p, "assign a2 >A\n")
}

func TestSpreadsAcrossDomains(t *testing.T) {
	a, b, c := box("a1"), box("a2"), box("b1")
	p := Make(Input{Sequences: []Sequence{seq("A", "", PriorityNormal, 20)}, Mailboxes: []Mailbox{a, b, c}})
	expect(t, p, "assign a1 >A\nassign b1 >A\n")
}

func TestLockedMailboxStaysAndIsNeverOffered(t *testing.T) {
	pinned := assigned(box("a1", "fin"), "abm", 0)
	pinned.Lock = &Lock{Kind: LockSequence, SequenceID: "abm"}
	free := box("a2", "fin")
	free.Lock = &Lock{Kind: LockSequence, SequenceID: "abm"}
	manual := box("a3", "fin")
	manual.Lock = &Lock{Kind: LockManualOnly}
	abm := seq("abm", "fin", PriorityLow, 0) // manual: no demand
	p := Make(Input{HasTeams: true,
		Sequences: []Sequence{abm, seq("A", "fin", PriorityHigh, 30)},
		Mailboxes: []Mailbox{pinned, free, manual}})
	expect(t, p, "")
	blocking := fmt.Sprint(p.Status["A"].Blocking)
	if !strings.Contains(blocking, whyLockedOther) || !strings.Contains(blocking, whyManualOnly) {
		t.Fatalf("blocking %s", blocking)
	}
}

func TestTeamLockOnlyServesThatTeam(t *testing.T) {
	m := box("a1")
	m.SenderShared, m.SenderTeams = true, nil
	m.Lock = &Lock{Kind: LockTeam, TeamID: "fin"}
	p := Make(Input{HasTeams: true,
		Sequences: []Sequence{seq("A", "saas", PriorityHigh, 10), seq("B", "fin", PriorityLow, 10)},
		Mailboxes: []Mailbox{m}})
	expect(t, p, "assign a1 >B\n")
}

func TestPausedSequenceKeepsInFlightAndFreesTheRest(t *testing.T) {
	a := seq("A", "", PriorityNormal, 20)
	a.Status = "paused"
	p := Make(Input{
		Sequences: []Sequence{a, seq("B", "", PriorityNormal, 10)},
		Mailboxes: []Mailbox{assigned(box("a1"), "A", 3), assigned(box("a2"), "A", 0)}})
	expect(t, p, "release a2 A>\nassign a2 >B\n")
}

func TestHigherPriorityDrainsLowerDownToItsMinimum(t *testing.T) {
	low := seq("L", "", PriorityLow, 30)
	low.MinDaily = 10
	p := Make(Input{
		Sequences: []Sequence{low, seq("H", "", PriorityHigh, 30)},
		Mailboxes: []Mailbox{assigned(box("a1"), "L", 0), assigned(box("b1"), "L", 5), assigned(box("c1"), "L", 2)}})
	// Two of three may go; the one with most in flight stays.
	expect(t, p, "move a1 L>H\ndrain c1 L>H\n")
	if st := p.Status["H"]; st.Active != 10 || st.Incoming != 10 {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(p.Changes[1].Reason, "finishes 2 follow-ups") {
		t.Fatalf("reason %q", p.Changes[1].Reason)
	}
}

func TestEqualPriorityNeverTakes(t *testing.T) {
	p := Make(Input{
		Sequences: []Sequence{seq("A", "", PriorityNormal, 10), seq("B", "", PriorityNormal, 10)},
		Mailboxes: []Mailbox{assigned(box("a1"), "A", 0)}})
	expect(t, p, "")
	if st := p.Status["B"]; !strings.Contains(fmt.Sprint(st.Blocking), whyBusy) || !strings.Contains(st.Note, "Short by 10") {
		t.Fatalf("status %+v", st)
	}
}

func TestAgentWithoutTakePermissionOnlyProposes(t *testing.T) {
	h := seq("H", "", PriorityHigh, 10)
	h.MayTake = false
	p := Make(Input{
		Sequences: []Sequence{seq("L", "", PriorityLow, 10), h},
		Mailboxes: []Mailbox{assigned(box("a1"), "L", 0)}})
	expect(t, p, "move a1 L>H [owner_approval]\n")
	if p.Status["L"].Active != 10 {
		t.Fatalf("L lost capacity before approval: %+v", p.Status["L"])
	}
}

func TestDrainingMailboxFinishesThenJoinsNext(t *testing.T) {
	m := assigned(box("a1"), "A", 0)
	m.Assignment.State, m.Assignment.NextSequenceID = "draining", "B"
	still := assigned(box("b1"), "A", 4)
	still.Assignment.State = "draining"
	p := Make(Input{
		Sequences: []Sequence{seq("A", "", PriorityLow, 0), seq("B", "", PriorityHigh, 10)},
		Mailboxes: []Mailbox{m, still}})
	expect(t, p, "finish_drain a1 A>B\n")
	if p.Status["B"].Active != 10 {
		t.Fatalf("status %+v", p.Status["B"])
	}
}

func TestSurplusReleasesIdleOtherTeamFirst(t *testing.T) {
	shared := assigned(box("c1"), "A", 0)
	shared.SenderShared, shared.SenderTeams = true, nil
	p := Make(Input{HasTeams: true,
		Sequences: []Sequence{seq("A", "fin", PriorityNormal, 20)},
		Mailboxes: []Mailbox{assigned(box("a1", "fin"), "A", 0), assigned(box("a2", "fin"), "A", 7), shared}})
	expect(t, p, "release c1 A>\n")
}

func TestUnconfirmedSendersOnlyWithoutTeams(t *testing.T) {
	m := box("a1")
	m.SenderID, m.SenderConfirmed = "", false
	if p := Make(Input{Sequences: []Sequence{seq("A", "", PriorityNormal, 10)}, Mailboxes: []Mailbox{m}}); summary(p) != "assign a1 >A\n" {
		t.Fatalf("no teams: %s", summary(p))
	}
	p := Make(Input{HasTeams: true, Sequences: []Sequence{seq("A", "fin", PriorityNormal, 10)}, Mailboxes: []Mailbox{m}})
	expect(t, p, "")
	if !strings.Contains(fmt.Sprint(p.Status["A"].Blocking), whyUnconfirmed) {
		t.Fatalf("blocking %+v", p.Status["A"].Blocking)
	}
}

func TestNotReadyMailboxIsReplacedButKeepsItsLeads(t *testing.T) {
	sick := assigned(box("a1"), "A", 6)
	sick.NotReady = "domain paused"
	idle := assigned(box("b1"), "A", 0)
	idle.NotReady = "warming up"
	p := Make(Input{Sequences: []Sequence{seq("A", "", PriorityNormal, 10)}, Mailboxes: []Mailbox{sick, idle, box("c1")}})
	expect(t, p, "release b1 A>\nassign c1 >A\n")
}

func TestRetiredSenderDrains(t *testing.T) {
	m := assigned(box("a1"), "A", 2)
	m.SenderRetired = true
	n := assigned(box("b1"), "A", 0)
	n.SenderRetired = true
	p := Make(Input{Sequences: []Sequence{seq("A", "", PriorityNormal, 0)}, Mailboxes: []Mailbox{m, n}})
	expect(t, p, "drain a1 A>\nrelease b1 A>\n")
}

func TestArchivedSequenceReleasesEverything(t *testing.T) {
	a := seq("A", "", PriorityNormal, 10)
	a.Status = "archived"
	p := Make(Input{Sequences: []Sequence{a}, Mailboxes: []Mailbox{assigned(box("a1"), "A", 9)}})
	expect(t, p, "release a1 A>\n")
}

func TestPoolRestrictsCandidates(t *testing.T) {
	a, b := box("a1"), box("b1")
	b.PoolID = "p"
	s := seq("A", "", PriorityNormal, 20)
	s.PoolID = "p"
	p := Make(Input{Sequences: []Sequence{s}, Mailboxes: []Mailbox{a, b}})
	expect(t, p, "assign b1 >A\n")
}

func TestManualSequenceWithoutDemandIsLeftAlone(t *testing.T) {
	p := Make(Input{Sequences: []Sequence{seq("A", "", PriorityNormal, 0)}, Mailboxes: []Mailbox{box("a1"), assigned(box("b1"), "A", 0)}})
	expect(t, p, "")
	if !strings.Contains(p.Status["A"].Note, "daily") {
		t.Fatalf("note %q", p.Status["A"].Note)
	}
}

func TestPlanIsStable(t *testing.T) {
	in := Input{Sequences: []Sequence{seq("A", "", PriorityNormal, 10)}, Mailboxes: []Mailbox{assigned(box("a1"), "A", 0), box("b1")}}
	if got := summary(Make(in)); got != "" {
		t.Fatalf("a covered sequence should not change: %s", got)
	}
}

func inProject(m Mailbox, project string) Mailbox {
	m.ProjectID = project
	return m
}

func seqIn(s Sequence, project string) Sequence {
	s.ProjectID = project
	return s
}

func TestProjectMailboxesServeOnlyTheirProject(t *testing.T) {
	in := Input{
		Sequences: []Sequence{seqIn(seq("A", "", PriorityNormal, 20), "p1"), seq("B", "", PriorityNormal, 20)},
		Mailboxes: []Mailbox{inProject(box("a1"), "p1"), inProject(box("b1"), "p2"), box("c1")},
	}
	p := Make(in)
	// A takes its project's mailbox, then the shared one. B, outside any
	// project, can use neither p1's nor p2's mailbox.
	expect(t, p, "assign a1 >A\nassign c1 >A\n")
	var blocked int
	for _, b := range p.Status["B"].Blocking {
		if b.Reason == whyOtherProject {
			blocked = b.Count
		}
	}
	if blocked != 2 {
		t.Fatalf("B blocking %+v", p.Status["B"].Blocking)
	}
}

func TestProjectMailboxesBeforeSharedInTheSameTier(t *testing.T) {
	in := Input{
		Sequences: []Sequence{seqIn(seq("A", "", PriorityNormal, 10), "p1")},
		// The shared mailbox sorts first by email; the project's still wins.
		Mailboxes: []Mailbox{box("a1"), inProject(box("b1"), "p1")},
	}
	expect(t, Make(in), "assign b1 >A\n")
}

func TestMailboxMovedToAnotherProjectLeavesItsSequence(t *testing.T) {
	in := Input{
		Sequences: []Sequence{seq("A", "", PriorityNormal, 20)},
		Mailboxes: []Mailbox{
			inProject(assigned(box("a1"), "A", 0), "p1"),
			inProject(assigned(box("a2"), "A", 3), "p1"),
		},
	}
	expect(t, Make(in), "release a1 A>\ndrain a2 A>\n")
}

func TestHigherPriorityNeverDrainsAnotherProjectsMailbox(t *testing.T) {
	in := Input{
		Sequences: []Sequence{
			seqIn(seq("H", "", PriorityHigh, 10), "p1"),
			seqIn(seq("L", "", PriorityLow, 10), "p2"),
		},
		Mailboxes: []Mailbox{inProject(assigned(box("a1"), "L", 0), "p2")},
	}
	expect(t, Make(in), "")
}
