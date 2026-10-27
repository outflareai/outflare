package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// inboxPass is how many inbox rows one workspace's turn stores in one
	// transaction, so a busy site never holds up the others for long.
	inboxPass = 50
	// inboxAttempts is how many times a row is tried before it is set aside.
	inboxAttempts = 5
)

// wakeSiteInbox tells this process's RunSiteInbox there is work, so events
// are stored at once when the collector and the workers share a server. On
// a split install the workers find them on their next poll instead.
func (s *Server) wakeSiteInbox() {
	select {
	case s.inboxWake <- struct{}{}:
	default:
	}
}

// RunSiteInbox stores the browser events waiting in site_event_inbox until
// ctx ends: straight away while there are any, then on a wake from this
// process's collector or every poll.
func (s *Server) RunSiteInbox(ctx context.Context, poll time.Duration) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.inboxWake:
		}
		n, err := s.DrainSiteInbox(ctx)
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "site_inbox.failed", ActorType: "system", Surface: "job"}, "internal", err)
		}
		wait := poll
		if n > 0 && err == nil {
			wait = 0
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(wait)
	}
}

// DrainSiteInbox gives every workspace with waiting events one turn and
// reports how many inbox rows it finished (stored or set aside).
func (s *Server) DrainSiteInbox(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT workspace_id::text FROM site_event_inbox WHERE failed_at IS NULL LIMIT 500`)
	if err != nil {
		return 0, err
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	done := 0
	for _, ws := range workspaces {
		n, err := s.drainWorkspaceInbox(ctx, ws)
		done += n
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// drainWorkspaceInbox stores up to inboxPass of one workspace's waiting
// rows, in the order they arrived. One worker at a time holds a workspace,
// so a visitor's events join their session in order; another worker skips
// it. Each row is stored in its own savepoint and deleted with it, so a
// row is stored exactly once. A row that fails is rolled back alone and
// tried again on a later turn, up to inboxAttempts times.
func (s *Server) drainWorkspaceInbox(ctx context.Context, ws string) (int, error) {
	done := 0
	var failed []inboxFailure
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		done, failed = 0, nil
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('site_event_inbox'), hashtext($1))`, ws).Scan(&mine); err != nil || !mine {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT i.id, i.key_id::text, k.brand_id::text, k.origins, i.user_agent, i.batch, i.received_at, i.attempts
			FROM site_event_inbox i JOIN site_keys k ON k.id = i.key_id
			WHERE i.workspace_id = $1 AND i.failed_at IS NULL ORDER BY i.id LIMIT $2`, ws, inboxPass)
		if err != nil {
			return err
		}
		type row struct {
			id       int64
			key      siteKeyRow
			ua       string
			batch    json.RawMessage
			at       time.Time
			attempts int
		}
		var todo []row
		for rows.Next() {
			var r row
			r.key.workspaceID = ws
			if err := rows.Scan(&r.id, &r.key.id, &r.key.brandID, &r.key.origins, &r.ua, &r.batch, &r.at, &r.attempts); err != nil {
				rows.Close()
				return err
			}
			todo = append(todo, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range todo {
			start := time.Now()
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			var batch []json.RawMessage
			var stored, rejected int
			err = json.Unmarshal(r.batch, &batch)
			if err == nil {
				stored, rejected, _, err = s.storeSiteBatch(ctx, sp, r.key, r.ua, batch, r.at.UTC())
			}
			if err == nil {
				_, err = sp.Exec(ctx, `DELETE FROM site_event_inbox WHERE id = $1`, r.id)
			}
			if err == nil {
				err = sp.Commit(ctx)
			}
			if err != nil {
				if rb := sp.Rollback(ctx); rb != nil && !errors.Is(rb, pgx.ErrTxClosed) {
					return rb
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				f := inboxFailure{id: r.id, events: len(batch), attempts: r.attempts + 1, err: err}
				if _, err := tx.Exec(ctx, `UPDATE site_event_inbox SET attempts = $2::int, last_error = $3,
					failed_at = CASE WHEN $2::int >= $4::int THEN now() END WHERE id = $1`,
					r.id, f.attempts, cutUTF8(err.Error(), 2000), inboxAttempts); err != nil {
					return err
				}
				failed = append(failed, f)
				metrics.InboxStored(0, 0, len(batch), time.Since(start))
				continue
			}
			done++
			metrics.InboxStored(stored, rejected, 0, time.Since(start))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, f := range failed {
		setAside := f.attempts >= inboxAttempts
		if setAside {
			done++
		}
		track.Fail(ctx, s.log, track.Event{Name: "site_events.store_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"events": f.events, "attempts": f.attempts, "set_aside": setAside}}, "internal", f.err)
	}
	return done, nil
}

type inboxFailure struct {
	id       int64
	events   int
	attempts int
	err      error
}
