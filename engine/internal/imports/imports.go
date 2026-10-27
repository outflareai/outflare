// Package imports moves a workspace over from another sequencer: campaigns
// become sequences (always as drafts, so nothing sends until someone starts
// them), leads come across with where they got to, bounced and unsubscribed
// leads go on the suppression list, and sending accounts are matched to
// mailboxes by address. Passwords are never readable through other tools'
// APIs, so accounts not yet here are listed for the person to connect.
package imports

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
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
)

// JobImport runs one import_runs row.
const JobImport = "import.platform"

// ErrNotImporter means the integration's connector cannot import.
var ErrNotImporter = errors.New("this connection cannot import campaigns")

// Options says what to bring over. Nil slices mean everything.
type Options struct {
	CampaignIDs []string `json:"campaign_ids,omitempty"`
	Leads       bool     `json:"leads"`
	Mailboxes   bool     `json:"mailboxes"`
}

// Run is one import and what it did.
type Run struct {
	ID            string     `json:"id"`
	IntegrationID string     `json:"integration_id"`
	Connector     string     `json:"connector"`
	Options       Options    `json:"options"`
	Status        string     `json:"status"`
	Summary       Summary    `json:"summary"`
	Error         *string    `json:"error"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

// Summary is what an import found and changed.
type Summary struct {
	Campaigns []CampaignResult `json:"campaigns"`
	Mailboxes *MailboxResult   `json:"mailboxes,omitempty"`
	Leads     LeadCounts       `json:"leads"`
}

type CampaignResult struct {
	ExternalID string   `json:"external_id"`
	Name       string   `json:"name"`
	Status     string   `json:"status"` // the outside tool's status
	SequenceID string   `json:"sequence_id"`
	Created    bool     `json:"created"` // false when an earlier import's sequence was reused
	Steps      int      `json:"steps"`
	Leads      int      `json:"leads"`
	Enrolled   int      `json:"enrolled"`
	Warnings   []string `json:"warnings"`
}

type MailboxResult struct {
	Total   int                          `json:"total"`
	Matched int                          `json:"matched"`
	Missing []connectors.ExternalMailbox `json:"missing"` // not in this workspace yet; connect them to send
}

type LeadCounts struct {
	Seen       int `json:"seen"`
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Suppressed int `json:"suppressed"` // bounced or unsubscribed at the other tool, added to suppression
	Enrolled   int `json:"enrolled"`
	Skipped    int `json:"skipped"` // replied, finished, or past the last step
}

// Preview is what an import would bring over, read live.
type Preview struct {
	Campaigns []connectors.ExternalCampaign `json:"campaigns"`
	Mailboxes MailboxResult                 `json:"mailboxes"`
}

type Service struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
}

func (s *Service) Register(q *jobs.Queue) { q.Handle(JobImport, s.job) }

func (s *Service) importer(ctx context.Context, ws, integrationID string) (*connectors.Opened, connectors.Importer, error) {
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return nil, nil, err
	}
	imp, ok := o.Connector.(connectors.Importer)
	if !ok {
		return nil, nil, ErrNotImporter
	}
	return o, imp, nil
}

// Preview lists the outside campaigns and matches sending accounts, without
// changing anything.
func (s *Service) Preview(ctx context.Context, ws, integrationID string) (Preview, error) {
	o, imp, err := s.importer(ctx, ws, integrationID)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{Campaigns: []connectors.ExternalCampaign{}}
	err = imp.Campaigns(ctx, o.Client, o.Creds, func(k connectors.ExternalCampaign) error {
		p.Campaigns = append(p.Campaigns, k)
		return nil
	})
	if err == nil {
		p.Mailboxes, err = s.matchMailboxes(ctx, ws, o, imp)
	}
	s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, err)
	return p, err
}

// Start records a run and queues it, in the caller's transaction.
func Start(ctx context.Context, tx pgx.Tx, ws, integrationID, connector, actorID, actorType string, opts Options) (Run, error) {
	var r Run
	b, _ := json.Marshal(opts)
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO import_runs (workspace_id, integration_id, connector, options, created_by, created_by_type)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, ws, integrationID, connector, b, actor, actorType).Scan(&r.ID)
	if err != nil {
		return r, err
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobImport, WorkspaceID: &ws, Payload: map[string]string{"run_id": r.ID}, MaxAttempts: 3}); err != nil {
		return r, err
	}
	return Get(ctx, tx, ws, r.ID)
}

const runCols = `id, integration_id, connector, options, status, summary, error, created_at, started_at, finished_at`

func scan(row pgx.Row) (Run, error) {
	var r Run
	var opts, sum []byte
	if err := row.Scan(&r.ID, &r.IntegrationID, &r.Connector, &opts, &r.Status, &sum, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
		return r, err
	}
	_ = json.Unmarshal(opts, &r.Options)
	_ = json.Unmarshal(sum, &r.Summary)
	if r.Summary.Campaigns == nil {
		r.Summary.Campaigns = []CampaignResult{}
	}
	return r, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func Get(ctx context.Context, q querier, ws, id string) (Run, error) {
	return scan(q.QueryRow(ctx, `SELECT `+runCols+` FROM import_runs WHERE workspace_id = $1 AND id = $2`, ws, id))
}

func List(ctx context.Context, q querier, ws string) ([]Run, error) {
	rows, err := q.Query(ctx, `SELECT `+runCols+` FROM import_runs WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT 50`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return s.Execute(ctx, *job.WorkspaceID, p.RunID)
}

// Execute runs an import now. Outside-service failures end the run as
// failed with the reason, rather than retrying.
func (s *Service) Execute(ctx context.Context, ws, runID string) error {
	var integrationID, actorType string
	var actorID *string
	var optsRaw []byte
	err := s.Pool.QueryRow(ctx, `
		UPDATE import_runs SET status = 'running', started_at = now(), error = NULL
		WHERE workspace_id = $1 AND id = $2 AND status IN ('queued', 'running')
		RETURNING integration_id, options, created_by, created_by_type`, ws, runID).Scan(&integrationID, &optsRaw, &actorID, &actorType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // finished or gone
	}
	if err != nil {
		return err
	}
	var opts Options
	_ = json.Unmarshal(optsRaw, &opts)
	sum, runErr := s.run(ctx, ws, integrationID, opts)
	status, msg := "done", (*string)(nil)
	if runErr != nil {
		status = "failed"
		m := runErr.Error()
		msg = &m
	}
	b, _ := json.Marshal(sum)
	actor := audit.Event{WorkspaceID: ws, ActorType: actorType, Action: "import." + status, TargetType: "import_run", TargetID: runID,
		Data: map[string]any{"integration_id": integrationID, "campaigns": len(sum.Campaigns), "leads": sum.Leads.Seen, "error": msg}}
	if actorID != nil {
		actor.ActorID = *actorID
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE import_runs SET status = $3, summary = $4, error = $5, finished_at = now() WHERE workspace_id = $1 AND id = $2`,
			ws, runID, status, b, msg); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor)
	})
}

func (s *Service) run(ctx context.Context, ws, integrationID string, opts Options) (Summary, error) {
	sum := Summary{Campaigns: []CampaignResult{}}
	o, imp, err := s.importer(ctx, ws, integrationID)
	if err != nil {
		return sum, err
	}
	defer func() { s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, err) }()
	if opts.Mailboxes {
		m, err := s.matchMailboxes(ctx, ws, o, imp)
		if err != nil {
			return sum, err
		}
		sum.Mailboxes = &m
	}
	want := map[string]bool{}
	for _, id := range opts.CampaignIDs {
		want[id] = true
	}
	var camps []connectors.ExternalCampaign
	if err = imp.Campaigns(ctx, o.Client, o.Creds, func(k connectors.ExternalCampaign) error {
		if len(want) == 0 || want[k.ID] {
			camps = append(camps, k)
		}
		return nil
	}); err != nil {
		return sum, err
	}
	for _, k := range camps {
		res, err := s.campaign(ctx, ws, integrationID, o, imp, k, opts.Leads, &sum.Leads)
		sum.Campaigns = append(sum.Campaigns, res)
		if err != nil {
			return sum, fmt.Errorf("%s: %w", k.Name, err)
		}
	}
	return sum, nil
}

func (s *Service) matchMailboxes(ctx context.Context, ws string, o *connectors.Opened, imp connectors.Importer) (MailboxResult, error) {
	res := MailboxResult{Missing: []connectors.ExternalMailbox{}}
	have := map[string]bool{}
	rows, err := s.Pool.Query(ctx, `SELECT lower(email) FROM mailboxes WHERE workspace_id = $1`, ws)
	if err != nil {
		return res, err
	}
	emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return res, err
	}
	for _, e := range emails {
		have[e] = true
	}
	err = imp.SendingAccounts(ctx, o.Client, o.Creds, func(m connectors.ExternalMailbox) error {
		res.Total++
		if have[strings.ToLower(m.Email)] {
			res.Matched++
		} else if len(res.Missing) < 10000 {
			res.Missing = append(res.Missing, m)
		}
		return nil
	})
	return res, err
}

// campaign brings one campaign over: the sequence (reused on a second
// import), then its leads in batches.
func (s *Service) campaign(ctx context.Context, ws, integrationID string, o *connectors.Opened, imp connectors.Importer, k connectors.ExternalCampaign, withLeads bool, counts *LeadCounts) (CampaignResult, error) {
	res := CampaignResult{ExternalID: k.ID, Name: k.Name, Status: k.Status, Warnings: []string{}}
	steps := make([]step, 0, len(k.Steps))
	for i, st := range k.Steps {
		subject, w1 := Convert(st.Subject)
		body, w2 := Convert(st.Body)
		for _, w := range append(w1, w2...) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("Step %d %s", i+1, w))
		}
		if len(st.Variants) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("Step %d had %d A/B variants; only the first came over", i+1, len(st.Variants)+1))
		}
		if body == "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("Step %d was empty and was left out", i+1))
			continue
		}
		delay := max(0, min(st.DelayDays, 365))
		if len(steps) > 0 && delay < 1 {
			delay = 1
		}
		if len(steps) == 0 {
			delay = 0
			if subject == "" {
				subject = k.Name
				res.Warnings = append(res.Warnings, "The first step had no subject; it is set to the campaign name")
			}
		}
		steps = append(steps, step{delay, subject, body})
	}
	res.Steps = len(steps)

	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var seqID string
		err := tx.QueryRow(ctx, `
			SELECT i.local_id FROM import_items i JOIN sequences q ON q.id = i.local_id AND q.workspace_id = i.workspace_id
			WHERE i.integration_id = $1 AND i.kind = 'campaign' AND i.external_id = $2`, integrationID, k.ID).Scan(&seqID)
		if errors.Is(err, pgx.ErrNoRows) {
			tz := k.Timezone
			if tz == "" || !validZone(tz) {
				tz = "UTC"
			}
			var limit *int
			if k.DailyLimit > 0 {
				limit = &k.DailyLimit
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO sequences (workspace_id, name, timezone, daily_new_limit) VALUES ($1, $2, $3, $4) RETURNING id`,
				ws, uniqueName(k.Name, o.Connector.Definition().Name), tz, limit).Scan(&seqID); err != nil {
				return err
			}
			for i, st := range steps {
				if _, err := tx.Exec(ctx, `INSERT INTO sequence_steps (sequence_id, position, delay_days, subject, body) VALUES ($1, $2, $3, $4, $5)`,
					seqID, i, st.delay, st.subject, st.body); err != nil {
					return err
				}
			}
			res.Created = true
		} else if err != nil {
			return err
		}
		res.SequenceID = seqID
		_, err = tx.Exec(ctx, `
			INSERT INTO import_items (workspace_id, integration_id, kind, external_id, local_id, name) VALUES ($1, $2, 'campaign', $3, $4, $5)
			ON CONFLICT (integration_id, kind, external_id) DO UPDATE SET local_id = EXCLUDED.local_id, name = EXCLUDED.name, updated_at = now()`,
			ws, integrationID, k.ID, seqID, k.Name)
		return err
	})
	if err != nil || !withLeads {
		return res, err
	}

	mailboxes, err := s.mailboxIDs(ctx, ws)
	if err != nil {
		return res, err
	}
	var batch []connectors.ExternalLead
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, e, err := s.leadBatch(ctx, ws, res.SequenceID, o.Connector.Definition().Name, k.Name, len(steps), steps, batch, mailboxes, counts)
		res.Leads += n
		res.Enrolled += e
		batch = batch[:0]
		return err
	}
	err = imp.CampaignLeads(ctx, o.Client, o.Creds, k.ID, func(l connectors.ExternalLead) error {
		batch = append(batch, l)
		if len(batch) >= 1000 {
			return flush()
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, flush()
}

type step struct {
	delay         int
	subject, body string
}

func (s *Service) mailboxIDs(ctx context.Context, ws string) (map[string]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT lower(email), id FROM mailboxes WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var e, id string
		if err := rows.Scan(&e, &id); err != nil {
			return nil, err
		}
		out[e] = id
	}
	return out, rows.Err()
}

// leadBatch upserts leads, suppresses the ones the other tool saw bounce or
// unsubscribe, and enrolls the active ones at the step they reached, on the
// mailbox that already wrote to them so follow-ups stay in one thread.
func (s *Service) leadBatch(ctx context.Context, ws, seqID, tool, campaign string, nSteps int, steps []step, batch []connectors.ExternalLead, mailboxes map[string]string, counts *LeadCounts) (int, int, error) {
	in := make([]leads.Lead, 0, len(batch))
	byEmail := map[string]connectors.ExternalLead{}
	var bounced, unsubscribed []leads.Suppression
	for _, l := range batch {
		email := strings.ToLower(strings.TrimSpace(l.Email))
		if email == "" || !strings.Contains(email, "@") {
			continue
		}
		in = append(in, leads.Lead{Email: email, FirstName: l.FirstName, LastName: l.LastName, Company: l.Company, Title: l.Title, Custom: l.Fields})
		byEmail[email] = l
		switch l.Status {
		case "bounced":
			bounced = append(bounced, leads.Suppression{Kind: "email", Value: email})
		case "unsubscribed":
			unsubscribed = append(unsubscribed, leads.Suppression{Kind: "email", Value: email})
		}
	}
	in, _ = leads.Dedupe(in)
	enrolled := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, err := leads.Import(ctx, tx, ws, strings.ToLower(tool)+"_import", nil, in)
		if err != nil {
			return err
		}
		counts.Seen += len(in)
		counts.Created += r.Created
		counts.Updated += r.Updated
		note := "Imported from " + tool + ": " + campaign
		n1, err := leads.AddSuppressions(ctx, tx, ws, "bounced", strings.ToLower(tool)+"_import", note, bounced)
		if err != nil {
			return err
		}
		n2, err := leads.AddSuppressions(ctx, tx, ws, "unsubscribed", strings.ToLower(tool)+"_import", note, unsubscribed)
		if err != nil {
			return err
		}
		counts.Suppressed += n1 + n2
		emails := make([]string, 0, len(in))
		for _, l := range in {
			emails = append(emails, l.Email)
		}
		rows, err := tx.Query(ctx, `SELECT lower(email), id FROM leads WHERE workspace_id = $1 AND lower(email) = ANY($2)`, ws, emails)
		if err != nil {
			return err
		}
		ids := map[string]string{}
		for rows.Next() {
			var e, id string
			if err := rows.Scan(&e, &id); err != nil {
				rows.Close()
				return err
			}
			ids[e] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		firstSubject := ""
		if len(steps) > 0 {
			firstSubject = steps[0].subject
		}
		for email, id := range ids {
			l := byEmail[email]
			if l.Status != "active" && l.Status != "paused" || l.StepDone >= nSteps {
				counts.Skipped++
				continue
			}
			var mb *string
			if m, ok := mailboxes[strings.ToLower(l.Mailbox)]; ok {
				mb = &m
			}
			// A lead part-way through keeps its thread: later steps with no
			// subject reply under the first step's subject.
			var subject *string
			if l.StepDone > 0 && firstSubject != "" {
				subject = &firstSubject
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO enrollments (workspace_id, sequence_id, lead_id, mailbox_id, next_step, next_send_at, thread_subject)
				VALUES ($1, $2, $3, $4, $5, now(), $6) ON CONFLICT (sequence_id, lead_id) DO NOTHING`,
				ws, seqID, id, mb, l.StepDone, subject)
			if err != nil {
				return err
			}
			enrolled += int(tag.RowsAffected())
		}
		counts.Enrolled += enrolled
		return nil
	})
	return len(in), enrolled, err
}

func validZone(tz string) bool {
	_, err := time.LoadLocation(tz)
	return err == nil
}

func uniqueName(name, tool string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Untitled"
	}
	return name + " (from " + tool + ")"
}
