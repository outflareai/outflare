package api

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/track"
)

type salesMessagePreview struct {
	Body       string    `json:"body"`
	Direction  string    `json:"direction"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
}

// salesPostPreview is the newest Slack post on a conversation, as the queue
// shows it: the fields its channel shows first, and under them what the
// person wrote. A post with no fields has the AI's summary, else the post.
type salesPostPreview struct {
	Body       string          `json:"body"`
	Fields     []feedFieldView `json:"fields"`
	OccurredAt time.Time       `json:"occurred_at"`
}

type salesLeadActivity struct {
	Action     string    `json:"action"`
	ActorID    *string   `json:"actor_id"`
	ActorRole  string    `json:"actor_role"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Enrich only the already-authorized page, with at most two message excerpts
// per lead. The existing conversation/time index bounds each lateral lookup; neither
// full threads nor one HTTP/database round trip per row are needed.
func (s *Server) salesQueuePreviews(ctx context.Context, a *access, leads []salesLead, manager bool) error {
	if len(leads) == 0 {
		return nil
	}
	ids := make([]string, len(leads))
	byID := make(map[string]*salesLead, len(leads))
	for i := range leads {
		ids[i] = leads[i].ID
		byID[leads[i].ID] = &leads[i]
	}
	rows, err := s.pool.Query(ctx, `SELECT wanted.id::text,pick.incoming,m.id::text,m.direction,m.kind,m.occurred_at,m.content_sealed
 FROM unnest($3::uuid[]) AS wanted(id)
 CROSS JOIN (VALUES(false),(true)) AS pick(incoming)
 CROSS JOIN LATERAL (
  SELECT id,direction,kind,occurred_at,content_sealed FROM sales_messages
  WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=wanted.id AND superseded_by IS NULL
   AND (NOT pick.incoming OR direction='incoming')
  ORDER BY occurred_at DESC,id DESC LIMIT 1
 ) m`, a.WorkspaceID, a.Brand, ids)
	if err != nil {
		return err
	}
	_, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) {
		var lead, id string
		var incoming bool
		var sealed []byte
		var p salesMessagePreview
		if err := row.Scan(&lead, &incoming, &id, &p.Direction, &p.Kind, &p.OccurredAt, &sealed); err != nil {
			return struct{}{}, err
		}
		var m salesMessage
		if err := s.salesOpen(sealed, id, &m); err != nil {
			return struct{}{}, err
		}
		body := []rune(m.Body)
		if len(body) > 1200 {
			body = body[:1200]
		}
		p.Body = string(body)
		if incoming {
			byID[lead].LatestReply = &p
		} else {
			byID[lead].LatestMessage = &p
		}
		return struct{}{}, nil
	})
	if err != nil || !manager {
		return err
	}
	rows, err = s.pool.Query(ctx, `SELECT wanted.id::text,e.activity
 FROM unnest($3::uuid[]) AS wanted(id)
 CROSS JOIN LATERAL (
  SELECT jsonb_build_object('action',action,'actor_id',actor_id,'actor_role',actor_role,'occurred_at',occurred_at) AS activity
  FROM sales_events WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=wanted.id
  ORDER BY occurred_at DESC,id DESC LIMIT 1
 ) e`, a.WorkspaceID, a.Brand, ids)
	if err != nil {
		return err
	}
	_, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) {
		var lead string
		var raw []byte
		if err := row.Scan(&lead, &raw); err != nil {
			return struct{}{}, err
		}
		var activity salesLeadActivity
		if err := json.Unmarshal(raw, &activity); err != nil {
			return struct{}{}, err
		}
		byID[lead].LastActivity = &activity
		return struct{}{}, nil
	})
	return err
}

// salesLatestPosts fills LatestPost for an already-authorized page of
// conversations, with one query: the newest post on each that is not spam,
// set aside or deleted in Slack. A post that can't be opened leaves that
// row without a preview; it never fails the page.
func (s *Server) salesLatestPosts(ctx context.Context, a *access, leads []salesLead) {
	if len(leads) == 0 {
		return
	}
	ids := make([]string, len(leads))
	byID := make(map[string]*salesLead, len(leads))
	for i := range leads {
		ids[i] = leads[i].ID
		byID[leads[i].ID] = &leads[i]
	}
	fail := func(err error) {
		track.Fail(ctx, s.log, track.Event{Name: "sales_feed.preview_failed", WorkspaceID: a.WorkspaceID, Outcome: track.Error,
			Props: map[string]any{"brand_id": a.Brand}}, "internal", err)
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (i.lead_id) i.lead_id::text,i.id::text,i.occurred_at,i.content_sealed,COALESCE(c.preview_fields,'{}')
 FROM sales_feed_items i LEFT JOIN sales_feed_channels c ON c.id=i.feed_channel_id AND c.workspace_id=i.workspace_id
 WHERE i.workspace_id=$1 AND i.brand_id=$2 AND i.lead_id=ANY($3::uuid[]) AND i.status NOT IN ('spam','ignored') AND NOT i.deleted_in_slack
 ORDER BY i.lead_id,i.occurred_at DESC,i.id DESC`, a.WorkspaceID, a.Brand, ids)
	if err != nil {
		fail(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var lead, id string
		var sealed []byte
		var p salesPostPreview
		var layout []string
		if err := rows.Scan(&lead, &id, &p.OccurredAt, &sealed, &layout); err != nil {
			fail(err)
			return
		}
		var c feedContent
		if err := s.salesOpen(sealed, id, &c); err != nil {
			fail(err)
			continue
		}
		p.Fields = feedKeyFields(c, layout, feedQueueFields)
		body := feedQueueBody(c, p.Fields)
		if rs := []rune(body); len(rs) > 1200 {
			body = string(rs[:1200])
		}
		p.Body = body
		byID[lead].LatestPost = &p
	}
	if err := rows.Err(); err != nil {
		fail(err)
	}
}

// feedSaid matches the label of the field that holds what the person wrote
// or what the post is about, in a form or a ticket.
var feedSaid = regexp.MustCompile(`(?i)message|use case|description|comment|question|goal|detail|subject|title|note|reason|request`)

// feedSaidText is what the person wrote in a form or a ticket: the field
// whose label says it is the message, else the longest answer of a sentence
// or more; "" when the post has neither.
func feedSaidText(c feedContent) string {
	longest := ""
	for _, f := range c.Fields {
		v := strings.TrimSpace(f.Value)
		if v != "" && feedSaid.MatchString(f.Label) {
			return v
		}
		if utf8.RuneCountInString(v) > utf8.RuneCountInString(longest) {
			longest = v
		}
	}
	if utf8.RuneCountInString(longest) >= 40 && strings.Contains(longest, " ") {
		return longest
	}
	return ""
}

// feedPreviewText is a post in one line when no AI read it: what the person
// wrote rather than a form's first lines ("First name: Ana Last name: ..."),
// else the post as written.
func feedPreviewText(c feedContent) string {
	if said := feedSaidText(c); said != "" {
		return said
	}
	return c.Text
}
