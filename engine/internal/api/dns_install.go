package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// One click for the other records Outflare asks a workspace to put in its
// DNS: a site's proof of ownership (SEO) and a tracking domain's CNAME.
// The workspace's own DNS host connection (Cloudflare) adds them in a job.
// Sending domains have their own (emails.JobDomainDNS), and brand setup its
// install step.

const (
	jobDNSAdd = "dns.add"
	// jobTrackingCheck looks for a tracking domain's CNAME after it was
	// added, a few times over the next hour, so nobody has to press Check.
	jobTrackingCheck = "tracking.domain_check"
)

// dnsSubjects maps what a record is for to the table holding its last run.
var dnsSubjects = map[string]string{"seo_site": "seo_sites", "tracking_domain": "tracking_domains"}

// trackingChecks are the waits between looks for a new tracking domain.
var trackingChecks = []time.Duration{20 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

type dnsAddJob struct {
	Subject       string `json:"subject"`
	ID            string `json:"id"`
	BrandID       string `json:"brand_id,omitempty"`
	IntegrationID string `json:"integration_id"`
	ActorID       string `json:"actor_id,omitempty"`
	ActorType     string `json:"actor_type,omitempty"`
}

// dnsConnections gives, for a brand, the workspace's connections that can
// add DNS records for it.
func (s *Server) dnsConnections(ctx context.Context, ws string) (func(brand string) []string, error) {
	var conns []connectors.Integration
	if s.integrations != nil {
		var err error
		if conns, err = s.integrations.List(ctx, s.pool, ws, ""); err != nil {
			return nil, err
		}
	}
	return func(brand string) []string {
		out := []string{}
		for _, c := range conns {
			if d, ok := s.integrations.Registry.Definition(c.Connector); ok && d.Has(connectors.CapDNSWrite) && c.Serves(brand, conns) {
				out = append(out, c.ID)
			}
		}
		return out
	}, nil
}

// queueDNSAdd answers a request to add a subject's records with the
// connection in the body.
func (s *Server) queueDNSAdd(w http.ResponseWriter, r *http.Request, a *access, subject, id, brand string) {
	var req struct {
		IntegrationID string `json:"integration_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if s.allocError(w, r, s.setupConnection(ctx, a.WorkspaceID, brand, req.IntegrationID, connectors.CapDNSWrite)) {
		return
	}
	run, _ := json.Marshal(connectors.DNSInstall{Status: "queued", IntegrationID: req.IntegrationID, At: time.Now().UTC()})
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE `+dnsSubjects[subject]+` SET dns_install = $3 WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, run); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobDNSAdd, WorkspaceID: &a.WorkspaceID, MaxAttempts: 4,
			Payload: dnsAddJob{Subject: subject, ID: id, BrandID: brand, IntegrationID: req.IntegrationID, ActorID: a.ActorID, ActorType: a.ActorType}}); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, subject+".dns_requested", subject, id, map[string]any{"integration_id": req.IntegrationID})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) dnsAddJob(ctx context.Context, job jobs.Job) error {
	var p dnsAddJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || dnsSubjects[p.Subject] == "" || !validUUID(p.ID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	run := connectors.DNSInstall{Status: "done", IntegrationID: p.IntegrationID, At: time.Now().UTC()}
	res, err := s.addSubjectRecords(ctx, ws, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // removed meanwhile
	}
	if err != nil {
		kind := connectors.KindOf(err)
		var bad errInvalid
		if errors.As(err, &bad) {
			kind = connectors.ErrInvalid
		} else if (kind == connectors.ErrUnavailable || kind == connectors.ErrQuota) && job.Attempt < job.MaxAttempts {
			return err
		}
		run.Status, run.Error = "failed", err.Error()
		track.Fail(ctx, s.log, track.Event{Name: p.Subject + ".dns_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"kind": kind}}, "dns_"+kind, err)
	}
	run.Added, run.Existing, run.Zone = res.Added, res.Existing, res.Zone
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(run)
		if _, err := tx.Exec(ctx, `UPDATE `+dnsSubjects[p.Subject]+` SET dns_install = $3 WHERE workspace_id = $1 AND id = $2`, ws, p.ID, b); err != nil {
			return err
		}
		action := p.Subject + ".dns_added"
		if run.Status == "failed" {
			action = p.Subject + ".dns_failed"
		} else if err := s.afterDNSAdded(ctx, tx, ws, p); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorID: p.ActorID, ActorType: firstNonEmpty(p.ActorType, "system"), BrandID: p.BrandID,
			Action: action, TargetType: p.Subject, TargetID: p.ID, Data: map[string]any{"integration_id": p.IntegrationID, "added": run.Added, "existing": run.Existing}})
	})
}

// addSubjectRecords adds what the subject needs: a site's TXT proof, or a
// tracking domain's CNAME to this server.
func (s *Server) addSubjectRecords(ctx context.Context, ws string, p dnsAddJob) (connectors.DNSResult, error) {
	var want connectors.DNSRecord
	switch p.Subject {
	case "seo_site":
		var startURL, host, token string
		if err := s.pool.QueryRow(ctx, `SELECT start_url, host, verify_token FROM seo_sites WHERE workspace_id = $1 AND id = $2`, ws, p.ID).Scan(&startURL, &host, &token); err != nil {
			return connectors.DNSResult{}, err
		}
		pr := crawl.ProofsFor(startURL, host, token)
		want = connectors.DNSRecord{Type: "TXT", Name: pr.DNSName, Content: pr.DNSValue}
	case "tracking_domain":
		var domain string
		if err := s.pool.QueryRow(ctx, `SELECT domain FROM tracking_domains WHERE workspace_id = $1 AND id = $2`, ws, p.ID).Scan(&domain); err != nil {
			return connectors.DNSResult{}, err
		}
		want = connectors.DNSRecord{Type: "CNAME", Name: domain, Content: tracking.Target(s.publicURL)}
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return connectors.DNSResult{}, errInvalid("the DNS connection is gone; connect it again")
	}
	if err != nil {
		return connectors.DNSResult{}, err
	}
	host, ok := o.Connector.(connectors.DNSHost)
	if !ok {
		return connectors.DNSResult{}, errInvalid(o.Connector.Definition().Name + " can't add DNS records")
	}
	return host.EnsureRecords(ctx, o.Client, o.Creds, []connectors.DNSRecord{want})
}

// afterDNSAdded starts looking for the records, so they tick off on their
// own: the site's proof is looked for every minute (the brand setup
// watcher), a tracking domain a few times over the next hour.
func (s *Server) afterDNSAdded(ctx context.Context, tx pgx.Tx, ws string, p dnsAddJob) error {
	switch p.Subject {
	case "seo_site":
		_, err := tx.Exec(ctx, `UPDATE seo_sites SET verify_watch_until = now() + $2::interval, verify_checked_at = NULL WHERE id = $1 AND verified_at IS NULL`,
			p.ID, fmt.Sprintf("%d seconds", int(verifyWatch.Seconds())))
		return err
	case "tracking_domain":
		return enqueueTrackingCheck(ctx, tx, ws, p.ID, 0)
	}
	return nil
}

type trackingCheckJob struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

func enqueueTrackingCheck(ctx context.Context, tx pgx.Tx, ws, id string, n int) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobTrackingCheck, WorkspaceID: &ws, MaxAttempts: 2, RunAt: time.Now().Add(trackingChecks[n]),
		Payload: trackingCheckJob{ID: id, N: n}})
	return err
}

func (s *Server) trackingCheckJob(ctx context.Context, job jobs.Job) error {
	var p trackingCheckJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	d, err := s.checkTracking(ctx, *job.WorkspaceID, p.ID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && d.Status == "verified" || p.N+1 >= len(trackingChecks) {
		return nil
	}
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return enqueueTrackingCheck(ctx, tx, *job.WorkspaceID, p.ID, p.N+1) })
}
