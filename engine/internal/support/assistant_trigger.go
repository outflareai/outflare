package support

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// JobAssistant has the support assistant look at one customer message.
const JobAssistant = "support.assistant"

// AssistantMode is how the assistant works on a channel ("email" or
// "chat") for a brand's conversations: off, draft or auto. The brand's own
// setting wins over the workspace's; none is off.
func AssistantMode(ctx context.Context, q jobs.Querier, ws string, brand *string, channel string) (string, error) {
	col := "email_mode"
	if channel == ChannelChat {
		col = "chat_mode"
	}
	var mode string
	err := q.QueryRow(ctx, `SELECT `+col+` FROM support_assistant_settings
		WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid LIMIT 1`, ws, brand).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "off", nil
	}
	return mode, err
}

// queueAssistant asks the assistant to look at a customer's new message,
// when it is on for the conversation's channel. One job per message.
func queueAssistant(ctx context.Context, tx pgx.Tx, ws, conv, message string) error {
	var brand *string
	var channel string
	if err := tx.QueryRow(ctx, `SELECT brand_id::text, channel FROM support_conversations WHERE id = $1`, conv).Scan(&brand, &channel); err != nil {
		return err
	}
	mode, err := AssistantMode(ctx, tx, ws, brand, channel)
	if err != nil || mode == "off" {
		return err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobAssistant, WorkspaceID: &ws, MaxAttempts: 2,
		Payload:        map[string]string{"conversation_id": conv, "message_id": message},
		IdempotencyKey: JobAssistant + ":" + message})
	return err
}
