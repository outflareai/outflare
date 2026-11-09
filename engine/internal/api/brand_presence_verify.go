package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Presence reads each find once to see whether the page is about the brand
// (verdict on coverage_candidates): it links to one of the brand's domains
// (links), names its website (mentions), only uses its name, which another
// product may share (name_only), doesn't name it at all (absent), or
// couldn't be read (unreachable). Backlinks are links by definition and
// need no read. The read is OutflareBot's bounded, robots-aware page read.

const (
	jobCoverageVerify = "coverage.verify"
	// coverageVerifyBatch is how many finds one run reads; a full batch
	// queues the next run.
	coverageVerifyBatch = 60
)

// enqueueCoverageVerify queues a read of the brand's unchecked finds; key
// makes it once per cause (a discovery day, a search run, the run before).
func enqueueCoverageVerify(ctx context.Context, tx pgx.Tx, ws, brand, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCoverageVerify, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brand},
		IdempotencyKey: "coverage.verify:" + brand + ":" + key, MaxAttempts: 3})
	return err
}

// coverageVerdict decides what a read page says about the brand.
func coverageVerdict(rd *crawl.Read, name *regexp.Regexp, hosts []string) string {
	if rd == nil || rd.Status < 200 || rd.Status >= 300 {
		return "unreachable"
	}
	for _, l := range rd.Links {
		if u, err := url.Parse(l.URL); err == nil && coverageHostIn(u.Hostname(), hosts) {
			return "links"
		}
	}
	text := strings.ToLower(rd.Title + "\n" + rd.Text)
	for _, h := range hosts {
		if h != "" && strings.Contains(text, h) {
			return "mentions"
		}
	}
	if name != nil && name.MatchString(text) {
		return "name_only"
	}
	return "absent"
}

func (s *Server) coverageVerifyJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Brand string `json:"brand_id"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.Brand) {
		return fmt.Errorf("%w: invalid coverage verify job", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	var name, website string
	var domains []string
	var test bool
	err := s.pool.QueryRow(ctx, `SELECT b.name,b.website,b.domains,w.test FROM brands b JOIN workspaces w ON w.id=b.workspace_id WHERE b.workspace_id=$1 AND b.id=$2`, ws, req.Brand).Scan(&name, &website, &domains, &test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	hosts := coverageBrandHosts("", website, domains)
	var nameRe *regexp.Regexp
	if n := strings.ToLower(strings.TrimSpace(name)); n != "" {
		nameRe = regexp.MustCompile(`(^|[^\pL\pN])` + regexp.QuoteMeta(n) + `($|[^\pL\pN])`)
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,url,source FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND status='new' AND verdict='unchecked' AND (recheck_at IS NULL OR recheck_at<=now()) ORDER BY created_at LIMIT $3`, ws, req.Brand, coverageVerifyBatch)
	if err != nil {
		return err
	}
	type find struct{ id, url, source, verdict string }
	var finds []find
	for rows.Next() {
		var f find
		if err = rows.Scan(&f.id, &f.url, &f.source); err != nil {
			rows.Close()
			return err
		}
		finds = append(finds, f)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(finds) == 0 {
		return nil
	}
	var fetch crawl.Fetcher
	var wait func(context.Context, time.Duration) error
	if s.crawler != nil {
		fetch, wait = s.crawler.ReadFetcher(test)
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range finds {
		f := &finds[i]
		if f.source == "backlinks" {
			f.verdict = "links"
			continue
		}
		if fetch == nil || crawl.CheckURL(f.url, false) != nil {
			f.verdict = "unreachable"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			u, _ := url.Parse(f.url)
			if opted, _ := crawl.OptedOut(ctx, s.pool, u.Hostname()); opted {
				f.verdict = "unreachable"
				metrics.CoverageCheck("blocked")
				return
			}
			rd, err := crawl.ReadPage(ctx, fetch, wait, f.url)
			if err != nil {
				rd = nil
			}
			f.verdict = coverageVerdict(rd, nameRe, hosts)
			if f.verdict == "unreachable" {
				metrics.CoverageCheck("unreachable")
			} else {
				metrics.CoverageCheck("up")
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a := &access{WorkspaceID: ws, Brand: req.Brand}
	counts := map[string]int{}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, f := range finds {
			if _, err := tx.Exec(ctx, `UPDATE coverage_candidates SET verdict=$4,checked_at=now(),recheck_at=NULL WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND verdict='unchecked'`, ws, req.Brand, f.id, f.verdict); err != nil {
				return err
			}
			counts[f.verdict]++
		}
		props := map[string]any{"brand_id": req.Brand, "checked": len(finds)}
		for k, v := range counts {
			props[k] = v
		}
		if err := recordWithProps(ctx, tx, coverageActor(a), ws, "coverage_discovery.checked", "brand", req.Brand, nil, props); err != nil {
			return err
		}
		if len(finds) == coverageVerifyBatch {
			return enqueueCoverageVerify(ctx, tx, ws, req.Brand, fmt.Sprint("after:", j.ID))
		}
		return nil
	})
}
