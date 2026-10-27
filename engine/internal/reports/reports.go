// Package reports answers "how is outreach going" at any grain: by day or
// week, sequence, step, mailbox, domain or brand, over any period, filtered
// to a sequence, mailbox, domain or brand. Every number comes from the engine's own
// records (sends, policy blocks, failures, bounces, replies, opt-outs,
// unsubscribes and warm-up placement), so people and agents read the same
// figures.
package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Groupings a report can be broken down by.
const (
	ByNone     = ""
	ByDay      = "day"
	ByWeek     = "week"
	BySequence = "sequence"
	ByStep     = "step"
	ByMailbox  = "mailbox"
	ByDomain   = "domain"
	// A record's brand is its sequence's, or its mailbox's when it has no
	// sequence (warm-up); shared records have none.
	ByBrand = "brand"
)

var Groupings = []string{ByDay, ByWeek, BySequence, ByStep, ByMailbox, ByDomain, ByBrand}

// Query picks the period, grain and filters.
type Query struct {
	From, To   time.Time
	GroupBy    string
	Timezone   string // for day and week buckets; default UTC
	SequenceID string
	MailboxID  string
	Domain     string
	// Brand is a brand's id, "none" for shared records, or "" for all.
	// BrandsIn, when set, keeps the report to these brands and shared
	// records (a brand-limited caller).
	Brand    string
	BrandsIn []string
}

// Metrics are the counts for one row, and rates derived from them.
type Metrics struct {
	Sent         int `json:"sent"`
	Contacted    int `json:"contacted"`  // distinct leads emailed
	NewLeads     int `json:"new_leads"`  // first steps sent
	FollowUps    int `json:"follow_ups"` // later steps sent
	Failed       int `json:"failed"`
	Blocked      int `json:"blocked"` // stopped by the send policy before sending
	Bounced      int `json:"bounced"`
	Replies      int `json:"replies"`
	RepliedLeads int `json:"replied_leads"`
	AutoReplies  int `json:"auto_replies"`
	OptOuts      int `json:"opt_outs"`     // replies asking not to be contacted
	Unsubscribes int `json:"unsubscribes"` // unsubscribe link or header
	WarmupInbox  int `json:"warmup_inbox"` // warm-up mail that landed in the inbox
	WarmupSpam   int `json:"warmup_spam"`  // ...in spam
	WarmupLost   int `json:"warmup_missing"`
	Clicks       int `json:"clicks"`        // clicks on tracked links, scanners left out
	ClickedLeads int `json:"clicked_leads"` // distinct leads who clicked

	// Percentages, null when there is nothing to divide by.
	ReplyRate     *float64 `json:"reply_rate"`      // replied leads / contacted
	BounceRate    *float64 `json:"bounce_rate"`     // bounced / sent
	OptOutRate    *float64 `json:"opt_out_rate"`    // (opt-outs + unsubscribes) / contacted
	PlacementRate *float64 `json:"inbox_placement"` // warm-up inbox / warm-up landed
	ClickRate     *float64 `json:"click_rate"`      // clicked leads / contacted (only meaningful with click tracking on)
}

// Row is one bucket of a report.
type Row struct {
	Key  string `json:"key"`  // the day, week start, step number, or id
	Name string `json:"name"` // what to show: the sequence name, mailbox address, "Step 2"
	// Label is Name under its first name, kept until clients move to name.
	Label string `json:"label"`
	Metrics
}

// Report is a whole report.
type Report struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	GroupBy string    `json:"group_by"`
	Totals  Metrics   `json:"totals"`
	Rows    []Row     `json:"rows"`
}

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// events is every countable thing in the period as (ts, sequence, step,
// mailbox, domain, lead, kind), filtered.
const events = `
	WITH ev AS (
		SELECT COALESCE(m.sent_at, m.created_at) AS ts, m.sequence_id, m.step_position AS step, m.mailbox_id,
		       lower(split_part(m.from_email, '@', 2)) AS domain, m.lead_id,
		       CASE m.status WHEN 'sent' THEN 'sent' WHEN 'failed' THEN 'failed' WHEN 'blocked' THEN 'blocked' END AS kind
		FROM messages m
		WHERE m.workspace_id = $1 AND m.status IN ('sent', 'failed', 'blocked') AND m.reply_to_inbound_id IS NULL
		  AND COALESCE(m.sent_at, m.created_at) >= $2 AND COALESCE(m.sent_at, m.created_at) < $3
		UNION ALL
		SELECT e.updated_at, e.sequence_id,
		       (SELECT max(step_position) FROM messages WHERE enrollment_id = e.id AND status IN ('sent', 'failed')),
		       e.mailbox_id, lower(split_part(mb.email, '@', 2)), e.lead_id, e.status
		FROM enrollments e LEFT JOIN mailboxes mb ON mb.id = e.mailbox_id
		WHERE e.workspace_id = $1 AND e.status IN ('bounced', 'unsubscribed') AND e.updated_at >= $2 AND e.updated_at < $3
		UNION ALL
		SELECT i.received_at, i.sequence_id, rm.step_position, i.mailbox_id, lower(split_part(mb.email, '@', 2)), i.lead_id,
		       CASE WHEN i.kind = 'reply' THEN 'reply' ELSE 'auto_reply' END
		FROM inbound_messages i JOIN mailboxes mb ON mb.id = i.mailbox_id LEFT JOIN messages rm ON rm.id = i.reply_to_message_id
		WHERE i.workspace_id = $1 AND i.kind IN ('reply', 'auto_reply') AND (i.sequence_id IS NOT NULL OR i.lead_id IS NOT NULL)
		  AND i.received_at >= $2 AND i.received_at < $3
		UNION ALL
		SELECT i.received_at, i.sequence_id, rm.step_position, i.mailbox_id, lower(split_part(mb.email, '@', 2)), i.lead_id, 'opt_out'
		FROM inbound_messages i JOIN mailboxes mb ON mb.id = i.mailbox_id LEFT JOIN messages rm ON rm.id = i.reply_to_message_id
		WHERE i.workspace_id = $1 AND i.opt_out AND i.received_at >= $2 AND i.received_at < $3
		UNION ALL
		SELECT w.landed_at, NULL::uuid, NULL::int, w.from_mailbox_id, lower(split_part(mb.email, '@', 2)), NULL::uuid, 'warmup_' || w.landed
		FROM warmup_messages w JOIN mailboxes mb ON mb.id = w.from_mailbox_id
		WHERE w.workspace_id = $1 AND w.landed IS NOT NULL AND w.landed_at >= $2 AND w.landed_at < $3
		UNION ALL
		SELECT c.clicked_at, t.sequence_id, t.step_position, en.mailbox_id, lower(split_part(mb.email, '@', 2)), en.lead_id, 'click'
		FROM link_clicks c JOIN tracked_links t ON t.id = c.link_id JOIN enrollments en ON en.id = t.enrollment_id
		LEFT JOIN mailboxes mb ON mb.id = en.mailbox_id
		WHERE c.workspace_id = $1 AND NOT c.likely_bot AND c.clicked_at >= $2 AND c.clicked_at < $3
	), eb AS (
		SELECT ev.*, COALESCE((SELECT brand_id FROM sequences WHERE id = ev.sequence_id),
		                      (SELECT brand_id FROM mailboxes WHERE id = ev.mailbox_id)) AS brand_id
		FROM ev
	), f AS (
		SELECT * FROM eb
		WHERE $4::text <> '' AND ($5::uuid IS NULL OR sequence_id = $5) AND ($6::uuid IS NULL OR mailbox_id = $6) AND ($7 = '' OR domain = lower($7))
		  AND ($8 = '' OR ($8 = 'none' AND brand_id IS NULL) OR brand_id::text = $8)
		  AND ($9::uuid[] IS NULL OR brand_id IS NULL OR brand_id = ANY($9::uuid[]))
	)`

const metrics = `
	count(*) FILTER (WHERE kind = 'sent')::int,
	count(DISTINCT lead_id) FILTER (WHERE kind = 'sent')::int,
	count(*) FILTER (WHERE kind = 'sent' AND step = 0)::int,
	count(*) FILTER (WHERE kind = 'sent' AND step > 0)::int,
	count(*) FILTER (WHERE kind = 'failed')::int,
	count(*) FILTER (WHERE kind = 'blocked')::int,
	count(*) FILTER (WHERE kind = 'bounced')::int,
	count(*) FILTER (WHERE kind = 'reply')::int,
	count(DISTINCT lead_id) FILTER (WHERE kind = 'reply')::int,
	count(*) FILTER (WHERE kind = 'auto_reply')::int,
	count(*) FILTER (WHERE kind = 'opt_out')::int,
	count(*) FILTER (WHERE kind = 'unsubscribed')::int,
	count(*) FILTER (WHERE kind = 'warmup_inbox')::int,
	count(*) FILTER (WHERE kind = 'warmup_spam')::int,
	count(*) FILTER (WHERE kind = 'warmup_missing')::int,
	count(*) FILTER (WHERE kind = 'click')::int,
	count(DISTINCT lead_id) FILTER (WHERE kind = 'click')::int`

func (m *Metrics) fields() []any {
	return []any{&m.Sent, &m.Contacted, &m.NewLeads, &m.FollowUps, &m.Failed, &m.Blocked, &m.Bounced, &m.Replies, &m.RepliedLeads,
		&m.AutoReplies, &m.OptOuts, &m.Unsubscribes, &m.WarmupInbox, &m.WarmupSpam, &m.WarmupLost, &m.Clicks, &m.ClickedLeads}
}

func rate(a, b int) *float64 {
	if b == 0 {
		return nil
	}
	v := float64(int(float64(a)*1000/float64(b)+0.5)) / 10
	return &v
}

func (m *Metrics) derive() {
	m.ReplyRate = rate(m.RepliedLeads, m.Contacted)
	m.BounceRate = rate(m.Bounced, m.Sent)
	m.OptOutRate = rate(m.OptOuts+m.Unsubscribes, m.Contacted)
	m.PlacementRate = rate(m.WarmupInbox, m.WarmupInbox+m.WarmupSpam+m.WarmupLost)
	m.ClickRate = rate(m.ClickedLeads, m.Contacted)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Run builds a report.
func Run(ctx context.Context, q Querier, ws string, qr Query) (Report, error) {
	tz := qr.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return Report{}, fmt.Errorf("unknown time zone %q", tz)
	}
	args := []any{ws, qr.From, qr.To, tz, nullable(qr.SequenceID), nullable(qr.MailboxID), qr.Domain, qr.Brand, qr.BrandsIn}
	rep := Report{From: qr.From, To: qr.To, GroupBy: qr.GroupBy, Rows: []Row{}}

	rows, err := q.Query(ctx, events+` SELECT `+metrics+` FROM f`, args...)
	if err != nil {
		return rep, err
	}
	if rows.Next() {
		if err := rows.Scan(rep.Totals.fields()...); err != nil {
			rows.Close()
			return rep, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	rep.Totals.derive()
	if qr.GroupBy == ByNone {
		return rep, nil
	}

	var key, label, join, order string
	switch qr.GroupBy {
	case ByDay:
		key = `to_char(date_trunc('day', ts AT TIME ZONE $4), 'YYYY-MM-DD')`
		label, order = key, "1"
	case ByWeek:
		key = `to_char(date_trunc('week', ts AT TIME ZONE $4), 'YYYY-MM-DD')`
		label, order = key, "1"
	case BySequence:
		key = `COALESCE(f.sequence_id::text, '')`
		label = `COALESCE(max(s.name), CASE WHEN f.sequence_id IS NULL THEN 'Not in a sequence' ELSE 'Deleted sequence' END)`
		join, order = `LEFT JOIN sequences s ON s.id = f.sequence_id`, "3 DESC, 2"
	case ByStep:
		key = `COALESCE(step::text, '')`
		label = `CASE WHEN step IS NULL THEN 'Not linked to a step' ELSE 'Step ' || (step + 1) END`
		order = "step NULLS LAST"
	case ByMailbox:
		key = `COALESCE(f.mailbox_id::text, '')`
		label = `COALESCE(max(mb.email), 'Removed mailbox')`
		join, order = `LEFT JOIN mailboxes mb ON mb.id = f.mailbox_id`, "3 DESC, 2"
	case ByDomain:
		key = `COALESCE(domain, '')`
		label, order = key, "3 DESC, 2"
	case ByBrand:
		key = `COALESCE(f.brand_id::text, '')`
		label = `COALESCE(max(b.name), CASE WHEN f.brand_id IS NULL THEN 'Shared' ELSE 'Deleted brand' END)`
		join, order = `LEFT JOIN brands b ON b.id = f.brand_id`, "3 DESC, 2"
	default:
		return rep, fmt.Errorf("group_by must be one of day, week, sequence, step, mailbox, domain, brand")
	}
	group := key
	if qr.GroupBy == ByStep {
		group = "step"
	}
	if qr.GroupBy == BySequence {
		group = "f.sequence_id"
	}
	if qr.GroupBy == ByMailbox {
		group = "f.mailbox_id"
	}
	if qr.GroupBy == ByBrand {
		group = "f.brand_id"
	}
	sql := events + ` SELECT ` + key + `, ` + label + `, ` + metrics + ` FROM f ` + join + ` GROUP BY ` + group + ` ORDER BY ` + order + ` LIMIT 1000`
	rows, err = q.Query(ctx, sql, args...)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Row
		if err := rows.Scan(append([]any{&r.Key, &r.Name}, r.fields()...)...); err != nil {
			return rep, err
		}
		r.Label = r.Name
		r.derive()
		rep.Rows = append(rep.Rows, r)
	}
	return rep, rows.Err()
}
