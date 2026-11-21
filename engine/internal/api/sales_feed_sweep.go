package api

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/slack"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leadfeed"
)

// feedSweep reads the last two weeks again: a stored post whose text
// changed is updated and read again, one Slack no longer has is marked
// deleted, one with new thread replies gets them, and one never stored
// (Slack showed it after the cursor passed it) is stored, unless it is
// older than the channel's read-back days. A sweep that fails is tried
// again after feedSweepRetry, not on every poll.
func (s *Server) feedSweep(ctx context.Context, a *access, ch feedChannel, o *connectors.Opened, reader connectors.FeedReader) (err error) {
	defer func() {
		if err != nil {
			_, _ = s.pool.Exec(ctx, `UPDATE sales_feed_channels SET last_swept_at=now()-make_interval(secs=>$4),updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`,
				a.WorkspaceID, a.Brand, ch.ID, (feedSweepEvery - feedSweepRetry).Seconds())
		}
	}()
	from := time.Now().AddDate(0, 0, -feedSweepDays)
	msgs, err := reader.History(ctx, o.Client, o.Creds, ch.ChannelID, feedStamp(from), "")
	if err != nil {
		return err
	}
	inSlack := make(map[string]connectors.FeedMessage, len(msgs))
	for _, m := range msgs {
		inSlack[m.ID] = m
	}
	// A day's margin before the read finds every stored post it returned.
	// A post whose person moved to another brand moved with them: it is
	// known, so never stored twice, and it is that brand's now, so the
	// sweep leaves it alone.
	rows, err := s.pool.Query(ctx, `SELECT slack_ts,content_hash,latest_reply,deleted_in_slack,occurred_at,brand_id::text=$2 FROM sales_feed_items
		WHERE workspace_id=$1 AND feed_channel_id=$3 AND occurred_at>$4`, a.WorkspaceID, a.Brand, ch.ID, from.AddDate(0, 0, -1))
	if err != nil {
		return err
	}
	type stored struct {
		ts, hash, latest string
		deleted, ours    bool
		at               time.Time
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stored, error) {
		var it stored
		return it, r.Scan(&it.ts, &it.hash, &it.latest, &it.deleted, &it.at, &it.ours)
	})
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(items))
	// A post whose person was erased is known too, and never stored again.
	erased, err := s.pool.Query(ctx, `SELECT slack_ts FROM sales_feed_erased WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3`, a.WorkspaceID, a.Brand, ch.ID)
	if err != nil {
		return err
	}
	gone, err := pgx.CollectRows(erased, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, ts := range gone {
		known[ts] = true
	}
	for _, it := range items {
		known[it.ts] = true
		if !it.ours {
			continue
		}
		m, ok := inSlack[it.ts]
		if !ok {
			// A minute's margin keeps a post at the read's edge from looking deleted.
			if !it.deleted && it.at.After(from.Add(time.Minute)) {
				if _, err := s.pool.Exec(ctx, `UPDATE sales_feed_items SET deleted_in_slack=true,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3 AND slack_ts=$4`,
					a.WorkspaceID, a.Brand, ch.ID, it.ts); err != nil {
					return err
				}
			}
			continue
		}
		if leadfeed.Hash(m.Text) == it.hash && m.LatestReply == it.latest && !it.deleted {
			continue
		}
		if m.Text == feedDeletedText && it.deleted && m.LatestReply == it.latest {
			continue // deleted in Slack, kept as it was, and its thread unchanged
		}
		var replies []connectors.FeedMessage
		if m.LatestReply != it.latest {
			replies = []connectors.FeedMessage{}
			if m.LatestReply != "" {
				if replies, err = reader.Thread(ctx, o.Client, o.Creds, ch.ChannelID, m.ID); err != nil {
					return err
				}
			}
		}
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			_, _, err := s.feedStore(ctx, tx, a, ch, m, "", replies)
			return err
		}); err != nil {
			return err
		}
	}
	// Reading began read_back_days before the channel was added or last
	// turned on; the sweep never stores anything older, so posts made while
	// it was off are not read.
	bound := ch.CreatedAt.AddDate(0, 0, -ch.ReadBackDays)
	if ch.ReadSince != nil {
		bound = *ch.ReadSince
	}
	base := ""
	for _, m := range msgs {
		if known[m.ID] || feedTime(m.ID).Before(bound) {
			continue
		}
		var replies []connectors.FeedMessage
		if m.LatestReply != "" {
			if replies, err = reader.Thread(ctx, o.Client, o.Creds, ch.ChannelID, m.ID); err != nil {
				return err
			}
		}
		if base == "" {
			if base, err = reader.BaseURL(ctx, o.Client, o.Creds); err != nil {
				return err
			}
		}
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			_, _, err := s.feedStore(ctx, tx, a, ch, m, base, replies)
			return err
		}); err != nil {
			return err
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE sales_feed_channels SET last_swept_at=now(),updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, ch.ID)
	return err
}

func feedReplies(msgs []connectors.FeedMessage) []feedReply {
	out := make([]feedReply, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, feedReply{Author: m.Author, At: m.At, Text: m.Text})
	}
	return out
}

// feedStore inserts or updates one post and enqueues jobSalesFeedItem when
// it is new or its text changed. replies nil keeps the stored replies;
// base "" keeps the stored link.
func (s *Server) feedStore(ctx context.Context, tx pgx.Tx, a *access, ch feedChannel, m connectors.FeedMessage, base string, replies []connectors.FeedMessage) (itemID string, changed bool, err error) {
	hash := leadfeed.Hash(m.Text)
	var oldHash, latest string
	var deleted, ours bool
	var sealed []byte
	// Found whatever brand it is in now: a post whose person moved to
	// another brand went with them, and is never stored again here.
	err = tx.QueryRow(ctx, `SELECT id::text,content_hash,latest_reply,deleted_in_slack,content_sealed,brand_id::text=$2 FROM sales_feed_items
		WHERE workspace_id=$1 AND feed_channel_id=$3 AND slack_ts=$4 FOR UPDATE`, a.WorkspaceID, a.Brand, ch.ID, m.ID).
		Scan(&itemID, &oldHash, &latest, &deleted, &sealed, &ours)
	if err == nil && !ours {
		return itemID, false, nil // another brand's now: never touched from here
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Its stored copy went when its person was erased: never again.
		var erased bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sales_feed_erased WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3 AND slack_ts=$4)`,
			a.WorkspaceID, a.Brand, ch.ID, m.ID).Scan(&erased); err != nil || erased {
			return "", false, err
		}
		itemID = newID()
		c := feedContent{Text: m.Text, Author: m.Author, Bot: m.Bot, Source: m.Source, Replies: feedReplies(replies)}
		if base != "" {
			c.Link = slack.Link(base, ch.ChannelID, m.ID)
		}
		latest = ""
		if replies != nil {
			latest = m.LatestReply
		}
		if _, err = tx.Exec(ctx, `INSERT INTO sales_feed_items(id,workspace_id,brand_id,feed_channel_id,slack_ts,kind,content_sealed,content_hash,latest_reply,occurred_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, itemID, a.WorkspaceID, a.Brand, ch.ID, m.ID, ch.Kind, s.salesSeal(itemID, c), hash, latest, m.At); err != nil {
			return "", false, err
		}
		return itemID, true, s.enqueueFeedItem(ctx, tx, a, itemID, hash)
	}
	if err != nil {
		return "", false, err
	}
	var c feedContent
	if m.Text == feedDeletedText {
		// Slack shows a deleted parent that still has replies as this text:
		// the post keeps what it said and is marked deleted, its replies
		// still follow, and nothing is read again.
		if deleted && replies == nil && base == "" {
			return itemID, false, nil
		}
		if err = s.salesOpen(sealed, itemID, &c); err != nil {
			return "", false, err
		}
		if replies != nil {
			c.Replies = feedReplies(replies)
			latest = m.LatestReply
		}
		if base != "" {
			c.Link = slack.Link(base, ch.ChannelID, m.ID)
		}
		_, err = tx.Exec(ctx, `UPDATE sales_feed_items SET content_sealed=$5,latest_reply=$6,deleted_in_slack=true,updated_at=now()
			WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, ch.ID, itemID, s.salesSeal(itemID, c), latest)
		return itemID, false, err
	}
	changed = hash != oldHash
	if !changed && replies == nil && !deleted && base == "" {
		return itemID, false, nil
	}
	if err = s.salesOpen(sealed, itemID, &c); err != nil {
		return "", false, err
	}
	var edited *time.Time
	if changed {
		c.Text, c.Author, c.Bot = m.Text, m.Author, m.Bot
		at := time.Now()
		if m.EditedAt != nil {
			at = *m.EditedAt
		}
		edited = &at
	}
	if replies != nil {
		c.Replies = feedReplies(replies)
		latest = m.LatestReply
	}
	if base != "" {
		c.Link = slack.Link(base, ch.ChannelID, m.ID)
	}
	if _, err = tx.Exec(ctx, `UPDATE sales_feed_items SET content_sealed=$5,content_hash=$6,latest_reply=$7,deleted_in_slack=false,edited_at=COALESCE($8,edited_at),updated_at=now()
		WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, ch.ID, itemID, s.salesSeal(itemID, c), hash, latest, edited); err != nil {
		return "", false, err
	}
	if changed {
		// Each edit is read again, also one back to an earlier text.
		err = s.enqueueFeedItem(ctx, tx, a, itemID, hash+":"+strconv.FormatInt(edited.UnixMicro(), 10))
	}
	return itemID, changed, err
}

// enqueueFeedItem queues a post to be read, once per version (key).
func (s *Server) enqueueFeedItem(ctx context.Context, tx pgx.Tx, a *access, id, version string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSalesFeedItem, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"id": id, "brand_id": a.Brand},
		MaxAttempts: 5, IdempotencyKey: id + ":" + version})
	return err
}
