package linkorders

import (
	"cmp"
	"context"
	"encoding/json"
	"math"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Target suggestions: which of the brand's own pages to point a paid link
// at, from data Outflare already stored. Nothing here calls a provider:
// it reads the weekly keyword snapshots (the site's and its tracked
// competitors'), keywords SE Ranking projects track, the Keywords page's
// page targets, and competitor link reads already kept in link_read_rows.
//
// A page scores when
//   - a tracked competitor ranks in the top gapTop for a keyword the page
//     ranks for (or targets) and the site is missing or gapBehind places
//     or more behind (keyword_gap), and
//   - sites link to the competitor's page that competes for the same
//     keywords, or whose address matches the page's (competitor_links).
// The score only orders the list. It does not create orders, and a
// person still ticks the page.

const (
	gapTop    = 20
	gapBehind = 3
	// maxReasons keeps each suggestion's evidence short.
	maxReasons = 8
	// maxAnchorOptions caps each anchor list.
	maxAnchorOptions = 10
)

// Reason kinds on a suggestion.
const (
	ReasonKeywordGap      = "keyword_gap"
	ReasonCompetitorLinks = "competitor_links"
	ReasonSaved           = "saved"
)

// TargetReason is one piece of evidence for a suggested page.
type TargetReason struct {
	Kind             string   `json:"kind"`
	Competitor       string   `json:"competitor"`
	Keyword          string   `json:"keyword"`
	Position         *float64 `json:"position"`
	YourPosition     *float64 `json:"your_position"`
	Volume           *float64 `json:"volume"`
	CompetitorURL    string   `json:"competitor_url"`
	ReferringDomains int      `json:"referring_domains"`
}

// AnchorOption is one ready-made anchor text and where it came from.
type AnchorOption struct {
	Text     string   `json:"text"`
	Source   string   `json:"source"`
	Position *float64 `json:"position"`
	Volume   *float64 `json:"volume"`
}

// AnchorOptions are the choices for a page's two anchors. Primary is
// SE Ranking first, then the page's keyword target, then other stored
// keywords. Secondary has one list per type: brand, url and keyword
// (the page's other keywords).
type AnchorOptions struct {
	Primary   []AnchorOption       `json:"primary"`
	Secondary SecondaryAnchorLists `json:"secondary"`
}

// SecondaryAnchorLists are the second-anchor choices by type.
type SecondaryAnchorLists struct {
	Brand   []AnchorOption `json:"brand"`
	URL     []AnchorOption `json:"url"`
	Keyword []AnchorOption `json:"keyword"`
}

// TargetSuggestion is one of the brand's pages, ranked by how much the
// competitor comparison says a link to it would help.
type TargetSuggestion struct {
	URL         string         `json:"url"`
	Kind        string         `json:"kind"`
	KindGuessed bool           `json:"kind_guessed"`
	TargetID    *uuid.UUID     `json:"target_id"`
	Selected    bool           `json:"selected"`
	Score       float64        `json:"score"`
	Competitors []string       `json:"competitors"`
	Reasons     []TargetReason `json:"reasons"`
	Anchors     AnchorOptions  `json:"anchors"`
}

// SuggestionSources says which stored data fed the suggestions, so the
// page can say honestly what it was based on.
type SuggestionSources struct {
	SERankingKeywords   int      `json:"se_ranking_keywords"`
	OtherKeywords       int      `json:"other_keywords"`
	PageTargets         int      `json:"page_targets"`
	Competitors         []string `json:"competitors"`
	CompetitorKeywords  int      `json:"competitor_keywords"`
	CompetitorLinkPages int      `json:"competitor_link_pages"`
}

// Suggestions is the answer of SuggestTargets.
type Suggestions struct {
	Data    []TargetSuggestion `json:"data"`
	Sources SuggestionSources  `json:"sources"`
}

type kwRow struct {
	Connector string
	Keyword   string
	URL       string
	Position  *float64
	Volume    *float64
}

type compKwRow struct {
	Competitor string
	Keyword    string
	URL        string
	Position   *float64
	Volume     *float64
}

type compLinkRow struct {
	Competitor   string
	TargetURL    string
	SourceDomain string
}

type pageTargetRow struct {
	URL       string
	Keyword   string
	Secondary []string
}

// targetInputs is everything the suggestions read, loaded once.
type targetInputs struct {
	BrandName    string
	Hosts        []string
	Saved        []Target
	PageTargets  []pageTargetRow
	OurKeywords  []kwRow // Connector "seranking" is SE Ranking
	CompKeywords []compKwRow
	CompLinks    []compLinkRow
	Competitors  []string
}

func normKeyword(k string) string { return strings.ToLower(strings.Join(strings.Fields(k), " ")) }

func normURL(u string) string {
	n, err := NormalizeTargetURL(u)
	if err != nil {
		return ""
	}
	return n
}

func better(a, b *float64) bool {
	if a == nil {
		return false
	}
	return b == nil || *a < *b
}

func volumeWeight(v *float64) float64 {
	if v == nil || *v <= 0 {
		return 1
	}
	return 1 + math.Log10(1+*v)
}

func slugTokens(u string) []string {
	p, err := url.Parse(u)
	if err != nil {
		return nil
	}
	segs := strings.Split(strings.Trim(p.Path, "/"), "/")
	last := strings.ToLower(segs[len(segs)-1])
	var out []string
	for _, t := range strings.FieldsFunc(last, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if len(t) >= 3 && t != "html" && t != "php" {
			out = append(out, t)
		}
	}
	return out
}

func slugMatch(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	shared := 0
	for _, t := range a {
		if slices.Contains(b, t) {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	return shared >= 2 || (shared >= 1 && float64(shared)/float64(union) >= 0.6)
}

type pageAcc struct {
	s       TargetSuggestion
	reasons map[string]bool
}

// buildSuggestions ranks the brand's pages. It is pure, so it is tested
// without a database.
func buildSuggestions(in targetInputs, kind string, limit int) Suggestions {
	out := Suggestions{Data: []TargetSuggestion{}, Sources: SuggestionSources{Competitors: slices.Clone(in.Competitors)}}
	if out.Sources.Competitors == nil {
		out.Sources.Competitors = []string{}
	}
	slices.Sort(out.Sources.Competitors)

	// The site's own keywords: SE Ranking's and other connections'.
	type ourKw struct {
		url      string
		position *float64
		volume   *float64
		se       bool
	}
	ours := map[string]ourKw{}         // keyword -> best page
	seByURL := map[string][]kwRow{}    // page -> SE Ranking keywords
	otherByURL := map[string][]kwRow{} // page -> other connections' keywords
	for _, k := range in.OurKeywords {
		u := normURL(k.URL)
		key := normKeyword(k.Keyword)
		if u == "" || key == "" || !onBrandHost(u, in.Hosts) {
			continue
		}
		k.Keyword = key
		se := k.Connector == "seranking"
		if se {
			seByURL[u] = append(seByURL[u], k)
			out.Sources.SERankingKeywords++
		} else {
			otherByURL[u] = append(otherByURL[u], k)
			out.Sources.OtherKeywords++
		}
		prev, ok := ours[key]
		if !ok || better(k.Position, prev.position) || (se && !prev.se && !better(prev.position, k.Position)) {
			ours[key] = ourKw{url: u, position: k.Position, volume: k.Volume, se: se}
		}
	}
	pageTarget := map[string]pageTargetRow{}
	for _, p := range in.PageTargets {
		u := normURL(p.URL)
		if u == "" || !onBrandHost(u, in.Hosts) {
			continue
		}
		out.Sources.PageTargets++
		p.URL = u
		pageTarget[u] = p
		for _, k := range append([]string{p.Keyword}, p.Secondary...) {
			key := normKeyword(k)
			if key == "" {
				continue
			}
			if _, ok := ours[key]; !ok {
				ours[key] = ourKw{url: u}
			}
		}
	}

	saved := map[string]Target{}
	for _, t := range in.Saved {
		saved[t.URL] = t
	}
	pages := map[string]*pageAcc{}
	page := func(u string) *pageAcc {
		p := pages[u]
		if p == nil {
			p = &pageAcc{reasons: map[string]bool{}, s: TargetSuggestion{URL: u, Competitors: []string{}, Reasons: []TargetReason{}}}
			pages[u] = p
		}
		return p
	}
	addReason := func(p *pageAcc, key string, r TargetReason, score float64) {
		if p.reasons[key] {
			return
		}
		p.reasons[key] = true
		p.s.Score += score
		if r.Competitor != "" && !slices.Contains(p.s.Competitors, r.Competitor) {
			p.s.Competitors = append(p.s.Competitors, r.Competitor)
		}
		p.s.Reasons = append(p.s.Reasons, r)
	}

	// Keyword gap: competitors rank well where the page is missing or behind.
	compPageKws := map[string][]compKwRow{} // competitor page -> its keywords
	for _, c := range in.CompKeywords {
		key := normKeyword(c.Keyword)
		if key == "" {
			continue
		}
		c.Keyword = key
		out.Sources.CompetitorKeywords++
		if cu := normURL(c.URL); cu != "" {
			compPageKws[cu] = append(compPageKws[cu], c)
		}
		if c.Position == nil || *c.Position > gapTop {
			continue
		}
		o, ok := ours[key]
		if !ok {
			continue // no page of ours for it: a content gap, not a link target
		}
		if o.position != nil && *o.position-*c.Position < gapBehind {
			continue // the page already ranks about as well
		}
		vol := c.Volume
		if vol == nil {
			vol = o.volume
		}
		addReason(page(o.url), ReasonKeywordGap+"|"+c.Competitor+"|"+key, TargetReason{
			Kind: ReasonKeywordGap, Competitor: c.Competitor, Keyword: key, Position: c.Position,
			YourPosition: o.position, Volume: vol, CompetitorURL: normURL(c.URL),
		}, volumeWeight(vol))
	}

	// Where competitors' links go: the referring domains per competitor page.
	type linked struct {
		competitor string
		domains    map[string]bool
	}
	links := map[string]*linked{}
	for _, l := range in.CompLinks {
		u := normURL(l.TargetURL)
		d := strings.TrimPrefix(strings.ToLower(l.SourceDomain), "www.")
		if u == "" || d == "" {
			continue
		}
		x := links[u]
		if x == nil {
			x = &linked{competitor: l.Competitor, domains: map[string]bool{}}
			links[u] = x
		}
		x.domains[d] = true
	}
	out.Sources.CompetitorLinkPages = len(links)
	candidates := map[string]bool{}
	for _, o := range ours {
		candidates[o.url] = true
	}
	for u := range saved {
		candidates[u] = true
	}
	linkURLs := make([]string, 0, len(links))
	for u := range links {
		linkURLs = append(linkURLs, u)
	}
	slices.Sort(linkURLs)
	for _, cu := range linkURLs {
		x := links[cu]
		n := len(x.domains)
		score := 2 * math.Log2(1+float64(n))
		matched := map[string]bool{}
		kws := slices.Clone(compPageKws[cu])
		slices.SortFunc(kws, func(a, b compKwRow) int { return cmp.Compare(volOf(b.Volume), volOf(a.Volume)) })
		for _, k := range kws {
			o, ok := ours[k.Keyword]
			if !ok || matched[o.url] {
				continue
			}
			matched[o.url] = true
			addReason(page(o.url), ReasonCompetitorLinks+"|"+cu, TargetReason{
				Kind: ReasonCompetitorLinks, Competitor: x.competitor, Keyword: k.Keyword, CompetitorURL: cu, ReferringDomains: n,
			}, score)
		}
		if len(matched) > 0 {
			continue
		}
		ct := slugTokens(cu)
		for u := range candidates {
			if slugMatch(ct, slugTokens(u)) {
				addReason(page(u), ReasonCompetitorLinks+"|"+cu, TargetReason{
					Kind: ReasonCompetitorLinks, Competitor: x.competitor, CompetitorURL: cu, ReferringDomains: n,
				}, score)
			}
		}
	}

	// Saved, ticked pages are always listed, even without evidence.
	for u, t := range saved {
		if t.Selected {
			page(u)
		}
	}

	list := make([]TargetSuggestion, 0, len(pages))
	for u, p := range pages {
		s := p.s
		if t, ok := saved[u]; ok {
			if !t.Selected {
				continue // unticked by a person: not offered
			}
			id := t.ID
			s.TargetID, s.Selected, s.Kind = &id, true, t.Kind
		} else {
			s.Kind, s.KindGuessed = GuessKind(u), true
		}
		if kind != "" && s.Kind != kind {
			continue
		}
		s.Score = math.Round(s.Score*100) / 100
		slices.SortFunc(s.Reasons, func(a, b TargetReason) int {
			if a.Kind != b.Kind {
				return strings.Compare(b.Kind, a.Kind) // keyword_gap before competitor_links
			}
			return cmp.Compare(volOf(b.Volume)+float64(b.ReferringDomains), volOf(a.Volume)+float64(a.ReferringDomains))
		})
		if len(s.Reasons) > maxReasons {
			s.Reasons = s.Reasons[:maxReasons]
		}
		slices.Sort(s.Competitors)
		s.Anchors = anchorOptions(u, in.BrandName, seByURL[u], otherByURL[u], pageTarget[u])
		list = append(list, s)
	}
	slices.SortFunc(list, func(a, b TargetSuggestion) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return strings.Compare(a.URL, b.URL)
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	out.Data = list
	return out
}

func volOf(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// anchorOptions lists the anchors for one page. SE Ranking keywords come
// first and say so; the page's keyword target and other connections'
// keywords follow, each labelled with its source.
func anchorOptions(pageURL, brandName string, se, other []kwRow, pt pageTargetRow) AnchorOptions {
	sortKw := func(list []kwRow) []kwRow {
		list = slices.Clone(list)
		slices.SortFunc(list, func(a, b kwRow) int {
			if c := cmp.Compare(volOf(b.Volume), volOf(a.Volume)); c != 0 {
				return c
			}
			if better(a.Position, b.Position) {
				return -1
			}
			if better(b.Position, a.Position) {
				return 1
			}
			return strings.Compare(a.Keyword, b.Keyword)
		})
		return list
	}
	seen := map[string]bool{}
	var all []AnchorOption
	add := func(o AnchorOption) {
		key := normKeyword(o.Text)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		o.Text = key
		all = append(all, o)
	}
	for _, k := range sortKw(se) {
		add(AnchorOption{Text: k.Keyword, Source: AnchorSourceSERanking, Position: k.Position, Volume: k.Volume})
	}
	if pt.Keyword != "" {
		add(AnchorOption{Text: pt.Keyword, Source: AnchorSourcePageTarget})
	}
	for _, s := range pt.Secondary {
		add(AnchorOption{Text: s, Source: AnchorSourcePageTarget})
	}
	for _, k := range sortKw(other) {
		add(AnchorOption{Text: k.Keyword, Source: AnchorSourceSEOKeywords, Position: k.Position, Volume: k.Volume})
	}
	out := AnchorOptions{Primary: []AnchorOption{}, Secondary: SecondaryAnchorLists{Brand: []AnchorOption{}, URL: []AnchorOption{}, Keyword: []AnchorOption{}}}
	if len(all) > maxAnchorOptions {
		all = all[:maxAnchorOptions]
	}
	out.Primary = append(out.Primary, all...)
	if len(all) > 1 {
		out.Secondary.Keyword = append(out.Secondary.Keyword, all[1:]...)
	}
	if b := strings.TrimSpace(brandName); b != "" {
		out.Secondary.Brand = append(out.Secondary.Brand, AnchorOption{Text: b, Source: AnchorBrand})
	}
	if pageURL != "" {
		out.Secondary.URL = append(out.Secondary.URL, AnchorOption{Text: pageURL, Source: AnchorURL})
		if h := targetHost(pageURL); h != "" {
			out.Secondary.URL = append(out.Secondary.URL, AnchorOption{Text: h, Source: AnchorURL})
			if p, err := url.Parse(pageURL); err == nil && p.Path != "/" && p.Path != "" {
				out.Secondary.URL = append(out.Secondary.URL, AnchorOption{Text: h + p.Path, Source: AnchorURL})
			}
		}
	}
	return out
}

// SuggestTargets ranks the brand's pages for a link from stored data
// only. kind narrows it to article or product pages; limit caps it.
func SuggestTargets(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, kind string, limit int) (Suggestions, error) {
	in, err := loadTargetInputs(ctx, q, workspaceID, brandID)
	if err != nil {
		return Suggestions{}, err
	}
	return buildSuggestions(in, kind, limit), nil
}

// AnchorsFor lists the anchor choices for any page of the brand, saved
// or not, from stored data only.
func AnchorsFor(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, pageURL string) (string, AnchorOptions, error) {
	u, err := NormalizeTargetURL(pageURL)
	if err != nil {
		return "", AnchorOptions{}, err
	}
	in, err := loadTargetInputs(ctx, q, workspaceID, brandID)
	if err != nil {
		return "", AnchorOptions{}, err
	}
	var se, other []kwRow
	for _, k := range in.OurKeywords {
		if normURL(k.URL) != u {
			continue
		}
		k.Keyword = normKeyword(k.Keyword)
		if k.Connector == "seranking" {
			se = append(se, k)
		} else {
			other = append(other, k)
		}
	}
	var pt pageTargetRow
	for _, p := range in.PageTargets {
		if normURL(p.URL) == u {
			pt = p
		}
	}
	return u, anchorOptions(u, in.BrandName, se, other, pt), nil
}

func loadTargetInputs(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID) (targetInputs, error) {
	var in targetInputs
	name, hosts, err := BrandHosts(ctx, q, workspaceID, brandID)
	if err != nil {
		return in, err
	}
	in.BrandName, in.Hosts = name, hosts
	if in.Saved, err = ListTargets(ctx, q, workspaceID, brandID, TargetFilter{}); err != nil {
		return in, err
	}
	rows, err := q.Query(ctx, `SELECT id, host FROM seo_sites WHERE workspace_id = $1 AND brand_id = $2`, workspaceID, brandID)
	if err != nil {
		return in, err
	}
	type site struct {
		id   uuid.UUID
		host string
	}
	sites, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (site, error) {
		var s site
		return s, r.Scan(&s.id, &s.host)
	})
	if err != nil {
		return in, err
	}
	if len(sites) == 0 {
		return in, nil
	}
	siteIDs := make([]uuid.UUID, 0, len(sites))
	var ourHosts []string
	for _, s := range sites {
		siteIDs = append(siteIDs, s.id)
		h, _, _ := strings.Cut(s.host, ":")
		t := seosync.LinkTarget(s.host)
		ourHosts = append(ourHosts, s.host, h, t, "www."+t)
	}
	slices.Sort(ourHosts)
	ourHosts = slices.Compact(ourHosts)

	// The Keywords page's page targets.
	rows, err = q.Query(ctx, `SELECT url, keyword, secondary FROM page_targets
		WHERE workspace_id = $1 AND site_id = ANY($2) AND status = 'set'`, workspaceID, siteIDs)
	if err != nil {
		return in, err
	}
	if in.PageTargets, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (pageTargetRow, error) {
		var p pageTargetRow
		return p, r.Scan(&p.URL, &p.Keyword, &p.Secondary)
	}); err != nil {
		return in, err
	}

	// The site's keywords: each connection's newest stored snapshot.
	own, err := latestKeywordRows(ctx, q, workspaceID, ourHosts)
	if err != nil {
		return in, err
	}
	for _, k := range own {
		in.OurKeywords = append(in.OurKeywords, k.kwRow)
	}
	// Keywords SE Ranking projects track, with the page that ranked last.
	rows, err = q.Query(ctx, `
		SELECT t.keyword, o.url, o.position::double precision
		FROM tracked_keywords t
		JOIN integrations i ON i.id = t.integration_id AND i.workspace_id = t.workspace_id
		JOIN LATERAL (
			SELECT r.url, r.position FROM rank_observations r
			WHERE r.workspace_id = t.workspace_id AND r.tracked_id = t.id
			ORDER BY r.day DESC LIMIT 1
		) o ON true
		WHERE t.workspace_id = $1 AND t.site_id = ANY($2) AND i.connector = 'seranking' AND o.url <> ''`,
		workspaceID, siteIDs)
	if err != nil {
		return in, err
	}
	tracked, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (kwRow, error) {
		k := kwRow{Connector: "seranking"}
		return k, r.Scan(&k.Keyword, &k.URL, &k.Position)
	})
	if err != nil {
		return in, err
	}
	in.OurKeywords = append(in.OurKeywords, tracked...)

	// Tracked competitors, their stored keywords and the link reads kept for them.
	rows, err = q.Query(ctx, `SELECT id, host FROM seo_competitors
		WHERE workspace_id = $1 AND site_id = ANY($2) AND status = 'tracked'`, workspaceID, siteIDs)
	if err != nil {
		return in, err
	}
	type comp struct {
		id   uuid.UUID
		host string
	}
	comps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (comp, error) {
		var c comp
		return c, r.Scan(&c.id, &c.host)
	})
	if err != nil {
		return in, err
	}
	if len(comps) == 0 {
		return in, nil
	}
	compIDs := make([]uuid.UUID, 0, len(comps))
	compHosts := []string{}
	hostOf := map[string]string{}
	for _, c := range comps {
		compIDs = append(compIDs, c.id)
		h := strings.TrimPrefix(strings.ToLower(c.host), "www.")
		if !slices.Contains(in.Competitors, h) {
			in.Competitors = append(in.Competitors, h)
		}
		for _, v := range []string{c.host, h, "www." + h} {
			compHosts = append(compHosts, v)
			hostOf[v] = h
		}
	}
	ck, err := latestKeywordRows(ctx, q, workspaceID, compHosts)
	if err != nil {
		return in, err
	}
	for _, k := range ck {
		in.CompKeywords = append(in.CompKeywords, compKwRow{Competitor: hostOf[k.host], Keyword: k.Keyword, URL: k.URL, Position: k.Position, Volume: k.Volume})
	}
	rows, err = q.Query(ctx, `
		SELECT DISTINCT c.host, rr.target_url, rr.source_domain
		FROM link_reads lr
		JOIN seo_competitors c ON c.id = lr.competitor_id AND c.workspace_id = lr.workspace_id
		JOIN link_read_rows rr ON rr.read_id = lr.id AND rr.workspace_id = lr.workspace_id
		WHERE lr.workspace_id = $1 AND lr.competitor_id = ANY($2) AND lr.status = 'done'
		  AND (lr.brand_id IS NULL OR lr.brand_id = $3) AND NOT rr.lost
		LIMIT 20000`, workspaceID, compIDs, brandID)
	if err != nil {
		return in, err
	}
	if in.CompLinks, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (compLinkRow, error) {
		var l compLinkRow
		err := r.Scan(&l.Competitor, &l.TargetURL, &l.SourceDomain)
		l.Competitor = strings.TrimPrefix(strings.ToLower(l.Competitor), "www.")
		return l, err
	}); err != nil {
		return in, err
	}
	return in, nil
}

type hostKwRow struct {
	kwRow
	host string
}

// latestKeywordRows reads each connection's newest keywords snapshot for
// the hosts. It only SELECTs seo_snapshots.
func latestKeywordRows(ctx context.Context, q Querier, workspaceID uuid.UUID, hosts []string) ([]hostKwRow, error) {
	rows, err := q.Query(ctx, `
		SELECT s.host, s.connector, s.key, s.url, s.metrics
		FROM seo_snapshots s
		WHERE s.workspace_id = $1 AND s.report = $2 AND s.host = ANY($3) AND s.key <> ''
		  AND s.day = (SELECT max(day) FROM seo_snapshots x
		               WHERE x.workspace_id = $1 AND x.report = $2 AND x.host = s.host AND x.integration_id = s.integration_id)
		LIMIT 50000`, workspaceID, connectors.SEOKeywords, hosts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (hostKwRow, error) {
		var v hostKwRow
		var raw []byte
		if err := r.Scan(&v.host, &v.Connector, &v.Keyword, &v.URL, &raw); err != nil {
			return v, err
		}
		var m connectors.SEOMetrics
		_ = json.Unmarshal(raw, &m)
		v.Position, v.Volume = m.Position, m.Volume
		return v, nil
	})
}
