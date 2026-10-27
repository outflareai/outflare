package vendorsync

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Run schedules the periodic work until ctx ends: a full sync of each
// supplier account every SyncEvery, and a round of app passwords every
// AppPasswordPoll while any are waiting. Jobs carry a key per time slot,
// so several engines running Run schedule each round once.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.tick(ctx); err != nil && ctx.Err() == nil {
			s.log().Warn("vendor sync schedule", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) tick(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT integration_id, workspace_id FROM vendor_syncs
			WHERE COALESCE(last_attempt_at, 'epoch') < now() - make_interval(secs => $1)`, s.syncEvery().Seconds())
		if err != nil {
			return err
		}
		due, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, WS string }])
		if err != nil {
			return err
		}
		now := time.Now()
		for _, d := range due {
			if err := s.queueSync(ctx, tx, d.WS, d.ID, now); err != nil {
				return err
			}
		}
		rows, err = tx.Query(ctx, `
			SELECT DISTINCT integration_id, workspace_id FROM vendor_mailboxes WHERE import_state = 'app_password'`)
		if err != nil {
			return err
		}
		waiting, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, WS string }])
		if err != nil {
			return err
		}
		for _, d := range waiting {
			if err := s.queueAppPasswords(ctx, tx, d.WS, d.ID, now); err != nil {
				return err
			}
		}
		return nil
	})
}
