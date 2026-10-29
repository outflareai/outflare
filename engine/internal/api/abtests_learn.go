package api

// What A/B tests taught: when a test ends, the workspace's own AI model
// writes its learning (what was tried, what happened, what it means), kept
// on the test. The library lists finished tests with their learnings and
// results, searchable, and the AI answers questions over it, citing the
// tests it drew on. Without an AI model the library still lists the tests.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/abtests"
)

// maxAskTests is how many recent finished tests a question is answered
// over.
const maxAskTests = 40

// abLearning is what a finished test taught, written by the AI.
type abLearning struct {
	Headline string   `json:"headline"`
	Learning string   `json:"learning"`
	Tags     []string `json:"tags"`
}

// queueABLearning asks the workspace's AI model, when it has one, to write
// the learning of a test that just ended, in tx. Without a model it does
// nothing: the library still lists the test.
func (s *Server) queueABLearning(ctx context.Context, tx pgx.Tx, p *principal, ws, actorID string, t abTestView) error {
	ok, err := s.hasAIModel(ctx, ws)
	if err != nil || !ok {
		return err
	}
	_, err = queueABAIRun(ctx, tx, p, ws, actorID, "learning", t.BrandID, &t.ID, &t.FlagID, map[string]any{"test_id": t.ID})
	return err
}

// writeABLearning (re)writes a finished test's learning.
func (s *Server) writeABLearning(w http.ResponseWriter, r *http.Request) {
	t, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	if t.Status != "done" {
		writeError(w, r, http.StatusConflict, "conflict", "a learning is written once a test ends")
		return
	}
	if !accessFrom(r.Context()).writes(t.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	s.startABAIRun(w, r, "learning", t.BrandID, &t.ID, &t.FlagID, map[string]any{"test_id": t.ID})
}

// abLibraryItem is a finished test as the library shows it.
type abLibraryItem struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Hypothesis string      `json:"hypothesis"`
	FlagKey    string      `json:"flag_key"`
	BrandID    *string     `json:"brand_id"`
	Control    string      `json:"control"`
	Decision   string      `json:"decision"`
	Winner     string      `json:"winner"`
	Goal       string      `json:"goal"`
	Outcome    string      `json:"outcome"`
	StartedAt  *time.Time  `json:"started_at"`
	EndedAt    *time.Time  `json:"ended_at"`
	Learning   *abLearning `json:"learning"`
}

// loadABLibrary is the workspace's finished tests, newest first, with
// their outcome in a line; q narrows them to those whose name, hypothesis,
// flag or learning mention it.
func (s *Server) loadABLibrary(ctx context.Context, ws, q string, limit int) ([]abLibraryItem, error) {
	where, args := ` AND t.status = 'done'`, []any{}
	if q != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)+"%")
		where += ` AND (t.name ILIKE $2 OR t.hypothesis ILIKE $2 OR f.key ILIKE $2 OR t.learning::text ILIKE $2)`
	}
	list, err := loadABTests(ctx, s.pool, ws, where, args...)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(list, func(a, b abTestView) int {
		if a.EndedAt == nil || b.EndedAt == nil {
			return 0
		}
		return b.EndedAt.Compare(*a.EndedAt)
	})
	out := []abLibraryItem{}
	for _, t := range list {
		if len(out) == limit {
			break
		}
		item := abLibraryItem{ID: t.ID, Name: t.Name, Hypothesis: t.Hypothesis, FlagKey: t.FlagKey, BrandID: t.BrandID, Control: t.Control,
			Decision: t.Decision, StartedAt: t.StartedAt, EndedAt: t.EndedAt, Learning: t.Learning}
		if len(t.Goals) > 0 {
			item.Goal = describeGoal(t.Goals[0])
		}
		var res abtests.Result
		if t.result != nil && json.Unmarshal(t.result, &res) == nil {
			item.Winner = abtests.Winner(res, t.Control)
			item.Outcome = abOutcome(t, res)
		}
		out = append(out, item)
	}
	return out, nil
}

// abOutcome says in a line how a finished test came out on its first goal.
func abOutcome(t abTestView, res abtests.Result) string {
	var visitors int64
	for _, arm := range res.Arms {
		visitors += arm.Visitors
	}
	switch w := abtests.Winner(res, t.Control); {
	case visitors == 0:
		return "No visitors were counted."
	case res.SampleRatio:
		return "No result: the split between versions was broken."
	case w == t.Control:
		return fmt.Sprintf("%s did better than every other version.", t.Control)
	case w != "":
		for _, c := range res.Goals[0].Comparisons {
			if c.Variant == w {
				return fmt.Sprintf("%s won: %+.0f%% (range %+.0f%% to %+.0f%%) against %s.", w, c.Lift*100, c.LiftLow*100, c.LiftHigh*100, t.Control)
			}
		}
	}
	if len(res.Goals) == 0 || len(res.Goals[0].Comparisons) == 0 {
		return "No clear winner."
	}
	best := res.Goals[0].Comparisons[0]
	for _, c := range res.Goals[0].Comparisons[1:] {
		if c.ChanceToBeat > best.ChanceToBeat {
			best = c
		}
	}
	return fmt.Sprintf("No clear winner; %s was %+.0f%% against %s, %.0f%% likely to beat it.", best.Variant, best.Lift*100, t.Control, best.ChanceToBeat*100)
}

// listABLearnings is the library of finished tests and what they taught.
func (s *Server) listABLearnings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "q is at most 200 characters")
		return
	}
	list, err := s.loadABLibrary(r.Context(), a.WorkspaceID, q, 200)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := list[:0]
	for _, item := range list {
		if a.sees(item.BrandID) {
			out = append(out, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

type abAskInput struct {
	Question string  `json:"question"`
	BrandID  *string `json:"brand_id"`
}

// askABTests answers a question over the workspace's finished tests.
func (s *Server) askABTests(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in abAskInput
	if !decode(w, r, &in) {
		return
	}
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" || len(in.Question) > maxABPrompt {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "question is required, up to 2,000 characters, such as \"what have we learned about headlines on the pricing page?\"")
		return
	}
	if in.BrandID != nil {
		if err := s.siteKeyBrand(r.Context(), a.WorkspaceID, in.BrandID); s.allocError(w, r, err) {
			return
		}
	}
	if !a.sees(in.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you are limited to some brands: pick one of your brands")
		return
	}
	s.startABAIRun(w, r, "ask", in.BrandID, nil, nil, in)
}

// ---- Prompts ----

const abLearningSystem = `You write the learning of a finished A/B test for the team's library: what was tried, what happened and what it means for the next test.
Say only what the results support. A test without a clear winner taught that the change made no large difference, not that the versions are equal. Keep the headline under 15 words and the learning under 120 words. Tags are 1 to 5 short lowercase words for what was tested (such as headline, pricing, cta, social-proof, form), never the result.
` + abAISafety

func abLearningSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"headline", "learning", "tags"},
		"properties": map[string]any{"headline": map[string]any{"type": "string"}, "learning": map[string]any{"type": "string"},
			"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}
}

func (s *Server) abLearningPrompt(ctx context.Context, ws string, run abAIRun) (string, func(string) (any, map[string]any, error), error) {
	if run.testID == nil {
		return "", nil, errInvalid("The test this run was for is gone.")
	}
	tests, err := loadABTests(ctx, s.pool, ws, ` AND t.id = $2`, *run.testID)
	if err != nil {
		return "", nil, err
	}
	if len(tests) != 1 || tests[0].Status != "done" || tests[0].result == nil {
		return "", nil, errInvalid("The test this run was for is gone or hasn't ended.")
	}
	t := tests[0]
	var res abtests.Result
	if err := json.Unmarshal(t.result, &res); err != nil {
		return "", nil, err
	}
	var b strings.Builder
	b.WriteString("Write this finished test's learning.\n\nData:\n\n")
	fmt.Fprintf(&b, "## Test\nName: %s\nHypothesis: %s\nFlag: %s\nControl: %s\nVersion kept: %s\nOutcome: %s\n",
		clip(t.Name, 200), clip(t.Hypothesis, 1000), t.FlagKey, t.Control, t.Decision, abOutcome(t, res))
	writeABResults(&b, t, res)
	finish := func(text string) (any, map[string]any, error) {
		var out abLearning
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			return nil, nil, err
		}
		out.Headline, out.Learning = clip(strings.TrimSpace(out.Headline), 200), clip(strings.TrimSpace(out.Learning), 1500)
		if out.Headline == "" || out.Learning == "" {
			return nil, nil, errors.New("learning is missing its headline or text")
		}
		tags := []string{}
		for _, tag := range out.Tags {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if fieldRe.MatchString(strings.ReplaceAll(tag, "-", "_")) && !slices.Contains(tags, tag) && len(tags) < 5 {
				tags = append(tags, tag)
			}
		}
		out.Tags = tags
		raw, _ := json.Marshal(out)
		if _, err := s.pool.Exec(ctx, `UPDATE ab_tests SET learning = $3, learning_at = now() WHERE workspace_id = $1 AND id = $2`, ws, t.ID, raw); err != nil {
			return nil, nil, err
		}
		return out, map[string]any{"tags": len(tags)}, nil
	}
	return b.String(), finish, nil
}

const abAskSystem = `You answer a marketer's question about their company's past A/B tests, using only the tests given.
Cite the tests your answer rests on by their id. When the tests don't answer the question, say so and suggest a test that would. Never generalise beyond what the results show: a test without a clear winner is not evidence that a change doesn't matter. Keep the answer under 200 words.
` + abAISafety

func abAskSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"answer", "test_ids"},
		"properties": map[string]any{"answer": map[string]any{"type": "string"},
			"test_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}
}

func (s *Server) abAskPrompt(ctx context.Context, ws string, run abAIRun) (string, func(string) (any, map[string]any, error), error) {
	var in abAskInput
	if err := json.Unmarshal(run.input, &in); err != nil || in.Question == "" {
		return "", nil, errInvalid("The question is missing.")
	}
	all, err := s.loadABLibrary(ctx, ws, "", 500)
	if err != nil {
		return "", nil, err
	}
	var tests []abLibraryItem
	for _, item := range all {
		// A brand's question draws on its tests and those of shared flags.
		if run.brandID == nil || item.BrandID == nil || *item.BrandID == *run.brandID {
			tests = append(tests, item)
		}
		if len(tests) == maxAskTests {
			break
		}
	}
	var b strings.Builder
	b.WriteString("Answer the question from these finished tests.\n\nData:\n\n## Question\n")
	b.WriteString(clip(in.Question, maxABPrompt))
	fmt.Fprintf(&b, "\n\n## Finished tests (%d, newest first)\n", len(tests))
	if len(tests) == 0 {
		b.WriteString("None yet.\n")
	}
	ids := map[string]bool{}
	for _, item := range tests {
		ids[item.ID] = true
		fmt.Fprintf(&b, "\n### %s\nName: %s\nHypothesis: %s\nFlag: %s\nJudged on: %s\nOutcome: %s\nVersion kept: %s\n",
			item.ID, clip(item.Name, 200), clip(item.Hypothesis, 500), item.FlagKey, item.Goal, item.Outcome, item.Decision)
		if item.Learning != nil {
			fmt.Fprintf(&b, "Learning: %s (tags: %s)\n", clip(item.Learning.Learning, 800), strings.Join(item.Learning.Tags, ", "))
		}
	}
	finish := func(text string) (any, map[string]any, error) {
		var out struct {
			Answer  string   `json:"answer"`
			TestIDs []string `json:"test_ids"`
		}
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			return nil, nil, err
		}
		out.Answer = clip(strings.TrimSpace(out.Answer), 3000)
		if out.Answer == "" {
			return nil, nil, errors.New("the answer is empty")
		}
		cited := []string{}
		for _, id := range out.TestIDs {
			if ids[id] && !slices.Contains(cited, id) {
				cited = append(cited, id)
			}
		}
		return map[string]any{"question": in.Question, "answer": out.Answer, "test_ids": cited, "tests_considered": len(tests)},
			map[string]any{"tests_considered": len(tests), "tests_cited": len(cited)}, nil
	}
	return b.String(), finish, nil
}
