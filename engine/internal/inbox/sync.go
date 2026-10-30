package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/linkbuild"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/stories"
	"github.com/outflareai/outflare/engine/internal/support"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobSync syncs one mailbox's INBOX.
const JobSync = "mailbox.sync"

const (
	// syncEvery is how often each active mailbox is checked for new mail.
	syncEvery = 2 * time.Minute
	// batch is the most messages read in one sync; the next sync continues.
	batch = 200
	// firstSyncBack is how far back the first sync of a mailbox reads, so
	// connecting an old inbox does not import its history.
	firstSyncBack = 24 * time.Hour
	session       = 3 * time.Minute

	// MinWarmupTag is the shortest warm-up tag matched, so a short one
	// cannot hide real mail.
	MinWarmupTag = 4
)

// Syncer reads new mail from connected mailboxes.
type Syncer struct {
	Pool *pgxpool.Pool
	Mail *mailbox.Service
	Log  *slog.Logger
}

// Register adds the sync job handler to the queue.
func (s *Syncer) Register(q *jobs.Queue) { q.Handle(JobSync, s.syncJob) }

// Run queues a sync for every active mailbox that is due, until ctx ends.
func (s *Syncer) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.QueueDue(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("inbox schedule", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type syncPayload struct {
	MailboxID string `json:"mailbox_id"`
}

// QueueDue queues a sync for each active mailbox not synced recently. The
// idempotency key stops a slow sync being queued twice.
func (s *Syncer) QueueDue(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, workspace_id::text FROM mailboxes
		WHERE status = 'active' AND has_login AND (last_synced_at IS NULL OR last_synced_at < now() - $1::interval)`,
		strconv.Itoa(int(syncEvery.Seconds()))+" seconds")
	if err != nil {
		return 0, err
	}
	type due struct{ id, ws string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ws)
	})
	if err != nil {
		return 0, err
	}
	bucket := strconv.FormatInt(time.Now().Unix()/int64(syncEvery.Seconds()), 10)
	for _, d := range list {
		if err := s.queue(ctx, s.Pool, d.ws, d.id, d.id+":"+bucket); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// QueueNow queues a sync of one mailbox straight away.
func (s *Syncer) QueueNow(ctx context.Context, q jobs.Querier, workspaceID, mailboxID string) error {
	return s.queue(ctx, q, workspaceID, mailboxID, mailboxID+":now:"+strconv.FormatInt(time.Now().UnixNano(), 10))
}

func (s *Syncer) queue(ctx context.Context, q jobs.Querier, ws, id, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSync, WorkspaceID: &ws, Payload: syncPayload{id}, MaxAttempts: 1, IdempotencyKey: key})
	return err
}

func (s *Syncer) syncJob(ctx context.Context, job jobs.Job) error {
	var p syncPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	_, err := s.Sync(ctx, *job.WorkspaceID, p.MailboxID)
	return err
}

// Sync reads new messages from a mailbox's INBOX and acts on them. It
// returns how many were new. Connection problems are recorded on the
// mailbox as sync_error rather than returned, so the mailbox waits for
// its next turn instead of failing every few minutes. When its sign-in has
// stopped working for good (a revoked OAuth grant), the mailbox is also
// marked as an error, which asks the person to connect it again and stops
// the syncs until they do.
func (s *Syncer) Sync(ctx context.Context, workspaceID, mailboxID string) (int, error) {
	acct, err := s.Mail.Load(ctx, s.Pool, workspaceID, mailboxID)
	if errors.Is(err, mailbox.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		var ce *mailbox.ConnError
		if !errors.As(err, &ce) && !errors.Is(err, mailbox.ErrCredentials) {
			return 0, err
		}
		return 0, s.failed(ctx, workspaceID, mailboxID, err)
	}
	n, syncErr := s.sync(ctx, acct)
	if syncErr != nil {
		var ce *mailbox.ConnError
		var imapErr *imap.Error
		if !errors.As(syncErr, &ce) && !errors.As(syncErr, &imapErr) {
			return n, syncErr
		}
		return n, s.failed(ctx, workspaceID, acct.ID, syncErr)
	}
	_, err = s.Pool.Exec(ctx, `UPDATE mailboxes SET last_synced_at = now(), sync_error = NULL WHERE id = $1`, acct.ID)
	return n, err
}

// EventSyncFailed is a mailbox sync that could not read the inbox.
const EventSyncFailed = "mailbox.sync_failed"

// failed records a sync that could not reach the mailbox: on the mailbox
// as sync_error, and as an event. A lost sign-in also turns the mailbox to
// error, as a failed sign-in check does.
func (s *Syncer) failed(ctx context.Context, ws, id string, syncErr error) error {
	lost := mailbox.SignInLost(syncErr)
	code, props := "mailbox_unreachable", map[string]any{"mailbox_id": id, "sign_in_lost": lost}
	var ce *mailbox.ConnError
	if errors.As(syncErr, &ce) {
		code = "mailbox_" + string(ce.Stage)
		props["protocol"], props["stage"] = ce.Protocol, string(ce.Stage)
	}
	if lost {
		code = "mailbox_sign_in_lost"
	}
	track.Fail(ctx, s.Log, track.Event{Name: EventSyncFailed, WorkspaceID: ws, ActorType: "system", Surface: "job", Props: props},
		code, fmt.Errorf("inbox sync of mailbox %s: %w", id, syncErr))
	reason := syncErr.Error()
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET last_synced_at = now(), sync_error = $3 WHERE id = $1 AND workspace_id = $2`,
			id, ws, reason); err != nil || !lost {
			return err
		}
		var email string
		err := tx.QueryRow(ctx, `
			UPDATE mailboxes SET status = 'error', status_reason = $3, smtp_ok = false, imap_ok = false, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND status = 'active'
			RETURNING email`, id, ws, reason).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // paused, or already marked
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			WorkspaceID: ws, ActorType: "system", Action: "mailbox.verify_failed",
			TargetType: "mailbox", TargetID: id,
			Data: map[string]any{"email": email, "smtp_ok": false, "imap_ok": false, "reason": reason, "via": "sync"},
		})
	})
}

func (s *Syncer) sync(ctx context.Context, acct *mailbox.Account) (int, error) {
	if mailbox.Simulated(acct.IMAP) {
		return 0, nil // nothing arrives unless made with Deliver
	}
	var validity *int64
	var lastUID int64
	var created time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT imap_uidvalidity, imap_last_uid, created_at FROM mailboxes WHERE id = $1`, acct.ID).
		Scan(&validity, &lastUID, &created); err != nil {
		return 0, err
	}

	c, err := s.Mail.Dialer.IMAPSession(ctx, acct.IMAP, acct.IMAPCred, session)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	sel, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, &mailbox.ConnError{Protocol: "IMAP", Stage: mailbox.StageMailbox, Err: err}
	}
	uv := int64(sel.UIDValidity)
	criteria := &imap.SearchCriteria{}
	if validity == nil || *validity != uv {
		// New mailbox, or the server renumbered it: start again from
		// recent mail. Messages already stored are skipped by their key.
		lastUID = 0
		criteria.Since = created.Add(-firstSyncBack)
	} else {
		criteria.UID = []imap.UIDSet{{{Start: imap.UID(lastUID + 1), Stop: 0}}}
	}
	res, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return 0, &mailbox.ConnError{Protocol: "IMAP", Stage: mailbox.StageMailbox, Err: err}
	}
	var uids []imap.UID
	for _, u := range res.AllUIDs() {
		if int64(u) > lastUID { // "n:*" always matches the last message
			uids = append(uids, u)
		}
	}
	slices.Sort(uids)
	if len(uids) > batch {
		uids = uids[:batch]
	}
	if len(uids) == 0 {
		if lastUID == 0 && sel.UIDNext > 0 {
			// Nothing recent: start after what is there now.
			lastUID = int64(sel.UIDNext) - 1
		}
		_, err := s.Pool.Exec(ctx, `UPDATE mailboxes SET imap_uidvalidity = $2, imap_last_uid = $3 WHERE id = $1`, acct.ID, uv, lastUID)
		return 0, err
	}

	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: 2 << 20}}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return 0, &mailbox.ConnError{Protocol: "IMAP", Stage: mailbox.StageMailbox, Err: err}
	}
	c.Logout().Wait() //nolint:errcheck
	slices.SortFunc(msgs, func(a, b *imapclient.FetchMessageBuffer) int { return int(a.UID) - int(b.UID) })

	n := 0
	for _, m := range msgs {
		p, err := Parse(m.FindBodySection(section))
		if err != nil {
			s.Log.Warn("inbox parse", "mailbox", acct.Email, "uid", m.UID, "err", err)
			p = &Parsed{Subject: "(could not be read)"}
		}
		received := m.InternalDate
		if received.IsZero() {
			received = time.Now()
		}
		var isNew bool
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			isNew, err = s.store(ctx, tx, acct, uv, int64(m.UID), received, p)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE mailboxes SET imap_uidvalidity = $2,
					imap_last_uid = CASE WHEN imap_uidvalidity = $2 THEN GREATEST(imap_last_uid, $3) ELSE $3 END
				WHERE id = $1`,
				acct.ID, uv, int64(m.UID))
			return err
		})
		if err != nil {
			return n, err
		}
		if isNew {
			n++
		}
	}
	return n, nil
}

// match is the sent message a received one answers.
type match struct {
	messageID, enrollmentID, sequenceID, leadID *string
	to                                          string
}

// store saves one received message and acts on it. It reports false if the
// message was stored before.
func (s *Syncer) store(ctx context.Context, tx pgx.Tx, acct *mailbox.Account, uv, uid int64, received time.Time, p *Parsed) (bool, error) {
	ws := acct.WorkspaceID
	if acct.Purpose == support.PurposeSupport {
		// Customers writing to support: the support inbox, not sales.
		r, err := support.Intake(ctx, tx, support.Email{WorkspaceID: ws, BrandID: acct.BrandID, MailboxID: acct.ID,
			UIDValidity: uv, UID: uid, MessageID: p.MessageID, InReplyTo: p.InReplyTo, References: p.References,
			From: p.From, FromName: p.FromName, To: p.To, Subject: p.Subject, Body: p.Body, Received: received,
			AutoReply: p.AutoReply || p.Bounce != nil})
		return r.New, err
	}
	ids := append([]string{p.InReplyTo}, p.References...)
	if p.Bounce != nil {
		ids = append(ids, p.Bounce.MessageIDs...)
	}
	var m match
	err := tx.QueryRow(ctx, `
		SELECT id::text, enrollment_id::text, sequence_id::text, lead_id::text, to_email FROM messages
		WHERE workspace_id = $1 AND message_id = ANY($2) AND status <> 'blocked'
		ORDER BY created_at DESC LIMIT 1`, ws, ids).Scan(&m.messageID, &m.enrollmentID, &m.sequenceID, &m.leadID, &m.to)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}

	var fromOwnMailbox bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE workspace_id = $1 AND lower(email) = $2)`, ws, p.From).Scan(&fromOwnMailbox); err != nil {
		return false, err
	}

	if p.Bounce == nil && m.messageID == nil && !fromOwnMailbox {
		// A sequencer that warms up this mailbox marks its warm-up mail
		// with a tag, which the workspace keeps on that integration
		// (config.warmup_tags). Such mail is left alone, unread and where
		// it is, for the sequencer's own engagement, and kept out of the
		// inbox.
		tagged, err := warmupTagged(ctx, tx, ws, p.Subject, p.Body)
		if err != nil {
			return false, err
		}
		if tagged {
			return false, nil
		}
	}

	kind := "other"
	switch {
	case p.Bounce != nil:
		kind = "bounce"
	case fromOwnMailbox:
		// Mail between the workspace's own mailboxes (warm-up, tests).
	default:
		if m.leadID == nil {
			// Not threaded: match the sender to a lead this mailbox wrote to.
			var lead, en, seq *string
			err := tx.QueryRow(ctx, `
				SELECT l.id::text, e.id::text, e.sequence_id::text FROM leads l
				LEFT JOIN LATERAL (
					SELECT id, sequence_id FROM enrollments
					WHERE lead_id = l.id AND mailbox_id = $3 ORDER BY created_at DESC LIMIT 1
				) e ON true
				WHERE l.workspace_id = $1 AND lower(l.email) = $2`, ws, p.From, acct.ID).Scan(&lead, &en, &seq)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return false, err
			}
			m.leadID, m.enrollmentID, m.sequenceID = lead, en, seq
		}
		if m.leadID != nil {
			kind = "reply"
			if p.AutoReply {
				kind = "auto_reply"
			}
		}
	}
	complained := kind == "reply" && Complaint(p.Body)
	optOut := kind == "reply" && (complained || OptOut(p.Body))

	body := p.Body
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	var category *string
	if c := Categorize(kind, optOut, p.Body); c != "" {
		category = &c
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, message_id, in_reply_to, references_ids,
			from_email, from_name, to_email, subject, body, kind, opt_out, lead_id, enrollment_id, sequence_id,
			reply_to_message_id, received_at, category, category_source, done, category_confidence, classified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
			CASE WHEN $20::text IS NULL THEN NULL ELSE 'rules' END,
			-- Already acted on (suppressed) or nothing to act on.
			COALESCE($20::text IN ('unsubscribe', 'out_of_office', 'auto_reply'), false),
			CASE WHEN $20::text IS NULL THEN NULL ELSE 1 END,
			CASE WHEN $20::text IS NULL THEN NULL ELSE now() END)
		ON CONFLICT (mailbox_id, uidvalidity, uid) DO NOTHING
		RETURNING id`,
		ws, acct.ID, uv, uid, p.MessageID, p.InReplyTo, strings.Join(p.References, " "),
		p.From, p.FromName, p.To, p.Subject, body, kind, optOut, m.leadID, m.enrollmentID, m.sequenceID,
		m.messageID, received, category).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if category != nil {
		if err := Classified(ctx, tx, Classification{WorkspaceID: ws, InboundID: id, LeadID: m.leadID,
			SequenceID: m.sequenceID, MailboxID: acct.ID, Source: "mailbox", Category: category, By: "rules",
			Confidence: 1}); err != nil {
			return false, err
		}
	}
	if category == nil && kind == "reply" {
		if err := QueueLabel(ctx, tx, ws, id); err != nil {
			return false, err
		}
	}
	switch {
	case kind == "bounce":
		return true, s.bounced(ctx, tx, ws, id, p, m)
	case optOut:
		return true, s.optedOut(ctx, tx, ws, id, *m.leadID, p.From, complained)
	case kind == "reply":
		return true, s.replied(ctx, tx, ws, id, *m.leadID, map[string]any{
			"category": category, "sequence_id": m.sequenceID, "mailbox_id": acct.ID, "from_email": p.From, "subject": p.Subject})
	}
	return true, nil
}

// warmupTagged reports whether a message carries a warm-up tag the
// workspace keeps on one of its integrations (config.warmup_tags).
func warmupTagged(ctx context.Context, tx pgx.Tx, ws, subject, body string) (bool, error) {
	var tagged bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM integrations i,
				jsonb_array_elements_text(CASE WHEN jsonb_typeof(i.config->'warmup_tags') = 'array'
					THEN i.config->'warmup_tags' ELSE '[]' END) t
			WHERE i.workspace_id = $1 AND length(btrim(t)) >= `+strconv.Itoa(MinWarmupTag)+`
				AND (strpos($2, btrim(t)) > 0 OR strpos($3, btrim(t)) > 0))`,
		ws, subject, body).Scan(&tagged)
	return tagged, err
}

// replied ends the lead's active enrollments in sequences that stop on reply.
func (s *Syncer) replied(ctx context.Context, tx pgx.Tx, ws, inboundID, leadID string, data map[string]any) error {
	tag, err := tx.Exec(ctx, `
		UPDATE enrollments e SET status = 'replied', status_reason = NULL, next_send_at = NULL, updated_at = now()
		FROM sequences q WHERE q.id = e.sequence_id AND q.stop_on_reply
		AND e.workspace_id = $1 AND e.lead_id = $2 AND e.status = 'active'`, ws, leadID)
	if err != nil {
		return err
	}
	data["inbound_id"], data["enrollments_stopped"] = inboundID, tag.RowsAffected()
	// A reply to a link pitch moves its prospect along.
	if err := linkbuild.Replied(ctx, tx, ws, leadID, inboundID); err != nil {
		return err
	}
	// A journalist's reply to a story pitch is read for what it says.
	if err := stories.Replied(ctx, tx, ws, leadID, inboundID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "lead.replied",
		TargetType: "lead", TargetID: leadID, Data: data})
}

// optedOut suppresses a lead who asked not to be contacted and stops all
// their sequences.
func (s *Syncer) optedOut(ctx context.Context, tx pgx.Tx, ws, inboundID, leadID, email string, complained bool) error {
	return OptedOut(ctx, tx, ws, inboundID, leadID, email, complained)
}

// OptedOut suppresses a lead who asked in a reply not to be contacted (or
// complained about being contacted) and stops all their sequences. With
// no lead (a reply from a sequencer to someone Outflare doesn't hold) the
// address is still suppressed.
func OptedOut(ctx context.Context, tx pgx.Tx, ws, inboundID, leadID, email string, complained bool) error {
	reason, via := "asked in a reply", "reply"
	if complained {
		reason, via = "complained in a reply", "complaint"
	}
	// Say how the reply was read: the AI's label is a suggestion, with
	// how sure it was; rules and people are not.
	by := leads.Added{Reason: "unsubscribed", Source: "reply", Note: reason, Evidence: map[string]any{"inbound_message_id": inboundID}}
	if inboundID != "" {
		var labelledBy, source, integration *string
		var confidence *float64
		err := tx.QueryRow(ctx, `SELECT category_source, category_confidence::float8, source, integration_id::text FROM inbound_messages
			WHERE workspace_id = $1 AND id = $2`, ws, inboundID).Scan(&labelledBy, &confidence, &source, &integration)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if labelledBy != nil {
			by.Evidence["labelled_by"] = *labelledBy
			switch *labelledBy {
			case "ai", "agent":
				by.Method, by.Confidence = leads.MethodAISuggested, confidence
			case "human":
				by.Method = leads.MethodManual
			}
		}
		if source != nil && *source != "mailbox" {
			by.Evidence["sequencer"] = *source
		}
		if integration != nil {
			by.IntegrationID = *integration
		}
	}
	if _, err := leads.Suppress(ctx, tx, ws, by, []leads.Suppression{{Kind: "email", Value: email}}); err != nil {
		return err
	}
	if leadID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE enrollments SET status = 'unsubscribed', status_reason = $3, next_send_at = NULL, updated_at = now()
			WHERE workspace_id = $1 AND lead_id = $2 AND status = 'active'`, ws, leadID, reason); err != nil {
			return err
		}
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "lead.unsubscribed",
		TargetType: "lead", TargetID: leadID, Data: map[string]any{"email": email, "inbound_id": inboundID, "via": via},
		Props: map[string]any{"via": via}})
}

// bounced suppresses addresses a delivery report says failed permanently.
// Only addresses this workspace emailed are acted on, so a forged report
// cannot suppress arbitrary addresses.
func (s *Syncer) bounced(ctx context.Context, tx pgx.Tx, ws, inboundID string, p *Parsed, m match) error {
	failed := slices.Clone(p.Bounce.Failed)
	if len(failed) == 0 && m.messageID != nil && len(p.Bounce.MessageIDs) > 0 {
		// A plain-text report naming the original message and a 5xx code.
		failed = []string{strings.ToLower(m.to)}
	}
	if len(failed) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT lower(to_email), lead_id::text FROM messages
		WHERE workspace_id = $1 AND lower(to_email) = ANY($2) AND status IN ('sent', 'unknown')`, ws, failed)
	if err != nil {
		return err
	}
	type hit struct {
		email  string
		leadID *string
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (hit, error) {
		var h hit
		return h, r.Scan(&h.email, &h.leadID)
	})
	if err != nil {
		return err
	}
	for _, h := range hits {
		if _, err := leads.AddSuppressions(ctx, tx, ws, "bounced", "bounce_report", "",
			[]leads.Suppression{{Kind: "email", Value: h.email}}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE enrollments e SET status = 'bounced', status_reason = 'bounce report', next_send_at = NULL, updated_at = now()
			FROM leads l WHERE l.id = e.lead_id AND e.workspace_id = $1 AND lower(l.email) = $2 AND e.status = 'active'`, ws, h.email); err != nil {
			return err
		}
		target := ""
		if h.leadID != nil {
			target = *h.leadID
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "lead.bounced",
			TargetType: "lead", TargetID: target, Data: map[string]any{"email": h.email, "inbound_id": inboundID}}); err != nil {
			return err
		}
	}
	return nil
}
