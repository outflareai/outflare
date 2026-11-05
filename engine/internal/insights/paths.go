package insights

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// PathPoint names a page or an event on a path.
type PathPoint struct {
	// Kind is page or event.
	Kind string `json:"kind"`
	// Name is the page's path (/pricing) or the event's catalog name.
	Name string `json:"name"`
}

func (p PathPoint) node() string { return p.Kind + ":" + p.Name }

// Paths asks what people did step by step: the pages they viewed and the
// events they caused, in order, from a starting point or towards an end.
type Paths struct {
	// From and To bound when the paths started (or, towards an end, when
	// they ended): From inclusive, To exclusive.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Start is where every path begins; End is where every path ends.
	// Set one or neither: with neither, a path begins at each person's
	// first page or event in the dates.
	Start *PathPoint `json:"start,omitempty"`
	End   *PathPoint `json:"end,omitempty"`
	// Steps is how many steps a path shows, Start or End included: 2 to
	// MaxPathSteps, DefaultPathSteps when 0.
	Steps int `json:"steps,omitempty"`
	// Kinds says what counts as a step: page, event or both (the default).
	Kinds []string `json:"kinds,omitempty"`
	// Window is how long a whole path may take from its first step;
	// DefaultPathWindow when unset.
	Window *Window `json:"window,omitempty"`
	// Top is how many pages or events a step shows before the rest fold
	// into "other": 1 to MaxPathTop, DefaultPathTop when 0.
	Top int `json:"top,omitempty"`
	// Exclude leaves pages or events out as if they never happened.
	Exclude []PathPoint `json:"exclude,omitempty"`
}

// Path limits.
const (
	MaxPathSteps      = 6
	DefaultPathSteps  = 4
	MaxPathTop        = 20
	DefaultPathTop    = 8
	DefaultPathWindow = 24 * time.Hour
	MaxPathExclude    = 20
)

// PathKinds are what a path step can be.
var PathKinds = []string{KindPage, KindEvent}

// OtherNode is the name of the step node that holds everything past Top.
const OtherNode = "other"

// PathNode is one page or event at one step, and how many people were
// there. Kind is page, event or other.
type PathNode struct {
	Step  int    `json:"step"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	// DropOff is how many people at this node went nowhere in the window:
	// Count minus the links out, except at the last step.
	DropOff int `json:"drop_off"`
}

// PathLink is how many people went from one node to another at the next
// step.
type PathLink struct {
	Step     int    `json:"step"`
	FromKind string `json:"from_kind"`
	From     string `json:"from"`
	ToKind   string `json:"to_kind"`
	To       string `json:"to"`
	Count    int    `json:"count"`
}

// PathsResult is a path graph: nodes per step and the links between
// steps, ready for a flow chart.
type PathsResult struct {
	// People is how many people's paths were counted.
	People int        `json:"people"`
	Steps  int        `json:"steps"`
	Nodes  []PathNode `json:"nodes"`
	Links  []PathLink `json:"links"`
}

func validPoint(p *PathPoint, at string) error {
	if p == nil {
		return nil
	}
	switch p.Kind {
	case KindPage:
		if p.Name == "" || len(p.Name) > 500 || p.Name[0] != '/' {
			return Invalid(at + ".name is a page path, such as /pricing")
		}
	case KindEvent:
		if !eventName(p.Name) {
			return Invalid(at + ".name is an event's catalog name, such as signed_up")
		}
	default:
		return Invalid(at + ".kind is page or event")
	}
	return nil
}

// Validate checks a path question's shape without the database.
func (p Paths) Validate() error {
	if p.From.IsZero() || p.To.IsZero() || !p.To.After(p.From) || p.To.Sub(p.From) > MaxRange {
		return Invalid("from and to are dates, from before to, at most 366 days apart")
	}
	if p.Start != nil && p.End != nil {
		return Invalid("set start or end, not both: paths run from a start, or towards an end")
	}
	if err := validPoint(p.Start, "start"); err != nil {
		return err
	}
	if err := validPoint(p.End, "end"); err != nil {
		return err
	}
	if p.Steps != 0 && (p.Steps < 2 || p.Steps > MaxPathSteps) {
		return Invalid(fmt.Sprintf("steps is 2 to %d", MaxPathSteps))
	}
	if p.Top != 0 && (p.Top < 1 || p.Top > MaxPathTop) {
		return Invalid(fmt.Sprintf("top is 1 to %d", MaxPathTop))
	}
	for _, k := range p.Kinds {
		if !slices.Contains(PathKinds, k) {
			return Invalid("kinds are page and event")
		}
	}
	if err := validWindow(p.Window, "", "window"); err != nil {
		return err
	}
	if len(p.Exclude) > MaxPathExclude {
		return Invalid(fmt.Sprintf("exclude has at most %d pages or events", MaxPathExclude))
	}
	for i := range p.Exclude {
		if err := validPoint(&p.Exclude[i], fmt.Sprintf("exclude[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

// RunPaths counts the paths in workspace ws and brand.
func RunPaths(ctx context.Context, q Querier, ws, brand string, p Paths) (PathsResult, error) {
	if err := p.Validate(); err != nil {
		return PathsResult{}, err
	}
	steps, top := p.Steps, p.Top
	if steps == 0 {
		steps = DefaultPathSteps
	}
	if top == 0 {
		top = DefaultPathTop
	}
	window := DefaultPathWindow
	if p.Window != nil {
		window = p.Window.duration()
	}
	var a args
	pWS, pBrand := a.add(ws), a.add(brand)
	pFrom, pTo := a.add(p.From), a.add(p.To)
	pWindow := a.add(fmt.Sprintf("%d seconds", int64(window.Seconds())))
	// Items can be a window before the dates (towards an end) or after
	// them (from a start).
	pLow, pHigh := a.add(p.From.Add(-window)), a.add(p.To.Add(window))
	where := []string{`e.workspace_id = ` + pWS, `e.brand_id = ` + pBrand, `(e.record_id IS NOT NULL OR e.anonymous_id IS NOT NULL)`,
		`e.occurred_at >= ` + pLow, `e.occurred_at < ` + pHigh}
	switch {
	case len(p.Kinds) == 1 && p.Kinds[0] == KindPage:
		where = append(where, `e.name = 'page.viewed'`)
	case len(p.Kinds) == 1 && p.Kinds[0] == KindEvent:
		where = append(where, `e.name <> 'page.viewed'`)
	}
	node := `CASE WHEN e.name = 'page.viewed' THEN 'page:' || COALESCE(e.context #>> '{page,path}', '') ELSE 'event:' || e.name END`
	if len(p.Exclude) > 0 {
		var ex []string
		for _, x := range p.Exclude {
			ex = append(ex, x.node())
		}
		where = append(where, `NOT (`+node+` = ANY (`+a.add(ex)+`::text[]))`)
	}
	items := `SELECT COALESCE(e.record_id::text, 'a:' || e.anonymous_id) AS actor, e.occurred_at AS ts, ` + node + ` AS node FROM customer_events e WHERE ` + strings.Join(where, " AND ")

	var anchors, seq string
	reverse := false
	switch {
	case p.Start != nil:
		anchors = `SELECT DISTINCT ON (actor) actor, ts FROM items WHERE node = ` + a.add(p.Start.node()) + ` AND ts >= ` + pFrom + ` AND ts < ` + pTo + ` ORDER BY actor, ts`
	case p.End != nil:
		reverse = true
		anchors = `SELECT DISTINCT ON (actor) actor, ts FROM items WHERE node = ` + a.add(p.End.node()) + ` AND ts >= ` + pFrom + ` AND ts < ` + pTo + ` ORDER BY actor, ts`
	default:
		anchors = `SELECT actor, min(ts) AS ts FROM items WHERE ts >= ` + pFrom + ` AND ts < ` + pTo + ` GROUP BY actor`
	}
	if reverse {
		seq = `SELECT i.actor, i.node, row_number() OVER (PARTITION BY i.actor ORDER BY i.ts DESC, i.node) AS n
			FROM anchors an JOIN items i ON i.actor = an.actor AND i.ts <= an.ts AND i.ts >= an.ts - ` + pWindow + `::interval`
	} else {
		seq = `SELECT i.actor, i.node, row_number() OVER (PARTITION BY i.actor ORDER BY i.ts, i.node) AS n
			FROM anchors an JOIN items i ON i.actor = an.actor AND i.ts >= an.ts AND i.ts <= an.ts + ` + pWindow + `::interval`
	}
	pSteps, pTop := a.add(steps), a.add(top)
	sql := `WITH items AS (` + items + `),
		anchors AS (` + anchors + `),
		seq AS (` + seq + `),
		lim AS (SELECT actor, node, n::int AS n FROM seq WHERE n <= ` + pSteps + `),
		ranked AS (SELECT n, node, row_number() OVER (PARTITION BY n ORDER BY count(*) DESC, node) AS r FROM lim GROUP BY n, node),
		named AS (SELECT l.actor, l.n, CASE WHEN r.r <= ` + pTop + ` THEN l.node ELSE 'other:' END AS node FROM lim l JOIN ranked r ON r.n = l.n AND r.node = l.node)
		SELECT 'n', n, node, '', count(*)::int FROM named GROUP BY n, node
		UNION ALL SELECT 'l', a.n, a.node, b.node, count(*)::int FROM named a JOIN named b ON b.actor = a.actor AND b.n = a.n + 1 GROUP BY a.n, a.node, b.node
		UNION ALL SELECT 'p', 0, '', '', count(*)::int FROM anchors`
	rows, err := q.Query(ctx, sql, a.list...)
	if err != nil {
		return PathsResult{}, err
	}
	defer rows.Close()
	res := PathsResult{Steps: steps, Nodes: []PathNode{}, Links: []PathLink{}}
	split := func(node string) (string, string) {
		kind, name, _ := strings.Cut(node, ":")
		return kind, name
	}
	// Towards an end, n counts back from the end: the end is the last
	// step and shorter paths start later.
	step := func(n int) int {
		if reverse {
			return steps - n + 1
		}
		return n
	}
	for rows.Next() {
		var row, from, to string
		var n, count int
		if err := rows.Scan(&row, &n, &from, &to, &count); err != nil {
			return res, err
		}
		switch row {
		case "p":
			res.People = count
		case "n":
			k, name := split(from)
			res.Nodes = append(res.Nodes, PathNode{Step: step(n), Kind: k, Name: name, Count: count})
		case "l":
			fk, fn := split(from)
			tk, tn := split(to)
			l := PathLink{Step: step(n), FromKind: fk, From: fn, ToKind: tk, To: tn, Count: count}
			if reverse {
				l = PathLink{Step: step(n + 1), FromKind: tk, From: tn, ToKind: fk, To: fn, Count: count}
			}
			res.Links = append(res.Links, l)
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	out := map[[3]string]int{}
	for _, l := range res.Links {
		out[[3]string{fmt.Sprint(l.Step), l.FromKind, l.From}] += l.Count
	}
	for i := range res.Nodes {
		nd := &res.Nodes[i]
		if nd.Step < steps {
			nd.DropOff = nd.Count - out[[3]string{fmt.Sprint(nd.Step), nd.Kind, nd.Name}]
		}
	}
	sort.Slice(res.Nodes, func(i, j int) bool {
		if res.Nodes[i].Step != res.Nodes[j].Step {
			return res.Nodes[i].Step < res.Nodes[j].Step
		}
		if res.Nodes[i].Count != res.Nodes[j].Count {
			return res.Nodes[i].Count > res.Nodes[j].Count
		}
		return res.Nodes[i].Name < res.Nodes[j].Name
	})
	sort.Slice(res.Links, func(i, j int) bool {
		if res.Links[i].Step != res.Links[j].Step {
			return res.Links[i].Step < res.Links[j].Step
		}
		if res.Links[i].Count != res.Links[j].Count {
			return res.Links[i].Count > res.Links[j].Count
		}
		return res.Links[i].From+res.Links[i].To < res.Links[j].From+res.Links[j].To
	})
	return res, nil
}
