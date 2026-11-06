package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// What AI answers say about a brand: the attributes they give it and the
// objections they raise, read from each answer with its stance
// (engine/internal/aivis/stances.go) and stored in ai_claims. Grouped by
// phrase, with how many answers said it, this period and the one before,
// and the pages those answers cited, so a brand can see what it is known
// for, what is held against it and where that comes from. An objection
// that isn't true is disputed: a fix (ai_wrong_claim) with the quote and
// the pages to correct.

const (
	maxClaimGroups      = 50
	maxCompetitorClaims = 5
	maxClaimSources     = 3
)

type aiVisClaimSource struct {
	URL  string `json:"url"`
	Host string `json:"host"`
}

type aiVisClaim struct {
	Key       string `json:"key"`
	Text      string `json:"text"`
	Kind      string `json:"kind"`
	Sentiment string `json:"sentiment"`
	// Answers that made the claim this period, out of the answers naming
	// the entity (share), and the period before.
	Answers         int      `json:"answers"`
	Share           *float64 `json:"share"`
	PreviousAnswers int      `json:"previous_answers"`
	Questions       int      `json:"questions"`
	Quote           string   `json:"quote"`
	// Assistants that made it, most often first.
	Assistants []string `json:"assistants"`
	// Sources are pages the answers making it cited, most often first.
	Sources    []aiVisClaimSource `json:"sources"`
	LastSeenAt time.Time          `json:"last_seen_at"`
	// FixID is the open fix disputing it, when a person has.
	FixID *string `json:"fix_id"`
	// bookkeeping
	texts      map[string]int
	sentiments map[string]int
	assistants map[string]int
	sources    map[string]int
	hosts      map[string]string
	prompts    map[string]bool
	samples    map[string]bool
}

type aiVisCompetitorClaims struct {
	Name       string       `json:"name"`
	Answers    int          `json:"answers"`
	Attributes []aiVisClaim `json:"attributes"`
	Objections []aiVisClaim `json:"objections"`
}

func (s *Server) routesAIVisibilityClaims(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/brands/{brand}/ai-visibility/claims", s.onBrand(read, s.getAIVisibilityClaims))
	mux.HandleFunc("POST /v1/brands/{brand}/ai-visibility/claims/dispute", s.onBrand(manage, s.disputeAIVisibilityClaim))
}

func (s *Server) getAIVisibilityClaims(w http.ResponseWriter, r *http.Request) {
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
	attrs, objs, comps, err := s.aiVisibilityClaims(r.Context(), a.WorkspaceID, a.Brand, cohort, from, from.AddDate(0, 0, -days))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attributes": attrs, "objections": objs, "competitors": comps,
		"days": days, "from": from.Format(time.RFC3339), "previous_from": from.AddDate(0, 0, -days).Format(time.RFC3339)})
}

func (s *Server) aiVisibilityClaims(ctx context.Context, ws, brand, cohort string, from, before time.Time) ([]aiVisClaim, []aiVisClaim, []aiVisCompetitorClaims, error) {
	var cohortID *string
	if cohort != "" {
		cohortID = &cohort
	}
	type gkey struct{ entity, name, kind, key string }
	groups := map[gkey]*aiVisClaim{}
	// Answers naming each entity this period, for the share.
	named := map[[2]string]int{}
	rows, err := s.pool.Query(ctx, `
		SELECT m.entity, m.name, count(*) FROM ai_mentions m JOIN ai_samples s ON s.id = m.sample_id
		WHERE m.workspace_id = $1 AND s.brand_id = $2 AND s.status = 'ok' AND s.sampled_at >= $3 AND ($4::uuid IS NULL OR s.cohort_id = $4::uuid)
		GROUP BY 1, 2`, ws, brand, from, cohortID)
	if err != nil {
		return nil, nil, nil, err
	}
	var entity, name string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&entity, &name, &n}, func() error {
		named[[2]string{entity, name}] = n
		return nil
	}); err != nil {
		return nil, nil, nil, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT c.entity, c.name, c.kind, c.key, c.text, c.sentiment, c.quote, c.sample_id::text, c.prompt_id::text, c.sampled_at >= $3, c.sampled_at,
		       s.model, s.web_search
		FROM ai_claims c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.sampled_at >= $4 AND ($5::uuid IS NULL OR c.cohort_id = $5::uuid) AND s.status = 'ok'
		ORDER BY c.sampled_at DESC`, ws, brand, from, before, cohortID)
	if err != nil {
		return nil, nil, nil, err
	}
	var kind, key, text, sentiment, quote, sample, prompt, model string
	var now, web bool
	var at time.Time
	if _, err := pgx.ForEachRow(rows, []any{&entity, &name, &kind, &key, &text, &sentiment, &quote, &sample, &prompt, &now, &at, &model, &web}, func() error {
		k := gkey{entity, name, kind, key}
		g := groups[k]
		if g == nil {
			g = &aiVisClaim{Key: key, Kind: kind, Assistants: []string{}, Sources: []aiVisClaimSource{},
				texts: map[string]int{}, sentiments: map[string]int{}, assistants: map[string]int{}, sources: map[string]int{}, hosts: map[string]string{}, prompts: map[string]bool{}, samples: map[string]bool{}}
			groups[k] = g
		}
		if !now {
			g.PreviousAnswers++
			return nil
		}
		g.Answers++
		g.texts[text]++
		g.sentiments[sentiment]++
		g.assistants[assistantLabel(model, web, "")]++
		g.prompts[prompt] = true
		g.samples[sample] = true
		if g.Quote == "" && quote != "" {
			g.Quote = quote
		}
		if at.After(g.LastSeenAt) {
			g.LastSeenAt = at
		}
		return nil
	}); err != nil {
		return nil, nil, nil, err
	}
	// The pages the claiming answers cited.
	samples := map[string][]*aiVisClaim{}
	ids := []string{}
	for _, g := range groups {
		for id := range g.samples {
			if samples[id] == nil {
				ids = append(ids, id)
			}
			samples[id] = append(samples[id], g)
		}
	}
	if len(ids) > 0 {
		rows, err = s.pool.Query(ctx, `SELECT sample_id::text, url, host FROM ai_citations WHERE workspace_id = $1 AND sample_id = ANY($2) AND owner <> 'brand'`, ws, ids)
		if err != nil {
			return nil, nil, nil, err
		}
		var url, host string
		if _, err := pgx.ForEachRow(rows, []any{&sample, &url, &host}, func() error {
			for _, g := range samples[sample] {
				g.sources[url]++
				g.hosts[url] = host
			}
			return nil
		}); err != nil {
			return nil, nil, nil, err
		}
	}
	// Open disputes.
	fixes := map[[2]string]string{}
	rows, err = s.pool.Query(ctx, `SELECT f.group_key, c.kind, f.id::text FROM fixes f JOIN ai_claims c ON c.id = f.subject_id AND c.workspace_id = f.workspace_id
		WHERE f.workspace_id = $1 AND f.brand_id = $2 AND f.source = $3 AND f.rule = 'ai_wrong_claim' AND f.status IN ('open', 'in_progress')`, ws, brand, aivis.FixSource)
	if err != nil {
		return nil, nil, nil, err
	}
	var fid string
	if _, err := pgx.ForEachRow(rows, []any{&key, &kind, &fid}, func() error {
		fixes[[2]string{kind, key}] = fid
		return nil
	}); err != nil {
		return nil, nil, nil, err
	}
	top := func(m map[string]int) string {
		best, bn := "", -1
		for k, v := range m {
			if v > bn || (v == bn && k < best) {
				best, bn = k, v
			}
		}
		return best
	}
	finish := func(g *aiVisClaim, entity, name string) {
		g.Text = top(g.texts)
		g.Sentiment = top(g.sentiments)
		g.Questions = len(g.prompts)
		g.Share = rate(g.Answers, named[[2]string{entity, name}])
		for k := range g.assistants {
			g.Assistants = append(g.Assistants, k)
		}
		sort.Slice(g.Assistants, func(i, j int) bool {
			a, b := g.assistants[g.Assistants[i]], g.assistants[g.Assistants[j]]
			return a > b || (a == b && g.Assistants[i] < g.Assistants[j])
		})
		urls := []string{}
		for u := range g.sources {
			urls = append(urls, u)
		}
		sort.Slice(urls, func(i, j int) bool {
			a, b := g.sources[urls[i]], g.sources[urls[j]]
			return a > b || (a == b && urls[i] < urls[j])
		})
		for _, u := range urls[:min(len(urls), maxClaimSources)] {
			g.Sources = append(g.Sources, aiVisClaimSource{URL: u, Host: g.hosts[u]})
		}
		if entity == "brand" {
			if f, ok := fixes[[2]string{g.Kind, g.Key}]; ok {
				g.FixID = &f
			}
		}
	}
	attrs, objs := []aiVisClaim{}, []aiVisClaim{}
	comps := map[string]*aiVisCompetitorClaims{}
	for k, g := range groups {
		if g.Answers == 0 {
			continue // only said in the period before
		}
		finish(g, k.entity, k.name)
		switch {
		case k.entity == "brand" && k.kind == "attribute":
			attrs = append(attrs, *g)
		case k.entity == "brand":
			objs = append(objs, *g)
		default:
			c := comps[k.name]
			if c == nil {
				c = &aiVisCompetitorClaims{Name: k.name, Answers: named[[2]string{"competitor", k.name}], Attributes: []aiVisClaim{}, Objections: []aiVisClaim{}}
				comps[k.name] = c
			}
			if k.kind == "attribute" {
				c.Attributes = append(c.Attributes, *g)
			} else {
				c.Objections = append(c.Objections, *g)
			}
		}
	}
	order := func(list []aiVisClaim, max int) []aiVisClaim {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Answers != list[j].Answers {
				return list[i].Answers > list[j].Answers
			}
			return list[i].Key < list[j].Key
		})
		return list[:min(len(list), max)]
	}
	attrs, objs = order(attrs, maxClaimGroups), order(objs, maxClaimGroups)
	list := []aiVisCompetitorClaims{}
	for _, c := range comps {
		c.Attributes, c.Objections = order(c.Attributes, maxCompetitorClaims), order(c.Objections, maxCompetitorClaims)
		list = append(list, *c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Answers != list[j].Answers {
			return list[i].Answers > list[j].Answers
		}
		return list[i].Name < list[j].Name
	})
	return attrs, objs, list, nil
}

// disputeAIVisibilityClaim says a claim AI answers make about the brand
// isn't true: a fix (ai_wrong_claim) on the latest answer that made it,
// with the pages those answers cite, so someone corrects it at the source.
func (s *Server) disputeAIVisibilityClaim(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Key  string `json:"key"`
		Kind string `json:"kind"`
		Note string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Key = aivis.ClaimKey(req.Key)
	if req.Key == "" || len(req.Key) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "key is the claim's key from the claims list")
		return
	}
	if req.Kind == "" {
		req.Kind = "objection"
	}
	if req.Kind != "attribute" && req.Kind != "objection" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is attribute or objection")
		return
	}
	if len(req.Note) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2000 characters")
		return
	}
	ctx := r.Context()
	var fixID, claimID string
	var answers int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The latest answer that made it, and how many did in the last 28
		// days.
		err := tx.QueryRow(ctx, `
			SELECT c.id::text, (SELECT count(*) FROM ai_claims x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.entity = 'brand' AND x.kind = $3 AND x.key = $4 AND x.sampled_at >= now() - interval '28 days')
			FROM ai_claims c WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.entity = 'brand' AND c.kind = $3 AND c.key = $4
			ORDER BY c.sampled_at DESC LIMIT 1`, a.WorkspaceID, a.Brand, req.Kind, req.Key).Scan(&claimID, &answers)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit, note)
			VALUES ($1, $2, $3, 'ai_claim', $4, 'ai_wrong_claim', 'error', $5, $6, 'answers', $7)
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, note = CASE WHEN EXCLUDED.note <> '' THEN EXCLUDED.note ELSE fixes.note END,
				status = CASE WHEN fixes.status IN ('done', 'ignored') THEN 'open' ELSE fixes.status END,
				confirmed_by = NULL, fixed_at = NULL, updated_at = now()
			RETURNING id::text`, a.WorkspaceID, a.Brand, aivis.FixSource, claimID, req.Key, answers, req.Note).Scan(&fixID)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "ai_claim.disputed", "fix", fixID,
			map[string]any{"brand_id": a.Brand, "kind": req.Kind, "claim_id": claimID, "answers": answers})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no AI answer made this claim about the brand")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fix_id": fixID, "answers": answers})
}
