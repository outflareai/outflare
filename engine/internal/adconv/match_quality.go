package adconv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/conversions"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Match quality: once a day, for each pixel a brand's enabled conversion
// rules send to on a platform that reports it (Meta's event match quality),
// a job reads what the platform says about how well the conversions match
// its accounts: a score out of 10 per event, the share of events that
// carried each identifier, and the problems it found. One row per event and
// day is kept (ad_match_quality), so the send log shows the score, how it
// moved and what to fix. An event the rules send whose score is low, or
// fell, raises the ads.match_quality_low alert, at most once a week.

const (
	// JobMatchQuality reads one pixel's match quality.
	JobMatchQuality = "ads.match_quality_read"
	// AlertMatchQualityLow: the platform matches too few of an event's
	// conversions to its accounts.
	AlertMatchQualityLow = "ads.match_quality_low"

	// MatchQualityLow is the score under which an event is low: under it,
	// the platform is matching too few conversions to learn from them.
	MatchQualityLow = 6.0
	// MatchQualityDrop is how far a score falls below the average of the
	// seven days before for it to count as fallen.
	MatchQualityDrop = 1.0
	// matchQualityKeep is how many days of rows are kept.
	matchQualityKeep = 400
)

func init() {
	alerts.Register(alerts.Kind{ID: AlertMatchQualityLow, Title: "Ad platform matching fewer conversions",
		Description: "An ad platform's match quality score for an event your conversion rules send is under 6 out of 10, or fell by a point or more against the week before, so it ties fewer conversions to its ads. At most once a week per event. It goes to the owners and admins.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describeMatchQualityLow})
}

type matchQualityPayload struct {
	Account string `json:"ad_account_id"`
	Target  string `json:"target_id"`
}

// MatchQualityStatus says how a score reads: "low" under MatchQualityLow,
// "fell" when it is MatchQualityDrop or more under the average of the week
// before, "ok" otherwise, and "none" without a score.
func MatchQualityStatus(score, before *float64) string {
	switch {
	case score == nil:
		return "none"
	case *score < MatchQualityLow:
		return "low"
	case before != nil && *before-*score >= MatchQualityDrop:
		return "fell"
	}
	return "ok"
}

// EnqueueMatchQuality asks for a read of one pixel; key makes the request
// idempotent (the day).
func EnqueueMatchQuality(ctx context.Context, q jobs.Querier, ws, accountID, targetID, key string) (int64, error) {
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobMatchQuality, WorkspaceID: &ws, Payload: matchQualityPayload{Account: accountID, Target: targetID},
		IdempotencyKey: "match_quality:" + accountID + ":" + targetID + ":" + key, MaxAttempts: 3})
}

// matchQualityConnectors are the connectors whose platform reports match
// quality.
func (s *Sender) matchQualityConnectors() []string {
	out := []string{}
	if s.Store == nil || s.Store.Registry == nil {
		return out
	}
	for _, d := range s.Store.Registry.All() {
		if c, ok := s.Store.Registry.Get(d.ID); ok {
			if _, ok := c.(connectors.MatchQualityReader); ok {
				out = append(out, d.ID)
			}
		}
	}
	return out
}

// queueMatchQuality asks for today's read of every pixel an enabled rule
// sends to, on the platforms that report match quality.
func (s *Sender) queueMatchQuality(ctx context.Context) error {
	ids := s.matchQualityConnectors()
	if len(ids) == 0 {
		return nil
	}
	day := s.now().UTC().Format(time.DateOnly)
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT r.workspace_id::text, r.ad_account_id::text, r.target_id FROM ad_conversion_rules r
		JOIN ad_accounts c ON c.id = r.ad_account_id AND c.workspace_id = r.workspace_id
		WHERE r.enabled AND c.status = 'active' AND c.connector = ANY($1)
		  AND NOT EXISTS (SELECT 1 FROM ad_match_quality m WHERE m.ad_account_id = r.ad_account_id AND m.target_id = r.target_id AND m.day = $2::date)`, ids, day)
	if err != nil {
		return err
	}
	type due struct{ ws, account, target string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.ws, &d.account, &d.target); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		if _, err := EnqueueMatchQuality(ctx, s.Pool, d.ws, d.account, d.target, day); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sender) matchQualityJob(ctx context.Context, job jobs.Job) error {
	var p matchQualityPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	ws := *job.WorkspaceID
	res, err := s.ReadMatchQuality(ctx, ws, p.Account, p.Target)
	ev := track.Event{Name: "ad_match_quality.read", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"ad_account_id": p.Account, "connector": res.Connector, "events": res.Events, "low": res.Low, "alerts": res.Alerts}}
	var db dbError
	switch {
	case err == nil:
		metrics.AdsFeedBatch("match_quality", "sent")
		track.Emit(ctx, ev)
		return nil
	case errors.Is(err, connectors.ErrNotFound):
		return nil
	case errors.As(err, &db):
		track.Fail(ctx, s.Log, ev, "internal", err)
		return err
	}
	metrics.AdsFeedBatch("match_quality", "failed")
	kind := connectors.KindOf(err)
	ev.Outcome = track.Failure
	if kind == connectors.ErrUnavailable {
		ev.Outcome = track.Error
	}
	track.Fail(ctx, s.Log, ev, "connector_"+kind, err)
	if kind == connectors.ErrUnavailable || kind == connectors.ErrQuota {
		return err
	}
	return nil
}

// MatchQualityResult counts what one read did.
type MatchQualityResult struct {
	Connector string `json:"connector"`
	// Events is how many events the platform scored; Low how many of the
	// events the rules send read low or fell; Alerts how many alerts that
	// raised.
	Events int `json:"events"`
	Low    int `json:"low"`
	Alerts int `json:"alerts"`
}

// ReadMatchQuality reads one pixel's match quality from the platform and
// keeps today's row per event. connectors.ErrNotFound means there is
// nothing to read: no enabled rule sends to the pixel, the account is gone,
// or its platform doesn't report match quality.
func (s *Sender) ReadMatchQuality(ctx context.Context, ws, accountID, targetID string) (MatchQualityResult, error) {
	var res MatchQualityResult
	var integrationID, brand string
	var acct connectors.AdAccount
	var meta []byte
	var sends []string
	err := s.Pool.QueryRow(ctx, `SELECT c.integration_id::text, c.connector, c.external_id, c.name, c.currency, c.time_zone, c.meta,
			min(COALESCE(r.brand_id, c.brand_id)::text), array_agg(DISTINCT r.event_type)
		FROM ad_conversion_rules r JOIN ad_accounts c ON c.id = r.ad_account_id AND c.workspace_id = r.workspace_id AND c.status = 'active'
		WHERE r.workspace_id = $1 AND r.ad_account_id = $2 AND r.target_id = $3 AND r.enabled
		GROUP BY c.id`, ws, accountID, targetID).Scan(&integrationID, &res.Connector, &acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &meta, &brand, &sends)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, connectors.ErrNotFound
	}
	if err != nil {
		return res, dbError{err}
	}
	_ = json.Unmarshal(meta, &acct.Meta)
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return res, err
	}
	reader, ok := o.Connector.(connectors.MatchQualityReader)
	if !ok {
		return res, connectors.ErrNotFound
	}
	list, err := reader.MatchQuality(ctx, o.Client, o.Creds, acct, targetID)
	if err != nil {
		_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
		return res, err
	}
	// The events the rules send: a rule without an event type sends the
	// platform's default, which the reader can't name, so every scored
	// event counts for it.
	sent := map[string]bool{}
	every := false
	for _, e := range sends {
		if e == "" {
			every = true
		}
		sent[e] = true
	}
	now := s.now().UTC()
	day := now.Format(time.DateOnly)
	year, week := now.ISOWeek()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return res, dbError{err}
	}
	defer tx.Rollback(ctx)
	for _, m := range list {
		if len(m.Event) > 200 {
			continue
		}
		var score *float64
		if m.Score != nil && !math.IsNaN(*m.Score) && *m.Score >= 0 && *m.Score <= 10 {
			v := math.Round(*m.Score*100) / 100
			score = &v
		}
		keys, issues := m.Keys, m.Issues
		if keys == nil {
			keys = []connectors.MatchKey{}
		}
		if issues == nil {
			issues = []connectors.MatchIssue{}
		}
		for i := range issues {
			issues[i].Name, issues[i].Description, issues[i].Solution = trim(issues[i].Name, 300), trim(issues[i].Description, 1000), trim(issues[i].Solution, 1000)
		}
		k, _ := json.Marshal(keys)
		is, _ := json.Marshal(issues)
		if _, err := tx.Exec(ctx, `INSERT INTO ad_match_quality (workspace_id, brand_id, ad_account_id, target_id, event, day, score, keys, issues, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6::date, $7, $8, $9, $10)
			ON CONFLICT (ad_account_id, target_id, event, day) DO UPDATE SET score = EXCLUDED.score, keys = EXCLUDED.keys, issues = EXCLUDED.issues, fetched_at = EXCLUDED.fetched_at`,
			ws, brand, accountID, targetID, m.Event, day, score, k, is, now); err != nil {
			return res, dbError{err}
		}
		res.Events++
		if !every && !sent[m.Event] {
			continue
		}
		var before *float64
		if err := tx.QueryRow(ctx, `SELECT avg(score)::float8 FROM ad_match_quality
			WHERE ad_account_id = $1 AND target_id = $2 AND event = $3 AND day BETWEEN $4::date - 7 AND $4::date - 1`, accountID, targetID, m.Event, day).Scan(&before); err != nil {
			return res, dbError{err}
		}
		status := MatchQualityStatus(score, before)
		if status != "low" && status != "fell" {
			continue
		}
		res.Low++
		data := map[string]any{"connector": res.Connector, "event": m.Event, "status": status, "score": *score}
		if before != nil {
			data["score_before"] = math.Round(*before*100) / 100
		}
		id, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: &brand, Kind: AlertMatchQualityLow,
			DedupeKey:  fmt.Sprintf("%s:%s:%s:%d-%02d", accountID, targetID, m.Event, year, week),
			TargetType: "ad_account", TargetID: accountID, Data: data})
		if err != nil {
			return res, dbError{err}
		}
		if id != "" {
			res.Alerts++
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ad_match_quality WHERE ad_account_id = $1 AND target_id = $2 AND day < $3::date - $4::int`, accountID, targetID, day, matchQualityKeep); err != nil {
		return res, dbError{err}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, dbError{err}
	}
	return res, nil
}

func describeMatchQualityLow(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var name string
	if err := q.QueryRow(ctx, `SELECT name FROM ad_accounts WHERE id = $1 AND workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&name); err != nil {
		return alerts.View{}, err
	}
	if name == "" {
		name = "An ad account"
	}
	event, _ := a.Data["event"].(string)
	score, _ := a.Data["score"].(float64)
	text := fmt.Sprintf("The platform scores %s at %.1f out of 10 for matching, under 6, so it ties fewer conversions to its ads. The send log lists what to fix.", event, score)
	if a.Data["status"] == "fell" {
		before, _ := a.Data["score_before"].(float64)
		text = fmt.Sprintf("The platform's match score for %s fell from %.1f to %.1f out of 10, so it ties fewer conversions to its ads. The send log lists what to fix.", event, before, score)
	}
	return alerts.View{Title: name + ": match quality is low", Text: text, Path: "ads/sends"}, nil
}

// MatchQualityRow is one event's latest match quality on one pixel.
type MatchQualityRow struct {
	AdAccountID string `json:"ad_account_id"`
	AccountName string `json:"account_name"`
	Connector   string `json:"connector"`
	TargetID    string `json:"target_id"`
	TargetName  string `json:"target_name"`
	Event       string `json:"event"`
	// Sent says whether one of the brand's enabled rules sends this event.
	Sent  bool     `json:"sent"`
	Day   string   `json:"day"`
	Score *float64 `json:"score"`
	// ScoreBefore is the average over the seven days before Day.
	ScoreBefore *float64 `json:"score_before"`
	// Status is ok, low, fell or none (MatchQualityStatus).
	Status    string                  `json:"status"`
	Keys      []connectors.MatchKey   `json:"keys"`
	Issues    []connectors.MatchIssue `json:"issues"`
	FetchedAt time.Time               `json:"fetched_at"`
	// History is the score per day, oldest first, over the last 30 days.
	History []MatchQualityDay `json:"history"`
}

// MatchQualityDay is one day's score.
type MatchQualityDay struct {
	Day   string   `json:"day"`
	Score *float64 `json:"score"`
}

// LoadMatchQuality lists a brand's latest match quality per pixel and
// event, the events its rules send first, then lowest score first.
func LoadMatchQuality(ctx context.Context, q conversions.Querier, ws, brand string) ([]MatchQualityRow, error) {
	rows, err := q.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (m.ad_account_id, m.target_id, m.event) m.* FROM ad_match_quality m
			WHERE m.workspace_id = $1 AND m.brand_id = $2 AND m.day >= current_date - 30
			ORDER BY m.ad_account_id, m.target_id, m.event, m.day DESC)
		SELECT l.ad_account_id::text, a.name, a.connector, l.target_id,
			COALESCE((SELECT max(r.target_name) FROM ad_conversion_rules r WHERE r.ad_account_id = l.ad_account_id AND r.target_id = l.target_id), ''),
			l.event,
			EXISTS (SELECT 1 FROM ad_conversion_rules r WHERE r.ad_account_id = l.ad_account_id AND r.target_id = l.target_id AND r.enabled AND r.event_type IN (l.event, '')),
			to_char(l.day, 'YYYY-MM-DD'), l.score::float8,
			(SELECT avg(p.score)::float8 FROM ad_match_quality p WHERE p.ad_account_id = l.ad_account_id AND p.target_id = l.target_id AND p.event = l.event AND p.day BETWEEN l.day - 7 AND l.day - 1),
			l.keys, l.issues, l.fetched_at,
			(SELECT COALESCE(jsonb_agg(jsonb_build_object('day', to_char(h.day, 'YYYY-MM-DD'), 'score', h.score) ORDER BY h.day), '[]')
				FROM ad_match_quality h WHERE h.ad_account_id = l.ad_account_id AND h.target_id = l.target_id AND h.event = l.event AND h.day >= current_date - 30)
		FROM latest l JOIN ad_accounts a ON a.id = l.ad_account_id AND a.workspace_id = l.workspace_id
		ORDER BY 7 DESC, l.score NULLS LAST, a.connector, lower(a.name), l.event`, ws, brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MatchQualityRow{}
	for rows.Next() {
		var r MatchQualityRow
		var keys, issues, history []byte
		if err := rows.Scan(&r.AdAccountID, &r.AccountName, &r.Connector, &r.TargetID, &r.TargetName, &r.Event, &r.Sent, &r.Day, &r.Score, &r.ScoreBefore,
			&keys, &issues, &r.FetchedAt, &history); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(keys, &r.Keys)
		_ = json.Unmarshal(issues, &r.Issues)
		_ = json.Unmarshal(history, &r.History)
		if r.Keys == nil {
			r.Keys = []connectors.MatchKey{}
		}
		if r.Issues == nil {
			r.Issues = []connectors.MatchIssue{}
		}
		if r.History == nil {
			r.History = []MatchQualityDay{}
		}
		if r.ScoreBefore != nil {
			v := math.Round(*r.ScoreBefore*100) / 100
			r.ScoreBefore = &v
		}
		r.Status = MatchQualityStatus(r.Score, r.ScoreBefore)
		out = append(out, r)
	}
	return out, rows.Err()
}
