// Package alerts tells people when something needs them now: a hot reply,
// a hot website visitor, a negative review. Any module raises one with
// Raise, in the transaction that found it. Each alert is kept for the app
// (GET .../alerts), recorded as alert.raised (a webhook, so Slack, a CRM or
// Zapier can take it), counted in the digest, and emailed to the person it
// is for, or to the owners and admins, as the workspace chooses per kind
// and per brand. People can mute a kind for themselves.
//
// Alert emails are Outflare's own account email (sysmail), like sign-in
// links and AI budget warnings: they name what happened and link to it,
// and never quote a message, review or page.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// JobDeliver emails one alert to one person.
const JobDeliver = "alert.deliver"

// Who gets a kind of alert by email.
const (
	ToOwner  = "owner"  // the person it is for, else the owners and admins
	ToAdmins = "admins" // every owner and admin
	ToOff    = "off"
)

// DailyCap is the most alert emails one person gets in a day; past it the
// rest wait in the app and the digest.
const DailyCap = 50

// A Kind is a sort of alert a module raises. Describe turns an alert into
// what people see, read fresh from its target, so an alert never keeps a
// copy of a name or a message.
type Kind struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Scope is what a caller needs to see these alerts in the app.
	Scope    string                                                         `json:"-"`
	Describe func(ctx context.Context, q jobs.Querier, a Row) (View, error) `json:"-"`
}

// A View is an alert as people see it: a title, a line of detail and the
// app page to open (under the workspace, such as "inbox/<id>").
type View struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Path  string `json:"path"`
}

var kinds []Kind

// Register adds a kind. Modules call it from init.
func Register(k Kind) {
	if slices.ContainsFunc(kinds, func(o Kind) bool { return o.ID == k.ID }) {
		panic("alerts: kind registered twice: " + k.ID)
	}
	kinds = append(kinds, k)
}

// Kinds lists every registered kind, in the order registered.
func Kinds() []Kind { return slices.Clone(kinds) }

// Find returns a registered kind.
func Find(id string) (Kind, bool) {
	i := slices.IndexFunc(kinds, func(k Kind) bool { return k.ID == id })
	if i < 0 {
		return Kind{}, false
	}
	return kinds[i], true
}

// An Alert is what a module raises.
type Alert struct {
	WorkspaceID string
	BrandID     *string
	Kind        string
	// DedupeKey makes the same thing raise one alert: a reply's id, or a
	// record and a week.
	DedupeKey  string
	TargetType string
	TargetID   string
	RecordID   *string
	// OwnerID is the user it is for, when there is one.
	OwnerID *string
	// SkipActorID is someone who already knows (who labelled the reply).
	SkipActorID string
	// Data holds ids, enums and counts only, as in a webhook.
	Data      map[string]any
	RequestID string
}

// A Row is a kept alert.
type Row struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"-"`
	BrandID     *string        `json:"brand_id"`
	Kind        string         `json:"kind"`
	TargetType  string         `json:"target_type"`
	TargetID    string         `json:"target_id"`
	RecordID    *string        `json:"record_id"`
	OwnerID     *string        `json:"owner_id"`
	Data        map[string]any `json:"data"`
	CreatedAt   time.Time      `json:"created_at"`
}

// Raise keeps an alert, records alert.raised and queues its emails, all in
// q's transaction. It returns the alert's id, or "" when the same thing
// was already raised.
func Raise(ctx context.Context, q jobs.Querier, a Alert) (string, error) {
	if _, ok := Find(a.Kind); !ok {
		return "", fmt.Errorf("alerts: unknown kind %q", a.Kind)
	}
	if a.Data == nil {
		a.Data = map[string]any{}
	}
	raw, err := json.Marshal(a.Data)
	if err != nil {
		return "", err
	}
	var id string
	err = q.QueryRow(ctx, `INSERT INTO alerts (workspace_id, brand_id, kind, dedupe_key, target_type, target_id, record_id, owner_id, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING RETURNING id::text`,
		a.WorkspaceID, a.BrandID, a.Kind, a.DedupeKey, a.TargetType, a.TargetID, a.RecordID, a.OwnerID, raw).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	data := map[string]any{"alert_id": id, "kind": a.Kind, "target_type": a.TargetType, "target_id": a.TargetID,
		"record_id": a.RecordID, "owner_id": a.OwnerID, "brand_id": a.BrandID}
	for k, v := range a.Data {
		if _, taken := data[k]; !taken {
			data[k] = v
		}
	}
	brand := ""
	if a.BrandID != nil {
		brand = *a.BrandID
	}
	if err := audit.Record(ctx, q, audit.Event{WorkspaceID: a.WorkspaceID, ActorType: "system", Action: "alert.raised",
		TargetType: "alert", TargetID: id, RequestID: a.RequestID, BrandID: brand, Data: data,
		Props: map[string]any{"kind": a.Kind, "has_owner": a.OwnerID != nil}}); err != nil {
		return "", err
	}
	to, err := EmailSetting(ctx, q, a.WorkspaceID, a.BrandID, a.Kind)
	if err != nil {
		return "", err
	}
	users, err := recipients(ctx, q, a, to)
	if err != nil {
		return "", err
	}
	deliver := func(channel string, user, integration, target *string) error {
		var delivery string
		if err := q.QueryRow(ctx, `INSERT INTO alert_deliveries (workspace_id, alert_id, channel, user_id, integration_id, target, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'queued') RETURNING id::text`, a.WorkspaceID, id, channel, user, integration, target).Scan(&delivery); err != nil {
			return err
		}
		_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobDeliver, WorkspaceID: &a.WorkspaceID, IdempotencyKey: "alert:" + delivery,
			MaxAttempts: 5, Payload: map[string]any{"delivery_id": delivery}})
		return err
	}
	for _, u := range users {
		if err := deliver("email", &u, nil, nil); err != nil {
			return "", err
		}
	}
	integration, channel, err := SlackSetting(ctx, q, a.WorkspaceID, a.BrandID, a.Kind)
	if err != nil {
		return "", err
	}
	if integration != "" {
		if err := deliver("slack", nil, &integration, &channel); err != nil {
			return "", err
		}
	}
	return id, nil
}

// EmailSetting is who gets a kind by email: the brand's choice, else the
// workspace's, else the person it is for.
func EmailSetting(ctx context.Context, q jobs.Querier, ws string, brand *string, kind string) (string, error) {
	var to string
	err := q.QueryRow(ctx, `SELECT email FROM alert_settings WHERE workspace_id = $1 AND kind = $3 AND email IS NOT NULL
		AND (brand_id IS NULL OR brand_id = $2) ORDER BY brand_id IS NULL LIMIT 1`, ws, brand, kind).Scan(&to)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToOwner, nil
	}
	return to, err
}

// SlackSetting is the Slack channel a kind posts to: the brand's, else
// the workspace's, or "" for none.
func SlackSetting(ctx context.Context, q jobs.Querier, ws string, brand *string, kind string) (integration, channel string, err error) {
	err = q.QueryRow(ctx, `SELECT slack_integration_id::text, slack_channel FROM alert_settings
		WHERE workspace_id = $1 AND kind = $3 AND slack_integration_id IS NOT NULL AND slack_channel IS NOT NULL
		AND (brand_id IS NULL OR brand_id = $2) ORDER BY brand_id IS NULL LIMIT 1`, ws, brand, kind).Scan(&integration, &channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return
}

// recipients are the people to email: members with a confirmed address
// who haven't muted the kind, leaving out who already knows.
func recipients(ctx context.Context, q jobs.Querier, a Alert, to string) ([]string, error) {
	if to == ToOff {
		return nil, nil
	}
	owner := ""
	if to == ToOwner && a.OwnerID != nil {
		owner = *a.OwnerID
	}
	var users []string
	err := q.QueryRow(ctx, `
		SELECT COALESCE(array_agg(u.id::text ORDER BY u.id), '{}') FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND u.email_verified_at IS NOT NULL AND u.disabled_at IS NULL
		  AND CASE WHEN $2 <> '' THEN u.id::text = $2 ELSE m.role IN ('owner', 'admin') END
		  AND u.actor_id::text <> $3
		  AND NOT EXISTS (SELECT 1 FROM alert_mutes x WHERE x.workspace_id = $1 AND x.user_id = u.id AND x.kind = $4)`,
		a.WorkspaceID, owner, a.SkipActorID, a.Kind).Scan(&users)
	if err != nil {
		return nil, err
	}
	// The owner may have left the workspace: the admins hear instead.
	if owner != "" && len(users) == 0 {
		var member bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id::text = $2)`,
			a.WorkspaceID, owner).Scan(&member); err != nil {
			return nil, err
		}
		if !member {
			b := a
			b.OwnerID = nil
			return recipients(ctx, q, b, ToAdmins)
		}
	}
	return users, nil
}

// Load reads one alert in a workspace.
func Load(ctx context.Context, q jobs.Querier, ws, id string) (Row, error) {
	var r Row
	var raw []byte
	err := q.QueryRow(ctx, `SELECT id::text, workspace_id::text, brand_id::text, kind, target_type, target_id::text, record_id::text,
			owner_id::text, data, created_at FROM alerts WHERE id = $1 AND workspace_id = $2`, id, ws).
		Scan(&r.ID, &r.WorkspaceID, &r.BrandID, &r.Kind, &r.TargetType, &r.TargetID, &r.RecordID, &r.OwnerID, &raw, &r.CreatedAt)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(raw, &r.Data)
}

// Describe is the alert as people see it, or a plain fallback when its
// kind can't say (its target was deleted).
func Describe(ctx context.Context, q jobs.Querier, r Row) View {
	if k, ok := Find(r.Kind); ok && k.Describe != nil {
		if v, err := k.Describe(ctx, q, r); err == nil {
			return v
		}
	}
	title := r.Kind
	if k, ok := Find(r.Kind); ok {
		title = k.Title
	}
	return View{Title: title, Text: "What this alert was about is no longer there."}
}
