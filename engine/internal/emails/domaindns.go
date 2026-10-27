package emails

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobDomainDNS adds a sending domain's records through the workspace's
// own DNS host connection (Cloudflare), the one-click path.
const JobDomainDNS = "email.domain_dns"

// DNSInstall is the last one-click run on a sending domain.
type DNSInstall = connectors.DNSInstall

type dnsJob struct {
	DomainID      string `json:"domain_id"`
	IntegrationID string `json:"integration_id"`
	ActorID       string `json:"actor_id,omitempty"`
	ActorType     string `json:"actor_type,omitempty"`
	UserID        string `json:"user_id,omitempty"`
}

// InstallDNS queues adding a domain's records with a DNS host connection.
// The caller checks the connection can write DNS for the domain's brand.
func (s *Service) InstallDNS(ctx context.Context, tx pgx.Tx, who Who, ws, id, integrationID string) error {
	run, _ := json.Marshal(DNSInstall{Status: "queued", IntegrationID: integrationID, At: time.Now().UTC()})
	tag, err := tx.Exec(ctx, `UPDATE email_domains SET dns_install = $3, checks_from = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, id, run)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobDomainDNS, WorkspaceID: &ws, MaxAttempts: 4,
		Payload: dnsJob{DomainID: id, IntegrationID: integrationID, ActorID: who.ActorID, ActorType: who.ActorType, UserID: who.UserID}}); err != nil {
		return err
	}
	return s.record(ctx, tx, who, ws, "email_domain.dns_requested", "email_domain", id, map[string]any{"integration_id": integrationID})
}

func (s *Service) dnsJob(ctx context.Context, job jobs.Job) error {
	var p dnsJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	run := DNSInstall{Status: "done", IntegrationID: p.IntegrationID, At: time.Now().UTC()}
	n, err := s.addRecords(ctx, ws, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the domain was removed meanwhile
	}
	if err != nil {
		kind := connectors.KindOf(err)
		if (kind == connectors.ErrUnavailable || kind == connectors.ErrQuota) && job.Attempt < job.MaxAttempts {
			return err
		}
		run.Status, run.Error = "failed", err.Error()
		track.Fail(ctx, s.Log, track.Event{Name: "email_domain.dns_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"kind": kind}}, "dns_"+kind, err)
	}
	run.Added, run.Existing, run.Zone = n.Added, n.Existing, n.Zone
	who := Who{ActorID: p.ActorID, ActorType: p.ActorType, UserID: p.UserID}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(run)
		if _, err := tx.Exec(ctx, `UPDATE email_domains SET dns_install = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, p.DomainID, b); err != nil {
			return err
		}
		action := "email_domain.dns_added"
		if run.Status == "failed" {
			action = "email_domain.dns_failed"
		} else if err := s.enqueueCheck(ctx, tx, ws, p.DomainID, time.Time{}, "dns:"+run.At.Format("20060102T150405.000")); err != nil {
			return err
		}
		return s.record(ctx, tx, who, ws, action, "email_domain", p.DomainID, map[string]any{
			"integration_id": p.IntegrationID, "added": run.Added, "existing": run.Existing})
	})
}

// addRecords adds the records that aren't in place yet. A DMARC policy
// already in place is kept: a name holds one, and any will do.
func (s *Service) addRecords(ctx context.Context, ws string, p dnsJob) (connectors.DNSResult, error) {
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT records FROM email_domains WHERE workspace_id = $1 AND id = $2`, ws, p.DomainID).Scan(&raw); err != nil {
		return connectors.DNSResult{}, err
	}
	var recs []connectors.DomainRecord
	_ = json.Unmarshal(raw, &recs)
	if len(recs) == 0 {
		return connectors.DNSResult{}, connectors.Errf(connectors.ErrUnavailable, "the provider hasn't sent the domain's records yet")
	}
	want := []connectors.DNSRecord{}
	for _, r := range recs {
		if r.Status == "verified" {
			continue
		}
		want = append(want, connectors.DNSRecord{Type: strings.ToUpper(r.Type), Name: strings.TrimSuffix(r.Name, "."), Content: r.Value, Priority: r.Priority})
	}
	if len(want) == 0 {
		return connectors.DNSResult{}, nil
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return connectors.DNSResult{}, connectors.Errf(connectors.ErrInvalid, "the DNS connection is gone; connect it again")
	}
	if err != nil {
		return connectors.DNSResult{}, err
	}
	host, ok := o.Connector.(connectors.DNSHost)
	if !ok {
		return connectors.DNSResult{}, connectors.Errf(connectors.ErrInvalid, "%s can't add DNS records", o.Connector.Definition().Name)
	}
	return host.EnsureRecords(ctx, o.Client, o.Creds, want)
}
