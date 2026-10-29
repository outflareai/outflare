package outcomes

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// Report groupings.
const (
	GroupNone     = "none"
	GroupSequence = "sequence"
	// A row's brand is its sequence's; spend without a sequence takes its
	// own brand, else its mailbox's or domain's.
	GroupBrand = "brand"
)

// ReportFilter picks what a report covers.
type ReportFilter struct {
	From, To   time.Time // [From, To)
	GroupBy    string
	SequenceID string
	MissionID  string
	// Brand is a brand's id, "none" for what no brand owns, or "" for all.
	// BrandsIn, when set, keeps the report to these brands and shared
	// records (a brand-limited caller).
	Brand    string
	BrandsIn []string
	// Currency the report adds up in. Money in other currencies is left
	// out and counted in Report.OtherCurrencies; nothing is converted.
	Currency string
}

// Row is one line of a report: a sequence, a brand, or the whole
// workspace. Key is empty for outcomes and spend that belong to no group.
type Row struct {
	Key  string `json:"key"`
	Name string `json:"name"`

	Sent      int      `json:"sent"`
	Replies   int      `json:"replies"`
	ReplyRate *float64 `json:"reply_rate"`

	// Meetings booked in the period and tied to outreach, canceled ones
	// left out. Held and NoShows are those of them with that status.
	Meetings  int `json:"meetings"`
	Held      int `json:"held"`
	NoShows   int `json:"no_shows"`
	Qualified int `json:"qualified"`

	Deals         int   `json:"deals"`
	Won           int   `json:"won"`
	PipelineCents int64 `json:"pipeline_cents"`
	WonCents      int64 `json:"won_cents"`

	// Links pitched through the row's sequences that went live in the
	// period (link building).
	LiveLinks int `json:"live_links"`

	Spend Spend `json:"spend"`

	CostPerReply   *int64 `json:"cost_per_reply_cents"`
	CostPerMeeting *int64 `json:"cost_per_meeting_cents"`
	CostPerHeld    *int64 `json:"cost_per_held_meeting_cents"`
	CostPerDeal    *int64 `json:"cost_per_deal_cents"`
	CostPerWon     *int64 `json:"cost_per_won_deal_cents"`
	CostPerLink    *int64 `json:"cost_per_live_link_cents"`
	// Won revenue per unit of spend, when both are known.
	ROI *float64 `json:"roi"`
}

// Spend is money spent, split by how it was tied to the row.
type Spend struct {
	TotalCents int64 `json:"total_cents"`
	// Charged to this row directly, or through the mailboxes and domains
	// its sends went out from.
	DirectCents int64 `json:"direct_cents"`
	// The row's share of workspace-wide costs, by its share of sends.
	SharedCents int64 `json:"shared_cents"`
	// Part of the total priced from units with the workspace's rates
	// rather than recorded as money.
	EstimatedCents int64 `json:"estimated_cents"`
	// Recurring costs spread over the period.
	RecurringCents int64            `json:"recurring_cents"`
	ByCategory     map[string]int64 `json:"by_category"`
}

// Unpriced is usage recorded without money and without a rate.
type Unpriced struct {
	Provider string  `json:"provider"`
	UnitName string  `json:"unit_name"`
	Units    float64 `json:"units"`
}

// Report is cost per outcome over a period.
type Report struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	GroupBy  string    `json:"group_by"`
	Currency string    `json:"currency"`
	Totals   Row       `json:"totals"`
	Rows     []Row     `json:"rows"`
	// Meetings booked in the period that no lead or campaign explains.
	UnattributedMeetings int        `json:"unattributed_meetings"`
	Unpriced             []Unpriced `json:"unpriced"`
	// Money left out because it is in another currency, by currency.
	OtherCurrencies map[string]int64 `json:"other_currencies"`
	// Plain-language caveats about the numbers, for people and agents.
	Notes []string `json:"notes"`
}

type acc struct {
	Row
	direct, shared, estimated, recurring float64
	cat                                  map[string]float64
}

type sendKey struct{ seq, mailbox, domain string }

// Summary builds a cost-per-outcome report.
func Summary(ctx context.Context, q Querier, ws string, f ReportFilter) (Report, error) {
	if f.GroupBy == "" {
		f.GroupBy = GroupNone
	}
	if f.GroupBy != GroupNone && f.GroupBy != GroupSequence && f.GroupBy != GroupBrand {
		return Report{}, invalid("group_by must be none, sequence or brand")
	}
	if f.Currency == "" {
		f.Currency = "USD"
	}
	f.Currency = strings.ToUpper(f.Currency)
	if !currencyRe.MatchString(f.Currency) {
		return Report{}, invalid("currency must be a three-letter code such as USD")
	}
	if !f.To.After(f.From) {
		return Report{}, invalid("to must be after from")
	}
	if f.To.Sub(f.From) > 3*366*24*time.Hour {
		return Report{}, invalid("a report covers at most three years")
	}
	rep := Report{From: f.From, To: f.To, GroupBy: f.GroupBy, Currency: f.Currency, Rows: []Row{},
		Unpriced: []Unpriced{}, OtherCurrencies: map[string]int64{}, Notes: []string{}}

	rows := map[string]*acc{}
	get := func(k string) *acc {
		a, ok := rows[k]
		if !ok {
			a = &acc{Row: Row{Key: k}, cat: map[string]float64{}}
			rows[k] = a
		}
		return a
	}
	// Brands, when the report is by brand or kept to some.
	brands := brandMaps{}
	if f.GroupBy == GroupBrand || f.Brand != "" || f.BrandsIn != nil {
		var err error
		if brands, err = loadBrandMaps(ctx, q, ws); err != nil {
			return rep, err
		}
	}
	keep := func(b string) bool {
		if f.BrandsIn != nil && b != "" && !slices.Contains(f.BrandsIn, b) {
			return false
		}
		switch f.Brand {
		case "":
			return true
		case "none":
			return b == ""
		}
		return b == f.Brand
	}
	// key is the row a sequence's outcome goes to. brand is the brand of
	// spend that has no sequence, or nil.
	key := func(seq, brand *string) string {
		switch f.GroupBy {
		case GroupBrand:
			if seq == nil && brand != nil {
				return *brand
			}
			return brands.of(seq)
		case GroupSequence:
			if seq != nil {
				return *seq
			}
		case GroupNone:
			return "all"
		}
		return ""
	}
	if f.GroupBy == GroupNone {
		get("all")
	}
	// Sends and replies have no mission; a mission report counts only what
	// the mission recorded.
	withSends := f.MissionID == ""

	// Sends, by sequence, mailbox and domain, so infrastructure costs can
	// follow the mail that used it.
	sends := map[sendKey]int{}
	if withSends {
		rs, err := q.Query(ctx, `
			SELECT m.sequence_id::text, m.mailbox_id::text, lower(split_part(m.from_email, '@', 2)), count(*)
			FROM messages m
			WHERE m.workspace_id = $1 AND m.status = 'sent' AND m.sent_at >= $2 AND m.sent_at < $3
			  AND ($4 = '' OR m.sequence_id::text = $4)
			GROUP BY 1, 2, 3`, ws, f.From, f.To, f.SequenceID)
		if err != nil {
			return rep, err
		}
		for rs.Next() {
			var seq, mailbox *string
			var domain string
			var n int
			if err := rs.Scan(&seq, &mailbox, &domain, &n); err != nil {
				rs.Close()
				return rep, err
			}
			if !keep(brands.of(seq)) {
				continue
			}
			sk := sendKey{seq: deref(seq), mailbox: deref(mailbox), domain: domain}
			sends[sk] += n
			get(key(seq, nil)).Sent += n
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return rep, err
		}

		rs, err = q.Query(ctx, `
			SELECT i.sequence_id::text, count(*)
			FROM inbound_messages i
			WHERE i.workspace_id = $1 AND i.kind = 'reply' AND i.received_at >= $2 AND i.received_at < $3
			  AND ($4 = '' OR i.sequence_id::text = $4)
			GROUP BY 1`, ws, f.From, f.To, f.SequenceID)
		if err != nil {
			return rep, err
		}
		for rs.Next() {
			var seq *string
			var n int
			if err := rs.Scan(&seq, &n); err != nil {
				rs.Close()
				return rep, err
			}
			if !keep(brands.of(seq)) {
				continue
			}
			get(key(seq, nil)).Replies += n
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return rep, err
		}
	}

	// Meetings booked in the period.
	rs, err := q.Query(ctx, `
		SELECT sequence_id::text, attribution, status, COALESCE(qualified, false), count(*)
		FROM meetings
		WHERE workspace_id = $1 AND booked_at >= $2 AND booked_at < $3 AND status <> 'canceled'
		  AND ($4 = '' OR sequence_id::text = $4) AND ($5 = '' OR mission_id::text = $5)
		GROUP BY 1, 2, 3, 4`, ws, f.From, f.To, f.SequenceID, f.MissionID)
	if err != nil {
		return rep, err
	}
	for rs.Next() {
		var seq *string
		var attribution, status string
		var qualified bool
		var n int
		if err := rs.Scan(&seq, &attribution, &status, &qualified, &n); err != nil {
			rs.Close()
			return rep, err
		}
		if attribution == AttrNone {
			if f.Brand == "" && f.BrandsIn == nil {
				rep.UnattributedMeetings += n
			}
			continue
		}
		if !keep(brands.of(seq)) {
			continue
		}
		a := get(key(seq, nil))
		a.Meetings += n
		switch status {
		case MeetingHeld:
			a.Held += n
		case MeetingNoShow:
			a.NoShows += n
		}
		if qualified {
			a.Qualified += n
		}
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return rep, err
	}

	// Links that went live in the period, by the sequence that pitched
	// them.
	rs, err = q.Query(ctx, `
		SELECT k.sequence_id::text, count(DISTINCT x.id)
		FROM link_prospects x JOIN link_pitches k ON k.prospect_id = x.id AND k.status = 'enrolled'
		WHERE x.workspace_id = $1 AND x.stage = 'live' AND x.live_at >= $2 AND x.live_at < $3 AND $5 = ''
		  AND ($4 = '' OR k.sequence_id::text = $4)
		GROUP BY 1`, ws, f.From, f.To, f.SequenceID, f.MissionID)
	if err != nil {
		return rep, err
	}
	for rs.Next() {
		var seq *string
		var n int
		if err := rs.Scan(&seq, &n); err != nil {
			rs.Close()
			return rep, err
		}
		if keep(brands.of(seq)) {
			get(key(seq, nil)).LiveLinks += n
		}
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return rep, err
	}

	// Deals opened in the period.
	rs, err = q.Query(ctx, `
		SELECT sequence_id::text, state, currency, count(*), COALESCE(sum(amount_cents), 0)
		FROM deals
		WHERE workspace_id = $1 AND opened_at >= $2 AND opened_at < $3 AND attribution <> 'none'
		  AND ($4 = '' OR sequence_id::text = $4) AND ($5 = '' OR mission_id::text = $5)
		GROUP BY 1, 2, 3`, ws, f.From, f.To, f.SequenceID, f.MissionID)
	if err != nil {
		return rep, err
	}
	for rs.Next() {
		var seq *string
		var state, currency string
		var n int
		var amount int64
		if err := rs.Scan(&seq, &state, &currency, &n, &amount); err != nil {
			rs.Close()
			return rep, err
		}
		if !keep(brands.of(seq)) {
			continue
		}
		a := get(key(seq, nil))
		a.Deals += n
		if state == DealWon {
			a.Won += n
		}
		if currency != f.Currency {
			if amount > 0 {
				rep.OtherCurrencies[currency] += amount
			}
			continue
		}
		if state != DealLost {
			a.PipelineCents += amount
		}
		if state == DealWon {
			a.WonCents += amount
		}
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return rep, err
	}

	// Spend: one-off entries, then recurring costs spread over the days of
	// the period they were active.
	type charge struct {
		seq, brand, mailbox, domain *string
		cents                       float64
		estimated, recurring        bool
		category                    string
	}
	var charges []charge
	unpriced := map[[2]string]float64{}
	rs, err = q.Query(ctx, `
		SELECT e.sequence_id::text, e.brand_id::text, e.mailbox_id::text, NULLIF(e.domain, ''),
			e.category, e.amount_cents, e.units::float8, e.currency, lower(e.provider), e.unit_name, r.unit_price_micros, r.currency
		FROM spend_entries e
		LEFT JOIN spend_rates r ON r.workspace_id = e.workspace_id AND r.provider = lower(e.provider) AND r.unit_name = e.unit_name
		WHERE e.workspace_id = $1 AND e.occurred_at >= $2 AND e.occurred_at < $3
		  AND ($4 = '' OR e.sequence_id::text = $4)
		  AND ($5 = '' OR e.mission_id::text = $5)`, ws, f.From, f.To, f.SequenceID, f.MissionID)
	if err != nil {
		return rep, err
	}
	for rs.Next() {
		var c charge
		var amount *int64
		var units *float64
		var currency, provider, unit string
		var micros *int64
		var rateCurrency *string
		if err := rs.Scan(&c.seq, &c.brand, &c.mailbox, &c.domain, &c.category, &amount, &units, &currency, &provider, &unit,
			&micros, &rateCurrency); err != nil {
			rs.Close()
			return rep, err
		}
		switch {
		case amount != nil && currency == f.Currency:
			c.cents = float64(*amount)
		case amount != nil:
			rep.OtherCurrencies[currency] += *amount
			continue
		case units != nil && micros != nil && deref(rateCurrency) == f.Currency:
			c.cents, c.estimated = *units*float64(*micros)/10000, true
		case units != nil && micros != nil:
			rep.OtherCurrencies[deref(rateCurrency)] += int64(math.Round(*units * float64(*micros) / 10000))
			continue
		default:
			unpriced[[2]string{provider, unit}] += deref64(units)
			continue
		}
		if !keep(brands.ofCharge(c.seq, c.brand, c.mailbox, c.domain)) {
			continue
		}
		charges = append(charges, c)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return rep, err
	}
	if f.MissionID == "" {
		rs, err = q.Query(ctx, `
			SELECT r.sequence_id::text, r.brand_id::text, r.category, r.amount_cents, r.currency, r.every,
				r.starts_on, r.ends_on
			FROM spend_recurring r
			WHERE r.workspace_id = $1 AND r.starts_on < $3::timestamptz::date + 1 AND (r.ends_on IS NULL OR r.ends_on >= $2::timestamptz::date)
			  AND ($4 = '' OR r.sequence_id::text = $4)`,
			ws, f.From, f.To, f.SequenceID)
		if err != nil {
			return rep, err
		}
		for rs.Next() {
			var c charge
			var r Recurring
			var ends *time.Time
			if err := rs.Scan(&c.seq, &c.brand, &c.category, &r.AmountCents, &r.Currency, &r.Every, &r.StartsOn.Time, &ends); err != nil {
				rs.Close()
				return rep, err
			}
			days := activeDays(r.StartsOn.Time, ends, f.From, f.To)
			if days <= 0 {
				continue
			}
			if r.Currency != f.Currency {
				rep.OtherCurrencies[r.Currency] += int64(math.Round(r.Daily() * days))
				continue
			}
			if !keep(brands.ofCharge(c.seq, c.brand, nil, nil)) {
				continue
			}
			c.cents, c.recurring = r.Daily()*days, true
			charges = append(charges, c)
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return rep, err
		}
	}

	// Send weights for splitting: by group, and per mailbox and domain.
	total := map[string]float64{}
	byMailbox := map[string]map[string]float64{}
	byDomain := map[string]map[string]float64{}
	for sk, n := range sends {
		k := key(ptr(sk.seq), nil)
		total[k] += float64(n)
		if sk.mailbox != "" {
			if byMailbox[sk.mailbox] == nil {
				byMailbox[sk.mailbox] = map[string]float64{}
			}
			byMailbox[sk.mailbox][k] += float64(n)
		}
		if byDomain[sk.domain] == nil {
			byDomain[sk.domain] = map[string]float64{}
		}
		byDomain[sk.domain][k] += float64(n)
	}
	add := func(k string, cents float64, c charge, shared bool) {
		a := get(k)
		if shared {
			a.shared += cents
		} else {
			a.direct += cents
		}
		if c.estimated {
			a.estimated += cents
		}
		if c.recurring {
			a.recurring += cents
		}
		a.cat[c.category] += cents
	}
	split := func(weights map[string]float64, c charge, shared bool) bool {
		sum := 0.0
		for k, w := range weights {
			if k != "" {
				sum += w
			}
		}
		if sum == 0 {
			return false
		}
		for k, w := range weights {
			if k != "" {
				add(k, c.cents*w/sum, c, shared)
			}
		}
		return true
	}
	for _, c := range charges {
		if k := key(c.seq, c.brand); k != "" {
			add(k, c.cents, c, false)
			continue
		}
		if c.mailbox != nil && split(byMailbox[*c.mailbox], c, false) {
			continue
		}
		if c.domain != nil && split(byDomain[*c.domain], c, false) {
			continue
		}
		if split(total, c, true) {
			continue
		}
		add("", c.cents, c, true)
	}

	// Names.
	if f.GroupBy != GroupNone {
		ids := []string{}
		for k := range rows {
			if k != "" {
				ids = append(ids, k)
			}
		}
		table := "sequences"
		switch f.GroupBy {
		case GroupBrand:
			table = "brands"
		}
		if len(ids) > 0 {
			rs, err := q.Query(ctx, `SELECT id::text, name FROM `+table+` WHERE workspace_id = $1 AND id::text = ANY($2)`, ws, ids)
			if err != nil {
				return rep, err
			}
			for rs.Next() {
				var id, name string
				if err := rs.Scan(&id, &name); err != nil {
					rs.Close()
					return rep, err
				}
				rows[id].Name = name
			}
			rs.Close()
			if err := rs.Err(); err != nil {
				return rep, err
			}
		}
		if a, ok := rows[""]; ok {
			a.Name = "Not in a " + f.GroupBy
			if f.GroupBy == GroupBrand {
				a.Name = "Shared"
			}
		}
	} else {
		rows["all"].Name = "Workspace"
	}

	tot := &acc{Row: Row{Key: "", Name: "Total"}, cat: map[string]float64{}}
	for _, a := range rows {
		tot.Sent += a.Sent
		tot.Replies += a.Replies
		tot.Meetings += a.Meetings
		tot.Held += a.Held
		tot.NoShows += a.NoShows
		tot.Qualified += a.Qualified
		tot.Deals += a.Deals
		tot.Won += a.Won
		tot.LiveLinks += a.LiveLinks
		tot.PipelineCents += a.PipelineCents
		tot.WonCents += a.WonCents
		tot.direct += a.direct
		tot.shared += a.shared
		tot.estimated += a.estimated
		tot.recurring += a.recurring
		for c, v := range a.cat {
			tot.cat[c] += v
		}
		a.finish()
		if f.GroupBy != GroupNone {
			rep.Rows = append(rep.Rows, a.Row)
		}
	}
	tot.finish()
	rep.Totals = tot.Row
	if f.GroupBy == GroupNone {
		rep.Rows = append(rep.Rows, rows["all"].Row)
		rep.Totals.Key, rep.Totals.Name = "all", "Workspace"
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		if (a.Key == "") != (b.Key == "") {
			return b.Key == ""
		}
		if a.Meetings != b.Meetings {
			return a.Meetings > b.Meetings
		}
		if a.Spend.TotalCents != b.Spend.TotalCents {
			return a.Spend.TotalCents > b.Spend.TotalCents
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	for k, units := range unpriced {
		rep.Unpriced = append(rep.Unpriced, Unpriced{Provider: k[0], UnitName: k[1], Units: units})
	}
	sort.Slice(rep.Unpriced, func(i, j int) bool { return rep.Unpriced[i].Provider < rep.Unpriced[j].Provider })

	if len(rep.Unpriced) > 0 {
		rep.Notes = append(rep.Notes, "Some usage has no price yet, so spend is understated. Set a rate for each provider and unit listed under unpriced.")
	}
	if len(rep.OtherCurrencies) > 0 {
		rep.Notes = append(rep.Notes, "Money in other currencies is left out, not converted. Run the report in that currency to see it.")
	}
	if rep.Totals.Spend.EstimatedCents > 0 {
		rep.Notes = append(rep.Notes, "Part of the spend is estimated from units and the workspace's rates.")
	}
	if rep.Totals.Spend.SharedCents > 0 && f.GroupBy != GroupNone {
		rep.Notes = append(rep.Notes, "Costs not tied to one "+f.GroupBy+" are shared out by each one's share of emails sent.")
	}
	if rep.UnattributedMeetings > 0 {
		rep.Notes = append(rep.Notes, "Some meetings could not be tied to a lead or campaign and are not counted. Attribute them on the meeting to count them.")
	}
	if f.Brand != "" || f.BrandsIn != nil {
		rep.Notes = append(rep.Notes, "Kept to brands: meetings no lead or campaign explains are not counted, and costs no brand owns are left out.")
	}
	if f.MissionID != "" {
		rep.Notes = append(rep.Notes, "A mission report counts only meetings, deals and spend recorded for the mission; sends and replies are not tracked per mission.")
	}
	return rep, nil
}

// brandMaps holds the brand of each sequence, mailbox and domain that has
// one.
type brandMaps struct{ seq, mailbox, domain map[string]string }

func loadBrandMaps(ctx context.Context, q Querier, ws string) (brandMaps, error) {
	m := brandMaps{}
	load := func(sql string) (map[string]string, error) {
		rs, err := q.Query(ctx, sql, ws)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		out := map[string]string{}
		for rs.Next() {
			var k, v string
			if err := rs.Scan(&k, &v); err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, rs.Err()
	}
	var err error
	if m.seq, err = load(`SELECT id::text, brand_id::text FROM sequences WHERE workspace_id = $1 AND brand_id IS NOT NULL`); err != nil {
		return m, err
	}
	if m.mailbox, err = load(`SELECT id::text, brand_id::text FROM mailboxes WHERE workspace_id = $1 AND brand_id IS NOT NULL`); err != nil {
		return m, err
	}
	m.domain, err = load(`SELECT lower(domain), brand_id::text FROM sending_domains WHERE workspace_id = $1 AND brand_id IS NOT NULL`)
	return m, err
}

// of is the brand of a sequence; "" for shared or none.
func (m brandMaps) of(seq *string) string {
	if seq != nil {
		return m.seq[*seq]
	}
	return ""
}

// ofCharge is the brand of spend: its sequence's, else its own, else its
// mailbox's or domain's.
func (m brandMaps) ofCharge(seq, brand, mailbox, domain *string) string {
	if seq != nil {
		return m.of(seq)
	}
	if brand != nil {
		return *brand
	}
	if mailbox != nil {
		return m.mailbox[*mailbox]
	}
	if domain != nil {
		return m.domain[strings.ToLower(*domain)]
	}
	return ""
}

func (a *acc) finish() {
	a.Spend = Spend{
		TotalCents:     int64(math.Round(a.direct + a.shared)),
		DirectCents:    int64(math.Round(a.direct)),
		SharedCents:    int64(math.Round(a.shared)),
		EstimatedCents: int64(math.Round(a.estimated)),
		RecurringCents: int64(math.Round(a.recurring)),
		ByCategory:     map[string]int64{},
	}
	for c, v := range a.cat {
		if r := int64(math.Round(v)); r != 0 {
			a.Spend.ByCategory[c] = r
		}
	}
	if a.Sent > 0 {
		r := float64(a.Replies) / float64(a.Sent)
		a.ReplyRate = &r
	}
	spend := a.direct + a.shared
	per := func(n int) *int64 {
		if n == 0 || spend == 0 {
			return nil
		}
		v := int64(math.Round(spend / float64(n)))
		return &v
	}
	a.CostPerReply, a.CostPerMeeting, a.CostPerHeld, a.CostPerDeal, a.CostPerWon, a.CostPerLink =
		per(a.Replies), per(a.Meetings), per(a.Held), per(a.Deals), per(a.Won), per(a.LiveLinks)
	if spend > 0 && a.WonCents > 0 {
		r := float64(a.WonCents) / spend
		a.ROI = &r
	}
}

// activeDays is how many days of [from, to) a recurring cost was active,
// counting part days at the edges of the period.
func activeDays(starts time.Time, ends *time.Time, from, to time.Time) float64 {
	start := time.Date(starts.Year(), starts.Month(), starts.Day(), 0, 0, 0, 0, time.UTC)
	if from.After(start) {
		start = from
	}
	end := to
	if ends != nil {
		e := time.Date(ends.Year(), ends.Month(), ends.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
		if e.Before(end) {
			end = e
		}
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start).Hours() / 24
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func deref64(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
