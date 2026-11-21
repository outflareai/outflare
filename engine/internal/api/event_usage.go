package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The event dictionary's detail: what one event name looked like in the
// last 30 days (how often, from how many people, which properties it
// carried) and where the brand uses it. It reads stored events only and
// changes nothing; the definition itself (label, description, property
// notes) is the catalog's.

// eventUsageSample is how many of the newest events the properties are
// read from: enough to see every property in use, cheap on a busy name.
const eventUsageSample = 5000

type eventUsageDay struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type eventUsageValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type eventUsageProperty struct {
	Key string `json:"key"`
	// Types are the JSON types seen, most common first.
	Types []string `json:"types"`
	// Share is the part of the sampled events that carried it, 0 to 1.
	Share float64 `json:"share"`
	// Values are its most common short values, none for an event marked
	// confidential or restricted, or for values that look like a person.
	Values []eventUsageValue `json:"values"`
}

type eventUsedIn struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type eventUsageView struct {
	Name       string               `json:"name"`
	Count      int                  `json:"count"`
	People     int                  `json:"people"`
	Days       []eventUsageDay      `json:"days"`
	Sampled    int                  `json:"sampled"`
	Properties []eventUsageProperty `json:"properties"`
	UsedIn     []eventUsedIn        `json:"used_in"`
}

func (s *Server) getEventUsage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	name := r.PathValue("event")
	if !eventName.MatchString(name) || len(name) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "event is a catalog name: lowercase words joined by dots, such as order_completed")
		return
	}
	ctx := r.Context()
	out := eventUsageView{Name: name, Days: []eventUsageDay{}, Properties: []eventUsageProperty{}, UsedIn: []eventUsedIn{}}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '15s'`); err != nil {
			return err
		}
		// A missing definition reads as internal: names nobody defined
		// arrive without one.
		sensitivity := "internal"
		if err := tx.QueryRow(ctx, `SELECT sensitivity FROM event_definitions WHERE workspace_id = $1 AND brand_id = $2 AND name = $3`,
			a.WorkspaceID, a.Brand, name).Scan(&sensitivity); err != nil && err != pgx.ErrNoRows {
			return err
		}
		showValues := sensitivity != "confidential" && sensitivity != "restricted"

		rows, err := tx.Query(ctx, `
			WITH d AS (SELECT generate_series((now() AT TIME ZONE 'UTC')::date - 29, (now() AT TIME ZONE 'UTC')::date, interval '1 day')::date AS day)
			SELECT d.day, count(e.id)::int FROM d
			LEFT JOIN customer_events e ON e.workspace_id = $1 AND e.brand_id = $2 AND e.name = $3
				AND e.occurred_at >= d.day AND e.occurred_at < d.day + 1
			GROUP BY d.day ORDER BY d.day`, a.WorkspaceID, a.Brand, name)
		if err != nil {
			return err
		}
		out.Days, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (eventUsageDay, error) {
			var d eventUsageDay
			var day time.Time
			err := row.Scan(&day, &d.Count)
			d.Day = day.Format("2006-01-02")
			return d, err
		})
		if err != nil {
			return err
		}
		for _, d := range out.Days {
			out.Count += d.Count
		}
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT COALESCE(record_id::text, 'a:' || anonymous_id, 'u:' || user_id))::int FROM customer_events
			WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= (now() AT TIME ZONE 'UTC')::date - 29`,
			a.WorkspaceID, a.Brand, name).Scan(&out.People); err != nil {
			return err
		}

		const sample = `WITH s AS (SELECT properties FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3
			AND occurred_at >= (now() AT TIME ZONE 'UTC')::date - 29 ORDER BY occurred_at DESC LIMIT $4) `
		if err := tx.QueryRow(ctx, sample+`SELECT count(*)::int FROM s`, a.WorkspaceID, a.Brand, name, eventUsageSample).Scan(&out.Sampled); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, sample+`SELECT p.key, jsonb_typeof(p.value), count(*)::int FROM s, jsonb_each(s.properties) p
			WHERE jsonb_typeof(s.properties) = 'object' GROUP BY 1, 2 ORDER BY 1, 3 DESC`, a.WorkspaceID, a.Brand, name, eventUsageSample)
		if err != nil {
			return err
		}
		byKey := map[string]*eventUsageProperty{}
		var keys []string
		var key, typ string
		var n int
		if _, err := pgx.ForEachRow(rows, []any{&key, &typ, &n}, func() error {
			p := byKey[key]
			if p == nil {
				p = &eventUsageProperty{Key: key, Types: []string{}, Values: []eventUsageValue{}}
				byKey[key] = p
				keys = append(keys, key)
			}
			p.Types = append(p.Types, typ)
			// A property has one value per event, so the types' counts add
			// up to the events that carried it.
			p.Share += float64(n)
			return nil
		}); err != nil {
			return err
		}
		if showValues && len(keys) > 0 {
			rows, err = tx.Query(ctx, sample+`, v AS (
				SELECT p.key, p.value #>> '{}' AS value, count(*)::int AS n FROM s, jsonb_each(s.properties) p
				WHERE jsonb_typeof(s.properties) = 'object' AND jsonb_typeof(p.value) IN ('string', 'boolean')
				  AND length(p.value #>> '{}') BETWEEN 1 AND 60
				GROUP BY 1, 2 HAVING count(*) >= 3)
				SELECT key, value, n FROM (SELECT *, row_number() OVER (PARTITION BY key ORDER BY n DESC, value) AS rn FROM v) x
				WHERE rn <= 5 ORDER BY key, n DESC, value`, a.WorkspaceID, a.Brand, name, eventUsageSample)
			if err != nil {
				return err
			}
			var value string
			if _, err := pgx.ForEachRow(rows, []any{&key, &value, &n}, func() error {
				// An address or a link can name a person: never shown.
				if strings.Contains(value, "@") || strings.Contains(value, "://") {
					return nil
				}
				if p := byKey[key]; p != nil {
					p.Values = append(p.Values, eventUsageValue{Value: value, Count: n})
				}
				return nil
			}); err != nil {
				return err
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			p := byKey[k]
			if out.Sampled > 0 {
				p.Share = min(1, p.Share/float64(out.Sampled))
			}
			out.Properties = append(out.Properties, *p)
		}
		if len(out.Properties) > 200 {
			out.Properties = out.Properties[:200]
		}

		rows, err = tx.Query(ctx, `
			SELECT 'insight', id::text, name FROM insights WHERE workspace_id = $1 AND brand_id = $2
				AND jsonb_path_exists(spec, '$.** ? (@.event == $n)', jsonb_build_object('n', $3::text))
			UNION ALL
			SELECT 'conversion', key, name FROM conversion_definitions WHERE workspace_id = $1 AND brand_id = $2 AND event_name = $3 AND archived_at IS NULL
			UNION ALL
			SELECT 'journey', id::text, name FROM journeys WHERE workspace_id = $1 AND brand_id = $2
				AND (trigger_event = $3 OR goal_event = $3 OR $3 = ANY(exit_events))
			ORDER BY 1, 3 LIMIT 100`, a.WorkspaceID, a.Brand, name)
		if err != nil {
			return err
		}
		out.UsedIn, err = pgx.CollectRows(rows, pgx.RowToStructByPos[eventUsedIn])
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out.UsedIn == nil {
		out.UsedIn = []eventUsedIn{}
	}
	writeJSON(w, http.StatusOK, out)
}
