// Package platforms keeps Outflare in step with the outside sequencers a
// workspace connected (EmailBison, Smartlead, Instantly, Apollo) for
// mailbox allocation.
//
// A sync reads each platform account's mailboxes and campaigns, links its
// live campaigns to sequences, records where each mailbox is attached,
// and accepts changes people made inside the platform as manual changes.
// The allocator plans on that picture; its attach and detach calls run
// here as jobs, and the next sync confirms them by reading back.
package platforms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/links"
)

// JobSync syncs one platform account.
const JobSync = "platforms.sync"

// SyncEvery is how often each platform account is read.
const SyncEvery = 5 * time.Minute

// Service syncs platform accounts and carries out platform operations.
type Service struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// Now is overridable in tests.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobSync, s.syncJob)
	q.Handle(allocation.JobPlatform, s.applyJob)
	q.Handle(JobWarmup, s.warmupJob)
	// A newly connected sequencer is read straight away.
	s.Store.OnConnect = append(s.Store.OnConnect, func(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
		if !s.observes(i.Connector) {
			return nil
		}
		return RequestSync(ctx, tx, i.WorkspaceID, i.ID, 0)
	})
}

type syncPayload struct {
	WorkspaceID   string `json:"workspace_id"`
	IntegrationID string `json:"integration_id"`
}

// RequestSync queues a sync of one platform account after delay. Requests
// in the same half minute share one run.
func RequestSync(ctx context.Context, q jobs.Querier, ws, integrationID string, delay time.Duration) error {
	const window = 30 * time.Second
	at := time.Now().Add(delay).Truncate(window).Add(window)
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{
		Kind: JobSync, WorkspaceID: &ws, Payload: syncPayload{ws, integrationID}, RunAt: at, MaxAttempts: 2,
		IdempotencyKey: integrationID + ":" + strconv.FormatInt(at.Unix(), 10),
	})
	return err
}

func (s *Service) observes(connector string) bool {
	c, ok := s.Store.Registry.Get(connector)
	if !ok {
		return false
	}
	_, ok = c.(connectors.Observer)
	return ok
}

// Loop queues a sync for every platform account that is due.
func (s *Service) Loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.queueDue(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("platform sync loop", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) queueDue(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id::text, i.workspace_id::text, i.connector FROM integrations i
		LEFT JOIN platform_syncs ps ON ps.integration_id = i.id
		WHERE ps.attempted_at IS NULL OR ps.attempted_at < $1::timestamptz - $2::interval`,
		s.now(), strconv.Itoa(int(SyncEvery.Seconds()))+" seconds")
	if err != nil {
		return err
	}
	type due struct{ id, ws, connector string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ws, &d.connector)
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		if s.observes(d.connector) {
			if err := RequestSync(ctx, s.Pool, d.ws, d.id, 0); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) syncJob(ctx context.Context, job jobs.Job) error {
	var p syncPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	_, err := s.Sync(ctx, p.WorkspaceID, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) || errors.Is(err, ErrNotPlatform) {
		return nil // disconnected since
	}
	return err
}

// Result is what one sync found and changed.
type Result struct {
	Mailboxes int `json:"mailboxes"`
	Campaigns int `json:"campaigns"`
	// Accepted are changes made inside the platform, taken as manual.
	Accepted int `json:"accepted"`
	// Confirmed are Outflare's own attaches the platform now shows.
	Confirmed int `json:"confirmed"`
	// Replies are new replies brought into the inbox.
	Replies int `json:"replies"`
	// OptOutsPulled are entries of the platform's blocklist added to the
	// opt-out list; OptOutsPushed are opt-out list entries written to it.
	OptOutsPulled int `json:"opt_outs_pulled"`
	OptOutsPushed int `json:"opt_outs_pushed"`
}

// ErrNotPlatform is returned for a connection that cannot report mailboxes.
var ErrNotPlatform = errors.New("this connection does not report mailboxes and campaigns")

// Sync reads one platform account and brings Outflare in step with it.
// A failed read is recorded on the account, and every mailbox seen there
// then counts as busy until a sync succeeds.
func (s *Service) Sync(ctx context.Context, ws, integrationID string) (Result, error) {
	var res Result
	op, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return res, err
	}
	obs, ok := op.Connector.(connectors.Observer)
	if !ok {
		return res, ErrNotPlatform
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO platform_syncs (integration_id, workspace_id, attempted_at) VALUES ($1, $2, $3)
		ON CONFLICT (integration_id) DO UPDATE SET attempted_at = $3`, integrationID, ws, s.now()); err != nil {
		return res, err
	}
	_, controls := op.Connector.(connectors.Controller)
	control := links.ReadOnly
	if controls {
		control = links.Full
	}
	snap, err := obs.Observe(ctx, op.Client, op.Creds)
	if err != nil {
		_ = s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, err)
		_, _ = s.Pool.Exec(ctx, `UPDATE platform_syncs SET error = $2 WHERE integration_id = $1`, integrationID, err.Error())
		return res, err
	}
	platform := op.Integration.Name
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The allocator holds this lock while it plans, so a sync never
		// changes assignments under a plan.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('allocation:' || $1::text, 0))`, ws); err != nil {
			return err
		}
		// Live campaigns are linked to sequences, so their mailboxes are
		// planned with everything else. Finished ones are only refreshed.
		live := map[string]links.Link{}
		// A platform Outflare cannot control (Apollo picks mailboxes
		// itself) is only watched: its campaigns are not linked and its
		// mailboxes only flag the ones Outflare already has.
		campaigns := snap.Campaigns
		if !controls {
			campaigns = nil
		}
		for _, c := range campaigns {
			st := links.SequenceStatus(c.Status)
			_, found, err := links.Find(ctx, tx, integrationID, c.ExternalID)
			if err != nil {
				return err
			}
			if !found && (st == "archived" || st == "draft") {
				continue
			}
			id, seqID, err := links.Upsert(ctx, tx, ws, links.Link{IntegrationID: integrationID, ExternalID: c.ExternalID, Name: c.Name,
				Status: c.Status, Control: control, SequenceDays: c.SequenceDays, DailyLimit: c.DailyLimit})
			if err != nil {
				return err
			}
			if found && st != "draft" {
				// The platform decides whether its campaign runs.
				if _, err := tx.Exec(ctx, `UPDATE sequences SET status = $2, updated_at = now() WHERE id = $1 AND status <> $2`, seqID, st); err != nil {
					return err
				}
			}
			if st != "archived" {
				live[c.ExternalID] = links.Link{ID: id, SequenceID: seqID, Name: c.Name}
			}
			res.Campaigns++
		}

		seen := []string{}
		for _, m := range snap.Mailboxes {
			email := strings.ToLower(strings.TrimSpace(m.Email))
			if email == "" || !strings.Contains(email, "@") {
				continue
			}
			mailboxID, err := s.mailbox(ctx, tx, ws, op.Integration.Connector, email, m, controls)
			if err != nil {
				return err
			}
			if mailboxID == "" {
				continue
			}
			seen = append(seen, mailboxID)
			attached := []string{}
			for _, id := range m.CampaignIDs {
				if _, ok := live[id]; ok {
					attached = append(attached, id)
				}
			}
			slices.Sort(attached)
			if _, err := tx.Exec(ctx, `
				INSERT INTO mailbox_platforms (mailbox_id, integration_id, workspace_id, external_id, status, status_reason, daily_limit,
				                               warmup_enabled, warmup_score, campaign_ids, observed_at)
				VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11)
				ON CONFLICT (mailbox_id, integration_id) DO UPDATE SET external_id = $4, status = $5, status_reason = NULLIF($6, ''),
				    daily_limit = $7, warmup_enabled = $8, warmup_score = $9, campaign_ids = $10, observed_at = $11`,
				mailboxID, integrationID, ws, m.ExternalID, m.Status, m.StatusReason, m.DailyLimit, m.WarmupEnabled, m.WarmupScore,
				attached, s.now()); err != nil {
				return err
			}
			// Warm-up already running here, on a mailbox Outflare isn't
			// warming up, is taken as this platform's: Outflare shows it
			// and leaves the mailbox out of its own warm-up.
			if _, warms := op.Connector.(connectors.WarmupController); warms && m.WarmupEnabled {
				var adopted bool
				if err := tx.QueryRow(ctx, `
					UPDATE mailboxes SET warmup_enabled = true, warmup_integration_id = $2, warmup_started_at = COALESCE(warmup_started_at, $3), updated_at = $3
					WHERE id = $1 AND NOT warmup_enabled AND warmup_integration_id IS NULL
					RETURNING true`, mailboxID, integrationID, s.now()).Scan(&adopted); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if adopted {
					if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "mailbox.warmup_updated",
						TargetType: "mailbox", TargetID: mailboxID,
						Data: map[string]any{"enabled": true, "provider": integrationID, "source": "external",
							"reason": "Warm-up was already on in " + op.Integration.Name + "."}}); err != nil {
						return err
					}
				}
			}
			n, c, err := s.reconcile(ctx, tx, ws, integrationID, platform, mailboxID, attached, live)
			if err != nil {
				return err
			}
			res.Accepted += n
			res.Confirmed += c
			res.Mailboxes++
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mailbox_platforms WHERE integration_id = $1 AND NOT (mailbox_id = ANY($2::uuid[]))`,
			integrationID, seen); err != nil {
			return err
		}
		// Assignments through this account whose mailbox the platform no
		// longer shows were detached there, or the mailbox was removed.
		n, err := s.acceptDetached(ctx, tx, ws, integrationID, platform, seen)
		if err != nil {
			return err
		}
		res.Accepted += n
		if _, err := tx.Exec(ctx, `
			UPDATE platform_syncs SET synced_at = $2, error = NULL, mailboxes = $3, campaigns = $4, control = $5,
			       reviewed_at = CASE WHEN reviewed_at IS NULL AND $6 THEN $2 ELSE reviewed_at END
			WHERE integration_id = $1`,
			integrationID, s.now(), res.Mailboxes, res.Campaigns, control, res.Campaigns == 0); err != nil {
			return err
		}
		return allocation.Request(ctx, tx, ws)
	})
	if err == nil {
		_ = s.Store.Checked(ctx, s.Pool, ws, integrationID, &op.Integration.Account, nil)
		// Replies come after the mailboxes, so a new sender email has
		// its mailbox to land in.
		res.Replies = s.syncReplies(ctx, ws, op)
		res.OptOutsPulled, res.OptOutsPushed = s.syncBlocklist(ctx, ws, op)
	}
	return res, err
}

// mailbox finds the workspace's mailbox for email, creating one without a
// login when the platform is the only place it exists.
func (s *Service) mailbox(ctx context.Context, tx pgx.Tx, ws, connector, email string, m connectors.MailboxState, create bool) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM mailboxes WHERE workspace_id = $1 AND lower(email) = $2`, ws, email).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	if !create {
		return "", nil
	}
	limit := 30
	if m.DailyLimit != nil && *m.DailyLimit >= 1 {
		limit = min(*m.DailyLimit, 500)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO mailboxes (workspace_id, email, display_name, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port,
		                       imap_username, credentials, status, status_reason, daily_limit, source, has_login)
		VALUES ($1, $2, $3, 'other', '', 0, '', '', 0, '', '\x', 'active', $4, $5, $6, false)
		RETURNING id::text`,
		ws, email, m.Name, "Sends through "+connector+"; Outflare has no login for it.", limit, connector).Scan(&id)
	return id, err
}

// reconcile compares where the platform shows a mailbox attached with its
// assignment. Outflare's own pending attach is confirmed; anything else a
// person did in the platform is accepted as a manual change. A mailbox
// attached in two places keeps its assignment, and the second place shows
// as a conflict until someone removes it.
func (s *Service) reconcile(ctx context.Context, tx pgx.Tx, ws, integrationID, platform, mailboxID string, attached []string, live map[string]links.Link) (accepted, confirmed int, err error) {
	var cur struct {
		seq, link string
		confirmed bool
	}
	err = tx.QueryRow(ctx, `SELECT sequence_id::text, COALESCE(link_id::text, ''), confirmed_at IS NOT NULL FROM mailbox_assignments WHERE mailbox_id = $1`,
		mailboxID).Scan(&cur.seq, &cur.link, &cur.confirmed)
	free := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !free {
		return 0, 0, err
	}
	for _, ext := range attached {
		l := live[ext]
		switch {
		case !free && cur.link == l.ID:
			if !cur.confirmed {
				if _, err := tx.Exec(ctx, `UPDATE mailbox_assignments SET confirmed_at = $2 WHERE mailbox_id = $1`, mailboxID, s.now()); err != nil {
					return 0, 0, err
				}
				confirmed++
			}
			return accepted, confirmed, nil
		case free:
			reason := fmt.Sprintf("Attached to %s in %s, outside Outflare. Accepted as a manual change.", l.Name, platform)
			if _, err := tx.Exec(ctx, `
				INSERT INTO mailbox_assignments (mailbox_id, workspace_id, sequence_id, source, reason, link_id, confirmed_at)
				VALUES ($1, $2, $3, 'external', $4, $5, $6)`, mailboxID, ws, l.SequenceID, reason, l.ID, s.now()); err != nil {
				return 0, 0, err
			}
			if err := record(ctx, tx, ws, mailboxID, allocation.Assign, "", l.SequenceID, l.ID, reason); err != nil {
				return 0, 0, err
			}
			return 1, 0, nil
		}
	}
	return 0, 0, nil
}

// acceptDetached ends confirmed assignments through integrationID whose
// mailbox the platform no longer shows attached to the linked campaign.
func (s *Service) acceptDetached(ctx context.Context, tx pgx.Tx, ws, integrationID, platform string, seen []string) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.mailbox_id::text, a.sequence_id::text, l.id::text, l.external_name
		FROM mailbox_assignments a
		JOIN campaign_links l ON l.id = a.link_id AND l.integration_id = $1
		LEFT JOIN mailbox_platforms p ON p.mailbox_id = a.mailbox_id AND p.integration_id = $1
		WHERE a.workspace_id = $2 AND a.confirmed_at IS NOT NULL AND a.state = 'active'
		  AND (p.mailbox_id IS NULL OR NOT (l.external_id = ANY(p.campaign_ids)))`, integrationID, ws)
	if err != nil {
		return 0, err
	}
	type gone struct{ mailbox, seq, link, name string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (gone, error) {
		var g gone
		return g, r.Scan(&g.mailbox, &g.seq, &g.link, &g.name)
	})
	if err != nil {
		return 0, err
	}
	for _, g := range list {
		reason := fmt.Sprintf("Removed from %s in %s, outside Outflare. Accepted as a manual change.", g.name, platform)
		if !slices.Contains(seen, g.mailbox) {
			reason = fmt.Sprintf("%s no longer shows this mailbox, so it left %s.", platform, g.name)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mailbox_assignments WHERE mailbox_id = $1`, g.mailbox); err != nil {
			return 0, err
		}
		if err := record(ctx, tx, ws, g.mailbox, allocation.Release, g.seq, "", g.link, reason); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

func record(ctx context.Context, tx pgx.Tx, ws, mailboxID, action, from, to, linkID, reason string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO allocation_changes (workspace_id, mailbox_id, action, from_sequence_id, to_sequence_id, source, reason, link_id)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, 'external', $6, NULLIF($7, '')::uuid)`,
		ws, mailboxID, action, from, to, reason, linkID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "mailbox.allocation." + action,
		TargetType: "mailbox", TargetID: mailboxID,
		Data: map[string]any{"from_sequence_id": from, "to_sequence_id": to, "source": "external", "reason": reason}})
}

func (s *Service) applyJob(ctx context.Context, job jobs.Job) error {
	var p allocation.PlatformOp
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	err := s.Apply(ctx, p)
	if err != nil && connectors.KindOf(err) != connectors.ErrUnavailable && connectors.KindOf(err) != connectors.ErrQuota {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	return err
}

// Apply attaches or detaches one mailbox in a platform campaign. A refused
// attach releases the assignment with the platform's words as the reason,
// so the allocator can try another mailbox.
func (s *Service) Apply(ctx context.Context, p allocation.PlatformOp) error {
	var integrationID, campaignID, campaignName, seqID string
	err := s.Pool.QueryRow(ctx, `SELECT integration_id::text, external_id, external_name, sequence_id::text FROM campaign_links WHERE id = $1 AND workspace_id = $2`,
		p.LinkID, p.WorkspaceID).Scan(&integrationID, &campaignID, &campaignName, &seqID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // unlinked since
	}
	if err != nil {
		return err
	}
	var m connectors.MailboxState
	err = s.Pool.QueryRow(ctx, `
		SELECT p.external_id, m.email FROM mailbox_platforms p JOIN mailboxes m ON m.id = p.mailbox_id
		WHERE p.mailbox_id = $1 AND p.integration_id = $2`, p.MailboxID, integrationID).Scan(&m.ExternalID, &m.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		if p.Op == allocation.OpDetach {
			return nil // the platform no longer has it
		}
		return s.refused(ctx, p, seqID, fmt.Sprintf("The platform account no longer shows this mailbox, so it could not join %s.", campaignName))
	}
	if err != nil {
		return err
	}
	if p.Op == allocation.OpAttach {
		// Only attach while the assignment still wants it.
		var still bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailbox_assignments WHERE mailbox_id = $1 AND link_id = $2)`,
			p.MailboxID, p.LinkID).Scan(&still); err != nil || !still {
			return err
		}
	}
	op, err := s.Store.Open(ctx, s.Pool, p.WorkspaceID, integrationID)
	if err != nil {
		return err
	}
	ctl, ok := op.Connector.(connectors.Controller)
	if !ok {
		return s.refused(ctx, p, seqID, op.Integration.Name+" cannot be told which mailboxes a campaign uses.")
	}
	if p.Op == allocation.OpAttach {
		err = ctl.Attach(ctx, op.Client, op.Creds, campaignID, []connectors.MailboxState{m})
	} else {
		err = ctl.Detach(ctx, op.Client, op.Creds, campaignID, []connectors.MailboxState{m})
	}
	if err != nil {
		_ = s.Store.Checked(ctx, s.Pool, p.WorkspaceID, integrationID, nil, err)
		if p.Op == allocation.OpAttach && connectors.KindOf(err) == connectors.ErrInvalid {
			return s.refused(ctx, p, seqID, fmt.Sprintf("%s refused to add it to %s: %s", op.Integration.Name, campaignName, err.Error()))
		}
		return err
	}
	// Read back soon: the assignment counts once the platform shows it.
	return RequestSync(ctx, s.Pool, p.WorkspaceID, integrationID, 0)
}

func (s *Service) refused(ctx context.Context, p allocation.PlatformOp, seqID, reason string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM mailbox_assignments WHERE mailbox_id = $1 AND link_id = $2 AND confirmed_at IS NULL`, p.MailboxID, p.LinkID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO allocation_changes (workspace_id, mailbox_id, action, from_sequence_id, source, reason, link_id)
			VALUES ($1, $2, 'release', $3, 'automatic', $4, $5)`, p.WorkspaceID, p.MailboxID, seqID, reason, p.LinkID); err != nil {
			return err
		}
		// Not offered to this campaign again until a person clears it.
		if _, err := tx.Exec(ctx, `
			INSERT INTO allocation_declines (workspace_id, mailbox_id, sequence_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			p.WorkspaceID, p.MailboxID, seqID); err != nil {
			return err
		}
		return allocation.Request(ctx, tx, p.WorkspaceID)
	})
}
