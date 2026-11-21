package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/metrics"
)

// The tracking plan. An event in the catalog lists the properties it
// should carry and marks the required ones. Events that don't follow it
// are stored like any other, and counted here per day, so a person sees
// what their code sends wrong without losing data: a required property
// that was missing, or a property the event doesn't list. An event with
// no listed properties has no plan and is never counted. (A property of
// the wrong type is refused when the event arrives; see propertyMismatch.)

const (
	planMissing   = "missing_property"
	planUnplanned = "unplanned_property"
	// planKeysPerName caps the property keys remembered per event name in
	// one batch, and planKeyLength the keys worth remembering, so a sender
	// of random keys can't fill the table.
	planKeysPerName = 100
	planKeyLength   = 100
	// planUnplannedPerName caps the unlisted keys counted per event name
	// in one batch.
	planUnplannedPerName = 20
	// planKeepDays is how long a day's counts are kept.
	planKeepDays = 90
)

// sawProperties counts the properties one track event carried.
func (n *seenName) sawProperties(props map[string]any) {
	n.checked++
	for key, v := range props {
		// Keys Outflare adds itself (its link to a payment) aren't the sender's.
		if v == nil || key == "" || len(key) > planKeyLength || strings.HasPrefix(key, "outflare_") {
			continue
		}
		if n.props == nil {
			n.props = map[string]int{}
		}
		if _, known := n.props[key]; !known && len(n.props) >= planKeysPerName {
			continue
		}
		n.props[key]++
	}
}

// savePlanProblems compares a batch's track events with the brand's plan
// and adds what doesn't follow it to today's counts.
func savePlanProblems(ctx context.Context, tx pgx.Tx, ws, brand string, names map[string]*seenName) error {
	var tracked []string
	for name, n := range names {
		if n.kind == "track" && n.checked > 0 {
			tracked = append(tracked, name)
		}
	}
	if len(tracked) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT name, properties FROM event_definitions WHERE workspace_id = $1 AND brand_id = $2::uuid
		AND name = ANY($3) AND jsonb_array_length(properties) > 0`, ws, brand, tracked)
	if err != nil {
		return err
	}
	plans := map[string][]eventProperty{}
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			rows.Close()
			return err
		}
		var props []eventProperty
		if json.Unmarshal(raw, &props) == nil && len(props) > 0 {
			plans[name] = props
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	type problem struct {
		name, kind, property string
		events               int
	}
	var found []problem
	// In name order, as saveEventNames locks its rows.
	for _, name := range slices.Sorted(maps.Keys(plans)) {
		n, listed := names[name], map[string]bool{}
		for _, p := range plans[name] {
			listed[p.Key] = true
			if missing := n.checked - n.props[p.Key]; p.Required && missing > 0 {
				found = append(found, problem{name, planMissing, p.Key, missing})
			}
		}
		unplanned := 0
		for _, key := range slices.Sorted(maps.Keys(n.props)) {
			if listed[key] || unplanned >= planUnplannedPerName {
				continue
			}
			unplanned++
			found = append(found, problem{name, planUnplanned, key, n.props[key]})
		}
	}
	if len(found) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, p := range found {
		if _, err := tx.Exec(ctx, `INSERT INTO event_plan_problems (workspace_id, brand_id, event_name, kind, property, day, events)
			VALUES ($1, $2::uuid, $3, $4, $5, (now() AT TIME ZONE 'utc')::date, $6)
			ON CONFLICT (brand_id, event_name, kind, property, day) DO UPDATE
			SET events = event_plan_problems.events + EXCLUDED.events, last_seen_at = now()`,
			ws, brand, p.name, p.kind, p.property, p.events); err != nil {
			return err
		}
		counts[p.kind] += p.events
	}
	if _, err := tx.Exec(ctx, `DELETE FROM event_plan_problems WHERE workspace_id = $1 AND brand_id = $2::uuid
		AND day < (now() AT TIME ZONE 'utc')::date - $3::int`, ws, brand, planKeepDays); err != nil {
		return err
	}
	for kind, n := range counts {
		metrics.EventPlanProblems(kind, n)
	}
	return nil
}

// settlePlanProblems forgets the problems a changed plan no longer has:
// a property now listed isn't unplanned, and one no longer required
// isn't missing.
func settlePlanProblems(ctx context.Context, tx pgx.Tx, ws, brand, name string, props []eventProperty) error {
	listed, required := []string{}, []string{}
	for _, p := range props {
		listed = append(listed, p.Key)
		if p.Required {
			required = append(required, p.Key)
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM event_plan_problems WHERE workspace_id = $1 AND brand_id = $2::uuid AND event_name = $3
		AND ((kind = 'unplanned_property' AND (property = ANY($4) OR cardinality($4::text[]) = 0))
		  OR (kind = 'missing_property' AND NOT property = ANY($5)))`, ws, brand, name, listed, required)
	return err
}

type planProblemView struct {
	EventName  string    `json:"event_name"`
	Kind       string    `json:"kind"`
	Property   string    `json:"property"`
	Events     int64     `json:"events"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// listEventPlanProblems is what a brand's events got wrong against its
// tracking plan over the last days, most events first.
func (s *Server) listEventPlanProblems(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > planKeepDays {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 90")
			return
		}
		days = n
	}
	brand, ok := s.requestBrand(w, r, true)
	if !ok {
		return
	}
	ctx := r.Context()
	ws := accessFrom(ctx).WorkspaceID
	list := []planProblemView{}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := catalogBrand(ctx, tx, ws, brand)
		if err != nil || b == nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT event_name, kind, property, sum(events)::bigint, max(last_seen_at)
			FROM event_plan_problems WHERE workspace_id = $1 AND brand_id = $2::uuid
			AND day > (now() AT TIME ZONE 'utc')::date - $3::int
			GROUP BY event_name, kind, property ORDER BY sum(events) DESC, event_name, kind, property LIMIT 1000`, ws, *b, days)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v planProblemView
			if err := rows.Scan(&v.EventName, &v.Kind, &v.Property, &v.Events, &v.LastSeenAt); err != nil {
				return err
			}
			list = append(list, v)
		}
		return rows.Err()
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "days": days})
}
