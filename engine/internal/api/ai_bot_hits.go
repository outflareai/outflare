package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
)

// AI crawlers in request logs: what the analytics sync stored from
// connections with logs (Cloudflare), per host, crawler and day.

type aiBotHitsBot struct {
	crawl.AIBot
	Requests int64 `json:"requests"`
}

type aiBotHitsDay struct {
	Day      string `json:"day"`
	Search   int64  `json:"search"`
	User     int64  `json:"user"`
	Training int64  `json:"training"`
}

type aiBotHitsHost struct {
	Host     string         `json:"host"`
	Requests int64          `json:"requests"`
	Search   int64          `json:"search"`
	User     int64          `json:"user"`
	Training int64          `json:"training"`
	Bots     []aiBotHitsBot `json:"bots"`
	Days     []aiBotHitsDay `json:"days"`
}

type aiBotSyncView struct {
	IntegrationID string     `json:"integration_id"`
	Connector     string     `json:"connector"`
	Status        string     `json:"status"`
	ErrorCode     *string    `json:"error_code"`
	Message       *string    `json:"message"`
	LastRunAt     *time.Time `json:"last_run_at"`
}

func (s *Server) getAIBotHits(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	days := 28
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	host := strings.ToLower(strings.TrimSpace(q.Get("host")))
	from := time.Now().UTC().AddDate(0, 0, -days).Format(time.DateOnly)
	purpose := map[string]crawl.AIBot{}
	for _, b := range crawl.AIBots {
		purpose[b.Token] = b
	}
	rows, err := s.pool.Query(ctx, `
		SELECT host, to_char(day, 'YYYY-MM-DD'), token, sum(requests)::bigint FROM ai_bot_hits
		WHERE workspace_id = $1 AND day >= $2::date AND ($3 = '' OR host = $3)
		GROUP BY host, day, token ORDER BY host, day`, a.WorkspaceID, from, host)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byHost := map[string]*aiBotHitsHost{}
	perBot := map[string]map[string]int64{}
	perDay := map[string]map[string]*aiBotHitsDay{}
	var h, day, token string
	var n int64
	if _, err := pgx.ForEachRow(rows, []any{&h, &day, &token, &n}, func() error {
		x := byHost[h]
		if x == nil {
			x = &aiBotHitsHost{Host: h, Bots: []aiBotHitsBot{}, Days: []aiBotHitsDay{}}
			byHost[h], perBot[h], perDay[h] = x, map[string]int64{}, map[string]*aiBotHitsDay{}
		}
		d := perDay[h][day]
		if d == nil {
			d = &aiBotHitsDay{Day: day}
			perDay[h][day] = d
		}
		x.Requests += n
		perBot[h][token] += n
		switch purpose[token].Purpose {
		case "search":
			x.Search, d.Search = x.Search+n, d.Search+n
		case "user":
			x.User, d.User = x.User+n, d.User+n
		default:
			x.Training, d.Training = x.Training+n, d.Training+n
		}
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]aiBotHitsHost, 0, len(byHost))
	for h, x := range byHost {
		for _, b := range crawl.AIBots {
			if n := perBot[h][b.Token]; n > 0 {
				x.Bots = append(x.Bots, aiBotHitsBot{AIBot: b, Requests: n})
			}
		}
		sort.SliceStable(x.Bots, func(i, j int) bool { return x.Bots[i].Requests > x.Bots[j].Requests })
		for _, d := range perDay[h] {
			x.Days = append(x.Days, *d)
		}
		sort.Slice(x.Days, func(i, j int) bool { return x.Days[i].Day < x.Days[j].Day })
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Requests > out[j].Requests || (out[i].Requests == out[j].Requests && out[i].Host < out[j].Host)
	})

	// Every connection that can read logs, with how its last read went.
	rows, err = s.pool.Query(ctx, `
		SELECT i.id, i.connector, COALESCE(b.status, 'pending'), NULLIF(b.error_code, ''), NULLIF(b.message, ''), b.last_run_at
		FROM integrations i LEFT JOIN ai_bot_syncs b ON b.integration_id = i.id
		WHERE i.workspace_id = $1 AND i.connector = ANY($2) ORDER BY i.created_at`, a.WorkspaceID, s.botLogConnectors())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	conns, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (aiBotSyncView, error) {
		var v aiBotSyncView
		return v, row.Scan(&v.IntegrationID, &v.Connector, &v.Status, &v.ErrorCode, &v.Message, &v.LastRunAt)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if conns == nil {
		conns = []aiBotSyncView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "days": days, "connections": conns})
}

// botLogConnectors names the connectors that can read crawler requests
// from their logs.
func (s *Server) botLogConnectors() []string {
	ids := []string{}
	for _, d := range s.integrations.Registry.All() {
		if c, ok := s.integrations.Registry.Get(d.ID); ok {
			if _, ok := c.(connectors.BotLogReader); ok {
				ids = append(ids, d.ID)
			}
		}
	}
	return ids
}
