package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Placements: the pages AI assistants cite, or read before answering,
// when they answer a brand's questions, ranked by how much being on each
// would be worth. A page's value adds up, over the answers that cite it,
// the demand weight of the question answered (as the score weights it),
// in full when the answer doesn't name the brand and a quarter when it
// does (it is already there), plus a share of the same for answers that
// read the page without citing it. Values are relative: the top page is
// 100. With each page: which competitors those answers named, so a person
// sees who is winning the placement.

const (
	placementNamedShare = 0.25
	placementReadShare  = 0.3
	maxPlacements       = 100
	maxOwnPlacements    = 50
)

type aiVisPlacementBrand struct {
	Name    string `json:"name"`
	Answers int    `json:"answers"`
}

type aiVisPlacement struct {
	URL        string `json:"url"`
	Host       string `json:"host"`
	Title      string `json:"title"`
	SourceType string `json:"source_type"`
	// Answers citing it, those among them naming the brand, answers that
	// read it without citing it, and the questions they answered.
	Answers   int `json:"answers"`
	Named     int `json:"named"`
	Reads     int `json:"reads"`
	Questions int `json:"questions"`
	// Value is relative: the top placement is 100.
	Value       float64               `json:"value"`
	Competitors []aiVisPlacementBrand `json:"competitors"`
	LastSeen    time.Time             `json:"last_seen_at"`

	raw       float64
	questions map[string]bool
}

// aiVisOwnPage is one of the brand's own pages the answers cite.
type aiVisOwnPage struct {
	URL       string    `json:"url"`
	Host      string    `json:"host"`
	Title     string    `json:"title"`
	Answers   int       `json:"answers"`
	Questions int       `json:"questions"`
	LastSeen  time.Time `json:"last_seen_at"`
}

func (s *Server) getAIVisibilityPlacements(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days := 28
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	cohort := r.URL.Query().Get("cohort")
	if cohort != "" && !validUUID(cohort) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "cohort is a question set's id")
		return
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	if f := r.URL.Query().Get("format"); f != "" && f != "json" && f != "csv" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "format is json or csv")
		return
	}
	places, own, err := s.aiVisibilityPlacements(r.Context(), a.WorkspaceID, a.Brand, cohort, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		header := []string{"url", "host", "title", "source_type", "value", "answers", "named", "reads", "questions", "competitors", "last_seen_at"}
		rows := make([][]string, 0, len(places))
		for _, p := range places {
			comps := make([]string, 0, len(p.Competitors))
			for _, c := range p.Competitors {
				comps = append(comps, c.Name)
			}
			rows = append(rows, []string{p.URL, p.Host, p.Title, p.SourceType, strconv.FormatFloat(p.Value, 'f', 1, 64), strconv.Itoa(p.Answers), strconv.Itoa(p.Named), strconv.Itoa(p.Reads), strconv.Itoa(p.Questions), strings.Join(comps, "; "), p.LastSeen.UTC().Format(time.RFC3339)})
		}
		writeCSV(w, "ai-visibility-placements", header, rows)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": places, "own_pages": own, "days": days, "from": from.Format(time.RFC3339)})
}

func (s *Server) aiVisibilityPlacements(ctx context.Context, ws, brand, cohort string, from time.Time) ([]*aiVisPlacement, []aiVisOwnPage, error) {
	// Each question's weight, from its demand.
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.demand FROM ai_prompts p JOIN ai_cohorts c ON c.id = p.cohort_id
		WHERE p.workspace_id = $1 AND c.workspace_id = $1 AND c.brand_id = $2`, ws, brand)
	if err != nil {
		return nil, nil, err
	}
	demand := map[string]*int{}
	var pid string
	var d *int
	if _, err := pgx.ForEachRow(rows, []any{&pid, &d}, func() error {
		demand[pid] = d
		return nil
	}); err != nil {
		return nil, nil, err
	}
	weight := weights(demand)
	args := []any{ws, brand, from}
	where := ""
	if cohort != "" {
		args = append(args, cohort)
		where = " AND s.cohort_id = $4"
	}
	places := []*aiVisPlacement{}
	byURL := map[string]*aiVisPlacement{}
	place := func(url, host, title, typ string) *aiVisPlacement {
		p := byURL[url]
		if p == nil {
			p = &aiVisPlacement{URL: url, Host: host, Title: title, SourceType: typ, Competitors: []aiVisPlacementBrand{}, questions: map[string]bool{}}
			byURL[url] = p
			places = append(places, p)
		}
		if p.Title == "" {
			p.Title = title
		}
		if p.SourceType == "" {
			p.SourceType = typ
		}
		return p
	}
	// Answers citing a page that is nobody's.
	rows, err = s.pool.Query(ctx, `
		SELECT c.url, c.host, c.title, c.source_type, s.prompt_id, c.sampled_at,
		       EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand')
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.owner = 'other' AND s.status = 'ok' AND s.sampled_at >= $3`+where+`
		ORDER BY c.sampled_at`, args...)
	if err != nil {
		return nil, nil, err
	}
	var url, host, title, typ string
	var at time.Time
	var named bool
	if _, err := pgx.ForEachRow(rows, []any{&url, &host, &title, &typ, &pid, &at, &named}, func() error {
		p := place(url, host, title, typ)
		p.Answers++
		share := 1.0
		if named {
			p.Named++
			share = placementNamedShare
		}
		p.raw += weight[pid] * share
		p.questions[pid] = true
		p.LastSeen = at
		return nil
	}); err != nil {
		return nil, nil, err
	}
	// Answers that read a page and didn't cite it.
	rows, err = s.pool.Query(ctx, `
		SELECT r.url, r.host, r.title, s.prompt_id, r.sampled_at,
		       EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand')
		FROM ai_pages_read r JOIN ai_samples s ON s.id = r.sample_id
		WHERE r.workspace_id = $1 AND s.brand_id = $2 AND r.owner = 'other' AND NOT r.cited AND s.status = 'ok' AND s.sampled_at >= $3`+where+`
		ORDER BY r.sampled_at`, args...)
	if err != nil {
		return nil, nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&url, &host, &title, &pid, &at, &named}, func() error {
		p := place(url, host, title, "")
		p.Reads++
		share := placementReadShare
		if named {
			share *= placementNamedShare
		}
		p.raw += weight[pid] * share
		p.questions[pid] = true
		if at.After(p.LastSeen) {
			p.LastSeen = at
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	// Which competitors the answers citing each page named.
	rows, err = s.pool.Query(ctx, `
		SELECT c.url, m.name, count(DISTINCT m.sample_id)
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id JOIN ai_mentions m ON m.sample_id = s.id AND m.entity = 'competitor'
		WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.owner = 'other' AND s.status = 'ok' AND s.sampled_at >= $3`+where+`
		GROUP BY 1, 2 ORDER BY 1, 3 DESC, 2`, args...)
	if err != nil {
		return nil, nil, err
	}
	var name string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&url, &name, &n}, func() error {
		if p := byURL[url]; p != nil {
			p.Competitors = append(p.Competitors, aiVisPlacementBrand{Name: name, Answers: n})
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	top := 0.0
	for _, p := range places {
		p.Questions = len(p.questions)
		top = math.Max(top, p.raw)
	}
	for _, p := range places {
		if top > 0 {
			p.Value = math.Round(p.raw/top*1000) / 10
		}
	}
	sort.SliceStable(places, func(i, j int) bool {
		a, b := places[i], places[j]
		return a.raw > b.raw || a.raw == b.raw && (a.Answers > b.Answers || a.Answers == b.Answers && a.URL < b.URL)
	})
	if len(places) > maxPlacements {
		places = places[:maxPlacements]
	}
	// The brand's own pages the answers cite.
	rows, err = s.pool.Query(ctx, `
		SELECT c.url, max(c.host), max(c.title), count(DISTINCT c.sample_id), count(DISTINCT s.prompt_id), max(c.sampled_at)
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.owner = 'brand' AND s.status = 'ok' AND s.sampled_at >= $3`+where+`
		GROUP BY c.url ORDER BY 4 DESC, 1 LIMIT $`+strconv.Itoa(len(args)+1), append(args, maxOwnPlacements)...)
	if err != nil {
		return nil, nil, err
	}
	own := []aiVisOwnPage{}
	var o aiVisOwnPage
	if _, err := pgx.ForEachRow(rows, []any{&o.URL, &o.Host, &o.Title, &o.Answers, &o.Questions, &o.LastSeen}, func() error {
		own = append(own, o)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return places, own, nil
}
