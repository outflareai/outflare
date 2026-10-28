// Package audit writes the append-only audit log.
package audit

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// Event is one audited action. WorkspaceID and ActorID may be empty for
// system-level events.
type Event struct {
	WorkspaceID string
	ActorID     string
	ActorType   string // human, api_token, service_account, agent, system
	Action      string // e.g. "mailbox.connected", "message.sent"
	TargetType  string
	TargetID    string
	RequestID   string
	Data        map[string]any
	// UserID is the person behind the actor, for the product event only
	// (not stored). Left empty, it is looked up from the actor: the user,
	// or the one who made the API token.
	UserID string
	// Props are extra product event properties (not stored): ids, enums
	// and counts only, never names, addresses or text.
	Props map[string]any
	// BrandID is the brand the change belongs to. Left empty, it is the
	// target's brand when the target has one (see brandOf), or data's
	// brand_id.
	BrandID string
}

// brandOf finds a target's brand, by target type, from its id ($1) in the
// workspace ($2).
var brandOf = map[string]string{
	"brand":      `SELECT id FROM brands WHERE id = $1::uuid AND workspace_id = $2`,
	"project":    `SELECT brand_id FROM projects WHERE id = $1::uuid AND workspace_id = $2`,
	"sequence":   `SELECT brand_id FROM sequences WHERE id = $1::uuid AND workspace_id = $2`,
	"lead_list":  `SELECT brand_id FROM lead_lists WHERE id = $1::uuid AND workspace_id = $2`,
	"mission":    `SELECT brand_id FROM missions WHERE id = $1::uuid AND workspace_id = $2`,
	"mailbox":    `SELECT brand_id FROM mailboxes WHERE id = $1::uuid AND workspace_id = $2`,
	"memory":     `SELECT brand_id FROM memories WHERE id = $1::uuid AND workspace_id = $2`,
	"enrollment": `SELECT s.brand_id FROM enrollments e JOIN sequences s ON s.id = e.sequence_id WHERE e.id = $1::uuid AND e.workspace_id = $2`,
	"inbound_message": `SELECT COALESCE(s.brand_id, m.brand_id) FROM inbound_messages i
		LEFT JOIN sequences s ON s.id = i.sequence_id LEFT JOIN mailboxes m ON m.id = i.mailbox_id
		WHERE i.id = $1::uuid AND i.workspace_id = $2`,
	"meeting": `SELECT COALESCE(s.brand_id, p.brand_id) FROM meetings x
		LEFT JOIN sequences s ON s.id = x.sequence_id LEFT JOIN projects p ON p.id = x.project_id
		WHERE x.id = $1::uuid AND x.workspace_id = $2`,
	"deal": `SELECT COALESCE(s.brand_id, p.brand_id) FROM deals x
		LEFT JOIN sequences s ON s.id = x.sequence_id LEFT JOIN projects p ON p.id = x.project_id
		WHERE x.id = $1::uuid AND x.workspace_id = $2`,
	"mission_action": `SELECT m.brand_id FROM mission_actions a JOIN missions m ON m.id = a.mission_id WHERE a.id = $1::uuid AND m.workspace_id = $2`,
	"review_page":    `SELECT brand_id FROM review_pages WHERE id = $1::uuid AND workspace_id = $2`,
	"review":         `SELECT brand_id FROM reviews WHERE id = $1::uuid AND workspace_id = $2`,
	// A domain's id is its name.
	"domain": `SELECT brand_id FROM sending_domains WHERE domain = lower($1) AND workspace_id = $2`,
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// brand returns the event's brand id, or "".
func brand(ctx context.Context, q jobs.Querier, e Event) (string, error) {
	if e.BrandID != "" || e.WorkspaceID == "" {
		return e.BrandID, nil
	}
	if sql, ok := brandOf[e.TargetType]; ok && e.TargetID != "" && (e.TargetType == "domain" || uuidRe.MatchString(e.TargetID)) {
		var id *string
		err := q.QueryRow(ctx, `SELECT (`+sql+`)::text`, e.TargetID, e.WorkspaceID).Scan(&id)
		if err != nil {
			return "", err
		}
		if id != nil {
			return *id, nil
		}
	}
	// A record already deleted can say which brand it had.
	if b, ok := e.Data["brand_id"].(string); ok && uuidRe.MatchString(b) {
		return b, nil
	}
	if b, ok := e.Data["brand_id"].(*string); ok && b != nil && uuidRe.MatchString(*b) {
		return *b, nil
	}
	return "", nil
}

// Record inserts the event. Pass the transaction that made the change so the
// audit row commits or rolls back with it. Events in the webhook catalog are
// queued for the workspace's webhooks in the same transaction. Every action
// is also a product event (package track) with its target but never its
// data, which can hold names and addresses.
func Record(ctx context.Context, q jobs.Querier, e Event) error {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	if e.Data == nil {
		data = []byte(`{}`)
	}
	if e.BrandID, err = brand(ctx, q, e); err != nil {
		return err
	}
	var (
		id int64
		at time.Time
	)
	err = q.QueryRow(ctx, `
		INSERT INTO audit_events (workspace_id, actor_id, actor_type, action, target_type, target_id, request_id, data, brand_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, created_at`,
		nullable(e.WorkspaceID), nullable(e.ActorID), e.ActorType, e.Action,
		nullable(e.TargetType), nullable(e.TargetID), nullable(e.RequestID), data, nullable(e.BrandID)).Scan(&id, &at)
	if err != nil {
		return err
	}
	props := map[string]any{}
	for k, v := range e.Props {
		props[k] = v
	}
	if e.TargetType != "" {
		props["target_type"], props["target_id"] = e.TargetType, e.TargetID
	}
	if m, ok := e.Data["mission_id"].(string); ok {
		props["mission_id"] = m
	}
	if e.BrandID != "" {
		props["brand_id"] = e.BrandID
	}
	if e.UserID == "" && e.ActorID != "" {
		// Work done for someone outside their own request (an OAuth
		// callback, a connect link, a provider's webhook) still counts as
		// theirs. The actor id was just stored, so this cannot fail on it.
		_ = q.QueryRow(ctx, `SELECT COALESCE(
			(SELECT id::text FROM users WHERE actor_id = $1),
			(SELECT created_by::text FROM api_tokens WHERE actor_id = $1), '')`, e.ActorID).Scan(&e.UserID)
	}
	track.Emit(ctx, track.Event{Name: e.Action, UserID: e.UserID, ActorID: e.ActorID, ActorType: e.ActorType,
		WorkspaceID: e.WorkspaceID, RequestID: e.RequestID, Props: props, At: at})
	if e.WorkspaceID == "" || !webhooks.Delivered(e.Action) {
		return err
	}
	ev := webhooks.FromAudit(id, at, e.WorkspaceID, e.ActorType, nullable(e.ActorID), e.Action,
		nullable(e.TargetType), nullable(e.TargetID), nullable(e.RequestID), e.Data)
	ev.BrandID = nullable(e.BrandID)
	return webhooks.Enqueue(ctx, q, ev)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
