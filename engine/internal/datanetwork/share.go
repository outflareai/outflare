package datanetwork

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Jobs.
const (
	// JobShare passes a joined workspace's settled sends to the network.
	JobShare = "datanetwork.share"
	// JobWithdraw has the network remove what a workspace that left shared.
	JobWithdraw = "datanetwork.withdraw"
)

const (
	// shareBatch is the most sends one batch reads.
	shareBatch = 2000
	// shareBatches is the most batches one job sends before the next tick
	// carries on.
	shareBatches = 10
)

// sharedSQL is what a workspace shares: its sequence sends that went out
// ($1 workspace, sent after $2 and by $3, at most $4 rows), each with the
// person's name (only to work out the address shape; the name never
// leaves) and whether a bounce report came back for the address. Left out:
// addresses or domains anyone opted out or blocked (unsubscribed,
// complained, do not contact, added by hand), whose existence is never
// shared, and personal mail providers ($5), whose shapes say nothing about
// a company.
const sharedSQL = `
	SELECT lower(m.to_email) AS email, m.sent_at,
	       coalesce(l.first_name, '') AS first_name, coalesce(l.last_name, '') AS last_name,
	       EXISTS (SELECT 1 FROM suppressions b WHERE b.workspace_id = m.workspace_id AND b.kind = 'email'
	               AND b.value = lower(m.to_email) AND b.reason = 'bounced' AND b.source = 'bounce_report') AS bounced
	FROM messages m
	LEFT JOIN leads l ON l.id = m.lead_id AND l.workspace_id = m.workspace_id
	WHERE m.workspace_id = $1 AND m.status = 'sent' AND m.sent_at > $2 AND m.sent_at <= $3
	  AND position('@' IN m.to_email) > 1
	  AND split_part(lower(m.to_email), '@', 2) <> ALL ($5::text[])
	  AND NOT EXISTS (SELECT 1 FROM suppressions s WHERE s.workspace_id = m.workspace_id
	      AND ((s.kind = 'email' AND s.value = lower(m.to_email))
	        OR (s.kind = 'domain' AND s.value = split_part(lower(m.to_email), '@', 2)))
	      AND NOT (s.reason = 'bounced' AND s.source = 'bounce_report'))
	ORDER BY m.sent_at
	LIMIT $4`

// Register adds the job handlers.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobShare, s.shareJob)
	q.Handle(JobWithdraw, s.withdrawJob)
}

// Run queues sharing for joined workspaces, and withdrawals still to do,
// every tick until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "data_network.scheduler_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one round of Run.
func (s *Service) Tick(ctx context.Context) error {
	var members, pending int
	var overdue, lag float64
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'joined')::int,
		       count(*) FILTER (WHERE status = 'left' AND withdrawn_at IS NULL)::int,
		       coalesce(extract(epoch FROM max(now() - withdraw_due_at) FILTER (WHERE status = 'left' AND withdrawn_at IS NULL)), 0)::float8,
		       coalesce(extract(epoch FROM max(now() - $1 * interval '1 second' - coalesce(shared_through, joined_at))
		                FILTER (WHERE status = 'joined')), 0)::float8
		FROM data_network_members`, int64(SettleAfter/time.Second)).Scan(&members, &pending, &overdue, &lag); err != nil {
		return err
	}
	metrics.SetDataNetwork(members, pending, max(overdue, 0), max(lag, 0))
	if s.Network() == nil {
		return nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT workspace_id::text, status, coalesce(withdraw_due_at, now()) FROM data_network_members
		WHERE status = 'joined' OR (status = 'left' AND withdrawn_at IS NULL)`)
	if err != nil {
		return err
	}
	type due struct {
		ws, status string
		at         time.Time
	}
	var list []due
	var d due
	if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.status, &d.at}, func() error {
		list = append(list, d)
		return nil
	}); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, d := range list {
		var err error
		if d.status == Joined {
			ws := d.ws
			_, err = jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: JobShare, WorkspaceID: &ws, MaxAttempts: 5,
				IdempotencyKey: "datanetwork.share:" + ws + ":" + now.Format("2006-01-02T15")})
		} else {
			err = enqueueWithdraw(ctx, s.Pool, d.ws, d.at, now.Format("2006-01-02"))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// lockWorkspace holds a lock that keeps sharing and withdrawing for one
// workspace from running at once, so nothing is shared after a withdrawal
// started. Call the returned func to let go.
func (s *Service) lockWorkspace(ctx context.Context, ws string) (func(), error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('datanetwork:' || $1))`, ws); err != nil {
		conn.Release()
		return nil, err
	}
	return func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('datanetwork:' || $1))`, ws)
		conn.Release()
	}, nil
}

func (s *Service) shareJob(ctx context.Context, j jobs.Job) error {
	if j.WorkspaceID == nil {
		return fmt.Errorf("%w: no workspace", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	n := s.For(ctx, ws)
	if n == nil {
		return nil // no network, or not turned on for this workspace
	}
	unlock, err := s.lockWorkspace(ctx, ws)
	if err != nil {
		return err
	}
	defer unlock()
	ev := track.Event{Name: "data_network.shared", WorkspaceID: ws, ActorType: "system", Surface: "job"}
	var delivered, bounced int
	for range shareBatches {
		d, b, more, err := s.shareBatch(ctx, n, ws)
		delivered, bounced = delivered+d, bounced+b
		if err != nil {
			metrics.DataNetworkShareRun("failed")
			track.Fail(ctx, s.Log, ev, "internal_error", err)
			return err
		}
		if !more {
			break
		}
	}
	metrics.DataNetworkShareRun("success")
	if delivered+bounced > 0 {
		ev.Props = map[string]any{"delivered": delivered, "bounced": bounced}
		track.Emit(ctx, ev)
	}
	return nil
}

type sharedRow struct {
	email, first, last string
	sentAt             time.Time
	bounced            bool
}

// shareBatch passes one batch of a joined workspace's settled sends to the
// network and moves its cursor past them.
func (s *Service) shareBatch(ctx context.Context, n Network, ws string) (delivered, bounced int, more bool, err error) {
	m, err := Load(ctx, s.Pool, ws)
	if err != nil || m.Status != Joined {
		return 0, 0, false, err
	}
	after := time.Time{}
	if m.SharedThrough != nil {
		after = *m.SharedThrough
	}
	upto := time.Now().Add(-SettleAfter)
	if !after.Before(upto) {
		return 0, 0, false, nil
	}
	rows, err := s.Pool.Query(ctx, sharedSQL, ws, after, upto, shareBatch, outcomes.FreeMailDomains())
	if err != nil {
		return 0, 0, false, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sharedRow, error) {
		var x sharedRow
		return x, r.Scan(&x.email, &x.sentAt, &x.first, &x.last, &x.bounced)
	})
	if err != nil {
		return 0, 0, false, err
	}
	through := upto
	if len(list) == shareBatch {
		// More to come. Stop before the last send time, so sends sharing
		// it are all in the next batch; unless the whole batch has one
		// send time, which can't be split.
		more = true
		last := list[len(list)-1].sentAt
		cut := len(list)
		for cut > 0 && list[cut-1].sentAt.Equal(last) {
			cut--
		}
		if cut > 0 {
			list = list[:cut]
		}
		through = list[len(list)-1].sentAt
	}
	batch := contributions(list)
	if len(batch) > 0 {
		if err := n.Contribute(ctx, ws, batch); err != nil {
			return 0, 0, false, err
		}
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE data_network_members SET shared_through = $2, updated_at = now()
		WHERE workspace_id = $1 AND status = 'joined'`, ws, through); err != nil {
		return 0, 0, false, err
	}
	for _, c := range batch {
		if c.Outcome == Bounced {
			bounced++
		} else {
			delivered++
		}
	}
	metrics.DataNetworkShared(delivered, bounced)
	return delivered, bounced, more, nil
}

// contributions turns sends into one contribution per address: its latest
// send, bounced if a bounce report ever came back, and the shapes it fits
// for the person's name. The name itself stays here.
func contributions(rows []sharedRow) []Contribution {
	idx := map[string]int{}
	out := make([]Contribution, 0, len(rows))
	for _, r := range rows {
		at := lastAt(r.email)
		if at < 1 || at == len(r.email)-1 {
			continue
		}
		outcome := Delivered
		if r.bounced {
			outcome = Bounced
		}
		if i, ok := idx[r.email]; ok {
			c := &out[i]
			if r.sentAt.After(c.SentAt) {
				c.SentAt = r.sentAt
			}
			if r.bounced {
				c.Outcome = Bounced
			}
			if len(c.Shapes) == 0 {
				c.Shapes = emailfind.ShapesOf(r.first, r.last, r.email)
			}
			continue
		}
		idx[r.email] = len(out)
		out = append(out, Contribution{Email: r.email, Domain: r.email[at+1:], Outcome: outcome, SentAt: r.sentAt,
			Shapes: emailfind.ShapesOf(r.first, r.last, r.email)})
	}
	return out
}

func lastAt(email string) int {
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			return i
		}
	}
	return -1
}

func (s *Service) withdrawJob(ctx context.Context, j jobs.Job) error {
	if j.WorkspaceID == nil {
		return fmt.Errorf("%w: no workspace", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	unlock, err := s.lockWorkspace(ctx, ws)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := Load(ctx, s.Pool, ws)
	if err != nil {
		return err
	}
	if m.Status != Left || m.WithdrawnAt != nil {
		return nil // came back, or already done
	}
	ev := track.Event{Name: "data_network.withdrawn", WorkspaceID: ws, ActorType: "system", Surface: "job"}
	n := s.Network()
	if n == nil {
		// Joining needs a network, so one was here; without it nothing can
		// be withdrawn. Keep trying: the alert on overdue withdrawals says so.
		err := fmt.Errorf("data network: no network registered to withdraw from")
		track.Fail(ctx, s.Log, ev, "unavailable", err)
		return err
	}
	if err := n.Withdraw(ctx, ws); err != nil {
		track.Fail(ctx, s.Log, ev, "internal_error", err)
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE data_network_members SET withdrawn_at = now(), withdraw_due_at = NULL, updated_at = now()
		WHERE workspace_id = $1 AND status = 'left'`, ws); err != nil {
		return err
	}
	track.Emit(ctx, ev)
	return nil
}
