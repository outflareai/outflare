package platforms

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// statsDays is how many days of each mailbox's counts a sync reads,
	// today included. Sequencers correct recent days (a late bounce or
	// reply), so the whole window is read and replaced each time.
	statsDays = 7
	// statsEvery is the least time between two reads of one account's
	// counts: they move slowly and some sequencers answer a day at a time.
	statsEvery = time.Hour
	// statsKeep is how long a day's counts are kept.
	statsKeep = 400 * 24 * time.Hour
)

// syncStats reads how much each of the account's mailboxes sent, and the
// replies and bounces that came back, per day, and stores them
// (mailbox_platform_days). A failed read is an event and a log line; it
// never fails the mailbox sync around it.
func (s *Service) syncStats(ctx context.Context, ws string, op *connectors.Opened) int {
	src, ok := op.Connector.(connectors.MailboxStatReader)
	if !ok {
		return 0
	}
	connector, id := op.Integration.Connector, op.Integration.ID
	fail := func(code string, err error) int {
		metrics.MailboxStatsSync(connector, code)
		track.Fail(ctx, s.Log, track.Event{Name: "mailbox.stats_sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"connector": connector, "integration_id": id}}, code, err)
		return 0
	}
	var last *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT max(observed_at) FROM mailbox_platform_days WHERE workspace_id = $1 AND integration_id = $2`, ws, id).Scan(&last); err != nil {
		return fail("internal", err)
	}
	if last != nil && s.now().Sub(*last) < statsEvery {
		return 0
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT lower(m.email), m.id::text FROM mailbox_platforms p JOIN mailboxes m ON m.id = p.mailbox_id
		WHERE p.workspace_id = $1 AND p.integration_id = $2`, ws, id)
	if err != nil {
		return fail("internal", err)
	}
	byEmail := map[string]string{}
	var email, mailbox string
	if _, err := pgx.ForEachRow(rows, []any{&email, &mailbox}, func() error {
		byEmail[email] = mailbox
		return nil
	}); err != nil {
		return fail("internal", err)
	}
	if len(byEmail) == 0 {
		return 0
	}
	emails := make([]string, 0, len(byEmail))
	for e := range byEmail {
		emails = append(emails, e)
	}
	to := s.now().UTC()
	from := to.AddDate(0, 0, -(statsDays - 1))
	days, err := src.MailboxDays(ctx, op.Client, op.Creds, emails, from, to)
	if err != nil {
		return fail("provider_"+connectors.KindOf(err), err)
	}
	n := 0
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The window is replaced whole: a day the sequencer no longer
		// reports has nothing on it.
		if _, err := tx.Exec(ctx, `DELETE FROM mailbox_platform_days WHERE workspace_id = $1 AND integration_id = $2 AND (day >= $3::date OR day < $4::date)`,
			ws, id, from.Format(time.DateOnly), s.now().Add(-statsKeep).Format(time.DateOnly)); err != nil {
			return err
		}
		for _, d := range days {
			mailbox, ok := byEmail[strings.ToLower(d.Email)]
			if !ok || d.Day < from.Format(time.DateOnly) || d.Day > to.Format(time.DateOnly) || (d.Sent == 0 && d.Replies == 0 && d.Bounced == 0) {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO mailbox_platform_days (mailbox_id, integration_id, workspace_id, brand_id, day, sent, replies, bounced, observed_at)
				SELECT m.id, $2, $3, m.brand_id, $4::date, $5, $6, $7, $8 FROM mailboxes m WHERE m.id = $1 AND m.workspace_id = $3 AND m.brand_id IS NOT NULL
				ON CONFLICT (mailbox_id, integration_id, day) DO UPDATE SET sent = mailbox_platform_days.sent + EXCLUDED.sent,
				    replies = mailbox_platform_days.replies + EXCLUDED.replies, bounced = mailbox_platform_days.bounced + EXCLUDED.bounced`,
				mailbox, id, ws, d.Day, max(d.Sent, 0), max(d.Replies, 0), max(d.Bounced, 0), s.now()); err != nil {
				return err
			}
			n++
		}
		// A marker for today, so an account with nothing to report is
		// still not read again within the hour.
		_, err := tx.Exec(ctx, `
			INSERT INTO mailbox_platform_days (mailbox_id, integration_id, workspace_id, brand_id, day, observed_at)
			SELECT m.id, $2, $3, m.brand_id, $4::date, $5 FROM mailboxes m WHERE m.id = $1 AND m.workspace_id = $3 AND m.brand_id IS NOT NULL
			ON CONFLICT (mailbox_id, integration_id, day) DO UPDATE SET observed_at = $5`,
			byEmail[emails[0]], id, ws, to.Format(time.DateOnly), s.now())
		return err
	})
	if err != nil {
		return fail("internal", err)
	}
	metrics.MailboxStatsSync(connector, "success")
	track.Emit(ctx, track.Event{Name: "mailbox.stats_synced", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"connector": connector, "integration_id": id, "mailboxes": len(byEmail), "days": n}})
	return n
}
