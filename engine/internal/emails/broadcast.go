package emails

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Broadcasts: one lifecycle email sent to many people on customer records
// at once. Sending freezes the template's published version, then a job
// goes through the audience in record order, a batch per transaction,
// making one email per person through Compose and Queue like any other
// send. Who chose not to get it is skipped, with why, before an email is
// made; the send-time policy check still applies to every email.

const broadcastBatch = 100

// StartBroadcast sends a draft broadcast now, or at sendAt. It reports
// false when the broadcast is not a draft.
func (s *Service) StartBroadcast(ctx context.Context, tx pgx.Tx, who Who, ws, id string, sendAt *time.Time) (bool, error) {
	var templateID string
	var published *int
	err := tx.QueryRow(ctx, `SELECT b.template_id, t.published_version FROM email_broadcasts b JOIN email_templates t ON t.id = b.template_id
		WHERE b.workspace_id = $1 AND b.id = $2 AND b.status = 'draft' FOR UPDATE OF b`, ws, id).Scan(&templateID, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if published == nil {
		return false, &ComposeError{Status: 409, Code: "template_not_published", Message: "publish the template before sending the broadcast"}
	}
	status, runAt, action := "sending", time.Time{}, "email_broadcast.started"
	if sendAt != nil && sendAt.After(time.Now()) {
		status, runAt, action = "scheduled", *sendAt, "email_broadcast.scheduled"
	}
	var sentBy *string
	if who.ActorID != "" {
		sentBy = &who.ActorID
	}
	if _, err := tx.Exec(ctx, `UPDATE email_broadcasts SET status = $3, template_version = $4, send_at = $5, sent_by = $6,
		request_id = NULLIF($7, ''), started_at = CASE WHEN $3 = 'sending' THEN now() END, updated_at = now()
		WHERE workspace_id = $1 AND id = $2`, ws, id, status, *published, sendAt, sentBy, who.RequestID); err != nil {
		return false, err
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobBroadcast, WorkspaceID: &ws, Payload: map[string]string{"broadcast_id": id},
		RunAt: runAt, IdempotencyKey: "broadcast:" + id, MaxAttempts: 10}); err != nil {
		return false, err
	}
	return true, s.record(ctx, tx, who, ws, action, "email_broadcast", id, map[string]any{"template_version": *published})
}

// CancelBroadcast stops a scheduled or sending broadcast and the emails it
// made that have not gone yet. It reports false when there is nothing to
// stop.
func (s *Service) CancelBroadcast(ctx context.Context, tx pgx.Tx, who Who, ws, id string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE email_broadcasts SET status = 'cancelled', finished_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status IN ('scheduled', 'sending')`, ws, id)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	rows, err := tx.Query(ctx, `UPDATE emails SET status = 'cancelled', status_reason = 'The broadcast was cancelled', updated_at = now()
		WHERE workspace_id = $1 AND broadcast_id = $2 AND status IN ('queued', 'scheduled') RETURNING id`, ws, id)
	if err != nil {
		return false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	for _, e := range ids {
		if err := addEvent(ctx, tx, ws, e, "cancelled", "", "The broadcast was cancelled", ""); err != nil {
			return false, err
		}
	}
	return true, s.record(ctx, tx, who, ws, "email_broadcast.cancelled", "email_broadcast", id, map[string]any{"emails_cancelled": len(ids)})
}

func (s *Service) broadcastJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		BroadcastID string `json:"broadcast_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	// A run goes through up to 20 batches, then hands on to a new job so
	// a big audience never holds one job for long.
	for range 20 {
		done, err := s.broadcastStep(ctx, in.BroadcastID)
		if err != nil || done {
			return err
		}
	}
	var ws, cursor string
	if err := s.Pool.QueryRow(ctx, `SELECT workspace_id, COALESCE(cursor_record::text, '') FROM email_broadcasts WHERE id = $1`, in.BroadcastID).
		Scan(&ws, &cursor); err != nil {
		return err
	}
	_, err := jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: JobBroadcast, WorkspaceID: &ws, Payload: map[string]string{"broadcast_id": in.BroadcastID},
		IdempotencyKey: "broadcast:" + in.BroadcastID + ":" + cursor, MaxAttempts: 10})
	return err
}

// broadcast is a broadcast as its fan-out reads it.
type broadcast struct {
	ws, status, templateID, from, audience string
	brandID, topicID, providerID           *string
	version                                *int
	recordIDs                              []string
	data                                   map[string]any
	trackOpens, trackClicks                *bool
	cursor                                 *string
	sentBy                                 *string
	topicKey, topicName                    string
	topicDefault                           bool
}

// broadcastStep makes the emails for the next batch of the audience, and
// reports whether the broadcast is finished.
func (s *Service) broadcastStep(ctx context.Context, id string) (bool, error) {
	done := false
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var b broadcast
		var data []byte
		err := tx.QueryRow(ctx, `SELECT b.workspace_id, b.status, b.template_id, b.from_address, b.audience_kind, b.brand_id, b.topic_id,
			b.provider_id, b.template_version, b.record_ids::text[], b.data, b.track_opens, b.track_clicks, b.cursor_record::text, b.sent_by,
			COALESCE(tp.key, ''), COALESCE(tp.name, ''), COALESCE(tp.subscribed_by_default, true)
			FROM email_broadcasts b LEFT JOIN email_topics tp ON tp.id = b.topic_id WHERE b.id = $1 FOR UPDATE OF b`, id).
			Scan(&b.ws, &b.status, &b.templateID, &b.from, &b.audience, &b.brandID, &b.topicID, &b.providerID, &b.version, &b.recordIDs,
				&data, &b.trackOpens, &b.trackClicks, &b.cursor, &b.sentBy, &b.topicKey, &b.topicName, &b.topicDefault)
		if errors.Is(err, pgx.ErrNoRows) {
			done = true
			return nil
		}
		if err != nil {
			return err
		}
		switch b.status {
		case "scheduled":
			if _, err := tx.Exec(ctx, `UPDATE email_broadcasts SET status = 'sending', started_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
				return err
			}
			if err := s.record(ctx, tx, System, b.ws, "email_broadcast.started", "email_broadcast", id, nil); err != nil {
				return err
			}
		case "sending":
		default:
			done = true
			return nil
		}
		_ = json.Unmarshal(data, &b.data)
		who := System
		if b.sentBy != nil {
			who.ActorID = *b.sentBy
		}

		people, err := audienceBatch(ctx, tx, b)
		if err != nil {
			return err
		}
		made, skipped := 0, 0
		for _, p := range people {
			tag, err := tx.Exec(ctx, `INSERT INTO email_broadcast_recipients (broadcast_id, workspace_id, record_id, address)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, id, b.ws, p.id, p.address)
			if err != nil {
				return err
			}
			b.cursor = &p.id
			if tag.RowsAffected() == 0 {
				continue // a retried batch: this person has theirs
			}
			why, err := broadcastSkip(ctx, tx, b, p)
			if err != nil {
				return err
			}
			var emailID string
			if why == "" {
				emailID, why, err = s.broadcastEmail(ctx, tx, who, id, b, p)
				var ce *ComposeError
				if errors.As(err, &ce) && ce.Code != "template_variables_missing" && ce.Code != "invalid_request" {
					// Nobody can get it: the provider, domain or template went.
					done = true
					return s.failBroadcast(ctx, tx, b.ws, id, ce.Message)
				}
				if err != nil {
					return err
				}
			}
			if why != "" {
				skipped++
				_, err = tx.Exec(ctx, `UPDATE email_broadcast_recipients SET skipped = $3 WHERE broadcast_id = $1 AND record_id = $2`, id, p.id, why)
			} else {
				made++
				_, err = tx.Exec(ctx, `UPDATE email_broadcast_recipients SET email_id = $3 WHERE broadcast_id = $1 AND record_id = $2`, id, p.id, emailID)
			}
			if err != nil {
				return err
			}
		}
		finished := len(people) < broadcastBatch
		if _, err := tx.Exec(ctx, `UPDATE email_broadcasts SET cursor_record = $2, recipients = recipients + $3, skipped = skipped + $4,
			status = CASE WHEN $5 THEN 'sent' ELSE status END, finished_at = CASE WHEN $5 THEN now() END, updated_at = now() WHERE id = $1`,
			id, b.cursor, made, skipped, finished); err != nil {
			return err
		}
		if !finished {
			return nil
		}
		done = true
		var total, none int
		if err := tx.QueryRow(ctx, `SELECT recipients, skipped FROM email_broadcasts WHERE id = $1`, id).Scan(&total, &none); err != nil {
			return err
		}
		return s.record(ctx, tx, System, b.ws, "email_broadcast.sent", "email_broadcast", id, map[string]any{"recipients": total, "skipped": none})
	})
	return done, err
}

func (s *Service) failBroadcast(ctx context.Context, tx pgx.Tx, ws, id, why string) error {
	if _, err := tx.Exec(ctx, `UPDATE email_broadcasts SET status = 'failed', status_reason = $2, finished_at = now(), updated_at = now() WHERE id = $1`, id, why); err != nil {
		return err
	}
	return s.record(ctx, tx, System, ws, "email_broadcast.failed", "email_broadcast", id, nil)
}

// person is one of a broadcast's audience.
type person struct {
	id, name, origin, address string
	attributes                map[string]any
}

// audienceWhere picks a broadcast's audience: people on customer records
// with a verified email address, of the broadcast's brand when it has one,
// and, for a list of records, only those. $1 is the workspace, $2 the
// brand, $3 the audience kind and $4 the record ids.
const audienceWhere = `
	FROM records r
	JOIN LATERAL (SELECT value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.verified
		AND i.valid_to IS NULL ORDER BY i.valid_from, i.id LIMIT 1) i ON true
	WHERE r.workspace_id = $1 AND r.record_type = 'person' AND r.archived_at IS NULL AND r.merged_into IS NULL
	  AND ($2::uuid IS NULL OR r.brand_id = $2::uuid)
	  AND ($3 <> 'records' OR r.id = ANY($4::uuid[]))`

// AudienceSize is how many people a broadcast's audience has now, before
// what each of them chose.
func AudienceSize(ctx context.Context, q jobs.Querier, ws string, brandID *string, kind string, recordIDs []string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*)::int`+audienceWhere, ws, brandID, kind, recordIDs).Scan(&n)
	return n, err
}

// AudienceBases counts a broadcast's audience now by contact basis
// (customers.Basis): opted_in, opted_out, customer, third_party, unknown.
func AudienceBases(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ws string, brandID *string, kind string, recordIDs []string) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT b, count(*)::int FROM (SELECT CASE
			(SELECT c.status FROM consents c WHERE c.workspace_id = r.workspace_id AND c.record_id = r.id AND c.channel = 'email'
				AND c.topic = '' AND c.purpose = 'marketing' ORDER BY c.occurred_at DESC, c.created_at DESC LIMIT 1)
			WHEN 'granted' THEN 'opted_in' WHEN 'withdrawn' THEN 'opted_out'
			ELSE CASE r.origin WHEN 'first_party' THEN 'customer' WHEN 'prospect' THEN 'third_party' ELSE 'unknown' END
		END AS b`+audienceWhere+`) t GROUP BY b`, ws, brandID, kind, recordIDs)
	if err != nil {
		return nil, err
	}
	out := map[string]int{customers.BasisOptedIn: 0, customers.BasisOptedOut: 0, customers.BasisCustomer: 0, customers.BasisThirdParty: 0, customers.BasisUnknown: 0}
	defer rows.Close()
	for rows.Next() {
		var b string
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			return nil, err
		}
		out[b] = n
	}
	return out, rows.Err()
}

// audienceBatch is the next people after the cursor.
func audienceBatch(ctx context.Context, tx pgx.Tx, b broadcast) ([]person, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text, r.name, r.origin, i.value, r.attributes`+audienceWhere+`
		AND ($5::uuid IS NULL OR r.id > $5::uuid) ORDER BY r.id LIMIT $6`, b.ws, b.brandID, b.audience, b.recordIDs, b.cursor, broadcastBatch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (person, error) {
		var p person
		var attrs []byte
		err := row.Scan(&p.id, &p.name, &p.origin, &p.address, &attrs)
		_ = json.Unmarshal(attrs, &p.attributes)
		return p, err
	})
}

// broadcastSkip is why a person does not get a broadcast, or "".
func broadcastSkip(ctx context.Context, tx pgx.Tx, b broadcast, p person) (string, error) {
	return skipFor(ctx, tx, b.ws, p.id, p.origin, b.topicKey, b.topicName, b.topicDefault)
}

// skipFor is why a person's choices keep a marketing email of a topic
// (key "" for none) from them, or "": what they chose for the topic or
// for all marketing email, and then their contact basis (see
// customers.MayMarket): a third-party lead only with their own consent,
// and anyone else who never agreed only when the workspace allows it.
func skipFor(ctx context.Context, q jobs.Querier, ws, recordID, origin, key, name string, byDefault bool) (string, error) {
	status, _, err := customers.Consent(ctx, q, ws, recordID, "email", key, "marketing")
	if err != nil {
		return "", err
	}
	if why := choiceBlock(status, key, name, byDefault); why != "" {
		return why, nil
	}
	return basisBlock(ctx, q, ws, recordID, origin, status == customers.Granted)
}

// basisBlock is why a person's contact basis keeps marketing email from
// them, or "". recordID is "" for someone with no customer record, whose
// basis is unknown. consented says they agreed to the email's topic.
func basisBlock(ctx context.Context, q jobs.Querier, ws, recordID, origin string, consented bool) (string, error) {
	all := ""
	if recordID != "" && !consented {
		var err error
		if all, _, err = customers.Consent(ctx, q, ws, recordID, "email", "", "marketing"); err != nil {
			return "", err
		}
	}
	rule, err := customers.MarketingRule(ctx, q, ws)
	if err != nil {
		return "", err
	}
	if ok, why := customers.MayMarket(customers.Basis(origin, all), rule, consented); !ok {
		return why, nil
	}
	return "", nil
}

// SkipReason is why a person on a customer record should not get a
// lifecycle email of the topic (nil for none), or "".
func SkipReason(ctx context.Context, q jobs.Querier, ws, recordID string, topicID *string) (string, error) {
	var origin string
	if err := q.QueryRow(ctx, `SELECT origin FROM records WHERE workspace_id = $1 AND id = $2`, ws, recordID).Scan(&origin); err != nil {
		return "", err
	}
	key, name, byDefault := "", "", true
	if topicID != nil {
		err := q.QueryRow(ctx, `SELECT key, name, subscribed_by_default FROM email_topics WHERE workspace_id = $1 AND id = $2`, ws, *topicID).
			Scan(&key, &name, &byDefault)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	return skipFor(ctx, q, ws, recordID, origin, key, name, byDefault)
}

// PersonData is what templates see of a person, under person.
func PersonData(recordID, name, address string, attributes map[string]any) map[string]any {
	them := map[string]any{}
	for k, v := range attributes {
		them[k] = v
	}
	them["id"], them["email"] = recordID, address
	if name != "" {
		them["name"] = name
	}
	return them
}

// broadcastEmail makes and queues one person's email. A problem with just
// this person's content is returned as why it was skipped.
func (s *Service) broadcastEmail(ctx context.Context, tx pgx.Tx, who Who, id string, b broadcast, p person) (string, string, error) {
	data := map[string]any{}
	for k, v := range b.data {
		data[k] = v
	}
	data["person"] = PersonData(p.id, p.name, p.address, p.attributes)
	d := Draft{NewEmail: NewEmail{WorkspaceID: b.ws, BrandID: b.brandID, From: b.from, To: []string{p.address}, Purpose: "lifecycle",
		ProviderID: deref(b.providerID), TopicID: b.topicID, Trigger: "broadcast", TriggerRef: id, BroadcastID: id, Surface: "job"},
		TemplateID: b.templateID, Version: b.version, Data: data, TopicSet: true, TrackOpens: b.trackOpens, TrackClicks: b.trackClicks}
	e, err := s.Compose(ctx, tx, d)
	var ce *ComposeError
	if errors.As(err, &ce) && (ce.Code == "template_variables_missing" || ce.Code == "invalid_request") {
		return "", strings.TrimSuffix(ce.Message, "."), nil
	}
	if err != nil {
		return "", "", err
	}
	emailID, err := s.Queue(ctx, tx, who, e)
	return emailID, "", err
}
