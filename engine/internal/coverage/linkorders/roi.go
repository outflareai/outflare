package linkorders

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// ROIWindowDays are the look-around periods, in days, used for traffic,
// rank and referring-domain changes on a published order's target page.
var ROIWindowDays = []int{30, 60, 90}

// ROINote is returned on every payload. The figures do not claim that a
// link caused a change, and nothing here calls a paid provider.
const ROINote = "Cost is the donor price on each published link. Traffic, rank and referring-domain figures are already-stored Search Console, ranking and backlink rows around publication. They do not show that the link caused a change, and no SEO provider is called."

const maxROIOrders = 1000

// ROI is cost per published link against stored changes on the target page.
type ROI struct {
	Causality bool       `json:"causality"`
	Note      string     `json:"note"`
	Windows   []int      `json:"windows"`
	Totals    ROITotals  `json:"totals"`
	Orders    []ROIOrder `json:"orders"`
	Pages     []ROIPage  `json:"pages"`
}

// ROITotals is brand-wide counts and spend. USD and EUR are never summed.
type ROITotals struct {
	Orders      int             `json:"orders"`
	Published   int             `json:"published"`
	Paid        int             `json:"paid"`
	Verified    int             `json:"verified"`
	Requested   int             `json:"open"`
	Lost        int             `json:"lost"`
	Spend       []CurrencyTotal `json:"spend"`
	CostPerLink []CurrencyTotal `json:"cost_per_link"`
	// PublishedWithCost is how many published rows have a donor or quoted amount.
	PublishedWithCost int `json:"published_with_cost"`
	// WithTraffic is how many published rows have Search Console rows in a window.
	WithTraffic int `json:"with_traffic"`
	// WithRank is how many published rows have a stored ranking snapshot in a window.
	WithRank int `json:"with_rank"`
	// WithReferringDomains is how many published rows have stored backlinks on the target page.
	WithReferringDomains int `json:"with_referring_domains"`
}

// ROIOrder is one published, paid or verified order and its stored changes.
type ROIOrder struct {
	ID                uuid.UUID   `json:"id"`
	Status            string      `json:"status"`
	Domain            string      `json:"domain"`
	TargetURL         string      `json:"target_url"`
	PublishedURL      string      `json:"published_url"`
	PublishedAt       *time.Time  `json:"published_at"`
	AmountMinor       *int64      `json:"amount_minor"`
	Currency          *string     `json:"currency"`
	DonorDomainRating *int        `json:"donor_domain_rating"`
	DonorTraffic      *int64      `json:"donor_traffic"`
	Windows           []ROIWindow `json:"windows"`
}

// ROIPage rolls published orders up by target page. Amounts stay per currency.
type ROIPage struct {
	TargetURL        string          `json:"target_url"`
	Links            int             `json:"links"`
	Spend            []CurrencyTotal `json:"spend"`
	Domains          []string        `json:"domains"`
	Windows          []ROIWindow     `json:"windows"`
	FirstPublishedAt *time.Time      `json:"first_published_at"`
}

// ROIWindow is one 30-, 60- or 90-day look around publication.
type ROIWindow struct {
	Days                         int         `json:"days"`
	Elapsed                      bool        `json:"elapsed"`
	Traffic                      *ROITraffic `json:"traffic"`
	Rank                         *ROIRank    `json:"rank"`
	ReferringDomains             *ROIRefs    `json:"referring_domains"`
	CostPerClickGained           *float64    `json:"cost_per_click_gained"`
	CostPerReferringDomainGained *float64    `json:"cost_per_referring_domain_gained"`
}

// ROITraffic is stored Search Console totals before and after publication.
type ROITraffic struct {
	Available         bool     `json:"available"`
	BaselineFrom      string   `json:"baseline_from,omitempty"`
	BaselineTo        string   `json:"baseline_to,omitempty"`
	AfterFrom         string   `json:"after_from,omitempty"`
	AfterTo           string   `json:"after_to,omitempty"`
	ClicksBefore      int64    `json:"clicks_before"`
	ClicksAfter       int64    `json:"clicks_after"`
	ClicksChange      int64    `json:"clicks_change"`
	ImpressionsBefore int64    `json:"impressions_before"`
	ImpressionsAfter  int64    `json:"impressions_after"`
	ImpressionsChange int64    `json:"impressions_change"`
	PositionBefore    *float64 `json:"position_before"`
	PositionAfter     *float64 `json:"position_after"`
	PositionChange    *float64 `json:"position_change"`
}

// ROIRank is the impression-weighted average stored keyword position.
type ROIRank struct {
	Available      bool     `json:"available"`
	Keywords       int      `json:"keywords"`
	PositionBefore *float64 `json:"position_before"`
	PositionAfter  *float64 `json:"position_after"`
	PositionChange *float64 `json:"position_change"`
}

// ROIRefs is distinct referring domains stored against the target page.
type ROIRefs struct {
	Available bool  `json:"available"`
	Before    int64 `json:"before"`
	After     int64 `json:"after"`
	Change    int64 `json:"change"`
}

type roiOrderRow struct {
	ID                uuid.UUID
	Status            string
	Domain            string
	TargetURL         string
	PublishedURL      string
	PublishedAt       *time.Time
	DonorAmountMinor  *int64
	Currency          *string
	QuotedAmountMinor *int64
	QuotedCurrency    *string
	DonorDomainRating *int
	DonorTraffic      *int64
}

// LoadROI reads published link orders and already-saved Search Console,
// ranking and backlink rows around each publication date. It does not
// call a provider.
func LoadROI(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, now time.Time) (*ROI, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	out := &ROI{
		Causality: false,
		Note:      ROINote,
		Windows:   append([]int(nil), ROIWindowDays...),
		Orders:    []ROIOrder{},
		Pages:     []ROIPage{},
	}

	countRows, err := q.Query(ctx, `
		SELECT status, count(*)
		FROM coverage_link_orders
		WHERE workspace_id = $1 AND brand_id = $2
		GROUP BY status`, workspaceID, brandID)
	if err != nil {
		return nil, err
	}
	counts, err := pgx.CollectRows(countRows, func(row pgx.CollectableRow) (struct {
		Status string
		N      int
	}, error) {
		var v struct {
			Status string
			N      int
		}
		err := row.Scan(&v.Status, &v.N)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	for _, c := range counts {
		out.Totals.Orders += c.N
		switch c.Status {
		case StatusPublished:
			out.Totals.Published += c.N
		case StatusPaid:
			out.Totals.Paid += c.N
			out.Totals.Published += c.N
		case StatusVerified:
			out.Totals.Verified += c.N
			out.Totals.Published += c.N
		case StatusLost:
			out.Totals.Lost += c.N
		case StatusRequested, StatusQuoted, StatusNegotiating, StatusApproved:
			out.Totals.Requested += c.N
		}
	}

	rows, err := q.Query(ctx, `
		SELECT o.id, o.status, pub.domain, o.target_url, COALESCE(o.published_url, ''),
		       o.published_at, o.donor_amount_minor, o.currency,
		       o.quoted_amount_minor, o.quoted_currency, p.domain_rating, p.traffic
		FROM coverage_link_orders o
		JOIN coverage_donor_profiles p ON p.id = o.donor_profile_id
		JOIN coverage_publishers pub ON pub.id = p.publisher_id
		WHERE o.workspace_id = $1 AND o.brand_id = $2
		  AND o.published_at IS NOT NULL
		  AND o.status IN ('published', 'paid', 'verified')
		ORDER BY o.published_at DESC, o.id DESC
		LIMIT $3`, workspaceID, brandID, maxROIOrders)
	if err != nil {
		return nil, err
	}
	orderRows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (roiOrderRow, error) {
		var v roiOrderRow
		err := row.Scan(&v.ID, &v.Status, &v.Domain, &v.TargetURL, &v.PublishedURL,
			&v.PublishedAt, &v.DonorAmountMinor, &v.Currency, &v.QuotedAmountMinor, &v.QuotedCurrency,
			&v.DonorDomainRating, &v.DonorTraffic)
		return v, err
	})
	if err != nil {
		return nil, err
	}

	pagesByOrder := make([][]string, len(orderRows))
	var allPages []string
	seenPage := map[string]bool{}
	var minDay, maxDay time.Time
	for i, o := range orderRows {
		vars := pageVariants(o.TargetURL)
		pagesByOrder[i] = vars
		for _, p := range vars {
			if !seenPage[p] {
				seenPage[p] = true
				allPages = append(allPages, p)
			}
		}
		if o.PublishedAt == nil {
			continue
		}
		from := o.PublishedAt.UTC().AddDate(0, 0, -90)
		to := o.PublishedAt.UTC().AddDate(0, 0, 90)
		if minDay.IsZero() || from.Before(minDay) {
			minDay = from
		}
		if maxDay.IsZero() || to.After(maxDay) {
			maxDay = to
		}
	}

	gsc := map[string][]ImpactDayPoint{}
	ranks := map[string][]ImpactRankPoint{}
	refs := map[string][]rdEvent{}
	if len(allPages) > 0 && !minDay.IsZero() {
		gsc, err = loadROIConsole(ctx, q, workspaceID, allPages, minDay, maxDay)
		if err != nil {
			return nil, err
		}
		ranks, err = loadROIRanks(ctx, q, workspaceID, allPages, minDay, maxDay)
		if err != nil {
			return nil, err
		}
		refs, err = loadROIRefs(ctx, q, workspaceID, brandID, allPages)
		if err != nil {
			return nil, err
		}
	}

	spend := map[string]int64{}
	var withCost, withTraffic, withRank, withRD int
	pageAcc := map[string]*pageAccum{}
	for i, o := range orderRows {
		amount, currency := orderCost(o)
		row := ROIOrder{
			ID:                o.ID,
			Status:            o.Status,
			Domain:            o.Domain,
			TargetURL:         o.TargetURL,
			PublishedURL:      o.PublishedURL,
			PublishedAt:       o.PublishedAt,
			AmountMinor:       amount,
			Currency:          currency,
			DonorDomainRating: o.DonorDomainRating,
			DonorTraffic:      o.DonorTraffic,
			Windows:           []ROIWindow{},
		}
		if amount != nil && currency != nil {
			spend[*currency] += *amount
			withCost++
		}
		canon := canonicalTarget(o.TargetURL)
		acc := pageAcc[canon]
		if acc == nil {
			acc = &pageAccum{target: canon, domains: map[string]bool{}, spend: map[string]int64{}}
			pageAcc[canon] = acc
		}
		acc.links++
		acc.domains[o.Domain] = true
		if amount != nil && currency != nil {
			acc.spend[*currency] += *amount
		}
		if o.PublishedAt != nil && (acc.first == nil || o.PublishedAt.Before(*acc.first)) {
			t := o.PublishedAt.UTC()
			acc.first = &t
		}

		var sawTraffic, sawRank, sawRD bool
		for _, days := range ROIWindowDays {
			w := buildROIWindow(now, o.PublishedAt, days, pagesByOrder[i], gsc, ranks, refs, amount)
			row.Windows = append(row.Windows, w)
			if w.Traffic != nil && w.Traffic.Available {
				sawTraffic = true
			}
			if w.Rank != nil && w.Rank.Available {
				sawRank = true
			}
			if w.ReferringDomains != nil && w.ReferringDomains.Available {
				sawRD = true
			}
		}
		if sawTraffic {
			withTraffic++
		}
		if sawRank {
			withRank++
		}
		if sawRD {
			withRD++
		}
		out.Orders = append(out.Orders, row)
		acc.orders = append(acc.orders, row)
	}
	out.Totals.PublishedWithCost = withCost
	out.Totals.WithTraffic = withTraffic
	out.Totals.WithRank = withRank
	out.Totals.WithReferringDomains = withRD
	out.Totals.Spend = currencyTotals(spend)
	if withCost > 0 {
		var per []CurrencyTotal
		for _, s := range out.Totals.Spend {
			per = append(per, CurrencyTotal{Currency: s.Currency, AmountMinor: s.AmountMinor / int64(withCost)})
		}
		out.Totals.CostPerLink = per
	} else {
		out.Totals.CostPerLink = []CurrencyTotal{}
	}
	if out.Totals.Spend == nil {
		out.Totals.Spend = []CurrencyTotal{}
	}

	var pages []ROIPage
	for _, acc := range pageAcc {
		if acc.spend == nil {
			acc.spend = map[string]int64{}
		}
		domains := []string{}
		for d := range acc.domains {
			domains = append(domains, d)
		}
		sort.Strings(domains)
		p := ROIPage{
			TargetURL:        acc.target,
			Links:            acc.links,
			Spend:            currencyTotals(acc.spend),
			Domains:          domains,
			FirstPublishedAt: acc.first,
			Windows:          rollupPageWindows(now, acc),
		}
		pages = append(pages, p)
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].Links != pages[j].Links {
			return pages[i].Links > pages[j].Links
		}
		return pages[i].TargetURL < pages[j].TargetURL
	})
	if pages == nil {
		pages = []ROIPage{}
	}
	out.Pages = pages
	return out, nil
}

type pageAccum struct {
	target  string
	links   int
	spend   map[string]int64
	domains map[string]bool
	first   *time.Time
	orders  []ROIOrder
}

type rdEvent struct {
	Domain string
	First  time.Time
	Lost   *time.Time
	Pages  []string
}

func orderCost(o roiOrderRow) (*int64, *string) {
	if o.DonorAmountMinor != nil && o.Currency != nil && *o.Currency != "" {
		return o.DonorAmountMinor, o.Currency
	}
	if o.QuotedAmountMinor != nil && o.QuotedCurrency != nil && *o.QuotedCurrency != "" {
		return o.QuotedAmountMinor, o.QuotedCurrency
	}
	return nil, nil
}

func canonicalTarget(raw string) string {
	vars := pageVariants(raw)
	if len(vars) == 0 {
		return strings.TrimSpace(raw)
	}
	sort.Strings(vars)
	// Prefer the canonical form when coverage.Canonical succeeded: it is
	// usually the shortest https host/path without a trailing slash, but
	// pageVariants already includes the raw URL. Use the raw trimmed URL
	// when it is in the set, else the first variant.
	raw = strings.TrimSpace(raw)
	for _, v := range vars {
		if v == raw {
			return v
		}
	}
	return vars[0]
}

func currencyTotals(m map[string]int64) []CurrencyTotal {
	if len(m) == 0 {
		return []CurrencyTotal{}
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CurrencyTotal, 0, len(keys))
	for _, k := range keys {
		out = append(out, CurrencyTotal{Currency: k, AmountMinor: m[k]})
	}
	return out
}

func loadROIConsole(ctx context.Context, q Querier, workspaceID uuid.UUID, pages []string, from, to time.Time) (map[string][]ImpactDayPoint, error) {
	rows, err := q.Query(ctx, `
		SELECT day::text,
		       page,
		       coalesce(sum(clicks), 0),
		       coalesce(sum(impressions), 0),
		       CASE WHEN coalesce(sum(impressions), 0) > 0
		            THEN sum(position * impressions) / sum(impressions)
		            ELSE avg(position) END
		FROM search_daily
		WHERE workspace_id = $1
		  AND day BETWEEN $2::date AND $3::date
		  AND page = ANY($4)
		GROUP BY day, page
		ORDER BY day`, workspaceID, from, to, pages)
	if err != nil {
		return nil, err
	}
	type row struct {
		Page string
		ImpactDayPoint
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		err := r.Scan(&v.Day, &v.Page, &v.Clicks, &v.Impressions, &v.Position)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	out := map[string][]ImpactDayPoint{}
	for _, r := range list {
		out[r.Page] = append(out[r.Page], r.ImpactDayPoint)
	}
	return out, nil
}

func loadROIRanks(ctx context.Context, q Querier, workspaceID uuid.UUID, pages []string, from, to time.Time) (map[string][]ImpactRankPoint, error) {
	rankRows, err := q.Query(ctx, `
		SELECT day::text, key, url, metrics
		FROM seo_snapshots
		WHERE workspace_id = $1 AND report = $2
		  AND day BETWEEN $3::date AND $4::date
		  AND key <> '' AND url = ANY($5)
		ORDER BY day, key, connector`,
		workspaceID, connectors.SEOKeywords, from, to, pages)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rankRows, func(row pgx.CollectableRow) (ImpactRankPoint, error) {
		var v ImpactRankPoint
		var raw []byte
		err := row.Scan(&v.Day, &v.Keyword, &v.URL, &raw)
		var m connectors.SEOMetrics
		_ = json.Unmarshal(raw, &m)
		v.Position = m.Position
		return v, err
	})
	if err != nil {
		return nil, err
	}
	out := map[string][]ImpactRankPoint{}
	for _, r := range list {
		out[r.URL] = append(out[r.URL], r)
	}
	return out, nil
}

func loadROIRefs(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, pages []string) (map[string][]rdEvent, error) {
	rows, err := q.Query(ctx, `
		SELECT source_domain, target_url, first_seen_at, lost_at
		FROM backlinks
		WHERE workspace_id = $1 AND brand_id = $2
		  AND target_url = ANY($3)
		  AND first_seen_at IS NOT NULL`, workspaceID, brandID, pages)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (rdEvent, error) {
		var v rdEvent
		var target string
		err := row.Scan(&v.Domain, &target, &v.First, &v.Lost)
		v.Pages = []string{target}
		return v, err
	})
	if err != nil {
		return nil, err
	}
	out := map[string][]rdEvent{}
	for _, r := range list {
		for _, p := range r.Pages {
			out[p] = append(out[p], r)
		}
	}
	return out, nil
}

func buildROIWindow(now time.Time, publishedAt *time.Time, days int, pages []string, gsc map[string][]ImpactDayPoint, ranks map[string][]ImpactRankPoint, refs map[string][]rdEvent, amount *int64) ROIWindow {
	w := ROIWindow{Days: days}
	if publishedAt == nil {
		return w
	}
	pub := publishedAt.UTC()
	w.Elapsed = !now.Before(pub.AddDate(0, 0, days))
	from := pub.AddDate(0, 0, -days)
	to := pub.AddDate(0, 0, days)
	pubDay := pub.Format("2006-01-02")
	fromDay := from.Format("2006-01-02")
	toDay := to.Format("2006-01-02")

	var series []ImpactDayPoint
	byDay := map[string]ImpactDayPoint{}
	for _, p := range pages {
		for _, pt := range gsc[p] {
			if pt.Day < fromDay || pt.Day > toDay {
				continue
			}
			byDay[pt.Day] = mergeDay(byDay[pt.Day], pt)
		}
	}
	for _, pt := range byDay {
		series = append(series, pt)
	}
	sort.Slice(series, func(i, j int) bool { return series[i].Day < series[j].Day })
	var before, after []ImpactDayPoint
	for _, p := range series {
		if p.Day < pubDay {
			before = append(before, p)
		} else {
			after = append(after, p)
		}
	}
	tr := &ROITraffic{}
	if b := sumPoints(before); b != nil {
		tr.Available = true
		tr.BaselineFrom, tr.BaselineTo = b.From, b.To
		tr.ClicksBefore, tr.ImpressionsBefore = b.Clicks, b.Impressions
		tr.PositionBefore = b.Position
	}
	if a := sumPoints(after); a != nil {
		tr.Available = true
		tr.AfterFrom, tr.AfterTo = a.From, a.To
		tr.ClicksAfter, tr.ImpressionsAfter = a.Clicks, a.Impressions
		tr.PositionAfter = a.Position
	}
	if tr.Available {
		tr.ClicksChange = tr.ClicksAfter - tr.ClicksBefore
		tr.ImpressionsChange = tr.ImpressionsAfter - tr.ImpressionsBefore
		tr.PositionChange = diffPtr(tr.PositionAfter, tr.PositionBefore)
		w.Traffic = tr
		if amount != nil && tr.ClicksChange > 0 {
			v := float64(*amount) / float64(tr.ClicksChange)
			w.CostPerClickGained = &v
		}
	}

	var rb, ra []ImpactRankPoint
	seenKw := map[string]bool{}
	for _, p := range pages {
		for _, pt := range ranks[p] {
			if pt.Day < fromDay || pt.Day > toDay {
				continue
			}
			seenKw[pt.Keyword] = true
			if pt.Day < pubDay {
				rb = append(rb, pt)
			} else {
				ra = append(ra, pt)
			}
		}
	}
	rk := &ROIRank{Keywords: len(seenKw)}
	rk.PositionBefore = avgRank(rb)
	rk.PositionAfter = avgRank(ra)
	if rk.PositionBefore != nil || rk.PositionAfter != nil {
		rk.Available = true
		rk.PositionChange = diffPtr(rk.PositionAfter, rk.PositionBefore)
		w.Rank = rk
	}

	asOfBefore := pub.Add(-time.Second)
	asOfAfter := pub.AddDate(0, 0, days)
	if asOfAfter.After(now) {
		asOfAfter = now
	}
	beforeN, okB := countRefs(pages, refs, asOfBefore)
	afterN, okA := countRefs(pages, refs, asOfAfter)
	if okB || okA {
		rd := &ROIRefs{Available: true, Before: beforeN, After: afterN, Change: afterN - beforeN}
		w.ReferringDomains = rd
		if amount != nil && rd.Change > 0 {
			v := float64(*amount) / float64(rd.Change)
			w.CostPerReferringDomainGained = &v
		}
	}
	return w
}

func mergeDay(cur, add ImpactDayPoint) ImpactDayPoint {
	var weighted, impressions float64
	addPos := func(p ImpactDayPoint) {
		if p.Position != nil && p.Impressions > 0 {
			weighted += *p.Position * float64(p.Impressions)
			impressions += float64(p.Impressions)
		}
	}
	out := ImpactDayPoint{Day: add.Day, Clicks: cur.Clicks + add.Clicks, Impressions: cur.Impressions + add.Impressions}
	// Re-weight from the two points only when both carry impressions.
	if cur.Day != "" {
		addPos(cur)
	}
	addPos(add)
	if impressions > 0 {
		pos := weighted / impressions
		out.Position = &pos
	}
	return out
}

func avgRank(points []ImpactRankPoint) *float64 {
	var sum float64
	var n int
	for _, p := range points {
		if p.Position != nil {
			sum += *p.Position
			n++
		}
	}
	if n == 0 {
		return nil
	}
	v := math.Round(sum/float64(n)*100) / 100
	return &v
}

func diffPtr(after, before *float64) *float64 {
	if after == nil || before == nil {
		return nil
	}
	v := math.Round((*after-*before)*100) / 100
	return &v
}

func countRefs(pages []string, refs map[string][]rdEvent, asOf time.Time) (int64, bool) {
	set := map[string]bool{}
	found := false
	pageSet := map[string]bool{}
	for _, p := range pages {
		pageSet[p] = true
	}
	for p, evs := range refs {
		if !pageSet[p] {
			continue
		}
		for _, e := range evs {
			found = true
			if e.First.After(asOf) {
				continue
			}
			if e.Lost != nil && !e.Lost.After(asOf) {
				continue
			}
			set[e.Domain] = true
		}
	}
	return int64(len(set)), found
}

func rollupPageWindows(now time.Time, acc *pageAccum) []ROIWindow {
	// Use the earliest publication as the page's baseline, and sum traffic
	// changes from each order's own window only when a page has one link.
	// Several links on the same page share one GSC series, so we measure
	// once around the first publication rather than adding the same clicks.
	if acc.first == nil {
		out := make([]ROIWindow, 0, len(ROIWindowDays))
		for _, d := range ROIWindowDays {
			out = append(out, ROIWindow{Days: d})
		}
		return out
	}
	if len(acc.orders) == 1 {
		return acc.orders[0].Windows
	}
	// For several links, copy the window of the earliest published order
	// (same page, same series) and drop per-order cost-per-gain: spend is
	// on the page row instead.
	var earliest *ROIOrder
	for i := range acc.orders {
		o := &acc.orders[i]
		if o.PublishedAt == nil {
			continue
		}
		if earliest == nil || o.PublishedAt.Before(*earliest.PublishedAt) {
			earliest = o
		}
	}
	if earliest == nil {
		return []ROIWindow{}
	}
	out := make([]ROIWindow, len(earliest.Windows))
	for i, w := range earliest.Windows {
		w.CostPerClickGained = nil
		w.CostPerReferringDomainGained = nil
		if w.Traffic != nil && w.Traffic.Available && w.Traffic.ClicksChange > 0 && len(acc.spend) == 1 {
			for _, amt := range acc.spend {
				v := float64(amt) / float64(w.Traffic.ClicksChange)
				w.CostPerClickGained = &v
			}
		}
		if w.ReferringDomains != nil && w.ReferringDomains.Available && w.ReferringDomains.Change > 0 && len(acc.spend) == 1 {
			for _, amt := range acc.spend {
				v := float64(amt) / float64(w.ReferringDomains.Change)
				w.CostPerReferringDomainGained = &v
			}
		}
		out[i] = w
	}
	return out
}
