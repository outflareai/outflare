package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// TrackingCerts gets HTTPS certificates for workspaces' tracking domains,
// for a server whose own proxy can't (an extension can ask a certificate
// provider for one per domain). Without one, the operator's proxy serves HTTPS for
// tracking domains as it does for PUBLIC_URL.
type TrackingCerts interface {
	// Ensure asks for a certificate for domain, or reports on the one
	// already asked for. Calling it again for the same domain is safe.
	Ensure(ctx context.Context, domain string) (CertState, error)
	// Remove stops serving domain and drops its certificate. A domain
	// that is already gone is not an error.
	Remove(ctx context.Context, domain string) error
}

// CertState is where a tracking domain's certificate stands.
type CertState struct {
	Status string // pending, active or failed
	// Error says what to fix when Status is failed, or why it is still
	// pending when the provider knows.
	Error string
}

const (
	jobTrackingHTTPS       = "tracking.domain_https"
	jobTrackingHTTPSRemove = "tracking.domain_https_remove"
)

// trackingHTTPSChecks is how long to wait before each look at a pending
// certificate: quick at first, as most are issued within minutes, then
// for about a day.
var trackingHTTPSChecks = []time.Duration{0, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 8 * time.Hour}

// trackingHost is what tracking domains' CNAME records point at.
func (s *Server) trackingHost() string {
	if s.trackingTarget != "" {
		return s.trackingTarget
	}
	return tracking.Target(s.publicURL)
}

type trackingHTTPSJob struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

// queueTrackingHTTPS marks a verified domain's certificate pending and
// asks for it, when the server has a provider.
func (s *Server) queueTrackingHTTPS(ctx context.Context, tx pgx.Tx, ws, id string) error {
	if s.trackingCerts == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE tracking_domains SET https_status = 'pending', https_error = '' WHERE id = $1 AND workspace_id = $2`, id, ws); err != nil {
		return err
	}
	return enqueueTrackingHTTPS(ctx, tx, ws, id, 0)
}

func enqueueTrackingHTTPS(ctx context.Context, tx pgx.Tx, ws, id string, n int) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobTrackingHTTPS, WorkspaceID: &ws, MaxAttempts: 3, RunAt: time.Now().Add(trackingHTTPSChecks[n]),
		Payload: trackingHTTPSJob{ID: id, N: n}})
	return err
}

func (s *Server) trackingHTTPSJob(ctx context.Context, job jobs.Job) error {
	var p trackingHTTPSJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ID) || p.N < 0 || p.N >= len(trackingHTTPSChecks) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	if s.trackingCerts == nil {
		return nil
	}
	ws := *job.WorkspaceID
	var domain, status string
	var https *string
	err := s.pool.QueryRow(ctx, `SELECT domain, status, https_status FROM tracking_domains WHERE id = $1 AND workspace_id = $2`, p.ID, ws).Scan(&domain, &status, &https)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (status != "verified" || https == nil || *https == "active") {
		return nil // removed, no longer pointing here, or already done
	}
	if err != nil {
		return err
	}
	ev := track.Event{ActorType: "system", Surface: "job", WorkspaceID: ws, Props: map[string]any{"tracking_domain_id": p.ID, "check": p.N}}
	state, err := s.trackingCerts.Ensure(ctx, domain)
	if err != nil {
		if job.Attempt < job.MaxAttempts {
			return err
		}
		ev.Name = "tracking_domain.https_failed"
		track.Fail(ctx, s.log, ev, "unavailable", err)
		state = CertState{Status: "pending", Error: "couldn't reach the certificate service; trying again"}
	}
	last := p.N+1 >= len(trackingHTTPSChecks)
	if state.Status != "active" && state.Status != "failed" {
		state.Status = "pending"
		if last {
			state.Status = "failed"
			if state.Error == "" {
				state.Error = "the certificate wasn't issued within a day; check that the CNAME still points to " + s.trackingHost() + ", then check the domain again"
			}
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE tracking_domains SET https_status = $3, https_error = $4 WHERE id = $1 AND workspace_id = $2`,
			p.ID, ws, state.Status, truncate(state.Error, 500)); err != nil {
			return err
		}
		switch state.Status {
		case "active":
			ev.Name = "tracking_domain.https_ready"
			track.Emit(ctx, ev)
		case "failed":
			ev.Name = "tracking_domain.https_failed"
			track.Fail(ctx, s.log, ev, "verification_failed", errors.New(state.Error))
		default:
			return enqueueTrackingHTTPS(ctx, tx, ws, p.ID, p.N+1)
		}
		return nil
	})
}

type trackingHTTPSRemoveJob struct {
	Domain string `json:"domain"`
}

// queueTrackingHTTPSRemove drops a removed domain's certificate.
func (s *Server) queueTrackingHTTPSRemove(ctx context.Context, tx pgx.Tx, ws, domain string) error {
	if s.trackingCerts == nil {
		return nil
	}
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobTrackingHTTPSRemove, WorkspaceID: &ws, MaxAttempts: 5, Payload: trackingHTTPSRemoveJob{Domain: domain}})
	return err
}

func (s *Server) trackingHTTPSRemoveJob(ctx context.Context, job jobs.Job) error {
	var p trackingHTTPSRemoveJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.Domain == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	if s.trackingCerts == nil {
		return nil
	}
	// Added again since (by any workspace): keep its certificate.
	var again bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tracking_domains WHERE domain = $1)`, p.Domain).Scan(&again); err != nil || again {
		return err
	}
	return s.trackingCerts.Remove(ctx, p.Domain)
}

// StartTrackingHTTPS asks for certificates for verified tracking domains
// that have none yet, such as those verified before the server had a
// provider. Their links use the server's own address until each is
// active.
func (s *Server) StartTrackingHTTPS(ctx context.Context) error {
	if s.trackingCerts == nil {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE tracking_domains SET https_status = 'pending', https_error = ''
			WHERE status = 'verified' AND https_status IS NULL RETURNING id::text, workspace_id::text`)
		if err != nil {
			return err
		}
		type pending struct{ id, ws string }
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
			var p pending
			err := r.Scan(&p.id, &p.ws)
			return p, err
		})
		if err != nil {
			return err
		}
		for _, p := range list {
			if err := enqueueTrackingHTTPS(ctx, tx, p.ws, p.id, 0); err != nil {
				return err
			}
		}
		return nil
	})
}
