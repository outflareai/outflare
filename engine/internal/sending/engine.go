package sending

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// JobSend sends the next step of one enrollment.
const JobSend = "sequence.send"

// maxAttempts is how many times one step is tried before the enrollment is
// marked failed.
const maxAttempts = 5

// Engine schedules and sends sequence steps.
type Engine struct {
	Pool      *pgxpool.Pool
	Mail      *mailbox.Service
	Box       *secrets.Box
	PublicURL string
	// Platforms opens sequencer connections, to answer replies that came
	// through one; nil turns that off.
	Platforms *connectors.Store
	Log       *slog.Logger
	// Now is the clock; tests replace it.
	Now func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Register adds the send handler to a queue.
func (e *Engine) Register(q *jobs.Queue) { q.Handle(JobSend, e.sendJob) }

// Run ticks the scheduler every interval until ctx ends.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := e.Tick(ctx); err != nil && ctx.Err() == nil {
			e.Log.Error("sequence tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick queues a send job for every due enrollment in an active sequence and
// returns how many it queued. Taking an enrollment clears next_send_at in
// the same transaction, so several engines can tick at once without
// queueing a step twice.
func (e *Engine) Tick(ctx context.Context) (int, error) {
	// An enrollment whose job died without rescheduling it is picked up
	// again. The messages table stops a step from being sent twice.
	if _, err := e.Pool.Exec(ctx, `
		UPDATE enrollments SET next_send_at = now(), updated_at = now()
		WHERE status = 'active' AND next_send_at IS NULL AND updated_at < now() - interval '15 minutes'`); err != nil {
		return 0, err
	}
	n := 0
	err := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH due AS (
				SELECT en.id FROM enrollments en
				JOIN sequences s ON s.id = en.sequence_id AND s.status = 'active'
				WHERE en.status = 'active' AND en.next_send_at <= $1
				ORDER BY en.next_send_at
				LIMIT 1000
				FOR UPDATE OF en SKIP LOCKED
			)
			UPDATE enrollments en SET next_send_at = NULL, updated_at = now()
			FROM due WHERE en.id = due.id
			RETURNING en.id::text, en.workspace_id::text`, e.now())
		if err != nil {
			return err
		}
		type due struct{ id, ws string }
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
			var d due
			return d, r.Scan(&d.id, &d.ws)
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobSend, WorkspaceID: &d.ws, Payload: sendPayload{EnrollmentID: d.id}, MaxAttempts: 3}); err != nil {
				return err
			}
		}
		n = len(list)
		return nil
	})
	return n, err
}

type sendPayload struct {
	EnrollmentID string `json:"enrollment_id"`
	// Now skips the wait for the sending window and the daily new-lead
	// cap: SendNow in a test workspace. Every other check still applies.
	Now bool `json:"now,omitempty"`
}

// SendNow queues the next step of every active enrollment in a
// workspace's active sequences (or one sequence) at once, without waiting
// for its delay, the sending window or the daily new-lead cap. It is for
// test workspaces, whose simulated mailboxes deliver nowhere, so a whole
// sequence can be tried in a minute. It returns how many it queued.
func (e *Engine) SendNow(ctx context.Context, workspaceID string, sequenceID *string) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		// An enrollment without next_send_at is being sent already.
		rows, err := tx.Query(ctx, `
			WITH due AS (
				SELECT en.id FROM enrollments en
				JOIN sequences s ON s.id = en.sequence_id AND s.status = 'active'
				WHERE en.workspace_id = $1 AND ($2::uuid IS NULL OR en.sequence_id = $2)
				  AND en.status = 'active' AND en.next_send_at IS NOT NULL
				LIMIT 1000
				FOR UPDATE OF en SKIP LOCKED
			)
			UPDATE enrollments en SET next_send_at = NULL, updated_at = now()
			FROM due WHERE en.id = due.id
			RETURNING en.id::text`, workspaceID, sequenceID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobSend, WorkspaceID: &workspaceID, Payload: sendPayload{id, true}, MaxAttempts: 3}); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// enrollment is the state a send needs, read under a row lock.
type enrollment struct {
	ID, WorkspaceID, SequenceID, LeadID string
	MailboxID                           *string
	Status                              string
	NextStep                            int
	ThreadSubject, LastMessageID        *string
	References                          string
	Handoff                             bool // follow-ups continue from another mailbox as fresh emails
	SeqStatus                           string
	Timezone                            string
	SendDays                            []int16
	SendStart, SendEnd                  pgtype.Time
	DailyNewLimit                       *int
	DailySendLimit                      *int
	// The actor that created the sequence, and its daily send budget when
	// that actor is an API token with one.
	CreatedBy   *string
	TokenBudget *int
	// The sequence's project, when it has one: its status and its cap on
	// sends from all of its sequences together.
	ProjectID     *string
	ProjectStatus *string
	ProjectLimit  *int
	// The agent mission that started the sequence, if any.
	MissionID *string
	// The sequence's brand, and the workspace's cross-brand window in days
	// (nil when the rule is off).
	BrandID        *string
	CrossBrandDays *int
}

// prepared is a message reserved in phase one and sent in phase two.
type prepared struct {
	messageID string // row id
	email     Email
	account   *mailbox.Account
	attempts  int
	nextDelay *int // delay of the step after this one, nil if last
}

func (e *Engine) sendJob(ctx context.Context, job jobs.Job) error {
	var p sendPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	var prep *prepared
	var en enrollment
	err := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		prep, en, err = e.prepare(ctx, tx, p.EnrollmentID, p.Now)
		return err
	})
	if err != nil || prep == nil {
		return err
	}

	_, sendErr := e.Mail.Dialer.Send(ctx, prep.account.SMTP, prep.account.SMTPCred, prep.email.From,
		[]string{prep.email.To}, strings.NewReader(string(prep.email.Bytes())))
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		return e.finish(ctx, tx, en, prep, sendErr)
	})
}

// prepare runs every check and, when the email may go, reserves it as a
// 'sending' message row and returns it. A nil result means nothing to send
// now: the enrollment was finished, blocked or rescheduled.
func (e *Engine) prepare(ctx context.Context, tx pgx.Tx, enrollmentID string, skipWait bool) (*prepared, enrollment, error) {
	var en enrollment
	err := tx.QueryRow(ctx, `
		SELECT en.id, en.workspace_id, en.sequence_id, en.lead_id, en.mailbox_id, en.status, en.next_step,
		       en.thread_subject, en.last_message_id, en.references_ids, en.handoff,
		       s.status, s.timezone, s.send_days, s.send_start, s.send_end, s.daily_new_limit, s.daily_send_limit,
		       s.created_by, t.daily_send_budget, s.project_id, p.status, p.daily_send_limit, s.mission_id,
		       s.brand_id::text, w.cross_brand_days
		FROM enrollments en JOIN sequences s ON s.id = en.sequence_id JOIN workspaces w ON w.id = en.workspace_id
		LEFT JOIN api_tokens t ON t.actor_id = s.created_by
		LEFT JOIN projects p ON p.id = s.project_id
		WHERE en.id = $1 FOR UPDATE OF en`, enrollmentID,
	).Scan(&en.ID, &en.WorkspaceID, &en.SequenceID, &en.LeadID, &en.MailboxID, &en.Status, &en.NextStep,
		&en.ThreadSubject, &en.LastMessageID, &en.References, &en.Handoff,
		&en.SeqStatus, &en.Timezone, &en.SendDays, &en.SendStart, &en.SendEnd, &en.DailyNewLimit, &en.DailySendLimit,
		&en.CreatedBy, &en.TokenBudget, &en.ProjectID, &en.ProjectStatus, &en.ProjectLimit, &en.MissionID,
		&en.BrandID, &en.CrossBrandDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, en, nil
	}
	if err != nil || en.Status != "active" {
		return nil, en, err
	}
	now := e.now()
	defer_ := func(until time.Time, reason string) (*prepared, enrollment, error) {
		_, err := tx.Exec(ctx, `UPDATE enrollments SET next_send_at = $2, status_reason = $3, updated_at = now() WHERE id = $1`, en.ID, until, reason)
		return nil, en, err
	}
	end := func(status, reason string) (*prepared, enrollment, error) {
		_, err := tx.Exec(ctx, `UPDATE enrollments SET status = $2, status_reason = NULLIF($3, ''), next_send_at = NULL, updated_at = now() WHERE id = $1`, en.ID, status, reason)
		return nil, en, err
	}
	if en.SeqStatus != "active" {
		// Paused: the tick skips it until the sequence is active again.
		return defer_(now, "")
	}
	if en.ProjectStatus != nil && *en.ProjectStatus != "active" {
		// The project's switch is over all of its sequences; check again
		// shortly rather than lose the place in the schedule.
		return defer_(now.Add(15*time.Minute), policy.ProjectPaused)
	}

	var stepSubject, stepBody string
	err = tx.QueryRow(ctx, `SELECT subject, body FROM sequence_steps WHERE sequence_id = $1 AND position = $2`,
		en.SequenceID, en.NextStep).Scan(&stepSubject, &stepBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return end("completed", "")
	}
	if err != nil {
		return nil, en, err
	}

	// A step reserved by a run that never recorded the outcome may have
	// been delivered. Never risk sending it twice: count it and move on.
	var msgRowID, msgStatus string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id, status, attempts FROM messages WHERE enrollment_id = $1 AND step_position = $2 AND status <> 'blocked'`,
		en.ID, en.NextStep).Scan(&msgRowID, &msgStatus, &attempts)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, en, err
	case msgStatus == "sending" || msgStatus == "unknown" || msgStatus == "sent":
		if _, err := tx.Exec(ctx, `UPDATE messages SET status = 'unknown' WHERE id = $1 AND status = 'sending'`, msgRowID); err != nil {
			return nil, en, err
		}
		return nil, en, e.advance(ctx, tx, en, nil, "")
	}

	var lead struct {
		Email, FirstName, LastName, Company, Title, Phone, LinkedIn, Website, Domain string
		Custom                                                                       map[string]string
	}
	err = tx.QueryRow(ctx, `
		SELECT email, first_name, last_name, company, title, phone, linkedin_url, website, domain, custom
		FROM leads WHERE id = $1`, en.LeadID).Scan(&lead.Email, &lead.FirstName, &lead.LastName, &lead.Company,
		&lead.Title, &lead.Phone, &lead.LinkedIn, &lead.Website, &lead.Domain, &lead.Custom)
	if err != nil {
		return nil, en, err
	}

	// 1. Recipient: never email someone suppressed or invalid.
	decisions, err := policy.CheckRecipients(ctx, tx, en.WorkspaceID, []string{lead.Email})
	if err != nil {
		return nil, en, err
	}
	if d := decisions[0]; !d.Allowed {
		reason := strings.Join(d.Reasons, ", ")
		if err := e.recordBlocked(ctx, tx, en, lead.Email, reason); err != nil {
			return nil, en, err
		}
		return end("blocked", reason)
	}
	// 1b. Cross-brand rule: a brand's first email never reaches someone
	// another brand emailed within the workspace's window. Follow-ups carry
	// on a conversation already started, so only the first step checks.
	if en.NextStep == 0 && en.BrandID != nil && en.CrossBrandDays != nil {
		other, err := policy.OtherBrands(ctx, tx, en.WorkspaceID, []string{lead.Email}, *en.BrandID, *en.CrossBrandDays, now)
		if err != nil {
			return nil, en, err
		}
		if h, ok := other[strings.ToLower(lead.Email)]; ok {
			reason := policy.OtherBrand + ":" + h
			if err := e.recordBlocked(ctx, tx, en, lead.Email, reason); err != nil {
				return nil, en, err
			}
			return end("blocked", reason)
		}
	}
	g, err := verify.Check(ctx, tx, en.WorkspaceID, lead.Email, en.NextStep == 0, now)
	if err != nil {
		return nil, en, err
	}
	switch {
	case g.Defer:
		return defer_(now.Add(5*time.Minute), g.Reason)
	case !g.Allow:
		if err := e.recordBlocked(ctx, tx, en, lead.Email, g.Reason); err != nil {
			return nil, en, err
		}
		return end("blocked", g.Reason)
	}

	// 2. Sending window, in the sequence's time zone.
	loc, err := time.LoadLocation(en.Timezone)
	if err != nil {
		return end("failed", "invalid time zone "+en.Timezone)
	}
	win := policy.Window{Location: loc, Start: clock(en.SendStart), End: clock(en.SendEnd)}
	for _, d := range en.SendDays {
		win.Days = append(win.Days, int(d))
	}
	v, err := policy.CheckWindow(win, now)
	if err != nil {
		return end("failed", err.Error())
	}
	if !v.Allow && !skipWait {
		return defer_(v.DeferUntil, v.Reason)
	}

	// 3. New leads per day.
	if en.NextStep == 0 && en.DailyNewLimit != nil && !skipWait {
		// Serialise first sends of this sequence so two jobs cannot both
		// see room under the cap.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM sequences WHERE id = $1 FOR NO KEY UPDATE`, en.SequenceID); err != nil {
			return nil, en, err
		}
		local := now.In(loc)
		dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		var started int
		if err := tx.QueryRow(ctx, `
			-- Every attempt counts, bounces included: the lead was started.
			SELECT count(*) FROM messages WHERE sequence_id = $1 AND step_position = 0
			AND status <> 'blocked' AND created_at >= $2`, en.SequenceID, dayStart).Scan(&started); err != nil {
			return nil, en, err
		}
		if started >= *en.DailyNewLimit {
			return defer_(win.Next(dayStart.AddDate(0, 0, 1)).Add(policy.Jitter(time.Hour)), policy.NewLeadLimit)
		}
	}

	// 3a. Sequence budget: every step of the sequence together sends at
	// most its daily send limit.
	if en.DailySendLimit != nil {
		// Serialise sends of one sequence so two jobs cannot both see room.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM sequences WHERE id = $1 FOR NO KEY UPDATE`, en.SequenceID); err != nil {
			return nil, en, err
		}
		var sent int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM messages WHERE sequence_id = $1 AND status IN ('sending', 'sent', 'unknown')
			AND created_at > $2::timestamptz - interval '24 hours'`, en.SequenceID, now).Scan(&sent); err != nil {
			return nil, en, err
		}
		if sent >= *en.DailySendLimit {
			return defer_(now.Add(time.Hour), policy.SequenceLimit)
		}
	}

	// 3b. Agent budget: an API token's sequences together send at most its
	// daily budget.
	if en.TokenBudget != nil {
		var sent int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM messages g JOIN sequences q ON q.id = g.sequence_id
			WHERE q.created_by = $1 AND g.status IN ('sending', 'sent', 'unknown') AND g.created_at > $2::timestamptz - interval '24 hours'`,
			*en.CreatedBy, now).Scan(&sent); err != nil {
			return nil, en, err
		}
		if sent >= *en.TokenBudget {
			return defer_(now.Add(time.Hour), policy.TokenBudget)
		}
	}

	// 3c. Project budget: the project's sequences together send at most
	// its daily limit.
	if en.ProjectLimit != nil {
		// Serialise sends of one project so two jobs cannot both see room.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM projects WHERE id = $1 FOR NO KEY UPDATE`, *en.ProjectID); err != nil {
			return nil, en, err
		}
		var sent int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM messages g JOIN sequences q ON q.id = g.sequence_id
			WHERE q.project_id = $1 AND g.status IN ('sending', 'sent', 'unknown') AND g.created_at > $2::timestamptz - interval '24 hours'`,
			*en.ProjectID, now).Scan(&sent); err != nil {
			return nil, en, err
		}
		if sent >= *en.ProjectLimit {
			return defer_(now.Add(time.Hour), policy.ProjectLimit)
		}
	}

	// 3d. Mission: a sequence started in an agent mission sends only while
	// the mission is not paused, and at most the mission's send limit.
	if en.MissionID != nil {
		var status string
		var limit *int
		// Locked, so two sends cannot both see room under the limit.
		if err := tx.QueryRow(ctx, `SELECT status, max_sends FROM missions WHERE id = $1 FOR NO KEY UPDATE`, *en.MissionID).Scan(&status, &limit); err != nil {
			return nil, en, err
		}
		if status == "paused" {
			return defer_(now.Add(time.Hour), policy.MissionPaused)
		}
		if limit != nil {
			var sent int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM messages g JOIN sequences q ON q.id = g.sequence_id
				WHERE q.mission_id = $1 AND g.status IN ('sending', 'sent', 'unknown')`, *en.MissionID).Scan(&sent); err != nil {
				return nil, en, err
			}
			if sent >= *limit {
				return defer_(now.Add(time.Hour), policy.MissionBudget)
			}
		}
	}

	// 4. Content: a missing variable blocks the send rather than greeting
	// someone with "Hi ,". Sender fields are checked once the mailbox is known.
	vars := map[string]string{
		"email": lead.Email, "first_name": lead.FirstName, "last_name": lead.LastName, "company": lead.Company,
		"title": lead.Title, "phone": lead.Phone, "linkedin_url": lead.LinkedIn, "website": lead.Website,
		"domain": lead.Domain, "sender_name": "-", "sender_first_name": "-", "sender_email": "-", "unsubscribe_url": "-",
	}
	for k, v := range lead.Custom {
		if _, standard := vars[k]; !standard {
			vars[k] = v
		}
	}
	if err := customers.PersonVars(ctx, tx, en.LeadID, vars); err != nil {
		return nil, en, err
	}
	subject := stepSubject
	if subject == "" && en.ThreadSubject != nil {
		subject = "Re: " + strings.TrimPrefix(*en.ThreadSubject, "Re: ")
		if en.Handoff {
			// A new sender starts a new conversation.
			subject = strings.TrimPrefix(*en.ThreadSubject, "Re: ")
		}
	}
	blockMissing := func() (bool, error) {
		_, missingS := Render(subject, vars)
		_, missingB := Render(stepBody, vars)
		missing := append(missingS, missingB...)
		if len(missing) == 0 {
			return false, nil
		}
		reason := "missing_variable: " + strings.Join(missing, ", ")
		if err := e.recordBlocked(ctx, tx, en, lead.Email, reason); err != nil {
			return true, err
		}
		_, _, err := end("blocked", reason)
		return true, err
	}
	if blocked, err := blockMissing(); blocked || err != nil {
		return nil, en, err
	}

	// 5. Mailbox: the one already writing to this lead, or the least used
	// mailbox allocated to the sequence.
	mailboxID, v, err := e.pickMailbox(ctx, tx, en, now, skipWait)
	if err != nil {
		return nil, en, err
	}
	if !v.Allow {
		return defer_(v.DeferUntil, v.Reason)
	}
	acct, err := e.Mail.Load(ctx, tx, en.WorkspaceID, mailboxID)
	if ce := (*mailbox.ConnError)(nil); errors.As(err, &ce) {
		// An OAuth mailbox could not get an access token: take it out of
		// rotation until it is fixed and re-verified, and try again later.
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET status = 'error', status_reason = $2, smtp_ok = false, updated_at = now() WHERE id = $1 AND status = 'active'`,
			mailboxID, err.Error()); err != nil {
			return nil, en, err
		}
		return defer_(now.Add(15*time.Minute), "mailbox_auth_failed")
	}
	if err != nil {
		return nil, en, err
	}

	// 6. Render with the sender's details. Links use the workspace's own
	// tracking domain when it has one.
	var trackClicks, unsubHeader bool
	if err := tx.QueryRow(ctx, `SELECT track_clicks, unsubscribe_header FROM sequences WHERE id = $1`, en.SequenceID).Scan(&trackClicks, &unsubHeader); err != nil {
		return nil, en, err
	}
	base, err := tracking.BaseURL(ctx, tx, en.WorkspaceID, e.PublicURL)
	if err != nil {
		return nil, en, err
	}
	unsub := e.unsubscribeURLAt(base, en.ID)
	vars["sender_name"], vars["sender_first_name"], vars["sender_email"], vars["unsubscribe_url"] =
		acct.DisplayName, firstWord(acct.DisplayName), acct.Email, unsub
	if blocked, err := blockMissing(); blocked || err != nil {
		return nil, en, err
	}
	subject, _ = Render(subject, vars)
	body, _ := Render(stepBody, vars)
	if body, err = tagOwnLinks(ctx, tx, en, body, base); err != nil {
		return nil, en, err
	}
	if trackClicks {
		if body, err = tracking.Rewrite(ctx, tx, en.WorkspaceID, en.ID, en.SequenceID, en.NextStep, body, base, unsub); err != nil {
			return nil, en, err
		}
	}
	headerUnsub := unsub
	if !unsubHeader {
		headerUnsub = ""
	}
	if strings.TrimSpace(subject) == "" {
		return end("failed", "the first step has no subject")
	}

	email := Email{
		FromName: acct.DisplayName, From: acct.Email, To: lead.Email, Subject: subject, Body: body,
		MessageID:      newID() + "@" + acct.Email[strings.LastIndexByte(acct.Email, '@')+1:],
		UnsubscribeURL: headerUnsub, Date: now,
	}
	if en.LastMessageID != nil && stepSubject == "" {
		email.InReplyTo, email.References = *en.LastMessageID, en.References
	}

	if msgRowID == "" {
		err = tx.QueryRow(ctx, `
			INSERT INTO messages (workspace_id, enrollment_id, sequence_id, step_position, lead_id, mailbox_id,
				from_email, to_email, subject, body, message_id, status, attempts, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'sending', 1, $12) RETURNING id`,
			en.WorkspaceID, en.ID, en.SequenceID, en.NextStep, en.LeadID, mailboxID,
			email.From, email.To, email.Subject, email.Body, email.MessageID, now).Scan(&msgRowID)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE messages SET mailbox_id = $2, from_email = $3, subject = $4, body = $5, message_id = $6,
				status = 'sending', error = NULL, attempts = attempts + 1, created_at = $7
			WHERE id = $1`, msgRowID, mailboxID, email.From, email.Subject, email.Body, email.MessageID, now)
	}
	if err != nil {
		return nil, en, err
	}
	if _, err := tx.Exec(ctx, `UPDATE enrollments SET mailbox_id = $2, status_reason = NULL, updated_at = now() WHERE id = $1`, en.ID, mailboxID); err != nil {
		return nil, en, err
	}
	var nextDelay *int
	if err := tx.QueryRow(ctx, `SELECT delay_days FROM sequence_steps WHERE sequence_id = $1 AND position = $2`,
		en.SequenceID, en.NextStep+1).Scan(&nextDelay); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, en, err
	}
	return &prepared{messageID: msgRowID, email: email, account: acct, attempts: attempts + 1, nextDelay: nextDelay}, en, nil
}

// pickMailbox returns a mailbox that may send now, holding its row lock so
// concurrent sends see this one's reservation. skipGap leaves out the
// pause between one mailbox's sends (a test workspace's send-now).
func (e *Engine) pickMailbox(ctx context.Context, tx pgx.Tx, en enrollment, now time.Time, skipGap bool) (string, policy.Verdict, error) {
	state := func(id string, lock bool) (policy.Mailbox, bool, error) {
		var m policy.Mailbox
		var gap int
		var last *time.Time
		var ws, domain string
		var domainLimit, serverLimit *int
		var serverID *string
		wait := ""
		if !lock {
			wait = " SKIP LOCKED"
		}
		err := tx.QueryRow(ctx, `
			SELECT m.status, m.lifecycle_stage, m.daily_limit, m.min_gap_seconds, m.workspace_id, m.domain,
			       COALESCE(d.status = 'paused', false), d.daily_limit,
			       m.server_id::text, COALESCE(sv.status = 'paused', false), sv.daily_limit
			FROM mailboxes m
			LEFT JOIN sending_domains d ON d.workspace_id = m.workspace_id AND d.domain = m.domain
			LEFT JOIN sending_servers sv ON sv.id = m.server_id
			WHERE m.id = $1 FOR UPDATE OF m`+wait, id).Scan(&m.Status, &m.Stage, &m.DailyLimit, &gap, &ws, &domain, &m.DomainPaused, &domainLimit,
			&serverID, &m.ServerPaused, &serverLimit)
		if errors.Is(err, pgx.ErrNoRows) {
			return m, false, nil
		}
		if err != nil {
			return m, false, err
		}
		if domainLimit != nil {
			// Lock the domain too, so two mailboxes on it cannot both take
			// its last send of the day.
			var one int
			err := tx.QueryRow(ctx, `SELECT 1 FROM sending_domains WHERE workspace_id = $1 AND domain = $2 FOR UPDATE`+wait, ws, domain).Scan(&one)
			if errors.Is(err, pgx.ErrNoRows) {
				return m, false, nil
			}
			if err != nil {
				return m, false, err
			}
			m.DomainLimit = *domainLimit
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM messages g JOIN mailboxes mm ON mm.id = g.mailbox_id
				WHERE mm.workspace_id = $1 AND mm.domain = $2 AND g.status IN ('sending', 'sent', 'unknown')
				  AND g.created_at > $3::timestamptz - interval '24 hours'`, ws, domain, now).Scan(&m.DomainSentToday); err != nil {
				return m, false, err
			}
		}
		if serverID != nil && serverLimit != nil {
			// Lock the server too, for the same reason as the domain.
			var one int
			err := tx.QueryRow(ctx, `SELECT 1 FROM sending_servers WHERE id = $1 FOR UPDATE`+wait, *serverID).Scan(&one)
			if errors.Is(err, pgx.ErrNoRows) {
				return m, false, nil
			}
			if err != nil {
				return m, false, err
			}
			m.ServerLimit = *serverLimit
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM messages g JOIN mailboxes mm ON mm.id = g.mailbox_id
				WHERE mm.server_id = $1 AND g.status IN ('sending', 'sent', 'unknown')
				  AND g.created_at > $2::timestamptz - interval '24 hours'`, *serverID, now).Scan(&m.ServerSentToday); err != nil {
				return m, false, err
			}
		}
		err = tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE created_at > $2::timestamptz - interval '24 hours'), max(created_at)
			FROM messages WHERE mailbox_id = $1 AND status IN ('sending', 'sent', 'unknown') AND created_at > $2::timestamptz - interval '24 hours'`,
			id, now).Scan(&m.SentToday, &last)
		if last != nil {
			m.LastSentAt = *last
		}
		m.MinGap = time.Duration(gap) * time.Second
		if skipGap {
			m.MinGap = 0
		}
		return m, true, err
	}

	if en.MailboxID != nil {
		// Follow-ups always come from the mailbox that sent the first
		// email, wherever it is allocated now.
		m, ok, err := state(*en.MailboxID, true)
		if err != nil {
			return "", policy.Verdict{}, err
		}
		// A retired mailbox is gone for good, like a deleted one.
		if ok && m.Stage != "retired" {
			return *en.MailboxID, policy.CheckMailbox(m, now), nil
		}
	}
	if en.NextStep > 0 && !en.Handoff {
		// The mailbox that started this conversation is gone. A person
		// decides whether to cancel the rest or hand it to another mailbox.
		return "", policy.Verdict{DeferUntil: now.Add(6 * time.Hour), Reason: policy.SenderGone}, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT m.id::text FROM mailboxes m
		LEFT JOIN LATERAL (
			SELECT count(*) AS sent FROM messages g
			WHERE g.mailbox_id = m.id AND g.status IN ('sending', 'sent', 'unknown') AND g.created_at > $3::timestamptz - interval '24 hours'
		) c ON true
		JOIN mailbox_assignments a ON a.mailbox_id = m.id AND a.sequence_id = $2 AND a.state = 'active' AND a.link_id IS NULL
		WHERE m.workspace_id = $1 AND m.status = 'active' AND m.has_login AND m.lifecycle_stage = 'ready'
		  -- One sending home: a mailbox someone attached to a platform campaign
		  -- waits until a person decides where it sends.
		  AND NOT EXISTS (SELECT 1 FROM mailbox_platforms p WHERE p.mailbox_id = m.id AND cardinality(p.campaign_ids) > 0)
		  AND NOT EXISTS (SELECT 1 FROM sending_domains d WHERE d.workspace_id = m.workspace_id AND d.domain = m.domain AND d.status = 'paused')
		  AND NOT EXISTS (SELECT 1 FROM sending_servers sv WHERE sv.id = m.server_id AND sv.status = 'paused')
		ORDER BY c.sent::float / m.daily_limit, random()
		LIMIT 50`, en.WorkspaceID, en.SequenceID, now)
	if err != nil {
		return "", policy.Verdict{}, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", policy.Verdict{}, err
	}
	best := policy.Verdict{DeferUntil: now.Add(15 * time.Minute), Reason: policy.NoMailbox}
	if len(ids) == 0 {
		// Say so when the sequence has mailboxes but their domain or server
		// is paused, rather than that it has none.
		var reason *string
		if err := tx.QueryRow(ctx, `
			SELECT CASE
				WHEN EXISTS (SELECT 1 FROM mailboxes m
				             JOIN mailbox_assignments a ON a.mailbox_id = m.id AND a.sequence_id = $2 AND a.state = 'active' AND a.link_id IS NULL
				             JOIN sending_domains d ON d.workspace_id = m.workspace_id AND d.domain = m.domain
				             WHERE m.workspace_id = $1 AND m.status = 'active' AND d.status = 'paused')
					THEN 'domain_paused'
				WHEN EXISTS (SELECT 1 FROM mailboxes m
				             JOIN mailbox_assignments a ON a.mailbox_id = m.id AND a.sequence_id = $2 AND a.state = 'active' AND a.link_id IS NULL
				             JOIN sending_servers sv ON sv.id = m.server_id
				             WHERE m.workspace_id = $1 AND m.status = 'active' AND sv.status = 'paused')
					THEN 'server_paused'
			END`, en.WorkspaceID, en.SequenceID).Scan(&reason); err != nil {
			return "", policy.Verdict{}, err
		}
		if reason != nil {
			best.Reason = *reason
		}
	}
	for _, id := range ids {
		m, ok, err := state(id, false)
		if err != nil {
			return "", policy.Verdict{}, err
		}
		if !ok {
			continue // another send holds it
		}
		// The allocator moves mailboxes with their row locked; check the
		// mailbox is still this sequence's now that this send holds it.
		var still bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailbox_assignments WHERE mailbox_id = $1 AND sequence_id = $2 AND state = 'active' AND link_id IS NULL)`,
			id, en.SequenceID).Scan(&still); err != nil {
			return "", policy.Verdict{}, err
		}
		if !still {
			continue
		}
		v := policy.CheckMailbox(m, now)
		if v.Allow {
			return id, v, nil
		}
		if best.Reason == policy.NoMailbox || v.DeferUntil.Before(best.DeferUntil) {
			best = v
		}
	}
	if len(ids) > 0 && best.Reason == policy.NoMailbox {
		best = policy.Verdict{DeferUntil: now.Add(time.Minute), Reason: policy.MailboxGap}
	}
	return "", best, nil
}

// finish records the outcome of an SMTP send.
func (e *Engine) finish(ctx context.Context, tx pgx.Tx, en enrollment, p *prepared, sendErr error) error {
	now := e.now()
	if sendErr == nil {
		if _, err := tx.Exec(ctx, `UPDATE messages SET status = 'sent', sent_at = $2, error = NULL WHERE id = $1`, p.messageID, now); err != nil {
			return err
		}
		return e.advance(ctx, tx, en, p, "")
	}

	errText := sendErr.Error()
	if _, err := tx.Exec(ctx, `UPDATE messages SET status = 'failed', error = $2 WHERE id = $1`, p.messageID, errText); err != nil {
		return err
	}
	end := func(status, reason string) error {
		_, err := tx.Exec(ctx, `UPDATE enrollments SET status = $2, status_reason = $3, next_send_at = NULL, updated_at = now() WHERE id = $1`,
			en.ID, status, reason)
		return err
	}
	switch {
	case mailbox.IsHardBounce(sendErr):
		if _, err := leads.AddSuppressions(ctx, tx, en.WorkspaceID, "bounced", "smtp", errText,
			[]leads.Suppression{{Kind: "email", Value: strings.ToLower(p.email.To)}}); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: en.WorkspaceID, ActorType: "system", Action: "lead.bounced",
			TargetType: "lead", TargetID: en.LeadID, Data: map[string]any{"email": p.email.To, "error": errText}}); err != nil {
			return err
		}
		return end("bounced", errText)
	case p.attempts >= maxAttempts:
		return end("failed", errText)
	}
	var ce *mailbox.ConnError
	if errors.As(sendErr, &ce) && ce.Stage == mailbox.StageAuth {
		// The mailbox can no longer sign in: stop using it until it is
		// fixed and re-verified.
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET status = 'error', status_reason = $2, smtp_ok = false, updated_at = now() WHERE id = $1 AND status = 'active'`,
			p.account.ID, errText); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE enrollments SET next_send_at = $2, status_reason = $3, updated_at = now() WHERE id = $1`,
		en.ID, now.Add(time.Duration(p.attempts)*15*time.Minute), "send_failed: "+errText)
	return err
}

// advance moves an enrollment past its current step.
func (e *Engine) advance(ctx context.Context, tx pgx.Tx, en enrollment, p *prepared, _ string) error {
	if p == nil {
		// Outcome unknown: move on without new threading data.
		var delay *int
		if err := tx.QueryRow(ctx, `SELECT delay_days FROM sequence_steps WHERE sequence_id = $1 AND position = $2`,
			en.SequenceID, en.NextStep+1).Scan(&delay); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return e.schedule(ctx, tx, en.ID, delay, nil, nil, "")
	}
	refs := strings.TrimSpace(en.References + " <" + p.email.MessageID + ">")
	subject := p.email.Subject
	if en.ThreadSubject != nil {
		subject = *en.ThreadSubject
	}
	return e.schedule(ctx, tx, en.ID, p.nextDelay, &subject, &p.email.MessageID, refs)
}

func (e *Engine) schedule(ctx context.Context, tx pgx.Tx, id string, nextDelay *int, subject, msgID *string, refs string) error {
	if nextDelay == nil {
		_, err := tx.Exec(ctx, `
			UPDATE enrollments SET next_step = next_step + 1, status = 'completed', next_send_at = NULL, status_reason = NULL,
				thread_subject = COALESCE($2, thread_subject), last_message_id = COALESCE($3, last_message_id),
				references_ids = CASE WHEN $4 = '' THEN references_ids ELSE $4 END, updated_at = now()
			WHERE id = $1`, id, subject, msgID, refs)
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE enrollments SET next_step = next_step + 1, next_send_at = $5, status_reason = NULL,
			thread_subject = COALESCE($2, thread_subject), last_message_id = COALESCE($3, last_message_id),
			references_ids = CASE WHEN $4 = '' THEN references_ids ELSE $4 END, updated_at = now()
		WHERE id = $1`, id, subject, msgID, refs, e.now().Add(time.Duration(*nextDelay)*24*time.Hour))
	return err
}

func (e *Engine) recordBlocked(ctx context.Context, tx pgx.Tx, en enrollment, to, reason string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO messages (workspace_id, enrollment_id, sequence_id, step_position, lead_id, from_email, to_email,
			subject, body, message_id, status, error)
		VALUES ($1, $2, $3, $4, $5, '', $6, '', '', '', 'blocked', $7)`,
		en.WorkspaceID, en.ID, en.SequenceID, en.NextStep, en.LeadID, to, reason)
	if err != nil {
		return err
	}
	// The reason's code only: what follows a colon can name a brand or
	// quote a suppression note.
	code, _, _ := strings.Cut(strings.Split(reason, ", ")[0], ":")
	props := map[string]any{"reason": code, "sequence_id": en.SequenceID, "enrollment_id": en.ID, "step": en.NextStep}
	if en.BrandID != nil {
		props["brand_id"] = *en.BrandID
	}
	track.Emit(ctx, track.Event{Name: "message.blocked", ActorType: "system", WorkspaceID: en.WorkspaceID, Surface: "job", Props: props})
	return nil
}

// UnsubscribeURL is the one-click unsubscribe link for an enrollment.
func (e *Engine) UnsubscribeURL(enrollmentID string) string {
	return e.unsubscribeURLAt(strings.TrimRight(e.PublicURL, "/"), enrollmentID)
}

func (e *Engine) unsubscribeURLAt(base, enrollmentID string) string {
	return base + "/u/" + enrollmentID + "." + e.Box.Sign("unsubscribe", enrollmentID)
}

// ParseUnsubscribeToken returns the enrollment id from a link token, or
// false if the signature does not match.
func (e *Engine) ParseUnsubscribeToken(token string) (string, bool) {
	id, sig, ok := strings.Cut(token, ".")
	if !ok || len(id) != 36 || !e.Box.Verify("unsubscribe", id, sig) {
		return "", false
	}
	return id, true
}

// Unsubscribe suppresses the enrollment's lead and stops every active
// enrollment for that address in the workspace. It is idempotent.
func (e *Engine) Unsubscribe(ctx context.Context, enrollmentID string) error {
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		var ws, leadID, email string
		err := tx.QueryRow(ctx, `
			SELECT en.workspace_id, en.lead_id, l.email FROM enrollments en JOIN leads l ON l.id = en.lead_id
			WHERE en.id = $1`, enrollmentID).Scan(&ws, &leadID, &email)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already gone
		}
		if err != nil {
			return err
		}
		added, err := leads.AddSuppressions(ctx, tx, ws, "unsubscribed", "unsubscribe_link", "",
			[]leads.Suppression{{Kind: "email", Value: strings.ToLower(email)}})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE enrollments en SET status = 'unsubscribed', next_send_at = NULL, status_reason = 'unsubscribe link', updated_at = now()
			FROM leads l WHERE l.id = en.lead_id AND en.workspace_id = $1 AND lower(l.email) = lower($2) AND en.status = 'active'`,
			ws, email); err != nil {
			return err
		}
		if added == 0 {
			return nil
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "lead.unsubscribed",
			TargetType: "lead", TargetID: leadID, Data: map[string]any{"email": email, "enrollment_id": enrollmentID}})
	})
}

func clock(t pgtype.Time) time.Duration { return time.Duration(t.Microseconds) * time.Microsecond }

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func newID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	return fmt.Sprintf("%x", b)
}

// ErrBlocked is a send the policy refused.
type ErrBlocked struct{ Reason string }

func (e ErrBlocked) Error() string { return "blocked by the send policy: " + e.Reason }

// TestSend sends one message from a mailbox now, to check delivery. It goes
// through the same recipient and mailbox checks as a sequence (except the
// sending window) and counts towards the mailbox's daily limit.
func (e *Engine) TestSend(ctx context.Context, workspaceID, mailboxID, to, subject, body string) (string, error) {
	var email Email
	var acct *mailbox.Account
	var rowID string
	err := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		d, err := policy.CheckRecipients(ctx, tx, workspaceID, []string{to})
		if err != nil {
			return err
		}
		if !d[0].Allowed {
			return ErrBlocked{strings.Join(d[0].Reasons, ", ")}
		}
		// The workspace check is in Load; lock the row first so the count is exact.
		en := enrollment{WorkspaceID: workspaceID, MailboxID: &mailboxID}
		if acct, err = e.Mail.Load(ctx, tx, workspaceID, mailboxID); err != nil {
			return err
		}
		_, v, err := e.pickMailbox(ctx, tx, en, e.now(), false)
		if err != nil {
			return err
		}
		if !v.Allow {
			return ErrBlocked{v.Reason}
		}
		email = Email{
			FromName: acct.DisplayName, From: acct.Email, To: to, Subject: subject, Body: body, Date: e.now(),
			MessageID: newID() + "@" + acct.Email[strings.LastIndexByte(acct.Email, '@')+1:],
		}
		return tx.QueryRow(ctx, `
			INSERT INTO messages (workspace_id, mailbox_id, from_email, to_email, subject, body, message_id, status, attempts, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'sending', 1, $8) RETURNING id`,
			workspaceID, mailboxID, email.From, to, subject, body, email.MessageID, email.Date).Scan(&rowID)
	})
	if err != nil {
		return "", err
	}
	_, sendErr := e.Mail.Dialer.Send(ctx, acct.SMTP, acct.SMTPCred, email.From, []string{to}, strings.NewReader(string(email.Bytes())))
	status, errText := "sent", ""
	if sendErr != nil {
		status, errText = "failed", sendErr.Error()
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE messages SET status = $2, error = NULLIF($3, ''), sent_at = CASE WHEN $2 = 'sent' THEN $4::timestamptz END WHERE id = $1`,
		rowID, status, errText, e.now()); err != nil {
		return rowID, err
	}
	return rowID, sendErr
}
