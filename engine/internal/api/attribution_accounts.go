package api

import (
	"net/http"
	"sort"
)

// Credit for companies and deals: what each company's people brought, with
// the deals it won, and the value of won deals per channel, spread by the
// credit its people earned before it closed.

// accountRow is one company.
type accountRow struct {
	RecordID        string  `json:"record_id"`
	Name            string  `json:"name"`
	People          int     `json:"people"`
	Conversions     float64 `json:"conversions"`
	Revenue         []money `json:"revenue"`
	TopChannel      string  `json:"top_channel"`
	TopChannelLabel string  `json:"top_channel_label"`
	DealsWon        int     `json:"deals_won"`
	DealValue       []money `json:"deal_value"`
	conv            float64
	people          map[string]bool
	revenue, deals  map[string]float64
	byChannel       map[string]float64
}

// personCompany is a person's first company, the one their outreach uses.
const personCompany = ` JOIN LATERAL (SELECT rr.to_id FROM record_relationships rr
		WHERE rr.workspace_id = c.workspace_id AND rr.from_id = c.record_id AND rr.kind = 'companies' AND rr.valid_to IS NULL
		ORDER BY rr.valid_from, rr.to_id LIMIT 1) co ON true`

// attributionAccounts rolls credit up from people to their company.
func (s *Server) attributionAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rows := map[string]*accountRow{}
	row := func(id string) *accountRow {
		if rows[id] == nil {
			rows[id] = &accountRow{RecordID: id, people: map[string]bool{}, revenue: map[string]float64{},
				deals: map[string]float64{}, byChannel: map[string]float64{}}
		}
		return rows[id]
	}
	// creditRows counts its key columns by their commas, so the keys have none.
	err := s.creditRows(ctx, a.WorkspaceID, f, personCompany, `co.to_id::text, cr.channel, c.record_id::text`, ``, nil,
		func(key []any, name string, weight float64, currency *string, value *float64) error {
			id, _ := key[0].(string)
			ch, _ := key[1].(string)
			person, _ := key[2].(string)
			x := row(id)
			if name != "payment.refunded" {
				x.conv += weight
				x.byChannel[ch] += weight
				x.people[person] = true
			}
			if value != nil && currency != nil && *currency != "" {
				x.revenue[*currency] += *value
			}
			return nil
		})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Deals won in the period, by the company on the deal.
	args := []any{a.WorkspaceID, f.start, f.end}
	q, err := s.pool.Query(ctx, `SELECT dc.to_id::text, count(*)::int, coalesce(d.currency, ''), coalesce(sum(d.amount_cents), 0)::float8
		FROM deals d
		JOIN record_relationships dc ON dc.workspace_id = d.workspace_id AND dc.from_id = d.id AND dc.field_key = 'associated_company' AND dc.valid_to IS NULL
		LEFT JOIN records dr ON dr.id = d.id
		WHERE d.workspace_id = $1 AND d.state = 'won' AND d.closed_at >= $2 AND d.closed_at < $3`+
		f.brand.sql(`dr.brand_id`, &args)+`
		GROUP BY dc.to_id, d.currency`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for q.Next() {
		var id, cur string
		var n int
		var v float64
		if err := q.Scan(&id, &n, &cur, &v); err != nil {
			q.Close()
			s.internal(w, r, err)
			return
		}
		x := row(id)
		x.DealsWon += n
		if cur != "" && v != 0 {
			x.deals[cur] += v
		}
	}
	q.Close()
	if err := q.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]*accountRow, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for id, x := range rows {
		x.People, x.Conversions = len(x.people), round2(x.conv)
		x.Revenue, x.DealValue = moneyList(x.revenue), moneyList(x.deals)
		for ch, v := range x.byChannel {
			if v > x.byChannel[x.TopChannel] || (v == x.byChannel[x.TopChannel] && ch < x.TopChannel) {
				x.TopChannel = ch
			}
		}
		if x.TopChannel != "" {
			x.TopChannelLabel = channelLabel(x.TopChannel)
		}
		out = append(out, x)
		ids = append(ids, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].conv != out[j].conv {
			return out[i].conv > out[j].conv
		}
		if out[i].DealsWon != out[j].DealsWon {
			return out[i].DealsWon > out[j].DealsWon
		}
		return out[i].RecordID < out[j].RecordID
	})
	if len(out) > 200 {
		out = out[:200]
	}
	names := map[string]string{}
	q, err = s.pool.Query(ctx, `SELECT id::text, name FROM records WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, a.WorkspaceID, ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for q.Next() {
		var id, name string
		if err := q.Scan(&id, &name); err != nil {
			q.Close()
			s.internal(w, r, err)
			return
		}
		names[id] = name
	}
	q.Close()
	if err := q.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	for _, x := range out {
		x.Name = names[x.RecordID]
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": f.ref, "model_version": f.version, "from": f.from, "to": f.to, "data": out})
}

// dealRow is one channel's share of won deals.
type dealRow struct {
	Channel string  `json:"channel"`
	Label   string  `json:"label"`
	Deals   float64 `json:"deals"`
	Value   []money `json:"value"`
	n       float64
	value   map[string]float64
}

// attributionDeals spreads each deal won in the period across channels, in
// proportion to the credit its people's conversions before the close earned.
func (s *Server) attributionDeals(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, f.start, f.end, f.model}
	won := `SELECT d.id, d.closed_at, d.currency, d.amount_cents, d.record_id FROM deals d
			LEFT JOIN records dr ON dr.id = d.id
			WHERE d.workspace_id = $1 AND d.state = 'won' AND d.closed_at >= $2 AND d.closed_at < $3` +
		f.brand.sql(`dr.brand_id`, &args)
	matched, matchedWhere := f.creditSQL()
	sql := `WITH won AS (` + won + `),
		people AS (
			SELECT w.id AS deal_id, w.record_id AS person FROM won w WHERE w.record_id IS NOT NULL
			UNION SELECT w.id, rr.to_id FROM won w JOIN record_relationships rr ON rr.workspace_id = $1 AND rr.from_id = w.id
				AND rr.field_key = 'associated_people' AND rr.valid_to IS NULL
			UNION SELECT w.id, pc.from_id FROM won w
				JOIN record_relationships dc ON dc.workspace_id = $1 AND dc.from_id = w.id AND dc.field_key = 'associated_company' AND dc.valid_to IS NULL
				JOIN record_relationships pc ON pc.workspace_id = $1 AND pc.to_id = dc.to_id AND pc.kind = 'companies' AND pc.valid_to IS NULL),
		weights AS (
			SELECT p.deal_id, cr.channel, sum(cr.weight) AS w FROM people p
			JOIN won w ON w.id = p.deal_id
			JOIN attribution_conversions c ON c.workspace_id = $1 AND c.record_id = p.person AND c.occurred_at <= w.closed_at AND c.name <> 'payment.refunded'
			JOIN attribution_credits cr ON cr.event_id = c.event_id AND cr.model = $4` + matched + `
			WHERE true` + matchedWhere + f.brand.sql(`c.brand_id`, &args) + `
			GROUP BY p.deal_id, cr.channel),
		shares AS (SELECT deal_id, channel, w / nullif(sum(w) OVER (PARTITION BY deal_id), 0) AS share FROM weights)
		SELECT s.channel, coalesce(w.currency, ''), sum(coalesce(s.share, 1))::float8, coalesce(sum(coalesce(s.share, 1) * w.amount_cents), 0)::float8
		FROM won w LEFT JOIN shares s ON s.deal_id = w.id AND s.share IS NOT NULL
		GROUP BY s.channel, w.currency`
	q, err := s.pool.Query(r.Context(), sql, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer q.Close()
	rows := map[string]*dealRow{}
	var none float64
	noneValue := map[string]float64{}
	for q.Next() {
		var ch *string
		var cur string
		var n, v float64
		if err := q.Scan(&ch, &cur, &n, &v); err != nil {
			s.internal(w, r, err)
			return
		}
		if ch == nil {
			none += n
			if cur != "" && v != 0 {
				noneValue[cur] += v
			}
			continue
		}
		x := rows[*ch]
		if x == nil {
			x = &dealRow{Channel: *ch, Label: channelLabel(*ch), value: map[string]float64{}}
			rows[*ch] = x
		}
		x.n += n
		if cur != "" && v != 0 {
			x.value[cur] += v
		}
	}
	if err := q.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]*dealRow, 0, len(rows))
	for _, x := range rows {
		x.Deals, x.Value = round2(x.n), moneyList(x.value)
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].Channel < out[j].Channel
	})
	writeJSON(w, http.StatusOK, map[string]any{"model": f.ref, "model_version": f.version, "from": f.from, "to": f.to, "data": out,
		"unattributed": map[string]any{"deals": int(none + 0.5), "value": moneyList(noneValue)}})
}
