package api

import (
	"net/http"
	"sort"
)

// adLevels maps ?level= to the key the tracking template stores it under
// in a visit's ad (adtrack.AdParams), and to the level synced ad entities
// use for it.
var adLevels = map[string]struct{ key, entity string }{
	"campaign": {"campaign", "campaign"},
	"adset":    {"adset", "ad_group"},
	"ad":       {"ad", "ad"},
}

type attributionAdRow struct {
	// ID is the network's own id, as the tracking template recorded it.
	ID string `json:"id"`
	// The synced ad entity with that id, when an ad account has one.
	AdEntityID  *string `json:"ad_entity_id"`
	AdAccountID *string `json:"ad_account_id"`
	Connector   *string `json:"connector"`
	Name        string  `json:"name"`
	// First-party numbers: visits that arrived through the ad, and the
	// conversions and revenue credited to them under the model.
	Visits      int                `json:"visits"`
	Conversions float64            `json:"conversions"`
	ByEvent     map[string]float64 `json:"by_event"`
	Revenue     []money            `json:"revenue"`
	// What the ad platform reports for the same days: spend, and the
	// conversions it claims, never added to first-party ones.
	Cost                []money  `json:"cost"`
	PlatformConversions *float64 `json:"platform_conversions"`
	row                 *attributionRow
}

// attributionAds joins paid visits, by the network's ids the tracking
// template put on the landing page (site_sessions.ad), to what the ad
// platforms report for the same ids: first-party visits, credited
// conversions and revenue beside spend and the platform's own count. A
// campaign with spend but no tracked visits is listed too, since that
// usually means its tracking template is missing.
func (s *Server) attributionAds(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	levelName := r.URL.Query().Get("level")
	if levelName == "" {
		levelName = "campaign"
	}
	level, ok := adLevels[levelName]
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "level is campaign, adset or ad")
		return
	}
	ctx := r.Context()
	rows := map[string]*attributionAdRow{}
	row := func(id string) *attributionAdRow {
		if rows[id] == nil {
			rows[id] = &attributionAdRow{ID: id, Cost: []money{}, row: newAttributionRow("")}
		}
		return rows[id]
	}

	// level.key comes from adLevels, never from the request.
	args := []any{a.WorkspaceID, f.from, f.to}
	vq, err := s.pool.Query(ctx, `SELECT ad->>'`+level.key+`', count(*)::int FROM site_sessions
		WHERE workspace_id = $1 AND started_at >= $2::date AND started_at < $3::date + 1 AND ad ? '`+level.key+`'`+
		f.brand.sql(`brand_id`, &args)+`
		GROUP BY 1`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for vq.Next() {
		var id string
		var n int
		if err := vq.Scan(&id, &n); err != nil {
			vq.Close()
			s.internal(w, r, err)
			return
		}
		row(id).Visits += n
	}
	vq.Close()
	if err := vq.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	err = s.creditRows(ctx, a.WorkspaceID, f, ` JOIN site_sessions s ON s.id = cr.session_id`, `s.ad->>'`+level.key+`'`,
		` AND s.ad ? '`+level.key+`'`, nil,
		func(key []any, name string, weight float64, currency *string, value *float64) error {
			if id, ok := key[0].(string); ok && id != "" {
				row(id).row.addCredit(name, weight, currency, value)
			}
			return nil
		})
	if err != nil {
		s.internal(w, r, err)
		return
	}

	// The synced entities with those ids, or with spend in the range. An
	// id two accounts share takes the one that spent more.
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	eargs := []any{a.WorkspaceID, f.from, f.to, level.entity, ids}
	eq, err := s.pool.Query(ctx, `SELECT DISTINCT ON (e.external_id) e.external_id, e.id::text, a.id::text, a.connector, e.name, a.currency,
			sum(m.spend_micros)::float8, sum(m.conversions)::float8
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		LEFT JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day BETWEEN $2::date AND $3::date
		WHERE e.workspace_id = $1 AND e.level = $4`+f.brand.sql(`COALESCE(e.brand_id, a.brand_id)`, &eargs)+`
		GROUP BY e.id, a.id
		HAVING e.external_id = ANY($5::text[]) OR sum(m.spend_micros) > 0
		ORDER BY e.external_id, sum(m.spend_micros) DESC NULLS LAST`, eargs...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for eq.Next() {
		var id, entity, account, connector, name, currency string
		var spend, convs *float64
		if err := eq.Scan(&id, &entity, &account, &connector, &name, &currency, &spend, &convs); err != nil {
			eq.Close()
			s.internal(w, r, err)
			return
		}
		x := row(id)
		x.AdEntityID, x.AdAccountID, x.Connector, x.Name = &entity, &account, &connector, name
		if spend != nil && *spend > 0 {
			// Micros to cents.
			x.Cost = moneyList(map[string]float64{currency: *spend / 10_000})
		}
		if convs != nil {
			v := round2(*convs)
			x.PlatformConversions = &v
		}
	}
	eq.Close()
	if err := eq.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	out := make([]*attributionAdRow, 0, len(rows))
	for _, x := range rows {
		x.row.finish()
		x.Conversions, x.ByEvent, x.Revenue = x.row.Conversions, x.row.ByEvent, x.row.Revenue
		out = append(out, x)
	}
	spent := func(x *attributionAdRow) int64 {
		var n int64
		for _, c := range x.Cost {
			n += c.AmountCents
		}
		return n
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Conversions != out[j].Conversions {
			return out[i].Conversions > out[j].Conversions
		}
		if si, sj := spent(out[i]), spent(out[j]); si != sj {
			return si > sj
		}
		if out[i].Visits != out[j].Visits {
			return out[i].Visits > out[j].Visits
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > 500 {
		out = out[:500]
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": f.ref, "model_version": f.version, "from": f.from, "to": f.to, "level": levelName, "data": out})
}
