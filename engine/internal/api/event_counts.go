package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// The event list's counts: how many of each event name a brand stored in
// the last days, from how many people, and when the newest arrived. The
// definitions carry an all-time count (seen_count); these let a person
// check a name is the busy one they mean over a recent range before they
// build on it (a conversion, a journey, a report).

type eventCountView struct {
	Name   string    `json:"name"`
	Events int64     `json:"events"`
	People int64     `json:"people"`
	LastAt time.Time `json:"last_at"`
	// Sources are where they came from (website, api, transactions for
	// the payments Outflare makes from orders and billing, a connector),
	// most events first.
	Sources []eventCountSource `json:"sources"`
}

type eventCountSource struct {
	Source string `json:"source"`
	Events int64  `json:"events"`
}

// eventCountMaxDays is the longest range the counts read.
const eventCountMaxDays = 365

func (s *Server) listEventCounts(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > eventCountMaxDays {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	a := accessFrom(r.Context())
	ctx := r.Context()
	list := []eventCountView{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '20s'`); err != nil {
			return err
		}
		// Days are whole UTC days, today included, as in the usage detail.
		rows, err := tx.Query(ctx, `SELECT name, count(*)::bigint,
				count(DISTINCT COALESCE(record_id::text, 'a:' || anonymous_id, 'u:' || user_id))::bigint, max(occurred_at)
			FROM customer_events WHERE workspace_id = $1 AND brand_id = $2
			  AND occurred_at >= (now() AT TIME ZONE 'UTC')::date - ($3::int - 1)
			GROUP BY name ORDER BY count(*) DESC, name LIMIT 5000`, a.WorkspaceID, a.Brand, days)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (eventCountView, error) {
			var c eventCountView
			c.Sources = []eventCountSource{}
			err := row.Scan(&c.Name, &c.Events, &c.People, &c.LastAt)
			return c, err
		})
		if err != nil {
			return err
		}
		at := make(map[string]int, len(list))
		for i, c := range list {
			at[c.Name] = i
		}
		rows, err = tx.Query(ctx, `SELECT name, source, count(*)::bigint FROM customer_events
			WHERE workspace_id = $1 AND brand_id = $2 AND occurred_at >= (now() AT TIME ZONE 'UTC')::date - ($3::int - 1)
			GROUP BY name, source ORDER BY count(*) DESC, source`, a.WorkspaceID, a.Brand, days)
		if err != nil {
			return err
		}
		var name string
		var src eventCountSource
		_, err = pgx.ForEachRow(rows, []any{&name, &src.Source, &src.Events}, func() error {
			if i, ok := at[name]; ok {
				list[i].Sources = append(list[i].Sources, src)
			}
			return nil
		})
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "days": days})
}
