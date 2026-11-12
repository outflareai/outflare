// Package digests sends each brand of a workspace a daily or weekly
// summary of the brand: fixes found and confirmed, deploys, site health,
// outreach, lifecycle email, missions and what needs a person. Each digest
// is kept in the app and recorded as digest.ready (a webhook, so Slack or
// another system can post it). It is emailed only through the workspace's
// own email provider, never an account of Outflare's.
package digests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobBuild builds and sends one digest.
const JobBuild = "digest.build"

// Frequencies and recipients.
const (
	Off    = "off"
	Daily  = "daily"
	Weekly = "weekly"

	ToNone    = "none"
	ToAdmins  = "admins"
	ToMembers = "members"
)

// Settings are a brand's digest choices. Without a row: weekly on
// Mondays at 08:00 UTC, kept in the app and sent as a webhook, no email.
type Settings struct {
	Frequency   string     `json:"frequency"`
	Weekday     int        `json:"weekday"`
	Hour        int        `json:"hour"`
	Timezone    string     `json:"timezone"`
	Recipients  string     `json:"recipients"`
	ProviderID  *string    `json:"provider_id"`
	FromAddress string     `json:"from_address"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

// Defaults are the settings of a workspace that changed nothing.
func Defaults() Settings {
	return Settings{Frequency: Weekly, Weekday: 1, Hour: 8, Timezone: "UTC", Recipients: ToNone}
}

// Load reads a brand's settings (the first brand's when brand is empty),
// or the defaults, in the brand's reporting time zone (B4a).
func Load(ctx context.Context, q Querier, ws, brand string) (Settings, error) {
	s := Defaults()
	if err := q.QueryRow(ctx, `SELECT timezone FROM brands WHERE workspace_id = $1 AND id = COALESCE(NULLIF($2, '')::uuid, outflare_first_brand($1))`,
		ws, brand).Scan(&s.Timezone); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	var at time.Time
	err := q.QueryRow(ctx, `SELECT frequency, weekday, hour, timezone, recipients, provider_id::text, from_address, updated_at
		FROM digest_settings WHERE workspace_id = $1 AND brand_id = COALESCE(NULLIF($2, '')::uuid, outflare_first_brand($1))`, ws, brand).
		Scan(&s.Frequency, &s.Weekday, &s.Hour, &s.Timezone, &s.Recipients, &s.ProviderID, &s.FromAddress, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	s.UpdatedAt = &at
	return s, err
}

// Due is the end of the newest period that has closed by now, in the
// settings' time zone: the latest hour (daily) or weekday and hour
// (weekly) at or before now.
func (s Settings) Due(now time.Time) (Period, bool) {
	if s.Frequency != Daily && s.Frequency != Weekly {
		return Period{}, false
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		loc = time.UTC
	}
	t := now.In(loc)
	end := time.Date(t.Year(), t.Month(), t.Day(), s.Hour, 0, 0, 0, loc)
	if end.After(t) {
		end = end.AddDate(0, 0, -1)
	}
	if s.Frequency == Daily {
		return Period{Start: end.AddDate(0, 0, -1), End: end}, true
	}
	for int(end.Weekday()) != s.Weekday {
		end = end.AddDate(0, 0, -1)
	}
	return Period{Start: end.AddDate(0, 0, -7), End: end}, true
}

// Digest is one digest as kept.
type Digest struct {
	ID          string    `json:"id"`
	BrandID     *string   `json:"brand_id"`
	Frequency   string    `json:"frequency"`
	Trigger     string    `json:"trigger"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Sections    []Section `json:"sections"`
	Emailed     int       `json:"emailed"`
	EmailError  *string   `json:"email_error"`
	CreatedAt   time.Time `json:"created_at"`
}

// Service schedules, builds and sends digests.
type Service struct {
	Pool   *pgxpool.Pool
	Emails *emails.Service
	// PublicURL is where links in the email point.
	PublicURL string
	Log       *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Payload is what one digest covers.
type Payload struct {
	// Brand is the brand's id; empty (a job queued before digests were per
	// brand) is the workspace's first brand.
	Brand     string    `json:"brand,omitempty"`
	Frequency string    `json:"frequency"`
	Trigger   string    `json:"trigger"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	// RequestedBy is the actor who asked for a manual digest.
	RequestedBy string `json:"requested_by,omitempty"`
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobBuild, s.job) }

// Run schedules digests until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "digest.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick queues the digests whose period closed in the last six hours and
// that don't exist yet, one per brand. Older periods are skipped, so a
// server that was down for a day doesn't send a pile of stale digests.
func (s *Service) Tick(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT b.workspace_id, b.id::text, COALESCE(d.frequency, 'weekly'), COALESCE(d.weekday, 1), COALESCE(d.hour, 8), COALESCE(d.timezone, b.timezone, 'UTC')
		FROM brands b JOIN workspaces w ON w.id = b.workspace_id LEFT JOIN digest_settings d ON d.workspace_id = b.workspace_id AND d.brand_id = b.id
		WHERE b.status <> 'archived' AND COALESCE(d.frequency, 'weekly') <> 'off'`)
	if err != nil {
		return err
	}
	type due struct {
		ws, brand string
		Settings
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.ws, &d.brand, &d.Frequency, &d.Weekday, &d.Hour, &d.Timezone)
	})
	if err != nil {
		return err
	}
	now := s.now()
	for _, d := range list {
		p, ok := d.Due(now)
		if !ok || now.Sub(p.End) > 6*time.Hour {
			continue
		}
		var exists bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM digests WHERE workspace_id = $1 AND brand_id = $4 AND frequency = $2 AND period_end = $3 AND trigger = 'scheduled')`,
			d.ws, d.Frequency, p.End, d.brand).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: JobBuild, WorkspaceID: &d.ws,
			Payload:        Payload{Brand: d.brand, Frequency: d.Frequency, Trigger: "scheduled", Start: p.Start, End: p.End},
			IdempotencyKey: fmt.Sprintf("digest:%s:%s:%s:%d", d.ws, d.brand, d.Frequency, p.End.Unix()), MaxAttempts: 3}); err != nil {
			return err
		}
	}
	return nil
}

// Request queues a digest of a brand for the period that ends now, in tx,
// for a person or agent who asked for one.
func Request(ctx context.Context, tx pgx.Tx, ws, brand, frequency, actorID string, now time.Time) (int64, error) {
	length := 7 * 24 * time.Hour
	if frequency == Daily {
		length = 24 * time.Hour
	}
	end := now.Truncate(time.Minute)
	return jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobBuild, WorkspaceID: &ws,
		Payload:        Payload{Brand: brand, Frequency: frequency, Trigger: "manual", Start: end.Add(-length), End: end, RequestedBy: actorID},
		IdempotencyKey: fmt.Sprintf("digest:%s:%s:manual:%d", ws, brand, end.Unix()/30), MaxAttempts: 3})
}

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p Payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	_, err := s.Make(ctx, *job.WorkspaceID, p)
	return err
}

// Make builds a digest, keeps it, records digest.ready and emails it when
// the workspace picked its own email provider and recipients. A scheduled
// period that has nothing to say is skipped.
func (s *Service) Make(ctx context.Context, ws string, p Payload) (*Digest, error) {
	start := time.Now()
	brand := p.Brand
	if brand == "" {
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(outflare_first_brand($1)::text, '')`, ws).Scan(&brand); err != nil {
			return nil, err
		}
		if brand == "" {
			return nil, nil // a workspace with no brand has nothing to sum up
		}
	}
	sections, err := Build(ctx, s.Pool, ws, brand, Period{Start: p.Start, End: p.End})
	if err != nil {
		return nil, err
	}
	if len(sections) == 0 && p.Trigger == "scheduled" {
		track.Emit(ctx, track.Event{Name: "digest.skipped", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"frequency": p.Frequency, "reason": "nothing_happened"}})
		return nil, nil
	}
	set, err := Load(ctx, s.Pool, ws, brand)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(sections)
	d := &Digest{BrandID: &brand, Frequency: p.Frequency, Trigger: p.Trigger, PeriodStart: p.Start, PeriodEnd: p.End, Sections: sections}
	var emailErr string
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO digests (workspace_id, brand_id, frequency, trigger, period_start, period_end, sections)
			VALUES ($1, $7, $2, $3, $4, $5, $6)
			ON CONFLICT DO NOTHING
			RETURNING id::text, created_at`, ws, p.Frequency, p.Trigger, p.Start, p.End, raw, brand).Scan(&d.ID, &d.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			d = nil // made already
			return nil
		}
		if err != nil {
			return err
		}
		if set.Recipients != ToNone {
			d.Emailed, emailErr, err = s.email(ctx, tx, ws, brand, set, d)
			if err != nil {
				return err
			}
			if emailErr != "" {
				d.EmailError = &emailErr
			}
			if _, err := tx.Exec(ctx, `UPDATE digests SET emailed = $2, email_error = NULLIF($3, '') WHERE id = $1`, d.ID, d.Emailed, emailErr); err != nil {
				return err
			}
		}
		ids := make([]string, 0, len(sections))
		for _, sec := range sections {
			ids = append(ids, sec.ID)
		}
		ev := audit.Event{WorkspaceID: ws, ActorType: "system", Action: "digest.ready", TargetType: "digest", TargetID: d.ID,
			Data: map[string]any{"brand_id": brand, "frequency": p.Frequency, "trigger": p.Trigger, "period_start": p.Start.UTC().Format(time.RFC3339),
				"period_end": p.End.UTC().Format(time.RFC3339), "sections": ids, "emailed": d.Emailed},
			Props: map[string]any{"brand_id": brand, "sections": len(ids), "emailed": d.Emailed, "duration_ms": time.Since(start).Milliseconds()}}
		if p.RequestedBy != "" {
			ev.ActorID, ev.ActorType = p.RequestedBy, "human"
		}
		return audit.Record(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	if emailErr != "" {
		track.Fail(ctx, s.Log, track.Event{Name: "digest.email_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"digest_id": d.ID}}, "not_configured", errors.New(emailErr))
	}
	return d, nil
}

// Recipient is a member who gets the digest by email.
type Recipient struct {
	UserID, Email, Name string
}

// Recipients lists who gets a brand's digest email: owners and admins,
// or every member, who sees the brand, has a confirmed email address and
// hasn't turned it off.
func Recipients(ctx context.Context, q Querier, ws, brand, who string) ([]Recipient, error) {
	if who == ToNone {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT u.id::text, u.email, u.name
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND (cardinality(m.brand_ids) = 0 OR $3::uuid = ANY(m.brand_ids)) AND u.email_verified_at IS NOT NULL
		  AND ($2 = 'members' OR m.role IN ('owner', 'admin'))
		  AND NOT EXISTS (SELECT 1 FROM digest_optouts o WHERE o.workspace_id = $1 AND o.user_id = u.id)
		ORDER BY u.email`, ws, who, brand)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Recipient, error) {
		var x Recipient
		return x, r.Scan(&x.UserID, &x.Email, &x.Name)
	})
}

// Problem says why a workspace's digest can't be emailed with these
// settings, or "" when it can: a sender on a verified domain of the
// workspace's own active email provider.
func Problem(ctx context.Context, q Querier, ws string, set Settings) (string, error) {
	if set.Recipients == ToNone {
		return "", nil
	}
	if set.ProviderID == nil || set.FromAddress == "" {
		return "pick your email provider and a sender under Digests to email it", nil
	}
	var ok bool
	_, domain, _ := strings.Cut(strings.ToLower(set.FromAddress), "@")
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_providers p WHERE p.id = $1 AND p.workspace_id = $2 AND p.status = 'active'
		AND EXISTS (SELECT 1 FROM email_domains d WHERE d.provider_id = p.id AND d.status = 'verified' AND ($3 = d.domain OR $3 LIKE '%.' || d.domain)))`,
		*set.ProviderID, ws, domain).Scan(&ok); err != nil {
		return "", err
	}
	if !ok {
		return set.FromAddress + " is not on a verified sending domain of an active email provider", nil
	}
	return "", nil
}

// email queues the digest to its recipients through the workspace's own
// provider, in tx. A missing or unusable provider is not an error: it
// comes back as why nothing was sent.
func (s *Service) email(ctx context.Context, tx pgx.Tx, ws, brand string, set Settings, d *Digest) (int, string, error) {
	if s.Emails == nil {
		return 0, "email is not set up on this server", nil
	}
	if why, err := Problem(ctx, tx, ws, set); err != nil || why != "" {
		return 0, why, err
	}
	to, err := Recipients(ctx, tx, ws, brand, set.Recipients)
	if err != nil {
		return 0, "", err
	}
	// The workspace's name, with the brand's when it has more than one.
	var name, slug string
	if err := tx.QueryRow(ctx, `SELECT w.name || CASE WHEN (SELECT count(*) FROM brands x WHERE x.workspace_id = w.id) > 1 THEN ' · ' || b.name ELSE '' END, w.slug
		FROM workspaces w JOIN brands b ON b.id = $2 AND b.workspace_id = w.id WHERE w.id = $1`, ws, brand).Scan(&name, &slug); err != nil {
		return 0, "", err
	}
	subject, html, text := Render(name, strings.TrimRight(s.PublicURL, "/")+"/"+slug, d)
	n := 0
	for _, r := range to {
		if _, err := s.Emails.Queue(ctx, tx, emails.System, emails.NewEmail{
			WorkspaceID: ws, BrandID: &brand, ProviderID: *set.ProviderID, Purpose: "transactional", From: set.FromAddress,
			To: []string{r.Email}, Subject: subject, HTML: html, Text: text, Tags: []string{"digest"},
			Metadata: map[string]any{"digest_id": d.ID}, Trigger: "system", TriggerRef: d.ID, Surface: "job",
		}); err != nil {
			return n, "", err
		}
		n++
	}
	if n == 0 {
		return 0, "nobody to email: no " + map[string]string{ToAdmins: "owner or admin", ToMembers: "member"}[set.Recipients] + " with a confirmed address who sees this brand", nil
	}
	return n, "", nil
}
