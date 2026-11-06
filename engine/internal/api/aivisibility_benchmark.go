package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The AI visibility benchmark: after a cohort's run, once a day, the
// workspace's own DataForSEO counts how many of the AI answers it keeps
// (ChatGPT in the US in English, Google's AI Overviews) mention the brand
// and each competitor. Without a demand reader the job does nothing.

// maxBenchmarkCompetitors is how many competitors one benchmark counts
// beside the brand.
const maxBenchmarkCompetitors = 9

func (s *Server) aiBenchmarkJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		CohortID string `json:"cohort_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.CohortID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	cohort, err := aivis.LoadCohort(ctx, s.pool, ws, p.CohortID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	demand := s.openDemand(ctx, ws)
	if demand == nil {
		return nil
	}
	start := time.Now()
	ev := track.Event{Name: "ai_benchmark.read", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"cohort_id": cohort.ID, "brand_id": cohort.BrandID}}
	targets := []connectors.AIBenchmarkTarget{{Key: "brand", Domains: hostsOf(cohort.Brand.Domains), Keywords: append([]string{cohort.Brand.Name}, cohort.Brand.Aliases...)}}
	for _, c := range cohort.Competitors {
		if len(targets) > maxBenchmarkCompetitors {
			break
		}
		targets = append(targets, connectors.AIBenchmarkTarget{Key: "c:" + c.Name, Domains: hostsOf(c.Domains), Keywords: append([]string{c.Name}, c.Aliases...)})
	}
	lang, country := splitCohortLocale(cohort.Locale)
	ref := "ai_benchmark:" + cohort.ID + ":" + start.UTC().Format(time.RFC3339)
	res, err := ranks.AIBenchmark(ctx, s.pool, s.integrations, demand, targets, country, lang, ref)
	if serr := s.tagSpend(ctx, ws, ref, cohort.BrandID); serr != nil {
		return serr
	}
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		ev.Name = "ai_benchmark.failed"
		track.Fail(ctx, s.log, ev, ranks.SearchErrorCode(err), err)
		return nil
	}
	day := start.UTC().Format("2006-01-02")
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, t := range targets {
			n, ok := res.Counts[t.Key]
			if !ok {
				continue
			}
			entity, name := "brand", ""
			if t.Key != "brand" {
				entity, name = "competitor", t.Key[2:]
			}
			rows := map[string]int{"": n.Mentions}
			for p, m := range n.Platforms {
				rows[p] = m
			}
			for platform, mentions := range rows {
				vol := int64(0)
				if platform == "" {
					vol = n.AISearchVolume
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO ai_benchmarks (workspace_id, brand_id, cohort_id, day, entity, name, platform, mentions, ai_search_volume)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
					ON CONFLICT (cohort_id, day, entity, name, platform) DO UPDATE SET mentions = excluded.mentions, ai_search_volume = excluded.ai_search_volume`,
					ws, cohort.BrandID, cohort.ID, day, entity, name, platform, max(mentions, 0), max(vol, 0)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	ev.Props["targets"], ev.Props["duration_ms"] = len(res.Counts), time.Since(start).Milliseconds()
	track.Emit(ctx, ev)
	return nil
}

// aiVisibilityBenchmarks adds each cohort's latest benchmark to its brand
// and competitors, with the change since the count before.
func (s *Server) aiVisibilityBenchmarks(ctx context.Context, ws string, ids []string, byID map[string]*aiVisCohort) error {
	rows, err := s.pool.Query(ctx, `
		WITH days AS (
			SELECT cohort_id, day, dense_rank() OVER (PARTITION BY cohort_id ORDER BY day DESC) AS n
			FROM (SELECT DISTINCT cohort_id, day FROM ai_benchmarks WHERE workspace_id = $1 AND cohort_id = ANY($2)) d)
		SELECT b.cohort_id, to_char(b.day, 'YYYY-MM-DD'), d.n, b.entity, b.name, b.mentions
		FROM ai_benchmarks b JOIN days d ON d.cohort_id = b.cohort_id AND d.day = b.day
		WHERE b.workspace_id = $1 AND b.platform = '' AND d.n <= 2`, ws, ids)
	if err != nil {
		return err
	}
	type k struct{ cohort, entity, name string }
	latest, before := map[k]int{}, map[k]int{}
	totals := map[string]int{}
	var id, day, entity, name string
	var n, mentions int
	if _, err := pgx.ForEachRow(rows, []any{&id, &day, &n, &entity, &name, &mentions}, func() error {
		key := k{id, entity, name}
		if n == 1 {
			latest[key] = mentions
			totals[id] += mentions
			d := day
			byID[id].BenchmarkDay = &d
		} else {
			before[key] = mentions
		}
		return nil
	}); err != nil {
		return err
	}
	set := func(e *aiVisEntity, key k) {
		m, ok := latest[key]
		if !ok {
			return
		}
		e.BenchmarkMentions, e.BenchmarkShare = &m, rate(m, totals[key.cohort])
		if b, ok := before[key]; ok {
			ch := m - b
			e.BenchmarkChange = &ch
		}
	}
	for _, id := range ids {
		c := byID[id]
		set(&c.Brand.aiVisEntity, k{id, "brand", ""})
		seen := map[string]bool{}
		for i := range c.Competitors {
			seen[c.Competitors[i].Name] = true
			set(&c.Competitors[i], k{id, "competitor", c.Competitors[i].Name})
		}
		// Competitors no answer of the cohort's named, but the benchmark counts.
		extra := []string{}
		for key := range latest {
			if key.cohort == id && key.entity == "competitor" && !seen[key.name] {
				extra = append(extra, key.name)
			}
		}
		sort.Strings(extra)
		for _, name := range extra {
			e := aiVisEntity{Name: name, MentionRate: rate(0, c.Answers), RecommendRate: rate(0, c.Answers)}
			set(&e, k{id, "competitor", name})
			c.Competitors = append(c.Competitors, e)
		}
	}
	return nil
}
