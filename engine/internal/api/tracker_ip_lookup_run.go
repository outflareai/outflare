package api

// The IP lookup worker (plans/ad-tracker.md, B7c): every few seconds it
// claims the kept click addresses that are due (flagged or sampled clicks,
// and clicks that converted), looks each one up on its brand's own ip.score
// connection, and turns the answer into fraud reasons on the click and its
// conversions. The address goes as soon as it is looked up (or skipped), so
// only the score stays. Never in the redirect: a click goes on at once.
//
// A conversion's postback to its traffic source waits for its click's
// lookup (enqueueSourcePostback), so a network never learns from a click
// the lookup flags; the worker queues it once the lookup is done, skipped
// or given up.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/fraud"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// ipLookupBatch is how many addresses one pass claims.
	ipLookupBatch = 100
	// ipLookupConcurrency is how many lookups run at once.
	ipLookupConcurrency = 8
	// ipLookupLease is how long a claimed address waits before another
	// worker may take it, if this one stops half-way.
	ipLookupLease = 5 * time.Minute
	// ipLookupAttempts is how many times a lookup that failed for a
	// passing reason (the provider down, out of credits) is tried.
	ipLookupAttempts = 3
	ipLookupTimeout  = 15 * time.Second
)

// ipLookupRetry is the wait before a failed lookup's next try, by the
// number of tries so far.
func ipLookupRetry(attempts int) time.Duration {
	if attempts <= 1 {
		return time.Minute
	}
	return 5 * time.Minute
}

// RunIPLookups looks up due click addresses every interval, at once again
// while a pass finds a full batch, until ctx ends.
func (s *Server) RunIPLookups(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := s.LookUpDueAddresses(ctx)
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "tracker.ip_lookups_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		if n == ipLookupBatch && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type dueAddress struct {
	key, ws, brand, ip, ua string
	attempts               int
}

// ipLookupBrand is what one pass knows of a brand: its settings, its
// connection, and how many lookups it has left this month.
type ipLookupBrand struct {
	settings ipLookupSettings
	opened   *connectors.Opened
	scorer   connectors.IPScorer
	// off says the brand's lookups are off or can't run (no connection,
	// one that can't score): its addresses go without a lookup.
	off bool
	// paused says the provider refused this pass (a wrong key, no
	// credits): the brand's other addresses wait for a later try.
	paused bool
	left   int
}

// LookUpDueAddresses claims one batch of due addresses and looks them up,
// and reports how many it claimed.
func (s *Server) LookUpDueAddresses(ctx context.Context) (int, error) {
	var waiting int
	var oldest float64
	if err := s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(EXTRACT(EPOCH FROM now() - min(next_at)), 0)::float8
		FROM (SELECT next_at FROM tracker_click_addresses WHERE look_up AND next_at <= now() LIMIT 100000) d`).Scan(&waiting, &oldest); err != nil {
		return 0, err
	}
	metrics.TrackerIPLookupBacklog(waiting, time.Duration(oldest*float64(time.Second)))
	if waiting == 0 {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `UPDATE tracker_click_addresses a SET next_at = now() + make_interval(secs => $2), attempts = a.attempts + 1
		WHERE a.click_key IN (SELECT click_key FROM tracker_click_addresses WHERE look_up AND next_at <= now() ORDER BY next_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING a.click_key, a.workspace_id::text, a.brand_id::text, host(a.ip), a.user_agent, a.attempts`, ipLookupBatch, ipLookupLease.Seconds())
	if err != nil {
		return 0, err
	}
	var due []dueAddress
	var d dueAddress
	if _, err := pgx.ForEachRow(rows, []any{&d.key, &d.ws, &d.brand, &d.ip, &d.ua, &d.attempts}, func() error {
		due = append(due, d)
		return nil
	}); err != nil {
		return 0, err
	}
	brands := map[string]*ipLookupBrand{}
	for _, d := range due {
		if brands[d.brand] == nil {
			b, err := s.ipLookupBrand(ctx, d.ws, d.brand)
			if err != nil {
				return len(due), err
			}
			brands[d.brand] = b
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	sem := make(chan struct{}, ipLookupConcurrency)
	for _, d := range due {
		b := brands[d.brand]
		mu.Lock()
		action := "look_up"
		switch {
		case b.off:
			action = "off"
		case b.paused:
			action = "wait"
		case b.left <= 0:
			action = "capped"
		default:
			b.left--
		}
		mu.Unlock()
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			var err error
			switch action {
			case "off", "capped":
				err = s.finishIPLookup(ctx, d, action, nil)
			case "wait":
				err = s.retryIPLookup(ctx, d, "")
			default:
				err = s.lookUpAddress(ctx, d, b, &mu)
			}
			if err != nil && ctx.Err() == nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return len(due), firstErr
}

// ipLookupBrand loads a brand's settings, opens its connection and counts
// what it has left of this month's cap (the UTC month).
func (s *Server) ipLookupBrand(ctx context.Context, ws, brand string) (*ipLookupBrand, error) {
	v, err := s.loadIPLookups(ctx, s.pool, ws, brand)
	if err != nil {
		return nil, err
	}
	b := &ipLookupBrand{settings: v}
	if !v.Enabled || v.IntegrationID == nil || s.integrations == nil {
		b.off = true
		return b, nil
	}
	used, err := s.ipLookupsUsed(ctx, ws, brand)
	if err != nil {
		return nil, err
	}
	b.left = v.MonthlyCap - used
	o, err := s.integrations.Open(ctx, s.pool, ws, *v.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		b.off = true
		return b, nil
	}
	if err != nil {
		// A connection that can't be opened now (a refresh failed) is
		// tried again later, like a provider that is down.
		b.paused = true
		track.Fail(ctx, s.log, track.Event{Name: "tracker.ip_lookup_failed", WorkspaceID: ws, ActorType: "system", Surface: "system",
			Props: map[string]any{"brand_id": brand}}, ipLookupErrorCode(err), err)
		return b, nil
	}
	scorer, ok := o.Connector.(connectors.IPScorer)
	if !ok || !o.Connector.Definition().Has(connectors.CapIPScore) {
		b.off = true
		return b, nil
	}
	b.opened, b.scorer = o, scorer
	return b, nil
}

// lookUpAddress looks one address up and keeps the answer.
func (s *Server) lookUpAddress(ctx context.Context, d dueAddress, b *ipLookupBrand, mu *sync.Mutex) error {
	cctx, cancel := context.WithTimeout(ctx, ipLookupTimeout)
	start := time.Now()
	score, err := b.scorer.ScoreIP(cctx, b.opened.Client, b.opened.Creds, d.ip, d.ua)
	cancel()
	metrics.TrackerIPLookupSeconds(time.Since(start))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		kind := connectors.KindOf(err)
		track.Fail(ctx, s.log, track.Event{Name: "tracker.ip_lookup_failed", WorkspaceID: d.ws, ActorType: "system", Surface: "system",
			Props: map[string]any{"brand_id": d.brand, "attempt": d.attempts, "kind": kind}}, ipLookupErrorCode(err), err)
		mu.Lock()
		b.left++ // not spent
		if kind == connectors.ErrAuth || kind == connectors.ErrQuota {
			b.paused = true
		}
		mu.Unlock()
		if kind == connectors.ErrAuth {
			// A refused key marks the connection broken, so the workspace
			// sees it under Connectors.
			_ = s.integrations.Checked(ctx, s.pool, d.ws, b.opened.ID, nil, err)
		}
		if kind == connectors.ErrInvalid || d.attempts >= ipLookupAttempts {
			return s.finishIPLookup(ctx, d, "failed_"+kind, nil)
		}
		return s.retryIPLookup(ctx, d, "retried")
	}
	return s.finishIPLookup(ctx, d, "scored", &ipLookupAnswer{score: score, threshold: b.settings.Threshold})
}

type ipLookupAnswer struct {
	score     connectors.IPScore
	threshold int
}

// reasons are the fraud reasons an answer adds: a score at or over the
// brand's threshold, and a hosting network the provider knows of.
func (a ipLookupAnswer) reasons() []string {
	var out []string
	if a.score.Score != nil && *a.score.Score >= a.threshold {
		out = append(out, fraud.IPReputation)
	}
	if a.score.Hosting {
		out = append(out, fraud.DatacenterNetwork)
	}
	return out
}

func ipLookupErrorCode(err error) string { return "connector_" + connectors.KindOf(err) }

// retryIPLookup leaves an address for a later try.
func (s *Server) retryIPLookup(ctx context.Context, d dueAddress, outcome string) error {
	wait := ipLookupRetry(d.attempts)
	if outcome == "" {
		// Not tried (the brand's provider refused another lookup this
		// pass): this pass doesn't count as a try.
		_, err := s.pool.Exec(ctx, `UPDATE tracker_click_addresses SET attempts = greatest(attempts - 1, 0), next_at = now() + make_interval(secs => $2)
			WHERE click_key = $1`, d.key, wait.Seconds())
		return err
	}
	metrics.TrackerIPLookup(outcome)
	_, err := s.pool.Exec(ctx, `UPDATE tracker_click_addresses SET next_at = now() + make_interval(secs => $2) WHERE click_key = $1`, d.key, wait.Seconds())
	return err
}

// finishIPLookup ends an address's lookup: with an answer, its result is
// kept and its reasons added to the click and its conversions. Either way
// the address goes, and the click's conversions' postbacks, which waited
// for it, are queued.
func (s *Server) finishIPLookup(ctx context.Context, d dueAddress, outcome string, ans *ipLookupAnswer) error {
	var added []string
	var convs int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		added, convs = nil, 0
		tag, err := tx.Exec(ctx, `DELETE FROM tracker_click_addresses WHERE click_key = $1 AND workspace_id = $2`, d.key, d.ws)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // lookups were turned off meanwhile, which queued its postbacks
		}
		if ans != nil {
			reasons := ans.reasons()
			var have []string
			err := tx.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE workspace_id = $1 AND click_key = $2`, d.ws, d.key).Scan(&have)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			for _, r := range reasons {
				if !slices.Contains(have, r) {
					added = append(added, r)
				}
			}
			if added == nil {
				added = []string{}
			}
			sc := ans.score
			var score *int16
			if sc.Score != nil {
				v := int16(min(max(*sc.Score, 0), 100))
				score = &v
			}
			if _, err := tx.Exec(ctx, `INSERT INTO tracker_ip_lookups (workspace_id, brand_id, click_key, score, proxy, vpn, tor, hosting, crawler, flagged)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, d.ws, d.brand, d.key, score, sc.Proxy, sc.VPN, sc.Tor, sc.Hosting, sc.Crawler, added); err != nil {
				return err
			}
			if len(added) > 0 {
				if _, err := tx.Exec(ctx, `UPDATE tracking_link_clicks SET fraud_reasons = fraud_reasons || $3::text[]
					WHERE workspace_id = $1 AND click_key = $2`, d.ws, d.key, added); err != nil {
					return err
				}
				// The click's conversions inherit its reasons, as they do
				// when the click was flagged before they came.
				tag, err := tx.Exec(ctx, `UPDATE tracker_conversions SET fraud_reasons = fraud_reasons || ARRAY(
						SELECT r FROM unnest($3::text[]) r WHERE NOT r = ANY(fraud_reasons)), updated_at = now()
					WHERE workspace_id = $1 AND click_key = $2 AND NOT fraud_reasons @> $3::text[]`, d.ws, d.key, added)
				if err != nil {
					return err
				}
				convs = int(tag.RowsAffected())
			}
		}
		return queueHeldPostbacks(ctx, tx, d.ws, []string{d.key})
	})
	if err != nil {
		return err
	}
	switch {
	case ans != nil && len(added) > 0:
		metrics.TrackerIPLookup("flagged")
	default:
		metrics.TrackerIPLookup(outcome)
	}
	for _, r := range added {
		metrics.TrackerFraudFlag("click", r)
		for range convs {
			metrics.TrackerFraudFlag("conversion", r)
		}
	}
	return nil
}

// queueHeldPostbacks queues the postbacks of the conversions of the given
// clicks, which waited for the clicks' lookups (enqueueSourcePostback). A
// state already sent isn't sent again.
func queueHeldPostbacks(ctx context.Context, tx pgx.Tx, ws string, keys []string) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM tracker_conversions WHERE workspace_id = $1 AND click_key = ANY($2::text[])`, ws, keys)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := enqueueSourcePostback(ctx, tx, ws, id); err != nil {
			return err
		}
	}
	return nil
}
