package vendorsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

var jsonUnmarshal = json.Unmarshal

// maxRunning is how many app passwords may be in the works at once at the
// supplier (Cheap Inboxes runs three per organization).
const maxRunning = 3

// appPasswordTimeout is how long a request may stay pending before it is
// asked for again.
const appPasswordTimeout = 30 * time.Minute

// appPasswordGrace is how long a just-claimed request may read as not
// started (the round that claimed it is still asking) before it is asked
// for again. Without it, two rounds running at once could ask twice.
const appPasswordGrace = time.Minute

func (s *Service) queueAppPasswords(ctx context.Context, q jobs.Querier, ws, id string, at time.Time) error {
	j := jobs.NewJob{Kind: JobAppPasswords, WorkspaceID: &ws, Payload: idPayload{id}, MaxAttempts: 5}
	if !at.IsZero() {
		j.RunAt = at
		j.IdempotencyKey = fmt.Sprintf("%s:%d", id, at.UnixMilli()/s.appPasswordPoll().Milliseconds())
	}
	_, err := jobs.Enqueue(ctx, q, j)
	return err
}

// appPasswordsJob paces Google app passwords: it collects finished ones
// (and imports those mailboxes), then asks for new ones within the
// supplier's hourly allowance, and comes back while any are waiting.
func (s *Service) appPasswordsJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[idPayload](job)
	if err != nil {
		return err
	}
	o, v, err := s.open(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	apv, ok := v.(connectors.AppPasswordVendor)
	if !ok {
		_, err := s.Pool.Exec(ctx, `UPDATE vendor_mailboxes SET import_state = 'failed',
			import_error = 'This supplier cannot create Google app passwords. Turn off app passwords for it, or connect these mailboxes with Google OAuth.'
			WHERE integration_id = $1 AND import_state = 'app_password'`, p.IntegrationID)
		return err
	}
	st, err := s.GetSettings(ctx, s.Pool, ws, p.IntegrationID)
	if err != nil {
		return err
	}
	def := o.Connector.Definition()

	// 1. Collect the ones already asked for.
	rows, err := s.Pool.Query(ctx, `
		SELECT external_id, email, first_name, last_name, provider, status, daily_limit, app_password_requested_at
		FROM vendor_mailboxes WHERE integration_id = $1 AND import_state = 'app_password' AND app_password_requested_at IS NOT NULL
		ORDER BY app_password_requested_at`, p.IntegrationID)
	if err != nil {
		return err
	}
	type asked struct {
		pending
		at time.Time
	}
	inFlight, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (asked, error) {
		var a asked
		err := r.Scan(&a.ExternalID, &a.Email, &a.FirstName, &a.LastName, &a.Provider, &a.Status, &a.DailyLimit, &a.at)
		return a, err
	})
	if err != nil {
		return err
	}
	running := 0
	for _, a := range inFlight {
		state, password, reason, err := apv.AppPasswordStatus(ctx, o.Client, o.Creds, a.ExternalID)
		if err != nil {
			if connectors.KindOf(err) == connectors.ErrAuth {
				return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
			}
			running++
			continue
		}
		switch state {
		case connectors.AppPasswordReady:
			creds, err := v.MailboxCredentials(ctx, o.Client, o.Creds, a.ExternalID)
			if err != nil {
				running++
				continue
			}
			if password == "" {
				password = creds.AppPassword
			}
			if err := s.importOne(ctx, ws, p.IntegrationID, def, a.pending, creds, password, st); err != nil {
				return err
			}
		case connectors.AppPasswordFailed:
			if err := s.setState(ctx, p.IntegrationID, a.ExternalID, StateFailed,
				"The supplier could not create a Google app password: "+orDefault(reason, "no reason given")); err != nil {
				return err
			}
		case connectors.AppPasswordNone:
			// Another round may have just claimed it and not yet asked;
			// give the ask time to land before treating it as lost.
			if time.Since(a.at) < appPasswordGrace {
				running++
				continue
			}
			// Lost or never started: ask again.
			if _, err := s.Pool.Exec(ctx, `UPDATE vendor_mailboxes SET app_password_requested_at = NULL
				WHERE integration_id = $1 AND external_id = $2`, p.IntegrationID, a.ExternalID); err != nil {
				return err
			}
		default:
			if time.Since(a.at) > appPasswordTimeout {
				if _, err := s.Pool.Exec(ctx, `UPDATE vendor_mailboxes SET app_password_requested_at = NULL
					WHERE integration_id = $1 AND external_id = $2`, p.IntegrationID, a.ExternalID); err != nil {
					return err
				}
				continue
			}
			running++
		}
	}

	// 2. Ask for more, within the hourly allowance.
	var lastHour int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM vendor_mailboxes
		WHERE integration_id = $1 AND app_password_requested_at > now() - interval '1 hour'`, p.IntegrationID).Scan(&lastHour); err != nil {
		return err
	}
	room := min(apv.AppPasswordRate()-lastHour, maxRunning-running)
	if room > 0 {
		// Claim before calling, so two rounds running at once never ask
		// twice for the same mailbox.
		rows, err := s.Pool.Query(ctx, `
			UPDATE vendor_mailboxes SET app_password_requested_at = now(), updated_at = now()
			WHERE integration_id = $1 AND external_id IN (
				SELECT external_id FROM vendor_mailboxes
				WHERE integration_id = $1 AND import_state = 'app_password' AND app_password_requested_at IS NULL
				ORDER BY email LIMIT $2 FOR UPDATE SKIP LOCKED)
			  AND app_password_requested_at IS NULL
			RETURNING external_id`, p.IntegrationID, room)
		if err != nil {
			return err
		}
		next, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		release := func(ids []string) error {
			_, err := s.Pool.Exec(ctx, `UPDATE vendor_mailboxes SET app_password_requested_at = NULL
				WHERE integration_id = $1 AND external_id = ANY($2)`, p.IntegrationID, ids)
			return err
		}
		for i, ext := range next {
			err := apv.RequestAppPassword(ctx, o.Client, o.Creds, ext)
			switch {
			case err == nil:
			case connectors.KindOf(err) == connectors.ErrQuota:
				// Over the supplier's limit: the next round tries again.
				return release(next[i:])
			case connectors.KindOf(err) == connectors.ErrInvalid:
				if err := s.setState(ctx, p.IntegrationID, ext, StateFailed, err.Error()); err != nil {
					return err
				}
			case connectors.KindOf(err) == connectors.ErrUnavailable:
				// The supplier is down or slow for a moment. Retrying this
				// round is pointless: the next one, a poll away, asks again.
				if rerr := release(next[i:]); rerr != nil {
					return rerr
				}
				track.Fail(ctx, s.log(), track.Event{Name: "vendor.app_password_request_failed", WorkspaceID: ws,
					ActorType: "system", Surface: "job", Props: map[string]any{"integration_id": p.IntegrationID}},
					"connector_unavailable", err)
				return nil
			default:
				if rerr := release(next[i:]); rerr != nil {
					return rerr
				}
				return err
			}
		}
	}

	// Run comes back every AppPasswordPoll while any are waiting.
	return nil
}
