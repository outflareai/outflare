package support

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Live chat: a message a visitor writes in the website widget joins their
// conversation that isn't closed, or starts one. The team's replies are
// stored as sent: the widget shows them.

// ChannelChat is a conversation from the website widget.
const ChannelChat = "chat"

// ChatMessage is one message a visitor wrote in the widget.
type ChatMessage struct {
	WorkspaceID, WidgetID string
	BrandID               *string
	// TokenHash names the visitor: the hash of the token their browser keeps.
	TokenHash []byte
	// Email and Name, when they gave them or the site vouched for them
	// (Verified).
	Email, Name string
	Verified    bool
	Body        string
}

// ChatConversation is the visitor's conversation that isn't closed, if
// any: "" when there is none.
func ChatConversation(ctx context.Context, q pgx.Tx, ws, widget string, token []byte) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM support_conversations
		WHERE workspace_id = $1 AND chat_widget_id = $2 AND chat_token_hash = $3 AND status <> 'closed'
		ORDER BY last_message_at DESC LIMIT 1`, ws, widget, token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// IntakeChat files a visitor's message.
func IntakeChat(ctx context.Context, tx pgx.Tx, m ChatMessage) (Received, error) {
	var out Received
	ws := m.WorkspaceID
	email := strings.ToLower(strings.TrimSpace(m.Email))
	var record *string
	if email != "" {
		ids, err := customers.EnsurePeople(ctx, tx, ws, "first_party", "support", []customers.Person{{Email: email, Name: m.Name}})
		if err != nil {
			return out, err
		}
		if id, ok := ids[email]; ok {
			record = &id
		}
	}
	conv, err := ChatConversation(ctx, tx, ws, m.WidgetID, m.TokenHash)
	if err != nil {
		return out, err
	}
	now := time.Now()
	if conv == "" {
		out.Started = true
		subject := strings.Join(strings.Fields(m.Body), " ")
		if len([]rune(subject)) > 80 {
			subject = strings.TrimSpace(string([]rune(subject)[:79])) + "…"
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO support_conversations (workspace_id, brand_id, record_id, channel, subject, contact_email,
				first_message_at, last_message_at, chat_widget_id, chat_token_hash, identity_verified, visitor_seen_at)
			VALUES ($1, COALESCE($2::uuid, outflare_first_brand($1)), $3, 'chat', $4, $5, $6, $6, $7, $8, $9, $6) RETURNING id::text`,
			ws, m.BrandID, record, subject, email, now, m.WidgetID, m.TokenHash, m.Verified).Scan(&conv)
		if err != nil {
			return out, err
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE support_conversations SET last_message_at = $2, last_from = 'customer', unread = true, status = 'open',
			record_id = COALESCE(record_id, $3), contact_email = CASE WHEN contact_email = '' THEN $4 ELSE contact_email END,
			identity_verified = identity_verified OR $5, visitor_seen_at = $2, updated_at = now()
		WHERE id = $1`, conv, now, record, email, m.Verified); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO support_messages (workspace_id, conversation_id, kind, record_id, from_email, from_name, body, status, created_at)
		VALUES ($1, $2, 'in', $3, $4, $5, $6, 'received', $7) RETURNING id::text`,
		ws, conv, record, email, truncate(m.Name, 300), truncate(m.Body, 5000), now).Scan(&out.MessageID)
	if err != nil {
		return out, err
	}
	out.ConversationID, out.New = conv, true
	if err := Clock(ctx, tx, conv); err != nil {
		return out, err
	}
	if err := queueAssistant(ctx, tx, ws, conv, out.MessageID); err != nil {
		return out, err
	}
	action := "support.message_received"
	if out.Started {
		action = "support.conversation_started"
	}
	metrics.SupportMessage("in", "success")
	return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: action,
		TargetType: "support_conversation", TargetID: conv,
		Data:  map[string]any{"message_id": out.MessageID, "auto_reply": false, "channel": ChannelChat, "via": "widget", "verified": m.Verified},
		Props: map[string]any{"auto_reply": false, "channel": ChannelChat, "via": "widget", "verified": m.Verified}})
}

// SetChatContact records who the visitor is once they give an email (or
// the site vouches for them), on their open conversation.
func SetChatContact(ctx context.Context, tx pgx.Tx, ws, conv, email, name string, verified bool) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	ids, err := customers.EnsurePeople(ctx, tx, ws, "first_party", "support", []customers.Person{{Email: email, Name: name}})
	if err != nil {
		return err
	}
	rec := ids[email]
	_, err = tx.Exec(ctx, `UPDATE support_conversations SET record_id = COALESCE(record_id, NULLIF($3, '')::uuid),
		contact_email = CASE WHEN contact_email = '' THEN $4 ELSE contact_email END, identity_verified = identity_verified OR $5, updated_at = now()
		WHERE id = $1 AND workspace_id = $2`, conv, ws, rec, email, verified)
	return err
}
