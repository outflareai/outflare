package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/rules"
)

// The automation map: the whole workspace's automations as one read-only
// graph. Events, field changes and smart lists set off rules and journeys;
// rules emit events; journeys touch lists, sequences, tags and other
// journeys. Each node carries its volume for the last 7 days, and the map
// flags what needs a look: loops, two journeys emailing the same people
// within a day, events nothing listens to and journeys whose trigger never
// fires. It changes nothing.

const (
	mapWindowDays = 7
	// A journey whose trigger event hasn't happened in this many days
	// never fires.
	mapQuietDays = 30
)

type mapNode struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // event, field, rule, journey, smart_list, sequence or tag
	Label string `json:"label"`
	// The rule, journey, list or sequence it stands for.
	RefID   string  `json:"ref_id,omitempty"`
	Status  string  `json:"status,omitempty"`
	BrandID *string `json:"brand_id,omitempty"`
	// How often it happened in the last 7 days: events, rule firings,
	// people entering a journey. Null where it doesn't apply.
	Volume *int `json:"volume"`
	// A line about it, such as a list's size.
	Detail string `json:"detail,omitempty"`
	// With ?record=, what the person has to do with it: did (an event),
	// member (a list), fired (a rule fired for them), in (a journey they
	// are in now) or entered (one they went through).
	Reached string `json:"reached,omitempty"`
}

type mapEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type mapFlag struct {
	Kind     string   `json:"kind"` // loop, overlap, unheard or never_fires
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Nodes    []string `json:"nodes"`
}

type automationMap struct {
	Nodes      []*mapNode `json:"nodes"`
	Edges      []mapEdge  `json:"edges"`
	Flags      []mapFlag  `json:"flags"`
	WindowDays int        `json:"window_days"`
	// The person ?record= named, if any.
	RecordID *string `json:"record_id"`
}

// driving edges are the ones that set the next thing off, which is what
// makes a loop.
var drivingEdges = []string{"starts", "emits", "enters", "fills"}

// mapBuilder collects nodes and edges once each.
type mapBuilder struct {
	m     *automationMap
	nodes map[string]*mapNode
	edges map[mapEdge]bool
}

func (b *mapBuilder) node(n *mapNode) *mapNode {
	if got, ok := b.nodes[n.ID]; ok {
		return got
	}
	b.nodes[n.ID] = n
	b.m.Nodes = append(b.m.Nodes, n)
	return n
}

func (b *mapBuilder) event(name string) string {
	return b.node(&mapNode{ID: "event:" + name, Kind: "event", Label: name}).ID
}

func (b *mapBuilder) edge(from, to, kind string) {
	e := mapEdge{From: from, To: to, Kind: kind}
	if from == "" || to == "" || b.edges[e] {
		return
	}
	b.edges[e] = true
	b.m.Edges = append(b.m.Edges, e)
}

func (s *Server) getAutomationMap(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !a.has(auth.ScopeCustomersRead) {
		writeError(w, r, http.StatusForbidden, "forbidden", "the automation map shows rules and smart lists too, which needs the customers.read scope")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	rec := r.URL.Query().Get("record")
	if rec != "" {
		var person bool
		if validUUID(rec) {
			if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM records WHERE workspace_id = $1 AND id = $2 AND record_type = 'person' AND archived_at IS NULL AND merged_into IS NULL)`,
				a.WorkspaceID, rec).Scan(&person); err != nil {
				s.internal(w, r, err)
				return
			}
		}
		if !person {
			writeError(w, r, http.StatusNotFound, "not_found", "record is not a person in this workspace")
			return
		}
	}
	var out *automationMap
	err := pgx.BeginTxFunc(r.Context(), s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = buildAutomationMap(r.Context(), tx, a, sel)
		if err == nil && rec != "" {
			out.RecordID = &rec
			err = mapPerson(r.Context(), tx, a.WorkspaceID, rec, out)
		}
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func buildAutomationMap(ctx context.Context, tx pgx.Tx, a *access, sel brandSel) (*automationMap, error) {
	ws := a.WorkspaceID
	visible := func(brand *string) bool { return a.sees(brand) && sel.matches(brand) }
	b := &mapBuilder{m: &automationMap{Nodes: []*mapNode{}, Edges: []mapEdge{}, Flags: []mapFlag{}, WindowDays: mapWindowDays},
		nodes: map[string]*mapNode{}, edges: map[mapEdge]bool{}}

	// Smart lists, by id, for rules and journey steps to point at.
	rows, err := tx.Query(ctx, smartListSelect, ws)
	if err != nil {
		return nil, err
	}
	lists, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (smartListView, error) { return scanSmartList(row) })
	if err != nil {
		return nil, err
	}
	listNode := map[string]string{}
	for _, l := range lists {
		if !visible(l.BrandID) {
			continue
		}
		n := b.node(&mapNode{ID: "list:" + l.ID, Kind: "smart_list", Label: l.Name, RefID: l.ID, Status: l.Kind, BrandID: l.BrandID,
			Detail: countOf(l.Members, "person", "people")})
		listNode[l.ID] = n.ID
		for _, rule := range l.Rules {
			if rule.Event != "" {
				b.edge(b.event(rule.Event), n.ID, "fills")
			}
		}
	}

	// Rules.
	rows, err = tx.Query(ctx, ruleSelect+` AND status <> 'archived' ORDER BY created_at`, ws)
	if err != nil {
		return nil, err
	}
	ruleList, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ruleView, error) { return scanRuleView(row) })
	if err != nil {
		return nil, err
	}
	var ruleIDs []string
	for _, rv := range ruleList {
		if !visible(rv.BrandID) {
			continue
		}
		n := b.node(&mapNode{ID: "rule:" + rv.ID, Kind: "rule", Label: rv.Name, RefID: rv.ID, Status: rv.Status, BrandID: rv.BrandID})
		ruleIDs = append(ruleIDs, rv.ID)
		t := rv.Trigger
		switch t.Kind {
		case rules.KindEvent:
			b.edge(b.event(t.Event), n.ID, "starts")
		case rules.KindAbsence:
			b.edge(b.event(t.Start()), n.ID, "starts")
			if t.StartEvent != "" && t.StartEvent != t.Event {
				b.edge(b.event(t.Event), n.ID, "waits_for")
			}
		case rules.KindAudience:
			b.edge(listNode[t.ListID], n.ID, "starts")
		case rules.KindChange:
			for _, c := range rv.Conditions {
				if c.Attribute != "" {
					f := b.node(&mapNode{ID: "field:" + c.Attribute, Kind: "field", Label: c.Attribute})
					b.edge(f.ID, n.ID, "starts")
				}
			}
		}
		for _, c := range rv.Conditions {
			if c.Event != "" {
				b.edge(b.event(c.Event), n.ID, "checks")
			}
		}
		b.edge(n.ID, b.event(rv.EmitEvent), "emits")
	}

	// Journeys, then what their steps touch.
	rows, err = tx.Query(ctx, journeySelect+` AND j.status <> 'archived' ORDER BY j.created_at`, ws)
	if err != nil {
		return nil, err
	}
	journeyList, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (journeyView, error) { return scanJourney(row) })
	if err != nil {
		return nil, err
	}
	journeyNode := map[string]string{}
	var journeyIDs []string
	for i := range journeyList {
		j := &journeyList[i]
		if !visible(j.BrandID) {
			continue
		}
		journeyNode[j.ID] = b.node(&mapNode{ID: "journey:" + j.ID, Kind: "journey", Label: j.Name, RefID: j.ID, Status: j.Status, BrandID: j.BrandID,
			Detail: countOf(len(j.Steps), "step", "steps")}).ID
		journeyIDs = append(journeyIDs, j.ID)
	}
	var seqIDs []string
	for i := range journeyList {
		j := &journeyList[i]
		n, ok := journeyNode[j.ID]
		if !ok {
			continue
		}
		if j.Trigger.Kind == "event" && j.Trigger.Event != nil {
			b.edge(b.event(*j.Trigger.Event), n, "starts")
		}
		if j.GoalEvent != nil {
			b.edge(b.event(*j.GoalEvent), n, "goal")
		}
		for _, e := range j.ExitEvents {
			b.edge(b.event(e), n, "exits_on")
		}
		for _, st := range j.Steps {
			for _, c := range st.Conditions {
				if c.Event != "" {
					b.edge(b.event(c.Event), n, "checks")
				}
			}
			switch st.Kind {
			case "add_to_list":
				b.edge(n, listNode[st.ListID], "adds_to")
			case "remove_from_list":
				b.edge(n, listNode[st.ListID], "removes_from")
			case "enter_journey":
				b.edge(n, journeyNode[st.JourneyID], "enters")
			case "exit_journey":
				if st.Scope != "others" {
					b.edge(n, journeyNode[st.JourneyID], "exits")
				}
			case "enroll_in_sequence", "stop_sequences":
				if st.SequenceID == "" {
					continue
				}
				kind := "enrolls"
				if st.Kind == "stop_sequences" {
					kind = "stops"
				}
				b.edge(n, b.node(&mapNode{ID: "sequence:" + st.SequenceID, Kind: "sequence", Label: "Sequence", RefID: st.SequenceID}).ID, kind)
				seqIDs = append(seqIDs, st.SequenceID)
			case "add_tag", "remove_tag":
				kind := "tags"
				if st.Kind == "remove_tag" {
					kind = "untags"
				}
				b.edge(n, b.node(&mapNode{ID: "tag:" + st.Tag, Kind: "tag", Label: st.Tag}).ID, kind)
			}
		}
	}

	if err := mapVolumes(ctx, tx, ws, b, ruleIDs, journeyIDs, seqIDs); err != nil {
		return nil, err
	}
	quiet, err := quietEvents(ctx, tx, ws, b)
	if err != nil {
		return nil, err
	}
	mapFlags(b, quiet)
	overlaps, err := emailOverlaps(ctx, tx, ws, journeyIDs)
	if err != nil {
		return nil, err
	}
	for _, o := range overlaps {
		a, c := b.nodes["journey:"+o.a], b.nodes["journey:"+o.b]
		b.m.Flags = append(b.m.Flags, mapFlag{Kind: "overlap", Severity: "warning", Nodes: []string{a.ID, c.ID},
			Message: fmt.Sprintf("%s and %s both emailed %s within a day of each other in the last %d days.", a.Label, c.Label, countOf(o.people, "person", "people"), mapWindowDays)})
	}
	return b.m, nil
}

// mapVolumes fills in each node's volume for the last 7 days, and the
// names of the sequences.
func mapVolumes(ctx context.Context, tx pgx.Tx, ws string, b *mapBuilder, ruleIDs, journeyIDs, seqIDs []string) error {
	set := func(id string, n int) {
		if node, ok := b.nodes[id]; ok {
			node.Volume = &n
		}
	}
	var events []string
	for _, n := range b.m.Nodes {
		if n.Kind == "event" {
			events = append(events, n.Label)
			zero := 0
			n.Volume = &zero
		}
	}
	type count struct {
		key string
		n   int
	}
	queries := []struct {
		prefix string
		sql    string
		ids    any
	}{
		{"event:", `SELECT name, count(*)::int FROM customer_events WHERE workspace_id = $1 AND name = ANY($2) AND occurred_at > now() - $3 * interval '1 day' GROUP BY name`, events},
		{"rule:", `SELECT rule_id::text, count(*)::int FROM rule_firings WHERE workspace_id = $1 AND rule_id = ANY($2::uuid[]) AND fired_at > now() - $3 * interval '1 day' GROUP BY rule_id`, ruleIDs},
		{"journey:", `SELECT journey_id::text, count(*)::int FROM journey_runs WHERE workspace_id = $1 AND journey_id = ANY($2::uuid[]) AND entered_at > now() - $3 * interval '1 day' GROUP BY journey_id`, journeyIDs},
	}
	for _, id := range ruleIDs {
		set("rule:"+id, 0)
	}
	for _, id := range journeyIDs {
		set("journey:"+id, 0)
	}
	for _, q := range queries {
		rows, err := tx.Query(ctx, q.sql, ws, q.ids, mapWindowDays)
		if err != nil {
			return err
		}
		counts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (count, error) {
			var c count
			return c, row.Scan(&c.key, &c.n)
		})
		if err != nil {
			return err
		}
		for _, c := range counts {
			set(q.prefix+c.key, c.n)
		}
	}
	if len(seqIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, name, status FROM sequences WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, seqIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, status string
		if err := rows.Scan(&id, &name, &status); err != nil {
			return err
		}
		if n, ok := b.nodes["sequence:"+id]; ok {
			n.Label, n.Status = name, status
		}
	}
	return rows.Err()
}

// quietEvents are the events that start an active journey but haven't
// happened in the last 30 days. A journey people entered in that time
// (added by hand or through the API) isn't quiet, so its event isn't
// either.
func quietEvents(ctx context.Context, tx pgx.Tx, ws string, b *mapBuilder) (map[string]bool, error) {
	var names []string
	var busy []string
	for _, e := range b.m.Edges {
		if e.Kind == "starts" && strings.HasPrefix(e.From, "event:") && b.nodes[e.To].Kind == "journey" && b.nodes[e.To].Status == "active" {
			names = append(names, strings.TrimPrefix(e.From, "event:"))
			busy = append(busy, b.nodes[e.To].RefID)
		}
	}
	quiet := map[string]bool{}
	if len(names) == 0 {
		return quiet, nil
	}
	for _, n := range names {
		quiet[n] = true
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT name FROM customer_events WHERE workspace_id = $1 AND name = ANY($2)
		AND occurred_at > now() - $3 * interval '1 day'
		UNION
		SELECT j.trigger_event FROM journeys j WHERE j.workspace_id = $1 AND j.id = ANY($4::uuid[]) AND EXISTS (
			SELECT 1 FROM journey_runs r WHERE r.journey_id = j.id AND r.entered_at > now() - $3 * interval '1 day')`, ws, names, mapQuietDays, busy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		delete(quiet, n)
	}
	return quiet, rows.Err()
}

// mapFlags adds the flags the graph itself shows: loops, events a rule
// emits that nothing listens to, and journeys whose trigger never fires.
func mapFlags(b *mapBuilder, quiet map[string]bool) {
	label := func(id string) string { return b.nodes[id].Label }
	for _, cycle := range mapCycles(b.m) {
		names := make([]string, 0, len(cycle)+1)
		for _, id := range cycle {
			if k := b.nodes[id].Kind; k == "rule" || k == "journey" {
				names = append(names, label(id))
			}
		}
		if len(names) == 0 {
			continue
		}
		names = append(names, names[0])
		b.m.Flags = append(b.m.Flags, mapFlag{Kind: "loop", Severity: "warning", Nodes: cycle,
			Message: "These set each other off in a loop: " + strings.Join(names, " → ") + "."})
	}
	listened := map[string]bool{}
	for _, e := range b.m.Edges {
		if strings.HasPrefix(e.From, "event:") {
			listened[e.From] = true
		}
	}
	for _, e := range b.m.Edges {
		if e.Kind == "emits" && !listened[e.To] {
			b.m.Flags = append(b.m.Flags, mapFlag{Kind: "unheard", Severity: "info", Nodes: []string{e.From, e.To},
				Message: fmt.Sprintf("Nothing listens to %s, which the rule %s emits: no journey, rule or list uses it.", label(e.To), label(e.From))})
		}
	}
	for _, e := range b.m.Edges {
		if e.Kind == "starts" && quiet[strings.TrimPrefix(e.From, "event:")] && strings.HasPrefix(e.From, "event:") && b.nodes[e.To].Kind == "journey" {
			b.m.Flags = append(b.m.Flags, mapFlag{Kind: "never_fires", Severity: "warning", Nodes: []string{e.From, e.To},
				Message: fmt.Sprintf("%s is live, but nobody has done %s in the last %d days, so it never starts.", label(e.To), label(e.From), mapQuietDays)})
		}
	}
}

// mapCycles finds the loops among the driving edges: each strongly
// connected group of more than one node, or a node that sets itself off,
// in a stable order.
func mapCycles(m *automationMap) [][]string {
	next := map[string][]string{}
	for _, e := range m.Edges {
		if slices.Contains(drivingEdges, e.Kind) {
			next[e.From] = append(next[e.From], e.To)
		}
	}
	// Tarjan's algorithm.
	index, low, onStack := map[string]int{}, map[string]int{}, map[string]bool{}
	var stack []string
	var out [][]string
	n := 0
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = n, n
		n++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range next[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var group []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			group = append(group, w)
			if w == v {
				break
			}
		}
		if len(group) > 1 || slices.Contains(next[v], v) {
			slices.Reverse(group)
			out = append(out, group)
		}
	}
	for _, node := range m.Nodes {
		if _, seen := index[node.ID]; !seen {
			visit(node.ID)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

type overlap struct {
	a, b   string
	people int
}

// emailOverlaps finds pairs of journeys that emailed the same people
// within a day of each other in the last 7 days. It gives up quietly when
// the workspace sends too much to check in time.
func emailOverlaps(ctx context.Context, tx pgx.Tx, ws string, ids []string) ([]overlap, error) {
	if len(ids) < 2 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT map_overlaps`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `WITH sent AS (
			SELECT s.journey_id, r.record_id, s.at FROM journey_run_steps s JOIN journey_runs r ON r.id = s.run_id
			WHERE s.workspace_id = $1 AND s.journey_id = ANY($2::uuid[]) AND s.kind IN ('`+journeys.KindEmail+`', 'transactional_email')
			  AND s.outcome = 'sent' AND s.at > now() - $3 * interval '1 day')
		SELECT x.journey_id::text, y.journey_id::text, count(DISTINCT x.record_id)::int
		FROM sent x JOIN sent y ON y.record_id = x.record_id AND x.journey_id < y.journey_id AND abs(extract(epoch FROM x.at - y.at)) < 86400
		GROUP BY 1, 2 ORDER BY 3 DESC, 1, 2 LIMIT 20`, ws, ids, mapWindowDays)
	var out []overlap
	if err == nil {
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (overlap, error) {
			var o overlap
			return o, row.Scan(&o.a, &o.b, &o.people)
		})
	}
	if err != nil {
		if !isStatementTimeout(err) {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT map_overlaps`); err != nil {
			return nil, err
		}
		return nil, nil
	}
	_, err = tx.Exec(ctx, `RELEASE SAVEPOINT map_overlaps`)
	return out, err
}

// mapPerson marks what one person has to do with the map: the events they
// did, the lists they are in, the rules that fired for them and the
// journeys they are in or went through. What those set off is theirs to
// reach next; the web app lights that up.
func mapPerson(ctx context.Context, tx pgx.Tx, ws, rec string, m *automationMap) error {
	byID := map[string]*mapNode{}
	var events []string
	for _, n := range m.Nodes {
		byID[n.ID] = n
		if n.Kind == "event" {
			events = append(events, n.Label)
		}
	}
	queries := []struct{ sql, prefix string }{
		{`SELECT DISTINCT name, 'did' FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = ANY($3)`, "event:"},
		{`SELECT list_id::text, 'member' FROM smart_list_members WHERE workspace_id = $1 AND record_id = $2`, "list:"},
		{`SELECT DISTINCT rule_id::text, 'fired' FROM rule_firings WHERE workspace_id = $1 AND record_id = $2`, "rule:"},
		{`SELECT journey_id::text, CASE WHEN bool_or(status = 'active') THEN 'in' ELSE 'entered' END FROM journey_runs
			WHERE workspace_id = $1 AND record_id = $2 GROUP BY journey_id`, "journey:"},
	}
	for _, q := range queries {
		args := []any{ws, rec}
		if strings.Contains(q.sql, "$3") {
			args = append(args, events)
		}
		rows, err := tx.Query(ctx, q.sql, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key, how string
			if err := rows.Scan(&key, &how); err != nil {
				rows.Close()
				return err
			}
			if n, ok := byID[q.prefix+key]; ok {
				n.Reached = how
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}
