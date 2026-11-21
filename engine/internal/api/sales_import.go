package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crmmail"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// salesSourceNative is the source of a Sales conversation Outflare opened
// itself from a reply it read from a connected sequencer.
const salesSourceNative = "outflare"

// salesImport is a source conversation to bring into Sales: from an
// outside importer (POST .../sales/bridge) or from a reply Outflare read
// itself (salesReplyJob).
type salesImport struct {
	Source, SourceID        string
	RecordID, IntegrationID string
	// SenderID and ReplyID are the sequencer's ids for the mailbox the
	// reply arrived at and for the reply.
	SenderID, ReplyID string
	Messages          []salesMessage
	Attribution       json.RawMessage
	// SamePerson adds to the conversation the person already has on this
	// connection, whatever opened it, before opening another. Outflare's
	// own replies set it: every reply of a person has its own id there.
	SamePerson bool
}

// salesReplyHash names a sequencer reply across sources: the connection
// and the sequencer's id for the reply.
func salesReplyHash(integration, reply string) string {
	return salesHash("reply:" + integration + ":" + reply)
}

// importSalesConversation opens the Sales conversation of a source
// conversation, or adds its new messages to the one that exists. Every
// path that brings a conversation in goes through it, so each writes the
// same rows, the same history on the person and the same audit actions
// (sales.lead_arrived once, sales.message_received per new message).
//
// The conversation is, in order: the one with this source and source id;
// one from another source that holds the same sequencer reply (so a reply
// that arrives both through an importer and through Outflare's own read
// has one conversation); with SamePerson, the person's conversation on
// this connection; else a new one. It reports whether the conversation is
// new and the direction of each message it added.
func (s *Server) importSalesConversation(ctx context.Context, tx pgx.Tx, a *access, in salesImport) (salesLead, bool, []string, error) {
	var l salesLead
	// An importer keeps the record id it was given. When that person has
	// since been merged into another, the conversation moved with them, so
	// the import goes on with the record they became instead of being
	// refused on every later send.
	recordID, err := mergedRecord(ctx, tx, a, in.RecordID)
	if err != nil {
		return l, false, nil, err
	}
	in.RecordID = recordID
	rec, err := lockRecord(ctx, tx, a, in.RecordID)
	if err != nil {
		return l, false, nil, err
	}
	if rec.Type != "person" {
		return l, false, nil, errInvalid("Sales conversation must reference a person CRM record")
	}
	i, err := s.integrations.Get(ctx, tx, a.WorkspaceID, in.IntegrationID)
	if err != nil {
		return l, false, nil, err
	}
	if i.Connector != "emailbison" || i.BrandID != nil && *i.BrandID != a.Brand || len(i.BrandIDs) > 0 && !slices.Contains(i.BrandIDs, a.Brand) {
		return l, false, nil, errInvalid("choose this brand's original EmailBison connection")
	}
	provider := "emailbison:" + in.IntegrationID
	replyHash := salesReplyHash(in.IntegrationID, in.ReplyID)
	var id string
	fresh := false
	err = tx.QueryRow(ctx, `SELECT id::text FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND source=$3 AND source_id=$4 FOR UPDATE`, a.WorkspaceID, a.Brand, in.Source, in.SourceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// The same sequencer reply from another source: the conversation's
		// own reply, or a message already kept in one.
		err = tx.QueryRow(ctx, `SELECT l.id::text FROM sales_leads l WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.integration_id=$3 AND l.source<>$4 AND l.id IN (
			SELECT o.id FROM sales_leads o WHERE o.workspace_id=$1 AND o.brand_id=$2 AND o.reply_hash=$5
			UNION
			SELECT m.lead_id FROM sales_message_copies c JOIN sales_messages m ON m.id=c.message_id AND m.workspace_id=c.workspace_id AND m.brand_id=c.brand_id WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.provider_hash=$6)
			ORDER BY l.created_at,l.id LIMIT 1 FOR UPDATE OF l`, a.WorkspaceID, a.Brand, in.IntegrationID, in.Source, replyHash, salesHash(provider+":"+in.ReplyID)).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) && in.SamePerson {
		err = tx.QueryRow(ctx, `SELECT id::text FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND record_id=$3 AND integration_id=$4 ORDER BY (state='archived'),updated_at DESC,id LIMIT 1 FOR UPDATE`, a.WorkspaceID, a.Brand, in.RecordID, in.IntegrationID).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		id = newID()
		var tag interface{ RowsAffected() int64 }
		tag, err = tx.Exec(ctx, `INSERT INTO sales_leads(id,workspace_id,brand_id,record_id,source,source_id,integration_id,source_sealed,reply_hash)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)ON CONFLICT(workspace_id,brand_id,source,source_id)DO NOTHING`, id, a.WorkspaceID, a.Brand, in.RecordID, in.Source, in.SourceID, in.IntegrationID, s.salesSeal(id, salesSource{SenderID: in.SenderID, ReplyID: in.ReplyID, Attribution: in.Attribution}), replyHash)
		if err != nil {
			return l, false, nil, err
		}
		fresh = tag.RowsAffected() > 0
		err = tx.QueryRow(ctx, `SELECT id::text FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND source=$3 AND source_id=$4 FOR UPDATE`, a.WorkspaceID, a.Brand, in.Source, in.SourceID).Scan(&id)
	}
	if err != nil {
		return l, false, nil, err
	}
	l, err = s.salesLoad(ctx, tx, a, id, false)
	if err != nil {
		return l, false, nil, err
	}
	if l.RecordID != in.RecordID || deref(l.IntegrationID) != in.IntegrationID {
		return l, false, nil, errInvalid("source conversation already maps to another record or connection")
	}
	var added []string
	for _, msg := range in.Messages {
		_, stored, e := s.storeSalesMessage(ctx, tx, a, l, msg, provider)
		if e != nil {
			return l, false, nil, e
		}
		if stored {
			added = append(added, msg.Direction)
			if e = s.salesEvent(ctx, tx, a, l, "bridge", "message_received", nil, nil); e != nil {
				return l, false, nil, e
			}
		}
	}
	if fresh {
		if err = s.salesEvent(ctx, tx, a, l, "bridge", "lead_arrived", nil, map[string]string{"source": in.Source}); err != nil {
			return l, false, nil, err
		}
	}
	l, err = s.salesLoad(ctx, tx, a, id, false)
	return l, fresh, added, err
}

// mergedRecord follows a merged record to the one it became, through at
// most ten merges. A record that was never merged, or isn't there, comes
// back as it was given: lockRecord then decides.
func mergedRecord(ctx context.Context, tx pgx.Tx, a *access, id string) (string, error) {
	if !validUUID(id) {
		return id, nil
	}
	for hop := 0; hop < 10; hop++ {
		var into *string
		err := tx.QueryRow(ctx, `SELECT merged_into::text FROM records WHERE id=$1 AND workspace_id=$2 AND deleted_at IS NULL`, id, a.WorkspaceID).Scan(&into)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && into == nil) {
			return id, nil
		}
		if err != nil {
			return id, err
		}
		id = *into
	}
	return id, nil
}

// salesProviderMessage is a message of a sequencer's thread as Sales keeps
// it. Its text comes from the HTML when the sequencer has no plain text.
func salesProviderMessage(reply connectors.Reply) (salesMessage, error) {
	msg := salesMessage{Direction: reply.Direction, From: reply.FromEmail, To: reply.To, CC: reply.CC, Subject: reply.Subject, Body: reply.Body, MessageID: reply.MessageID, InReplyTo: reply.InReplyTo, References: reply.References, OccurredAt: reply.ReceivedAt, ProviderID: reply.ExternalID}
	if msg.Body == "" && reply.HTML != "" {
		parsed, err := crmmail.Parse("From: capture@outflare.invalid\r\nTo: archive@outflare.invalid\r\nContent-Type: text/html; charset=utf-8\r\n\r\n"+reply.HTML, "capture@outflare.invalid")
		if err != nil {
			return msg, err
		}
		msg.Body = parsed.Body
	}
	if msg.MessageID != "" {
		msg.MessageID = "<" + strings.Trim(msg.MessageID, "<> ") + ">"
	}
	return msg, nil
}

// salesReplyJob opens or updates the Sales conversation of a hot reply
// Outflare read from a connected sequencer (inbox.QueueSalesReply), with
// the thread the sequencer holds for it. Nothing here asks an outside
// tool. A reply that no longer qualifies when the job runs (relabelled,
// the person deleted, the campaign unlinked) is left as an Inbox message;
// running it again adds nothing.
func (s *Server) salesReplyJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		InboundID string `json:"inbound_id"`
	}
	if json.Unmarshal(job.Payload, &p) != nil || job.WorkspaceID == nil || !validUUID(p.InboundID) {
		return jobs.ErrPermanent
	}
	ws := *job.WorkspaceID
	props := map[string]any{"inbound_id": p.InboundID}
	fail := func(code string, err error, permanent bool) error {
		track.Fail(ctx, s.log, track.Event{Name: "sales.reply_import_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: props}, code, err)
		if permanent {
			return errors.Join(jobs.ErrPermanent, err)
		}
		return err
	}
	reply, err := inbox.SalesReplyOf(ctx, s.pool, ws, p.InboundID)
	if err != nil {
		return fail("internal", err, false)
	}
	if reply == nil {
		return nil
	}
	props["brand_id"], props["integration_id"], props["connector"] = reply.BrandID, reply.IntegrationID, reply.Source
	opened, err := s.integrations.Open(ctx, s.pool, ws, reply.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil // the connection was removed since
	}
	if err != nil {
		return fail("internal", err, false)
	}
	source, ok := opened.Connector.(connectors.ReplyThreadSource)
	if !ok || opened.Connector.Definition().ID != reply.Source || opened.BrandID != nil && *opened.BrandID != reply.BrandID || len(opened.BrandIDs) > 0 && !slices.Contains(opened.BrandIDs, reply.BrandID) {
		return nil // not a connection of the person's brand
	}
	thread, err := source.ReadReplyThread(ctx, opened.Client, opened.Creds, reply.ExternalID)
	if err != nil {
		props["kind"] = connectors.KindOf(err)
		return fail("connector_unavailable", err, false)
	}
	sender, campaign := reply.SenderExternalID, ""
	for _, r := range thread {
		if r.ExternalID != reply.ExternalID {
			continue
		}
		campaign = r.CampaignExternalID
		if r.MailboxExternalID != "" && r.MailboxExternalID != "null" {
			sender = r.MailboxExternalID
		}
	}
	// When the sequencer names the reply's campaign, it must be one
	// Outflare added the person to.
	if campaign != "" && !slices.Contains(reply.Campaigns, campaign) {
		return nil
	}
	if sender == "" {
		return fail("invalid_request", errors.New("the sequencer did not say which mailbox the reply arrived at"), true)
	}
	messages := []salesMessage{}
	for _, r := range thread {
		own := r.ExternalID == reply.ExternalID
		if !own && r.MailboxExternalID != "" && r.MailboxExternalID != sender {
			continue
		}
		msg, e := salesProviderMessage(r)
		if own {
			// What the thread leaves out, the Inbox copy of the reply has.
			if msg.From == "" {
				msg.From = reply.FromEmail
			}
			if len(msg.To) == 0 && reply.MailboxEmail != "" {
				msg.To = []string{reply.MailboxEmail}
			}
			if msg.Subject == "" {
				msg.Subject = reply.Subject
			}
			if msg.Body == "" {
				msg.Body, e = reply.Body, nil
			}
			if msg.MessageID == "" && reply.MessageID != "" {
				msg.MessageID = "<" + strings.Trim(reply.MessageID, "<> ") + ">"
			}
			msg.Direction, msg.OccurredAt = "incoming", reply.ReceivedAt
		}
		if msg.To == nil {
			msg.To = []string{}
		}
		if msg.CC == nil {
			msg.CC = []string{}
		}
		if e != nil || msg.ProviderID == "" || !validSalesMessage(msg) {
			if own {
				return fail("invalid_request", errors.New("the sequencer's copy of the reply is incomplete"), true)
			}
			continue // an older message the sequencer holds only in part
		}
		messages = append(messages, msg)
	}
	// The newest messages, like an importer's limit; the reply is among them
	// or added back.
	if len(messages) > 100 {
		slices.SortStableFunc(messages, func(x, y salesMessage) int { return x.OccurredAt.Compare(y.OccurredAt) })
		kept := messages[len(messages)-100:]
		if !slices.ContainsFunc(kept, func(m salesMessage) bool { return m.ProviderID == reply.ExternalID }) {
			own := messages[slices.IndexFunc(messages, func(m salesMessage) bool { return m.ProviderID == reply.ExternalID })]
			kept = append([]salesMessage{own}, kept[1:]...)
		}
		messages = kept
	}
	a := &access{WorkspaceID: ws, Brand: reply.BrandID}
	a.ActorType = "system"
	var added []string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		added = nil
		// The reply must still qualify when its conversation is written.
		again, e := inbox.SalesReplyOf(ctx, tx, ws, p.InboundID)
		if e != nil || again == nil || again.RecordID != reply.RecordID || again.BrandID != reply.BrandID {
			return e
		}
		_, _, added, e = s.importSalesConversation(ctx, tx, a, salesImport{Source: salesSourceNative,
			SourceID: reply.IntegrationID + ":" + reply.ExternalID, RecordID: reply.RecordID, IntegrationID: reply.IntegrationID,
			SenderID: sender, ReplyID: reply.ExternalID, Messages: messages, SamePerson: true})
		return e
	})
	var invalid errInvalid
	var conflict recordConflict
	switch {
	case err == nil:
	case errors.Is(err, errNotFound), errors.Is(err, errSharedRecord), errors.As(err, &conflict):
		return nil // the person was deleted, merged or moved since
	case errors.As(err, &invalid):
		// The reply's conversation belongs to another person or connection.
		return fail("invalid_request", err, true)
	default:
		return fail("internal", err, false)
	}
	for _, direction := range added {
		metrics.SalesMessageStored(direction)
	}
	return nil
}
