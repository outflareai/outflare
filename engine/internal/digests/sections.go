package digests

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Metric is one number in a section, with the same number for the period
// before when it compares.
type Metric struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Value    float64  `json:"value"`
	Previous *float64 `json:"previous"`
}

// Item is one line worth reading: a fix to take, a connection to repair.
// Tab is the app page that has it; Tone is good, bad or neutral.
type Item struct {
	Text string `json:"text"`
	Tab  string `json:"tab"`
	Tone string `json:"tone"`
}

// Section is one part of the workspace in a digest.
type Section struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Metrics []Metric `json:"metrics"`
	Items   []Item   `json:"items"`
}

func (s Section) empty() bool {
	for _, m := range s.Metrics {
		if m.Value != 0 {
			return false
		}
	}
	return len(s.Items) == 0
}

// Period is what a digest covers, and the same length before it.
type Period struct {
	Start, End time.Time
}

func (p Period) before() Period { return Period{Start: p.Start.Add(-p.End.Sub(p.Start)), End: p.Start} }

// A Builder adds one section. Every module that has something to say adds
// one with Register, so a digest covers the whole workspace in one place.
type Builder struct {
	ID    string
	Title string
	Build func(ctx context.Context, q Querier, ws string, p Period) (Section, error)
}

var builders []Builder

// Register adds a section to every digest, after the ones registered
// before it.
func Register(b Builder) { builders = append(builders, b) }

func init() {
	Register(Builder{ID: "attention", Title: "Needs attention", Build: attention})
	Register(Builder{ID: "fixes", Title: "Fixes", Build: fixes})
	Register(Builder{ID: "deploys", Title: "Deploys", Build: deploys})
	Register(Builder{ID: "sites", Title: "Site health", Build: sites})
	Register(Builder{ID: "outreach", Title: "Outreach", Build: outreach})
	Register(Builder{ID: "lifecycle", Title: "Lifecycle email", Build: lifecycle})
	Register(Builder{ID: "missions", Title: "Missions", Build: missions})
}

// Build runs every section for a workspace and keeps the ones with
// something in them.
func Build(ctx context.Context, q Querier, ws string, p Period) ([]Section, error) {
	out := []Section{}
	for _, b := range builders {
		s, err := b.Build(ctx, q, ws, p)
		if err != nil {
			return nil, fmt.Errorf("digest section %s: %w", b.ID, err)
		}
		s.ID, s.Title = b.ID, b.Title
		if s.Metrics == nil {
			s.Metrics = []Metric{}
		}
		if s.Items == nil {
			s.Items = []Item{}
		}
		if !s.empty() {
			out = append(out, s)
		}
	}
	return out, nil
}

// counts runs one query of count columns over a period and the one before,
// and returns metrics with previous values. sql takes $1 workspace, $2
// start, $3 end.
func counts(ctx context.Context, q Querier, ws string, p Period, sql string, labels [][2]string) ([]Metric, error) {
	read := func(p Period) ([]float64, error) {
		vals := make([]float64, len(labels))
		ptrs := make([]any, len(labels))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		return vals, q.QueryRow(ctx, sql, ws, p.Start, p.End).Scan(ptrs...)
	}
	now, err := read(p)
	if err != nil {
		return nil, err
	}
	prev, err := read(p.before())
	if err != nil {
		return nil, err
	}
	out := make([]Metric, len(labels))
	for i, l := range labels {
		v := prev[i]
		out[i] = Metric{Key: l[0], Label: l[1], Value: now[i], Previous: &v}
	}
	return out, nil
}

func attention(ctx context.Context, q Querier, ws string, p Period) (Section, error) {
	var s Section
	var errorsOpen, held float64
	if err := q.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM fixes WHERE workspace_id = $1 AND status IN ('open', 'in_progress') AND severity = 'error')::float8,
		(SELECT count(*) FROM mission_actions WHERE workspace_id = $1 AND status = 'pending')::float8`, ws).Scan(&errorsOpen, &held); err != nil {
		return s, err
	}
	s.Metrics = []Metric{
		{Key: "fixes_errors_open", Label: "Open errors to fix", Value: errorsOpen},
		{Key: "mission_actions_waiting", Label: "Mission actions waiting for you", Value: held},
	}
	rows, err := q.Query(ctx, `SELECT name FROM integrations WHERE workspace_id = $1 AND status = 'error' ORDER BY name LIMIT 5`, ws)
	if err != nil {
		return s, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return s, err
	}
	for _, n := range names {
		s.Items = append(s.Items, Item{Text: n + " needs to be connected again.", Tab: "connectors", Tone: "bad"})
	}
	rows, err = q.Query(ctx, `SELECT name FROM deploy_targets WHERE workspace_id = $1 AND status = 'active' AND last_error IS NOT NULL ORDER BY name LIMIT 3`, ws)
	if err != nil {
		return s, err
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return s, err
	}
	for _, n := range targets {
		s.Items = append(s.Items, Item{Text: "Deploys from " + n + " can't be read.", Tab: "connectors", Tone: "bad"})
	}
	if held > 0 {
		s.Items = append(s.Items, Item{Text: plural(int(held), "mission action is", "mission actions are") + " waiting for your approval.", Tab: "missions", Tone: "neutral"})
	}
	return s, nil
}

func fixes(ctx context.Context, q Querier, ws string, p Period) (Section, error) {
	var s Section
	var err error
	s.Metrics, err = counts(ctx, q, ws, p, `SELECT
		(SELECT count(*) FROM fixes WHERE workspace_id = $1 AND created_at >= $2 AND created_at < $3)::float8,
		(SELECT count(*) FROM fixes WHERE workspace_id = $1 AND status = 'done' AND fixed_at >= $2 AND fixed_at < $3)::float8,
		(SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'fix.reopened' AND created_at >= $2 AND created_at < $3)::float8`,
		[][2]string{{"fixes_found", "New fixes found"}, {"fixes_done", "Fixes confirmed done"}, {"fixes_reopened", "Fixes that came back"}})
	if err != nil {
		return s, err
	}
	var open float64
	if err := q.QueryRow(ctx, `SELECT count(*)::float8 FROM fixes WHERE workspace_id = $1 AND status IN ('open', 'in_progress')`, ws).Scan(&open); err != nil {
		return s, err
	}
	s.Metrics = append(s.Metrics, Metric{Key: "fixes_open", Label: "Open now", Value: open})
	if empty := (Section{Metrics: s.Metrics}).empty(); empty {
		return s, nil
	}
	// The biggest open errors, worth taking first.
	rows, err := q.Query(ctx, `SELECT f.rule, f.group_key, f.count, f.unit, COALESCE(si.host, '')
		FROM fixes f LEFT JOIN seo_sites si ON f.subject_type = 'seo_site' AND si.id = f.subject_id
		WHERE f.workspace_id = $1 AND f.status = 'open' AND f.severity = 'error'
		ORDER BY f.count DESC, f.rule LIMIT 3`, ws)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var rule, group, unit, host string
		var n int
		if err := rows.Scan(&rule, &group, &n, &unit, &host); err != nil {
			return s, err
		}
		title := rule
		if r, ok := crawl.RuleFor(rule); ok {
			title = r.Title
		}
		where := host + group
		if where != "" {
			where = " on " + where
		}
		left := ""
		if n > 0 && unit != "" {
			left = fmt.Sprintf(" (%d %s)", n, unit)
		}
		s.Items = append(s.Items, Item{Text: title + where + left, Tab: "fixes", Tone: "bad"})
	}
	return s, rows.Err()
}

func deploys(ctx context.Context, q Querier, ws string, p Period) (Section, error) {
	var s Section
	var err error
	s.Metrics, err = counts(ctx, q, ws, p, `SELECT
		(SELECT count(*) FROM deploys WHERE workspace_id = $1 AND state = 'ready' AND deployed_at >= $2 AND deployed_at < $3)::float8,
		(SELECT count(*) FROM deploys WHERE workspace_id = $1 AND state = 'failed' AND deployed_at >= $2 AND deployed_at < $3)::float8`,
		[][2]string{{"deploys_live", "Went live"}, {"deploys_failed", "Failed"}})
	if err != nil {
		return s, err
	}
	rows, err := q.Query(ctx, `SELECT COALESCE(si.host, t.name), count(*)
		FROM deploys d JOIN deploy_targets t ON t.id = d.target_id LEFT JOIN seo_sites si ON si.id = d.site_id
		WHERE d.workspace_id = $1 AND d.state = 'ready' AND d.deployed_at >= $2 AND d.deployed_at < $3
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 5`, ws, p.Start, p.End)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return s, err
		}
		s.Items = append(s.Items, Item{Text: fmt.Sprintf("%s went live %s.", name, times(n)), Tab: "connectors", Tone: "neutral"})
	}
	return s, rows.Err()
}

func sites(ctx context.Context, q Querier, ws string, p Period) (Section, error) {
	var s Section
	// Each site's latest audited crawl by the end, against its latest one
	// before the period.
	rows, err := q.Query(ctx, `
		SELECT si.host,
		  (SELECT r.health_score FROM crawl_runs r WHERE r.site_id = si.id AND r.health_score IS NOT NULL AND r.finished_at < $3 ORDER BY r.finished_at DESC LIMIT 1),
		  (SELECT r.health_score FROM crawl_runs r WHERE r.site_id = si.id AND r.health_score IS NOT NULL AND r.finished_at < $2 ORDER BY r.finished_at DESC LIMIT 1),
		  (SELECT count(*) FROM crawl_runs r WHERE r.site_id = si.id AND r.finished_at >= $2 AND r.finished_at < $3)
		FROM seo_sites si WHERE si.workspace_id = $1 ORDER BY si.host LIMIT 20`, ws, p.Start, p.End)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	var crawled float64
	for rows.Next() {
		var host string
		var now, before *int
		var n int
		if err := rows.Scan(&host, &now, &before, &n); err != nil {
			return s, err
		}
		crawled += float64(n)
		if now == nil || n == 0 {
			continue
		}
		it := Item{Text: fmt.Sprintf("%s health %d", host, *now), Tab: "audits", Tone: "neutral"}
		if before != nil && *before != *now {
			it.Text += fmt.Sprintf(", %+d since the last digest", *now-*before)
			if *now > *before {
				it.Tone = "good"
			} else {
				it.Tone = "bad"
			}
		}
		s.Items = append(s.Items, Item{Text: it.Text + ".", Tab: it.Tab, Tone: it.Tone})
	}
	s.Metrics = []Metric{{Key: "crawls", Label: "Crawls finished", Value: crawled}}
	return s, rows.Err()
}

func outreach(ctx context.Context, q Querier, ws string, p Period) (Section, error) {
	var s Section
	var err error
	s.Metrics, err = counts(ctx, q, ws, p, `SELECT
		(SELECT count(*) FROM messages WHERE workspace_id = $1 AND status = 'sent' AND created_at >= $2 AND created_at < $3)::float8,
		(SELECT count(*) FROM inbound_messages WHERE workspace_id = $1 AND kind = 'reply' AND received_at >= $2 AND received_at < $3)::float8,
		(SELECT count(*) FROM inbound_messages WHERE workspace_id = $1 AND kind = 'reply' AND category IN ('interested', 'meeting_booked') AND received_at >= $2 AND received_at < $3)::float8,
		(SELECT count(*) FROM inbound_messages WHERE workspace_id = $1 AND kind = 'bounce' AND received_at >= $2 AND received_at < $3)::float8,
		(SELECT count(*) FROM meetings WHERE workspace_id = $1 AND booked_at >= $2 AND booked_at < $3)::float8`,
		[][2]string{{"emails_sent", "Emails sent"}, {"replies", "Replies"}, {"replies_interested", "Interested replies"}, {"bounces", "Bounces"}, {"meetings_booked", "Meetings booked"}})
	if err != nil {
		return s, err
	}
	var unread int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM inbound_messages WHERE workspace_id = $1 AND kind = 'reply' AND NOT read AND NOT done`, ws).Scan(&unread); err != nil {
		return s, err
	}
	if unread > 0 {
		s.Items = append(s.Items, Item{Text: plural(unread, "reply is", "replies are") + " waiting in the inbox.", Tab: "inbox", Tone: "neutral"})
	}
	return s, nil
}

func lifecycle(ctx context.Context, q Querier, ws string, p Period) (Section, error) {
	var s Section
	var err error
	s.Metrics, err = counts(ctx, q, ws, p, `SELECT
		(SELECT count(*) FROM emails WHERE workspace_id = $1 AND NOT test AND sent_at >= $2 AND sent_at < $3)::float8,
		(SELECT count(*) FROM emails WHERE workspace_id = $1 AND NOT test AND opened_at >= $2 AND opened_at < $3)::float8,
		(SELECT count(*) FROM emails WHERE workspace_id = $1 AND NOT test AND status IN ('bounced', 'complained') AND sent_at >= $2 AND sent_at < $3)::float8`,
		[][2]string{{"lifecycle_sent", "Sent"}, {"lifecycle_opened", "Opened"}, {"lifecycle_bounced", "Bounced or marked spam"}})
	return s, err
}

func missions(ctx context.Context, q Querier, ws string, p Period) (Section, error) {
	var s Section
	var err error
	s.Metrics, err = counts(ctx, q, ws, p, `SELECT
		(SELECT count(*) FROM mission_actions WHERE workspace_id = $1 AND status = 'executed' AND created_at >= $2 AND created_at < $3)::float8,
		(SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'mission.completed' AND created_at >= $2 AND created_at < $3)::float8`,
		[][2]string{{"mission_actions_run", "Agent actions run"}, {"missions_completed", "Missions completed"}})
	return s, err
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}
