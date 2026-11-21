package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leadfeed"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reading the Slack channels a brand chose (sales_feed_channels): a job per
// channel stores each new post once, sealed, and queues it to be read.
const (
	jobSalesFeedPoll = "sales.feed_poll" // payload {"id": feed channel id, "brand_id"}
	jobSalesFeedItem = "sales.feed_item" // payload {"id": item id, "brand_id"}

	feedPollEvery  = 2 * time.Minute
	feedSweepEvery = time.Hour
	feedSweepDays  = 14
	// feedWindow is how much of a channel's history one call asks for, so a
	// long first read stores what it got before a later call fails.
	feedWindow = 24 * time.Hour
	// feedOverlap is how far back from now a clean read leaves the cursor,
	// in case Slack shows a post a moment after its timestamp.
	// The cursor can pass a post Slack shows late (or a host clock ahead of
	// Slack's): the sweep stores any post it finds that never was.
	feedOverlap   = time.Minute
	feedQuotaWait = 60 * time.Second
	// feedSweepRetry is the wait after a sweep that failed.
	feedSweepRetry = 10 * time.Minute
)

type feedReply struct {
	Author string    `json:"author"`
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
}

// feedContent is a stored post, sealed in sales_feed_items.content_sealed
// with the item's id.
type feedContent struct {
	Text       string           `json:"text"`
	Author     string           `json:"author"`
	Bot        bool             `json:"bot"`
	Source     string           `json:"source,omitempty"` // the posting app's name; "" for a person
	Link       string           `json:"link"`
	Replies    []feedReply      `json:"replies"`
	Fields     []leadfeed.Field `json:"fields"`  // filled when the post is read
	Reading    *feedReading     `json:"reading"` // filled when the post is read
	CRMLink    string           `json:"crm_link"`
	Candidates []string         `json:"candidates"` // outside addresses, for "Needs a look"
}

// feedChannel is one row of sales_feed_channels.
type feedChannel struct {
	ID            string
	IntegrationID string // "" when its connection was deleted
	ChannelID     string
	ChannelName   string
	Kind          string
	SignalType    string
	ReadBackDays  int
	CursorTS      string
	ReadSince     *time.Time
	ErrorCode     string
	LastSweptAt   *time.Time
	CreatedAt     time.Time
}

const feedChannelColumns = `id::text,COALESCE(integration_id::text,''),channel_id,channel_name,kind,signal_type,read_back_days,cursor_ts,read_since,error_code,last_swept_at,created_at`

func scanFeedChannel(row pgx.Row) (feedChannel, error) {
	var ch feedChannel
	err := row.Scan(&ch.ID, &ch.IntegrationID, &ch.ChannelID, &ch.ChannelName, &ch.Kind, &ch.SignalType, &ch.ReadBackDays, &ch.CursorTS, &ch.ReadSince, &ch.ErrorCode, &ch.LastSweptAt, &ch.CreatedAt)
	return ch, err
}

// loadFeedChannel is one of the brand's channels that is read: turned on
// and not removed.
func loadFeedChannel(ctx context.Context, q connectors.Querier, a *access, id string) (feedChannel, error) {
	return scanFeedChannel(q.QueryRow(ctx, `SELECT `+feedChannelColumns+`
		FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND enabled AND removed_at IS NULL`, a.WorkspaceID, a.Brand, id))
}

// feedReadStart is where reading a channel begins when it is added, turned
// on again or added back: read_back_days before now.
func feedReadStart(now time.Time, days int) time.Time {
	return now.AddDate(0, 0, -days)
}

// RunSalesFeeds queues a read of every channel that is due, every interval
// until ctx ends.
func (s *Server) RunSalesFeeds(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.enqueueDueSalesFeeds(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "sales_feed.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// enqueueDueSalesFeeds queues a poll for each enabled channel not read in
// the last two minutes, once per two-minute slot.
func (s *Server) enqueueDueSalesFeeds(ctx context.Context) (int, error) {
	// A removed channel, or one whose connection was deleted, is not read.
	rows, err := s.pool.Query(ctx, `SELECT id::text,workspace_id::text,brand_id::text FROM sales_feed_channels
		WHERE enabled AND removed_at IS NULL AND integration_id IS NOT NULL AND (last_read_at IS NULL OR last_read_at < now()-make_interval(secs=>$1)) ORDER BY last_read_at NULLS FIRST LIMIT 1000`, feedPollEvery.Seconds())
	if err != nil {
		return 0, err
	}
	type due struct{ id, ws, brand string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ws, &d.brand)
	})
	if err != nil {
		return 0, err
	}
	slot := strconv.FormatInt(time.Now().Unix()/int64(feedPollEvery.Seconds()), 10)
	for _, d := range list {
		if _, err := jobs.Enqueue(ctx, s.pool, jobs.NewJob{Kind: jobSalesFeedPoll, WorkspaceID: &d.ws, Payload: map[string]string{"id": d.id, "brand_id": d.brand},
			MaxAttempts: 5, IdempotencyKey: d.id + ":" + slot}); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

func (s *Server) salesFeedPollJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ID    string `json:"id"`
		Brand string `json:"brand_id"`
	}
	if json.Unmarshal(job.Payload, &p) != nil || job.WorkspaceID == nil || !validUUID(p.ID) || !validUUID(p.Brand) {
		return jobs.ErrPermanent
	}
	a := &access{WorkspaceID: *job.WorkspaceID, Brand: p.Brand}
	// A session advisory lock keeps two reads of a channel apart without
	// holding a transaction open during Slack's calls.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "sales-feed:"+p.ID).Scan(&locked); err != nil || !locked {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "sales-feed:"+p.ID)
	}()
	ch, err := loadFeedChannel(ctx, s.pool, a, p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // removed, turned off, or another brand's
	}
	if err != nil {
		return err
	}
	if ch.IntegrationID == "" {
		return nil // its connection was deleted: it waits to be saved with another
	}

	code, err := s.feedPoll(ctx, a, ch)
	done := func(code string) error {
		_, e := s.pool.Exec(ctx, `UPDATE sales_feed_channels SET error_code=$4,last_read_at=now(),updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, ch.ID, code)
		return e
	}
	switch {
	case code != "":
		// Slack refused this channel: say so on the channel and stop it
		// until the next read; other channels have their own jobs. The event
		// and the error log are for the change only, not every two minutes
		// while it stays refused.
		if code != ch.ErrorCode {
			track.Fail(ctx, s.log, track.Event{Name: "sales_feed.read_failed", ActorType: "system", Surface: "job", WorkspaceID: a.WorkspaceID, Outcome: track.Failure,
				Props: map[string]any{"feed_channel_id": ch.ID, "integration_id": ch.IntegrationID, "brand_id": a.Brand}}, code, err)
		} else {
			s.log.Debug("sales feed channel still refused", "feed_channel_id", ch.ID, "error_code", code)
		}
		return done(code)
	case err == nil:
		return done("")
	}
	// What was stored stays stored and the next try starts at the cursor;
	// the read counts, so the loop doesn't queue more while this one retries.
	_, _ = s.pool.Exec(ctx, `UPDATE sales_feed_channels SET last_read_at=now(),updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, ch.ID)
	if !isConnectorErr(err) {
		return err // Outflare's own, such as the database: not the channel's
	}
	if connectors.KindOf(err) == connectors.ErrQuota {
		return jobs.RetryLater(err, feedQuotaWait)
	}
	// Slack failed: the channel says so until a clean read, and the job
	// retries.
	_, _ = s.pool.Exec(ctx, `UPDATE sales_feed_channels SET error_code='slack_unavailable' WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, ch.ID)
	return err
}

func isConnectorErr(err error) bool {
	var ce *connectors.Error
	return errors.As(err, &ce)
}

// feedErr keeps a connector error's kind and drops its message, which can
// quote Slack's answer; other errors pass unchanged.
func feedErr(err error) error {
	var ce *connectors.Error
	if errors.As(err, &ce) {
		return connectors.Errf(ce.Kind, "Slack refused the read (%s)", ce.Kind)
	}
	return err
}

// feedRefused is the channel's error code for an error that stops it:
// slack_auth for a token Slack refused, slack_channel for a channel the app
// can't read; "" for anything else.
func feedRefused(err error) string {
	if !isConnectorErr(err) {
		return ""
	}
	switch connectors.KindOf(err) {
	case connectors.ErrAuth:
		return "slack_auth"
	case connectors.ErrInvalid:
		return "slack_channel"
	}
	return ""
}

// feedOpen opens the channel's connection, which must still be a Slack
// (sales channels) connection serving the channel's brand.
func (s *Server) feedOpen(ctx context.Context, a *access, ch feedChannel) (*connectors.Opened, connectors.FeedReader, error) {
	refused := connectors.Errf(connectors.ErrAuth, "the channel's Slack connection is gone or no longer serves its brand")
	list, err := s.integrations.List(ctx, s.pool, a.WorkspaceID, "slack_feeds")
	if err != nil {
		return nil, nil, err
	}
	serves := false
	for _, i := range list {
		if i.ID == ch.IntegrationID {
			serves = i.Serves(a.Brand, list)
		}
	}
	if !serves {
		return nil, nil, refused
	}
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, ch.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil, nil, refused
	}
	if err != nil {
		return nil, nil, feedErr(err)
	}
	reader, ok := o.Connector.(connectors.FeedReader)
	if !ok {
		return nil, nil, refused
	}
	return o, reader, nil
}

// feedPoll stores the channel's new posts, then, once an hour, looks back
// over the last two weeks for edits, deletions and new replies. It returns
// the channel's error code when Slack refused it.
func (s *Server) feedPoll(ctx context.Context, a *access, ch feedChannel) (string, error) {
	o, reader, err := s.feedOpen(ctx, a, ch)
	if err == nil {
		err = s.feedReadNew(ctx, a, ch, o, reader)
	}
	if err == nil && (ch.LastSweptAt == nil || time.Since(*ch.LastSweptAt) >= feedSweepEvery) {
		err = s.feedSweep(ctx, a, ch, o, reader)
	}
	if err == nil {
		return "", nil
	}
	code := feedRefused(err)
	if code == "slack_auth" && o != nil {
		_ = s.integrations.Checked(ctx, s.pool, a.WorkspaceID, o.ID, nil, err)
	}
	return code, feedErr(err)
}

func feedStamp(t time.Time) string {
	return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000)
}

// feedTime reads a Slack timestamp ("1790000000.000100") to the microsecond.
func feedTime(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	s, _ := strconv.ParseInt(sec, 10, 64)
	frac = (frac + "000000")[:6]
	us, _ := strconv.ParseInt(frac, 10, 64)
	return time.Unix(s, us*1000)
}

// feedReadNew stores the posts after the cursor, a day of history per call,
// each in its own transaction that also moves the cursor to it.
func (s *Server) feedReadNew(ctx context.Context, a *access, ch feedChannel, o *connectors.Opened, reader connectors.FeedReader) error {
	// To the microsecond, as Slack's timestamps are: the last day's window
	// of a first read ends exactly at now, and must count as reaching it.
	// With a finer clock it ended a moment before, was read as one more
	// closed window, and left the cursor at now instead of feedOverlap
	// behind it, so a post in that last moment waited for the hourly sweep.
	now := time.Now().Truncate(time.Microsecond)
	oldest := ch.CursorTS
	if oldest == "" {
		oldest = feedStamp(feedReadStart(now, ch.ReadBackDays))
	}
	first := oldest
	base := ""
	for {
		hi := feedTime(oldest).Add(feedWindow)
		latest := ""
		// A window that reaches into the overlap is the last one and has no
		// end, so the cursor it leaves never passes now less the overlap.
		if hi.Before(now.Add(-feedOverlap)) {
			// Both ends are exclusive: a post exactly at hi is in this
			// window, and the next one starts after hi.
			latest = feedStamp(hi.Add(time.Microsecond))
		}
		msgs, err := reader.History(ctx, o.Client, o.Creds, ch.ChannelID, oldest, latest)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			// Stored already, in this brand or in the one its person moved to,
			// or stored once and erased with its person.
			var seen bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sales_feed_items WHERE workspace_id=$1 AND feed_channel_id=$2 AND slack_ts=$3)
				OR EXISTS(SELECT 1 FROM sales_feed_erased WHERE workspace_id=$1 AND brand_id=$4 AND feed_channel_id=$2 AND slack_ts=$3)`, a.WorkspaceID, ch.ID, m.ID, a.Brand).Scan(&seen); err != nil {
				return err
			}
			if seen {
				continue // edits and replies are the sweep's
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
				if _, _, err := s.feedStore(ctx, tx, a, ch, m, base, replies); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE sales_feed_channels SET cursor_ts=$4,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND cursor_ts<$4`, a.WorkspaceID, a.Brand, ch.ID, m.ID)
				return err
			}); err != nil {
				return err
			}
		}
		if latest == "" {
			break
		}
		// The whole window is read and stored: a later failure starts after
		// it, so a long gap is never read twice.
		oldest = feedStamp(hi)
		if _, err := s.pool.Exec(ctx, `UPDATE sales_feed_channels SET cursor_ts=$4,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND cursor_ts<$4`, a.WorkspaceID, a.Brand, ch.ID, oldest); err != nil {
			return err
		}
	}
	// Read to the end: a quiet channel's next read starts near now, never
	// before where the first read began.
	cursor := max(feedStamp(now.Add(-feedOverlap)), first)
	_, err := s.pool.Exec(ctx, `UPDATE sales_feed_channels SET cursor_ts=$4,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND cursor_ts<$4`, a.WorkspaceID, a.Brand, ch.ID, cursor)
	return err
}
