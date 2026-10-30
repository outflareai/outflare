package metrics

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Postgres reports the connection pool, the job queue and the database's
// own counters. It queries on each scrape, at most once every cacheFor, so
// a busy scraper or several cannot load the database.
type Postgres struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger

	mu      sync.Mutex
	at      time.Time
	samples []prometheus.Metric
}

const (
	cacheFor     = 10 * time.Second
	queryTimeout = 5 * time.Second
	topTables    = 25 // largest tables get their own size series
)

func desc(sub, name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName(namespace, sub, name), help, labels, nil)
}

var (
	poolAcquired    = desc("db_pool", "acquired_conns", "Pool connections in use.")
	poolIdle        = desc("db_pool", "idle_conns", "Pool connections idle.")
	poolTotal       = desc("db_pool", "total_conns", "Pool connections open.")
	poolMax         = desc("db_pool", "max_conns", "Most connections the pool will open.")
	poolAcquires    = desc("db_pool", "acquires_total", "Connections handed out by the pool.")
	poolWaits       = desc("db_pool", "empty_acquires_total", "Acquires that had to wait because every connection was busy.")
	poolWaitSeconds = desc("db_pool", "empty_acquire_wait_seconds_total", "Time spent waiting for a busy pool.")
	poolAcquireSecs = desc("db_pool", "acquire_seconds_total", "Time spent acquiring connections.")
	poolCanceled    = desc("db_pool", "canceled_acquires_total", "Acquires given up before a connection was free.")
	queued          = desc("jobs", "queued", "Jobs due now and waiting for a worker, by kind.", "kind")
	scheduled       = desc("jobs", "scheduled", "Jobs waiting for a later run_at (retries and schedules), by kind.", "kind")
	oldestDue       = desc("jobs", "oldest_due_seconds", "How long the longest-waiting due job has waited, by kind.", "kind")
	running         = desc("jobs", "running", "Jobs claimed by any worker, by kind.", "kind")
	dbBackends      = desc("pg", "connections", "Connections to the database, by state.", "state")
	dbMaxConns      = desc("pg", "max_connections", "The server's max_connections.")
	dbLongestXact   = desc("pg", "longest_transaction_seconds", "Age of the oldest open transaction.")
	dbLockWaits     = desc("pg", "lock_waiting_connections", "Connections waiting on a lock.")
	dbSize          = desc("pg", "database_bytes", "Size of the database.")
	dbCommits       = desc("pg", "commits_total", "Transactions committed.")
	dbRollbacks     = desc("pg", "rollbacks_total", "Transactions rolled back.")
	dbBlocksHit     = desc("pg", "blocks_hit_total", "Blocks read from the buffer cache.")
	dbBlocksRead    = desc("pg", "blocks_read_total", "Blocks read from disk.")
	dbDeadlocks     = desc("pg", "deadlocks_total", "Deadlocks detected.")
	dbTempBytes     = desc("pg", "temp_bytes_total", "Bytes written to temporary files by queries too big for work_mem.")
	tableBytes      = desc("pg", "table_bytes", "Size of a table with its indexes and TOAST, for the largest tables.", "table")
	tableDeadRows   = desc("pg", "table_dead_rows", "Dead rows waiting for vacuum, for the largest tables.", "table")
	tableSeqScans   = desc("pg", "table_seq_scans_total", "Sequential scans, for the largest tables.", "table")
	pipeItems       = desc("pipeline", "items", "Pipeline items now, by pipeline, stage and state (due, scheduled, working, held).", "pipeline", "stage", "state")
	pipeOldest      = desc("pipeline", "oldest_due_seconds", "How long the longest-waiting due item has waited, by pipeline and stage. The highest is the bottleneck.", "pipeline", "stage")
	pipeRuns        = desc("pipeline", "runs", "Pipeline runs that are running or paused, by pipeline and state.", "pipeline", "state")
	pipeBreakers    = desc("pipeline", "breakers", "Outside accounts by breaker state (open, half_open) and kind of account.", "account", "state")
	inboxPending    = desc("inbox", "pending", "Inbox rows (browser batches) waiting for a worker.")
	inboxOldest     = desc("inbox", "oldest_seconds", "How long the oldest waiting inbox row has waited.")
	inboxFailed     = desc("inbox", "failed", "Inbox rows set aside after failing too often.")
	smartLists      = desc("smart_lists", "lists", "Smart lists in every workspace.")
	smartOldest     = desc("smart_lists", "oldest_refresh_seconds", "How long since the least recently refreshed smart list was brought up to date (since it was made, if never). Lists refresh every 15 minutes.")
	smartFailing    = desc("smart_lists", "failing", "Smart lists whose last refresh failed.")
	rulesActive     = desc("rules", "active", "Active rules in every workspace, by trigger kind.", "trigger")
	rulesTimersDue  = desc("rules", "timers_due", "Absence timers of active rules that ran out and wait to be handled. Normally 0 or close to it: they are handled every minute.")
	rulesOldestDue  = desc("rules", "oldest_due_seconds", "How long the longest-waiting absence timer of an active rule has been due. Normally under two minutes.")
	journeysWaiting = desc("journeys", "waiting_runs", "Journey runs on a wait_until step, waiting for an event, conditions or their timeout.")
	journeysOverdue = desc("journeys", "oldest_due_seconds", "How long past its time the most overdue run of an active journey is (a wait, a wait_until check or timeout, or a call that never came back). Runs move on within seconds of their time, so this stays near 0.")
	reviewPages     = desc("reviews", "pages", "Review pages being read, by platform and state (ok, failing: the last read failed).", "platform", "state")
	reviewStuck     = desc("reviews", "stuck_pages", "Active review pages whose last 3 or more reads failed, retries included, by platform and the last error code.", "platform", "code")
	socialStuck     = desc("social", "stuck_accounts", "Active social accounts whose last 3 or more reads failed, retries included, by channel and the last error code.", "channel", "code")
	syncStuck       = desc("sync", "stuck", "Scheduled reads (connections' syncs, SEO site reads, news searches, competitor looks) whose last 3 or more runs failed, retries included, by module and the last error code.", "module", "code")
	reviewOldest    = desc("reviews", "oldest_read_seconds", "How long since the least recently read active review page was read (or added, if never read), by platform.", "platform")
	socialScheduled = desc("social", "publications_scheduled", "Approved posts waiting to go out, by channel and state (due: its time has come, later: scheduled ahead).", "channel", "state")
	socialOverdue   = desc("social", "publication_overdue_seconds", "How long past its time the most overdue approved post is, by channel. Posts go out within a minute or two of their time.", "channel")
	scrapeErrors    = desc("metrics", "scrape_errors", "Queries that failed in the last database scrape.")
	scrapeDuration  = desc("metrics", "scrape_duration_seconds", "Time the last database scrape took.")
	newsSearches    = desc("news", "searches", "Active news searches, by state (ok, failing: the last run failed).", "state")
	newsOverdue     = desc("news", "search_overdue_seconds", "How long past its time the most overdue active news search is. Searches are queued every few minutes, so this stays near 0.")
	allDescs        = []*prometheus.Desc{poolAcquired, poolIdle, poolTotal, poolMax, poolAcquires, poolWaits, poolWaitSeconds, poolAcquireSecs, poolCanceled, queued, scheduled, oldestDue, running, dbBackends, dbMaxConns, dbLongestXact, dbLockWaits, dbSize, dbCommits, dbRollbacks, dbBlocksHit, dbBlocksRead, dbDeadlocks, dbTempBytes, tableBytes, tableDeadRows, tableSeqScans, pipeItems, pipeOldest, pipeRuns, pipeBreakers, inboxPending, inboxOldest, inboxFailed, smartLists, smartOldest, smartFailing, rulesActive, rulesTimersDue, rulesOldestDue, journeysWaiting, journeysOverdue, reviewPages, reviewStuck, socialStuck, syncStuck, reviewOldest, newsSearches, newsOverdue, socialScheduled, socialOverdue, scrapeErrors, scrapeDuration}
	gauge, counter  = prometheus.GaugeValue, prometheus.CounterValue
)

// rowser is the part of pgx.Rows the scrapes read.
type rowser interface {
	Next() bool
	Scan(...any) error
}

func (p *Postgres) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range allDescs {
		ch <- d
	}
}

func (p *Postgres) Collect(ch chan<- prometheus.Metric) {
	// The pool's own numbers are free; read them every time.
	s := p.Pool.Stat()
	ch <- prometheus.MustNewConstMetric(poolAcquired, gauge, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(poolIdle, gauge, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(poolTotal, gauge, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(poolMax, gauge, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolAcquires, counter, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolWaits, counter, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolWaitSeconds, counter, s.EmptyAcquireWaitTime().Seconds())
	ch <- prometheus.MustNewConstMetric(poolAcquireSecs, counter, s.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(poolCanceled, counter, float64(s.CanceledAcquireCount()))

	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.at) > cacheFor {
		p.samples = p.query()
		p.at = time.Now()
	}
	for _, m := range p.samples {
		ch <- m
	}
}

func (p *Postgres) query() []prometheus.Metric {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var out []prometheus.Metric
	add := func(d *prometheus.Desc, t prometheus.ValueType, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(d, t, v, labels...))
	}
	failed := 0
	run := func(name, sql string, scan func(rowser) error) {
		rows, err := p.Pool.Query(ctx, sql)
		if err == nil {
			err = scan(rows)
			rows.Close()
			if err == nil {
				err = rows.Err()
			}
		}
		if err != nil {
			failed++
			if p.Log != nil {
				p.Log.Warn("metrics query", "query", name, "err", err)
			}
		}
	}
	// Both filters use the partial indexes on queued and running jobs.
	run("jobs_queued", `
		SELECT kind,
			count(*) FILTER (WHERE run_at <= now()),
			count(*) FILTER (WHERE run_at > now()),
			coalesce(extract(epoch FROM now() - min(run_at) FILTER (WHERE run_at <= now())), 0)::float8
		FROM jobs WHERE status = 'queued' GROUP BY kind`, func(r rowser) error {
		for r.Next() {
			var kind string
			var due, later int64
			var oldest float64
			if err := r.Scan(&kind, &due, &later, &oldest); err != nil {
				return err
			}
			add(queued, gauge, float64(due), kind)
			add(scheduled, gauge, float64(later), kind)
			add(oldestDue, gauge, oldest, kind)
		}
		return nil
	})
	// The pending filter uses the inbox's partial index.
	run("inbox", `
		SELECT count(*) FILTER (WHERE failed_at IS NULL),
			coalesce(extract(epoch FROM now() - min(received_at) FILTER (WHERE failed_at IS NULL)), 0)::float8,
			count(*) FILTER (WHERE failed_at IS NOT NULL)
		FROM site_event_inbox`, func(r rowser) error {
		for r.Next() {
			var pending, failed int64
			var oldest float64
			if err := r.Scan(&pending, &oldest, &failed); err != nil {
				return err
			}
			add(inboxPending, gauge, float64(pending))
			add(inboxOldest, gauge, oldest)
			add(inboxFailed, gauge, float64(failed))
		}
		return nil
	})
	run("smart_lists", `
		SELECT count(*),
			coalesce(extract(epoch FROM now() - min(coalesce(refreshed_at, created_at))), 0)::float8,
			count(*) FILTER (WHERE error_code IS NOT NULL)
		FROM smart_lists`, func(r rowser) error {
		for r.Next() {
			var lists, failing int64
			var oldest float64
			if err := r.Scan(&lists, &oldest, &failing); err != nil {
				return err
			}
			add(smartLists, gauge, float64(lists))
			add(smartOldest, gauge, oldest)
			add(smartFailing, gauge, float64(failing))
		}
		return nil
	})
	run("rules_active", `SELECT trigger_kind, count(*) FROM rules WHERE status = 'active' GROUP BY trigger_kind`, func(r rowser) error {
		for r.Next() {
			var kind string
			var n int64
			if err := r.Scan(&kind, &n); err != nil {
				return err
			}
			add(rulesActive, gauge, float64(n), kind)
		}
		return nil
	})
	run("rules_timers", `
		SELECT count(*), coalesce(extract(epoch FROM now() - min(s.due_at)), 0)::float8
		FROM rule_states s JOIN rules r ON r.id = s.rule_id
		WHERE s.due_at <= now() AND r.status = 'active'`, func(r rowser) error {
		for r.Next() {
			var n int64
			var oldest float64
			if err := r.Scan(&n, &oldest); err != nil {
				return err
			}
			add(rulesTimersDue, gauge, float64(n))
			add(rulesOldestDue, gauge, oldest)
		}
		return nil
	})
	run("journey_waiting", `SELECT count(*) FROM journey_runs WHERE status = 'active' AND wait_deadline IS NOT NULL`, func(r rowser) error {
		for r.Next() {
			var n int64
			if err := r.Scan(&n); err != nil {
				return err
			}
			add(journeysWaiting, gauge, float64(n))
		}
		return nil
	})
	// The earliest due run of an active journey, through the next_at index.
	run("journey_due", `
		SELECT coalesce((SELECT extract(epoch FROM now() - r.next_at) FROM journey_runs r JOIN journeys j ON j.id = r.journey_id
			WHERE r.status = 'active' AND r.next_at IS NOT NULL AND j.status = 'active' ORDER BY r.next_at LIMIT 1), 0)::float8`, func(r rowser) error {
		for r.Next() {
			var oldest float64
			if err := r.Scan(&oldest); err != nil {
				return err
			}
			add(journeysOverdue, gauge, max(oldest, 0))
		}
		return nil
	})
	run("jobs_running", `SELECT kind, count(*) FROM jobs WHERE status = 'running' GROUP BY kind`, func(r rowser) error {
		for r.Next() {
			var kind string
			var n int64
			if err := r.Scan(&kind, &n); err != nil {
				return err
			}
			add(running, gauge, float64(n), kind)
		}
		return nil
	})
	// Live items only: finished ones are not scanned.
	run("pipeline_items", `
		SELECT pipeline, stage,
			count(*) FILTER (WHERE state = 'waiting' AND next_at <= now()),
			count(*) FILTER (WHERE state = 'waiting' AND next_at > now()),
			count(*) FILTER (WHERE state = 'working'),
			count(*) FILTER (WHERE state = 'held'),
			coalesce(extract(epoch FROM now() - min(next_at) FILTER (WHERE state = 'waiting' AND next_at <= now())), 0)::float8
		FROM pipeline_items WHERE state IN ('waiting', 'working', 'held') GROUP BY pipeline, stage`, func(r rowser) error {
		for r.Next() {
			var pipeline, stage string
			var due, later, working, held int64
			var oldest float64
			if err := r.Scan(&pipeline, &stage, &due, &later, &working, &held, &oldest); err != nil {
				return err
			}
			add(pipeItems, gauge, float64(due), pipeline, stage, "due")
			add(pipeItems, gauge, float64(later), pipeline, stage, "scheduled")
			add(pipeItems, gauge, float64(working), pipeline, stage, "working")
			add(pipeItems, gauge, float64(held), pipeline, stage, "held")
			add(pipeOldest, gauge, oldest, pipeline, stage)
		}
		return nil
	})
	run("pipeline_runs", `SELECT pipeline, state, count(*) FROM pipeline_runs WHERE state IN ('running', 'paused') GROUP BY 1, 2`, func(r rowser) error {
		for r.Next() {
			var pipeline, state string
			var n int64
			if err := r.Scan(&pipeline, &state, &n); err != nil {
				return err
			}
			add(pipeRuns, gauge, float64(n), pipeline, state)
		}
		return nil
	})
	run("pipeline_breakers", `SELECT split_part(key, ':', 1), breaker, count(*) FROM pipeline_limits WHERE breaker <> 'closed' GROUP BY 1, 2`, func(r rowser) error {
		for r.Next() {
			var account, state string
			var n int64
			if err := r.Scan(&account, &state, &n); err != nil {
				return err
			}
			add(pipeBreakers, gauge, float64(n), account, state)
		}
		return nil
	})
	run("review_pages", `
		SELECT platform, CASE WHEN last_error IS NULL THEN 'ok' ELSE 'failing' END, count(*),
			coalesce(max(extract(epoch FROM now() - coalesce(last_read_at, created_at))), 0)::float8
		FROM review_pages WHERE status = 'active' GROUP BY 1, 2`, func(r rowser) error {
		oldest := map[string]float64{}
		for r.Next() {
			var platform, state string
			var n int64
			var age float64
			if err := r.Scan(&platform, &state, &n, &age); err != nil {
				return err
			}
			add(reviewPages, gauge, float64(n), platform, state)
			oldest[platform] = max(oldest[platform], age)
		}
		for platform, age := range oldest {
			add(reviewOldest, gauge, age, platform)
		}
		return nil
	})
	run("syncs_stuck", `
		SELECT x.module, coalesce(x.code, 'unknown'), count(*) FROM (
			SELECT 'analytics' AS module, a.last_error_code AS code, a.integration_id FROM analytics_syncs a WHERE a.failures >= 3
			UNION ALL SELECT 'search_console', y.last_error_code, y.integration_id FROM search_syncs y WHERE y.failures >= 3
			UNION ALL SELECT 'page_conversions', c.last_error_code, c.integration_id FROM page_conversion_syncs c WHERE c.failures >= 3
			UNION ALL SELECT 'ads', d.last_error_code, d.integration_id FROM ad_syncs d WHERE d.failures >= 3
			UNION ALL SELECT 'seo', e.last_error_code, e.integration_id FROM seo_syncs e WHERE e.failures >= 3
			UNION ALL SELECT 'seo_site_' || f.kind, f.last_error_code, f.integration_id FROM seo_site_read_failures f
				WHERE f.failures >= 3 AND f.last_failed_at > now() - interval '7 days'
			-- Not a connection's: news searches and competitor looks.
			UNION ALL SELECT 'news', n.last_error_code, NULL FROM news_searches n WHERE n.status = 'active' AND n.failures >= 3
			UNION ALL SELECT 'competitor_glances', CASE WHEN g.check_error IS NULL THEN NULL ELSE 'crawl_failed' END, NULL
				FROM seo_competitors g WHERE g.status = 'tracked' AND g.check_failures >= 3
		) x LEFT JOIN integrations i ON i.id = x.integration_id
		WHERE x.integration_id IS NULL OR i.status = 'connected'
		GROUP BY 1, 2`, func(r rowser) error {
		for r.Next() {
			var module, code string
			var n int64
			if err := r.Scan(&module, &code, &n); err != nil {
				return err
			}
			add(syncStuck, gauge, float64(n), module, code)
		}
		return nil
	})
	run("news_searches", `
		SELECT CASE WHEN last_error_code IS NULL THEN 'ok' ELSE 'failing' END, count(*),
			coalesce(max(extract(epoch FROM now() - next_check_at)) FILTER (WHERE next_check_at <= now()), 0)::float8
		FROM news_searches WHERE status = 'active' GROUP BY 1`, func(r rowser) error {
		overdue := 0.0
		for r.Next() {
			var state string
			var n int64
			var age float64
			if err := r.Scan(&state, &n, &age); err != nil {
				return err
			}
			add(newsSearches, gauge, float64(n), state)
			overdue = max(overdue, age)
		}
		add(newsOverdue, gauge, overdue)
		return nil
	})
	run("social_accounts_stuck", `
		SELECT channel, coalesce(last_error_code, 'unknown'), count(*)
		FROM social_accounts WHERE status = 'active' AND failures >= 3 GROUP BY 1, 2`, func(r rowser) error {
		for r.Next() {
			var channel, code string
			var n int64
			if err := r.Scan(&channel, &code, &n); err != nil {
				return err
			}
			add(socialStuck, gauge, float64(n), channel, code)
		}
		return nil
	})
	run("review_pages_stuck", `
		SELECT platform, coalesce(last_error_code, 'unknown'), count(*)
		FROM review_pages WHERE status = 'active' AND failures >= 3 GROUP BY 1, 2`, func(r rowser) error {
		for r.Next() {
			var platform, code string
			var n int64
			if err := r.Scan(&platform, &code, &n); err != nil {
				return err
			}
			add(reviewStuck, gauge, float64(n), platform, code)
		}
		return nil
	})
	run("social_publications", `
		SELECT channel, CASE WHEN coalesce(scheduled_for, approved_at) <= now() THEN 'due' ELSE 'later' END, count(*),
			coalesce(max(extract(epoch FROM now() - coalesce(scheduled_for, approved_at))) FILTER (WHERE coalesce(scheduled_for, approved_at) <= now()), 0)::float8
		FROM social_publications WHERE status = 'approved' GROUP BY 1, 2`, func(r rowser) error {
		overdue := map[string]float64{}
		for r.Next() {
			var channel, state string
			var n int64
			var age float64
			if err := r.Scan(&channel, &state, &n, &age); err != nil {
				return err
			}
			add(socialScheduled, gauge, float64(n), channel, state)
			overdue[channel] = max(overdue[channel], age)
		}
		for channel, age := range overdue {
			add(socialOverdue, gauge, age, channel)
		}
		return nil
	})
	run("pg_activity", `
		SELECT coalesce(state, 'unknown'), count(*),
			coalesce(max(extract(epoch FROM now() - xact_start)), 0)::float8,
			count(*) FILTER (WHERE wait_event_type = 'Lock')
		FROM pg_stat_activity WHERE datname = current_database() AND backend_type = 'client backend'
		GROUP BY 1`, func(r rowser) error {
		var longest, waits float64
		for r.Next() {
			var state string
			var n, w int64
			var age float64
			if err := r.Scan(&state, &n, &age, &w); err != nil {
				return err
			}
			add(dbBackends, gauge, float64(n), state)
			longest, waits = max(longest, age), waits+float64(w)
		}
		add(dbLongestXact, gauge, longest)
		add(dbLockWaits, gauge, waits)
		return nil
	})
	run("pg_database", `
		SELECT current_setting('max_connections')::float8, pg_database_size(current_database())::float8,
			xact_commit::float8, xact_rollback::float8, blks_hit::float8, blks_read::float8, deadlocks::float8, temp_bytes::float8
		FROM pg_stat_database WHERE datname = current_database()`, func(r rowser) error {
		for r.Next() {
			var maxConns, size, commits, rollbacks, hit, read, deadlocks, temp float64
			if err := r.Scan(&maxConns, &size, &commits, &rollbacks, &hit, &read, &deadlocks, &temp); err != nil {
				return err
			}
			add(dbMaxConns, gauge, maxConns)
			add(dbSize, gauge, size)
			add(dbCommits, counter, commits)
			add(dbRollbacks, counter, rollbacks)
			add(dbBlocksHit, counter, hit)
			add(dbBlocksRead, counter, read)
			add(dbDeadlocks, counter, deadlocks)
			add(dbTempBytes, counter, temp)
		}
		return nil
	})
	run("pg_tables", `
		SELECT relname, pg_total_relation_size(relid)::float8, n_dead_tup::float8, seq_scan::float8
		FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT `+strconv.Itoa(topTables), func(r rowser) error {
		for r.Next() {
			var table string
			var size, dead, scans float64
			if err := r.Scan(&table, &size, &dead, &scans); err != nil {
				return err
			}
			add(tableBytes, gauge, size, table)
			add(tableDeadRows, gauge, dead, table)
			add(tableSeqScans, counter, scans, table)
		}
		return nil
	})

	add(scrapeErrors, gauge, float64(failed))
	add(scrapeDuration, gauge, time.Since(start).Seconds())
	return out
}
