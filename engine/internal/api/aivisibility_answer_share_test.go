package api_test

import (
	"context"
	"testing"
	"time"
)

func TestAIAnswerShareFromStoredRuns(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brandID := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "https://acme.example", "handle": "acme"})["id"].(string)
	empty := owner.must(200, "GET", "/v1/brands/"+brandID+"/ai-answer-share", nil)
	if empty["note"] == "" || empty["totals"].(map[string]any)["answers"] != 0.0 {
		t.Fatalf("empty: %v", empty)
	}

	cohort := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{
		"name": "Proxy buyers", "brand_id": "acme",
		"competitors": []map[string]any{{"name": "Northpeak", "domains": []string{"northpeak.example"}}},
	})
	cid := cohort["id"].(string)
	added := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{
		{"text": "best residential proxies", "topic": "residential", "question_type": "best"},
		{"text": "Acme vs Northpeak", "topic": "compare", "question_type": "versus"},
		{"text": "is Acme legit?"},
	}})
	prompts := added["data"].([]any)
	if len(prompts) != 3 {
		t.Fatalf("prompts: %v", added)
	}
	var pidBest, pidVs, pidLegit string
	for _, row := range prompts {
		m := row.(map[string]any)
		switch m["text"] {
		case "best residential proxies":
			pidBest = m["id"].(string)
		case "Acme vs Northpeak":
			pidVs = m["id"].(string)
		case "is Acme legit?":
			pidLegit = m["id"].(string)
		}
	}
	if pidBest == "" || pidVs == "" || pidLegit == "" {
		t.Fatalf("prompt ids: %v", added)
	}

	var run string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ai_runs (workspace_id, cohort_id, trigger, status) VALUES ($1, $2, 'manual', 'done') RETURNING id::text`, ws, cid).Scan(&run); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().AddDate(0, 0, -3)
	insert := func(prompt, model, at string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO ai_samples (workspace_id, run_id, cohort_id, prompt_id, brand_id, provider, model, status, answer, sampled_at)
			VALUES ($1, $2, $3, $4, $5, 'openai', $6, 'ok', 'an answer', $7) RETURNING id::text`,
			ws, run, cid, prompt, brandID, model, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sBest := insert(pidBest, "gpt-test-1", day.Format(time.RFC3339))
	sVs := insert(pidVs, "gpt-test-2", day.Format(time.RFC3339))
	sLegit := insert(pidLegit, "gpt-test-3", day.AddDate(0, 0, -10).Format(time.RFC3339))

	ment := func(sample, entity, name, stance string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO ai_mentions (sample_id, workspace_id, cohort_id, entity, name, position, count, stance, sampled_at)
			VALUES ($1, $2, $3, $4, $5, 1, 1, $6, $7)`, sample, ws, cid, entity, name, stance, day); err != nil {
			t.Fatal(err)
		}
	}
	ment(sBest, "brand", "Acme", "recommended")
	ment(sBest, "competitor", "Northpeak", "listed")
	ment(sVs, "competitor", "Northpeak", "recommended")
	ment(sLegit, "brand", "Acme", "")

	if _, err := e.pool.Exec(ctx, `INSERT INTO ai_citations (sample_id, workspace_id, cohort_id, url, host, title, position, owner, competitor, sampled_at)
		VALUES ($1, $2, $3, 'https://acme.example/compare/northpeak', 'acme.example', 'Compare', 1, 'brand', '', $4),
		       ($5, $2, $3, 'https://northpeak.example/blog/compare-notes', 'northpeak.example', 'Notes', 1, 'competitor', 'Northpeak', $4)`,
		sVs, ws, cid, day, sBest); err != nil {
		t.Fatal(err)
	}

	owner.must(400, "GET", "/v1/brands/"+brandID+"/ai-answer-share?days=0", nil)
	got := owner.must(200, "GET", "/v1/brands/"+brandID+"/ai-answer-share?days=28", nil)
	if got["note"] == "" || got["days"] != 28.0 {
		t.Fatalf("meta: %v", got)
	}
	tot := got["totals"].(map[string]any)
	// 3 answers; brand named in best+legit = 2; mention rate 2/3 ≈ 0.667.
	// Entity mentions: brand 2 + competitor 2 = 4; SoV 2/4 = 0.5.
	if tot["answers"] != 3.0 || tot["questions"] != 3.0 || tot["mention_rate"] != 0.667 || tot["share_of_voice"] != 0.5 {
		t.Fatalf("totals: %v", tot)
	}
	if tot["recommend_rate"] != 0.333 || tot["citation_rate"] != 0.333 || tot["compare_citation_rate"] != 0.333 || tot["clusters"] != 2.0 {
		t.Fatalf("rates/clusters: %v", tot)
	}
	rivals := tot["competitors"].([]any)
	if len(rivals) != 1 || rivals[0].(map[string]any)["name"] != "Northpeak" || rivals[0].(map[string]any)["answers"] != 2.0 {
		t.Fatalf("competitors: %v", rivals)
	}

	byKey := map[string]map[string]any{}
	for _, row := range got["clusters"].([]any) {
		m := row.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	if byKey["residential"]["named"] != 1.0 || byKey["residential"]["mention_rate"] != 1.0 {
		t.Fatalf("residential: %v", byKey["residential"])
	}
	if byKey["compare"]["named"] != 0.0 || byKey["compare"]["compare_cited"] != 1.0 || byKey["compare"]["compare_citation_rate"] != 1.0 {
		t.Fatalf("compare cluster: %v", byKey["compare"])
	}
	if byKey["untagged"]["named"] != 1.0 || byKey["untagged"]["label"] != "Untagged" {
		t.Fatalf("untagged: %v", byKey["untagged"])
	}

	cmp := got["compare"].(map[string]any)
	if cmp["brand_cited"] != 1.0 || cmp["competitor_cited"] != 0.0 {
		t.Fatalf("blog/compare-notes must not count as a /compare page: %v", cmp)
	}
	pages := cmp["pages"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["url"] != "https://acme.example/compare/northpeak" {
		t.Fatalf("compare pages: %v", pages)
	}

	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	obr := owner.must(200, "GET", "/v1/workspaces/"+other+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	iso := owner.must(200, "GET", "/v1/brands/"+obr+"/ai-answer-share", nil)
	if iso["totals"].(map[string]any)["answers"] != 0.0 {
		t.Fatalf("other brand: %v", iso)
	}
}

func TestAIAnswerShareRejectsBadDays(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	owner.must(400, "GET", "/v1/brands/"+brand+"/ai-answer-share?days=366", nil)
	owner.must(400, "GET", "/v1/brands/"+brand+"/ai-answer-share?days=no", nil)
}
