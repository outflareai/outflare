package api

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reading social accounts: each connection that reaches pages or profiles
// (X, LinkedIn, or a page reader for the public profiles in brand links)
// is read on a schedule. A sync finds the accounts it
// reaches, then reads each one's followers and its own recent posts, and
// keeps a row per account per day and a snapshot per post per read.

const (
	jobSocialSync = "social.sync"

	// How far back a sync reads an account's own posts, and how many.
	// X bills each post read to the workspace's own developer account, so
	// this stays small: a week of posts, read once a day.
	socialPostsWindow = 7 * 24 * time.Hour
	socialPostsLimit  = 50
)

// socialSyncEvery is how often an account is read, by channel. X is read
// once a day to keep the workspace's X bill small; LinkedIn is free.
var socialSyncEvery = map[string]time.Duration{"x": 24 * time.Hour, "linkedin": 12 * time.Hour}

func socialSyncInterval(channel string) time.Duration {
	if d, ok := socialSyncEvery[channel]; ok {
		return d
	}
	return 24 * time.Hour
}

func (s *Server) registerSocialAccountJobs(q *jobs.Queue) {
	q.Handle(jobSocialSync, s.socialSyncJob)
}

// watchSocialConnections reads a new connection that reaches accounts
// straight away.
func (s *Server) watchSocialConnections() {
	if s.integrations == nil || s.integrations.Registry == nil {
		return
	}
	s.integrations.OnConnect = append(s.integrations.OnConnect, func(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
		if c, ok := s.integrations.Registry.Get(i.Connector); ok && (c.Definition().Has(connectors.CapSocialMetrics) || c.Definition().Has(connectors.CapCrawlFetch)) {
			return enqueueSocialSync(ctx, tx, i.WorkspaceID, i.ID, "connected")
		}
		return nil
	})
}

// enqueueSocialSync queues a sync of one connection; key keeps it to one
// per moment.
func enqueueSocialSync(ctx context.Context, tx pgx.Tx, ws, integrationID, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSocialSync, WorkspaceID: &ws, Payload: map[string]string{"integration_id": integrationID},
		IdempotencyKey: "social.sync:" + integrationID + ":" + key, MaxAttempts: 3})
	return err
}

// socialMetricsConnectors are the connector ids that reach accounts, and
// of those the page readers, which read the public profiles in brand links.
func (s *Server) socialMetricsConnectors() (all, readers []string) {
	if s.integrations == nil {
		return nil, nil
	}
	for _, d := range s.integrations.Registry.All() {
		if d.Has(connectors.CapSocialMetrics) || d.Has(connectors.CapCrawlFetch) {
			all = append(all, d.ID)
		}
		if d.Has(connectors.CapCrawlFetch) && !d.Has(connectors.CapSocialMetrics) {
			readers = append(readers, d.ID)
		}
	}
	return all, readers
}

// QueueDueSocialSyncs queues a sync for each connection with an account
// that is due, for each connection with no accounts yet (twice a day), and
// for each page reader in a workspace whose brands link to a profile (every
// hour, so a profile no read has started for yet, such as one linked
// before its channel could be read, starts within the hour; a sync reads
// only the accounts that are due, so this costs no reads), and reports how
// many it queued.
func (s *Server) QueueDueSocialSyncs(ctx context.Context) (int, error) {
	ids, readers := s.socialMetricsConnectors()
	if len(ids) == 0 {
		return 0, nil
	}
	bucket := strconv.FormatInt(time.Now().Unix()/(12*3600), 10)
	hour := "h" + strconv.FormatInt(time.Now().Unix()/3600, 10)
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT a.workspace_id::text, a.integration_id::text, false FROM social_accounts a
			JOIN integrations i ON i.id = a.integration_id AND i.workspace_id = a.workspace_id
			WHERE a.status = 'active' AND a.next_sync_at <= now() AND i.status = 'connected'
			UNION
			SELECT i.workspace_id::text, i.id::text, false FROM integrations i
			WHERE i.connector = ANY($1) AND i.status = 'connected'
			  AND NOT EXISTS (SELECT 1 FROM social_accounts a WHERE a.workspace_id = i.workspace_id AND a.integration_id = i.id)
			UNION
			SELECT i.workspace_id::text, i.id::text, true FROM integrations i
			WHERE i.connector = ANY($2) AND i.status = 'connected'
			  AND EXISTS (SELECT 1 FROM brands b, jsonb_array_elements(b.links) l
				WHERE b.workspace_id = i.workspace_id AND b.status = 'active' AND l->>'kind' = ANY($3))
			LIMIT 500`, ids, readers, sociallinks.PublicKinds)
		if err != nil {
			return err
		}
		type due struct {
			ws, id string
			hourly bool
		}
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.id, &d.hourly}, func() error { list = append(list, d); return nil }); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, d := range list {
			key := bucket
			if d.hourly {
				key = hour
			}
			if seen[d.id+key] {
				continue
			}
			seen[d.id+key] = true
			if err := enqueueSocialSync(ctx, tx, d.ws, d.id, key); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func (s *Server) socialSyncJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "integration_id")
	if err != nil {
		return err
	}
	return s.SyncSocialAccounts(ctx, ws, id)
}

// SyncSocialAccounts finds the accounts a connection reaches and reads the
// ones that are due. A refusal is kept on the accounts and in the events;
// only an outage is retried.
func (s *Server) SyncSocialAccounts(ctx context.Context, ws, integrationID string) error {
	if s.integrations == nil {
		return nil
	}
	start := time.Now()
	ev := track.Event{Name: "social_account.synced", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"integration_id": integrationID}}
	retryable := func(err error) bool {
		k := connectors.KindOf(err)
		return k == connectors.ErrUnavailable || k == connectors.ErrQuota
	}
	failAll := func(err error) error {
		ev.Name = "social_account.sync_failed"
		track.Fail(ctx, s.log, ev, "connector_"+connectors.KindOf(err), err)
		if _, uerr := s.pool.Exec(ctx, `UPDATE social_accounts SET last_error = $3, updated_at = now() WHERE workspace_id = $1 AND integration_id = $2`,
			ws, integrationID, truncate(err.Error(), 500)); uerr != nil {
			return uerr
		}
		if retryable(err) {
			return err
		}
		return nil
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return failAll(err)
	}
	if m, ok := o.Connector.(connectors.SocialMetrics); ok {
		return s.syncSocialChannel(ctx, ws, o, o.Connector.Definition().ID, m, nil, ev, start, failAll, retryable)
	}
	// A page reader reads the public profiles in brand links, channel by
	// channel.
	f, isFetcher := o.Connector.(connectors.PageFetcher)
	if !isFetcher {
		return nil
	}
	// One channel's outage (a slow page, Geonode busy) doesn't stop the
	// others: they are read, and the job is retried for what's left.
	var outage error
	for _, channel := range sociallinks.PublicKinds {
		accounts, brands, err := s.publicAccounts(ctx, ws, o.ID, channel)
		if err != nil {
			return err
		}
		if len(accounts) == 0 {
			// Nothing on this channel: only mark a removed link's account
			// disconnected, without an event a day per empty channel.
			if err := s.saveSocialAccounts(ctx, ws, o.ID, channel, nil, nil); err != nil {
				return err
			}
			continue
		}
		ev := track.Event{Name: ev.Name, WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"integration_id": integrationID, "source": "public_page"}}
		if err := s.syncSocialChannel(ctx, ws, o, channel, publicProfiles{channel: channel, fetch: f, accounts: accounts}, brands, ev, start, failAll, retryable); err != nil {
			if !retryable(err) {
				return err
			}
			outage = err
		}
	}
	return outage
}

// syncSocialChannel saves the accounts a connection reaches on one channel
// and reads the ones that are due.
func (s *Server) syncSocialChannel(ctx context.Context, ws string, o *connectors.Opened, channel string, m connectors.SocialMetrics, brands map[string]string,
	ev track.Event, start time.Time, failAll func(error) error, retryable func(error) bool) error {
	ev.Props["channel"] = channel
	found, err := m.SocialAccounts(ctx, o.Client, o.Creds)
	if err != nil {
		_ = s.integrations.Checked(ctx, s.pool, ws, o.ID, nil, err)
		return failAll(err)
	}
	if err := s.saveSocialAccounts(ctx, ws, o.ID, channel, found, brands); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, external_id, kind, handle, url FROM social_accounts
		WHERE workspace_id = $1 AND integration_id = $2 AND channel = $3 AND status = 'active' AND next_sync_at <= now() + interval '1 minute'`, ws, o.ID, channel)
	if err != nil {
		return err
	}
	type acct struct {
		id string
		a  connectors.SocialAccount
	}
	var due []acct
	var x acct
	if _, err := pgx.ForEachRow(rows, []any{&x.id, &x.a.ExternalID, &x.a.Kind, &x.a.Handle, &x.a.URL}, func() error { due = append(due, x); return nil }); err != nil {
		return err
	}
	posts, synced := 0, 0
	var lastErr error
	for _, d := range due {
		n, err := s.syncSocialAccount(ctx, ws, d.id, channel, m, o, d.a)
		if err != nil {
			lastErr = err
			if !retryable(err) {
				// One account refused (a page the person no longer
				// administers): keep reading the others.
				continue
			}
			return failAll(err)
		}
		posts += n
		synced++
	}
	ev.Props["accounts"], ev.Props["accounts_synced"], ev.Props["posts"] = len(found), synced, posts
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	if lastErr != nil {
		ev.Name = "social_account.sync_failed"
		track.Fail(ctx, s.log, ev, "connector_"+connectors.KindOf(lastErr), lastErr)
		return nil
	}
	track.Emit(ctx, ev)
	return nil
}

// saveSocialAccounts adds the accounts a connection reaches, refreshes
// their names, and marks the ones it no longer reaches disconnected.
// brands gives an account's brand by its external id, where its source
// knows it (a brand's own link); the others get the workspace's only brand.
func (s *Server) saveSocialAccounts(ctx context.Context, ws, integrationID, channel string, found []connectors.SocialAccount, brands map[string]string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A workspace with one brand gets its accounts on that brand.
		var brand *string
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN count(*) = 1 THEN min(id::text) END FROM brands WHERE workspace_id = $1 AND status = 'active'`, ws).Scan(&brand); err != nil {
			return err
		}
		ext := make([]string, 0, len(found))
		for _, a := range found {
			ext = append(ext, a.ExternalID)
			brand := brand
			if b, ok := brands[a.ExternalID]; ok {
				brand = &b
			}
			var id string
			var inserted bool
			err := tx.QueryRow(ctx, `
				INSERT INTO social_accounts (workspace_id, brand_id, channel, kind, integration_id, external_id, handle, name, url, avatar_url)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (workspace_id, channel, external_id) DO UPDATE SET
					integration_id = EXCLUDED.integration_id, kind = EXCLUDED.kind, handle = EXCLUDED.handle,
					name = COALESCE(NULLIF(EXCLUDED.name, ''), social_accounts.name), url = EXCLUDED.url,
					avatar_url = COALESCE(NULLIF(EXCLUDED.avatar_url, ''), social_accounts.avatar_url),
					status = CASE WHEN social_accounts.status = 'disconnected' THEN 'active' ELSE social_accounts.status END,
					updated_at = now()
				RETURNING id::text, xmax = 0`,
				ws, brand, channel, a.Kind, integrationID, a.ExternalID, truncate(a.Handle, 200), truncate(a.Name, 300), truncate(a.URL, 500), truncate(a.AvatarURL, 1000)).
				Scan(&id, &inserted)
			if err != nil {
				return err
			}
			if inserted {
				if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "social_account.added", TargetType: "social_account",
					TargetID: id, Data: map[string]any{"channel": channel, "kind": a.Kind, "integration_id": integrationID}}); err != nil {
					return err
				}
			}
		}
		gone, err := tx.Query(ctx, `UPDATE social_accounts SET status = 'disconnected', updated_at = now()
			WHERE workspace_id = $1 AND integration_id = $2 AND channel = $4 AND status <> 'disconnected' AND NOT (external_id = ANY($3))
			RETURNING id::text`, ws, integrationID, ext, channel)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(gone, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "social_account.disconnected", TargetType: "social_account",
				TargetID: id, Data: map[string]any{"channel": channel, "integration_id": integrationID}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// syncSocialAccount reads one account's counts and its own recent posts,
// and reports how many posts it read.
func (s *Server) syncSocialAccount(ctx context.Context, ws, id, channel string, m connectors.SocialMetrics, o *connectors.Opened, a connectors.SocialAccount) (int, error) {
	fail := func(err error) (int, error) {
		_, uerr := s.pool.Exec(ctx, `UPDATE social_accounts SET last_error = $3, updated_at = now(),
			next_sync_at = CASE WHEN $4 THEN next_sync_at ELSE now() + make_interval(secs => $5) END
			WHERE workspace_id = $1 AND id = $2`,
			ws, id, truncate(err.Error(), 500), connectors.KindOf(err) == connectors.ErrUnavailable, socialSyncInterval(channel).Seconds())
		if uerr != nil {
			return 0, uerr
		}
		return 0, err
	}
	st, err := m.SocialStats(ctx, o.Client, o.Creds, a)
	if err != nil {
		return fail(err)
	}
	posts, err := m.SocialPosts(ctx, o.Client, o.Creds, a, time.Now().Add(-socialPostsWindow), socialPostsLimit)
	if err != nil {
		return fail(err)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE social_accounts SET followers = COALESCE($3, followers), following = COALESCE($4, following),
			posts = COALESCE($5, posts), last_synced_at = now(), last_error = NULL, next_sync_at = now() + make_interval(secs => $6),
			name = COALESCE(NULLIF($7, ''), name), avatar_url = COALESCE(NULLIF($8, ''), avatar_url), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, ws, id, st.Followers, st.Following, st.Posts, socialSyncInterval(channel).Seconds(),
			truncate(st.Name, 300), truncate(st.AvatarURL, 1000)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO social_account_days (workspace_id, account_id, day, followers, following, posts)
			VALUES ($1, $2, (now() AT TIME ZONE 'UTC')::date, $3, $4, $5)
			ON CONFLICT (account_id, day) DO UPDATE SET followers = COALESCE(EXCLUDED.followers, social_account_days.followers),
				following = COALESCE(EXCLUDED.following, social_account_days.following), posts = COALESCE(EXCLUDED.posts, social_account_days.posts),
				updated_at = now()`, ws, id, st.Followers, st.Following, st.Posts); err != nil {
			return err
		}
		for _, p := range posts {
			pm := p.Metrics
			var postID string
			if err := tx.QueryRow(ctx, `
				INSERT INTO social_posts (workspace_id, brand_id, account_id, channel, external_id, url, body, posted_at,
					impressions, likes, replies, reposts, quotes, bookmarks, clicks, metrics_at)
				SELECT $1, a.brand_id, a.id, a.channel, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now()
				FROM social_accounts a WHERE a.workspace_id = $1 AND a.id = $2
				ON CONFLICT (account_id, external_id) DO UPDATE SET url = EXCLUDED.url, body = EXCLUDED.body,
					impressions = EXCLUDED.impressions, likes = EXCLUDED.likes, replies = EXCLUDED.replies, reposts = EXCLUDED.reposts,
					quotes = EXCLUDED.quotes, bookmarks = EXCLUDED.bookmarks, clicks = EXCLUDED.clicks, metrics_at = now(), updated_at = now()
				RETURNING id::text`,
				ws, id, p.ExternalID, truncate(p.URL, 1000), truncate(p.Body, 10000), p.PostedAt,
				pm.Impressions, pm.Likes, pm.Replies, pm.Reposts, pm.Quotes, pm.Bookmarks, pm.Clicks).Scan(&postID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO social_post_snapshots (workspace_id, post_id, impressions, likes, replies, reposts, quotes, bookmarks, clicks)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING`,
				ws, postID, pm.Impressions, pm.Likes, pm.Replies, pm.Reposts, pm.Quotes, pm.Bookmarks, pm.Clicks); err != nil {
				return err
			}
		}
		return nil
	})
	return len(posts), err
}
