package emails

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A sending domain is registered with the provider by a job (calling a
// provider never happens in a request), which hands back the DNS records
// to add. The same job then checks the records in public DNS and asks the
// provider whether it has verified the domain, again and again with longer
// gaps, for up to three days.

// checkFor is how long a pending domain keeps being checked on its own.
const checkFor = 72 * time.Hour

// ErrDomainTaken means the domain is already added to that provider.
var ErrDomainTaken = errors.New("domain already added")

// AddDomain records a sending domain for a provider and queues its
// registration. It returns the domain's id.
func (s *Service) AddDomain(ctx context.Context, tx pgx.Tx, who Who, ws, providerID, domain string, brandID *string) (string, error) {
	id := newID()
	tag, err := tx.Exec(ctx, `
		INSERT INTO email_domains (id, workspace_id, brand_id, provider_id, domain)
		SELECT $1, $2, $3, $4, $5 FROM email_providers WHERE id = $4 AND workspace_id = $2
		ON CONFLICT (provider_id, domain) DO NOTHING`, id, ws, brandID, providerID, domain)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_providers WHERE id = $1 AND workspace_id = $2)`, providerID, ws).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return "", pgx.ErrNoRows
		}
		return "", ErrDomainTaken
	}
	if err := s.enqueueCheck(ctx, tx, ws, id, time.Time{}, "add"); err != nil {
		return "", err
	}
	return id, s.record(ctx, tx, who, ws, "email_domain.added", "email_domain", id, map[string]any{"provider_id": providerID})
}

// CheckDomain queues a check now, for the "Check now" button, and starts
// the every-minute checks again: people press it when they have just
// added the records.
func (s *Service) CheckDomain(ctx context.Context, tx pgx.Tx, ws, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE email_domains SET checks_from = now() WHERE workspace_id = $1 AND id = $2`, ws, id); err != nil {
		return err
	}
	return s.enqueueCheck(ctx, tx, ws, id, time.Time{}, "now:"+time.Now().UTC().Format("20060102T150405.000"))
}

func (s *Service) enqueueCheck(ctx context.Context, q jobs.Querier, ws, id string, at time.Time, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobDomainCheck, WorkspaceID: &ws, Payload: map[string]string{"domain_id": id},
		RunAt: at, IdempotencyKey: "email_domain:" + id + ":" + key, MaxAttempts: 3})
	return err
}

// nextCheck is when to look again at a domain added (or last nudged with
// Check now or a one-click install) at created: every minute at first
// (people add records while they watch), then less often.
func nextCheck(created, now time.Time) (time.Time, bool) {
	age := now.Sub(created)
	if age >= checkFor {
		return time.Time{}, false
	}
	var gap time.Duration
	switch {
	case age < 15*time.Minute:
		gap = time.Minute
	case age < 2*time.Hour:
		gap = 10 * time.Minute
	case age < 24*time.Hour:
		gap = time.Hour
	default:
		gap = 6 * time.Hour
	}
	return now.Add(gap), true
}

func (s *Service) domainJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		DomainID string `json:"domain_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	err := s.RunDomainCheck(ctx, in.DomainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // removed meanwhile
	}
	return err
}

type domainRow struct {
	ID, WorkspaceID, ProviderID, Domain, Status string
	Ref                                         *string
	Records                                     []connectors.DomainRecord
	// Created is when the checks started: when the domain was added, or
	// last nudged.
	Created time.Time
}

// RunDomainCheck registers the domain with its provider if that has not
// happened yet, checks its DNS records and the provider's verdict, saves
// both, and schedules the next check while it is pending.
func (s *Service) RunDomainCheck(ctx context.Context, id string) error {
	var d domainRow
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT id, workspace_id, provider_id, domain, status, provider_ref, records, GREATEST(created_at, checks_from) FROM email_domains WHERE id = $1`, id).
		Scan(&d.ID, &d.WorkspaceID, &d.ProviderID, &d.Domain, &d.Status, &d.Ref, &raw, &d.Created)
	if err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &d.Records)
	p, err := s.loadProvider(ctx, s.Pool, d.WorkspaceID, d.ProviderID)
	if err != nil {
		return err
	}
	op, sender, err := s.open(ctx, p)
	var setup connectors.DomainSetup
	if err == nil {
		if d.Ref == nil && len(d.Records) == 0 {
			setup, err = sender.AddDomain(ctx, op.Client, op.Creds, d.Domain)
		} else {
			setup, err = sender.CheckDomain(ctx, op.Client, op.Creds, d.Domain, deref(d.Ref))
		}
	}
	if err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "email_domain.check_failed", WorkspaceID: d.WorkspaceID, Surface: "job",
			Props: map[string]any{"connector": p.Connector, "kind": connectors.KindOf(err)}}, "provider_"+connectors.KindOf(err), err)
		return s.saveDomainError(ctx, d, err)
	}
	if setup.Ref == "" {
		setup.Ref = deref(d.Ref)
	}
	records := withDMARC(d.Domain, setup.Records)
	s.lookup(ctx, d.Domain, records)

	status, reason := "pending", setup.Reason
	if setup.Verified {
		status, reason = "verified", ""
	} else if reason == "" {
		reason = pendingReason(records)
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(records)
		if _, err := tx.Exec(ctx, `
			UPDATE email_domains SET status = $2, status_reason = NULLIF($3, ''), records = $4, provider_ref = NULLIF($5, ''), checked_at = now(),
				verified_at = CASE WHEN $2 = 'verified' THEN COALESCE(verified_at, now()) ELSE NULL END, updated_at = now()
			WHERE id = $1`, d.ID, status, reason, b, setup.Ref); err != nil {
			return err
		}
		if status == "verified" && d.Status != "verified" {
			return s.record(ctx, tx, System, d.WorkspaceID, "email_domain.verified", "email_domain", d.ID, map[string]any{"provider_id": p.ID, "connector": p.Connector})
		}
		if status == "verified" {
			return nil
		}
		next, ok := nextCheck(d.Created, time.Now())
		if !ok {
			// Three days is long enough: stop checking on our own. "Check
			// again" still works.
			if d.Status != "failed" {
				if _, err := tx.Exec(ctx, `UPDATE email_domains SET status = 'failed', status_reason = $2 WHERE id = $1`, d.ID,
					"Not verified after three days. Check the records below, then press Check again."); err != nil {
					return err
				}
				return s.record(ctx, tx, System, d.WorkspaceID, "email_domain.verification_failed", "email_domain", d.ID, map[string]any{"connector": p.Connector})
			}
			return nil
		}
		return s.enqueueCheck(ctx, tx, d.WorkspaceID, d.ID, next, next.UTC().Format("20060102T1504"))
	})
}

// saveDomainError keeps the provider's answer on the domain and keeps to
// the schedule: a refusal (a wrong key, say) can be fixed meanwhile.
func (s *Service) saveDomainError(ctx context.Context, d domainRow, cause error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE email_domains SET status_reason = $2, checked_at = now(), updated_at = now() WHERE id = $1`, d.ID, cause.Error()); err != nil {
			return err
		}
		if next, ok := nextCheck(d.Created, time.Now()); ok && d.Status != "verified" {
			return s.enqueueCheck(ctx, tx, d.WorkspaceID, d.ID, next, next.UTC().Format("20060102T1504"))
		}
		return nil
	})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// withDMARC adds a recommended DMARC record when the provider did not ask
// for one. Gmail and Yahoo expect one from anyone sending in volume.
func withDMARC(domain string, rs []connectors.DomainRecord) []connectors.DomainRecord {
	for _, r := range rs {
		if r.Purpose == "dmarc" {
			return rs
		}
	}
	return append(rs, connectors.DomainRecord{Purpose: "dmarc", Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=none;", Required: false})
}

// lookup checks each record against public DNS and sets its status, and
// for a record that isn't right, says in words what is wrong when it can
// tell.
func (s *Service) lookup(ctx context.Context, domain string, rs []connectors.DomainRecord) {
	res := s.resolver()
	for i := range rs {
		r := &rs[i]
		lctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		r.Status, r.Found = checkRecord(lctx, res, *r)
		r.Hint = ""
		if r.Status != "verified" {
			r.Hint = diagnose(lctx, res, domain, *r)
		}
		cancel()
	}
}

// hostResolver looks up a name's addresses, to tell a proxied CNAME.
type hostResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// diagnose explains the usual mistakes with a record that isn't in place:
// the domain typed twice in the name, a CNAME that Cloudflare proxies, or
// a second SPF record.
func diagnose(ctx context.Context, res Resolver, domain string, r connectors.DomainRecord) string {
	name := strings.TrimSuffix(strings.ToLower(r.Name), ".")
	domain = strings.ToLower(domain)
	if doubled := name + "." + domain; r.Status == "missing" && name != domain {
		if st, _ := checkRecord(ctx, res, connectors.DomainRecord{Purpose: r.Purpose, Type: r.Type, Name: doubled, Value: r.Value}); st == "verified" || st == "mismatch" {
			return "It's at " + doubled + ": your DNS host added " + domain + " to the name again. Change the name to " +
				strings.TrimSuffix(strings.TrimSuffix(name, domain), ".") + " only."
		}
	}
	switch strings.ToUpper(r.Type) {
	case "CNAME":
		if h, ok := res.(hostResolver); ok && r.Status == "missing" {
			if addrs, err := h.LookupHost(ctx, name); err == nil && len(addrs) > 0 {
				return "This name answers with an IP address instead of the CNAME, which usually means the record is proxied. In Cloudflare, set it to DNS only (grey cloud)."
			}
		}
	case "TXT":
		if r.Purpose == "spf" && r.Status == "mismatch" {
			spf := 0
			for _, v := range strings.Split(r.Found, " | ") {
				if strings.HasPrefix(normTXT(v), "v=spf1") {
					spf++
				}
			}
			if spf > 1 {
				return "There are " + fmt.Sprint(spf) + " SPF records on this name, and a name may hold only one. Merge them into one record with " + spfInclude(r.Value) + " in it."
			}
			if spf == 1 {
				return "There is an SPF record here without " + spfInclude(r.Value) + ". Add it to that record rather than adding a second one."
			}
		}
	case "MX":
		if r.Status == "mismatch" {
			return "This name has other MX records. The MAIL FROM subdomain needs only the provider's one, so remove the others or pick another subdomain."
		}
	}
	return ""
}

func checkRecord(ctx context.Context, res Resolver, r connectors.DomainRecord) (string, string) {
	name := strings.TrimSuffix(r.Name, ".")
	switch strings.ToUpper(r.Type) {
	case "CNAME":
		got, err := res.LookupCNAME(ctx, name)
		got = strings.TrimSuffix(strings.ToLower(got), ".")
		want := strings.TrimSuffix(strings.ToLower(r.Value), ".")
		switch {
		case err != nil || got == "" || got == strings.ToLower(name):
			return "missing", ""
		case got == want:
			return "verified", ""
		default:
			return "mismatch", got
		}
	case "TXT":
		got, err := res.LookupTXT(ctx, name)
		if err != nil || len(got) == 0 {
			return "missing", ""
		}
		if r.Purpose == "dmarc" {
			// Any DMARC policy will do; the one suggested is only a start.
			// Found keeps the one in place, so it isn't mistaken for ours.
			for _, v := range got {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "v=dmarc1") {
					return "verified", strings.TrimSpace(v)
				}
			}
			return "missing", ""
		}
		want := normTXT(r.Value)
		for _, v := range got {
			if normTXT(v) == want {
				return "verified", ""
			}
		}
		if r.Purpose == "spf" {
			// An SPF record that includes the provider counts, even with
			// other senders in it.
			inc := spfInclude(r.Value)
			for _, v := range got {
				if inc != "" && strings.HasPrefix(normTXT(v), "v=spf1") && strings.Contains(normTXT(v), inc) {
					return "verified", ""
				}
			}
		}
		return "mismatch", strings.Join(got, " | ")
	case "MX":
		got, err := res.LookupMX(ctx, name)
		if err != nil || len(got) == 0 {
			return "missing", ""
		}
		want := strings.TrimSuffix(strings.ToLower(r.Value), ".")
		hosts := []string{}
		for _, mx := range got {
			h := strings.TrimSuffix(strings.ToLower(mx.Host), ".")
			if h == want {
				return "verified", ""
			}
			hosts = append(hosts, h)
		}
		return "mismatch", strings.Join(hosts, ", ")
	}
	return "pending", ""
}

func normTXT(v string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.Trim(v, `"`))), " ")
}

func spfInclude(v string) string {
	for _, f := range strings.Fields(strings.ToLower(v)) {
		if strings.HasPrefix(f, "include:") {
			return f
		}
	}
	return ""
}

// pendingReason says what is still missing, in words.
func pendingReason(rs []connectors.DomainRecord) string {
	missing := 0
	for _, r := range rs {
		if r.Required && r.Status != "verified" {
			missing++
		}
	}
	switch missing {
	case 0:
		return "The records are in place. Waiting for the provider to confirm, which can take a few minutes."
	case 1:
		return "1 required DNS record is not in place yet."
	default:
		return fmt.Sprintf("%d required DNS records are not in place yet.", missing)
	}
}

var _ Resolver = net.DefaultResolver
