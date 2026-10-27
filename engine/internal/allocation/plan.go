// Package allocation decides which mailboxes send for which sequence.
//
// Plan is a pure function: given every sequence and mailbox in a
// workspace, it returns the fewest changes that give each sequence the
// capacity it needs, with a plain-language reason for each, and for each
// sequence what it has and why it is short. The Service loads that input
// from Postgres and applies the changes under the hard rules, which the
// schema also enforces.
//
// Hard rules, never broken to cover a shortage:
//   - A mailbox serves one sequence at a time (one assignment row).
//   - Follow-ups come from the mailbox that sent the first email, so a
//     mailbox with leads in flight drains before it moves.
//   - Locks are respected; manual_only mailboxes are never touched.
//   - Only ready mailboxes are allocated.
//   - A mailbox in a project serves only that project's sequences.
//
// Soft preferences decide the order: the sequence's preferred senders,
// then its team's senders, then shared or unassigned senders; within each,
// the sequence's own project's mailboxes before workspace-shared ones. Another
// team's mailboxes are only proposed, for that team to approve.
package allocation

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// Actions a change can take.
const (
	Assign      = "assign"       // free mailbox joins To
	Release     = "release"      // mailbox leaves From and becomes free
	Move        = "move"         // mailbox with nothing in flight goes straight from From to To
	Drain       = "drain"        // mailbox stops taking new leads in From, then joins To (or becomes free)
	FinishDrain = "finish_drain" // a draining mailbox has no follow-ups left
)

// What a change needs before it may happen, beyond the sequence's mode.
const (
	NeedsTeamApproval  = "team_approval"  // the mailbox's sender belongs to another team
	NeedsOwnerApproval = "owner_approval" // an agent token may not take mailboxes from other sequences
)

// Lock kinds.
const (
	LockSequence   = "sequence"
	LockTeam       = "team"
	LockManualOnly = "manual_only"
)

// Priorities.
const (
	PriorityLow    = 0
	PriorityNormal = 1
	PriorityHigh   = 2
)

type Lock struct {
	Kind       string
	SequenceID string
	TeamID     string
}

type Assignment struct {
	SequenceID string
	// LinkID is the platform campaign it sends in, "" for Outflare itself.
	LinkID         string
	State          string // active or draining
	NextSequenceID string
	Source         string // manual or automatic
	Version        int
	// InFlight is how many of SequenceID's leads are waiting on a
	// follow-up from this mailbox. Platforms do not report this per
	// mailbox, so in a platform campaign it is 1 until DrainDays have
	// passed since the mailbox started draining.
	InFlight int
	// DrainDays is how long a mailbox leaving the platform campaign stays
	// in it so its leads get their follow-ups: the campaign's length.
	DrainDays int
}

type Mailbox struct {
	ID     string
	Email  string
	Domain string
	PoolID string
	// ProjectID is the project that owns the mailbox, or "" when shared.
	ProjectID string
	// BrandID is the brand that owns the mailbox, or "" when shared.
	BrandID    string
	DailyLimit int
	// NotReady is why the mailbox cannot be given new work, or "".
	NotReady string

	SenderID        string
	SenderName      string
	SenderConfirmed bool
	SenderShared    bool
	SenderRetired   bool
	SenderTeams     []string

	Lock       *Lock
	Assignment *Assignment

	// NoLogin: Outflare cannot sign in to it; it was found in a connected
	// platform account and can only send there.
	NoLogin bool
	// Platforms holds the platform accounts (integration ids) it is
	// connected to and able to send from.
	Platforms map[string]bool
	// Outside is why it is busy outside Outflare's control, or "": attached
	// to a platform campaign it is not assigned to, seen by a platform
	// account that has not synced recently, or in a platform that picks
	// mailboxes itself.
	Outside string
}

// Link is a platform campaign a sequence runs as.
type Link struct {
	ID            string
	IntegrationID string
	Platform      string // display name, e.g. Instantly
	// ReadOnly: the platform picks mailboxes itself (Apollo), so Outflare
	// cannot assign any.
	ReadOnly bool
}

type Sequence struct {
	ID       string
	Name     string
	Status   string // draft, active, paused, archived
	TeamID   string
	Priority int
	// Demand is the sends a day it should have capacity for; 0 means it is
	// not allocated automatically.
	Demand   int
	MinDaily int
	PoolID   string
	// ProjectID is the sequence's project, or "".
	ProjectID string
	// BrandID is the sequence's brand, or "".
	BrandID string
	// Preferred holds the sender ids picked before the rest of the team.
	Preferred map[string]bool
	// MayTake is false for sequences an agent token created without
	// permission to take mailboxes from other sequences.
	MayTake   bool
	CreatedAt time.Time
	// Links are the platform campaigns it runs as. None: it sends through
	// Outflare.
	Links []Link
}

type Input struct {
	Sequences []Sequence
	Mailboxes []Mailbox
	// HasTeams is whether the workspace has any teams. Without them every
	// mailbox counts as the sequence's own, confirmed sender or not.
	HasTeams bool
	// Declined holds mailbox+"/"+sequence pairs a person turned down.
	Declined map[string]bool
}

type Change struct {
	MailboxID string
	Action    string
	From      string // sequence the mailbox leaves, or ""
	To        string // sequence it joins (now, or after draining), or ""
	Reason    string
	// Needs is "" when the sequence's mode decides, else an approval.
	Needs        string
	ApproverTeam string
	BaseVersion  int // assignment version the change was planned against; 0 when free
	// LinkID is the platform campaign the mailbox joins in To, "" for Open
	// GTM itself.
	LinkID string
}

// Blocking counts mailboxes a short sequence could not use, by reason.
type Blocking struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type Status struct {
	SequenceID string
	Needed     int // 0: not allocated automatically
	Active     int // capacity of its active, ready mailboxes after this plan
	Incoming   int // capacity on its way: draining elsewhere, or waiting for approval
	Blocking   []Blocking
	Note       string
}

type Plan struct {
	Changes []Change
	Status  map[string]*Status
}

// Reasons a mailbox was not used, as they appear in shortage reports.
const (
	whyNotReady      = "not ready"
	whyBusy          = "in use by an equal or higher priority sequence"
	whyLockedOther   = "locked to another sequence or team"
	whyManualOnly    = "manual only"
	whyUnconfirmed   = "sender not confirmed"
	whyRetired       = "sender retired"
	whyOutsidePool   = "outside the sequence's mailbox pool"
	whyDraining      = "finishing follow-ups for another sequence"
	whyAwaitApproval = "another team's, waiting for approval"
	whyProtected     = "would take another sequence below its minimum"
	whyDeclined      = "declined for this sequence"
	whyOutside       = "busy on an outside platform, or its platform has not synced"
	whyNoRoute       = "not connected where this campaign sends"
	whyReadOnly      = "the platform picks mailboxes itself"
	whyOtherProject  = "belongs to another project"
	whyOtherBrand    = "belongs to another brand"
)

type planner struct {
	in    Input
	seqs  map[string]*Sequence
	boxes []*Mailbox
	now   map[string]string // mailbox -> sequence it actively sends for after this plan, "" free
	busy  map[string]bool   // mailbox already decided this run (changed, proposed or reserved)
	free  map[string]bool   // mailbox has no sequence and nothing is proposed for it
	plan  Plan
}

// Make returns the plan for in. It never returns a change that breaks a
// hard rule; when a sequence cannot be covered it says why instead.
func Make(in Input) Plan {
	p := &planner{in: in, seqs: map[string]*Sequence{}, now: map[string]string{}, busy: map[string]bool{}, free: map[string]bool{},
		plan: Plan{Status: map[string]*Status{}}}
	for i := range in.Sequences {
		s := &in.Sequences[i]
		p.seqs[s.ID] = s
		p.plan.Status[s.ID] = &Status{SequenceID: s.ID}
	}
	for i := range in.Mailboxes {
		m := &in.Mailboxes[i]
		p.boxes = append(p.boxes, m)
		if a := m.Assignment; a == nil {
			p.free[m.ID] = true
		} else if a.State == "active" {
			p.now[m.ID] = a.SequenceID
		}
	}
	// Deterministic order everywhere: plans are compared and explained.
	slices.SortFunc(p.boxes, func(a, b *Mailbox) int { return cmp.Compare(a.Email, b.Email) })

	p.housekeeping()
	p.trimSurplus()
	p.fill()
	p.report()
	return p.plan
}

func (p *planner) add(c Change) {
	p.plan.Changes = append(p.plan.Changes, c)
	p.busy[c.MailboxID] = true
	p.free[c.MailboxID] = c.Needs == "" && c.To == "" && (c.Action == Release || c.Action == FinishDrain)
}

func (p *planner) seq(id string) *Sequence { return p.seqs[id] }

// housekeeping settles mailboxes whose assignment no longer fits: finished
// drains, paused or archived sequences, mailboxes that stopped being ready,
// retired senders and locks set after the assignment.
func (p *planner) housekeeping() {
	for _, m := range p.boxes {
		a := m.Assignment
		if a == nil {
			continue
		}
		s := p.seq(a.SequenceID)
		inFlight := a.InFlight
		if s == nil || s.Status == "archived" {
			inFlight = 0 // an archived sequence sends nothing more
		}

		if a.State == "draining" {
			if inFlight > 0 {
				continue
			}
			to := ""
			reason := "Finished its follow-ups for " + p.name(a.SequenceID) + "."
			var link string
			if n := p.seq(a.NextSequenceID); n != nil && n.Status != "archived" && p.lockAllows(m, n) && p.routable(m, n) && projectAllows(m, n) {
				to = n.ID
				link, _ = p.route(m, n)
				p.now[m.ID] = to
				reason += " Now sending for " + n.Name + "."
			} else {
				reason += " It is free for other sequences."
			}
			p.add(Change{MailboxID: m.ID, Action: FinishDrain, From: a.SequenceID, To: to, Reason: reason, BaseVersion: a.Version, LinkID: link})
			continue
		}
		if m.Lock != nil && m.Lock.Kind == LockManualOnly {
			continue // never touched by the allocator
		}

		var why string
		var next string
		switch {
		case s == nil || s.Status == "archived":
			why = "Its sequence was archived."
		case m.Lock != nil && !p.lockAllows(m, s):
			why = "It is locked elsewhere."
			if m.Lock.Kind == LockSequence {
				next = m.Lock.SequenceID
			}
		case !projectAllows(m, s):
			why = "It now belongs to another project."
		case !brandAllows(m, s):
			why = "It now belongs to another brand."
		case m.SenderRetired:
			why = "Its sender is retired."
		case s.Status == "paused":
			if inFlight > 0 || (m.Lock != nil && m.Lock.Kind == LockSequence) {
				continue // follow-ups wait for the sequence; a pin keeps it
			}
			why = "Its sequence is paused and it has no follow-ups waiting."
		case m.NotReady != "" && a.Source == "automatic":
			if inFlight > 0 {
				continue // keeps its leads; the sequence is topped up around it
			}
			why = "It is not ready: " + m.NotReady + "."
		default:
			continue
		}
		if inFlight > 0 {
			reason := why + drainText(a, inFlight, "", "")
			p.add(Change{MailboxID: m.ID, Action: Drain, From: a.SequenceID, To: next, Reason: reason, BaseVersion: a.Version})
		} else if next != "" {
			link, ok := "", false
			if n := p.seq(next); n != nil {
				link, ok = p.route(m, n)
			}
			if !ok {
				p.add(Change{MailboxID: m.ID, Action: Release, From: a.SequenceID, Reason: why, BaseVersion: a.Version})
				delete(p.now, m.ID)
				continue
			}
			p.add(Change{MailboxID: m.ID, Action: Move, From: a.SequenceID, To: next, Reason: why + " Moved to " + p.name(next) + ".", BaseVersion: a.Version, LinkID: link})
			p.now[m.ID] = next
			continue
		} else {
			p.add(Change{MailboxID: m.ID, Action: Release, From: a.SequenceID, Reason: why, BaseVersion: a.Version})
		}
		delete(p.now, m.ID)
	}
}

// capacity is the daily sends of a sequence's active, ready mailboxes.
func (p *planner) capacity(seqID string) int {
	n := 0
	for _, m := range p.boxes {
		if p.now[m.ID] == seqID && m.NotReady == "" && !m.SenderRetired {
			n += m.DailyLimit
		}
	}
	return n
}

// trimSurplus releases capacity a sequence no longer needs, starting with
// mailboxes that have nothing in flight and senders from outside its team.
func (p *planner) trimSurplus() {
	for _, s := range p.sorted() {
		if s.Status != "active" || s.Demand == 0 {
			continue
		}
		have := p.capacity(s.ID)
		if have <= s.Demand {
			continue
		}
		var mine []*Mailbox
		for _, m := range p.boxes {
			if p.now[m.ID] == s.ID && !p.busy[m.ID] && m.Lock == nil {
				mine = append(mine, m)
			}
		}
		slices.SortStableFunc(mine, func(a, b *Mailbox) int {
			if c := cmp.Compare(a.Assignment.InFlight, b.Assignment.InFlight); c != 0 {
				return c
			}
			if c := -cmp.Compare(p.tier(a, s), p.tier(b, s)); c != 0 {
				return c // other teams' first
			}
			return -cmp.Compare(shared(a, s), shared(b, s)) // then shared ones, keeping the project's own
		})
		for _, m := range mine {
			if have-m.DailyLimit < s.Demand {
				continue
			}
			have -= m.DailyLimit
			delete(p.now, m.ID)
			a := m.Assignment
			if a.InFlight > 0 {
				p.add(Change{MailboxID: m.ID, Action: Drain, From: s.ID, BaseVersion: a.Version,
					Reason: s.Name + " needs less capacity." + drainText(a, a.InFlight, "", "")})
			} else {
				p.add(Change{MailboxID: m.ID, Action: Release, From: s.ID, BaseVersion: a.Version,
					Reason: s.Name + " needs less capacity than it has."})
			}
		}
	}
}

// Tiers of fit for a sequence: lower is better.
const (
	tierPreferred = 0
	tierTeam      = 1
	tierShared    = 2
	tierOther     = 3 // another team's: needs that team's approval
	tierNever     = 9
)

// tier is how well m's sender fits s, and tierNever when it may not be
// used for s at all.
func (p *planner) tier(m *Mailbox, s *Sequence) int {
	if !p.in.HasTeams {
		if m.SenderID != "" && s.Preferred[m.SenderID] && m.SenderConfirmed {
			return tierPreferred
		}
		return tierTeam
	}
	if m.SenderID == "" || !m.SenderConfirmed {
		return tierNever
	}
	if s.Preferred[m.SenderID] {
		return tierPreferred
	}
	if m.SenderShared || len(m.SenderTeams) == 0 {
		return tierShared
	}
	if s.TeamID != "" && slices.Contains(m.SenderTeams, s.TeamID) {
		return tierTeam
	}
	return tierOther
}

// eligible reports whether m may be given to s at all, and if not why.
func (p *planner) eligible(m *Mailbox, s *Sequence) (bool, string) {
	switch {
	case m.Lock != nil && m.Lock.Kind == LockManualOnly:
		return false, whyManualOnly
	case !projectAllows(m, s):
		return false, whyOtherProject
	case !brandAllows(m, s):
		return false, whyOtherBrand
	case m.Lock != nil && !p.lockAllows(m, s):
		return false, whyLockedOther
	case m.SenderRetired:
		return false, whyRetired
	case m.NotReady != "":
		return false, whyNotReady + ": " + m.NotReady
	case m.Outside != "":
		return false, whyOutside
	case p.readOnly(s):
		return false, whyReadOnly
	case !p.routable(m, s):
		return false, whyNoRoute
	case s.PoolID != "" && m.PoolID != s.PoolID:
		return false, whyOutsidePool
	case p.tier(m, s) == tierNever:
		return false, whyUnconfirmed
	case p.in.Declined[m.ID+"/"+s.ID]:
		return false, whyDeclined
	}
	return true, ""
}

// projectAllows reports whether m may serve s: a project's mailbox serves
// only that project's sequences; a shared one serves any.
func projectAllows(m *Mailbox, s *Sequence) bool {
	return m.ProjectID == "" || m.ProjectID == s.ProjectID
}

// brandAllows reports whether m may serve s: a brand's mailbox serves only
// that brand's sequences; a shared one serves any.
func brandAllows(m *Mailbox, s *Sequence) bool {
	return m.BrandID == "" || m.BrandID == s.BrandID
}

// shared is 1 for a workspace-shared mailbox serving a project's sequence,
// so the project's own mailboxes sort first.
func shared(m *Mailbox, s *Sequence) int {
	if s.ProjectID != "" && m.ProjectID == "" {
		return 1
	}
	return 0
}

// route is the link m would send in for s, "" for Outflare itself, and
// whether there is one: a sequence without links needs a mailbox Outflare
// can sign in to, one with links a mailbox connected to one of their
// platform accounts.
func (p *planner) route(m *Mailbox, s *Sequence) (string, bool) {
	if len(s.Links) == 0 {
		return "", !m.NoLogin
	}
	for _, l := range s.Links {
		if !l.ReadOnly && m.Platforms[l.IntegrationID] {
			return l.ID, true
		}
	}
	return "", false
}

func (p *planner) routable(m *Mailbox, s *Sequence) bool {
	_, ok := p.route(m, s)
	return ok
}

// readOnly reports a sequence whose platforms all pick mailboxes themselves.
func (p *planner) readOnly(s *Sequence) bool {
	if len(s.Links) == 0 {
		return false
	}
	for _, l := range s.Links {
		if !l.ReadOnly {
			return false
		}
	}
	return true
}

func (p *planner) lockAllows(m *Mailbox, s *Sequence) bool {
	if m.Lock == nil {
		return true
	}
	switch m.Lock.Kind {
	case LockSequence:
		return m.Lock.SequenceID == s.ID
	case LockTeam:
		return m.Lock.TeamID == s.TeamID
	}
	return true // manual_only allows anything a person does
}

// sorted returns sequences by priority, highest first, then oldest first.
func (p *planner) sorted() []*Sequence {
	list := make([]*Sequence, 0, len(p.seqs))
	for i := range p.in.Sequences {
		list = append(list, &p.in.Sequences[i])
	}
	slices.SortStableFunc(list, func(a, b *Sequence) int {
		if c := cmp.Compare(b.Priority, a.Priority); c != 0 {
			return c
		}
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return list
}

// fill gives each short sequence, highest priority first, free mailboxes,
// then proposes other teams' mailboxes, then drains lower priority ones.
func (p *planner) fill() {
	for _, s := range p.sorted() {
		if s.Status != "active" || s.Demand == 0 {
			continue
		}
		need := s.Demand - p.capacity(s.ID)
		st := p.plan.Status[s.ID]
		if need <= 0 {
			continue
		}

		// Free mailboxes, best fit first.
		var free []*Mailbox
		for _, m := range p.boxes {
			if !p.free[m.ID] {
				continue
			}
			if ok, _ := p.eligible(m, s); ok {
				free = append(free, m)
			}
		}
		for need > 0 && len(free) > 0 {
			i := p.best(free, s)
			m := free[i]
			free = slices.Delete(free, i, i+1)
			t := p.tier(m, s)
			base := 0
			if m.Assignment != nil {
				base = m.Assignment.Version
			}
			link, _ := p.route(m, s)
			if t == tierOther {
				team := firstSorted(m.SenderTeams)
				p.add(Change{MailboxID: m.ID, Action: Assign, To: s.ID, Needs: NeedsTeamApproval, ApproverTeam: team, BaseVersion: base, LinkID: link,
					Reason: fmt.Sprintf("%s is short and this mailbox is idle, but its sender %s belongs to another team, which has to agree.", s.Name, m.SenderName)})
				st.Incoming += m.DailyLimit
			} else {
				p.add(Change{MailboxID: m.ID, Action: Assign, To: s.ID, BaseVersion: base, LinkID: link, Reason: p.why(m, s, t, "Free and ready")})
				p.now[m.ID] = s.ID
			}
			need -= m.DailyLimit
		}
		if need <= 0 {
			continue
		}

		// Lower priority sequences give up mailboxes down to their minimum.
		donors := []*Sequence{}
		for _, d := range p.sorted() {
			if d.Priority < s.Priority && d.Status == "active" {
				donors = append(donors, d)
			}
		}
		slices.Reverse(donors) // lowest priority first
		for _, d := range donors {
			if need <= 0 {
				break
			}
			left := p.capacity(d.ID)
			var cands []*Mailbox
			for _, m := range p.boxes {
				if p.now[m.ID] != d.ID || p.busy[m.ID] || (m.Lock != nil && m.Lock.Kind == LockSequence) {
					continue
				}
				if ok, _ := p.eligible(m, s); ok {
					cands = append(cands, m)
				}
			}
			slices.SortStableFunc(cands, func(a, b *Mailbox) int {
				if c := cmp.Compare(a.Assignment.InFlight, b.Assignment.InFlight); c != 0 {
					return c
				}
				if c := cmp.Compare(p.tier(a, s), p.tier(b, s)); c != 0 {
					return c
				}
				if c := cmp.Compare(shared(a, s), shared(b, s)); c != 0 {
					return c
				}
				return cmp.Compare(b.DailyLimit, a.DailyLimit)
			})
			for _, m := range cands {
				if need <= 0 {
					break
				}
				if left-m.DailyLimit < d.MinDaily {
					continue
				}
				left -= m.DailyLimit
				need -= m.DailyLimit
				a := m.Assignment
				link, _ := p.route(m, s)
				c := Change{MailboxID: m.ID, From: d.ID, To: s.ID, BaseVersion: a.Version, LinkID: link}
				switch t := p.tier(m, s); {
				case t == tierOther:
					c.Needs, c.ApproverTeam = NeedsTeamApproval, firstSorted(m.SenderTeams)
				case !s.MayTake:
					c.Needs = NeedsOwnerApproval
				}
				prio := fmt.Sprintf("%s has higher priority than %s and is short.", s.Name, d.Name)
				if a.InFlight > 0 {
					c.Action = Drain
					c.Reason = prio + drainText(a, a.InFlight, d.Name, s.Name)
				} else {
					c.Action = Move
					c.Reason = prio + " It has no follow-ups waiting, so it moves now."
				}
				p.add(c)
				if c.Action == Move && c.Needs == "" {
					p.now[m.ID] = s.ID
				} else {
					st.Incoming += m.DailyLimit
					if c.Needs == "" {
						delete(p.now, m.ID) // draining: no longer takes new leads for d
					}
				}
			}
		}
	}
}

// best is the index of the best mailbox for s: preferred and own-team
// senders first, then the domain s uses least, then the most capacity.
func (p *planner) best(list []*Mailbox, s *Sequence) int {
	perDomain := map[string]int{}
	for _, m := range p.boxes {
		if p.now[m.ID] == s.ID {
			perDomain[m.Domain]++
		}
	}
	best := 0
	for i := 1; i < len(list); i++ {
		a, b := list[i], list[best]
		if c := cmp.Compare(p.tier(a, s), p.tier(b, s)); c != 0 {
			if c < 0 {
				best = i
			}
			continue
		}
		if c := cmp.Compare(shared(a, s), shared(b, s)); c != 0 {
			if c < 0 {
				best = i
			}
			continue
		}
		if c := cmp.Compare(perDomain[a.Domain], perDomain[b.Domain]); c != 0 {
			if c < 0 {
				best = i
			}
			continue
		}
		if a.DailyLimit > b.DailyLimit {
			best = i
		}
	}
	return best
}

func (p *planner) why(m *Mailbox, s *Sequence, tier int, start string) string {
	who := ""
	switch tier {
	case tierPreferred:
		who = fmt.Sprintf(", and %s is a preferred sender for %s", m.SenderName, s.Name)
	case tierTeam:
		if p.in.HasTeams {
			who = fmt.Sprintf(", and its sender %s is on %s's team", m.SenderName, s.Name)
		}
	case tierShared:
		who = ", from a shared sender, since the team had no free mailboxes left"
	}
	return fmt.Sprintf("%s%s. Adds %d sends a day.", start, who, m.DailyLimit)
}

// report fills each sequence's status and, when it is short, why other
// mailboxes could not be used.
func (p *planner) report() {
	for _, s := range p.sorted() {
		st := p.plan.Status[s.ID]
		st.Needed = s.Demand
		st.Active = p.capacity(s.ID)
		switch {
		case s.Status != "active":
			st.Note = "Mailboxes are allocated while the sequence is active."
			continue
		case p.readOnly(s):
			st.Note = "Its platform picks a mailbox for each contact, so Outflare watches its mailboxes instead of assigning them."
			continue
		case s.Demand == 0:
			st.Note = "Set a daily new-lead limit or a daily target to allocate mailboxes automatically."
			continue
		case st.Active >= s.Demand:
			continue
		}
		counts := map[string]int{}
		for _, m := range p.boxes {
			if p.now[m.ID] == s.ID {
				continue
			}
			why := ""
			if ok, reason := p.eligible(m, s); !ok {
				why = reason
			} else if m.Assignment != nil && m.Assignment.State == "draining" {
				why = whyDraining
			} else if p.pending(m.ID, s.ID) {
				why = whyAwaitApproval
			} else if other := p.now[m.ID]; other != "" {
				if o := p.seq(other); o != nil && o.Priority < s.Priority {
					why = whyProtected
				} else {
					why = whyBusy
				}
			} else if p.busy[m.ID] {
				why = whyDraining
			}
			if why != "" {
				counts[why]++
			}
		}
		for reason, n := range counts {
			st.Blocking = append(st.Blocking, Blocking{Reason: reason, Count: n})
		}
		slices.SortFunc(st.Blocking, func(a, b Blocking) int {
			if c := cmp.Compare(b.Count, a.Count); c != 0 {
				return c
			}
			return cmp.Compare(a.Reason, b.Reason)
		})
		short := s.Demand - st.Active - st.Incoming
		if short > 0 {
			st.Note = fmt.Sprintf("Short by %d sends a day: no other suitable mailbox is free.", short)
		} else {
			st.Note = "Short for now; more capacity is on its way."
		}
	}
}

func (p *planner) pending(mailboxID, seqID string) bool {
	for _, c := range p.plan.Changes {
		if c.MailboxID == mailboxID && c.To == seqID && c.Needs != "" {
			return true
		}
	}
	return false
}

func (p *planner) name(id string) string {
	if s := p.seq(id); s != nil {
		return s.Name
	}
	return "a deleted sequence"
}

func firstSorted(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return slices.Min(list)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// drainText explains a drain: from and to name the sequences when known.
func drainText(a *Assignment, inFlight int, from, to string) string {
	then := " first."
	if to != "" {
		then = ", then joins " + to + "."
	}
	if a.LinkID != "" {
		return fmt.Sprintf(" Its platform cannot stop one mailbox taking new leads, so it stays in the campaign for its %d-day sequence, letting its leads get their follow-ups from it%s", a.DrainDays, then)
	}
	in := ""
	if from != "" {
		in = " in " + from
	}
	if to != "" {
		return fmt.Sprintf(" It takes no new leads%s, finishes %d follow-up%s%s", in, inFlight, plural(inFlight), then)
	}
	return fmt.Sprintf(" It takes no new leads%s and finishes %d follow-up%s%s", in, inFlight, plural(inFlight), then)
}
