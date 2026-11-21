package support

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// JobChatUnanswered looks at a new chat again after PassAfter: when the
// teammate it went to hasn't answered, it goes to the next one online.
const JobChatUnanswered = "support.chat_unanswered"

// PassAfter is how long a teammate has to answer a new chat before it is
// passed on; AwayAfter is how long a visitor waits, or has been gone,
// before the conversation carries on by email.
const (
	PassAfter = 30 * time.Second
	AwayAfter = 2 * time.Minute
)

// awaySQL is AwayAfter as a SQL interval.
const awaySQL = `interval '2 minutes'`

// supportMailboxSQL picks the active support mailbox of conversation c's
// brand that a chat carries on from by email.
const supportMailboxSQL = `SELECT mb.id FROM mailboxes mb WHERE mb.workspace_id = c.workspace_id AND mb.brand_id = c.brand_id
	AND mb.purpose = 'support' AND mb.status = 'active' ORDER BY mb.created_at, mb.id LIMIT 1`

// Online and Away: who is taking new chats for a brand right now. A
// teammate is online while they say so and their Inbox checked in within
// PresenceWindow, so a closed page takes them out on its own.

// PresenceWindow is the SQL interval a check-in stays good for.
const PresenceWindow = `interval '3 minutes'`

// onlineSQL is the condition on an inbox_presence row p for online now.
const onlineSQL = `p.status = 'online' AND p.seen_at > now() - ` + PresenceWindow

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TeamOnline says whether anyone is online for the brand: nil when the
// brand's team has never used Online and Away, so nothing is claimed.
func TeamOnline(ctx context.Context, q querier, ws string, brand *string) (*bool, error) {
	var used, online bool
	err := q.QueryRow(ctx, `SELECT count(*) > 0, count(*) FILTER (WHERE `+onlineSQL+`) > 0 FROM inbox_presence p
		WHERE p.workspace_id = $1 AND p.brand_id = COALESCE($2::uuid, outflare_first_brand($1))`, ws, brand).Scan(&used, &online)
	if err != nil || !used {
		return nil, err
	}
	return &online, nil
}

// assignToOnline gives a new chat nobody owns to whoever is online with
// the fewest open conversations; with nobody online it stays unassigned.
func assignToOnline(ctx context.Context, tx pgx.Tx, ws, conv string) error {
	user, err := passToOnline(ctx, tx, ws, conv, nil, "online")
	if err == nil && user == "" {
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM support_conversations c JOIN inbox_presence p
			ON p.workspace_id = c.workspace_id AND p.brand_id = c.brand_id WHERE c.id = $1)`, conv).Scan(&used); err != nil {
			return err
		}
		if used {
			metrics.SupportChatRouted("nobody")
		}
	}
	return err
}

// passToOnline assigns the chat to the online teammate with the fewest
// open conversations who isn't in tried, and queues the look after
// PassAfter. It answers who, or "" when nobody else is online.
func passToOnline(ctx context.Context, tx pgx.Tx, ws, conv string, tried []string, via string) (string, error) {
	var user string
	err := tx.QueryRow(ctx, `
		SELECT p.user_id::text FROM support_conversations c
		JOIN inbox_presence p ON p.workspace_id = c.workspace_id AND p.brand_id = COALESCE(c.brand_id, outflare_first_brand(c.workspace_id))
		JOIN memberships m ON m.workspace_id = c.workspace_id AND m.user_id = p.user_id
		WHERE c.id = $1 AND c.workspace_id = $2 AND `+onlineSQL+` AND NOT (p.user_id::text = ANY($3::text[]))
		ORDER BY (SELECT count(*) FROM support_conversations o WHERE o.workspace_id = c.workspace_id AND o.assignee_id = p.user_id AND o.status = 'open'),
			p.updated_at, p.user_id
		LIMIT 1`, conv, ws, append([]string{}, tried...)).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE support_conversations SET assignee_id = $2, updated_at = now() WHERE id = $1`, conv, user); err != nil {
		return "", err
	}
	tried = append(append([]string{}, tried...), user)
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobChatUnanswered, WorkspaceID: &ws, MaxAttempts: 3, RunAt: time.Now().Add(PassAfter),
		Payload:        map[string]any{"conversation_id": conv, "assignee_id": user, "tried": tried},
		IdempotencyKey: JobChatUnanswered + ":" + conv + ":" + user}); err != nil {
		return "", err
	}
	metrics.SupportChatRouted(via)
	return user, audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "support.conversation_assigned",
		TargetType: "support_conversation", TargetID: conv,
		Data: map[string]any{"assignee_id": user, "via": via}, Props: map[string]any{"via": via}})
}

// ChatUnanswered runs JobChatUnanswered: a chat still waiting for its
// first answer from the teammate it went to goes to the next one online.
// Anything else (answered, closed, moved by a person) is left alone.
func ChatUnanswered(pool *pgxpool.Pool) jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		var p struct {
			ConversationID string   `json:"conversation_id"`
			AssigneeID     string   `json:"assignee_id"`
			Tried          []string `json:"tried"`
		}
		if err := json.Unmarshal(j.Payload, &p); err != nil || j.WorkspaceID == nil {
			return nil
		}
		ws := *j.WorkspaceID
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var waiting bool
			err := tx.QueryRow(ctx, `SELECT c.status = 'open' AND c.first_reply_at IS NULL AND c.assignee_id::text = $3
					AND NOT EXISTS (SELECT 1 FROM support_messages m WHERE m.conversation_id = c.id AND m.kind = 'out')
				FROM support_conversations c WHERE c.id = $1 AND c.workspace_id = $2 FOR UPDATE`,
				p.ConversationID, ws, p.AssigneeID).Scan(&waiting)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !waiting {
				return nil
			}
			if err != nil {
				return err
			}
			_, err = passToOnline(ctx, tx, ws, p.ConversationID, p.Tried, "passed")
			return err
		})
	}
}

// ChatWaiting says what a visitor still waiting for a first answer is
// told: "" (keep waiting here), "email" (the team will answer by email)
// or "email_needed" (the same, once they leave an address). It turns to
// email when nobody is online, or after AwayAfter with no answer, and only
// when the brand has a support mailbox to send from.
func ChatWaiting(ctx context.Context, q querier, conv string) (string, error) {
	var w string
	err := q.QueryRow(ctx, `
		SELECT CASE WHEN c.channel = 'chat' AND c.status <> 'closed' AND c.first_reply_at IS NULL
				AND NOT EXISTS (SELECT 1 FROM support_messages m WHERE m.conversation_id = c.id AND m.kind = 'out')
				AND EXISTS (`+supportMailboxSQL+`)
				AND (c.first_message_at < now() - `+awaySQL+`
					OR (EXISTS (SELECT 1 FROM inbox_presence p WHERE p.workspace_id = c.workspace_id AND p.brand_id = c.brand_id)
						AND NOT EXISTS (SELECT 1 FROM inbox_presence p WHERE p.workspace_id = c.workspace_id AND p.brand_id = c.brand_id AND `+onlineSQL+`)))
			THEN CASE WHEN c.contact_email = '' THEN 'email_needed' ELSE 'email' END ELSE '' END
		FROM support_conversations c WHERE c.id = $1`, conv).Scan(&w)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return w, err
}
