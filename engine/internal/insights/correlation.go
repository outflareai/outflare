package insights

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// CorrelationQuery asks what the people who reached a funnel's step did
// differently from those who didn't.
type CorrelationQuery struct {
	// Step is the step reached, counted from 1; the last step when 0.
	Step int `json:"step,omitempty"`
	// Limit is how many items each way; 10 when 0, at most 50.
	Limit int `json:"limit,omitempty"`
}

// CorrelationItem is one event, page or person property and how the two
// groups did or had it.
type CorrelationItem struct {
	// Kind is event, page or property.
	Kind string `json:"kind"`
	// Name is the event's name, the page's path, or the property as
	// "key: value".
	Name string `json:"name"`
	// Converted and Dropped are how many people of each group did it
	// after step 1.
	Converted int `json:"converted"`
	Dropped   int `json:"dropped"`
	// ConvertedRate and DroppedRate are those as shares of each group.
	ConvertedRate float64 `json:"converted_rate"`
	DroppedRate   float64 `json:"dropped_rate"`
	// OddsRatio over 1 means people who did it converted more often;
	// under 1, less often. Small counts are smoothed, so it is never 0
	// or infinite.
	OddsRatio float64 `json:"odds_ratio"`
}

// CorrelationResult splits what people did into what went with
// converting and what went with dropping off.
type CorrelationResult struct {
	Step      int    `json:"step"`
	Label     string `json:"label"`
	Converted int    `json:"converted"`
	Dropped   int    `json:"dropped"`
	// Positive is what converters did more, strongest first; Negative is
	// what people who dropped off did more.
	Positive []CorrelationItem `json:"positive"`
	Negative []CorrelationItem `json:"negative"`
	// Skipped says how many items had too few people to say anything.
	Skipped int `json:"skipped"`
}

// MinCorrelationPeople is how many people must have done something
// before it is reported: fewer tells nothing and names individuals.
const MinCorrelationPeople = 3

// correlationPrivateKeys are record attributes that identify a person
// rather than describe a group, so they are never correlated.
var correlationPrivateKeys = []string{"email", "phone", "name", "first_name", "last_name", "full_name", "website", "domain", "linkedin", "linkedin_url", "address", "street", "postal_code", "zip", "avatar", "avatar_url", "external_id"}

// maxPropertyValue is the longest attribute value correlated: longer
// ones are free text, not a group.
const maxPropertyValue = 100

// Correlate compares, for everyone at step 1, the events and pages of the
// people who reached the step against those who didn't. Events and
// pages are counted between the person's step 1 and the step (or step
// 1's deadline for those who dropped off), once per person. The
// properties of identified people (their record's text, number and
// yes/no attributes, short ones, never identifying ones) are compared
// too, as "key: value".
func (c *Compiled) Correlate(ctx context.Context, q Querier, cq CorrelationQuery) (CorrelationResult, error) {
	n := len(c.f.Steps)
	if cq.Step == 0 {
		cq.Step = n
	}
	if cq.Step < 2 || cq.Step > n {
		return CorrelationResult{}, Invalid(fmt.Sprintf("step is 2 to %d", n))
	}
	if cq.Limit == 0 {
		cq.Limit = 10
	}
	if cq.Limit < 1 || cq.Limit > 50 {
		return CorrelationResult{}, Invalid("limit is 1 to 50")
	}
	if c.f.By == ByCompany {
		return CorrelationResult{}, Invalid("correlation is for funnels of people: a company's events are its people's")
	}
	a := args{list: slices.Clone(c.args.list)}
	overall := DefaultWindow
	if c.f.Window != nil {
		overall = c.f.Window.duration()
	}
	pOverall := a.add(fmt.Sprintf("%d seconds", int64(overall.Seconds())))
	pEnd := a.add(c.f.To.Add(overall))
	pFrom := a.add(c.f.From)
	pWS, pBrand := a.add(c.ws), a.add(c.brand)
	target := fmt.Sprintf("t%d", cq.Step)
	sql := c.with + `,
		pop AS (SELECT actor, t1, ` + target + ` IS NOT NULL AS converted, COALESCE(` + target + `, t1 + ` + pOverall + `::interval) AS until FROM f),
		items AS (SELECT DISTINCT p.actor, p.converted,
				CASE WHEN e.name = 'page.viewed' THEN 'page' ELSE 'event' END AS kind,
				CASE WHEN e.name = 'page.viewed' THEN COALESCE(e.context #>> '{page,path}', '') ELSE e.name END AS name
			FROM pop p JOIN customer_events e ON e.workspace_id = ` + pWS + ` AND e.brand_id = ` + pBrand + `
				AND e.occurred_at >= ` + pFrom + ` AND e.occurred_at < ` + pEnd + `
				AND e.occurred_at > p.t1 AND e.occurred_at <= p.until
				AND COALESCE(e.record_id::text, 'a:' || e.anonymous_id) = p.actor
			UNION ALL
			SELECT p.actor, p.converted, 'property', kv.key || ': ' || kv.value
			FROM pop p JOIN records r ON p.actor NOT LIKE 'a:%' AND r.workspace_id = ` + pWS + `
				AND r.id = (CASE WHEN p.actor LIKE 'a:%' THEN NULL ELSE p.actor END)::uuid
			CROSS JOIN LATERAL jsonb_each_text(r.attributes) kv
			WHERE jsonb_typeof(r.attributes -> kv.key) IN ('string', 'boolean', 'number')
				AND kv.value <> '' AND length(kv.value) <= ` + a.add(maxPropertyValue) + `
				AND kv.key <> ALL(` + a.add(correlationPrivateKeys) + `::text[]))
		SELECT kind, name, count(*) FILTER (WHERE converted)::int, count(*) FILTER (WHERE NOT converted)::int FROM items GROUP BY kind, name
		UNION ALL SELECT '', '', count(*) FILTER (WHERE converted)::int, count(*) FILTER (WHERE NOT converted)::int FROM pop`
	rows, err := q.Query(ctx, sql, a.list...)
	if err != nil {
		return CorrelationResult{}, err
	}
	defer rows.Close()
	res := CorrelationResult{Step: cq.Step, Label: c.labels[cq.Step-1], Positive: []CorrelationItem{}, Negative: []CorrelationItem{}}
	var items []CorrelationItem
	for rows.Next() {
		var it CorrelationItem
		if err := rows.Scan(&it.Kind, &it.Name, &it.Converted, &it.Dropped); err != nil {
			return res, err
		}
		if it.Kind == "" {
			res.Converted, res.Dropped = it.Converted, it.Dropped
			continue
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	// The steps themselves tell nothing: converters did them by
	// definition.
	stepEvents := map[string]bool{}
	for _, s := range c.f.Steps {
		if s.Kind == KindEvent {
			stepEvents[s.Event] = true
		}
	}
	A, B := float64(res.Converted), float64(res.Dropped)
	var kept []CorrelationItem
	for _, it := range items {
		if it.Kind == "event" && stepEvents[it.Name] {
			continue
		}
		if it.Converted+it.Dropped < MinCorrelationPeople || (it.Converted == res.Converted && it.Dropped == res.Dropped) {
			res.Skipped++
			continue
		}
		if A > 0 {
			it.ConvertedRate = float64(it.Converted) / A
		}
		if B > 0 {
			it.DroppedRate = float64(it.Dropped) / B
		}
		// Haldane-Anscombe: half a person in every cell, so an empty
		// cell doesn't make the ratio 0 or infinite.
		a1, a0 := float64(it.Converted)+0.5, A-float64(it.Converted)+0.5
		b1, b0 := float64(it.Dropped)+0.5, B-float64(it.Dropped)+0.5
		it.OddsRatio = math.Round((a1/a0)/(b1/b0)*1000) / 1000
		kept = append(kept, it)
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].OddsRatio != kept[j].OddsRatio {
			return kept[i].OddsRatio > kept[j].OddsRatio
		}
		return strings.Compare(kept[i].Name, kept[j].Name) < 0
	})
	for _, it := range kept {
		if it.OddsRatio > 1 && len(res.Positive) < cq.Limit {
			res.Positive = append(res.Positive, it)
		}
	}
	for i := len(kept) - 1; i >= 0; i-- {
		if it := kept[i]; it.OddsRatio < 1 && len(res.Negative) < cq.Limit {
			res.Negative = append(res.Negative, it)
		}
	}
	return res, nil
}
