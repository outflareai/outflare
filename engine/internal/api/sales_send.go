package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/track"
)

func (s *Server) salesReplyEnvelope(ctx context.Context, tx pgx.Tx, a *access, l salesLead, body string) (salesMessage, error) {
	messages, err := s.salesMessages(ctx, tx, a, l.ID)
	if err != nil {
		return salesMessage{}, err
	}
	var parent *salesMessage
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Status == "received" || messages[i].Status == "accepted" {
			parent = &messages[i]
			break
		}
	}
	if parent == nil || parent.MessageID == "" {
		return salesMessage{}, errInvalid("the original message headers are required before replying")
	}
	m := salesMessage{ID: newID(), Direction: "outgoing", Kind: "reply", Status: "queued", To: []string{l.Email}, CC: append([]string{}, parent.CC...), Subject: parent.Subject, Body: strings.TrimSpace(body), InReplyTo: parent.MessageID, References: strings.TrimSpace(parent.References + " " + parent.MessageID), OccurredAt: time.Now()}
	// Reply-all keeps the original Bison sender and introduced colleagues.
	// salesRecipients removes this sender and the primary To recipient before
	// the policy check covers the complete envelope.
	m.CC = append(m.CC, parent.To...)
	m.CC = append(m.CC, parent.From)
	m.MessageID = "<sales-" + m.ID + "@outflare.invalid>"
	if !strings.HasPrefix(strings.ToLower(m.Subject), "re:") {
		m.Subject = "Re: " + m.Subject
	}
	return m, nil
}
func salesRecipients(m *salesMessage, from string, add string) {
	m.From = from
	seen := map[string]bool{strings.ToLower(from): true}
	for _, v := range m.To {
		seen[strings.ToLower(v)] = true
	}
	cc := []string{}
	for _, v := range append(m.CC, add) {
		if v != "" && !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			cc = append(cc, v)
		}
	}
	m.CC = cc
}
func (s *Server) salesQueueMessage(ctx context.Context, tx pgx.Tx, a *access, l salesLead, m salesMessage, target *string) error {
	if !validSalesMessage(m) || strings.TrimSpace(m.Body) == "" {
		return errInvalid("a valid reply and original thread are required")
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND status IN ('queued','sending','uncertain'))`, a.WorkspaceID, a.Brand, l.ID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return recordConflict{"conflict", "A send is pending or needs reconciliation. Resolve it before sending another message."}
	}
	_, err := tx.Exec(ctx, `INSERT INTO sales_messages(id,workspace_id,brand_id,record_id,lead_id,sender_id,handoff_to,draft_id,kind,direction,status,content_sealed,identity_hash,occurred_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'outgoing','queued',$10,$11,$12)`, m.ID, a.WorkspaceID, a.Brand, l.RecordID, l.ID, a.UserID, target, m.DraftID, m.Kind, s.salesSeal(m.ID, m), salesHash("rfc:"+strings.Trim(m.MessageID, "<> ")), m.OccurredAt)
	if err != nil {
		return err
	}
	return s.enqueueSales(ctx, tx, a, jobSalesSend, m.ID, time.Now())
}
func (s *Server) salesQueueIntroduction(ctx context.Context, tx pgx.Tx, a *access, l salesLead, target salesMember, kind, body string) error {
	var address string
	if err := tx.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id=$1`, target.UserID).Scan(&address); err != nil {
		return err
	}
	if body == "" {
		body = "Hi " + strings.Fields(l.Name + " there")[0] + ",\n\nI'm introducing " + target.Name
		if target.Introduction != "" {
			body += ", " + target.Introduction
		}
		body += ". I've included them on CC so you can continue here."
	}
	m, err := s.salesReplyEnvelope(ctx, tx, a, l, body)
	if err != nil {
		return err
	}
	m.Kind = kind
	var from string
	if kind == "introduction" {
		var ref salesSource
		var sourceSealed []byte
		if e := tx.QueryRow(ctx, `SELECT source_sealed FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID).Scan(&sourceSealed); e != nil {
			return e
		}
		if e := s.salesOpen(sourceSealed, l.ID, &ref); e != nil {
			return e
		}
		messages, e := s.salesMessages(ctx, tx, a, l.ID)
		if e != nil {
			return e
		}
		for _, msg := range messages {
			if msg.Direction == "incoming" && msg.ProviderID == ref.ReplyID && len(msg.To) > 0 {
				from = msg.To[0]
				break
			}
		}
		if l.IntegrationID == nil {
			return errInvalid("reconnect the original EmailBison account")
		}
	} else {
		if err = tx.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id=$1`, a.UserID).Scan(&from); err != nil {
			return err
		}
	}
	salesRecipients(&m, from, address)
	return s.salesQueueMessage(ctx, tx, a, l, m, &target.UserID)
}
func (s *Server) sendSalesReply(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var in struct {
		DraftID string `json:"draft_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validUUID(in.DraftID) {
		writeError(w, r, 400, "invalid_request", "save the draft first")
		return
	}
	var messageID string
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		l, e := s.salesLoad(r.Context(), tx, a, r.PathValue("id"), true)
		if e != nil {
			return e
		}
		if deref(l.OwnerID) != a.UserID {
			return errNotFound
		}
		if !m.Connected {
			return errInvalid("connect your email in Sales → My email")
		}
		e = tx.QueryRow(r.Context(), `SELECT id::text FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND sender_id=$4 AND draft_id=$5`, a.WorkspaceID, a.Brand, l.ID, a.UserID, in.DraftID).Scan(&messageID)
		if e == nil {
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if l.State != "active" && l.State != "customer" {
			return errInvalid("reopen this lead before sending")
		}
		d, e := s.salesDraft(r.Context(), tx, a, l.ID)
		if e != nil {
			return e
		}
		if d == nil || d.ID != in.DraftID {
			return errInvalid("send the latest saved draft")
		}
		msg, e := s.salesReplyEnvelope(r.Context(), tx, a, l, d.Body)
		if e != nil {
			return e
		}
		msg.DraftID = &d.ID
		var from string
		if e = tx.QueryRow(r.Context(), `SELECT lower(email) FROM users WHERE id=$1`, a.UserID).Scan(&from); e != nil {
			return e
		}
		salesRecipients(&msg, from, "")
		if e = s.salesQueueMessage(r.Context(), tx, a, l, msg, nil); e != nil {
			return e
		}
		messageID = msg.ID
		return s.salesEvent(r.Context(), tx, a, l, m.Role, "reply_queued", nil, map[string]string{"message_id": msg.ID, "draft_id": d.ID})
	})
	if !s.recordError(w, r, err) {
		writeJSON(w, 200, map[string]any{"queued": true, "message_id": messageID})
	}
}
func salesRFC(m salesMessage) string {
	header := "From: " + m.From + "\r\nTo: " + strings.Join(m.To, ", ") + "\r\n"
	if len(m.CC) > 0 {
		header += "Cc: " + strings.Join(m.CC, ", ") + "\r\n"
	}
	return header + "Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\nMessage-ID: " + m.MessageID + "\r\nIn-Reply-To: " + m.InReplyTo + "\r\nReferences: " + m.References + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(m.Body)) + "\r\n"
}
func (s *Server) salesSendJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ID    string `json:"id"`
		Brand string `json:"brand_id"`
	}
	if json.Unmarshal(job.Payload, &p) != nil || job.WorkspaceID == nil || !validUUID(p.ID) || !validUUID(p.Brand) {
		return jobs.ErrPermanent
	}
	a := &access{WorkspaceID: *job.WorkspaceID, Brand: p.Brand}
	var lead, status, kind, owner string
	var target *string
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT lead_id::text,status,kind,sender_id::text,handoff_to::text,content_sealed FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, p.ID).Scan(&lead, &status, &kind, &owner, &target, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "accepted" || status == "failed" {
		return nil
	}
	a.UserID = owner
	l, err := s.salesLoad(ctx, s.pool, a, lead, false)
	if err != nil {
		return err
	}
	var msg salesMessage
	if err = s.salesOpen(sealed, p.ID, &msg); err != nil {
		return err
	}
	member, err := salesMemberAt(ctx, s.pool, a, owner)
	if err != nil {
		return err
	}
	if !member.Connected || !slices.Contains([]string{"sdr", "ae"}, member.Role) {
		return errors.New("Sales sender must reconnect their own email")
	}
	// Reconcile an attempted send before checking whether a new send would be
	// allowed. Archiving, reassigning or changing policy after a provider accepts
	// mail must never turn an uncertain attempt into a safely retryable failure.
	var token string
	if kind != "introduction" {
		_, _, token, err = s.salesGrant(ctx, a, owner)
		if err != nil {
			return err
		}
	}
	if status == "sending" || status == "uncertain" {
		if kind != "introduction" {
			var found struct {
				Messages []struct {
					ID     string `json:"id"`
					Thread string `json:"threadId"`
				} `json:"messages"`
			}
			if err = s.oauth.ReadPersonalGoogle(ctx, token, "messages", url.Values{"q": {"in:sent rfc822msgid:" + strings.Trim(msg.MessageID, "<> ")}}, &found); err != nil {
				return err
			}
			if len(found.Messages) == 1 {
				msg.ProviderID, msg.ThreadID = found.Messages[0].ID, found.Messages[0].Thread
				return s.salesSendResult(ctx, a, l, msg, "accepted", "", target)
			}
		}
		return s.salesSendResult(ctx, a, l, msg, "uncertain", "send_uncertain", target)
	}
	if deref(l.OwnerID) != owner || !slices.Contains([]string{"active", "customer"}, l.State) {
		return s.salesSendResult(ctx, a, l, msg, "failed", "owner_changed", target)
	}
	if target != nil {
		targetMember, e := salesMemberAt(ctx, s.pool, a, *target)
		if e != nil {
			return e
		}
		if !targetMember.Connected {
			return s.salesSendResult(ctx, a, l, msg, "failed", "assignee_disconnected", target)
		}
	}
	decisions, err := policy.CheckRecipientsIn(ctx, s.pool, a.WorkspaceID, a.Brand, append(append([]string{}, msg.To...), msg.CC...))
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if !d.Allowed {
			return s.salesSendResult(ctx, a, l, msg, "failed", "policy_blocked", target)
		}
	}
	var opened *connectors.Opened
	var source salesSource
	if kind == "introduction" {
		if l.IntegrationID == nil {
			return errors.New("original EmailBison connection is missing")
		}
		opened, err = s.integrations.Open(ctx, s.pool, a.WorkspaceID, *l.IntegrationID)
		if err != nil {
			return err
		}
		if opened.Connector.Definition().ID != "emailbison" || opened.BrandID != nil && *opened.BrandID != a.Brand || len(opened.BrandIDs) > 0 && !slices.Contains(opened.BrandIDs, a.Brand) {
			return errors.New("original EmailBison connection is unavailable to this brand")
		}
		var sourceSealed []byte
		if err = s.pool.QueryRow(ctx, `SELECT source_sealed FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID).Scan(&sourceSealed); err != nil {
			return err
		}
		if err = s.salesOpen(sourceSealed, l.ID, &source); err != nil {
			return err
		}
		reader, ok := opened.Connector.(connectors.ReplyThreadSource)
		if !ok {
			return errors.New("original EmailBison source cannot preserve threads")
		}
		thread, e := reader.ReadReplyThread(ctx, opened.Client, opened.Creds, source.ReplyID)
		if e != nil {
			return e
		}
		matched := false
		for _, parent := range thread {
			if parent.ExternalID == source.ReplyID && parent.MailboxExternalID == source.SenderID && strings.EqualFold(parent.FromEmail, l.Email) && strings.EqualFold(parent.MailboxEmail, msg.From) {
				matched = true
			}
		}
		if !matched {
			return s.salesSendResult(ctx, a, l, msg, "failed", "source_thread_changed", target)
		}
	}
	// Persist the attempt before the provider call. Only one worker can claim it.
	tag, err := s.pool.Exec(ctx, `UPDATE sales_messages SET status='sending' WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND status='queued'`, a.WorkspaceID, a.Brand, p.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if kind == "introduction" {
		sender, ok := opened.Connector.(connectors.ReplyThreadSource)
		if !ok {
			return s.salesSendResult(ctx, a, l, msg, "failed", "provider_unavailable", target)
		}
		var receipt connectors.Reply
		receipt, err = sender.SendReplyReceipt(ctx, opened.Client, opened.Creds, connectors.ReplyOut{ReplyExternalID: source.ReplyID, MailboxExternalID: source.SenderID, ToEmail: msg.To[0], Body: msg.Body, CC: msg.CC})
		if err == nil {
			msg.ProviderID = receipt.ExternalID
			if receipt.MessageID != "" {
				msg.MessageID = "<" + strings.Trim(receipt.MessageID, "<> ") + ">"
			}
			if receipt.ExternalID == "" || receipt.MessageID == "" {
				err = errors.New("EmailBison send receipt is incomplete")
			}
		}

	} else {
		// Gmail thread ids are mailbox-local. Resolve using a message actually in
		// this user's mailbox rather than borrowing a colleague's thread id.
		var found struct {
			Messages []struct {
				Thread string `json:"threadId"`
			} `json:"messages"`
		}
		if e := s.oauth.ReadPersonalGoogle(ctx, token, "messages", url.Values{"q": {"rfc822msgid:" + strings.Trim(msg.InReplyTo, "<> ")}}, &found); e != nil {
			return s.salesSendResult(ctx, a, l, msg, "failed", "thread_lookup_failed", target)
		}
		thread := ""
		if len(found.Messages) > 0 {
			thread = found.Messages[0].Thread
		}
		msg.ProviderID, msg.ThreadID, err = s.oauth.SendSalesGoogle(ctx, token, base64.RawURLEncoding.EncodeToString([]byte(salesRFC(msg))), thread)
	}
	if err != nil {
		var refused oauth.GoogleSendRejected
		if errors.As(err, &refused) || kind == "introduction" && slices.Contains([]string{connectors.ErrAuth, connectors.ErrInvalid, connectors.ErrQuota}, connectors.KindOf(err)) {
			return s.salesSendResult(ctx, a, l, msg, "failed", "provider_refused", target)
		}
		if e := s.salesSendResult(ctx, a, l, msg, "uncertain", "send_uncertain", target); e != nil {
			return e
		}
		return fmt.Errorf("Sales send needs reconciliation: %w", err)
	}
	return s.salesSendResult(ctx, a, l, msg, "accepted", "", target)
}
func (s *Server) salesSendResult(ctx context.Context, a *access, l salesLead, msg salesMessage, status, code string, target *string) error {
	changed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := s.salesLoad(ctx, tx, a, l.ID, true)
		if err != nil {
			return err
		}
		identity := salesHash("rfc:" + strings.Trim(msg.MessageID, "<> "))
		// A provider copy may have arrived while the send response was in flight.
		// Keep that evidence, but show one conversation entry with the sent draft.
		var duplicate string
		err = tx.QueryRow(ctx, `SELECT id::text FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND identity_hash=$4 AND id<>$5`, a.WorkspaceID, a.Brand, l.ID, identity, msg.ID).Scan(&duplicate)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if duplicate != "" {
			if _, err = tx.Exec(ctx, `UPDATE sales_messages SET superseded_by=$4,identity_hash=$5 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, duplicate, msg.ID, salesHash("superseded:"+duplicate)); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE sales_message_copies SET message_id=$4 WHERE workspace_id=$1 AND brand_id=$2 AND message_id=$3`, a.WorkspaceID, a.Brand, duplicate, msg.ID); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE sales_messages SET status=$4,content_sealed=$5,identity_hash=$6 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND status<>$4 AND status<>'accepted'`, a.WorkspaceID, a.Brand, msg.ID, status, s.salesSeal(msg.ID, msg), salesHash("rfc:"+strings.Trim(msg.MessageID, "<> ")))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		changed = true
		_, err = tx.Exec(ctx, `UPDATE sales_leads SET updated_at=now(),version=version+1,reply_state=COALESCE((SELECT CASE WHEN direction='incoming' THEN 'reply_received' ELSE 'waiting_for_reply' END FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND status IN ('received','accepted') AND superseded_by IS NULL ORDER BY occurred_at DESC,id DESC LIMIT 1),reply_state) WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID)
		if err != nil {
			return err
		}
		if status == "accepted" {
			if target != nil && (msg.Kind == "introduction" || msg.Kind == "handoff") {
				if msg.Kind == "handoff" && deref(current.OwnerID) == a.UserID {
					_, err = tx.Exec(ctx, `UPDATE sales_leads SET owner_id=$4,ae_id=$4,introduced_by=$5,introduced_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID, *target, a.UserID)
				} else {
					_, err = tx.Exec(ctx, `UPDATE sales_leads SET introduced_by=$4,introduced_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID, a.UserID)
				}
				if err != nil {
					return err
				}
			}
		}
		eventCtx := ctx
		if code != "" {
			eventCtx = track.Mute(ctx)
		}
		return s.salesEvent(eventCtx, tx, a, l, "worker", "send_"+status, nil, map[string]string{"message_id": msg.ID, "error_code": code})
	})
	if err == nil && changed {
		metrics.SalesSendResult(status)
	}
	if err == nil && changed && code != "" {
		track.Fail(ctx, s.log, track.Event{Name: "sales.send_" + status, WorkspaceID: a.WorkspaceID, UserID: a.UserID, ActorType: "human", Surface: "job", Props: map[string]any{"brand_id": a.Brand, "message_id": msg.ID, "reason": code}}, "connector_unavailable", errors.New(code))
	}
	return err
}
