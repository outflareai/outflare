package api

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/reviews"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reading review pages: each brand's review pages (its links of a review
// kind) are read through the workspace's own page reader (Geonode) as soon
// as they're added, then once a day. The first read takes every review the
// page shows, up to a limit, so the counts and Outflare's review score
// have a history from day one; later reads take the newest. Finding pages
// runs a few web searches through the workspace's own Geonode account and
// suggests what matches the brand, to confirm or dismiss like any link.

const (
	jobReviewsRead     = "reviews.read"
	jobReviewsDiscover = "reviews.discover"

	// reviewReadEvery is how often a page is read.
	reviewReadEvery = 24 * time.Hour
	// reviewBackfillPages is how many pages of reviews the first read
	// takes (about 20 a page), and reviewDailyPages how many a daily read
	// takes at most, while every review on a page is new.
	reviewBackfillPages = 25
	reviewDailyPages    = 5
	// reviewAnswerWindow is how far back a negative review without a
	// reply goes on the fixes list.
	reviewAnswerWindow = 90 * 24 * time.Hour
	reviewFixSource    = "reviews"
)

func (s *Server) registerReviewJobs(q *jobs.Queue) {
	q.Handle(jobReviewsRead, s.reviewsReadJob)
	q.Handle(jobReviewsDiscover, s.reviewsDiscoverJob)
	q.Handle(jobReviewDraft, s.reviewDraftJob)
}

// enqueueReviewRead reads the brands' review pages through each page
// reader now, after links change. Pages that aren't due are skipped, so
// this costs only the new ones.
func (s *Server) enqueueReviewRead(ctx context.Context, tx pgx.Tx, ws, key string) error {
	readers, err := s.pageReaders(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, id := range readers {
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobReviewsRead, WorkspaceID: &ws, Payload: map[string]string{"integration_id": id},
			IdempotencyKey: "reviews.read:" + id + ":" + key, MaxAttempts: 3}); err != nil {
			return err
		}
	}
	return nil
}

// webSearchers are the workspace's connected connections that search the
// web (Geonode), first connected first.
func (s *Server) webSearchers(ctx context.Context, q connectors.Querier, ws string) ([]string, error) {
	if s.integrations == nil {
		return nil, nil
	}
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if d.Has(connectors.CapWebSearch) {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id::text FROM integrations WHERE workspace_id = $1 AND status = 'connected' AND connector = ANY($2) ORDER BY created_at`, ws, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// enqueueReviewDiscovery queues searching for a brand's review pages, when
// the workspace has a web search connection. It reports whether it queued.
func (s *Server) enqueueReviewDiscovery(ctx context.Context, tx pgx.Tx, ws, brandID, key string) (bool, error) {
	searchers, err := s.webSearchers(ctx, tx, ws)
	if err != nil || len(searchers) == 0 {
		return false, err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobReviewsDiscover, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brandID},
		IdempotencyKey: "reviews.discover:" + brandID + ":" + key, MaxAttempts: 3})
	return err == nil, err
}

// QueueDueReviewReads queues a read through each page reader in a
// workspace whose brands link to a review page, once an hour. A read only
// reads the pages that are due, so this costs nothing until one is.
func (s *Server) QueueDueReviewReads(ctx context.Context) (int, error) {
	_, readers := s.socialMetricsConnectors()
	if len(readers) == 0 {
		return 0, nil
	}
	hour := "h" + strconv.FormatInt(time.Now().Unix()/3600, 10)
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT i.workspace_id::text, i.id::text FROM integrations i
			WHERE i.connector = ANY($1) AND i.status = 'connected'
			  AND (EXISTS (SELECT 1 FROM brands b, jsonb_array_elements(b.links) l
				WHERE b.workspace_id = i.workspace_id AND b.status = 'active' AND l->>'kind' = ANY($2))
			   OR EXISTS (SELECT 1 FROM review_pages p WHERE p.workspace_id = i.workspace_id AND p.status = 'active'))
			LIMIT 500`, readers, reviews.Platforms)
		if err != nil {
			return err
		}
		type due struct{ ws, id string }
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.id}, func() error { list = append(list, d); return nil }); err != nil {
			return err
		}
		for _, d := range list {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobReviewsRead, WorkspaceID: &d.ws, Payload: map[string]string{"integration_id": d.id},
				IdempotencyKey: "reviews.read:" + d.id + ":" + hour, MaxAttempts: 3}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func (s *Server) reviewsReadJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "integration_id")
	if err != nil {
		return err
	}
	return s.ReadReviewPages(ctx, ws, id)
}

// syncReviewPages makes a review page of every review link in the brands'
// links, and marks the pages no longer linked removed (their reviews stay).
func (s *Server) syncReviewPages(ctx context.Context, ws string) error {
	rows, err := s.pool.Query(ctx, `
		SELECT b.id::text, l->>'kind', l->>'url' FROM brands b, jsonb_array_elements(b.links) l
		WHERE b.workspace_id = $1 AND b.status = 'active' AND l->>'kind' = ANY($2)
		ORDER BY b.created_at`, ws, reviews.Platforms)
	if err != nil {
		return err
	}
	type link struct{ brand, kind, url string }
	var links []link
	var l link
	if _, err := pgx.ForEachRow(rows, []any{&l.brand, &l.kind, &l.url}, func() error { links = append(links, l); return nil }); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		keep := []string{}
		for _, l := range links {
			p, ok := reviews.ParsePage(l.url)
			if !ok || p.Platform != l.kind {
				continue
			}
			var id string
			var inserted bool
			if err := tx.QueryRow(ctx, `
				INSERT INTO review_pages (workspace_id, brand_id, platform, external_id, url)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (workspace_id, platform, external_id) DO UPDATE SET brand_id = EXCLUDED.brand_id, url = EXCLUDED.url,
					status = CASE WHEN review_pages.status = 'removed' THEN 'active' ELSE review_pages.status END,
					next_read_at = CASE WHEN review_pages.status = 'removed' THEN now() ELSE review_pages.next_read_at END,
					updated_at = now()
				RETURNING id::text, xmax = 0`, ws, l.brand, p.Platform, truncate(p.ID, 500), truncate(p.URL, 2000)).Scan(&id, &inserted); err != nil {
				return err
			}
			keep = append(keep, id)
			if inserted {
				if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review_page.added", TargetType: "review_page",
					TargetID: id, Data: map[string]any{"platform": p.Platform, "brand_id": l.brand}}); err != nil {
					return err
				}
			}
		}
		gone, err := tx.Query(ctx, `UPDATE review_pages SET status = 'removed', updated_at = now()
			WHERE workspace_id = $1 AND status <> 'removed' AND NOT (id::text = ANY($2)) RETURNING id::text, platform`, ws, keep)
		if err != nil {
			return err
		}
		type removed struct{ id, platform string }
		var list []removed
		var x removed
		if _, err := pgx.ForEachRow(gone, []any{&x.id, &x.platform}, func() error { list = append(list, x); return nil }); err != nil {
			return err
		}
		for _, x := range list {
			if err := syncReviewFixes(ctx, tx, ws, x.id); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review_page.removed", TargetType: "review_page",
				TargetID: x.id, Data: map[string]any{"platform": x.platform}}); err != nil {
				return err
			}
		}
		return nil
	})
}

type dueReviewPage struct {
	id, platform, url string
	brand             *string
	backfilled        bool
}

// ReadReviewPages reads the review pages that are due through one page
// reader. One page's failure doesn't stop the others; an outage is retried.
func (s *Server) ReadReviewPages(ctx context.Context, ws, integrationID string) error {
	if s.integrations == nil {
		return nil
	}
	if err := s.syncReviewPages(ctx, ws); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, platform, url, brand_id::text, backfilled_at IS NOT NULL FROM review_pages
		WHERE workspace_id = $1 AND status = 'active' AND next_read_at <= now() + interval '1 minute' ORDER BY next_read_at LIMIT 200`, ws)
	if err != nil {
		return err
	}
	var due []dueReviewPage
	var d dueReviewPage
	if _, err := pgx.ForEachRow(rows, []any{&d.id, &d.platform, &d.url, &d.brand, &d.backfilled}, func() error { due = append(due, d); return nil }); err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	ev := track.Event{Name: "review_page.read_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"integration_id": integrationID}}
	if err != nil {
		track.Fail(ctx, s.log, ev, "connector_"+connectors.KindOf(err), err)
		if k := connectors.KindOf(err); k == connectors.ErrUnavailable || k == connectors.ErrQuota {
			return err
		}
		return nil
	}
	f, ok := o.Connector.(connectors.PageFetcher)
	if !ok {
		return nil
	}
	var outage error
	for _, p := range due {
		if err := s.readReviewPage(ctx, ws, o, f, p); err != nil {
			k := connectors.KindOf(err)
			if k == connectors.ErrUnavailable || k == connectors.ErrQuota {
				outage = err
				if k == connectors.ErrQuota {
					break
				}
			}
		}
	}
	return outage
}

// readReviewPage reads one review page and keeps what it found.
func (s *Server) readReviewPage(ctx context.Context, ws string, o *connectors.Opened, f connectors.PageFetcher, p dueReviewPage) error {
	start := time.Now()
	ev := track.Event{Name: "review_page.read", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"page_id": p.id, "platform": p.platform, "backfill": !p.backfilled}}
	page, _ := reviews.ParsePage(p.url)
	page.Platform = p.platform
	limit := reviewDailyPages
	if !p.backfilled {
		limit = reviewBackfillPages
	}
	var first reviews.Read
	var found []reviews.Review
	tokens := 0.0
	known, seen := map[string]bool{}, map[string]bool{}
	if p.backfilled {
		rows, err := s.pool.Query(ctx, `SELECT external_id FROM reviews WHERE page_id = $1 ORDER BY posted_at DESC LIMIT 500`, p.id)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			known[id] = true
		}
	}
	fail := func(code string, err error) error {
		ev.Name = "review_page.read_failed"
		ev.Props["tokens"] = tokens
		track.Fail(ctx, s.log, ev, code, err)
		if code != "not_a_review_page" {
			code = "connector_error"
		}
		metrics.ReviewPageRead(p.platform, code, 0)
		_, uerr := s.pool.Exec(ctx, `UPDATE review_pages SET last_error = $3, updated_at = now(),
			next_read_at = CASE WHEN $4 THEN now() + interval '1 hour' ELSE now() + make_interval(secs => $5) END
			WHERE workspace_id = $1 AND id = $2`, ws, p.id, truncate(err.Error(), 500), connectors.KindOf(err) == connectors.ErrUnavailable, reviewReadEvery.Seconds())
		if uerr != nil {
			return uerr
		}
		return err
	}
	now := time.Now()
	for n := 1; n <= limit; n++ {
		res, err := f.FetchPage(ctx, o.Client, o.Creds, reviews.PageURL(page, n), reviews.RenderJS(p.platform))
		tokens += res.Cost.Units
		if err != nil {
			if n > 1 {
				break // keep what the first pages gave
			}
			return fail("connector_"+connectors.KindOf(err), err)
		}
		r := reviews.Parse(p.platform, res.HTML, now)
		if n == 1 {
			if !r.Found() {
				return fail("not_a_review_page", errors.New("the page didn't show any reviews or a rating: it may be behind a login or a challenge, or the address may be wrong"))
			}
			first = r
		}
		fresh := 0
		for _, x := range r.Reviews {
			if seen[x.ExternalID] {
				continue
			}
			seen[x.ExternalID] = true
			if !known[x.ExternalID] {
				fresh++
			}
			known[x.ExternalID] = true
			found = append(found, x)
		}
		// A daily read stops at the first page with a review it has, and
		// any read stops at a page with nothing new (a site that ignores
		// the page number).
		if !r.More || fresh == 0 || (p.backfilled && fresh < len(r.Reviews)) {
			break
		}
	}
	added, replied, err := s.saveReviews(ctx, ws, p, first, found, !p.backfilled)
	if err != nil {
		return err
	}
	metrics.ReviewPageRead(p.platform, "success", added)
	ev.Props["reviews"], ev.Props["new"], ev.Props["replies"], ev.Props["tokens"] = len(found), added, replied, tokens
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	track.Emit(ctx, ev)
	return nil
}

// saveReviews keeps a page's reviews and its numbers for today, and
// reports how many reviews and replies were new. A first read records one
// event for the whole history, not one per review.
func (s *Server) saveReviews(ctx context.Context, ws string, p dueReviewPage, head reviews.Read, found []reviews.Review, backfill bool) (int, int, error) {
	added, replied := 0, 0
	ids := make([]string, 0, len(found))
	for _, x := range found {
		ids = append(ids, truncate(x.ExternalID, 500))
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Which of these were kept before, and whether they had a reply.
		rows, err := tx.Query(ctx, `SELECT external_id, reply_body <> '' FROM reviews WHERE page_id = $1 AND external_id = ANY($2)`, p.id, ids)
		if err != nil {
			return err
		}
		hadReply := map[string]bool{}
		var ext string
		var had bool
		if _, err := pgx.ForEachRow(rows, []any{&ext, &had}, func() error { hadReply[ext] = had; return nil }); err != nil {
			return err
		}
		for _, x := range found {
			if x.PostedAt.IsZero() || x.Rating < 1 {
				continue
			}
			key := truncate(x.ExternalID, 500)
			had, existed := hadReply[key]
			newReply := x.Reply != "" && existed && !had
			repliedAt := x.RepliedAt
			if x.Reply != "" && repliedAt == nil && (newReply || (!existed && !backfill)) {
				t := time.Now()
				repliedAt = &t
			}
			var id string
			err := tx.QueryRow(ctx, `
				INSERT INTO reviews (workspace_id, brand_id, page_id, platform, external_id, url, rating, rating_native, rating_scale, title, body,
					language, author_name, author_country, verified, posted_at, edited_at, sentiment, reply_body, replied_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
				ON CONFLICT (page_id, external_id) DO UPDATE SET url = EXCLUDED.url, rating = EXCLUDED.rating, rating_native = EXCLUDED.rating_native,
					title = EXCLUDED.title, body = EXCLUDED.body, edited_at = COALESCE(EXCLUDED.edited_at, reviews.edited_at), verified = EXCLUDED.verified,
					sentiment = CASE WHEN reviews.sentiment_source = 'rating' THEN EXCLUDED.sentiment ELSE reviews.sentiment END,
					reply_body = CASE WHEN EXCLUDED.reply_body <> '' THEN EXCLUDED.reply_body ELSE reviews.reply_body END,
					replied_at = COALESCE(reviews.replied_at, EXCLUDED.replied_at),
					brand_id = EXCLUDED.brand_id, state = 'live', last_seen_at = now(), updated_at = now()
				RETURNING id::text`,
				ws, p.brand, p.id, p.platform, key, truncate(x.URL, 2000), x.Rating, x.RatingNative, x.RatingScale,
				x.Title, x.Body, truncate(x.Language, 20), x.Author, truncate(x.Country, 10), x.Verified, x.PostedAt, x.EditedAt,
				reviews.Sentiment(x.Rating), x.Reply, repliedAt).Scan(&id)
			if err != nil {
				return err
			}
			if !existed {
				added++
				hadReply[key] = x.Reply != ""
			}
			if !existed && !backfill {
				if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review.posted", TargetType: "review", TargetID: id,
					Data: map[string]any{"page_id": p.id, "platform": p.platform, "brand_id": deref(p.brand), "rating": x.Rating, "sentiment": reviews.Sentiment(x.Rating), "verified": x.Verified}}); err != nil {
					return err
				}
			}
			if newReply {
				replied++
				if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review.replied", TargetType: "review", TargetID: id,
					Data: map[string]any{"page_id": p.id, "platform": p.platform, "via": "platform"}}); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE review_pages SET score = COALESCE($3, score), review_count = COALESCE($4, review_count),
			name = COALESCE(NULLIF($5, ''), name), last_read_at = now(), last_error = NULL, backfilled_at = COALESCE(backfilled_at, now()),
			next_read_at = now() + make_interval(secs => $6), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, ws, p.id, head.Score, head.Count, truncate(head.Name, 300), reviewReadEvery.Seconds()); err != nil {
			return err
		}
		if backfill {
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review_page.backfilled", TargetType: "review_page", TargetID: p.id,
				Data: map[string]any{"platform": p.platform, "reviews": added}}); err != nil {
				return err
			}
		}
		if err := saveReviewDay(ctx, tx, ws, p, head); err != nil {
			return err
		}
		return syncReviewFixes(ctx, tx, ws, p.id)
	})
	return added, replied, err
}

// pageRatings are a page's (or several pages') reviews' stars and dates.
func pageRatings(ctx context.Context, q pgx.Tx, ws string, pageIDs []string) ([]reviews.Rated, error) {
	rows, err := q.Query(ctx, `SELECT rating::float8, posted_at FROM reviews WHERE workspace_id = $1 AND page_id::text = ANY($2) AND state = 'live'`, ws, pageIDs)
	if err != nil {
		return nil, err
	}
	var out []reviews.Rated
	var x reviews.Rated
	_, err = pgx.ForEachRow(rows, []any{&x.Stars, &x.At}, func() error { out = append(out, x); return nil })
	return out, err
}

// saveReviewDay writes today's row for a page, and records a change in
// its momentum.
func saveReviewDay(ctx context.Context, tx pgx.Tx, ws string, p dueReviewPage, head reviews.Read) error {
	list, err := pageRatings(ctx, tx, ws, []string{p.id})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var score *int
	if sc, ok := reviews.ScoreAt(list, now, reviews.HalfLife); ok {
		score = &sc.Score
	}
	m := reviews.MomentumAt(list, now, reviews.HalfLife)
	var prev *string
	if err := tx.QueryRow(ctx, `SELECT momentum FROM review_page_days WHERE page_id = $1 AND day < (now() AT TIME ZONE 'UTC')::date
		ORDER BY day DESC LIMIT 1`, p.id).Scan(&prev); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO review_page_days (workspace_id, page_id, brand_id, day, score, review_count, new_reviews, stars_1, stars_2, stars_3, stars_4, stars_5,
			outflare_score, momentum)
		SELECT $1, $2, $3, (now() AT TIME ZONE 'UTC')::date, $4, $5,
			count(*) FILTER (WHERE (r.posted_at AT TIME ZONE 'UTC')::date = (now() AT TIME ZONE 'UTC')::date),
			count(*) FILTER (WHERE round(r.rating) <= 1), count(*) FILTER (WHERE round(r.rating) = 2), count(*) FILTER (WHERE round(r.rating) = 3),
			count(*) FILTER (WHERE round(r.rating) = 4), count(*) FILTER (WHERE round(r.rating) >= 5), $6, $7
		FROM reviews r WHERE r.workspace_id = $1 AND r.page_id = $2 AND r.state = 'live'
		ON CONFLICT (page_id, day) DO UPDATE SET score = COALESCE(EXCLUDED.score, review_page_days.score),
			review_count = COALESCE(EXCLUDED.review_count, review_page_days.review_count), new_reviews = EXCLUDED.new_reviews,
			stars_1 = EXCLUDED.stars_1, stars_2 = EXCLUDED.stars_2, stars_3 = EXCLUDED.stars_3, stars_4 = EXCLUDED.stars_4, stars_5 = EXCLUDED.stars_5,
			outflare_score = EXCLUDED.outflare_score, momentum = EXCLUDED.momentum, brand_id = EXCLUDED.brand_id, updated_at = now()`,
		ws, p.id, p.brand, head.Score, head.Count, score, m.State); err != nil {
		return err
	}
	if prev != nil && *prev != m.State && *prev != reviews.TooFew {
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "review_momentum.changed", TargetType: "review_page", TargetID: p.id,
			Data: map[string]any{"platform": p.platform, "brand_id": deref(p.brand), "from": *prev, "to": m.State, "score": m.Score, "score_change": m.ScoreChange}})
	}
	return nil
}

// syncReviewFixes keeps one fix per review page while it has negative
// reviews from the last 90 days without a reply.
func syncReviewFixes(ctx context.Context, tx pgx.Tx, ws, pageID string) error {
	_, err := tx.Exec(ctx, `
		WITH p AS (SELECT id, brand_id, status FROM review_pages WHERE id = $2 AND workspace_id = $1),
		n AS (SELECT 'reviews_to_answer' AS rule, 'warning' AS severity, 'reviews' AS unit,
			(SELECT count(*) FROM reviews r, p WHERE r.page_id = p.id AND p.status = 'active' AND r.state = 'live' AND r.sentiment = 'negative'
				AND r.replied_at IS NULL AND r.posted_at > now() - make_interval(secs => $4))::int AS cnt),
		up AS (
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit)
			SELECT $1, p.brand_id, $3, 'review_page', p.id, n.rule, n.severity, '', n.cnt, n.unit FROM p, n WHERE n.cnt > 0
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, brand_id = EXCLUDED.brand_id,
				status = CASE WHEN fixes.status = 'done' THEN 'open' ELSE fixes.status END,
				fixed_at = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.fixed_at END,
				updated_at = now()
			RETURNING rule)
		UPDATE fixes f SET count = 0, status = CASE WHEN f.status = 'ignored' THEN f.status ELSE 'done' END,
			fixed_at = CASE WHEN f.status = 'ignored' THEN f.fixed_at ELSE now() END, updated_at = now()
		FROM n WHERE f.workspace_id = $1 AND f.source = $3 AND f.subject_id = $2 AND f.rule = n.rule AND n.cnt = 0
		  AND (f.count <> 0 OR f.status IN ('open', 'in_progress'))`, ws, pageID, reviewFixSource, reviewAnswerWindow.Seconds())
	return err
}

func (s *Server) reviewsDiscoverJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "brand_id")
	if err != nil {
		return err
	}
	return s.DiscoverReviewPages(ctx, ws, id)
}

// DiscoverReviewPages searches the web for a brand's review pages through
// the workspace's own web search connection (Geonode) and suggests the
// ones that match the brand. Nothing is added on its own.
func (s *Server) DiscoverReviewPages(ctx context.Context, ws, brandID string) error {
	start := time.Now()
	ev := track.Event{Name: "review_pages.discovered", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": brandID}}
	var name, website string
	var links []brandLink
	err := s.pool.QueryRow(ctx, `SELECT name, website, links FROM brands WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, ws, brandID).Scan(&name, &website, &links)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(code string, err error) error {
		ev.Name = "review_pages.discovery_failed"
		track.Fail(ctx, s.log, ev, code, err)
		if k := connectors.KindOf(err); k == connectors.ErrUnavailable || k == connectors.ErrQuota {
			return err
		}
		return nil
	}
	searchers, err := s.webSearchers(ctx, s.pool, ws)
	if err != nil {
		return err
	}
	if len(searchers) == 0 {
		return fail("connector_needed", errors.New("no web search connection"))
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, searchers[0])
	if err != nil {
		return fail("connector_"+connectors.KindOf(err), err)
	}
	ws2, ok := o.Connector.(connectors.WebSearcher)
	if !ok {
		return nil
	}
	have := map[string]bool{}
	for _, l := range links {
		if reviews.IsPlatform(l.Kind) {
			have[l.Kind] = true
		}
	}
	type hit struct{ kind, url, query string }
	var hits []hit
	searches, cost := 0, 0.0
	queries := reviews.SearchQueries(name, website)
	for _, platform := range reviews.Platforms {
		q, ok := queries[platform]
		if !ok || have[platform] {
			continue
		}
		res, err := ws2.WebSearch(ctx, o.Client, o.Creds, connectors.WebQuery{Query: q})
		searches++
		cost += res.Cost.Units
		if err != nil {
			ev.Props["searches"], ev.Props["tokens"] = searches, cost
			return fail("connector_"+connectors.KindOf(err), err)
		}
		for _, r := range res.Results {
			p, ok := reviews.ParsePage(r.URL)
			if !ok || p.Platform != platform || !reviews.Matches(p, name, website) {
				continue
			}
			hits = append(hits, hit{p.Platform, p.URL, q})
			break // the best match per platform
		}
	}
	suggested := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, h := range hits {
			if slices.ContainsFunc(links, func(b brandLink) bool { return sameLink(b.URL, h.url) }) {
				continue
			}
			tag, err := tx.Exec(ctx, `INSERT INTO brand_link_suggestions (workspace_id, brand_id, kind, url, found_on)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (brand_id, url) DO NOTHING`, ws, brandID, h.kind, truncate(h.url, 2000), "search")
			if err != nil {
				return err
			}
			suggested += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return err
	}
	ev.Props["searches"], ev.Props["found"], ev.Props["suggested"], ev.Props["tokens"] = searches, len(hits), suggested, cost
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	track.Emit(ctx, ev)
	return nil
}

// reviewKinds lists the review link kinds for messages.
func reviewKinds() string {
	names := make([]string, 0, len(reviews.Platforms))
	for _, p := range reviews.Platforms {
		names = append(names, reviews.Names[p])
	}
	return strings.Join(names, ", ")
}
