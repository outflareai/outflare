package api

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AIAnswerShareNote is returned on every payload. The figures are counted
// from already-stored AI visibility runs only.
const AIAnswerShareNote = "Share of voice and mention rates are counted from already-stored AI answer runs. This does not start a new run or call a paid provider. A cluster is the question's topic, or Untagged when the question has none."

var compareLocalePrefix = map[string]bool{
	"zh": true, "fr": true, "es": true, "pt": true, "id": true, "tr": true, "ja": true,
}

// aiShare is mention rate and share of voice by topic, question set and
// question type, plus citations of /compare pages, from stored runs.
type aiShare struct {
	Days          int            `json:"days"`
	From          string         `json:"from"`
	Note          string         `json:"note"`
	Totals        aiShareTotals  `json:"totals"`
	Clusters      []aiShareGroup `json:"clusters"`
	QuestionSets  []aiShareGroup `json:"question_sets"`
	QuestionTypes []aiShareGroup `json:"question_types"`
	Compare       aiShareCompare `json:"compare"`
	Weeks         []aiShareWeek  `json:"weeks"`
}

type aiShareTotals struct {
	Answers             int            `json:"answers"`
	Questions           int            `json:"questions"`
	Clusters            int            `json:"clusters"`
	MentionRate         *float64       `json:"mention_rate"`
	ShareOfVoice        *float64       `json:"share_of_voice"`
	RecommendRate       *float64       `json:"recommend_rate"`
	CitationRate        *float64       `json:"citation_rate"`
	CompareCitationRate *float64       `json:"compare_citation_rate"`
	Competitors         []aiShareRival `json:"competitors"`
}

type aiShareGroup struct {
	Key                 string         `json:"key"`
	Label               string         `json:"label"`
	Kind                string         `json:"kind"`
	Questions           int            `json:"questions"`
	Answers             int            `json:"answers"`
	Named               int            `json:"named"`
	Recommended         int            `json:"recommended"`
	Cited               int            `json:"cited"`
	CompareCited        int            `json:"compare_cited"`
	MentionRate         *float64       `json:"mention_rate"`
	ShareOfVoice        *float64       `json:"share_of_voice"`
	RecommendRate       *float64       `json:"recommend_rate"`
	CitationRate        *float64       `json:"citation_rate"`
	CompareCitationRate *float64       `json:"compare_citation_rate"`
	Competitors         []aiShareRival `json:"competitors"`
	Weeks               []aiShareWeek  `json:"weeks"`
}

type aiShareRival struct {
	Name         string   `json:"name"`
	Answers      int      `json:"answers"`
	MentionRate  *float64 `json:"mention_rate"`
	ShareOfVoice *float64 `json:"share_of_voice"`
	Recommended  int      `json:"recommended"`
}

type aiShareWeek struct {
	Week                string   `json:"week"`
	Answers             int      `json:"answers"`
	Named               int      `json:"named"`
	MentionRate         *float64 `json:"mention_rate"`
	ShareOfVoice        *float64 `json:"share_of_voice"`
	CompareCited        int      `json:"compare_cited"`
	CompareCitationRate *float64 `json:"compare_citation_rate"`
}

type aiShareCompare struct {
	Answers         int                  `json:"answers"`
	BrandCited      int                  `json:"brand_cited"`
	CompetitorCited int                  `json:"competitor_cited"`
	BrandRate       *float64             `json:"brand_rate"`
	CompetitorRate  *float64             `json:"competitor_rate"`
	Pages           []aiShareComparePage `json:"pages"`
	Weeks           []aiShareCompareWeek `json:"weeks"`
}

type aiShareComparePage struct {
	URL        string `json:"url"`
	Owner      string `json:"owner"`
	Competitor string `json:"competitor,omitempty"`
	Answers    int    `json:"answers"`
}

type aiShareCompareWeek struct {
	Week            string   `json:"week"`
	Answers         int      `json:"answers"`
	BrandCited      int      `json:"brand_cited"`
	CompetitorCited int      `json:"competitor_cited"`
	BrandRate       *float64 `json:"brand_rate"`
}

func (s *Server) getAIAnswerShare(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	days := 84
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	out, err := s.aiAnswerShare(r.Context(), a.WorkspaceID, sel, days, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type shareSample struct {
	id, cohortID, cohortName, promptID string
	topic, questionType, week          string
	brandNamed, brandCited             bool
	brandCompare, compCompare          bool
	recommended                        bool
}

type shareMention struct {
	sampleID, entity, name, stance string
}

func (s *Server) aiAnswerShare(ctx context.Context, ws string, sel brandSel, days int, from time.Time) (*aiShare, error) {
	out := &aiShare{
		Days:          days,
		From:          from.Format(time.RFC3339),
		Note:          AIAnswerShareNote,
		Clusters:      []aiShareGroup{},
		QuestionSets:  []aiShareGroup{},
		QuestionTypes: []aiShareGroup{},
		Weeks:         []aiShareWeek{},
		Compare:       aiShareCompare{Pages: []aiShareComparePage{}, Weeks: []aiShareCompareWeek{}},
		Totals:        aiShareTotals{Competitors: []aiShareRival{}},
	}
	args := []any{ws, from}
	rows, err := s.pool.Query(ctx, `
		SELECT s.id::text, s.cohort_id::text, c.name, s.prompt_id::text,
		       COALESCE(NULLIF(btrim(p.topic), ''), 'untagged'),
		       COALESCE(NULLIF(p.question_type, ''), 'untyped'),
		       to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
		       EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand'),
		       EXISTS (SELECT 1 FROM ai_citations c2 WHERE c2.sample_id = s.id AND c2.owner = 'brand'),
		       EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand' AND m.stance = 'recommended')
		FROM ai_samples s
		JOIN ai_cohorts c ON c.id = s.cohort_id
		JOIN ai_prompts p ON p.id = s.prompt_id
		WHERE s.workspace_id = $1 AND s.status = 'ok' AND s.sampled_at >= $2`+sel.sql("s.brand_id", &args)+`
		ORDER BY s.sampled_at`, args...)
	if err != nil {
		return nil, err
	}
	samples, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (shareSample, error) {
		var v shareSample
		err := row.Scan(&v.id, &v.cohortID, &v.cohortName, &v.promptID, &v.topic, &v.questionType, &v.week, &v.brandNamed, &v.brandCited, &v.recommended)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return out, nil
	}
	byID := map[string]*shareSample{}
	ids := make([]string, 0, len(samples))
	for i := range samples {
		byID[samples[i].id] = &samples[i]
		ids = append(ids, samples[i].id)
	}

	mentRows, err := s.pool.Query(ctx, `
		SELECT sample_id::text, entity, name, stance
		FROM ai_mentions
		WHERE workspace_id = $1 AND sample_id = ANY($2::uuid[])`, ws, ids)
	if err != nil {
		return nil, err
	}
	ments, err := pgx.CollectRows(mentRows, func(row pgx.CollectableRow) (shareMention, error) {
		var v shareMention
		err := row.Scan(&v.sampleID, &v.entity, &v.name, &v.stance)
		return v, err
	})
	if err != nil {
		return nil, err
	}

	citeRows, err := s.pool.Query(ctx, `
		SELECT sample_id::text, url, owner, competitor
		FROM ai_citations
		WHERE workspace_id = $1 AND sample_id = ANY($2::uuid[])`, ws, ids)
	if err != nil {
		return nil, err
	}
	type cite struct {
		sampleID, url, owner, competitor string
	}
	cites, err := pgx.CollectRows(citeRows, func(row pgx.CollectableRow) (cite, error) {
		var v cite
		err := row.Scan(&v.sampleID, &v.url, &v.owner, &v.competitor)
		return v, err
	})
	if err != nil {
		return nil, err
	}

	comparePages := map[string]*aiShareComparePage{}
	for _, c := range cites {
		if !isCompareURL(c.url) {
			continue
		}
		sm := byID[c.sampleID]
		if sm == nil {
			continue
		}
		if c.owner == "brand" {
			sm.brandCompare = true
		}
		if c.owner == "competitor" {
			sm.compCompare = true
		}
		key := c.owner + "\t" + c.url
		p := comparePages[key]
		if p == nil {
			p = &aiShareComparePage{URL: c.url, Owner: c.owner, Competitor: c.competitor}
			comparePages[key] = p
		}
		p.Answers++
	}

	mentsBySample := map[string][]shareMention{}
	for _, m := range ments {
		mentsBySample[m.sampleID] = append(mentsBySample[m.sampleID], m)
	}

	clusters := map[string]*groupAcc{}
	sets := map[string]*groupAcc{}
	types := map[string]*groupAcc{}
	overall := newGroupAcc("all", "All", "all")
	compareWeek := map[string]*aiShareCompareWeek{}

	add := func(m map[string]*groupAcc, key, label, kind string, sm shareSample, ments []shareMention) *groupAcc {
		g := m[key]
		if g == nil {
			g = newGroupAcc(key, label, kind)
			m[key] = g
		}
		g.add(sm, ments)
		return g
	}

	for _, sm := range samples {
		ms := mentsBySample[sm.id]
		overall.add(sm, ms)
		topicLabel := sm.topic
		if sm.topic == "untagged" {
			topicLabel = "Untagged"
		}
		add(clusters, sm.topic, topicLabel, "topic", sm, ms)
		add(sets, sm.cohortID, sm.cohortName, "question_set", sm, ms)
		typeLabel := sm.questionType
		if sm.questionType == "untyped" {
			typeLabel = "Untyped"
		}
		add(types, sm.questionType, typeLabel, "question_type", sm, ms)

		cw := compareWeek[sm.week]
		if cw == nil {
			cw = &aiShareCompareWeek{Week: sm.week}
			compareWeek[sm.week] = cw
		}
		cw.Answers++
		if sm.brandCompare {
			cw.BrandCited++
		}
		if sm.compCompare {
			cw.CompetitorCited++
		}
	}

	out.Totals = overall.asTotals()
	out.Weeks = overall.weeks()
	out.Clusters = finishGroups(clusters)
	out.QuestionSets = finishGroups(sets)
	out.QuestionTypes = finishGroups(types)
	out.Totals.Clusters = 0
	for _, g := range out.Clusters {
		if g.Key != "untagged" {
			out.Totals.Clusters++
		}
	}

	out.Compare.Answers = overall.answers
	out.Compare.BrandCited = overall.compareCited
	out.Compare.CompetitorCited = overall.compCompare
	out.Compare.BrandRate = rate(overall.compareCited, overall.answers)
	out.Compare.CompetitorRate = rate(overall.compCompare, overall.answers)
	var pages []aiShareComparePage
	for _, p := range comparePages {
		pages = append(pages, *p)
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].Answers != pages[j].Answers {
			return pages[i].Answers > pages[j].Answers
		}
		return pages[i].URL < pages[j].URL
	})
	if len(pages) > 25 {
		pages = pages[:25]
	}
	if pages == nil {
		pages = []aiShareComparePage{}
	}
	out.Compare.Pages = pages
	cweeks := []aiShareCompareWeek{}
	for _, w := range compareWeek {
		w.BrandRate = rate(w.BrandCited, w.Answers)
		cweeks = append(cweeks, *w)
	}
	sort.Slice(cweeks, func(i, j int) bool { return cweeks[i].Week < cweeks[j].Week })
	out.Compare.Weeks = cweeks
	return out, nil
}

type groupAcc struct {
	key, label, kind                                              string
	answers, named, recommended, cited, compareCited, compCompare int
	prompts                                                       map[string]bool
	rivals                                                        map[string]*aiShareRival
	entityN                                                       int // all entity mention-answers, for share of voice
	week                                                          map[string]*aiShareWeek
	weekEnt                                                       map[string]int
}

func newGroupAcc(key, label, kind string) *groupAcc {
	return &groupAcc{key: key, label: label, kind: kind, prompts: map[string]bool{}, rivals: map[string]*aiShareRival{}, week: map[string]*aiShareWeek{}, weekEnt: map[string]int{}}
}

func (g *groupAcc) add(sm shareSample, ments []shareMention) {
	g.answers++
	g.prompts[sm.promptID] = true
	w := g.week[sm.week]
	if w == nil {
		w = &aiShareWeek{Week: sm.week}
		g.week[sm.week] = w
	}
	w.Answers++
	if sm.brandNamed {
		g.named++
		w.Named++
	}
	if sm.recommended {
		g.recommended++
	}
	if sm.brandCited {
		g.cited++
	}
	if sm.brandCompare {
		g.compareCited++
		w.CompareCited++
	}
	if sm.compCompare {
		g.compCompare++
	}
	for _, m := range ments {
		g.entityN++
		g.weekEnt[sm.week]++
		if m.entity == "competitor" {
			r := g.rivals[m.name]
			if r == nil {
				r = &aiShareRival{Name: m.name}
				g.rivals[m.name] = r
			}
			r.Answers++
			if m.stance == "recommended" {
				r.Recommended++
			}
		}
	}
}

func (g *groupAcc) finalize() {
	for _, r := range g.rivals {
		r.MentionRate = rate(r.Answers, g.answers)
		r.ShareOfVoice = rate(r.Answers, g.entityN)
	}
}

func (g *groupAcc) asGroup() aiShareGroup {
	return aiShareGroup{
		Key:                 g.key,
		Label:               g.label,
		Kind:                g.kind,
		Questions:           len(g.prompts),
		Answers:             g.answers,
		Named:               g.named,
		Recommended:         g.recommended,
		Cited:               g.cited,
		CompareCited:        g.compareCited,
		MentionRate:         rate(g.named, g.answers),
		ShareOfVoice:        rate(g.named, g.entityN),
		RecommendRate:       rate(g.recommended, g.answers),
		CitationRate:        rate(g.cited, g.answers),
		CompareCitationRate: rate(g.compareCited, g.answers),
		Competitors:         g.rivalList(),
		Weeks:               g.weeks(),
	}
}

func (g *groupAcc) asTotals() aiShareTotals {
	return aiShareTotals{
		Answers:             g.answers,
		Questions:           len(g.prompts),
		MentionRate:         rate(g.named, g.answers),
		ShareOfVoice:        rate(g.named, g.entityN),
		RecommendRate:       rate(g.recommended, g.answers),
		CitationRate:        rate(g.cited, g.answers),
		CompareCitationRate: rate(g.compareCited, g.answers),
		Competitors:         g.rivalList(),
	}
}

func (g *groupAcc) rivalList() []aiShareRival {
	out := make([]aiShareRival, 0, len(g.rivals))
	for _, r := range g.rivals {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Answers != out[j].Answers {
			return out[i].Answers > out[j].Answers
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > 12 {
		out = out[:12]
	}
	if out == nil {
		return []aiShareRival{}
	}
	return out
}

func (g *groupAcc) weeks() []aiShareWeek {
	out := make([]aiShareWeek, 0, len(g.week))
	for _, w := range g.week {
		w.MentionRate = rate(w.Named, w.Answers)
		w.ShareOfVoice = rate(w.Named, g.weekEnt[w.Week])
		w.CompareCitationRate = rate(w.CompareCited, w.Answers)
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Week < out[j].Week })
	return out
}

func finishGroups(m map[string]*groupAcc) []aiShareGroup {
	out := make([]aiShareGroup, 0, len(m))
	for _, g := range m {
		g.finalize()
		out = append(out, g.asGroup())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Answers != out[j].Answers {
			return out[i].Answers > out[j].Answers
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// isCompareURL reports whether a cited page is a /compare path, after
// stripping a single locale prefix such as /zh or /fr.
func isCompareURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	path := strings.ToLower(u.Path)
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return false
	}
	seg := strings.Split(path, "/")
	if compareLocalePrefix[seg[0]] {
		seg = seg[1:]
	}
	return len(seg) > 0 && seg[0] == "compare"
}
