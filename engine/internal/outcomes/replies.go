package outcomes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// CapPushReplies marks a CRM that labelled replies are written into: the
// connector implements ReplyWriter.
const CapPushReplies = "crm.push_replies"

// JobPushReply writes one reply into one CRM connection.
const JobPushReply = "outcomes.push_reply"

// PushReply is a labelled reply to write into a CRM: the contact, a note
// with the reply, and optionally a task to follow up.
type PushReply struct {
	Email, FirstName, LastName, Company string
	// Label is the reply's label, such as interested; LabelText says it
	// in words ("Interested").
	Label, LabelText string
	Sequence         string
	// Body is the reply's new text, without the quoted thread.
	Body       string
	ReceivedAt time.Time
	// URL opens the reply in Outflare.
	URL string
	// Task adds a follow-up task, due a day after the reply, for OwnerEmail
	// when the CRM has a user with that address.
	Task       bool
	OwnerEmail string
}

// PushedReply is what the CRM made.
type PushedReply struct {
	ContactID, NoteID, TaskID string
}

// ReplyWriter writes labelled replies into a CRM.
type ReplyWriter interface {
	connectors.Connector
	PushReply(ctx context.Context, c *connectors.Client, creds connectors.Creds, r PushReply) (PushedReply, error)
}

// QueueReplyPush queues a reply for every connected CRM whose push_replies
// includes its label. expand turns the configured labels and groups into
// labels.
func QueueReplyPush(ctx context.Context, q jobs.Querier, ws, inboundID, category string, expand func([]string) []string) error {
	var ids []string
	var configs []string
	if err := q.QueryRow(ctx, `SELECT COALESCE(array_agg(id::text ORDER BY created_at), '{}'), COALESCE(array_agg(config::text ORDER BY created_at), '{}')
		FROM integrations WHERE workspace_id = $1 AND status = 'connected' AND jsonb_array_length(COALESCE(config->'push_replies', '[]')) > 0`, ws).
		Scan(&ids, &configs); err != nil {
		return err
	}
	for i, id := range ids {
		var cfg SourceConfig
		if json.Unmarshal([]byte(configs[i]), &cfg) != nil || cfg.Paused {
			continue
		}
		match := false
		for _, l := range expand(cfg.PushReplies) {
			match = match || l == category
		}
		if !match {
			continue
		}
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobPushReply, WorkspaceID: &ws, IdempotencyKey: "crm-reply:" + id + ":" + inboundID,
			MaxAttempts: 5, Payload: map[string]string{"integration_id": id, "inbound_id": inboundID}}); err != nil {
			return err
		}
	}
	return nil
}

// HandlePushReply runs JobPushReply.
func (s *Syncer) HandlePushReply(ctx context.Context, job jobs.Job) error {
	var p struct {
		Integration string `json:"integration_id"`
		Inbound     string `json:"inbound_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	ev := track.Event{Name: "crm.reply_pushed", WorkspaceID: ws, ActorType: "system", Surface: "job"}
	o, err := s.Store.Open(ctx, s.Pool, ws, p.Integration)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil // disconnected meanwhile
	}
	if err != nil {
		track.Fail(ctx, s.Log, ev, "connector_"+connectors.KindOf(err), err)
		return err
	}
	ev.Props = map[string]any{"connector": o.Connector.Definition().ID}
	w, ok := o.Connector.(ReplyWriter)
	if !ok || !o.Connector.Definition().Has(CapPushReplies) {
		return nil
	}
	var cfg SourceConfig
	_ = json.Unmarshal(o.Config, &cfg)
	r := PushReply{Task: cfg.ReplyTasks}
	var slug string
	err = s.Pool.QueryRow(ctx, `
		SELECT COALESCE(l.email, i.from_email), COALESCE(l.first_name, ''), COALESCE(l.last_name, ''), COALESCE(l.company, ''),
			COALESCE(i.category, ''), COALESCE(sq.name, ''), COALESCE(i.body, ''), i.received_at,
			COALESCE(ua.email, us.email, ''), COALESCE(NULLIF(w.slug, ''), w.id::text)
		FROM inbound_messages i
		JOIN workspaces w ON w.id = i.workspace_id
		LEFT JOIN leads l ON l.id = i.lead_id
		LEFT JOIN sequences sq ON sq.id = i.sequence_id
		LEFT JOIN users ua ON ua.id = i.assigned_to
		LEFT JOIN users us ON us.actor_id = sq.created_by
		WHERE i.id = $1 AND i.workspace_id = $2`, p.Inbound, ws).
		Scan(&r.Email, &r.FirstName, &r.LastName, &r.Company, &r.Label, &r.Sequence, &r.Body, &r.ReceivedAt, &r.OwnerEmail, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Label == "" {
		return nil // the label was taken off meanwhile
	}
	r.LabelText = labelText(r.Label)
	r.URL = s.PublicURL + "/" + slug + "/inbox/" + p.Inbound
	ev.Props["category"], ev.Props["task"] = r.Label, r.Task
	out, err := w.PushReply(ctx, o.Client, o.Creds, r)
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		track.Fail(ctx, s.Log, ev, code, err)
		if k := connectors.KindOf(err); k == connectors.ErrAuth {
			_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
		}
		if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrInvalid {
			return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
		}
		return err
	}
	return audit.Record(ctx, s.Pool, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crm.reply_pushed",
		TargetType: "inbound_message", TargetID: p.Inbound,
		Data: map[string]any{"integration_id": o.ID, "connector": o.Connector.Definition().ID, "category": r.Label,
			"contact_id": out.ContactID, "note_id": out.NoteID, "task_id": nilIfEmpty(out.TaskID)},
		Props: ev.Props})
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// labelText says a reply label in words.
func labelText(l string) string {
	words := map[string]string{
		"interested": "Interested", "meeting_booked": "Meeting booked", "question": "Question",
		"referral": "Referral", "not_now": "Not now", "objection": "Objection",
		"not_interested": "Not interested", "wrong_person": "Wrong person",
		"unsubscribe": "Unsubscribe", "complaint": "Complaint",
		"out_of_office": "Out of office", "auto_reply": "Automatic reply",
	}
	if w, ok := words[l]; ok {
		return w
	}
	return l
}
