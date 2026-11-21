package api

// Tracking domain registration (plans/ad-tracker.md, B8). Every link a
// tracking domain carries stops working when the domain's registration
// runs out, and someone else can then register it. A worker reads each
// verified tracking domain's registration record (public RDAP: the
// registries publish it, with no account or key) once a week, daily once
// it is within 45 days of running out, and raises the
// tracking_domain.expiring alert 30 days before, 7 days before and when
// it has run out. Renewing is the domain's owner's to do, at their
// registrar.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/rdap"
	"github.com/outflareai/outflare/engine/internal/track"
)

// AlertTrackingDomainExpiring is the alert for a tracking domain whose
// registration runs out soon or has run out.
const AlertTrackingDomainExpiring = "tracking_domain.expiring"

const (
	// domainExpiryBatch is how many domains one pass reads.
	domainExpiryBatch = 20
	// domainExpiryEvery is how often a domain is read; domainExpiryNear is
	// how close to running out it is read every domainExpiryRetry instead,
	// which is also when a failed read is tried again.
	domainExpiryEvery = 7 * 24 * time.Hour
	domainExpiryNear  = 45 * 24 * time.Hour
	domainExpiryRetry = 24 * time.Hour
	// domainExpiryOverdue is how long without a read counts as behind.
	domainExpiryOverdue = 14 * 24 * time.Hour
)

func init() {
	alerts.Register(alerts.Kind{ID: AlertTrackingDomainExpiring, Title: "Tracking domain expiring",
		Description: "A tracking domain's registration runs out within 30 days, within 7 days, or has run out: every link on it stops working until it is renewed at its registrar. Once at each. It goes to the owners and admins.",
		Scope:       auth.ScopeMailboxesRead, Describe: describeDomainExpiring})
}

// RunTrackingDomainExpiry reads due tracking domains' registrations every
// interval until ctx ends.
func (s *Server) RunTrackingDomainExpiry(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.CheckTrackingDomainExpiry(ctx, 2*time.Second); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "tracking_domain.expiry_checks_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// domainExpiryThreshold is the warning a registration that runs out in
// left is due: 30 or 7 days before, 0 once it has run out, -1 for none
// yet; with the whole days left, never below 0.
func domainExpiryThreshold(left time.Duration) (threshold, days int) {
	days = max(int(math.Ceil(left.Hours()/24)), 0)
	switch {
	case left <= 0:
		return 0, 0
	case days <= 7:
		return 7, days
	case days <= 30:
		return 30, days
	}
	return -1, days
}

// CheckTrackingDomainExpiry reads the registration of the verified
// tracking domains that are due, up to domainExpiryBatch, waiting pause
// between reads so the public RDAP service isn't hurried. It returns how
// many it read. Only one worker runs it at a time.
func (s *Server) CheckTrackingDomainExpiry(ctx context.Context, pause time.Duration) (int, error) {
	if s.crawler == nil || s.crawler.RDAP == nil {
		return 0, nil
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('tracking_domain_expiry', 0))`).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, nil // another worker has it
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('tracking_domain_expiry', 0))`)

	now := time.Now()
	rows, err := s.pool.Query(ctx, `SELECT id::text, workspace_id::text, brand_id::text, domain FROM tracking_domains
		WHERE status = 'verified' AND (registration_checked_at IS NULL OR registration_checked_at < $1
			OR (registration_expires_at IS NOT NULL AND registration_expires_at < $2 AND registration_checked_at < $3))
		ORDER BY registration_checked_at NULLS FIRST, id LIMIT $4`,
		now.Add(-domainExpiryEvery), now.Add(domainExpiryNear), now.Add(-domainExpiryRetry), domainExpiryBatch)
	if err != nil {
		return 0, err
	}
	type due struct {
		id, ws, domain string
		brand          *string
	}
	var list []due
	var d due
	if _, err := pgx.ForEachRow(rows, []any{&d.id, &d.ws, &d.brand, &d.domain}, func() error {
		list = append(list, d)
		return nil
	}); err != nil {
		return 0, err
	}
	// Tracking domains under one registrable domain share its record.
	type answer struct {
		at  time.Time
		err error
	}
	read := map[string]answer{}
	done := 0
	for _, d := range list {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		reg := rdap.Domain(d.domain)
		a, seen := read[reg]
		if !seen {
			if len(read) > 0 && pause > 0 {
				select {
				case <-ctx.Done():
					return done, ctx.Err()
				case <-time.After(pause):
				}
			}
			a.at, a.err = s.crawler.RDAP.Expires(ctx, d.domain)
			read[reg] = a
		}
		switch {
		case a.err == nil:
			metrics.TrackingDomainExpiryCheck("checked")
			if err := s.saveDomainExpiry(ctx, d.id, d.ws, d.brand, a.at, now); err != nil {
				return done, err
			}
		case errors.Is(a.err, rdap.ErrNotFound):
			// The registry gives no date: nothing to warn about.
			metrics.TrackingDomainExpiryCheck("no_date")
			if _, err := s.pool.Exec(ctx, `UPDATE tracking_domains SET registration_expires_at = NULL, registration_checked_at = $3
				WHERE id = $1 AND workspace_id = $2`, d.id, d.ws, now); err != nil {
				return done, err
			}
		default:
			// Unreachable: what was read before stays, and it is tried
			// again in a day.
			metrics.TrackingDomainExpiryCheck("failed")
			track.Fail(ctx, s.log, track.Event{Name: "tracking_domain.expiry_check_failed", WorkspaceID: d.ws, ActorType: "system", Surface: "system",
				Props: map[string]any{"tracking_domain_id": d.id}}, "unavailable", a.err)
			if _, err := s.pool.Exec(ctx, `UPDATE tracking_domains SET registration_checked_at = $3 WHERE id = $1 AND workspace_id = $2`,
				d.id, d.ws, now.Add(domainExpiryRetry-domainExpiryEvery)); err != nil {
				return done, err
			}
		}
		done++
	}
	var expired, week, month, overdue int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE registration_expires_at <= $1),
			count(*) FILTER (WHERE registration_expires_at > $1 AND registration_expires_at <= $2),
			count(*) FILTER (WHERE registration_expires_at > $2 AND registration_expires_at <= $3),
			count(*) FILTER (WHERE registration_checked_at IS NULL OR registration_checked_at < $4)
		FROM tracking_domains WHERE status = 'verified'`,
		now, now.Add(7*24*time.Hour), now.Add(30*24*time.Hour), now.Add(-domainExpiryOverdue)).Scan(&expired, &week, &month, &overdue); err != nil {
		return done, err
	}
	metrics.TrackingDomainsExpiring(expired, week, month, overdue)
	return done, nil
}

// saveDomainExpiry stores when a domain's registration runs out and, in
// the same transaction, raises the alert its nearness is due. The alert's
// key holds the date and the warning, so each warning is raised once and
// a renewed domain starts again.
func (s *Server) saveDomainExpiry(ctx context.Context, id, ws string, brand *string, at, now time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE tracking_domains SET registration_expires_at = $3, registration_checked_at = $4
			WHERE id = $1 AND workspace_id = $2 AND status = 'verified'`, id, ws, at, now)
		if err != nil || tag.RowsAffected() == 0 {
			return err // removed, or no longer verified, while it was read
		}
		threshold, days := domainExpiryThreshold(at.Sub(now))
		if threshold < 0 {
			return nil
		}
		_, err = alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: brand, Kind: AlertTrackingDomainExpiring,
			DedupeKey:  id + ":" + at.UTC().Format(time.DateOnly) + ":" + strconv.Itoa(threshold),
			TargetType: "tracking_domain", TargetID: id, Data: map[string]any{"days": days, "threshold": threshold}})
		return err
	})
}

// describeDomainExpiring says where the domain stands now, read fresh: it
// may have been renewed since the alert.
func describeDomainExpiring(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var domain string
	var at *time.Time
	if err := q.QueryRow(ctx, `SELECT domain, registration_expires_at FROM tracking_domains WHERE id = $1 AND workspace_id = $2`,
		a.TargetID, a.WorkspaceID).Scan(&domain, &at); err != nil {
		return alerts.View{}, err
	}
	v := alerts.View{Path: "domains"}
	if at == nil {
		v.Title = domain + " may be expiring"
		v.Text = "Its registry no longer says when its registration runs out. Check it at your registrar."
		return v, nil
	}
	reg, day := rdap.Domain(domain), at.UTC().Format("2 January 2006")
	threshold, days := domainExpiryThreshold(time.Until(*at))
	switch {
	case threshold == 0:
		v.Title = domain + " has expired"
		v.Text = fmt.Sprintf("The registration of %s ran out on %s. Renew it at your registrar now: the links on it stop working, and someone else could register it.", reg, day)
	case threshold < 0:
		v.Title = domain + " was renewed"
		v.Text = fmt.Sprintf("The registration of %s now runs until %s.", reg, day)
	default:
		in := fmt.Sprintf("in %d days", days)
		if days == 1 {
			in = "tomorrow"
		}
		v.Title = domain + " expires " + in
		v.Text = fmt.Sprintf("The registration of %s runs out on %s. Renew it at your registrar, or every tracking link on it stops working.", reg, day)
	}
	return v, nil
}
